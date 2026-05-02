package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestSearch_ReturnsMatchedIssues(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "fix login crash on Safari")
	createIssueViaHTTP(t, env, dir, "unrelated issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "search", "login Safari"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "fix login crash on Safari")
	assert.NotContains(t, buf.String(), "unrelated issue")
}

func TestSearch_EmptyQueryIsValidationError(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--workspace", dir, "search", "  "})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
}

// TestSearch_UnquotedMultiTerm verifies that `kata search login Safari`
// (no quotes) joins the args with spaces and matches the same way as the
// quoted form. Required by the BM25 implicit-AND contract.
func TestSearch_UnquotedMultiTerm(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "fix login crash on Safari")
	createIssueViaHTTP(t, env, dir, "unrelated issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "search", "login", "Safari"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "fix login crash on Safari")
	assert.NotContains(t, buf.String(), "unrelated issue")
}

// TestSearch_RejectsNonPositiveLimit covers hammer-test #5: --limit
// 0/-1 used to be silently treated as "no limit" because
// buildSearchURL only set the param when limit > 0. Now mirrors
// list/ready/events/daemon-logs validation.
func TestSearch_RejectsNonPositiveLimit(t *testing.T) {
	for _, lim := range []string{"0", "-1"} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"search", "x", "--limit", lim})
		cmd.SetContext(context.Background())

		err := cmd.Execute()
		require.Errorf(t, err, "--limit %s should reject", lim)
		var ce *cliError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ExitValidation, ce.ExitCode)
	}
}
