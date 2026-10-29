package server

import (
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketClosesWithoutPong(t *testing.T) {
	old := pingInterval
	pingInterval = 50 * time.Millisecond
	t.Cleanup(func() { pingInterval = old })
	srv, cookie := newLLMServer(t)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.httpSrv.Close() })
	hdr := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, _ = conn.ReadMessage()                           // initial snapshot
	conn.SetPingHandler(func(string) error { return nil }) // a peer that no longer answers
	_ = conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	_, _, err = conn.ReadMessage()
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("server never closed the unresponsive peer")
	}
	if err == nil {
		t.Fatal("expected connection close")
	}
}

func TestWebSocketPongsKeepConnectionAlive(t *testing.T) {
	old := pingInterval
	pingInterval = 40 * time.Millisecond
	t.Cleanup(func() { pingInterval = old })
	srv, cookie := newLLMServer(t)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.httpSrv.Close() })
	hdr := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, _ = conn.ReadMessage() // initial snapshot
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, _, err = conn.ReadMessage() // default ping handler sends pongs
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("responsive peer closed early: %v", err)
	}
}

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
	_, _, _ = conn.ReadMessage() // initial snapshot

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
