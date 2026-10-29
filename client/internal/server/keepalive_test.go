package server

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Idle WebSockets must receive pings so the Cloudflare tunnel keeps them open.
func TestWebSocketKeepAlivePings(t *testing.T) {
	old := pingInterval
	pingInterval = 50 * time.Millisecond
	t.Cleanup(func() { pingInterval = old })

	srv, cookie := newLLMServer(t)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.httpSrv.Close() })

	hdr := http.Header{}
	hdr.Add("Cookie", cookie.Name+"="+cookie.Value)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var pings atomic.Int32
	conn.SetPingHandler(func(string) error {
		pings.Add(1)
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, _, _ = conn.ReadMessage() // control frames are handled while reading

	if n := pings.Load(); n < 2 {
		t.Fatalf("expected at least 2 pings on an idle socket, got %d", n)
	}
}
