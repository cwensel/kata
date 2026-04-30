package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

// testDBHandle bundles a fresh *db.DB and a started-at timestamp for daemon
// server tests.
type testDBHandle struct {
	db  *db.DB
	now time.Time
}

// openTestDB opens a fresh sqlite DB rooted in t.TempDir() and registers a
// cleanup to close it. The returned handle is suitable for daemon.ServerConfig.
func openTestDB(t *testing.T) testDBHandle {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return testDBHandle{db: d, now: time.Now().UTC()}
}

// newServerWithGitWorkspace creates a fresh git repo in t.TempDir(), wires a
// daemon server against a fresh DB, and returns a handle exposing both. When
// originURL is non-empty it is added as the "origin" remote so alias
// derivation has an http(s) URL to chew on.
func newServerWithGitWorkspace(t *testing.T, originURL string) *httptestServerHandle {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	if originURL != "" {
		runGit(t, dir, "remote", "add", "origin", originURL)
	}
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &httptestServerHandle{ts: ts, dir: dir}
}

// patchJSON issues a PATCH request with a JSON body and returns the response
// plus the buffered body. Mirrors postJSON for the PATCH-only handlers.
func patchJSON(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPatch, ts.URL+path, bytes.NewReader(js))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test request to httptest server URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}

// getBody runs a GET against the test server and asserts a 2xx status. Returns
// the body as a string for easy substring assertions.
func getBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req) //nolint:gosec // G704: test request to httptest server URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(bs))
	return string(bs)
}

// getStatusBody is like getBody but returns the response so callers can assert
// on non-2xx status codes.
func getStatusBody(t *testing.T, ts *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req) //nolint:gosec // G704: test request to httptest server URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, bs
}

// httpResp is a status+body pair captured by postWithHeader. The bytes are
// already drained, so callers can read the body multiple times.
type httpResp struct {
	status int
	body   []byte
}

// postWithHeader is like postJSON but allows setting custom headers (e.g. the
// Idempotency-Key header tested by the createIssue handler).
func postWithHeader(t *testing.T, ts *httptest.Server, path string, headers map[string]string, body any) httpResp {
	t.Helper()
	bs, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.URL+path, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req) //nolint:gosec // G704: test request to httptest server URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return httpResp{status: resp.StatusCode, body: out}
}

// requireOK asserts that the captured response was a 200 OK; surfaces the body
// in the failure message so callers don't need to repeat the wrap.
func requireOK(t *testing.T, r httpResp) {
	t.Helper()
	require.Equalf(t, 200, r.status, "expected 200, got %d: %s", r.status, string(r.body))
}
