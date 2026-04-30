package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestEvents_OneShotPlainOutput(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "first")
	createIssueViaHTTP(t, env, dir, "second")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "events"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	out := buf.String()
	assert.Contains(t, out, "issue.created")
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	assert.Equal(t, 2, lines)
}

func TestEvents_OneShotJSON(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "only")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "events", "--json"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	var b struct {
		KataAPIVersion int `json:"kata_api_version"`
		Events         []struct {
			EventID int64  `json:"event_id"`
			Type    string `json:"type"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &b))
	assert.Equal(t, 1, b.KataAPIVersion)
	require.Len(t, b.Events, 1)
	assert.Equal(t, "issue.created", b.Events[0].Type)
	assert.Equal(t, int64(1), b.NextAfterID)
}

func TestEvents_OneShotAllProjectsHitsCrossProject(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dirA := initBoundWorkspace(t, env.URL, "https://github.com/wesm/a.git")
	dirB := initBoundWorkspace(t, env.URL, "https://github.com/wesm/b.git")
	createIssueViaHTTP(t, env, dirA, "a-issue")
	createIssueViaHTTP(t, env, dirB, "b-issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"events", "--all-projects", "--json"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	var b struct {
		Events []struct {
			ProjectID int64 `json:"project_id"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &b))
	assert.Len(t, b.Events, 2, "all-projects must include both projects")
}
