package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"testing"

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
