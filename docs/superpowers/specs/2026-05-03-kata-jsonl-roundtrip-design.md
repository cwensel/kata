# JSONL Roundtrip — Export/Import as Schema Evolution

> **Status:** design / spec. Companion to `docs/superpowers/specs/2026-04-29-kata-design.md` (master design) and `docs/superpowers/specs/2026-05-03-kata-issue-identity-design.md` (issue-identity work, which depends on this). Subsumes the unimplemented "kata #14 backup / restore" work and provides the foundation for "kata #25 importers."

## 1. Locked decisions

These four decisions are settled here. The implementation plan does not re-litigate them.

1. **Format — NDJSON with kind-tagged envelopes.** One JSON object per line. Each line is `{"kind":"<kind>","data":{...}}` where `<kind>` is one of a fixed enumeration: `meta`, `project`, `project_alias`, `issue`, `comment`, `issue_label`, `link`, `event`, `purge_log`, `sqlite_sequence`. The `data` object's fields exactly mirror the SQL column names of the corresponding table — no renames, no synthetic fields, no nesting beyond what the column itself carries (`events.payload` is a JSON value already and is emitted as a JSON value, not a stringified blob). Unknown kinds on import → fail with a clear error naming the offending line. Unknown fields within a known kind → log warning, ignore (forward compatibility for additive schema changes).

2. **Ordering — topological by FK dependency, deterministic within each kind, `sqlite_sequence` last.** The exporter writes records in this order: `meta` → `project` → `project_alias` → `issue` → `comment` → `issue_label` → `link` → `event` → `purge_log` → `sqlite_sequence`. Within each kind, ordered by primary key ascending. `sqlite_sequence` appears **at the end** because (a) it is a reserved SQLite-internal table that is auto-created lazily on the first AUTOINCREMENT insert into the destination — it does not exist yet at import start; and (b) explicit-`id` inserts during the data phase already cause SQLite to populate `sqlite_sequence` rows with `seq = MAX(id)` per table, so the trailing `sqlite_sequence` records exist to **raise** those values to the source's recorded seq when needed (the post-purge "highest-ever-used > MAX(id)" case — see §5.1). Strict-ordering requirement is for FK-bearing tables; the importer asserts the order on read (out-of-order kind → fail-fast with diagnostic). Roundtrip-tested against the live kata DB: 1124 records, 4 projects, 186 issues, 365 events, 152 links, 430 issue_labels, 47 comments — exporting → init fresh schema → importing → re-exporting yields a byte-equivalent JSONL file.

3. **Atomicity — daemon-stopped, build-temp-then-swap.** The cutover never modifies the source DB in place. The full sequence (also given in §6.3 with diagrams):
   1. Read-only export from `<dbpath>` to `<dbpath>.import.tmp.jsonl`.
   2. Init a fresh schema at `<dbpath>.import.tmp.db`.
   3. Import the JSONL into `<dbpath>.import.tmp.db` inside one transaction with `PRAGMA foreign_keys=ON`; commit only after `PRAGMA foreign_key_check` and `PRAGMA integrity_check` pass.
   4. Atomic-rename `<dbpath>` → `<dbpath>.bak.<ts>` (preserves the source).
   5. Atomic-rename `<dbpath>.import.tmp.db` → `<dbpath>` (the new DB becomes live).
   6. Delete `<dbpath>.import.tmp.jsonl`.
   On any failure before step 4, the temp files are deleted and `<dbpath>` is untouched. The CLI refuses to import while the daemon process is running (PID-file check); the daemon refuses to start if either temp file exists (operator must investigate).

