package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

// safeBuffer is a mutex-protected bytes.Buffer used by tail tests so that
// `go test -race` does not flag the goroutine running cmd.Execute writing to
// the buffer racing with the test goroutine reading it via Snapshot.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestEvents_TailEmitsNDJSON(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	cmd := newRootCmd()
	buf := &safeBuffer{}
	cmd.SetOut(buf)
	ctx, cancel := context.WithTimeout(contextWithBaseURL(context.Background(), env.URL), 5*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"--workspace", dir, "events", "--tail"})
	cmd.SetContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cmd.Execute()
	}()

	time.Sleep(200 * time.Millisecond)
	createIssueViaHTTP(t, env, dir, "first")
	createIssueViaHTTP(t, env, dir, "second")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(buf.Snapshot(), "issue.created") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	out := buf.Snapshot()
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	require.GreaterOrEqual(t, len(lines), 2, "expected at least 2 NDJSON lines, got: %q", out)
	for _, l := range lines[:2] {
		var env map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &env), "each line must be a JSON object")
		assert.Equal(t, "issue.created", env["type"])
	}
}

func TestEvents_TailFollowsResetRequired(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "doomed")

	cmd := newRootCmd()
	buf := &safeBuffer{}
	cmd.SetOut(buf)
	ctx, cancel := context.WithTimeout(contextWithBaseURL(context.Background(), env.URL), 5*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"--workspace", dir, "events", "--tail"})
	cmd.SetContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cmd.Execute()
	}()

	time.Sleep(300 * time.Millisecond)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	purgeURL := env.URL + "/api/v1/projects/" + itoa(pid) + "/issues/1/actions/purge"
	body := strings.NewReader(`{"actor":"tester"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, purgeURL, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kata-Confirm", "PURGE #1")
	resp, err := env.HTTP.Do(req) //nolint:gosec // G704: test server URL, not user-controlled
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.Snapshot(), `"reset_required":true`) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	assert.Contains(t, buf.Snapshot(), `"reset_required":true`,
		"--tail must emit a reset envelope when the daemon sends sync.reset_required")
}
