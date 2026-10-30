package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/gorilla/websocket"
)

func TestReconnectWaitsForOldCleanup(t *testing.T) {
	srv, cookie := newLLMServer(t)
	srv.llm.SetAPIKey("test")
	e1 := &heldExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	e2 := &heldExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	defer close(e2.release)
	released := false
	defer func() {
		if !released {
			close(e1.release)
		}
	}()
	var calls atomic.Int32
	srv.newExecutor = func() agent.Executor {
		if calls.Add(1) == 1 {
			return e1
		}
		return e2
	}
	c1 := agentSocket(t, srv, cookie)
	c1.WriteJSON(agentClientMsg{Type: "start", Task: "first"})
	waitAgentState(t, c1, "running")
	select {
	case <-e1.started:
	case <-time.After(time.Second):
		t.Fatal("first not started")
	}
	c1.WriteJSON(agentClientMsg{Type: "stop"})
	waitAgentState(t, c1, "stopping")
	c1.Close()
	select {
	case <-e1.cancelled:
	case <-time.After(time.Second):
		t.Fatal("first not cancelled")
	}
	c2 := agentSocket(t, srv, cookie)
	c2.WriteJSON(agentClientMsg{Type: "start", Task: "must wait"})
	event := readAgentMessage(t, c2)
	if event["type"] != "error" {
		t.Fatalf("new task accepted before old cleanup: %v", event)
	}

	if calls.Load() != 1 {
		t.Fatal("overlapping executor created")
	}
	close(e1.release)
	released = true
	waitAgentState(t, c2, "idle")
	c2.WriteJSON(agentClientMsg{Type: "start", Task: "after cleanup"})
	waitAgentState(t, c2, "running")
	select {
	case <-e2.started:
	case <-time.After(time.Second):
		t.Fatal("replacement not started")
	}
	c2.WriteJSON(agentClientMsg{Type: "stop"})
	waitAgentState(t, c2, "stopping")
	c2.Close()
	<-e2.cancelled
}

func shortSessionServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	m, err := auth.NewManager(auth.Options{IdleTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Assets: testAssets(), Auth: m, LLM: llm.New(llm.Config{APIKey: "test"})}), &http.Cookie{Name: auth.CookieName, Value: sid}
}

func TestIdleSessionClosesSocket(t *testing.T) {
	srv, cookie := shortSessionServer(t)
	c := agentSocket(t, srv, cookie)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = c.ReadMessage() // initial snapshot
	_, _, err := c.ReadMessage()
	if err == nil {
		t.Fatal("idle socket stayed open")
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		t.Fatal("socket was not closed at session expiry")
	}
}

func TestRevokedSessionClosesSocket(t *testing.T) {
	srv, cookie := newLLMServer(t)
	c := agentSocket(t, srv, cookie)
	srv.auth.Revoke()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = c.ReadMessage() // initial snapshot
	_, _, err := c.ReadMessage()
	if err == nil {
		t.Fatal("revoked socket stayed open")
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		t.Fatal("socket not closed after revocation")
	}
}

func TestSocketActivityRenewsSession(t *testing.T) {
	srv, cookie := shortSessionServer(t)
	c := agentSocket(t, srv, cookie)
	for i := 0; i < 6; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := c.WriteJSON(agentClientMsg{Type: "stop"}); err != nil {
			t.Fatal(err)
		}
		waitAgentState(t, c, "idle")
	}
	if !srv.auth.Validate(cookie.Value) {
		t.Fatal("real socket activity did not renew session")
	}
}

func TestCookieLifetimeDelegatesToServer(t *testing.T) {
	srv, m := newAuthServer(t)
	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	auth.SetCookie(rec, sid, m.IdleTimeout())
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != 0 || !cookies[0].Expires.IsZero() {
		t.Fatalf("cookie expires before active server session: %+v", cookies)
	}
	req := httptest.NewRequest("GET", "/api/session", nil)
	req.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("session rejected: %d", response.Code)
	}
}

func TestAutomaticPongsDoNotRenewSession(t *testing.T) {
	old := pingInterval
	pingInterval = 20 * time.Millisecond
	t.Cleanup(func() { pingInterval = old })
	srv, cookie := shortSessionServer(t)
	c := agentSocket(t, srv, cookie)
	pings := 0
	c.SetPingHandler(func(data string) error {
		pings++
		return c.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = c.ReadMessage() // initial snapshot
	_, _, err := c.ReadMessage()
	if err == nil {
		t.Fatal("idle connection remained open")
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		t.Fatal("automatic pong extended authentication")
	}
	if pings < 2 {
		t.Fatalf("expiry test did not exercise heartbeats: %d", pings)
	}
}

func TestLongSilentTaskHoldsSessionAndRevocationCancelsIt(t *testing.T) {
	srv, cookie := shortSessionServer(t)
	e := &heldExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	defer close(e.release)
	srv.newExecutor = func() agent.Executor { return e }
	c := agentSocket(t, srv, cookie)
	c.WriteJSON(agentClientMsg{Type: "start", Task: "wait until session expires"})
	waitAgentState(t, c, "running")
	select {
	case <-e.started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	// The task emits no progress while running, but its session remains valid
	// beyond the configured 300ms idle timeout.
	time.Sleep(650 * time.Millisecond)
	if !srv.auth.IsValid(cookie.Value) {
		t.Fatal("silent task lost its held session")
	}
	select {
	case <-e.cancelled:
		t.Fatal("idle timeout cancelled a held task")
	default:
	}
	// Explicit revocation remains authoritative while the hold is active.
	srv.auth.Revoke()
	select {
	case <-e.cancelled:
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel active task")
	}
}
