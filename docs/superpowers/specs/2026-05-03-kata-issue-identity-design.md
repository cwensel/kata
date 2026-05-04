# Issue Identity — Stable UIDs Before Federation

> **Status:** design / spec. Companion to `docs/superpowers/specs/2026-04-29-kata-design.md` (master design), `docs/superpowers/specs/2026-04-29-kata-shared-server-mode.md` (shared-mode roadmap), and `docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md` (JSONL roundtrip — this spec depends on it for the v1→v2 cutover). This spec lands **before** any other shared-mode work in `kata #3` (Phase 2 readiness pass) — see §11 ordering.

## 1. Locked decisions

These four decisions are settled here and are not re-litigated by the implementation plan.

1. **ID format — ULID, Crockford base32, column name `uid`.**
   - 128-bit ULID (48-bit timestamp + 80-bit randomness, monotonic-by-default within a millisecond). Stored and sent on the wire as the canonical 26-character Crockford base32 representation, e.g. `01JTYJ73R8K7XGZ5T6FJ8VYC2N`.
   - Column name is `uid` (or `<thing>_uid` in foreign-key columns), not `uuid`. ULIDs are not UUIDs and the name should not lie about that.
   - Rationale over UUIDv7: 26 chars vs 36 (with hyphens) is meaningfully shorter on terminal/CLI surfaces; Crockford base32 is unambiguous (no I/L/O/0/1) which matters when a user types or copies one. The roborev symmetry argument is weak — roborev uses Postgres-native `UUID` columns and `crypto/rand` UUIDv4, which is a different format and a different ordering story; kata never round-trips ids through roborev.
   - Generation: pure-Go using `github.com/oklog/ulid/v2` (or equivalent — see §9 open questions). Crypto-strong randomness, monotonic entropy source within a process so within-millisecond bursts produce strictly increasing ULIDs.

2. **Link authority — stored UID columns, integer FKs are a join cache.**
   - `links.from_issue_uid` and `links.to_issue_uid` are `TEXT NOT NULL` columns. They are the **authoritative** identifier of each endpoint.
   - `links.from_issue_id` and `links.to_issue_id` stay as `INTEGER NOT NULL REFERENCES issues(id)` and are kept in sync with the UIDs by the daemon on every insert. They exist as a **performance cache** so `JOIN issues ON issues.id = links.from_issue_id` stays cheap on the hot list-render and detail-render paths.
   - The integer FKs are not removed in v1 of this change. Removing them would force every list/render query to join on TEXT UID columns instead of integer ids, which is a measurable slowdown we do not need to take right now.
   - Future federation (a kata that receives a link to an issue UID it does not yet have a local row for) is contemplated by this storage shape: the daemon could insert the link with `from_issue_id`/`to_issue_id` NULL and a NOT NULL UID, but **v1 of this spec does not enable that** — see §3 scope. The shape is forward-compatible, not forward-active.

3. **Same-project links — enforcement preserved unchanged.**
   - The existing `trg_links_same_project_insert` / `_update` triggers stay. They continue to enforce that `links.project_id` matches both endpoints' `issues.project_id`. The check uses the integer FKs (which still point to local rows in v1) — no UID-based cross-project semantics are added here.
   - This is a conscious decision to **not** mix two distinct concerns. Issue-identity stability (this spec) is a foundation. Cross-project linking (a product question about whether kata wants graph edges between projects) is a separate decision and is not part of this change. Removing the same-project rule without thinking through cycle handling, search semantics, and access-control implications would be premature.

4. **Project identity — `projects.uid` lands now; `instance_uid` deferred.**
   - Add `projects.uid` (`TEXT NOT NULL UNIQUE`, ULID) as part of this change. Projects are first-class identity carriers in the federated story, and adding the column now costs nothing (one row per workspace, ~26 bytes) but means future federation/server-mode work does not need a second cutover to retrofit it.
   - Do **not** add `instance_uid` in v1. Kata has no notion of an "instance" yet (no kata-to-kata sync, no replication topology, no instance-issued credential scopes). Adding the column without a use case is speculative — the kind of premature plumbing the project's CLAUDE.md explicitly cautions against. Revisit if/when shared-mode evolves into multi-instance federation.

The rest of this document expands on what these decisions imply for the schema, the wire, the cutover, and the test plan.

## 2. Goal

Give every project and every issue a 128-bit, time-ordered, opaque, URL-safe stable identifier that is generated **once** at row insert time and never changes — not under rename, not under renumber, not under cross-instance import, not under shared-mode deployment.

