package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCreateLink_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "blocks",
		"to_number": b,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(a, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
		Link struct {
			ID         int64  `json:"id"`
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"link"`
		Event *struct {
			Type string `json:"type"`
		} `json:"event"`
		Changed bool `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "blocks", out.Link.Type)
	assert.Equal(t, a, out.Link.FromNumber)
	assert.Equal(t, b, out.Link.ToNumber)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.linked", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestCreateLink_DuplicateIsNoop(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	postLink(t, env, pid, a, "blocks", b)

	out := postLink(t, env, pid, a, "blocks", b)
	assert.Nil(t, out.Event, "duplicate link is no-op (event:null)")
	assert.False(t, out.Changed)
}

func TestCreateLink_RelatedCanonicalizesOrder(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)           // a < b
	out := postLink(t, env, pid, b, "related", a) // user passes b → a
	assert.Equal(t, "related", out.Link.Type)
	assert.Equal(t, a, out.Link.FromNumber, "canonical: from < to")
	assert.Equal(t, b, out.Link.ToNumber)
}

func TestCreateLink_ParentAlreadySetIs409(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "parent",
		"to_number": p2,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(child, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCreateLink_ParentReplaceSwapsParent(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "parent",
		"to_number": p2,
		"replace":   true,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(child, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Link struct {
			ToNumber int64 `json:"to_number"`
		} `json:"link"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, p2, out.Link.ToNumber)
}

func TestCreateLink_ParentReplaceUnlinkEventPointsToOldParent(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "parent",
		"to_number": p2,
		"replace":   true,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(child, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 200, resp.StatusCode)

	// The unlink event isn't in the response (response carries only the
	// linked event). Query the events table directly to verify the unlink
	// event references the OLD parent (p1), not the new (p2).
	row := env.DB.QueryRowContext(t.Context(),
		`SELECT payload FROM events
		 WHERE project_id = ? AND type = 'issue.unlinked'
		 ORDER BY id DESC LIMIT 1`, pid)
	var payload string
	require.NoError(t, row.Scan(&payload))
	var pl struct {
		ToNumber int64 `json:"to_number"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &pl))
	assert.Equal(t, p1, pl.ToNumber, "unlink event must reference the old parent's number")
}

func TestDeleteLink_RemovesAndEmitsUnlink(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	created := postLink(t, env, pid, a, "blocks", b)

	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(a, 10)+
			"/links/"+strconv.FormatInt(created.Link.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req) //nolint:gosec // test-only, loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event *struct {
			Type string `json:"type"`
		} `json:"event"`
		Changed bool `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unlinked", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestDeleteLink_AbsentIs200NoOp(t *testing.T) {
	env := testenv.New(t)
	pid, a, _ := setupTwoIssues(t, env)
	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(a, 10)+
			"/links/9999?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req) //nolint:gosec // test-only, loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event *struct {
			Type string `json:"type"`
		} `json:"event"`
		Changed bool `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

// --- helpers used across handlers_links_test.go and handlers_labels_test.go ---

// setupTwoIssues creates a workspace, two issues, and returns (project_id, a_number, b_number).
func setupTwoIssues(t *testing.T, env *testenv.Env) (int64, int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	a := createIssueViaHTTP(t, env, pid, "a")
	b := createIssueViaHTTP(t, env, pid, "b")
	return pid, a, b
}

// initWorkspaceViaHTTP runs git init in a temp dir, adds origin, posts to
// /api/v1/projects, and returns the resolved project_id.
func initWorkspaceViaHTTP(t *testing.T, env *testenv.Env, origin string) int64 {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--quiet")
	mustRun(t, dir, "git", "remote", "add", "origin", origin)

	body, _ := json.Marshal(map[string]string{"start_path": dir})
	resp, err := env.HTTP.Post(env.URL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	body, _ = json.Marshal(map[string]string{"start_path": dir})
	resp, err = env.HTTP.Post(env.URL+"/api/v1/projects/resolve", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Project.ID
}

// createIssueViaHTTP creates an issue and returns its number.
func createIssueViaHTTP(t *testing.T, env *testenv.Env, projectID int64, title string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": "tester", "title": title})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Issue.Number
}

// linkResp is the decoded shape of a CreateLinkResponse body.
type linkResp struct {
	Issue struct {
		Number int64 `json:"number"`
	} `json:"issue"`
	Link struct {
		ID         int64  `json:"id"`
		Type       string `json:"type"`
		FromNumber int64  `json:"from_number"`
		ToNumber   int64  `json:"to_number"`
	} `json:"link"`
	Event *struct {
		Type string `json:"type"`
	} `json:"event"`
	Changed bool `json:"changed"`
}

// postLink is a small wrapper that calls POST /links and returns the decoded
// CreateLinkResponse-shaped body.
func postLink(t *testing.T, env *testenv.Env, projectID, fromNumber int64, linkType string, toNumber int64) linkResp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "type": linkType, "to_number": toNumber,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+
			"/issues/"+strconv.FormatInt(fromNumber, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, 200, resp.StatusCode, "postLink expected 200, got %d", resp.StatusCode)
	var out linkResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// mustRun runs a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...) //nolint:gosec // G204: test-controlled args
	cmd.Dir = dir
	require.NoErrorf(t, cmd.Run(), "%s %v", name, args)
}
