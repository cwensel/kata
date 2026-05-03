package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // git binary is fixed; args are test-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(js))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}

func TestResolve_FailsOutsideKataTomlAndWithoutAlias(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"start_path": t.TempDir(),
	})
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(bs), "project_not_initialized")
}

func TestInit_FromGitRemoteCreatesProject(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			ID       int64
			Identity string
			Name     string
		} `json:"project"`
		Alias struct {
			AliasIdentity string `json:"alias_identity"`
			AliasKind     string `json:"alias_kind"`
		} `json:"alias"`
		WorkspaceRoot string `json:"workspace_root"`
		Created       bool   `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/kata", body.Project.Identity)
	assert.Equal(t, "kata", body.Project.Name)
	assert.True(t, body.Created)
	assert.Equal(t, "github.com/wesm/kata", body.Alias.AliasIdentity)

	// .kata.toml must have been written
	_, err := os.Stat(filepath.Join(dir, ".kata.toml"))
	assert.NoError(t, err)
}

func TestInit_FreshCloneFromExistingKataToml(t *testing.T) {
	// Simulate "git clone, kata init" on a repo that already had .kata.toml.
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture matches production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/system"
name     = "system"
`), 0o644))

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct {
			Identity string
		} `json:"project"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/system", body.Project.Identity)
	assert.True(t, body.Created)
}

func TestResolve_AfterInitSucceeds(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

func TestInit_AliasConflictWithoutReassign(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	// First init binds the alias to "github.com/wesm/kata".
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	// .kata.toml now declares a different identity.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"), //nolint:gosec // test fixture matches production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/other"
name     = "other"
`), 0o644))

	// Re-init without --replace must fail.
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, string(bs), "project_alias_conflict")

	// With --reassign + --replace, succeeds and rewrites alias.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
		"replace":    true,
		"reassign":   true,
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
}

func TestListProjectsAndShow(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/x.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, err := http.Get(ts.URL + "/api/v1/projects")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/x"`)

	// pull project_id from the resolve flow then GET the show endpoint.
	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))
	resp2, err := http.Get(ts.URL + "/api/v1/projects/" + strconv.FormatInt(rbody.Project.ID, 10))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(body2), `"aliases":`)
}

func TestRenameProject_UpdatesNameAndKeepsIdentity(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+strconv.FormatInt(rbody.Project.ID, 10), map[string]any{
		"name": "Kata Tracker",
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
	assert.Contains(t, string(bs), `"name":"Kata Tracker"`)
	assert.Contains(t, string(bs), `"aliases":`)

	resp2, err := http.Get(ts.URL + "/api/v1/projects/" + strconv.FormatInt(rbody.Project.ID, 10))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(body2), `"name":"Kata Tracker"`)
}

func TestRenameProject_RejectsBlankName(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct {
		Project struct{ ID int64 }
	}
	require.NoError(t, json.Unmarshal(rb, &rbody))

	resp, bs := patchJSON(t, ts, "/api/v1/projects/"+strconv.FormatInt(rbody.Project.ID, 10), map[string]any{
		"name": "   ",
	})
	assert.Equal(t, 400, resp.StatusCode)
	assert.Contains(t, string(bs), "name must be non-empty")
}

func TestRenameProject_MissingIs404(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := patchJSON(t, ts, "/api/v1/projects/9999", map[string]any{
		"name": "Missing",
	})
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(bs), "project_not_found")
}

func TestMergeProject_SourceMovesIntoSurvivingTarget(t *testing.T) {
	h := newServerWithGitWorkspace(t, "")
	store := h.DB()
	ctx := t.Context()
	kenn, err := store.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	steward, err := store.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, kenn.ID, "github.com/wesm/kenn", "git", "/tmp/kenn")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, steward.ID, "github.com/wesm/steward", "git", "/tmp/steward")
	require.NoError(t, err)
	_, _, err = store.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: kenn.ID, Title: "existing work", Author: "tester",
	})
	require.NoError(t, err)

	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(steward.ID, 10)+"/merge",
		map[string]any{"source_project_id": kenn.ID})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/steward"`)
	assert.Contains(t, string(bs), `"issues_moved":1`)
	assert.Contains(t, string(bs), `"next_issue_number":2`)

	issue, err := store.IssueByNumber(ctx, steward.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "existing work", issue.Title)
	_, err = store.ProjectByID(ctx, kenn.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestInit_MergedKataTomlIdentityResolvesToSurvivingProject(t *testing.T) {
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/steward.git")
	store := h.DB()
	ctx := t.Context()
	kenn, err := store.CreateProject(ctx, "github.com/wesm/kenn", "kenn")
	require.NoError(t, err)
	steward, err := store.CreateProject(ctx, "github.com/wesm/steward", "steward")
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, kenn.ID, "github.com/wesm/kenn", "git", h.dir)
	require.NoError(t, err)
	_, err = store.AttachAlias(ctx, steward.ID, "github.com/wesm/steward", "git", h.dir)
	require.NoError(t, err)
	_, err = store.MergeProjects(ctx, db.MergeProjectsParams{
		SourceProjectID: kenn.ID,
		TargetProjectID: steward.ID,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(h.dir, ".kata.toml"), //nolint:gosec // test fixture mirrors production .kata.toml mode
		[]byte(`version = 1

[project]
identity = "github.com/wesm/kenn"
name     = "kenn"
`), 0o644))

	resp, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{
		"start_path": h.dir,
	})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/steward"`)

	cfgBytes, err := os.ReadFile(filepath.Join(h.dir, ".kata.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfgBytes), `identity = "github.com/wesm/steward"`)
}
