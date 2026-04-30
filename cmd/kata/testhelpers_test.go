package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/testenv"
)

// pipeServer starts a TCP listener on a random loopback port, registers
// GET /api/v1/ping, and returns the host:port address and a cleanup function.
func pipeServer(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pipeServer: listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	go func() { _ = http.Serve(l, mux) }() //nolint:gosec // test-only, loopback only
	return l.Addr().String(), func() { _ = l.Close() }
}

// writeRuntimeFor writes a daemon.<pid>.json inside the namespace DataDir that
// resolves from the given KATA_HOME (tmp). The test must have already called
// t.Setenv("KATA_HOME", tmp) and t.Setenv("KATA_DB", ...) before this.
func writeRuntimeFor(home, addr string) error {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if err := ns.EnsureDirs(); err != nil {
		return err
	}
	rec := daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   addr,
		DBPath:    home + "/kata.db",
		StartedAt: time.Now().UTC(),
	}
	_, err = daemon.WriteRuntimeFile(ns.DataDir, rec)
	return err
}

// contextWithBaseURL injects a daemon base URL into the context so CLI
// commands bypass real daemon discovery during tests.
func contextWithBaseURL(ctx context.Context, url string) context.Context {
	return context.WithValue(ctx, baseURLKey{}, url)
}

// initBoundWorkspace creates a temporary git workspace, adds a git remote, and
// registers it with the test daemon via POST /api/v1/projects. Returns the
// workspace directory path.
func initBoundWorkspace(t *testing.T, baseURL, origin string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "remote", "add", "origin", origin) //nolint:gosec // G204: git with test-controlled origin
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	body, err := json.Marshal(map[string]string{"start_path": dir})
	require.NoError(t, err)
	resp, err := http.Post(baseURL+"/api/v1/projects", "application/json", bytes.NewReader(body)) //nolint:gosec,noctx // test-only
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	return dir
}

// resolvePIDViaHTTP calls POST /api/v1/projects/resolve with start_path and
// returns the resolved project ID.
func resolvePIDViaHTTP(t *testing.T, baseURL, startPath string) int64 {
	t.Helper()
	body, err := json.Marshal(map[string]string{"start_path": startPath})
	require.NoError(t, err)
	resp, err := http.Post(baseURL+"/api/v1/projects/resolve", "application/json", bytes.NewReader(body)) //nolint:gosec,noctx // test-only
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	return b.Project.ID
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// createIssueViaHTTP creates an issue in dir's project via the testenv daemon.
// Returns the issue number from the response. Reused across destructive-ladder
// tests so each test doesn't have to resolve the project ID itself.
func createIssueViaHTTP(t *testing.T, env *testenv.Env, dir, title string) int64 {
	t.Helper()
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	body, err := json.Marshal(map[string]any{"actor": "tester", "title": title})
	require.NoError(t, err)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues",
		"application/json", bytes.NewReader(body)) //nolint:gosec,noctx // test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Issue struct {
			Number int64 `json:"number"`
		} `json:"issue"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	return b.Issue.Number
}

// resetFlags restores global flag state for cobra tests. Use t.Cleanup so
// LIFO ordering plays nicely with other cleanups.
func resetFlags(t *testing.T) {
	t.Helper()
	saved := flags
	flags = globalFlags{}
	t.Cleanup(func() { flags = saved })
}
