package jsonl_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	_ "modernc.org/sqlite"
)

func TestAutoCutoverNoopsAtCurrentSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	ver, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, db.CurrentSchemaVersion(), ver)
	assertNoCutoverTemps(t, path)
}

func TestAutoCutoverRefusesExistingTempFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	require.NoError(t, os.WriteFile(path+".import.tmp.jsonl", []byte("partial"), 0o600))

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	assert.True(t, errors.Is(err, jsonl.ErrCutoverInProgress))
}

func TestAutoCutoverFailureLeavesSourceAndRemovesTemps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeVersionZeroDB(t, path)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	err = jsonl.AutoCutover(ctx, path)

	require.Error(t, err)
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
	assertNoCutoverTemps(t, path)
}

func TestPeekSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	ver, err := db.PeekSchemaVersion(ctx, path)
	require.NoError(t, err)
	assert.Equal(t, db.CurrentSchemaVersion(), ver)

	noMeta := filepath.Join(t.TempDir(), "empty.db")
	raw, err := sql.Open("sqlite", noMeta)
	require.NoError(t, err)
	require.NoError(t, raw.PingContext(ctx))
	require.NoError(t, raw.Close())
	ver, err = db.PeekSchemaVersion(ctx, noMeta)
	require.NoError(t, err)
	assert.Equal(t, 0, ver)
}

func writeVersionZeroDB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	_, err = d.ExecContext(ctx, `UPDATE meta SET value='0' WHERE key='schema_version'`)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}

func assertNoCutoverTemps(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{".import.tmp.jsonl", ".import.tmp.db"} {
		_, err := os.Stat(path + suffix)
		assert.True(t, os.IsNotExist(err), path+suffix)
	}
}
