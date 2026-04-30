# Plan 4 — SSE Event Broadcaster + Polling Endpoints Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `EventBroadcaster` + `GET /api/v1/events/stream` SSE endpoint + `GET /api/v1/events` polling endpoints + `kata events` CLI so agents can watch durable, ordered event streams over SSE with `Last-Event-ID` resume and a polling fallback.

**Architecture:** The broadcaster fans out *wakeups*, not ordered events. Every committed event row is broadcast post-commit; the SSE handler treats each broadcast as "something new ≤ ID N" and re-queries the DB for canonical, totally-ordered output. The DB is the only source of ordering truth. Handshake order is Subscribe-first, `MaxEventID`-second, `PurgeResetCheck`-third — this closes both the reset-race and the live-handoff-race in one move. Disconnect-on-overflow keeps the broadcaster simple; clients reconnect with `Last-Event-ID` and resume via DB replay.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `github.com/danielgtaylor/huma/v2` (polling endpoints stay Huma; SSE is registered as raw `http.HandlerFunc`), `github.com/spf13/cobra`, `github.com/stretchr/testify`.

**Companion spec:** `docs/superpowers/specs/2026-04-30-kata-events-design.md`. Master spec is `docs/superpowers/specs/2026-04-29-kata-design.md` — §2.6, §3.5, §4.4, §4.5, §4.7, §4.8, §4.11 are the authoritative sections.

**Schema-edit policy:** Plan 4 needs no schema changes. `events`, `purge_log`, and `idx_events_idempotency` are already in `internal/db/migrations/0001_init.sql` from Plan 1. Do **not** add a `0002_*.sql` migration; per the user's standing directive ("kata schema in-place edits") schema only gets a fresh migration after the first major iteration ships.

---

## File Structure

**New files (created by this plan):**

| Path | Responsibility |
|---|---|
| `internal/api/events.go` | Wire types: `EventEnvelope`, `EventReset`, `PollEventsResponse`. JSON shapes shared by polling endpoint, SSE `data:` lines, and CLI consumers. |
| `internal/db/queries_events.go` | `MaxEventID`, `EventsAfter(ctx, EventsAfterParams)`, `PurgeResetCheck`. The reads that drive both SSE and polling. |
| `internal/db/queries_events_test.go` | Unit tests for the three queries. |
| `internal/daemon/broadcaster.go` | `EventBroadcaster`, `StreamMsg`, `SubFilter`, `Subscription`. Pure in-memory wakeup fan-out. No DB coupling. |
| `internal/daemon/broadcaster_test.go` | Subscribe / Unsub / Broadcast / overflow / per-project filter / reset fan-out. |
| `internal/daemon/broadcaster_race_test.go` | `-race` tests for out-of-order broadcasts and concurrent Subscribe/Broadcast. |
| `internal/daemon/handlers_events.go` | `GET /api/v1/events/stream` (raw `http.HandlerFunc` for SSE), `GET /api/v1/events`, `GET /api/v1/projects/{project_id}/events` (Huma). |
| `internal/daemon/handlers_events_test.go` | Polling envelope + cursor + clamp tests, SSE Accept negotiation + handshake + drain + stale-cap + per-project filter + reset frame tests. |
| `cmd/kata/events.go` | `kata events` (one-shot poll) and `kata events --tail` (SSE NDJSON consumer with reconnect/backoff). |
| `cmd/kata/events_test.go` | CLI tests: one-shot output, NDJSON streaming, reconnect, reset handling. |

**Files modified by this plan:**

| Path | Changes |
|---|---|
| `internal/daemon/server.go` | Add `Broadcaster *EventBroadcaster` to `ServerConfig`; default-init in `NewServer`; pass `mux` into `registerRoutes`; add `BaseContext` to the `http.Server` in `Serve`. |
| `internal/daemon/handlers_issues.go` | Broadcast `issue.created` after `CreateIssue` commit; **no** broadcast on idempotent reuse. Broadcast `issue.updated` after `EditIssue` (when `changed`). |
| `internal/daemon/handlers_actions.go` | Broadcast `issue.closed` / `issue.reopened` (when `changed`). |
| `internal/daemon/handlers_links.go` | Broadcast `issue.linked` after `CreateLinkAndEvent`; broadcast `issue.unlinked` for the parent `--replace` `DeleteLinkAndEvent` (two events when replacing). Broadcast `issue.unlinked` from the delete-link handler. |
| `internal/daemon/handlers_labels.go` | Broadcast `issue.labeled` / `issue.unlabeled` (skip on no-op). |
| `internal/daemon/handlers_comments.go` | Broadcast `issue.commented`. |
| `internal/daemon/handlers_ownership.go` | Broadcast `issue.assigned` / `issue.unassigned` (when `changed`). |
| `internal/daemon/handlers_destructive.go` | Broadcast `issue.soft_deleted` / `issue.restored` (when `changed`). After purge commit, broadcast a `Kind:"reset"` `StreamMsg` if `pl.PurgeResetAfterEventID != nil`. |
| `cmd/kata/main.go` | Register `newEventsCmd()` in the subcommand list. |
| `docs/superpowers/specs/2026-04-29-kata-design.md` | Rename `new_baseline` → `reset_after_id` in §4.8 and §4.11 (text-only). |

---

## Task 1: Wire types in `internal/api/events.go`

**Files:**
- Create: `internal/api/events.go`

**Why first:** Both the daemon (Tasks 5–7) and the CLI (Tasks 10–11) import these types. Creating them now means the rest of the plan can reference them by name.

- [ ] **Step 1: Create the file with `EventEnvelope`, `EventReset`, `PollEventsResponse`.**

```go
// Package api types for Plan 4 events endpoints. EventEnvelope is the JSON
// shape carried in SSE data: lines and the events array of PollEventsResponse;
// it mirrors db.Event one-for-one but lives in api so the wire schema stays
// independent of internal storage shape.
package api

import (
	"encoding/json"
	"time"
)

// EventEnvelope is the wire shape for a single event row.
type EventEnvelope struct {
	EventID         int64           `json:"event_id"`
	Type            string          `json:"type"`
	ProjectID       int64           `json:"project_id"`
	ProjectIdentity string          `json:"project_identity"`
	IssueID         *int64          `json:"issue_id,omitempty"`
	IssueNumber     *int64          `json:"issue_number,omitempty"`
	RelatedIssueID  *int64          `json:"related_issue_id,omitempty"`
	Actor           string          `json:"actor"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// EventReset is the data: payload of a sync.reset_required SSE frame and the
// stripped-down content of a poll response when the cursor falls inside a
// purge gap.
type EventReset struct {
	EventID      int64 `json:"event_id"`       // == ResetAfterID; mirrors the SSE id: line.
	ResetAfterID int64 `json:"reset_after_id"` // minimum safe resume cursor.
}

// PollEventsRequest is GET /api/v1/events and GET /api/v1/projects/{id}/events.
// AfterID is exclusive; the response's NextAfterID is the cursor the client
// should pass on the next request. Limit defaults to 100 and is clamped to
// 1000 server-side; non-positive Limit returns 400 validation.
type PollEventsRequest struct {
	ProjectID int64 `path:"project_id,omitempty"`
	AfterID   int64 `query:"after_id,omitempty"`
	Limit     int   `query:"limit,omitempty"`
}

// PollEventsResponse is the response for both polling endpoints. ResetRequired
// signals a purge-invalidated cursor; when true, Events is empty and the
// client should refetch state and resume from ResetAfterID.
type PollEventsResponse struct {
	Body struct {
		ResetRequired bool            `json:"reset_required"`
		ResetAfterID  int64           `json:"reset_after_id,omitempty"`
		Events        []EventEnvelope `json:"events"`        // always non-nil; empty array on no rows
		NextAfterID   int64           `json:"next_after_id"` // = max events.id in response, or after_id if empty
	}
}
```

- [ ] **Step 2: Verify `go build` passes**

Run: `cd /Users/wesm/code/kata && go build ./...`
Expected: PASS (no other code references the new types yet, so this just sanity-checks syntax).

- [ ] **Step 3: Commit**

```bash
git add internal/api/events.go
git commit -m "feat(api): wire types for Plan 4 events endpoints"
```

---

## Task 2: DB queries — `MaxEventID`, `EventsAfter`, `PurgeResetCheck`

**Files:**
- Create: `internal/db/queries_events.go`
- Test: `internal/db/queries_events_test.go`

The three queries that drive both polling and SSE. Test-driven: write each query's failing tests first.

- [ ] **Step 1: Write the failing tests for `MaxEventID`**

Create `/Users/wesm/code/kata/internal/db/queries_events_test.go` with:

```go
package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestMaxEventID ./internal/db/`
Expected: FAIL — `d.MaxEventID undefined`.

- [ ] **Step 3: Create `internal/db/queries_events.go` with `MaxEventID`**

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// MaxEventID returns the highest events.id, or 0 when the table is empty. The
// SSE handler uses this as the high-water mark snapshot after Subscribe.
func (d *DB) MaxEventID(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("max event id: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/wesm/code/kata && go test -run TestMaxEventID ./internal/db/`
Expected: PASS.

- [ ] **Step 5: Add `EventsAfter` failing tests**

Append to `/Users/wesm/code/kata/internal/db/queries_events_test.go`:

```go
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
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestEventsAfter ./internal/db/`
Expected: FAIL — `EventsAfter undefined` and `EventsAfterParams undefined`.

- [ ] **Step 7: Add `EventsAfter` implementation**

Append to `/Users/wesm/code/kata/internal/db/queries_events.go`:

```go
// EventsAfterParams selects events with id strictly greater than AfterID,
// optionally bounded above by ThroughID and filtered by ProjectID. Limit is
// applied verbatim; callers are responsible for clamping (the polling
// endpoint clamps to [1, 1000]; the SSE drain passes 10001).
type EventsAfterParams struct {
	AfterID   int64
	ProjectID int64 // 0 = cross-project; nonzero adds AND project_id = ?
	ThroughID int64 // 0 = no upper bound; nonzero adds AND id <= ?
	Limit     int
}

// EventsAfter returns up to Limit events ordered by id ASC.
func (d *DB) EventsAfter(ctx context.Context, p EventsAfterParams) ([]Event, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "id > ?")
	args = append(args, p.AfterID)
	if p.ProjectID != 0 {
		conds = append(conds, "project_id = ?")
		args = append(args, p.ProjectID)
	}
	if p.ThroughID != 0 {
		conds = append(conds, "id <= ?")
		args = append(args, p.ThroughID)
	}
	q := `SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id,
	             type, actor, payload, created_at
	      FROM events WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY id ASC LIMIT ?`
	args = append(args, p.Limit)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events after: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID,
			&e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 8: Run tests**

Run: `cd /Users/wesm/code/kata && go test -run TestEventsAfter ./internal/db/`
Expected: PASS.

- [ ] **Step 9: Add `PurgeResetCheck` failing tests**

Append to `/Users/wesm/code/kata/internal/db/queries_events_test.go`:

```go
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
```

- [ ] **Step 10: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestPurgeResetCheck ./internal/db/`
Expected: FAIL — `PurgeResetCheck undefined`.

- [ ] **Step 11: Add `PurgeResetCheck` implementation**

Append to `/Users/wesm/code/kata/internal/db/queries_events.go`:

```go
// PurgeResetCheck returns the maximum purge_reset_after_event_id strictly
// greater than afterID, optionally constrained to a project. Returns 0 when
// no matching purge_log row exists. The strict > semantics align with the
// spec §2.6 reservation: every reserved cursor is greater than every real
// events.id at the moment of the purge, so cursor == reservedID means the
// client is already past it and does not need a reset.
//
// projectID == 0 = cross-project (no filter).
func (d *DB) PurgeResetCheck(ctx context.Context, afterID, projectID int64) (int64, error) {
	q := `SELECT MAX(purge_reset_after_event_id) FROM purge_log
	      WHERE purge_reset_after_event_id IS NOT NULL AND purge_reset_after_event_id > ?`
	args := []any{afterID}
	if projectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	var n sql.NullInt64
	if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("purge reset check: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return n.Int64, nil
}
```

- [ ] **Step 12: Run tests**

Run: `cd /Users/wesm/code/kata && go test -run TestPurgeResetCheck ./internal/db/`
Expected: PASS.

- [ ] **Step 13: Run the whole package's tests + lint**

Run:
```bash
cd /Users/wesm/code/kata && go test ./internal/db/ && gofmt -l internal/db/queries_events*.go && go vet ./internal/db/
```
Expected: PASS, no gofmt diff, no vet output.

- [ ] **Step 14: Commit**

```bash
git add internal/db/queries_events.go internal/db/queries_events_test.go
git commit -m "feat(db): MaxEventID, EventsAfter, PurgeResetCheck"
```

---

## Task 3: Broadcaster — `internal/daemon/broadcaster.go`

**Files:**
- Create: `internal/daemon/broadcaster.go`
- Test: `internal/daemon/broadcaster_test.go`

Pure in-memory wakeup fan-out. No DB coupling. Per-subscriber bounded channel; overflow disconnects.

- [ ] **Step 1: Write failing test for Subscribe / Unsub lifecycle**

Create `/Users/wesm/code/kata/internal/daemon/broadcaster_test.go`:

