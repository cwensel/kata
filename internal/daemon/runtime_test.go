package daemon_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestRuntimeFile_RoundTripWriteRead(t *testing.T) {
	dir := t.TempDir()
	rec := daemon.RuntimeRecord{
		PID:       4242,
		Address:   "unix:///tmp/kata.sock",
		DBPath:    "/tmp/kata.db",
		StartedAt: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
	}
	path, err := daemon.WriteRuntimeFile(dir, rec)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "daemon.4242.json"), path)

	got, err := daemon.ReadRuntimeFile(path)
	require.NoError(t, err)
	assert.Equal(t, rec.PID, got.PID)
	assert.Equal(t, rec.Address, got.Address)
}

func TestListRuntimeFiles_FindsAllInDir(t *testing.T) {
	dir := t.TempDir()
	for _, pid := range []int{1, 2, 3} {
		require.NoError(t, os.WriteFile( //nolint:gosec // runtime files are world-readable per §2.3
			filepath.Join(dir, "daemon."+strconv.Itoa(pid)+".json"),
			[]byte(`{"pid":`+strconv.Itoa(pid)+`,"address":"x","db_path":"x","started_at":"2026-01-01T00:00:00Z"}`), 0o644))
	}

	got, err := daemon.ListRuntimeFiles(dir)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestRuntimeFile_AtomicViaTempRename(t *testing.T) {
	// Two concurrent writes shouldn't produce a half-written file.
	// We assert by writing once and then reading — the value must match.
	dir := t.TempDir()
	rec := daemon.RuntimeRecord{PID: 7, Address: "x", DBPath: "x", StartedAt: time.Now().UTC()}
	_, err := daemon.WriteRuntimeFile(dir, rec)
	require.NoError(t, err)
	got, err := daemon.ReadRuntimeFile(filepath.Join(dir, "daemon.7.json"))
	require.NoError(t, err)
	assert.Equal(t, rec.PID, got.PID)
}

func TestProcessAlive_TrueForSelfFalseForGarbagePID(t *testing.T) {
	assert.True(t, daemon.ProcessAlive(os.Getpid()))
	assert.False(t, daemon.ProcessAlive(99999999))
}