The display label `#42` is a per-project ordinal, owned by humans, and is allowed to be ambiguous in cross-project / cross-instance contexts. The UID is the authoritative reference and is unique across all projects on this kata instance and (with overwhelming probability) across all kata instances anywhere.

The downstream goal is that links, events, hooks, purges, and any future federation/sync protocol can speak in UIDs and survive every renumbering, rename, or cross-instance import that the human-facing label cannot.

## 3. Scope

**In scope**
- Schema declaration: `projects.uid` and `issues.uid` are `TEXT NOT NULL UNIQUE` columns in the canonical `0001_init.sql`. `links.from_issue_uid` and `links.to_issue_uid` are `TEXT NOT NULL` endpoint columns with lookup indexes; multiple links may share the same endpoint. `events.issue_uid`, `events.related_issue_uid`, `purge_log.issue_uid`, `purge_log.project_uid` are nullable (mirroring their integer FKs).
- Schema-version bump from `1` to `2` (master spec §3.1 `meta` table).
- ULID generation library (`internal/uid`) and a deterministic test-clock seam.
- Wire-shape updates: `Issue`, `Project`, `Link`, `EventEnvelope`, and `PurgeLogEntry` JSON shapes gain `uid` (or `*_uid`) fields. Existing fields (`number`, `id`, `from_issue_id`, etc.) stay.
- DB-side triggers that keep row UIDs immutable and assert `links.from_issue_uid`/`to_issue_uid` resolve to the same `issues` rows the integer FKs point at — so UID/FK drift is impossible without disabling foreign keys.
- Daemon insert paths (`internal/db/queries.go` etc.) extended to emit a fresh ULID and write both UID and integer FK columns on every insert.
- CLI lookups: every command that accepts `<number>` also accepts a UID (or its leftmost-N-char prefix, with ambiguity → error). User-facing display stays `#42`; the UID surfaces in `--json` output and on the detail view's metadata footer.
- New endpoint `GET /api/v1/issues/{uid}` for cross-project UID lookup.
- v1→v2 fill rules added to the JSONL importer (per JSONL spec §6.1) — UIDs generated deterministically from `created_at` for legacy records.
- Documentation update to the master spec (§3.1, §4 wire shapes) noting that `uid` is the authoritative identifier and `#N` is a display label.

**Out of scope (deferred)**
- `instance_uid` or any other instance-level identifier (per §1.4).
- Removing `links.from_issue_id` / `links.to_issue_id` integer FKs (per §1.2).
- Cross-project linking semantics or any change to the `same_project` triggers (per §1.3).
- A federation/sync protocol that round-trips UIDs between kata instances. This spec lands the identity layer; protocol work is its own design.
- Receiving a link whose endpoint UID does not exist locally yet (the schema permits the future shape but v1 still requires the integer FK to be set).
- ULID-keyed comments, labels, or other attached records. Comments and labels are owned by their issue; the issue's UID is sufficient.
- In-place migration runners, `0002_uids.sql` files, Go-coded backfill, table-rebuild dances, FK toggling. The cutover is owned by the JSONL roundtrip spec and is **not re-implemented here**. This spec only contributes the v1→v2 fill rules to the importer.

## 4. Architecture summary

ULIDs are generated by the daemon at insert time, in a Go helper that wraps `oklog/ulid/v2`. The helper exposes:

```go
package uid

// New returns a fresh ULID-formatted string. Safe for concurrent use; uses an
// internal monotonic entropy source so within-millisecond bursts produce
// strictly increasing UIDs.
func New() string

// FromTime returns a ULID-formatted string with the timestamp portion set to
// t and a *random* 80-bit entropy. Calls with the same t produce ULIDs that
// share their leading 10 chars but differ in the trailing 16. NOT suitable
// for the JSONL fill rule — see FromStableSeed.
func FromTime(t time.Time) string

// FromStableSeed returns a ULID-formatted string with the timestamp portion
// set to t and the 80-bit entropy derived deterministically from seed (via
// SHA-256(seed)[:10]). Same seed + same t → same ULID, byte-equivalent across
// processes and machines. Used by the JSONL importer's v1→v2 fill rule so a
// re-run of the cutover produces identical UIDs.
//
// Callers compose seed from row-stable identifiers, e.g. for issues:
//   seed = []byte(fmt.Sprintf("issue:%d:%d", project_id, number))
// Stability of the seed across kata versions is the caller's contract — once
// a fill rule has run, changing its seed shape silently re-issues UIDs.
func FromStableSeed(seed []byte, t time.Time) string

// Valid reports whether s is a syntactically valid ULID (26-char Crockford
// base32, leading bits clear). Cheap; no DB lookup.
func Valid(s string) bool

// ValidPrefix reports whether s is a valid ULID prefix (1- to 26-char
// Crockford base32). Used by CLI prefix lookup before issuing the SQL query.
func ValidPrefix(s string) bool
```

