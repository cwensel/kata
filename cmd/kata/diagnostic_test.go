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

func TestWhoami_FlagOverride(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"whoami", "--as", "claude-4.7"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "claude-4.7")
	assert.Contains(t, out, "flag")
}

func TestHealth_PrintsSchemaVersion(t *testing.T) {
	env := testenv.New(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"health"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "schema_version=1")
}

func TestProjectsList_PrintsKnown(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	_ = dir

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "list"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "github.com/wesm/kata"))
}
