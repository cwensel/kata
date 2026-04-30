# Plan 3 — Search + Idempotency + Soft-Delete/Purge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the kata daemon + CLI for content search (`kata search`), idempotent creation (`--idempotency-key`), look-alike soft-block at create time, and the soft-delete / restore / purge ladder — all the dedup + destructive machinery that Plan 2 left as stubs.

**Architecture:** Plan 1 already declared the `issues_fts` virtual table, the `idx_events_idempotency` index, and the `purge_log` table. Plan 3 makes them real. The FTS sync triggers on `issues` and `comments` are appended to the existing `0001_init.sql` (no new migration file — see "Schema-edit policy" below). A new `internal/similarity/` package provides Canonical (NFC + trim + collapse), Tokenize, Jaccard, and the weighted Score used both at create-time look-alike check and by `kata search`. Idempotency lives in `internal/db/queries_idempotency.go`: `Fingerprint()` is a pure function over the canonical (title, body, owner, labels, links) tuple; `LookupIdempotency()` reads the `events` table. Soft-delete / restore / purge live in `internal/db/queries_delete.go`; purge runs the multi-step transaction from spec §3.5 (capture min/max event ids → cascade-delete dependents → bump `sqlite_sequence.seq` for `events` → write `purge_log` → delete `issues` row). No SSE consumer yet; the reserved cursor sits in the row for Plan 4 to consume.

**Schema-edit policy:** kata is pre-release; Wes is the only user. Until v1 ships, schema changes go **directly into `0001_init.sql`**, not into new migration files. The migrations runner (`internal/db/db.go`) and the `meta.schema_version='1'` row stay in place — they'll start earning their keep once kata is shipping — but Plan 3 doesn't add `0002_*.sql`. During development, recreate the local DB as needed (`trash $KATA_HOME/kata.db` between runs).

**Tech Stack:** Same as Plans 1+2. No new dependencies — `crypto/sha256`, `golang.org/x/text/unicode/norm` (already in module graph if any dep pulled it in; otherwise add it explicitly in Task 2).

**Reference spec:** `docs/superpowers/specs/2026-04-29-kata-design.md`. Plan 3 covers §3.3 (event types `issue.soft_deleted`, `issue.restored`, plus the `idempotency_key`/`idempotency_fingerprint` fields in `issue.created` payload), §3.4 (lifecycle entries for delete/restore/purge), §3.5 (destructive ladder), §3.6 (idempotency), §3.7 (look-alike soft-block), §4.1 (search/delete/restore/purge endpoints), §4.4 (`Idempotency-Key`, `X-Kata-Confirm` headers), §4.5 (mutation envelope with `reused:true` and `original_event` in idempotent reuse), §4.7 (status code → exit code mapping for the new error codes), §4.10 (search response shape), §6.1 (`kata search`, `kata delete`, `kata restore`, `kata purge`, plus `kata create --idempotency-key`/`--force-new`).

**Out of scope for Plan 3 (deferred to later plans):** SSE consumer of `purge_reset_after_event_id` (Plan 4), event polling with `reset_required` (Plan 4), hooks (Plan 5), TUI (Plan 6), skills/doctor/`kata agent-instructions` (Plan 7), `kata config get/set` for the `similarity_threshold` / `idempotency_window` knobs (Plan 7 — Plan 3 hard-codes both at the spec defaults). `--all-projects` and cross-project search are also Plan 4 territory; Plan 3's `kata search` is project-scoped only.

**Spec deviations declared up front:**
- Plan 1 created `issues_fts` with `content=''`. With contentless FTS5 each delete has to provide the previously indexed column values. The triggers in Task 1 implement this by re-aggregating from the comments table at the moment the trigger fires; this works because every change to title, body, or comments goes through a trigger that keeps FTS in sync. If the implementer finds the contentless approach unworkable in practice, switching to `content='issues'` external content (with a sibling `comments_fts` table) is a documented escape hatch — but try the spec's shape first.
- The "Plan 3 migration" referenced in spec §3.2 ("kept in sync via triggers in Plan 3") is **not** a separate migration file. Per the schema-edit policy above, the triggers are appended to `0001_init.sql`.
- Spec §3.6 specifies a "default lookback window 7 days (configurable)" for idempotency. Plan 3 hard-codes 7 days; the configuration surface lands in Plan 7 alongside the rest of `kata config`.
- Spec §3.7 specifies similarity threshold 0.7 (configurable). Plan 3 hard-codes 0.7 with a `const` at the daemon side.
- Spec §6.1 lists `kata create --idempotency-key K` as a flag; the wire transport is the `Idempotency-Key` HTTP header (spec §4.4). The CLI flag maps to the header — the request body never carries the key.

**Conventions for every task** — same as Plans 1+2:

- TDD: write the failing test first, run it to confirm it fails, implement, run to confirm pass, commit.
- Use `testify/require` for setup/preconditions and `testify/assert` for non-blocking checks; never `t.Fatal`/`t.Error` directly.
- Table-driven tests where multiple cases exist.
- `t.TempDir()` for any filesystem state. Never write to `~/.kata` from tests.
- Tests run with `-shuffle=on`; never pass `-count=1`; never pass `-v` unless asked.
- CLI tests must call `resetFlags(t)` (in `cmd/kata/testhelpers_test.go`) before constructing root commands.
- Commit messages: conventional (`feat:`, `fix:`, `chore:`, `test:`); subject ≤72 chars; one logical change per commit. Co-author trailer encouraged.
- Pre-commit hook (`prek`) runs `make lint`. Run `make lint` locally before committing.
- Never amend commits; always create new ones for fixes.
- Tests must hit `make test`. Don't run `go test -v` or `-count=1`.

**Plans 1+2 surface to reuse (not redefine):**

- `internal/api/types.go` — `MutationResponse{Issue, Event, Changed, Reused}`. Plan 3 extends it (adds `OriginalEvent`); does **not** rename existing fields.
- `internal/api/errors.go` — `api.NewError(status, code, message, hint, data)`.
- `internal/db/types.go` — `Issue`, `Project`, `Comment`, `Event`, `Link`, `IssueLabel`, `LabelCount`. Plan 3 adds `PurgeLog` and `IdempotencyMatch` here.
- `internal/db/queries.go` — `IssueByID`, `IssueByNumber`, `lookupIssueForEvent`, `insertEventTx`, `eventInsert{...}`, `ErrNotFound`, `dedupeStrings`, `dedupeLinks`, `sortStrings`. Reuse all of these.
- `internal/db/queries_links.go` / `queries_labels.go` — typed errors and the `*AndEvent` mutation patterns.
- `internal/daemon/server.go` — `registerRoutes` calls `registerHealth/Projects/Issues/Comments/Actions/Links/Labels/Ownership/Ready`. Add `registerSearch/Destructive` in this plan.
- `internal/daemon/handlers_issues.go` — `createIssue` handler. Plan 3 extends it with idempotency + look-alike checks, but the existing initial-state pass-through stays.
- `cmd/kata/helpers.go` — `httpDoJSON`, `emitJSON`, `BodySources`, `resolveActor`, `Exit*` constants. Plan 3 adds nothing here.
- `cmd/kata/init.go` — `cliError{Message, Code, ExitCode}`, `apiErrFromBody`, `mapStatusToExit`, `resolveStartPath`.
- `cmd/kata/client.go` — `ensureDaemon(ctx)`, `httpClientFor(ctx, baseURL)` (two args, ctx first).
- `cmd/kata/create.go` — `resolveProjectID(ctx, baseURL, startPath)`, `printMutation(cmd, bs)`. Plan 3 extends `create.go` with new flags but reuses `resolveProjectID` and `printMutation`.
- `cmd/kata/main.go` — `flags globalFlags`, `runEEntered` sentinel, `exitCodeFor`, `subs := []*cobra.Command{...}` registration list.
- `cmd/kata/testhelpers_test.go` — `pipeServer`, `writeRuntimeFor`, `contextWithBaseURL`, `initBoundWorkspace`, `resolvePIDViaHTTP`, `itoa`, `resetFlags(t)`.
- `internal/testenv/testenv.go` — `testenv.New(t)` returns `*Env{URL, HTTP, DB, Home}`.
- `e2e/e2e_test.go` — `initRepo`, `requireOK`, `postJSON`, `getBody`, `resolvePID`, `drain`, `deleteWith` helpers.

---

### Task 1: `internal/db/migrations/0001_init.sql` — FTS sync triggers

Spec refs: §3.2 (the FTS5 declaration with the comment "kept in sync via triggers in Plan 3"), §3.7 (FTS5 candidate retrieval at create time), §4.10 (search response shape).

Append five triggers to `0001_init.sql` so `issues_fts` stays in sync with `issues` and `comments`. Five triggers, not six, because spec §3.5's purge is the only place that ever DELETEs from `issues` — and the cascade order means `comments` are gone before `issues` is — so the issues-AFTER-DELETE trigger reads "current" comments as empty, which is correct.

The contentless FTS5 contract: to remove a row from the index you must `INSERT INTO ft(ft, rowid, c1, c2, ...) VALUES('delete', rowid, <previously indexed values>)`. We track those values by always re-syncing through these triggers — every state change to title, body, or comments routes through one of them.

The five triggers:

1. **`issues_ai_fts`** (AFTER INSERT on issues) — index the new row with empty comments.
2. **`issues_au_fts`** (AFTER UPDATE OF title, body on issues) — `delete` then re-insert with the new title/body and current comments.
3. **`issues_ad_fts`** (AFTER DELETE on issues) — `delete` from FTS using the OLD row's title/body and the current comments aggregate (which is `''` because purge deletes comments first; this still has to be provided so the contentless `delete` command can find the row).
4. **`comments_ai_fts`** (AFTER INSERT on comments) — `delete` the issue's row using the comments aggregate **excluding** the new comment, then re-insert with the full aggregate.
5. **`comments_ad_fts`** (AFTER DELETE on comments) — `delete` the issue's row using the comments aggregate **including** the deleted comment, then re-insert with the post-delete aggregate.

`deleted_at` does **not** trigger FTS changes — soft-delete keeps the FTS row so `kata search --include-deleted` (Plan 6+ TUI) and the look-alike check can both see deleted issues. Look-alike is filtered at the query layer (Task 4), not in FTS.

**Files:**
- Modify: `internal/db/migrations/0001_init.sql` — append the five triggers below the existing `CREATE VIRTUAL TABLE issues_fts ...` block.
- Test: `internal/db/queries_search_test.go` (new file; the schema_completeness check covers the triggers' existence; this test exercises sync behavior end-to-end).

- [ ] **Step 1: Write the failing test**

```go
// internal/db/queries_search_test.go
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
		ProjectID: p.ID, Title: "old title", Body: "old body", Author: "tester",
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
```

`openTestDB(t)` already exists in `internal/db/queries_projects_test.go`'s package — reuse it.

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./internal/db/... -run TestFTS`
Expected: all three FAIL — FTS index is empty because no triggers populate it.

- [ ] **Step 3: Append the triggers to `0001_init.sql`**

Add immediately below the `CREATE VIRTUAL TABLE issues_fts ...` block (so the FTS table exists by the time the triggers reference it). The migrations runner executes the whole file as a single multi-statement Exec, so trigger definitions appearing later in the file are fine.

```sql
-- FTS5 sync triggers. The issues_fts table uses content='' so each delete must
-- provide the previously indexed column values; we stay in sync by routing every
-- title/body/comments mutation through one of the five triggers below. comments
-- is stored as a single space-separated aggregate built from the comments table
-- at trigger time.
--
-- Soft-delete (issues.deleted_at IS NOT NULL) does NOT remove rows from FTS —
-- look-alike checks and search filter deleted rows at query time so soft-deleted
-- issues remain reachable for `kata search --include-deleted` later.

CREATE TRIGGER issues_ai_fts AFTER INSERT ON issues BEGIN
  INSERT INTO issues_fts(rowid, title, body, comments)
  VALUES (NEW.id, NEW.title, NEW.body, '');
END;

CREATE TRIGGER issues_au_fts AFTER UPDATE OF title, body ON issues BEGIN
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete', OLD.id, OLD.title, OLD.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_id = OLD.id), '')
  );
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    NEW.id, NEW.title, NEW.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_id = NEW.id), '')
  );
END;

CREATE TRIGGER issues_ad_fts AFTER DELETE ON issues BEGIN
  -- Purge cascade deletes comments before issues, so the GROUP_CONCAT here is
  -- always '' at trigger time. We still pass it explicitly so the FTS delete
  -- command sees the same column shape we last inserted.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete', OLD.id, OLD.title, OLD.body,
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_id = OLD.id), '')
  );
END;

CREATE TRIGGER comments_ai_fts AFTER INSERT ON comments BEGIN
  -- Pre-insert state (what FTS currently holds) excludes the just-inserted row.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete',
    NEW.issue_id,
    (SELECT title FROM issues WHERE id = NEW.issue_id),
    (SELECT body  FROM issues WHERE id = NEW.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments
              WHERE issue_id = NEW.issue_id AND id <> NEW.id), '')
  );
  -- Post-insert state (what FTS should hold) includes it.
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    NEW.issue_id,
    (SELECT title FROM issues WHERE id = NEW.issue_id),
    (SELECT body  FROM issues WHERE id = NEW.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_id = NEW.issue_id), '')
  );
END;

CREATE TRIGGER comments_ad_fts AFTER DELETE ON comments BEGIN
  -- Pre-delete state (what FTS currently holds) included the deleted row.
  -- Reconstruct it as: current aggregate UNION ALL old.body, then GROUP_CONCAT.
  INSERT INTO issues_fts(issues_fts, rowid, title, body, comments) VALUES (
    'delete',
    OLD.issue_id,
    (SELECT title FROM issues WHERE id = OLD.issue_id),
    (SELECT body  FROM issues WHERE id = OLD.issue_id),
    COALESCE(
      (SELECT GROUP_CONCAT(body, ' ') FROM (
         SELECT body FROM comments WHERE issue_id = OLD.issue_id
         UNION ALL
         SELECT OLD.body
      )),
      ''
    )
  );
  -- Post-delete state (what FTS should hold) excludes it.
  INSERT INTO issues_fts(rowid, title, body, comments) VALUES (
    OLD.issue_id,
    (SELECT title FROM issues WHERE id = OLD.issue_id),
    (SELECT body  FROM issues WHERE id = OLD.issue_id),
    COALESCE((SELECT GROUP_CONCAT(body, ' ') FROM comments WHERE issue_id = OLD.issue_id), '')
  );
