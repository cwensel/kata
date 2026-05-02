# Kata TUI Professional Workspace Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the professional `kata tui` workspace: hierarchy-aware issue queue, parent/children detail sections, complete contextual footers, and polished app chrome/forms.

**Architecture:** Add hierarchy metadata to daemon list/show responses first, then update the TUI client/model to use a capped all-status working set. Rendering changes come after the data model and footer registry are in place so golden snapshots churn once, not repeatedly.

**Tech Stack:** Go 1.26, Bubble Tea, Lipgloss, existing daemon HTTP API, SQLite.

**Spec:** `docs/superpowers/specs/2026-05-02-kata-tui-professional-workspace-design.md`

---

## File Structure

Modify:

- `internal/db/types.go` — add `ChildCounts` DB-facing helper type if useful.
- `internal/db/queries_links.go` — add relationship read helpers near existing link queries.
- `internal/db/queries_links_test.go` — DB tests for parent numbers, child counts, children list, chunking.
- `internal/api/types.go` — add hierarchy fields to `IssueOut` and `ShowIssueResponse`, keep existing create initial-link DTO.
- `internal/daemon/handlers_issues.go` — hydrate hierarchy metadata in list/show handlers.
- `internal/daemon/handlers_issues_test.go` — daemon wire tests for list and show hierarchy fields.
- `internal/tui/client_types.go` — add parent number/counts, parent/children detail fields, create initial links.
- `internal/tui/client.go` — decode show hierarchy, add list limit query support through `ListFilter` or a small query type.
- `internal/tui/cache.go` — change cache key from rendered filter to all-status working-set key.
- `internal/tui/list.go` — add expansion state, queue-row model, client-side status filtering, `space`, `N`, no-refetch status changes.
- `internal/tui/list_render.go` — render disclosure glyphs, indentation, child counts, context rows, truncation notice.
- `internal/tui/detail.go` — add parent/children state, explicit detail focus, child cursor, child jump.
- `internal/tui/detail_render.go` — render parent summary and Children section.
- `internal/tui/detail_tabs.go` — keep activity tabs unchanged except focus integration.
- `internal/tui/input.go` — add parent field/new-child form state.
- `internal/tui/inputs_render.go` — render Parent field and centered comment form state if any inline path remains.
- `internal/tui/keymap.go` — add expand/collapse and new-child bindings.
- `internal/tui/help.go` — rebuild help groups from context registry.
- `internal/tui/model.go` — route new-child, expand/collapse, parent-link SSE invalidation, all-status refetches.
- `internal/tui/messages.go` — add hierarchy/focus messages if needed.
- `internal/tui/snapshot_test.go` and `internal/tui/testdata/golden/*.txt` — update/add golden snapshots once rendering lands.

Create:

- `internal/tui/queue_rows.go` — visible queue row construction from flat issue working set.
- `internal/tui/queue_rows_test.go` — collapsed/expanded/context-row/filter/cursor behavior.
- `internal/tui/footer_hints.go` — context-aware footer registry.
- `internal/tui/footer_hints_test.go` — complete footer matrix tests.

## Key Design Constraints

- Queue is hierarchical-only. No alternate flat queue toggle.
- Queue fetches all statuses with a 2,001-row probe, trims to a hard v1 cap of 2,000 rendered rows, then filters status/search/owner/labels client-side.
- Cache key for the queue working set is scope + fetch limit, not rendered filter.
- Direct child counts only. No recursive progress counts.
- Detail Children section is rendered only from `show issue`'s `children` field, never from the Links tab.
- No steady-state N+1 detail fetches for child rows.
- `space` on a row with no children is a silent no-op.
- `N` with no visible row selected is a silent no-op, and the footer hides `N new child` in that state.
- `NO_COLOR` disclosure fallback is `+` collapsed and `-` expanded.
- Context rows render a leading `~` tree-cell marker in both color and no-color modes; subdued color is additive only.

## Implementation Notes From Current Code

- `internal/tui/cache.go` currently stores `cacheKey{scope, filter}`. This must change because status/search/owner/labels become render filters over the same all-status working set.
- `internal/tui/list.go::matchesFilter` currently does not check `Status`; add status matching when filtering moves client-side.
- `listModel.applyFilterKey` currently refetches for `s` and `c`. After this change `s` should only update `lm.filter.Status` and recompute visible rows; `c` should clear filters without refetching unless the working-set cache is empty/stale.
- `internal/db/queries_labels_by_issues.go` has the chunking pattern to reuse for new `IN (...)` relationship helpers.
- Daemon create already supports initial links through `api.CreateInitialLinkBody`; only the TUI DTO and form submit path need to grow create-with-parent-link.

---

## Task 1: DB Hierarchy Helpers

**Files:**

- Modify: `internal/db/types.go`
- Modify: `internal/db/queries_links.go`
- Modify: `internal/db/queries_links_test.go`

- [ ] **Step 1.1: Add helper types**

In `internal/db/types.go`, add:

```go
// ChildCounts is the direct-child aggregate for one parent issue.
type ChildCounts struct {
	Open  int `json:"open"`
	Total int `json:"total"`
}
```

- [ ] **Step 1.2: Write tests for empty inputs**

In `internal/db/queries_links_test.go`, add tests:

```go
func TestParentNumbersByIssues_EmptyInput(t *testing.T)
func TestChildCountsByParents_EmptyInput(t *testing.T)
```

Expected: both return empty maps and nil errors.

Run:

```bash
go test ./internal/db -run 'Test(ParentNumbersByIssues|ChildCountsByParents)_EmptyInput' -count=1
```

Expected: FAIL because helpers do not exist.

- [ ] **Step 1.3: Implement empty-input helper shells**

In `internal/db/queries_links.go`, add:

