# Plan 4 — SSE Event Broadcaster + Polling Endpoints

> **Status:** design / spec. Companion to `docs/superpowers/specs/2026-04-29-kata-design.md` (the master design). Plan 4 implements the event-stream slice of the master design: §2.6 SSE durability, §4.8 SSE protocol, §4.11 event polling, and the §3.5 purge-cursor consumer.

## 1. Goal

Build a durable event-stream subsystem that (a) lets agents long-poll for new events with `GET /api/v1/events`, and (b) lets clients subscribe to a live SSE stream that picks up where they left off. The stream is the foundation for Plan 6's TUI live updates, modeled on roborev's "ring bell, refetch" pattern but with a durable replay path.

The Plan 3 destructive ladder already writes `purge_log.purge_reset_after_event_id` (a reserved synthetic cursor) on every purge that deletes events. Plan 4 makes that cursor mean something — both endpoints translate it into a `sync.reset_required` signal, and live SSE subscribers receive a corresponding broadcast.

## 2. Scope

**In scope**
- In-memory `EventBroadcaster` that fans out wakeups + reset signals to subscribers.
- `GET /api/v1/events/stream[?project_id=N]` SSE endpoint with `Last-Event-ID` resume, drain-then-live handoff, heartbeats, and `sync.reset_required` framing.
- `GET /api/v1/events?after_id=N&limit=L` and `GET /api/v1/projects/{project_id}/events?after_id=N&limit=L` polling endpoints.
- `cmd/kata events` (one-shot poll) and `kata events --tail` (NDJSON-over-SSE consumer with reconnect/backoff).
- Wire types in `internal/api/events.go` shared between the daemon, the CLI, and any future TUI.
- Mutation handlers extended to broadcast every committed event row post-commit (`handlers_issues.go`, `handlers_actions.go`, `handlers_links.go`, `handlers_labels.go`, `handlers_comments.go`, `handlers_ownership.go`, `handlers_destructive.go`).
- Purge handler extended to broadcast `sync.reset_required` post-commit when `purge_reset_after_event_id` is non-null.
- `internal/daemon/server.go` updated to set `http.Server.BaseContext` so daemon shutdown reliably cancels in-flight SSE handlers.

**Out of scope (deferred)**
- Cross-project list endpoint `GET /api/v1/issues` (master spec §4.9). Independent feature; small follow-up plan.
- Multi-token AND search queries (master spec §4.10 plus FTS escaping notes in Plan 3 §11). Separate work.
- Hooks (Plan 5) — share the event-type vocabulary but dispatch through a different mechanism.
- TUI consumer (Plan 6) — `kata events --tail` is the reference implementation it will mirror.
- `kata config get/set` exposure for heartbeat interval, replay cap, channel buffer, polling limit (Plan 7).
- Authentication on SSE — single-user local daemon; CSRF guard already handles drive-by browser attacks (master spec §2.9).

## 3. Architecture summary

The broadcaster is a wakeup channel, not an ordered event delivery channel. Every committed event row is broadcast post-commit, but the SSE handler treats each broadcast as "something changed at or below ID `N`" and re-queries the DB to get canonical, totally-ordered output. The DB is the only source of ordering truth; the broadcaster only saves polling latency.

This means:
- Two concurrent commits whose broadcast calls reorder cannot produce out-of-order SSE frames.
- A subscriber that connects with `Last-Event-ID: K` and a high-water mark `M` captured immediately after Subscribe reads `events WHERE id IN (K, M]` from the DB (the "drain"), then forwards live broadcasts where the re-query catches `(lastSent, msg.Event.ID]` — so the live phase composes seamlessly with the drain.
- Late-arriving wakeups for events already covered by an earlier re-query are no-ops.

Reset signals flow through the same channel as event wakeups but use a distinct envelope kind so `sync.reset_required` can never be confused with a normal event row.

## 4. Components

### 4.1 Wire types (`internal/api/events.go`)

```go
package api

import (
    "encoding/json"
    "time"
)

// EventEnvelope is the JSON shape carried in SSE data: lines and in
// PollEventsResponse.Events. Field-for-field mirror of db.Event but defined
// here so the wire schema is independent of internal storage shape.
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

// EventReset is the data: payload of a sync.reset_required frame and the
// stripped-down content of a poll response when the cursor falls inside a
// purge gap.
type EventReset struct {
    EventID       int64 `json:"event_id"`        // == ResetAfterID; mirrored for consistency with the SSE id: line.
    ResetAfterID  int64 `json:"reset_after_id"`  // minimum safe resume cursor; client uses this for next request.
}

// PollEventsResponse is the envelope returned by GET /api/v1/events and
// GET /api/v1/projects/{project_id}/events. Exactly one of {events,
// reset_after_id} is informative depending on reset_required.
type PollEventsResponse struct {
    Body struct {
        ResetRequired bool             `json:"reset_required"`
        ResetAfterID  int64            `json:"reset_after_id,omitempty"`
        Events        []EventEnvelope  `json:"events"`           // always non-nil; empty array on no rows
        NextAfterID   int64            `json:"next_after_id"`    // = max events.id in response, or after_id if empty
    }
}
```

