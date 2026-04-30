package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestAddLabel_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	out := postLabel(t, env, pid, n, "needs-review")
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.labeled", out.Event.Type)
	assert.True(t, out.Changed)
	assert.Equal(t, "needs-review", out.Label.Label)
}

func TestAddLabel_DuplicateIsNoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	postLabel(t, env, pid, n, "bug")
	out := postLabel(t, env, pid, n, "bug")
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
	// No-op response must still carry the existing label row, not a zero value.
	assert.Equal(t, "bug", out.Label.Label)
}

func TestAddLabel_InvalidIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "tester", "label": "Bad-Case"})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(n, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestRemoveLabel_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	postLabel(t, env, pid, n, "bug")

	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels/bug?actor=tester", nil)
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
	assert.Equal(t, "issue.unlabeled", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestRemoveLabel_AbsentIs200NoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels/never-attached?actor=tester", nil)
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
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

func TestAddLabel_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "   ", "label": "bug"})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestRemoveLabel_BlankActorIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels/bug?actor=%20%20", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req) //nolint:gosec // test-only, loopback URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestLabelsList_ReturnsCounts(t *testing.T) {
	env := testenv.New(t)
	pid, a := setupOneIssue(t, env)
	b := createIssueViaHTTP(t, env, pid, "b")
	postLabel(t, env, pid, a, "bug")
	postLabel(t, env, pid, a, "priority:high")
	postLabel(t, env, pid, b, "bug")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/labels")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Labels []struct {
			Label string `json:"label"`
			Count int64  `json:"count"`
		} `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	got := map[string]int64{}
	for _, c := range out.Labels {
		got[c.Label] = c.Count
	}
	assert.Equal(t, int64(2), got["bug"])
	assert.Equal(t, int64(1), got["priority:high"])
}

// --- helpers ---

// setupOneIssue creates a workspace + one issue, returns (project_id, issue_number).
func setupOneIssue(t *testing.T, env *testenv.Env) (int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	n := createIssueViaHTTP(t, env, pid, "x")
	return pid, n
}

// labelResp is the decoded shape of an AddLabelResponse body.
type labelResp struct {
	Issue struct {
		Number int64 `json:"number"`
	} `json:"issue"`
	Label struct {
		Label string `json:"label"`
	} `json:"label"`
	Event *struct {
		Type string `json:"type"`
	} `json:"event"`
	Changed bool `json:"changed"`
}

// postLabel calls POST /labels and returns the decoded response.
func postLabel(t *testing.T, env *testenv.Env, projectID, number int64, label string) labelResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": "tester", "label": label})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+
			"/issues/"+strconv.FormatInt(number, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, 200, resp.StatusCode, "postLabel expected 200, got %d", resp.StatusCode)
	var out labelResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}
