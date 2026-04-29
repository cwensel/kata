package daemon_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
)

func TestNamespace_DataDirIsKataHomeRuntime(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	hash := config.DBHash(filepath.Join(tmp, "kata.db"))
	assert.Equal(t, filepath.Join(tmp, "runtime", hash), ns.DataDir)
	assert.Equal(t, hash, ns.DBHash)
}

func TestNamespace_SocketDirHonorsXDGRuntimeDir(t *testing.T) {
	tmp := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "kata", ns.DBHash), ns.SocketDir)
}

func TestNamespace_SocketDirFallsBackToTmpDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "/var/folders/xy")

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	assert.Contains(t, ns.SocketDir, "kata-")
	assert.Contains(t, ns.SocketDir, ns.DBHash)
}