4. **Versioning — `meta.export_version` is the source schema version, not the binary's.** The first non-comment record in every export file is `{"kind":"meta","data":{"key":"export_version","value":"<N>"}}` where `<N>` is the integer value of `meta.schema_version` in the **source** DB at export time. The exporter emits each record in its source-schema shape: a v1 source emits records without UID fields and `export_version=1`; a v2 source emits records with UID fields and `export_version=2`. The exporter never synthesizes new-schema fields it doesn't have in the source — that work is the importer's job, gated on the version it reads.

   The importer matches on `<N>` against its own binary's `currentSchemaVersion`:
   - `<N>` equals binary current → straight import (records carry every field; no transformation).
   - `<N>` less than binary current → upgrade import (importer applies version-specific fill rules to bring records up to the binary's schema, e.g. generating UIDs for v1 records that lack them — see §6.1 of the issue-identity spec).
   - `<N>` greater than binary current → fail with `export_too_new` and the supported max. Operators downgrade the export by re-exporting from the appropriate kata version, or upgrade the binary.
   - The kata binary supports importing exports from `<current> - K` to `<current>`, where `K` is the rolling-deprecation window (initially `K=1`; revisit when needed). Older exports must first be passed through an intermediate kata version's import-then-export.

   This semantics removes the previous contradiction where the locked decision said "current = no transformation" while the cutover description had the exporter emit `N+1` records from an `N` source. The exporter never lies about the source's shape; the importer alone owns the upgrade transformation.

The rest of this document expands on what these decisions imply for round-trip fidelity, schema evolution, the CLI, and the daemon's first-boot-cutover behavior.

## 2. Goal

Make the SQLite database a recreatable artifact, not a primary one. Every byte of kata state — projects, aliases, issues, comments, labels, links, events (including idempotency-key payloads and synthetic reset cursors), purge_log audit rows, and the `sqlite_sequence` state that backs AUTOINCREMENT — round-trips through a single newline-delimited JSON file with no information loss. The importer reproduces the source database byte-equivalent at the row level (FTS shadow tables are deliberately excluded — they are an index, regenerated automatically by AFTER INSERT triggers as records are imported).

The downstream goals are three:

- **Schema evolution without in-place migrations.** When the schema changes (e.g. adding `uid` columns), the cutover is `kata export` (with the older binary or via the new binary's compat-aware exporter) → init a fresh DB at the current schema → `kata import`. Old DB shapes go away as soon as they're not in the wild. There is no migration runner with versioned `0002_uids.sql` / Go-coded backfill steps; there is one schema file (`0001_init.sql`, kept as the latest) and one importer that fills any version-skew gaps.
- **Operator-visible backup and restore** (subsumes the deferred kata #14). `kata export` produces an artifact you can copy, version, and grep; `kata import` consumes it. Same code path serves both schema migration and operator workflows.
- **Foundation for cross-instance import** (foundation for the deferred kata #25). Beads/GitHub Issues/JIRA importers all produce the same JSONL shape; the existing import path consumes them. Whether the JSONL came from another kata instance or an external source is not the importer's concern.

## 3. Scope

**In scope**
- The wire format (§4 below), including the exact field set per kind.
- Exporter that walks the DB and emits the NDJSON file in the §1.2 ordering.
- Importer that reads the NDJSON, validates kind ordering and version, applies the v1→current fill rules (§6), and writes a fresh DB.
- `kata export [--output PATH] [--project-id N]` and `kata import --input PATH [--target DBPATH] [--force]` CLI commands.
- Daemon first-boot cutover: when the daemon sees `meta.schema_version` older than the current binary's expected version, it runs export-then-import internally before serving any requests.
- Round-trip property tests over real fixtures (§7.1).
- The "exporter is schema-version-aware" capability needed to drop `<N> - K` versions out of compat: the exporter knows about every supported source schema version and emits that source version's canonical JSONL shape. Upgrade fills are importer-owned.

**Out of scope (deferred)**
- Live import (no daemon-stopped requirement). Adds locking complexity and isn't worth it for kata's workflow — operators can stop the daemon for a few seconds.
- Streaming import / progress reporting beyond a simple line counter on stderr. Imports for kata-sized data complete in milliseconds; richer UX waits for evidence we need it.
- Encrypted exports. Operators who need encryption use `gpg` or `age` on the file; bundling crypto inside kata is unnecessary surface area.
- Differential / delta exports. Re-export the whole thing; kata data is small.
- Selective import (e.g., import only one project from a multi-project export). Re-export from the source instance with `--project-id` filter.
- Compression. Operators who want compressed artifacts pipe through `gzip`; we don't bundle a format choice.
- A separate "schema dump" command. The schema is whatever `0001_init.sql` declares at the binary version, recorded in the export's `meta.created_by_version` field.

## 4. Wire format

### 4.1 File header

The first record in every export file is the export-version meta record, with the value set to the **source DB's** `meta.schema_version` at export time (per §1.4). The shape is:

```json
{"kind":"meta","data":{"key":"export_version","value":"<source_schema_version>"}}
```

Concrete examples:

```json
// Exported from a v1 source DB (no UID columns):
{"kind":"meta","data":{"key":"export_version","value":"1"}}

// Exported from a v2 source DB (after the issue-identity work has landed):
{"kind":"meta","data":{"key":"export_version","value":"2"}}
```

Followed by additional `meta` records for any other key/value pairs (`schema_version` matching `export_version`, `created_by_version`, `exported_at`). All `meta` records appear before any data records.

The export version is integer-typed but written as a string to match the existing `meta.value TEXT NOT NULL` column. The exporter never lies about the source: a v1 source always emits `"1"` even if the binary running the export has `currentSchemaVersion=2`. The importer is what bridges the gap (§6.1 fill rules).

### 4.2 Per-kind shapes

Each kind's `data` object mirrors its SQL row exactly. NULL columns are emitted as JSON `null` (not omitted). Datetime columns retain their stored format (ISO-8601 with millisecond precision, UTC `Z` suffix).

```jsonc
// projects
{"kind":"project","data":{
  "id":1, "uid":"01JTYJ4XRP9XMP2GWDQE5J7ZN1",
  "identity":"github.com/wesm/kata", "name":"kata",
  "created_at":"2026-04-29T00:00:00.000Z", "next_issue_number":30
}}

// project_aliases
{"kind":"project_alias","data":{
  "id":1, "project_id":1,
  "alias_identity":"github.com/wesm/kata", "alias_kind":"git",
  "root_path":"/Users/wesm/code/kata",
  "created_at":"...", "last_seen_at":"..."
}}

// issues
{"kind":"issue","data":{
  "id":110, "uid":"01JTYJ73...", "project_id":1, "number":2,
  "title":"...", "body":"...", "status":"open",
  "closed_reason":null, "owner":null, "author":"wesm",
  "created_at":"...", "updated_at":"...",
  "closed_at":null, "deleted_at":null
}}

// comments
{"kind":"comment","data":{
  "id":12, "issue_id":110, "author":"wesm",
  "body":"...", "created_at":"..."
}}

// issue_labels
{"kind":"issue_label","data":{
  "issue_id":110, "label":"epic",
  "author":"wesm", "created_at":"..."
}}

// links — both UID columns present in v2 exports; absent in v1 (importer fills)
{"kind":"link","data":{
  "id":42, "project_id":1, "type":"parent",
  "from_issue_id":137, "to_issue_id":110,
  "from_issue_uid":"01JTYJ73...", "to_issue_uid":"01JTY9X4...",
  "author":"wesm", "created_at":"..."
}}

// events
{"kind":"event","data":{
  "id":81235, "project_id":1, "project_identity":"github.com/wesm/kata",
  "project_uid":"01JTYJ4XRP9XMP2GWDQE5J7ZN1",
  "issue_id":110, "issue_number":2, "issue_uid":"01JTY9X4...",
  "related_issue_id":null, "related_issue_uid":null,
  "type":"issue.commented", "actor":"wesm",
  "payload":{"comment_id":12},
  "created_at":"..."
}}

// purge_log — pre-v2 rows may have null issue_uid/project_uid
{"kind":"purge_log","data":{
  "id":1, "project_id":1, "purged_issue_id":42,
  "project_identity":"github.com/wesm/kata",
  "issue_uid":"01JTY9X4...", "project_uid":"01JTYJ4X...",
  "issue_number":42, "issue_title":"...", "issue_author":"...",
  "comment_count":3, "link_count":1, "label_count":2, "event_count":7,
  "events_deleted_min_id":81100, "events_deleted_max_id":81234,
  "purge_reset_after_event_id":81235,
  "actor":"wesm", "reason":"duplicate of #...",
  "purged_at":"..."
}}

// sqlite_sequence — emitted last, after every data record. The table is
// SQLite-internal/reserved and is auto-created by AUTOINCREMENT inserts above;
// these records exist to raise seq values past current MAX(id) for the
// post-purge "highest-ever-used" case (see §5.1).
{"kind":"sqlite_sequence","data":{"name":"projects","seq":4}}
{"kind":"sqlite_sequence","data":{"name":"issues","seq":186}}
{"kind":"sqlite_sequence","data":{"name":"links","seq":152}}
{"kind":"sqlite_sequence","data":{"name":"events","seq":81235}}
{"kind":"sqlite_sequence","data":{"name":"comments","seq":47}}
```

### 4.3 What is *not* in the file

- `issues_fts` and its shadow tables (`issues_fts_data`, `issues_fts_idx`, `issues_fts_docsize`, `issues_fts_config`). These are an index. The importer relies on the AFTER INSERT triggers on `issues` and `comments` (`issues_ai_fts`, `comments_ai_fts`) to rebuild the FTS index automatically as records are inserted. The importer asserts post-import that `SELECT count(*) FROM issues_fts` matches `SELECT count(*) FROM issues` as a sanity check.
- The schema itself. The importer assumes the target DB was initialized with `0001_init.sql` (the latest schema) before import begins. The exporter records `meta.created_by_version` so an operator can correlate. **Note:** `0001_init.sql` does not contain a `CREATE TABLE sqlite_sequence` statement — `sqlite_sequence` is a reserved SQLite-internal table that is auto-created on the first AUTOINCREMENT insert. An init script that tries to declare it explicitly fails with `object name reserved for internal use: sqlite_sequence`. The schema file already complies; this note exists to prevent an accidental "I'll just clone the schema by snapshotting `.schema` output" approach from breaking, since `.schema` does emit the table.
- `sqlite_master` artifacts (indexes, triggers). All of these come from the schema file.

## 5. Round-trip fidelity

### 5.1 Identity preservation

- Every `id` column is preserved verbatim. The importer issues `INSERT INTO <table>(id, ...) VALUES (?, ...)`. SQLite accepts explicit ids on AUTOINCREMENT tables unconditionally and updates `sqlite_sequence.seq` to `MAX(old_seq, this_id)` as a side effect — so by the time all data rows are inserted, `sqlite_sequence` already holds `MAX(id)` per table.
- The `sqlite_sequence` records emitted at the **end** of the export then bring those values up to the source's recorded seq for the post-purge "highest-ever-used > MAX(id)" case (a purged-then-deleted issue with id > all surviving issues left a sequence value > current MAX(id); without this step the next post-import insert would re-issue that purged id, silently colliding with `purge_log.purged_issue_id` and any historical reference).
- The importer applies these sqlite_sequence records by `UPDATE sqlite_sequence SET seq = ? WHERE name = ?` if the row already exists (which it will for tables that received any data inserts — SQLite auto-created the row), or `INSERT INTO sqlite_sequence(name, seq) VALUES (?, ?)` otherwise. **Cannot use `INSERT OR REPLACE`**: `sqlite_sequence` has no UNIQUE constraint on `name` (it's a SQLite-internal table without a primary key), so `INSERT OR REPLACE` does not deduplicate and would produce multiple rows per table.
- After all data + sqlite_sequence records are processed, the importer runs a final reconciliation per AUTOINCREMENT table: `seq := MAX(stored_seq, current MAX(id))`. This is defensive — if the export came from a source where a sqlite_sequence row was somehow missing, MAX(id) from the data inserts is still recorded. The reconciliation uses the same UPDATE-or-INSERT pattern as above.
- Reserved cursors from `kata purge` (master spec §3.5: purge bumps `sqlite_sequence.seq` for `events` without inserting a row) live in `events.sqlite_sequence.seq` and survive intact through this path, since the source's recorded seq is preserved verbatim.

### 5.2 Synthetic reset cursors

`purge_log.purge_reset_after_event_id` values are reserved cursors past every real `events.id` at the moment of purge (master spec §3.5 step 4). They live in `events.sqlite_sequence.seq` (purge bumps `seq` by one without inserting a row). On export, both the `purge_log` row's `purge_reset_after_event_id` and the `events.sqlite_sequence` value carry this state forward. On import, explicit-id event inserts auto-create the `events` row in `sqlite_sequence` with `seq = MAX(events.id)`; the trailing `sqlite_sequence` record (per §1.2 ordering) then raises that value to the source's recorded seq, recovering the reserved cursor. The `purge_log` row's own `purge_reset_after_event_id` field is preserved verbatim by its INSERT. SSE subscribers that reconnect after a cutover see the same "below the reset → `sync.reset_required`" behavior they would have seen against the source DB.

### 5.3 Timestamps

`DATETIME` columns store ISO-8601 with `%f` format (millisecond precision, UTC `Z` suffix). The exporter emits them as JSON strings in the same shape; the importer inserts them verbatim. There is no datetime parsing on either side — the column is `TEXT` underneath in SQLite, and we treat it that way.

### 5.4 JSON payloads

`events.payload` and any future JSON-typed columns are emitted as JSON values inside the envelope's `data` object (not stringified). The exporter validates `json_valid(payload)` on read; the importer validates again on insert (via the existing CHECK constraint). A row that fails either check fails the export/import with a diagnostic naming the row id.

### 5.5 Soft-delete and purge

Soft-deleted issues (`deleted_at IS NOT NULL`) export with `deleted_at` populated; importer respects the column. The FTS index regenerates from the AFTER INSERT trigger regardless of soft-delete state — which is what we want, since soft-deleted issues remain searchable with `--include-deleted`.

Purge-deleted state is captured by `purge_log` rows, which are exported and imported. The `events` rows that were deleted at purge time are gone in the source DB and stay gone in the destination (export sees what's there).

### 5.6 What round-trip means precisely

After `export → init → import`, the destination DB satisfies:

- Same row count per table.
- Same primary keys per table.
- Same column values per row (modulo FTS shadow tables, which are regenerated and not byte-equivalent — they hold the same logical content though).
- Same `sqlite_sequence` state.
- Same `meta` values (`schema_version`, `created_by_version`).
- Same FTS results: every query that returned a row against the source returns the same row against the destination.

The §7 test plan asserts each of these on a real fixture (the test author's live DB, copied to a tmpdir).

## 6. Schema evolution = export-then-import

Cutting over from one schema version to the next, in operational terms (this section describes the contract; §6.3 below describes the on-disk file dance, which is owned by `jsonl.AutoCutover`):

1. The daemon at first boot detects the source DB at `meta.schema_version = N` while its own `currentSchemaVersion` constant is `N+1` (or higher).
2. The exporter reads the source DB and emits a JSONL file with **`meta.export_version = N`** — matching the source's schema version, not the binary's. Records carry only fields that exist in the source schema; new-schema fields (e.g. v2's `issues.uid`) are simply absent from records. The exporter is single-pass and read-only against the source.
3. The binary inits a fresh DB at the binary's current schema (`0001_init.sql`, declaring schema version `currentSchemaVersion`).
4. The importer reads the JSONL. Because `export_version (N) < currentSchemaVersion (N+1)`, this is an upgrade import: for each record kind that gained a field in `N+1` (e.g. `issues.uid` is `NOT NULL` in v2), the importer applies a deterministic fill rule (see §6.1) to populate it from the v1 record's existing fields. Imports complete inside a single transaction with FK enforcement and `foreign_key_check`/`integrity_check` validation.
5. The atomic file swap (§6.3) makes the freshly imported DB the live one; the source DB is preserved as a backup.

The important conceptual move: there is **no `0002_uids.sql`**, no Go-coded migration runner with FK toggling, no in-place rebuild. The `0001_init.sql` file is always the latest schema. Old shapes evaporate as soon as the export-import cutover runs.

### 6.0 Migration runner change

`internal/db/db.go`'s migration runner currently records `meta.schema_version` by parsing the migration filename (`0001_init.sql` → version `1`) at `internal/db/db.go:84-89`. This conflicts with "0001 is always the latest schema": after editing `0001_init.sql` to declare v2 (per the issue-identity spec), a fresh init would still write `meta.schema_version = '1'` because the filename hasn't changed.

The runner is updated to:

1. Run any pending `migrations/*.sql` files from `embed.FS` (still in filename-sorted order — there will only ever be `0001_init.sql` once the JSONL approach lands, but the loop stays for the trivial case).
2. After the SQL has been exec'd, set `meta.schema_version` to the binary's `currentSchemaVersion` constant (a Go integer in `internal/db`), not to the parsed filename version.
3. `currentSchemaVersion` is the single source of truth for "what schema does this binary expect." It bumps from `1` to `2` when the issue-identity work lands.

`0001_init.sql` itself does **not** insert `meta.schema_version` — the seed insert is removed from the SQL file. The runner owns that key. (The `created_by_version` seed insert stays where it is.)

This change is small and precedes the issue-identity work logically: it lets `0001_init.sql` declare arbitrary schema content while the runner records the version the binary believes it is. After this lands, the issue-identity spec's "edits to 0001_init.sql + bump `currentSchemaVersion`" is a pure SQL edit + Go constant edit with no migration-runner work.

### 6.1 Fill rules for v1 → v2 (issue identity)

When the importer sees `meta.export_version=1` on input with `currentSchemaVersion=2`, for each record kind that gained a field in v2 the importer applies a deterministic fill rule:

- **`projects.uid`** = `uid.FromStableSeed(seed("project", project.id, project.identity), projects.created_at)`. The seed is a stable byte string built from the row's primary identifiers; the helper produces a ULID whose timestamp portion encodes `created_at` and whose entropy is `SHA-256(seed)[:10]`. Same source row → same UID across reruns.
- **`issues.uid`** = `uid.FromStableSeed(seed("issue", issue.project_id, issue.number), issues.created_at)`. Same.
- **`links.from_issue_uid` / `links.to_issue_uid`** = the imported `issues.uid` values, looked up by `from_issue_id` / `to_issue_id`. Issues are imported before links (per §1.2 ordering), so the lookup always succeeds.
- **`events.issue_uid` / `events.related_issue_uid`** = the imported `issues.uid` looked up by `issue_id` / `related_issue_id` (when non-null). Events are imported after issues; the lookup always succeeds for v1-source data because v1 invariant is "events with non-null issue FK resolve to a live or soft-deleted issue row" (master spec §3.5: purge cascade-deletes events). If the lookup fails, the importer fails with `corrupt_event_fk` naming the offending event id and FK column.
- **`purge_log.project_uid`** = the imported `projects.uid` looked up by `project_id`; the lookup must succeed or the importer fails with `corrupt_purge_log_fk`.
- **`purge_log.issue_uid`** = the imported `issues.uid` looked up by `purged_issue_id`. The issue may already be gone (purge cascade removed it) — in that case only `issue_uid` stays NULL. This is the only legal NULL `purge_log.issue_uid`: a v1 purge that completed before the cutover. Future purges (v2+) populate both columns from the live `issues.uid` / `projects.uid` read at purge time.

The fill is deterministic: the same v1 DB exported and imported twice produces the same UIDs.

### 6.2 Future schema versions

Same shape. Adding a column in v3? The v3 schema declares it; the v3 exporter emits it (NULL where the source can't supply it); the v3 importer either honors the input or applies a fill rule, just like v1→v2.

Removing a column in v3? The v3 importer sees the field on v2 records, ignores it (per §1.1 "unknown fields → log + ignore").

Renaming a column in v3? The v3 importer sees the old field on v2 records and maps it to the new column. We don't promise renames are free — they need explicit importer logic — but the cost is bounded to one pure-function mapping per rename.

### 6.3 Daemon first-boot cutover

The daemon's startup sequence at `internal/daemon/server.go` adds an early step:

```go
ver, err := db.PeekSchemaVersion(path)
if err != nil { ... }
if ver < currentSchemaVersion {
    if err := jsonl.AutoCutover(ctx, path); err != nil {
        return fmt.Errorf("schema cutover from v%d to v%d failed: %w", ver, currentSchemaVersion, err)
    }
}
// proceed to db.Open as usual
```

`jsonl.AutoCutover` follows the temp-DB-then-swap pattern from §1.3. The source `<path>` is **not touched** until the new DB has been built and validated:

1. Verify `<path>.import.tmp.db` and `<path>.import.tmp.jsonl` do not exist; if either does, fail with `cutover_in_progress` (operator inspection required — no auto-cleanup).
2. Open `<path>` read-only and run the exporter, writing to `<path>.import.tmp.jsonl`. Close the source.
3. Init a fresh DB at `<path>.import.tmp.db` from `0001_init.sql` (which now declares the binary's `currentSchemaVersion` schema).
4. Open `<path>.import.tmp.db` and run the importer against `<path>.import.tmp.jsonl`. Single transaction; FK enforcement on; `foreign_key_check` and `integrity_check` before commit. Apply the v1→v2 fill rules per §6.1.
5. **The atomic swap** (only after every step above succeeded):
   - Rename `<path>` → `<path>.bak.v<ver>.<ts>` (preserves the source).
   - Rename `<path>.import.tmp.db` → `<path>` (the freshly-imported DB becomes live).
   - Both renames are on the same filesystem, so each is atomic at the OS level. The window between the two is short and recoverable: if the second rename fails, the source backup is still on disk and the operator can rename it back.
6. Delete `<path>.import.tmp.jsonl`. Log `cutover succeeded; old DB preserved at <path>.bak.v<ver>.<ts>`.

On any failure before step 5: delete `<path>.import.tmp.jsonl` and `<path>.import.tmp.db` if they exist; the source `<path>` is untouched and the daemon exits non-zero with the underlying error.

On a failure inside step 5 (rare — both files are on the same FS): leave both `<path>.bak.v<ver>.<ts>` and any partial `<path>.import.tmp.db` on disk and exit with a hard error pointing the operator at the recovery procedure (`mv <path>.bak.v<ver>.<ts> <path>` to restore).

If `<path>.import.tmp.db` or `<path>.import.tmp.jsonl` exists at startup, the daemon refuses to start with `cutover_in_progress` (per step 1). This signals a prior crash mid-cutover; operator inspection picks the recovery path.

Operators who prefer manual control can run `kata export`, init a new DB, and `kata import` themselves. The daemon's auto-cutover is a convenience, not the only path.

## 7. CLI surface

### 7.1 `kata export`

```
kata export [--output PATH] [--project-id N] [--include-deleted]
```

- Default `--output` is `kata-export-<ts>.jsonl` in the current dir.
- `--project-id N` filters: emits only the named project, its aliases, its issues + descendants. Cross-project events / `purge_log` rows for that project are included; rows for other projects are not. Importer of a project-filtered export into a fresh DB succeeds; importing into a DB that already has data is rejected (see §7.2).
- `--include-deleted` is on by default — soft-deleted issues are exported. Add `--no-include-deleted` to skip them, in which case the exporter must also skip events / purge_log rows that reference the dropped issues (or it would produce a broken export). Default behavior is "everything," for safety.
- Refuses to run if a daemon is detected on the same DB path (PID-file check). Add `--allow-running-daemon` to override; the docs warn that the resulting export may be inconsistent if writes happen mid-export.
- Emits to the output file via a buffered writer. Flushes on each line. Closes and `fsync`s before exit.
- Exit codes per master spec §4.7. `0` on success; `2` on `validation`; `5` on `internal`.

### 7.2 `kata import`

```
kata import --input PATH [--target DBPATH] [--force]
```

- `--input` is required. Defaults to stdin if `-`.
- `--target DBPATH` defaults to `$KATA_HOME/kata.db`. The target file must not exist (or must be empty after init); add `--force` to overwrite an existing populated DB. With `--force`, the existing file is moved to `<target>.bak.<ts>`.
- Refuses if a daemon is running against the target.
- Validates the file header (`meta.export_version` present, value within supported range).
- Runs the entire import inside one transaction. The transaction's last two statements (before COMMIT) are `PRAGMA foreign_key_check` and `PRAGMA integrity_check`; any non-`ok` row from either aborts the transaction, the partial target file is deleted, and the import exits non-zero. Both checks run **before** commit so a bad temp DB rolls back cleanly and never reaches a state where it could be swapped into place.
- On any other error during import (malformed JSON, kind-order violation, FK violation caught by the in-transaction `foreign_key_check`): same rollback + delete + non-zero exit.
- Exit codes per master spec.

### 7.3 Tests of the CLI

Per §8.

## 8. Test plan

### 8.1 Round-trip property test (the load-bearing one)

`TestRoundtrip_LiveDB` (in `internal/jsonl`):

1. Take a fixture DB. Two cases: (a) the bundled "rich fixture" with curated rows covering every kind including soft-deletes and a purge_log row; (b) a copy of the test author's `$HOME/.kata/kata.db` if `KATA_TEST_LIVE_DB=1` is set in env (opt-in to avoid CI flakiness on machines without a live DB).
2. Export to a tmpfile.
3. Init a fresh DB from `0001_init.sql` in another tmpdir.
4. Import the tmpfile into the fresh DB.
5. Assert byte-equivalent round-trip:
   - Same row count per table (every table from §4.2).
   - For each row in each table (except FTS shadow tables): every column value equal between source and destination, ordered by primary key. Use `reflect.DeepEqual` after rows are scanned into a struct per table.
   - `sqlite_sequence` rows match exactly.
   - `meta` rows match exactly except `created_by_version` (which may legitimately advance).
   - FTS-driven search assertion: pick 5 random issue titles + 5 random comment bodies from the source, run `kata search` against both DBs, assert results are identical.
6. Re-export from the destination DB → second tmpfile. Assert the second export is byte-equal to the first (modulo `meta.exported_at`, which is filtered before comparison). This guarantees idempotency of the round-trip.

### 8.2 v1 → v2 cutover on the live DB

`TestCutover_V1ToV2_LiveDB` (gated on `KATA_TEST_LIVE_DB=1`):

1. Take `$HOME/.kata/kata.db` (or its copy). Confirm `meta.schema_version=1`.
2. Run `jsonl.AutoCutover` against a copy in tmpdir.
3. Assert post-cutover: `meta.schema_version=2`, every issue has a 26-char Crockford UID, every link's `from_issue_uid` resolves to the corresponding issue's UID via the integer FK, every event with non-null `issue_id` has an `issue_uid` matching the issue's UID.
4. Assert FTS works: `kata search` returns the same results as the v1 source for a sample of queries.
5. Assert `purge_log.issue_uid` is populated for any rows whose `purged_issue_id` still resolves; NULL otherwise.
6. Assert the `<path>.bak.v1.<ts>` file exists and is byte-identical to the source.
7. Re-run cutover on the post-cutover DB: it's a no-op (binary sees `schema_version=2`, target version is also `2`). No file moves, no temp file.

### 8.3 Failure paths

- Truncated input (file ends mid-record): import fails inside the transaction, rolls back, target file is removed.
- Corrupt JSON on a line: same.
- `meta.export_version` missing: fails before any insert with `missing_export_version`.
- `meta.export_version` greater than supported: fails with `export_too_new`.
- Out-of-order kind (e.g. `link` before `issue`): fails with `kind_order_violation` naming the offending line.
- `FK violation` (e.g., `comment` references a non-existent `issue`): caught by the in-transaction `PRAGMA foreign_key_check` before commit; transaction rolls back, temp DB deleted.
- `integrity_check` non-`ok` before commit: same — transaction rolls back, temp DB deleted, operator told to inspect the source export. A bad temp DB never reaches the swap step.
- Daemon running during export: rejected with `daemon_running` unless `--allow-running-daemon`.
- Daemon running during import: rejected unconditionally.
- `<path>.import.tmp.jsonl` or `<path>.import.tmp.db` exists at daemon startup: fails with `cutover_in_progress`, no auto-cleanup.

### 8.4 Determinism of v1→v2 fill

`TestCutover_V1ToV2_Deterministic`:

1. Fixed v1 fixture DB.
2. Cutover twice into separate tmpdirs.
3. Assert UIDs match exactly between the two destinations (confirms `uid.FromStableSeed(seed, t)` is deterministic given identical inputs — the seed is composed from the row's stable identifiers per identity spec §5.2; the timestamp is the row's `created_at`).

### 8.5 Synthetic-cursor preservation

`TestRoundtrip_PurgeReservedCursor`:

1. Fixture: a v2 DB with a `purge_log` row whose `purge_reset_after_event_id = N`, where `N > MAX(events.id)` (i.e., a real reservation).
2. Round-trip.
3. Assert `events.sqlite_sequence.seq >= N` in the destination.
4. Open a fake SSE handler against the destination with a cursor below `N`; assert it sees `sync.reset_required` with `id = N`.

### 8.6 Issue-label round-trip on the live DB

The live DB has 430 `issue_labels` rows distributed across issues. Round-trip test §8.1 covers them, but a focused assertion: for each `(issue_id, label)` pair in the source, the destination has the same pair.

### 8.7 Multi-alias project round-trip

The live DB has 4 projects + 5 aliases (one project has multiple aliases). Round-trip test asserts the alias multiplicity is preserved exactly.

### 8.8 Idempotency-key payload preservation

Many `issue.created` events in the live DB have `idempotency_key` and `idempotency_fingerprint` in their payload. Round-trip test asserts the payload JSON is byte-equal.

### 8.9 e2e

`TestSmoke_ExportImport`:

1. Start daemon, init project, create issues + comments + labels + links.
2. `kata export --output /tmp/x.jsonl`.
3. Stop daemon.
4. Init a fresh `$KATA_HOME` in a different tmpdir.
5. `kata import --input /tmp/x.jsonl --target $NEW/kata.db`.
6. Start daemon against the new path.
7. Assert `kata list --json` matches between original and new.

## 9. Open questions / tunables

- **Compression / encryption.** Out of scope. Noted as "operators pipe through `gzip` / `age`" in the docs.
- **Streaming progress.** A line counter on stderr is enough for v1. Reconsider when imports take measurable time.
- **Rolling support window K.** Default `K=1` (importer reads `current` and `current-1`). Reconsider when a v3 lands and we have a real two-step deprecation case to evaluate.
- **`--project-id` filter for export.** Leaves a question about cross-project links — those would dangle in a project-filtered export. v1 of the export filter rejects with `cross_project_link_in_filter` if any link spans the filter boundary; the current schema disallows cross-project links anyway (master spec §3.4 + 0001 triggers), so this branch is unreachable today but the importer asserts it for forward-safety.
- **Lockfile / PID-file location.** The daemon's PID-file lives at `$KATA_HOME/runtime/kata.pid` per master spec; the export/import CLI consults it to decide whether to refuse. If the file is stale (PID dead), the CLI proceeds with a warning. No new design here.
- **Encrypted-at-rest DB.** Not currently a thing in kata. If it ever is, the export logic stays the same — open the source DB with the appropriate key, walk rows.

## 10. Sequencing — where this lands

This spec is foundational for the issue-identity work tracked in `kata #?` (the issue-identity epic — tracked separately) and subsumes `kata #14` (backup/restore). Order:

1. **JSONL roundtrip (this spec) lands first.** It introduces the export / import / cutover infrastructure with no schema changes — `0001_init.sql` is unchanged in this step. The result is `kata export` and `kata import` work, and the daemon's first-boot cutover is wired but degrades to a no-op when the schema version matches.
2. **Issue identity lands second** (depending on #1). It updates `0001_init.sql` to include `uid` columns as `NOT NULL UNIQUE`, bumps `currentSchemaVersion` to `2`, and adds the v1→v2 fill rules from §6.1 to the importer. The cutover path is now real: a binary with `currentSchemaVersion=2` running against a v1 DB performs export → init → import → fill UIDs. Existing `kata #3` (Phase 2 readiness pass) keeps its scope but now consumes UIDs in its wire envelopes.
3. Subsequent issue-identity-style schema changes (e.g., the deferred `kata #6` schema rename for `project_aliases.root_path` → `last_seen_path`) follow the same pattern: bump schema version, declare the column rename in the importer, reuse the cutover path.

## 11. Implementation order (for the plan doc)

Hand off to `superpowers:writing-plans`. Expected ordering:

1. `internal/jsonl` package: encoder, decoder, kind enumeration, ordering enforcement.
2. Exporter: walks DB tables in §1.2 order, emits NDJSON. Tests against bundled fixture.
3. Importer: streams NDJSON, validates, executes inserts in one transaction. Tests against bundled fixture, including failure paths.
4. Round-trip property test (§8.1) on bundled fixture; opt-in test on live DB.
5. CLI: `kata export` + `kata import`.
6. Daemon `jsonl.AutoCutover` and the startup hook. e2e test that starts a daemon against a v(N-1) fixture and asserts the cutover lands and serves correctly.
7. Documentation: master spec gets a §X section noting that schema evolution flows through export/import; deferred kata #14 (backup/restore) is closed as "implemented by `kata export` + `kata import`."
8. The issue-identity work then lands as a follow-up that updates `0001_init.sql`, bumps `currentSchemaVersion`, and adds v1→v2 fill rules to the importer (per §6.1).