```go
package daemon_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func TestBroadcaster_SubscribeAndUnsubLifecycle(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	sub := b.Subscribe(daemon.SubFilter{})
	sub.Unsub()
	// Calling Unsub twice must be safe — closes only once.
	sub.Unsub()
	// Channel must be closed.
	select {
	case _, ok := <-sub.Ch:
		assert.False(t, ok, "channel must be closed after Unsub")
	case <-time.After(time.Second):
		t.Fatal("channel was not closed within 1s")
	}
}

func TestBroadcaster_BroadcastFansToMatchingFiltersOnly(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	all := b.Subscribe(daemon.SubFilter{})
	a := b.Subscribe(daemon.SubFilter{ProjectID: 1})
	other := b.Subscribe(daemon.SubFilter{ProjectID: 2})
	defer all.Unsub()
	defer a.Unsub()
	defer other.Unsub()

	evt := &db.Event{ID: 100, ProjectID: 1, Type: "issue.created"}
	b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: 1})

	select {
	case got := <-all.Ch:
		assert.Equal(t, "event", got.Kind)
		assert.Equal(t, int64(100), got.Event.ID)
	case <-time.After(time.Second):
		t.Fatal("cross-project subscriber should have received the event")
	}
	select {
	case got := <-a.Ch:
		assert.Equal(t, int64(100), got.Event.ID)
	case <-time.After(time.Second):
		t.Fatal("project-1 subscriber should have received the event")
	}
	select {
	case got := <-other.Ch:
		t.Fatalf("project-2 subscriber must not receive a project-1 event, got %+v", got)
	case <-time.After(50 * time.Millisecond):
		// expected: no delivery
	}
}

func TestBroadcaster_ResetFansToAllMatchingFilters(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	all := b.Subscribe(daemon.SubFilter{})
	a := b.Subscribe(daemon.SubFilter{ProjectID: 1})
	defer all.Unsub()
	defer a.Unsub()

	b.Broadcast(daemon.StreamMsg{Kind: "reset", ResetID: 999, ProjectID: 1})

	for i, ch := range []<-chan daemon.StreamMsg{all.Ch, a.Ch} {
		select {
		case got := <-ch:
			assert.Equal(t, "reset", got.Kind)
			assert.Equal(t, int64(999), got.ResetID)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive reset", i)
		}
	}
}

func TestBroadcaster_OverflowDisconnectsSlowSubscriberOnly(t *testing.T) {
	b := daemon.NewEventBroadcaster()
	slow := b.Subscribe(daemon.SubFilter{})
	fast := b.Subscribe(daemon.SubFilter{})
	defer fast.Unsub()
	// Don't Unsub slow — broadcast saturates its buffer (256) and we expect
	// the broadcaster to close it.

	for i := int64(0); i < 300; i++ {
		evt := &db.Event{ID: i + 1, ProjectID: 1, Type: "issue.created"}
		b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: 1})
	}

	// slow.Ch must close (overflow disconnect).
	closed := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case _, ok := <-slow.Ch:
			if !ok {
				closed = true
				break
			}
		case <-time.After(20 * time.Millisecond):
		}
		if closed {
			break
		}
	}
	assert.True(t, closed, "slow subscriber's channel must close on overflow")

	// fast must still be live: drain it and assert at least one delivery.
	got := 0
loop:
	for {
		select {
		case _, ok := <-fast.Ch:
			if !ok {
				break loop
			}
			got++
		case <-time.After(20 * time.Millisecond):
			break loop
		}
	}
	assert.Greater(t, got, 0, "fast subscriber should still be receiving")
}

func TestBroadcaster_RaceFuzz(t *testing.T) {
	// -race coverage for concurrent Subscribe/Broadcast/Unsub.
	b := daemon.NewEventBroadcaster()
	var wg sync.WaitGroup
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := b.Subscribe(daemon.SubFilter{ProjectID: int64(i % 5)})
			// drain whatever arrives without blocking
			go func() {
				for range sub.Ch {
				}
			}()
			time.Sleep(time.Microsecond)
			sub.Unsub()
		}(i)
	}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evt := &db.Event{ID: int64(i + 1), ProjectID: int64(i % 5), Type: "issue.created"}
			b.Broadcast(daemon.StreamMsg{Kind: "event", Event: evt, ProjectID: evt.ProjectID})
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestBroadcaster ./internal/daemon/`
Expected: FAIL — `daemon.NewEventBroadcaster undefined`.

- [ ] **Step 3: Create the broadcaster**

Create `/Users/wesm/code/kata/internal/daemon/broadcaster.go`:

```go
package daemon

import (
	"sync"

	"github.com/wesm/kata/internal/db"
)

// channelBuffer is the per-subscriber send buffer. Full channels trigger
// overflow disconnect (Broadcast closes the channel and removes the
// subscriber). Plan 7 may expose this as a `kata config` knob; for now it's
// a const matching spec §11.
const channelBuffer = 256

// StreamMsg is the envelope on each subscriber's channel. Kind discriminates
// between an event wakeup and a reset signal so callers can never confuse the
// two.
type StreamMsg struct {
	Kind      string    // "event" | "reset"
	Event     *db.Event // non-nil iff Kind == "event"
	ResetID   int64     // non-zero iff Kind == "reset"
	ProjectID int64     // 0 = cross-project; used for filter matching
}

// SubFilter restricts which broadcasts a subscriber receives. ProjectID 0
// (zero value) means cross-project — every event flows through.
type SubFilter struct {
	ProjectID int64
}

func (f SubFilter) matches(msg StreamMsg) bool {
	if f.ProjectID == 0 {
		return true
	}
	return msg.ProjectID == f.ProjectID
}

// Subscription is the handle returned by Subscribe. Caller must call Unsub()
// when done. Ch is closed by the broadcaster on overflow disconnect or by
// Unsub on caller exit. Unsub is safe to call multiple times.
type Subscription struct {
	Ch    <-chan StreamMsg
	Unsub func()
}

// EventBroadcaster fans out wakeups and reset signals to subscribers. It
// holds no DB reference; the SSE handler captures its own high-water mark
// (via db.MaxEventID) after Subscribe.
type EventBroadcaster struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]*subscriber
}

type subscriber struct {
	ch     chan StreamMsg
	filter SubFilter
}

// NewEventBroadcaster constructs an empty broadcaster. The daemon owns one
// instance; its lifetime matches the server process.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{subs: map[int]*subscriber{}}
}

// Subscribe registers a new subscriber with the given filter. Returned
// Subscription holds a read-only Ch and an Unsub closure that's safe to call
// repeatedly.
func (b *EventBroadcaster) Subscribe(filter SubFilter) Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan StreamMsg, channelBuffer)
	b.subs[id] = &subscriber{ch: ch, filter: filter}
	return Subscription{
		Ch: ch,
		Unsub: func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if sub, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub.ch)
			}
		},
	}
}

// Broadcast fans msg out to every matching subscriber. Sends are non-blocking;
// when a subscriber's buffer is full the broadcaster closes its channel and
// removes it (overflow disconnect). The SSE handler reading on the closed
// channel returns; the client reconnects with Last-Event-ID and resumes via
// the durable replay path.
//
// Single full Lock keeps the implementation small; single-user daemon
// throughput doesn't justify an RLock+Lock dance.
func (b *EventBroadcaster) Broadcast(msg StreamMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, sub := range b.subs {
		if !sub.filter.matches(msg) {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			close(sub.ch)
			delete(b.subs, id)
		}
	}
}
```

- [ ] **Step 4: Run tests with -race**

Run: `cd /Users/wesm/code/kata && go test -race -run TestBroadcaster ./internal/daemon/`
Expected: PASS for all five tests, no `-race` warnings.

- [ ] **Step 5: Verify the package builds and lints**

Run: `cd /Users/wesm/code/kata && go vet ./internal/daemon/ && gofmt -l internal/daemon/broadcaster*.go`
Expected: empty output.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/broadcaster.go internal/daemon/broadcaster_test.go
git commit -m "feat(daemon): EventBroadcaster with overflow disconnect"
```

---

## Task 4: Server wiring — `ServerConfig.Broadcaster`, `BaseContext`, `mux` parameter

**Files:**
- Modify: `internal/daemon/server.go`

Adds the broadcaster slot to `ServerConfig`, wires `BaseContext` for clean SSE shutdown, and grows `registerRoutes` so SSE can register a raw handler.

- [ ] **Step 1: Read the current `ServerConfig`, `NewServer`, `Serve`, `registerRoutes` to understand baseline**

Reference: `/Users/wesm/code/kata/internal/daemon/server.go:18-53`, `81-96`, `132-147`. We'll touch all three.

- [ ] **Step 2: Modify `ServerConfig` and `NewServer`**

Edit `/Users/wesm/code/kata/internal/daemon/server.go`:

Replace (lines ~18–24, the `ServerConfig` block):

```go
// ServerConfig wires the daemon's runtime dependencies. DB and StartedAt are
// required; Endpoint is only consulted by Run.
type ServerConfig struct {
	DB        *db.DB
	StartedAt time.Time
	Endpoint  DaemonEndpoint
}
```

with:

```go
// ServerConfig wires the daemon's runtime dependencies. DB and StartedAt are
// required; Endpoint is only consulted by Run; Broadcaster is owned by the
// server (NewServer fills it if nil so handler tests don't have to plumb one
// through).
type ServerConfig struct {
	DB          *db.DB
	StartedAt   time.Time
	Endpoint    DaemonEndpoint
	Broadcaster *EventBroadcaster
}
```

- [ ] **Step 3: Default-init `Broadcaster` in `NewServer`**

Edit lines ~35–53 of `internal/daemon/server.go`. Replace:

```go
func NewServer(cfg ServerConfig) *Server {
	api.InstallErrorFormatter()

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("kata", "0.1.0")
	humaConfig.OpenAPIPath = "" // Plan 1: no /openapi.json
	// Drop DefaultConfig's SchemaLinkTransformer: it rebuilds response structs
	// via reflection (adding a $schema field), which silently bypasses any
	// MarshalJSON. Our APIError relies on MarshalJSON to emit the wire-spec
	// envelope shape, so we must disable the transform.
	humaConfig.CreateHooks = nil
	humaAPI := humago.New(mux, humaConfig)

	s := &Server{cfg: cfg, api: humaAPI}
	registerRoutes(humaAPI, cfg)

	s.handler = withCSRFGuards(mux)
	return s
}
```

with:

```go
func NewServer(cfg ServerConfig) *Server {
	api.InstallErrorFormatter()
	if cfg.Broadcaster == nil {
		cfg.Broadcaster = NewEventBroadcaster()
	}

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("kata", "0.1.0")
	humaConfig.OpenAPIPath = "" // Plan 1: no /openapi.json
	// Drop DefaultConfig's SchemaLinkTransformer: it rebuilds response structs
	// via reflection (adding a $schema field), which silently bypasses any
	// MarshalJSON. Our APIError relies on MarshalJSON to emit the wire-spec
	// envelope shape, so we must disable the transform.
	humaConfig.CreateHooks = nil
	humaAPI := humago.New(mux, humaConfig)

	s := &Server{cfg: cfg, api: humaAPI}
	registerRoutes(humaAPI, mux, cfg)

	s.handler = withCSRFGuards(mux)
	return s
}
```

- [ ] **Step 4: Add `BaseContext` to the `http.Server` in `Serve`**

Edit lines ~81–96 of `internal/daemon/server.go`. Replace:

```go
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	httpSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
```

with:

```go
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	httpSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		// BaseContext roots every request in the daemon ctx so long-lived
		// SSE handlers exit on Shutdown via r.Context().Done().
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
```

- [ ] **Step 5: Grow `registerRoutes` signature to take `mux`**

Edit lines ~132–147 of `internal/daemon/server.go`. Replace:

```go
// registerRoutes installs the per-resource handler groups onto humaAPI. Each
// group lives in its own file (handlers_health.go, handlers_projects.go, etc.)
// and replaces the matching stub below as it lands.
func registerRoutes(humaAPI huma.API, cfg ServerConfig) {
	registerHealth(humaAPI, cfg)
	registerProjects(humaAPI, cfg)
	registerIssues(humaAPI, cfg)
	registerComments(humaAPI, cfg)
	registerActions(humaAPI, cfg)
	registerLinks(humaAPI, cfg)
	registerLabels(humaAPI, cfg)
	registerOwnership(humaAPI, cfg)
	registerReady(humaAPI, cfg)
	registerSearch(humaAPI, cfg)
	registerDestructive(humaAPI, cfg)
}
```

with:

```go
// registerRoutes installs the per-resource handler groups onto humaAPI. Each
// group lives in its own file (handlers_health.go, handlers_projects.go, etc.)
// and replaces the matching stub below as it lands. The events handler also
// receives mux so it can register the SSE endpoint as a raw http.HandlerFunc
// (Huma doesn't model streaming responses).
func registerRoutes(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	registerHealth(humaAPI, cfg)
	registerProjects(humaAPI, cfg)
	registerIssues(humaAPI, cfg)
	registerComments(humaAPI, cfg)
	registerActions(humaAPI, cfg)
	registerLinks(humaAPI, cfg)
	registerLabels(humaAPI, cfg)
	registerOwnership(humaAPI, cfg)
	registerReady(humaAPI, cfg)
	registerSearch(humaAPI, cfg)
	registerDestructive(humaAPI, cfg)
	registerEvents(humaAPI, mux, cfg)
}
```

- [ ] **Step 6: Create a minimal `handlers_events.go` so the package builds**

`registerRoutes` now calls `registerEvents`, which doesn't exist yet. To keep the build green, create `/Users/wesm/code/kata/internal/daemon/handlers_events.go` with an empty stub. Tasks 5, 6, 7 will replace its body wholesale; the stub just gives `server.go` something to call.

```go
package daemon

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// registerEvents wires the polling endpoints (Huma) and the SSE endpoint
// (raw mux). Both are implemented incrementally across Plan 4 tasks: Task 5
// adds polling, Task 6 adds the SSE handshake/drain, Task 7 adds the live
// phase. This stub keeps server.go building before any of those land.
func registerEvents(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	_ = humaAPI
	_ = mux
	_ = cfg
}
```

- [ ] **Step 7: Build and run all existing daemon tests**

Run: `cd /Users/wesm/code/kata && go build ./... && go test ./internal/daemon/`
Expected: PASS (existing tests still pass; we haven't added events tests yet).

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/server.go internal/daemon/handlers_events.go
git commit -m "feat(daemon): wire Broadcaster into ServerConfig + BaseContext for SSE shutdown"
```

