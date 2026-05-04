package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func TestImportCreatesTargetDB(t *testing.T) {
	resetFlags(t)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))
	input := writeExportFixture(t, home)
	target := filepath.Join(home, "target.db")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"import", "--input", input, "--target", target})
	require.NoError(t, cmd.Execute())

	d, err := db.Open(context.Background(), target)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	got, err := d.ProjectByIdentity(context.Background(), "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, "kata", got.Name)
	assert.Contains(t, buf.String(), target)
}

func TestImportRejectsExistingTargetWithoutForce(t *testing.T) {
	resetFlags(t)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))
	input := writeExportFixture(t, home)
	target := filepath.Join(home, "target.db")
	d, err := db.Open(context.Background(), target)
	require.NoError(t, err)
	_, err = d.CreateProject(context.Background(), "github.com/wesm/existing", "existing")
	require.NoError(t, err)
	require.NoError(t, d.Close())

	cmd := newRootCmd()
	cmd.SetArgs([]string{"import", "--input", input, "--target", target})
	err = cmd.Execute()

	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Message, "target already exists")
}

func TestImportRefusesDaemon(t *testing.T) {
	resetFlags(t)
	home := t.TempDir()
	dbPath := filepath.Join(home, "kata.db")
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", dbPath)
	input := writeExportFixture(t, home)
	target := filepath.Join(home, "target.db")
	d, err := db.Open(context.Background(), dbPath)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	addr, cleanup := pipeServer(t)
	t.Cleanup(cleanup)
	require.NoError(t, writeRuntimeFor(home, addr))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"import", "--input", input, "--target", target})
	err = cmd.Execute()

	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Message, "daemon is running")
	assert.NotContains(t, ce.Message, "--allow-running-daemon")
}

func writeExportFixture(t *testing.T, home string) string {
	t.Helper()
	srcPath := filepath.Join(home, "source.db")
	src, err := db.Open(context.Background(), srcPath)
	require.NoError(t, err)
	p, err := src.CreateProject(context.Background(), "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, _, err = src.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "imported issue",
		Author:    "tester",
	})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, jsonl.Export(context.Background(), src, &out, jsonl.ExportOptions{IncludeDeleted: true}))
	require.NoError(t, src.Close())
	input := filepath.Join(home, "input.jsonl")
	require.NoError(t, os.WriteFile(input, out.Bytes(), 0o600))
	return input
}