### 4.2 Broadcaster (`internal/daemon/broadcaster.go`)

```go
package daemon

import (
    "sync"
    "github.com/wesm/kata/internal/db"
)

// StreamMsg is the envelope on each subscriber's channel. Kind discriminates
// between an event wakeup and a reset signal so callers can never confuse the
// two.
type StreamMsg struct {
    Kind      string    // "event" | "reset"
    Event     *db.Event // non-nil iff Kind == "event"
    ResetID   int64     // non-zero iff Kind == "reset"
    ProjectID int64     // 0 = cross-project (used for filter matching)
}

// SubFilter restricts which broadcasts a subscriber sees. Empty ProjectID
// (zero) means "every event" (cross-project subscriber).
type SubFilter struct {
    ProjectID int64 // 0 = all projects
}

func (f SubFilter) matches(msg StreamMsg) bool {
    if f.ProjectID == 0 {
        return true // cross-project subscriber sees everything
    }
    return msg.ProjectID == f.ProjectID
}

// Subscription is the handle returned to a caller who has registered for
// broadcasts. The caller must call Unsub() when done; Ch is closed by the
// broadcaster on overflow disconnect or by Unsub() on caller exit.
type Subscription struct {
    Ch    <-chan StreamMsg
    Unsub func()
}

// EventBroadcaster fans out wakeups and reset signals to subscribers. It
// holds no DB reference; callers (the SSE handler) capture their own
// high-water mark via db.MaxEventID after Subscribe.
type EventBroadcaster struct {
    mu       sync.Mutex
    nextID   int
    subs     map[int]*subscriber
}

type subscriber struct {
    ch     chan StreamMsg
    filter SubFilter
}

func NewEventBroadcaster() *EventBroadcaster {
    return &EventBroadcaster{subs: map[int]*subscriber{}}
}

// Subscribe inserts a new subscriber. The returned channel buffer is 256;
// when a Broadcast call cannot send without blocking, the broadcaster closes
// the channel and removes the subscriber (overflow disconnect). The caller
// (SSE handler) detects this via select on a closed channel and exits, after
// which the client reconnects with Last-Event-ID and resumes via DB replay.
func (b *EventBroadcaster) Subscribe(filter SubFilter) Subscription {
    b.mu.Lock()
    defer b.mu.Unlock()
    id := b.nextID
    b.nextID++
    ch := make(chan StreamMsg, 256)
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

// Broadcast fans msg out to all matching subscribers. Blocking sends are
// converted to overflow disconnects: close the channel, remove from map.
// Single full-Lock keeps the implementation small; single-user daemon
// throughput doesn't justify the RLock+Lock dance.
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

### 4.3 DB queries (`internal/db/queries_events.go`)

```go
package db

import "context"

type EventsAfterParams struct {
    AfterID    int64 // exclusive lower bound (events.id > AfterID)
    ProjectID  int64 // 0 = cross-project; nonzero adds AND project_id = ?
    ThroughID  int64 // 0 = no upper bound; nonzero adds AND id <= ThroughID
    Limit      int   // caller clamps; queries enforce LIMIT only, not bounds
}

func (db *DB) EventsAfter(ctx context.Context, p EventsAfterParams) ([]Event, error)

// MaxEventID returns the highest events.id, or 0 if the table is empty.
func (db *DB) MaxEventID(ctx context.Context) (int64, error)

