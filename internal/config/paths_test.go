package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestKataHome_PrefersEnvOverDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, tmp, got)
}

func TestKataHome_DefaultsToUserHomeDotKata(t *testing.T) {
	t.Setenv("KATA_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".kata"), got)
}

func TestKataDB_PrefersEnvOverHomeJoin(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "custom.db"))

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmp, "custom.db"), got)
}

func TestKataDB_DefaultsToHomeKataDB(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", "")

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmp, "kata.db"), got)
}

func TestDBHash_StableTwelveLowerHex(t *testing.T) {
	a := config.DBHash("/Users/foo/.kata/kata.db")
	b := config.DBHash("/Users/foo/.kata/kata.db")
	c := config.DBHash("/Users/foo/.kata/other.db")

	assert.Len(t, a, 12)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, strings.ToLower(a), a)
}

func TestRuntimeDir_NamespaceIsDBHashUnderHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	got, err := config.RuntimeDir()
	require.NoError(t, err)
	hash := config.DBHash(filepath.Join(tmp, "kata.db"))
	assert.Equal(t, filepath.Join(tmp, "runtime", hash), got)
}

func TestHookConfigPath_HonorsKataHome(t *testing.T) {
	t.Setenv("KATA_HOME", "/tmp/kata-test")
	got, err := config.HookConfigPath()
	if err != nil {
		t.Fatalf("HookConfigPath: %v", err)
	}
	want := filepath.Join("/tmp/kata-test", "hooks.toml")
	if got != want {
		t.Fatalf("HookConfigPath = %q, want %q", got, want)
	}
}

func TestHookRootDir_NamespacedByDBHash(t *testing.T) {
	t.Setenv("KATA_HOME", "/tmp/kata-test")
	got, err := config.HookRootDir("abc123def456")
	if err != nil {
		t.Fatalf("HookRootDir: %v", err)
	}
	want := filepath.Join("/tmp/kata-test", "hooks", "abc123def456")
	if got != want {
		t.Fatalf("HookRootDir = %q, want %q", got, want)
	}
}

func TestHookOutputDir_UnderHookRoot(t *testing.T) {
	t.Setenv("KATA_HOME", "/tmp/kata-test")
	got, err := config.HookOutputDir("abc123def456")
	if err != nil {
		t.Fatalf("HookOutputDir: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("hooks", "abc123def456", "output")) {
		t.Fatalf("HookOutputDir = %q, want suffix hooks/abc123def456/output", got)
	}
}

func TestHookRunsPath_UnderHookRoot(t *testing.T) {
	t.Setenv("KATA_HOME", "/tmp/kata-test")
	got, err := config.HookRunsPath("abc123def456")
	if err != nil {
		t.Fatalf("HookRunsPath: %v", err)
	}
	want := filepath.Join("/tmp/kata-test", "hooks", "abc123def456", "runs.jsonl")
	if got != want {
		t.Fatalf("HookRunsPath = %q, want %q", got, want)
	}
}
