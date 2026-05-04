# JSONL Roundtrip And Issue Identity Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build JSONL export/import as kata's schema-evolution path, then land stable UID identity for projects, issues, links, events, and purge audit records.

**Architecture:** Land the JSONL roundtrip infrastructure first while the schema is still v1, including the runner change that records `currentSchemaVersion` instead of filename-derived migration versions. Then update the canonical schema to v2, add deterministic v1-to-v2 importer fill rules, and expose UID fields through DB, API, CLI, and TUI surfaces. No in-place `0002` migration is introduced.

**Tech Stack:** Go, SQLite via `modernc.org/sqlite`, Huma HTTP handlers, Cobra CLI, Bubble Tea TUI, `oklog/ulid/v2`, NDJSON.

---

## Specs

- `docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md`
- `docs/superpowers/specs/2026-05-03-kata-issue-identity-design.md`
- `docs/superpowers/specs/2026-04-29-kata-design.md`

## Review Cadence

Run `roborev-fix` after every five implementation tasks, before proceeding to the next batch:

- After Task 5: run `roborev-fix`, test, commit any review fixes.
- After Task 10: run `roborev-fix`, test, commit any review fixes.
- After Task 15: run `roborev-fix`, test, commit any review fixes.

Use the `roborev-fix` skill at each checkpoint. If no open reviews exist, record that outcome in the checkpoint commit or task notes.

## File Structure

### New Packages

- `internal/jsonl/`
  - `types.go`: kind enum, envelope structs, schema-version constants local to import/export.
  - `encoder.go`: deterministic NDJSON writer.
  - `decoder.go`: line scanner, strict kind ordering, unknown-kind/unknown-field handling.
  - `export.go`: DB table walkers, source-schema-aware row emission.
  - `import.go`: fresh-DB importer, transaction, validation, sequence reconciliation.
  - `cutover.go`: daemon first-boot export/import temp-db swap.
  - `fixtures_test.go`: fixture setup helpers for v1 and v2 DBs.
  - `*_test.go`: encoder, decoder, importer, cutover, and failure-path tests.
- `internal/uid/`
  - `uid.go`: `New`, `FromTime`, `FromStableSeed`, `Valid`, `ValidPrefix`.
  - `uid_test.go`: deterministic, monotonic, validation, and collision tests.

### Modified DB Files

- `internal/db/db.go`: `currentSchemaVersion`, runner version recording, `PeekSchemaVersion`, init/open behavior.
- `internal/db/migrations/0001_init.sql`: remove `schema_version` seed during JSONL phase; add UID columns/indexes/triggers during identity phase.
- `internal/db/types.go`: add UID fields to `Project`, `Issue`, `Link`, `Event`, `PurgeLog`.
- `internal/db/queries.go`: project/issue writes and scans include UIDs.
- `internal/db/queries_links.go`: link writes/scans include endpoint UIDs.
- `internal/db/queries_events.go`: event writes/scans include UID columns.
- `internal/db/queries_delete.go`: purge log captures `issue_uid` and `project_uid`.
- `internal/db/schema_completeness_test.go`: v2 schema invariant coverage.

### Modified API/Daemon Files

- `internal/api/types.go`: request/response DTOs include UID fields and UID lookup request.
- `internal/api/events.go`: `EventEnvelope` includes `project_uid`, `issue_uid`, `related_issue_uid`.
- `internal/daemon/server.go`: startup cutover hook before `db.Open` in daemon command path, plus route registration for UID lookup.
- `internal/daemon/handlers_issues.go`: create/show/list/UID lookup emit UID-bearing envelopes.
- `internal/daemon/handlers_links.go`: link requests accept number or UID refs where applicable.
- `internal/daemon/handlers_events.go`: event envelopes include UID fields.
- `internal/daemon/handlers_destructive.go`: purge response carries UID-bearing purge log.

### Modified CLI/TUI Files

- `cmd/kata/main.go`: register `export` and `import`.
- `cmd/kata/export.go`: `kata export`.
- `cmd/kata/import.go`: `kata import`.
- `cmd/kata/show.go`: accept issue number, full UID, or UID prefix.
- `cmd/kata/link.go`: accept UID refs in link-related commands.
- `cmd/kata/events.go`: JSON event output includes UID fields.
- `internal/tui/client_types.go`: UID fields in issue/link/event types.
- `internal/tui/client.go`: parse UID-bearing responses.
- `internal/tui/detail*.go`: footer UID display.
- `internal/tui/sse*.go`: parse UID-bearing events.