// PurgeResetCheck returns the maximum purge_reset_after_event_id strictly
// greater than afterID, optionally constrained to project_id. Returns 0 if
// no matching purge_log rows.
//
//   resetTo, err := db.PurgeResetCheck(ctx, cursor, projectID)
//   if resetTo > 0 { /* cursor invalidated */ }
//
// The per-project SSE/poll endpoint passes projectID; the cross-project
// poll endpoint passes 0.
func (db *DB) PurgeResetCheck(ctx context.Context, afterID, projectID int64) (resetTo int64, err error)
```

The `ThroughID` upper bound is what makes the SSE drain race-free: the handler captures `hwm = MaxEventID()` after Subscribe, then drains `EventsAfter(cursor, projectID, ThroughID: hwm, Limit: 10001)`. Anything above `hwm` is left to the live phase.

### 4.4 Server wiring (`internal/daemon/server.go` modifications)

Add to `ServerConfig`:

```go
type ServerConfig struct {
    DB          *db.DB
    StartedAt   time.Time
    Endpoint    DaemonEndpoint
    Broadcaster *EventBroadcaster // nil → NewServer fills with NewEventBroadcaster()
}
```

`NewServer` initializes `cfg.Broadcaster` if nil. `registerRoutes` signature grows to take `mux *http.ServeMux` so the SSE handler can be registered as a raw `http.HandlerFunc` (Huma's response model doesn't fit a streaming response).

`Serve` adds `BaseContext` so SSE handlers reliably exit on daemon shutdown:

```go
httpSrv := &http.Server{
    Handler:           s.handler,
    ReadHeaderTimeout: 10 * time.Second,
    BaseContext:       func(net.Listener) context.Context { return ctx },
}
```

Without `BaseContext`, request contexts are rooted in the listener's accept goroutine and are not cancelled on `Shutdown`. With it, `r.Context().Done()` fires when the daemon ctx cancels, every SSE select loop exits, and `Shutdown` finishes within its 10s grace window.

### 4.5 Mutation handlers (modifications)

Every handler that commits an event row broadcasts post-commit. The broadcast happens **once per committed event**, not once per response.

**No-op guard.** Per master spec §4.5, several mutation paths return `event: null, changed: false` because no row was actually inserted (close-already-closed, label-already-applied, link-already-linked, idempotent create reuse, etc.). These paths must **not** broadcast — the implementation is a defensive nil/zero check before the `Broadcast` call. Idempotent reuse in particular must skip broadcasting even though the response carries an `original_event` field (the reused issue's original `issue.created`); that event was already broadcast at original-creation time.

Concretely:

- `handlers_issues.go` (create) — broadcast the `issue.created` event.
- `handlers_issues.go` (edit) — broadcast `issue.updated`.
- `handlers_actions.go` (close, reopen) — broadcast `issue.closed` / `issue.reopened`.
- `handlers_links.go` (create link) — broadcast the `issue.linked` event. **Plus**, in the parent `--replace` path (handlers_links.go:85-112), the prior `DeleteLinkAndEvent` emits an `issue.unlinked` event in its own TX; that event MUST also be broadcast. Two events committed → two `Broadcast` calls.
- `handlers_links.go` (delete link) — broadcast `issue.unlinked`.
- `handlers_labels.go` (add, rm) — broadcast `issue.labeled` / `issue.unlabeled`.
- `handlers_comments.go` — broadcast `issue.commented`.
- `handlers_ownership.go` (assign, unassign) — broadcast `issue.assigned` / `issue.unassigned`.
- `handlers_destructive.go` (delete) — broadcast `issue.soft_deleted`.
- `handlers_destructive.go` (restore) — broadcast `issue.restored`.
- `handlers_destructive.go` (purge) — `Broadcast(StreamMsg{Kind:"reset", ResetID: <reserved>, ProjectID: <pid>})` if `purge_reset_after_event_id` is non-null. Purge does not produce a persistent event.

Pattern (sketch, per handler):

```go
// existing path: DB commit returns updated row + event (or zero event for no-op)
issue, evt, changed, err := cfg.DB.SomeMutationAndEvent(ctx, ...)
if err != nil { return mapErr(err) }

