package main

import (
	"context"
	"os/exec"
	"path/filepath"
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
