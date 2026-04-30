package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestAddLabel_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	row, err := d.AddLabel(ctx, i.ID, "needs-review", "tester")
	require.NoError(t, err)
	assert.Equal(t, "needs-review", row.Label)
	assert.Equal(t, i.ID, row.IssueID)

	got, err := d.LabelsByIssue(ctx, i.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "needs-review", got[0].Label)
}

func TestAddLabel_DuplicateIsErrLabelExists(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	require.NoError(t, err)
	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	assert.True(t, errors.Is(err, db.ErrLabelExists), "got %v", err)
}

func TestAddLabel_RejectsBadCharset(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	for _, label := range []string{"UPPER", "with space", "emoji😀", "" /* empty */, "exclam!"} {
		_, err := d.AddLabel(ctx, i.ID, label, "tester")
		assert.Truef(t, errors.Is(err, db.ErrLabelInvalid),
			"label=%q: expected ErrLabelInvalid, got %v", label, err)
	}
}

func TestAddLabel_AcceptsAllAllowedChars(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	for _, label := range []string{"bug", "priority:high", "v1.0", "needs-review", "a-z_0-9"} {
		_, err := d.AddLabel(ctx, i.ID, label, "tester")
		assert.NoErrorf(t, err, "label=%q must be accepted", label)
	}
}

func TestRemoveLabel_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")
	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	require.NoError(t, err)

	require.NoError(t, d.RemoveLabel(ctx, i.ID, "bug"))

	had, err := d.HasLabel(ctx, i.ID, "bug")
	require.NoError(t, err)
	assert.False(t, had)

	// Idempotent — removing an absent label returns ErrNotFound.
	err = d.RemoveLabel(ctx, i.ID, "bug")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestLabelCounts_AggregatesPerProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	for _, lab := range []string{"bug", "priority:high"} {
		_, err := d.AddLabel(ctx, a.ID, lab, "tester")
		require.NoError(t, err)
	}
	_, err = d.AddLabel(ctx, b.ID, "bug", "tester")
	require.NoError(t, err)

	counts, err := d.LabelCounts(ctx, p.ID)
	require.NoError(t, err)
	got := map[string]int64{}
	for _, c := range counts {
		got[c.Label] = c.Count
	}
	assert.Equal(t, int64(2), got["bug"])
	assert.Equal(t, int64(1), got["priority:high"])
}
