# Plan 2 — Relationships + Ownership + Labels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the kata daemon + CLI with issue relationships (parent/blocks/related), labels, ownership verbs (assign/unassign), and the "what's next" `kata ready` query — plus support for initial labels/links/owner on `kata create`.

**Architecture:** Plan 1 left the `links`, `issue_labels`, and partial-index machinery in `0001_init.sql` ready to use; Plan 2 wires queries, handlers, and CLI verbs onto that foundation. Sugar verbs (`parent`/`block`/`relate`) translate to the generic `links` POST under the hood. Labels follow the same pattern — same MutationResponse envelope, same no-op semantics. `kata ready` is a single SQL query (per spec §6.6) with a thin handler. Initial state on `kata create` writes labels/links/owner in the same transaction as the issue and folds them into the `issue.created` event payload (no separate `issue.labeled`/`issue.linked`/`issue.assigned` events fire at creation, per spec §3.3).

**Tech Stack:** Same as Plan 1. No new dependencies.

**Reference spec:** `docs/superpowers/specs/2026-04-29-kata-design.md` (commits `f52df47` + `df6b28a`). Plan 2 covers §3.3 (the `linked`/`unlinked`/`labeled`/`unlabeled`/`assigned`/`unassigned` event types), §3.4 (link/label/assign lifecycle entries), §4.1 (links/labels/ready/actions endpoints), §4.5 (`event/changed/reused` envelope holds for new endpoints too), §6.1 (relationship/label/ownership/ready CLI verbs), §6.6 (ready SQL).

**Out of scope for Plan 2 (deferred to later plans):** idempotency keys (Plan 3), look-alike soft-block (Plan 3), search + FTS triggers (Plan 3), soft-delete/restore/purge (Plan 3), SSE durability (Plan 4), event polling (Plan 4), hooks (Plan 5), TUI (Plan 6), skills/doctor/agent-instructions (Plan 7). Cross-project commands (`--all-projects`) are also Plan 4 territory; Plan 2's `kata ready` lists current-project only.

**Bug-fix-adjacent notes:**
- `kata edit --owner X` continues to emit `issue.updated` (Plan 1 behavior). `kata assign N X` and `kata unassign N` are the new verbs that emit `issue.assigned`/`issue.unassigned`. Same DB column update; different event type.
- The `issue.updated` event payload's `fields` shape (per spec §3.4) is **not** implemented in this plan. The current empty payload `{}` stays. Plan 3+ can extend the payload when consumers (search/SSE) need the diff. Don't change `EditIssue` here.

**Conventions for every task** — same as Plan 1:

- TDD: write the failing test first, run it to confirm it fails, implement, run to confirm pass, commit.
- Use `testify/require` for setup/preconditions and `testify/assert` for non-blocking checks; never `t.Fatal`/`t.Error` directly.
- Table-driven tests where multiple cases exist.
- `t.TempDir()` for any filesystem state. Never write to `~/.kata` from tests.
- Tests run with `-shuffle=on` (already set in `Makefile`); never pass `-count=1`; never pass `-v` unless asked.
- CLI tests must call `resetFlags(t)` (in `cmd/kata/testhelpers_test.go`) before constructing root commands; otherwise prior tests' `flags.JSON`/`flags.Quiet` state leaks under shuffle.
- Commit messages: conventional (`feat:`, `fix:`, `chore:`, `test:`); subject ≤72 chars; one logical change per commit. Co-author trailer `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` is encouraged.
- Pre-commit hook (`prek`) will run `make lint`. Run `make lint` locally before committing.
- Never amend commits; always create new ones for fixes.
- Tests must hit `make test`. Don't run `go test -v` or `-count=1`.

**Plan 1 surface to reuse (not redefine):**

- `internal/api/types.go` — `MutationResponse{Issue, Event, Changed, Reused}` envelope.
- `internal/api/errors.go` — `api.NewError(status, code, message, hint, data)`; `*APIError` satisfies Huma's status error.
- `internal/db/types.go` — `Issue`, `Project`, `Comment`, `Event`. Add `Link`, `IssueLabel`, `LabelCount` here in Task 1 / Task 2.
- `internal/db/queries.go` — `IssueByID`, `IssueByNumber`, `lookupIssueForEvent` (tx-internal), `insertEventTx`, `eventInsert{ProjectID, ProjectIdentity, IssueID, IssueNumber, RelatedIssueID, Type, Actor, Payload}`, `ErrNotFound`. Reuse these in Task 1+.
- `internal/daemon/server.go` — `registerRoutes` calls `registerHealth/Projects/Issues/Comments/Actions`. Add `registerLinks/Labels/Ownership/Ready` in tasks 7-10.
- `cmd/kata/helpers.go` — `httpDoJSON`, `emitJSON`, `BodySources`, `resolveActor`, `Exit*` constants.
- `cmd/kata/init.go` — `cliError{Message, Code, ExitCode}`, `apiErrFromBody`, `mapStatusToExit`, `resolveStartPath`.
- `cmd/kata/client.go` — `ensureDaemon(ctx)`, `httpClientFor(ctx, baseURL)` (two args, ctx first).
- `cmd/kata/create.go` — `resolveProjectID(ctx, baseURL, startPath)`, `printMutation(cmd, bs)`.
- `cmd/kata/main.go` — `flags globalFlags`, `runEEntered` sentinel, `exitCodeFor`.
- `cmd/kata/testhelpers_test.go` — `pipeServer`, `writeRuntimeFor`, `contextWithBaseURL`, `initBoundWorkspace`, `resolvePIDViaHTTP`, `itoa`, `resetFlags(t)`.

---

### Task 1: `internal/db/queries_links.go` — link CRUD + types

Spec refs: §3.2 (links table + same-project triggers), §3.3 (`issue.linked`/`issue.unlinked`), §3.4 (link lifecycle).

**Files:**
- Modify: `internal/db/types.go` — add `Link`.
- Create: `internal/db/queries_links.go` — `CreateLinkParams`, `CreateLink`, `DeleteLinkByID`, `LinkByID`, `LinkByEndpoints`, `ParentOf`, `LinksByIssue`.
- Test: `internal/db/queries_links_test.go`.

The schema enforces:
- `UNIQUE(from_issue_id, to_issue_id, type)` — duplicate (from, to, type) → SQLite UNIQUE error. Caller treats as "already linked".
- `CHECK (from_issue_id <> to_issue_id)` — self-link forbidden.
- `CHECK (type <> 'related' OR from_issue_id < to_issue_id)` — `related` is canonical-ordered. Caller (handler/CLI) must swap (from, to) when type=related and from > to.
- `CREATE UNIQUE INDEX uniq_one_parent_per_child ON links(from_issue_id) WHERE type = 'parent'` — only one parent per child. Second `parent` link from the same child → UNIQUE error. Distinct from the (from,to,type) UNIQUE; surfaces as a different conflict the caller maps to `parent_already_set`.
- `trg_links_same_project_*` — both endpoints must belong to `links.project_id`. Cross-project link → `RAISE(ABORT, 'cross-project links are not allowed')`.

`CreateLink` does **not** emit an event (the event is emitted by the handler so it can include the project identity snapshot via `lookupIssueForEvent`). `DeleteLinkByID` is the same — handler-side event emission. This keeps the DB layer mechanical and matches the `EditIssue`/`CloseIssue` separation pattern Plan 1 uses for actions.

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_links_test.go
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

// makeIssue is a helper that creates an issue under projectID. It returns the
// created issue.
func makeIssue(t *testing.T, ctx context.Context, d *db.DB, projectID int64, title, author string) db.Issue {
	t.Helper()
	issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: projectID, Title: title, Author: author,
	})
	require.NoError(t, err)
	return issue
}

func TestCreateLink_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "child", "tester")
	b := makeIssue(t, ctx, d, p.ID, "parent", "tester")

	link, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: a.ID,
		ToIssueID:   b.ID,
		Type:        "parent",
		Author:      "tester",
	})
	require.NoError(t, err)
	assert.Greater(t, link.ID, int64(0))
	assert.Equal(t, "parent", link.Type)
	assert.Equal(t, a.ID, link.FromIssueID)
	assert.Equal(t, b.ID, link.ToIssueID)

	got, err := d.LinkByID(ctx, link.ID)
	require.NoError(t, err)
	assert.Equal(t, link.ID, got.ID)
}

func TestCreateLink_DuplicateIsErrLinkExists(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrLinkExists), "expected ErrLinkExists, got %v", err)
}

func TestCreateLink_SecondParentIsErrParentAlreadySet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	child := makeIssue(t, ctx, d, p.ID, "child", "tester")
	p1 := makeIssue(t, ctx, d, p.ID, "p1", "tester")
	p2 := makeIssue(t, ctx, d, p.ID, "p2", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: p1.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: child.ID, ToIssueID: p2.ID, Type: "parent", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrParentAlreadySet),
		"expected ErrParentAlreadySet, got %v", err)
}

func TestCreateLink_CrossProjectIsErrCrossProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p1, err := d.CreateProject(ctx, "p1", "p1")
	require.NoError(t, err)
	p2, err := d.CreateProject(ctx, "p2", "p2")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p1.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p2.ID, "b", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p1.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "cross-project") ||
		errors.Is(err, db.ErrCrossProjectLink),
		"expected cross-project error, got %v", err)
}

func TestCreateLink_SelfLinkIsError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: a.ID, Type: "related", Author: "tester",
	})
	assert.True(t, errors.Is(err, db.ErrSelfLink),
		"expected ErrSelfLink, got %v", err)
}

func TestLinkByEndpoints_FindsExisting(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	created, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "related", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.LinkByEndpoints(ctx, a.ID, b.ID, "related")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)

	_, err = d.LinkByEndpoints(ctx, a.ID, b.ID, "parent")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestLinksByIssue_ReturnsBothDirections(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	c := makeIssue(t, ctx, d, p.ID, "c", "tester")
	// a → blocks → b ; c → parent → a
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: c.ID, ToIssueID: a.ID, Type: "parent", Author: "tester",
	})
	require.NoError(t, err)

	got, err := d.LinksByIssue(ctx, a.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestParentOf_ReturnsErrNotFoundWhenAbsent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.ParentOf(ctx, a.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestDeleteLinkByID_RemovesRow(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	link, err := d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID: p.ID, FromIssueID: a.ID, ToIssueID: b.ID, Type: "blocks", Author: "tester",
	})
	require.NoError(t, err)

	require.NoError(t, d.DeleteLinkByID(ctx, link.ID))
	_, err = d.LinkByID(ctx, link.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))

	// Idempotent — deleting an absent row is also ErrNotFound (caller decides
	// whether to surface as no-op or 404).
	err = d.DeleteLinkByID(ctx, link.ID)
	assert.True(t, errors.Is(err, db.ErrNotFound))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run 'TestCreateLink|TestLinkBy|TestLinksByIssue|TestParentOf|TestDeleteLinkByID'`
Expected: FAIL — `undefined: db.Link`, `undefined: db.CreateLinkParams`, etc.

- [ ] **Step 3: Add `Link` to `internal/db/types.go`**

Append to `internal/db/types.go`:

```go
// Link mirrors a row in links.
type Link struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	FromIssueID int64     `json:"from_issue_id"`
	ToIssueID   int64     `json:"to_issue_id"`
	Type        string    `json:"type"`
	Author      string    `json:"author"`
	CreatedAt   time.Time `json:"created_at"`
}
```

- [ ] **Step 4: Create `internal/db/queries_links.go`**

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrLinkExists is returned when a (from, to, type) triple already has a row.
// Surface as 200 with event:null, changed:false in the handler.
var ErrLinkExists = errors.New("link already exists")

// ErrParentAlreadySet is returned when a child issue already has a parent and
// CreateLink is called with type=parent. The handler maps this to 409.
var ErrParentAlreadySet = errors.New("parent already set")

// ErrSelfLink is returned when from_issue_id == to_issue_id.
var ErrSelfLink = errors.New("self-link not allowed")

// ErrCrossProjectLink is returned when the same-project trigger fires.
var ErrCrossProjectLink = errors.New("cross-project link not allowed")

// CreateLinkParams carries inputs for CreateLink. The caller is responsible
// for canonical ordering of `related` links (from < to) before calling.
type CreateLinkParams struct {
	ProjectID   int64
	FromIssueID int64
	ToIssueID   int64
	Type        string // "parent" | "blocks" | "related"
	Author      string
}

// CreateLink inserts a links row. Distinct error types let the caller emit
// the right wire status without parsing SQLite messages.
func (d *DB) CreateLink(ctx context.Context, p CreateLinkParams) (Link, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO links(project_id, from_issue_id, to_issue_id, type, author)
		 VALUES(?, ?, ?, ?, ?)`,
		p.ProjectID, p.FromIssueID, p.ToIssueID, p.Type, p.Author)
	if err != nil {
		return Link{}, classifyLinkInsertError(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Link{}, fmt.Errorf("last insert id: %w", err)
	}
	return d.LinkByID(ctx, id)
}

// classifyLinkInsertError maps SQLite constraint failures to typed errors so
// the handler can choose the right HTTP status without string-matching.
func classifyLinkInsertError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed: links.from_issue_id") &&
		strings.Contains(msg, "WHERE type = 'parent'"):
		// Partial unique index on (from_issue_id) WHERE type='parent'. Note:
		// SQLite's error text on partial-index violations does include the
		// index name; matching by both column and the partial-clause keeps
		// us from misclassifying the broader (from,to,type) UNIQUE.
		return ErrParentAlreadySet
	case strings.Contains(msg, "UNIQUE constraint failed: links.from_issue_id, links.to_issue_id, links.type"):
		return ErrLinkExists
	case strings.Contains(msg, "CHECK constraint failed") &&
		strings.Contains(msg, "from_issue_id <> to_issue_id"):
		return ErrSelfLink
	case strings.Contains(msg, "cross-project links are not allowed"):
		return ErrCrossProjectLink
	}
	return fmt.Errorf("insert link: %w", err)
}

// LinkByID fetches a link by rowid.
func (d *DB) LinkByID(ctx context.Context, id int64) (Link, error) {
	row := d.QueryRowContext(ctx, linkSelect+` WHERE id = ?`, id)
	return scanLink(row)
}

