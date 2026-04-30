package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
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

	body := []byte(`{"start_path":"` + dir + `"}`)
	resp, err := http.Post(baseURL+"/api/v1/projects", "application/json", bytes.NewReader(body)) //nolint:gosec,noctx // test-only
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	return dir
}
