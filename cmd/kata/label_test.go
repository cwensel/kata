package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestLabelAdd_HappyPath(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "needs-review"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "needs-review")
}

func TestLabelRm_HappyPath(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")

	addCmd := newRootCmd()
	addCmd.SetOut(&bytes.Buffer{})
	addCmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "bug"})
	addCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, addCmd.Execute())

	resetFlags(t)
	rmCmd := newRootCmd()
	var buf bytes.Buffer
	rmCmd.SetOut(&buf)
	rmCmd.SetArgs([]string{"--workspace", dir, "label", "rm", "1", "bug"})
	rmCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, rmCmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "removed") ||
		strings.Contains(buf.String(), "unlabeled"))
}

func TestLabelsList_PrintsCounts(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	addCmd := newRootCmd()
	addCmd.SetOut(&bytes.Buffer{})
	addCmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "bug"})
	addCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, addCmd.Execute())

	resetFlags(t)
	listCmd := newRootCmd()
	var buf bytes.Buffer
	listCmd.SetOut(&buf)
	listCmd.SetArgs([]string{"--workspace", dir, "labels"})
	listCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, listCmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "bug")
	assert.Contains(t, out, "1")
}

// TestLabel_RejectsEmptyLabel covers hammer-test finding #8: label
// rm 1 "" used to URL-encode to "" and hit /labels/?actor=... which
// the daemon answered with a raw 404 page. label add 1 "" was already
// validated in some daemon path but the messaging was inconsistent.
// Now both reject client-side with a uniform validation message.
func TestLabel_RejectsEmptyLabel(t *testing.T) {
	for _, args := range [][]string{
		{"label", "add", "1", ""},
		{"label", "rm", "1", "  "},
	} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		cmd.SetContext(context.Background())

		err := cmd.Execute()
		require.Errorf(t, err, "args %v should reject", args)
		var ce *cliError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, ExitValidation, ce.ExitCode)
		assert.Contains(t, ce.Message, "label must not be empty")
	}
}

// TestCreate_RejectsWhitespaceLabel covers the create --label case
// from hammer #8. Pflag's StringSliceVar drops a literal empty
// argument (""), but a whitespace-only label like "   " makes it
// through and used to be silently dropped by the daemon. Reject
// client-side instead.
func TestCreate_RejectsWhitespaceLabel(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"create", "title", "--label", "   "})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
	assert.Contains(t, ce.Message, "label must not be empty")
}