if changed && evt.ID != 0 {
    cfg.Broadcaster.Broadcast(daemon.StreamMsg{
        Kind:      "event",
        Event:     &evt,
        ProjectID: in.ProjectID,
    })
}
return responseFor(issue, evt, changed), nil
```

The exact return shape varies per handler — some return `(issue, evt, err)` only, with the no-op signaled by a sentinel zero `evt.ID` and a `changed=false` in the response builder. Whichever convention applies to a given handler, the broadcast precondition is "a real event row was inserted." The implementation must check both before calling `Broadcast`.

For the parent `--replace` path:

```go
if existing parent && replacing with a different parent {
    _, unlinkEvt, err := cfg.DB.DeleteLinkAndEvent(ctx, existing, unlinkParams)
    if err != nil { return mapErr(err) }
    cfg.Broadcaster.Broadcast(daemon.StreamMsg{
        Kind: "event", Event: &unlinkEvt, ProjectID: in.ProjectID,
    })
}
// then the linked-event broadcast for the new link
```

## 5. SSE protocol

`GET /api/v1/events/stream[?project_id=N][?after_id=K]` plus the `Last-Event-ID` header alternative.

### 5.1 Cursor input and Accept negotiation

Exactly one of `Last-Event-ID` (header) and `?after_id=K` (query) is allowed. Both → `400 cursor_conflict`. Neither → cursor 0 (replay from the beginning, capped by the stale-cap policy below).

Per master spec §4.4, the request **must** include `Accept: text/event-stream`. Missing or non-matching Accept → `406 not_acceptable` (JSON envelope, before any SSE bytes). Acceptable values are `text/event-stream` and `*/*`; `application/json` and other types are rejected. The check happens before cursor parsing so badly typed clients fail fast.

### 5.2 Frame shape

```
: connected

id: 81235
event: issue.commented
data: {"event_id":81235,"type":"issue.commented","project_id":3,"project_identity":"github.com/wesm/kata","issue_number":42,"actor":"claude-4.7","payload":{"comment_id":104},"created_at":"2026-04-29T14:22:11.482Z"}

id: 81236
event: sync.reset_required
data: {"event_id":81236,"reset_after_id":81236}

: keepalive
```

- Initial `: connected\n\n` comment frame is written and flushed immediately after headers, before any drain query — confirms handshake without sending data.
- `event:` line is the fully qualified `events.type` (e.g. `issue.commented`) or the literal `sync.reset_required`. Same vocabulary used by hook matchers (master spec §3.3, §8.3).
- `data:` is single-line JSON (`EventEnvelope` for events, `EventReset` for resets).
- Heartbeats are `: keepalive\n\n` every 25s. Comments are no-ops per the SSE spec; their purpose is to keep the TCP connection alive through stateful middleboxes.

### 5.3 Handshake order — Subscribe-first, check-second

The SSE handler executes the following steps in order:

1. **Validate Accept** (per §5.1): `Accept` header must be `text/event-stream` or `*/*`; otherwise return 406 `not_acceptable` (JSON envelope, no SSE bytes written).
2. Parse cursor; reject `cursor_conflict` (both query and header) with 400.
3. Write headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`), write `: connected\n\n`, flush.
4. **Subscribe first:** `sub := cfg.Broadcaster.Subscribe(filter)`. From this point any commit+broadcast lands on `sub.Ch`.
5. **Then capture high-water mark:** `hwm, err := cfg.DB.MaxEventID(ctx)`. On error, write nothing further and return; the client sees a closed stream and reconnects.
6. **Then check for purge-invalidated cursor:** `resetTo, err := cfg.DB.PurgeResetCheck(ctx, cursor, projectID)`. If `resetTo > 0`: write the reset frame, flush, `sub.Unsub()`, return.
7. **Drain:** `events, err := cfg.DB.EventsAfter(ctx, EventsAfterParams{AfterID: cursor, ProjectID: projectID, ThroughID: hwm, Limit: 10001})`.
8. **Stale-cap:** if `len(events) == 10001`, the client is too far behind. Discard the slice, write a `sync.reset_required` frame with `id: <hwm>` and `data.reset_after_id: <hwm>`, flush, `sub.Unsub()`, return.
9. Write the ≤10000 drained events as frames in id order. Track `lastSent` as the last frame's id (or `cursor` if no frames were written).
10. **Live phase:** see §5.4.

The Subscribe-first ordering closes the reset race: any purge that committed and broadcast its reset between cursor parse and Subscribe is captured by `PurgeResetCheck`; any purge after Subscribe lands on `sub.Ch` and is handled by the live phase. Between Subscribe and `PurgeResetCheck`, both paths may detect the same reset; `PurgeResetCheck` wins by emitting the frame and unsubscribing (the channel-side reset is dropped when we close).

### 5.4 Live phase — wakeup-and-requery

`lastSent` enters this loop as initialized in §5.3 step 9: the id of the last drained frame, or `cursor` if no frames were written.

```go
ticker := time.NewTicker(25 * time.Second)
defer ticker.Stop()

for {
    select {
    case <-ctx.Done():
        return
    case <-ticker.C:
        if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil { return }
        flush(w)
    case msg, ok := <-sub.Ch:
        if !ok { return } // overflow disconnect
        switch msg.Kind {
        case "reset":
            writeResetFrame(w, msg.ResetID)
            flush(w)
            return
        case "event":
            // Defensive: a concurrent purge may have committed before this
            // event broadcast was processed (broadcaster lock race). Surface
            // the reset before any post-purge frames so a client cannot
            // disconnect at id > resetAfterID and silently miss the reset.
            resetTo, err := cfg.DB.PurgeResetCheck(ctx, lastSent, projectID)
            if err != nil { return }
            if resetTo > 0 {
                writeResetFrame(w, resetTo)
                flush(w)
                return
            }
            // Drain every row at or below the wakeup's id. A single broadcast
            // can carry > sseLiveBatch pending rows; without the loop, the
            // tail would languish in the DB until the next wakeup.
            through := msg.Event.ID
            for {
                rows, err := cfg.DB.EventsAfter(ctx, EventsAfterParams{
                    AfterID: lastSent, ProjectID: projectID, ThroughID: through, Limit: 1000,
                })
                if err != nil { return }
                for _, ev := range rows {
                    writeEventFrame(w, ev)
                    flush(w)
                    lastSent = ev.ID
                }
                if len(rows) < 1000 { break }
            }
        }
    }
}
```

