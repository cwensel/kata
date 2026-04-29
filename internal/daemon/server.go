package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// ServerConfig wires the daemon's runtime dependencies. DB and StartedAt are
// required; Endpoint is only consulted by Run.
type ServerConfig struct {
	DB        *db.DB
	StartedAt time.Time
	Endpoint  DaemonEndpoint
}

// Server bundles the http handler and lifecycle.
type Server struct {
	cfg     ServerConfig
	handler http.Handler
	api     huma.API
}

// NewServer wires routes onto a fresh http.ServeMux. The returned handler is
// safe to mount in tests via httptest.NewServer.
func NewServer(cfg ServerConfig) *Server {
	api.InstallErrorFormatter()

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("kata", "0.1.0")
	humaConfig.OpenAPIPath = "" // Plan 1: no /openapi.json
	humaAPI := humago.New(mux, humaConfig)

	s := &Server{cfg: cfg, api: humaAPI}
	registerRoutes(humaAPI, cfg)

	s.handler = withCSRFGuards(mux)
	return s
}

// Handler returns the http.Handler suitable for httptest.NewServer.
func (s *Server) Handler() http.Handler { return s.handler }

// API returns the underlying huma.API for handler registration in tests.
func (s *Server) API() huma.API { return s.api }

// Close releases server-owned resources. Currently a no-op since the DB is
// owned by the caller.
func (s *Server) Close() error { return nil }

// Run listens on the configured endpoint until ctx is cancelled. The caller is
// responsible for writing the runtime file once Run has started.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.Endpoint == nil {
		return errors.New("server: endpoint is required for Run")
	}
	l, err := s.cfg.Endpoint.Listen()
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	if err := httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// withCSRFGuards rejects browser-borne requests and enforces JSON content type
// on mutation methods that carry a body. Per spec §2.9, CLI/TUI never set
// Origin so this is transparent for our own clients.
func withCSRFGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			http.Error(w, "Origin header forbidden", http.StatusForbidden)
			return
		}
		if isMutation(r.Method) && r.ContentLength != 0 {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isMutation reports whether the HTTP method modifies state and therefore
// should be subject to the JSON content-type guard.
func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// registerRoutes installs the per-resource handler groups onto humaAPI. Each
// group lives in its own file (handlers_health.go, handlers_projects.go, etc.)
// and replaces the matching stub below as it lands.
func registerRoutes(humaAPI huma.API, cfg ServerConfig) {
	registerHealth(humaAPI, cfg)
	registerProjects(humaAPI, cfg)
	registerIssues(humaAPI, cfg)
	registerComments(humaAPI, cfg)
	registerActions(humaAPI, cfg)
}

// registerHealth registers /api/v1/ping and /api/v1/health.
func registerHealth(humaAPI huma.API, cfg ServerConfig) {
	registerHealthHandlers(humaAPI, cfg)
}

// registerProjects registers project-scoped routes (resolve, init, list, show).
func registerProjects(humaAPI huma.API, cfg ServerConfig) {
	registerProjectsHandlers(humaAPI, cfg)
}

// registerIssues registers issue CRUD routes. Stub until Task 16.
func registerIssues(_ huma.API, _ ServerConfig) {}

// registerComments registers issue-comment routes. Stub until Task 17.
func registerComments(_ huma.API, _ ServerConfig) {}

// registerActions registers close/reopen action routes. Stub until Task 17.
func registerActions(_ huma.API, _ ServerConfig) {}
