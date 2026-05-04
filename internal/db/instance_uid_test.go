package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/uid"
)

// TestOpenSeedsInstanceUID covers the §8.2 invariant: a fresh db.Open writes
// meta.instance_uid as a valid 26-char ULID and exposes it via InstanceUID().
func TestOpenSeedsInstanceUID(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	got := d.InstanceUID()
	require.NotEmpty(t, got)
	assert.True(t, uid.Valid(got), "InstanceUID %q is not a valid ULID", got)
	var stored string
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='instance_uid'`).Scan(&stored))
	assert.Equal(t, got, stored)
}

// TestInstanceUIDStableAcrossReopen covers the spec's "set once at first init,
// never changes" rule: a second db.Open on the same path returns the same UID.
func TestInstanceUIDStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kata.db")
	first, err := db.Open(ctx, path)
	require.NoError(t, err)
	original := first.InstanceUID()
	require.NoError(t, first.Close())

	second, err := db.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	assert.Equal(t, original, second.InstanceUID())
}

// TestEventInsertCarriesUIDAndOrigin covers §8.3: a new event written through
// the daemon's mutation path has a valid UID and origin_instance_uid matching
// the local meta.instance_uid.
func TestEventInsertCarriesUIDAndOrigin(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	_, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "uid-stamping",
		Author:    "tester",
	})
	require.NoError(t, err)
	assert.True(t, uid.Valid(evt.UID), "event UID %q invalid", evt.UID)
	assert.Equal(t, d.InstanceUID(), evt.OriginInstanceUID)
}

// TestPurgeInsertCarriesUIDAndOrigin covers §8.3 for purge_log: a purge writes
// a row with valid uid + origin_instance_uid matching the local instance.
func TestPurgeInsertCarriesUIDAndOrigin(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "purge me", Author: "tester",
	})
	require.NoError(t, err)
	pl, err := d.PurgeIssue(ctx, issue.ID, "tester", nil)
	require.NoError(t, err)
	assert.True(t, uid.Valid(pl.UID), "purge UID %q invalid", pl.UID)
	assert.Equal(t, d.InstanceUID(), pl.OriginInstanceUID)
}