END;
```

A note on `comments_ad_fts`: when the issue is being purged, the `issues` row still exists at the moment this trigger fires (purge order: events → comments → links → issue_labels → purge_log → issues). So `(SELECT title FROM issues WHERE id = OLD.issue_id)` returns the row's actual title. The post-delete `INSERT INTO issues_fts(...)` writes a row with empty comments; immediately afterward, `issues_ad_fts` removes it via the contentless `delete` command using those same empty comments. That round-trip is wasteful but correct, and it keeps the trigger logic uniform. If future profiling shows this matters, add a `WHEN (SELECT 1 FROM issues WHERE id = OLD.issue_id)` guard.

- [ ] **Step 4: Run the tests (expect pass)**

Run: `go test ./internal/db/... -run TestFTS`
Expected: all three PASS.

Run the full suite to confirm no regression: `make test`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/migrations/0001_init.sql internal/db/queries_search_test.go
git commit -m "feat(db): FTS5 sync triggers on issues + comments"
```

Note: the schema_completeness test (`internal/db/schema_completeness_test.go`) only checks tables, not triggers. The TestFTS_* tests above are the regression net for the trigger logic. If you ever rename a trigger, the migration test still passes — but the FTS round-trip tests will fail loudly.

---

### Task 2: `internal/similarity/` — Canonical, Tokenize, Jaccard, Score

Spec refs: §3.6 (`canonical()` for fingerprints — NFC + trim + collapse internal whitespace), §3.7 (look-alike pipeline — tokenize, lowercase, stop-word, stem; Jaccard on title (0.6) + Jaccard on first 500 chars of body (0.4)), §9.2 (`internal/similarity/similarity.go`).

This package is **pure functions**, no DB, no imports of `internal/db`. It owns text normalization for both fingerprinting (where Canonical is the only thing used) and look-alike scoring (where the full Tokenize → Jaccard → Score pipeline runs).

**Design choices:**

- **Canonical** = NFC-normalize, trim leading/trailing whitespace, collapse runs of internal whitespace (any Unicode whitespace) to a single space (U+0020). No lowercasing — fingerprint is case-sensitive per spec §3.6.
- **Tokenize** = NFC + lowercase + split on Unicode word boundaries (anything that is not a letter or digit is a separator), drop tokens that are stop-words or shorter than 2 characters, apply a tiny suffix-stripper (`-ing`/`-ed`/`-es`/`-s`). The result is a slice with deduplication left to the caller (Jaccard treats inputs as multisets but we'll dedupe before passing in).
- **Jaccard** = `|A ∩ B| / |A ∪ B|`, treating the inputs as sets (the function dedupes internally so the caller doesn't have to).
- **Score(titleA, bodyA, titleB, bodyB)** = `0.6 * Jaccard(Tokenize(titleA), Tokenize(titleB)) + 0.4 * Jaccard(Tokenize(bodyA[:500]), Tokenize(bodyB[:500]))`. Per spec §3.7 only the first 500 characters of each body are scored — not 500 tokens, not 500 bytes — so `bodyA[:500]` slices on **runes** (so multibyte UTF-8 doesn't get cut mid-codepoint).

**Stop words:** a small fixed list — `a, an, and, are, as, at, be, by, for, from, has, have, in, is, it, of, on, or, that, the, this, to, was, were, will, with`. Hard-coded; not configurable in v1.

**Why NFC, not NFKC:** spec §3.6 specifies NFC. NFKC would collapse e.g. `①` → `1`, which is the wrong semantics for fingerprinting where two visibly different strings should produce different fingerprints.

**Files:**
- Create: `internal/similarity/similarity.go`
- Test: `internal/similarity/similarity_test.go`

- [ ] **Step 1: Add the dependency**

The unicode normalization library is part of `golang.org/x/text`:

```bash
go get golang.org/x/text@latest
go mod tidy
```

This module is already in `go.sum` indirectly via huma's dependencies, but pin it as a direct dep so future `go mod tidy` doesn't drop it.

- [ ] **Step 2: Write the failing test**

```go
// internal/similarity/similarity_test.go
package similarity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/similarity"
)

func TestCanonical(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"trim", "  hello  ", "hello"},
		{"collapse_internal_runs", "fix\t\nlogin   bug", "fix login bug"},
		{"preserves_case", "Fix Login Bug", "Fix Login Bug"},
		{"nfc_normalizes_combining_marks",
			"café",                  // é precomposed
			"café"},                 // unchanged (already NFC)
		{"nfc_normalizes_decomposed_form",
			"café",                 // e + combining acute
			"café"},                 // → precomposed
		{"non_ascii_whitespace_is_collapsed",
			"foo bar",               // non-breaking space
			"foo bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, similarity.Canonical(tc.in))
		})
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single_word", "fix", []string{"fix"}},
		{"drops_stop_words", "the bug is in login", []string{"bug", "login"}},
		{"lowercases", "Fix Login", []string{"fix", "login"}},
		{"stems_simple_suffixes",
			"fixing crashes for testing",
			[]string{"fix", "crash", "test"}}, // ing/es/ing dropped; "for" stop-worded
		{"drops_short_tokens", "a b is fix", []string{"fix"}},
		{"splits_on_punctuation", "fix-login: crash!", []string{"fix", "login", "crash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, similarity.Tokenize(tc.in))
		})
	}
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want float64
	}{
		{"both_empty", nil, nil, 0.0},
		{"one_empty", []string{"x"}, nil, 0.0},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, 1.0},
		{"half_overlap", []string{"a", "b"}, []string{"b", "c"}, 1.0 / 3.0},
		{"dedupes_inputs", []string{"a", "a", "b"}, []string{"a", "b", "b"}, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, similarity.Jaccard(tc.a, tc.b), 1e-9)
		})
	}
}

func TestScore_WeightedSum(t *testing.T) {
	// Identical title + body → 1.0.
	got := similarity.Score("fix login crash", "stack trace here",
		"fix login crash", "stack trace here")
	assert.InDelta(t, 1.0, got, 1e-9)

	// Identical title, different body — score = 0.6.
	got = similarity.Score("fix login crash", "stack trace here",
		"fix login crash", "completely different body")
	assert.InDelta(t, 0.6, got, 1e-9)

	// Different title, identical body — score = 0.4.
	got = similarity.Score("fix login crash", "shared body text",
		"unrelated title", "shared body text")
	assert.InDelta(t, 0.4, got, 1e-9)

	// Disjoint everything → 0.0.
	got = similarity.Score("alpha", "beta", "gamma", "delta")
	assert.InDelta(t, 0.0, got, 1e-9)
}

func TestScore_Body500CharLimit(t *testing.T) {
	// Build a body whose first 500 runes match between the two issues, then
	// diverge afterwards. Score should reflect the matched prefix only — the
	// trailing divergence is past the 500-rune cap.
	prefix := ""
	for i := 0; i < 500; i++ {
		prefix += "x"
	}
	got := similarity.Score("same", prefix+" alpha-divergent",
		"same", prefix+" beta-divergent")
	// Both bodies tokenize to {"x...x"} for the prefix portion (single repeated
	// token gets deduped); identical → 1.0 for body. Title also identical → 1.0.
	assert.InDelta(t, 1.0, got, 1e-9, "divergence past 500 chars must not affect the score")
}
```

- [ ] **Step 3: Run the test (expect failure)**

Run: `go test ./internal/similarity/...`
Expected: FAIL — package doesn't exist.

- [ ] **Step 4: Implement the package**

```go
// internal/similarity/similarity.go

// Package similarity provides text-normalization primitives used by both the
// idempotency fingerprint (Canonical only) and the look-alike soft-block
// pipeline (full Tokenize → Jaccard → Score). All functions are pure; no DB,
// no I/O, no goroutines.
package similarity

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Canonical normalizes a string for fingerprinting: NFC, trim leading/trailing
// whitespace, collapse runs of any Unicode whitespace into a single ASCII
// space. Case is preserved — fingerprint is case-sensitive per spec §3.6.
func Canonical(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // suppresses leading whitespace
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	out := b.String()
	return strings.TrimRight(out, " ")
}

// stopWords is the fixed v1 list. Membership check is O(1) via map.
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"by": {}, "for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "is": {},
	"it": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "this": {},
	"to": {}, "was": {}, "were": {}, "will": {}, "with": {},
}

// Tokenize lowercases s, splits on non-letter-or-digit boundaries, drops
// stop-words and tokens shorter than 2 runes, and applies a simple
// suffix-stripper (ing → "", ed → "", es → "", s → ""). Returns a slice with
// no stability guarantees on repeated tokens — callers wanting set semantics
// should use Jaccard (which dedupes internally).
func Tokenize(s string) []string {
	s = norm.NFC.String(s)
	s = strings.ToLower(s)

	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := stem(cur.String())
		cur.Reset()
		if len(tok) < 2 {
			return
		}
		if _, isStop := stopWords[tok]; isStop {
			return
		}
		out = append(out, tok)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// stem strips a small set of English suffixes. Order matters: longer suffixes
// before shorter ones so "testing" → "test" not "testin".
func stem(t string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(t) > len(suf)+1 && strings.HasSuffix(t, suf) {
			return t[:len(t)-len(suf)]
		}
	}
	return t
}

// Jaccard returns |A ∩ B| / |A ∪ B| treating inputs as sets. Empty inputs
// (either side) return 0 — a deliberate choice so a brand-new issue's empty
// body doesn't inflate similarity against another issue with an empty body.
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}
	intersect := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersect++
		}
	}
	union := len(setA) + len(setB) - intersect
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

// Score returns the weighted similarity between two issues:
//
//	0.6 * Jaccard(title tokens) + 0.4 * Jaccard(body[:500] tokens)
//
// Body slicing is rune-based: the first 500 Unicode codepoints of each body
// are tokenized. Spec §3.7.
func Score(titleA, bodyA, titleB, bodyB string) float64 {
	titleScore := Jaccard(Tokenize(titleA), Tokenize(titleB))
	bodyScore := Jaccard(Tokenize(firstRunes(bodyA, 500)), Tokenize(firstRunes(bodyB, 500)))
	return 0.6*titleScore + 0.4*bodyScore
}

// firstRunes returns the first n runes of s. If s has fewer than n runes,
// it's returned unchanged.
func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/similarity/...`
Expected: PASS for every case.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/similarity/ go.mod go.sum
git commit -m "feat(similarity): canonical, tokenize, jaccard, weighted score"
```

---

### Task 3: `internal/db/queries_search.go` — FTS candidate retrieval

Spec refs: §3.7 (top-20 by BM25, then app-side similarity), §4.10 (search response shape; matched_in is daemon-side).

> **Implementation deviation (recorded after the fact).** The plan below prescribes
> `highlight()` for column-attribution, but `highlight()` returns NULL on contentless
> FTS5 tables (`issues_fts` is declared `content=''` in Task 1), so `MatchedIn` was
> always empty. The shipped code uses per-column `MATCH` subqueries instead — three
> correlated `(issues_fts.rowid IN (SELECT rowid FROM issues_fts WHERE col MATCH ?))`
> booleans. See commit `779e56d` for the fix and `24d1d81` for polish (limit cap,
> rank-test dominance, doc fixes). The Step 4 SQL block below is preserved as
> originally written — refer to `internal/db/queries_search.go` at HEAD for the
> actual approach.

A single new function: `SearchFTS(ctx, projectID, q, limit, includeDeleted) ([]SearchCandidate, error)`. Uses FTS5 BM25 to rank, joins back to `issues` to filter by project and (optionally) by `deleted_at`, returns the top `limit` rows along with which columns matched.

**Match-column detection** uses per-column `MATCH` subqueries (see deviation note above). The result is a `[]string` of `"title"`/`"body"`/`"comments"` per row — these are what spec §4.10's `matched_in` field surfaces.

**FTS query escaping:** SQLite FTS5 treats `:` `"` `*` and parens as MATCH operators. To support arbitrary user queries (`fix login: bug`) without surfacing those as syntax errors, we wrap the query in double quotes and double any embedded quotes (`fix "x" bug` → `"fix ""x"" bug"`). This makes the whole user query a single phrase token. For Plan 3 that's good enough — multi-token AND queries are a Plan 4+ concern.

**Files:**
- Modify: `internal/db/types.go` — add `SearchCandidate`.
- Create: `internal/db/queries_search.go` — `SearchFTS`.
- Test: `internal/db/queries_search_test.go` (extend the file from Task 1).

- [ ] **Step 1: Add `SearchCandidate` type**

In `internal/db/types.go`, append:

```go
// SearchCandidate is one row from SearchFTS: an issue, a BM25 score (lower is
// better in raw form; we negate to ascending = better), and the columns where
// the query matched. MatchedIn is the basis for the wire response's matched_in.
type SearchCandidate struct {
	Issue     Issue    `json:"issue"`
	Score     float64  `json:"score"` // BM25, negated; higher = better match
	MatchedIn []string `json:"matched_in"`
}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/db/queries_search_test.go`:

```go
func TestSearchFTS_RanksByBM25(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)

	// Three issues. Only the first two mention "login"; the second has it twice.
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "fix login crash", Body: "stack trace", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "login is broken on login screen",
		Body: "login fails twice", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "unrelated issue", Body: "no match here", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login", 20, false)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	// The doubly-mentioned issue should outrank the singly-mentioned one.
	assert.Equal(t, int64(2), got[0].Issue.Number, "more matches → higher rank")
	assert.Equal(t, int64(1), got[1].Issue.Number)
}

func TestSearchFTS_FiltersByProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)

	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p1.ID, Title: "login bug", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p2.ID, Title: "login bug", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p1.ID, "login", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, p1.ID, got[0].Issue.ProjectID)
}

func TestSearchFTS_ExcludesDeletedByDefault(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	keep, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "keep login", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	gone, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "deleted login", Body: "", Author: "tester",
	})
	require.NoError(t, err)
	// Mark the second issue soft-deleted directly via SQL — Task 5 ships
	// SoftDeleteIssue but this test runs before that, and the DB-layer
	// behavior we want to verify is "search query filters deleted_at IS NULL".
	_, err = d.ExecContext(ctx,
		`UPDATE issues SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		gone.ID)
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "login", 20, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, keep.ID, got[0].Issue.ID, "soft-deleted issue must be filtered")

	// includeDeleted=true returns both.
	got, err = d.SearchFTS(ctx, p.ID, "login", 20, true)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestSearchFTS_EmptyQueryReturnsEmpty(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "anything", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.SearchFTS(ctx, p.ID, "   ", 20, false)
	require.NoError(t, err)
	assert.Empty(t, got, "blank query → empty result, not an error")
}

func TestSearchFTS_QueryEscaping(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	_, _, err = d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: `fix "login" crash`, Body: "", Author: "tester",
	})
	require.NoError(t, err)

	// FTS5 syntax characters must not surface as syntax errors.
	got, err := d.SearchFTS(ctx, p.ID, `"login"`, 20, false)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
