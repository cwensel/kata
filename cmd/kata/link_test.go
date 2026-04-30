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

func TestLink_GenericRoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "link", "1", "blocks", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "linked") || strings.Contains(out, "blocks"))
}

func TestParent_WithReplace(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "p1")
	createIssue(t, env, pid, "p2")

	cmd1 := newRootCmd()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetArgs([]string{"--workspace", dir, "parent", "1", "2"})
	cmd1.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd1.Execute())

	resetFlags(t)
	cmd2 := newRootCmd()
	var buf bytes.Buffer
	cmd2.SetOut(&buf)
	cmd2.SetArgs([]string{"--workspace", dir, "parent", "1", "3", "--replace"})
	cmd2.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd2.Execute())
	assert.True(t, strings.Contains(buf.String(), "linked") ||
		strings.Contains(buf.String(), "parent"))
}

func TestUnlink_RemovesLink(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	createLinkViaHTTP(t, env, pid, 1, "blocks", 2)

	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "unlink", "1", "blocks", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "unlinked") ||
		strings.Contains(buf.String(), "removed"))
}

func TestUnparent_RemovesParentLink(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "p")

	createLinkViaHTTP(t, env, pid, 1, "parent", 2)

	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "unparent", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "unlinked") ||
		strings.Contains(buf.String(), "removed"))
}

// createLinkViaHTTP is a thin test helper duplicated in link_test.go and
// label_test.go because cmd/kata is a separate package from internal/daemon.
func createLinkViaHTTP(t *testing.T, env *testenv.Env, projectID, fromNumber int64, linkType string, toNumber int64) {
	t.Helper()
	body := []byte(`{"actor":"tester","type":"` + linkType + `","to_number":` + itoa(toNumber) + `}`)
	resp, err := http.Post(
		env.URL+"/api/v1/projects/"+itoa(projectID)+
			"/issues/"+itoa(fromNumber)+"/links",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
}

// createIssue is a test helper that creates an issue via HTTP and discards
// the response. Use when you need an issue but not its number (numbers go
// 1, 2, 3 in order so you can hard-code them).
func createIssue(t *testing.T, env *testenv.Env, projectID int64, title string) {
	t.Helper()
	body := []byte(`{"actor":"tester","title":"` + title + `"}`)
	resp, err := http.Post(
		env.URL+"/api/v1/projects/"+itoa(projectID)+"/issues",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only loopback
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
