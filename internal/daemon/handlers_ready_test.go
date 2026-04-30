package daemon_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestReady_FiltersBlocked(t *testing.T) {
	env := testenv.New(t)
	pid, blocker, blocked := setupTwoIssues(t, env)
	standalone := createIssueViaHTTP(t, env, pid, "standalone")
	postLink(t, env, pid, blocker, "blocks", blocked)

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/ready")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			Number int64 `json:"number"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	got := map[int64]bool{}
	for _, i := range out.Issues {
		got[i.Number] = true
	}
	assert.True(t, got[blocker], "blocker is ready")
	assert.True(t, got[standalone], "standalone is ready")
	assert.False(t, got[blocked], "blocked while blocker is open")
}

func TestReady_RespectsLimit(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	for i := 0; i < 3; i++ {
		createIssueViaHTTP(t, env, pid, "x")
	}

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/ready?limit=2")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct {
			Number int64 `json:"number"`
		} `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out.Issues, 2)
}
