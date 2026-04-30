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
	createIssue(t, env, pid, "parent-issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--workspace", dir, "create", "child",
		"--label", "bug", "--label", "needs-review",
		"--parent", "1",
		"--owner", "alice",
	})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "child")
}
