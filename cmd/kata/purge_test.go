package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestPurge_NoForceIsValidationError(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "vaporize")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--workspace", dir, "purge", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
}

func TestPurge_ForceWithConfirmRemovesEverything(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "vaporize")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "purge", "1", "--force", "--confirm", "PURGE #1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "purged")
}
