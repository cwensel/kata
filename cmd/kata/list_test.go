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

func TestList_DefaultsToOpenIssuesInProject(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	pid := resolvePIDViaHTTP(t, env.URL, dir)
	for _, title := range []string{"alpha", "beta"} {
		body := []byte(`{"actor":"x","title":"` + title + `"}`)
		resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues", "application/json", bytes.NewReader(body)) //nolint:gosec,noctx // test-only
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		_ = resp.Body.Close()
	}

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "list"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "alpha"))
	assert.True(t, strings.Contains(out, "beta"))
}
