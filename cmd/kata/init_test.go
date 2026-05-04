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