Properties:
- DB is the ordering authority. Two concurrent commits with reordered Broadcast calls are emitted in DB-id order: each wakeup re-queries `(lastSent, msg.Event.ID]`.
- Late-arriving wakeups for IDs already covered by an earlier re-query no-op (`EventsAfter(lastSent, ..., ThroughID: <stale id>)` returns nothing if `<stale id> ≤ lastSent`).
- A burst of N commits produces N wakeups but each re-query catches everything up to its `ThroughID`. Coalescing is automatic.
- A wakeup that carries more than `sseLiveBatch` rows is fully drained inside the loop (re-query until `len(rows) < sseLiveBatch`), not stranded for the next wakeup.
- Reset signals bypass the requery path entirely and are terminal. Both channel-side ("reset" Kind) and DB-side (PurgeResetCheck) paths terminate the live phase; whichever is observed first wins.

`lastSent` is server-side state for the live loop's de-duplication. It is **not** the client's cursor. The client's `Last-Event-ID` only advances when it receives a frame with an `id:` line. If the drain emits no frames (empty drain on reconnect), the client cursor stays at its prior value — harmless.

### 5.5 Overflow

The broadcaster's `Broadcast` uses non-blocking `select` sends. When a subscriber's channel buffer (256) is full, the broadcaster closes the channel and removes the subscriber. The SSE handler's `select` sees `!ok` from the channel read and returns. The HTTP response closes; the client's reconnect path resumes via `Last-Event-ID` + DB replay. No event is lost.

In the wakeup-and-requery model, real overflow is rare: the channel carries at most one wakeup per commit, and the SSE handler drains it with a quick re-query. Bursty writes against a paused client (e.g., terminal in background) can still trigger overflow, which is the intended failure mode.

### 5.6 Heartbeats and shutdown

- Heartbeat interval: 25s, via `time.NewTicker`. Plan 7 may expose this as a `kata config` knob.
- On `ctx.Done()` (daemon shutdown via `BaseContext` propagation, or client close): the `select` exits, `defer sub.Unsub()` runs, and the response closes.
- v1 sets no per-write deadline. A wedged write blocks the SSE goroutine until the kernel's socket buffer fills and the OS closes the connection. For local-loopback this is acceptable. If we hit problems we can wrap each write/flush with `http.NewResponseController(w).SetWriteDeadline(...)`.

## 6. Polling protocol

`GET /api/v1/events?after_id=N&limit=L` (cross-project) and `GET /api/v1/projects/{project_id}/events?after_id=N&limit=L` (per-project). Both stay Huma-registered.

### 6.1 Inputs

- `after_id` (query, optional, default 0). Negative or non-numeric → `400 validation`.
- `limit` (query, optional, default 100). Malformed or non-positive (`<= 0`, non-numeric) → `400 validation`. Values `> 1000` are silently clamped to 1000 (no error). The asymmetry is intentional: a client that accidentally over-asks gets the maximum allowed page; a client that under-specifies (`0`, `-5`) is signaling a bug and gets a hard error.
- `project_id` (path, required for the per-project endpoint). Malformed → `400 validation`.

`Last-Event-ID` is **not** accepted on polling endpoints — `?after_id` is the only supported cursor input. `cursor_conflict` is an SSE-only error.

### 6.2 Response

```json
{
    "reset_required": false,
    "events": [
        { "event_id": 101, "type": "issue.commented", ... },
        ...
    ],
    "next_after_id": 105
}
```

When the cursor falls inside a purge gap:

```json
{
    "reset_required": true,
    "reset_after_id": 81236,
    "events": [],
    "next_after_id": 81236
}
```

`events` is always a non-nil array (`[]`, never `null`). `next_after_id` is the max `event_id` in the response, or echoes `after_id` when the response is empty (so a polling client never goes backward).

### 6.3 Server flow

