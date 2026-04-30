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
	assert.JSONEq(t, "{}", string(evt.Payload), "soft_deleted event has empty payload")
	require.NotNil(t, evt.IssueID)
	assert.Equal(t, issue.ID, *evt.IssueID, "event refs the soft-deleted issue id")
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, issue.Number, *evt.IssueNumber, "event refs the soft-deleted issue number")
	require.NotNil(t, updated.DeletedAt)
	assert.True(t, updated.UpdatedAt.After(issue.UpdatedAt) || updated.UpdatedAt.Equal(issue.UpdatedAt),
		"updated_at must not regress on soft-delete")
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

func TestSoftDeleteRestore_RoundTripVisibility(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "round trip", Author: "tester",
	})
	require.NoError(t, err)

	// Initial: visible in default list.
	listed, err := d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, issue.ID, listed[0].ID)

	// After soft-delete: hidden from default list.
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	listed, err = d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	assert.Empty(t, listed, "soft-deleted issue must be hidden from default list")

	// After restore: visible again.
	_, _, _, err = d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	listed, err = d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, issue.ID, listed[0].ID, "restored issue is visible again")
}

func TestRestoreIssue_EmitsEventWithPayloadAndRefs(t *testing.T) {
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
	assert.JSONEq(t, "{}", string(evt.Payload), "restored event has empty payload")
	require.NotNil(t, evt.IssueID)
	assert.Equal(t, issue.ID, *evt.IssueID)
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, issue.Number, *evt.IssueNumber)
	assert.True(t, updated.UpdatedAt.After(issue.UpdatedAt) || updated.UpdatedAt.Equal(issue.UpdatedAt),
		"updated_at must not regress on restore")
}

func TestSoftDeleteIssue_ScopesByIssueID(t *testing.T) {
	// SoftDeleteIssue takes an issue ID, not a project ID — it must work
	// across projects without requiring a project context.
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID, Title: "in p1", Author: "tester",
	})
	require.NoError(t, err)
	target, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID, Title: "in p2", Author: "tester",
	})
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, target.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	require.NotNil(t, updated.DeletedAt)
	assert.Equal(t, p2.ID, updated.ProjectID, "deleted issue belongs to p2")
}