```go
const relationshipChunkSize = labelsByIssuesChunkSize

func (d *DB) ParentNumbersByIssues(
	ctx context.Context, projectID int64, issueIDs []int64,
) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(issueIDs) == 0 {
		return out, nil
	}
	// filled in next steps
	return out, nil
}

func (d *DB) ChildCountsByParents(
	ctx context.Context, projectID int64, parentIssueIDs []int64,
) (map[int64]ChildCounts, error) {
	out := map[int64]ChildCounts{}
	if len(parentIssueIDs) == 0 {
		return out, nil
	}
	// filled in next steps
	return out, nil
}
```

Run the test from Step 1.2. Expected: PASS.

- [ ] **Step 1.4: Write tests for parent-number hydration**

Add `TestParentNumbersByIssues_ReturnsImmediateParents`:

- create one project
- create parent issue P and children C1/C2
- link C1 -> P and C2 -> P with type `parent`
- create unrelated issue U
- call `ParentNumbersByIssues(ctx, projectID, []int64{C1.ID, C2.ID, U.ID})`
- assert map contains C1.ID -> P.Number, C2.ID -> P.Number, and no U key

Run:

```bash
go test ./internal/db -run TestParentNumbersByIssues_ReturnsImmediateParents -count=1
```

Expected: FAIL.

- [ ] **Step 1.5: Implement `ParentNumbersByIssues` with chunking**

Use the `LabelsByIssues` chunk pattern. Query per chunk:

```sql
SELECT l.from_issue_id, parent.number
FROM links l
JOIN issues child ON child.id = l.from_issue_id
JOIN issues parent ON parent.id = l.to_issue_id
WHERE l.project_id = ?
  AND child.project_id = ?
  AND parent.project_id = ?
  AND l.type = 'parent'
  AND l.from_issue_id IN (...)
ORDER BY l.from_issue_id ASC
```

Use args `projectID, projectID, projectID, chunk...`.

Run Step 1.4. Expected: PASS.

- [ ] **Step 1.6: Write project-scoping test**

Add `TestParentNumbersByIssues_ConstrainsProject`:

- create two projects
- create same-shape parent/child links in both
- call helper for project A with IDs from both projects
- assert only project A child appears

Run:

```bash
go test ./internal/db -run TestParentNumbersByIssues_ConstrainsProject -count=1
```

Expected: PASS after Step 1.5.

- [ ] **Step 1.7: Write child-count tests**

Add `TestChildCountsByParents_ReturnsOpenAndTotalDirectChildren`:

- create parent P
- create children C1 open, C2 closed, C3 open
- link all three as `parent`
- call `ChildCountsByParents(ctx, projectID, []int64{P.ID})`
- assert `Open == 2`, `Total == 3`

Run:

```bash
go test ./internal/db -run TestChildCountsByParents_ReturnsOpenAndTotalDirectChildren -count=1
```

Expected: FAIL.

- [ ] **Step 1.8: Implement `ChildCountsByParents` with chunking**

Query per chunk:

```sql
SELECT l.to_issue_id,
       SUM(CASE WHEN child.status = 'open' THEN 1 ELSE 0 END) AS open_count,
       COUNT(*) AS total_count
FROM links l
JOIN issues child ON child.id = l.from_issue_id
JOIN issues parent ON parent.id = l.to_issue_id
WHERE l.project_id = ?
  AND child.project_id = ?
  AND parent.project_id = ?
  AND l.type = 'parent'
  AND child.deleted_at IS NULL
  AND l.to_issue_id IN (...)
GROUP BY l.to_issue_id
ORDER BY l.to_issue_id ASC
```

Run Step 1.7. Expected: PASS.

- [ ] **Step 1.9: Write children listing test**

Add `TestChildrenOfIssue_ReturnsDirectChildrenOnly`:

- create parent P, direct children C1/C2, grandchild G under C1
- call `ChildrenOfIssue(ctx, projectID, P.ID)`
- assert result contains C1/C2 only, not G
- assert order is `updated_at DESC, id DESC` matching list order

Run:

```bash
go test ./internal/db -run TestChildrenOfIssue_ReturnsDirectChildrenOnly -count=1
```

Expected: FAIL.

- [ ] **Step 1.10: Implement `ChildrenOfIssue`**

In `internal/db/queries_links.go`, add:

```go
func (d *DB) ChildrenOfIssue(
	ctx context.Context, projectID, parentIssueID int64,
) ([]Issue, error)
```

Use:

```sql
SELECT <issue columns for child>
FROM issues child
JOIN links l ON l.from_issue_id = child.id
WHERE l.project_id = ?
  AND child.project_id = ?
  AND l.type = 'parent'
  AND l.to_issue_id = ?
  AND child.deleted_at IS NULL
ORDER BY child.updated_at DESC, child.id DESC
```

Use the existing `scanIssue` helper.

Run:

```bash
go test ./internal/db -run 'Test(ParentNumbersByIssues|ChildCountsByParents|ChildrenOfIssue)' -count=1
```

Expected: PASS.

- [ ] **Step 1.11: Add chunking regression**

Add a large-input test for `ChildCountsByParents` with more than `relationshipChunkSize` parent IDs. It does not need thousands of child links; create enough parent issue IDs to cross the chunk boundary and one linked child on the last parent.

Run:

```bash
go test ./internal/db -run TestChildCountsByParents_ChunksLargeInputs -count=1
```

Expected: PASS.

- [ ] **Step 1.12: Run DB tests**

```bash
go test ./internal/db -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/db/types.go internal/db/queries_links.go internal/db/queries_links_test.go
git commit -m "feat(db): add issue hierarchy read helpers"
```

---

## Task 2: Daemon Hierarchy Wire Fields

**Files:**

- Modify: `internal/api/types.go`
- Modify: `internal/daemon/handlers_issues.go`
- Modify: `internal/daemon/handlers_issues_test.go`

- [ ] **Step 2.1: Add API DTO fields**

In `internal/api/types.go`, update:

```go
type IssueOut struct {
	db.Issue
	Labels       []string        `json:"labels,omitempty"`
	ParentNumber *int64          `json:"parent_number,omitempty"`
	ChildCounts  *db.ChildCounts `json:"child_counts,omitempty"`
}

type IssueRef struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
```

Add to `ShowIssueResponse.Body`:

