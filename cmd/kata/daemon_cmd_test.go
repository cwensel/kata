package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestDaemonStatus_NoDaemonReportsAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newDaemonCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "no daemon")
}

func TestDaemonStatus_JSONReportsDaemonsWithVersion(t *testing.T) {
	resetFlags(t)
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	require.NoError(t, ns.EnsureDirs())
	started := time.Date(2026, 5, 4, 1, 2, 3, 0, time.UTC)
	_, err = daemon.WriteRuntimeFile(ns.DataDir, daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   "unix:///tmp/kata-test.sock",
		DBPath:    filepath.Join(tmp, "kata.db"),
		Version:   "v-test-status",
		StartedAt: started,
	})
	require.NoError(t, err)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"daemon", "status", "--json"})
	require.NoError(t, cmd.Execute())

	var got struct {
		KataAPIVersion int `json:"kata_api_version"`
		Daemons        []struct {
			PID       int    `json:"pid"`
			Version   string `json:"version"`
			Address   string `json:"address"`
			DBPath    string `json:"db_path"`
			StartedAt string `json:"started_at"`
		} `json:"daemons"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, 1, got.KataAPIVersion)
	require.Len(t, got.Daemons, 1)
	assert.Equal(t, os.Getpid(), got.Daemons[0].PID)
	assert.Equal(t, "v-test-status", got.Daemons[0].Version)
	assert.Equal(t, "unix:///tmp/kata-test.sock", got.Daemons[0].Address)
	assert.Equal(t, filepath.Join(tmp, "kata.db"), got.Daemons[0].DBPath)
	assert.Equal(t, started.Format(time.RFC3339), got.Daemons[0].StartedAt)
}

func TestDaemonStatus_JSONReportsEmptyDaemonList(t *testing.T) {
	resetFlags(t)
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"daemon", "status", "--json"})
	require.NoError(t, cmd.Execute())

	var got struct {
		KataAPIVersion int             `json:"kata_api_version"`
		Daemons        json.RawMessage `json:"daemons"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, 1, got.KataAPIVersion)
	assert.JSONEq(t, "[]", string(got.Daemons))
}

func TestDaemonStart_ListenFlagRejectsPublicAddress(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "start", "--listen", "8.8.8.8:7777"})

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestDaemonStart_ListenFlagRejectsMalformed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "start", "--listen", "not-a-host-port"})

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--listen")
}

func TestEnsureDaemon_ReturnsExistingURL(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	addr, cleanup := pipeServer(t)
	t.Cleanup(cleanup)
	require.NoError(t, writeRuntimeFor(tmp, addr))

	url, err := ensureDaemon(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "http://"))
}
