package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
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
	createIssue(t, env, pid, "parent") // #1
	createIssue(t, env, pid, "child")  // #2
	// Two labels so we exercise the comma-join.
	for _, label := range []string{"bug", "priority:high"} {
		body := []byte(`{"actor":"tester","label":"` + label + `"}`)
		resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues/2/labels",
			"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "show", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	// Exact section headers and comma-joined label rendering.
	assert.Contains(t, out, "--- labels ---")
	assert.Contains(t, out, "bug, priority:high")
	// Links section: child is the link's "from" side, so the arrow points
	// outward (→) toward parent #1.
	assert.Contains(t, out, "--- links ---")
	assert.Contains(t, out, "parent → #1")
}

// TestShow_LinkArrowReversesOnToSide verifies that when show runs against
// the link's "to" side, the rendered arrow flips (←) so the line still reads
// from the perspective of the issue being shown.
func TestShow_LinkArrowReversesOnToSide(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent") // #1
	createIssue(t, env, pid, "child")  // #2
	// child → parent stores (from=2, to=1). Showing #1 puts us on the to side.
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "show", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "parent ← #2", "to-side show must reverse the arrow")
}

func TestShow_AcceptsHashFullUIDAndUniquePrefix(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "uid target")
	issue, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)

	for _, ref := range []string{"#1", issue.UID, issue.UID[:12]} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{"--workspace", dir, "show", ref})
		cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
		require.NoError(t, cmd.Execute(), "ref %s", ref)
		assert.Contains(t, buf.String(), "uid target")
	}
}

func TestShow_UIDPrefixErrors(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")
	uidA := "01JZ0000000000000000000001"
	uidB := "01JZ0000000000000000000002"
	_, err := env.DB.ExecContext(context.Background(), `DROP TRIGGER trg_issues_uid_immutable`)
	require.NoError(t, err)
	_, err = env.DB.ExecContext(context.Background(), `UPDATE issues SET uid = ? WHERE project_id = ? AND number = 1`, uidA, pid)
	require.NoError(t, err)
	_, err = env.DB.ExecContext(context.Background(), `UPDATE issues SET uid = ? WHERE project_id = ? AND number = 2`, uidB, pid)
	require.NoError(t, err)
	first, err := env.DB.IssueByNumber(context.Background(), pid, 1)
	require.NoError(t, err)

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--workspace", dir, "show", first.UID[:4]})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err = cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, "prefix_too_short", ce.Code)

	resetFlags(t)
	cmd = newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--workspace", dir, "show", first.UID[:8]})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err = cmd.Execute()
	require.Error(t, err)
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, "prefix_ambiguous", ce.Code)
	assert.Contains(t, ce.Message, first.UID)
}
