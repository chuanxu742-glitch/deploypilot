package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
)

func newAuthServer(t *testing.T) (*Server, *auth.Manager) {
	t.Helper()
	m, err := auth.NewManager(auth.Options{
		TokenTTL:    10 * time.Minute,
		IdleTimeout: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("auth manager: %v", err)
	}
	return New(Options{Version: "t", Assets: testAssets(), Auth: m}), m
}

func TestPairEndpoint(t *testing.T) {
	srv, m := newAuthServer(t)

	// Good token pairs and sets a hardened cookie.
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"token":"` + m.Token() + `"}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pair", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("pair want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var sc *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("no session cookie set")
	}
	if !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie not hardened: %+v", sc)
	}

	// Protected route requires the cookie.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("protected route without cookie want 401, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(sc)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("protected route with cookie want 200, got %d", rec.Code)
	}
}

func TestPairBadToken(t *testing.T) {
	srv, _ := newAuthServer(t)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"token":"nope"}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pair", body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token want 401, got %d", rec.Code)
	}
}
