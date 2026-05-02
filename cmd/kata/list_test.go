package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestList_DefaultsToOpenIssuesInProject(t *testing.T) {
	resetFlags(t)
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

// TestList_SanitizesAnsiAndNewlinesInTitle covers hammer-test
// finding #2: a malicious title containing ANSI escape sequences or
// embedded newlines must not reach stdout raw, where it could clear
// the screen, set the window title, or break row layout. Sanitized
// at the human-output boundary; the JSON path is exempt (agents need
// the raw bytes).
func TestList_SanitizesAnsiAndNewlinesInTitle(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)

	hostile := "evil\x1b[2Jtitle\nwith newline"
	body, err := json.Marshal(map[string]string{"actor": "x", "title": hostile})
	require.NoError(t, err)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues", //nolint:gosec,noctx
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	_ = resp.Body.Close()

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "list"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.NotContains(t, out, "\x1b", "ESC reached stdout")
	// The newline in the title must be escaped (\n literal) so the
	// list row stays on one visual line.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, ln := range lines {
		assert.NotEmpty(t, ln, "list output produced a blank row from injected newline")
	}
}