```go
Parent   *IssueRef  `json:"parent,omitempty"`
Children []IssueOut `json:"children,omitempty"`
```

- [ ] **Step 2.2: Write daemon list hierarchy test**

In `internal/daemon/handlers_issues_test.go`, add `TestListIssues_IncludesHierarchyMetadata`:

- create parent P and child C
- link C -> P
- GET `/api/v1/projects/{id}/issues?status=`
- decode list response
- assert P row has `child_counts.total == 1`, `child_counts.open == 1`
- assert C row has `parent_number == P.Number`

Run:

```bash
go test ./internal/daemon -run TestListIssues_IncludesHierarchyMetadata -count=1
```

Expected: FAIL.

- [ ] **Step 2.3: Hydrate list hierarchy fields**

In list handler:

1. Keep current `ListIssues` call and labels hydration.
2. Collect issue IDs.
3. Call `ParentNumbersByIssues(ctx, projectID, ids)`.
4. Call `ChildCountsByParents(ctx, projectID, ids)`.
5. Populate `IssueOut{Issue: iss, Labels: ..., ParentNumber: ..., ChildCounts: ...}`.

Only set `ChildCounts` when `Total > 0`.

Run Step 2.2. Expected: PASS.

- [ ] **Step 2.4: Write show hierarchy test**

Add `TestShowIssue_IncludesParentAndChildren`:

- create parent P, child C, grandchild G
- link C -> P and G -> C
- GET show for C
- assert `parent.number == P.Number`
- assert `children` contains G only
- assert child row includes labels and child counts if applicable

Run:

```bash
go test ./internal/daemon -run TestShowIssue_IncludesParentAndChildren -count=1
```

Expected: FAIL.

- [ ] **Step 2.5: Add show helper functions**

In `internal/daemon/handlers_issues.go`, add small helpers:

```go
func issueRefFromDB(iss db.Issue) api.IssueRef
func loadParentRef(ctx context.Context, store *db.DB, issue db.Issue) (*api.IssueRef, error)
func hydrateIssueOuts(ctx context.Context, store *db.DB, projectID int64, issues []db.Issue) ([]api.IssueOut, error)
```

`hydrateIssueOuts` should reuse labels, parent-number, and child-count helpers for children rows.

- [ ] **Step 2.6: Hydrate show hierarchy fields**

In show handler after labels:

1. `parent, err := loadParentRef(...)`
2. `children, err := cfg.DB.ChildrenOfIssue(ctx, in.ProjectID, issue.ID)`
3. `childOuts, err := hydrateIssueOuts(ctx, cfg.DB, in.ProjectID, children)`
4. Assign `out.Body.Parent = parent`, `out.Body.Children = childOuts`.

Run Step 2.4. Expected: PASS.

- [ ] **Step 2.7: Run daemon and DB tests**

```bash
go test ./internal/db ./internal/daemon -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/api/types.go internal/daemon/handlers_issues.go internal/daemon/handlers_issues_test.go
git commit -m "feat(daemon): expose issue hierarchy metadata"
```

---

## Task 3: TUI Client Types And All-Status Working Set

**Files:**

- Modify: `internal/tui/client_types.go`
- Modify: `internal/tui/client.go`
- Modify: `internal/tui/client_test.go`
- Modify: `internal/tui/cache.go`
- Modify: `internal/tui/cache_test.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/list_filter_test.go`
- Modify: `internal/tui/model.go`

- [ ] **Step 3.1: Add TUI wire fields**

In `internal/tui/client_types.go`, add:

```go
type ChildCounts struct {
	Open  int `json:"open"`
	Total int `json:"total"`
}

type IssueRef struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
```

Extend `Issue`:

```go
ParentNumber *int64       `json:"parent_number,omitempty"`
ChildCounts  *ChildCounts `json:"child_counts,omitempty"`
```

Extend `showIssueBody`:

```go
Parent   *IssueRef `json:"parent,omitempty"`
Children []Issue   `json:"children,omitempty"`
```

Add:

```go
type CreateInitialLinkBody struct {
	Type     string `json:"type"`
	ToNumber int64  `json:"to_number"`
}
```

Extend `CreateIssueBody` with:

```go
Links []CreateInitialLinkBody `json:"links,omitempty"`
```

- [ ] **Step 3.2: Add list limit query support test**

In `internal/tui/client_test.go`, add a test that calls `Client.ListIssues` with a filter/query carrying `Limit: 2001` and asserts the request URL includes `limit=2001` and does not include `status=` when status is empty.

Run:

```bash
go test ./internal/tui -run TestClient_ListIssues_SendsLimit -count=1
```

Expected: FAIL.

- [ ] **Step 3.3: Add `Limit` to `ListFilter.values`**

In `internal/tui/client_types.go`:

```go
type ListFilter struct {
	Status, Owner, Author, Search string
	Labels                        []string
	Limit                         int
}
```

In `values()`, add:

```go
if f.Limit > 0 {
	v.Set("limit", strconv.Itoa(f.Limit))
}
```

Import `strconv`.

Run Step 3.2. Expected: PASS.

- [ ] **Step 3.4: Decode show parent/children**

Add a new TUI client method:

```go
type IssueDetail struct {
	Issue    *Issue
	Parent   *IssueRef
	Children []Issue
}

func (c *Client) GetIssueDetail(ctx context.Context, projectID, number int64) (*IssueDetail, error)
```

Make `showIssue` decode parent/children and keep labels sorted on both the top issue and child rows. Move `detailAPI`, `fetchIssue`, fake detail APIs, and tests to `GetIssueDetail`; then delete the exported `Client.GetIssue` method. `Client.ListComments` and `Client.ListLinks` may keep using the private `showIssue` helper internally, but they are not public wrapper call sites.

Add client test:

```bash
go test ./internal/tui -run TestClient_ShowIssue_DecodesHierarchy -count=1
```

Expected: PASS after implementation.

- [ ] **Step 3.5: Change cache key to working-set key**

