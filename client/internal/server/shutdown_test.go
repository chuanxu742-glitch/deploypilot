package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Shutdown must close long-lived WebSocket handlers (agent/terminal) instead
// of leaving them running after Ctrl+C.
func TestShutdownClosesWebSockets(t *testing.T) {
	srv, cookie := newLLMServer(t)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}

	hdr := http.Header{}
	hdr.Add("Cookie", cookie.Name+"="+cookie.Value)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("shutdown took too long: %s", time.Since(start))
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected websocket to be closed after shutdown")
	} else if strings.Contains(err.Error(), "timeout") {
		t.Fatalf("websocket still open after shutdown: %v", err)
	}
	_ = httptest.NewRecorder
}
