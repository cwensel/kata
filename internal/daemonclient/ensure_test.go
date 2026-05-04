package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestEnsureRunningRestartsWhenDaemonVersionDiffers(t *testing.T) {
	t.Setenv("KATA_SKIP_DAEMON_VERSION_CHECK", "")
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "kata",
			"version": "old-version",
			"pid":     os.Getpid(),
		})
	}))
	t.Cleanup(server.Close)

	require.NoError(t, writeRuntimeRecord(t, tmp, strings.TrimPrefix(server.URL, "http://")))
	restore := patchEnsureHooks(t, "new-version", "http://new-daemon")
	url, err := EnsureRunning(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "http://new-daemon", url)
	assert.Equal(t, 1, restore.stopCalls)
	assert.Equal(t, 1, restore.startCalls)
}

func TestEnsureRunningRestartsWhenDaemonVersionUnknown(t *testing.T) {
	t.Setenv("KATA_SKIP_DAEMON_VERSION_CHECK", "")
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(server.Close)

	require.NoError(t, writeRuntimeRecord(t, tmp, strings.TrimPrefix(server.URL, "http://")))
	restore := patchEnsureHooks(t, "new-version", "http://new-daemon")
	url, err := EnsureRunning(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "http://new-daemon", url)
	assert.Equal(t, 1, restore.stopCalls)
	assert.Equal(t, 1, restore.startCalls)
}

func TestShouldRefuseAutoStartDaemonFromGoTestBinary(t *testing.T) {
	assert.True(t, shouldRefuseAutoStartDaemon("/tmp/go-build123/b001/kata.test"))
	assert.True(t, shouldRefuseAutoStartDaemon("/var/folders/x/go-build123/b001/kata"))
	assert.False(t, shouldRefuseAutoStartDaemon("/usr/local/bin/kata"))
}

func TestStopRunningDaemonsDoesNotSignalUnverifiedRuntimePID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-waitCh
		}
	})

	require.NoError(t, writeRuntimeRecordForPID(t, tmp, cmd.Process.Pid, "127.0.0.1:1"))
	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	require.NoError(t, stopRunningDaemons(context.Background(), ns.DataDir))

	select {
	case err := <-waitCh:
		t.Fatalf("unverified runtime PID was signaled; process exited with %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopRunningDaemonsRemovesUnverifiableIncompatibleRuntime(t *testing.T) {
	t.Setenv("KATA_SKIP_DAEMON_VERSION_CHECK", "")
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "kata",
			"version": "old-version",
		})
	}))
	t.Cleanup(server.Close)
	require.NoError(t, writeRuntimeRecordForPID(t, tmp, os.Getpid(), strings.TrimPrefix(server.URL, "http://")))
	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	require.NoError(t, stopRunningDaemons(ctx, ns.DataDir))

	_, err = os.Stat(filepath.Join(ns.DataDir, fmt.Sprintf("daemon.%d.json", os.Getpid())))
	assert.True(t, os.IsNotExist(err), "unverifiable incompatible runtime file should be removed")
}

func writeRuntimeRecord(t *testing.T, home, addr string) error {
	t.Helper()
	return writeRuntimeRecordForPID(t, home, os.Getpid(), addr)
}

func writeRuntimeRecordForPID(t *testing.T, home string, pid int, addr string) error {
	t.Helper()
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if err := ns.EnsureDirs(); err != nil {
		return err
	}
	_, err = daemon.WriteRuntimeFile(ns.DataDir, daemon.RuntimeRecord{
		PID:       pid,
		Address:   addr,
		DBPath:    filepath.Join(home, "kata.db"),
		StartedAt: time.Now().UTC(),
	})
	return err
}

type ensurePatchState struct {
	stopCalls  int
	startCalls int
}

func patchEnsureHooks(t *testing.T, version, startedURL string) *ensurePatchState {
	t.Helper()
	state := &ensurePatchState{}
	origCurrent := currentVersionForEnsure
	origStop := stopRunningDaemonsForEnsure
	origStart := startDaemonForEnsure
	currentVersionForEnsure = func() string { return version }
	stopRunningDaemonsForEnsure = func(context.Context, string) error {
		state.stopCalls++
		return nil
	}
	startDaemonForEnsure = func(context.Context, string) (string, error) {
		state.startCalls++
		return startedURL, nil
	}
	t.Cleanup(func() {
		currentVersionForEnsure = origCurrent
		stopRunningDaemonsForEnsure = origStop
		startDaemonForEnsure = origStart
	})
	return state
}
