package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

func newLLMServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	m, err := auth.NewManager(auth.Options{TokenTTL: time.Minute, IdleTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Version: "t", Assets: testAssets(), Auth: m, LLM: llm.New(llm.Config{}), HistoryDir: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"token":"` + m.Token() + `"}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pair", body))
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return srv, c
		}
	}
	t.Fatal("no session cookie")
	return nil, nil
}

func TestLLMStatusAndConfig(t *testing.T) {
	srv, cookie := newLLMServer(t)

	// Initially no key.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/llm/status", nil)
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"hasKey":false`) {
		t.Fatalf("unexpected status body: %s", rec.Body.String())
	}

	// Set key + model.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/llm/config", strings.NewReader(`{"apiKey":"abc","model":"bailu-pro"}`))
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"hasKey":true`) || !strings.Contains(rec.Body.String(), `"model":"bailu-pro"`) {
		t.Fatalf("config body: %s", rec.Body.String())
	}

	// The key must never be echoed back.
	if strings.Contains(rec.Body.String(), "abc") {
		t.Fatalf("response leaked api key: %s", rec.Body.String())
	}
}

func TestLLMConfigRequiresAuth(t *testing.T) {
	srv, _ := newLLMServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/llm/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without cookie, got %d", rec.Code)
	}
}