1. Validate inputs. Return `400 validation` on malformed/out-of-range.
2. `resetTo, err := cfg.DB.PurgeResetCheck(ctx, afterID, projectID)`. On err → `500 internal`.
3. If `resetTo > 0`: return `{reset_required:true, reset_after_id:resetTo, events:[], next_after_id:resetTo}`.
4. Otherwise: `events, err := cfg.DB.EventsAfter(ctx, EventsAfterParams{AfterID: afterID, ProjectID: projectID, Limit: clamp(limit, 1, 1000)})`. Return `{reset_required:false, events:..., next_after_id: maxOrEcho(events, afterID)}`.

The endpoint snapshot does not need a `ThroughID` — pure pagination, no concurrent live stream to coordinate against.

## 7. CLI surface

### 7.1 `kata events` (one-shot poll)

```
kata events [--project-id N | --all-projects] [--after K] [--limit L] [--json]
```

- Default scope: the current workspace's project (resolved via the standard project-resolution flow). `--project-id N` overrides; `--all-projects` uses the cross-project endpoint.
- `--after K` sets the polling cursor (default 0).
- `--limit L` defaults to 100, max 1000.
- Without `--json`: human-friendly columnar output (event id, type, project, issue, actor, time).
- With `--json`: `PollEventsResponse` body printed verbatim. NDJSON is reserved for `--tail`.
- Exit codes per master spec §4.7.

### 7.2 `kata events --tail` (NDJSON-over-SSE)

```
kata events --tail [--project-id N | --all-projects] [--last-event-id K] [--json]
```

