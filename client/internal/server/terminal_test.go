package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSameOrigin(t *testing.T) {
	mk := func(host, origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/ws/term", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	if !sameOrigin(mk("example.trycloudflare.com", "https://example.trycloudflare.com")) {
		t.Error("matching origin should be allowed")
	}
	if !sameOrigin(mk("h", "")) {
		t.Error("empty origin should be allowed")
	}
	if sameOrigin(mk("example.trycloudflare.com", "https://evil.com")) {
		t.Error("cross origin must be rejected")
	}
}

func TestTerminalRequiresAuth(t *testing.T) {
	srv, _ := newAuthServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/term"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected handshake failure without session cookie")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("want 401, got %d", got)
	}
}
