package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOwner_AssignFromNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	owner := "alice"
	updated, evt, changed, err := d.UpdateOwner(ctx, i.ID, &owner, "tester")
	require.NoError(t, err)
	assert.True(t, changed)
	require.NotNil(t, updated.Owner)
	assert.Equal(t, "alice", *updated.Owner)
	require.NotNil(t, evt)
	assert.Equal(t, "issue.assigned", evt.Type)
	assert.Equal(t, `{"owner":"alice"}`, evt.Payload)
}

func TestUpdateOwner_UnassignFromValue(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")
	owner := "alice"
	_, _, _, err = d.UpdateOwner(ctx, i.ID, &owner, "tester")
	require.NoError(t, err)

	updated, evt, changed, err := d.UpdateOwner(ctx, i.ID, nil, "tester")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Nil(t, updated.Owner)
	require.NotNil(t, evt)
	assert.Equal(t, "issue.unassigned", evt.Type)
	assert.Equal(t, "{}", evt.Payload)
}

func TestUpdateOwner_NoOpSameOwner(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")
	owner := "alice"
	_, _, _, err = d.UpdateOwner(ctx, i.ID, &owner, "tester")
	require.NoError(t, err)

	_, evt, changed, err := d.UpdateOwner(ctx, i.ID, &owner, "tester")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, evt)
}

func TestUpdateOwner_NoOpAlreadyUnassigned(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, evt, changed, err := d.UpdateOwner(ctx, i.ID, nil, "tester")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, evt)
}

// Regression: %q-encoded payloads produced invalid JSON for owner strings
// containing control bytes (e.g. NUL), tripping the events.payload
// json_valid CHECK and rolling back the assignment. Now built via
// encoding/json so any schema-accepted owner value round-trips cleanly.
func TestUpdateOwner_ControlByteOwnerProducesValidJSON(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	owner := "alice\x00bob"
	updated, evt, changed, err := d.UpdateOwner(ctx, i.ID, &owner, "tester")
	require.NoError(t, err)
	assert.True(t, changed)
	require.NotNil(t, updated.Owner)
	assert.Equal(t, owner, *updated.Owner)
	require.NotNil(t, evt)

	var payload struct {
		Owner string `json:"owner"`
	}
	require.NoError(t, json.Unmarshal([]byte(evt.Payload), &payload))
	assert.Equal(t, owner, payload.Owner)
}
