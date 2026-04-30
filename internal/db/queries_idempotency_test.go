package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestFingerprint_DeterministicOverInputOrder(t *testing.T) {
	owner := "alice"
	a := db.Fingerprint("fix login", "details", &owner,
		[]string{"bug", "ui"},
		[]db.InitialLink{{Type: "blocks", ToNumber: 7}, {Type: "parent", ToNumber: 3}})
	b := db.Fingerprint("fix login", "details", &owner,
		[]string{"ui", "bug"}, // labels reordered
		[]db.InitialLink{{Type: "parent", ToNumber: 3}, {Type: "blocks", ToNumber: 7}}) // links reordered
	assert.Equal(t, a, b, "fingerprint must be order-independent for labels and links")
}

func TestFingerprint_CanonicalizesWhitespace(t *testing.T) {
	a := db.Fingerprint("fix login", "body text", nil, nil, nil)
	b := db.Fingerprint("  fix\t\n  login  ", "body  text", nil, nil, nil)
	assert.Equal(t, a, b, "internal whitespace runs and trimming must collapse")
}

func TestFingerprint_DiffersOnDifferentInputs(t *testing.T) {
	base := db.Fingerprint("a", "b", nil, nil, nil)
	cases := []struct {
		name        string
		fingerprint string
	}{
		{"different_title", db.Fingerprint("aa", "b", nil, nil, nil)},
		{"different_body", db.Fingerprint("a", "bb", nil, nil, nil)},
		{"different_owner", db.Fingerprint("a", "b", strPtr("x"), nil, nil)},
		{"different_labels", db.Fingerprint("a", "b", nil, []string{"bug"}, nil)},
		{"different_links", db.Fingerprint("a", "b", nil, nil,
			[]db.InitialLink{{Type: "blocks", ToNumber: 1}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, base, tc.fingerprint)
		})
	}
}

func TestFingerprint_CaseSensitive(t *testing.T) {
	// Spec §3.6: canonical() does NOT lowercase. Title casing matters.
	a := db.Fingerprint("Fix Login", "", nil, nil, nil)
	b := db.Fingerprint("fix login", "", nil, nil, nil)
	assert.NotEqual(t, a, b)
}

func TestFingerprint_NilAndEmptyOwnerAreEquivalent(t *testing.T) {
	empty := ""
	a := db.Fingerprint("a", "b", nil, nil, nil)
	b := db.Fingerprint("a", "b", &empty, nil, nil)
	assert.Equal(t, a, b, "nil owner and empty owner produce the same fingerprint")
}

func TestFingerprint_HexLowercaseSHA256(t *testing.T) {
	got := db.Fingerprint("a", "b", nil, nil, nil)
	assert.Len(t, got, 64, "sha256 hex is 64 chars")
	assert.True(t, strings.ToLower(got) == got, "must be lowercase hex")
}

func strPtr(s string) *string { return &s }

func TestLookupIdempotency_ReturnsMatchWithinWindow(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	// Hand-write an issue.created event with idempotency_key + fingerprint
	// in the payload so we test LookupIdempotency in isolation from the
	// CreateIssue extension landing in Task 9.
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	fp := "abc123"
	_, err = d.ExecContext(ctx,
		`UPDATE events
		 SET payload = json_set(payload, '$.idempotency_key', 'K1', '$.idempotency_fingerprint', ?)
		 WHERE issue_id = ? AND type = 'issue.created'`, fp, issue.ID)
	require.NoError(t, err)

	since := time.Now().Add(-1 * time.Hour)
	got, err := d.LookupIdempotency(ctx, p.ID, "K1", since)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, issue.ID, got.IssueID)
	assert.Equal(t, issue.Number, got.IssueNumber)
	assert.Equal(t, fp, got.Fingerprint)
	assert.Equal(t, "issue.created", got.Event.Type)
}

func TestLookupIdempotency_OutsideWindowIsNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
		`UPDATE events
		 SET payload = json_set(payload, '$.idempotency_key', 'K1', '$.idempotency_fingerprint', 'fp')
		 WHERE issue_id = ?`, issue.ID)
	require.NoError(t, err)

	// Window starts in the future — every existing event is "outside".
	future := time.Now().Add(1 * time.Hour)
	got, err := d.LookupIdempotency(ctx, p.ID, "K1", future)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookupIdempotency_DifferentKeyIsNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	got, err := d.LookupIdempotency(ctx, p.ID, "no-such-key", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookupIdempotency_DifferentProjectIsNil(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.ExecContext(ctx,
		`UPDATE events
		 SET payload = json_set(payload, '$.idempotency_key', 'K1', '$.idempotency_fingerprint', 'fp')
		 WHERE issue_id = ?`, issue.ID)
	require.NoError(t, err)

	got, err := d.LookupIdempotency(ctx, p2.ID, "K1", time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, got, "key in p1 must not match a lookup in p2")
}
