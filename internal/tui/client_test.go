package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_ListIssues_BuildsExpectedURLAndDecodes(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"number": 1, "title": "a", "status": "open"},
				{"number": 2, "title": "b", "status": "open"},
			},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListIssues(context.Background(), 7, ListFilter{Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotURL, "/api/v1/projects/7/issues") {
		t.Fatalf("unexpected URL: %s", gotURL)
	}
	if !strings.Contains(gotURL, "status=open") {
		t.Fatalf("status filter missing: %s", gotURL)
	}
	if len(got) != 2 {
		t.Fatalf("got %d issues, want 2", len(got))
	}
}

func TestClient_GetIssue_DecodesWrappedEnvelope(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue":    map[string]any{"number": 42, "title": "fix", "status": "open"},
			"comments": []any{},
			"links":    []any{},
			"labels":   []any{},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.GetIssue(context.Background(), 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/projects/7/issues/42" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if got == nil || got.Number != 42 || got.Title != "fix" {
		t.Fatalf("unexpected issue: %+v", got)
	}
}

func TestClient_CreateIssue_SendsIdempotencyHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	_, err := c.CreateIssue(context.Background(), 7, CreateIssueBody{
		Title: "t", Actor: "alice", IdempotencyKey: "my-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "my-key" {
		t.Fatalf("Idempotency-Key not forwarded: %q", gotKey)
	}
}

func TestClient_DecodeError_ReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(
			`{"status":404,"error":{"code":"project_not_initialized",` +
				`"message":"no .kata.toml ancestor","hint":"run kata init"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	_, err := c.GetIssue(context.Background(), 7, 42)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "project_not_initialized" {
		t.Fatalf("Code = %q, want project_not_initialized", apiErr.Code)
	}
	if apiErr.Status != http.StatusNotFound {
		t.Fatalf("Status = %d, want 404", apiErr.Status)
	}
	if apiErr.Hint != "run kata init" {
		t.Fatalf("Hint = %q, want run kata init", apiErr.Hint)
	}
	if !strings.Contains(apiErr.Error(), "project_not_initialized") {
		t.Fatalf("Error() = %q, want it to mention the code", apiErr.Error())
	}
}

func TestClient_RemoveLabel_PathEscapesLabel(t *testing.T) {
	var gotRawURI, gotMethod, gotActor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawURI = r.RequestURI
		gotMethod = r.Method
		gotActor = r.URL.Query().Get("actor")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	_, err := c.RemoveLabel(context.Background(), 7, 42, "team/backend", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", gotMethod)
	}
	if !strings.Contains(gotRawURI, "labels/team%2Fbackend") {
		t.Fatalf("label not path-escaped, raw URI = %s", gotRawURI)
	}
	if gotActor != "alice" {
		t.Fatalf("actor query missing: %q", gotActor)
	}
}

func TestClient_ListComments_RoutesThroughShowIssue(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue":    map[string]any{"number": 42, "title": "t", "status": "open"},
			"comments": []map[string]any{{"id": 1, "author": "a", "body": "hi"}},
			"links":    []any{},
			"labels":   []any{},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListComments(context.Background(), 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/projects/7/issues/42" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if len(got) != 1 || got[0].Body != "hi" {
		t.Fatalf("unexpected comments: %+v", got)
	}
}

func TestClient_AssignEmptyOwnerRoutesToUnassign(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue":   map[string]any{"number": 1, "title": "t", "status": "open"},
			"changed": true,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	_, err := c.Assign(context.Background(), 7, 42, "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/actions/unassign") {
		t.Fatalf("expected unassign path, got %s", gotPath)
	}
}

func TestClient_ListEvents_FiltersByIssueClientSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/7/events" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{"event_id": 1, "type": "issue.commented", "issue_number": 42, "actor": "a"},
				{"event_id": 2, "type": "issue.commented", "issue_number": 99, "actor": "a"},
				{"event_id": 3, "type": "issue.labeled", "issue_number": 42, "actor": "a"},
			},
			"next_after_id":  3,
			"reset_required": false,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListEvents(context.Background(), 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events for #42, want 2", len(got))
	}
	for _, e := range got {
		if e.Type != "issue.commented" && e.Type != "issue.labeled" {
			t.Fatalf("unexpected event leaked through filter: %+v", e)
		}
	}
}

// TestClient_ListIssues_NotNilOnSuccess guards the bug where listIssuesAt
// returned resp.Issues evaluated *before* c.do filled it (the do call was
// the second operand of the comma-statement, so resp was nil at capture).
func TestClient_ListIssues_NotNilOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"number":1,"title":"a","status":"open"}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListIssues(context.Background(), 7, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("got %+v, want one issue with number=1", got)
	}
}

// TestClient_ListAllIssues_NotNilOnSuccess covers the same regression on
// the cross-project endpoint.
func TestClient_ListAllIssues_NotNilOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"number":2,"title":"b","status":"open"}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListAllIssues(context.Background(), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("got %+v, want one issue with number=2", got)
	}
}

// TestClient_ListProjects_NotNilOnSuccess is the analogue for ListProjects.
func TestClient_ListProjects_NotNilOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"id":7,"identity":"x","name":"k"}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	got, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("got %+v, want one project with id=7", got)
	}
}

// TestClient_ListIssues_FilterShape asserts only the daemon-honored
// query params land on the wire. Owner/Author/Search/Labels are kept on
// the struct for client-side filtering but must not leak as URL params.
func TestClient_ListIssues_FilterShape(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	if _, err := c.ListIssues(context.Background(), 7, ListFilter{
		Status:         "open",
		Owner:          "alice",
		Author:         "bob",
		Search:         "foo",
		Labels:         []string{"x"},
		IncludeDeleted: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotURL, "status=open") {
		t.Fatalf("status not sent: %s", gotURL)
	}
	for _, leaked := range []string{"owner=", "author=", "q=", "label=", "include_deleted="} {
		if strings.Contains(gotURL, leaked) {
			t.Fatalf("client leaked %q to wire (daemon ignores it): %s", leaked, gotURL)
		}
	}
}

// TestAPIError_EmptyBodyFallback covers the 404 with no body case where
// Code and Message are both blank. Without the fallback, Error() would
// return ": ".
func TestAPIError_EmptyBodyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	_, err := c.GetIssue(context.Background(), 7, 42)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	msg := apiErr.Error()
	if !strings.Contains(msg, "HTTP 404") {
		t.Fatalf("Error() = %q, want it to mention HTTP 404", msg)
	}
	if !strings.Contains(msg, "/api/v1/projects/7/issues/42") {
		t.Fatalf("Error() = %q, want it to mention the path", msg)
	}
}
