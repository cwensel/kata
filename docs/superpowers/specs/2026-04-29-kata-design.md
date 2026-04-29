# kata — Lightweight Issue Tracker for Agents

**Status:** Design (v1)
**Date:** 2026-04-29
**Topic:** kata — a local SQLite + daemon + TUI issue tracker, agent-first, modeled on the roborev modality.

## 1. Overview

kata replaces ad-hoc use of GitHub Issues for agent task-tracking. It is a single-binary local tool with a long-lived daemon, a SQLite database, a CLI, and a Bubble Tea TUI. Agents are the primary writers; humans observe and steer through the TUI.

The shape is borrowed deliberately from roborev: pure-Go SQLite via `modernc.org/sqlite`, a Huma-based HTTP API on a Unix socket (TCP loopback fallback on Windows), per-PID runtime files, durable SSE event stream, and directory-style installable agent skills. Where roborev runs review/fix workloads, kata runs only issue CRUD plus an event broadcaster and a small bounded hook runner — there is no agentic review worker pool and no daemon-driven code execution beyond the user's own configured hook scripts.

The design optimizes for three things:

1. **Agent ergonomics.** Stable JSON, stable exit codes, search-before-create, idempotency keys with fingerprints, structured error envelopes, no implicit `$EDITOR` invocation in machine paths.
2. **Auditability.** Every state change appends to an immutable `events` table with the actor recorded. Comments are append-only. Deletion has a soft tier (`kata delete --force`) and a hard tier (`kata purge --force --confirm`) gated by interactive prompt or exact-string flag. The hard tier writes an out-of-cascade `purge_log` row.
3. **A small, sharp surface.** Issue lifecycle, three relationship types (`parent`, `blocks`, `related`), labels, owners, comments. No `in_progress` status, no severities, no priorities, no attachments, no threaded replies, no markdown rendering.

## 2. Architecture

```
CLI (kata) ──HTTP/JSON──> Daemon ──> SQLite (~/.kata/kata.db, WAL, FK ON)
                            │
                            ├─> SSE event broadcaster (durable, resumable)
                            └─> Bounded hook runner (post-commit, no shell)

TUI (kata tui) ──HTTP + SSE──> Daemon
```

### 2.1 Stack

- Go, single binary. Pure-Go SQLite (`modernc.org/sqlite`). WAL mode.
- Huma for HTTP API + OpenAPI generation.
- Cobra for CLI. Bubble Tea + Lipgloss for TUI.
- testify for tests. Table-driven, `t.TempDir()`, `-shuffle=on`. No CGO.
- All timestamps UTC, RFC3339 at API boundaries, RFC3339 with milliseconds at SQLite boundary.

### 2.2 Daemon transport

The daemon listens on one of:

- **Unix socket (default on Unix)**: parent dir `0700`, socket `0600`.
- **TCP loopback (default on Windows; opt-in elsewhere)**: address validated to be loopback (`127.0.0.1`, `::1`, `localhost`); port auto-increments from `7474` if busy.

