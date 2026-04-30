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