### Spec/Docs Files

- `docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md`: fix final source-shape wording before code.
- `docs/superpowers/specs/2026-05-03-kata-issue-identity-design.md`: keep aligned if implementation reveals drift.

---

## Phase 1: JSONL Roundtrip Infrastructure

### Task 1: Final Spec Cleanup

**Files:**
- Modify: `docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md`

- [ ] **Step 1: Fix the stale source-shape wording**

Change the scope bullet that says the exporter "emits the most recent format" to source-shape wording:

```markdown
- The "exporter is schema-version-aware" capability needed to drop `<N> - K` versions out of compat: the exporter knows about every supported source schema version and emits that source version's canonical JSONL shape. Upgrade fills are importer-owned.
```

- [ ] **Step 2: Search for contradictory wording**

Run:

```bash
rg -n "most recent format|newest format|exporter.*synthesizes|exporter.*N\\+1|after commit|restored first|at the top" docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md
```

Expected: no matches except intentional historical text if clearly marked as removed/previous.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-05-03-kata-jsonl-roundtrip-design.md
git commit -m "docs: align JSONL export version wording"
```

### Task 2: Schema Version Runner Change

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/migrations/0001_init.sql`
- Test: `internal/db/db_test.go`
- Test: `cmd/kata/diagnostic_test.go`

- [ ] **Step 1: Write failing DB tests**

Add tests that assert fresh init writes `schema_version` from a Go constant rather than the migration filename:

```go
func TestOpen_RecordsCurrentSchemaVersion(t *testing.T) {
    ctx := context.Background()
    path := filepath.Join(t.TempDir(), "kata.db")
    d, err := db.Open(ctx, path)
    require.NoError(t, err)
    defer d.Close()

    var got string
    require.NoError(t, d.QueryRowContext(ctx,
        `SELECT value FROM meta WHERE key='schema_version'`).Scan(&got))
    require.Equal(t, strconv.Itoa(db.CurrentSchemaVersion()), got)
}
```

- [ ] **Step 2: Run the focused test and confirm failure**

Run:

```bash
go test ./internal/db -run TestOpen_RecordsCurrentSchemaVersion -count=1
```

Expected: fail before implementation because no exported/current version helper exists or because the SQL still seeds filename-derived version.

- [ ] **Step 3: Implement `currentSchemaVersion`**

In `internal/db/db.go`, add:

```go
const currentSchemaVersion = 1

func CurrentSchemaVersion() int { return currentSchemaVersion }
```

Update `migrate` so after executing SQL for an applied migration it records `currentSchemaVersion`, not the parsed filename version. For v1 this still writes `1`; the behavior change becomes visible when v2 lands.

- [ ] **Step 4: Remove SQL seeding of `schema_version`**

In `internal/db/migrations/0001_init.sql`, remove:

```sql
INSERT INTO meta(key, value) VALUES ('schema_version', '1');
```

Keep:

```sql
INSERT INTO meta(key, value) VALUES ('created_by_version', '0.1.0');
```

- [ ] **Step 5: Update diagnostics expectation**

Where tests assert human diagnostic output contains `schema_version=1`, keep the assertion but source it from `db.CurrentSchemaVersion()` if useful.

- [ ] **Step 6: Run focused tests**

Run:

```bash
go test ./internal/db ./cmd/kata -run 'TestOpen_RecordsCurrentSchemaVersion|TestDiagnostic' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/db/db.go internal/db/migrations/0001_init.sql internal/db/db_test.go cmd/kata/diagnostic_test.go
git commit -m "db: record schema version from binary constant"
```

### Task 3: JSONL Envelope Encoder/Decoder

**Files:**
- Create: `internal/jsonl/types.go`
- Create: `internal/jsonl/encoder.go`
- Create: `internal/jsonl/decoder.go`
- Test: `internal/jsonl/decoder_test.go`

- [ ] **Step 1: Write failing decoder tests**

Cover:

- first record must be `meta/export_version`
- unknown kind fails
- out-of-order kind fails
- unknown field on known kind warns/ignores
- invalid JSON includes line number