- Opens the SSE stream and prints one JSON envelope per line (NDJSON), one envelope per SSE frame. `--json` is implied; included for explicitness.
- Sends `Accept: text/event-stream` on every request (master spec §4.4 / Plan 4 §5.1).
- Reconnect on disconnect: exponential backoff starting at 1s, capping at 30s. Reset to 1s after a successful connection that read at least one frame (matches roborev's pattern).
- On `sync.reset_required` frame: emit a one-line NDJSON envelope `{"reset_required":true, "reset_after_id":N}` (so downstream tooling sees it), then reconnect with the new cursor. Don't exit; the client is expected to refetch state and resume tailing.
- On daemon shutdown / unexpected EOF: log to stderr `daemon disconnected, reconnecting...` (only when `--quiet` is not set), reconnect.
- `--last-event-id K` overrides the initial cursor; default is 0 (consume from the start). Polite use is for resuming a previously interrupted tail.
- Exit codes per master spec §4.7. SIGINT/SIGTERM trigger graceful shutdown (close stream, exit 0).

#### Retryable vs terminal errors

The reconnect/backoff loop is for transient failures, not for malformed local input or server-validated rejection. The classification:

| Class | Examples | Behavior |
|-------|----------|----------|
| **Terminal (fail fast)** | local validation (negative `--last-event-id`, mutually exclusive flags); HTTP 4xx from the daemon (400 validation, 404 project_not_found, 405 method_not_allowed, 406 not_acceptable, 400 cursor_conflict) | Exit non-zero with `ExitUsage` for local arg errors, `ExitInternal` (or surfaced API code) for server rejection. **Never reconnect.** Looping on a 4xx is an infinite spin. |
| **Retryable (backoff)** | TCP/connection errors before headers, EOF after headers, HTTP 5xx, daemon shutdown mid-stream, response-header timeout | Log to stderr (unless `--quiet`), back off, reconnect. Cursor is preserved (last id advanced by the previous attempt). |
| **Reset (semantic)** | `sync.reset_required` frame | Emit the NDJSON envelope and reconnect with the new cursor. Backoff resets to 1s (the daemon is healthy; the cursor was just invalidated). |

The CLI surfaces local validation before any HTTP call so a permanently broken request never enters the reconnect loop. Server-side 4xx responses must be detected at the response-status check (before parsing the body) and propagated as a hard error.

### 7.3 Reference for the Plan 6 TUI

The `--tail` consumer is the canonical client-side pattern for live updates. The Plan 6 TUI's SSE consumer should mirror its reconnect/backoff state machine and its `sync.reset_required` handling. See master spec §7.4 for TUI invalidation behavior.

## 8. Spec cross-references and renames

The master design doc currently uses `new_baseline` as the reset-cursor field name (master spec §4.8 SSE protocol, §4.11 polling). Plan 4 renames this to `reset_after_id` everywhere on the wire because the cursor is a minimum safe resume id, not a state baseline. Plan 4's implementation lands the rename in:

- §4.8 SSE protocol — `data.new_baseline` → `data.reset_after_id`.
- §4.11 event polling — `new_baseline` → `reset_after_id` in the response shape.

The DB column `purge_log.purge_reset_after_event_id` (Plan 1 schema, master spec §3.5) keeps its existing name; only the wire-side projection changes.

## 9. Test plan

### 9.1 Unit — broadcaster (`internal/daemon/broadcaster_test.go`)

- Subscribe / Unsub lifecycle leaves no goroutines or map entries.
- Broadcast to multiple subscribers fans to all matching subscribers and skips non-matching (per-project filter).
- Reset broadcast fans the same way.
- Overflow: subscriber A reads slowly, subscriber B reads promptly; saturate A's buffer; assert A's channel closes and B continues receiving.
- Race fuzz with `-race`: concurrent Subscribe/Broadcast/Unsub for ~10000 iterations. No data races, no panics.

### 9.2 Unit — DB queries (`internal/db/queries_events_test.go`)

- `MaxEventID` on empty table → 0.
- `MaxEventID` after inserts → highest id.
- `EventsAfter` cross-project, with/without `ThroughID`.
- `EventsAfter` per-project filters out events from other projects.
- `EventsAfter` honors `Limit`.
- `PurgeResetCheck` returns 0 when no purges exist.
- `PurgeResetCheck` returns the max `purge_reset_after_event_id` strictly greater than `after_id`.
- `PurgeResetCheck` per-project filter excludes other projects' purges.

### 9.3 Handler — polling (`internal/daemon/handlers_events_test.go`, polling cases)

- Empty result with no purges: `{reset_required:false, events:[], next_after_id:after_id}`.
- After-id inside a purge gap: `{reset_required:true, reset_after_id:N, events:[], next_after_id:N}`.
- Limit clamp: request `limit=99999` → response capped at 1000 (no error).
- Limit invalid: request `limit=0` or `limit=-5` → 400 validation.
- Limit non-numeric: `limit=foo` → 400 validation.
- Per-project endpoint excludes other-project events.
- Per-project endpoint excludes other-project purge resets.
- `next_after_id` echoes `after_id` when events array is empty, advances to max id otherwise.
- Wire shape: `events:[]` (not `null`) when empty.

### 9.4 Handler — SSE (`internal/daemon/handlers_events_test.go`, SSE cases)

- Accept negotiation: missing `Accept` → 406; `Accept: application/json` → 406; `Accept: text/event-stream` → 200 stream; `Accept: */*` → 200 stream. The 406 response is a JSON envelope, not an SSE frame.
- Cursor-conflict: both header and query → 400 `cursor_conflict`.
- Handshake: response writes `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, then `: connected\n\n`.
- Drain: with cursor 0 and N pre-existing events (N < 10000), the client receives N frames in id-order before the live phase begins.
- Drain followed by live: after drain finishes, calling `Broadcaster.Broadcast(...)` emits the new event as a frame.
- Stale-cap: with 10001 pre-existing events, the client receives a single `sync.reset_required` frame with `id: <hwm>` and the stream closes.
- Per-project filter: SSE subscriber with `?project_id=A` does not see events from project B's broadcasts.
- Reset before subscribe: a purge committed before connect with a cursor inside its gap → client receives one `sync.reset_required` frame and stream closes.
- Reset after subscribe: a purge broadcast after Subscribe → client receives `sync.reset_required` via the live channel and stream closes.

### 9.5 Race-window — out-of-order broadcasts (`internal/daemon/broadcaster_race_test.go`)

Pin the wakeup-and-requery model's correctness. The test must arrange for the events to land *after* the SSE consumer is in its live phase — otherwise they'd be drained in id order and the inversion would be invisible.

- Setup: open SSE consumer at cursor 100 against an events table whose max id ≤ 100. Drain returns nothing; lastSent = 100.
- Insert events 101 and 102 into the events table (in id order — SQLite autoincrement enforces this).
- `Broadcast(102)` *first*, then `Broadcast(101)` (intentional inversion).
- Assert: the consumer's first frame has `id: 101`, the second has `id: 102`. No duplicate frames.

The first wakeup (id 102) triggers `EventsAfter(100, projectID, ThroughID: 102)` which returns rows 101 and 102 in id order. lastSent advances to 102. The second wakeup (id 101) triggers `EventsAfter(102, projectID, ThroughID: 101)` which returns no rows.

Run with `-race`. Repeat for many iterations to catch nondeterminism.

### 9.6 Race-window — Subscribe vs. concurrent Broadcast

- Setup: 100 goroutines each call `Subscribe`, drain a small buffer, `Unsub`. In parallel, another 100 goroutines emit `Broadcast` calls with sequential ids.
- Assert with `-race`: no panics, no goroutine leaks. Each subscriber sees a contiguous (possibly empty) suffix of broadcasts after its Subscribe.

### 9.7 e2e (`e2e/e2e_test.go`)

`TestSmoke_Plan4Events`:
1. Start daemon, init two projects (A, B) with one issue each.
2. **Capture baseline cursor:** poll `GET /api/v1/projects/{A}/events?after_id=0` and record `next_after_id` as `baseline_A`. Same for project B → `baseline_B` (used only as a sanity guard). The baseline absorbs the setup `issue.created` events so subsequent assertions count only the events the test itself produces.
3. Open SSE consumer for project A from `baseline_A` (via `kata events --tail --project-id A --last-event-id <baseline_A>`).
4. Open polling client for project A from `baseline_A`.
5. Mutate project A: comment, label, assign. Mutate project B: comment.
6. SSE consumer sees three frames (A's three events) — and no project B events.
7. Polling client returns three events on next poll, advances `next_after_id`. Subsequent poll from the new cursor returns empty events with `next_after_id` echoed.
8. `--replace` parent path: create issue 3 in project A with parent issue 1, then re-link to parent issue 2 with `--replace`. SSE consumer sees the new `issue.created` frame, then an `issue.unlinked` frame (for the old parent), followed by an `issue.linked` frame (for the new parent). Three frames total for this step.
9. Purge issue 1 in project A. SSE consumer sees `sync.reset_required` and the stream closes. Polling client (with cursor below the reset value) gets `{reset_required:true, reset_after_id:N, events:[], next_after_id:N}`.
10. Reconnect SSE with the reset cursor; resume cleanly with no further events.

### 9.8 CLI — `cmd/kata/events_test.go`

- `kata events` (one-shot, no flags) prints results for the resolved project.
- `kata events --json` returns valid `PollEventsResponse`.
- `kata events --all-projects` queries the cross-project endpoint.
- `kata events --tail --json` streams NDJSON, one envelope per line.
- `kata events --tail` reconnects after a simulated daemon close (via httptest server lifecycle); backoff is exercised with a fake clock.
- `kata events --tail` follows `sync.reset_required`: re-fetches with `?after_id=<reset_after_id>` and continues tailing.

## 10. Migration / compatibility notes

- `0001_init.sql` already declares `purge_log`, `events`, and the `idx_events_idempotency` partial index (Plan 1). No schema changes for Plan 4.
- The `new_baseline` → `reset_after_id` rename is a wire-shape change. Plan 4 is the first plan to expose the polling and SSE endpoints to clients, so no compatibility shim is needed. The master spec doc gets edited as part of Plan 4.
- `cmd/kata/events.go` is new; no conflicts with Plan 3.

## 11. Open questions / tunables

- **Heartbeat interval (25s):** matches master spec §4.8. Plan 7 may expose as `kata config events.heartbeat_seconds`.
- **Subscriber channel buffer (256):** large enough to absorb commit bursts under the wakeup-and-requery model. Plan 7 may expose as `events.channel_buffer`.
- **Drain row cap (10000):** matches master spec §4.8 ("bounded ~10k rows"). Plan 7 may expose as `events.replay_cap`.
- **Polling default/max limit (100/1000):** Plan 7 may expose.
- **Per-write deadline on SSE (none in v1):** revisit if production traces show wedged writes. The mitigation lives in `http.NewResponseController(w).SetWriteDeadline(...)` if needed.

## 12. Implementation order (for the Plan 4 plan doc)

This spec hands off to `superpowers:writing-plans`. The expected task ordering:

1. `internal/api/events.go` — wire types.
2. `internal/db/queries_events.go` — `MaxEventID`, `EventsAfter`, `PurgeResetCheck` with table-driven tests.
3. `internal/daemon/broadcaster.go` — `EventBroadcaster`, `StreamMsg`, `SubFilter`, with broadcaster-only unit tests.
4. `internal/daemon/server.go` — wire `Broadcaster` into `ServerConfig`, add `BaseContext`, grow `registerRoutes` signature.
5. `internal/daemon/handlers_events.go` — polling endpoints first (Huma).
6. `internal/daemon/handlers_events.go` — SSE endpoint with handshake + Subscribe-first ordering + drain + stale-cap.
7. `internal/daemon/handlers_events.go` — SSE live phase (wakeup-and-requery loop, reset handling, heartbeat).
8. Wire broadcasts into mutation handlers (issues, links, labels, comments, assignment, destructive). Two events for the `--replace` parent path.
9. Race-window tests: out-of-order broadcasts, concurrent Subscribe/Broadcast.
10. `cmd/kata/events.go` — one-shot polling CLI.
11. `cmd/kata/events.go` — `--tail` SSE consumer with reconnect/backoff and reset handling.
12. e2e smoke covering all 9 steps in §9.7.
13. Master spec doc edits: §4.8 / §4.11 `new_baseline` → `reset_after_id`.
14. Cross-cutting review.
