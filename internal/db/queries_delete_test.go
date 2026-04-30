package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestSoftDeleteIssue_SetsDeletedAtAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.soft_deleted", evt.Type)
	assert.Equal(t, "agent", evt.Actor)
	require.NotNil(t, updated.DeletedAt)
}

func TestSoftDeleteIssue_AlreadyDeletedIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt, "no-op should return nil event")
	assert.False(t, changed)
	assert.NotNil(t, updated.DeletedAt, "issue stays deleted")
}

func TestSoftDeleteIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.SoftDeleteIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestRestoreIssue_ClearsDeletedAtAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)

	updated, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.restored", evt.Type)
	assert.Nil(t, updated.DeletedAt)
}

func TestRestoreIssue_NotDeletedIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)

	_, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt)
	assert.False(t, changed)
}

func TestRestoreIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.RestoreIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}