Example:

```go
func TestDecoderRejectsOutOfOrderKind(t *testing.T) {
    input := strings.NewReader(
        `{"kind":"meta","data":{"key":"export_version","value":"1"}}` + "\n" +
        `{"kind":"link","data":{"id":1}}` + "\n" +
        `{"kind":"issue","data":{"id":1}}` + "\n",
    )
    dec := jsonl.NewDecoder(input)
    _, err := dec.ReadAll(context.Background())
    require.ErrorIs(t, err, jsonl.ErrKindOrderViolation)
}
```

- [ ] **Step 2: Run focused test and confirm failure**

```bash
go test ./internal/jsonl -run TestDecoder -count=1
```

Expected: fail because package does not exist.

- [ ] **Step 3: Implement types and ordering**

Use fixed kind order:

```go
var kindOrder = map[Kind]int{
    KindMeta: 0, KindProject: 1, KindProjectAlias: 2, KindIssue: 3,
    KindComment: 4, KindIssueLabel: 5, KindLink: 6, KindEvent: 7,
    KindPurgeLog: 8, KindSQLiteSequence: 9,
}
```

Keep data as `json.RawMessage` at this layer. Table-specific validation belongs in importer/exporter tasks.

- [ ] **Step 4: Implement deterministic encoder**

The encoder writes one compact JSON object per line:

```go
type Envelope struct {
    Kind Kind            `json:"kind"`
    Data json.RawMessage `json:"data"`
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/jsonl -run 'TestDecoder|TestEncoder' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jsonl
git commit -m "jsonl: add ordered envelope codec"
```

### Task 4: JSONL Exporter

**Files:**
- Create: `internal/jsonl/export.go`
- Test: `internal/jsonl/export_test.go`
- Modify: `internal/db/db.go` if a read-only open helper is needed.

- [ ] **Step 1: Write failing export tests**

Use a small fixture DB created through `db.Open`, `CreateProject`, `CreateIssue`, labels, comments, links, events, and purge log where possible. Assert:

- first record is `meta/export_version`
- kind order matches spec
- records within each kind are ordered by primary key
- `events.payload` is emitted as JSON, not a string
- `sqlite_sequence` records are last

- [ ] **Step 2: Run test and confirm failure**

```bash
go test ./internal/jsonl -run TestExport -count=1
```

Expected: fail because exporter is unimplemented.

- [ ] **Step 3: Implement `Export`**

Suggested public API:

```go
type ExportOptions struct {
    ProjectID      int64
    IncludeDeleted bool
}

func Export(ctx context.Context, d *db.DB, w io.Writer, opts ExportOptions) error
```

Use explicit SELECT lists per table. Do not export FTS tables or `sqlite_master`.

- [ ] **Step 4: Preserve JSON payload values**

When writing `events`, parse `payload` into `json.RawMessage` and insert it into the row struct as a JSON value:

```go
type eventRecord struct {
    ID int64 `json:"id"`
    Payload json.RawMessage `json:"payload"`
}
```

- [ ] **Step 5: Run focused tests**

```bash
go test ./internal/jsonl -run TestExport -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jsonl/export.go internal/jsonl/export_test.go
git commit -m "jsonl: export database state"
```

### Task 5: JSONL Importer

**Files:**
- Create: `internal/jsonl/import.go`
- Test: `internal/jsonl/import_test.go`

- [ ] **Step 1: Write failing import tests**

Cover:

- import into fresh v1 DB preserves row IDs and values
- `sqlite_sequence` uses update-or-insert, not `INSERT OR REPLACE`
- `foreign_key_check` and `integrity_check` run before commit
- corrupt JSON and FK violations delete the partial target DB

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/jsonl -run TestImport -count=1
```

Expected: fail because importer is unimplemented.

- [ ] **Step 3: Implement import API**

Suggested public API:

```go
type ImportOptions struct {
    Force bool
}