In `internal/tui/cache.go`, remove `filter ListFilter` from `cacheKey` and replace with:

```go
limit int
```

Update `cacheKeysEqual` in `model.go` accordingly.

Update `cache_test.go`: replace filter-change test with `TestCache_RenderFilterDoesNotChangeSlotKey`.

Run:

```bash
go test ./internal/tui -run 'TestCache|TestCurrentCacheKey' -count=1
```

Expected: adjust until PASS.

- [ ] **Step 3.6: Add queue working-set constants**

In `internal/tui/list.go`:

```go
const queueWorkingSetLimit = 2000
const queueFetchLimit = queueWorkingSetLimit + 1

func queueFetchFilter() ListFilter {
	return ListFilter{Limit: queueFetchLimit}
}
```

In `Model.currentCacheKey`, return scope + `limit: queueFetchLimit`.

- [ ] **Step 3.7: Make initial/refetch use all-status capped fetch**

Update:

- `Model.fetchInitial`
- `listModel.refetchCmd`

Both should dispatch with `queueFetchFilter()` instead of `lm.filter`.

`dispatchKey` must use scope + limit only. The rendered filter stays on `lm.filter`.

- [ ] **Step 3.8: Move status to client-side filtering**

Update `matchesFilter`:

```go
if f.Status != "" && iss.Status != f.Status {
	return false
}
```

Update comments that say status is server-side.

- [ ] **Step 3.9: Stop refetching on status/clear filter changes**

Update `applyFilterKey`:

- `s`: update `lm.filter.Status`, reset cursor/selection/status, return nil cmd.
- `c`: clear filters, reset cursor/selection/status, return nil cmd unless future code explicitly detects no working set.

Update `commitFilterForm`: apply filter and close form without refetch.

Update existing tests:

- `TestList_StatusCycle` should assert zero API calls and status filters visible rows client-side.
- `TestList_ClearFilters_ZeroesEveryField` should assert nil command.

- [ ] **Step 3.10: Add truncation marker**

Add `truncated bool` to `listModel`. After a successful queue fetch:

1. Set `lm.truncated = len(issues) > queueWorkingSetLimit`.
2. If truncated, trim `issues = issues[:queueWorkingSetLimit]` before caching/applying them.
3. If exactly 2,000 rows are returned, `lm.truncated` is false because the 2,001-row sentinel was absent.

Rendering happens in Task 7; in this task add the model state and test:

```bash
go test ./internal/tui -run TestList_ApplyFetched_SetsTruncatedAboveWorkingSetLimitAndTrims -count=1
```

Expected: PASS.

- [ ] **Step 3.11: Run TUI model/client tests**

```bash
go test ./internal/tui -run 'Test(Client|Cache|List_|ListStatus|List_Clear|Filter)' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/client_types.go internal/tui/client.go internal/tui/client_test.go internal/tui/cache.go internal/tui/cache_test.go internal/tui/list.go internal/tui/list_filter_test.go internal/tui/model.go
git commit -m "feat(tui): use capped all-status queue working set"
```

---

## Task 4: Queue Row Model And Expansion State

**Files:**

- Create: `internal/tui/queue_rows.go`
- Create: `internal/tui/queue_rows_test.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/keymap.go`
- Modify: `internal/tui/model.go`

- [ ] **Step 4.1: Add queue row types**

Create `internal/tui/queue_rows.go`:

```go
type issueKey struct {
	projectID int64
	number    int64
}

type queueRow struct {
	issue       Issue
	key         issueKey
	depth       int
	hasChildren bool
	expanded    bool
	context     bool
	lastChild   bool
}

type expansionSet map[issueKey]bool
```

- [ ] **Step 4.2: Write collapsed top-level test**

In `queue_rows_test.go`, add `TestBuildQueueRows_CollapsedShowsTopLevelOnly`:

- P has child C
- U has no parent
- no expanded keys
- expect visible rows P and U only

Run:

```bash
go test ./internal/tui -run TestBuildQueueRows_CollapsedShowsTopLevelOnly -count=1
```

Expected: FAIL.

- [ ] **Step 4.3: Implement `buildQueueRows` collapsed behavior**

Signature:

```go
func buildQueueRows(issues []Issue, filter ListFilter, expanded expansionSet) []queueRow
```

Build maps:

- `byKey`
- `childrenByParent`
- stable issue order from `issues`

Top-level rows are `ParentNumber == nil`.

Run Step 4.2. Expected: PASS.

- [ ] **Step 4.4: Write expanded direct-child test**

Add `TestBuildQueueRows_ExpandedShowsDirectChildren`:

- expanded parent P
- expect P then direct children C1/C2, with depth 1
- grandchild G appears only when C1 is also expanded

Run test. Expected: FAIL.

- [ ] **Step 4.5: Implement expansion recursion one user level at a time**

`buildQueueRows` may recurse through expanded children, but only when each ancestor key is explicitly expanded. Add cycle guard:

```go
seenPath map[issueKey]bool
```

If a cycle appears, stop descending.

This is a render-side safety net for a malformed or incomplete working set. The DB one-parent invariant prevents multiple parents, but the renderer should still avoid infinite recursion if bad hierarchy data reaches the TUI.

Run Step 4.4. Expected: PASS.

- [ ] **Step 4.6: Write client-side filter/context tests**

Add:

```go
func TestBuildQueueRows_FilteredChildAutoShowsAncestorContext(t *testing.T)
func TestBuildQueueRows_StatusFilterIsClientSide(t *testing.T)
func TestBuildQueueRows_LabelsFilterAnyOf(t *testing.T)
```

The first test should assert:

- search matches child C
- parent P does not match
- output includes P as `context=true`, then C

Run. Expected: FAIL.

- [ ] **Step 4.7: Implement context-row filtering**

Approach:

1. Compute `matches := matchesFilter(issue, filter)`.
2. Include matched rows.
3. For each matched row, walk `ParentNumber` through `byKey` and include ancestors as context.
4. Render ancestors before descendants in normal queue order.
5. Auto-expand available ancestor chain for matched descendants.
6. Mark non-matching ancestors `context=true`.

