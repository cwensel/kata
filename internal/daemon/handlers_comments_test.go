package daemon_test

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommentEndpoint_AppendsAndEmitsEvent(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "first comment"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"body":"first comment"`)
	assert.Contains(t, string(bs), `"type":"issue.commented"`)
}

func TestActionsClose_ReopenRoundtrip(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent", "reason": "wontfix"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"status":"closed"`)
	assert.Contains(t, string(bs), `"closed_reason":"wontfix"`)

	resp2, bs2 := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/reopen",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
	assert.Contains(t, string(bs2), `"status":"open"`)
}

func TestActionsClose_AlreadyClosedIsNoOpEnvelope(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":false`)
	assert.Contains(t, string(bs), `"event":null`)
}