func ImportFile(ctx context.Context, inputPath, targetPath string, opts ImportOptions) error
func Import(ctx context.Context, r io.Reader, target *db.DB) error
```

For tests, it is acceptable to expose a lower-level transaction importer that accepts `*sql.Tx`.

- [ ] **Step 4: Insert records with explicit columns**

Do not build SQL from arbitrary JSON fields. Decode each known kind into a typed struct and use a fixed insert statement.

- [ ] **Step 5: Apply `sqlite_sequence` last**

Use update-then-insert:

```sql
UPDATE sqlite_sequence SET seq = ? WHERE name = ?;
-- if rows affected == 0:
INSERT INTO sqlite_sequence(name, seq) VALUES (?, ?);
```

Then reconcile to `MAX(stored_seq, current MAX(id))` for each AUTOINCREMENT table.

- [ ] **Step 6: Run validation before commit**

Inside the transaction, run:

```sql
PRAGMA foreign_key_check;
PRAGMA integrity_check;
```

Any returned row from `foreign_key_check`, or any `integrity_check` value other than `ok`, aborts the transaction.

- [ ] **Step 7: Run focused tests**

```bash
go test ./internal/jsonl -run TestImport -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/jsonl/import.go internal/jsonl/import_test.go
git commit -m "jsonl: import database state"
```

### Roborev Checkpoint A: After Task 5

**Skill:** `roborev-fix`

- [ ] **Step 1: Discover and fix open reviews**

Run:

```bash
roborev fix --open --list
```

If open failing reviews exist, follow the `roborev-fix` skill:

```bash
roborev show --job <job_id> --json
# fix findings
go test ./...
roborev comment --commenter roborev-fix --job <job_id> "<summary>"
roborev close <job_id>
```

- [ ] **Step 2: Commit review fixes if any**

```bash
git add <fixed-files>
git commit -m "fix: address roborev findings after JSONL importer"
```

If no open findings exist, do not create an empty commit. Record "no open roborev findings" in task notes.

### Task 6: JSONL Roundtrip Property Tests

**Files:**
- Test: `internal/jsonl/roundtrip_test.go`
- Test helper: `internal/jsonl/fixtures_test.go`

- [ ] **Step 1: Add rich fixture builder**

Build a fixture with:

- multiple projects and aliases
- issues with labels, comments, links
- at least one soft-deleted issue
- at least one purge log row with a reserved event cursor

- [ ] **Step 2: Write roundtrip test**

Test export -> fresh DB -> import -> re-export. Assert:

- row counts match
- typed row values match
- `sqlite_sequence` rows match
- FTS search results match for sampled title/comment terms
- second export equals first after filtering `meta.exported_at`

- [ ] **Step 3: Run focused test**

```bash
go test ./internal/jsonl -run TestRoundtrip -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/jsonl/roundtrip_test.go internal/jsonl/fixtures_test.go
git commit -m "jsonl: verify full database roundtrip"
```

### Task 7: JSONL CLI Commands

**Files:**
- Create: `cmd/kata/export.go`
- Create: `cmd/kata/import.go`
- Modify: `cmd/kata/main.go`
- Test: `cmd/kata/export_test.go`
- Test: `cmd/kata/import_test.go`

- [ ] **Step 1: Write CLI tests**

Cover:

- `kata export --output path` writes NDJSON
- `kata import --input path --target db` creates a DB
- import refuses existing populated target without `--force`
- export refuses running daemon unless `--allow-running-daemon`

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./cmd/kata -run 'TestExport|TestImport' -count=1
```

Expected: fail because commands are not registered.

- [ ] **Step 3: Implement commands**

Register in `newRootCmd()`:

```go
newExportCmd(),
newImportCmd(),
```

Use the existing CLI error style (`cliError`, `kindValidation`, `ExitValidation`).

- [ ] **Step 4: Run focused tests**

```bash
go test ./cmd/kata -run 'TestExport|TestImport' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/kata/export.go cmd/kata/import.go cmd/kata/main.go cmd/kata/export_test.go cmd/kata/import_test.go
git commit -m "cli: add JSONL export and import"
```

### Task 8: AutoCutover

**Files:**
- Create: `internal/jsonl/cutover.go`
- Test: `internal/jsonl/cutover_test.go`
- Modify: daemon startup command path in `cmd/kata/daemon_cmd.go` or the code path that opens the daemon DB.

- [ ] **Step 1: Write failing cutover tests**

Cover:

