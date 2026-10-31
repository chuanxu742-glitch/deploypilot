package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>spa</title>")},
		"assets/app.js":    {Data: []byte("console.log(1)")},
		"assets/style.css": {Data: []byte("body{}")},
	}
}

func newTestServer() *Server {
	return New(Options{Version: "9.9.9", Assets: testAssets()})
}

func TestHealth(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v (%q)", err, rec.Body.String())
	}
	if body.Status != "ok" || body.Version != "9.9.9" {
		t.Fatalf("unexpected health body: %+v", body)
	}
}

func TestServesIndex(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatalf("missing content-type")
	}
	if body := rec.Body.String(); body == "" {
		t.Fatalf("empty index body")
	}
}

func TestServesStaticAsset(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	data, _ := io.ReadAll(rec.Body)
	if string(data) != "console.log(1)" {
		t.Fatalf("unexpected asset body: %q", data)
	}
}

func TestSPAFallback(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/some/client/route", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for SPA fallback, got %d", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Fatalf("expected index.html fallback body")
	}
}

func TestStartBindsLoopback(t *testing.T) {
	srv := newTestServer()
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.httpSrv.Close() })
	if addr == "" {
		t.Fatalf("empty addr")
	}
	// Must be loopback.
	if got := addr[:min(len(addr), 9)]; got != "127.0.0.1" {
		t.Fatalf("server must bind loopback, got %q", addr)
	}
}