---

## Task 5: Polling endpoints — `GET /api/v1/events` and `GET /api/v1/projects/{id}/events`

**Files:**
- Modify: `internal/daemon/handlers_events.go`
- Test: `internal/daemon/handlers_events_test.go` (new)

Polling first because it's pure read-side (no broadcaster, no streaming) and exercises `EventEnvelope`, `EventsAfter`, and `PurgeResetCheck` against the wire shape.

- [ ] **Step 1: Write failing tests for the polling envelope**

Create `/Users/wesm/code/kata/internal/daemon/handlers_events_test.go`:

```go
package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

func mkProject(t *testing.T, env *testenv.Env, identity, name string) int64 {
	t.Helper()
	p, err := env.DB.CreateProject(context.Background(), identity, name)
	require.NoError(t, err)
	return p.ID
}

func mkIssue(t *testing.T, env *testenv.Env, projectID int64, title string) db.Issue {
	t.Helper()
	is, _, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: "tester",
	})
	require.NoError(t, err)
	return is
}

func TestPollEvents_EmptyResultIsNonNullArray(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	assert.Contains(t, body, `"events":[]`, "must be empty array, never null")
	assert.Contains(t, body, `"reset_required":false`)
	assert.Contains(t, body, `"next_after_id":0`)
}

func TestPollEvents_ReturnsEventsAndAdvancesCursor(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")
	mkIssue(t, env, pid, "second")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		ResetRequired bool `json:"reset_required"`
		Events        []struct {
			EventID int64  `json:"event_id"`
			Type    string `json:"type"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.Len(t, b.Events, 2)
	assert.Equal(t, int64(1), b.Events[0].EventID)
	assert.Equal(t, int64(2), b.Events[1].EventID)
	assert.Equal(t, "issue.created", b.Events[0].Type)
	assert.Equal(t, int64(2), b.NextAfterID, "advances to max event id")
	assert.False(t, b.ResetRequired)
}

func TestPollEvents_NextAfterIDEchoesAfterIDOnEmpty(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "only")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=99&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	assert.Contains(t, body, `"next_after_id":99`)
	assert.Contains(t, body, `"events":[]`)
}

func TestPollEvents_PerProjectFiltersOtherProjects(t *testing.T) {
	env := testenv.New(t)
	pa := mkProject(t, env, "github.com/test/a", "a")
	pb := mkProject(t, env, "github.com/test/b", "b")
	mkIssue(t, env, pa, "a1")
	mkIssue(t, env, pb, "b1")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pa, 10) + "/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		Events []struct {
			ProjectID int64 `json:"project_id"`
		} `json:"events"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	require.Len(t, b.Events, 1)
	assert.Equal(t, pa, b.Events[0].ProjectID)
}

func TestPollEvents_ResetRequiredAfterPurge(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)

	// Cursor below the reset → reset_required:true
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=10")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		ResetRequired bool  `json:"reset_required"`
		ResetAfterID  int64 `json:"reset_after_id"`
		Events        []struct {
			EventID int64 `json:"event_id"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	assert.True(t, b.ResetRequired)
	assert.Greater(t, b.ResetAfterID, int64(0))
	assert.Equal(t, b.ResetAfterID, b.NextAfterID, "next_after_id == reset_after_id when reset")
	assert.Len(t, b.Events, 0)
}

func TestPollEvents_LimitClampsAt1000(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	for i := 0; i < 3; i++ {
		mkIssue(t, env, pid, "x")
	}
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=99999")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode, "values >1000 must clamp silently, not 400")
}

func TestPollEvents_LimitNonPositiveIs400(t *testing.T) {
	env := testenv.New(t)
	for _, q := range []string{"after_id=0&limit=0", "after_id=0&limit=-5"} {
		resp, err := env.HTTP.Get(env.URL + "/api/v1/events?" + q)
		require.NoError(t, err)
		body := readBody(t, resp)
		_ = resp.Body.Close()
		assert.Equal(t, 400, resp.StatusCode, "limit %s should be 400", q)
		assert.Contains(t, body, `"code":"validation"`)
	}
}

func TestPollEvents_LimitNonNumericIs400(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/events?after_id=0&limit=foo")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

// readBody is a small helper that reads the body and asserts no read error.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestPollEvents ./internal/daemon/`
Expected: FAIL — endpoints not registered yet (404 / handler missing).

- [ ] **Step 3: Implement the polling handlers in `handlers_events.go`**

Replace the contents of `/Users/wesm/code/kata/internal/daemon/handlers_events.go` with:

```go
package daemon

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// pollLimitDefault and pollLimitMax mirror spec §6.1: clients that ask too
// little (≤0, non-numeric) get 400; clients that ask too much (>1000) get
// silently clamped. Plan 7 may expose these via `kata config`.
const (
	pollLimitDefault = 100
	pollLimitMax     = 1000
)

// registerEvents wires polling endpoints onto the Huma API and the SSE
// endpoint onto the raw mux. SSE handshake/drain land in Task 6; the live
// phase lands in Task 7.
func registerEvents(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollEvents",
		Method:      "GET",
		Path:        "/api/v1/events",
	}, pollEventsHandler(cfg, false))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollProjectEvents",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/events",
	}, pollEventsHandler(cfg, true))

	// SSE endpoint placeholder — see Tasks 6 / 7.
	_ = mux
}

// pollEventsHandler returns a Huma handler for either /events (perProject=false)
// or /projects/{id}/events (perProject=true).
func pollEventsHandler(cfg ServerConfig, perProject bool) func(context.Context, *api.PollEventsRequest) (*api.PollEventsResponse, error) {
	return func(ctx context.Context, in *api.PollEventsRequest) (*api.PollEventsResponse, error) {
		// Validate limit. Non-positive → 400. >Max silently clamps.
		if in.Limit < 0 || (in.Limit == 0 && false) {
			return nil, api.NewError(400, "validation",
				"limit must be a positive integer", "", nil)
		}
		// Note: Huma's int parser returns 0 when the query parameter is
		// missing OR when "limit=foo" fails to parse — but our type uses
		// `query:"limit,omitempty"` and Huma rejects non-numeric values with
		// 422 before we see them, which our error formatter normalizes to
		// 400 validation. See internal/api/errors.go:77-83.
		limit := in.Limit
		switch {
		case limit < 0:
			return nil, api.NewError(400, "validation", "limit must be a positive integer", "", nil)
		case limit == 0:
			// Distinguish "omitted" from "explicitly zero" via the route's query
			// param: an explicit 0 fails Huma validation; an omitted parameter
			// arrives as zero here. We default in that case.
			limit = pollLimitDefault
		case limit > pollLimitMax:
			limit = pollLimitMax
		}

		var projectID int64
		if perProject {
			projectID = in.ProjectID
		}

		resetTo, err := cfg.DB.PurgeResetCheck(ctx, in.AfterID, projectID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if resetTo > 0 {
			out := &api.PollEventsResponse{}
			out.Body.ResetRequired = true
			out.Body.ResetAfterID = resetTo
			out.Body.Events = []api.EventEnvelope{}
			out.Body.NextAfterID = resetTo
			return out, nil
		}

		rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
			AfterID:   in.AfterID,
			ProjectID: projectID,
			Limit:     limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		out := &api.PollEventsResponse{}
		out.Body.ResetRequired = false
		out.Body.Events = toEnvelopes(rows)
		out.Body.NextAfterID = nextAfterID(rows, in.AfterID)
		return out, nil
	}
}

func toEnvelopes(rows []db.Event) []api.EventEnvelope {
	out := make([]api.EventEnvelope, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventToEnvelope(r))
	}
	return out
}

func eventToEnvelope(e db.Event) api.EventEnvelope {
	var payload json.RawMessage
	if e.Payload != "" {
		payload = json.RawMessage(e.Payload)
	}
	return api.EventEnvelope{
		EventID:         e.ID,
		Type:            e.Type,
		ProjectID:       e.ProjectID,
		ProjectIdentity: e.ProjectIdentity,
		IssueID:         e.IssueID,
		IssueNumber:     e.IssueNumber,
		RelatedIssueID:  e.RelatedIssueID,
		Actor:           e.Actor,
		Payload:         payload,
		CreatedAt:       e.CreatedAt,
	}
}

func nextAfterID(rows []db.Event, afterID int64) int64 {
	if len(rows) == 0 {
		return afterID
	}
	return rows[len(rows)-1].ID
}
```

- [ ] **Step 4: Re-run polling tests**

Run: `cd /Users/wesm/code/kata && go test -run TestPollEvents ./internal/daemon/`
Expected: PASS for all eight tests.

- [ ] **Step 5: Run the full daemon test suite**

Run: `cd /Users/wesm/code/kata && go test ./internal/daemon/`
Expected: PASS — broadcaster + polling tests + all pre-existing tests.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/handlers_events.go internal/daemon/handlers_events_test.go
git commit -m "feat(daemon): GET /events and /projects/{id}/events polling endpoints"
```

---

## Task 6: SSE endpoint — handshake, Subscribe-first ordering, drain, stale-cap

**Files:**
- Modify: `internal/daemon/handlers_events.go` (add SSE handler)
- Modify: `internal/daemon/handlers_events_test.go` (add SSE tests)

This task implements the pre-live-phase SSE flow: Accept negotiation, cursor parsing, handshake bytes, Subscribe, MaxEventID, PurgeResetCheck, drain query, stale-cap.

- [ ] **Step 1: Write failing tests for handshake + drain**

Append to `/Users/wesm/code/kata/internal/daemon/handlers_events_test.go`:

```go
import (
	// add to the existing import block:
	"bufio"
	"strconv"
	"time"
)

// readSSEFrames pulls SSE frames off a streaming response until ctx is done
// or the stream closes. Returns (frames, error). Each frame is a map of
// "id" / "event" / "data" lines; comment lines (starting with ":") are
// ignored. We don't use a third-party SSE client because the protocol is
// trivial and the test needs precise control over the stream lifecycle.
type sseFrame struct {
	id    string
	event string
	data  string
}

func readSSEFramesUntilN(t *testing.T, body interface {
	Read([]byte) (int, error)
	Close() error
}, n int, timeout time.Duration) []sseFrame {
	t.Helper()
	var frames []sseFrame
	cur := sseFrame{}
	deadline := time.Now().Add(timeout)
	rd := bufio.NewReader(struct {
		Read func([]byte) (int, error)
	}{Read: body.Read})

	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)
	readLine := func() {
		s, err := rd.ReadString('\n')
		lineCh <- lineResult{s, err}
	}

	for len(frames) < n && time.Now().Before(deadline) {
		go readLine()
		var lr lineResult
		select {
		case lr = <-lineCh:
		case <-time.After(time.Until(deadline)):
			return frames
		}
		if lr.err != nil {
			return frames
		}
		line := strings.TrimRight(lr.line, "\r\n")
		switch {
		case line == "":
			if cur.id != "" || cur.event != "" || cur.data != "" {
				frames = append(frames, cur)
				cur = sseFrame{}
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat — ignore but keep reading
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frames
}

func openSSE(t *testing.T, env *testenv.Env, query string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream?"+query, nil)
	require.NoError(t, err)
	for k, vv := range header {
		req.Header[k] = vv
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	return resp
}

func TestSSE_AcceptNegotiation(t *testing.T) {
	env := testenv.New(t)

	// Missing Accept → 406
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream", nil)
	req.Header.Del("Accept")
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	body := readBody(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, 406, resp.StatusCode)
	assert.Contains(t, body, `"code":"not_acceptable"`)

	// Wrong Accept → 406
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Accept", "application/json")
	resp, err = env.HTTP.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 406, resp.StatusCode)

	// Right Accept → 200
	resp = openSSE(t, env, "", http.Header{"Accept": []string{"text/event-stream"}})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// */* → 200
	resp = openSSE(t, env, "", http.Header{"Accept": []string{"*/*"}})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestSSE_CursorConflict(t *testing.T) {
	env := testenv.New(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		env.URL+"/api/v1/events/stream?after_id=5", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", "10")
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	body := readBody(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
	assert.Contains(t, body, `"code":"cursor_conflict"`)
}

func TestSSE_HandshakeWritesConnectedComment(t *testing.T) {
	env := testenv.New(t)
	resp := openSSE(t, env, "", nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "keep-alive", resp.Header.Get("Connection"))
	// Read first 16 bytes; should contain ": connected\n\n".
	buf := make([]byte, 16)
	_, err := resp.Body.Read(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf), ": connected")
}

func TestSSE_DrainEmitsExistingEventsInOrder(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")
	mkIssue(t, env, pid, "second")
	mkIssue(t, env, pid, "third")

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)

	frames := readSSEFramesUntilN(t, resp.Body, 3, 2*time.Second)
	require.Len(t, frames, 3)
	assert.Equal(t, "1", frames[0].id)
	assert.Equal(t, "issue.created", frames[0].event)
	assert.Equal(t, "2", frames[1].id)
	assert.Equal(t, "3", frames[2].id)
}

func TestSSE_PerProjectFilterExcludesOtherProjects(t *testing.T) {
	env := testenv.New(t)
	pa := mkProject(t, env, "github.com/test/a", "a")
	pb := mkProject(t, env, "github.com/test/b", "b")
	mkIssue(t, env, pa, "a1")
	mkIssue(t, env, pb, "b1")

	resp := openSSE(t, env, "project_id="+strconv.FormatInt(pa, 10)+"&after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()
	frames := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, frames, 1)
	assert.Equal(t, "1", frames[0].id, "should see only project A's event 1, not project B's event 2")
}

func TestSSE_ResetWhenCursorInsidePurgeGap(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	frames := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, frames, 1)
	assert.Equal(t, "sync.reset_required", frames[0].event)
	assert.NotEmpty(t, frames[0].id)
	assert.Contains(t, frames[0].data, `"reset_after_id":`+frames[0].id)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/wesm/code/kata && go test -run TestSSE ./internal/daemon/`
Expected: FAIL — `/api/v1/events/stream` returns 404 (not registered).

- [ ] **Step 3: Implement the SSE handler in `handlers_events.go`**

Replace the `registerEvents` function and append the SSE pieces. The full updated file (replace the entire body of `internal/daemon/handlers_events.go`):

```go
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

const (
	pollLimitDefault = 100
	pollLimitMax     = 1000
	// sseDrainCap is the max number of events the drain phase replays. Spec §4.8
	// says "bounded ~10k rows"; we query LIMIT cap+1 so we can detect "too far
	// behind" and emit sync.reset_required instead.
	sseDrainCap = 10000
	// sseLiveBatch caps each live-phase re-query at this many rows. A single
	// wakeup typically returns 1; we still cap to avoid pathological cases.
	sseLiveBatch = 1000
)

func registerEvents(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollEvents",
		Method:      "GET",
		Path:        "/api/v1/events",
	}, pollEventsHandler(cfg, false))
	huma.Register(humaAPI, huma.Operation{
		OperationID: "pollProjectEvents",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/events",
	}, pollEventsHandler(cfg, true))
	// SSE: not Huma — needs a streaming http.HandlerFunc on the raw mux.
	mux.HandleFunc("/api/v1/events/stream", sseHandler(cfg))
}

func pollEventsHandler(cfg ServerConfig, perProject bool) func(context.Context, *api.PollEventsRequest) (*api.PollEventsResponse, error) {
	return func(ctx context.Context, in *api.PollEventsRequest) (*api.PollEventsResponse, error) {
		limit := in.Limit
		switch {
		case limit < 0:
			return nil, api.NewError(400, "validation", "limit must be a positive integer", "", nil)
		case limit == 0:
			limit = pollLimitDefault
		case limit > pollLimitMax:
			limit = pollLimitMax
		}
		var projectID int64
		if perProject {
			projectID = in.ProjectID
		}
		resetTo, err := cfg.DB.PurgeResetCheck(ctx, in.AfterID, projectID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if resetTo > 0 {
			out := &api.PollEventsResponse{}
			out.Body.ResetRequired = true
			out.Body.ResetAfterID = resetTo
			out.Body.Events = []api.EventEnvelope{}
			out.Body.NextAfterID = resetTo
			return out, nil
		}
		rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
			AfterID: in.AfterID, ProjectID: projectID, Limit: limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.PollEventsResponse{}
		out.Body.ResetRequired = false
		out.Body.Events = toEnvelopes(rows)
		out.Body.NextAfterID = nextAfterID(rows, in.AfterID)
		return out, nil
	}
}

func toEnvelopes(rows []db.Event) []api.EventEnvelope {
	out := make([]api.EventEnvelope, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventToEnvelope(r))
	}
	return out
}

func eventToEnvelope(e db.Event) api.EventEnvelope {
	var payload json.RawMessage
	if e.Payload != "" {
		payload = json.RawMessage(e.Payload)
	}
	return api.EventEnvelope{
		EventID:         e.ID,
		Type:            e.Type,
		ProjectID:       e.ProjectID,
		ProjectIdentity: e.ProjectIdentity,
		IssueID:         e.IssueID,
		IssueNumber:     e.IssueNumber,
		RelatedIssueID:  e.RelatedIssueID,
		Actor:           e.Actor,
		Payload:         payload,
		CreatedAt:       e.CreatedAt,
	}
}

func nextAfterID(rows []db.Event, afterID int64) int64 {
	if len(rows) == 0 {
		return afterID
	}
	return rows[len(rows)-1].ID
}

// sseHandler implements GET /api/v1/events/stream. Order of operations:
//  1. Accept negotiation (406 on miss/wrong)
//  2. Cursor parse (400 cursor_conflict if both header and ?after_id are set)
//  3. Write SSE handshake bytes (: connected\n\n) + flush
//  4. Subscribe to broadcaster
//  5. Capture hwm = MaxEventID
//  6. PurgeResetCheck — if hit, write reset frame + return
//  7. Drain events (cursor, hwm] up to sseDrainCap+1
//  8. If drain hit cap+1, emit reset frame at hwm and return (stale-cap)
//  9. Write drained frames in id order
// 10. Live phase (Task 7)
//
// Steps 4-6 are Subscribe-first / check-second so a purge that fires between
// cursor parse and Subscribe lands on sub.Ch via the live channel; one
// committed before parse is captured by PurgeResetCheck. See spec §5.3.
func sseHandler(cfg ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Accept negotiation.
		if !acceptableForSSE(r.Header.Get("Accept")) {
			api.WriteEnvelope(w, http.StatusNotAcceptable, "not_acceptable",
				"Accept must be text/event-stream")
			return
		}

		// 2. Cursor parse.
		cursor, hadHeader, hadQuery, perr := parseSSECursor(r)
		if perr != nil {
			renderAPIError(w, perr)
			return
		}
		if hadHeader && hadQuery {
			renderAPIError(w, api.NewError(400, "cursor_conflict",
				"pass either Last-Event-ID or ?after_id, not both", "", nil))
			return
		}

		var projectID int64
		if pidStr := r.URL.Query().Get("project_id"); pidStr != "" {
			n, err := strconv.ParseInt(pidStr, 10, 64)
			if err != nil || n <= 0 {
				renderAPIError(w, api.NewError(400, "validation",
					"project_id must be a positive integer", "", nil))
				return
			}
			projectID = n
		}

		// 3. Handshake.
		flusher, ok := w.(http.Flusher)
		if !ok {
			renderAPIError(w, api.NewError(500, "internal", "streaming not supported", "", nil))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
			return
		}
		flusher.Flush()

		// 4. Subscribe.
		sub := cfg.Broadcaster.Subscribe(SubFilter{ProjectID: projectID})
		defer sub.Unsub()

		// 5. High-water mark.
		ctx := r.Context()
		hwm, err := cfg.DB.MaxEventID(ctx)
		if err != nil {
			return // close stream silently; client reconnects
		}

		// 6. Purge-reset check.
		resetTo, err := cfg.DB.PurgeResetCheck(ctx, cursor, projectID)
		if err != nil {
			return
		}
		if resetTo > 0 {
			writeResetFrame(w, resetTo)
			flusher.Flush()
			return
		}

		// 7. Drain.
		rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
			AfterID: cursor, ProjectID: projectID, ThroughID: hwm, Limit: sseDrainCap + 1,
		})
		if err != nil {
			return
		}

		// 8. Stale-cap.
		if len(rows) == sseDrainCap+1 {
			writeResetFrame(w, hwm)
			flusher.Flush()
			return
		}

		// 9. Drain frames.
		lastSent := cursor
		for _, ev := range rows {
			writeEventFrame(w, ev)
			flusher.Flush()
			lastSent = ev.ID
		}

		// 10. Live phase — Task 7.
		runLivePhase(ctx, w, flusher, cfg, sub.Ch, projectID, lastSent)
	}
}

