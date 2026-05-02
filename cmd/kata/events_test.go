package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestEvents_NegativeAfterRejected(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"events", "--all-projects", "--after=-1"})
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitUsage, ce.ExitCode)
	assert.Contains(t, ce.Message, "non-negative")
}

func TestEvents_NegativeLastEventIDRejected(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"events", "--all-projects", "--tail", "--last-event-id=-1"})
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitUsage, ce.ExitCode)
	assert.Contains(t, ce.Message, "non-negative")
}

// TestEvents_TailFailsFastOn4xx pins the spec §7.2 rule: HTTP 4xx responses
// are terminal, not retryable. A bad cursor or unknown project must surface
// to the caller, not spin in the reconnect loop.
func TestEvents_TailFailsFastOn4xx(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	cmd := newRootCmd()
	buf := &safeBuffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	ctx, cancel := context.WithTimeout(contextWithBaseURL(context.Background(), env.URL), 5*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"events", "--project-id", "99999", "--tail"})
	cmd.SetContext(ctx)
	err := cmd.Execute()
	require.Error(t, err, "tail must surface 404 instead of looping")
	assert.Contains(t, err.Error(), "404")
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

// TestEvents_TailRejectsOneShotFlags covers hammer-test finding #6:
// --tail with --limit or --after used to be silently accepted, even
// though those flags are documented as one-shot mode. --limit 1
// still streamed indefinitely. Now both reject as kindUsage.
func TestEvents_TailRejectsOneShotFlags(t *testing.T) {
	for _, args := range [][]string{
		{"events", "--tail", "--limit", "1"},
		{"events", "--tail", "--after", "5"},
	} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		cmd.SetContext(context.Background())

		err := cmd.Execute()
		require.Errorf(t, err, "args %v should reject", args)
		var ce *cliError
		require.True(t, errors.As(err, &ce), "expected *cliError, got %T", err)
		assert.Equalf(t, ExitUsage, ce.ExitCode, "args %v: wrong exit code", args)
		assert.Equalf(t, kindUsage, ce.Kind, "args %v: wrong kind", args)
	}
}

// TestEvents_OneShotRejectsTailFlag mirrors the symmetric case:
// --last-event-id is documented as --tail-only, so passing it without
// --tail should reject loudly instead of being silently ignored.
func TestEvents_OneShotRejectsTailFlag(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"events", "--last-event-id", "5"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, ExitUsage, ce.ExitCode)
}

// TestEvents_OneShotRejectsNonPositiveLimit: parallel to list/ready,
// --limit 0/-1 in one-shot mode rejects with kindValidation. Search
// has the same check after hammer-test #5.
func TestEvents_OneShotRejectsNonPositiveLimit(t *testing.T) {
	for _, lim := range []string{"0", "-1"} {
		resetFlags(t)
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"events", "--limit", lim})
		cmd.SetContext(context.Background())

		err := cmd.Execute()
		require.Errorf(t, err, "--limit %s should reject", lim)
		var ce *cliError
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, ExitValidation, ce.ExitCode)
	}
}
