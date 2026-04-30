package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestMaxEventID_EmptyTable(t *testing.T) {
	d := openTestDB(t)
	got, err := d.MaxEventID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), got)
}

func TestMaxEventID_AfterInserts(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "t", Body: "", Author: "tester",
		})
		require.NoError(t, err)
	}
	got, err := d.MaxEventID(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got, "three issue.created events → highest event id is 3")
}

func TestEventsAfter_CrossProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pa, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	pb, err := d.CreateProject(ctx, "github.com/test/b", "b")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: pa.ID, Title: "a1", Author: "tester"})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: pb.ID, Title: "b1", Author: "tester"})
	require.NoError(t, err)

	all, err := d.EventsAfter(ctx, db.EventsAfterParams{AfterID: 0, Limit: 100})
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Equal(t, int64(1), all[0].ID)
	assert.Equal(t, int64(2), all[1].ID)
}

func TestEventsAfter_PerProjectFilter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pa, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	pb, err := d.CreateProject(ctx, "github.com/test/b", "b")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: pa.ID, Title: "a1", Author: "tester"})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: pb.ID, Title: "b1", Author: "tester"})
	require.NoError(t, err)

	onlyA, err := d.EventsAfter(ctx, db.EventsAfterParams{AfterID: 0, ProjectID: pa.ID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, onlyA, 1)
	assert.Equal(t, pa.ID, onlyA[0].ProjectID)
}

func TestEventsAfter_RespectsThroughID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "tester"})
		require.NoError(t, err)
	}
	got, err := d.EventsAfter(ctx, db.EventsAfterParams{AfterID: 0, ThroughID: 3, Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, int64(3), got[2].ID)
}

func TestEventsAfter_RespectsLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "tester"})
		require.NoError(t, err)
	}
	got, err := d.EventsAfter(ctx, db.EventsAfterParams{AfterID: 0, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestPurgeResetCheck_NoPurges(t *testing.T) {
	d := openTestDB(t)
	got, err := d.PurgeResetCheck(context.Background(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got)
}

func TestPurgeResetCheck_AfterPurgeWithEvents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	is, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "doomed", Author: "tester"})
	require.NoError(t, err)
	_, err = d.PurgeIssue(ctx, is.ID, "tester", nil)
	require.NoError(t, err)

	// cursor below the reset → returns the reset cursor
	got, err := d.PurgeResetCheck(ctx, 0, 0)
	require.NoError(t, err)
	assert.Greater(t, got, int64(0), "purge of an issue with events reserves a synthetic cursor")

	// cursor at-or-above the reset → returns 0
	zero, err := d.PurgeResetCheck(ctx, got, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), zero, "PurgeResetCheck uses strict > so cursor==reset is unaffected")
}

func TestPurgeResetCheck_PerProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pa, err := d.CreateProject(ctx, "github.com/test/a", "a")
	require.NoError(t, err)
	pb, err := d.CreateProject(ctx, "github.com/test/b", "b")
	require.NoError(t, err)
	is, _, err := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: pa.ID, Title: "doomed", Author: "tester"})
	require.NoError(t, err)
	_, err = d.PurgeIssue(ctx, is.ID, "tester", nil)
	require.NoError(t, err)

	// per-project filter: a purge in A is invisible to a B-scoped subscriber
	got, err := d.PurgeResetCheck(ctx, 0, pb.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), got, "per-project filter excludes other-project purges")

	// per-project filter: visible to A-scoped subscriber
	gotA, err := d.PurgeResetCheck(ctx, 0, pa.ID)
	require.NoError(t, err)
	assert.Greater(t, gotA, int64(0))
}