```

- [ ] **Step 3: Run the tests (expect failure)**

Run: `go test ./internal/db/... -run TestSearchFTS`
Expected: FAIL — function doesn't exist.

- [ ] **Step 4: Implement `SearchFTS`**

```go
// internal/db/queries_search.go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SearchFTS runs an FTS5 BM25-ranked query against issues_fts, joins back to
// issues, and returns the top `limit` rows scoped to the given project. When
// includeDeleted is false, soft-deleted issues are filtered. The returned
// Score is the negated raw BM25 (so higher = better match), matched_in is the
// list of FTS columns that contributed to the row's score.
func (d *DB) SearchFTS(ctx context.Context, projectID int64, q string, limit int, includeDeleted bool) ([]SearchCandidate, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	// Wrap the user query as a single FTS5 phrase. Embedded double quotes are
	// doubled per FTS5 quoting rules so the whole query is opaque to FTS's
	// special characters (`:`, `*`, parens, `OR`/`AND`/`NOT` as bare words).
	phrase := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`

	// highlight(table, col, ...) returns the matched text wrapped in markers;
	// when the column didn't match it returns the empty matched substring.
	// We use a sentinel pair to detect non-empty highlights.
	const (
		startMark = "\x01"
		endMark   = "\x02"
	)
	deletedFilter := "AND i.deleted_at IS NULL"
	if includeDeleted {
		deletedFilter = ""
	}
	query := fmt.Sprintf(`
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at,
		       bm25(issues_fts),
		       highlight(issues_fts, 0, ?, ?),
		       highlight(issues_fts, 1, ?, ?),
		       highlight(issues_fts, 2, ?, ?)
		FROM issues_fts
		JOIN issues i ON i.id = issues_fts.rowid
		WHERE issues_fts MATCH ?
		  AND i.project_id = ?
		  %s
		ORDER BY bm25(issues_fts) ASC
		LIMIT %d`, deletedFilter, limit)

	rows, err := d.QueryContext(ctx, query,
		startMark, endMark, startMark, endMark, startMark, endMark,
		phrase, projectID)
	if err != nil {
		return nil, fmt.Errorf("search fts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchCandidate
	for rows.Next() {
		var (
			i                                              Issue
			rawScore                                       float64
			titleHL, bodyHL, commentsHL                    string
		)
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt,
			&rawScore, &titleHL, &bodyHL, &commentsHL); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		matched := make([]string, 0, 3)
		if strings.Contains(titleHL, startMark) {
			matched = append(matched, "title")
		}
		if strings.Contains(bodyHL, startMark) {
			matched = append(matched, "body")
		}
		if strings.Contains(commentsHL, startMark) {
			matched = append(matched, "comments")
		}
		// FTS5 BM25 returns negative numbers; invert so callers compare with
		// "higher = better" semantics.
		out = append(out, SearchCandidate{
			Issue:     i,
			Score:     -rawScore,
			MatchedIn: matched,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// (sql import is here to silence the "imported and not used" lint when this
// file grows queries that need it. Currently unused; remove if it triggers
// the unused-import lint and add back when needed.)
var _ = sql.ErrNoRows
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/db/... -run TestSearchFTS`
Expected: PASS for all five subtests.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/types.go internal/db/queries_search.go internal/db/queries_search_test.go
git commit -m "feat(db): SearchFTS with BM25 ranking and matched-in detection"
```

---

### Task 4: `internal/db/queries_idempotency.go` — Fingerprint + LookupIdempotency

Spec refs: §3.6 (idempotency fingerprint shape, lookup window), §3.3 (idempotency_key + idempotency_fingerprint in `issue.created` event payload), the `idx_events_idempotency` partial index already declared in `0001_init.sql`.

Two pieces:

1. **`Fingerprint(title, body, owner, labels, links []InitialLink) string`** — pure function returning the lowercase hex sha256 of the canonical concatenation. Deterministic across processes; the wire-stable form makes cross-language clients (e.g. a future Python client) able to compute the same hash.
2. **`LookupIdempotency(ctx, projectID, key, since) (*IdempotencyMatch, error)`** — queries the `events` table via `idx_events_idempotency`. Returns the matching `issue.created` event's payload metadata if found within the window, or `(nil, nil)` if not. Caller decides whether to reuse, mismatch, or surface the deleted-issue case.

`IdempotencyMatch` carries enough info for the daemon to make all three decisions (reuse / mismatch / deleted) without a second query: it includes the issue id, issue number, the persisted fingerprint, and the original event row (for `original_event` in the response envelope when reused).

**Why fingerprint inputs are exactly title/body/owner/labels/links:** spec §3.6 lists these as "every creation-affecting field." `actor` is intentionally excluded — the same idempotency key being retried by a different actor (e.g. retry from a different host with a different `git config user.name`) should still dedup.

**Files:**
- Modify: `internal/db/types.go` — add `IdempotencyMatch`.
- Create: `internal/db/queries_idempotency.go` — `Fingerprint`, `LookupIdempotency`.
- Test: `internal/db/queries_idempotency_test.go`.

- [ ] **Step 1: Add `IdempotencyMatch` type**

In `internal/db/types.go`, append:

```go
// IdempotencyMatch is the payload returned by LookupIdempotency. The Event row
// is included so the handler can populate `original_event` in the reuse-case
// MutationResponse without a second query.
type IdempotencyMatch struct {
	IssueID     int64
	IssueNumber int64
	Fingerprint string
	Event       Event
}
```

- [ ] **Step 2: Write the failing test**

```go
// internal/db/queries_idempotency_test.go
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
	// CreateIssue extension landing in Task 7.
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
```

- [ ] **Step 3: Run the tests (expect failure)**

Run: `go test ./internal/db/... -run "TestFingerprint|TestLookupIdempotency"`
Expected: FAIL — neither function exists.

- [ ] **Step 4: Implement**

```go
// internal/db/queries_idempotency.go
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wesm/kata/internal/similarity"
)

// Fingerprint returns the lowercase hex SHA-256 of the canonical concatenation
// of (title, body, owner, sorted labels, sorted links) per spec §3.6. The
// fingerprint is order-independent for labels and links: both are sorted before
// hashing. Owner is canonicalized as "" when nil or empty. Labels are
// alphabetized. Links are sorted by (type, to_number).
func Fingerprint(title, body string, owner *string, labels []string, links []InitialLink) string {
	ownerStr := ""
	if owner != nil {
		ownerStr = *owner
	}
	sortedLabels := append([]string(nil), labels...)
	sort.Strings(sortedLabels)
	sortedLinks := append([]InitialLink(nil), links...)
	sort.Slice(sortedLinks, func(i, j int) bool {
		if sortedLinks[i].Type != sortedLinks[j].Type {
			return sortedLinks[i].Type < sortedLinks[j].Type
		}
		return sortedLinks[i].ToNumber < sortedLinks[j].ToNumber
	})
	// Use a fixed JSON form for the links portion so cross-language clients
	// can reproduce the same bytes. Each entry is {"type":"…","other_number":N}
	// per spec §3.6 ("two-element record with a fixed JSON form").
	type linkRec struct {
		Type        string `json:"type"`
		OtherNumber int64  `json:"other_number"`
	}
	linkRecs := make([]linkRec, 0, len(sortedLinks))
	for _, l := range sortedLinks {
		linkRecs = append(linkRecs, linkRec{Type: l.Type, OtherNumber: l.ToNumber})
	}
	linksJSON, _ := json.Marshal(linkRecs) // never errors on this shape

	var b strings.Builder
	b.WriteString("title=")
	b.WriteString(similarity.Canonical(title))
	b.WriteString("\nbody=")
	b.WriteString(similarity.Canonical(body))
	b.WriteString("\nowner=")
	b.WriteString(similarity.Canonical(ownerStr))
	b.WriteString("\nlabels=")
	b.WriteString(strings.Join(sortedLabels, ","))
	b.WriteString("\nlinks=")
	b.WriteString(similarity.Canonical(string(linksJSON)))

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// LookupIdempotency searches `events` for an `issue.created` row in the given
// project whose payload's `idempotency_key` equals key and whose created_at is
// at-or-after `since`. Returns nil when no match. Uses the partial index
// idx_events_idempotency declared in 0001_init.sql.
func (d *DB) LookupIdempotency(ctx context.Context, projectID int64, key string, since time.Time) (*IdempotencyMatch, error) {
	const q = `
		SELECT e.id, e.project_id, e.project_identity, e.issue_id, e.issue_number,
		       e.related_issue_id, e.type, e.actor, e.payload, e.created_at,
		       json_extract(e.payload, '$.idempotency_fingerprint')
		FROM events e
		WHERE e.type = 'issue.created'
		  AND e.project_id = ?
		  AND json_extract(e.payload, '$.idempotency_key') = ?
		  AND e.created_at >= ?
		ORDER BY e.id DESC
		LIMIT 1`
	row := d.QueryRowContext(ctx, q, projectID, key, since.UTC().Format("2006-01-02T15:04:05.000Z"))

	var (
		evt Event
		fp  sql.NullString
	)
	err := row.Scan(&evt.ID, &evt.ProjectID, &evt.ProjectIdentity, &evt.IssueID,
		&evt.IssueNumber, &evt.RelatedIssueID, &evt.Type, &evt.Actor,
		&evt.Payload, &evt.CreatedAt, &fp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency: %w", err)
	}
	if evt.IssueID == nil || evt.IssueNumber == nil {
		// Defensive: an issue.created event without an issue_id is malformed.
		return nil, fmt.Errorf("idempotency match has no issue_id")
	}
	return &IdempotencyMatch{
		IssueID:     *evt.IssueID,
		IssueNumber: *evt.IssueNumber,
		Fingerprint: fp.String,
		Event:       evt,
	}, nil
}
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/db/... -run "TestFingerprint|TestLookupIdempotency"`
Expected: PASS for every subtest.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/types.go internal/db/queries_idempotency.go internal/db/queries_idempotency_test.go
git commit -m "feat(db): Fingerprint + LookupIdempotency over events index"
```

---

### Task 5: `internal/db/queries_delete.go` — SoftDeleteIssue + RestoreIssue

Spec refs: §3.3 (`issue.soft_deleted`, `issue.restored`), §3.4 (delete/restore lifecycle), §3.5 (destructive ladder steps 3 + 4).

Two functions: SoftDeleteIssue and RestoreIssue. Both follow the established `*AndEvent` pattern from Plan 2's link/label mutations: open a TX, mutate, emit event, bump updated_at, commit.

**SoftDeleteIssue contract:**
- Takes (issueID, actor).
- If issue is already soft-deleted (deleted_at IS NOT NULL): no-op envelope (`*Event=nil, changed=false`). The handler in Task 9 maps this to a 200 response.
- Otherwise: sets `deleted_at = strftime(...)`, emits `issue.soft_deleted` event with empty payload, bumps updated_at, returns updated Issue + Event + changed=true.

The existing `lookupIssueForEvent` excludes deleted issues, so SoftDeleteIssue uses a custom inline lookup that DOES see deleted rows (otherwise calling soft-delete on an already-deleted issue would 404 instead of returning the no-op envelope).

**RestoreIssue contract:**
- Takes (issueID, actor).
- If issue is not deleted (deleted_at IS NULL): no-op envelope.
- Otherwise: sets `deleted_at = NULL`, emits `issue.restored` event, bumps updated_at, returns updated Issue + Event + changed=true.

Both also fail with `ErrNotFound` when no row exists for issueID at all (vs. the no-op case where the row exists but is already in target state).

**Files:**
- Create: `internal/db/queries_delete.go` — `SoftDeleteIssue`, `RestoreIssue`, `lookupIssueIncludingDeleted` helper.
- Test: `internal/db/queries_delete_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// internal/db/queries_delete_test.go
package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestSoftDeleteIssue_SetsDeletedAtAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.soft_deleted", evt.Type)
	assert.Equal(t, "agent", evt.Actor)
	require.NotNil(t, updated.DeletedAt)
}

func TestSoftDeleteIssue_AlreadyDeletedIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)

	updated, evt, changed, err := d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt, "no-op should return nil event")
	assert.False(t, changed)
	assert.NotNil(t, updated.DeletedAt, "issue stays deleted")
}

func TestSoftDeleteIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.SoftDeleteIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestRestoreIssue_ClearsDeletedAtAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.SoftDeleteIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)

	updated, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	require.NotNil(t, evt)
	assert.True(t, changed)
	assert.Equal(t, "issue.restored", evt.Type)
	assert.Nil(t, updated.DeletedAt)
}

func TestRestoreIssue_NotDeletedIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)

	_, evt, changed, err := d.RestoreIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.Nil(t, evt)
	assert.False(t, changed)
}

func TestRestoreIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _, _, err := d.RestoreIssue(ctx, 9999, "agent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}
```

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./internal/db/... -run "TestSoftDeleteIssue|TestRestoreIssue"`
Expected: FAIL — both functions absent.

- [ ] **Step 3: Implement**

```go
// internal/db/queries_delete.go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SoftDeleteIssue sets deleted_at on the issue and emits issue.soft_deleted.
// Already-deleted issues are returned as a no-op envelope (nil event,
// changed=false). Unknown issues return ErrNotFound.
func (d *DB) SoftDeleteIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt != nil {
		// Already soft-deleted; commit so the read-side state is consistent
		// (no-op tx is harmless) and return the no-op envelope.
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("soft delete: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.soft_deleted",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// RestoreIssue clears deleted_at and emits issue.restored. Not-deleted issues
// are returned as a no-op envelope. Unknown issues return ErrNotFound.
func (d *DB) RestoreIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.DeletedAt == nil {
		if err := tx.Commit(); err != nil {
			return Issue{}, nil, false, err
		}
		return issue, nil, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET deleted_at = NULL,
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("restore: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.restored",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// lookupIssueIncludingDeleted fetches an issue + its project's identity for
// event snapshotting. Unlike lookupIssueForEvent (queries.go), this version
// does NOT filter out soft-deleted rows — it's the right primitive for the
// destructive ladder verbs that need to operate on deleted issues.
func lookupIssueIncludingDeleted(ctx context.Context, tx *sql.Tx, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ?`
	var (
		i        Issue
		identity string
	)
	err := tx.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue including deleted: %w", err)
	}
	return i, identity, nil
}
```

- [ ] **Step 4: Run the tests (expect pass)**

Run: `go test ./internal/db/... -run "TestSoftDeleteIssue|TestRestoreIssue"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries_delete.go internal/db/queries_delete_test.go
git commit -m "feat(db): SoftDeleteIssue + RestoreIssue with no-op envelopes"
```

---

### Task 6: `internal/db/queries_delete.go` — PurgeIssue (the multi-step TX)

Spec refs: §2.6 (SSE durability — purge reserves a synthetic cursor by bumping `sqlite_sequence`), §3.5 (purge ladder, exact step ordering), §6.5 (`purge_log_inconsistency` doctor check).

PurgeIssue is the riskiest function in Plan 3. It runs the entire seven-step transaction from spec §3.5 and writes the audit row. **No `issue.purged` event is persisted** (per §3.5: "The `purge_log` row is the only persisted record."). The reserved SSE cursor is captured in `purge_log.purge_reset_after_event_id` for Plan 4's broadcaster to consume.

**Step ordering (all in one TX, BEGIN IMMEDIATE):**

1. `lookupIssueIncludingDeleted` — fetch the issue + project identity. ErrNotFound if missing.
2. Capture `(min, max)` of `events.id` where `issue_id = N OR related_issue_id = N`. Both NULL if zero matches.
3. Compute counts of dependents (comments, links, issue_labels, events).
4. DELETE `events WHERE issue_id = N OR related_issue_id = N`.
5. DELETE `comments WHERE issue_id = N`.
6. DELETE `links WHERE from_issue_id = N OR to_issue_id = N`.
7. DELETE `issue_labels WHERE issue_id = N`.
8. If any events were deleted (events_deleted_min_id IS NOT NULL): bump `sqlite_sequence` for `events` and capture the new value as `purge_reset_after_event_id`.
9. INSERT `purge_log` row with all the captured fields.
10. DELETE `issues WHERE id = N` (this triggers `issues_ad_fts` from Task 1; comments cascade has already removed comments_ad_fts triggers from earlier rows).
11. Commit.

**Returns** the `PurgeLog` row (so the handler in Task 9 can echo the captured counts and reserved cursor in the response).

The schema-trigger order matters: events first (no FTS impact), then comments (each one re-syncs FTS via `comments_ad_fts`, but by the end FTS has comments=''), then links/labels (no FTS), then issues (the `issues_ad_fts` trigger fires here using the OLD title/body and the now-empty comments). End state: zero rows in FTS for this issue. Verified by Step 4.

**`reason` parameter:** purge has an optional reason string that lands in `purge_log.reason`. Plan 3 doesn't surface it on the wire (the daemon handler reads only `actor`); we accept it as a parameter so future tooling can add `--reason` without a schema change.

**Files:**
- Modify: `internal/db/types.go` — add `PurgeLog`.
- Modify: `internal/db/queries_delete.go` — add `PurgeIssue`.
- Test: extend `internal/db/queries_delete_test.go`.

- [ ] **Step 1: Add `PurgeLog` type**

In `internal/db/types.go`:

```go
// PurgeLog mirrors a row in purge_log. Snapshots the issue identity at purge
// time so audits survive any future project rename. EventsDeletedMinID/MaxID
// and PurgeResetAfterEventID are nullable: NULL when no events were attached
// to the purged issue.
type PurgeLog struct {
	ID                      int64     `json:"id"`
	ProjectID               int64     `json:"project_id"`
	PurgedIssueID           int64     `json:"purged_issue_id"`
	ProjectIdentity         string    `json:"project_identity"`
	IssueNumber             int64     `json:"issue_number"`
	IssueTitle              string    `json:"issue_title"`
	IssueAuthor             string    `json:"issue_author"`
	CommentCount            int64     `json:"comment_count"`
	LinkCount               int64     `json:"link_count"`
	LabelCount              int64     `json:"label_count"`
	EventCount              int64     `json:"event_count"`
	EventsDeletedMinID      *int64    `json:"events_deleted_min_id,omitempty"`
	EventsDeletedMaxID      *int64    `json:"events_deleted_max_id,omitempty"`
	PurgeResetAfterEventID  *int64    `json:"purge_reset_after_event_id,omitempty"`
	Actor                   string    `json:"actor"`
	Reason                  *string   `json:"reason,omitempty"`
	PurgedAt                time.Time `json:"purged_at"`
}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/db/queries_delete_test.go`:

```go
func TestPurgeIssue_RemovesAllDependentsAndAudits(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	target, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "delete me", Body: "body", Author: "tester",
	})
	require.NoError(t, err)
	keeper, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "keep me", Body: "", Author: "tester",
	})
	require.NoError(t, err)

	// Add a comment, a label, and a link from keeper → target so cascade
	// removes a non-trivial set of dependents.
	_, _, err = d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: target.ID, Author: "tester", Body: "comment body",
	})
	require.NoError(t, err)
	_, err = d.AddLabel(ctx, target.ID, "bug", "tester")
	require.NoError(t, err)
	_, _, err = d.CreateLinkAndEvent(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: keeper.ID, ToIssueID: target.ID,
		Type: "blocks", Author: "tester",
	}, db.LinkEventParams{
		EventType: "issue.linked", EventIssueID: keeper.ID, EventIssueNumber: keeper.Number,
		FromNumber: keeper.Number, ToNumber: target.Number, Actor: "tester",
	})
	require.NoError(t, err)

	pl, err := d.PurgeIssue(ctx, target.ID, "agent", nil)
	require.NoError(t, err)

	assert.Equal(t, target.ID, pl.PurgedIssueID)
	assert.Equal(t, "github.com/wesm/kata", pl.ProjectIdentity)
	assert.Equal(t, target.Number, pl.IssueNumber)
	assert.Equal(t, "delete me", pl.IssueTitle)
	assert.Equal(t, "tester", pl.IssueAuthor)
	assert.Equal(t, int64(1), pl.CommentCount)
	assert.Equal(t, int64(1), pl.LinkCount)
	assert.Equal(t, int64(1), pl.LabelCount)
	// Events: issue.created + issue.commented + issue.labeled = 3 attached to target,
	// plus 1 issue.linked attributed to target via related_issue_id (keeper's event).
	// Total: 4.
	assert.Equal(t, int64(4), pl.EventCount)
	require.NotNil(t, pl.EventsDeletedMinID)
	require.NotNil(t, pl.EventsDeletedMaxID)
	require.NotNil(t, pl.PurgeResetAfterEventID, "events were deleted, reset cursor must be set")

	// Verify rows actually gone.
	var n int
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues WHERE id = ?`, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "issue row removed")
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM comments WHERE issue_id = ?`, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "comments removed")
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		target.ID, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "links removed")
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issue_labels WHERE issue_id = ?`, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "labels removed")
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		target.ID, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "events removed")

	// FTS row gone.
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM issues_fts WHERE rowid = ?`, target.ID).Scan(&n))
	assert.Equal(t, 0, n, "FTS row removed")

	// keeper's events.created is the only event attributed to keeper that
	// survives — keeper's issue.linked was deleted because related_issue_id
	// pointed to target.
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT count(*) FROM events WHERE issue_id = ?`, keeper.ID).Scan(&n))
	assert.Equal(t, 1, n, "keeper's issue.created survives; its issue.linked was cascade-deleted via related_issue_id")
}