Run Step 4.6. Expected: PASS.

- [ ] **Step 4.8: Add expansion state to list model**

In `listModel`:

```go
expanded expansionSet
```

Initialize nil lazily or in `newListModel`.

Add helper:

```go
func (lm listModel) visibleRows() []queueRow
```

Update `targetRow`, cursor movement, open detail, close/reopen to use visible queue rows.

- [ ] **Step 4.9: Add keybindings**

In `keymap.go`, add:

```go
ExpandCollapse key
NewChild       key
```

Bindings:

- `space`: expand/collapse
- `N`: new child

Update tests for help coverage after footer registry lands.

- [ ] **Step 4.10: Implement silent no-op expand on leaf**

Add `listModel.toggleExpanded()` and route `space`.

Tests:

```bash
go test ./internal/tui -run 'TestList_ExpandCollapse|TestBuildQueueRows' -count=1
```

Expected: PASS. Include a test that `space` on no-child row leaves `expanded` unchanged and returns nil cmd.

- [ ] **Step 4.11: Preserve cursor across refetch/parent insertion/filter change**

Add tests:

```go
func TestList_SelectionPreservedAcrossRefetchWithParentInsertion(t *testing.T)
func TestList_SelectionClampsWhenFilterHidesSelectedChild(t *testing.T)
```

Use `selectedNumber` plus project ID if needed. In all-projects mode issue numbers collide; queue identity should be `(project_id, number)`.

Run tests. Expected: PASS after updating sync-selection helpers to use queue rows.

- [ ] **Step 4.12: Run queue tests**

```bash
go test ./internal/tui -run 'TestBuildQueueRows|TestList_.*(Expand|Selection|Cursor|Filter)' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/queue_rows.go internal/tui/queue_rows_test.go internal/tui/list.go internal/tui/keymap.go internal/tui/model.go
git commit -m "feat(tui): add expandable issue queue model"
```

---

## Task 5: Detail Hierarchy Model

**Files:**

- Modify: `internal/tui/client_types.go`
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/messages.go`
- Modify: `internal/tui/detail_test.go`
- Modify: `internal/tui/edge_test.go`

- [ ] **Step 5.1: Add detail hierarchy fields**

In `detailModel`:

```go
parent      *IssueRef
children    []Issue
detailFocus detailFocus
childCursor int
```

Add:

```go
type detailFocus int

