package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
