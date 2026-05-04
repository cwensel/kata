package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestInit_FreshGitRepoBindsViaRemote(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	ctx := context.Background()
	out, err := callInit(ctx, env.URL, dir, callInitOpts{})
	require.NoError(t, err)
	assert.Contains(t, out, `"identity":"github.com/wesm/kata"`)
	assert.FileExists(t, filepath.Join(dir, ".kata.toml"))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // git binary is trusted; args are test-controlled
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func TestInit_AddsLocalToGitignoreWhenAbsent(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), ".kata.local.toml")
}

func TestInit_GitignoreIsIdempotent(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("node_modules/\n.kata.local.toml\n"), 0o644))

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	// Exactly one occurrence — no duplication on re-run.
	assert.Equal(t, 1, strings.Count(string(content), ".kata.local.toml"))
	assert.Contains(t, string(content), "node_modules/")
}

// TestInit_GitignoreLandsAtWorkspaceRoot exercises the nested-init case:
// when `kata init` runs from a subdirectory of the git workspace, the
// daemon writes .kata.toml at the git root and reports that root in
// workspace_root. The CLI must place .gitignore beside .kata.toml at
// the workspace root, not at the cwd subdirectory.
func TestInit_GitignoreLandsAtWorkspaceRoot(t *testing.T) {
	env := testenv.New(t)
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	sub := filepath.Join(root, "internal", "tui")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, sub, callInitOpts{})
	require.NoError(t, err)

	// .kata.toml is written by the daemon at the git root, not the subdir.
	assert.FileExists(t, filepath.Join(root, ".kata.toml"))
	assert.NoFileExists(t, filepath.Join(sub, ".kata.toml"))

	// .gitignore must follow .kata.toml — at the git root.
	rootIgnore := filepath.Join(root, ".gitignore")
	assert.FileExists(t, rootIgnore)
	content, err := os.ReadFile(rootIgnore)
	require.NoError(t, err)
	assert.Contains(t, string(content), ".kata.local.toml")

	// And nothing was written in the subdir.
	assert.NoFileExists(t, filepath.Join(sub, ".gitignore"))
}

func TestInit_GitignoreAppendsToExisting(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("dist/\n"), 0o644))

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "dist/")
	assert.Contains(t, string(content), ".kata.local.toml")
}
