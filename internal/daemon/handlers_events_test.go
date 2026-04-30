package daemon_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func mkProject(t *testing.T, env *testenv.Env, identity, name string) int64 {
	t.Helper()
	p, err := env.DB.CreateProject(context.Background(), identity, name)
	require.NoError(t, err)
	return p.ID
}

func mkIssue(t *testing.T, env *testenv.Env, projectID int64, title string) db.Issue {
	t.Helper()
	is, _, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: "tester",
	})
	require.NoError(t, err)
	return is
}

func TestPollEvents_EmptyResultIsNonNullArray(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	bs, _ := io.ReadAll(resp.Body)
	body := string(bs)
	assert.Contains(t, body, `"events":[]`, "must be empty array, never null")
	assert.Contains(t, body, `"reset_required":false`)
	assert.Contains(t, body, `"next_after_id":0`)
}

func TestPollEvents_ReturnsEventsAndAdvancesCursor(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")
	mkIssue(t, env, pid, "second")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		ResetRequired bool `json:"reset_required"`
		Events        []struct {
			EventID int64  `json:"event_id"`
			Type    string `json:"type"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.Len(t, b.Events, 2)
	assert.Equal(t, int64(1), b.Events[0].EventID)
	assert.Equal(t, int64(2), b.Events[1].EventID)
	assert.Equal(t, "issue.created", b.Events[0].Type)
	assert.Equal(t, int64(2), b.NextAfterID, "advances to max event id")
	assert.False(t, b.ResetRequired)
}

func TestPollEvents_NextAfterIDEchoesAfterIDOnEmpty(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "only")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=99&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	bs, _ := io.ReadAll(resp.Body)
	body := string(bs)
	assert.Contains(t, body, `"next_after_id":99`)
	assert.Contains(t, body, `"events":[]`)
}

func TestPollEvents_PerProjectFiltersOtherProjects(t *testing.T) {
	env := testenv.New(t)
	pa := mkProject(t, env, "github.com/test/a", "a")
	pb := mkProject(t, env, "github.com/test/b", "b")
	mkIssue(t, env, pa, "a1")
	mkIssue(t, env, pb, "b1")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pa, 10) + "/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Events []struct {
			ProjectID int64 `json:"project_id"`
		} `json:"events"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.Len(t, b.Events, 1)
	assert.Equal(t, pa, b.Events[0].ProjectID)
}

func TestPollEvents_ResetRequiredAfterPurge(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)

	// Cursor below the reset → reset_required:true
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		ResetRequired bool  `json:"reset_required"`
		ResetAfterID  int64 `json:"reset_after_id"`
		Events        []struct {
			EventID int64 `json:"event_id"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	assert.True(t, b.ResetRequired)
	assert.Greater(t, b.ResetAfterID, int64(0))
	assert.Equal(t, b.ResetAfterID, b.NextAfterID, "next_after_id == reset_after_id when reset")
	assert.Len(t, b.Events, 0)
}

func TestPollEvents_LimitClampsAt1000(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	for i := 0; i < 3; i++ {
		mkIssue(t, env, pid, "x")
	}
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=99999")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode, "values >1000 must clamp silently, not 400")
}

func TestPollEvents_LimitNonPositiveIs400(t *testing.T) {
	env := testenv.New(t)
	for _, q := range []string{"after_id=0&limit=0", "after_id=0&limit=-5"} {
		resp, err := env.HTTP.Get(env.URL + "/api/v1/events?" + q)
		require.NoError(t, err)
		bs, _ := io.ReadAll(resp.Body)
		body := string(bs)
		_ = resp.Body.Close()
		assert.Equal(t, 400, resp.StatusCode, "limit %s should be 400", q)
		assert.Contains(t, body, `"code":"validation"`)
	}
}

func TestPollEvents_LimitNonNumericIs400(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=foo")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestPollEvents_LimitAbsentUsesDefault(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")
	mkIssue(t, env, pid, "second")

	// No limit query param at all — should default to 100 and return both rows.
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Events []struct {
			EventID int64 `json:"event_id"`
		} `json:"events"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.Len(t, b.Events, 2, "missing limit should default to pollLimitDefault, not reject the request")
}

// TestPollEvents_PerProject_NonPositiveProjectIDIs400 ensures that a request
// to /api/v1/projects/0/events does not silently fall through to the
// cross-project sentinel (projectID == 0) and leak every project's events.
func TestPollEvents_PerProject_NonPositiveProjectIDIs400(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/0/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
	bs, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(bs), `"code":"validation"`)
}

// TestPollEvents_PerProject_UnknownProjectIs404 mirrors sibling project-scoped
// handlers (e.g. issues/comments) which 404 with project_not_found rather
// than returning an empty list for a project that does not exist.
func TestPollEvents_PerProject_UnknownProjectIs404(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/9999/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 404, resp.StatusCode)
	bs, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(bs), `"code":"project_not_found"`)
}

type sseFrame struct {
	id    string
	event string
	data  string
}