- no-op when source schema equals `db.CurrentSchemaVersion()`
- temp files cause `cutover_in_progress`
- source DB untouched until temp DB validates
- backup is byte-identical to source after successful cutover

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/jsonl ./cmd/kata -run 'TestAutoCutover|TestDaemon.*Cutover' -count=1
```

Expected: fail because cutover is unimplemented.

- [ ] **Step 3: Implement `PeekSchemaVersion`**

Add to `internal/db/db.go`:

```go
func PeekSchemaVersion(ctx context.Context, path string) (int, error)
```

It opens read-only, reads `meta.schema_version`, returns `0` for a DB without `meta`, and does not run migrations.

- [ ] **Step 4: Implement `AutoCutover` temp-db swap**

Follow spec:

1. fail if `<path>.import.tmp.db` or `<path>.import.tmp.jsonl` exists
2. export source read-only to temp JSONL
3. init temp DB at current schema
4. import and validate temp DB
5. rename source to backup
6. rename temp DB to source path

- [ ] **Step 5: Wire daemon startup**

Before the daemon opens the DB for serving:

```go
ver, err := db.PeekSchemaVersion(ctx, path)
if err == nil && ver < db.CurrentSchemaVersion() {
    err = jsonl.AutoCutover(ctx, path)
}
```

- [ ] **Step 6: Run focused tests**

```bash
go test ./internal/jsonl ./cmd/kata -run 'TestAutoCutover|TestDaemon.*Cutover' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jsonl/cutover.go internal/jsonl/cutover_test.go internal/db/db.go cmd/kata/daemon_cmd.go
git commit -m "jsonl: cut over old schemas through import"
```

### Task 9: JSONL Failure Path Coverage

**Files:**
- Test: `internal/jsonl/failure_test.go`
- Test: `cmd/kata/import_test.go`

- [ ] **Step 1: Add failure tests from spec**

Cover:

- truncated input
- corrupt JSON
- missing `meta.export_version`
- `export_too_new`
- kind order violation
- FK violation
- integrity failure if practical
- daemon running during import

- [ ] **Step 2: Run focused tests**

```bash
go test ./internal/jsonl ./cmd/kata -run 'TestImport.*Failure|TestDecoder.*Failure|TestImportRefusesDaemon' -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/jsonl/failure_test.go cmd/kata/import_test.go
git commit -m "jsonl: cover import failure paths"
```

### Task 10: JSONL E2E Smoke

**Files:**
- Test: `cmd/kata/export_test.go`
- Test: `cmd/kata/import_test.go`
- Test: `internal/jsonl/roundtrip_test.go`

- [ ] **Step 1: Add daemon-backed smoke**

Use existing CLI test helpers to:

1. start daemon
2. init project
3. create issues, comment, label, link
4. export
5. stop daemon
6. import into new temp home
7. start daemon on imported DB
8. compare `kata list --json`

- [ ] **Step 2: Run smoke**

```bash
go test ./cmd/kata -run TestSmoke_ExportImport -count=1
```

Expected: PASS.

- [ ] **Step 3: Run JSONL package tests**

```bash
go test ./internal/jsonl -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/kata/export_test.go cmd/kata/import_test.go internal/jsonl
git commit -m "test: add JSONL export import smoke"
```

### Roborev Checkpoint B: After Task 10

**Skill:** `roborev-fix`

- [ ] **Step 1: Run roborev cleanup**

```bash
roborev fix --open --list
```

If jobs exist, fetch, fix, test, comment, and close per `roborev-fix`.

- [ ] **Step 2: Verify**

```bash
go test ./...
```

Expected: PASS before continuing.

- [ ] **Step 3: Commit fixes if any**

```bash
git add <fixed-files>
git commit -m "fix: address roborev findings after JSONL roundtrip"
```

---

## Phase 2: Stable UID Identity

### Task 11: UID Package

**Files:**
- Create: `internal/uid/uid.go`
- Test: `internal/uid/uid_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Write failing UID tests**

Cover:

- `New` returns valid 26-char ULID
- 100k `New` calls unique and monotonic in a tight loop
- `FromTime` encodes timestamp and uses non-deterministic entropy
- `FromStableSeed` is byte-equivalent for same seed/time
- `Valid` and `ValidPrefix` reject bad lengths/chars/overflow

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/uid -count=1
```

Expected: fail because package does not exist.

- [ ] **Step 3: Add dependency**

```bash
go get github.com/oklog/ulid/v2
```

- [ ] **Step 4: Implement package**

`FromStableSeed` uses ULID timestamp from `t` and entropy bytes `SHA-256(seed)[:10]`.

- [ ] **Step 5: Run focused tests**

```bash
go test ./internal/uid -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/uid go.mod go.sum
git commit -m "uid: add ULID helpers"
```

### Task 12: V2 Schema Declaration

**Files:**
- Modify: `internal/db/migrations/0001_init.sql`
- Modify: `internal/db/db.go`
- Modify: `internal/db/schema_completeness_test.go`
- Test: `internal/db/db_test.go`

- [x] **Step 1: Write failing schema tests**

Assert fresh DB has:

- `projects.uid TEXT NOT NULL UNIQUE`
- `issues.uid TEXT NOT NULL UNIQUE`
- link UID columns
- event/purge UID columns
- UID indexes
- link UID/FK consistency triggers
- `meta.schema_version == "2"`

- [x] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/db -run 'TestSchema|TestOpen_RecordsCurrentSchemaVersion' -count=1
```

Expected: fail on missing columns/triggers/version.

- [x] **Step 3: Edit schema**

Add columns/indexes/triggers exactly per identity spec. Do not add a `meta.schema_version` seed insert.

- [x] **Step 4: Bump version constant**

In `internal/db/db.go`:

```go
const currentSchemaVersion = 2
```

- [x] **Step 5: Run focused tests**

```bash
go test ./internal/db -run 'TestSchema|TestOpen_RecordsCurrentSchemaVersion' -count=1
```

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/db/migrations/0001_init.sql internal/db/db.go internal/db/schema_completeness_test.go internal/db/db_test.go
git commit -m "db: declare issue identity schema"
```

### Task 13: DB Types And Queries Write UIDs

**Files:**
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Modify: `internal/db/queries_links.go`
- Modify: `internal/db/queries_events.go`
- Modify: `internal/db/queries_delete.go`
- Test: `internal/db/queries_*_test.go`

- [x] **Step 1: Write failing DB behavior tests**

Cover:

- `CreateProject` returns `Project.UID`
- `CreateIssue` returns `Issue.UID`
- link insert stores both UID columns matching integer FKs
- event insert stores project/issue/related UID fields
- purge log captures issue/project UIDs before deletion
- direct mismatched link insert/update fails via trigger

- [x] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/db -run 'UID|CreateProject|CreateIssue|CreateLink|Purge' -count=1
```

Expected: fail on missing fields/write paths.

- [x] **Step 3: Update types and scan helpers**

Add fields:

```go
UID string `json:"uid"`
ProjectUID string `json:"project_uid,omitempty"`
IssueUID *string `json:"issue_uid,omitempty"`
RelatedIssueUID *string `json:"related_issue_uid,omitempty"`
```

Use concrete string fields for NOT NULL columns and pointers for nullable columns.

- [x] **Step 4: Update insert paths**

Generate `uid.New()` for new projects and issues. Resolve issue UIDs when inserting links and events.

- [x] **Step 5: Update purge**

Read `issues.uid` and `projects.uid` before cascade deletion and insert them into `purge_log`.

- [x] **Step 6: Run focused tests**

```bash
go test ./internal/db -run 'UID|CreateProject|CreateIssue|CreateLink|Purge' -count=1
```

Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add internal/db
git commit -m "db: write and read stable UIDs"
```

### Task 14: JSONL V1-to-V2 Fill Rules

**Files:**
- Modify: `internal/jsonl/import.go`
- Test: `internal/jsonl/cutover_test.go`
- Test: `internal/jsonl/roundtrip_test.go`

- [x] **Step 1: Write failing v1 fixture tests**

Create a v1 fixture DB using an embedded v1 schema copy in tests or a helper that creates the old tables. Assert after `AutoCutover`:

- projects/issues have deterministic valid UIDs
- links resolve endpoint UIDs
- events resolve issue UIDs
- purge log has `project_uid`, and `issue_uid` only when issue still exists
- running cutover twice from same source produces identical UIDs

- [x] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/jsonl -run 'TestCutover_V1ToV2|TestCutover_V1ToV2_Deterministic' -count=1
```