func TestPurgeIssue_NoEventsLeavesResetCursorNull(t *testing.T) {
	// Manually craft an issue row with no events: insert directly so we
	// bypass CreateIssue's automatic issue.created event. Verify that
	// PurgeIssue sees zero attached events and leaves PurgeResetAfterEventID
	// as nil (no SSE cursor reservation needed).
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	res, err := d.ExecContext(ctx,
		`INSERT INTO issues(project_id, number, title, author) VALUES(?, 1, 'no-events', 'tester')`,
		p.ID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	pl, err := d.PurgeIssue(ctx, id, "agent", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), pl.EventCount)
	assert.Nil(t, pl.EventsDeletedMinID)
	assert.Nil(t, pl.EventsDeletedMaxID)
	assert.Nil(t, pl.PurgeResetAfterEventID, "no events deleted → no reset cursor")
}

func TestPurgeIssue_ReservesSqliteSequenceAboveMaxEventID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	target, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "x", Author: "tester",
	})
	require.NoError(t, err)
	// Capture max events.id BEFORE purge.
	var maxBefore int64
	require.NoError(t, d.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&maxBefore))

	pl, err := d.PurgeIssue(ctx, target.ID, "agent", nil)
	require.NoError(t, err)
	require.NotNil(t, pl.PurgeResetAfterEventID)
	assert.Greater(t, *pl.PurgeResetAfterEventID, maxBefore,
		"reserved cursor must exceed every events.id that existed at purge time")

	// Now create another issue and verify the next events.id is strictly
	// greater than the reserved cursor.
	keeper, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID, Title: "next", Author: "tester",
	})
	require.NoError(t, err)
	assert.Greater(t, evt.ID, *pl.PurgeResetAfterEventID,
		"next real events.id must continue from reserved+1")
	_ = keeper
}

