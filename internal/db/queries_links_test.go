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

func TestParentNumbersByIssues_EmptyInput(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	got, err := d.ParentNumbersByIssues(ctx, p.ID, nil)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	got, err = d.ParentNumbersByIssues(ctx, p.ID, []int64{})
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestChildCountsByParents_EmptyInput(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	got, err := d.ChildCountsByParents(ctx, p.ID, nil)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	got, err = d.ChildCountsByParents(ctx, p.ID, []int64{})
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestParentNumbersByIssues_ReturnsImmediateParents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
	child1 := makeIssue(t, ctx, d, p.ID, "child 1", "tester")
	child2 := makeIssue(t, ctx, d, p.ID, "child 2", "tester")
	unrelated := makeIssue(t, ctx, d, p.ID, "unrelated", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child1.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child2.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.ParentNumbersByIssues(ctx, p.ID, []int64{child1.ID, child2.ID, unrelated.ID})
	require.NoError(t, err)
	assert.Equal(t, parent.Number, got[child1.ID])
	assert.Equal(t, parent.Number, got[child2.ID])
	assert.NotContains(t, got, unrelated.ID)
}

func TestParentNumbersByIssues_ConstrainsProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pa, err := d.CreateProject(ctx, "a", "a")
	require.NoError(t, err)
	pb, err := d.CreateProject(ctx, "b", "b")
	require.NoError(t, err)
	parentA := makeIssue(t, ctx, d, pa.ID, "parent a", "tester")
	childA := makeIssue(t, ctx, d, pa.ID, "child a", "tester")
	parentB := makeIssue(t, ctx, d, pb.ID, "parent b", "tester")
	childB := makeIssue(t, ctx, d, pb.ID, "child b", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: pa.ID, FromIssueID: childA.ID, ToIssueID: parentA.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: pb.ID, FromIssueID: childB.ID, ToIssueID: parentB.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.ParentNumbersByIssues(ctx, pa.ID, []int64{childA.ID, childB.ID})
	require.NoError(t, err)
	assert.Equal(t, parentA.Number, got[childA.ID])
	assert.NotContains(t, got, childB.ID)
}

func TestChildCountsByParents_ReturnsOpenAndTotalDirectChildren(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
	child1 := makeIssue(t, ctx, d, p.ID, "child 1", "tester")
	child2 := makeIssue(t, ctx, d, p.ID, "child 2", "tester")
	child3 := makeIssue(t, ctx, d, p.ID, "child 3", "tester")
	for _, child := range []db.Issue{child1, child2, child3} {
		_, err = d.CreateLink(ctx, db.CreateLinkParams{
			ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
		})
		require.NoError(t, err)
	}
	_, _, _, err = d.CloseIssue(ctx, child2.ID, "done", "tester")
	require.NoError(t, err)

	got, err := d.ChildCountsByParents(ctx, p.ID, []int64{parent.ID})
	require.NoError(t, err)
	assert.Equal(t, db.ChildCounts{Open: 2, Total: 3}, got[parent.ID])
}

func TestChildrenOfIssue_ReturnsDirectChildrenOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
	child1 := makeIssue(t, ctx, d, p.ID, "child 1", "tester")
	child2 := makeIssue(t, ctx, d, p.ID, "child 2", "tester")
	grandchild := makeIssue(t, ctx, d, p.ID, "grandchild", "tester")
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child1.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child2.ID, ToIssueID: parent.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: grandchild.ID, ToIssueID: child1.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.ChildrenOfIssue(ctx, p.ID, parent.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, child2.ID, got[0].ID)
	assert.Equal(t, child1.ID, got[1].ID)
}

func TestChildCountsByParents_ChunksLargeInputs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	const parentCount = 501
	parentIDs := make([]int64, 0, parentCount)
	for i := 0; i < parentCount; i++ {
		parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
		parentIDs = append(parentIDs, parent.ID)
	}
	child := makeIssue(t, ctx, d, p.ID, "child", "tester")
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: parentIDs[parentCount-1], Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.ChildCountsByParents(ctx, p.ID, parentIDs)
	require.NoError(t, err, "large parent batches must be chunked under SQLite parameter limits")
	assert.Equal(t, db.ChildCounts{Open: 1, Total: 1}, got[parentIDs[parentCount-1]])
	assert.NotContains(t, got, parentIDs[0])
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
