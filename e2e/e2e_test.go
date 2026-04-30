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

// TestSmoke_Plan2Lifecycle exercises the Plan 2 verbs end-to-end via HTTP:
// link, label, assign, ready (with blocked filtering), unassign, label rm,
// and unlink — all on a real daemon over a loopback listener.
func TestSmoke_Plan2Lifecycle(t *testing.T) {
	env := testenv.New(t)
	dir := initRepo(t, "https://github.com/wesm/system.git")

	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dir}))
	pid := resolvePID(t, env.HTTP, env.URL, dir)
	pidStr := strconv.FormatInt(pid, 10)

	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "parent"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "child"}))

	// Hierarchy: child has parent #1.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/links",
		map[string]any{"actor": "agent", "type": "parent", "to_number": 1}))

	// Label child as bug.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/labels",
		map[string]any{"actor": "agent", "label": "bug"}))

	// Assign child to alice.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/actions/assign",
		map[string]any{"actor": "agent", "owner": "alice"}))

	// parent links don't gate ready — only blocks links do. Both issues are
	// ready right now.
	readyBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/ready")
	assert.Contains(t, readyBody, `"title":"parent"`)
	assert.Contains(t, readyBody, `"title":"child"`)

	// Now make parent block child explicitly. child must drop out of ready.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/links",
		map[string]any{"actor": "agent", "type": "blocks", "to_number": 2}))
	readyBody = getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/ready")
	assert.Contains(t, readyBody, `"title":"parent"`)
	assert.NotContains(t, readyBody, `"title":"child"`,
		"child must be filtered while parent (blocker) is open")

	// Look up the blocks-link id so we can DELETE it after unassign.
	parentShow := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1")
	var parentBody struct {
		Links []struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"links"`
	}
	require.NoError(t, json.Unmarshal([]byte(parentShow), &parentBody))
	var blocksLinkID int64
	for _, l := range parentBody.Links {
		if l.Type == "blocks" {
			blocksLinkID = l.ID
			break
		}
	}
	require.NotZero(t, blocksLinkID, "blocks link must be present on #1 before unlink")

	// Unassign + remove label + unlink to verify the reverse paths.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/actions/unassign",
		map[string]any{"actor": "agent"}))
	deleteWith(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/labels/bug?actor=agent")
	deleteWith(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/links/"+
		strconv.FormatInt(blocksLinkID, 10)+"?actor=agent")

	// show #2 must reflect the post-state: owner cleared, bug label gone,
	// parent link still present. Decode the response so the owner check is
	// exact — a substring miss could pass if the handler wrote a different
	// owner string (or "") instead of clearing the column.
	showBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2")
	var post struct {
		Issue struct {
			Owner *string `json:"owner"`
		} `json:"issue"`
	}
	require.NoError(t, json.Unmarshal([]byte(showBody), &post),
		"show response must decode")
	assert.Nil(t, post.Issue.Owner, "owner must be nil after unassign")
	assert.NotContains(t, showBody, `"label":"bug"`, "bug label must be gone from issue #2")
	assert.Contains(t, showBody, `"parent"`, "parent link must still be present on issue #2")

	// And the blocks link must be gone — child is ready again.
	finalReadyBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/ready")
	assert.Contains(t, finalReadyBody, `"title":"child"`,
		"child must be ready again after the blocks link is removed")
}

// deleteWith issues a DELETE through the bounded testenv client and asserts
// the response is 200.
func deleteWith(t *testing.T, client *http.Client, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req) //nolint:gosec // G704: test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body := drain(t, resp)
	require.Equalf(t, 200, resp.StatusCode, "DELETE %s → %d: %s", url, resp.StatusCode, body)
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
