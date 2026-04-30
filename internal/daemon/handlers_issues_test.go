package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

// httptestServerHandle bundles a httptest.Server with the on-disk workspace
// directory the server was bootstrapped against. The ts field is typed as
// any to keep cross-helper imports loose; tests cast to *httptest.Server.
type httptestServerHandle struct {
	ts  any // *httptest.Server, but kept generic to avoid import cycles in helpers
	dir string
}

// bootstrapProject spins up a fresh server + git workspace and runs `kata
// init` against it, returning the handle and the project rowid. Used as a
// shared setup for every issue handler test.
func bootstrapProject(t *testing.T) (*httptestServerHandle, int64) {
	t.Helper()
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	_, bs := postJSON(t, h.ts.(*httptest.Server), "/api/v1/projects", map[string]any{"start_path": h.dir})
	var resp struct{ Project struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(bs, &resp))
	return h, resp.Project.ID
}

func TestIssues_CreateRoundtrip(t *testing.T) {
	h, projectID := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "first", "body": "details"})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Issue struct {
			Number int64
			Title  string
			Status string
		}
		Event struct{ Type string }
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.EqualValues(t, 1, body.Issue.Number)
	assert.Equal(t, "first", body.Issue.Title)
	assert.Equal(t, "open", body.Issue.Status)
	assert.Equal(t, "issue.created", body.Event.Type)
}

func TestIssues_ListAndShow(t *testing.T) {
	h, pid := bootstrapProject(t)
	for _, title := range []string{"a", "b"} {
		_, _ = postJSON(t, h.ts.(*httptest.Server),
			"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
			map[string]any{"actor": "x", "title": title})
	}

	resp, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues?status=open")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"title":"a"`)
	assert.Contains(t, string(bs), `"title":"b"`)

	resp2, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues/1")
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	bs2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(bs2), `"comments":`)
}

func TestIssues_ListMissingProjectIs404(t *testing.T) {
	h, _ := bootstrapProject(t)
	resp, err := http.Get(h.ts.(*httptest.Server).URL + "/api/v1/projects/9999/issues")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 404, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"project_not_found"`)
}

func TestIssues_PatchEditTitleAndBody(t *testing.T) {
	h, pid := bootstrapProject(t)
	_, _ = postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1",
		map[string]any{"actor": "x", "title": "new"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"title":"new"`)
}

func TestCreateIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "   ", "title": "x"})
	assert.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"validation"`)
}

func TestEditIssue_BlankActorIs400(t *testing.T) {
	h, pid := bootstrapProject(t)
	_, _ = postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1",
		map[string]any{"actor": "   ", "title": "new"})
	assert.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"code":"validation"`)
}

func TestCreateIssue_WithInitialState(t *testing.T) {
	env := testenv.New(t)
	pid, parent, _ := setupTwoIssues(t, env)

	body, _ := json.Marshal(map[string]any{
		"actor":  "tester",
		"title":  "child",
		"owner":  "alice",
		"labels": []string{"bug", "needs-review"},
		"links":  []map[string]any{{"type": "parent", "to_number": parent}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue struct {
			Number int64   `json:"number"`
			Owner  *string `json:"owner"`
		} `json:"issue"`
		Event struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		} `json:"event"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Issue.Owner)
	assert.Equal(t, "alice", *out.Issue.Owner)
	assert.Equal(t, "issue.created", out.Event.Type)
	assert.Contains(t, out.Event.Payload, `"labels":["bug","needs-review"]`)
	assert.Contains(t, out.Event.Payload, `"owner":"alice"`)
	assert.Contains(t, out.Event.Payload, `"type":"parent"`)
}

func TestCreateIssue_InitialLinkToMissingTargetIs404(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "child",
		"links": []map[string]any{{"type": "parent", "to_number": 99}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 404, resp.StatusCode)
}

func TestCreateIssue_InvalidLabelIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "x",
		"labels": []string{"BadCase"},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestShowIssue_IncludesLinksAndLabels(t *testing.T) {
	env := testenv.New(t)
	pid, parent, child := setupTwoIssues(t, env)
	postLabel(t, env, pid, child, "bug")
	postLink(t, env, pid, child, "parent", parent)

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(child, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Links []struct {
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"links"`
		Labels []struct {
			Label string `json:"label"`
		} `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 1)
	assert.Equal(t, "parent", out.Links[0].Type)
	assert.Equal(t, child, out.Links[0].FromNumber)
	assert.Equal(t, parent, out.Links[0].ToNumber)
	require.Len(t, out.Labels, 1)
	assert.Equal(t, "bug", out.Labels[0].Label)
}