func TestPurgeIssue_UnknownIssueIsErrNotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.PurgeIssue(context.Background(), 9999, "agent", nil)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}
```

- [ ] **Step 3: Run the tests (expect failure)**

Run: `go test ./internal/db/... -run TestPurgeIssue`
Expected: FAIL.

- [ ] **Step 4: Implement `PurgeIssue`**

Append to `internal/db/queries_delete.go`:

```go
// PurgeIssue runs the seven-step transaction from spec §3.5: capture event
// id range → cascade-delete dependents → bump sqlite_sequence to reserve a
// synthetic SSE cursor → insert purge_log row → delete issues row. Returns
// the inserted purge_log row. ErrNotFound when the issue doesn't exist.
//
// reason is optional; pass nil to leave purge_log.reason NULL.
//
// No issue.purged event is persisted; purge_log is the only audit record.
func (d *DB) PurgeIssue(ctx context.Context, issueID int64, actor string, reason *string) (PurgeLog, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return PurgeLog{}, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueIncludingDeleted(ctx, tx, issueID)
	if err != nil {
		return PurgeLog{}, err
	}

	// Step 2: capture min/max event ids attached to this issue.
	var (
		minEventID sql.NullInt64
		maxEventID sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT MIN(id), MAX(id) FROM events
		 WHERE issue_id = ? OR related_issue_id = ?`,
		issueID, issueID).Scan(&minEventID, &maxEventID); err != nil {
		return PurgeLog{}, fmt.Errorf("capture event id range: %w", err)
	}

	// Step 3: capture dependent counts.
	var commentCount, linkCount, labelCount, eventCount int64
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM comments WHERE issue_id = ?`, issueID).Scan(&commentCount); err != nil {
		return PurgeLog{}, fmt.Errorf("count comments: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		issueID, issueID).Scan(&linkCount); err != nil {
		return PurgeLog{}, fmt.Errorf("count links: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM issue_labels WHERE issue_id = ?`, issueID).Scan(&labelCount); err != nil {
		return PurgeLog{}, fmt.Errorf("count labels: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		issueID, issueID).Scan(&eventCount); err != nil {
		return PurgeLog{}, fmt.Errorf("count events: %w", err)
	}

	// Step 4: cascade-delete dependents in the order events → comments → links → issue_labels.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM events WHERE issue_id = ? OR related_issue_id = ?`,
		issueID, issueID); err != nil {
		return PurgeLog{}, fmt.Errorf("delete events: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM comments WHERE issue_id = ?`, issueID); err != nil {
		return PurgeLog{}, fmt.Errorf("delete comments: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM links WHERE from_issue_id = ? OR to_issue_id = ?`,
		issueID, issueID); err != nil {
		return PurgeLog{}, fmt.Errorf("delete links: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM issue_labels WHERE issue_id = ?`, issueID); err != nil {
		return PurgeLog{}, fmt.Errorf("delete labels: %w", err)
	}

	// Step 5: if any events were deleted, reserve a synthetic SSE cursor by
	// bumping sqlite_sequence.seq for events. Future inserts continue from
	// reserved+1 so the reserved value is unattainable.
	var reservedCursor sql.NullInt64
	if minEventID.Valid {
		var seq int64
		if err := tx.QueryRowContext(ctx,
			`SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq); err != nil {
			return PurgeLog{}, fmt.Errorf("read events seq: %w", err)
		}
		seq++
		if _, err := tx.ExecContext(ctx,
			`UPDATE sqlite_sequence SET seq = ? WHERE name = 'events'`, seq); err != nil {
			return PurgeLog{}, fmt.Errorf("bump events seq: %w", err)
		}
		reservedCursor = sql.NullInt64{Int64: seq, Valid: true}
	}

	// Step 6: insert purge_log row.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO purge_log(
			project_id, purged_issue_id, project_identity, issue_number,
			issue_title, issue_author, comment_count, link_count, label_count,
			event_count, events_deleted_min_id, events_deleted_max_id,
			purge_reset_after_event_id, actor, reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issue.ProjectID, issue.ID, projectIdentity, issue.Number,
		issue.Title, issue.Author, commentCount, linkCount, labelCount,
		eventCount, minEventID, maxEventID, reservedCursor, actor, reason)
	if err != nil {
		return PurgeLog{}, fmt.Errorf("insert purge_log: %w", err)
	}
	purgeLogID, err := res.LastInsertId()
	if err != nil {
		return PurgeLog{}, err
	}

	// Step 7: delete the issues row (this fires issues_ad_fts).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM issues WHERE id = ?`, issueID); err != nil {
		return PurgeLog{}, fmt.Errorf("delete issue: %w", err)
	}

	// Re-fetch the purge_log row inside the TX so the caller gets typed
	// nullable fields rather than re-converting nullable scalars.
	pl, err := scanPurgeLogTx(ctx, tx, purgeLogID)
	if err != nil {
		return PurgeLog{}, fmt.Errorf("re-fetch purge_log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PurgeLog{}, fmt.Errorf("commit: %w", err)
	}
	return pl, nil
}

func scanPurgeLogTx(ctx context.Context, tx *sql.Tx, id int64) (PurgeLog, error) {
	const q = `
		SELECT id, project_id, purged_issue_id, project_identity, issue_number,
		       issue_title, issue_author, comment_count, link_count, label_count,
		       event_count, events_deleted_min_id, events_deleted_max_id,
		       purge_reset_after_event_id, actor, reason, purged_at
		FROM purge_log WHERE id = ?`
	var (
		pl                                                 PurgeLog
		minID, maxID, resetCursor                          sql.NullInt64
		reason                                             sql.NullString
	)
	err := tx.QueryRowContext(ctx, q, id).Scan(
		&pl.ID, &pl.ProjectID, &pl.PurgedIssueID, &pl.ProjectIdentity, &pl.IssueNumber,
		&pl.IssueTitle, &pl.IssueAuthor, &pl.CommentCount, &pl.LinkCount, &pl.LabelCount,
		&pl.EventCount, &minID, &maxID, &resetCursor, &pl.Actor, &reason, &pl.PurgedAt)
	if err != nil {
		return PurgeLog{}, err
	}
	if minID.Valid {
		v := minID.Int64
		pl.EventsDeletedMinID = &v
	}
	if maxID.Valid {
		v := maxID.Int64
		pl.EventsDeletedMaxID = &v
	}
	if resetCursor.Valid {
		v := resetCursor.Int64
		pl.PurgeResetAfterEventID = &v
	}
	if reason.Valid {
		v := reason.String
		pl.Reason = &v
	}
	return pl, nil
}
```

Add `import "time"` to `internal/db/types.go` if it isn't already there (it is — Issue and Event already have time.Time fields).

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/db/... -run TestPurgeIssue`
Expected: PASS for all four subtests.

Run the full DB suite as a sanity check: `go test ./internal/db/...`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/types.go internal/db/queries_delete.go internal/db/queries_delete_test.go
git commit -m "feat(db): PurgeIssue with cascade + reserved SSE cursor + audit"
```

---

### Task 7: API DTOs — idempotency, force_new, search, destructive verbs

Spec refs: §4.1 (endpoint surface), §4.4 (`Idempotency-Key`, `X-Kata-Confirm` headers), §4.5 (`reused`/`original_event` extension to MutationResponse), §4.10 (search response), §6.1 (CLI flag → wire mapping), §3.6 (idempotency error codes).

Add the request/response shapes Plan 3's handlers need. Existing types are extended additively (no field renames), new types are added.

**Wire shape additions:**
- `CreateIssueRequest.IdempotencyKey` — read from the `Idempotency-Key` header, not the body. Huma `header:"Idempotency-Key"`.
- `CreateIssueRequest.Body.ForceNew` — body field `force_new`. Bypasses look-alike soft-block; idempotency still wins.
- `MutationResponse.Body.OriginalEvent *Event` — populated only on idempotent reuse so clients can correlate to the prior creation. Existing handlers leave it nil; `omitempty` keeps the wire shape backward-compatible.
- `SearchRequest`/`SearchResponse` — new.
- `DestructiveActionRequest` — body has `actor`; `Confirm` is the `X-Kata-Confirm` header (Huma `header:"X-Kata-Confirm"`). Used by /actions/delete and /actions/purge.
- `RestoreRequest` — body has `actor` only; no confirmation needed (restore is reversible).
- `ShowIssueRequest.IncludeDeleted` — query param. Default false; soft-deleted issues 404 unless explicitly requested.

**Files:**
- Modify: `internal/api/types.go`.

This task has no new tests — these are wire DTOs exercised by the handler tests in subsequent tasks. Lint (`make lint`) and `go build ./...` are the verification.

- [ ] **Step 1: Extend `MutationResponse`**

In `internal/api/types.go`, replace the existing `MutationResponse`:

```go
// MutationResponse is the standard mutation envelope (§4.5). OriginalEvent is
// non-nil only on idempotent reuse — the issue.created event row of the prior
// creation, so clients can correlate the reuse to the original mutation.
type MutationResponse struct {
	Body struct {
		Issue         db.Issue  `json:"issue"`
		Event         *db.Event `json:"event"`
		OriginalEvent *db.Event `json:"original_event,omitempty"`
		Changed       bool      `json:"changed"`
		Reused        bool      `json:"reused,omitempty"`
	}
}
```

- [ ] **Step 2: Extend `CreateIssueRequest`**

Replace:

```go
// CreateIssueRequest is POST /api/v1/projects/{id}/issues.
//
// IdempotencyKey is read from the Idempotency-Key HTTP header (spec §4.4).
// Body.ForceNew bypasses look-alike soft-block but is overridden by an
// idempotent match (idempotency wins per spec §3.7).
type CreateIssueRequest struct {
	ProjectID      int64  `path:"project_id" required:"true"`
	IdempotencyKey string `header:"Idempotency-Key,omitempty"`
	Body           struct {
		Actor    string                  `json:"actor" required:"true"`
		Title    string                  `json:"title" required:"true"`
		Body     string                  `json:"body,omitempty"`
		Owner    *string                 `json:"owner,omitempty"`
		Labels   []string                `json:"labels,omitempty"`
		Links    []CreateInitialLinkBody `json:"links,omitempty"`
		ForceNew bool                    `json:"force_new,omitempty"`
	}
}
```

- [ ] **Step 3: Extend `ShowIssueRequest`**

Replace:

```go
// ShowIssueRequest is GET /api/v1/projects/{id}/issues/{number}.
// IncludeDeleted=true allows fetching soft-deleted issues; default returns 404
// for them.
type ShowIssueRequest struct {
	ProjectID      int64 `path:"project_id" required:"true"`
	Number         int64 `path:"number" required:"true"`
	IncludeDeleted bool  `query:"include_deleted,omitempty"`
}
```

- [ ] **Step 4: Add destructive action requests**

Append to `internal/api/types.go`:

```go
// DestructiveActionRequest is POST /api/v1/projects/{id}/issues/{number}/actions/delete
// and .../actions/purge. Confirm is read from the X-Kata-Confirm header per
// spec §4.4 and must equal the exact strings "DELETE #N" / "PURGE #N".
type DestructiveActionRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Number    int64  `path:"number" required:"true"`
	Confirm   string `header:"X-Kata-Confirm,omitempty"`
	Body      struct {
		Actor  string `json:"actor" required:"true"`
		Reason string `json:"reason,omitempty"` // purge only; lands in purge_log.reason
	}
}

// RestoreRequest is POST /api/v1/projects/{id}/issues/{number}/actions/restore.
// No confirmation header — restore is reversible and idempotent.
type RestoreRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
	}
}

// PurgeResponse extends the standard envelope with the purge_log row so callers
// see the captured counts and reserved SSE cursor without a follow-up GET.
type PurgeResponse struct {
	Body struct {
		PurgeLog db.PurgeLog `json:"purge_log"`
	}
}
```

- [ ] **Step 5: Add search request/response**

Append:

```go
// SearchRequest is GET /api/v1/projects/{id}/search?q=...&limit=...&include_deleted=...
type SearchRequest struct {
	ProjectID      int64  `path:"project_id" required:"true"`
	Query          string `query:"q" required:"true"`
	Limit          int    `query:"limit,omitempty"`
	IncludeDeleted bool   `query:"include_deleted,omitempty"`
}

// SearchHit is one row in SearchResponse. Score is the negated raw BM25
// (higher = better match), MatchedIn is the FTS columns that contributed.
type SearchHit struct {
	Issue     db.Issue `json:"issue"`
	Score     float64  `json:"score"`
	MatchedIn []string `json:"matched_in"`
}

// SearchResponse mirrors spec §4.10.
type SearchResponse struct {
	Body struct {
		Query   string      `json:"query"`
		Results []SearchHit `json:"results"`
	}
}
```

- [ ] **Step 6: Build and lint**

Run: `go build ./... && make lint`
Expected: clean build, no lint errors.

- [ ] **Step 7: Commit**

```bash
git add internal/api/types.go
git commit -m "feat(api): DTOs for search, idempotency, destructive verbs"
```

---

### Task 8: `internal/daemon/handlers_search.go` — GET /search

Spec refs: §4.1 (`GET /api/v1/projects/{id}/search?q=...`), §4.10 (response shape).

The search handler is small: validate the query, look up the project, call `cfg.DB.SearchFTS`, transform rows to `SearchHit`. Score values come straight from `SearchFTS`; the daemon doesn't apply the similarity threshold here — search returns everything FTS matched, and the look-alike check at create-time (Task 9) is the only place the 0.7 threshold gates an action.

**Files:**
- Create: `internal/daemon/handlers_search.go`.
- Modify: `internal/daemon/server.go` — register the new handler group.
- Test: `internal/daemon/handlers_search_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// internal/daemon/handlers_search_test.go
package daemon_test

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchEndpoint_ReturnsHitsWithScores(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash on Safari"})
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "unrelated"})

	bs := getBody(t, ts, "/api/v1/projects/"+pidStr+"/search?q="+url.QueryEscape("login Safari"))
	assert.Contains(t, bs, `"query":"login Safari"`)
	assert.Contains(t, bs, `"title":"fix login crash on Safari"`)
	assert.Contains(t, bs, `"matched_in"`)
	assert.NotContains(t, bs, `"title":"unrelated"`,
		"unrelated issue should not appear in results")
}

func TestSearchEndpoint_EmptyQueryIsValidationError(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	resp, bs := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/search?q=")
	require.Equal(t, 400, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"validation"`)
}

func TestSearchEndpoint_UnknownProjectIs404(t *testing.T) {
	h, _ := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	resp, bs := getStatusBody(t, ts, "/api/v1/projects/9999/search?q=anything")
	require.Equal(t, 404, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"project_not_found"`)
}
```

If `getStatusBody` doesn't exist in `internal/daemon/testhelpers_test.go`, add it next to `getBody`:

```go
// getStatusBody is like getBody but returns the response so callers can assert
// on non-2xx status codes.
func getStatusBody(t *testing.T, ts *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, bs
}
```

If imports drift, add the `net/http`, `io`, `context` imports at the top of `testhelpers_test.go`.

- [ ] **Step 2: Run the test (expect failure)**

Run: `go test ./internal/daemon/... -run TestSearchEndpoint`
Expected: FAIL — handler not registered.

- [ ] **Step 3: Implement the handler**

```go
// internal/daemon/handlers_search.go
package daemon

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerSearchHandlers installs GET /api/v1/projects/{id}/search. Returns
// the spec §4.10 envelope: query echo + ranked results with score + matched_in.
func registerSearchHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "searchIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/search",
	}, func(ctx context.Context, in *api.SearchRequest) (*api.SearchResponse, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, api.NewError(400, "validation",
				"query parameter q must be non-empty", "", nil)
		}
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		candidates, err := cfg.DB.SearchFTS(ctx, in.ProjectID, in.Query, limit, in.IncludeDeleted)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		hits := make([]api.SearchHit, 0, len(candidates))
		for _, c := range candidates {
			hits = append(hits, api.SearchHit{
				Issue:     c.Issue,
				Score:     c.Score,
				MatchedIn: c.MatchedIn,
			})
		}
		out := &api.SearchResponse{}
		out.Body.Query = in.Query
		out.Body.Results = hits
		return out, nil
	})
}
```

- [ ] **Step 4: Register the handler**

In `internal/daemon/server.go`, add `registerSearch(humaAPI, cfg)` to the `registerRoutes` function (alongside `registerReady`):

```go
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
}

// registerSearch registers GET /projects/{id}/search.
func registerSearch(humaAPI huma.API, cfg ServerConfig) {
	registerSearchHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/daemon/... -run TestSearchEndpoint`
Expected: PASS for all three subtests.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_search.go internal/daemon/handlers_search_test.go internal/daemon/server.go internal/daemon/testhelpers_test.go
git commit -m "feat(daemon): GET /search with BM25 ranking"
```

---

### Task 9: extend `createIssue` handler — idempotency + look-alike soft-block

Spec refs: §3.6 (idempotency contract — match/mismatch/deleted), §3.7 (look-alike pipeline; idempotency wins over force_new), §4.5 (`reused`/`original_event` fields), §4.7 (status mapping for `idempotency_mismatch`/`idempotency_deleted`/`duplicate_candidates`), §6.4 (creation flow ordering).

The createIssue handler in `internal/daemon/handlers_issues.go` runs three checks in this exact order before reaching the existing DB call:

1. **Project exists.** (Already present — no change.)
2. **Idempotency.** If `IdempotencyKey != ""`:
   - Compute fingerprint over (title, body, owner, labels, links).
   - `LookupIdempotency` within the 7-day window.
   - On match:
     - Same fingerprint → fetch the issue:
       - If issue is soft-deleted → 409 `idempotency_deleted`.
       - Otherwise → return reuse envelope (`event=null, original_event=<found>, changed=false, reused=true`).
     - Different fingerprint → 409 `idempotency_mismatch`.
3. **Look-alike soft-block.** If `!ForceNew` and idempotency didn't reuse:
   - Build `q = title + " " + body` (no body → just title).
   - `SearchFTS(projectID, q, 20, includeDeleted=false)`.
   - For each candidate, compute `similarity.Score`.
   - If any candidate ≥ 0.7 → 409 `duplicate_candidates` with the matched candidates in `data.candidates`.

After the three checks, the existing CreateIssue call runs with one tweak: `idempotency_key` and `idempotency_fingerprint` are folded into the issue.created event payload when the key was provided. That requires extending `CreateIssueParams` to carry these two fields, and `buildCreatedPayload` in `queries.go` to emit them.

**Why idempotency runs before look-alike:** spec §3.7 — "Idempotency wins over force_new (idempotent reuse never emits a duplicate even if force_new is set)." Implementation: idempotency check returns early on a match; otherwise we fall through to the look-alike check which respects `force_new`.

**Files:**
- Modify: `internal/db/queries.go` — extend `CreateIssueParams`, extend `buildCreatedPayload`.
- Modify: `internal/daemon/handlers_issues.go` — add the idempotency + look-alike pre-checks.
- Test: extend `internal/daemon/handlers_issues_test.go`.

- [ ] **Step 1: Extend `CreateIssueParams` and `buildCreatedPayload`**

In `internal/db/queries.go`, replace `CreateIssueParams`:

```go
// CreateIssueParams carries inputs for CreateIssue.
type CreateIssueParams struct {
	ProjectID int64
	Title     string
	Body      string
	Author    string

	Labels []string
	Links  []InitialLink
	Owner  *string

	// Optional. When non-empty, both fields are folded into the issue.created
	// event payload so future LookupIdempotency calls can find the row via
	// idx_events_idempotency.
	IdempotencyKey         string
	IdempotencyFingerprint string
}
```

Replace `buildCreatedPayload`:

```go
// buildCreatedPayload returns the issue.created event payload as JSON. Empty
// initial state → "{}". Otherwise emits keys for whichever components are set,
// preserving determinism (sorted labels) so events are byte-stable.
func buildCreatedPayload(labels []string, links []InitialLink, owner *string, idempotencyKey, idempotencyFingerprint string) string {
	type linkOut struct {
		Type     string `json:"type"`
		ToNumber int64  `json:"to_number"`
	}
	type out struct {
		Labels                 []string  `json:"labels,omitempty"`
		Links                  []linkOut `json:"links,omitempty"`
		Owner                  string    `json:"owner,omitempty"`
		IdempotencyKey         string    `json:"idempotency_key,omitempty"`
		IdempotencyFingerprint string    `json:"idempotency_fingerprint,omitempty"`
	}
	var o out
	if len(labels) > 0 {
		o.Labels = labels
	}
	if len(links) > 0 {
		o.Links = make([]linkOut, 0, len(links))
		for _, l := range links {
			o.Links = append(o.Links, linkOut(l))
		}
	}
	if owner != nil {
		o.Owner = *owner
	}
	o.IdempotencyKey = idempotencyKey
	o.IdempotencyFingerprint = idempotencyFingerprint
	bs, err := json.Marshal(o)
	if err != nil {
		return "{}"
	}
	return string(bs)
}
```

Update the call site in `CreateIssue` to pass the new fields:

```go
payload := buildCreatedPayload(labels, links, owner, p.IdempotencyKey, p.IdempotencyFingerprint)
```

- [ ] **Step 2: Write the failing handler tests**

Append to `internal/daemon/handlers_issues_test.go`:

```go
func TestCreate_IdempotencyReuse_SameFingerprint(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	first := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K1"},
		map[string]any{"actor": "agent", "title": "fix login", "body": "details"})
	require.Equal(t, 200, first.status, string(first.body))
	assert.Contains(t, string(first.body), `"changed":true`)
	assert.Contains(t, string(first.body), `"reused":false`, "first call must not be reused")

	second := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K1"},
		map[string]any{"actor": "agent", "title": "fix login", "body": "details"})
	require.Equal(t, 200, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"reused":true`)
	assert.Contains(t, string(second.body), `"changed":false`)
	assert.Contains(t, string(second.body), `"original_event"`,
		"reuse envelope must echo the original event")
}

