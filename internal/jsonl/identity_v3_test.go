package jsonl_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/uid"
)

// TestV1ToV3CutoverFillsIdentity covers spec §8.5: a v1 source going through
// the cutover path lands at v3 with valid project UIDs, issue UIDs, event UIDs,
// purge_log UIDs, and origin_instance_uid stamped on every event/purge_log row
// matching the new local meta.instance_uid.
func TestV1ToV3CutoverFillsIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	writeLegacyV1DB(t, path)

	require.NoError(t, jsonl.AutoCutover(ctx, path))

	d, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	var localUID string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&localUID))
	assert.True(t, uid.Valid(localUID))

	var eventUID, eventOrigin string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT uid, origin_instance_uid FROM events WHERE id=1`).
		Scan(&eventUID, &eventOrigin))
	assert.True(t, uid.Valid(eventUID))
	assert.Equal(t, localUID, eventOrigin)
}

// TestV1ToV3CutoverDeterministicRowUIDs covers spec §8.5: rerunning the cutover
// on the same v1 source produces identical row UIDs (FromStableSeed) for
// projects, issues, events, and purge_log. Note that meta.instance_uid and
// origin_instance_uid columns are intentionally non-deterministic across reruns
// (per §5.3) — those are excluded from the equality check.
func TestV1ToV3CutoverDeterministicRowUIDs(t *testing.T) {
	ctx := context.Background()

	pathA := filepath.Join(t.TempDir(), "a.db")
	writeLegacyV1DB(t, pathA)
	require.NoError(t, jsonl.AutoCutover(ctx, pathA))

	pathB := filepath.Join(t.TempDir(), "b.db")
	writeLegacyV1DB(t, pathB)
	require.NoError(t, jsonl.AutoCutover(ctx, pathB))

	a, err := db.Open(ctx, pathA)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	b, err := db.Open(ctx, pathB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	for _, q := range []string{
		`SELECT uid FROM projects ORDER BY id ASC`,
		`SELECT uid FROM issues ORDER BY id ASC`,
		`SELECT uid FROM events ORDER BY id ASC`,
	} {
		assert.Equal(t, scanUIDs(t, a, q), scanUIDs(t, b, q), q)
	}

	// Sanity: meta.instance_uid is intentionally NOT deterministic across
	// reruns — two clones of the same v1 source must become two distinct
	// installations.
	var aUID, bUID string
	require.NoError(t, a.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&aUID))
	require.NoError(t, b.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&bUID))
	assert.NotEqual(t, aUID, bUID)
}

// TestRoundtripV3PreservesInstanceUID covers spec §8.6: a v3 export → v3
// default-mode import preserves meta.instance_uid end-to-end and every event's
// origin_instance_uid still matches the source's identity.
func TestRoundtripV3PreservesInstanceUID(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, _, err = src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "preserve identity", Author: "tester",
	})
	require.NoError(t, err)

	var srcUID string
	require.NoError(t, src.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&srcUID))

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := db.Open(ctx, dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, jsonl.Import(ctx, bytes.NewReader(buf.Bytes()), dst))

	var dstUID string
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&dstUID))
	assert.Equal(t, srcUID, dstUID, "default mode preserves source identity")

	for _, origin := range scanUIDs(t, dst, `SELECT origin_instance_uid FROM events`) {
		assert.Equal(t, srcUID, origin)
	}
}

// TestImportNewInstanceRegeneratesIdentity covers spec §8.7: --new-instance
// mode keeps the target's fresh meta.instance_uid (db.Open's value) and
// preserves the source's origin_instance_uid on every imported event.
func TestImportNewInstanceRegeneratesIdentity(t *testing.T) {
	ctx := context.Background()
	src := openExportTestDB(t)
	p, err := src.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, _, err = src.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "clone me", Author: "tester",
	})
	require.NoError(t, err)

	var srcUID string
	require.NoError(t, src.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&srcUID))

	var buf bytes.Buffer
	require.NoError(t, jsonl.Export(ctx, src, &buf, jsonl.ExportOptions{IncludeDeleted: true}))

	dstPath := filepath.Join(t.TempDir(), "dst.db")
	dst, err := db.Open(ctx, dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })
	require.NoError(t, jsonl.ImportWithOptions(ctx, bytes.NewReader(buf.Bytes()), dst, jsonl.ImportOptions{
		NewInstance: true,
	}))

	var dstUID string
	require.NoError(t, dst.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&dstUID))
	assert.NotEqual(t, srcUID, dstUID, "new-instance keeps target's fresh identity")
	assert.True(t, uid.Valid(dstUID))

	// Imported events keep the source's origin (loop-detection contract).
	for _, origin := range scanUIDs(t, dst, `SELECT origin_instance_uid FROM events`) {
		assert.Equal(t, srcUID, origin)
	}
}

func scanUIDs(t *testing.T, d *db.DB, query string) []string {
	t.Helper()
	rows, err := d.Query(query)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