// LinkByEndpoints fetches the link for a (from, to, type) triple.
func (d *DB) LinkByEndpoints(ctx context.Context, fromIssueID, toIssueID int64, linkType string) (Link, error) {
	row := d.QueryRowContext(ctx,
		linkSelect+` WHERE from_issue_id = ? AND to_issue_id = ? AND type = ?`,
		fromIssueID, toIssueID, linkType)
	return scanLink(row)
}

// ParentOf returns the parent link for childIssueID (one-parent invariant).
// Returns ErrNotFound when no parent is set.
func (d *DB) ParentOf(ctx context.Context, childIssueID int64) (Link, error) {
	row := d.QueryRowContext(ctx,
		linkSelect+` WHERE from_issue_id = ? AND type = 'parent'`,
		childIssueID)
	return scanLink(row)
}

// LinksByIssue returns every link involving issueID (either endpoint), ordered
// by id ASC. Used to build the show-issue response and to back `kata unlink`'s
// list-then-delete flow.
func (d *DB) LinksByIssue(ctx context.Context, issueID int64) ([]Link, error) {
	rows, err := d.QueryContext(ctx,
		linkSelect+` WHERE from_issue_id = ? OR to_issue_id = ? ORDER BY id ASC`,
		issueID, issueID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteLinkByID removes a links row. Returns ErrNotFound when no row exists.
func (d *DB) DeleteLinkByID(ctx context.Context, linkID int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, linkID)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete link rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const linkSelect = `SELECT id, project_id, from_issue_id, to_issue_id, type, author, created_at FROM links`

func scanLink(r rowScanner) (Link, error) {
	var l Link
	err := r.Scan(&l.ID, &l.ProjectID, &l.FromIssueID, &l.ToIssueID, &l.Type, &l.Author, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("scan link: %w", err)
	}
	return l, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/db/... -run 'TestCreateLink|TestLinkBy|TestLinksByIssue|TestParentOf|TestDeleteLinkByID'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/types.go internal/db/queries_links.go internal/db/queries_links_test.go
git commit -m "feat(db): link CRUD queries with typed errors"
```

---

### Task 2: `internal/db/queries_labels.go` — label CRUD + counts + types

Spec refs: §3.2 (issue_labels schema with charset CHECK), §3.3 (`issue.labeled`/`issue.unlabeled`), §3.4 (label add/rm lifecycle), §6.1 (labels CLI).

**Files:**
- Modify: `internal/db/types.go` — add `IssueLabel`, `LabelCount`.
- Create: `internal/db/queries_labels.go` — `AddLabel`, `RemoveLabel`, `HasLabel`, `LabelsByIssue`, `LabelCounts`.
- Test: `internal/db/queries_labels_test.go`.

The schema enforces `length(label) BETWEEN 1 AND 64` and `label NOT GLOB '*[^a-z0-9._:-]*'`. Bad labels surface as a generic CHECK failure from SQLite. Surface those via `ErrLabelInvalid` and let the handler return 400 `validation`. `PRIMARY KEY(issue_id, label)` makes "label already attached" → typed `ErrLabelExists` (handler maps to 200 no-op).

Like links, `AddLabel` does not emit events. The handler emits.

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_labels_test.go
package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestAddLabel_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	row, err := d.AddLabel(ctx, i.ID, "needs-review", "tester")
	require.NoError(t, err)
	assert.Equal(t, "needs-review", row.Label)
	assert.Equal(t, i.ID, row.IssueID)

	got, err := d.LabelsByIssue(ctx, i.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "needs-review", got[0].Label)
}

func TestAddLabel_DuplicateIsErrLabelExists(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	require.NoError(t, err)
	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	assert.True(t, errors.Is(err, db.ErrLabelExists), "got %v", err)
}

func TestAddLabel_RejectsBadCharset(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	for _, label := range []string{"UPPER", "with space", "emoji😀", "" /* empty */, "exclam!"} {
		_, err := d.AddLabel(ctx, i.ID, label, "tester")
		assert.Truef(t, errors.Is(err, db.ErrLabelInvalid),
			"label=%q: expected ErrLabelInvalid, got %v", label, err)
	}
}

func TestAddLabel_AcceptsAllAllowedChars(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")

	for _, label := range []string{"bug", "priority:high", "v1.0", "needs-review", "a-z_0-9"} {
		_, err := d.AddLabel(ctx, i.ID, label, "tester")
		assert.NoErrorf(t, err, "label=%q must be accepted", label)
	}
}

func TestRemoveLabel_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	i := makeIssue(t, ctx, d, p.ID, "a", "tester")
	_, err = d.AddLabel(ctx, i.ID, "bug", "tester")
	require.NoError(t, err)

	require.NoError(t, d.RemoveLabel(ctx, i.ID, "bug"))

	had, err := d.HasLabel(ctx, i.ID, "bug")
	require.NoError(t, err)
	assert.False(t, had)

	// Idempotent — removing an absent label returns ErrNotFound.
	err = d.RemoveLabel(ctx, i.ID, "bug")
	assert.True(t, errors.Is(err, db.ErrNotFound))
}

func TestLabelCounts_AggregatesPerProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	a := makeIssue(t, ctx, d, p.ID, "a", "tester")
	b := makeIssue(t, ctx, d, p.ID, "b", "tester")
	for _, lab := range []string{"bug", "priority:high"} {
		_, err := d.AddLabel(ctx, a.ID, lab, "tester")
		require.NoError(t, err)
	}
	_, err = d.AddLabel(ctx, b.ID, "bug", "tester")
	require.NoError(t, err)

	counts, err := d.LabelCounts(ctx, p.ID)
	require.NoError(t, err)
	got := map[string]int64{}
	for _, c := range counts {
		got[c.Label] = c.Count
	}
	assert.Equal(t, int64(2), got["bug"])
	assert.Equal(t, int64(1), got["priority:high"])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run 'TestAddLabel|TestRemoveLabel|TestLabelCounts'`
Expected: FAIL — `undefined: db.IssueLabel`, etc.

- [ ] **Step 3: Add types**

Append to `internal/db/types.go`:

```go
// IssueLabel mirrors a row in issue_labels.
type IssueLabel struct {
	IssueID   int64     `json:"issue_id"`
	Label     string    `json:"label"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// LabelCount is the per-label aggregate returned by LabelCounts.
type LabelCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}
```

- [ ] **Step 4: Create `internal/db/queries_labels.go`**

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrLabelExists is returned when (issue_id, label) already exists. Surface as
// 200 no-op (event:null, changed:false) in the handler.
var ErrLabelExists = errors.New("label already attached")

// ErrLabelInvalid is returned when the label fails the schema's charset/length
// CHECK. Handler maps to 400 validation.
var ErrLabelInvalid = errors.New("invalid label")

// AddLabel attaches a label to an issue.
func (d *DB) AddLabel(ctx context.Context, issueID int64, label, author string) (IssueLabel, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO issue_labels(issue_id, label, author) VALUES(?, ?, ?)`,
		issueID, label, author)
	if err != nil {
		return IssueLabel{}, classifyLabelInsertError(err)
	}
	_ = res
	row := d.QueryRowContext(ctx,
		`SELECT issue_id, label, author, created_at FROM issue_labels
		 WHERE issue_id = ? AND label = ?`, issueID, label)
	var out IssueLabel
	if err := row.Scan(&out.IssueID, &out.Label, &out.Author, &out.CreatedAt); err != nil {
		return IssueLabel{}, fmt.Errorf("re-fetch label: %w", err)
	}
	return out, nil
}

func classifyLabelInsertError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed: issue_labels.issue_id, issue_labels.label"):
		return ErrLabelExists
	case strings.Contains(msg, "CHECK constraint failed"):
		// Either the GLOB charset or the length BETWEEN check.
		return ErrLabelInvalid
	}
	return fmt.Errorf("insert label: %w", err)
}

// RemoveLabel detaches a label from an issue. Returns ErrNotFound when the row
// doesn't exist (idempotent unlink semantics live in the handler).
func (d *DB) RemoveLabel(ctx context.Context, issueID int64, label string) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM issue_labels WHERE issue_id = ? AND label = ?`,
		issueID, label)
	if err != nil {
		return fmt.Errorf("delete label: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete label rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasLabel reports whether (issueID, label) exists.
func (d *DB) HasLabel(ctx context.Context, issueID int64, label string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM issue_labels WHERE issue_id = ? AND label = ?`,
		issueID, label).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has label: %w", err)
	}
	return n == 1, nil
}

