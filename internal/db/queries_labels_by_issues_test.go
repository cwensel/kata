package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLabelsByIssues_EmptyInput_ReturnsEmptyMap verifies the
// short-circuit: an empty issueIDs slice returns an empty (non-nil)
// map without a SQL roundtrip. The daemon's list handler relies on
// this so an empty list page doesn't waste a query.
func TestLabelsByIssues_EmptyInput_ReturnsEmptyMap(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	got, err := d.LabelsByIssues(ctx, p.ID, nil)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)

	got, err = d.LabelsByIssues(ctx, p.ID, []int64{})
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestLabelsByIssues_ConstrainedByProjectID verifies the cross-project
// safety check: passing an issueID belonging to project A while
// querying project B returns no labels for that ID. issue_labels has
// no project_id column, so the constraint runs through the JOIN.
func TestLabelsByIssues_ConstrainedByProjectID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pa, err := d.CreateProject(ctx, "a", "a")
	require.NoError(t, err)
	pb, err := d.CreateProject(ctx, "b", "b")
	require.NoError(t, err)
	ia := makeIssue(t, ctx, d, pa.ID, "a", "tester")
	ib := makeIssue(t, ctx, d, pb.ID, "b", "tester")
	_, err = d.AddLabel(ctx, ia.ID, "bug", "tester")
	require.NoError(t, err)
	_, err = d.AddLabel(ctx, ib.ID, "feature", "tester")
	require.NoError(t, err)

	// Query project A with both issue IDs; only ia's labels return.
	got, err := d.LabelsByIssues(ctx, pa.ID, []int64{ia.ID, ib.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug"}, got[ia.ID])
	assert.Empty(t, got[ib.ID], "issue from a different project must not leak labels")
}

// TestLabelsByIssues_OrdersByIssueThenLabel verifies the per-issue
// alphabetical sort. Insertion order is intentionally non-alphabetical
// so the assertion would fail if ORDER BY were dropped.
func TestLabelsByIssues_OrdersByIssueThenLabel(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	for _, lbl := range []string{"prio-1", "bug", "needs-review"} {
		_, err := d.AddLabel(ctx, i.ID, lbl, "tester")
		require.NoError(t, err)
	}

	got, err := d.LabelsByIssues(ctx, p.ID, []int64{i.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug", "needs-review", "prio-1"}, got[i.ID])
}

// TestLabelsByIssues_MultiIssue_HappyPath verifies the map structure
// across multiple issues with overlapping and disjoint labels: each
// issue's slice is independently sorted and only contains its own labels.
func TestLabelsByIssues_MultiIssue_HappyPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i1 := makeIssue(t, ctx, d, p.ID, "a", "tester")
	i2 := makeIssue(t, ctx, d, p.ID, "b", "tester")
	i3 := makeIssue(t, ctx, d, p.ID, "c", "tester")

	add := func(issueID int64, label string) {
		_, err := d.AddLabel(ctx, issueID, label, "tester")
		require.NoError(t, err)
	}
	add(i1.ID, "bug")
	add(i1.ID, "prio-1")
	add(i2.ID, "feature")
	add(i2.ID, "needs-review")
	add(i2.ID, "bug")
	add(i3.ID, "prio-1")
	add(i3.ID, "wontfix")

	got, err := d.LabelsByIssues(ctx, p.ID, []int64{i1.ID, i2.ID, i3.ID})
	require.NoError(t, err)
	assert.Len(t, got, 3)
	assert.Equal(t, []string{"bug", "prio-1"}, got[i1.ID])
	assert.Equal(t, []string{"bug", "feature", "needs-review"}, got[i2.ID])
	assert.Equal(t, []string{"prio-1", "wontfix"}, got[i3.ID])
}

// TestLabelsByIssues_IssueWithNoLabelsAbsent verifies the contract that
// issues with no labels are absent from the map. Callers treat a
// missing key as "no labels"; this prevents allocation noise on the
// common case where most issues are unlabeled.
func TestLabelsByIssues_IssueWithNoLabelsAbsent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i1 := makeIssue(t, ctx, d, p.ID, "labeled", "tester")
	i2 := makeIssue(t, ctx, d, p.ID, "naked", "tester")
	_, err = d.AddLabel(ctx, i1.ID, "bug", "tester")
	require.NoError(t, err)

	got, err := d.LabelsByIssues(ctx, p.ID, []int64{i1.ID, i2.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug"}, got[i1.ID])
	_, present := got[i2.ID]
	assert.False(t, present, "issue with no labels must be absent from map")
}
