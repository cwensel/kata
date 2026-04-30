package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestReadyIssues_FiltersOutClosed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	open := makeIssue(t, ctx, d, p.ID, "open", "tester")
	closed := makeIssue(t, ctx, d, p.ID, "closed", "tester")
	_, _, _, err = d.CloseIssue(ctx, closed.ID, "done", "tester")
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, open.Number)
	assert.NotContains(t, got, closed.Number)
}

func TestReadyIssues_ExcludesIssuesBlockedByOpenBlocker(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	standalone := makeIssue(t, ctx, d, p.ID, "standalone", "tester")
	// blocker → blocks → blocked
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   blocked.ID,
		Type:        "blocks",
		Author:      "tester",
	})
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, blocker.Number, "blocker is ready (not blocked itself)")
	assert.Contains(t, got, standalone.Number, "standalone is ready")
	assert.NotContains(t, got, blocked.Number, "blocked is not ready while blocker is open")
}

func TestReadyIssues_ClosedBlockerUnblocksDownstream(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   blocked.ID,
		Type:        "blocks",
		Author:      "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.CloseIssue(ctx, blocker.ID, "done", "tester")
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, blocked.Number, "blocked is ready once blocker closes")
}

func numbers(rows []db.Issue) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}
