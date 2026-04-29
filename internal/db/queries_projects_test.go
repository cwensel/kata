package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestCreateProject_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", p.Identity)
	assert.Equal(t, "kata", p.Name)
	assert.Equal(t, int64(1), p.NextIssueNumber)
	assert.False(t, p.CreatedAt.IsZero())

	got, err := d.ProjectByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
}

func TestProjectByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.ProjectByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestCreateProject_DuplicateIdentity(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	_, err = d.CreateProject(ctx, "x", "x")
	require.Error(t, err)
}

func TestAttachAlias_AndLookup(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)

	a, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/kata", "git", "/tmp/x")
	require.NoError(t, err)
	assert.Equal(t, p.ID, a.ProjectID)
	assert.Equal(t, "git", a.AliasKind)

	got, err := d.AliasByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
}

func TestAliasByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.AliasByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestTouchAlias_UpdatesLastSeen(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	a, err := d.AttachAlias(ctx, p.ID, "x", "git", "/tmp/x")
	require.NoError(t, err)

	require.NoError(t, d.TouchAlias(ctx, a.ID, "/tmp/y"))
	got, err := d.AliasByIdentity(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/y", got.RootPath)
	assert.True(t, !got.LastSeenAt.Before(a.LastSeenAt))
}

func TestListProjects_Empty(t *testing.T) {
	d := openTestDB(t)
	got, err := d.ListProjects(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListProjects_OrdersByIDAsc(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _ = d.CreateProject(ctx, "a", "a")
	_, _ = d.CreateProject(ctx, "b", "b")

	got, err := d.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Identity)
	assert.Equal(t, "b", got[1].Identity)
}

func TestProjectAliases_ReturnsAllForProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-a", "git", "/tmp/a")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-b", "git", "/tmp/b")

	got, err := d.ProjectAliases(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
