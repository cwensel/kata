package daemon_test

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchEndpoint_ReturnsHitsWithScores(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash on Safari"})
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "unrelated"})

	bs := getBody(t, ts, "/api/v1/projects/"+pidStr+"/search?q="+url.QueryEscape("login Safari"))
	assert.Contains(t, bs, `"query":"login Safari"`)
	assert.Contains(t, bs, `"title":"fix login crash on Safari"`)
	assert.Contains(t, bs, `"matched_in"`)
	assert.NotContains(t, bs, `"title":"unrelated"`,
		"unrelated issue should not appear in results")
}

func TestSearchEndpoint_EmptyQueryIsValidationError(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp, bs := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/search?q=")
	require.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"validation"`)
}

func TestSearchEndpoint_UnknownProjectIs404(t *testing.T) {
	h, _ := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	resp, bs := getStatusBody(t, ts, "/api/v1/projects/9999/search?q=anything")
	require.Equal(t, 404, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"project_not_found"`)
}
