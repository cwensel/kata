package jsonl_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

type richJSONLFixture struct {
	DB      *db.DB
	Project db.Project
}

func buildRichJSONLFixture(t *testing.T) richJSONLFixture {
	t.Helper()
	ctx := context.Background()
	d := openExportTestDB(t)

	p1, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "github.com/wesm/other", "other")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p1.ID, "github.com/wesm/kata", "git", "/tmp/kata")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p1.ID, "kata-local", "local", "/work/kata")
	require.NoError(t, err)
	_, err = d.AttachAlias(ctx, p2.ID, "github.com/wesm/other", "git", "/tmp/other")
	require.NoError(t, err)

	login, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID,
		Title:     "orchid login regression",
		Body:      "Safari login fails after the orchid rollout",
		Author:    "tester",
		Labels:    []string{"bug", "frontend"},
	})
	require.NoError(t, err)
	blocker, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID,
		Title:     "api blocker",
		Body:      "Backend response blocks login",
		Author:    "tester",
		Labels:    []string{"backend"},
	})
	require.NoError(t, err)
	softDeleted, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID,
		Title:     "soft deleted keeps FTS",
		Body:      "deleted but still exportable",
		Author:    "tester",
	})
	require.NoError(t, err)
	purged, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID,
		Title:     "purged audit trail",
		Body:      "purged body should leave purge_log only",
		Author:    "tester",
		Labels:    []string{"audit"},
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID,
		Title:     "other project orchid",
		Body:      "cross project export coverage",
		Author:    "tester",
	})
	require.NoError(t, err)

	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: login.ID,
		Author:  "tester",
		Body:    "watermelon comment text",
	})
	require.NoError(t, err)
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: purged.ID,
		Author:  "tester",
		Body:    "purged comment text",
	})
	require.NoError(t, err)
	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID:   p1.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   login.ID,
		Type:        "blocks",
		Author:      "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: blocker.ID, EventIssueNumber: blocker.Number,
		FromNumber: blocker.Number, ToNumber: login.Number, Actor: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, softDeleted.ID, "tester")
	require.NoError(t, err)
	reason := "roundtrip fixture"
	pl, err := d.PurgeIssue(ctx, purged.ID, "tester", &reason)
	require.NoError(t, err)
	require.NotNil(t, pl.PurgeResetAfterEventID)

	return richJSONLFixture{DB: d, Project: p1}
}