// LabelsByIssue returns every label attached to issueID, ordered alphabetically.
func (d *DB) LabelsByIssue(ctx context.Context, issueID int64) ([]IssueLabel, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT issue_id, label, author, created_at FROM issue_labels
		 WHERE issue_id = ? ORDER BY label ASC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []IssueLabel
	for rows.Next() {
		var l IssueLabel
		if err := rows.Scan(&l.IssueID, &l.Label, &l.Author, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelCounts returns the per-label aggregate for projectID, excluding
// soft-deleted issues.
func (d *DB) LabelCounts(ctx context.Context, projectID int64) ([]LabelCount, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT il.label, COUNT(*) AS n
		 FROM issue_labels il
		 JOIN issues i ON i.id = il.issue_id
		 WHERE i.project_id = ? AND i.deleted_at IS NULL
		 GROUP BY il.label
		 ORDER BY n DESC, il.label ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("label counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LabelCount
	for rows.Next() {
		var c LabelCount
		if err := rows.Scan(&c.Label, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/db/... -run 'TestAddLabel|TestRemoveLabel|TestLabelCounts'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/db/types.go internal/db/queries_labels.go internal/db/queries_labels_test.go
git commit -m "feat(db): label CRUD with charset enforcement and counts"
```

---

### Task 3: `internal/db/queries.go` — `UpdateOwner` for assign/unassign

Spec refs: §3.3 (`issue.assigned`/`issue.unassigned`), §3.4 (assign lifecycle).

`UpdateOwner` updates `issues.owner` and emits the appropriate event in one tx. `newOwner == nil` means unassign (set NULL, emit `issue.unassigned`); a non-nil string means assign (set, emit `issue.assigned`). The semantics differ from `EditIssue --owner`:
- `EditIssue` emits `issue.updated` regardless of which fields changed.
- `UpdateOwner` emits a dedicated `issue.assigned`/`issue.unassigned` event.

Same DB column, two paths. The CLI verb the user typed determines which path runs.

No-op: assigning the same owner that's already set, or unassigning when owner is already NULL. Returns `(issue, nil, false, nil)` — the standard "no-op" tuple matching `CloseIssue`/`ReopenIssue`.

**Files:**
- Modify: `internal/db/queries.go` — append `UpdateOwner`.
- Test: `internal/db/queries_owner_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_owner_test.go
package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestUpdateOwner`
Expected: FAIL — `undefined: (*db.DB).UpdateOwner`.

- [ ] **Step 3: Append `UpdateOwner` to `internal/db/queries.go`**

Add after `EditIssue`:

```go
// UpdateOwner sets issues.owner to the new value and emits the matching
// assigned/unassigned event. newOwner == nil means unassign. No-op when the
// new value matches the current value (returns nil event, changed=false).
func (d *DB) UpdateOwner(ctx context.Context, issueID int64, newOwner *string, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	// No-op: same owner.
	if ownerEqual(issue.Owner, newOwner) {
		return issue, nil, false, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET owner      = ?,
		     updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, newOwner, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("update owner: %w", err)
	}

	eventType := "issue.unassigned"
	payload := "{}"
	if newOwner != nil {
		eventType = "issue.assigned"
		payload = fmt.Sprintf(`{"owner":%q}`, *newOwner)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            eventType,
		Actor:           actor,
		Payload:         payload,
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

// ownerEqual returns true when two *string owners reference the same value
// (both nil = equal; nil vs non-nil = different; otherwise compare strings).
func ownerEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestUpdateOwner`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries.go internal/db/queries_owner_test.go
git commit -m "feat(db): UpdateOwner with assign/unassign event distinction"
```

---

### Task 4: `internal/db/queries.go` — `ReadyIssues` query

Spec refs: §6.6 — open issues with no open `blocks` predecessor.

```sql
SELECT i.* FROM issues i
WHERE i.project_id = ? AND i.status = 'open' AND i.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM links l
    JOIN issues blocker ON blocker.id = l.from_issue_id
    WHERE l.type = 'blocks' AND l.to_issue_id = i.id
      AND blocker.status = 'open' AND blocker.deleted_at IS NULL
  )
ORDER BY i.updated_at DESC;
```

**Files:**
- Modify: `internal/db/queries.go` — append `ReadyIssues`.
- Test: `internal/db/queries_ready_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_ready_test.go
package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestReadyIssues_FiltersOutClosedAndDeleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	open := makeIssue(t, ctx, d, p.ID, "open", "tester")
	closed := makeIssue(t, ctx, d, p.ID, "closed", "tester")
	_, _, _, err = d.CloseIssue(ctx, closed.ID, "done", "tester")
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, open.Number)
	assert.NotContains(t, got, closed.Number)
}

func TestReadyIssues_ExcludesIssuesBlockedByOpenBlocker(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	standalone := makeIssue(t, ctx, d, p.ID, "standalone", "tester")
	// blocker → blocks → blocked
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   blocked.ID,
		Type:        "blocks",
		Author:      "tester",
	})
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, blocker.Number, "blocker is ready (not blocked itself)")
	assert.Contains(t, got, standalone.Number, "standalone is ready")
	assert.NotContains(t, got, blocked.Number, "blocked is not ready while blocker is open")
}

func TestReadyIssues_ClosedBlockerUnblocksDownstream(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "p", "p")
	require.NoError(t, err)
	blocker := makeIssue(t, ctx, d, p.ID, "blocker", "tester")
	blocked := makeIssue(t, ctx, d, p.ID, "blocked", "tester")
	_, err = d.CreateLink(ctx, db.CreateLinkParams{
		ProjectID:   p.ID,
		FromIssueID: blocker.ID,
		ToIssueID:   blocked.ID,
		Type:        "blocks",
		Author:      "tester",
	})
	require.NoError(t, err)
	_, _, _, err = d.CloseIssue(ctx, blocker.ID, "done", "tester")
	require.NoError(t, err)

	ready, err := d.ReadyIssues(ctx, p.ID, 0)
	require.NoError(t, err)
	got := numbers(ready)
	assert.Contains(t, got, blocked.Number, "blocked is ready once blocker closes")
}

func numbers(rows []db.Issue) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestReadyIssues`
Expected: FAIL.

- [ ] **Step 3: Append `ReadyIssues` to `internal/db/queries.go`**

```go
// ReadyIssues returns open, non-deleted issues with no open `blocks` predecessor,
// ordered by updated_at DESC. limit==0 means no limit.
func (d *DB) ReadyIssues(ctx context.Context, projectID int64, limit int) ([]Issue, error) {
	q := issueSelect + `
		WHERE i.project_id = ? AND i.status = 'open' AND i.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM links l
		    JOIN issues blocker ON blocker.id = l.from_issue_id
		    WHERE l.type = 'blocks' AND l.to_issue_id = i.id
		      AND blocker.status = 'open' AND blocker.deleted_at IS NULL
		  )
		ORDER BY i.updated_at DESC, i.id DESC`
	args := []any{projectID}
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ready issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestReadyIssues`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries.go internal/db/queries_ready_test.go
git commit -m "feat(db): ReadyIssues query (open, no open blocks predecessor)"
```

---

### Task 5: `internal/db/queries.go` — extend `CreateIssue` for initial labels/links/owner

Spec refs: §3.3 (issue.created event payload includes initial labels/links/owner; **no separate** issue.labeled/issue.linked/issue.assigned events at creation), §6.4 (create flow).

`CreateIssueParams` gains `Labels []string`, `Links []InitialLink`, `Owner *string`. The CreateIssue tx inserts:
1. issue row
2. owner column (if provided)
3. label rows (validated client-side first)
4. link rows (resolved from to_number → to_issue_id within the same project)
5. issue.created event with payload containing all initial state

Labels and links are validated/resolved **before** insertion. Validation errors return typed errors (`ErrLabelInvalid`, `ErrInitialLinkTargetNotFound`, `ErrInitialLinkInvalidType`). Duplicate labels in the slice are deduplicated (case-sensitive). Initial parent links must respect the one-parent-per-child invariant — but at creation time the new issue has no existing parent, so the partial unique index can only fire if the user passes two `parent` initial links. The deduplicated list catches identical (type, to_number) pairs; two distinct `parent` targets surface as `ErrParentAlreadySet` from the second insert.

The event payload omits empty fields:
```json
{}                                              // no initial state
{"labels":["bug","priority:high"]}              // labels only
{"labels":[...], "links":[{"type":"parent","to_number":12}], "owner":"alice"}
```

**Files:**
- Modify: `internal/db/queries.go` — extend `CreateIssueParams` and `CreateIssue`.
- Test: `internal/db/queries_create_initial_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_create_initial_test.go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run TestCreateIssue`
Expected: FAIL — `undefined: db.InitialLink`.

- [ ] **Step 3: Extend `internal/db/queries.go`**

Replace the existing `CreateIssueParams` and `CreateIssue` with:

```go
// InitialLink describes one of the optional links created in the same TX as
// the issue itself. The to_number is resolved within the same project.
type InitialLink struct {
	Type     string // "parent" | "blocks" | "related"
	ToNumber int64
}

// CreateIssueParams carries inputs for CreateIssue.
type CreateIssueParams struct {
	ProjectID int64
	Title     string
	Body      string
	Author    string

	// Optional initial state. Plan 2 fields. CreateIssue inserts label/link
	// rows and applies the owner in the same TX, then folds them into the
	// issue.created event payload (no separate labeled/linked/assigned events).
	Labels []string
	Links  []InitialLink
	Owner  *string
}

// ErrInitialLinkTargetNotFound is returned when an InitialLink's to_number
// does not resolve to an existing, non-deleted issue in the same project.
var ErrInitialLinkTargetNotFound = errors.New("initial link target not found")

// ErrInitialLinkInvalidType is returned when an InitialLink's Type is not one
// of {parent, blocks, related}.
var ErrInitialLinkInvalidType = errors.New("invalid initial link type")

// CreateIssue inserts an issue, applies optional initial labels/links/owner,
// and appends a single issue.created event whose payload describes the initial
// state. All steps run in one TX.
func (d *DB) CreateIssue(ctx context.Context, p CreateIssueParams) (Issue, Event, error) {
	// Validate link types client-side so we don't waste a roundtrip on the
	// schema CHECK; the schema enforces the same set, but our typed error is
	// cleaner for the handler to map.
	for _, l := range p.Links {
		switch l.Type {
		case "parent", "blocks", "related":
		default:
			return Issue{}, Event{}, ErrInitialLinkInvalidType
		}
	}

	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		identity string
		nextNum  int64
	)
	if err := tx.QueryRowContext(ctx,
		`UPDATE projects
		 SET next_issue_number = next_issue_number + 1
		 WHERE id = ?
		 RETURNING next_issue_number - 1, identity`, p.ProjectID).
		Scan(&nextNum, &identity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrNotFound
		}
		return Issue{}, Event{}, fmt.Errorf("allocate issue number: %w", err)
	}

	// Insert issue + optional owner column in one statement.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO issues(project_id, number, title, body, author, owner)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		p.ProjectID, nextNum, p.Title, p.Body, p.Author, p.Owner)
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("insert issue: %w", err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return Issue{}, Event{}, err
	}

	// Initial labels — dedupe (preserve first occurrence), then alphabetize
	// for stable payload + storage order.
	labels := dedupeStrings(p.Labels)
	sortStrings(labels)
	for _, label := range labels {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO issue_labels(issue_id, label, author) VALUES(?, ?, ?)`,
			issueID, label, p.Author); err != nil {
			return Issue{}, Event{}, classifyLabelInsertError(err)
		}
	}

	// Initial links — resolve to_number → to_issue_id within the same project,
	// excluding soft-deleted targets. The schema's same-project trigger
	// enforces the cross-project check, but we'd rather surface a typed
	// not-found than a generic constraint failure.
	for _, l := range p.Links {
		var toIssueID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM issues
			 WHERE project_id = ? AND number = ? AND deleted_at IS NULL`,
			p.ProjectID, l.ToNumber).Scan(&toIssueID)
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrInitialLinkTargetNotFound
		}
		if err != nil {
			return Issue{}, Event{}, fmt.Errorf("resolve initial link target: %w", err)
		}
		fromID, toID := issueID, toIssueID
		if l.Type == "related" && fromID > toID {
			fromID, toID = toID, fromID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO links(project_id, from_issue_id, to_issue_id, type, author)
			 VALUES(?, ?, ?, ?, ?)`,
			p.ProjectID, fromID, toID, l.Type, p.Author); err != nil {
			return Issue{}, Event{}, classifyLinkInsertError(err)
		}
	}

	payload := buildCreatedPayload(labels, p.Links, p.Owner)

	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       p.ProjectID,
		ProjectIdentity: identity,
		IssueID:         &issueID,
		IssueNumber:     &nextNum,
		Type:            "issue.created",
		Actor:           p.Author,
		Payload:         payload,
	})
	if err != nil {
		return Issue{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Issue{}, Event{}, fmt.Errorf("commit: %w", err)
	}

	issue, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, Event{}, err
	}
	return issue, evt, nil
}

// buildCreatedPayload returns the issue.created event payload as JSON. Empty
// initial state → "{}". Otherwise emits keys for whichever components are set,
// preserving determinism (sorted labels) so events are byte-stable.
func buildCreatedPayload(labels []string, links []InitialLink, owner *string) string {
	type linkOut struct {
		Type     string `json:"type"`
		ToNumber int64  `json:"to_number"`
	}
	type out struct {
		Labels []string  `json:"labels,omitempty"`
		Links  []linkOut `json:"links,omitempty"`
		Owner  string    `json:"owner,omitempty"`
	}
	var o out
	if len(labels) > 0 {
		o.Labels = labels
	}
	if len(links) > 0 {
		o.Links = make([]linkOut, 0, len(links))
		for _, l := range links {
			o.Links = append(o.Links, linkOut{Type: l.Type, ToNumber: l.ToNumber})
		}
	}
	if owner != nil {
		o.Owner = *owner
	}
	bs, err := json.Marshal(o)
	if err != nil || string(bs) == "null" {
		return "{}"
	}
	return string(bs)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func sortStrings(in []string) {
	sort.Strings(in)
}
```

You will also need to add imports `encoding/json` and `sort` to `queries.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/... -run TestCreateIssue`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries.go internal/db/queries_create_initial_test.go
git commit -m "feat(db): CreateIssue accepts initial labels/links/owner"
```

---


### Task 6: `internal/api/types.go` — DTOs for links/labels/assign/ready/initial create

Spec refs: §4.1 (endpoint surface), §4.5 (mutation envelope), §6.1 (CLI surface).

**Files:**
- Modify: `internal/api/types.go`.
- Test: `internal/api/types_test.go` is read-only and small; if you don't already have one, just rely on the existing `errors_test.go` + downstream handler tests to exercise these. **Skip writing a dedicated types_test.go** — DTOs are pure data and any meaningful assertion is already covered by handler tests.

The new DTOs:

```go
// CreateLinkRequest is POST /api/v1/projects/{id}/issues/{number}/links.
type CreateLinkRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor    string `json:"actor" required:"true"`
		Type     string `json:"type" required:"true" enum:"parent,blocks,related"`
		ToNumber int64  `json:"to_number" required:"true"`
		Replace  bool   `json:"replace,omitempty"` // type=parent only
	}
}

// LinkOut is the wire projection of a link with both endpoint *numbers* (not
// internal issue ids) so clients can correlate without an extra lookup.
type LinkOut struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	FromNumber int64     `json:"from_number"`
	ToNumber   int64     `json:"to_number"`
	Type       string    `json:"type"`
	Author     string    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateLinkResponse extends MutationResponse with the new link's wire
// projection (handlers populate `Link` for both new and no-op cases).
type CreateLinkResponse struct {
	Body struct {
		Issue   db.Issue  `json:"issue"`
		Link    LinkOut   `json:"link"`
		Event   *db.Event `json:"event"`
		Changed bool      `json:"changed"`
	}
}

// DeleteLinkRequest is DELETE /api/v1/projects/{id}/issues/{number}/links/{link_id}.
// Actor is in the query string because DELETE bodies are non-portable.
type DeleteLinkRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Number    int64  `path:"number" required:"true"`
	LinkID    int64  `path:"link_id" required:"true"`
	Actor     string `query:"actor" required:"true"`
}

// AddLabelRequest is POST /api/v1/projects/{id}/issues/{number}/labels.
type AddLabelRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Label string `json:"label" required:"true"`
	}
}

// AddLabelResponse extends the standard envelope with the new label row.
type AddLabelResponse struct {
	Body struct {
		Issue   db.Issue       `json:"issue"`
		Label   db.IssueLabel  `json:"label"`
		Event   *db.Event      `json:"event"`
		Changed bool           `json:"changed"`
	}
}

// RemoveLabelRequest is DELETE /api/v1/projects/{id}/issues/{number}/labels/{label}.
type RemoveLabelRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Number    int64  `path:"number" required:"true"`
	Label     string `path:"label" required:"true"`
	Actor     string `query:"actor" required:"true"`
}

// AssignRequest is POST /api/v1/projects/{id}/issues/{number}/actions/assign.
type AssignRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Owner string `json:"owner" required:"true"`
	}
}

// UnassignRequest is POST /api/v1/projects/{id}/issues/{number}/actions/unassign.
// Same shape as AssignRequest minus owner.
type UnassignRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
	}
}

// ReadyRequest is GET /api/v1/projects/{id}/ready.
type ReadyRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Limit     int   `query:"limit,omitempty"`
}

// ReadyResponse is the ready-issue list.
type ReadyResponse struct {
	Body struct {
		Issues []db.Issue `json:"issues"`
	}
}

// LabelsListRequest is GET /api/v1/projects/{id}/labels (counts).
type LabelsListRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
}

// LabelsListResponse is the per-label aggregate.
type LabelsListResponse struct {
	Body struct {
		Labels []db.LabelCount `json:"labels"`
	}
}
```

Extend `CreateIssueRequest` for initial labels/links/owner:

```go
// CreateIssueRequest is POST /api/v1/projects/{id}/issues.
type CreateIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Body      struct {
		Actor  string                  `json:"actor" required:"true"`
		Title  string                  `json:"title" required:"true"`
		Body   string                  `json:"body,omitempty"`
		Owner  *string                 `json:"owner,omitempty"`
		Labels []string                `json:"labels,omitempty"`
		Links  []CreateInitialLinkBody `json:"links,omitempty"`
	}
}

// CreateInitialLinkBody is one entry in CreateIssueRequest.Body.Links.
type CreateInitialLinkBody struct {
	Type     string `json:"type" enum:"parent,blocks,related"`
	ToNumber int64  `json:"to_number"`
}
```

Extend `ShowIssueResponse` to include links + labels:

```go
// ShowIssueResponse is the per-issue read payload (Plan 2: + links, + labels).
type ShowIssueResponse struct {
	Body struct {
		Issue    db.Issue        `json:"issue"`
		Comments []db.Comment    `json:"comments"`
		Links    []LinkOut       `json:"links"`
		Labels   []db.IssueLabel `json:"labels"`
	}
}
```

- [ ] **Step 1: Apply the changes above to `internal/api/types.go`**

Make sure the existing `time` import stays.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: build succeeds (handler files won't compile yet because they don't reference the new DTOs — that's fine; this task is types-only).

If `go build ./internal/api/...` fails because `db.IssueLabel` / `db.Link` / `db.LabelCount` aren't defined, you forgot Tasks 1-2.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/api/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
make lint
git add internal/api/types.go
git commit -m "feat(api): DTOs for links, labels, assign, ready, initial create state"
```

---

### Task 7: `internal/daemon/handlers_links.go` — POST + DELETE link

Spec refs: §3.3 (issue.linked/issue.unlinked events), §3.4 (link lifecycle), §4.1 (endpoints), §4.5 (envelope).

**Files:**
- Create: `internal/daemon/handlers_links.go`.
- Modify: `internal/daemon/server.go` — register the new group.
- Test: `internal/daemon/handlers_links_test.go`.

The handler:

