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

func TestShow_RendersLabelsAndLinksSections(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent")
	createIssue(t, env, pid, "child")
	body := []byte(`{"actor":"tester","label":"bug"}`)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues/2/labels",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "show", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "labels"), "expected labels section")
	assert.Contains(t, out, "bug")
	assert.True(t, strings.Contains(out, "links"), "expected links section")
	assert.Contains(t, out, "parent")
}
