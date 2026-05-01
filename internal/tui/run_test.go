package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBoot_ResolvesProject covers §7.2 case 1: cwd is bound to a registered
// project. bootResolveScope should return single-project scope, and the
// initial list fetch should hit the project-scoped endpoint.
func TestBoot_ResolvesProject(t *testing.T) {
	var sawList bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": map[string]any{
					"id":       7,
					"identity": "github.com/wesm/kata",
					"name":     "kata",
				},
				"workspace_root": "/tmp/x",
			})
		case "/api/v1/projects/7/issues":
			sawList = true
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	sc, err := bootResolveScope(t.Context(), c, "/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	if sc.allProjects {
		t.Fatal("expected single-project scope, got allProjects")
	}
	if sc.projectID != 7 {
		t.Fatalf("got projectID %d, want 7", sc.projectID)
	}
	if sc.projectName != "kata" {
		t.Fatalf("projectName = %q, want kata", sc.projectName)
	}
	if sc.workspace != "/tmp/x" {
		t.Fatalf("workspace = %q, want /tmp/x", sc.workspace)
	}
	if sc.homeProjectID != 7 || sc.homeProjectName != "kata" {
		t.Fatalf("home* not seeded: id=%d name=%q", sc.homeProjectID, sc.homeProjectName)
	}
	if _, err := c.ListIssues(t.Context(), sc.projectID, ListFilter{}); err != nil {
		t.Fatal(err)
	}
	if !sawList {
		t.Fatal("expected list endpoint to have been hit")
	}
}

// TestBoot_UnboundCwd_LandsInEmptyState: cwd is unbound; even though
// the daemon has registered projects, we don't fall into all-projects
// (the daemon has no cross-project list endpoint, so a fallback would
// 404). The honest outcome is the empty state with the "run kata
// init" hint. Re-add a fallback-to-allProjects test once the daemon
// ships GET /issues for cross-project reads.
func TestBoot_UnboundCwd_LandsInEmptyState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 404,
				"error": map[string]any{
					"code":    "project_not_initialized",
					"message": "no .kata.toml",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	sc, err := bootResolveScope(t.Context(), c, "/tmp/no-binding")
	if err != nil {
		t.Fatal(err)
	}
	if !sc.empty {
		t.Fatal("expected empty scope, got something else")
	}
	if sc.allProjects {
		t.Fatal("all-projects fallback is gated off; got allProjects=true")
	}
}

// TestBoot_EmptyState_NoProjectsRegistered covers §7.2 case 4: cwd is
// unbound and no projects are registered. bootResolveScope should signal
// the empty view so Run renders an onboarding hint instead of a blank list.
func TestBoot_EmptyState_NoProjectsRegistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "project_not_initialized"},
			})
		case "/api/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{}})
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	sc, err := bootResolveScope(t.Context(), c, "/tmp/empty")
	if err != nil {
		t.Fatal(err)
	}
	if !sc.empty {
		t.Fatal("expected scope.empty=true")
	}
	if sc.allProjects {
		t.Fatal("did not expect allProjects")
	}
}

// TestBoot_NonResolveErrorPropagates: a 500 from /resolve should fail Run
// instead of silently downgrading. Black-screen prevention.
func TestBoot_NonResolveErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":500,"error":{"code":"internal","message":"db down"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	if _, err := bootResolveScope(t.Context(), c, "/tmp/x"); err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
}

// TestInitialFilter_ZeroValueByDefault asserts the boot-time filter is
// the zero value: today there's no Options field that drives initial
// filter state. The shape is preserved so a future task can wire one up
// without changing fetchInitial.
func TestInitialFilter_ZeroValueByDefault(t *testing.T) {
	got := initialFilter(Options{})
	if got.Status != "" || got.Owner != "" || got.Author != "" ||
		got.Search != "" || len(got.Labels) != 0 {
		t.Fatalf("initialFilter = %+v, want zero value", got)
	}
}

// TestOutputIsTerminal_RejectsNonFile confirms a non-*os.File writer
// (e.g., bytes.Buffer in tests) is treated as a non-terminal so Run
// surfaces errNotATTY instead of writing alt-screen control sequences
// into a buffer that cannot honor them.
func TestOutputIsTerminal_RejectsNonFile(t *testing.T) {
	var buf bytes.Buffer
	if outputIsTerminal(&buf) {
		t.Fatal("outputIsTerminal(*bytes.Buffer) = true, want false")
	}
}

// TestRun_NonFileStdout_ReturnsNotATTY: piping into a bytes.Buffer (the
// natural test rig) must surface errNotATTY rather than panicking deep
// inside Bubble Tea's renderer.
func TestRun_NonFileStdout_ReturnsNotATTY(t *testing.T) {
	var buf bytes.Buffer
	err := Run(t.Context(), Options{Stdout: &buf})
	if !errors.Is(err, errNotATTY) {
		t.Fatalf("Run returned %v, want errNotATTY", err)
	}
}