func TestCreate_IdempotencyMismatch(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K2"},
		map[string]any{"actor": "agent", "title": "fix login", "body": "details"}))

	// Different title under the same key → fingerprint diverges.
	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K2"},
		map[string]any{"actor": "agent", "title": "different title", "body": "details"})
	require.Equal(t, 409, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"idempotency_mismatch"`)
}

func TestCreate_LookalikeSoftBlock(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	requireOK(t, postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash on Safari",
			"body": "stack trace details"}))

	// Re-create with near-identical title + body — should soft-block.
	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash Safari",
			"body": "stack trace details"})
	require.Equal(t, 409, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"duplicate_candidates"`)
	assert.Contains(t, string(bs), `"candidates"`)
}

func TestCreate_ForceNewBypassesLookalike(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	requireOK(t, postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash on Safari",
			"body": "stack trace details"}))

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash Safari",
			"body": "stack trace details", "force_new": true})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":true`)
}

func TestCreate_IdempotencyWinsOverForceNew(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)

	first := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K3"},
		map[string]any{"actor": "agent", "title": "fix login", "body": "details", "force_new": true})
	require.Equal(t, 200, first.status, string(first.body))

	// Same key + same fingerprint, BUT force_new=true. Idempotency wins:
	// reuse envelope, no second issue created.
	second := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "K3"},
		map[string]any{"actor": "agent", "title": "fix login", "body": "details", "force_new": true})
	require.Equal(t, 200, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"reused":true`)
}
```

If `postWithHeader` doesn't exist, add it to `internal/daemon/testhelpers_test.go`:

```go
type httpResp struct {
	status int
	body   []byte
}

// postWithHeader is like postJSON but allows setting custom headers.
func postWithHeader(t *testing.T, ts *httptest.Server, path string, headers map[string]string, body any) httpResp {
	t.Helper()
	bs, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		ts.URL+path, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return httpResp{status: resp.StatusCode, body: out}
}

// requireOK accepts the new httpResp shape too.
func requireOK(t *testing.T, r httpResp) {
	t.Helper()
	require.Equalf(t, 200, r.status, "expected 200, got %d: %s", r.status, string(r.body))
}
```

If `requireOK` already takes `(*http.Response, []byte)`, keep both overloads — or rename the new helper `requireOKResp` and call it where needed. Either is fine; pick the lower-friction option.

- [ ] **Step 3: Run the tests (expect failure)**

Run: `go test ./internal/daemon/... -run "TestCreate_(Idempotency|Lookalike|ForceNew)"`
Expected: FAIL — handler doesn't yet do the checks.

- [ ] **Step 4: Implement idempotency + look-alike in `createIssue`**

Replace the body of the createIssue closure in `internal/daemon/handlers_issues.go`:

```go
func registerIssuesHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.CreateIssueRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		links := make([]db.InitialLink, 0, len(in.Body.Links))
		for _, l := range in.Body.Links {
			links = append(links, db.InitialLink{Type: l.Type, ToNumber: l.ToNumber})
		}

		// Idempotency check (spec §3.6). Wins over force_new per §3.7.
		var idempotencyFingerprint string
		if in.IdempotencyKey != "" {
			idempotencyFingerprint = db.Fingerprint(in.Body.Title, in.Body.Body, in.Body.Owner, in.Body.Labels, links)
			since := time.Now().Add(-idempotencyWindow)
			match, err := cfg.DB.LookupIdempotency(ctx, in.ProjectID, in.IdempotencyKey, since)
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			if match != nil {
				if match.Fingerprint != idempotencyFingerprint {
					return nil, api.NewError(409, "idempotency_mismatch",
						"idempotency key matched a prior issue with a different fingerprint",
						"either use a fresh key, or send the exact same fields as the original",
						map[string]any{"original_issue_number": match.IssueNumber})
				}
				existing, err := cfg.DB.IssueByID(ctx, match.IssueID)
				if err != nil {
					return nil, api.NewError(500, "internal", err.Error(), "", nil)
				}
				if existing.DeletedAt != nil {
					return nil, api.NewError(409, "idempotency_deleted",
						"idempotency key matched a soft-deleted issue",
						"run `kata restore "+formatNumber(existing.Number)+"` or use a fresh key",
						map[string]any{"original_issue_number": existing.Number})
				}
				out := &api.MutationResponse{}
				out.Body.Issue = existing
				out.Body.Event = nil
				origCopy := match.Event
				out.Body.OriginalEvent = &origCopy
				out.Body.Changed = false
				out.Body.Reused = true
				return out, nil
			}
		}

		// Look-alike soft-block (spec §3.7). Skipped on force_new.
		if !in.Body.ForceNew {
			candidates, err := cfg.DB.SearchFTS(ctx, in.ProjectID,
				strings.TrimSpace(in.Body.Title+" "+in.Body.Body), 20, false)
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			matched := []map[string]any{}
			for _, c := range candidates {
				score := similarity.Score(in.Body.Title, in.Body.Body, c.Issue.Title, c.Issue.Body)
				if score >= similarityThreshold {
					matched = append(matched, map[string]any{
						"number": c.Issue.Number,
						"title":  c.Issue.Title,
						"score":  score,
					})
				}
			}
			if len(matched) > 0 {
				return nil, api.NewError(409, "duplicate_candidates",
					formatDuplicateMessage(matched),
					"comment on an existing issue, or pass force_new=true to create anyway",
					map[string]any{"candidates": matched})
			}
		}

		issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID:              in.ProjectID,
			Title:                  in.Body.Title,
			Body:                   in.Body.Body,
			Author:                 in.Body.Actor,
			Owner:                  in.Body.Owner,
			Labels:                 in.Body.Labels,
			Links:                  links,
			IdempotencyKey:         in.IdempotencyKey,
			IdempotencyFingerprint: idempotencyFingerprint,
		})
		switch {
		case errors.Is(err, db.ErrInitialLinkInvalidType):
			return nil, api.NewError(400, "validation",
				"link.type must be parent|blocks|related", "", nil)
		case errors.Is(err, db.ErrInitialLinkTargetNotFound):
			return nil, api.NewError(404, "issue_not_found",
				"initial link target not found in this project", "", nil)
		case errors.Is(err, db.ErrSelfLink):
			return nil, api.NewError(400, "validation",
				"cannot link an issue to itself", "", nil)
		case errors.Is(err, db.ErrLabelInvalid):
			return nil, api.NewError(400, "validation",
				"label must match charset [a-z0-9._:-] and length 1..64", "", nil)
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"duplicate parent in initial links", "pass at most one parent link", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})

	// (listIssues, showIssue, editIssue handlers unchanged — keep their existing
	// definitions below.)
}
```

Add the constants and helpers at the bottom of the file:

```go
const (
	// idempotencyWindow is the 7-day lookback per spec §3.6.
	idempotencyWindow = 7 * 24 * time.Hour

	// similarityThreshold is the soft-block trigger per spec §3.7.
	similarityThreshold = 0.7
)

// formatNumber returns the issue number as a decimal string, used in hint
// strings without pulling in fmt.Sprintf for one int.
func formatNumber(n int64) string {
	return strconv.FormatInt(n, 10)
}

// formatDuplicateMessage builds the message for the duplicate_candidates error.
// Plural-aware so a single-candidate match doesn't read "1 issues match".
func formatDuplicateMessage(matched []map[string]any) string {
	n := len(matched)
	if n == 1 {
		return "1 open issue matches this title"
	}
	return strconv.Itoa(n) + " open issues match this title"
}
```

Update imports at the top of the file:

```go
import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/similarity"
)
```

Update `internal/daemon/handlers_issues.go`'s `showIssue` handler so deleted issues 404 unless `IncludeDeleted=true`. Replace the relevant block:

```go
huma.Register(humaAPI, huma.Operation{
	OperationID: "showIssue",
	Method:      "GET",
	Path:        "/api/v1/projects/{project_id}/issues/{number}",
}, func(ctx context.Context, in *api.ShowIssueRequest) (*api.ShowIssueResponse, error) {
	issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
	if errors.Is(err, db.ErrNotFound) {
		return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
	}
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	if issue.DeletedAt != nil && !in.IncludeDeleted {
		return nil, api.NewError(404, "issue_not_found",
			"issue not found",
			"pass include_deleted=true to view soft-deleted issues",
			nil)
	}
	// (rest of the handler unchanged: comments, links, labels, response wiring)
	// ...
})
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/daemon/... -run "TestCreate_(Idempotency|Lookalike|ForceNew)"`
Expected: PASS for all five subtests.

Run the broader create-issue suite to confirm no regression: `go test ./internal/daemon/... -run TestCreate`
Expected: all green (idempotency-key-absent path still works, look-alike doesn't trigger when nothing matches, etc).

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/queries.go internal/daemon/handlers_issues.go internal/daemon/handlers_issues_test.go internal/daemon/testhelpers_test.go
git commit -m "feat(daemon): idempotency + look-alike soft-block at create"
```

---

### Task 10: `internal/daemon/handlers_destructive.go` — delete + restore + purge

Spec refs: §3.5 (destructive ladder), §4.1 (action endpoints), §4.4 (X-Kata-Confirm header), §4.7 (412 → exit 6).

Three POST endpoints:

- `POST /actions/delete` — soft delete. Requires `X-Kata-Confirm: DELETE #N`. Returns MutationResponse.
- `POST /actions/restore` — undo soft delete. No confirm header. Returns MutationResponse.
- `POST /actions/purge` — hard delete. Requires `X-Kata-Confirm: PURGE #N`. Returns PurgeResponse with the purge_log row.

**Confirm header validation:** the daemon validates the header equals the exact required string for the issue's number. Missing header → 412 `confirm_required`. Wrong value → 412 `confirm_mismatch`.

**Restore lookup must use IncludeDeleted semantics:** the issue is presumed deleted at the time of restore, so the handler looks up via `IssueByNumber` (which doesn't filter deleted) and then asserts the row exists. The DB-layer RestoreIssue handles the no-op-when-not-deleted case.

**Files:**
- Create: `internal/daemon/handlers_destructive.go`.
- Modify: `internal/daemon/server.go` — register the new handler group.
- Test: `internal/daemon/handlers_destructive_test.go`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/daemon/handlers_destructive_test.go
package daemon_test

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelete_RequiresConfirmHeader(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		nil, // no header
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_required"`)
}

func TestDelete_RejectsWrongConfirmValue(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #2"}, // wrong number
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_mismatch"`)
}

func TestDelete_AcceptsCorrectConfirmAndSoftDeletes(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"changed":true`)
	assert.Contains(t, string(resp.body), `"issue.soft_deleted"`)

	// show without include_deleted now 404s.
	respShow, bs := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1")
	require.Equal(t, 404, respShow.StatusCode, string(bs))
}

func TestDelete_AlreadyDeletedIsNoOp(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})
	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"}))

	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"changed":false`)
	assert.Contains(t, string(resp.body), `"event":null`)
}

func TestRestore_ClearsDeletedAt(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "x"})
	requireOK(t, postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"}))

	resp, bs := postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/restore",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":true`)
	assert.Contains(t, string(bs), `"issue.restored"`)

	// show without include_deleted works again.
	respShow, bsShow := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1")
	require.Equal(t, 200, respShow.StatusCode, string(bsShow))
}

func TestPurge_RequiresConfirmHeaderAndRemovesAllRows(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	pidStr := strconv.FormatInt(pid, 10)
	_, _ = postJSON(t, ts, "/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "purge me"})

	// Missing header → 412.
	resp := postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		nil, map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_required"`)

	// Wrong header → 412 confirm_mismatch.
	resp = postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 412, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"confirm_mismatch"`)

	// Correct header → 200 with purge_log.
	resp = postWithHeader(t, ts, "/api/v1/projects/"+pidStr+"/issues/1/actions/purge",
		map[string]string{"X-Kata-Confirm": "PURGE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.status, string(resp.body))
	assert.Contains(t, string(resp.body), `"purge_log"`)
	assert.Contains(t, string(resp.body), `"purged_issue_id"`)

	// Subsequent show 404s — issue is gone.
	respShow, _ := getStatusBody(t, ts, "/api/v1/projects/"+pidStr+"/issues/1?include_deleted=true")
	assert.Equal(t, 404, respShow.StatusCode)
}
```

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./internal/daemon/... -run "TestDelete|TestRestore|TestPurge"`
Expected: FAIL — handlers not registered.

- [ ] **Step 3: Implement the handlers**

```go
// internal/daemon/handlers_destructive.go
package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// registerDestructiveHandlers installs /actions/delete, /actions/restore, and
// /actions/purge. Delete and purge gate on the X-Kata-Confirm header per spec
// §4.4 / §3.5. Restore is reversible and idempotent so it ships unguarded.
func registerDestructiveHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "deleteIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/delete",
	}, func(ctx context.Context, in *api.DestructiveActionRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		if err := validateConfirm(in.Confirm, "DELETE", in.Number); err != nil {
			return nil, err
		}
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.SoftDeleteIssue(ctx, issue.ID, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "restoreIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/restore",
	}, func(ctx context.Context, in *api.RestoreRequest) (*api.MutationResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.RestoreIssue(ctx, issue.ID, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "purgeIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/purge",
	}, func(ctx context.Context, in *api.DestructiveActionRequest) (*api.PurgeResponse, error) {
		if err := validateActor(in.Body.Actor); err != nil {
			return nil, err
		}
		if err := validateConfirm(in.Confirm, "PURGE", in.Number); err != nil {
			return nil, err
		}
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		var reasonPtr *string
		if in.Body.Reason != "" {
			r := in.Body.Reason
			reasonPtr = &r
		}
		pl, err := cfg.DB.PurgeIssue(ctx, issue.ID, in.Body.Actor, reasonPtr)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.PurgeResponse{}
		out.Body.PurgeLog = pl
		return out, nil
	})
}

// validateConfirm checks an X-Kata-Confirm header against the verb-specific
// expected value ("DELETE #N" or "PURGE #N"). Missing header → confirm_required;
// wrong value → confirm_mismatch.
func validateConfirm(got, verb string, number int64) error {
	expected := fmt.Sprintf("%s #%d", verb, number)
	if got == "" {
		return api.NewError(412, "confirm_required",
			"this action requires X-Kata-Confirm",
			"set the header to "+expected, nil)
	}
	if got != expected {
		return api.NewError(412, "confirm_mismatch",
			"X-Kata-Confirm header value does not match",
			"expected "+expected, nil)
	}
	return nil
}
```

- [ ] **Step 4: Register the handler group**

In `internal/daemon/server.go`, add `registerDestructive(humaAPI, cfg)` to `registerRoutes`:

```go
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