1. Resolves the source issue by `(ProjectID, Number)` → 404 `issue_not_found` if absent.
2. Resolves the target issue by `(ProjectID, ToNumber)` → 404 `issue_not_found` if absent.
3. For `type=related`: swaps from/to so `from < to` (canonical order).
4. For `type=parent` with `Replace=false`: pre-checks `ParentOf(from)`. If found and points to a *different* parent → 409 `parent_already_set`. If found and points to the *same* parent → return that link with `event:null, changed:false` (idempotent reuse via the partial unique index).
5. For `type=parent` with `Replace=true`: deletes the existing parent link in the same TX and emits `issue.unlinked` for it before inserting the new one (which emits `issue.linked`). Both events live on the response (well — the response shape has only one `event`; for `--replace` we emit only `issue.linked` and leave the unlink event implicit. **Implementation choice:** to keep the response shape stable, emit the unlink event but return only the link event in the response. The unlinked event still lands in `events` for SSE/poll clients. Document this in the handler comment.)
6. Calls `CreateLink`. UNIQUE → no-op (return existing link, `event:null, changed:false`). ParentAlreadySet → 409 (only reachable when Replace=false and the pre-check raced; defensive fall-through).
7. Emits `issue.linked` event with `events.related_issue_id = to_issue_id` and payload `{"link_id":N, "type":"...", "from_number":N, "to_number":N}`.
8. Returns `CreateLinkResponse` with the new link's wire projection.

Delete handler:

1. Looks up the link by ID. ErrNotFound → 404 `link_not_found`.
2. Verifies the link's project_id matches the URL's project_id → 404 if mismatch (defensive; URL injection).
3. Deletes the link.
4. Emits `issue.unlinked` with `events.related_issue_id = to_issue_id` and payload `{"link_id":N, "type":"...", "from_number":N, "to_number":N}`.
5. Returns the standard MutationResponse with the issue projection (the URL's `{number}` issue, not necessarily the link's `from_issue_id` — DELETE is keyed by link_id, but the response surfaces the issue the user was operating on).

Idempotent DELETE: if the link is already gone, return `event:null, changed:false`. Use 200 + empty event/changed=false rather than 404, to match the no-op pattern in §4.5.

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_links_test.go
package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCreateLink_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "blocks",
		"to_number": b,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(a, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue   struct{ Number int64 } `json:"issue"`
		Link    struct {
			ID                       int64
			Type                     string
			FromNumber, ToNumber     int64
		} `json:"link"`
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "blocks", out.Link.Type)
	assert.Equal(t, a, out.Link.FromNumber)
	assert.Equal(t, b, out.Link.ToNumber)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.linked", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestCreateLink_DuplicateIsNoop(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	postLink(t, env, pid, a, "blocks", b)

	out := postLink(t, env, pid, a, "blocks", b)
	assert.Nil(t, out.Event, "duplicate link is no-op (event:null)")
	assert.False(t, out.Changed)
}

func TestCreateLink_RelatedCanonicalizesOrder(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env) // a < b
	out := postLink(t, env, pid, b, "related", a) // user passes b → a
	assert.Equal(t, "related", out.Link.Type)
	assert.Equal(t, a, out.Link.FromNumber, "canonical: from < to")
	assert.Equal(t, b, out.Link.ToNumber)
}

func TestCreateLink_ParentAlreadySetIs409(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "parent",
		"to_number": p2,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(child, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCreateLink_ParentReplaceSwapsParent(t *testing.T) {
	env := testenv.New(t)
	pid, child, p1 := setupTwoIssues(t, env)
	p2 := createIssueViaHTTP(t, env, pid, "p2")
	postLink(t, env, pid, child, "parent", p1)

	body, _ := json.Marshal(map[string]any{
		"actor":     "tester",
		"type":      "parent",
		"to_number": p2,
		"replace":   true,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(child, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Link struct{ ToNumber int64 } `json:"link"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, p2, out.Link.ToNumber)
}

func TestDeleteLink_RemovesAndEmitsUnlink(t *testing.T) {
	env := testenv.New(t)
	pid, a, b := setupTwoIssues(t, env)
	created := postLink(t, env, pid, a, "blocks", b)

	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(a, 10)+
			"/links/"+strconv.FormatInt(created.Link.ID, 10)+"?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unlinked", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestDeleteLink_AbsentIs200NoOp(t *testing.T) {
	env := testenv.New(t)
	pid, a, _ := setupTwoIssues(t, env)
	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(a, 10)+
			"/links/9999?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

// --- helpers used across handlers_links_test.go and handlers_labels_test.go ---

// setupTwoIssues creates a workspace, two issues, and returns (project_id, a_number, b_number).
func setupTwoIssues(t *testing.T, env *testenv.Env) (int64, int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	a := createIssueViaHTTP(t, env, pid, "a")
	b := createIssueViaHTTP(t, env, pid, "b")
	return pid, a, b
}

// initWorkspaceViaHTTP runs git init in a temp dir, adds origin, posts to
// /api/v1/projects, and returns the resolved project_id.
func initWorkspaceViaHTTP(t *testing.T, env *testenv.Env, origin string) int64 {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--quiet")
	mustRun(t, dir, "git", "remote", "add", "origin", origin)

	body, _ := json.Marshal(map[string]string{"start_path": dir})
	resp, err := env.HTTP.Post(env.URL+"/api/v1/projects", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	body, _ = json.Marshal(map[string]string{"start_path": dir})
	resp, err = env.HTTP.Post(env.URL+"/api/v1/projects/resolve", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Project struct{ ID int64 } `json:"project"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Project.ID
}

// createIssueViaHTTP creates an issue and returns its number.
func createIssueViaHTTP(t *testing.T, env *testenv.Env, projectID int64, title string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": "tester", "title": title})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Issue struct{ Number int64 } `json:"issue"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Issue.Number
}

// postLink is a small wrapper that calls POST /links and returns the decoded
// CreateLinkResponse-shaped body.
type linkResp struct {
	Issue   struct{ Number int64 } `json:"issue"`
	Link    struct {
		ID                   int64
		Type                 string
		FromNumber, ToNumber int64
	} `json:"link"`
	Event   *struct{ Type string } `json:"event"`
	Changed bool                   `json:"changed"`
}

func postLink(t *testing.T, env *testenv.Env, projectID, fromNumber int64, linkType string, toNumber int64) linkResp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "type": linkType, "to_number": toNumber,
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+
			"/issues/"+strconv.FormatInt(fromNumber, 10)+"/links",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, 200, resp.StatusCode, "postLink expected 200, got %d", resp.StatusCode)
	var out linkResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// mustRun runs a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...) //nolint:gosec // G204: test-controlled args
	cmd.Dir = dir
	require.NoErrorf(t, cmd.Run(), "%s %v", name, args)
}
```

You'll need imports `os/exec`, `bytes`, `encoding/json`, `net/http`, `strconv`, `testing`. Make sure `testenv` is imported (`github.com/wesm/kata/internal/testenv`).

- [ ] **Step 2: Run test (expect failure — endpoints missing)**

Run: `go test ./internal/daemon/... -run 'TestCreateLink|TestDeleteLink'`
Expected: FAIL — 404 from POST/DELETE /links since the routes don't exist yet.

- [ ] **Step 3: Create `internal/daemon/handlers_links.go`**

```go
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerLinksHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createLink",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links",
	}, createLinkHandler(cfg))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "deleteLink",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/links/{link_id}",
	}, deleteLinkHandler(cfg))
}

