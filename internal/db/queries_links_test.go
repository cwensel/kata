package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

// makeIssue is a helper that creates an issue under projectID. It returns the
// created issue.
//
//nolint:revive // test helper: t *testing.T conventionally precedes ctx.
func makeIssue(t *testing.T, ctx context.Context, d *db.DB, projectID int64, title, author string) db.Issue {
	t.Helper()
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: author,
	})
	require.NoError(t, err)
	return issue
}

func TestCreateLink_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "child", "tester")
	b := makeIssue(t, ctx, d, p.ID, "parent", "tester")

	link, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: a.ID,
		ToIssueID:   b.ID,
		Type:        "parent",
		Author:      "tester",
	})
	require.NoError(t, err)
	assert.Greater(t, link.ID, int64(0))
	assert.Equal(t, "parent", link.Type)
	assert.Equal(t, a.ID, link.FromIssueID)
	assert.Equal(t, b.ID, link.ToIssueID)

	got, err := d.LinkByID(ctx, link.ID)
	require.NoError(t, err)
	assert.Equal(t, link.ID, got.ID)
}

func TestCreateLink_DuplicateIsErrLinkExists(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrLinkExists), "expected ErrLinkExists, got %v", err)
}

func TestCreateLink_SecondParentIsErrParentAlreadySet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	child := makeIssue(t, ctx, d, p.ID, "child", "tester")
	p1 := makeIssue(t, ctx, d, p.ID, "p1", "tester")
	p2 := makeIssue(t, ctx, d, p.ID, "p2", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: p1.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: p2.ID, Type: "parent", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrParentAlreadySet),
		"expected ErrParentAlreadySet, got %v", err)
}

func TestCreateLink_ExactDuplicateParentIsErrLinkExists(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	child := makeIssue(t, ctx, d, p.ID, "child", "tester")
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")

	// First insert succeeds.
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	// Re-inserting the exact same triple is "already linked" (idempotent
	// no-op), not "different parent set". Must be ErrLinkExists.
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrLinkExists),
		"exact duplicate parent must be ErrLinkExists, got %v", err)
}

func TestCreateLink_CrossProjectIsErrCrossProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p1.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p2.ID, "b", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p1.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrCrossProjectLink),
		"expected ErrCrossProjectLink, got %v", err)
}

func TestCreateLink_SelfLinkIsError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: a.ID, Type: "related", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrSelfLink),
		"expected ErrSelfLink, got %v", err)
}

func TestLinkByEndpoints_FindsExisting(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	created, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "related", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.LinkByEndpoints(ctx, a.ID, b.ID, "related")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)

	_, err = d.LinkByEndpoints(ctx, a.ID, b.ID, "parent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestLinksByIssue_ReturnsBothDirections(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	c := makeIssue(t, ctx, d, p.ID, "c", "tester")
	// a → blocks → b ; c → parent → a
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: c.ID, ToIssueID: a.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.LinksByIssue(ctx, a.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestParentOf_ReturnsErrNotFoundWhenAbsent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.ParentOf(ctx, a.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestDeleteLinkByID_RemovesRow(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	link, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)

	require.NoError(t, d.DeleteLinkByID(ctx, link.ID))
	_, err = d.LinkByID(ctx, link.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))

	// Re-deleting returns ErrNotFound (caller decides whether to surface as
	// no-op or 404).
	err = d.DeleteLinkByID(ctx, link.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}