const (
	focusChildren detailFocus = iota
	focusActivity
)
```

Keep `activeTab` for Comments/Events/Links. `tab` cycles Children + activity tabs; when focus is activity, `activeTab` says which activity tab.

- [ ] **Step 5.2: Add detail fetched payload**

Extend `detailFetchedMsg` in `messages.go`:

```go
parent   *IssueRef
children []Issue
```

Update `fetchIssue` in `model.go` to call the `Client.GetIssueDetail` method added in Task 3:

```go
type IssueDetail struct {
	Issue    *Issue
	Parent   *IssueRef
	Children []Issue
}
```

- [ ] **Step 5.3: Write apply-fetched hierarchy test**

In `detail_test.go`, add:

```go
func TestDetailApplyFetched_PopulatesParentAndChildren(t *testing.T)
```

Run:

```bash
go test ./internal/tui -run TestDetailApplyFetched_PopulatesParentAndChildren -count=1
```

Expected: FAIL.

- [ ] **Step 5.4: Implement hierarchy apply-fetched**

Update `detailModel.applyFetched` for `detailFetchedMsg` to copy `parent` and `children`.

Run Step 5.3. Expected: PASS.

- [ ] **Step 5.5: Write focus-cycle tests**

Add:

```go
func TestDetailFocus_TabCyclesChildrenCommentsEventsLinks(t *testing.T)
func TestDetailFocus_SkipsChildrenWhenEmpty(t *testing.T)
func TestDetailFocus_ShiftTabCyclesBackward(t *testing.T)
```

Run. Expected: FAIL.

- [ ] **Step 5.6: Implement detail focus cycle**

Update `handleNavKey`:

- `tab`: children -> comments -> events -> links -> children
- `shift+tab`: reverse
- if no children, cycle comments/events/links only

When entering children focus, keep `childCursor` clamped.

Run Step 5.5. Expected: PASS.

- [ ] **Step 5.7: Write child cursor/jump tests**

Add:

```go
func TestDetailChildren_JKMovesChildCursor(t *testing.T)
func TestDetailChildren_EnterJumpsToChild(t *testing.T)
```

Run. Expected: FAIL.

- [ ] **Step 5.8: Implement child cursor and jump**

When `detailFocus == focusChildren`:

- up/down move `childCursor`
- enter emits `jumpDetailMsg{number: children[childCursor].Number}`

Keep existing comments/events/links behavior unchanged when focus is activity.

Run Step 5.7. Expected: PASS.

- [ ] **Step 5.9: Update nav-stack restoration**

Ensure `navStack` preserves parent/children/focus/childCursor. Extend existing edge tests for jump back.

Run:

```bash
go test ./internal/tui -run 'TestEdge_DetailJumpBack|TestDetailChildren' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/client_types.go internal/tui/detail.go internal/tui/model.go internal/tui/messages.go internal/tui/detail_test.go internal/tui/edge_test.go
git commit -m "feat(tui): model issue parent and children in detail"
```

---

## Task 6: Context-Aware Footer And Help Registry

**Files:**

- Create: `internal/tui/footer_hints.go`
- Create: `internal/tui/footer_hints_test.go`
- Modify: `internal/tui/list_render.go`
- Modify: `internal/tui/detail_render.go`
- Modify: `internal/tui/split_render.go`
- Modify: `internal/tui/help.go`
- Modify: `internal/tui/help_test.go`

- [ ] **Step 6.1: Add footer context types**

Create `footer_hints.go`:

```go
type footerContext struct {
	view        viewID
	layout      layoutMode
	pane        focusPane
	input       inputKind
	modal       modalKind
	detailFocus detailFocus
	activeTab   detailTab
	hasRows     bool
	hasChildren bool
}
```

Add:

```go
func footerHints(ctx footerContext) []helpRow
```

Populate existing list/detail/default hints first, plus named context slots for children and forms.

- [ ] **Step 6.2: Write footer matrix tests**

In `footer_hints_test.go`, add tests:

- list with rows includes `N new child`
- list without rows omits `N new child`
- list leaf may include `space expand` only if `hasChildren`; otherwise omit
- detail comments includes comment/edit/label/owner
- detail children includes open child/new child
- search bar includes commit/cancel/clear
- filter form includes apply/cancel/reset
- quit modal includes confirm/cancel

Run:

```bash
go test ./internal/tui -run TestFooterHints -count=1
```

Expected: FAIL.

- [ ] **Step 6.3: Implement footer matrix**

Fill the registry. Keep descriptions short enough for 80-column fallback.

Run Step 6.2. Expected: PASS.

- [ ] **Step 6.4: Wire renderers through footer registry**

Replace hand-built `listFooterItemsFor`, `detailFooterItemsFor`, and split footer branching with calls to `footerHints`.

At this stage, pass conservative `hasChildren`/`hasRows` from existing state; rendering task will refine exact context.

- [ ] **Step 6.5: Update full help groups**

In `help.go`, group by:

- Global
- Queue
- Detail
- Children
- Forms
- Filters

Add coverage tests that every keymap binding appears in either full help or an explicitly documented context-only footer.

Run:

```bash
go test ./internal/tui -run 'TestHelp|TestFooterHints' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/footer_hints.go internal/tui/footer_hints_test.go internal/tui/list_render.go internal/tui/detail_render.go internal/tui/split_render.go internal/tui/help.go internal/tui/help_test.go
git commit -m "feat(tui): centralize contextual footer hints"
```

---

## Task 7: Queue Rendering And Snapshots

**Files:**

- Modify: `internal/tui/list_render.go`
- Modify: `internal/tui/split_render.go`
- Modify: `internal/tui/theme.go`
- Modify: `internal/tui/list_render_test.go`
- Modify: `internal/tui/snapshot_test.go`
- Add/update: `internal/tui/testdata/golden/list-tree-collapsed.txt`
- Add/update: `internal/tui/testdata/golden/list-tree-expanded.txt`
- Add/update: `internal/tui/testdata/golden/list-tree-auto-expanded-match.txt`
- Add/update: `internal/tui/testdata/golden/list-tree-context-row.txt`
- Add/update: `internal/tui/testdata/golden/list-tree-no-color.txt`
- Update existing list/split goldens.

- [ ] **Step 7.1: Add disclosure glyph helper**

In `list_render.go` or `theme.go`:

```go
func disclosureGlyph(hasChildren, expanded bool) string
```

Rules:

- no children: `" "`
- normal collapsed: `"▸"`
- normal expanded: `"▾"`
- `colorNone`: `"+"` collapsed, `"-"` expanded

Write unit tests:

```bash
go test ./internal/tui -run TestDisclosureGlyph -count=1
```

Expected: PASS.

- [ ] **Step 7.2: Render queue rows**

Update list body rendering to use `lm.visibleRows()` instead of `filteredIssues`.

Render:

- disclosure/tree glyph column
- a fixed context-marker column before disclosure: `~` for context rows, blank otherwise
- indentation for depth
- context-row subdued style, with the `~` marker still present under `NO_COLOR`
- child-count column
- no owner/labels in split narrow list if space is tight

Keep title truncation width-safe using existing helpers.

- [ ] **Step 7.3: Render truncation notice**

In list info line priority, add notice after mutation/error/SSE/toast but before scroll indicator:

```text
showing first 2000 issues; refine filters
```

Only show when `lm.truncated`.

- [ ] **Step 7.4: Add collapsed/expanded snapshots**

Add snapshot fixtures with:

- parent with children collapsed
- same parent expanded
- child with child count
- leaf row

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_List_Tree(Collapsed|Expanded)' -update-goldens -count=1
```

Inspect goldens for no overlap.

- [ ] **Step 7.5: Add auto-expanded/context snapshots**

Add fixtures:

- filter matches child only; parent context visible and subdued
- context row marked distinctly in `NO_COLOR` with the leading `~` context marker

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_List_Tree(AutoExpandedMatch|ContextRow)' -update-goldens -count=1
```

- [ ] **Step 7.6: Add no-color snapshot**

Set `KATA_COLOR_MODE=none` or `NO_COLOR=1` in test and assert `+`/`-` disclosure fallback appears. Include at least one context row in the fixture and assert the `~` marker is still present.

Run:

```bash
go test ./internal/tui -run TestSnapshot_List_TreeNoColor -update-goldens -count=1
```

- [ ] **Step 7.7: Update split list snapshots**

Update split list focus/detail focus snapshots to include:

- pane gutter
- `queue focus` title
- tree disclosure column
- context-aware footer hints

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_Split' -update-goldens -count=1
```

- [ ] **Step 7.8: Run list/split tests**

```bash
go test ./internal/tui -run 'TestSnapshot_List|TestSnapshot_Split|TestList|TestBuildQueueRows|TestDisclosureGlyph' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/list_render.go internal/tui/split_render.go internal/tui/theme.go internal/tui/list_render_test.go internal/tui/snapshot_test.go internal/tui/testdata/golden/
git commit -m "feat(tui): render expandable issue queue"
```

---

## Task 8: Detail Hierarchy Rendering And Snapshots

**Files:**

- Modify: `internal/tui/detail_render.go`
- Modify: `internal/tui/split_render.go`
- Modify: `internal/tui/snapshot_test.go`
- Add/update: `internal/tui/testdata/golden/detail-with-children.txt`
- Add/update: `internal/tui/testdata/golden/detail-children-focus.txt`
- Update existing detail goldens.

- [ ] **Step 8.1: Render parent summary**

Update detail header row:

```text
Parent: #12 workspace polish                     Children: 2 open / 5 total
```

