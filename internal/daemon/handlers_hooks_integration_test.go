package daemon_test

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/hooks"
)

// recordingSink captures every Enqueue for assertion. Lives only in tests;
// production paths use the real *hooks.Dispatcher.
type recordingSink struct {
	mu     sync.Mutex
	events []db.Event
}

func (r *recordingSink) Enqueue(evt db.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *recordingSink) snapshot() []db.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]db.Event, len(r.events))
	copy(out, r.events)
	return out
}

var _ hooks.Sink = (*recordingSink)(nil)

// newServerWithRecordingSink wires a fresh DB+server with the recordingSink
// installed, plus a git workspace bootstrapped with origin so kata init can
// derive an alias identity. Returns the test server, its workspace dir, and
// the sink so assertions can inspect captured events.
func newServerWithRecordingSink(t *testing.T) (*httptest.Server, string, *recordingSink) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	d := openTestDB(t)
	sink := &recordingSink{}
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now, Hooks: sink})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, dir, sink
}

// TestHooks_IssueCreate_EnqueuesSibling exercises the create-issue handler
// end-to-end and asserts the sibling Hooks.Enqueue fires alongside the
// Broadcaster.Broadcast — proving the integration point exists and matches
// the persisted event row's Type.
func TestHooks_IssueCreate_EnqueuesSibling(t *testing.T) {
	ts, dir, sink := newServerWithRecordingSink(t)

	// Bootstrap the project; project init does not emit events.
	_, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})
	var initResp struct{ Project struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(bs, &initResp))
	pid := initResp.Project.ID

	// Pre-condition: project init does not enqueue anything.
	require.Empty(t, sink.snapshot(), "project init should not emit hook events")

	resp, body := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "first", "body": "details"})
	require.Equal(t, 200, resp.StatusCode, string(body))

	captured := sink.snapshot()
	require.Len(t, captured, 1, "exactly one hook event should have been enqueued")
	assert.Equal(t, "issue.created", captured[0].Type)
	assert.Equal(t, "agent-1", captured[0].Actor)
	assert.NotZero(t, captured[0].ID, "captured event should carry the persisted row id")
}