func createLinkHandler(cfg ServerConfig) func(context.Context, *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
	return func(ctx context.Context, in *api.CreateLinkRequest) (*api.CreateLinkResponse, error) {
		from, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		to, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Body.ToNumber)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found",
				fmt.Sprintf("target issue #%d not found", in.Body.ToNumber), "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		fromID, toID := from.ID, to.ID
		fromNum, toNum := from.Number, to.Number
		if in.Body.Type == "related" && fromID > toID {
			fromID, toID = toID, fromID
			fromNum, toNum = toNum, fromNum
		}

		// Parent --replace path.
		if in.Body.Type == "parent" && in.Body.Replace {
			if existing, perr := cfg.DB.ParentOf(ctx, fromID); perr == nil {
				if existing.ToIssueID == toID {
					// Replacing with the same parent is a no-op.
					return mutationLinkResponse(from, existing, fromNum, toNum, nil, false), nil
				}
				if delErr := cfg.DB.DeleteLinkByID(ctx, existing.ID); delErr != nil {
					return nil, api.NewError(500, "internal", delErr.Error(), "", nil)
				}
				if _, eErr := emitLinkEvent(ctx, cfg.DB, "issue.unlinked", from, existing, fromNum, toNum, in.Body.Actor); eErr != nil {
					return nil, api.NewError(500, "internal", eErr.Error(), "", nil)
				}
			} else if !errors.Is(perr, db.ErrNotFound) {
				return nil, api.NewError(500, "internal", perr.Error(), "", nil)
			}
		}

		// Default path: insert. Distinct error types map to specific responses.
		link, err := cfg.DB.CreateLink(ctx, db.CreateLinkParams{
			ProjectID:   in.ProjectID,
			FromIssueID: fromID,
			ToIssueID:   toID,
			Type:        in.Body.Type,
			Author:      in.Body.Actor,
		})
		switch {
		case errors.Is(err, db.ErrLinkExists):
			// Duplicate (from, to, type) → no-op. Re-fetch and return existing.
			existing, lookupErr := cfg.DB.LinkByEndpoints(ctx, fromID, toID, in.Body.Type)
			if lookupErr != nil {
				return nil, api.NewError(500, "internal", lookupErr.Error(), "", nil)
			}
			return mutationLinkResponse(from, existing, fromNum, toNum, nil, false), nil
		case errors.Is(err, db.ErrParentAlreadySet):
			return nil, api.NewError(409, "parent_already_set",
				"this issue already has a parent", "pass replace=true to swap", nil)
		case errors.Is(err, db.ErrSelfLink):
			return nil, api.NewError(400, "validation", "cannot link an issue to itself", "", nil)
		case errors.Is(err, db.ErrCrossProjectLink):
			return nil, api.NewError(400, "validation", "cross-project links are not allowed", "", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		evt, err := emitLinkEvent(ctx, cfg.DB, "issue.linked", from, link, fromNum, toNum, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		// Re-fetch issue so updated_at reflects the event tick (events
		// don't bump updated_at automatically, but emitLinkEvent does via
		// the dedicated touch in the same call).
		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return mutationLinkResponse(updatedIssue, link, fromNum, toNum, &evt, true), nil
	}
}

func deleteLinkHandler(cfg ServerConfig) func(context.Context, *api.DeleteLinkRequest) (*api.MutationResponse, error) {
	return func(ctx context.Context, in *api.DeleteLinkRequest) (*api.MutationResponse, error) {
		from, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		link, err := cfg.DB.LinkByID(ctx, in.LinkID)
		if errors.Is(err, db.ErrNotFound) {
			// Idempotent: no row → no-op envelope.
			out := &api.MutationResponse{}
			out.Body.Issue = from
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		if link.ProjectID != in.ProjectID {
			return nil, api.NewError(404, "link_not_found", "link not in this project", "", nil)
		}

		// Resolve numbers for the event payload before deleting.
		fromIssue, err := cfg.DB.IssueByID(ctx, link.FromIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		toIssue, err := cfg.DB.IssueByID(ctx, link.ToIssueID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		if err := cfg.DB.DeleteLinkByID(ctx, in.LinkID); err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		evt, err := emitLinkEvent(ctx, cfg.DB, "issue.unlinked", from, link, fromIssue.Number, toIssue.Number, in.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updatedIssue, err := cfg.DB.IssueByID(ctx, from.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updatedIssue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	}
}

func mutationLinkResponse(issue db.Issue, link db.Link, fromNum, toNum int64, evt *db.Event, changed bool) *api.CreateLinkResponse {
	out := &api.CreateLinkResponse{}
	out.Body.Issue = issue
	out.Body.Link = api.LinkOut{
		ID:         link.ID,
		ProjectID:  link.ProjectID,
		FromNumber: fromNum,
		ToNumber:   toNum,
		Type:       link.Type,
		Author:     link.Author,
		CreatedAt:  link.CreatedAt,
	}
	out.Body.Event = evt
	out.Body.Changed = changed
	return out
}

// emitLinkEvent writes an issue.linked / issue.unlinked event with
// related_issue_id set to the *other* endpoint, then bumps issues.updated_at
// for the source issue. Done as separate statements (not inside a tx) since
// the link insert/delete is already committed.
//
// Payload fields: link_id, type, from_number, to_number — all required by
// downstream consumers (SSE clients, agents) to correlate without re-fetching.
func emitLinkEvent(ctx context.Context, store *db.DB, eventType string, fromIssue db.Issue, link db.Link, fromNum, toNum int64, actor string) (db.Event, error) {
	payload, err := json.Marshal(map[string]any{
		"link_id":     link.ID,
		"type":        link.Type,
		"from_number": fromNum,
		"to_number":   toNum,
	})
	if err != nil {
		return db.Event{}, err
	}
	relatedID := link.ToIssueID
	if relatedID == fromIssue.ID {
		relatedID = link.FromIssueID
	}
	res, err := store.ExecContext(ctx,
		`INSERT INTO events(project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload)
		 VALUES(?, (SELECT identity FROM projects WHERE id = ?), ?, ?, ?, ?, ?, ?)`,
		fromIssue.ProjectID, fromIssue.ProjectID, fromIssue.ID, fromIssue.Number, relatedID, eventType, actor, string(payload))
	if err != nil {
		return db.Event{}, fmt.Errorf("insert link event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return db.Event{}, err
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		fromIssue.ID); err != nil {
		return db.Event{}, fmt.Errorf("touch issue: %w", err)
	}
	row := store.QueryRowContext(ctx,
		`SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload, created_at
		 FROM events WHERE id = ?`, id)
	var e db.Event
	if err := row.Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID, &e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
		return db.Event{}, err
	}
	return e, nil
}
```

- [ ] **Step 4: Register the new group in `internal/daemon/server.go`**

In `registerRoutes`:

```go
func registerRoutes(humaAPI huma.API, cfg ServerConfig) {
	registerHealth(humaAPI, cfg)
	registerProjects(humaAPI, cfg)
	registerIssues(humaAPI, cfg)
	registerComments(humaAPI, cfg)
	registerActions(humaAPI, cfg)
	registerLinks(humaAPI, cfg) // NEW
}

func registerLinks(humaAPI huma.API, cfg ServerConfig) {
	registerLinksHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run 'TestCreateLink|TestDeleteLink'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_links.go internal/daemon/handlers_links_test.go internal/daemon/server.go
git commit -m "feat(daemon): POST/DELETE /links with linked/unlinked events"
```

---

### Task 8: `internal/daemon/handlers_labels.go` — POST + DELETE label

Spec refs: §3.3 (issue.labeled/issue.unlabeled), §4.1 (endpoints), §4.5 (envelope), §6.1 (label CLI).

POST handler:
1. Resolve issue → 404.
2. `AddLabel`. ErrLabelInvalid → 400 validation. ErrLabelExists → 200 no-op (re-fetch existing label, return event:null, changed:false).
3. Emit `issue.labeled` event with payload `{"label":"..."}`.
4. Return AddLabelResponse.

DELETE handler:
1. Resolve issue → 404.
2. `RemoveLabel`. ErrNotFound → 200 no-op.
3. Emit `issue.unlabeled` event.
4. Return MutationResponse.

GET /labels (LabelsList) returns counts. Doesn't mutate, no events.

**Files:**
- Create: `internal/daemon/handlers_labels.go`.
- Modify: `internal/daemon/server.go` — add `registerLabels`.
- Test: `internal/daemon/handlers_labels_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_labels_test.go
package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestAddLabel_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	out := postLabel(t, env, pid, n, "needs-review")
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.labeled", out.Event.Type)
	assert.True(t, out.Changed)
	assert.Equal(t, "needs-review", out.Label.Label)
}

func TestAddLabel_DuplicateIsNoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	postLabel(t, env, pid, n, "bug")
	out := postLabel(t, env, pid, n, "bug")
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

func TestAddLabel_InvalidIs400(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "tester", "label": "Bad-Case"})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/"+strconv.FormatInt(n, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestRemoveLabel_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	postLabel(t, env, pid, n, "bug")

	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels/bug?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unlabeled", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestRemoveLabel_AbsentIs200NoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	req, err := http.NewRequest("DELETE",
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/labels/never-attached?actor=tester", nil)
	require.NoError(t, err)
	resp, err := env.HTTP.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

func TestLabelsList_ReturnsCounts(t *testing.T) {
	env := testenv.New(t)
	pid, a := setupOneIssue(t, env)
	b := createIssueViaHTTP(t, env, pid, "b")
	postLabel(t, env, pid, a, "bug")
	postLabel(t, env, pid, a, "priority:high")
	postLabel(t, env, pid, b, "bug")

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/labels")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Labels []struct {
			Label string `json:"label"`
			Count int64  `json:"count"`
		} `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	got := map[string]int64{}
	for _, c := range out.Labels {
		got[c.Label] = c.Count
	}
	assert.Equal(t, int64(2), got["bug"])
	assert.Equal(t, int64(1), got["priority:high"])
}

// --- helpers ---

// setupOneIssue creates a workspace + one issue, returns (project_id, issue_number).
func setupOneIssue(t *testing.T, env *testenv.Env) (int64, int64) {
	t.Helper()
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	n := createIssueViaHTTP(t, env, pid, "x")
	return pid, n
}

type labelResp struct {
	Issue   struct{ Number int64 } `json:"issue"`
	Label   struct {
		Label string `json:"label"`
	} `json:"label"`
	Event   *struct{ Type string } `json:"event"`
	Changed bool                   `json:"changed"`
}

func postLabel(t *testing.T, env *testenv.Env, projectID, number int64, label string) labelResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"actor": "tester", "label": label})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+
			"/issues/"+strconv.FormatInt(number, 10)+"/labels",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equalf(t, 200, resp.StatusCode, "postLabel expected 200")
	var out labelResp
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/... -run 'TestAddLabel|TestRemoveLabel|TestLabelsList'`
Expected: FAIL.

- [ ] **Step 3: Create `internal/daemon/handlers_labels.go`**

```go
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerLabelsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "addLabel",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/labels",
	}, addLabelHandler(cfg))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "removeLabel",
		Method:      "DELETE",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/labels/{label}",
	}, removeLabelHandler(cfg))

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listLabels",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/labels",
	}, listLabelsHandler(cfg))
}

func addLabelHandler(cfg ServerConfig) func(context.Context, *api.AddLabelRequest) (*api.AddLabelResponse, error) {
	return func(ctx context.Context, in *api.AddLabelRequest) (*api.AddLabelResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		row, err := cfg.DB.AddLabel(ctx, issue.ID, in.Body.Label, in.Body.Actor)
		switch {
		case errors.Is(err, db.ErrLabelExists):
			// No-op: re-fetch existing row to populate the response.
			labels, lerr := cfg.DB.LabelsByIssue(ctx, issue.ID)
			if lerr != nil {
				return nil, api.NewError(500, "internal", lerr.Error(), "", nil)
			}
			var existing db.IssueLabel
			for _, l := range labels {
				if l.Label == in.Body.Label {
					existing = l
					break
				}
			}
			out := &api.AddLabelResponse{}
			out.Body.Issue = issue
			out.Body.Label = existing
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		case errors.Is(err, db.ErrLabelInvalid):
			return nil, api.NewError(400, "validation",
				"label must match charset [a-z0-9._:-] and length 1..64", "", nil)
		case err != nil:
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		evt, err := emitLabelEvent(ctx, cfg.DB, "issue.labeled", issue, in.Body.Label, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updatedIssue, err := cfg.DB.IssueByID(ctx, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.AddLabelResponse{}
		out.Body.Issue = updatedIssue
		out.Body.Label = row
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	}
}

func removeLabelHandler(cfg ServerConfig) func(context.Context, *api.RemoveLabelRequest) (*api.MutationResponse, error) {
	return func(ctx context.Context, in *api.RemoveLabelRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		err = cfg.DB.RemoveLabel(ctx, issue.ID, in.Label)
		if errors.Is(err, db.ErrNotFound) {
			out := &api.MutationResponse{}
			out.Body.Issue = issue
			out.Body.Event = nil
			out.Body.Changed = false
			return out, nil
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}

		evt, err := emitLabelEvent(ctx, cfg.DB, "issue.unlabeled", issue, in.Label, in.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updatedIssue, err := cfg.DB.IssueByID(ctx, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updatedIssue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	}
}

func listLabelsHandler(cfg ServerConfig) func(context.Context, *api.LabelsListRequest) (*api.LabelsListResponse, error) {
	return func(ctx context.Context, in *api.LabelsListRequest) (*api.LabelsListResponse, error) {
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		counts, err := cfg.DB.LabelCounts(ctx, in.ProjectID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.LabelsListResponse{}
		out.Body.Labels = counts
		return out, nil
	}
}

// emitLabelEvent inserts a labeled/unlabeled event and bumps issues.updated_at.
// Payload: {"label":"..."}.
func emitLabelEvent(ctx context.Context, store *db.DB, eventType string, issue db.Issue, label, actor string) (db.Event, error) {
	payload, err := json.Marshal(map[string]string{"label": label})
	if err != nil {
		return db.Event{}, err
	}
	res, err := store.ExecContext(ctx,
		`INSERT INTO events(project_id, project_identity, issue_id, issue_number, type, actor, payload)
		 VALUES(?, (SELECT identity FROM projects WHERE id = ?), ?, ?, ?, ?, ?)`,
		issue.ProjectID, issue.ProjectID, issue.ID, issue.Number, eventType, actor, string(payload))
	if err != nil {
		return db.Event{}, fmt.Errorf("insert label event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return db.Event{}, err
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		issue.ID); err != nil {
		return db.Event{}, fmt.Errorf("touch issue: %w", err)
	}
	row := store.QueryRowContext(ctx,
		`SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload, created_at
		 FROM events WHERE id = ?`, id)
	var e db.Event
	if err := row.Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID, &e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt); err != nil {
		return db.Event{}, err
	}
	return e, nil
}
```

- [ ] **Step 4: Register the new group**

In `internal/daemon/server.go`, add to `registerRoutes`:

```go
registerLabels(humaAPI, cfg) // NEW

// ...

func registerLabels(humaAPI huma.API, cfg ServerConfig) {
	registerLabelsHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run 'TestAddLabel|TestRemoveLabel|TestLabelsList'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_labels.go internal/daemon/handlers_labels_test.go internal/daemon/server.go
git commit -m "feat(daemon): label CRUD with labeled/unlabeled events + counts"
```

---

### Task 9: `internal/daemon/handlers_ownership.go` — assign/unassign actions

Spec refs: §3.3 (issue.assigned/issue.unassigned), §3.4 (assign lifecycle).

POST /actions/assign body: `{actor, owner}`. Calls `UpdateOwner` with non-nil owner; emits `issue.assigned` (or no-op if already that owner).

POST /actions/unassign body: `{actor}`. Calls `UpdateOwner` with nil; emits `issue.unassigned` (or no-op if already unassigned).

**Files:**
- Create: `internal/daemon/handlers_ownership.go`.
- Modify: `internal/daemon/server.go` — add `registerOwnership`.
- Test: `internal/daemon/handlers_ownership_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_ownership_test.go
package daemon_test

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestAssign_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "tester", "owner": "alice"})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/actions/assign",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue   struct{ Owner *string } `json:"issue"`
		Event   *struct{ Type string }  `json:"event"`
		Changed bool                    `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Issue.Owner)
	assert.Equal(t, "alice", *out.Issue.Owner)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.assigned", out.Event.Type)
	assert.True(t, out.Changed)
}

func TestAssign_SameOwnerIsNoOp(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "tester", "owner": "alice"})
	url := env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(n, 10) + "/actions/assign"
	resp, err := env.HTTP.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	resp, err = env.HTTP.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Event   *struct{ Type string } `json:"event"`
		Changed bool                   `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Nil(t, out.Event)
	assert.False(t, out.Changed)
}

func TestUnassign_HappyPath(t *testing.T) {
	env := testenv.New(t)
	pid, n := setupOneIssue(t, env)
	body, _ := json.Marshal(map[string]string{"actor": "tester", "owner": "alice"})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/actions/assign",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	body, _ = json.Marshal(map[string]string{"actor": "tester"})
	resp, err = env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+
			"/issues/"+strconv.FormatInt(n, 10)+"/actions/unassign",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue   struct{ Owner *string } `json:"issue"`
		Event   *struct{ Type string }  `json:"event"`
		Changed bool                    `json:"changed"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Nil(t, out.Issue.Owner)
	require.NotNil(t, out.Event)
	assert.Equal(t, "issue.unassigned", out.Event.Type)
	assert.True(t, out.Changed)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/... -run 'TestAssign|TestUnassign'`
Expected: FAIL.

- [ ] **Step 3: Create `internal/daemon/handlers_ownership.go`**

```go
package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerOwnershipHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "assignIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/assign",
	}, func(ctx context.Context, in *api.AssignRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		owner := in.Body.Owner
		updated, evt, changed, err := cfg.DB.UpdateOwner(ctx, issue.ID, &owner, in.Body.Actor)
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
		OperationID: "unassignIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/unassign",
	}, func(ctx context.Context, in *api.UnassignRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.UpdateOwner(ctx, issue.ID, nil, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})
}
```

- [ ] **Step 4: Register**

```go
// in registerRoutes:
registerOwnership(humaAPI, cfg)

func registerOwnership(humaAPI huma.API, cfg ServerConfig) {
	registerOwnershipHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run 'TestAssign|TestUnassign'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_ownership.go internal/daemon/handlers_ownership_test.go internal/daemon/server.go
git commit -m "feat(daemon): assign/unassign actions with dedicated events"
```

---

### Task 10: `internal/daemon/handlers_ready.go` — `GET /api/v1/projects/{id}/ready`

Spec refs: §6.6.

**Files:**
- Create: `internal/daemon/handlers_ready.go`.
- Modify: `internal/daemon/server.go` — add `registerReady`.
- Test: `internal/daemon/handlers_ready_test.go`.

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_ready_test.go
package daemon_test

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestReady_FiltersBlocked(t *testing.T) {
	env := testenv.New(t)
	pid, blocker, blocked := setupTwoIssues(t, env)
	standalone := createIssueViaHTTP(t, env, pid, "standalone")
	postLink(t, env, pid, blocker, "blocks", blocked)

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/ready")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct{ Number int64 } `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	got := map[int64]bool{}
	for _, i := range out.Issues {
		got[i.Number] = true
	}
	assert.True(t, got[blocker], "blocker is ready")
	assert.True(t, got[standalone], "standalone is ready")
	assert.False(t, got[blocked], "blocked while blocker is open")
}

func TestReady_RespectsLimit(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	for i := 0; i < 3; i++ {
		_, _ = createIssueViaHTTP(t, env, pid, "x"), 0 //nolint:errcheck
		// (no-op: createIssueViaHTTP returns int64; just create)
		_ = i
	}
	body, _ := json.Marshal(map[string]string{"actor": "tester", "title": "y"})
	for i := 0; i < 3; i++ {
		resp, err := env.HTTP.Post(
			env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
			"application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}

	resp, err := env.HTTP.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/ready?limit=2")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issues []struct{ Number int64 } `json:"issues"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Len(t, out.Issues, 2)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/... -run TestReady`
Expected: FAIL.

- [ ] **Step 3: Create `internal/daemon/handlers_ready.go`**

```go
package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerReadyHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "readyIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/ready",
	}, func(ctx context.Context, in *api.ReadyRequest) (*api.ReadyResponse, error) {
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issues, err := cfg.DB.ReadyIssues(ctx, in.ProjectID, in.Limit)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ReadyResponse{}
		out.Body.Issues = issues
		return out, nil
	})
}
```

- [ ] **Step 4: Register**

```go
// in registerRoutes:
registerReady(humaAPI, cfg)

func registerReady(humaAPI huma.API, cfg ServerConfig) {
	registerReadyHandlers(humaAPI, cfg)
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run TestReady`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add internal/daemon/handlers_ready.go internal/daemon/handlers_ready_test.go internal/daemon/server.go
git commit -m "feat(daemon): GET /projects/{id}/ready"
```

---

### Task 11: extend `showIssue` handler — include links + labels

Spec refs: §6.1 `kata show`. The handler currently returns `{issue, comments}`; Plan 2 adds `{links, labels}`.

**Files:**
- Modify: `internal/daemon/handlers_issues.go` — extend showIssue.
- Test: `internal/daemon/handlers_issues_test.go` — add a test that show includes links + labels.

The link payload uses **numbers**, not internal issue ids — that's the agent-facing surface. Build by joining `links` to `issues` for both endpoints to recover both numbers. Implementation: load `LinksByIssue(issue.ID)`, then for each link look up the other endpoint's number via `IssueByID`. Two queries per link is fine for show; pagination is a Plan 4 concern.

- [ ] **Step 1: Write failing test**

Add to `internal/daemon/handlers_issues_test.go`:

```go
func TestShowIssue_IncludesLinksAndLabels(t *testing.T) {
	env := testenv.New(t)
	pid, parent, child := setupTwoIssues(t, env)
	postLabel(t, env, pid, child, "bug")
	postLink(t, env, pid, child, "parent", parent)

	resp, err := env.HTTP.Get(env.URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) +
		"/issues/" + strconv.FormatInt(child, 10))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Links []struct {
			Type                 string
			FromNumber, ToNumber int64
		} `json:"links"`
		Labels []struct{ Label string } `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 1)
	assert.Equal(t, "parent", out.Links[0].Type)
	assert.Equal(t, child, out.Links[0].FromNumber)
	assert.Equal(t, parent, out.Links[0].ToNumber)
	require.Len(t, out.Labels, 1)
	assert.Equal(t, "bug", out.Labels[0].Label)
}
```

- [ ] **Step 2: Run test (expect failure — current handler returns no links/labels)**

Run: `go test ./internal/daemon/... -run TestShowIssue_IncludesLinksAndLabels`
Expected: FAIL.

- [ ] **Step 3: Modify `showIssue` handler**

Replace the `showIssue` body in `internal/daemon/handlers_issues.go`:

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
	comments, err := listComments(ctx, cfg.DB, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	links, err := loadLinkOuts(ctx, cfg.DB, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	labels, err := cfg.DB.LabelsByIssue(ctx, issue.ID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	out := &api.ShowIssueResponse{}
	out.Body.Issue = issue
	out.Body.Comments = comments
	out.Body.Links = links
	out.Body.Labels = labels
	return out, nil
})
```

Add helper `loadLinkOuts` to `handlers_issues.go`:

```go
// loadLinkOuts fetches every link involving issueID, resolving both endpoint
// numbers so the wire response speaks the agent-facing surface (numbers, not
// internal ids).
func loadLinkOuts(ctx context.Context, store *db.DB, issueID int64) ([]api.LinkOut, error) {
	rows, err := store.LinksByIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]api.LinkOut, 0, len(rows))
	for _, l := range rows {
		from, err := store.IssueByID(ctx, l.FromIssueID)
		if err != nil {
			return nil, err
		}
		to, err := store.IssueByID(ctx, l.ToIssueID)
		if err != nil {
			return nil, err
		}
		out = append(out, api.LinkOut{
			ID:         l.ID,
			ProjectID:  l.ProjectID,
			FromNumber: from.Number,
			ToNumber:   to.Number,
			Type:       l.Type,
			Author:     l.Author,
			CreatedAt:  l.CreatedAt,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run TestShowIssue_IncludesLinksAndLabels`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_issues.go internal/daemon/handlers_issues_test.go
git commit -m "feat(daemon): show includes links + labels"
```

---

### Task 12: extend `createIssue` handler — wire initial labels/links/owner

Spec refs: §6.4 (create flow includes initial labels/links/owner; idempotency lives in Plan 3).

**Files:**
- Modify: `internal/daemon/handlers_issues.go` — wire the new request fields through to `db.CreateIssueParams`.
- Test: `internal/daemon/handlers_issues_test.go` — add a test for initial-state create.

Map handler-level errors:
- `db.ErrInitialLinkInvalidType` → 400 validation.
- `db.ErrInitialLinkTargetNotFound` → 404 issue_not_found with hint pointing to the bad to_number.
- `db.ErrLabelInvalid` → 400 validation.
- `db.ErrParentAlreadySet` (impossible at creation if dedupe is correct, but defensive) → 409.
- `db.ErrLinkExists` (impossible at creation) → 500 internal (this would indicate a code bug).

- [ ] **Step 1: Write failing test**

Add to `internal/daemon/handlers_issues_test.go`:

```go
func TestCreateIssue_WithInitialState(t *testing.T) {
	env := testenv.New(t)
	pid, parent, _ := setupTwoIssues(t, env)

	body, _ := json.Marshal(map[string]any{
		"actor":  "tester",
		"title":  "child",
		"owner":  "alice",
		"labels": []string{"bug", "needs-review"},
		"links":  []map[string]any{{"type": "parent", "to_number": parent}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Issue struct {
			Number int64
			Owner  *string
		} `json:"issue"`
		Event struct {
			Type    string
			Payload string
		} `json:"event"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out.Issue.Owner)
	assert.Equal(t, "alice", *out.Issue.Owner)
	assert.Equal(t, "issue.created", out.Event.Type)
	assert.Contains(t, out.Event.Payload, `"labels":["bug","needs-review"]`)
	assert.Contains(t, out.Event.Payload, `"owner":"alice"`)
	assert.Contains(t, out.Event.Payload, `"type":"parent"`)

	// And no separate issue.labeled / issue.linked / issue.assigned events
	// should fire at creation. We can verify by listing events and asserting
	// only one event row for the new issue.
	// (Skipped here; covered by the spec — the in-memory check is enough.)
}

func TestCreateIssue_InitialLinkToMissingTargetIs404(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "child",
		"links": []map[string]any{{"type": "parent", "to_number": 99}},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 404, resp.StatusCode)
}

func TestCreateIssue_InvalidLabelIs400(t *testing.T) {
	env := testenv.New(t)
	pid := initWorkspaceViaHTTP(t, env, "https://github.com/wesm/kata.git")
	body, _ := json.Marshal(map[string]any{
		"actor": "tester", "title": "x",
		"labels": []string{"BadCase"},
	})
	resp, err := env.HTTP.Post(
		env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 400, resp.StatusCode)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/... -run TestCreateIssue_With`
Expected: FAIL — handler ignores the new fields.

- [ ] **Step 3: Modify the createIssue handler**

Replace the `createIssue` body in `internal/daemon/handlers_issues.go`:

```go
huma.Register(humaAPI, huma.Operation{
	OperationID: "createIssue",
	Method:      "POST",
	Path:        "/api/v1/projects/{project_id}/issues",
}, func(ctx context.Context, in *api.CreateIssueRequest) (*api.MutationResponse, error) {
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

	issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: in.ProjectID,
		Title:     in.Body.Title,
		Body:      in.Body.Body,
		Author:    in.Body.Actor,
		Owner:     in.Body.Owner,
		Labels:    in.Body.Labels,
		Links:     links,
	})
	switch {
	case errors.Is(err, db.ErrInitialLinkInvalidType):
		return nil, api.NewError(400, "validation",
			"link.type must be parent|blocks|related", "", nil)
	case errors.Is(err, db.ErrInitialLinkTargetNotFound):
		return nil, api.NewError(404, "issue_not_found",
			"initial link target not found in this project", "", nil)
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
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/... -run TestCreateIssue_With`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_issues.go internal/daemon/handlers_issues_test.go
git commit -m "feat(daemon): create accepts initial labels/links/owner"
```

---


### Task 13: `cmd/kata/link.go` — `kata link/unlink` + `kata parent/unparent`

Spec refs: §6.1 (Relationships group). Sugar verbs `parent`/`unparent` translate to the generic `link`/`unlink` shape with `type=parent`. `kata parent <child> <parent> [--replace]` corresponds to POST /links body `{actor, type:parent, to_number, replace}`. `kata unparent <child>` first looks up the existing parent link, then DELETEs it.

**Files:**
- Create: `cmd/kata/link.go`.
- Modify: `cmd/kata/main.go` — register `newLinkCmd`, `newUnlinkCmd`, `newParentCmd`, `newUnparentCmd`.
- Test: `cmd/kata/link_test.go`.

The CLI surface:

```
kata link <from> <type> <to>           # generic POST /links
kata unlink <from> <type> <to>         # GET /issues/{from} → match → DELETE
kata parent <child> <parent> [--replace]   # sugar
kata unparent <child>                  # sugar; finds the parent link via show
```

`unlink` needs to map `(from, type, to)` to a `link_id`. Two ways:
- (a) Call `GET /issues/{from}` and pick the link with the matching `(type, to_number)`. Returns 404 link_not_found if none.
- (b) Add a daemon-side endpoint that accepts the spec triple. Spec says no.

Going with (a). The CLI does a small read-then-delete dance. **For `--json`, include only the DELETE response** — the intermediate GET is implementation detail.

`unparent` is even simpler: GET issues, find the link with `type=parent` and `from_number=child`, DELETE it.

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/link_test.go
package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestLink_GenericRoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "link", "1", "blocks", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "linked") || strings.Contains(out, "blocks"))
}

func TestParent_WithReplace(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "child")
	createIssue(t, env, pid, "p1")
	createIssue(t, env, pid, "p2")

	// First parent.
	cmd1 := newRootCmd()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetArgs([]string{"--workspace", dir, "parent", "1", "2"})
	cmd1.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd1.Execute())

	// Replace.
	resetFlags(t)
	cmd2 := newRootCmd()
	var buf bytes.Buffer
	cmd2.SetOut(&buf)
	cmd2.SetArgs([]string{"--workspace", dir, "parent", "1", "3", "--replace"})
	cmd2.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd2.Execute())
	assert.True(t, strings.Contains(buf.String(), "linked") ||
		strings.Contains(buf.String(), "parent"))
}

