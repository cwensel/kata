package daemonclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func writeRuntimeRecord(t *testing.T, home, addr string) error {
	t.Helper()
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if err := ns.EnsureDirs(); err != nil {
		return err
	}
	_, err = daemon.WriteRuntimeFile(ns.DataDir, daemon.RuntimeRecord{
		PID:       os.Getpid(),
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