Expected: fail because fill rules are missing.

- [x] **Step 3: Implement fill rules**

Use:

```go
projectUID := uid.FromStableSeed(
    []byte(fmt.Sprintf("project:%d:%s", project.ID, project.Identity)),
    project.CreatedAt,
)
issueUID := uid.FromStableSeed(
    []byte(fmt.Sprintf("issue:%d:%d", issue.ProjectID, issue.Number)),
    issue.CreatedAt,
)
```

Build in-memory maps by source integer ID during import.

- [x] **Step 4: Reject corrupt event FKs**

If `events.issue_id` or `related_issue_id` is non-null and missing from the issue UID map, return `corrupt_event_fk` with event ID and column name.

- [x] **Step 5: Run tests**

```bash
go test ./internal/jsonl -run 'TestCutover_V1ToV2|TestCutover_V1ToV2_Deterministic|TestRoundtrip' -count=1
```

Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/jsonl
git commit -m "jsonl: fill UIDs during v1 cutover"
```

### Task 15: API And Daemon UID Wire Shape

**Files:**
- Modify: `internal/api/types.go`
- Modify: `internal/api/events.go`
- Modify: `internal/daemon/handlers_issues.go`
- Modify: `internal/daemon/handlers_links.go`
- Modify: `internal/daemon/handlers_events.go`
- Modify: `internal/daemon/handlers_destructive.go`
- Test: `internal/daemon/handlers_*_test.go`

- [ ] **Step 1: Write failing handler tests**

Cover:

- create issue response includes `uid` and `project_uid`
- show/list issue include UIDs
- link response includes endpoint UIDs
- event poll/SSE includes `project_uid`, `issue_uid`, `related_issue_uid`
- purge response includes `purge_log.issue_uid` and `project_uid`
- `GET /api/v1/issues/{uid}` returns issue or `issue_not_found`

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/daemon -run 'UID|IssueByUID|Event.*UID|Purge.*UID' -count=1
```

Expected: fail until wire shape is updated.

- [ ] **Step 3: Add API DTO fields**

Update `EventEnvelope`:

```go
ProjectUID string  `json:"project_uid"`
IssueUID *string `json:"issue_uid,omitempty"`
RelatedIssueUID *string `json:"related_issue_uid,omitempty"`
```

- [ ] **Step 4: Implement UID lookup endpoint**

Add route:

```text
GET /api/v1/issues/{uid}
```

Validate ULID syntax before DB lookup.

- [ ] **Step 5: Update event envelope builders**

Every path that converts `db.Event` to `api.EventEnvelope` must carry UID fields.

- [ ] **Step 6: Run focused tests**

```bash
go test ./internal/daemon -run 'UID|IssueByUID|Event.*UID|Purge.*UID' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api internal/daemon
git commit -m "api: expose stable UIDs"
```

### Roborev Checkpoint C: After Task 15

**Skill:** `roborev-fix`

- [ ] **Step 1: Run roborev cleanup**

```bash
roborev fix --open --list
```

If findings exist, fetch and fix them per `roborev-fix`.

- [ ] **Step 2: Verify**

```bash
go test ./...
```

Expected: PASS before continuing.

- [ ] **Step 3: Commit fixes if any**

```bash
git add <fixed-files>
git commit -m "fix: address roborev findings after UID API"
```

### Task 16: CLI UID References

**Files:**
- Modify: `cmd/kata/show.go`
- Modify: `cmd/kata/link.go`
- Modify: `cmd/kata/events.go`
- Modify: `cmd/kata/client.go`
- Test: `cmd/kata/show_test.go`
- Test: `cmd/kata/link_test.go`
- Test: `cmd/kata/events_test.go`

- [ ] **Step 1: Write failing CLI tests**

Cover:

- `kata show #42` and `kata show 42` still work
- `kata show <full_uid>` works
- `kata show <8_char_prefix>` works when unique
- short prefix fails with `prefix_too_short`
- ambiguous prefix fails with candidate list
- link commands accept UID refs
- `kata events --tail --json` includes UID fields

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./cmd/kata -run 'TestShow.*UID|TestLink.*UID|TestEvents.*UID' -count=1
```

Expected: fail until ref parsing is implemented.

- [ ] **Step 3: Implement issue ref resolver**

Add a helper:

```go
type issueRefKind int