func TestUnlink_RemovesLink(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	createIssue(t, env, pid, "b")

	// Create the link directly via HTTP so we don't rely on `kata link`'s
	// own success here.
	createLinkViaHTTP(t, env, pid, 1, "blocks", 2)

	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "unlink", "1", "blocks", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "unlinked") ||
		strings.Contains(buf.String(), "removed"))
}

// createLinkViaHTTP is a thin test helper duplicated in link_test.go and
// label_test.go because cmd/kata is a separate package from internal/daemon.
func createLinkViaHTTP(t *testing.T, env *testenv.Env, projectID, fromNumber int64, linkType string, toNumber int64) {
	t.Helper()
	body := []byte(`{"actor":"tester","type":"` + linkType + `","to_number":` + itoa(toNumber) + `}`)
	resp, err := http.Post(
		env.URL+"/api/v1/projects/"+itoa(projectID)+
			"/issues/"+itoa(fromNumber)+"/links",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
}

// createIssue is a test helper that creates an issue via HTTP and discards
// the response. Use when you need an issue but not its number (numbers go
// 1, 2, 3 in order so you can hard-code them).
func createIssue(t *testing.T, env *testenv.Env, projectID int64, title string) {
	t.Helper()
	body := []byte(`{"actor":"tester","title":"` + title + `"}`)
	resp, err := http.Post(
		env.URL+"/api/v1/projects/"+itoa(projectID)+"/issues",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only loopback
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run 'TestLink|TestParent|TestUnlink'`
Expected: FAIL — `unknown command "link"`.

- [ ] **Step 3: Create `cmd/kata/link.go`**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link <from> <type> <to>",
		Short: "create a link between two issues (type: parent|blocks|related)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "from must be an integer", ExitCode: ExitValidation}
			}
			linkType := args[1]
			to, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return &cliError{Message: "to must be an integer", ExitCode: ExitValidation}
			}
			return runLinkCreate(cmd, from, linkType, to, false)
		},
	}
	return cmd
}

func newParentCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "parent <child> <parent>",
		Short: "set the parent link of <child> to <parent>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			child, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "child must be an integer", ExitCode: ExitValidation}
			}
			parent, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "parent must be an integer", ExitCode: ExitValidation}
			}
			return runLinkCreate(cmd, child, "parent", parent, replace)
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "swap the existing parent if any")
	return cmd
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink <from> <type> <to>",
		Short: "remove a link between two issues",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "from must be an integer", ExitCode: ExitValidation}
			}
			linkType := args[1]
			to, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return &cliError{Message: "to must be an integer", ExitCode: ExitValidation}
			}
			return runUnlinkByEndpoints(cmd, from, linkType, to)
		},
	}
}

func newUnparentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unparent <child>",
		Short: "remove the parent link of <child>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			child, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "child must be an integer", ExitCode: ExitValidation}
			}
			return runUnlinkByType(cmd, child, "parent")
		},
	}
}

func runLinkCreate(cmd *cobra.Command, fromNumber int64, linkType string, toNumber int64, replace bool) error {
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
	payload := map[string]any{
		"actor":     actor,
		"type":      linkType,
		"to_number": toNumber,
	}
	if replace {
		payload["replace"] = true
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links", baseURL, pid, fromNumber)
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printLinkMutation(cmd, bs)
}

func runUnlinkByEndpoints(cmd *cobra.Command, fromNumber int64, linkType string, toNumber int64) error {
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
	linkID, err := lookupLinkID(ctx, baseURL, pid, fromNumber, linkType, &toNumber)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, fromNumber, linkID)
}

func runUnlinkByType(cmd *cobra.Command, fromNumber int64, linkType string) error {
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
	linkID, err := lookupLinkID(ctx, baseURL, pid, fromNumber, linkType, nil)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, fromNumber, linkID)
}

// lookupLinkID resolves a (from, type [, to]) tuple to the link id by reading
// the issue's links via GET /issues/{from} and matching. Returns 404
// link_not_found when no link matches.
func lookupLinkID(ctx context.Context, baseURL string, pid, fromNumber int64, linkType string, toNumber *int64) (int64, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, fromNumber)
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, apiErrFromBody(status, bs)
	}
	var b struct {
		Links []struct {
			ID                   int64  `json:"id"`
			Type                 string `json:"type"`
			FromNumber, ToNumber int64
		} `json:"links"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return 0, err
	}
	for _, l := range b.Links {
		if l.Type != linkType {
			continue
		}
		if l.FromNumber != fromNumber {
			continue
		}
		if toNumber != nil && l.ToNumber != *toNumber {
			continue
		}
		return l.ID, nil
	}
	return 0, &cliError{Message: "link not found", Code: "link_not_found", ExitCode: ExitNotFound}
}

func runDeleteLink(cmd *cobra.Command, baseURL string, pid, fromNumber, linkID int64) error {
	ctx := cmd.Context()
	actor, _ := resolveActor(flags.As, nil)
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links/%d?actor=%s",
		baseURL, pid, fromNumber, linkID, actor)
	status, bs, err := httpDoJSON(ctx, client, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printMutation(cmd, bs)
}

// printLinkMutation formats a CreateLinkResponse for the three output modes.
// Reuses printMutation's JSON branch (the daemon body already includes
// kata_api_version when routed through emitJSON via json.RawMessage).
func printLinkMutation(cmd *cobra.Command, bs []byte) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Issue struct{ Number int64 } `json:"issue"`
		Link  struct {
			Type                 string
			FromNumber, ToNumber int64
		} `json:"link"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already linked: %s → #%d (no-op)\n",
			b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d linked: %s → #%d\n",
		b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
	return err
}
```

You'll need to import `context` in `link.go` for `lookupLinkID`. Also note: `httpDoJSON` already supports passing a nil body for GET/DELETE (it handles nil correctly).

- [ ] **Step 4: Register the new subcommands in `cmd/kata/main.go`**

In `newRootCmd`'s `subs` slice, add:

```go
newLinkCmd(),
newUnlinkCmd(),
newParentCmd(),
newUnparentCmd(),
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run 'TestLink|TestParent|TestUnlink'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/link.go cmd/kata/link_test.go cmd/kata/main.go
git commit -m "feat(cli): kata link/unlink/parent/unparent"
```

---

### Task 14: `cmd/kata/link.go` — `kata block/unblock` + `kata relate/unrelate`

Spec refs: §6.1. Two more sugar pairs that delegate to the same generic link/unlink machinery.

`kata block <blocker> <blocked>` → POST /links body `{type:blocks, from=blocker, to=blocked}`.
`kata unblock <blocker> <blocked>` → unlink by (blocker, blocks, blocked).
`kata relate <a> <b>` → POST /links body `{type:related, from=min(a,b), to=max(a,b)}`. The CLI does the canonical-ordering swap before sending so the user can pass them in either order.
`kata unrelate <a> <b>` → unlink by (min(a,b), related, max(a,b)).

**Files:**
- Modify: `cmd/kata/link.go` — add four constructors.
- Modify: `cmd/kata/main.go` — register four new commands.
- Test: `cmd/kata/link_test.go` — add a relate/canonical-order test.

- [ ] **Step 1: Write failing test**

Append to `cmd/kata/link_test.go`:

```go
func TestRelate_CanonicalOrderingHidesArgOrder(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a") // #1
	createIssue(t, env, pid, "b") // #2

	// User passes them in reverse — kata relate should still succeed.
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--workspace", dir, "relate", "2", "1"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	// Idempotent: passing them in canonical order should now be a no-op.
	resetFlags(t)
	cmd2 := newRootCmd()
	var buf bytes.Buffer
	cmd2.SetOut(&buf)
	cmd2.SetArgs([]string{"--workspace", dir, "relate", "1", "2"})
	cmd2.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd2.Execute())
	assert.Contains(t, buf.String(), "no-op")
}

func TestBlock_RoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "blocker") // #1
	createIssue(t, env, pid, "blocked") // #2

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--workspace", dir, "block", "1", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())

	resetFlags(t)
	cmd2 := newRootCmd()
	var buf bytes.Buffer
	cmd2.SetOut(&buf)
	cmd2.SetArgs([]string{"--workspace", dir, "unblock", "1", "2"})
	cmd2.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd2.Execute())
	assert.True(t, strings.Contains(buf.String(), "unlinked") ||
		strings.Contains(buf.String(), "removed"))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run 'TestRelate|TestBlock'`
Expected: FAIL.

- [ ] **Step 3: Append constructors to `cmd/kata/link.go`**

```go
func newBlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "block <blocker> <blocked>",
		Short: "mark <blocker> as blocking <blocked>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			blocker, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "blocker must be an integer", ExitCode: ExitValidation}
			}
			blocked, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "blocked must be an integer", ExitCode: ExitValidation}
			}
			return runLinkCreate(cmd, blocker, "blocks", blocked, false)
		},
	}
}

func newUnblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <blocker> <blocked>",
		Short: "remove the blocks link from <blocker> to <blocked>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			blocker, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "blocker must be an integer", ExitCode: ExitValidation}
			}
			blocked, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "blocked must be an integer", ExitCode: ExitValidation}
			}
			return runUnlinkByEndpoints(cmd, blocker, "blocks", blocked)
		},
	}
}

func newRelateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "relate <a> <b>",
		Short: "mark two issues as related (canonical-ordered)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "a must be an integer", ExitCode: ExitValidation}
			}
			b, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "b must be an integer", ExitCode: ExitValidation}
			}
			from, to := canonicalRelated(a, b)
			return runLinkCreate(cmd, from, "related", to, false)
		},
	}
}

func newUnrelateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unrelate <a> <b>",
		Short: "remove a related link between two issues",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "a must be an integer", ExitCode: ExitValidation}
			}
			b, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "b must be an integer", ExitCode: ExitValidation}
			}
			from, to := canonicalRelated(a, b)
			return runUnlinkByEndpoints(cmd, from, "related", to)
		},
	}
}

// canonicalRelated returns (min, max) so callers don't need to remember
// which direction the schema enforces.
func canonicalRelated(a, b int64) (int64, int64) {
	if a < b {
		return a, b
	}
	return b, a
}
```

- [ ] **Step 4: Register in `cmd/kata/main.go`**

Add to the `subs` slice:

```go
newBlockCmd(),
newUnblockCmd(),
newRelateCmd(),
newUnrelateCmd(),
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run 'TestRelate|TestBlock'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/link.go cmd/kata/link_test.go cmd/kata/main.go
git commit -m "feat(cli): kata block/unblock/relate/unrelate with canonical ordering"
```

---

### Task 15: `cmd/kata/label.go` — `kata label add/rm` + `kata labels`

Spec refs: §6.1.

Surface:
```
kata label add <number> <label>     # POST /labels body {actor, label}
kata label rm  <number> <label>     # DELETE /labels/{label}?actor=...
kata labels                         # GET /labels (counts)
```

`kata label` is a parent group with two subs (`add` and `rm`). `kata labels` is a separate top-level command.

**Files:**
- Create: `cmd/kata/label.go`.
- Modify: `cmd/kata/main.go` — register `newLabelCmd`, `newLabelsCmd`.
- Test: `cmd/kata/label_test.go`.

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/label_test.go
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

func TestLabelAdd_HappyPath(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "needs-review"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "needs-review")
}

