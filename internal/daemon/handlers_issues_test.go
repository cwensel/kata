package daemon_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