Prefix lookup lives in `internal/db` (not `internal/uid`) because it issues SQL. There is no generic `PrefixMatch(table string, ...)` helper — caller-supplied table names would require dynamic SQL or a fragile string whitelist. Two typed methods exist, one per UID-bearing table:

```go
// in internal/db
func (d *DB) IssueUIDPrefixMatch(ctx context.Context, prefix string, limit int) ([]string, error)
func (d *DB) ProjectUIDPrefixMatch(ctx context.Context, prefix string, limit int) ([]string, error)
```

Both validate `prefix` against `uid.ValidPrefix` before issuing the query, and both use parameterized `LIKE ? || '%'` against the `idx_issues_uid` / `idx_projects_uid` UNIQUE indexes. Adding a third UID-bearing lookup later means adding another typed method, not extending a generic helper.

The daemon writes both `uid` and `id` (and, for links, both `*_uid` and `*_id` columns) on every insert. Reads continue to use integer FKs for joins; UIDs are returned in the wire envelope. DB triggers (§5 below) make `projects.uid` / `issues.uid` immutable and enforce that the UID and integer FK on every `links` row resolve to the same issue, so the daemon's "kept in sync on insert" promise is also a hard storage invariant — drift is impossible without disabling foreign keys.

CLI/TUI accept either a `#N` ordinal or a UID (full or prefix) wherever an issue argument is taken. Internally, both are resolved to an integer issue id for the daemon's local query path. The wire envelope always carries the UID so a future remote client can resolve UIDs without round-tripping through ordinals.

Soft-delete and purge handle UIDs differently:

- **Soft-delete** (`issues.deleted_at IS NOT NULL`) keeps the `uid` column intact. The row is hidden from default queries but the UID is preserved for `--include-deleted` lookups, the `events.issue_uid` references that point at it, and possible undelete. Soft-delete also leaves matching `events` rows in place per master spec §3.5, so `events.issue_uid` references remain resolvable.
- **Purge** (master spec §3.5) deletes the `issues` row **and** all matching `events` rows in the same transaction. The only persisted reference to a purged issue's UID is the `purge_log.issue_uid` snapshot. Subscribers whose cursor is below the purge's `purge_reset_after_event_id` receive a `sync.reset_required` frame (Plan 4 §5) and resync from current state — they do **not** replay the deleted events. The lifecycle is: live row → (optional soft-delete) → purge removes row + events → `purge_log.issue_uid` is the only audit trail.

## 5. Schema declaration

`internal/db/migrations/0001_init.sql` is the single canonical schema. The issue-identity changes are **edits to that file**, not a separate `0002` migration. The kata binary's `currentSchemaVersion` constant bumps from `1` to `2`; the JSONL roundtrip's first-boot cutover (per JSONL spec §6.3) is what rolls existing v1 databases forward.

This depends on the migration-runner change described in JSONL spec §6.0: the runner records `currentSchemaVersion` (a Go constant) in `meta.schema_version` after running migrations, instead of the filename-derived version. Without that change, editing `0001_init.sql` to declare v2 would still cause fresh inits to record `meta.schema_version = '1'` because the filename hasn't changed (per `internal/db/db.go:84-89`). The runner change must land before this spec's edits to `0001_init.sql`, but it is small and lives entirely in the JSONL roundtrip work.

The schema edits:

```sql
-- projects: uid added as NOT NULL UNIQUE
CREATE TABLE projects (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  uid               TEXT NOT NULL UNIQUE,
  identity          TEXT UNIQUE NOT NULL,
  name              TEXT NOT NULL,
  created_at        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  next_issue_number INTEGER NOT NULL DEFAULT 1,
  CHECK (length(trim(identity)) > 0),
  CHECK (length(trim(name))     > 0),
  CHECK (length(uid) = 26)
);

-- issues: uid added as NOT NULL UNIQUE
CREATE TABLE issues (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  uid           TEXT NOT NULL UNIQUE,
  project_id    INTEGER NOT NULL REFERENCES projects(id),
  number        INTEGER NOT NULL,
  -- ... (other columns unchanged)
  CHECK (length(uid) = 26)
);

-- links: both UID columns NOT NULL
CREATE TABLE links (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     INTEGER NOT NULL REFERENCES projects(id),
  from_issue_id  INTEGER NOT NULL REFERENCES issues(id),
  to_issue_id    INTEGER NOT NULL REFERENCES issues(id),
  from_issue_uid TEXT NOT NULL,
  to_issue_uid   TEXT NOT NULL,
  type           TEXT NOT NULL CHECK(type IN ('parent','blocks','related')),
  author         TEXT NOT NULL,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(from_issue_id, to_issue_id, type),
  CHECK (from_issue_id <> to_issue_id),
  CHECK (length(trim(author)) > 0),
  CHECK (type <> 'related' OR from_issue_id < to_issue_id),
  CHECK (length(from_issue_uid) = 26),
  CHECK (length(to_issue_uid) = 26)
);

-- events: nullable UID columns mirror nullable issue_id / related_issue_id
CREATE TABLE events (
  -- ... (other columns unchanged)
  issue_uid         TEXT,
  related_issue_uid TEXT,
  -- ...
);

-- purge_log: nullable UID columns (for the rare case where the issue is gone
-- before the purge writes its row — but in practice purge writes the log row
-- with project_uid + issue_uid both populated, since it reads them from the
-- live issues/projects rows before the delete)
CREATE TABLE purge_log (
  -- ... (other columns unchanged)
  issue_uid         TEXT,
  project_uid       TEXT,
  -- ...
);
```

New indexes:

```sql
CREATE INDEX idx_links_from_uid ON links(from_issue_uid);
CREATE INDEX idx_links_to_uid   ON links(to_issue_uid);
CREATE INDEX idx_events_issue_uid         ON events(issue_uid)         WHERE issue_uid IS NOT NULL;
CREATE INDEX idx_events_related_issue_uid ON events(related_issue_uid) WHERE related_issue_uid IS NOT NULL;
CREATE INDEX idx_purge_log_issue_uid ON purge_log(issue_uid) WHERE issue_uid IS NOT NULL;
```

(The `UNIQUE` constraints on `projects.uid` and `issues.uid` produce automatic `sqlite_autoindex_*` indexes; no separate `CREATE INDEX` is needed for them.)