Rules:

- no parent: `Parent: -`
- no children: `Children: 0 open / 0 total`
- sanitize parent title
- truncate parent title to fit before children summary

Add unit tests for width-safe formatting.

- [ ] **Step 8.2: Render Children section**

Between body and activity:

```text
-- children 2 open / 5 total --------------------------------------
> #43 open    detail hint bars incomplete             alice   1h ago
  #44 closed  new issue form labels                   wesm    2h ago
```

Use existing table/row helpers where possible. The section gets a row budget; if vertical space is tight, show header summary and clip rows before activity.

- [ ] **Step 8.3: Reflect detail focus**

When `detailFocus == focusChildren`, highlight child cursor and footer hints for children. When focus is activity, keep current tab highlighting.

- [ ] **Step 8.4: Update split detail render**

`ViewSplit` should include parent summary and Children section, using a smaller row budget. If too narrow/tight, keep parent/children summary in header and clip child rows.

- [ ] **Step 8.5: Add detail hierarchy snapshots**

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_Detail_(WithChildren|ChildrenFocus)' -update-goldens -count=1
```

Inspect:

- no footer drift
- activity section still visible
- child cursor appears
- parent title truncates safely

- [ ] **Step 8.6: Update existing detail snapshots**

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_Detail|TestSnapshot_Split' -update-goldens -count=1
```

Inspect diffs carefully; expected churn is parent/children rows, footer hints, and adjusted body/activity budgets only.

- [ ] **Step 8.7: Run detail tests**

```bash
go test ./internal/tui -run 'TestDetail|TestSnapshot_Detail|TestSnapshot_Split|TestEdge_Detail' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/detail_render.go internal/tui/split_render.go internal/tui/snapshot_test.go internal/tui/testdata/golden/
git commit -m "feat(tui): render issue parent and children in detail"
```

---

## Task 9: New Child Form And Create Initial Link

**Files:**

- Modify: `internal/tui/input.go`
- Modify: `internal/tui/inputs_render.go`
- Modify: `internal/tui/list.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/new_issue_form_test.go`
- Modify: `internal/tui/form_test.go`
- Modify: `internal/tui/snapshot_test.go`
- Update: `internal/tui/testdata/golden/new-issue-form-all-fields.txt`
- Add: `internal/tui/testdata/golden/new-child-form.txt`

- [ ] **Step 9.1: Add parent field to new issue form**

Update `newInputState` for `inputNewIssueForm` from 4 to 5 fields:

1. Title
2. Body
3. Labels
4. Owner
5. Parent

Parent field accepts an issue number string. Empty means no parent.

For `N new child`, the Parent field starts prefilled and locked. While locked, typed edits are ignored; backspace/delete/ctrl+u clears the whole Parent value and unlocks the field. Once empty, it behaves like the editable Parent field in `n new issue`.

- [ ] **Step 9.2: Write form normalization tests**

Add tests:

```go
func TestNewIssueForm_ParentBlankOmitsLink(t *testing.T)
func TestNewIssueForm_ParentNumberCreatesInitialParentLink(t *testing.T)
func TestNewIssueForm_ParentInvalidShowsError(t *testing.T)
```

Run:

```bash
go test ./internal/tui -run 'TestNewIssueForm_Parent' -count=1
```

Expected: FAIL.

- [ ] **Step 9.3: Implement parent parsing**

Add:

```go
func normalizeParentNumber(buf string) (*int64, error)
```

Rules:

- blank -> nil, nil
- `#42` or `42` -> pointer to 42
- <= 0 or non-number -> validation error in form

Update `commitNewIssueForm` and `dispatchFormCreateIssue` to accept links.

Run Step 9.2. Expected: PASS.

- [ ] **Step 9.4: Add new-child open path**

In list key handling:

- `N` opens `inputNewIssueForm` with mode/title `new child issue`
- prefill Parent with selected issue number and mark the Parent field locked
- if no selected visible row, no-op
- gate all-projects same as `n`

This may require `openInputMsg` to carry an optional parent target/title:

```go
type openInputMsg struct {
	kind inputKind
	parentNumber *int64
	titleOverride string
}
```

- [ ] **Step 9.5: Write no-selection and prefill tests**

Add:

```go
func TestList_NewChild_NoSelectionNoOp(t *testing.T)
func TestList_NewChild_PrefillsSelectedParent(t *testing.T)
func TestNewChildForm_ParentPrefillIgnoresEditsUntilCleared(t *testing.T)
func TestNewChildForm_ParentPrefillBackspaceClearsAndUnlocks(t *testing.T)
```

Run:

```bash
go test ./internal/tui -run 'TestList_NewChild|TestNewIssueForm_Parent' -count=1
```

Expected: PASS.

- [ ] **Step 9.6: Add new-child snapshot**

Run:

```bash
go test ./internal/tui -run 'TestSnapshot_New(Child|Issue)Form' -update-goldens -count=1
```

Inspect Parent field and footer hint.

- [ ] **Step 9.7: Run form tests**

```bash
go test ./internal/tui -run 'Test(NewIssue|List_NewChild|Snapshot_New|Form)' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/input.go internal/tui/inputs_render.go internal/tui/list.go internal/tui/model.go internal/tui/new_issue_form_test.go internal/tui/form_test.go internal/tui/snapshot_test.go internal/tui/testdata/golden/
git commit -m "feat(tui): create child issues from the queue"
```

---

## Task 10: Parent-Link SSE Invalidation

**Files:**

- Modify: `internal/tui/events_sse_parse.go`
- Modify: `internal/tui/messages.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/sse_test.go`
- Modify: `internal/tui/sse_update_test.go`

- [ ] **Step 10.1: Inspect event payload parsing**

Confirm `eventReceivedMsg` carries `eventType`, `projectID`, `issueNumber`, and a typed link payload for link events. If link payload data is not retained today, add:

```go
type linkPayload struct {
	Type       string `json:"type"`
	FromNumber int64  `json:"from_number"`
	ToNumber   int64  `json:"to_number"`
}

type eventReceivedMsg struct {
	// existing fields...
	link *linkPayload
}
```