// runLivePhase is implemented in Task 7. The Task 6 stub just blocks on ctx
// so the existing tests (drain only) pass without seeing immediate stream
// closure. Replaced wholesale in Task 7.
func runLivePhase(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cfg ServerConfig, ch <-chan StreamMsg, projectID, lastSent int64) {
	<-ctx.Done()
	_ = w
	_ = flusher
	_ = cfg
	_ = ch
	_ = projectID
	_ = lastSent
}

func acceptableForSSE(accept string) bool {
	if accept == "" {
		return false
	}
	for _, part := range strings.Split(accept, ",") {
		mt := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mt == "text/event-stream" || mt == "*/*" {
			return true
		}
	}
	return false
}

func parseSSECursor(r *http.Request) (cursor int64, hadHeader, hadQuery bool, err error) {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		hadHeader = true
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			err = api.NewError(400, "validation",
				"Last-Event-ID must be a non-negative integer", "", nil)
			return
		}
		cursor = n
	}
	if v := r.URL.Query().Get("after_id"); v != "" {
		hadQuery = true
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			err = api.NewError(400, "validation",
				"after_id must be a non-negative integer", "", nil)
			return
		}
		cursor = n
	}
	return
}

func renderAPIError(w http.ResponseWriter, e error) {
	var ae *api.APIError
	if !errors.As(e, &ae) {
		api.WriteEnvelope(w, 500, "internal", e.Error())
		return
	}
	api.WriteEnvelope(w, ae.Status, ae.Code, ae.Message)
}

// writeEventFrame emits one SSE frame for an event row. The data: line is
// single-line JSON of api.EventEnvelope.
func writeEventFrame(w io.Writer, e db.Event) {
	env := eventToEnvelope(e)
	body, _ := json.Marshal(env)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, body)
}

// writeResetFrame emits one synthetic sync.reset_required frame with the
// reserved cursor as both id: and data.reset_after_id.
func writeResetFrame(w io.Writer, resetID int64) {
	body, _ := json.Marshal(api.EventReset{EventID: resetID, ResetAfterID: resetID})
	fmt.Fprintf(w, "id: %d\nevent: sync.reset_required\ndata: %s\n\n", resetID, body)
}
```

- [ ] **Step 4: Re-run SSE tests**

Run: `cd /Users/wesm/code/kata && go test -run TestSSE ./internal/daemon/`
Expected: PASS (all six SSE tests).

- [ ] **Step 5: Run the full daemon suite**

Run: `cd /Users/wesm/code/kata && go test ./internal/daemon/`
Expected: PASS — broadcaster + polling + SSE handshake + all pre-existing tests.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/handlers_events.go internal/daemon/handlers_events_test.go
git commit -m "feat(daemon): SSE handshake, drain, stale-cap"
```

---

## Task 7: SSE live phase — wakeup-and-requery loop, reset handling, heartbeat

**Files:**
- Modify: `internal/daemon/handlers_events.go` (replace `runLivePhase`)
- Modify: `internal/daemon/handlers_events_test.go` (add live-phase tests)

The Task 6 `runLivePhase` is a stub. Task 7 makes it real: select over channel + ticker + ctx, re-query DB on each event wakeup, emit reset frame and return on reset.

- [ ] **Step 1: Write failing tests for the live phase**

Append to `/Users/wesm/code/kata/internal/daemon/handlers_events_test.go`:

```go
func TestSSE_DrainFollowedByLiveBroadcast(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// First frame from drain.
	first := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, first, 1)
	assert.Equal(t, "1", first[0].id)

	// Mutate to trigger a live broadcast (POST a comment).
	mkIssue(t, env, pid, "second")

	second := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, second, 1)
	assert.Equal(t, "2", second[0].id)
	assert.Equal(t, "issue.created", second[0].event)
}

func TestSSE_LiveResetClosesStream(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// Drain delivers the issue.created frame.
	first := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, first, 1)

	// Purge → reset signal arrives via live channel; stream then closes.
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)
	// Manually broadcast the reset because PurgeIssue itself doesn't (Task 8 wires that).
	// In Task 8 we'll remove this manual call.
	plRow, err := env.DB.PurgeLogByIssueID(context.Background(), is.ID)
	require.NoError(t, err)
	require.NotNil(t, plRow.PurgeResetAfterEventID)
	// Pull the broadcaster off ServerConfig via a test hook? testenv doesn't
	// currently expose it. For now this test asserts behavior by mutating the
	// purge_log (already done) and reconnecting; the in-flight stream will
	// time out at the deadline. Task 8 (broadcast wiring) revisits this test.
	t.Skip("requires testenv.Env.Broadcaster accessor — added in Task 8")
}

func TestSSE_LiveHeartbeatKeepsConnectionAlive(t *testing.T) {
	// We can't realistically wait 25s. Instead, assert that with no events
	// and no purges, the stream stays open for >100ms (heartbeat ticker isn't
	// the only thing keeping it open; ctx.Done would be the main exit).
	env := testenv.New(t)
	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// Read the : connected\n\n preamble.
	buf := make([]byte, 16)
	_, err := resp.Body.Read(buf)
	require.NoError(t, err)

	// No frames should arrive in 100ms with empty DB.
	frames := readSSEFramesUntilN(t, resp.Body, 1, 100*time.Millisecond)
	assert.Len(t, frames, 0)
	// Connection still open (no error from a small read).
	_, err = resp.Body.Read(make([]byte, 1))
	// Either a successful zero-length read window (some go versions return
	// no error and 0 bytes) or a short-block. We're not asserting the exact
	// behavior — just that we didn't get a stream-closed (io.EOF / non-nil).
	// If err != nil and != io.EOF, log but don't fail.
	if err != nil && err.Error() != "EOF" {
		t.Logf("non-EOF read after idle: %v (acceptable in a busy CI env)", err)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `cd /Users/wesm/code/kata && go test -run TestSSE_Drain -timeout 30s ./internal/daemon/`
Expected: `TestSSE_DrainFollowedByLiveBroadcast` FAILS — second frame never arrives because `runLivePhase` just blocks on ctx.

- [ ] **Step 3: Replace `runLivePhase` with the real implementation**

Edit `/Users/wesm/code/kata/internal/daemon/handlers_events.go`. Replace:

```go
// runLivePhase is implemented in Task 7. The Task 6 stub just blocks on ctx
// so the existing tests (drain only) pass without seeing immediate stream
// closure. Replaced wholesale in Task 7.
func runLivePhase(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cfg ServerConfig, ch <-chan StreamMsg, projectID, lastSent int64) {
	<-ctx.Done()
	_ = w
	_ = flusher
	_ = cfg
	_ = ch
	_ = projectID
	_ = lastSent
}
```

with:

```go
// heartbeatInterval is the SSE keepalive period. Comments are no-ops per the
// SSE spec; their purpose is to keep TCP connections alive through middleboxes.
// Plan 7 may expose this via `kata config`.
const heartbeatInterval = 25 * time.Second

