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

func TestAssign_RoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "x")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "assign", "1", "alice"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "assigned") ||
		strings.Contains(buf.String(), "alice"))

	resetFlags(t)
	uCmd := newRootCmd()
	var ubuf bytes.Buffer
	uCmd.SetOut(&ubuf)
	uCmd.SetArgs([]string{"--workspace", dir, "unassign", "1"})
	uCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, uCmd.Execute())
	assert.True(t, strings.Contains(ubuf.String(), "unassigned"))
}
