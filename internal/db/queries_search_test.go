package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestFTS_IssueInsertIsIndexed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "fix login crash", Body: "stack trace here", Author: "tester",
	})
	require.NoError(t, err)

	var hits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'login'`).Scan(&hits))
	assert.Equal(t, 1, hits, "FTS should index issue.title on insert")

	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'trace'`).Scan(&hits))
	assert.Equal(t, 1, hits, "FTS should index issue.body on insert")
}

func TestFTS_IssueUpdateReindexes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "old title", Body: "initial body content", Author: "tester",
	})
	require.NoError(t, err)

	newTitle := "fresh title"
	_, _, _, err = d.EditIssue(ctx, db.EditIssueParams{
		IssueID: issue.ID, Title: &newTitle, Actor: "tester",
	})
	require.NoError(t, err)

	var oldHits, newHits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'old'`).Scan(&oldHits))
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'fresh'`).Scan(&newHits))
	assert.Equal(t, 0, oldHits, "old title tokens must be gone after edit")
	assert.Equal(t, 1, newHits, "new title tokens must be searchable after edit")
}

func TestFTS_CommentInsertReindexes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "boring", Body: "body", Author: "tester",
	})
	require.NoError(t, err)

	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "tester", Body: "watermelon",
	})
	require.NoError(t, err)

	var hits int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE issues_fts MATCH 'watermelon'`).Scan(&hits))
	assert.Equal(t, 1, hits, "comment body must be searchable after insert")
}