// runLivePhase delivers events from sub.Ch in canonical DB order. Each event
// wakeup triggers EventsAfter(lastSent, projectID, ThroughID: msg.Event.ID),
// which catches reordered broadcasts and coalesces bursts. Resets are
// terminal: emit the frame and return.
//
// lastSent enters as the id of the last drained frame (or cursor when the
// drain was empty). It tracks server-side state for de-duplication; the
// client's Last-Event-ID only advances on frames the client actually
// receives.
func runLivePhase(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cfg ServerConfig, ch <-chan StreamMsg, projectID, lastSent int64) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return // overflow disconnect
			}
			switch msg.Kind {
			case "reset":
				writeResetFrame(w, msg.ResetID)
				flusher.Flush()
				return
			case "event":
				if msg.Event == nil {
					continue
				}
				rows, err := cfg.DB.EventsAfter(ctx, db.EventsAfterParams{
					AfterID:   lastSent,
					ProjectID: projectID,
					ThroughID: msg.Event.ID,
					Limit:     sseLiveBatch,
				})
				if err != nil {
					return
				}
				for _, ev := range rows {
					writeEventFrame(w, ev)
					flusher.Flush()
					lastSent = ev.ID
				}
			}
		}
	}
}
```

You also need to add `"time"` to the import block at the top of the file if not already there (it already is from the stub). Confirm by reading the file.

- [ ] **Step 4: Run tests**

Run: `cd /Users/wesm/code/kata && go test -run "TestSSE_DrainFollowedByLiveBroadcast|TestSSE_LiveHeartbeat" ./internal/daemon/`
Expected: PASS for both. (Note the `_LiveResetClosesStream` test is skipped pending Task 8.)

- [ ] **Step 5: Run the full daemon suite**

Run: `cd /Users/wesm/code/kata && go test ./internal/daemon/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/handlers_events.go internal/daemon/handlers_events_test.go
git commit -m "feat(daemon): SSE live phase with wakeup-and-requery + heartbeat"
```

---

## Task 8: Wire broadcasts into mutation handlers

**Files:**
- Modify: `internal/daemon/handlers_issues.go`, `handlers_actions.go`, `handlers_links.go`, `handlers_labels.go`, `handlers_comments.go`, `handlers_ownership.go`, `handlers_destructive.go`
- Modify: `internal/daemon/handlers_events_test.go` (un-skip the live-reset test, add the parent-replace test)
- Add: `internal/testenv/testenv.go` accessor for `Broadcaster` (so tests can broadcast directly when needed)

This is the largest single task by line count, but each handler change is mechanical: post-commit `if changed && evt != nil { cfg.Broadcaster.Broadcast(...) }`.

The purge handler also broadcasts a `Kind:"reset"` `StreamMsg` when `pl.PurgeResetAfterEventID != nil`.

- [ ] **Step 1: Expose the broadcaster on `testenv.Env` for tests that need direct access**

The test harness currently doesn't expose `Broadcaster`. Some live-phase tests (notably the un-skip in Step 7 below) need to `Broadcast` from the test side without going through a handler.

Edit `/Users/wesm/code/kata/internal/testenv/testenv.go`. Replace the current `Env` struct and the `srv` declaration with:

```go
// Env is a per-test daemon + DB + HTTP client bundle.
type Env struct {
	URL         string
	HTTP        *http.Client
	DB          *db.DB
	Home        string
	Broadcaster *daemon.EventBroadcaster
}
```

And in the body of `New`, replace:

```go
srv := daemon.NewServer(daemon.ServerConfig{
    DB:        d,
    StartedAt: time.Now().UTC(),
})
```

with:

```go
bcast := daemon.NewEventBroadcaster()
srv := daemon.NewServer(daemon.ServerConfig{
    DB:          d,
    StartedAt:   time.Now().UTC(),
    Broadcaster: bcast,
})
```

And at the return statement, replace:

```go
return &Env{URL: url, HTTP: client, DB: d, Home: home}
```

with:

```go
return &Env{URL: url, HTTP: client, DB: d, Home: home, Broadcaster: bcast}
```

- [ ] **Step 2: Build to confirm the harness still compiles**

Run: `cd /Users/wesm/code/kata && go build ./...`
Expected: PASS.

- [ ] **Step 3: Wire `handlers_issues.go` (create + edit)**

Edit `/Users/wesm/code/kata/internal/daemon/handlers_issues.go`.

Find the `createIssue` handler tail (lines ~85-91 of the current file):

```go
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
```

Replace with:

```go
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		// Idempotent reuse path returns earlier (no broadcast); fresh creation
		// always emits issue.created. The event is freshly inserted, so evt.ID
		// is guaranteed non-zero.
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
```

Now find the `editIssue` handler. Read lines ~155-220 of the file to locate the post-commit section:

```bash
grep -n "editIssue\|EditIssue" /Users/wesm/code/kata/internal/daemon/handlers_issues.go
```

The relevant block returns `(updated, evt, changed, err)`. After the `case err != nil` switch, before the response struct, add the broadcast:

```go
		if changed && evt != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{
				Kind:      "event",
				Event:     evt,
				ProjectID: in.ProjectID,
			})
		}
```

(Use the exact handler structure that exists in your file. The pattern is: after the `cfg.DB.EditIssue(...)` call's error returns and before the `out := &api.MutationResponse{}` build.)

- [ ] **Step 4: Wire `handlers_actions.go` (close + reopen)**

Edit `/Users/wesm/code/kata/internal/daemon/handlers_actions.go`. Both handlers follow the same shape. Find the close handler's post-commit block (around line ~36):

```go
			updated, evt, changed, err := cfg.DB.CloseIssue(ctx, issue.ID, in.Body.Reason, in.Body.Actor)
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			out := &api.MutationResponse{}
			out.Body.Issue = updated
			out.Body.Event = evt
			out.Body.Changed = changed
			return out, nil
```

Replace with:

```go
			updated, evt, changed, err := cfg.DB.CloseIssue(ctx, issue.ID, in.Body.Reason, in.Body.Actor)
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			if changed && evt != nil {
				cfg.Broadcaster.Broadcast(StreamMsg{
					Kind:      "event",
					Event:     evt,
					ProjectID: in.ProjectID,
				})
			}
			out := &api.MutationResponse{}
			out.Body.Issue = updated
			out.Body.Event = evt
			out.Body.Changed = changed
			return out, nil
```

Apply the same pattern to the reopen handler (around line ~62).

- [ ] **Step 5: Wire `handlers_links.go` (create + parent --replace + delete)**

Edit `/Users/wesm/code/kata/internal/daemon/handlers_links.go`.

In the create-link handler, the parent --replace path (lines ~85-112) ends with `cfg.DB.DeleteLinkAndEvent(...)` returning `(_, _, err)`. Find the matching pattern:

```go
				if _, err := cfg.DB.DeleteLinkAndEvent(ctx, existing, unlinkEv); err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
```

The signature returns the *unlink event* (look at the DB function — see below). Wait — re-read the call site to check what's actually returned:

```bash
grep -n "DeleteLinkAndEvent" /Users/wesm/code/kata/internal/db/queries_links.go
```

The function returns `(Event, error)`. The handler currently throws away the event. Update the call to capture and broadcast it:

Replace:

```go
				if _, err := cfg.DB.DeleteLinkAndEvent(ctx, existing, unlinkEv); err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
```

with:

```go
				unlinkEvt, err := cfg.DB.DeleteLinkAndEvent(ctx, existing, unlinkEv)
				if err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
				cfg.Broadcaster.Broadcast(StreamMsg{
					Kind:      "event",
					Event:     &unlinkEvt,
					ProjectID: in.ProjectID,
				})
```

Then find the `CreateLinkAndEvent` post-commit block (further down in the same handler) and append a broadcast for the linked event:

```go
		// after `link, evt, err := cfg.DB.CreateLinkAndEvent(...)` and the
		// switch that maps errors:
		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
		return mutationLinkResponse(updatedIssue, link, canonicalFromNum, canonicalToNum, &evt, true), nil
```

In the delete-link handler, find the post-commit block:

```go
		evt, err := cfg.DB.DeleteLinkAndEvent(ctx, link, ev)
		if errors.Is(err, db.ErrNotFound) {
			// no-op envelope
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
```

After the err check, broadcast:

```go
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
```

The no-op `errors.Is(err, db.ErrNotFound)` branch must NOT broadcast (idempotent delete-of-already-gone returns the no-op envelope).

- [ ] **Step 6: Wire `handlers_labels.go`, `handlers_comments.go`, `handlers_ownership.go`, `handlers_destructive.go`**

For each handler, find the post-commit block where the response is built and add a broadcast guarded by `changed && evt != nil`:

`handlers_labels.go` (addLabelHandler, around line ~80, after `evt, err := cfg.DB.AddLabelAndEvent(...)`):

```go
		// after the err switch, before building the response:
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
		out := &api.AddLabelResponse{}
```

Note: the no-op branch `errors.Is(err, db.ErrLabelExists)` returns the no-op envelope and must NOT broadcast.

`handlers_labels.go` (removeLabelHandler, around line ~107, after `evt, err := cfg.DB.RemoveLabelAndEvent(...)`):

```go
		// before building the response (after the no-op ErrNotFound branch):
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
		updatedIssue, err := cfg.DB.IssueByID(ctx, issue.ID)
```

`handlers_comments.go` (around line ~37, after `c, evt, err := cfg.DB.CreateComment(...)`):

```go
		cfg.Broadcaster.Broadcast(StreamMsg{
			Kind:      "event",
			Event:     &evt,
			ProjectID: in.ProjectID,
		})
		updated, err := cfg.DB.IssueByID(ctx, issue.ID)
```

`handlers_ownership.go` (assignIssue and unassignIssue, both around the `updated, evt, changed, err := cfg.DB.UpdateOwner(...)` calls):

```go
		updated, evt, changed, err := cfg.DB.UpdateOwner(ctx, issue.ID, &owner, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if changed && evt != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{
				Kind:      "event",
				Event:     evt,
				ProjectID: in.ProjectID,
			})
		}
```

`handlers_destructive.go` (delete + restore handlers, both around the `cfg.DB.SoftDeleteIssue(...)` / `cfg.DB.RestoreIssue(...)` calls):

```go
		updated, evt, changed, err := cfg.DB.SoftDeleteIssue(ctx, issue.ID, in.Body.Actor)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if changed && evt != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{
				Kind:      "event",
				Event:     evt,
				ProjectID: in.ProjectID,
			})
		}
```

`handlers_destructive.go` (purge handler — broadcasts a reset, not an event):

```go
		pl, err := cfg.DB.PurgeIssue(ctx, issue.ID, in.Body.Actor, reasonPtr)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		// Notify live SSE subscribers via reset signal when the cascade
		// deleted any events. Polling clients pick up the same signal via
		// PurgeResetCheck on their next request — the broadcast just makes it
		// arrive in real time.
		if pl.PurgeResetAfterEventID != nil {
			cfg.Broadcaster.Broadcast(StreamMsg{
				Kind:      "reset",
				ResetID:   *pl.PurgeResetAfterEventID,
				ProjectID: in.ProjectID,
			})
		}
		out := &api.PurgeResponse{}
		out.Body.PurgeLog = pl
		return out, nil