`DaemonEndpoint` abstraction handles both transports (matches roborev's `internal/daemon/endpoint.go`).

### 2.3 Per-PID runtime files; DB-namespaced

```
$KATA_DATA_DIR/runtime/<dbhash>/
  daemon.<pid>.json     # 0644, atomic write-and-rename
  daemon.log            # rotated 10MB × 5
```

`<dbhash>` = first 12 hex chars of `sha256(absolute(effective_db_path))`. Two `KATA_DB` instances never collide on runtime files, sockets, or logs.

The socket path is **recorded inside** the runtime file. Socket itself lives in `$XDG_RUNTIME_DIR/kata/<dbhash>/daemon.sock` (ephemeral runtime dir is fine; runtime file in data dir is what makes it discoverable).

Clients discover the daemon by:

1. Compute `<dbhash>` from effective DB path.
2. Scan `$KATA_DATA_DIR/runtime/<dbhash>/daemon.*.json`.
3. For each, probe `GET /api/v1/ping`. Live = use it; dead = clean up the file.
4. If none live, auto-start daemon.

Cleanup of stale files via `ListAllRuntimes` + `/ping` liveness probe.

### 2.4 Repo identity

Daemon-side resolution. CLI sends `root_path`; daemon computes identity in this order:

1. `<repo_root>/.kata-id` file (validated — non-empty, charset constrained, not a URL with credentials). User override for forks, mirrors, monorepo splits, identity changes.
2. Git remote `origin` URL → strip credentials → normalize SSH↔HTTPS variants.
3. Any other git remote → same normalization.
4. `local://<absolute_path>` fallback.

`repos.identity` is `UNIQUE`. `repos.root_path` is "last seen path" — updated on every successful resolve. Multiple clones at different paths share one `identity` row.

Clients never construct or submit identities; only paths.

### 2.5 Read/write split

All v1 reads go through the daemon. No direct SQLite access from the CLI in v1.

A future `OpenReadOnly` (no migrations, no PRAGMA mutation, schema-version check) can land later for hot paths if measured. Don't pay that complexity tax until measured.

### 2.6 SSE durability

- Events have monotonic `event_id` (= `events.id`), `actor`, `repo_id`, `repo_identity` (snapshot), `issue_id`, `issue_number`, `related_issue_id` (nullable), `type`, `payload`, `created_at`.
- Persisted in the `events` table (which is also the audit trail).
- Daemon broadcasts only **after DB commit**, with the row's `event_id` as the SSE `id:` field.
- **Purge reserves a synthetic SSE cursor** strictly greater than the current max `events.id`. Concretely: in the same transaction that purges an issue, if any events were deleted, the daemon advances `sqlite_sequence.seq` for `events` by one (without inserting a row) and stores that reserved value as `purge_log.purge_reset_after_event_id`. Future real `events.id` values continue from `reserved + 1`, so the synthetic cursor is unique and unattainable by any real event.
- On reconnect with `Last-Event-ID` (or `?after_id=N`; both → 400), daemon computes `MAX(purge_reset_after_event_id) FROM purge_log WHERE purge_reset_after_event_id > <cursor>`. The per-repo stream (`?repo_id=N`) adds `AND repo_id = ?` so a purge in some other repo can't invalidate this client's cursor; the cross-repo stream omits the predicate. If the result is non-null, the client's cursor is invalidated. Because every reserved cursor exceeds every event id that existed at the corresponding purge time, even a client at max-at-purge will be reset (strict `>` is correct against a strictly-greater reserved value; no off-by-one miss, no need for `>=`).
- If invalidated, daemon sends a single `sync.reset_required` synthetic event with `id:` = the **MAX** of all matching `purge_reset_after_event_id`s, then closes the stream. Using the max ensures one reset moves the client past every accumulated purge gap; the client adopts that id as its new cursor and refetches state.

### 2.7 Hooks (preview, full design §7)

- After-DB-commit, async, bounded concurrency (default pool=4, queue=1000), per-hook timeout (default 30s, max 5m).
- `exec.Command(cmd, args...)`. **No shell, no env-var expansion of args.** Data flows via JSON on stdin and a small set of `KATA_*` env scalars.
- Configured globally and per-repo. Per-repo configs loaded on demand with mtime cache.

### 2.8 Auditability summary

- `events` table is append-only and authoritative for state changes.
- `comments` table is append-only — no edit, no delete (purge only).
- Issues are mutable in their current-state row; the events log captures every mutation with field diffs in `payload`.
- Soft-delete sets `deleted_at`; reversible via `kata restore`.
- Purge requires `kata purge <id> --force --confirm "PURGE #N"`, writes a `purge_log` row that survives the cascade, then physically removes `comments`/`links`/`labels`/`events` for the issue and the issue itself.

### 2.9 Browser CSRF defense

The HTTP server rejects any non-empty `Origin` header (including `null`), requires `Content-Type: application/json` on mutations, and emits no CORS headers. CLI/TUI never set `Origin` so they're unaffected; this prevents drive-by browser exploits against the loopback socket.

## 3. Data Model

All schema lives in numbered files under `internal/db/migrations/`. The single `0001_init.sql` baseline is below; future migrations are additive.

### 3.1 DB open path

Every connection runs:

```sql
PRAGMA foreign_keys  = ON;       -- enforce FKs (default OFF; per-connection)
PRAGMA journal_mode  = WAL;
PRAGMA synchronous   = NORMAL;
PRAGMA busy_timeout  = 5000;
```

`foreign_keys=ON` is part of the data model contract.

### 3.2 Schema (0001_init.sql)

```sql
CREATE TABLE repos (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  identity          TEXT UNIQUE NOT NULL,
  root_path         TEXT NOT NULL,
  name              TEXT NOT NULL,
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  next_issue_number INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE issues (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id       INTEGER NOT NULL REFERENCES repos(id),
  number        INTEGER NOT NULL,
  title         TEXT NOT NULL,
  body          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL CHECK(status IN ('open','closed')) DEFAULT 'open',
  closed_reason TEXT CHECK(closed_reason IN ('done','wontfix','duplicate')),
  owner         TEXT,
  author        TEXT NOT NULL,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  closed_at     TEXT,
  deleted_at    TEXT,
  UNIQUE(repo_id, number),
  CHECK (length(trim(title))  > 0),
  CHECK (length(trim(author)) > 0),
  CHECK (status = 'closed' OR (closed_at IS NULL AND closed_reason IS NULL))
);
CREATE INDEX idx_issues_repo_status_updated
  ON issues(repo_id, status, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_repo_updated
  ON issues(repo_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_issues_owner
  ON issues(owner) WHERE owner IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE comments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id   INTEGER NOT NULL REFERENCES issues(id),
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(author)) > 0),
  CHECK (length(trim(body))   > 0)
);
CREATE INDEX idx_comments_issue ON comments(issue_id, created_at);

CREATE TABLE links (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id       INTEGER NOT NULL REFERENCES repos(id),
  from_issue_id INTEGER NOT NULL REFERENCES issues(id),
  to_issue_id   INTEGER NOT NULL REFERENCES issues(id),
  type          TEXT NOT NULL CHECK(type IN ('parent','blocks','related')),
  author        TEXT NOT NULL,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(from_issue_id, to_issue_id, type),
  CHECK (from_issue_id <> to_issue_id),
  CHECK (length(trim(author)) > 0),
  CHECK (type <> 'related' OR from_issue_id < to_issue_id)
);
CREATE UNIQUE INDEX uniq_one_parent_per_child
  ON links(from_issue_id) WHERE type = 'parent';
CREATE INDEX idx_links_from    ON links(from_issue_id, type);
CREATE INDEX idx_links_to      ON links(to_issue_id, type);
CREATE INDEX idx_links_repo    ON links(repo_id);

-- Enforce same-repo: both endpoints must belong to links.repo_id.
CREATE TRIGGER trg_links_same_repo_insert
BEFORE INSERT ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'cross-repo links are not allowed')
  WHERE (SELECT repo_id FROM issues WHERE id = NEW.from_issue_id) <> NEW.repo_id
     OR (SELECT repo_id FROM issues WHERE id = NEW.to_issue_id)   <> NEW.repo_id;
END;
CREATE TRIGGER trg_links_same_repo_update
BEFORE UPDATE ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'cross-repo links are not allowed')
  WHERE (SELECT repo_id FROM issues WHERE id = NEW.from_issue_id) <> NEW.repo_id
     OR (SELECT repo_id FROM issues WHERE id = NEW.to_issue_id)   <> NEW.repo_id;
END;

CREATE TABLE issue_labels (
  issue_id   INTEGER NOT NULL REFERENCES issues(id),
  label      TEXT NOT NULL,
  author     TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY(issue_id, label),
  CHECK (length(label) BETWEEN 1 AND 64),
  CHECK (label NOT GLOB '*[^a-z0-9._:-]*'),
  CHECK (length(trim(author)) > 0)
);
CREATE INDEX idx_issue_labels_label ON issue_labels(label);

CREATE TABLE events (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id          INTEGER NOT NULL REFERENCES repos(id),
  repo_identity    TEXT NOT NULL,
  issue_id         INTEGER REFERENCES issues(id),
  issue_number     INTEGER,
  related_issue_id INTEGER REFERENCES issues(id),
  type             TEXT NOT NULL,
  actor            TEXT NOT NULL,
  payload          TEXT NOT NULL DEFAULT '{}',
  created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(actor)) > 0),
  CHECK (json_valid(payload))
);
CREATE INDEX idx_events_repo    ON events(repo_id, id);
CREATE INDEX idx_events_issue   ON events(issue_id, id) WHERE issue_id IS NOT NULL;
CREATE INDEX idx_events_related ON events(related_issue_id, id) WHERE related_issue_id IS NOT NULL;
CREATE INDEX idx_events_idempotency
  ON events(repo_id, json_extract(payload, '$.idempotency_key'), created_at)
  WHERE type = 'issue.created' AND json_extract(payload, '$.idempotency_key') IS NOT NULL;

CREATE TABLE purge_log (
  id                          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id                     INTEGER NOT NULL,   -- snapshot; no FK so purge audit survives any future repo cleanup
  purged_issue_id             INTEGER NOT NULL,   -- the deleted issues.id; no FK (the row is gone)
  repo_identity               TEXT NOT NULL,      -- snapshot of repos.identity at purge time
  issue_number                INTEGER NOT NULL,
  issue_title                 TEXT NOT NULL,
  issue_author                TEXT NOT NULL,
  comment_count               INTEGER NOT NULL,
  link_count                  INTEGER NOT NULL,
  label_count                 INTEGER NOT NULL,
  event_count                 INTEGER NOT NULL,
  events_deleted_min_id       INTEGER,            -- audit (min events.id deleted; NULL if none)
  events_deleted_max_id       INTEGER,            -- audit (max events.id deleted; NULL if none)
  purge_reset_after_event_id  INTEGER,            -- SSE reset cursor; subscribers with Last-Event-ID < this must reset
  actor                       TEXT NOT NULL,
  reason                      TEXT,
  purged_at                   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (length(trim(actor)) > 0)
);
CREATE INDEX idx_purge_log_reset
  ON purge_log(purge_reset_after_event_id) WHERE purge_reset_after_event_id IS NOT NULL;
CREATE INDEX idx_purge_log_repo_reset
  ON purge_log(repo_id, purge_reset_after_event_id) WHERE purge_reset_after_event_id IS NOT NULL;
CREATE INDEX idx_purge_log_issue  ON purge_log(purged_issue_id);
CREATE INDEX idx_purge_log_lookup ON purge_log(repo_identity, issue_number);

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
INSERT INTO meta(key, value) VALUES ('schema_version', '1');
INSERT INTO meta(key, value) VALUES ('created_by_version', '0.1.0');

-- FTS5 virtual table over issue title+body+comments, kept in sync via triggers.
CREATE VIRTUAL TABLE issues_fts USING fts5(
  title, body, comments,
  content='', tokenize='unicode61 remove_diacritics 2'
);
-- Triggers: insert/update/delete on issues; insert/delete on comments. Triggers
-- maintain a rowid mapping table issues_fts_map(issue_id, fts_rowid). Details
-- in 0001_init.sql; full content here would be redundant.
```

### 3.3 Event types

Persisted in `events.type` as fully qualified strings:

`issue.created`, `issue.updated`, `issue.closed`, `issue.reopened`, `issue.commented`, `issue.linked`, `issue.unlinked`, `issue.labeled`, `issue.unlabeled`, `issue.assigned`, `issue.unassigned`, `issue.soft_deleted`, `issue.restored`.

Plus the synthetic control event `sync.reset_required` (not persisted; emitted to live subscribers on purge and to reconnecting subscribers when their cursor falls inside a deleted-events window). Hook matchers (§8.3) and SSE `event:` fields use the same fully qualified names — there is no namespace shift between persistence, broadcast, and hooks.

The `issue.created` event payload includes initial `labels`, `links`, `owner`, `idempotency_key`, `idempotency_fingerprint` if any of those were specified at create time. No separate `issue.labeled`/`issue.linked`/`issue.assigned` events fire at creation. Subsequent changes do emit their own events.

### 3.4 Issue lifecycle

- `kata create` → row in `issues`, event `issue.created`. `repos.next_issue_number` bumped in same TX (`BEGIN IMMEDIATE`).
- `kata close [--reason …]` → `status='closed'`, `closed_at` set, default `closed_reason='done'`. Event `issue.closed`.
- `kata reopen` → `status='open'`, `closed_at` and `closed_reason` cleared. Event `issue.reopened`.
- `kata edit` → mutates `title`/`body`/`owner`. Event `issue.updated` with `payload.fields = { "title": {"old":"…","new":"…"} }` etc.
- `kata comment` → row in `comments`, event `issue.commented` with `{ "comment_id": N }`.
- `kata link` / `unlink` (plus sugar verbs) → row in `links` or removal, event `issue.linked`/`issue.unlinked` with `related_issue_id` set on the event row.
- `kata label add/rm` → row in `issue_labels`, event `issue.labeled`/`issue.unlabeled`.
- `kata assign/unassign` → mutates `issues.owner`, event `issue.assigned`/`issue.unassigned`.

`updated_at` semantics: "last issue activity." Bumped by every event above.

### 3.5 Destructive ladder

1. `kata close` — closed but visible.
2. `kata delete <id>` — fails with hint *"deletion requires --force; use `kata restore` to undo."*
3. `kata delete <id> --force` — interactive prompt requires typing the issue number; sets `deleted_at`. Event `issue.soft_deleted`.
4. `kata restore <id>` — clears `deleted_at`. Event `issue.restored`.
5. `kata purge <id> --force` — interactive prompt requires typing exactly `PURGE #N`. In one TX:
   1. Capture `repo_id`, `repo_identity`, `purged_issue_id` (the `issues.id` rowid), `issue_number`, `issue_title`, `issue_author`, and counts of dependent rows.
   2. **Capture** `events_deleted_min_id` / `events_deleted_max_id` from `SELECT MIN(id), MAX(id) FROM events WHERE issue_id = N OR related_issue_id = N` *before* deleting anything. Both are NULL if the issue has no events.
   3. Cascade-delete `events WHERE issue_id = N OR related_issue_id = N`, `comments`, `links`, `issue_labels`.
   4. If any events were deleted in step 3: bump `sqlite_sequence.seq` for `events` by one (`UPDATE sqlite_sequence SET seq = seq + 1 WHERE name = 'events'`) and capture the new value as `purge_reset_after_event_id`.
   5. Insert `purge_log` row with `repo_id`, `purged_issue_id`, `repo_identity`, the captured fields and counts, `events_deleted_min_id`/`events_deleted_max_id` from step 2, and `purge_reset_after_event_id` from step 4 (NULL if no events deleted).
   6. Delete the `issues` row.
   7. After commit, the daemon broadcasts a `sync.reset_required` event over SSE with `id:` = `purge_reset_after_event_id` if that value is non-null. Live subscribers (with cursors below the reserved value) drop cache, refetch, and adopt the reserved id as their new cursor.

   The `purge_log` row is the only persisted record. **No `issue.purged` event is persisted.**

Both destructive verbs accept `--confirm "<exact-string>"` for noninteractive use, and require a TTY otherwise (else exit 6 `confirm_required`).

### 3.6 Idempotency

`POST /issues` with `Idempotency-Key: K`:

- Same key + same fingerprint → returns existing issue, `event=null`, `original_event=…`, `reused=true`. Exit 0.
- Same key + different fingerprint → exit 5 `idempotency_mismatch`.
- Same key + matched issue is soft-deleted → exit 5 `idempotency_deleted` with the deleted issue number; hint: `kata restore <id>` or use a fresh key.

**Fingerprint** covers every creation-affecting field so two requests with the same key but materially different inputs cannot silently reuse:

```
fingerprint = sha256(
    "title="   || canonical(title)        || "\n" ||
    "body="    || canonical(body)         || "\n" ||
    "owner="   || canonical(owner ?? "")  || "\n" ||
    "labels="  || join(",", sort(labels)) || "\n" ||
    "links="   || canonical(sort([{type, other_number} for each initial link]))
)
```

`canonical()` is NFC-normalized, trimmed of leading/trailing whitespace, with internal runs of whitespace collapsed to a single space (applied before hashing; the stored title/body remain verbatim). Initial links are sorted lexicographically by `(type, other_number)`. The two-element record uses a fixed JSON form for stability across language clients.

Idempotency key + fingerprint stored in the `issue.created` event's `payload`, indexed by `idx_events_idempotency`. Default lookback window 7 days (configurable).

### 3.7 Look-alike soft-block

Independent of idempotency. Pipeline:

1. FTS5 candidate retrieval over `(title, body, comments)`, top 20 by BM25.
2. App-level normalized similarity: tokenize, lowercase, stop-word, stem; Jaccard on title (weight 0.6) + Jaccard on first 500 chars of body (weight 0.4). Score in `[0, 1]`.
3. Soft-block when any candidate ≥ 0.7 (configurable).

Bypassed by `force_new=true` in the request body. **Idempotency wins** over `force_new` (idempotent reuse never emits a duplicate even if `force_new` is set).

## 4. Daemon HTTP API

### 4.1 Endpoint surface

```
GET    /api/v1/ping                                            # cheap liveness; no DB touch
GET    /api/v1/health                                          # deep health (DB, subscribers, uptime)

POST   /api/v1/repos                                           # body: {root_path, name?}
GET    /api/v1/repos
GET    /api/v1/repos/{repo_id}

POST   /api/v1/repos/{repo_id}/issues                          # body includes actor, force_new
GET    /api/v1/repos/{repo_id}/issues
GET    /api/v1/repos/{repo_id}/issues/{number}
PATCH  /api/v1/repos/{repo_id}/issues/{number}

POST   /api/v1/repos/{repo_id}/issues/{number}/actions/close
POST   /api/v1/repos/{repo_id}/issues/{number}/actions/reopen
POST   /api/v1/repos/{repo_id}/issues/{number}/actions/delete
POST   /api/v1/repos/{repo_id}/issues/{number}/actions/restore
POST   /api/v1/repos/{repo_id}/issues/{number}/actions/purge

POST   /api/v1/repos/{repo_id}/issues/{number}/comments
POST   /api/v1/repos/{repo_id}/issues/{number}/links
DELETE /api/v1/repos/{repo_id}/issues/{number}/links/{link_id}
POST   /api/v1/repos/{repo_id}/issues/{number}/labels
DELETE /api/v1/repos/{repo_id}/issues/{number}/labels/{label}

GET    /api/v1/repos/{repo_id}/ready
GET    /api/v1/repos/{repo_id}/search?q=...
GET    /api/v1/repos/{repo_id}/events?after_id=N&limit=N

GET    /api/v1/issues                                          # cross-repo list
GET    /api/v1/events?after_id=N&limit=N                       # cross-repo poll
GET    /api/v1/events/stream                                   # SSE; ?after_id or Last-Event-ID
```

### 4.2 Auth model

- None. Loopback TCP / Unix socket only. The OS user is the trust boundary.
- Unix socket parent dir `0700`, socket `0600`.
- Reject any non-empty `Origin`. Require `Content-Type: application/json` on mutations. No CORS headers.

### 4.3 Required headers

| Header | Where | Purpose |
|---|---|---|
| `Idempotency-Key` | `POST /issues` | Optional. Daemon-side dedup with fingerprint check. |
| `X-Kata-Confirm` | `POST /actions/delete`, `/actions/purge` | Must equal `"DELETE #N"` / `"PURGE #N"` exactly. |
| `Last-Event-ID` | `GET /events/stream` | Standard SSE resume; `sync.reset_required` if cursor invalidated by purge. |
| `Accept: text/event-stream` | `GET /events/stream` | Required, else 406. |

### 4.4 Request/response shape

Every mutation request body includes `actor` (required, non-empty). Every successful mutation response includes:

```json
{
  "issue":   { "...": "full issue projection" },
  "event":   { "id": 81234, "type": "issue.created", "created_at": "..." },
  "changed": true,
  "reused":  false
}
```

No-op mutations always set `event: null, changed: false`:

- "Already in target state" cases (`label add` already labeled, `link add` already linked, `close` already closed, `reopen` already open) return `{ "issue": {...}, "event": null, "changed": false }`.
- **Idempotent reuse** is the named exception: returns `{ "issue": {...}, "event": null, "original_event": { "id": ..., "type": "issue.created", ... }, "changed": false, "reused": true }`. The `original_event` field is populated *only* in the idempotent-reuse case so clients can correlate to the prior creation.

### 4.5 Error envelope

Every non-2xx response:

```json
{
  "status": 409,
  "error": {
    "code": "duplicate_candidates",
    "message": "3 open issues match \"fix login\"",
    "hint": "comment on an existing issue, or pass force_new=true",
    "data": { "candidates": [...] }
  }
}
```

`error.code` is declared as an OpenAPI enum so generators emit stable Go constants. Huma error handler is wired so every non-2xx response uses this envelope, never Huma's default.

### 4.6 HTTP status → CLI exit code

| HTTP | CLI exit | Stable codes |
|---|---|---|
| 400 | 2 (usage) or 3 (validation) | `usage`, `validation`, `body_source_conflict`, `cursor_conflict` |
| 404 | 4 | `repo_not_found`, `issue_not_found`, `link_not_found`, `label_not_found` |
| 409 | 5 | `duplicate_candidates`, `idempotency_mismatch`, `idempotency_deleted`, `parent_already_set` |
| 412 | 6 | `confirm_required`, `confirm_mismatch` |
| 500 | 1 | `internal` |
| (network) | 7 | (CLI maps `connection refused` → `daemon_unavailable`) |

### 4.7 SSE protocol

Endpoint: `GET /api/v1/events/stream[?repo_id=N][?after_id=N]`. `Last-Event-ID` header alternative; both → 400 `cursor_conflict`.

Frame:

```
id: 81235
event: issue.commented
data: {"event_id":81235,"type":"issue.commented","repo_id":3,"repo_identity":"github.com/wesm/kata","issue_number":42,"actor":"claude-4.7","payload":{"comment_id":104},"created_at":"2026-04-29T14:22:11.482Z"}
```

- `event:` field = `events.type` (e.g. `issue.commented`) or `sync.reset_required`. Same fully qualified strings used in `events.type` and hook matchers.
- `data:` is single-line JSON.
- Daemon broadcasts after DB commit; in-memory broadcaster fans out.
- On reconnect: compute `MAX(purge_reset_after_event_id) FROM purge_log WHERE purge_reset_after_event_id > <cursor>` (with `AND repo_id = ?` for `?repo_id=N` streams). If non-null → send single `sync.reset_required` (with `id:` = that max value, `data.new_baseline` = same), close stream. Otherwise: replay `events WHERE id > ?` ordered by id (bounded ~10k rows; continue streaming live afterward).
- Heartbeats: `: keepalive\n\n` every 25s.

`sync.reset_required` event IDs are reserved synthetic cursors. They are produced by bumping `sqlite_sequence.seq` for `events` (without inserting a row) at purge time, so the value is strictly greater than every real `events.id` that existed at the moment of purge, and no real event will ever be assigned that id (the next real insert continues from `reserved + 1`).

### 4.8 Cross-repo list

`GET /api/v1/issues` query params: `repo_id` (repeatable), `status`, `owner`, `author`, `label` (repeatable), `q`, `updated_since`, `limit`, `offset`. Default sort `updated_at DESC`. Cursor pagination via `?after_updated=<ts>&after_id=<id>`.

### 4.9 Search response

```json
{
  "query": "fix login",
  "results": [
    { "issue": { "..." : "..." }, "score": 0.83, "matched_in": ["title","body"] }
  ]
}
```

Scoring server-side; same numbers everywhere (CLI, TUI).

### 4.10 Event polling (non-SSE)

`GET /api/v1/events?after_id=N&limit=L` and `GET /api/v1/repos/{repo_id}/events?after_id=N&limit=L` are the polling counterparts to the SSE stream. They use the **same purge-invalidation rule** so an agent that polls cannot silently miss events.

For each request:

1. Compute `reset_to = MAX(purge_reset_after_event_id) FROM purge_log WHERE purge_reset_after_event_id > <after_id>`. The per-repo endpoint adds `AND repo_id = ?` (using the snapshotted `purge_log.repo_id`); the cross-repo endpoint omits the predicate.
2. **If `reset_to` is non-null** the cursor is invalidated. Return HTTP 200 with body:
   ```json
   {
     "reset_required": true,
     "new_baseline": <reset_to>,
     "events": [],
     "next_after_id": <reset_to>
   }
   ```
   The `events` array is empty; the client refetches state and resumes polling with `after_id = new_baseline`. (HTTP 200 keeps the response interpretable as a normal envelope; the `reset_required` flag is the trigger for the client.)
3. **Otherwise** return:
   ```json
   {
     "reset_required": false,
     "events": [ ...up to L envelopes, ordered by id ASC... ],
     "next_after_id": <max events.id in the response, or after_id if empty>
   }
   ```

The CLI text path treats `reset_required: true` the same way the TUI does on `sync.reset_required` over SSE: drop cached state, refetch, resume with the new cursor.

## 5. Agent Ergonomics & Skills

### 5.1 CLI conventions for agents

- `--json` is supported on **every** command (writes too). Stable schema, versioned (`{"kata_api_version":1, ...}`).
- Stable exit codes (table in §4.6); exposed as Go consts; man page entry `kata help exit-codes`.
- Body sources mutually exclusive: `--body`, `--body-file`, `--body-stdin`. Passing more than one → exit 2.
- With `--json`: missing body for `create` → empty body; for `comment` → exit 3 with hint. **Never opens `$EDITOR` in machine mode**, even if stdin happens to be a TTY.
- `--quiet, -q` suppresses non-essential output; compatible with `--json`. For `create`, `--quiet` without `--json` prints just the issue number.
- `kata events --tail --json` is **NDJSON** (one envelope per line).
- `kata events --after-id N --json` is the primary agent polling primitive; returns `next_after_id` for the next call. Timestamps (`--since`) are the human path. If the polling response sets `reset_required: true`, the agent must drop any cached state and resume polling from `next_after_id` (= the new baseline). See §4.10.

### 5.2 Identity

Precedence: `--as <name>` > `KATA_AUTHOR` > `git config user.name` > `anonymous`. `kata whoami` echoes the resolved identity and source (`flag`/`env`/`git`/`fallback`).

Skills tell agents: set `KATA_AUTHOR` once at session start; use a name that includes the model and a recognizable suffix (e.g. `claude-4.7-wesm-laptop`). Don't pass `--as` per-call unless acting as someone else.

### 5.3 Search before create

Two mechanisms working in concert:

1. **`kata search <query>`** — FTS5 + similarity score. Skills tell agents: always search before `create`.
2. **`kata create --idempotency-key <key>`** — if matched in the configured window, returns the existing issue with exit 0; with a different fingerprint, exit 5.

Look-alike soft-block at create time (≥ 0.7 similarity) errors with a candidate list; bypass with `--force-new`.

### 5.4 Error messages tell the agent what to do

Text mode:

```
$ kata create "fix login bug"
error: 3 open issues match "fix login" in this repo
  #12  fix login bug on Safari       (open, 2d ago, claude-3.7)
  #18  login form crashes on submit  (open, 4h ago, codex)
  #22  login bug regression          (open, 1h ago, claude-4.7)
hint: comment on an existing issue, or pass --force-new to create anyway
```

JSON mode:

```json
{
  "error": {
    "code": "duplicate_candidates",
    "message": "3 open issues match \"fix login\"",
    "hint": "comment on an existing issue, or pass --force-new",
    "next_commands": ["kata show 12 --json", "kata show 18 --json", "kata show 22 --json"],
    "data": { "candidates": [{"number":12,"title":"...","score":0.81}, ...] }
  }
}
```

### 5.5 Confirmation for destructive ops

Both `delete --force` and `purge --force` accept `--confirm "<exact-string>"` for noninteractive use. Agents in scripts use the flag; humans get the interactive prompt. Without TTY and without `--confirm` → exit 6 `confirm_required`. Mismatched `--confirm` → exit 6 `confirm_mismatch`.

The skill rule for agents: never invoke `kata delete` or `kata purge` unless the user explicitly named the issue number and instructed you to. Always include `--confirm` with the exact issue number.

### 5.6 Skill packaging

Directory-style. Embedded with `//go:embed`. Two targets v1:

```
~/.claude/skills/kata-using/SKILL.md      # honors $CLAUDE_CONFIG_DIR
~/.claude/skills/kata-triage/SKILL.md
~/.claude/skills/kata-decompose/SKILL.md

$CODEX_HOME/skills/kata-using/SKILL.md    # default ~/.codex
…
```

Each skill is a directory with at least `SKILL.md`; may include `references/`. Frontmatter is YAML with `name` and `description` — the description is the trigger phrase, written as roborev does it ("Use when …; do not use when …").

### 5.7 Skill set (3 skills, v1)

| Skill | Trigger | Content |
|---|---|---|
| `kata-using` | Foundation skill; install and prefer invoking when working in a kata-tracked repo | Identity, JSON-first, search-before-create, link semantics, link/comment/close hygiene. Each skill has a "When NOT to invoke" section. |
| `kata-triage` | When user says "triage", "go through open issues", or similar — not for asking about a single issue | Walk `kata list --status open --json`, decide each: keep / close / link / comment. |
| `kata-decompose` | Large feature request — not for trivial requests | Parent issue + child issues with `parent` links, `blocks` chains for sequencing. |

Skills are 100–200 lines. Numbered steps, `--json` everywhere, explicit error handling ("if X fails, report and continue").

`kata-using` does **not** teach a `(kata-#N)` commit-message convention. Issue tracker is not git-history-derived.

### 5.8 Verification commands

Agent skill activation isn't fully deterministic. Two backup commands:

- `kata skills doctor` — per-agent: installed / outdated / missing / skipped (config dir absent). Byte-compare installed content vs. embedded.
- `kata skills list` — names + descriptions; useful for verifying triggers loaded.
- `kata agent-instructions` — prints the canonical "what an agent should know about kata" text (same content shipped in `kata-using` SKILL.md). Deterministic fallback for when skills didn't load.

## 6. CLI Surface

Universal flags on every command: `--json`, `--quiet`/`-q`, `--as <name>`, `--repo <path>` (or `--repo-id <id>` when ID is already known). `--all-repos` for cross-repo reads where applicable.

### 6.1 Command map

| Group | Command | Notes |
|---|---|---|
| Lifecycle | `kata create <title> [--body* / --idempotency-key K / --force-new / --label L / --owner O / --parent N / --blocks N]` | `--label` repeated only (no CSV). Initial labels/links/owner go into the `issue.created` event payload. |
| | `kata show <number> [--include-events] [--include-deleted]` | Default: issue + comments + links + labels. |
| | `kata list [--status / --label / --owner / --author / --repo / --all-repos / --updated-since / --limit / --search]` | Default: this repo, `status=open`, `updated_at DESC`, limit 50. |
| | `kata edit <number> [--title / --body* / --owner]` | At least one field; else exit 3. |
| | `kata close <number> [--reason done\|wontfix\|duplicate]` | Default reason `done`. |
| | `kata reopen <number>` | |
| | `kata comment <number> [--body*]` | Body required (no implicit empty comment). |
| | `kata delete <number> --force [--confirm "DELETE #N"]` | Soft delete; reversible. |
| | `kata restore <number>` | |
| | `kata purge <number> --force [--confirm "PURGE #N"]` | Irreversible. |
| Relationships | `kata parent <child> <parent> [--replace]` | One-parent constraint; `--replace` swaps. |
| | `kata unparent <child>` | |
| | `kata block <blocker> <blocked>` / `kata unblock <blocker> <blocked>` | |
| | `kata relate <a> <b>` / `kata unrelate <a> <b>` | Canonical-ordered. |
| | `kata link <from> <type> <to>` / `kata unlink <from> <type> <to>` | Generic escape hatch. |
| Labels | `kata label add <number> <label>` / `kata label rm <number> <label>` | Charset `[a-z0-9._:-]{1,64}`. |
| | `kata labels [--repo / --all-repos]` | Counts. |
| Ownership | `kata assign <number> <owner>` / `kata unassign <number>` | |
| Discovery | `kata ready [--repo / --all-repos / --label / --owner]` | Open issues with no open `blocks` predecessor. **Primary "what's next" command.** |
| | `kata search <query> [--repo / --all-repos / --status / --limit]` | FTS5 + similarity. |
| | `kata events [--after-id / --since / --tail / --repo / --all-repos / --type]` | Default: this repo, 100 most recent. `--tail` → NDJSON over SSE. |
| Diagnostics | `kata doctor [--repo / --all-repos]` | Read-only; system health only (see §6.4). |
| | `kata whoami` | `{actor, source}`. |
| | `kata health` | `/api/v1/health`. |
| Repos | `kata init [--name <NAME>]` | Explicit repo registration. Equivalent to the auto-registration that any kata command performs in cwd, but intentional. Optional `--name` overrides the auto-derived name. No-op if already registered. |
| | `kata repos [list\|show]` | List registered repos / show one. Forgetting a repo is out of scope (§10). |
| Skills | `kata skills install [--target claude\|codex\|all]` | Idempotent; honors `$CLAUDE_CONFIG_DIR`/`$CODEX_HOME`. |
| | `kata skills doctor` / `kata skills list` | |
| | `kata agent-instructions` | Canonical agent doc. |
| Daemon | `kata daemon [start\|stop\|status\|logs\|reload]` | `logs --hooks` for hook runs. Auto-start by other commands. |
| TUI | `kata tui [--repo / --all-repos / --include-deleted]` | (see §7) |
| Config | `kata config [get\|set\|list\|path]` | TOML. |

### 6.2 Repo discovery

CLI walks up from `cwd` to find `.git`, sends `POST /api/v1/repos {root_path}` to upsert, caches the returned `repo_id` per process. If `cwd` is not in a git repo: most commands error with exit 4 `repo_not_found` and a hint to `cd` into a repo or use `--all-repos`. Outside-repo TUI behavior in §7.

### 6.3 Detailed: `kata create`

Most-hit command. JSON output is the full issue projection plus event metadata:

```json
{
  "issue":   { "number": 42, "...": "..." },
  "event":   { "id": 81237, "type": "issue.created" },
  "changed": true,
  "reused":  false
}
```

Daemon flow inside one TX: resolve repo → idempotency check → look-alike check → insert issue + initial labels/links → bump `repos.next_issue_number` → append `issue.created` event with payload (idempotency, fingerprint, initial labels/links/owner) → commit → broadcast.

### 6.4 Detailed: `kata doctor` (system-only)

Read-only. JSON output is an array of findings; text output groups by severity.

| Check | Severity | Notes |
|---|---|---|
| `daemon_unreachable` | error | `/ping` fails. |
| `db_integrity_failed` | error | `PRAGMA integrity_check`. |
| `schema_drift` | warn | DB `meta.schema_version` vs. binary's expected version. |
| `runtime_files_stale` | warn | `daemon.<pid>.json` for non-existent PIDs. |
| `config_parse_error` | warn | Global or per-repo config TOML failed to load. |
| `purge_log_inconsistency` | warn | For each `purge_log` row, no `events` row should have `issue_id = purged_issue_id` or `related_issue_id = purged_issue_id` (cascade missed rows). Uses the captured rowid rather than `repo_identity + issue_number` so identity changes can't mask stale events. Reports per offending event. |
| `skill_install_drift` | warn | Per agent: missing/outdated skills (byte-compare). |

Doctor never recommends workflow mutations. It may recommend system-repair commands like `kata daemon reload`, `kata skills install`, or "remove stale runtime files at <paths>".

### 6.5 Detailed: `kata ready`

```sql
SELECT i.* FROM issues i
WHERE i.repo_id = ? AND i.status = 'open' AND i.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM links l
    JOIN issues blocker ON blocker.id = l.from_issue_id
    WHERE l.type = 'blocks' AND l.to_issue_id = i.id
      AND blocker.status = 'open' AND blocker.deleted_at IS NULL
  )
ORDER BY i.updated_at DESC;
```

Default sort `updated_at DESC`. No `--by priority-label` flag (label-taxonomy bias); filter via `--label priority:high` instead.

### 6.6 Config

`$KATA_DATA_DIR/config.toml` (or `~/.kata/config.toml`). Per-repo overrides at `<repo_root>/.kata/config.toml`.

```toml
# server_addr = "unix:///custom/path/daemon.sock"
# server_addr = "127.0.0.1:7474"

similarity_threshold = 0.7
idempotency_window   = "7d"

[hooks]
max_concurrency = 4
queue_size      = 1000

# hook entries in hooks.toml (separate file)
```

## 7. TUI

Bubble Tea + Lipgloss. Single command: `kata tui`. API-only; no SQLite bypass; writes go through REST like any other client.

### 7.1 Views

- **List** (default landing): issues, filter/search inline (`/`), status/label/owner toggles.
- **Detail**: header + body + tabs `[ comments | events | links ]`.
- **Help** (`?`): keybindings, current filters, daemon health line.

### 7.2 Scope

- `kata tui` → current repo (resolved from `cwd`).
- `kata tui --all-repos` → cross-repo, repo column shown in list.
- Toggle at runtime with `R`.
- **Outside a git repo**: if any repos are registered, fall back to all-repos automatically. If none registered, show clean empty-state with hint *"Run `kata init` in a repo to get started."*
- `kata tui --include-deleted` shows soft-deleted rows with a `[deleted]` marker. Without the flag, deleted rows are entirely hidden.

### 7.3 Keybindings

**Global**: `?` help · `q` quit · `R` toggle repo scope. (`:` is unbound; reserved for a future command palette — see out-of-scope.)

**List**: `j/k`, arrows, `g/G`, `enter` open · `n` new (inline title prompt → optional `$EDITOR` for body) · `/` search · `s` cycle status filter · `o` filter by owner · `l` filter by label · `c` clear filters · `x` close · `r` reopen.

**Detail**: `j/k` scroll · `tab/shift-tab` cycle tabs · `enter` on an event referring to another issue → jump · `c` new comment (suspend Bubble Tea, run `$EDITOR`, resume, submit) · `e` edit body (same flow) · `x` close · `r` reopen · `p` set parent · `b` add blocker · `L` add link · `+`/`-` add/remove label · `a`/`A` assign/clear owner · `backspace`/`esc` back.

**Destructive ops** (`delete`, `purge`) intentionally **not** keybound. Use the CLI.

### 7.4 SSE behavior — invalidation, not replication

On any incoming event:

- Mark affected row(s) stale and schedule a debounced (~150ms) refetch of the active list query.
- If the detail view is showing the affected issue, refetch that issue.
- On `sync.reset_required`: drop cache, refetch current view, reopen stream with new cursor; show `resynced` toast for ~2s.

Reconnect on disconnect with exponential backoff to 30s; status bar shows `daemon: reconnecting…`.

### 7.5 Color

`KATA_COLOR_MODE` ∈ `auto` (default) / `dark` / `light` / `none`. `NO_COLOR=1` honored. Single theme v1; Lipgloss adaptive.

### 7.6 Performance

Responsive on a few thousand issues. Cold start to first paint and active-view refetch are tested with synthetic fixtures. No microsecond promises in v1.

## 8. Hooks

### 8.1 Goals

Local automation on `issue.*` events. Common use cases: post to chat, file follow-up GitHub issues, ping a notification daemon, log to disk. No remote webhooks v1.

### 8.2 Config

Two locations, merged:

- **Global**: `$KATA_DATA_DIR/hooks.toml`.
- **Per-repo**: `<repo_root>/.kata/hooks.toml` — applies only when the event's repo matches.

```toml
[[hook]]
event   = "issue.created"           # exact event type, "issue.*", or "*"
command = "/usr/local/bin/notify"   # absolute path or PATH-resolvable name
args    = ["--title", "kata"]       # literal strings; no env-var expansion
timeout = "30s"                     # default 30s, max 5m

[hook.env]
EXTRA = "value"                     # user env; keys matching ^KATA_ rejected at load
```

Optional fields: `working_dir` (absolute or repo-relative under repo root; default repo root).

`sync.reset_required` is **not** dispatched to hooks. Hooks see persisted domain events only.

### 8.3 Event matching

Hook `event` strings are fully qualified — same names used in `events.type` and SSE `event:` fields.

- Exact: `event = "issue.commented"`.
- Prefix wildcard: `event = "issue.*"` matches all `issue.<verb>`.
- Catch-all: `event = "*"` matches everything except `sync.reset_required` (which is never dispatched to hooks).

### 8.4 Stdin payload

```json
{
  "kata_hook_version": 1,
  "event_id": 81237,
  "type": "issue.commented",
  "actor": "claude-4.7-wesm-laptop",
  "created_at": "2026-04-29T14:22:11.482Z",
  "repo": {
    "id": 3,
    "identity": "github.com/wesm/kata",
    "root_path": "/Users/wesm/code/kata",
    "name": "kata"
  },
  "issue": {
    "number": 42,
    "title": "fix login crash on Safari",
    "status": "open",
    "labels": ["bug","safari"],
    "owner": "claude-4.7-wesm-laptop",
    "author": "claude-4.7-wesm-laptop",
    "_truncated": false
  },
  "payload": { "comment_id": 104, "comment_body": "..." }
}
```

Hook stdin is JSON, capped at 256KB. Large text fields (`issue.title`, `issue.body`, `payload.comment_body`) are truncated with sibling `_truncated: true` and `_full_size: N` markers. Hooks needing full content fetch via `kata show <number> --json`.

### 8.5 Env vars (safe scalars only)

```
KATA_HOOK_VERSION, KATA_EVENT_ID, KATA_EVENT_TYPE, KATA_ACTOR, KATA_CREATED_AT,
KATA_REPO_ID, KATA_REPO_IDENTITY, KATA_ROOT_PATH, KATA_ISSUE_NUMBER
```

User-defined `env` entries layer first; reserved `KATA_*` set last. Config rejects `env` keys matching `^KATA_` at load with a clear error. Issue title/body/comment text is **never** in env.

### 8.6 Execution

- **After DB commit.** Hooks never block or roll back state changes.
- `exec.Command(cmd, args...)`. **No shell.** **No env-var expansion in `args`.** Hook commands run with the daemon's UID/GID and inherit no kata internals beyond the documented `KATA_*` env vars; this avoids the obvious shell-injection surface but is **not** a sandbox. Operators who need OS-level isolation should write hooks that re-exec under their own sandboxing (containers, `firejail`, `bwrap`, etc.).
- Bounded hook-runner pool: default 4 goroutines (configurable, capped at 16). Bounded queue default 1000.
- Per-hook timeout. SIGTERM → 5s grace → SIGKILL.
- Capture stdout/stderr to `$KATA_DATA_DIR/hooks/<dbhash>/output/<event_id>.<hook_index>.{out,err}`. Total disk usage capped (default 100MB per `<dbhash>`; oldest files pruned first; configurable via `[hooks].output_disk_cap`).
- Hook-run index: `$KATA_DATA_DIR/hooks/<dbhash>/runs.jsonl` (rotated 50MB × 5). One JSON object per run with start/end times, exit code, timeout flag, stdout/stderr paths, truncation flag. Used by `kata daemon logs --hooks`.

`<dbhash>` namespacing prevents two `KATA_DB` daemons from interleaving event-ID-keyed output files or sharing one `runs.jsonl`.

### 8.7 Reload

- `kata daemon reload` (or SIGHUP) reloads global config and clears the per-repo mtime cache.
- Per-repo `<repo_root>/.kata/hooks.toml` loaded on demand when an event for that repo fires. Cached by mtime.
- Validation at load: required fields, `timeout` parses and is in `(0, 5m]`, `working_dir` (if set) parses correctly, `env` keys valid (no `KATA_`).
- **Not validated at load**: command on PATH, command file existence. PATH may differ at fire time; per-repo hooks may be on repo-local paths. Spawn errors get recorded per run.

### 8.8 Failure visibility

- `kata daemon logs --hooks` tails `runs.jsonl` (last 100 by default). `--tail` follows live. `--failed-only`, `--event-type`, `--hook-index` filters.
- Hook failures are **not** SSE events and **not** doctor findings (system health only).
- **Queue full** drop policy: drop newest. Log `hook_queue_full` once per 60s to `daemon.log`. Event handling and SSE broadcast unaffected.
- **`working_dir` missing** at fire time: run recorded as `failed: working_dir_missing`; logged once per 60s in `daemon.log` to avoid spam.

## 9. Filesystem Layout & Project Structure

### 9.1 Filesystem layout

**Data dir** (precedence: `KATA_DATA_DIR` → `~/.kata`; `KATA_DB` overrides DB path independently):

```
$KATA_DATA_DIR/
  config.toml
  hooks.toml
  kata.db                              # default; override KATA_DB
  kata.db-wal
  kata.db-shm
  hooks/<dbhash>/
    runs.jsonl
    output/<event_id>.<hook_index>.{out,err}
  runtime/<dbhash>/
    daemon.<pid>.json
    daemon.log
```

**Runtime dir** (ephemeral; socket only):

```
$XDG_RUNTIME_DIR/kata/<dbhash>/        # fallback: $TMPDIR/kata-<uid>/<dbhash>/
  daemon.sock                          # 0600; parent 0700
```

`<dbhash>` = `sha256(absolute(effective_db_path))[:12]`.

**Per-repo dir** (committed or git-ignored, user's discretion):

```
<repo_root>/.kata/
  config.toml
  hooks.toml
<repo_root>/.kata-id                   # optional identity override
```

### 9.2 Go project layout

```
cmd/kata/
  main.go
  {create,show,list,edit,close,reopen,comment,delete,restore,purge}.go
  {parent,unparent,block,unblock,relate,unrelate,link,unlink}.go
  {label,labels,assign,unassign,ready,search,events}.go
  {whoami,health,init,repos,doctor}.go
  {daemon_cmd,skills,agent_instructions,tui_cmd,config_cmd}.go
  helpers.go                           # CLI glue: body-source reader, JSON formatter, exit codes
  testmain_test.go
  tui/
    tui.go fetch.go handlers.go render_list.go render_detail.go filter.go theme.go

internal/
  api/
    routes.go            # huma route registration (source of truth)
    types.go             # request/response DTOs
    errors.go            # error envelope + Huma wiring
    openapi.yaml         # generated artifact (committed for diff)
  apiclient/generated/
    client.gen.go        # generated from openapi.yaml
  daemon/
    runtime.go           # daemon.<pid>.json, ListAllRuntimes, cleanup
    endpoint.go          # DaemonEndpoint (Unix vs TCP)
    namespace.go         # dbhash, runtime dir resolution
    server.go            # http.Server lifecycle, signal handling
    broadcaster.go       # SSE fan-out, Last-Event-ID resume, sync.reset_required
    hooks/
      runner.go          # worker pool, queue, exec
      config.go          # TOML load, validation, per-repo mtime cache
      log.go             # JSONL index + rotated stdout/stderr capture
      payload.go         # build stdin JSON, truncation
    health.go            # /health, /ping
    handlers_{repos,issues,events,actions,labels,links,comments,search,ready}.go
  db/
    db.go                # Open (pragmas, FK enforcement), migrations runner
    migrations/0001_init.sql
    queries.go           # all CRUD, single-writer aware
    types.go             # Issue, Comment, Link, Label, Event, PurgeLog, Repo
    fts.go               # FTS5 virtual table + sync triggers
  config/
    config.go            # global TOML + per-repo merge + key registry
    repo_identity.go     # ResolveRepoIdentity, normalization, .kata-id, credential strip
    paths.go             # KATA_DATA_DIR, KATA_DB, runtime dir, repo discovery (used by CLI too)
  similarity/
    similarity.go        # tokenize, normalize, jaccard, weighted score
  skills/
    skills.go            # install, status, doctor; CLAUDE_CONFIG_DIR / CODEX_HOME
    claude/{kata-using,kata-triage,kata-decompose}/SKILL.md
    codex/{kata-using,kata-triage,kata-decompose}/SKILL.md
  testenv/testenv.go     # temp data dir, fresh daemon, generated client wired
  testutil/testutil.go   # git temp repos, fixtures
```

**Identity resolution lives in `internal/config/repo_identity.go`**, called by the daemon. CLI does not own identity resolution; it sends `root_path`.

### 9.3 Build, test, lint

```
make build            # go build ./...
make install          # GOBIN=~/.local/bin go install ./cmd/kata
make test             # go test -shuffle=on ./...
make test-short       # go test -short -shuffle=on ./...
make lint             # golangci-lint run --config .golangci.yml
make vet              # go vet ./...
make api-generate     # huma routes → openapi.yaml → apiclient
```

API generation has one source of truth: Huma route/type registrations. `make api-generate` runs an internal generator that exports the spec to `internal/api/openapi.yaml` (committed for review/diff), then generates `internal/apiclient/generated/` from that file.

Conventions: testify preferred (`require` for setup, `assert` for non-blocking); table-driven tests; `t.TempDir()`; `-shuffle=on`; no `-count=1`; no `-v` by default. `modernc.org/sqlite`; no CGO. UTC timestamps; RFC3339 at API boundaries. No emojis in code or output.

Pre-commit via `prek` (matches roborev/middleman): runs `make lint` with `always_run`. CI uses `make lint-ci` non-mutating.

## 10. Out-of-Scope (Consolidated)

**Storage / DB:**
- Backwards-compat `daemon.json` alias.
- Direct SQLite reads from CLI (separate `OpenReadOnly` later).
- Importers (GitHub Issues, beads, JIRA).
- PostgreSQL mirror / multi-machine sync.
- Repo-row deletion / cleanup.

**API:**
- Multi-user auth, agent tokens.
- GraphQL/RPC.
- Bulk endpoints.
- Per-issue SSE subscriptions (clients filter).
- Remote webhooks.
- Diagnostic admin path for explicit-identity repo registration.

**CLI:**
- `kata sync`, `kata import`.
- `kata link-commits` and the `(kata-#N)` commit-message convention.
- `kata report` / `kata hygiene` (workflow checks).
- Interactive shell mode.
- Repo aliases / "previous repo" sugar.
- `--watch` on individual list/show commands (use `kata events --tail`).
- `kata repos forget` (no command for it in v1; deferred to v2).

**TUI:**
- Command palette (`:` reserved unbound).
- Multi-pane / split layout.
- Parent/child tree visualization.
- Bulk operations / multi-select.
- Markdown rendering.
- OS notifications (delegate to hooks).

**Hooks:**
- Remote webhooks.
- Retries.
- Conditional firing (`when = …`).
- Ordering guarantees.
- SSE-driven external triggers.
- Hot-reload of per-repo configs (use `kata daemon reload`).

**Skills:**
- Auto-installation on first daemon start.
- Skill marketplace / external registries.
- Per-project skill overrides.

**Doctor:**
- Workflow lints (stale opens, dangling owners, commit-ref orphans).
- Auto-applied fixes — doctor only *recommends* commands. Recommendations are limited to system-repair commands like `kata daemon reload`, `kata skills install`, or "remove stale runtime files at <paths>"; never workflow mutations.

**Issue model:**
- `in_progress` status (use labels or `owner`).
- Draft / needs-review states.
- Issue templates.
- Severity / priority as first-class fields (use labels).
- Threaded comment replies.
- Reactions / emoji.
- File attachments.

## 11. Open Questions / Tunables

- **Default similarity threshold 0.7** — empirical default. Calibrate against a fixture set of ~50 positive/negative issue-pair examples during initial implementation.
- **Default idempotency window 7 days** — long enough to catch flake-retry duplicates; short enough that intentional re-creates aren't blocked.
- **`--label` flag** is repeated only (`--label bug --label safari`); no CSV.
- **`--json` create output** is the full issue projection. `--quiet` (without `--json`) prints just the number for scripting.
- **Skill set v1**: `kata-using`, `kata-triage`, `kata-decompose`. A fourth "land/finish" skill was considered and dropped as imported process; add it later only if real usage shows it earns a skill.

---

End of v1 spec.
