package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_WithInitialLabels(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Labels: []string{"bug", "priority:high", "bug" /* dupe */},
	})
	require.NoError(t, err)
	assert.Equal(t, "issue.created", evt.Type)

	labels, err := d.LabelsByIssue(ctx, issue.ID)
	require.NoError(t, err)
	got := []string{}
	for _, l := range labels {
		got = append(got, l.Label)
	}
	assert.ElementsMatch(t, []string{"bug", "priority:high"}, got, "duplicates deduplicated")

	// Payload includes initial labels (sorted, deduplicated).
	var payload struct {
		Labels []string `json:"labels"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Payload), &payload))
	assert.Equal(t, []string{"bug", "priority:high"}, payload.Labels)
}

func TestCreateIssue_WithInitialOwner(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	owner := "alice"
	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Owner: &owner,
	})
	require.NoError(t, err)
	require.NotNil(t, issue.Owner)
	assert.Equal(t, "alice", *issue.Owner)

	var payload struct {
		Owner string `json:"owner"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Payload), &payload))
	assert.Equal(t, "alice", payload.Owner)
}

func TestCreateIssue_WithInitialLinks(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	parent := makeIssue(t, ctx, d, p.ID, "parent", "tester")
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")

	child, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "child", Author: "tester",
		Links: []db.InitialLink{
			{Type: "parent", ToNumber: parent.Number},
			{Type: "blocks", ToNumber: blocker.Number},
		},
	})
	require.NoError(t, err)

	// DB state: 2 link rows from child.
	links, err := d.LinksByIssue(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, links, 2)

	// Payload references to_number, not to_issue_id.
	var payload struct {
		Links []struct {
			Type     string `json:"type"`
			ToNumber int64  `json:"to_number"`
		} `json:"links"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Payload), &payload))
	require.Len(t, payload.Links, 2)
}

func TestCreateIssue_RejectsInitialLinkToMissingTarget(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Links: []db.InitialLink{{Type: "parent", ToNumber: 999}},
	})
	assert.True(t, errors.Is(err, db.ErrInitialLinkTargetNotFound),
		"expected ErrInitialLinkTargetNotFound, got %v", err)
}

func TestCreateIssue_RejectsInvalidInitialLinkType(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	target := makeIssue(t, ctx, d, p.ID, "t", "tester")

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Links: []db.InitialLink{{Type: "child", ToNumber: target.Number}},
	})
	assert.True(t, errors.Is(err, db.ErrInitialLinkInvalidType))
}

func TestCreateIssue_RejectsInvalidLabel(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
		Labels: []string{"BadCase"},
	})
	assert.True(t, errors.Is(err, db.ErrLabelInvalid))
}

func TestCreateIssue_NoInitialStateEmitsEmptyPayload(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	_, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	assert.Equal(t, "{}", evt.Payload)
}