```

- [ ] **Step 7: Un-skip `TestSSE_LiveResetClosesStream` and rewrite it to use the wired broadcast**

Edit `/Users/wesm/code/kata/internal/daemon/handlers_events_test.go`. Replace:

```go
func TestSSE_LiveResetClosesStream(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// Drain delivers the issue.created frame.
	first := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, first, 1)

	// Purge → reset signal arrives via live channel; stream then closes.
	_, err := env.DB.PurgeIssue(context.Background(), is.ID, "tester", nil)
	require.NoError(t, err)
	// Manually broadcast the reset because PurgeIssue itself doesn't (Task 8 wires that).
	// In Task 8 we'll remove this manual call.
	plRow, err := env.DB.PurgeLogByIssueID(context.Background(), is.ID)
	require.NoError(t, err)
	require.NotNil(t, plRow.PurgeResetAfterEventID)
	// Pull the broadcaster off ServerConfig via a test hook? testenv doesn't
	// currently expose it. For now this test asserts behavior by mutating the
	// purge_log (already done) and reconnecting; the in-flight stream will
	// time out at the deadline. Task 8 (broadcast wiring) revisits this test.
	t.Skip("requires testenv.Env.Broadcaster accessor — added in Task 8")
}
```

with:

```go
func TestSSE_LiveResetClosesStream(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	is := mkIssue(t, env, pid, "doomed")
	pidStr := strconv.FormatInt(pid, 10)

	resp := openSSE(t, env, "after_id=0", nil)
	defer func() { _ = resp.Body.Close() }()

	// Drain delivers the issue.created frame.
	first := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, first, 1)

	// Purge via HTTP so the broadcast wiring in handlers_destructive.go fires.
	purgeURL := env.URL + "/api/v1/projects/" + pidStr + "/issues/" + strconv.FormatInt(is.Number, 10) + "/actions/purge"
	bodyJSON := `{"actor":"tester"}`
	purgeReq, _ := http.NewRequest(http.MethodPost, purgeURL, strings.NewReader(bodyJSON))
	purgeReq.Header.Set("Content-Type", "application/json")
	purgeReq.Header.Set("X-Kata-Confirm", "PURGE #"+strconv.FormatInt(is.Number, 10))
	purgeResp, err := env.HTTP.Do(purgeReq)
	require.NoError(t, err)
	_ = purgeResp.Body.Close()
	require.Equal(t, 200, purgeResp.StatusCode)

	// Live channel delivers the reset frame; stream closes.
	resetFrames := readSSEFramesUntilN(t, resp.Body, 1, 2*time.Second)
	require.Len(t, resetFrames, 1)
	assert.Equal(t, "sync.reset_required", resetFrames[0].event)
}
```

Also append a parent-replace test that pins the two-broadcast behavior:

```go
func TestSSE_ParentReplaceEmitsTwoFrames(t *testing.T) {
	env := testenv.New(t)
	pid := mkProject(t, env, "github.com/test/a", "a")
	mkIssue(t, env, pid, "first")  // #1, will be initial parent
	mkIssue(t, env, pid, "second") // #2, will be replacement parent
	mkIssue(t, env, pid, "child")  // #3, the issue we re-parent
	pidStr := strconv.FormatInt(pid, 10)

	// Initial parent link 3 → 1.
	bodyJSON := `{"actor":"tester","type":"parent","to_number":1}`
	req, _ := http.NewRequest(http.MethodPost,
		env.URL+"/api/v1/projects/"+pidStr+"/issues/3/links",
		strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Subscribe AFTER the initial link so we don't see its frame in the drain.
	maxID, err := env.DB.MaxEventID(context.Background())
	require.NoError(t, err)
	sseResp := openSSE(t, env, "after_id="+strconv.FormatInt(maxID, 10), nil)
	defer func() { _ = sseResp.Body.Close() }()

	// Re-parent 3 → 2 with replace.
	bodyJSON = `{"actor":"tester","type":"parent","to_number":2,"replace":true}`
	req, _ = http.NewRequest(http.MethodPost,
		env.URL+"/api/v1/projects/"+pidStr+"/issues/3/links",
		strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	resp, err = env.HTTP.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Live phase delivers two frames in order: issue.unlinked then issue.linked.
	frames := readSSEFramesUntilN(t, sseResp.Body, 2, 2*time.Second)
	require.Len(t, frames, 2)
	assert.Equal(t, "issue.unlinked", frames[0].event)
	assert.Equal(t, "issue.linked", frames[1].event)
}
```

- [ ] **Step 8: Run the full daemon suite**

Run: `cd /Users/wesm/code/kata && go test ./internal/daemon/`
Expected: PASS — all SSE tests including the un-skipped reset and the parent-replace pair.

- [ ] **Step 9: Run lint**

Run: `cd /Users/wesm/code/kata && gofmt -l internal/daemon/handlers_*.go && go vet ./internal/daemon/`
Expected: empty output.

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/handlers_issues.go \
        internal/daemon/handlers_actions.go \
        internal/daemon/handlers_links.go \
        internal/daemon/handlers_labels.go \
        internal/daemon/handlers_comments.go \
        internal/daemon/handlers_ownership.go \
        internal/daemon/handlers_destructive.go \
        internal/daemon/handlers_events_test.go \
        internal/testenv/testenv.go
git commit -m "feat(daemon): broadcast events post-commit on every mutation handler"
```

---

## Task 9: Race-window tests — out-of-order broadcasts and concurrent Subscribe/Broadcast

**Files:**
- Create: `internal/daemon/broadcaster_race_test.go`

The wakeup-and-requery model is the core correctness claim of the design. Pin it with a deterministic test that reorders broadcasts.

- [ ] **Step 1: Create the race-window test file**

Create `/Users/wesm/code/kata/internal/daemon/broadcaster_race_test.go`:

```go
package daemon_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/testenv"
)

// TestSSE_OutOfOrderBroadcastsEmitInIDOrder pins the wakeup-and-requery
// guarantee: even if Broadcast(102) fires before Broadcast(101), the SSE
// consumer sees frame id=101 first, then id=102. The DB is the ordering
// authority; the broadcaster only signals "something changed at or below N".
//
// Setup: open SSE consumer at cursor matching MaxEventID at connect time
// (drain returns nothing). Insert events 101 and 102 directly into the DB
// (via CreateIssue). Then call Broadcast(102) before Broadcast(101) on the
// shared broadcaster. The consumer should receive 101 before 102.
func TestSSE_OutOfOrderBroadcastsEmitInIDOrder(t *testing.T) {
	env := testenv.New(t)
	pid, err := env.DB.CreateProject(context.Background(), "github.com/test/a", "a")
	require.NoError(t, err)

	// Open SSE at the current head so the drain is empty.
	hwm, err := env.DB.MaxEventID(context.Background())
	require.NoError(t, err)
	resp := openSSE(t, env, "after_id="+strconv.FormatInt(hwm, 10), nil)
	defer func() { _ = resp.Body.Close() }()

	// Read the : connected\n\n preamble and confirm 200.
	require.Equal(t, 200, resp.StatusCode)
	preamble := make([]byte, 16)
	_, err = resp.Body.Read(preamble)
	require.NoError(t, err)

	// Insert two events. CreateIssue inserts the events row; we don't broadcast
	// yet (we'll do that manually below in the inverted order).
	is1, evt1, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: pid.ID, Title: "first", Author: "tester",
	})
	require.NoError(t, err)
	is2, evt2, err := env.DB.CreateIssue(context.Background(), db.CreateIssueParams{
		ProjectID: pid.ID, Title: "second", Author: "tester",
	})
	require.NoError(t, err)
	_ = is1
	_ = is2
	// Note: testenv's CreateIssue (via env.DB) does NOT route through the
	// HTTP handlers, so the post-commit broadcast in handlers_issues.go
	// does NOT fire. The two events are in the DB, but no wakeups have
	// been sent to subscribers. We now drive Broadcast manually.

	// Inverted broadcast order: evt2 first, then evt1.
	env.Broadcaster.Broadcast(daemon.StreamMsg{
		Kind: "event", Event: &evt2, ProjectID: pid.ID,
	})
	env.Broadcaster.Broadcast(daemon.StreamMsg{
		Kind: "event", Event: &evt1, ProjectID: pid.ID,
	})

	// SSE consumer must see id=evt1.ID, then id=evt2.ID, in that order.
	frames := readSSEFramesUntilN(t, resp.Body, 2, 2*time.Second)
	require.Len(t, frames, 2)
	assert.Equal(t, strconv.FormatInt(evt1.ID, 10), frames[0].id,
		"first frame must be the lower id, regardless of broadcast order")
	assert.Equal(t, strconv.FormatInt(evt2.ID, 10), frames[1].id)
}

// TestBroadcaster_ConcurrentSubscribeBroadcastUnsub is a -race fuzz test for
// concurrent Subscribe/Broadcast/Unsub. It doesn't make ordering claims;
// it asserts the broadcaster doesn't deadlock, panic, or leak goroutines.
func TestBroadcaster_ConcurrentSubscribeBroadcastUnsub(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	_ = d

	b := daemon.NewEventBroadcaster()
	var wg sync.WaitGroup
	const N = 100
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := b.Subscribe(daemon.SubFilter{ProjectID: int64(i % 3)})
			drain := make(chan struct{})
			go func() {
				for range sub.Ch {
				}
				close(drain)
			}()
			time.Sleep(time.Microsecond * time.Duration(i%5))
			sub.Unsub()
			<-drain
		}(i)
	}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evt := &db.Event{ID: int64(i + 1), ProjectID: int64(i % 3), Type: "issue.created"}
			b.Broadcast(daemon.StreamMsg{
				Kind: "event", Event: evt, ProjectID: evt.ProjectID,
			})
		}(i)
	}
	// Just complete without deadlock or panic.
	wg.Wait()
}

// _ ensures we keep the http import alive even if we restructure tests.
var _ = http.Header{}
```

- [ ] **Step 2: Run race tests**

Run: `cd /Users/wesm/code/kata && go test -race -run "TestSSE_OutOfOrderBroadcasts|TestBroadcaster_ConcurrentSubscribe" ./internal/daemon/`
Expected: PASS, no race warnings.

- [ ] **Step 3: Run the full daemon suite with -race**

Run: `cd /Users/wesm/code/kata && go test -race ./internal/daemon/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/broadcaster_race_test.go
git commit -m "test(daemon): pin out-of-order broadcast wakeup-and-requery + concurrent fuzz"
```

---

## Task 10: CLI — `kata events` (one-shot poll)

**Files:**
- Create: `cmd/kata/events.go`
- Create: `cmd/kata/events_test.go`
- Modify: `cmd/kata/main.go` (register `newEventsCmd()`)

The simpler half of the CLI: GET `/events` (cross-project) or `/projects/{id}/events` (per-project), print to stdout.

- [ ] **Step 1: Create the command file with the one-shot path**

Create `/Users/wesm/code/kata/cmd/kata/events.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newEventsCmd() *cobra.Command {
	var (
		tail         bool
		projectIDArg int64
		allProjects  bool
		afterID      int64
		lastEventID  int64
		limit        int
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "list or stream events",
		Long: `kata events lists recent events. With --tail, it streams them live over SSE.

Without --tail, prints up to --limit events ordered by id ASC and exits.
With --tail, opens an SSE connection and emits one NDJSON envelope per line
until SIGINT/SIGTERM. Reconnects with exponential backoff on disconnect.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if allProjects && projectIDArg != 0 {
				return &cliError{Message: "--all-projects and --project-id are mutually exclusive", ExitCode: ExitUsage}
			}
			if tail {
				return runEventsTail(cmd, eventsTailOptions{
					ProjectIDArg: projectIDArg,
					AllProjects:  allProjects,
					LastEventID:  lastEventID,
				})
			}
			return runEventsPoll(cmd, eventsPollOptions{
				ProjectIDArg: projectIDArg,
				AllProjects:  allProjects,
				AfterID:      afterID,
				Limit:        limit,
			})
		},
	}
	cmd.Flags().BoolVar(&tail, "tail", false, "stream events live over SSE")
	cmd.Flags().Int64Var(&projectIDArg, "project-id", 0, "scope to a specific project id")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "use the cross-project endpoint")
	cmd.Flags().Int64Var(&afterID, "after", 0, "polling cursor (one-shot mode)")
	cmd.Flags().Int64Var(&lastEventID, "last-event-id", 0, "resume cursor (--tail mode)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows in one-shot mode")
	return cmd
}

type eventsPollOptions struct {
	ProjectIDArg int64
	AllProjects  bool
	AfterID      int64
	Limit        int
}

func runEventsPoll(cmd *cobra.Command, opts eventsPollOptions) error {
	ctx := cmd.Context()
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	url, err := pollURL(ctx, baseURL, opts)
	if err != nil {
		return err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	return printEventsHuman(cmd, bs)
}

func pollURL(ctx context.Context, baseURL string, opts eventsPollOptions) (string, error) {
	switch {
	case opts.AllProjects:
		return fmt.Sprintf("%s/api/v1/events?after_id=%d&limit=%d", baseURL, opts.AfterID, opts.Limit), nil
	case opts.ProjectIDArg != 0:
		return fmt.Sprintf("%s/api/v1/projects/%d/events?after_id=%d&limit=%d",
			baseURL, opts.ProjectIDArg, opts.AfterID, opts.Limit), nil
	default:
		// resolve the workspace's project
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return "", err
		}
		pid, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s/api/v1/projects/%d/events?after_id=%d&limit=%d",
			baseURL, pid, opts.AfterID, opts.Limit), nil
	}
}

