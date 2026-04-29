package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestDiscoverPaths_FindsKataTomlAndGit(t *testing.T) {
	root := t.TempDir()
	//nolint:gosec // test fixture under TempDir; permissive perms are intentional.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	//nolint:gosec // test fixture; mirrors how users commit .kata.toml world-readable.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".kata.toml"), []byte("version = 1\n\n[project]\nidentity = \"x\"\nname = \"x\"\n"), 0o644))
	sub := filepath.Join(root, "a", "b")
	//nolint:gosec // test fixture under TempDir.
	require.NoError(t, os.MkdirAll(sub, 0o755))

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, root, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_KataTomlInSubdirOfGit(t *testing.T) {
	root := t.TempDir()
	//nolint:gosec // test fixture under TempDir.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	sub := filepath.Join(root, "subproject")
	//nolint:gosec // test fixture under TempDir.
	require.NoError(t, os.MkdirAll(sub, 0o755))
	//nolint:gosec // test fixture; mirrors how users commit .kata.toml world-readable.
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".kata.toml"), []byte("version = 1\n\n[project]\nidentity = \"x\"\nname = \"x\"\n"), 0o644))

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, sub, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_NeitherFound(t *testing.T) {
	d, err := config.DiscoverPaths(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, d.WorkspaceRoot)
	assert.Empty(t, d.GitRoot)
}

func TestDiscoverPaths_StartPathMissingErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist", "deeper")
	_, err := config.DiscoverPaths(missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat")
}

func TestNormalizeRemoteURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"https://github.com/wesm/kata", "github.com/wesm/kata"},
		{"https://user:pass@github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"git@github.com:wesm/kata.git", "github.com/wesm/kata"},
		{"ssh://git@gitlab.com/team/repo.git", "gitlab.com/team/repo"},
	}
	for _, tc := range cases {
		got, err := config.NormalizeRemoteURL(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func TestComputeAliasIdentity_GitWithRemote(t *testing.T) {
	dir := initGitRepo(t)
	requireGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", a.Identity)
	assert.Equal(t, "git", a.Kind)
	assert.Equal(t, dir, a.RootPath)
}

func TestComputeAliasIdentity_GitNoRemote(t *testing.T) {
	dir := initGitRepo(t)

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "local://"+dir, a.Identity)
	assert.Equal(t, "local", a.Kind)
}

func TestComputeAliasIdentity_NonGitWorkspace(t *testing.T) {
	ws := t.TempDir()
	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{WorkspaceRoot: ws})
	require.NoError(t, err)
	assert.Equal(t, "local://"+ws, a.Identity)
	assert.Equal(t, "local", a.Kind)
	assert.Equal(t, ws, a.RootPath)
}

func TestComputeAliasIdentity_Neither(t *testing.T) {
	_, err := config.ComputeAliasIdentity(config.DiscoveredPaths{})
	require.Error(t, err)
}

func TestValidateIdentity(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		hint string
	}{
		{"github.com/wesm/kata", true, ""},
		{"local:///abs/path", true, ""},
		{"a_b.c-d:foo/bar", true, ""},
		{"", false, "non-empty"},
		{"  spaces in middle  ", false, "whitespace"},
		{"has space", false, "whitespace"},
		{"https://u:p@host/x", false, "credential"},
	}
	for _, tc := range cases {
		err := config.ValidateIdentity(tc.in)
		if tc.ok {
			assert.NoError(t, err, tc.in)
		} else {
			require.Error(t, err, tc.in)
			assert.Contains(t, err.Error(), tc.hint, tc.in)
		}
	}
}

// helpers

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	requireGit(t, dir, "init", "--quiet")
	requireGit(t, dir, "config", "user.email", "x@example.com")
	requireGit(t, dir, "config", "user.name", "x")
	return dir
}

func requireGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // git binary is fixed; args are test-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}