New triggers (storage-level UID/FK consistency, beyond the daemon's app-level write path):

```sql
CREATE TRIGGER trg_links_uid_consistency_insert
BEFORE INSERT ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'from_issue_uid does not match from_issue_id')
  WHERE NEW.from_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.from_issue_id);
  SELECT RAISE(ABORT, 'to_issue_uid does not match to_issue_id')
  WHERE NEW.to_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.to_issue_id);
END;
CREATE TRIGGER trg_links_uid_consistency_update
BEFORE UPDATE ON links
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'from_issue_uid does not match from_issue_id')
  WHERE NEW.from_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.from_issue_id);
  SELECT RAISE(ABORT, 'to_issue_uid does not match to_issue_id')
  WHERE NEW.to_issue_uid <> (SELECT uid FROM issues WHERE id = NEW.to_issue_id);
END;

CREATE TRIGGER trg_projects_uid_immutable
BEFORE UPDATE OF uid ON projects
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'projects.uid is immutable')
  WHERE NEW.uid <> OLD.uid;
END;
CREATE TRIGGER trg_issues_uid_immutable
BEFORE UPDATE OF uid ON issues
FOR EACH ROW BEGIN
  SELECT RAISE(ABORT, 'issues.uid is immutable')
  WHERE NEW.uid <> OLD.uid;
END;
```

The existing `trg_links_same_project_insert` / `_update` triggers stay unchanged — they enforce same-project links via the integer FKs (per §1.3), and that semantics is unaltered.

The FTS triggers (`issues_ai_fts`, `issues_au_fts`, `issues_ad_fts`, `comments_ai_fts`, `comments_ad_fts`) are unaffected — they reference columns that don't change shape under this work.

`meta.schema_version` is no longer seeded in `0001_init.sql` (per JSONL spec §6.0 — the migration runner owns that key and writes `currentSchemaVersion` post-init). The only thing this spec changes for version reporting is the `currentSchemaVersion` Go constant in `internal/db`, which bumps from `1` to `2`. Fresh inits using the new binary then record `meta.schema_version = '2'` automatically; existing v1 DBs go through the JSONL cutover (§5.1).

### 5.1 v1 → v2 cutover

Owned by the JSONL roundtrip spec (§6.1 of that doc). Summary:

1. The new binary (with `currentSchemaVersion=2`) sees a v1 SQLite DB at startup.
2. It runs the JSONL exporter against the v1 DB, which omits UID fields from records that didn't have them.
3. It initializes `<path>.import.tmp.db` at the v2 schema (above).
4. It runs the JSONL importer into the temp DB, which generates UIDs deterministically per the v1→v2 fill rule (`uid.FromStableSeed(stable_row_seed, created_at)` per record, then resolves cross-references in dependency order). `FromStableSeed` — not `FromTime` — is what gives the cutover its determinism guarantee: the same source row produces the same UID across reruns because both inputs are stable.
5. Only after import plus validation succeeds, it renames the v1 DB to `<path>.bak.v1.<ts>` and atomically moves the validated temp DB into place (per JSONL spec §6.3).
6. The fresh DB is byte-equivalent to what an in-place migration would have produced — the difference is only in **how**.

This spec provides the fill rule (§5.2 below); the JSONL spec provides everything else.

### 5.2 v1 → v2 fill rules (importer behavior)

When the importer processes a JSONL stream whose `meta.export_version` is `1` (the source DB's schema version, not the binary's — per JSONL spec §1.4), it generates UIDs for any record kind that requires one in v2:

- `projects.uid` ← `uid.FromStableSeed([]byte(fmt.Sprintf("project:%d:%s", project.id, project.identity)), projects.created_at)`. Same row → same UID across reruns because both seed inputs and `created_at` are stable.
- `issues.uid` ← `uid.FromStableSeed([]byte(fmt.Sprintf("issue:%d:%d", issue.project_id, issue.number)), issues.created_at)`. Same.
- `links.from_issue_uid` ← the imported `issues.uid` for `from_issue_id` (resolved via in-memory map built during the issue-import phase, since issues are imported before links per JSONL spec §1.2).
- `links.to_issue_uid` ← same for `to_issue_id`.
- `events.issue_uid` ← the imported `issues.uid` for `events.issue_id` when non-null. NULL when `events.issue_id` is NULL.
- `events.related_issue_uid` ← same for `events.related_issue_id`.
- `purge_log.issue_uid` ← the imported `issues.uid` for `purge_log.purged_issue_id` if the issue is still alive (soft-deleted included). NULL if the issue is already gone (purge cascade ran before this cutover). This is the only legal NULL `purge_log.issue_uid`: a v1 purge whose issue we cannot reconstruct.
- `purge_log.project_uid` ← the imported `projects.uid` for `purge_log.project_id`. Always non-NULL (projects are not deleted in v1).

The seed shapes above (`project:<id>:<identity>`, `issue:<project_id>:<number>`) are part of the v1→v2 fill contract: changing them silently re-issues UIDs on a re-run. They are deliberately built from primary identifiers that don't change post-fill (issue number is stable for a given project; project identity is the URL, also stable). Tested against the live kata DB, this produces 176 unique issue UIDs and 4 unique project UIDs across two independent runs (per `/tmp/v1_to_v2_fill_test.py`).

If the importer encounters an `events` row with non-null `issue_id` that does not resolve to an imported issue, that is v1 data corruption (master spec invariant: purge cascade-deletes events, so any event with non-null FK must resolve). The importer aborts with `corrupt_event_fk` naming the offending event id and FK column. We do **not** silently NULL the column.

Determinism: running the v1→v2 cutover twice on the same source produces identical destination DBs (modulo `meta.created_by_version` and `meta.exported_at`, neither of which load-bearing).

## 6. Wire-shape updates (`internal/api/types.go` + Plan 4 envelope)

All existing fields stay; UID fields are additive. Clients that ignore them remain functional.

### 6.1 `Project`

```jsonc
{
  "id": 1,
  "uid": "01JTYJ4XRP9XMP2GWDQE5J7ZN1",
  "identity": "github.com/wesm/kata",
  "name": "kata",
  "created_at": "2026-04-29T00:00:00.000Z"
}
```

### 6.2 `Issue`

```jsonc
{
  "id": 137,
  "uid": "01JTYJ73R8K7XGZ5T6FJ8VYC2N",
  "project_id": 1,
  "project_uid": "01JTYJ4XRP9XMP2GWDQE5J7ZN1",
  "number": 29,
  "title": "...",
  // ...
}
```

`project_uid` is denormalized onto the issue envelope for the same reason `project_identity` already is on `EventEnvelope`: a remote client receiving an issue without first having fetched the project should still know the project's stable identity.

### 6.3 `Link`

```jsonc
{
  "id": 42,
  "project_id": 1,
  "type": "parent",
  "from_issue_id": 137,
  "to_issue_id": 110,
  "from_issue_uid": "01JTYJ73R8K7XGZ5T6FJ8VYC2N",
  "to_issue_uid":   "01JTY9X4MPQ7VJK2SCH6FMR5W3",
  "from_issue_number": 29,
  "to_issue_number": 2,
  "author": "wesm",
  "created_at": "..."
}
```

### 6.4 `EventEnvelope` (Plan 4 §4.1 — additive)

```go
type EventEnvelope struct {
    EventID         int64           `json:"event_id"`
    Type            string          `json:"type"`
    ProjectID       int64           `json:"project_id"`
    ProjectIdentity string          `json:"project_identity"`
    ProjectUID      string          `json:"project_uid"`              // NEW
    IssueID         *int64          `json:"issue_id,omitempty"`
    IssueNumber     *int64          `json:"issue_number,omitempty"`
    IssueUID        *string         `json:"issue_uid,omitempty"`      // NEW
    RelatedIssueID  *int64          `json:"related_issue_id,omitempty"`
    RelatedIssueUID *string         `json:"related_issue_uid,omitempty"` // NEW
    Actor           string          `json:"actor"`
    Payload         json.RawMessage `json:"payload,omitempty"`
    CreatedAt       time.Time       `json:"created_at"`
}
```

### 6.5 `PurgeLogEntry`

Adds `issue_uid` and `project_uid`. May be NULL on rows that pre-date the v1→v2 cutover where the issue was already gone (per §5.2).

### 6.6 Lookup endpoints

`GET /api/v1/projects/{project_id}/issues/{number}` stays. New optional surface:

`GET /api/v1/issues/{uid}` — a cross-project UID lookup. Returns 404 with code `issue_not_found` if no row matches. Mirrors the existing per-project endpoint's response shape.

CLI `kata show` accepts either `#42` (current behavior) or a full/prefix UID. Prefix is allowed when it uniquely resolves; ambiguous prefix → error with the candidate list. Minimum prefix length is 8 chars (otherwise typos resolve to surprising rows).

## 7. CLI / TUI surface notes

- Default human display stays `#42`. UIDs only render in `--json`, the detail view's metadata footer, and `kata show --uid`.
- `kata show <ref>` accepts `#42`, `42`, or a UID/prefix.
- `kata link <from> <to>` accepts the same. The wire payload always carries the UIDs after lookup.
- TUI detail view's metadata footer gets a small uid-display row, dimmed, copy-friendly. The compact issue sheet doesn't change — UID is footer-only, not chrome.
- `kata config` exposes `display.uid_format = none|short|full`. Default `none` (no UID in human renderings); `short` renders the leading 8 chars of the UID with a leading `~` (e.g. `~01JTYJ73`) wherever the issue label would otherwise render; `full` renders the canonical 26-char form.

## 8. Test plan

The migration-correctness tests that previously sat in this spec move to the JSONL roundtrip spec — that is now the load-bearing path for v1→v2 cutover, and its `TestRoundtrip_LiveDB` / `TestCutover_V1ToV2_LiveDB` cover the schema transition. This section covers UID-specific behavior independent of the cutover.

### 8.1 Unit — `internal/uid` (new package)

- `uid.New()` returns 26-char Crockford base32; 100k generated UIDs are all unique and strictly monotonic when generated in a tight loop.
- `uid.FromTime(t)` produces a ULID whose timestamp portion decodes to `t` (rounded to ms). Two calls with the same `t` share the leading 10 chars but differ in the trailing 16 (random entropy).
- `uid.FromStableSeed(seed, t)` is **fully deterministic**: same seed + same `t` → byte-equivalent output. Different seed with same `t` → same leading 10 chars but different trailing 16 (the entropy is `SHA-256(seed)[:10]`). 100k distinct seeds at the same `t` produce 100k distinct UIDs (no SHA-256 collisions in 80 bits at this scale).
- `uid.Valid` accepts canonical ULIDs, rejects 25-char and 27-char strings, rejects non-Crockford chars (I, L, O, U), rejects ULIDs with bits-overflow in the leading character.
- `uid.ValidPrefix(s)` accepts 1- to 26-char Crockford base32 strings, rejects empty, rejects 27+ chars, rejects non-Crockford chars.

### 8.2 Unit — db queries

- `db.IssueByUID(ctx, uid)` returns the issue or sql.ErrNoRows.
- `db.IssuesByUIDs(ctx, []string)` is implemented for batch resolution.
- `db.IssueUIDPrefixMatch(ctx, prefix, limit)` returns matching UIDs; ambiguous prefix returns multiple; empty prefix returns `ErrPrefixTooShort`; >26-char prefix returns `ErrInvalidPrefix`.
- `db.ProjectUIDPrefixMatch(ctx, prefix, limit)` mirrors the above for projects.
- `db.CreateLinkParams` requires both UIDs and both integer FKs; insert succeeds and triggers fire (cross-project insert is rejected; UID/FK-mismatch insert is rejected by the new consistency triggers).

### 8.3 UID/FK consistency triggers

- Direct `INSERT INTO links` with `from_issue_uid` not matching `from_issue_id`: rejected with `from_issue_uid does not match from_issue_id`.
- Direct `UPDATE links SET from_issue_uid = '<wrong>' WHERE id = N`: same rejection.
- Same for `to_issue_uid`.
- The daemon's normal insert path (which writes both columns from the same source row) succeeds.

### 8.4 Handler

- `GET /api/v1/issues/{uid}` returns 200 + envelope on a known UID; 404 + `issue_not_found` on an unknown.
- `POST /api/v1/projects/{project_id}/issues` writes `uid`; the response envelope contains the new UID; the next read returns the same UID.
- `POST .../links` accepts UID-or-number for endpoints; the stored row carries both UID and integer FK; the response envelope carries both.
- `EventEnvelope` returned by mutation handlers includes `project_uid`, `issue_uid`, `related_issue_uid` when applicable.
- Plan 4 SSE: a new event broadcast carries the UIDs in its `data:` payload.

### 8.5 CLI

- `kata show #42` and `kata show 01JTYJ73R8K7XGZ5T6FJ8VYC2N` resolve to the same issue.
- `kata show 01JTYJ73` (8-char prefix) resolves when unique; errors with `prefix_ambiguous` and the candidate list when not.
- `kata show 01JT` (4-char prefix) errors with `prefix_too_short`.
- `kata show --json` includes `uid` and `project_uid` on the issue envelope.
- `kata events --tail --json`'s NDJSON envelopes carry `issue_uid` etc.

### 8.6 TUI

- Detail view's metadata footer renders the UID dimmed, full or short per `display.uid_format` config.
- Compact issue sheet does not render the UID (footer-only contract).
- A snapshot test asserts the footer shape across the three `display.uid_format` modes.
- TUI lookup commands (`p` set parent, `b` add blocker, `l` add link) accept UID input alongside number input.

### 8.7 Property tests (gopter)

- `kata create issue` then `kata show <returned_uid>` always resolves to the created issue. 1000 iterations.
- `kata create link` then read back: `from_issue_uid` matches the from-issue's `uid`, `to_issue_uid` matches the to-issue's `uid`. 1000 iterations.
- Round-trip: encode a UID with `uid.FromStableSeed(seed, t)`, decode the timestamp portion — should equal `t` to ms. The trailing 16 chars equal `Crockford(SHA-256(seed)[:10])`.

### 8.8 e2e

`TestSmoke_IssueIdentityV2`:

1. Start daemon against an empty DB at `currentSchemaVersion=2`. UIDs come from row insert.
2. Create project A (1 issue), project B (1 issue).
3. Assert response envelopes contain `uid` for project and issue.
4. SSE-tail project A; create comment on A's issue; assert the SSE frame's `data:` JSON contains `issue_uid` and `project_uid` matching the row.
5. Soft-delete A's issue; the next SSE frame is `issue.soft_deleted` and its `issue_uid` still points to A's UID.
6. Purge A's issue:
   - Purge transaction populates `purge_log.issue_uid` (matching A's UID) and `purge_log.project_uid` (matching A's project UID) before the cascade.
   - `events` rows for A's issue are deleted (master spec §3.5).
   - `sqlite_sequence.seq` for `events` is bumped by one to reserve `purge_reset_after_event_id`.
7. The SSE consumer (cursor below `purge_reset_after_event_id`) receives `sync.reset_required` and the stream closes. No replayed deleted events.
8. After reconnect with the reset cursor: no events for the purged issue.
9. `GET /api/v1/issues/{purged_uid}` → 404. `purge_log` row from step 6 has `issue_uid` and `project_uid` populated.

The v1→v2 cutover smoke test (`TestCutover_V1ToV2_LiveDB`) lives in the JSONL spec.

## 9. Open questions / tunables

- **ULID library choice.** `oklog/ulid/v2` is the most popular Go ULID library, MIT-licensed, single-purpose, no transitive deps. If supply-chain audit raises a concern, the alternative is a ~150-line in-tree implementation (Crockford base32 + monotonic entropy via `sync.Mutex` + `crypto/rand`). Default to the library; revisit if audit pushes back.
- **Prefix lookup minimum length.** Set to 8. Real-data finding: in the live kata DB after a v1→v2 cutover (176 issues, all backfilled with `uid.FromStableSeed(<row-stable-seed>, created_at)`), 8-char prefixes collide heavily (worst cluster: 15 issues sharing the same 8-char prefix) because ULID's timestamp portion is exactly 10 chars and many issues were created in a tight time window. 10-char prefixes have no collisions in that fixture. The minimum-8 guard prevents typo-driven near-matches; the ambiguity-with-candidate-list response handles the dense-prefix case. For new (non-backfilled) issues created post-v2 via `uid.New()`, the 80-bit random entropy portion makes 8-char prefixes effectively unique. Operators of cutover DBs will type more characters; that's acceptable. Revisit if real usage shows the friction outweighs the typo-guard value.
- **`display.uid_format` default.** Default `none` (no UID in human renderings) is the conservative pick — most users don't want a 26-char string in their list view. Flip to `short` later via config if users find the absence annoying.
- **Cross-instance import.** Out of scope here. When that lands, the JSONL importer should preserve incoming UIDs verbatim; ULID uniqueness across instances is statistically secure (80 bits of randomness) but the import path will need a UID-collision detection check anyway, plus a documented conflict-resolution rule.
- **Removal of integer FKs.** Out of scope. If a future federation milestone wants to carry "dangling" UIDs (links to non-local issues), revisit then with a separate spec — the storage shape is forward-compatible but the read paths and triggers will need updating.

## 10. Sequencing — where this lands in the existing plan

This spec **depends on** the JSONL roundtrip spec (`2026-05-03-kata-jsonl-roundtrip-design.md`), which provides the cutover infrastructure. The order is:

1. **JSONL roundtrip lands first** — `kata export` / `kata import` / daemon first-boot-cutover all wired up against the v1 schema (no UID columns yet). The cutover is a no-op when source and binary share a schema version.
2. **Issue identity (this spec) lands second.** Schema edits to `0001_init.sql`, `currentSchemaVersion` bumps to `2`, the JSONL importer's v1→v2 fill rules go in, and the wire envelopes pick up `uid` fields. The next time a v1-DB-bearing developer's binary updates, the daemon's first-boot cutover migrates them to v2 automatically.
3. After this lands, `kata #3` (Phase 2 readiness pass) proceeds in its current shape: schema renames, workspace resolver extraction, auth middleware, token storage. Each of those issues' wire-touching work picks up UID-bearing envelopes for free.
4. The TUI remote-engine work (`kata #5`) consumes UID-bearing envelopes and uses UIDs for any SSE-driven state reconciliation that needs to survive renumbering / cross-instance contexts.

Concretely: this spec produces one new kata epic with 5–6 child issues (UID library, schema edits to 0001_init.sql + version bump, importer fill rules, wire envelopes, CLI prefix lookup, TUI footer). The existing `kata #3` is re-parented to depend on the epic.

## 11. Implementation order (for the plan doc)

Hand off to `superpowers:writing-plans`. Expected ordering — note this assumes the JSONL roundtrip work is already in-tree:

1. `internal/uid` package: ULID generation, validation, prefix validation. With property tests (§8.1, §8.7).
2. Schema edits to `internal/db/migrations/0001_init.sql`: add `projects.uid`, `issues.uid`, `links.from_issue_uid`/`to_issue_uid`, `events.issue_uid`/`related_issue_uid`, `purge_log.issue_uid`/`project_uid`. New indexes. New consistency triggers. No `meta.schema_version` seed insert — the migration runner writes that key from `currentSchemaVersion` post-init (per JSONL spec §6.0).
3. `internal/db/db.go`: bump `currentSchemaVersion` constant to `2`. The migration runner change (per JSONL spec §6.0) lands as part of the JSONL roundtrip work, not here — by the time this step runs, the runner already records `currentSchemaVersion` post-init instead of the filename-derived version.
4. `internal/db/queries.go`: write paths emit `uid.New()` on every insert and populate UID columns alongside integer FKs.
5. `internal/api/types.go` + Plan 4's `internal/api/events.go`: wire envelopes gain `uid` / `*_uid` fields.
6. JSONL importer: add the v1→v2 fill rule (§5.2). The fill rule lives inside the importer because the importer is the one that observes `meta.export_version`. Tests in the JSONL spec's §8.2 / §8.4.
7. Daemon handlers: emit UIDs on responses; accept UIDs on inputs that take a `<ref>`.
8. New endpoint `GET /api/v1/issues/{uid}`.
9. CLI: `kata show` accepts UIDs and prefixes; same for the few other `<ref>`-taking commands.
10. TUI: footer UID display gated on `display.uid_format`.
11. e2e (`TestSmoke_IssueIdentityV2`).
12. Master spec doc edits: §3.1 (data model) and §4 (wire shapes) updated to reflect UID as authoritative identifier; §1 vocabulary updated.