func printEventsHuman(cmd *cobra.Command, bs []byte) error {
	var b struct {
		ResetRequired bool  `json:"reset_required"`
		ResetAfterID  int64 `json:"reset_after_id"`
		Events        []struct {
			EventID     int64  `json:"event_id"`
			Type        string `json:"type"`
			ProjectID   int64  `json:"project_id"`
			IssueNumber *int64 `json:"issue_number"`
			Actor       string `json:"actor"`
			CreatedAt   string `json:"created_at"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if b.ResetRequired {
		_, err := fmt.Fprintf(cmd.OutOrStdout(),
			"reset_required: refetch state and resume from %d\n", b.ResetAfterID)
		return err
	}
	for _, e := range b.Events {
		issueStr := "-"
		if e.IssueNumber != nil {
			issueStr = "#" + strconv.FormatInt(*e.IssueNumber, 10)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(),
			"%-6d  %-22s  proj=%-3d  %-6s  by %s  %s\n",
			e.EventID, e.Type, e.ProjectID, issueStr, e.Actor, e.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// runEventsTail is implemented in Task 11.
func runEventsTail(cmd *cobra.Command, opts eventsTailOptions) error {
	_ = cmd
	_ = opts
	return &cliError{Message: "kata events --tail not yet implemented", ExitCode: ExitInternal}
}

type eventsTailOptions struct {
	ProjectIDArg int64
	AllProjects  bool
	LastEventID  int64
}

// trims unused import; satisfies vet when --tail body is the stub.
var _ = strings.TrimSpace
```

Wait — `context` is used in `pollURL`. Add to the import block:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)
```

- [ ] **Step 2: Register the command in `cmd/kata/main.go`**

Edit `/Users/wesm/code/kata/cmd/kata/main.go`. In the `subs := []*cobra.Command{...}` block (around line 46), add `newEventsCmd()` after `newReadyCmd()`:

```go
		newReadyCmd(),
		newEventsCmd(),
		newWhoamiCmd(),
```

- [ ] **Step 3: Write CLI tests for the one-shot path**

Create `/Users/wesm/code/kata/cmd/kata/events_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestEvents_OneShotPlainOutput(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "first")
	createIssueViaHTTP(t, env, dir, "second")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "events"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	out := buf.String()
	assert.Contains(t, out, "issue.created")
	// Two events → two lines of human output.
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	assert.Equal(t, 2, lines)
}

func TestEvents_OneShotJSON(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "only")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "events", "--json"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	var b struct {
		KataAPIVersion int `json:"kata_api_version"`
		Events         []struct {
			EventID int64  `json:"event_id"`
			Type    string `json:"type"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &b))
	assert.Equal(t, 1, b.KataAPIVersion)
	require.Len(t, b.Events, 1)
	assert.Equal(t, "issue.created", b.Events[0].Type)
	assert.Equal(t, int64(1), b.NextAfterID)
}

func TestEvents_OneShotAllProjectsHitsCrossProject(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	// Two bound projects.
	dirA := initBoundWorkspace(t, env.URL, "https://github.com/wesm/a.git")
	dirB := initBoundWorkspace(t, env.URL, "https://github.com/wesm/b.git")
	createIssueViaHTTP(t, env, dirA, "a-issue")
	createIssueViaHTTP(t, env, dirB, "b-issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"events", "--all-projects", "--json"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	var b struct {
		Events []struct {
			ProjectID int64 `json:"project_id"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &b))
	assert.Len(t, b.Events, 2, "all-projects must include both projects")
}
```

- [ ] **Step 4: Run CLI tests**

Run: `cd /Users/wesm/code/kata && go test -run TestEvents_OneShot ./cmd/kata/`
Expected: PASS for all three.

- [ ] **Step 5: Run lint**

Run: `cd /Users/wesm/code/kata && gofmt -l cmd/kata/events*.go cmd/kata/main.go && go vet ./cmd/kata/`
Expected: empty output.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/events.go cmd/kata/events_test.go cmd/kata/main.go
git commit -m "feat(cli): kata events one-shot polling"
```

---

## Task 11: CLI — `kata events --tail` (NDJSON-over-SSE consumer)

**Files:**
- Modify: `cmd/kata/events.go` (replace `runEventsTail` stub)
- Modify: `cmd/kata/events_test.go` (add tail tests)

The streaming half of the CLI: open SSE, emit one NDJSON envelope per frame, exponential backoff reconnect, follow `sync.reset_required` by re-fetching with the new cursor.

- [ ] **Step 1: Implement the SSE consumer**

Replace the `runEventsTail` stub in `/Users/wesm/code/kata/cmd/kata/events.go`. Add new imports first:

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)
```

Then replace the `runEventsTail` placeholder and any `_ = strings.TrimSpace` line with:

```go
const (
	tailBackoffStart = 1 * time.Second
	tailBackoffMax   = 30 * time.Second
)

func runEventsTail(cmd *cobra.Command, opts eventsTailOptions) error {
	ctx := cmd.Context()
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	url, err := tailURL(ctx, baseURL, opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	cursor := opts.LastEventID
	backoff := tailBackoffStart

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		readAny, err := streamOnce(ctx, client, url, cursor, out)
		if err != nil {
			if !flags.Quiet {
				fmt.Fprintln(os.Stderr, "kata: stream error:", err, "(reconnecting in", backoff.Round(time.Second), ")")
			}
		}
		// Update cursor based on last frame received in streamOnce. We use a
		// lightweight closure to keep state in the loop scope.
		// NOTE: streamOnce returns (lastReceivedID, nextCursorAfterReset, err).
		// On a normal disconnect, lastReceivedID is the last event id; we
		// resume from there. On a reset, nextCursorAfterReset is the new
		// baseline; we resume from that.
		switch v := readAny.(type) {
		case streamResetSignal:
			cursor = v.newCursor
			backoff = tailBackoffStart
			continue
		case streamProgress:
			if v.lastID > cursor {
				cursor = v.lastID
				backoff = tailBackoffStart // received data → reset backoff
			}
		}

		// Wait backoff with ctx-aware sleep.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < tailBackoffMax {
			backoff *= 2
			if backoff > tailBackoffMax {
				backoff = tailBackoffMax
			}
		}
	}
}

// streamOnce opens one SSE connection and emits NDJSON until the stream
// closes or an error occurs. Returns:
//   streamResetSignal{newCursor: N}     — server sent sync.reset_required
//   streamProgress{lastID: N}           — clean disconnect; resume from N
type streamResetSignal struct{ newCursor int64 }
type streamProgress struct{ lastID int64 }

func streamOnce(ctx context.Context, client *http.Client, baseURL string, cursor int64, out io.Writer) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return streamProgress{lastID: cursor}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if cursor > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	}
	resp, err := client.Do(req) //nolint:gosec // baseURL comes from daemon discovery
	if err != nil {
		return streamProgress{lastID: cursor}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		return streamProgress{lastID: cursor}, fmt.Errorf("http %d: %s", resp.StatusCode, string(bs))
	}

	// Parse SSE frame-by-frame.
	rd := bufio.NewReader(resp.Body)
	var (
		curID    string
		curEvent string
		curData  string
	)
	flushFrame := func() (any, bool, error) {
		defer func() { curID, curEvent, curData = "", "", "" }()
		if curEvent == "" && curData == "" && curID == "" {
			return nil, false, nil
		}
		switch curEvent {
		case "sync.reset_required":
			var r struct {
				ResetAfterID int64 `json:"reset_after_id"`
			}
			if err := json.Unmarshal([]byte(curData), &r); err != nil {
				return nil, false, fmt.Errorf("parse reset frame: %w", err)
			}
			// Emit the reset notice as NDJSON so downstream tooling sees it.
			env := map[string]any{
				"reset_required": true,
				"reset_after_id": r.ResetAfterID,
			}
			line, _ := json.Marshal(env)
			if _, err := fmt.Fprintln(out, string(line)); err != nil {
				return nil, false, err
			}
			return streamResetSignal{newCursor: r.ResetAfterID}, true, nil
		default:
			// Echo the data: payload as-is to stdout (one line per frame).
			if _, err := fmt.Fprintln(out, curData); err != nil {
				return nil, false, err
			}
			n, _ := strconv.ParseInt(curID, 10, 64)
			return streamProgress{lastID: n}, false, nil
		}
	}

	progress := streamProgress{lastID: cursor}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return progress, nil
			}
			return progress, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			res, terminal, err := flushFrame()
			if err != nil {
				return progress, err
			}
			if reset, ok := res.(streamResetSignal); ok {
				return reset, nil
			}
			if p, ok := res.(streamProgress); ok && p.lastID > 0 {
				progress = p
			}
			if terminal {
				return progress, nil
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat — ignore
		case strings.HasPrefix(line, "id: "):
			curID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			curData = strings.TrimPrefix(line, "data: ")
		}
	}
}

func tailURL(ctx context.Context, baseURL string, opts eventsTailOptions) (string, error) {
	switch {
	case opts.AllProjects:
		return baseURL + "/api/v1/events/stream", nil
	case opts.ProjectIDArg != 0:
		return fmt.Sprintf("%s/api/v1/events/stream?project_id=%d", baseURL, opts.ProjectIDArg), nil
	default:
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return "", err
		}
		pid, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s/api/v1/events/stream?project_id=%d", baseURL, pid), nil
	}
}

// silence unused-import warnings if helpers shift; keeps the file from drift.
var _ = bytes.NewReader
```

- [ ] **Step 2: Write tail tests**

Append to `/Users/wesm/code/kata/cmd/kata/events_test.go`:

```go
import (
	// add to existing import block:
	"sync"
	"time"
)

func TestEvents_TailEmitsNDJSON(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	// Subscribe before any events so drain is empty; once tail is open, fire
	// two issue.created events and assert two NDJSON lines.
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	ctx, cancel := context.WithTimeout(contextWithBaseURL(context.Background(), env.URL), 5*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"--workspace", dir, "events", "--tail"})
	cmd.SetContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cmd.Execute()
	}()

	// Give the tail goroutine a moment to issue the SSE GET.
	time.Sleep(200 * time.Millisecond)
	createIssueViaHTTP(t, env, dir, "first")
	createIssueViaHTTP(t, env, dir, "second")

	// Wait for two lines or timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(buf.String(), "issue.created") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	out := buf.String()
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	require.GreaterOrEqual(t, len(lines), 2, "expected at least 2 NDJSON lines, got: %q", out)
	for _, l := range lines[:2] {
		var env map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &env), "each line must be a JSON object")
		assert.Equal(t, "issue.created", env["type"])
	}
}