func readSSEFramesUntilN(t *testing.T, body interface {
	Read([]byte) (int, error)
	Close() error
}, n int, timeout time.Duration) []sseFrame {
	t.Helper()
	var frames []sseFrame
	cur := sseFrame{}
	deadline := time.Now().Add(timeout)

	// A single long-lived goroutine owns the bufio.Reader; the test loop
	// pulls lines from lineCh. Concurrent ReadString on the same reader
	// would be undefined behavior, and a goroutine-per-line design would
	// leak the in-flight ReadString on timeout.
	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult)
	go func() {
		defer close(lineCh)
		rd := bufio.NewReader(body)
		for {
			s, err := rd.ReadString('\n')
			lineCh <- lineResult{s, err}
			if err != nil {
				return
			}
		}
	}()

	for len(frames) < n && time.Now().Before(deadline) {
		var lr lineResult
		var ok bool
		select {
		case lr, ok = <-lineCh:
			if !ok {
				return frames
			}
		case <-time.After(time.Until(deadline)):
			return frames
		}
		if lr.err != nil {
			return frames
		}
		line := strings.TrimRight(lr.line, "\r\n")
		switch {
		case line == "":
			if cur.id != "" || cur.event != "" || cur.data != "" {
				frames = append(frames, cur)
				cur = sseFrame{}
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat — ignore but keep reading
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frames
}

func openSSE(t *testing.T, env *testenv.Env, query string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream?"+query, nil)
	require.NoError(t, err)
	for k, vv := range header {
		req.Header[k] = vv
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	return resp
}

func TestSSE_AcceptNegotiation(t *testing.T) {
	env := testenv.New(t)

	// Missing Accept → 406
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream", nil)
	req.Header.Del("Accept")
	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	bs, _ := io.ReadAll(resp.Body)
	body := string(bs)
	_ = resp.Body.Close()
	assert.Equal(t, 406, resp.StatusCode)
	assert.Contains(t, body, `"code":"not_acceptable"`)

	// Wrong Accept → 406
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Accept", "application/json")
	resp, err = env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 406, resp.StatusCode)

	// Right Accept → 200
	resp = openSSE(t, env, "", http.Header{"Accept": []string{"text/event-stream"}})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// */* → 200
	resp = openSSE(t, env, "", http.Header{"Accept": []string{"*/*"}})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestSSE_CursorConflict(t *testing.T) {
	env := testenv.New(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream?after_id=5", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", "10")
	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	bs, _ := io.ReadAll(resp.Body)
	body := string(bs)
	_ = resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
	assert.Contains(t, body, `"code":"cursor_conflict"`)
}

func TestSSE_HandshakeWritesConnectedComment(t *testing.T) {
	env := testenv.New(t)
	resp := openSSE(t, env, "", nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"))
	// Read first 16 bytes; should contain ": connected\n\n".
	buf := make([]byte, 16)
	_, err := resp.Body.Read(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf), ": connected")
}

func TestSSE_DrainEmitsExistingEventsInOrder(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")
	mkIssue(t, env, pid, "second")
	mkIssue(t, env, pid, "third")

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	frames := readSSEFramesUntilN(t, resp.Body, 3, 2*time.Second)
	require.Len(t, frames, 3)
	assert.Equal(t, "1", frames[0].id)
	assert.Equal(t, "issue.created", frames[0].event)
	assert.Equal(t, "2", frames[1].id)
	assert.Equal(t, "3", frames[2].id)
}

func TestSSE_PerProjectFilterExcludesOtherProjects(t *testing.T) {
	env := testenv.New(t)
	pa := mkProject(t, env, "github.com/test/a", "a")
	pb := mkProject(t, env, "github.com/test/b", "b")
	mkIssue(t, env, pa, "a1")
	mkIssue(t, env, pb, "b1")

	resp := openSSE(t, env, "project_id="+strconv.FormatInt(pa, 10)+"&after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()
	frames := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, frames, 1)
	assert.Equal(t, "1", frames[0].id, "should see only project A's event 1, not project B's event 2")
}

func TestSSE_ResetWhenCursorInsidePurgeGap(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	frames := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, frames, 1)
	assert.Equal(t, "sync.reset_required", frames[0].event)
	assert.NotEmpty(t, frames[0].id)
	assert.Contains(t, frames[0].data, `"reset_after_id":`+frames[0].id)
}

func TestSSE_DrainFollowedByLiveBroadcast(t *testing.T) {
	t.Skip("requires testenv.Env.Broadcaster accessor — added in Task 8")
}

func TestSSE_LiveResetClosesStream(t *testing.T) {
	// Wired in Task 8 via testenv.Env.Broadcaster accessor + purge handler
	// broadcasting the reset signal post-commit.
	t.Skip("requires testenv.Env.Broadcaster accessor — added in Task 8")
}

func TestSSE_LiveHeartbeatKeepsConnectionAlive(t *testing.T) {
	// Connection should stay open for >100ms with empty DB and no events.
	// We don't wait for a 25s heartbeat; we only verify the stream isn't
	// immediately torn down.
	env := testenv.New(t)
	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// Read the : connected\n\n preamble.
	buf := make([]byte, 16)
	_, err := resp.Body.Read(buf)
	require.NoError(t, err)

	// No frames should arrive in 100ms with an empty DB.
	frames := readSSEFramesUntilN(t, resp.Body, 1, 100*time.Millisecond)
	assert.Len(t, frames, 0)
}
