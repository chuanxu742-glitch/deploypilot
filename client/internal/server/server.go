// Package server runs the loopback-only HTTP server that backs the web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
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
	baseCtx     context.Context
	cancelBase  context.CancelFunc
	active      sync.WaitGroup
	newExecutor func() agent.Executor
	// A task owns this slot through cancellation and cleanup, across connections.
	agentMu             sync.Mutex
	agentBusy           bool
	terminalAgentState  string
	terminalSubscribers map[chan []byte]struct{}
	agentContextBytes   int
	shellMu             sync.Mutex
	shell               *sharedShell
	historyDir          string
	legacyAuditDir      string
	historyErr          error
	historyLock         io.Closer
	historyTimer        *time.Timer
	shutdownOnce        sync.Once
	shutdownDone        chan struct{}
	taskMu              sync.Mutex
	tasks               []*taskRecord
	taskCancel          context.CancelFunc
	taskWG              sync.WaitGroup
	taskSubscribers     map[chan []byte]struct{}
	agentMaxSteps       int
	// The most recent finished run's conversation, kept in memory only, so the
	// operator can continue that task. Guarded by taskMu.
	lastConversation *agent.Conversation
	lastConvTaskID   string
}

func (s *Server) acquireAgent() bool {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.agentBusy {
		return false
	}
	s.agentBusy = true
	s.terminalAgentState = "running"
	s.broadcastTerminalLocked(map[string]string{"type": "agent_state", "state": "running"})
	return true
}

func (s *Server) releaseAgent() {
	s.agentMu.Lock()
	s.agentBusy = false
	s.terminalAgentState = "idle"
	s.broadcastTerminalLocked(map[string]string{"type": "agent_state", "state": "idle"})
	s.agentMu.Unlock()
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
	// AgentContextBytes bounds each model request; zero uses the agent default.
	AgentContextBytes int
	// AgentMaxSteps is the model-turn budget per run; zero uses the default.
	AgentMaxSteps  int
	HistoryDir     string
	LegacyAuditDir string
}

// New creates a server. It does not start listening until Start is called.
func New(opts Options) *Server {
	s := &Server{
		version:           opts.Version,
		assets:            opts.Assets,
		auth:              opts.Auth,
		llm:               opts.LLM,
		mux:               http.NewServeMux(),
		historyDir:        opts.HistoryDir,
		legacyAuditDir:    opts.LegacyAuditDir,
		taskSubscribers:   make(map[chan []byte]struct{}),
		agentContextBytes: opts.AgentContextBytes,
		agentMaxSteps:     opts.AgentMaxSteps,
	}
	s.shutdownDone = make(chan struct{})
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.newExecutor = func() agent.Executor { return &sessionExecutor{server: s, files: agent.NewShellExecutor()} }
	s.loadTasks()
	s.routes()
	return s
}

// Handler exposes the router for tests.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/api/health", s.handleHealth)
	if s.auth != nil {
		s.mux.HandleFunc("/api/pair", s.handlePair)
		// Host details and controls share the paired session boundary.
		s.mux.Handle("/api/session", s.auth.Middleware(http.HandlerFunc(s.handleSession)))
		s.mux.Handle("/api/machine", s.auth.Middleware(http.HandlerFunc(s.handleMachine)))
		s.mux.Handle("/ws/term", s.auth.Middleware(http.HandlerFunc(s.handleTerminal)))
		if s.llm != nil {
			s.mux.Handle("/api/llm/status", s.auth.Middleware(http.HandlerFunc(s.handleLLMStatus)))
			s.mux.Handle("/api/llm/config", s.auth.Middleware(http.HandlerFunc(s.handleLLMConfig)))
			s.mux.Handle("/api/llm/models", s.auth.Middleware(http.HandlerFunc(s.handleLLMModels)))
			s.mux.Handle("/api/tasks", s.auth.Middleware(http.HandlerFunc(s.handleTasks)))
			s.mux.Handle("/api/tasks/", s.auth.Middleware(http.HandlerFunc(s.handleTasks)))
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
	if s.historyErr != nil {
		// Fail closed so a corrupt or stale history is never silently lost,
		// but tell the operator exactly where it is and how to recover.
		return "", fmt.Errorf("历史记录不可用：%w；如该目录已损坏或为上次运行的残留，删除或移走后重试：%s", s.historyErr, s.historyDir)
	}
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

	s.shutdownOnce.Do(func() {
		go func() {
			s.active.Wait()
			s.taskWG.Wait()
			s.taskMu.Lock()
			if s.historyTimer != nil {
				s.historyTimer.Stop()
				s.historyTimer = nil
			}
			s.persistTasksLocked()
			s.taskMu.Unlock()
			s.shellMu.Lock()
			if s.shell != nil {
				_ = s.shell.Close()
			}
			s.shellMu.Unlock()
			s.taskMu.Lock()
			if s.historyLock != nil {
				_ = s.historyLock.Close()
				s.historyLock = nil
			}
			s.taskMu.Unlock()
			close(s.shutdownDone)
		}()
	})
	select {
	case <-s.shutdownDone:
	case <-ctx.Done():
		return ctx.Err()
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
	stopServer := context.AfterFunc(s.baseCtx, func() { _ = closer.Close() })
	return func() {
		stop()
		stopServer()
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