Decode `link` only for `issue.linked` and `issue.unlinked`; do not carry raw `map[string]any` payloads through consumers.

- [ ] **Step 10.2: Add parser test for parent link event**

In `sse_test.go`, parse an `issue.linked` SSE frame with payload:

```json
{"type":"parent","from_number":43,"to_number":42}
```

Assert `eventReceivedMsg` includes event type and typed link payload fields.

Run:

```bash
go test ./internal/tui -run TestSSEParser_LinkPayloadType -count=1
```

Expected: FAIL if typed link payload data is not decoded.

- [ ] **Step 10.3: Decode link event payload once**

Update parser/event message as needed. Link payload decoding should happen in the SSE parser so model consumers do not repeat type assertions. Keep existing tests passing.

- [ ] **Step 10.4: Add invalidation tests**

In `sse_update_test.go`, add:

```go
func TestHandleEventReceived_ParentLinkInvalidatesQueue(t *testing.T)
func TestHandleEventReceived_ParentLinkRefetchesOpenParentDetail(t *testing.T)
func TestHandleEventReceived_NonParentLinkDoesNotRefetchForHierarchy(t *testing.T)
```

Expected:

- parent link affects list working set like any relevant issue event
- if open detail issue number equals `to_number` parent, refetch detail because children changed
- if open detail issue number equals `from_number` child, refetch detail because parent changed

- [ ] **Step 10.5: Implement parent-link event helpers**

Add:

```go
func (msg eventReceivedMsg) parentLinkEndpoints() (from, to int64, ok bool)
```

Add `maybeRefetchHierarchyDetail` and call it from `handleEventReceived` alongside `maybeRefetchOpenDetail`:

- existing issue-number match still refetches
- parent-link payload match on from/to also refetches when open detail is either endpoint

Run Step 10.4. Expected: PASS.

- [ ] **Step 10.6: Run SSE tests**

```bash
go test ./internal/tui -run 'TestSSE|TestHandleEventReceived|TestRefetchOpenDetail' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/tui/events_sse_parse.go internal/tui/messages.go internal/tui/model.go internal/tui/sse_test.go internal/tui/sse_update_test.go
git commit -m "feat(tui): refresh hierarchy on parent link events"
```

---

## Task 11: Final Visual Polish And Full Verification

**Files:**

- Modify as needed: `internal/tui/theme.go`
- Modify as needed: `internal/tui/list_render.go`
- Modify as needed: `internal/tui/detail_render.go`
- Modify as needed: `internal/tui/inputs_render.go`
- Modify as needed: `internal/tui/help.go`
- Update: `internal/tui/testdata/golden/*.txt`

- [ ] **Step 11.1: Audit chrome consistency**

Check:

- title strip and state strip across list/detail/split/help
- split gutter visible
- focused pane has border + textual focus cue
- info line priority: input > mutation/error > SSE > toast > truncation > scroll
- footer rows fit at 80 columns by dropping low-priority hints

- [ ] **Step 11.2: Audit forms**

Confirm:

- new issue form centered
- new child form centered
- edit body form centered
- comment form centered
- panel prompts still commit/cancel cleanly

If any comment flow is still inline, convert it here.

For the filter form Labels axis, implement or lock down the full UI behavior here rather than treating it as a visual-only check:

- expose a Labels field in the filter form if it is not already present
- accept comma-separated labels and normalize with the same label parser used by issue forms
- apply labels as an any-of client-side filter against hydrated list-row labels
- ensure clear/reset removes label filters along with status/owner/search
- add or update form tests and snapshots for applying, resetting, and rendering Labels

- [ ] **Step 11.3: Run snapshot update once**

```bash
go test ./internal/tui -run TestSnapshot -update-goldens -count=1
```

Review every diff. Expected categories:

- list queue hierarchy
- detail parent/children
- footer hint rows
- title/state strip polish
- forms/help updates

- [ ] **Step 11.4: Run NO_COLOR/no-color snapshots**

```bash
NO_COLOR=1 go test ./internal/tui -run 'TestSnapshot_.*NoColor|TestSnapshot_List_TreeNoColor|TestSnapshot_NarrowTerminalHint' -count=1
KATA_COLOR_MODE=none go test ./internal/tui -run 'TestSnapshot_.*NoColor|TestSnapshot_List_TreeNoColor|TestSnapshot_NarrowTerminalHint' -count=1
```

Expected: PASS; disclosure fallback is `+`/`-`.

- [ ] **Step 11.5: Run focused packages**

```bash
go test ./internal/db ./internal/daemon ./internal/tui ./cmd/kata -count=1
```

Expected: PASS.

- [ ] **Step 11.6: Run full suite**

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 11.7: Optional lint if available**

```bash
golangci-lint run ./...
```

Expected: PASS. If `golangci-lint` is unavailable in the environment, record that in the final handoff.

Commit:

```bash
git add internal/tui internal/db internal/daemon internal/api cmd/kata
git commit -m "feat(tui): polish professional workspace experience"
```

---

## Plan Review Checklist

Before executing this plan, verify:

- [ ] Spec path still exists and matches the locked decisions.
- [ ] No implementation task reintroduces a flat queue mode.
- [ ] Status filtering is client-side in the TUI queue path.
- [ ] Cache key ignores rendered filter state.
- [ ] DB helper chunking follows `LabelsByIssues`.
- [ ] Detail Children uses `show issue.children`.
- [ ] Queue truncation uses a 2,001-row fetch probe, trims to 2,000 rows, and avoids the exact-boundary false positive.
- [ ] `Client.GetIssue` is removed after detail call sites move to `GetIssueDetail`; no unused compatibility wrapper remains.
- [ ] Context rows have a visible `~` marker in `NO_COLOR`, not color-only styling.
- [ ] `space` on leaf and `N` without selected row are silent no-ops.
- [ ] Phase 5-style visual work is split across Tasks 7, 8, 9, and 11, not one broad snapshot commit.
