package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCloseReopen_RoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues",
		"application/json", bytes.NewReader([]byte(`{"actor":"x","title":"x"}`))) //nolint:noctx // test-only
	require.NoError(t, err)
	_ = resp.Body.Close()

	closeCmd := newRootCmd()
	var bclose bytes.Buffer
	closeCmd.SetOut(&bclose)
	closeCmd.SetArgs([]string{"--workspace", dir, "close", "1", "--reason", "wontfix"})
	closeCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, closeCmd.Execute())
	assert.True(t, strings.Contains(bclose.String(), "closed"))

	reopenCmd := newRootCmd()
	var bo bytes.Buffer
	reopenCmd.SetOut(&bo)
	reopenCmd.SetArgs([]string{"--workspace", dir, "reopen", "1"})
	reopenCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, reopenCmd.Execute())
	assert.True(t, strings.Contains(bo.String(), "open"))
}
