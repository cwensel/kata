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

func TestComment_AppendsToIssue(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	body := []byte(`{"actor":"x","title":"x"}`)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues", "application/json", bytes.NewReader(body)) //nolint:noctx // test-only
	require.NoError(t, err)
	_ = resp.Body.Close()

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "comment", "1", "--body", "looks good"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "looks good") || strings.Contains(buf.String(), "comment"))
}