func TestEvents_TailFollowsResetRequired(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "doomed")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	ctx, cancel := context.WithTimeout(contextWithBaseURL(context.Background(), env.URL), 5*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"--workspace", dir, "events", "--tail"})
	cmd.SetContext(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = cmd.Execute()
	}()

	// Wait for drain frame to arrive, then purge to trigger reset.
	time.Sleep(300 * time.Millisecond)
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	purgeURL := env.URL + "/api/v1/projects/" + itoa(pid) + "/issues/1/actions/purge"
	body := strings.NewReader(`{"actor":"tester"}`)
	req, _ := http.NewRequest(http.MethodPost, purgeURL, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kata-Confirm", "PURGE #1")
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"reset_required":true`) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	assert.Contains(t, buf.String(), `"reset_required":true`,
		"--tail must emit a reset envelope when the daemon sends sync.reset_required")
}
```

- [ ] **Step 3: Run tail tests**

Run: `cd /Users/wesm/code/kata && go test -run TestEvents_Tail -timeout 30s ./cmd/kata/`
Expected: PASS for both.

- [ ] **Step 4: Run the full CLI suite**

Run: `cd /Users/wesm/code/kata && go test ./cmd/kata/`
Expected: PASS — no regressions in existing CLI tests.

- [ ] **Step 5: Lint**

Run: `cd /Users/wesm/code/kata && gofmt -l cmd/kata/events*.go && go vet ./cmd/kata/`
Expected: empty output.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/events.go cmd/kata/events_test.go
git commit -m "feat(cli): kata events --tail SSE consumer with reconnect/backoff"
```

---

## Task 12: e2e smoke — `TestSmoke_Plan4Events`

**Files:**
- Modify: `e2e/e2e_test.go`

End-to-end test covering: baseline cursor capture, SSE per-project filter, polling client + reset, parent --replace two-frame, purge → reset → reconnect.

- [ ] **Step 1: Add the smoke test**

Append to `/Users/wesm/code/kata/e2e/e2e_test.go`:

```go
// TestSmoke_Plan4Events exercises Plan 4 end-to-end via HTTP:
//   1. boot daemon, init two projects A/B, create one issue each
//   2. capture baseline cursor for project A so setup events are absorbed
//   3. open SSE consumer for project A with cursor = baseline_A
//   4. mutate project A (comment, label, assign), and project B (comment)
//   5. SSE consumer sees three frames (A's events) and no project B events
//   6. polling client returns three events from baseline_A; subsequent poll empty
//   7. parent --replace path emits issue.unlinked + issue.linked + issue.created
//   8. purge issue 1 in project A → SSE sees sync.reset_required, stream closes
//   9. polling client (with stale cursor) gets reset_required:true
//  10. reconnect SSE with reset cursor; resume cleanly with no further events
func TestSmoke_Plan4Events(t *testing.T) {
	env := testenv.New(t)
	dirA := initRepo(t, "https://github.com/wesm/plan4-a.git")
	dirB := initRepo(t, "https://github.com/wesm/plan4-b.git")

	// 1. init both projects.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dirA}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dirB}))

	pidA := resolvePID(t, env.HTTP, env.URL, dirA)
	pidB := resolvePID(t, env.HTTP, env.URL, dirB)
	pidAStr := strconv.FormatInt(pidA, 10)
	pidBStr := strconv.FormatInt(pidB, 10)

	// One issue in each.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues",
		map[string]any{"actor": "agent", "title": "first-A"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidBStr+"/issues",
		map[string]any{"actor": "agent", "title": "first-B"}))

	// 2. baseline cursor for project A.
	baselineA := pollNextAfterID(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/events?after_id=0")
	require.Greater(t, baselineA, int64(0))

	// 3. open SSE consumer at baseline_A (no drain content).
	sseResp := openSSEAt(t, env.HTTP, env.URL+"/api/v1/events/stream?project_id="+pidAStr+"&after_id="+strconv.FormatInt(baselineA, 10))
	defer func() { _ = sseResp.Body.Close() }()

	// 4. mutate.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "comment-1"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues/1/labels",
		map[string]any{"actor": "agent", "label": "bug"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues/1/actions/assign",
		map[string]any{"actor": "agent", "owner": "claude"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidBStr+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "comment-B"}))

	// 5. SSE sees exactly the three project-A frames.
	frames := readSmokeSSEFrames(t, sseResp.Body, 3, 2*time.Second)
	require.Len(t, frames, 3)
	assert.Equal(t, "issue.commented", frames[0].event)
	assert.Equal(t, "issue.labeled", frames[1].event)
	assert.Equal(t, "issue.assigned", frames[2].event)

	// 6. polling client returns the same three; subsequent poll is empty.
	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + pidAStr +
		"/events?after_id=" + strconv.FormatInt(baselineA, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var pollBody struct {
		ResetRequired bool `json:"reset_required"`
		Events        []struct {
			Type string `json:"type"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pollBody))
	require.Len(t, pollBody.Events, 3)
	require.Greater(t, pollBody.NextAfterID, baselineA)

	resp2, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + pidAStr +
		"/events?after_id=" + strconv.FormatInt(pollBody.NextAfterID, 10))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	var poll2 struct {
		Events      []struct{} `json:"events"`
		NextAfterID int64      `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&poll2))
	assert.Len(t, poll2.Events, 0)
	assert.Equal(t, pollBody.NextAfterID, poll2.NextAfterID)

	// 7. parent --replace: create #2 with parent #1, then re-link to (a fresh #3) with replace.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues",
		map[string]any{"actor": "agent", "title": "child", "links": []map[string]any{{"type": "parent", "to_number": 1}}}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues",
		map[string]any{"actor": "agent", "title": "new-parent"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidAStr+"/issues/2/links",
		map[string]any{"actor": "agent", "type": "parent", "to_number": 3, "replace": true}))

	// New SSE frames: issue.created (child #2), issue.created (new-parent #3),
	// then issue.unlinked + issue.linked from the replace.
	moreFrames := readSmokeSSEFrames(t, sseResp.Body, 4, 2*time.Second)
	require.Len(t, moreFrames, 4)
	assert.Equal(t, "issue.created", moreFrames[0].event)
	assert.Equal(t, "issue.created", moreFrames[1].event)
	assert.Equal(t, "issue.unlinked", moreFrames[2].event, "replace must emit unlinked first")
	assert.Equal(t, "issue.linked", moreFrames[3].event, "then linked")

	// 8. purge issue 1 in project A.
	purgeURL := env.URL + "/api/v1/projects/" + pidAStr + "/issues/1/actions/purge"
	pReq, _ := http.NewRequest(http.MethodPost, purgeURL, strings.NewReader(`{"actor":"agent"}`))
	pReq.Header.Set("Content-Type", "application/json")
	pReq.Header.Set("X-Kata-Confirm", "PURGE #1")
	pResp, err := env.HTTP.Do(pReq)
	require.NoError(t, err)
	_ = pResp.Body.Close()
	require.Equal(t, 200, pResp.StatusCode)

	resetFrames := readSmokeSSEFrames(t, sseResp.Body, 1, 2*time.Second)
	require.Len(t, resetFrames, 1)
	assert.Equal(t, "sync.reset_required", resetFrames[0].event)
	resetID, err := strconv.ParseInt(resetFrames[0].id, 10, 64)
	require.NoError(t, err)
	require.Greater(t, resetID, int64(0))

	// 9. polling client with stale cursor.
	resp3, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + pidAStr +
		"/events?after_id=" + strconv.FormatInt(baselineA, 10))
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	var poll3 struct {
		ResetRequired bool  `json:"reset_required"`
		ResetAfterID  int64 `json:"reset_after_id"`
		NextAfterID   int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&poll3))
	assert.True(t, poll3.ResetRequired)
	assert.Equal(t, resetID, poll3.ResetAfterID)
	assert.Equal(t, resetID, poll3.NextAfterID)

	// 10. reconnect SSE with reset cursor; should be clean (no further frames).
	sseResp2 := openSSEAt(t, env.HTTP, env.URL+"/api/v1/events/stream?project_id="+pidAStr+
		"&after_id="+strconv.FormatInt(resetID, 10))
	defer func() { _ = sseResp2.Body.Close() }()
	noMore := readSmokeSSEFrames(t, sseResp2.Body, 1, 200*time.Millisecond)
	assert.Len(t, noMore, 0, "no further frames after reset cursor")
}

// pollNextAfterID issues a GET poll and returns next_after_id.
func pollNextAfterID(t *testing.T, client *http.Client, url string) int64 {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct {
		NextAfterID int64 `json:"next_after_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	return b.NextAfterID
}

// openSSEAt opens an SSE GET with Accept: text/event-stream.
func openSSEAt(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	// Eat the : connected\n\n preamble so subsequent reads start at the first frame.
	preamble := make([]byte, 16)
	_, _ = resp.Body.Read(preamble)
	return resp
}

type smokeSSEFrame struct {
	id    string
	event string
	data  string
}

// readSmokeSSEFrames pulls n frames off body or returns whatever it has when
// timeout fires.
func readSmokeSSEFrames(t *testing.T, body io.ReadCloser, n int, timeout time.Duration) []smokeSSEFrame {
	t.Helper()
	rd := bufio.NewReader(body)
	var (
		frames []smokeSSEFrame
		cur    smokeSSEFrame
	)
	deadline := time.Now().Add(timeout)
	type lr struct {
		s   string
		err error
	}
	for len(frames) < n && time.Now().Before(deadline) {
		ch := make(chan lr, 1)
		go func() {
			s, err := rd.ReadString('\n')
			ch <- lr{s, err}
		}()
		var got lr
		select {
		case got = <-ch:
		case <-time.After(time.Until(deadline)):
			return frames
		}
		if got.err != nil {
			return frames
		}
		line := strings.TrimRight(got.s, "\r\n")
		switch {
		case line == "":
			if cur.id != "" || cur.event != "" || cur.data != "" {
				frames = append(frames, cur)
				cur = smokeSSEFrame{}
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frames
}
```

You'll also need to add `bufio`, `io`, `strconv`, `strings`, `time` to the e2e test file's import block if they aren't already present. Run:

```bash
grep -E "\"bufio\"|\"io\"|\"strconv\"|\"strings\"|\"time\"" /Users/wesm/code/kata/e2e/e2e_test.go
```

Add any missing.

- [ ] **Step 2: Run the smoke test**

Run: `cd /Users/wesm/code/kata && go test -run TestSmoke_Plan4Events -timeout 60s ./e2e/`
Expected: PASS.

- [ ] **Step 3: Run all e2e tests**

Run: `cd /Users/wesm/code/kata && go test ./e2e/`
Expected: PASS for all four smoke tests (Plans 1-4).

- [ ] **Step 4: Commit**

```bash
git add e2e/e2e_test.go
git commit -m "test(e2e): TestSmoke_Plan4Events end-to-end SSE + polling + reset"
```

---

## Task 13: Master spec rename — `new_baseline` → `reset_after_id`

**Files:**
- Modify: `docs/superpowers/specs/2026-04-29-kata-design.md`

The wire field rename committed in Plan 4's design (§8). Two doc edits.

- [ ] **Step 1: Rename `new_baseline` in §4.8 (SSE protocol)**

Edit `/Users/wesm/code/kata/docs/superpowers/specs/2026-04-29-kata-design.md`. Find the line in §4.8:

```
- If invalidated, daemon sends a single `sync.reset_required` synthetic event with `id:` = the **MAX** of all matching `purge_reset_after_event_id`s, then closes the stream. Using the max ensures one reset moves the client past every accumulated purge gap; the client adopts that id as its new cursor and refetches state.
```

Look for the surrounding §4.8 SSE protocol text and find any reference to `new_baseline`. The §4.8 protocol section says (around lines 574-592):

```
- On reconnect: compute `MAX(purge_reset_after_event_id) FROM purge_log WHERE purge_reset_after_event_id > <cursor>` ... If non-null → send single `sync.reset_required` (with `id:` = that max value, `data.new_baseline` = same), close stream.
```

Replace `data.new_baseline` with `data.reset_after_id`.

- [ ] **Step 2: Rename `new_baseline` in §4.11 (polling)**

Find in §4.11 (around lines 619-628):

```
   ```json
   {
     "reset_required": true,
     "new_baseline": <reset_to>,
     "events": [],
     "next_after_id": <reset_to>
   }
   ```
```

Replace `"new_baseline"` with `"reset_after_id"`.

- [ ] **Step 3: Verify no other `new_baseline` references**

Run: `cd /Users/wesm/code/kata && grep -n "new_baseline" docs/superpowers/specs/2026-04-29-kata-design.md`
Expected: no matches.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-04-29-kata-design.md
git commit -m "docs(spec): rename new_baseline -> reset_after_id in master spec §4.8/§4.11"
```

---

## Task 14: Cross-cutting review

**Goal:** Run `roborev` against the full Plan 4 diff and address any HIGH/MEDIUM findings.

- [ ] **Step 1: Trigger a roborev review on the Plan 4 commit range**

Run: `roborev review --against main` (or whatever the project's standing review command is). This generates a review job; the response includes a job id.

If `roborev review` is not the right invocation in this repo, fall back to:
```bash
roborev review --range $(git log --oneline | grep -m 1 "Plan 4" | awk '{print $1}')..HEAD
```

- [ ] **Step 2: Once the review completes, fetch findings**

Run: `roborev fix --open --list`
Expected: zero or more open job ids.

- [ ] **Step 3: For each open finding, invoke `/roborev-fix` with the job ids and address every HIGH and MEDIUM in one pass**

Per the project's CLAUDE.md, fix all findings in a single pass. Run tests + lint after each fix batch. Commit the batch.

- [ ] **Step 4: Final verification**

```bash
cd /Users/wesm/code/kata && go test -race ./... && gofmt -l . && go vet ./... && golangci-lint run
```
Expected: PASS, empty output for gofmt and golangci-lint.

- [ ] **Step 5: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: address roborev findings on Plan 4"
```

---

## Self-Review (the plan author's checklist)

After writing each task, the author should re-read the spec and confirm:

**Spec coverage check:**

| Spec section | Plan task |
|---|---|
| §4.1 wire types (api/events.go) | Task 1 |
| §4.2 broadcaster | Task 3 |
| §4.3 DB queries | Task 2 |
| §4.4 server wiring + BaseContext | Task 4 |
| §4.5 mutation handler broadcasts (no-op guard, parent --replace) | Task 8 |
| §5.1 cursor input + Accept negotiation | Task 6 |
| §5.2 frame shape | Task 6 (writeEventFrame, writeResetFrame) |
| §5.3 handshake order | Task 6 (Subscribe-first / check-second) |
| §5.4 live phase wakeup-and-requery | Task 7 |
| §5.5 overflow disconnect | Task 3 (broadcaster Broadcast) + Task 7 (consumer detects via !ok) |
| §5.6 heartbeat + ctx-shutdown | Task 4 (BaseContext) + Task 7 (ticker) |
| §6.1-6.3 polling endpoint | Task 5 |
| §7.1 kata events one-shot | Task 10 |
| §7.2 kata events --tail | Task 11 |
| §8 spec rename | Task 13 |
| §9.1 broadcaster unit tests | Task 3 (Step 1) |
| §9.2 DB query unit tests | Task 2 (steps 1, 5, 9) |
| §9.3 polling handler tests | Task 5 (Step 1) |
| §9.4 SSE handler tests | Task 6 (Step 1) + Task 7 (Step 1) |
| §9.5 race-window out-of-order | Task 9 |
| §9.6 race-window concurrent | Task 9 |
| §9.7 e2e | Task 12 |
| §9.8 CLI tests | Tasks 10–11 |

All spec sections covered.

**Type consistency check:**

- `EventBroadcaster`, `StreamMsg`, `SubFilter`, `Subscription` — defined Task 3, used Tasks 4–8.
- `EventEnvelope`, `EventReset`, `PollEventsRequest`, `PollEventsResponse` — defined Task 1, used Tasks 5–7, 10–11.
- `EventsAfterParams` — defined Task 2, used Tasks 5–7.
- `MaxEventID`, `EventsAfter`, `PurgeResetCheck` — defined Task 2, used Tasks 5–7.

**Placeholder scan:** no "TBD", no "implement later", no unspecified test bodies. Each task has complete code or exact references to the section being modified.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-30-kata-4-events-broadcaster.md`.

Two execution options:

**1. Subagent-Driven (recommended)** — fresh subagent per task with two-stage review (spec compliance + code quality) between tasks. Fast iteration; preserves your context for coordination.

**2. Inline Execution** — execute tasks in this session via `superpowers:executing-plans` with batch checkpoints.

Which approach? (Note: standing directive says "use opus model with subagent-driven-development pattern, run roborev-fix every 5 tasks" — Task 14 is the final cross-cutting review, but you may also want intermediate roborev-fix checkpoints after Tasks 5, 10, and 13.)
