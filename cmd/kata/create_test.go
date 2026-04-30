package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCreate_PrintsIssueNumberInQuietMode(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "--quiet", "create", "first issue", "--body", "details"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "1", strings.TrimSpace(buf.String()))
}

func TestCreate_WithInitialLabelsAndParent(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent-issue") // #1
	createIssue(t, env, pid, "blocker")      // #2

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--workspace", dir, "create", "child",
		"--label", "bug", "--label", "needs-review",
		"--parent", "1",
		"--blocks", "2",
		"--owner", "alice",
	})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "child")

	// Fetch the created issue (#3) and assert every initial-state flag was
	// actually persisted, not just echoed back in the create response.
	resp, err := http.Get(env.URL + "/api/v1/projects/" + itoa(pid) + "/issues/3") //nolint:noctx,gosec // test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Issue struct {
			Number int64   `json:"number"`
			Owner  *string `json:"owner"`
		} `json:"issue"`
		Labels []struct {
			Label string `json:"label"`
		} `json:"labels"`
		Links []struct {
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"links"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.NotNil(t, b.Issue.Owner)
	assert.Equal(t, "alice", *b.Issue.Owner)

	gotLabels := make([]string, 0, len(b.Labels))
	for _, l := range b.Labels {
		gotLabels = append(gotLabels, l.Label)
	}
	assert.ElementsMatch(t, []string{"bug", "needs-review"}, gotLabels)

	var sawParent, sawBlocks bool
	for _, l := range b.Links {
		switch l.Type {
		case "parent":
			if l.FromNumber == 3 && l.ToNumber == 1 {
				sawParent = true
			}
		case "blocks":
			if l.FromNumber == 3 && l.ToNumber == 2 {
				sawBlocks = true
			}
		}
	}
	assert.True(t, sawParent, "parent link from #3 to #1 must be persisted")
	assert.True(t, sawBlocks, "blocks link from #3 to #2 must be persisted")
}
