// Package httpapi wires the store, the catalog and the runner to HTTP.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"drill/internal/catalog"
	"drill/internal/runner"
	"drill/internal/store"
)

// Deps are the collaborators the API needs.
type Deps struct {
	Store   *store.Store
	Catalog *catalog.Catalog
	Runner  *runner.Runner
	Reload  func(context.Context) error
	Logger  *slog.Logger
	Static  http.Handler
	// Now supplies the current time; tests replace it.
	Now func() time.Time
}

// Server holds the API handlers and the mux.
type Server struct {
	deps Deps
	mux  *http.ServeMux

	// shutdown is closed when the process starts shutting down. Server.Shutdown
	// waits for active requests, and an SSE stream is an active request that
	// never ends on its own, so every stream watches this channel.
	shutdownOnce sync.Once
	shutdown     chan struct{}
}

// New builds the API.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &Server{deps: deps, mux: http.NewServeMux(), shutdown: make(chan struct{})}
	s.routes()
	return s
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.recoverPanics(s.logRequests(s.mux))
}

// Shutdown releases every streaming client so that http.Server.Shutdown can
// finish instead of waiting out its whole grace period.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

func (s *Server) routes() {
	m := s.mux

	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("GET /readyz", s.handleReadyz)

	m.HandleFunc("GET /api/today", s.handleToday)
	m.HandleFunc("GET /api/tasks", s.handleTasks)
	m.HandleFunc("GET /api/tasks/{id}", s.handleTask)
	m.HandleFunc("GET /api/tasks/{id}/solution", s.handleSolution)
	m.HandleFunc("POST /api/tasks/reload", s.handleReload)
	m.HandleFunc("POST /api/tasks/{id}/attempts", s.handleStartAttempt)

	m.HandleFunc("GET /api/attempts/{id}", s.handleAttempt)
	m.HandleFunc("POST /api/attempts/{id}/abandon", s.handleAbandonAttempt)
	m.HandleFunc("POST /api/attempts/{id}/heartbeat", s.handleHeartbeat)
	m.HandleFunc("PUT /api/attempts/{id}/draft", s.handleSaveDraft)
	m.HandleFunc("GET /api/attempts/{id}/review", s.handleReview)

	m.HandleFunc("POST /api/runs", s.handleStartRun)
	m.HandleFunc("GET /api/runs/{id}", s.handleRun)
	m.HandleFunc("GET /api/runs/{id}/events", s.handleRunEvents)
	m.HandleFunc("POST /api/runs/{id}/cancel", s.handleCancelRun)

	m.HandleFunc("GET /api/notes", s.handleListNotes)
	m.HandleFunc("POST /api/notes", s.handleSaveNote)
	m.HandleFunc("DELETE /api/notes/{id}", s.handleDeleteNote)

	m.HandleFunc("GET /api/stats/tasks", s.handleStatsTasks)
	m.HandleFunc("GET /api/stats/topics", s.handleStatsTopics)

	if s.deps.Static != nil {
		m.Handle("/", s.deps.Static)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.deps.Store.Ping(ctx); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusServiceUnavailable, "database is unreachable", err)
		return
	}
	snap := s.deps.Catalog.Snapshot()
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"status":      "ready",
		"tasks":       snap.Len(),
		"task_errors": snap.Errors(),
		"go_version":  s.deps.Runner.GoVersion(),
		"server_time": s.deps.Now().Format(time.RFC3339),
		"today":       store.DayOf(s.deps.Now()),
	})
}
