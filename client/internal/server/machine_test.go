package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

func TestMachineInfoRequiresPairing(t *testing.T) {
	srv, _ := newLLMServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/machine", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired status=%d", rec.Code)
	}
}

func TestMachineInfoReflectsCurrentTarget(t *testing.T) {
	srv, cookie := newLLMServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/machine", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var info map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()
	if rec.Code != http.StatusOK || info["hostname"] != hostname || info["cwd"] != cwd || info["platform"] != runtime.GOOS+"/"+runtime.GOARCH || info["version"] != "t" {
		t.Fatalf("incorrect target: status=%d info=%v", rec.Code, info)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("machine information must not be cached")
	}
}