// registerDestructive registers /actions/delete, /actions/restore, /actions/purge.
func registerDestructive(humaAPI huma.API, cfg ServerConfig) {
	registerDestructiveHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./internal/daemon/... -run "TestDelete|TestRestore|TestPurge"`
Expected: PASS for all six subtests.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_destructive.go internal/daemon/handlers_destructive_test.go internal/daemon/server.go
git commit -m "feat(daemon): /actions/delete, /actions/restore, /actions/purge"
```

---

### Task 11: `cmd/kata/delete.go` — `kata delete <number> --force [--confirm "DELETE #N"]`

Spec refs: §3.5 (delete ladder), §5.5 (skill rule for agents — always pass `--confirm` when scripted), §6.1 (CLI surface).

**CLI behavior:**
- `kata delete <number>` (no flags) → exit 3 `validation` with hint *"deletion requires --force; use `kata restore` to undo if you change your mind."*
- `kata delete <number> --force --confirm "DELETE #N"` → noninteractive; sends X-Kata-Confirm header.
- `kata delete <number> --force` (TTY) → prompts for the issue number; if input matches, builds `DELETE #N` and sends.
- `kata delete <number> --force` (no TTY, no `--confirm`) → exit 6 `confirm_required`.

The CLI translates the user's interactive input (just the number) into the wire-required string (`DELETE #N`); spec §3.5 calls this out explicitly: "interactive prompt requires typing the issue number" but the wire form is always the full string per §4.4.

**TTY detection** uses `(*os.File).Stat().Mode() & os.ModeCharDevice` — a small, dependency-free check. We test the `--force --confirm` path only; the interactive prompt path is covered by manual smoke testing during the final tidy task.

**Files:**
- Create: `cmd/kata/delete.go`.
- Modify: `cmd/kata/main.go` — register the new command.
- Test: `cmd/kata/delete_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/kata/delete_test.go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestDelete_NoForceIsValidationError(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "to be deleted")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "delete", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
	assert.Contains(t, ce.Message, "--force")
}

func TestDelete_ForceWithConfirmSoftDeletes(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "to be deleted")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "delete", "1", "--force", "--confirm", "DELETE #1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "deleted")
}

func TestDelete_ConfirmMismatchIsExit6(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "to be deleted")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "delete", "1", "--force", "--confirm", "DELETE #2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitConfirm, ce.ExitCode)
	assert.True(t, strings.Contains(ce.Code, "confirm_mismatch"))
}
```

`createIssueViaHTTP` is a small helper used here and in subsequent tests — add it to `cmd/kata/testhelpers_test.go` if not already present:

```go
// createIssueViaHTTP creates an issue in dir's project via the testenv daemon.
// Returns the issue number from the response.
func createIssueViaHTTP(t *testing.T, env *testenv.Env, dir, title string) int64 {
	t.Helper()
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	body, err := json.Marshal(map[string]any{"actor": "tester", "title": title})
	require.NoError(t, err)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues",
		"application/json", bytes.NewReader(body)) //nolint:gosec,noctx
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var b struct{ Issue struct{ Number int64 } `json:"issue"` }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&b))
	return b.Issue.Number
}
```

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./cmd/kata/... -run TestDelete`
Expected: FAIL — command not registered.

- [ ] **Step 3: Implement `delete.go`**

```go
// cmd/kata/delete.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newDeleteCmd() *cobra.Command {
	var force bool
	var confirm string
	cmd := &cobra.Command{
		Use:   "delete <number>",
		Short: "soft-delete an issue (reversible via kata restore)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			if !force {
				return &cliError{
					Message:  "deletion requires --force; use `kata restore` to undo if you change your mind",
					Code:     "validation",
					ExitCode: ExitValidation,
				}
			}
			expected := fmt.Sprintf("DELETE #%d", n)
			confirm, err = resolveConfirm(cmd, confirm, expected)
			if err != nil {
				return err
			}
			return runDestructive(cmd, n, "delete", confirm, nil)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to perform the soft delete")
	cmd.Flags().StringVar(&confirm, "confirm", "", `exact confirmation string ("DELETE #N")`)
	return cmd
}

// resolveConfirm returns the X-Kata-Confirm value the daemon expects:
//   - if --confirm was passed, use it as-is (the daemon validates exact match);
//   - otherwise, if stdin is a TTY, prompt for the issue number and build the
//     full string;
//   - otherwise, exit 6 confirm_required.
func resolveConfirm(cmd *cobra.Command, flagVal, expected string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if !isTTY(os.Stdin) {
		return "", &cliError{
			Message:  "no TTY: pass --confirm \"" + expected + "\" to proceed noninteractively",
			Code:     "confirm_required",
			ExitCode: ExitConfirm,
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Type the issue number to confirm: ")
	r := bufio.NewReader(cmd.InOrStdin())
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	verb, _, _ := strings.Cut(expected, " ")
	num := strings.TrimPrefix(strings.SplitN(expected, "#", 2)[1], "")
	if line != num {
		return "", &cliError{
			Message:  "confirmation input did not match issue number",
			Code:     "confirm_mismatch",
			ExitCode: ExitConfirm,
		}
	}
	return verb + " #" + num, nil
}

// runDestructive POSTs to /actions/{verb} with the X-Kata-Confirm header. Used
// by both delete and purge. Verb-specific success printing is handled here so
// the caller doesn't repeat scaffolding.
func runDestructive(cmd *cobra.Command, number int64, verb, confirm string, extraBody map[string]any) error {
	ctx := cmd.Context()
	start, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return err
	}
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	pid, err := resolveProjectID(ctx, baseURL, start)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	body := map[string]any{"actor": actor}
	for k, v := range extraBody {
		body[k] = v
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/%s", baseURL, pid, number, verb)
	status, bs, err := httpDoJSONWithHeader(ctx, baseURL, http.MethodPost, url,
		map[string]string{"X-Kata-Confirm": confirm}, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	if !flags.Quiet {
		switch verb {
		case "delete":
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d deleted (use `kata restore %d` to undo)\n", number, number)
		case "purge":
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d purged (irreversible)\n", number)
		}
	}
	return err
}

// httpDoJSONWithHeader is httpDoJSON + extra headers. Defined here so delete/
// purge don't reach into helpers.go to extend the existing signature.
func httpDoJSONWithHeader(ctx context.Context, baseURL, method, url string,
	headers map[string]string, body any) (int, []byte, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, nil, err
	}
	var rdr *strings.Reader
	if body != nil {
		bs, err := jsonMarshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = strings.NewReader(string(bs))
	}
	var req *http.Request
	if rdr != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, rdr)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req) //nolint:gosec // baseURL comes from our own runtime file
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := readAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, out, nil
}

// jsonMarshal wraps encoding/json.Marshal so we don't pull a fresh import
// path into delete.go just for one call.
func jsonMarshal(v any) ([]byte, error) {
	return jsonImpl.Marshal(v)
}

// readAll wraps io.ReadAll, same reason.
func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	return ioImpl.ReadAll(struct{ ReaderShim }{ReaderShim{r: r}})
}

// ReaderShim adapts a Read-only interface to io.Reader for ioImpl.ReadAll.
type ReaderShim struct {
	r interface{ Read(p []byte) (int, error) }
}

func (s ReaderShim) Read(p []byte) (int, error) { return s.r.Read(p) }
```

The shim/import indirection in the last block exists because `cmd/kata/delete.go` would otherwise need to introduce `encoding/json` and `io` imports that are already imported elsewhere in the package. **Simpler alternative for the implementer:** just import `encoding/json` and `io` directly at the top of delete.go and drop the shim — the indirection above is illustrative of the constraints, not the recommended final shape. Use:

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)
```

…and replace `jsonMarshal` / `readAll` / `ReaderShim` with direct calls to `json.Marshal` / `io.ReadAll` and use `bytes.NewReader` for the body. The plan's runtime behavior is unchanged.

Add the `isTTY` helper next to detachChild in `cmd/kata/client_unix.go` / `client_windows.go` (or create a new platform-shared file). The simplest cross-platform approach:

```go
// cmd/kata/tty.go
package main

import "os"

// isTTY reports whether f is a terminal device. Used to gate interactive
// prompts in delete/purge.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
```

- [ ] **Step 4: Register `newDeleteCmd` in main.go**

In `cmd/kata/main.go`, append `newDeleteCmd()` to the `subs` slice (after `newReopenCmd()` so the help output groups lifecycle verbs together):

```go
subs := []*cobra.Command{
	// ... existing ...
	newReopenCmd(),
	newDeleteCmd(),
	newRestoreCmd(),
	newPurgeCmd(),
	newSearchCmd(),
	// ... existing ...
}
```

(The other three are registered in their own tasks; this single edit keeps the subs slice in one place per pass.)

- [ ] **Step 5: Run the tests (expect pass)**

Run: `go test ./cmd/kata/... -run TestDelete`
Expected: PASS for all three subtests.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/delete.go cmd/kata/delete_test.go cmd/kata/main.go cmd/kata/testhelpers_test.go cmd/kata/tty.go
git commit -m "feat(cli): kata delete with --force + --confirm"
```

---

### Task 12: `cmd/kata/restore.go` — `kata restore <number>`

Spec refs: §3.5 (step 4), §6.1.

Restore is the simplest of the destructive verbs: no `--force`, no `--confirm`, no TTY prompt. POSTs to `/actions/restore` with just an actor.

**Files:**
- Create: `cmd/kata/restore.go`.
- Test: `cmd/kata/restore_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/kata/restore_test.go
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestRestore_ClearsDeletedAt(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "delete me")

	// Soft delete via the CLI first.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"--workspace", dir, "delete", "1", "--force", "--confirm", "DELETE #1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	// Then restore.
	resetFlags(t)
	cmd = newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "restore", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "restored")
}
```

- [ ] **Step 2: Run the test (expect failure)**

Run: `go test ./cmd/kata/... -run TestRestore`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/restore.go
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <number>",
		Short: "restore a soft-deleted issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/restore", baseURL, pid, n),
				map[string]any{"actor": actor})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if !flags.Quiet && !flags.JSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d restored\n", n)
				return err
			}
			return printMutation(cmd, bs)
		},
	}
}
```

- [ ] **Step 4: Run the test (expect pass)**

Run: `go test ./cmd/kata/... -run TestRestore`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/restore.go cmd/kata/restore_test.go
git commit -m "feat(cli): kata restore"
```

---

### Task 13: `cmd/kata/purge.go` — `kata purge <number> --force [--confirm "PURGE #N"]`

Spec refs: §3.5 (step 5 — irreversible), §5.5 (agent skill rule), §6.1.

Mirrors delete in shape: no flags → exit 3, `--force` required, `--confirm "PURGE #N"` (or TTY prompt) required, sends X-Kata-Confirm header. The differences from delete:

- The expected confirmation string is `PURGE #N`, not `DELETE #N`.
- The wire response carries the `purge_log` row instead of a MutationResponse. Plan 3's CLI prints a one-liner; the JSON path emits the full response.
- The interactive prompt requires the user to type `PURGE #N` exactly (per §3.5: "interactive prompt requires typing exactly `PURGE #N`"). Delete only required the bare number — the friction asymmetry is intentional.

**Files:**
- Create: `cmd/kata/purge.go`.
- Test: `cmd/kata/purge_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/kata/purge_test.go
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestPurge_NoForceIsValidationError(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "vaporize")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--workspace", dir, "purge", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
}

func TestPurge_ForceWithConfirmRemovesEverything(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "vaporize")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "purge", "1", "--force", "--confirm", "PURGE #1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "purged")
}
```

- [ ] **Step 2: Run the test (expect failure)**

Run: `go test ./cmd/kata/... -run TestPurge`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/purge.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newPurgeCmd() *cobra.Command {
	var force bool
	var confirm string
	cmd := &cobra.Command{
		Use:   "purge <number>",
		Short: "irreversibly remove an issue + all its rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			if !force {
				return &cliError{
					Message:  "purge requires --force; this is irreversible",
					Code:     "validation",
					ExitCode: ExitValidation,
				}
			}
			expected := fmt.Sprintf("PURGE #%d", n)
			confirm, err = resolvePurgeConfirm(cmd, confirm, expected)
			if err != nil {
				return err
			}
			return runDestructive(cmd, n, "purge", confirm, nil)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to perform the purge")
	cmd.Flags().StringVar(&confirm, "confirm", "", `exact confirmation string ("PURGE #N")`)
	return cmd
}

// resolvePurgeConfirm is like resolveConfirm (delete.go) but the interactive
// prompt requires the full "PURGE #N" string per spec §3.5.
func resolvePurgeConfirm(cmd *cobra.Command, flagVal, expected string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if !isTTY(os.Stdin) {
		return "", &cliError{
			Message:  "no TTY: pass --confirm \"" + expected + "\" to proceed noninteractively",
			Code:     "confirm_required",
			ExitCode: ExitConfirm,
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Type %q to confirm: ", expected)
	r := bufio.NewReader(cmd.InOrStdin())
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line != expected {
		return "", &cliError{
			Message:  "confirmation input did not match",
			Code:     "confirm_mismatch",
			ExitCode: ExitConfirm,
		}
	}
	return expected, nil
}
```

- [ ] **Step 4: Run the tests (expect pass)**

Run: `go test ./cmd/kata/... -run TestPurge`
Expected: PASS for both subtests.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/purge.go cmd/kata/purge_test.go
git commit -m "feat(cli): kata purge with --force + --confirm \"PURGE #N\""
```

---

### Task 14: `cmd/kata/search.go` — `kata search <query>`

Spec refs: §4.10 (search response), §5.3 (skill rule — always search before create), §6.1.

Calls GET /search, prints either the JSON envelope (with `--json`) or a human-readable list of `#N <title> [score]` lines. `--limit` (default 20). `--include-deleted` for completeness.

**Files:**
- Create: `cmd/kata/search.go`.
- Test: `cmd/kata/search_test.go`.

- [ ] **Step 1: Write the failing test**

```go
// cmd/kata/search_test.go
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestSearch_ReturnsMatchedIssues(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "fix login crash on Safari")
	createIssueViaHTTP(t, env, dir, "unrelated issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "search", "login Safari"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "fix login crash on Safari")
	assert.NotContains(t, buf.String(), "unrelated issue")
}

func TestSearch_EmptyQueryIsValidationError(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--workspace", dir, "search", "  "})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	err := cmd.Execute()
	require.Error(t, err)
	var ce *cliError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, ExitValidation, ce.ExitCode)
}
```

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./cmd/kata/... -run TestSearch`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/search.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newSearchCmd() *cobra.Command {
	var limit int
	var includeDeleted bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "search issues by title/body/comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			if strings.TrimSpace(query) == "" {
				return &cliError{Message: "query must be non-empty", ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("q", query)
			if limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			if includeDeleted {
				q.Set("include_deleted", "true")
			}
			searchURL := fmt.Sprintf("%s/api/v1/projects/%d/search?%s", baseURL, pid, q.Encode())
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, searchURL, nil)
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
			var b struct {
				Results []struct {
					Issue struct {
						Number int64  `json:"number"`
						Title  string `json:"title"`
						Status string `json:"status"`
					} `json:"issue"`
					Score     float64  `json:"score"`
					MatchedIn []string `json:"matched_in"`
				} `json:"results"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			if len(b.Results) == 0 {
				if !flags.Quiet {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), "no matches")
					return err
				}
				return nil
			}
			for _, r := range b.Results {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "#%-4d  %.2f  %-8s  %s  (%s)\n",
					r.Issue.Number, r.Score, r.Issue.Status, r.Issue.Title,
					strings.Join(r.MatchedIn, ",")); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted issues")
	return cmd
}
```

- [ ] **Step 4: Run the tests (expect pass)**

Run: `go test ./cmd/kata/... -run TestSearch`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/search.go cmd/kata/search_test.go
git commit -m "feat(cli): kata search with BM25-ranked results"
```