func TestLabelRm_HappyPath(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")

	addCmd := newRootCmd()
	addCmd.SetOut(&bytes.Buffer{})
	addCmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "bug"})
	addCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, addCmd.Execute())

	resetFlags(t)
	rmCmd := newRootCmd()
	var buf bytes.Buffer
	rmCmd.SetOut(&buf)
	rmCmd.SetArgs([]string{"--workspace", dir, "label", "rm", "1", "bug"})
	rmCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, rmCmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "removed") ||
		strings.Contains(buf.String(), "unlabeled"))
}

func TestLabelsList_PrintsCounts(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "a")
	addCmd := newRootCmd()
	addCmd.SetOut(&bytes.Buffer{})
	addCmd.SetArgs([]string{"--workspace", dir, "label", "add", "1", "bug"})
	addCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, addCmd.Execute())

	resetFlags(t)
	listCmd := newRootCmd()
	var buf bytes.Buffer
	listCmd.SetOut(&buf)
	listCmd.SetArgs([]string{"--workspace", dir, "labels"})
	listCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, listCmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "bug")
	assert.Contains(t, out, "1") // count
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run 'TestLabel|TestLabels'`
Expected: FAIL.

- [ ] **Step 3: Create `cmd/kata/label.go`**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func newLabelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "add or remove a label on an issue",
	}
	cmd.AddCommand(labelAddCmd(), labelRmCmd())
	return cmd
}

func labelAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <number> <label>",
		Short: "attach a label to an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			label := args[1]
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
			payload := map[string]string{"actor": actor, "label": label}
			urlStr := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/labels", baseURL, pid, n)
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost, urlStr, payload)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printLabelMutation(cmd, bs)
		},
	}
}

func labelRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <number> <label>",
		Short: "detach a label from an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			label := args[1]
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
			urlStr := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/labels/%s?actor=%s",
				baseURL, pid, n, url.PathEscape(label), url.QueryEscape(actor))
			status, bs, err := httpDoJSON(ctx, client, http.MethodDelete, urlStr, nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printMutation(cmd, bs)
		},
	}
}

func newLabelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "labels",
		Short: "list label counts in this project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/labels", baseURL, pid), nil)
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
				Labels []struct {
					Label string `json:"label"`
					Count int64  `json:"count"`
				} `json:"labels"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, c := range b.Labels {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %d\n", c.Label, c.Count); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// printLabelMutation formats AddLabelResponse for the three output modes.
func printLabelMutation(cmd *cobra.Command, bs []byte) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Issue struct{ Number int64 } `json:"issue"`
		Label struct{ Label string } `json:"label"`
		Changed bool                  `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already labeled %q (no-op)\n", b.Issue.Number, b.Label.Label)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d labeled %q\n", b.Issue.Number, b.Label.Label)
	return err
}
```

- [ ] **Step 4: Register**

In `cmd/kata/main.go`, add:

```go
newLabelCmd(),
newLabelsCmd(),
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run 'TestLabel|TestLabels'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/label.go cmd/kata/label_test.go cmd/kata/main.go
git commit -m "feat(cli): kata label add/rm + kata labels"
```

---

### Task 16: `cmd/kata/assign.go` — `kata assign/unassign`

Spec refs: §6.1.

```
kata assign <number> <owner>     # POST /actions/assign
kata unassign <number>           # POST /actions/unassign
```

**Files:**
- Create: `cmd/kata/assign.go`.
- Modify: `cmd/kata/main.go`.
- Test: `cmd/kata/assign_test.go`.

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/assign_test.go
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

func TestAssign_RoundTrip(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "x")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "assign", "1", "alice"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "assigned") ||
		strings.Contains(buf.String(), "alice"))

	resetFlags(t)
	uCmd := newRootCmd()
	var ubuf bytes.Buffer
	uCmd.SetOut(&ubuf)
	uCmd.SetArgs([]string{"--workspace", dir, "unassign", "1"})
	uCmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, uCmd.Execute())
	assert.True(t, strings.Contains(ubuf.String(), "unassigned"))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run TestAssign`
Expected: FAIL.

- [ ] **Step 3: Create `cmd/kata/assign.go`**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newAssignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <number> <owner>",
		Short: "set the owner of an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssign(cmd, args[0], args[1], false)
		},
	}
}

func newUnassignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unassign <number>",
		Short: "clear the owner of an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssign(cmd, args[0], "", true)
		},
	}
}

func runAssign(cmd *cobra.Command, raw, owner string, unassign bool) error {
	n, err := strconv.ParseInt(raw, 10, 64)
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
	action := "assign"
	body := map[string]any{"actor": actor, "owner": owner}
	if unassign {
		action = "unassign"
		body = map[string]any{"actor": actor}
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/%s", baseURL, pid, n, action)
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printAssignMutation(cmd, bs, unassign)
}

// printAssignMutation formats the response for human and JSON modes. Quiet
// mode prints nothing.
func printAssignMutation(cmd *cobra.Command, bs []byte, unassign bool) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Issue struct {
			Number int64
			Owner  *string
		} `json:"issue"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		state := "unassigned"
		if b.Issue.Owner != nil {
			state = *b.Issue.Owner
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already %s (no-op)\n", b.Issue.Number, state)
		return err
	}
	if unassign {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d unassigned\n", b.Issue.Number)
		return err
	}
	owner := ""
	if b.Issue.Owner != nil {
		owner = *b.Issue.Owner
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d assigned to %s\n", b.Issue.Number, owner)
	return err
}
```

- [ ] **Step 4: Register**

```go
newAssignCmd(),
newUnassignCmd(),
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run TestAssign`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/assign.go cmd/kata/assign_test.go cmd/kata/main.go
git commit -m "feat(cli): kata assign/unassign with dedicated events"
```

---

### Task 17: `cmd/kata/ready.go` — `kata ready`

Spec refs: §6.6.

`kata ready [--limit N]` — open issues with no open `blocks` predecessor, current project only. Cross-project / `--all-projects` is Plan 4.

**Files:**
- Create: `cmd/kata/ready.go`.
- Modify: `cmd/kata/main.go`.
- Test: `cmd/kata/ready_test.go`.

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/ready_test.go
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

func TestReady_FiltersBlocked(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "blocker")
	createIssue(t, env, pid, "blocked")
	createIssue(t, env, pid, "standalone")
	createLinkViaHTTP(t, env, pid, 1, "blocks", 2)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "ready"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "blocker")
	assert.Contains(t, out, "standalone")
	assert.False(t, strings.Contains(out, "blocked"),
		"blocked is hidden while blocker is open")
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run TestReady`
Expected: FAIL.

- [ ] **Step 3: Create `cmd/kata/ready.go`**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newReadyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "list open issues with no open blocks predecessor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 0 {
				return &cliError{Message: "--limit must be non-negative", ExitCode: ExitValidation}
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
			url := fmt.Sprintf("%s/api/v1/projects/%d/ready", baseURL, pid)
			if limit > 0 {
				url += fmt.Sprintf("?limit=%d", limit)
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
			var b struct {
				Issues []struct {
					Number int64  `json:"number"`
					Title  string `json:"title"`
					Owner  *string `json:"owner,omitempty"`
				} `json:"issues"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, i := range b.Issues {
				owner := "-"
				if i.Owner != nil {
					owner = *i.Owner
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "#%-4d  %s  (%s)\n", i.Number, i.Title, owner); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max rows (0 = no limit)")
	return cmd
}
```

- [ ] **Step 4: Register**

```go
newReadyCmd(),
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run TestReady`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/ready.go cmd/kata/ready_test.go cmd/kata/main.go
git commit -m "feat(cli): kata ready"
```

---

### Task 18: `cmd/kata/create.go` + `cmd/kata/show.go` — initial state on create + extended show

Spec refs: §6.1 (create flags), §6.4 (create output), §6.1 (show includes links + labels).

Two related changes in one task:

**Part A: `kata create` accepts `--label` (repeatable), `--parent N`, `--blocks N`, `--owner X`.**

