package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Opt-in only: expose an ephemeral health/echo service, never the control plane.
// This smoke test cannot establish an uptime guarantee or ISP-wide availability.
func TestLiveTunnelHealthAndWebSocket(t *testing.T) {
	bin := os.Getenv("DP_TEST_CLOUDFLARED_LIVE_BIN")
	if bin == "" {
		t.Skip("set DP_TEST_CLOUDFLARED_LIVE_BIN for public health/echo smoke test")
	}
	var echoRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, "deploypilot-tunnel-probe")
			return
		}
		if r.URL.Path != "/echo" {
			http.NotFound(w, r)
			return
		}
		echoRequests.Add(1)
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			kind, p, err := c.ReadMessage()
			if err != nil {
				return
			}
			if c.WriteMessage(kind, p) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	_, portText, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Second)
	defer cancel()
	start := time.Now()
	tun, err := Start(ctx, bin, port)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	t.Logf("first edge connection ready after %s", time.Since(start).Round(time.Millisecond))
	client := &http.Client{Timeout: 5 * time.Second}
	publicDeadline := time.Now().Add(30 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, tun.URL+"/health", nil)
		resp, requestErr := client.Do(req)
		ok := false
		if requestErr == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			ok = resp.StatusCode == 200 && string(body) == "deploypilot-tunnel-probe"
		}
		if ok {
			break
		}
		if time.Now().After(publicDeadline) {
			t.Fatal("public HTTP health did not become reachable within 30s of edge readiness")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Logf("public HTTP reached after %s", time.Since(start).Round(time.Millisecond))
	traceCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		ConnectDone:          func(_, _ string, err error) { t.Logf("WebSocket TCP connect: %v", err) },
		TLSHandshakeDone:     func(_ tls.ConnectionState, err error) { t.Logf("WebSocket TLS handshake: %v", err) },
		GotFirstResponseByte: func() { t.Log("WebSocket received HTTP response") },
	})
	c, _, err := websocket.DefaultDialer.DialContext(traceCtx, "wss"+strings.TrimPrefix(tun.URL, "https")+"/echo", nil)
	if err != nil {
		t.Fatalf("public WebSocket handshake: %v; requests reaching local echo: %d", err, echoRequests.Load())
	}
	defer c.Close()
	for i := 0; i < 7; i++ {
		payload := fmt.Sprintf("probe-%d", i)
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, got, err := c.ReadMessage()
		if err != nil || string(got) != payload {
			t.Fatalf("echo %d failed: %v", i, err)
		}
		if i < 6 {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Second):
			}
		}
	}
	t.Log("public WebSocket: 7 round trips over at least 30s passed")
}