---

### Task 15: extend `cmd/kata/create.go` — `--idempotency-key` + `--force-new`

Spec refs: §3.6 (idempotency contract on the wire), §3.7 (force_new bypass), §6.1 (CLI flags).

Two new flags on `kata create`:
- `--idempotency-key <K>` — sent as the `Idempotency-Key` HTTP header on the POST.
- `--force-new` — sent as `force_new: true` in the body. Bypasses look-alike soft-block (idempotency still wins).

The CLI still uses `httpDoJSON` for the no-header case; for the new key path we call `httpDoJSONWithHeader` (the helper added in Task 11 — assuming the implementer kept it; otherwise inline the request construction).

**Files:**
- Modify: `cmd/kata/create.go` — add flags, route to header on the POST.
- Test: `cmd/kata/create_test.go` — add idempotency tests.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/kata/create_test.go`:

```go
func TestCreate_WithIdempotencyKeyReusesOnRepeat(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	// First call.
	cmd := newRootCmd()
	var buf1 bytes.Buffer
	cmd.SetOut(&buf1)
	cmd.SetArgs([]string{"--workspace", dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	first := strings.TrimSpace(buf1.String())
	assert.Equal(t, "1", first)

	// Repeat with the same key + same fingerprint → reuse, same number.
	resetFlags(t)
	cmd = newRootCmd()
	var buf2 bytes.Buffer
	cmd.SetOut(&buf2)
	cmd.SetArgs([]string{"--workspace", dir, "--quiet", "create",
		"first issue", "--idempotency-key", "K1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	second := strings.TrimSpace(buf2.String())
	assert.Equal(t, "1", second, "same key + fingerprint must return existing issue number")
}

func TestCreate_ForceNewBypassesLookalike(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	createIssueViaHTTP(t, env, dir, "fix login crash on Safari")

	// Without --force-new the daemon would 409 on look-alike. With it, we get a new issue.
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "--quiet", "create",
		"fix login crash Safari", "--force-new"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "2", strings.TrimSpace(buf.String()))
}
```

- [ ] **Step 2: Run the tests (expect failure)**

Run: `go test ./cmd/kata/... -run TestCreate_With`
Expected: FAIL — flags not registered.

- [ ] **Step 3: Implement**

In `cmd/kata/create.go`, add the two new flags and rewire the POST. Find the `cmd.Flags().StringVar(&owner, ...)` block and append:

```go
var idempotencyKey string
var forceNew bool
cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "send Idempotency-Key header for safe retry")
cmd.Flags().BoolVar(&forceNew, "force-new", false, "bypass look-alike soft-block (idempotency still wins)")
```

Then in the RunE, after the existing `req` map construction:

```go
if forceNew {
	req["force_new"] = true
}
headers := map[string]string{}
if idempotencyKey != "" {
	headers["Idempotency-Key"] = idempotencyKey
}
url := fmt.Sprintf("%s/api/v1/projects/%d/issues", baseURL, projectID)
var status int
var bs []byte
if len(headers) > 0 {
	status, bs, err = httpDoJSONWithHeader(ctx, baseURL, http.MethodPost, url, headers, req)
} else {
	status, bs, err = httpDoJSON(ctx, client, http.MethodPost, url, req)
}
if err != nil {
	return err
}
```

Replace the old `httpDoJSON` call accordingly. The existing error-mapping (`apiErrFromBody`) and `printMutation` calls below stay unchanged.

- [ ] **Step 4: Run the tests (expect pass)**

Run: `go test ./cmd/kata/... -run TestCreate_With`
Expected: PASS.

Run the broader create suite: `go test ./cmd/kata/... -run TestCreate`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/create.go cmd/kata/create_test.go
git commit -m "feat(cli): kata create --idempotency-key and --force-new"
```

---

### Task 16: Final tidy — e2e smoke + advertise checks + self-review

Spec refs: writing-plans skill self-review checklist, §6.1 (CLI verb registration).

Three sub-steps:

**Step A:** Add a Plan 3 lifecycle smoke test (`TestSmoke_Plan3Lifecycle`) that exercises search → idempotent create → look-alike block → force-new bypass → soft-delete → restore → purge end-to-end.

**Step B:** Extend `cmd/kata/main_test.go` with `TestRoot_Plan3VerbsAdvertised` so a future `--help` regression on any new verb fails fast.

**Step C:** Run the full suite and walk through the spec checklist.

**Files:**
- Modify: `e2e/e2e_test.go` — append `TestSmoke_Plan3Lifecycle`.
- Modify: `cmd/kata/main_test.go` — append `TestRoot_Plan3VerbsAdvertised`.

- [ ] **Step 1: Add the Plan 3 smoke test**

Append to `e2e/e2e_test.go`:

```go
func TestSmoke_Plan3Lifecycle(t *testing.T) {
	env := testenv.New(t)
	dir := initRepo(t, "https://github.com/wesm/system.git")
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dir}))
	pid := resolvePID(t, env.HTTP, env.URL, dir)
	pidStr := strconv.FormatInt(pid, 10)

	// 1. create with idempotency key.
	first := postWithHeaderHTTP(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "smoke-K1"},
		map[string]any{"actor": "agent", "title": "fix login crash on Safari", "body": "stack trace"})
	require.Equalf(t, 200, first.status, "first create: %s", string(first.body))
	assert.Contains(t, string(first.body), `"reused":false`)

	// 2. repeat with the same key — reuse.
	second := postWithHeaderHTTP(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]string{"Idempotency-Key": "smoke-K1"},
		map[string]any{"actor": "agent", "title": "fix login crash on Safari", "body": "stack trace"})
	require.Equal(t, 200, second.status, string(second.body))
	assert.Contains(t, string(second.body), `"reused":true`)
	assert.Contains(t, string(second.body), `"original_event"`)

	// 3. search picks up the issue.
	bs := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/search?q=login")
	assert.Contains(t, bs, `"title":"fix login crash on Safari"`)

	// 4. look-alike soft-block on a near-identical title.
	resp, body := postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash Safari",
			"body": "stack trace"})
	require.Equal(t, 409, resp.StatusCode, string(body))
	assert.Contains(t, string(body), `"duplicate_candidates"`)

	// 5. force_new bypasses.
	resp, body = postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "fix login crash Safari",
			"body": "stack trace", "force_new": true})
	require.Equal(t, 200, resp.StatusCode, string(body))

	// 6. soft-delete #1 with confirm header.
	delResp := postWithHeaderHTTP(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/actions/delete",
		map[string]string{"X-Kata-Confirm": "DELETE #1"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, delResp.status, string(delResp.body))
	assert.Contains(t, string(delResp.body), `"issue.soft_deleted"`)

	// 7. show without include_deleted now 404s.
	resp, _ = getStatusBodyHTTP(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1")
	assert.Equal(t, 404, resp.StatusCode)

	// 8. restore brings it back.
	resp, body = postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/actions/restore",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(body))

	// 9. purge #2 (irreversible). Verify purge_log row in the response.
	purgeResp := postWithHeaderHTTP(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/actions/purge",
		map[string]string{"X-Kata-Confirm": "PURGE #2"},
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, purgeResp.status, string(purgeResp.body))
	assert.Contains(t, string(purgeResp.body), `"purge_log"`)
	assert.Contains(t, string(purgeResp.body), `"purge_reset_after_event_id"`)
}

// postWithHeaderHTTP and getStatusBodyHTTP mirror the daemon-test helpers but
// use a generic *http.Client (e.g. env.HTTP from testenv) instead of an
// httptest.Server's client.
type smokeResp struct {
	status int
	body   []byte
}

func postWithHeaderHTTP(t *testing.T, client *http.Client, url string,
	headers map[string]string, body any) smokeResp {
	t.Helper()
	bs, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req) //nolint:gosec
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return smokeResp{status: resp.StatusCode, body: out}
}

func getStatusBodyHTTP(t *testing.T, client *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req) //nolint:gosec
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, bs
}
```

Add `"io"` and `"context"` to the imports if not already present.

- [ ] **Step 2: Add the cobra advertise test**

Append to `cmd/kata/main_test.go`:

```go
func TestRoot_Plan3VerbsAdvertised(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	for _, verb := range []string{"delete", "restore", "purge", "search"} {
		assert.Containsf(t, out, verb, "root help must list %q", verb)
	}
}
```

- [ ] **Step 3: Run the suite**

```bash
make lint
make test
```

Expected: all green, 0 lint issues.

If anything fails, fix in place. Common pitfalls:
- A flag wasn't registered on the new cobra command (cobra silently accepts unknown flags only when `cmd.FParseErrWhitelist.UnknownFlags` is set — we don't set it, so unknown flags fail noisily).
- `resolvePIDViaHTTP` wasn't called in a test that needed a project id — surface as 404 from `/api/v1/projects/0/...`.
- A no-op envelope is leaking `null` for the `original_event` field in the JSON marshal because `omitempty` is missing — verify the `OriginalEvent *db.Event \`json:"original_event,omitempty"\`` tag.

- [ ] **Step 4: Self-review checklist**

Plan 3 should now satisfy:

1. **Schema (§3.2):** FTS sync triggers on issues + comments are appended to `0001_init.sql`. Confirmed by `TestFTS_*`.
2. **Event types (§3.3):** `issue.soft_deleted`, `issue.restored` emitted by Tasks 5 + 10. `idempotency_key`/`idempotency_fingerprint` fields land in `issue.created` payload (Task 9).
3. **Lifecycle (§3.4):** delete/restore wired through to events; purge writes `purge_log` instead of an event.
4. **Destructive ladder (§3.5):** all five rungs exist — close (Plan 1), delete-no-force-hint (Task 11), delete-with-force (Tasks 5+10+11), restore (Tasks 5+10+12), purge (Tasks 6+10+13).
5. **Idempotency (§3.6):** Fingerprint over (title, body, owner, labels, links); window 7d; same/different fingerprint cases handled; deleted-issue case returns `idempotency_deleted`.
6. **Look-alike soft-block (§3.7):** FTS5 candidates → similarity score 0.6/0.4 weighted → 0.7 threshold → 409 with candidate list. Force-new bypass; idempotency wins.
7. **Endpoint surface (§4.1):** GET `/search`, POST `/actions/delete`, POST `/actions/restore`, POST `/actions/purge` all registered.
8. **Headers (§4.4):** `Idempotency-Key` on create, `X-Kata-Confirm` on delete/purge — both validated.
9. **Mutation envelope (§4.5):** `reused:true` and `original_event` populated only on idempotent reuse; `omitempty` keeps prior endpoints' wire shape.
10. **Status → exit (§4.7):** `idempotency_mismatch` (409 → 5), `idempotency_deleted` (409 → 5), `confirm_required` (412 → 6), `confirm_mismatch` (412 → 6), `duplicate_candidates` (409 → 5) all surface through `apiErrFromBody`.
11. **Search response (§4.10):** `query` echoed; `results` carry score + matched_in.
12. **Polling reset (§4.11):** out of scope — Plan 4. The `purge_log.purge_reset_after_event_id` row is written for Plan 4's broadcaster to consume.
13. **CLI (§6.1):** `kata search`, `kata delete`, `kata restore`, `kata purge`, `kata create --idempotency-key`/`--force-new` all exist with the documented flags.

Out of scope for Plan 3 (defer to later plans): SSE consumer for the reserved cursor (Plan 4), event polling (Plan 4), hooks (Plan 5), TUI (Plan 6), skills/doctor (Plan 7), `kata config` for the threshold/window knobs (Plan 7).

- [ ] **Step 5: Commit**

```bash
git add e2e/e2e_test.go cmd/kata/main_test.go
git commit -m "test: Plan 3 smoke + advertise checks"
```

If `git status` is clean before this step (everything was committed in earlier tasks), skip — there's nothing to commit.

---

## Self-review checklist (run before declaring Plan 3 complete)

**Spec coverage:** §3.2 FTS triggers in place; §3.3 new event types fire; §3.4 destructive lifecycle entries; §3.5 destructive ladder rungs all wired (CLI + handler + DB); §3.6 idempotency fingerprint matches the spec's wire form (key=value\n separators, sha256 hex); §3.7 look-alike pipeline matches (top-20 BM25 → app-side Jaccard 0.6/0.4 → 0.7 threshold); §4.1 endpoint surface complete for Plan 3 scope; §4.4 headers validated; §4.5 envelope extended with `reused`/`original_event`; §4.7 exit-code mapping verified; §4.10 search shape; §6.1 CLI verbs and flags wired.

**Type consistency:**
- `db.SearchCandidate` (DB) ↔ `api.SearchHit` (wire) ↔ `SearchResponse.Results` — fields line up.
- `db.PurgeLog` (DB) ↔ `api.PurgeResponse.Body.PurgeLog` — directly embedded, no field-name drift.
- `db.IdempotencyMatch` is internal to the daemon's createIssue handler; not exposed on the wire.
- `api.MutationResponse.Body.OriginalEvent` is `*db.Event` (matches the existing `Event` field's pointer type).
- `api.DestructiveActionRequest` and `api.RestoreRequest` are distinct types — Huma needs distinct types for distinct operations even when bodies are similar.

**Conventions:**
- All tests use `testify/require` + `assert`; no bare `t.Fatal`.
- Every CLI test calls `resetFlags(t)` before constructing the root command.
- DB-typed errors map to HTTP statuses inside handlers, not via string-matching.
- No `//nolint` directives without a justification comment.

**Plan 1+2 surface preserved:** the existing `MutationResponse{Issue, Event, Changed, Reused}` shape is extended additively (added `OriginalEvent` with `omitempty`); existing endpoints still emit identical bytes for non-idempotent flows.

**Forward-compat:** the `purge_log.purge_reset_after_event_id` row is written but no client consumes it yet (Plan 4 will). The `idx_events_idempotency` partial index is exercised by `LookupIdempotency`. Soft-deleted issues stay in FTS by design so a future `kata search --include-deleted` (or the Plan 6 TUI) doesn't need additional indexing work. The 7-day window and 0.7 threshold are constants in `handlers_issues.go`; Plan 7's `kata config` work will turn them into config-driven values without changing the wire surface.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-29-kata-3-search-idempotency-purge.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. After every five completed tasks invoke `/roborev-fix` to clean up post-commit review findings (matching Plans 1+2 cadence).

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Standing authorization from Plans 1+2 ("perfect, yes, use opus with subagents and invoke roborev fix every 5 tasks") still holds unless you say otherwise.