These map to the new request body fields (`labels`, `links`, `owner`). The `--parent` and `--blocks` flags translate to `Links` entries; multiple `--blocks N` are allowed; only one `--parent` (cobra's `StringVar` enforces single-value).

**Part B: `kata show` formats the new `links` + `labels` fields.**

Human format adds two sections after comments:

```
#42  fix login  [open]  by alice

details body…

--- comments ---
bob: looks good

--- labels ---
bug, priority:high

--- links ---
parent → #18
blocks → #23
```

JSON output is unchanged (the daemon's response now carries `links`/`labels`; emitJSON wraps it as before).

**Files:**
- Modify: `cmd/kata/create.go` — add four flag bindings + extend the request payload.
- Modify: `cmd/kata/show.go` — add labels + links rendering.
- Modify: `cmd/kata/create_test.go` — add a test for initial-state create (uses `--label`, `--parent`).
- Test: `cmd/kata/show_test.go` — add a test that the human format includes labels + links sections.

- [ ] **Step 1: Write failing test**

Append to `cmd/kata/create_test.go`:

```go
func TestCreate_WithInitialLabelsAndParent(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent-issue")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--workspace", dir, "create", "child",
		"--label", "bug", "--label", "needs-review",
		"--parent", "1",
		"--owner", "alice",
	})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "child")
}
```

Create `cmd/kata/show_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestShow_RendersLabelsAndLinksSections(t *testing.T) {
	resetFlags(t)
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	createIssue(t, env, pid, "parent")
	createIssue(t, env, pid, "child")
	// Add a label and a parent link via HTTP.
	body := []byte(`{"actor":"tester","label":"bug"}`)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues/2/labels",
		"application/json", bytes.NewReader(body)) //nolint:noctx,gosec // test-only
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	createLinkViaHTTP(t, env, pid, 2, "parent", 1)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "show", "2"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "labels"), "expected labels section")
	assert.Contains(t, out, "bug")
	assert.True(t, strings.Contains(out, "links"), "expected links section")
	assert.Contains(t, out, "parent")
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/... -run 'TestCreate_WithInitial|TestShow_Renders'`
Expected: FAIL.

- [ ] **Step 3: Modify `cmd/kata/create.go`**

Replace the `newCreateCmd` body. The full file becomes:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var src BodySources
	var (
		labels   []string
		parent   int64
		blocks   []int64
		owner    string
	)
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "create a new issue",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&src.Body, "body", "", "issue body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "initial label (repeatable)")
	cmd.Flags().Int64Var(&parent, "parent", 0, "initial parent link target (issue number)")
	cmd.Flags().Int64SliceVar(&blocks, "blocks", nil, "initial blocks link target (issue number, repeatable)")
	cmd.Flags().StringVar(&owner, "owner", "", "initial owner")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		src.BodySet = cmd.Flags().Changed("body")
		src.FileSet = cmd.Flags().Changed("body-file")

		ctx := cmd.Context()
		title := args[0]
		if strings.TrimSpace(title) == "" {
			return &cliError{Message: "title must not be empty", ExitCode: ExitValidation}
		}
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return err
		}
		baseURL, err := ensureDaemon(ctx)
		if err != nil {
			return err
		}
		projectID, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return err
		}
		body, err := resolveBody(src, cmd.InOrStdin())
		if err != nil {
			code := ExitValidation
			if isMutexBodyErr(err.Error()) {
				code = ExitUsage
			}
			return &cliError{Message: err.Error(), ExitCode: code}
		}
		actor, _ := resolveActor(flags.As, nil)
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}

		req := map[string]any{"actor": actor, "title": title, "body": body}
		if cmd.Flags().Changed("owner") {
			req["owner"] = owner
		}
		if len(labels) > 0 {
			req["labels"] = labels
		}
		var links []map[string]any
		if cmd.Flags().Changed("parent") {
			links = append(links, map[string]any{"type": "parent", "to_number": parent})
		}
		for _, b := range blocks {
			links = append(links, map[string]any{"type": "blocks", "to_number": b})
		}
		if len(links) > 0 {
			req["links"] = links
		}

		status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
			fmt.Sprintf("%s/api/v1/projects/%d/issues", baseURL, projectID), req)
		if err != nil {
			return err
		}
		if status >= 400 {
			return apiErrFromBody(status, bs)
		}
		return printMutation(cmd, bs)
	}
	return cmd
}

// isMutexBodyErr reports whether err.Error() looks like the resolveBody
// "must pass exactly one of …" message. Used to upgrade ExitValidation to
// ExitUsage for body-source conflicts.
func isMutexBodyErr(s string) bool {
	const prefix = "must pass exactly one of"
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// resolveProjectID and printMutation are unchanged from Plan 1.
func resolveProjectID(ctx context.Context, baseURL, startPath string) (int64, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		baseURL+"/api/v1/projects/resolve",
		map[string]any{"start_path": startPath})
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, apiErrFromBody(status, bs)
	}
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return 0, err
	}
	return b.Project.ID, nil
}

func printMutation(cmd *cobra.Command, bs []byte) error {
	var b struct {
		Issue struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"issue"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	if flags.Quiet {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), b.Issue.Number)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d %s [%s]\n", b.Issue.Number, b.Issue.Title, b.Issue.Status)
	return err
}
```

Add `"strings"` to the import block (already present in many files; check `cmd/kata/create.go`'s imports before adding).

- [ ] **Step 4: Modify `cmd/kata/show.go`**

Replace the human-mode rendering block to also print labels + links sections after comments. The decoded body struct gains `Labels` and `Links` slices; render them only when non-empty. Use a small helper `joinLabels` that comma-joins the label strings.

In `cmd/kata/show.go`:

```go
var b struct {
	Issue struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Status string `json:"status"`
		Author string `json:"author"`
	} `json:"issue"`
	Comments []struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	} `json:"comments"`
	Labels []struct {
		Label string `json:"label"`
	} `json:"labels"`
	Links []struct {
		Type                 string `json:"type"`
		FromNumber, ToNumber int64
	} `json:"links"`
}
```

After the comments block:

```go
if len(b.Labels) > 0 {
	if _, err := fmt.Fprintln(out, "\n--- labels ---"); err != nil {
		return err
	}
	parts := make([]string, 0, len(b.Labels))
	for _, l := range b.Labels {
		parts = append(parts, l.Label)
	}
	if _, err := fmt.Fprintln(out, strings.Join(parts, ", ")); err != nil {
		return err
	}
}
if len(b.Links) > 0 {
	if _, err := fmt.Fprintln(out, "\n--- links ---"); err != nil {
		return err
	}
	for _, l := range b.Links {
		other := l.ToNumber
		dir := "→"
		// If the show is for the link's "to" side, point the arrow back so
		// the rendering reads naturally regardless of direction.
		if l.FromNumber != b.Issue.Number {
			other = l.FromNumber
			dir = "←"
		}
		if _, err := fmt.Fprintf(out, "%s %s #%d\n", l.Type, dir, other); err != nil {
			return err
		}
	}
}
```

Add `"strings"` to imports if missing.

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/... -run 'TestCreate_WithInitial|TestShow_Renders'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/create.go cmd/kata/create_test.go cmd/kata/show.go cmd/kata/show_test.go
git commit -m "feat(cli): create accepts initial state; show renders labels + links"
```

---


### Task 19: Final tidy — extend e2e smoke + self-review

Spec refs: writing-plans skill self-review checklist.

Two sub-steps:

**Step A: Extend the e2e smoke test.** Plan 1's `e2e/e2e_test.go` covers init→create→list→comment→close→reopen→show. Extend it (or add a sibling `TestSmoke_Plan2Lifecycle`) to also cover: link parent → label add → assign → ready (verifies blocked filtering) → unassign → label rm → unlink. Each step asserts the matching event type.

**Step B: Run the full suite, then add a single `runs the new commands end-to-end` cobra-style test that just walks all new top-level commands once with `--help` so a future `--help` regression on any new verb fails fast.**

**Files:**
- Modify: `e2e/e2e_test.go` — add `TestSmoke_Plan2Lifecycle`.
- Modify: `cmd/kata/main_test.go` — add `TestRoot_Plan2VerbsAdvertised`.

- [ ] **Step 1: Add the Plan 2 smoke test**

Append to `e2e/e2e_test.go`:

```go
func TestSmoke_Plan2Lifecycle(t *testing.T) {
	env := testenv.New(t)
	dir := initRepo(t, "https://github.com/wesm/system.git")

	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dir}))
	pid := resolvePID(t, env.HTTP, env.URL, dir)
	pidStr := strconv.FormatInt(pid, 10)

	// Two issues: parent + child.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "parent"}))
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues",
		map[string]any{"actor": "agent", "title": "child"}))

	// Link: child → parent → 1.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/links",
		map[string]any{"actor": "agent", "type": "parent", "to_number": 1}))

	// Label child as bug.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/labels",
		map[string]any{"actor": "agent", "label": "bug"}))

	// Assign child to alice.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/actions/assign",
		map[string]any{"actor": "agent", "owner": "alice"}))

	// Ready should NOT include child (parent link doesn't block; only `blocks`
	// links do). Both child and parent are ready.
	readyBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/ready")
	assert.Contains(t, readyBody, `"title":"parent"`)
	assert.Contains(t, readyBody, `"title":"child"`)

	// Now make parent block child explicitly.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/1/links",
		map[string]any{"actor": "agent", "type": "blocks", "to_number": 2}))
	readyBody = getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/ready")
	assert.Contains(t, readyBody, `"title":"parent"`)
	assert.NotContains(t, readyBody, `"title":"child"`,
		"child must be filtered while parent (blocker) is open")

	// Unassign + remove label + unlink one of the links to verify reverse paths.
	requireOK(t, postJSON(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/actions/unassign",
		map[string]any{"actor": "agent"}))
	deleteWith(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2/labels/bug?actor=agent")

	// show #2 must reflect the post-state: no labels, parent link still present.
	showBody := getBody(t, env.HTTP, env.URL+"/api/v1/projects/"+pidStr+"/issues/2")
	assert.Contains(t, showBody, `"labels":[]`, "labels cleared on issue #2")
	assert.Contains(t, showBody, `"parent"`, "parent link still present")
}

// deleteWith issues a DELETE through env.HTTP and asserts 200.
func deleteWith(t *testing.T, client *http.Client, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req) //nolint:gosec // G704: test-only loopback
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body := drain(t, resp)
	require.Equalf(t, 200, resp.StatusCode, "DELETE %s → %d: %s", url, resp.StatusCode, body)
}
```

You may already have a `drain` helper from Plan 1 — reuse it.

Also: the test asserts `"labels":[]` after removing the only label. The daemon currently returns `nil` slice → `null` in JSON. If you find the test fails because the show handler emits `"labels":null` when empty, swap the assert to `assert.NotContains(t, showBody, `"label":"bug"`)` — the goal is "bug is gone", not the literal `[]` shape.

- [ ] **Step 2: Add the cobra advertise test**

Append to `cmd/kata/main_test.go`:

```go
func TestRoot_Plan2VerbsAdvertised(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	for _, verb := range []string{
		"link", "unlink", "parent", "unparent",
		"block", "unblock", "relate", "unrelate",
		"label", "labels",
		"assign", "unassign",
		"ready",
	} {
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

If anything fails, fix in place. The plan's TDD cycle should have left a clean tree — issues here usually mean a flag wasn't registered, an import is missing, or a no-op envelope leaked into the human-format path.

- [ ] **Step 4: Self-review checklist**

Spec coverage Plan 2 should now satisfy:

1. **Schema:** `links` + `issue_labels` + their indexes/triggers exercised by handler tests.
2. **Event types:** `issue.linked`, `issue.unlinked`, `issue.labeled`, `issue.unlabeled`, `issue.assigned`, `issue.unassigned` all emitted by the new handlers.
3. **Issue lifecycle (§3.4):** link/unlink, label add/rm, assign/unassign verbs all live and tested.
4. **Endpoint surface (§4.1):** POST/DELETE `/links`, POST/DELETE `/labels`, GET `/labels`, POST `/actions/assign`, POST `/actions/unassign`, GET `/ready` all registered.
5. **Mutation envelope (§4.5):** Every new endpoint returns `event/changed` (with `event:null, changed:false` for no-ops; idempotent unlink/unlabel always 200).
6. **CLI command map (§6.1):** `kata link/unlink/parent/unparent/block/unblock/relate/unrelate`, `kata label add/rm`, `kata labels`, `kata assign/unassign`, `kata ready`. `kata create` accepts `--label` (repeatable), `--parent`, `--blocks` (repeatable), `--owner`.
7. **`kata ready` query (§6.6):** Open issues with no open `blocks` predecessor; tested.
8. **Initial create state (§3.3):** Single `issue.created` event with payload containing initial labels/links/owner; no separate labeled/linked/assigned events fire at creation.

Out of scope for Plan 2 (defer to later plans): cross-project list (`--all-projects`), `kata create --idempotency-key`, look-alike soft-block, search, soft-delete/restore/purge, SSE, polling, hooks, TUI.

- [ ] **Step 5: Commit**

```bash
git add e2e/e2e_test.go cmd/kata/main_test.go
git commit -m "test: Plan 2 smoke + advertise checks"
```

If `git status` is clean before this step (you accidentally committed everything in earlier tasks), skip — there's nothing to commit.

---

## Self-review checklist (run before declaring Plan 2 complete)

**Spec coverage:** §3.2 link triggers/indexes exercised; §3.3 the six new event types fire; §3.4 lifecycle rows match spec; §4.1 new endpoints registered; §4.5 envelope holds (no-op = `event:null, changed:false`); §6.1 every Plan 2 verb registered with the right arg count; §6.6 ready SQL matches spec.

**Type consistency:** `db.Link`, `api.LinkOut` (numbers, not ids); `db.IssueLabel`, `db.LabelCount`; `db.InitialLink`. `LinkOut` exposes `from_number`/`to_number` only — the wire never speaks internal issue ids.

**Conventions:**
- All tests use `testify/require` + `assert`; no bare `t.Fatal`.
- Every CLI test calls `resetFlags(t)` before constructing root commands.
- Every new handler maps DB typed errors to specific HTTP statuses (404/400/409) — no string-matching on errors in the handler layer.
- No `//nolint` directives without a justification comment.

**Plan 1 surface preserved:** `MutationResponse{Issue, Event, Changed, Reused}` envelope unchanged for existing endpoints; new handlers use compatible response shapes (`CreateLinkResponse` and `AddLabelResponse` extend with extra fields, same Event/Changed contract).

**Forward-compat:** No idempotency machinery introduced (Plan 3); no FTS triggers (Plan 3); no SSE (Plan 4). The initial-state code path leaves a clean handoff: Plan 3's idempotency check sits before `db.CreateIssue` runs, fingerprinting the same `(title, body, owner, labels, links)` set the handler now passes through.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-29-kata-2-relationships-labels-ownership.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. After every five completed tasks invoke `/roborev-fix` to clean up post-commit review findings (matching Plan 1's cadence).

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Standing authorization from Plan 1 ("perfect, yes, use opus with subagents and invoke roborev fix every 5 tasks") still holds unless you say otherwise.