func resolveIssueRef(ctx context.Context, baseURL string, projectID int64, ref string) (number int64, uid string, err error)
```

Rules:

- `#N` and `N` resolve by project/number
- valid UID or prefix routes through UID lookup/prefix lookup
- min prefix length is 8
- ambiguous prefix returns validation error with candidates

- [ ] **Step 4: Update commands**

Use the resolver in show and link-family commands. Keep human display as `#N`; UID appears in `--json`.

- [ ] **Step 5: Run focused tests**

```bash
go test ./cmd/kata -run 'TestShow.*UID|TestLink.*UID|TestEvents.*UID' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata
git commit -m "cli: resolve issue refs by UID"
```

### Task 17: TUI UID Surface

**Files:**
- Modify: `internal/tui/client_types.go`
- Modify: `internal/tui/client.go`
- Modify: `internal/tui/detail_render.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/events_sse_parse.go`
- Test: `internal/tui/*_test.go`
- Test: `internal/tui/testdata/golden/*`

- [ ] **Step 1: Write failing TUI tests**

Cover:

- client parses issue/link/event UID fields
- detail footer renders no UID by default
- `display.uid_format=short` renders `~<8 chars>`
- `display.uid_format=full` renders full UID
- compact issue sheet remains unchanged
- SSE update parsing preserves UID fields

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
go test ./internal/tui -run 'UID|Detail|SSE' -count=1
```

Expected: fail until types/rendering are updated.

- [ ] **Step 3: Update client types**

Add UID fields to issue/link/event structs. Avoid changing existing JSON field names.

- [ ] **Step 4: Add display config support**

If config plumbing does not yet exist for `display.uid_format`, add the minimal enum/setting needed by the TUI. Default `none`.

- [ ] **Step 5: Update detail footer only**

Do not put UID in list rows or compact sheet. Render it in the detail metadata/footer area.

- [ ] **Step 6: Update goldens**

Run the project’s golden update flow if one exists; otherwise update expected files manually after inspecting output.

- [ ] **Step 7: Run focused tests**

```bash
go test ./internal/tui -run 'UID|Detail|SSE' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/tui
git commit -m "tui: display issue UIDs in detail footer"
```

### Task 18: Full Verification And Docs

**Files:**
- Modify: `docs/superpowers/specs/2026-04-29-kata-design.md`
- Modify: any README/help docs that list CLI commands.
- Test: full repo.

- [ ] **Step 1: Update master spec**

Document:

- JSONL export/import is schema evolution path
- `uid` is authoritative identity
- `#N` is display label
- wire shapes include UID fields

- [ ] **Step 2: Run full test suite**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Optional live DB validation**

Only if safe on a copied DB:

```bash
KATA_TEST_LIVE_DB=1 go test ./internal/jsonl -run 'TestRoundtrip_LiveDB|TestCutover_V1ToV2_LiveDB' -count=1
```

Expected: PASS or skip when no live DB fixture is configured.

- [ ] **Step 4: Check worktree**

```bash
git status --short
```

Expected: only intentional doc/test changes staged for final commit.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-04-29-kata-design.md
git commit -m "docs: document JSONL cutover and issue UIDs"
```

### Task 19: Final Roborev Cleanup

**Skill:** `roborev-fix`

- [ ] **Step 1: Run one final roborev cleanup**

```bash
roborev fix --open --list
```

If findings exist, follow `roborev-fix`: fetch, fix, test, comment, close.

- [ ] **Step 2: Run full suite after any fixes**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Commit final fixes if any**

```bash
git add <fixed-files>
git commit -m "fix: address final roborev findings"
```

---

## Handoff Notes

- Do not implement issue identity before JSONL roundtrip and the runner version change are merged.
- Do not add `0002_uids.sql`; schema evolution goes through JSONL cutover.
- Do not seed `meta.schema_version` in `0001_init.sql`; the runner writes it from `currentSchemaVersion`.
- The exporter emits source schema shape. The importer owns upgrades.
- Use `uid.FromStableSeed`, not `uid.FromTime`, for v1-to-v2 deterministic fill.
- Preserve `sqlite_sequence` exactly, especially `events` reserved purge cursors.
- Commit after each task. Do not leave accepted changes uncommitted.
