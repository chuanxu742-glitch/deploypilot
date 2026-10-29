// Package server runs the loopback-only HTTP server that backs the web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// Server is the local control-plane HTTP server. It binds to loopback only;
// public exposure is handled separately by the Cloudflare tunnel.
type Server struct {
	version string
	assets  fs.FS
	auth    *auth.Manager
	llm     *llm.Client
	mux     *http.ServeMux
	httpSrv *http.Server
	ln      net.Listener

	// baseCtx is the parent of every request context. Shutdown cancels it so
	// long-lived WebSocket handlers (terminal, agent) stop immediately.
	baseCtx    context.Context
	cancelBase context.CancelFunc
	active     sync.WaitGroup
}

// Options configures the server.
type Options struct {
	// Version is reported by /api/health.
	Version string
	// Assets is the embedded (or on-disk) frontend file system.
	Assets fs.FS
	// Auth manages pairing tokens and sessions. Required for /api/pair and
	// protected routes; may be nil in tests that only exercise static serving.
	Auth *auth.Manager
	// LLM is the model client. May be nil in tests.
	LLM *llm.Client
}

// New creates a server. It does not start listening until Start is called.
func New(opts Options) *Server {
	s := &Server{
		version: opts.Version,
		assets:  opts.Assets,
		auth:    opts.Auth,
		llm:     opts.LLM,
		mux:     http.NewServeMux(),
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.routes()
	return s
}

// Handler exposes the router for tests.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/api/health", s.handleHealth)
	if s.auth != nil {
		s.mux.HandleFunc("/api/pair", s.handlePair)
		// Example protected endpoint; feature routes attach here in later tasks.
		s.mux.Handle("/api/session", s.auth.Middleware(http.HandlerFunc(s.handleSession)))
		s.mux.Handle("/ws/term", s.auth.Middleware(http.HandlerFunc(s.handleTerminal)))
		if s.llm != nil {
			s.mux.Handle("/api/llm/status", s.auth.Middleware(http.HandlerFunc(s.handleLLMStatus)))
			s.mux.Handle("/api/llm/config", s.auth.Middleware(http.HandlerFunc(s.handleLLMConfig)))
			s.mux.Handle("/api/llm/models", s.auth.Middleware(http.HandlerFunc(s.handleLLMModels)))
			s.mux.Handle("/ws/agent", s.auth.Middleware(http.HandlerFunc(s.handleAgent)))
		}
	}
	s.mux.Handle("/", s.handleSPA())
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sid, err := s.auth.Pair(req.Token)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrSessionActive):
			http.Error(w, "session already active", http.StatusConflict)
		default:
			http.Error(w, "invalid token", http.StatusUnauthorized)
		}
		return
	}
	auth.SetCookie(w, sid, s.auth.IdleTimeout())
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"paired"}`)
}

func (s *Server) handleSession(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"authenticated"}`)
}

// Start binds a listener on 127.0.0.1 using an OS-assigned port and begins
// serving in a background goroutine. The chosen address is returned.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.ln = ln
	s.httpSrv = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}
	go func() {
		_ = s.httpSrv.Serve(ln)
	}()
	return ln.Addr().String(), nil
}

// Addr returns the bound address, or "" if not started.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown stops the server. It cancels all in-flight handlers first (which
// closes terminal shells and stops agent runs, killing their commands), then
// waits for them to finish cleanup or for ctx to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.cancelBase()

	done := make(chan struct{})
	go func() {
		s.active.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// track registers a long-lived handler so Shutdown can wait for it, and
// closes closer when the request context is cancelled. Call the returned
// func when the handler returns.
func (s *Server) track(r *http.Request, closer io.Closer) func() {
	s.active.Add(1)
	stop := context.AfterFunc(r.Context(), func() { _ = closer.Close() })
	return func() {
		stop()
		s.active.Done()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `{"status":"ok","version":`+quote(s.version)+`}`)
}

// handleSPA serves embedded static assets and falls back to index.html for
// client-side routes (single-page app behavior).
func (s *Server) handleSPA() http.Handler {
	fileServer := http.FileServer(http.FS(s.assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}
		if _, err := fs.Stat(s.assets, upath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// SPA fallback: serve index.html for unknown non-asset paths.
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				serveIndex(w, r2, s.assets)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	f, err := assets.Open("index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
	_ = r
}

// quote produces a minimal JSON string literal.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
