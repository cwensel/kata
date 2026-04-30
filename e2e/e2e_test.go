package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestSmoke_FullLifecycle(t *testing.T) {
	env := testenv.New(t)
	dir := initRepo(t, "https://github.com/wesm/system.git")

	// 1. init via HTTP.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dir}))

	// 2. resolve project id.
	pid := resolvePID(t, env.HTTP, env.URL, dir)
	pidStr := strconv.FormatInt(pid, 10)

	// 3. create issue.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "first", "body": "details"}))

	// 4. list — body must contain the issue title.
	listBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues")
	assert.Contains(t, listBody, `"title":"first"`)

	// 5. comment.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "looks good"}))

	// 6. close — and verify the issue is actually closed before we move on.
	// A 200 from the action endpoint is necessary but not sufficient; a buggy
	// handler could no-op while still answering 200.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/actions/close",
		map[string]any{"actor": "agent", "reason": "done"}))
	closedBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1")
	assert.Contains(t, closedBody, `"status":"closed"`, "issue must be closed before reopen")

	// 7. reopen.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/actions/reopen",
		map[string]any{"actor": "agent"}))

	// 8. show with comments — issue is open again, comment from step 5 is preserved.
	showBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1")
	assert.Contains(t, showBody, `"body":"looks good"`)
	assert.Contains(t, showBody, `"status":"open"`)
}

// helpers

func initRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())                 //nolint:gosec // G204: test-controlled args
	require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", origin).Run()) //nolint:gosec // G204: test-controlled origin
	return dir
}

// postJSON sends a request through the bounded testenv client so a hung
// handler fails the test fast instead of stalling on the global timeout.
func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	bs, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) //nolint:gosec // G704: test-only loopback, caller-controlled URL
	require.NoError(t, err)
	return resp
}

// getBody runs a GET via the bounded testenv client and returns the response
// body as a string. The body is fully drained so the connection can be reused.
func getBody(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req) //nolint:gosec // G704: test-only loopback, caller-controlled URL
	require.NoError(t, err)
	body := drain(t, resp)
	require.Equal(t, 200, resp.StatusCode, body)
	return body
}

func requireOK(t *testing.T, resp *http.Response) {
	t.Helper()
	body := drain(t, resp)
	require.Equalf(t, 200, resp.StatusCode, "body: %s", body)
}

func resolvePID(t *testing.T, client *http.Client, baseURL, dir string) int64 {
	t.Helper()
	resp := postJSON(t, client, baseURL+"/api/v1/projects/resolve", map[string]any{"start_path": dir})
	body := drain(t, resp)
	require.Equal(t, 200, resp.StatusCode, body)
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &b), body)
	return b.Project.ID
}

// drain reads and closes the response body, returning the contents as a
// string. Use this on every response so the http.Client's connection pool can
// be reused.
func drain(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(bs)
}
