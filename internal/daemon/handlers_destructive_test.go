package daemon_test

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelete_RequiresConfirmHeader(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		nil, // no header
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_required"`)
}

func TestDelete_RejectsWrongConfirmValue(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #2"}, // wrong number
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_mismatch"`)
}

func TestDelete_AcceptsCorrectConfirmAndSoftDeletes(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"changed":true`)
	assert.Contains(t, string(resp.body), `"issue.soft_deleted"`)

	// show without include_deleted now 404s.
	respShow, bs := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1")
	require.Equal(t, 404, respShow.StatusCode, string(bs))
}

func TestDelete_AlreadyDeletedIsNoOp(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})
	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"}))

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"changed":false`)
	assert.Contains(t, string(resp.body), `"event":null`)
}

func TestRestore_ClearsDeletedAt(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})
	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"}))

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/restore",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":true`)
	assert.Contains(t, string(bs), `"issue.restored"`)

	// show without include_deleted works again.
	respShow, bsShow := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1")
	require.Equal(t, 200, respShow.StatusCode, string(bsShow))
}

func TestPurge_RequiresConfirmHeaderAndRemovesAllRows(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "purge me"})
	issue, err := h.DB().IssueByNumber(t.Context(), pid, 1)
	require.NoError(t, err)
	project, err := h.DB().ProjectByID(t.Context(), pid)
	require.NoError(t, err)

	// Missing header → 412.
	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		nil, map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_required"`)

	// Wrong header → 412 confirm_mismatch.
	resp = postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_mismatch"`)

	// Correct header → 200 with purge_log.
	resp = postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		map[string]string{"X-Kata-Confirm": "PURGE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"purge_log"`)
	assert.Contains(t, string(resp.body), `"purged_issue_id"`)
	assert.Contains(t, string(resp.body), `"issue_uid":"`+issue.UID+`"`)
	assert.Contains(t, string(resp.body), `"project_uid":"`+project.UID+`"`)

	// Subsequent show 404s — issue is gone.
	respShow, _ := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1?include_deleted=true")
	assert.Equal(t, 404, respShow.StatusCode)
}

// TestDelete_UnknownIssueIs404 covers the handler-level translation of
// db.ErrNotFound into the 404 issue_not_found wire envelope. The DB layer
// has its own no-op tests; this pins the handler edge.
func TestDelete_UnknownIssueIs404(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/9999/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #9999"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 404, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"issue_not_found"`)
}

// TestRestore_UnknownIssueIs404 mirrors TestDelete_UnknownIssueIs404 for the
// restore route (no confirm header gate, but the lookup still 404s).
func TestRestore_UnknownIssueIs404(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues/9999/actions/restore",
		map[string]any{"actor": "agent"})
	require.Equal(t, 404, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"issue_not_found"`)
}

// TestPurge_UnknownIssueIs404 mirrors the delete/restore 404 pin for purge.
func TestPurge_UnknownIssueIs404(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/9999/actions/purge",
		map[string]string{"X-Kata-Confirm": "PURGE #9999"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 404, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"issue_not_found"`)
}
