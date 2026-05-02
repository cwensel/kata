# Plan 8 — Detail Rework, Label Cache, Create Form, Filter Modal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Pair this with the design doc** at `2026-05-02-kata-8-detail-rework-and-create-form-design.md`. The design doc holds the rationale, mockups, and locked decisions. This file is operational: per-commit tasks, files to touch, tests to add, acceptance criteria.

**Goal:** Wire labels into the TUI's detail and list views, restructure the detail header for visual hierarchy, add a project label cache + autocomplete on `+`/`-`, replace the inline-row + post-create chain with a multi-field new-issue form, and add a filter modal with Status/Owner/Search axes (5a) plus a Labels axis (5b, daemon work).

**Architecture:** Extends Plan 7's centered-form input shell (`isCenteredForm` family) with two new kinds (`inputNewIssueForm`, `inputFilterForm`) and an autocomplete state on the existing M3b panel-local prompts. Adds a Model-level project label cache keyed by projectID with dispatch-time generation stamping for stale-response rejection. A daemon DB query + wire DTO addition (`db.LabelsByIssues` + `api.IssueOut`) lights up label-axis filtering on the list.

**Tech Stack:** Go 1.22, Bubble Tea v1.3.10, lipgloss, bubbles textinput/textarea, charmbracelet/x/ansi, runewidth, internal/textsafe.

---

## Standing directives

- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
- Run `make lint` (golangci-lint) and `go test ./...` before each commit. Zero warnings.
- Hard limits: ≤100 lines/function, cyclomatic complexity ≤8, 100-char lines.
- Each commit (1, 2, 3, 4, 5a, 5b) is a separate commit. 5a and 5b are sequential; 5b cannot ship without 5a's modal in place.
- Roborev-fix checkpoint after **commit 4** (M3.5c+M4 deletions; large surface) and **commit 5b** (daemon wire change). Run `roborev fix --open --list` before committing the milestone, address findings, then commit + close reviews.
- Snapshot fixtures live under `internal/tui/testdata/golden/`. Use `go test ./internal/tui/ -update-goldens` to regenerate after deliberate visual changes; review the diff before committing the new fixtures.

## Hard invariants (must hold through every commit)

These are tested-and-shipped behaviors from Plans 6+7 + this session's hardening that must not regress. Every commit's acceptance criteria includes "all hard invariants still hold" — verified by running the existing test suite.

| Invariant | Owning code | Test that proves it |
|---|---|---|
| List rendering is viewported by terminal height | `windowIssues` in `list_render.go` | `TestEdge_ListViewport_KeepsCursorVisible` |
| Cursor follows issue identity, not row index | `selectedNumber` + `applyFetched` in `list.go` | `TestEdge_IdentitySelection_FollowsIssueAcrossReorder` |
| Stale list refetches dropped | `dispatchKey` + `cacheKeysEqual` + `isStaleListFetch` | `TestEdge_StaleRefetch_DroppedAfterFilterChange` |
| Detail-fetch generation monotonic | `Model.nextGen`, `handleJumpDetail`, `applyFetched`'s gen check | `TestModel_GenMonotonicAcrossJumpBackOpen`, `TestDetail_StaleFetch_DroppedAcrossJump` |
| Mutation routing to originating model after view-switch | `Model.routeMutation` | `TestEdge_ListMutation_CompletesAfterDetailOpen`, `..._DetailMutation_CompletesAfterPopToList` |
| reset_required refreshes open detail too | `Model.refetchOpenDetail` from `handleResetRequired` | `TestRefetchOpenDetail_*`, `TestHandleResetRequired_DropsCacheAndShowsToast` |
| SSE invalidation refetches all four detail tabs + cross-project guard | `maybeRefetchOpenDetail` | `TestHandleEventReceived_DetailViewRefetchesAllTabs` |
| Sanitization at every render boundary | `textsafe.Block` / `textsafe.Line` calls in renderers | `TestSanitizeForDisplay_*`, `TestList_SanitizesAnsiAndNewlinesInTitle` |
| `--all-projects` and R-toggle gated until daemon ships cross-project list | `cmd/kata/tui_cmd.go`, `bootResolveScope`, `handleScopeToggle` | `TestScopeToggle_GatedNoOp`, `TestScopeToggle_RKeyDispatch_Gated` |
| Centered-form formGen guards stale editor returns | `Model.routeEditorReturn` | `TestForm_EditorReturn_StaleFormGen_DropsContent` |
| Form mutation success closes form + reclassifies as detail | `Model.routeFormMutation` | `TestRouteFormMutation_Success_ClosesFormAndDispatchesToDetail` |
| ctrl+c bypasses any modal | `Model.routeModalKey`'s ctrl+c branch | `TestQuit_CtrlCFastQuits` |

If a commit's work would require touching one of these, **port the test forward** rather than removing it. The invariants encode regressions worth keeping.

**New invariants this plan adds (each commit's tests pin them):**

| Invariant | Lands in | Test |
|---|---|---|
| `Issue.Labels` populates on first detail open via `fetchIssue` | Commit 1 | `TestDetailFetch_PopulatesIssueLabelsOnOpen` |
| `renderLabelChips` measures cell width with runewidth+sanitize | Commit 2 | `TestRenderLabelChips_WidthMeasureUsesRunewidth` |
| Label cache stamps gen at dispatch (not on response) | Commit 3 | `TestLabelCache_DispatchStampsGenBeforeResponse` |
| Label cache rejects stale gen response | Commit 3 | `TestLabelCache_StaleGenResponseDropped` |
| Label cache rejects mismatched-pid response | Commit 3 | `TestLabelCache_MismatchedPidResponseDropped` |
| SSE `issue.labeled` invalidates project-label cache distinctly from list refetch | Commit 3 | `TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly` |
| New-issue form mutation success routes through list create handling, not detail | Commit 4 | `TestNewIssueForm_MutationSuccessRoutesToList` |
| Inline new-issue row code + tests removed | Commit 4 | `grep -r inputNewIssueRow internal/tui/` returns nothing |
| Filter modal commit goes through dedicated `commitFilterForm`, not `applyLiveBarFilter` | Commit 5a | `TestFilterForm_CommitUsesDedicatedPath` |
| `filteredIssues` fast-path includes Labels in the early-return condition | Commit 5b | `TestFilteredIssues_FastPathIncludesLabels` |

## Files to preserve (transport/state layer)

Edit surgically only when a hard invariant requires it; never rewrite wholesale:

- `internal/tui/client.go` — typed daemon HTTP wrapper (commit 1 modifies `showIssue` only)
- `internal/tui/events_sse.go`, `events_sse_parse.go` — SSE consumer
- `internal/tui/messages.go` — message-type definitions (may add new types but don't reshape existing)
- `internal/tui/cache.go` — single-slot list cache (separate from new label cache)
- `internal/daemonclient/` — daemon discovery + HTTP client construction
- `internal/textsafe/` — sanitizer package

## Files in scope for this plan

Render, input, and routing — fair game for this plan:

- `internal/tui/client_types.go` — `Issue.Labels` field, `CreateIssueBody` extensions
- `internal/tui/client.go` — `showIssue` decode of `body.Labels` into `resp.Issue.Labels`
- `internal/tui/detail.go` — `handleOpenDetail` dispatches `fetchIssue`
- `internal/tui/detail_render.go` — header restructure, labeled rules, fixed-row budget
- `internal/tui/list_render.go` — `renderLabelChips`, `renderChips` (label chips), inline-row deletion
- `internal/tui/list.go` — drop new-issue-row code, drop `o` key, list-decode label hydration (5b)
- `internal/tui/input.go` — drop `inputNewIssueRow`/`inputBodyEditPostCreate`, add `inputNewIssueForm`/`inputFilterForm`, autocomplete state on `inputState`
- `internal/tui/inputs_render.go` — flesh out form rendering for new kinds + suggestion menu
- `internal/tui/model.go` — label cache, dispatch helpers, `routeFormMutation` branches, post-create chain removal, `routeDetailFormKey` for `e`/`c`/`f`
- `internal/tui/keymap.go` — drop `o`, add `f`
- `internal/tui/help.go` — refresh under new keymap
- `internal/tui/sse_update.go` — label SSE event handling for cache invalidation
- `internal/api/types.go` — `IssueOut` struct, `ListIssuesResponse` shape change (5b)
- `internal/daemon/handlers_issues.go` — list handler builds `[]IssueOut` (5b)
- `internal/db/queries.go` — new `LabelsByIssues` query (5b)

## New files this plan creates

- `internal/tui/label_cache.go` (commit 3) — `labelCache`, `labelCacheEntry`, dispatch + acceptance helpers
- `internal/tui/label_cache_test.go` (commit 3)
- `internal/tui/suggest_render.go` (commit 3) — right-anchored suggestion menu render + placement helper
- `internal/tui/new_issue_form_test.go` (commit 4)
- `internal/tui/filter_form_test.go` (commits 5a/5b)
- `internal/db/queries_labels_by_issues_test.go` (commit 5b)

## Test files modified or deleted

- DELETE in commit 4: tests for `inputNewIssueRow` (`TestList_NewIssueRow_*` in `list_filter_test.go`) and `inputBodyEditPostCreate` (`TestPostCreate_*` and `TestForm_OpenBodyEditPostCreate_*` in `form_test.go`).
- DELETE in commit 4: snapshot `TestSnapshot_List_NewIssueRow` + golden `list-new-issue-row.txt`.
- MODIFY in commit 2: every detail snapshot golden (header changed); `TestSnapshot_Detail_*`.
- MODIFY in commit 5a: list snapshots if footer help changes (it does — `o owner` → `f filter`).

---

## Commit 1 — Decode show labels + add fetchIssue to detail-open path

**Goal:** Get labels onto the detail view's data path. Two changes packaged together so labels actually appear on first detail open: extend the TUI's `Issue` projection with a `Labels []string` field, decode `body.Labels` from `showIssue`, and dispatch `fetchIssue` from `handleOpenDetail` so the show-response actually arrives.

**Files:**
- Modify: `internal/tui/client_types.go` — add `Labels []string` field to `Issue`
- Modify: `internal/tui/client.go` — extend `showIssue` to populate `resp.Issue.Labels` from `body.Labels`
- Modify: `internal/tui/model.go` — `handleOpenDetail` adds `fetchIssue` to its `tea.Batch`; same for `handleJumpDetail`
- Modify: `internal/tui/detail.go` — `applyFetched`'s `detailFetchedMsg` branch carries `Labels` through into `dm.issue`
- Test: `internal/tui/client_test.go` — show-response decode test
- Test: `internal/tui/detail_test.go` — `handleOpenDetail` dispatches fetchIssue; applyFetched preserves labels

**Invariants touched:**
- Detail-fetch generation (no change; `fetchIssue` already gen-tags via the existing `fetchIssue` helper).
- Sanitization (none — labels go through render-time sanitization in commit 2).

**Hard invariants this commit pins:**
- `Issue.Labels` populates on first detail open via `fetchIssue` (`TestDetailFetch_PopulatesIssueLabelsOnOpen`).

### Steps

- [ ] **Step 1.1: Add `Labels` field to `Issue`.** In `internal/tui/client_types.go`, add `Labels []string \`json:"labels,omitempty"\`` to the `Issue` struct (after `DeletedAt`). The tag matters: list decode (5b) populates this directly; detail decode (this commit) manually copies from `body.Labels`. `omitempty` means absence in show responses doesn't blank the field.

- [ ] **Step 1.2: Verify wire shape.** `rg -n 'ShowIssueResponse' internal/api/types.go` — confirm `Body.Labels` is `[]db.IssueLabel` with `IssueID, Label, Author, CreatedAt`. Confirm `Body.Issue` is `db.Issue` (no labels there).

- [ ] **Step 1.3: Write the failing show-decode test.** In `internal/tui/client_test.go`, add `TestShowIssue_PopulatesLabelsFromTopLevel`:
    ```go
    func TestShowIssue_PopulatesLabelsFromTopLevel(t *testing.T) {
        // Stand up a test server that returns a ShowIssueResponse body
        // with body.labels containing 3 IssueLabel rows ([bug, prio-1, needs-design]).
        // Call client.showIssue. Assert resp.Issue.Labels == ["bug", "needs-design", "prio-1"]
        // (alphabetical sort).
    }
    ```
    Run: `go test ./internal/tui/ -run TestShowIssue_PopulatesLabelsFromTopLevel -v`. Expected: FAIL.

- [ ] **Step 1.4: Implement.** In `internal/tui/client.go`'s `showIssue`, after JSON decode succeeds, walk `body.Labels` extracting `.Label` into a slice, sort alphabetically, assign to `resp.Issue.Labels`. Re-run the test; expected: PASS.

- [ ] **Step 1.5: Write the failing handleOpenDetail test.** In `internal/tui/detail_test.go`, add `TestHandleOpenDetail_DispatchesFetchIssue`:
    ```go
    func TestHandleOpenDetail_DispatchesFetchIssue(t *testing.T) {
        // Build a Model with an api stub that records calls.
        // Dispatch openDetailMsg{issue: Issue{Number: 42}}.
        // Drain the returned cmd. Assert the api stub recorded a GetIssue(pid, 42)
        // call alongside the existing comments/events/links calls (4 total).
    }
    ```
    Run; expected: FAIL (current code doesn't dispatch fetchIssue from handleOpenDetail).

- [ ] **Step 1.6: Implement.** In `internal/tui/model.go::handleOpenDetail` (around model.go:1135), add `fetchIssue(m.api, pid, iss.Number, gen)` to the `cmds` slice. Same for `handleJumpDetail` if it doesn't already. Verify by re-reading model.go:1180–1200. Re-run; expected: PASS.

- [ ] **Step 1.7: Write the failing applyFetched test.** Add `TestDetailFetch_PopulatesIssueLabelsOnOpen`:
    ```go
    func TestDetailFetch_PopulatesIssueLabelsOnOpen(t *testing.T) {
        // Build a detailModel.
        // Send detailFetchedMsg{gen: dm.gen, issue: &Issue{Number: 42, Labels: ["bug"]}}.
        // Assert dm.issue.Labels == ["bug"].
    }
    ```
    Run; expected: PASS already (applyFetched already replaces dm.issue verbatim from msg.issue).

- [ ] **Step 1.8: Lint + test.** `make lint` clean; `go test ./...` green. All hard invariants still hold (look for any `dm.issue.X` access that assumed Labels was nil — likely none).

- [ ] **Step 1.9: Commit.**
    ```bash
    git add internal/tui/client_types.go internal/tui/client.go internal/tui/model.go internal/tui/client_test.go internal/tui/detail_test.go
    git commit -m "feat(tui): decode show labels + dispatch fetchIssue on detail open

Plan 8 commit 1: extends Issue with Labels []string (json:\"labels,omitempty\"),
populates from showIssue body.labels (alphabetical), and adds fetchIssue
to handleOpenDetail / handleJumpDetail so the show response actually
arrives on first open. Without the fetchIssue dispatch, dm.issue is only
the list-row seed and Labels stays empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `Issue.Labels` populates on first detail open (verified by `TestDetailFetch_PopulatesIssueLabelsOnOpen`).
- `Client.showIssue` returns labels alphabetically sorted (verified by `TestShowIssue_PopulatesLabelsFromTopLevel`).
- `handleOpenDetail` and `handleJumpDetail` both dispatch `fetchIssue` alongside the three tab fetches.
- All hard invariants hold.

**Risks:** Low. Pure data-path widening; no render or input changes. The only sharp edge is making sure `applyFetched`'s `detailFetchedMsg` branch doesn't drop labels — verify by reading model.go:135–145.

---

## Commit 2 — Detail header restructure + section dividers + chip rendering

**Goal:** Replace the single-line detail header with a three-row layout (meta + assignment + title), add labeled rules above the body and the activity panel, and introduce `renderLabelChips` for width-managed alphabetical chip packing with `+N` overflow.

**Files:**
- Modify: `internal/tui/detail_render.go` — fixed-row budget (7→9), header restructure, labeled rules
- Modify: `internal/tui/list_render.go` — `renderLabelChips` lives here for shared use (commit 5b also calls it)
- Test: `internal/tui/list_render_test.go` — `renderLabelChips` unit tests
- Test: `internal/tui/snapshot_test.go` — new and updated detail snapshots
- Update: every existing `internal/tui/testdata/golden/detail-*.txt` (header layout changed)

**Invariants touched:**
- Sanitization (label chips render through `textsafe.Block`).
- Detail render budget (the fixed-row count change is the load-bearing part).

**Hard invariants this commit pins:**
- `renderLabelChips` measures cell width with runewidth + sanitize (`TestRenderLabelChips_WidthMeasureUsesRunewidth`).

### Steps

- [ ] **Step 2.1: Bump fixed-row budget.** In `internal/tui/detail_render.go::View`, the comment around line 42 says `Reserve: header (1) + detail-header (1) + title-row (1) + body-rule (1) + tab-rule (1) + info (1) + footer (1) = 7 fixed`. Update to: `header (1) + meta (1) + assignment (1) + title (1) + body-rule (1) + activity-rule (1) + tab-strip (1) + info (1) + footer (1) = 9 fixed`. Change `avail := height - 7` to `avail := height - 9`. Without this, the new rows push the footer off-screen.

- [ ] **Step 2.2: Write `renderLabelChips` tests.** In `internal/tui/list_render_test.go` (create if absent), add:
    ```go
    func TestRenderLabelChips_AlphabeticalSort(t *testing.T) {
        got := renderLabelChips([]string{"prio-1", "bug", "needs-design"}, 80)
        // Must contain "[bug]" before "[needs-design]" before "[prio-1]" in the output.
    }
    func TestRenderLabelChips_PacksUntilOverflow(t *testing.T) {
        got := renderLabelChips([]string{"a", "b", "c", "d", "e"}, 12)
        // Available width 12 fits about ~3 short chips; expect "+N" suffix for the rest.
    }
    func TestRenderLabelChips_PlusNOverflowFormat(t *testing.T) {
        // Verify the +N token format and that it reserves its own width.
    }
    func TestRenderLabelChips_UltraNarrowFallback(t *testing.T) {
        got := renderLabelChips([]string{"bug", "prio-1"}, 5)
        // Width too small for any chip — must collapse to "[2 labels]".
    }
    func TestRenderLabelChips_EmptyLabels(t *testing.T) {
        got := renderLabelChips(nil, 80)
        // Must render the placeholder "(no labels)".
    }
    func TestRenderLabelChips_WidthMeasureUsesRunewidth(t *testing.T) {
        // Label with wide-glyph "かた" (4 cells) and ANSI escape "\x1b[31m":
        // sanitize first, then runewidth.StringWidth. Verify packing math.
    }
    ```
    Run; expected: FAIL (function doesn't exist).

- [ ] **Step 2.3: Implement `renderLabelChips`.** Add to `internal/tui/list_render.go`:
    ```go
    // renderLabelChips packs label chips into `available` cells, alphabetical,
    // dropping trailing overflow into +N. ANSI/Unicode-control labels sanitized
    // first; cell width via runewidth. Empty labels yield "(no labels)";
    // ultra-narrow available yields "[N labels]".
    func renderLabelChips(labels []string, available int) string {
        if len(labels) == 0 {
            return subtleStyle.Render("(no labels)")
        }
        sorted := append([]string(nil), labels...)
        sort.Strings(sorted)
        // ... pack chips, drop tail into +N, fallback to [N labels].
    }
    ```
    Each chip = `[<label>]` + 1-space separator → width = `chipCellWidth(label) + 3`. `chipCellWidth` = `runewidth.StringWidth(textsafe.StripANSI(textsafe.Block(label)))`. Reserve `+N` width in the loop math (worst-case 4 chars: ` +99`).

- [ ] **Step 2.4: Run tests.** All `TestRenderLabelChips_*` should pass.

- [ ] **Step 2.5: Add labeled rule helper.** In `internal/tui/detail_render.go`:
    ```go
    // renderLabeledRule produces "── <label> ──" padded to width with dashes.
    // Falls back to a plain dash run when width is too narrow for the label.
    func renderLabeledRule(label string, width int) string {
        prefix := "── " + label + " ──"
        prefixW := runewidth.StringWidth(prefix)
        if prefixW > width {
            // Plain dash fallback; never strings.Repeat with a negative count.
            if width <= 0 {
                return ""
            }
            return strings.Repeat("─", width)
        }
        return prefix + strings.Repeat("─", width-prefixW)
    }
    ```

- [ ] **Step 2.6: Restructure header.** Replace `renderHeader` (currently single-line) with three render helpers:
    - `renderHeaderMeta(width, iss) string` — `#N · status · author · created Xago · updated Yago` (current content).
    - `renderHeaderAssignment(width, iss) string` — `Owner: <name>` left + `renderLabelChips(...)` right, joined via `padLeftRightInside`. Owner placeholder: `Owner: —`.
    - `renderHeaderTitle(width, iss) string` — bold full-width title (current `renderTitleRow`).

- [ ] **Step 2.7: Wire the new helpers.** In `View`, replace the existing 3-line header composition with:
    ```go
    title := renderTitleBar(width, chrome.scope, chrome.version)
    meta := renderHeaderMeta(width, *dm.issue)
    assign := renderHeaderAssignment(width, *dm.issue)
    titleRow := renderHeaderTitle(width, *dm.issue)
    bodyRule := renderLabeledRule("body", width)
    activityRule := renderLabeledRule("activity", width)
    // ... rest of View, joining meta/assign/titleRow/bodyRule/body/activityRule/tabs/...
    ```

- [ ] **Step 2.8: Update detail snapshot tests.** Run `go test ./internal/tui/ -run TestSnapshot_Detail -update-goldens`. Diff each golden — expect:
    - 1 line removed (old header), 3 lines added (meta + assignment + title).
    - 1 unlabeled rule replaced with `── body ──`.
    - 1 unlabeled rule above tab strip becomes `── activity ──`.
    - Owner: `—` placeholder visible when fixture has no owner.
    - `(no labels)` placeholder visible when fixture has no labels.

- [ ] **Step 2.9: Add chip-visible snapshot.** New `TestSnapshot_Detail_WithLabels` — fixture with `Labels: []string{"bug", "prio-1", "needs-design"}`, owner set, full-width terminal. Asserts the chip row renders as `Owner: alice                                  [bug] [needs-design] [prio-1]`.

- [ ] **Step 2.10: Add narrow-width snapshot.** New `TestSnapshot_Detail_LabelsNarrow_OverflowAndDegrade` — same fixture at 60-cell width, asserts `+N` overflow appears; and at 30-cell width, asserts `[N labels]` ultra-narrow fallback.

- [ ] **Step 2.11: Lint + test.** `make lint` clean; `go test ./...` green.

- [ ] **Step 2.12: Commit.**
    ```bash
    git add internal/tui/detail_render.go internal/tui/list_render.go internal/tui/list_render_test.go internal/tui/snapshot_test.go internal/tui/testdata/golden/
    git commit -m "feat(tui): detail header restructure + labeled rules + chip rendering

Plan 8 commit 2: three-row header (meta / assignment / title) replaces
the single-line header. Owner left, label chips right; alphabetical
packing with +N overflow and ultra-narrow [N labels] fallback. Labeled
── body ── / ── activity ── rules give panel boundaries explicit framing.
Fixed-row budget 7→9. Width math uses runewidth+sanitize, never
strings.Repeat with negative counts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- Detail header reads `meta / assignment / title` with chip row visible when labels exist, placeholder when not.
- `── body ──` and `── activity ──` rules render above their respective panels.
- Chip overflow uses `+N`; ultra-narrow widths collapse to `[N labels]`.
- All hard invariants hold (re-run full TUI test suite).

**Risks:** Medium. Snapshot churn is wide (every detail golden updates). Verify each diff is the expected three-row substitution, not an unintended layout shift. The fixed-row budget change is load-bearing — if it's wrong, the footer pins to the wrong row and the panel below the activity rule jitters.

---

## Commit 3 — Project label cache + autocomplete on `+` and `-` + SSE invalidation

**Goal:** Land the Model-level project label cache (keyed by projectID, gen-stamped at dispatch), the right-anchored vertical suggestion menu, and the autocomplete update routing for `+` (project labels) and `-` (currently-attached labels). SSE `issue.labeled` / `issue.unlabeled` events invalidate the project-label suggestion cache distinctly from list/detail refetch.

**Files:**
- Create: `internal/tui/label_cache.go` — `labelCache`, `labelCacheEntry`, dispatch + acceptance helpers
- Create: `internal/tui/label_cache_test.go`
- Create: `internal/tui/suggest_render.go` — right-anchored vertical menu render + `overlayAtCorner` placement helper
- Modify: `internal/tui/model.go` — `Model.projectLabels`, `nextLabelsGen`, dispatch wiring, response routing
- Modify: `internal/tui/messages.go` — `labelsFetchedMsg` carrying `(pid, gen, labels []LabelCount, err)`
- Modify: `internal/tui/input.go` — autocomplete state on `inputState` (`suggestHighlight`, `suggestScroll`); generalize `formTarget` to apply to label prompts
- Modify: `internal/tui/inputs_render.go` — render menu above the panel-prompt row, intercept ↑↓⇥ before textinput
- Modify: `internal/tui/sse_update.go` — `issue.labeled`/`issue.unlabeled` invalidate `m.projectLabels.byProject[pid]` and dispatch refetch
- Modify: `internal/tui/detail_render.go` — subtract menu height from tab/body budget when menu is open
- Test: `internal/tui/suggest_render_test.go` — menu render + scrolling
- Test: `internal/tui/snapshot_test.go` — menu fixtures (5 suggestions, loading, error, empty, scroll)

**Invariants touched:**
- Sanitization (suggestions render through `textsafe.Line`).
- Detail-fetch generation (label cache uses its own counter, doesn't collide with `nextGen` or `nextFormGen`).

**Hard invariants this commit pins:**
- `TestLabelCache_DispatchStampsGenBeforeResponse`.
- `TestLabelCache_StaleGenResponseDropped`.
- `TestLabelCache_MismatchedPidResponseDropped`.
- `TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly`.

### Steps

- [ ] **Step 3.1: Define cache types.** In `label_cache.go`:
    ```go
    type labelCache struct {
        byProject map[int64]labelCacheEntry
    }
    type labelCacheEntry struct {
        labels   []LabelCount
        gen      int64
        pid      int64
        err      error
        fetching bool
    }
    func newLabelCache() *labelCache { return &labelCache{byProject: map[int64]labelCacheEntry{}} }
    ```

- [ ] **Step 3.2: Add `Model.projectLabels` and `Model.nextLabelsGen`.** In `model.go`'s Model struct, add:
    ```go
    projectLabels  *labelCache
    nextLabelsGen  int64
    ```
    Initialize `projectLabels` in `initialModel`.

- [ ] **Step 3.3: Add `labelsFetchedMsg`.** In `messages.go`:
    ```go
    type labelsFetchedMsg struct {
        pid    int64
        gen    int64
        labels []LabelCount
        err    error
    }
    ```

- [ ] **Step 3.4: Write the dispatch-stamps-gen test.** In `label_cache_test.go`:
    ```go
    func TestLabelCache_DispatchStampsGenBeforeResponse(t *testing.T) {
        // m := buildModelWithLabelCache(); pid := int64(7)
        // m, _ = m.dispatchLabelFetch(pid)
        // entry := m.projectLabels.byProject[pid]
        // require.True(t, entry.fetching)
        // require.Greater(t, entry.gen, int64(0))
        // require.Equal(t, pid, entry.pid)
    }
    ```
    Run; expected: FAIL.

- [ ] **Step 3.5: Implement `dispatchLabelFetch`.** In `model.go`:
    ```go
    func (m Model) dispatchLabelFetch(pid int64) (Model, tea.Cmd) {
        m.nextLabelsGen++
        gen := m.nextLabelsGen
        entry := m.projectLabels.byProject[pid]
        entry.gen = gen
        entry.pid = pid
        entry.fetching = true
        entry.err = nil
        m.projectLabels.byProject[pid] = entry
        return m, fetchLabelsCmd(m.api, pid, gen)
    }
    ```
    `fetchLabelsCmd` is a new helper that calls `api.ListLabels(pid)` and emits `labelsFetchedMsg{pid, gen, labels, err}`. Re-run; expected: PASS.

- [ ] **Step 3.6: Write stale-gen rejection test.** Add `TestLabelCache_StaleGenResponseDropped`:
    ```go
    func TestLabelCache_StaleGenResponseDropped(t *testing.T) {
        // m := buildModelWithLabelCache(); pid := int64(7)
        // m, _ = m.dispatchLabelFetch(pid)  // gen=1
        // m, _ = m.dispatchLabelFetch(pid)  // gen=2 (newer dispatch)
        // // Old gen=1 response arrives:
        // out, _ := m.Update(labelsFetchedMsg{pid: pid, gen: 1, labels: []LabelCount{{"old", 1}}})
        // // Cache should NOT have "old" — gen=1 < cache.gen=2, dropped.
        // entry := out.(Model).projectLabels.byProject[pid]
        // require.Empty(t, entry.labels)
    }
    ```
    Run; expected: FAIL.

- [ ] **Step 3.7: Implement response routing.** In `model.go`'s message switch (somewhere appropriate, ~near `routeMutation`):
    ```go
    if lf, ok := msg.(labelsFetchedMsg); ok {
        m = m.handleLabelsFetched(lf)
        return m, nil
    }
    ```
    `handleLabelsFetched`:
    ```go
    func (m Model) handleLabelsFetched(msg labelsFetchedMsg) Model {
        entry, exists := m.projectLabels.byProject[msg.pid]
        if !exists || msg.gen < entry.gen {
            return m  // stale
        }
        if msg.pid != m.targetPID() {
            return m  // project switched
        }
        entry.labels = msg.labels
        entry.err = msg.err
        entry.fetching = false
        m.projectLabels.byProject[msg.pid] = entry
        return m
    }
    ```
    `targetPID` returns `m.detail.scopePID` when `m.view == viewDetail` and `m.scope.projectID` otherwise. Re-run stale-gen test; expected: PASS.

- [ ] **Step 3.8: Write mismatched-pid test.** Add `TestLabelCache_MismatchedPidResponseDropped` — dispatch for pid=7, simulate scope switch to pid=8, response for pid=7 arrives, must be dropped.

- [ ] **Step 3.9: Write SSE-invalidation test.** Add `TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly`:
    ```go
    func TestLabelCache_SSEEventInvalidatesSuggestionCacheOnly(t *testing.T) {
        // m := buildModelWithLabelCache(); pid := int64(7)
        // // Pre-populate cache.
        // m.projectLabels.byProject[pid] = labelCacheEntry{labels: ..., gen: 1, pid: 7}
        // // SSE event arrives.
        // out, cmd := m.Update(eventReceivedMsg{event: "issue.labeled", projectID: 7, issueNumber: 42})
        // nm := out.(Model)
        // // Suggestion cache: a new fetch should be in flight (fetching=true, higher gen).
        // require.True(t, nm.projectLabels.byProject[pid].fetching)
        // require.Greater(t, nm.projectLabels.byProject[pid].gen, int64(1))
        // // List/detail refetch path is a separate concern — not asserted here.
    }
    ```

- [ ] **Step 3.10: Wire SSE.** In `sse_update.go`, the existing `eventReceivedMsg` handler dispatches list/detail refetches. Add: if event type is `issue.labeled` or `issue.unlabeled` AND `m.projectLabels.byProject[event.projectID]` exists, call `dispatchLabelFetch(event.projectID)` and add its cmd to the batch. Don't conflate this with the existing list/detail refetch path.

- [ ] **Step 3.11: Generalize `formTarget` for label prompts.** In `input.go`, `formTarget` already carries `projectID, issueNumber, detailGen`. Verify it's the same struct used by M4 forms (it is). Update `newPanelPrompt` (the constructor for `+`/`-` etc.) to accept and store a `formTarget` instead of just `issueNumber`.

- [ ] **Step 3.12: Add autocomplete state to `inputState`.** In `input.go`:
    ```go
    type inputState struct {
        // ... existing fields
        target           formTarget
        suggestHighlight int
        suggestScroll    int
    }
    ```
    `formGen` already exists; reuse for label-prompt staleness if needed (the cache's own gen check is the primary guard).

- [ ] **Step 3.13: Suggestion source helper.** In `input.go` or `label_cache.go`:
    ```go
    func (m Model) suggestionsForPrompt(s inputState) []LabelCount {
        switch s.kind {
        case inputLabelPrompt:
            return m.projectLabels.byProject[s.target.projectID].labels
        case inputRemoveLabelPrompt:
            // - source is currently-attached labels (NOT the project cache).
            attached := m.detail.issue.Labels
            out := make([]LabelCount, len(attached))
            for i, l := range attached {
                out[i] = LabelCount{Label: l, Count: 0}  // count irrelevant for `-`
            }
            return out
        }
        return nil
    }
    ```

- [ ] **Step 3.14: Filter + sort suggestions.** Add `filterSuggestions(all []LabelCount, prefix string) []LabelCount` that returns a copy sorted by **count desc, label asc** with prefix-filter applied (case-insensitive). For `-` source where counts are 0, the secondary sort (label asc) is the effective order.

- [ ] **Step 3.15: Intercept ↑↓⇥ before textinput.** In `input.go::Update` (or wherever the panel prompt branch lives), add explicit cases for `tea.KeyUp`, `tea.KeyDown`, `tea.KeyTab` BEFORE delegating to `field.input.Update(msg)`. Up/Down move `suggestHighlight` (wrap at boundaries). Tab completes the buffer to the highlighted suggestion's label.

- [ ] **Step 3.16: Render suggestion menu.** In new `suggest_render.go`:
    ```go
    func renderSuggestMenu(s inputState, suggestions []LabelCount, maxRows int) string {
        // Vertical bordered box; highlighted row in selectedStyle.
        // Each row: "[label] (count)" or "[label]" if count==0.
        // Width = max suggestion-cell width + padding, capped at panel width.
        // Caller composes via overlayAtCorner.
    }
    ```
    Add `overlayAtCorner(bg, panel string, width, height, anchorRow, anchorCol int) string` placement helper that splices `panel` into `bg` at `(anchorRow, anchorCol)` using ANSI-aware row/col splicing (similar to `overlayModal` but with explicit placement, not centered).

- [ ] **Step 3.17: Wire menu into Model.View.** When `m.input` is a label prompt AND suggestions are available, compute menu placement (right-anchored, bottom row = info-line row - 1), splice via `overlayAtCorner`. Subtract menu height from tab/body budget so the scroll indicator doesn't lie about overflow.

- [ ] **Step 3.18: Loading/error/empty placeholder rendering.** In `renderSuggestMenu`, before the entries, check cache state:
    - `entry.fetching && len(entry.labels) == 0` → `loading…`
    - `entry.err != nil` → `(error: <message>)`
    - `len(entry.labels) == 0 && !entry.fetching` → `(no labels in project — type to add)`

- [ ] **Step 3.19: Trigger fetch on prompt open.** When `+` or `-` opens the panel prompt, check `m.projectLabels.byProject[targetPID]`; if entry is absent or stale, dispatch `dispatchLabelFetch(targetPID)` (only for `+`; `-` uses attached labels and doesn't need the project cache).

- [ ] **Step 3.20: Refresh cache on local mutation success.** In `routeFormMutation` and `applyMutation`, after a successful label add/remove/create-with-labels, dispatch `dispatchLabelFetch(pid)`. Counts may have changed (add introduces new labels; remove may decrement to zero).

- [ ] **Step 3.21: Snapshot tests.**
    - `TestSnapshot_LabelPrompt_MenuOpen` — fixture with 5 suggestions, highlighted first.
    - `TestSnapshot_LabelPrompt_Loading` — `fetching=true, labels=nil`.
    - `TestSnapshot_LabelPrompt_Error` — `err=errStub("daemon 500")`.
    - `TestSnapshot_LabelPrompt_Empty` — `labels=nil, fetching=false`.
    - `TestSnapshot_LabelPrompt_Scroll` — 12 suggestions, highlight at index 9, asserts the visible window scrolled.

- [ ] **Step 3.22: Unit tests for interaction.**
    - `TestLabelPrompt_ArrowKeys_MoveHighlight_WithWrap` (highlight cycles 0→N-1→0).
    - `TestLabelPrompt_TabCompletesHighlightedSuggestion`.
    - `TestLabelPrompt_EnterCommitsCurrentBuffer` (suggestion or free-typed).
    - `TestLabelPrompt_EscClosesPromptAndMenu`.
    - `TestLabelPrompt_EmptyBuffer_ShowsTopProjectLabels` (no filter, count-desc sort).
    - `TestLabelPrompt_PrefixFilterCaseInsensitive`.
    - `TestRemoveLabelPrompt_SourceIsAttachedLabelsNotProjectCache`.
    - `TestSuggestMenu_HeightSubtractedFromTabBudget` (visual; verifies layout math).

- [ ] **Step 3.23: Lint + test.** `make lint` clean; `go test ./...` green.

- [ ] **Step 3.24: Commit.**
    ```bash
    git add internal/tui/label_cache.go internal/tui/label_cache_test.go internal/tui/suggest_render.go internal/tui/suggest_render_test.go internal/tui/model.go internal/tui/messages.go internal/tui/input.go internal/tui/inputs_render.go internal/tui/sse_update.go internal/tui/detail_render.go internal/tui/snapshot_test.go internal/tui/testdata/golden/
    git commit -m "feat(tui): project label cache + + / - autocomplete + SSE invalidation

Plan 8 commit 3: Model-keyed projectLabels cache with dispatch-time gen
stamping (entry.gen = newGen + fetching = true BEFORE the HTTP request).
Acceptance check on response: gen >= cache.gen AND pid == targetPID.
Right-anchored vertical suggestion menu via overlayAtCorner placement
helper. + sources from project cache, - from dm.issue.Labels. Prefix
filter (case-insensitive); count-desc / label-asc sort. Menu height
counts against tab/body budget. SSE issue.labeled / issue.unlabeled
events invalidate the suggestion cache distinctly from list/detail
refetch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- All four commit-3 hard invariants pass (dispatch-time stamping, stale-gen drop, mismatched-pid drop, SSE invalidation).
- Suggestion menu renders for `+` and `-` with correct sources.
- Layout doesn't lie about scroll overflow when menu is open.
- All hard invariants hold.

**Risks:** Medium-high. Cache routing has subtle race conditions; the dispatch-time gen stamp is the load-bearing fix. Menu placement via `overlayAtCorner` is new infrastructure — verify ANSI-aware row/col splicing handles styled background correctly. SSE invalidation must NOT also trigger list/detail refetch from this code path (the existing SSE handler does that already; we're only adding the labels-cache invalidation).

---

## Commit 4 — New-issue multi-field modal form; drop M3.5c inline row + M4 post-create chain

**Goal:** Replace `inputNewIssueRow` (the inline title row at the top of the table) and `inputBodyEditPostCreate` (the auto-opened post-create body editor) with a single multi-field modal form that captures Title + Body + Labels + Owner in one shot. `n` opens it; `ctrl+s` commits with all fields.

**Files:**
- Modify: `internal/tui/input.go` — drop `inputNewIssueRow`, `inputBodyEditPostCreate`, their constructors; add `inputNewIssueForm` + `newNewIssueForm()`; add to `isCenteredForm()`
- Modify: `internal/tui/inputs_render.go` — flesh out form rendering for `inputNewIssueForm` (4 fields, footer hint with body-only ctrl+e)
- Modify: `internal/tui/model.go` — `routeDetailFormKey` recognizes `n` from list view; `routeFormMutation` branches new-issue routing through list create handling, NOT detail; `cancelInput` drops post-create branch; remove post-create chain in `routeMutation`; `editorKindFor` recognizes new-issue form's body field
- Modify: `internal/tui/list.go` — drop `dispatchCreateIssue`'s old 3-arg signature, replace with new 5-arg signature accepting labels + owner; drop new-issue-row code (`renderBodyWithNewIssueRow`, `applyPromptKey`'s `n` branch)
- Modify: `internal/tui/list_render.go` — drop `renderBodyWithNewIssueRow` and inline-row code paths
- Modify: `internal/tui/client_types.go` — extend `CreateIssueBody` with `Owner *string` and `Labels []string`
- Modify: `internal/tui/keymap.go` — `n` description updated for the new form
- Delete: tests `TestList_NewIssueRow_*` (5 tests in `list_filter_test.go`), `TestPostCreate_*` (3 tests in `form_test.go`), `TestForm_OpenBodyEditPostCreate_*`
- Delete: `internal/tui/testdata/golden/list-new-issue-row.txt`
- Create: `internal/tui/new_issue_form_test.go` — full new-issue-form coverage

**Invariants touched:**
- Form mutation routing (`routeFormMutation` gains a new branch — must preserve existing edit-body / comment behavior).
- Centered-form formGen (existing pattern; new form follows same staleness rules).
- Title whitespace preservation (legacy invariant — wire payload still sent untrimmed).

**Hard invariants this commit pins:**
- `TestNewIssueForm_MutationSuccessRoutesToList` (new-issue success goes through list create handling, NOT reclassified as detail).

### Steps

- [ ] **Step 4.1: Extend `CreateIssueBody`.** In `internal/tui/client_types.go`, add to the struct:
    ```go
    Owner  *string  `json:"owner,omitempty"`
    Labels []string `json:"labels,omitempty"`
    ```

- [ ] **Step 4.2: Add `inputNewIssueForm` kind.** In `input.go`:
    ```go
    const (
        // ... existing kinds
        inputNewIssueForm  // list `n` — multi-field modal: Title/Body/Labels/Owner
    )
    ```
    Add to `isCenteredForm()`.

- [ ] **Step 4.3: Constructor.** Add `newNewIssueForm() inputState`:
    ```go
    func newNewIssueForm() inputState {
        ti := textinput.New(); ti.Prompt = ""
        body := textarea.New(); body.ShowLineNumbers = false; body.Prompt = ""
        labels := textinput.New(); labels.Prompt = ""
        owner := textinput.New(); owner.Prompt = ""
        // Blur all, focus field 0:
        body.Blur(); labels.Blur(); owner.Blur()
        ti.Focus()
        return inputState{
            kind:  inputNewIssueForm,
            title: "new issue",
            fields: []inputField{
                {kind: fieldSingleLine, input: ti, label: "Title", required: true},
                {kind: fieldMultiLine,  area: body, label: "Body"},
                {kind: fieldSingleLine, input: labels, label: "Labels"},
                {kind: fieldSingleLine, input: owner, label: "Owner"},
            },
        }
    }
    ```

- [ ] **Step 4.4: Tab cycling.** In `input.go::updateForm` (or wherever the existing form key handler lives), handle `tea.KeyTab` and `tea.KeyShiftTab`:
    ```go
    case tea.KeyTab:
        s.fields[s.active].blur()
        s.active = (s.active + 1) % len(s.fields)
        s.fields[s.active].focus()
        return s, actionNone
    case tea.KeyShiftTab:
        s.fields[s.active].blur()
        s.active = (s.active - 1 + len(s.fields)) % len(s.fields)
        s.fields[s.active].focus()
        return s, actionNone
    ```
    `inputField.focus()` and `inputField.blur()` are existing helpers (un-nolint them).

- [ ] **Step 4.5: Enter-in-single-line advances field.** When `enter` is pressed and `s.active != bodyFieldIndex`, treat it as `tab` (advance to next field) — does NOT commit. `enter` in body field stays as newline insertion (delegated to textarea). `ctrl+s` is the only commit path.

- [ ] **Step 4.6: ctrl+e gating.** In `updateForm`, allow `tea.KeyCtrlE` only when `s.active == 1` (body). For other fields, swallow silently. The existing M4 ctrl+e routing already calls `editorCmd` — verify `editorKindFor` recognizes `inputNewIssueForm` (returns `"create"` or just `""` since the body content is what matters).

- [ ] **Step 4.7: Drop `n` -> inline row.** In `list.go::applyPromptKey`, the `n` case currently returns `openInputCmd(inputNewIssueRow)`. Replace with `openInputCmd(inputNewIssueForm)` (the open-handler in `model.go::openInput` opens the centered form via `Model.input = newNewIssueForm()`). Preserve the all-projects gate (`if scope.allProjects { return lm, toastHint(...) }`).

- [ ] **Step 4.8: Add `inputNewIssueForm` open path.** In `model.go::openInput`:
    ```go
    case kind == inputNewIssueForm:
        m.nextFormGen++
        s := newNewIssueForm()
        s.formGen = m.nextFormGen
        m.input = s
        return m
    ```

- [ ] **Step 4.9: Write the failing routing test.** In `new_issue_form_test.go`:
    ```go
    func TestNewIssueForm_MutationSuccessRoutesToList(t *testing.T) {
        // m := newIssueFormFixture()  // form open, all fields populated
        // mut := mutationDoneMsg{origin: "form", kind: "create", resp: &MutationResp{Issue: &Issue{Number: 99}}}
        // out, _ := m.routeFormMutation(mut)
        // nm := out.(Model)
        // require.Equal(t, inputNone, nm.input.kind, "form should close on success")
        // require.Equal(t, int64(99), nm.list.selectedNumber, "list should select new issue")
        // require.NotEqual(t, viewDetail, nm.view, "must NOT auto-open detail")
    }
    ```
    Run; expected: FAIL.

- [ ] **Step 4.10: Implement form mutation routing.** In `model.go::routeFormMutation`:
    ```go
    func (m Model) routeFormMutation(mut mutationDoneMsg) (Model, tea.Cmd) {
        // Branch 0 (5a will add): inputFilterForm — committed via dedicated path, not here.
        // Branch 1: inputNewIssueForm — clear form, route through list create handling.
        if m.input.kind == inputNewIssueForm {
            m.input = inputState{}
            if mut.err != nil {
                // Re-open form with err? Or surface as toast? Per design: form stays open with err.
                // For success, route to list:
                return m, nil  // err path: leave form open
            }
            // Success: seed selectedNumber, refetch list. Do NOT reclassify as detail.
            m.list, _ = m.list.applyMutation(mutationDoneMsg{
                origin: "list", kind: "create", resp: mut.resp,
            }, m.api, m.scope)
            return m, m.list.refetchCmd(m.api, m.scope)
        }
        // Branch 2 (existing): inputBodyEditForm / inputCommentForm — reclassify as detail.
        // ... existing logic
    }
    ```
    Re-run test; expected: PASS.

- [ ] **Step 4.11: Remove post-create chain.** In `model.go::routeMutation` (around model.go:306), find the `if isCreateSuccess(mut)` branch that calls `openBodyEditPostCreate`. Delete the block. After this, a successful list-create no longer auto-opens a body editor.

- [ ] **Step 4.12: Drop `inputNewIssueRow` and `inputBodyEditPostCreate`.** In `input.go`, delete the enum values, `newNewIssueRow()`, `newBodyEditPostCreate()`, and the `isCenteredForm()` entry for `inputBodyEditPostCreate`. In `list.go` and `list_render.go`, delete `renderBodyWithNewIssueRow` and any callers. In `model.go`, delete `openBodyEditPostCreate`, `openDetailFromTarget`, `isCreateSuccess`, and the related branches.

- [ ] **Step 4.13: Verify no lingering references.**
    ```bash
    rg -n 'inputNewIssueRow|inputBodyEditPostCreate|renderBodyWithNewIssueRow|openBodyEditPostCreate|openDetailFromTarget' internal/tui/
    ```
    Should return zero matches.

- [ ] **Step 4.14: Delete dead tests.** Remove `TestList_NewIssueRow_*` (5 tests) from `list_filter_test.go`. Remove `TestPostCreate_*` (3 tests) and `TestForm_OpenBodyEditPostCreate_*` from `form_test.go`. Remove `TestSnapshot_List_NewIssueRow` and the golden file `list-new-issue-row.txt`.

- [ ] **Step 4.15: Extend `dispatchCreateIssue` signature.** In `list.go`:
    ```go
    func (lm listModel) dispatchCreateIssue(
        api listAPI, sc scope,
        title, body string, labels []string, owner *string,
    ) (listModel, tea.Cmd) {
        // ... build CreateIssueBody{Title: title, Body: body, Labels: labels, Owner: owner, Actor: lm.actor}
    }
    ```

- [ ] **Step 4.16: Form commit handler.** In `model.go::commitInput` (or wherever `actionCommit` lands for centered forms), when `m.input.kind == inputNewIssueForm`:
    ```go
    title := m.input.fields[0].input.Value()
    body := m.input.fields[1].area.Value()
    labelsBuf := m.input.fields[2].input.Value()
    ownerBuf := m.input.fields[3].input.Value()
    if strings.TrimSpace(title) == "" {
        m.input.err = "title is required"
        return m, nil
    }
    labels := normalizeLabels(labelsBuf)
    owner := normalizeOwner(ownerBuf)  // nil if whitespace-only
    m.input.saving = true
    return m, m.dispatchFormCreate(title, body, labels, owner)
    ```
    `normalizeLabels` = comma-split, TrimSpace per token, drop empty. `normalizeOwner` = TrimSpace; nil if empty after trim, else `&trimmed`.

- [ ] **Step 4.17: Tests for the form.** In `new_issue_form_test.go`:
    - `TestNewIssueForm_OpensOnNKey_ListView`.
    - `TestNewIssueForm_AllProjectsScopeIsNoOp` (toast hint instead of opening form).
    - `TestNewIssueForm_ConstructorBlursAllFieldsFocusesField0`.
    - `TestNewIssueForm_TabCyclesFieldsWithWrap`.
    - `TestNewIssueForm_ShiftTabReverseCyclesWithWrap`.
    - `TestNewIssueForm_EnterInSingleLineAdvancesField`.
    - `TestNewIssueForm_EnterInBodyInsertsNewline`.
    - `TestNewIssueForm_CtrlSEmptyTitleSetsErrNoDispatch`.
    - `TestNewIssueForm_CtrlSTitleOnly_DispatchesWithMinimalPayload`.
    - `TestNewIssueForm_CtrlSAllFields_NormalizedPayload` (whitespace owner omitted, empty labels dropped, title sent untrimmed).
    - `TestNewIssueForm_CtrlEOnlyWhenBodyFocused`.
    - `TestNewIssueForm_StaleEditorReturnDropped` (formGen mismatch; reuses existing pattern).
    - `TestNewIssueForm_MutationFailureLeavesFormOpenWithErr`.
    - `TestNewIssueForm_EscDiscardsAndReturnsToList` (no auto-detail-open).

- [ ] **Step 4.18: Snapshot.** New `TestSnapshot_NewIssueForm_AllFields` — fixture with all fields populated, asserts the rendered modal layout.

- [ ] **Step 4.19: Negative tests.**
    - `TestNoLingeringInlineRowReferences` — `rg -c inputNewIssueRow internal/tui/` should return 0.
    - `TestNoLingeringPostCreateChain` — `rg -c openBodyEditPostCreate internal/tui/` should return 0.
    Implement as Go tests that read source files via `os.ReadFile` and grep, OR as a shell-script invocation in CI. Recommend the shell route for simplicity.

- [ ] **Step 4.20: Lint + test.** `make lint` clean; `go test ./...` green.

- [ ] **Step 4.21: Roborev fix checkpoint.** Run `roborev fix --open --list`. Address any findings. Then commit.

- [ ] **Step 4.22: Commit.**
    ```bash
    git add -A
    git commit -m "feat(tui): new-issue multi-field modal; drop inline row + post-create chain

Plan 8 commit 4: replaces M3.5c inline title row and M4 post-create
body editor with a single centered modal collecting Title (required) +
Body + Labels (comma-separated) + Owner (optional) in one go.

- New inputNewIssueForm kind in the centered-form family.
- Tab cycles fields with wrap; ctrl+s commits from any field; esc
  discards and returns to list (no auto-detail-open); ctrl+e only
  when body field focused.
- Title required (TrimSpace empty-check); whitespace owner omitted;
  empty labels dropped; payload sent raw (no display sanitization on
  the wire).
- Form mutation success routes through list create handling
  (selectedNumber seeded, refetch dispatched), NOT reclassified as
  detail. Failure leaves form open with err and saving=false.
- Drops: inputNewIssueRow, renderBodyWithNewIssueRow,
  inputBodyEditPostCreate, post-create chain in routeMutation,
  openDetailFromTarget, all related tests.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `n` opens the multi-field modal; `inputNewIssueRow` no longer exists in the codebase.
- `inputBodyEditPostCreate` no longer exists; post-create chain removed from `routeMutation`.
- Form mutation success routes through list create handling (`TestNewIssueForm_MutationSuccessRoutesToList`).
- All form interaction tests pass (tab cycle, enter advance, ctrl+s gates, ctrl+e gating, esc behavior, normalization).
- All hard invariants hold.

**Risks:** Medium-high. Large deletion surface; easy to miss a reference. The negative grep tests are mandatory. Form mutation routing must NOT reclassify new-issue success as detail (would auto-open detail and surprise users). The `dispatchCreateIssue` signature change ripples through any callers — `rg -n 'dispatchCreateIssue' internal/tui/` to find them all.

---

## Commit 5a — Filter modal `f` (Status/Owner/Search axes); drop `o` key

**Goal:** Add the centered filter modal accessible via `f`. Three axes in v1: Status (tri-state radio), Owner (textinput), Search (textinput). Drop the `o` keybinding (subsumed by modal). Keep `s`/`/`/`c` per Q6 hybrid (cheap paths for common gestures).

**Files:**
- Modify: `internal/tui/input.go` — add `inputFilterForm` kind + `newFilterForm(current ListFilter) inputState`; add to `isCenteredForm()`
- Modify: `internal/tui/inputs_render.go` — render shape for filter form (3 fields, status radio)
- Modify: `internal/tui/model.go` — `routeFormMutation` branches `inputFilterForm` BEFORE the saving/mutation handling; `commitFilterForm(form inputState) (Model, tea.Cmd)` dedicated path; `routeDetailFormKey` (or list-view key handler) recognizes `f`
- Modify: `internal/tui/list.go` — drop `o` key handling; drop `newOwnerBar` references
- Modify: `internal/tui/keymap.go` — drop `o` keymap entry; add `f` keymap entry
- Modify: `internal/tui/help.go` — refresh under new keymap (drop `o`, add `f`)
- Modify: `internal/tui/list_render.go` — `listFooterItems` and `listFooterItemsFor` swap `o owner` for `f filter`
- Create: `internal/tui/filter_form_test.go`
- Update: list-view snapshot goldens (footer help text changed)

**Invariants touched:**
- Form mutation routing (`routeFormMutation` branches `inputFilterForm` first to bypass mutation logic).
- preFilter snapshot pattern (filter form's `s.preFilter` is populated for esc-restore).

**Hard invariants this commit pins:**
- `TestFilterForm_CommitUsesDedicatedPath` (commit goes through `commitFilterForm`, NOT `applyLiveBarFilter`).

### Steps

- [ ] **Step 5a.1: Add `inputFilterForm` kind.** In `input.go`:
    ```go
    const (
        // ...
        inputFilterForm  // list `f` — multi-axis filter modal
    )
    ```
    Add to `isCenteredForm()`.

- [ ] **Step 5a.2: Constructor.** `newFilterForm(current ListFilter) inputState`:
    ```go
    func newFilterForm(current ListFilter) inputState {
        // Field 0: Status (custom field — not a textinput). Use a dedicated
        // helper renderStatusRadio; on Update, ←/→/space cycles the value.
        // Field 1: Owner — textinput pre-filled from current.Owner.
        // Field 2: Search — textinput pre-filled from current.Search.
        // preFilter := current  (snapshot for esc restore)
        // ...
    }
    ```
    Status field doesn't fit the existing `fieldSingleLine`/`fieldMultiLine` shapes. Add `fieldRadio` kind and a `radio` field on `inputField` (slice of choices + currentIndex).

- [ ] **Step 5a.3: Render.** In `inputs_render.go`, render the filter form similarly to the new-issue form but with the radio rendering:
    ```
    Status
    ◯ all   ◉ open   ◯ closed
    ```
    Bullets via `◉`/`◯` glyphs; fall back to `[X]`/`[ ]` under `KATA_COLOR_MODE=none`.

- [ ] **Step 5a.4: Status update routing.** When status field is active, `←`/`→` cycle the radio value; `space` cycles to next; `tab` advances to Owner.

- [ ] **Step 5a.5: Add `f` keymap.** In `keymap.go`:
    ```go
    type keymap struct {
        // ...
        FilterForm key
    }
    // newKeymap:
    FilterForm: key{Keys: []string{"f"}, Help: "filter"},
    ```
    Drop the `FilterOwner` field and entry (the `o` key).

- [ ] **Step 5a.6: Wire `f` in list.go.** In the list-view key router, handle `km.FilterForm.matches(msg)` → emit `openInputCmd(inputFilterForm)`.

- [ ] **Step 5a.7: Add filter-form open path.** In `model.go::openInput`:
    ```go
    case kind == inputFilterForm:
        m.nextFormGen++
        s := newFilterForm(m.list.filter)
        s.formGen = m.nextFormGen
        m.input = s
        return m
    ```

- [ ] **Step 5a.8: Write the failing commit-path test.** In `filter_form_test.go`:
    ```go
    func TestFilterForm_CommitUsesDedicatedPath(t *testing.T) {
        // m := buildFilterFormFixture()  // form open with status=open, owner=alice, search=login
        // // applyLiveBarFilter would only set ONE field; commitFilterForm sets all.
        // out, cmd := m.commitInput()  // ctrl+s analog
        // nm := out.(Model)
        // require.Equal(t, "open", nm.list.filter.Status)
        // require.Equal(t, "alice", nm.list.filter.Owner)
        // require.Equal(t, "login", nm.list.filter.Search)
        // require.NotNil(t, cmd, "must dispatch refetch")
    }
    ```
    Run; expected: FAIL.

- [ ] **Step 5a.9: Implement `commitFilterForm`.** In `model.go`:
    ```go
    func (m Model) commitFilterForm(form inputState) (Model, tea.Cmd) {
        m.list.filter = ListFilter{
            Status: form.fields[0].radio.value(),
            Owner:  strings.TrimSpace(form.fields[1].input.Value()),
            Search: strings.TrimSpace(form.fields[2].input.Value()),
        }
        m.list.selectedNumber = 0
        m.list = m.list.clampCursorToFilter()
        m.list.status = ""
        m.input = inputState{}
        return m, m.list.refetchCmd(m.api, m.scope)
    }
    ```

- [ ] **Step 5a.10: Branch in `routeFormMutation`.** Before the saving/mutation-form handling branch, add:
    ```go
    if m.input.kind == inputFilterForm {
        // Filter form's commit doesn't go through mutation routing — it lands
        // here as actionCommit at the input layer instead.
        return m, nil  // shouldn't reach this branch via mutation; safety net.
    }
    ```
    Actually the filter form's `actionCommit` is intercepted before `routeFormMutation` ever sees it (in `commitInput`). The branch in `routeFormMutation` is a safety net for stray messages — it just no-ops.

- [ ] **Step 5a.11: Add `actionCommit` filter branch in `commitInput`.** In `model.go::commitInput` (or wherever centered-form actionCommit lands):
    ```go
    if m.input.kind == inputFilterForm {
        return m.commitFilterForm(m.input)
    }
    // ... existing new-issue / body-edit / comment branches
    ```

- [ ] **Step 5a.12: Implement `ctrl+r` reset.** In `updateForm`, when `m.input.kind == inputFilterForm`, handle `tea.KeyCtrlR`:
    ```go
    case tea.KeyCtrlR:
        // Reset all fields to zero values; preFilter intact.
        s.fields[0].radio.set("all")
        s.fields[1].input.SetValue("")
        s.fields[2].input.SetValue("")
        return s, actionNone
    ```

- [ ] **Step 5a.13: Implement `esc` restore.** Already in `cancelInput`: when `s.preFilter != ListFilter{}`, restore `lm.filter = s.preFilter`. Verify the existing path handles filter form too.

- [ ] **Step 5a.14: Drop `o` key.** In `list.go`, remove the `o` key branch in `applyPromptKey` (and any references to `inputOwnerBar`'s opening flow). The `inputOwnerBar` kind itself stays (it's still used by the `o` field of the modal? — no, the modal uses textinput directly, not the bar). Verify with `rg -n inputOwnerBar internal/tui/` — if no consumers remain, delete the kind too.

- [ ] **Step 5a.15: Update help + footer.** In `keymap.go`'s help map, drop the `o` entry. In `list_render.go::listFooterItems`, swap `{key: "o", desc: "owner"}` for `{key: "f", desc: "filter"}`.

- [ ] **Step 5a.16: Update list snapshot goldens.** Run `go test ./internal/tui/ -run TestSnapshot_List -update-goldens`. Diff each — only the footer line should change (`o owner` → `f filter`).

- [ ] **Step 5a.17: Filter-form tests.** In `filter_form_test.go`:
    - `TestFilterForm_OpensOnFKey` (list view).
    - `TestFilterForm_AllProjectsScopeStillRenders` (modal works in cross-project mode too — it's filter-only).
    - `TestFilterForm_TabCyclesThreeFields_WithWrap`.
    - `TestFilterForm_StatusFieldRadioCycle_LeftRightSpace`.
    - `TestFilterForm_CommitUsesDedicatedPath` (already drafted in step 5a.8).
    - `TestFilterForm_CommitZeroesSelectedNumberAndClampsCursor`.
    - `TestFilterForm_CommitClearsLmStatus`.
    - `TestFilterForm_CommitDispatchesRefetch`.
    - `TestFilterForm_CtrlRResetsFieldsOnly_PreFilterIntact`.
    - `TestFilterForm_EscRestoresPreFilter`.
    - `TestFilterForm_RouteFormMutationBranchesFirst_NoSavingTrue`.
    - `TestKeymap_OKeyGone` (`km.FilterOwner` no longer exists OR doesn't match the `o` rune).
    - `TestHelpScreen_NoLongerMentionsO`.

- [ ] **Step 5a.18: Snapshot.** New `TestSnapshot_FilterForm_AllAxes` — fixture with status=open, owner=alice, search=login filled. Snapshot the rendered modal.

- [ ] **Step 5a.19: Snapshot.** New `TestSnapshot_List_WithFilterChipsFromModal` — apply filter via modal, assert chip strip in chrome reflects all axes.

- [ ] **Step 5a.20: Lint + test.** `make lint` clean; `go test ./...` green.

- [ ] **Step 5a.21: Commit.**
    ```bash
    git add -A
    git commit -m "feat(tui): filter modal f (Status/Owner/Search axes); drop o key

Plan 8 commit 5a: centered filter modal accessible via f. Three axes
in v1: Status (tri-state radio: all/open/closed), Owner (textinput),
Search (textinput). preFilter snapshot for esc-restore.

- Dedicated commitFilterForm path: sets full ListFilter from all
  three fields, zeroes selectedNumber, clamps cursor, clears
  lm.status, dispatches refetch. Does NOT call applyLiveBarFilter
  (that mirrors a single field).
- ctrl+r resets fields only; preFilter intact for esc.
- esc restores ListFilter to its at-open snapshot.
- routeFormMutation branches inputFilterForm first to skip the
  mutation-form / saving handling.
- o key dropped from keymap (subsumed by modal); s/ /c kept per
  Q6 hybrid (cheap paths for common gestures).
- Footer help: 'o owner' -> 'f filter'.

Labels axis lands in commit 5b once daemon support is in place.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `f` opens the filter modal; `o` keybinding gone (`TestKeymap_OKeyGone` passes).
- Modal commit goes through `commitFilterForm`, not `applyLiveBarFilter` (`TestFilterForm_CommitUsesDedicatedPath` passes).
- All 13 filter-form unit tests pass.
- Snapshot goldens updated: list footer text reflects new keymap.
- All hard invariants hold.

**Risks:** Medium. Adding a new field kind (`fieldRadio`) is a small architectural extension — make sure it integrates cleanly with `inputField.value()`/`focus()`/`blur()`. Filter form's `actionCommit` interception in `commitInput` must be the first branch so it doesn't accidentally hit the mutation-form path. The `o` key removal ripples into help text and tests.

---

## Commit 5b — List-side per-issue labels (daemon work) + Labels axis on filter modal

**Goal:** Add daemon-side `LabelsByIssues` batch query + `IssueOut` wire DTO so list responses carry per-issue labels. TUI list decode populates `Issue.Labels`; filter modal gains a Labels axis with multi-select via the project label cache (commit 3); chrome chip strip renders label chips; `filteredIssues` fast-path includes `Labels` in the early-return condition; `matchesFilter` implements any-of label semantics.

**Files:**
- Create: `internal/db/queries_labels_by_issues.go` — `LabelsByIssues` batch query
- Create: `internal/db/queries_labels_by_issues_test.go`
- Modify: `internal/db/types.go` — no change (existing `IssueLabel` covers the row shape)
- Modify: `internal/api/types.go` — `IssueOut` struct (embeds `db.Issue` + `Labels []string`); `ListIssuesResponse.Body.Issues` becomes `[]IssueOut`
- Modify: `internal/daemon/handlers_issues.go` — list handler runs `ListIssues` then `LabelsByIssues`, builds `[]IssueOut`
- Modify: `internal/daemon/handlers_issues_test.go` — list endpoint asserts label hydration
- Modify: `internal/tui/client_types.go` — no change (Issue.Labels tag already correct from commit 1)
- Modify: `internal/tui/client.go` — `listIssues` decodes new wire shape (auto-populates Issue.Labels via tag)
- Modify: `internal/tui/list.go` — `matchesFilter` implements any-of label semantics
- Modify: `internal/tui/list_render.go` — `filteredIssues` fast-path includes `len(f.Labels) == 0`; `renderChips` adds label chips
- Modify: `internal/tui/inputs_render.go` — filter form gains Labels field (chip-input style); use suggestion menu from commit 3
- Modify: `internal/tui/input.go` — `newFilterForm` takes a 4th field for Labels; `commitFilterForm` reads selected labels
- Test: append to `filter_form_test.go`

**Invariants touched:**
- List decode (Issue.Labels populates from list response now, in addition to detail).
- Filter behavior (any-of semantics for labels — new behavior, must be unambiguous).

**Hard invariants this commit pins:**
- `TestFilteredIssues_FastPathIncludesLabels`.
- `TestMatchesFilter_LabelsAnyOfSemantics`.

### Steps

- [ ] **Step 5b.1: Write failing daemon DB test.** In `internal/db/queries_labels_by_issues_test.go`:
    ```go
    func TestLabelsByIssues_EmptyInput_ReturnsEmptyMap(t *testing.T) {
        // Empty issueIDs slice → empty map, no SQL roundtrip.
    }
    func TestLabelsByIssues_ConstrainedByProjectID(t *testing.T) {
        // Two projects with same issueID — only the queried project's labels return.
    }
    func TestLabelsByIssues_OrdersByIssueThenLabel(t *testing.T) {
        // Mixed-issue mixed-label rows; verify per-issue slice is alphabetical.
    }
    func TestLabelsByIssues_MultiIssue_HappyPath(t *testing.T) {
        // 3 issues, 7 labels total; verify map structure.
    }
    ```
    Run; expected: FAIL (function doesn't exist).

- [ ] **Step 5b.2: Implement `LabelsByIssues`.** In `internal/db/queries_labels_by_issues.go`:
    ```go
    func (q *Queries) LabelsByIssues(ctx context.Context, projectID int64, issueIDs []int64) (map[int64][]string, error) {
        if len(issueIDs) == 0 {
            return map[int64][]string{}, nil
        }
        // Build SQL: SELECT issue_id, label FROM issue_labels
        //            WHERE project_id = ? AND issue_id IN (?, ?, ?, ...)
        //            ORDER BY issue_id ASC, label ASC
        // Bind projectID + issueIDs as args.
        // Scan into map[int64][]string.
    }
    ```
    `issue_labels` table needs a `project_id` column — verify it exists (it should via FK to issues). If it doesn't, the query goes via JOIN: `SELECT il.issue_id, il.label FROM issue_labels il JOIN issues i ON il.issue_id = i.id WHERE i.project_id = ? AND i.id IN (...)`.

- [ ] **Step 5b.3: Verify schema.** `rg -n 'CREATE TABLE issue_labels' internal/db/migrations/` to confirm whether `project_id` is on the table. Adapt the query accordingly.

- [ ] **Step 5b.4: Run DB tests.** All 4 should pass.

- [ ] **Step 5b.5: Add `IssueOut`.** In `internal/api/types.go`:
    ```go
    type IssueOut struct {
        db.Issue
        Labels []string `json:"labels,omitempty"`
    }
    ```
    Update `ListIssuesResponse.Body.Issues` from `[]db.Issue` to `[]IssueOut`.

- [ ] **Step 5b.6: Wire daemon list handler.** In `internal/daemon/handlers_issues.go`'s list handler (~line 110-115):
    ```go
    issues, err := cfg.DB.ListIssues(ctx, ...)
    if err != nil { return nil, ... }
    ids := make([]int64, len(issues))
    for i, iss := range issues { ids[i] = iss.ID }
    labelsByID, err := cfg.DB.LabelsByIssues(ctx, in.ProjectID, ids)
    if err != nil { return nil, ... }
    out := &api.ListIssuesResponse{}
    out.Body.Issues = make([]api.IssueOut, len(issues))
    for i, iss := range issues {
        out.Body.Issues[i] = api.IssueOut{Issue: iss, Labels: labelsByID[iss.ID]}
    }
    ```

- [ ] **Step 5b.7: Daemon integration test.** In `handlers_issues_test.go`, add `TestListIssues_HydratesLabels`:
    ```go
    func TestListIssues_HydratesLabels(t *testing.T) {
        // Create 2 issues with labels [bug, prio-1] and [enhancement] respectively.
        // GET /issues; assert response body has Issues[0].Labels == [bug, prio-1] and Issues[1].Labels == [enhancement].
    }
    ```

- [ ] **Step 5b.8: TUI list decode.** Already automatic — `Issue.Labels` has the right JSON tag from commit 1, and `Client.listIssues` JSON-decodes into `[]Issue`. Verify by adding `TestListIssues_TUIDecodePopulatesLabels` to `client_test.go`: stand up a test server returning the new wire shape, call `client.ListIssues`, assert `result[0].Labels` is populated.

- [ ] **Step 5b.9: Write failing fast-path test.** In `list_test.go` or `list_filter_test.go`:
    ```go
    func TestFilteredIssues_FastPathIncludesLabels(t *testing.T) {
        // f := ListFilter{Labels: []string{"bug"}}
        // // Even though Status/Owner/Author/Search are empty, the fast-path
        // // must NOT early-return — it must apply the label filter.
        // issues := []Issue{{Number: 1, Labels: []string{"bug"}}, {Number: 2, Labels: []string{"feature"}}}
        // got := filteredIssues(issues, f)
        // require.Len(t, got, 1)
        // require.Equal(t, int64(1), got[0].Number)
    }
    ```
    Run; expected: FAIL (current fast-path early-returns when Status+Owner+Author+Search are empty, ignoring Labels).

- [ ] **Step 5b.10: Fix fast-path.** In `list_render.go::filteredIssues`, find the early-return condition:
    ```go
    if f.Status == "" && f.Owner == "" && f.Author == "" && f.Search == "" {
        return issues
    }
    ```
    Add `&& len(f.Labels) == 0` to the condition.

- [ ] **Step 5b.11: Write any-of semantics test.** `TestMatchesFilter_LabelsAnyOfSemantics`:
    ```go
    // issue.Labels = [bug, prio-1]
    // matchesFilter(issue, ListFilter{Labels: []string{"bug"}}) == true
    // matchesFilter(issue, ListFilter{Labels: []string{"bug", "foo"}}) == true
    // matchesFilter(issue, ListFilter{Labels: []string{"foo"}}) == false
    // matchesFilter(issue, ListFilter{Labels: []string{}}) == true (no filter)
    ```

- [ ] **Step 5b.12: Implement label any-of in `matchesFilter`.** In `list.go` (or `list_render.go`):
    ```go
    if len(f.Labels) > 0 {
        attached := map[string]bool{}
        for _, l := range iss.Labels { attached[l] = true }
        anyMatch := false
        for _, l := range f.Labels {
            if attached[l] { anyMatch = true; break }
        }
        if !anyMatch { return false }
    }
    ```

- [ ] **Step 5b.13: Add Labels chips to `renderChips`.** Find the `renderChips(filter ListFilter) string` helper. Remove the `// label chips intentionally omitted (wire doesn't carry labels yet)` comment. Add chip rendering for `f.Labels`:
    ```go
    for _, l := range f.Labels {
        chips = append(chips, chipStyle.Render("["+l+"]"))
    }
    ```

- [ ] **Step 5b.14: Add Labels field to filter form.** In `input.go::newFilterForm`, add a 4th field. Use a textinput with a chip-input render (renders selected labels as removable chips above the input cursor). For v1 (no chip-removal UX): comma-separated text mirrors the new-issue form's Labels field shape. Pre-fills from `current.Labels`.

- [ ] **Step 5b.15: All-projects gate for label suggestion.** When `m.scope.allProjects`, the Labels field uses free-typed text only (no suggestion menu). Cache lookup `m.projectLabels.byProject[?]` is ambiguous in cross-project scope — there's no single "project" to source from. Filter still applies via per-issue `Labels` from list decode.

- [ ] **Step 5b.16: Wire suggestion menu for Labels field.** When the Labels field is focused AND `!m.scope.allProjects`, render the suggestion menu (from commit 3) above the field. Use the same multi-select selection UX (selected labels render as chips inside the field).

- [ ] **Step 5b.17: Update `commitFilterForm`.** In `commitFilterForm`:
    ```go
    m.list.filter = ListFilter{
        // ... existing fields
        Labels: parseFormLabels(form.fields[3].input.Value()),
    }
    ```
    `parseFormLabels` reuses `normalizeLabels` from commit 4 (comma-split, TrimSpace, drop empty).

- [ ] **Step 5b.18: Tests.** Append to `filter_form_test.go`:
    - `TestFilterForm_LabelsField_AnyOfSemantics_AppliesViaCommit`.
    - `TestFilterForm_LabelsField_SuggestionMenuDisabledInAllProjects`.
    - `TestFilterForm_LabelsField_FreeTypedInAllProjectsScope`.
    - `TestFilteredIssues_FastPathIncludesLabels` (already drafted).
    - `TestMatchesFilter_LabelsAnyOfSemantics` (already drafted).
    - `TestRenderChips_IncludesLabelChips`.

- [ ] **Step 5b.19: Snapshot.** New `TestSnapshot_FilterForm_WithLabelsAxis` and `TestSnapshot_FilterForm_LabelsDisabledAllProjects`. Update `TestSnapshot_List_WithFilterChipsFromModal` to include label chips.

- [ ] **Step 5b.20: Lint + test.** `make lint` clean; `go test ./...` green (across all packages — daemon test included).

- [ ] **Step 5b.21: Roborev fix checkpoint.** Run `roborev fix --open --list`. Address daemon-wire findings carefully — wire shape changes are easy to break agents.

- [ ] **Step 5b.22: Commit.**
    ```bash
    git add -A
    git commit -m "feat(daemon+tui): list-side per-issue labels + Labels filter axis

Plan 8 commit 5b: lights up the Labels axis on the filter modal. Two
ends:

Daemon:
- New db.LabelsByIssues(ctx, projectID, issueIDs) batch query;
  returns map[int64][]string ordered alphabetically per issue. Empty
  input → empty map (no SQL roundtrip). Constrained by both
  project_id and id IN (...) for cross-project safety.
- New api.IssueOut DTO embeds db.Issue + Labels []string. Don't
  mutate db.Issue — keep persistence and wire types separate.
- ListIssuesResponse.Body.Issues becomes []IssueOut. List handler
  runs ListIssues then LabelsByIssues, builds []IssueOut with per-
  issue label slices.

TUI:
- Issue.Labels now populates from list decode (already had the json
  tag from commit 1); detail decode still manually copies from
  body.Labels.
- filteredIssues fast-path includes len(f.Labels) == 0 in the
  early-return; matchesFilter implements any-of label semantics.
- renderChips renders label chips alongside status/owner.
- Filter form gains a 4th field (Labels, comma-separated text in
  v1) with suggestion menu in single-project scope; free-typed in
  all-projects (cache lookup ambiguous there).
- commitFilterForm reads the Labels field and applies via filter.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
    ```

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green (daemon + TUI).
- Daemon list endpoint returns labels per issue (verified by `TestListIssues_HydratesLabels`).
- TUI list decode populates `Issue.Labels` (verified by `TestListIssues_TUIDecodePopulatesLabels`).
- `filteredIssues` fast-path includes Labels (`TestFilteredIssues_FastPathIncludesLabels` passes).
- `matchesFilter` any-of semantics (`TestMatchesFilter_LabelsAnyOfSemantics` passes).
- Label chips render in chrome strip and filter form.
- All-projects scope falls back to free-typed labels (no suggestion menu).
- All hard invariants hold.

**Risks:** High. Wire-shape changes affect every consumer of `ListIssuesResponse` (TUI, CLI list command, agents). The CLI's `kata list` command should still work — it currently decodes `Issues []struct{ Number, Title, Status, Owner }` (loose anonymous decode). Verify no CLI test breaks. Daemon roborev review may find issues with the JOIN query under high-issue-count workloads — defer indexing optimizations to a follow-up unless egregious.

---

## Post-implementation cleanup

- [ ] Run `roborev fix --open --list`; address any open findings; commit.
- [ ] Update `CLAUDE.md` if the keymap change (`o` → `f`) needs documenting.
- [ ] Remove any `nolint:unused` markers that no longer apply (e.g. `inputField.label` is used by the form now).
- [ ] Verify the design doc at `2026-05-02-kata-8-detail-rework-and-create-form-design.md` is still accurate; if any commit deviated, update the doc with a postscript.

## Open follow-ups (out of scope; documented in design doc)

- Author and `include_deleted` filter axes (need wire support).
- CLI `kata list` showing labels in human output.
- Owner autocomplete in new-issue form / filter modal (would need a `GET /owners` endpoint).
- Draft preservation on Esc for the new-issue form.
- Rich label chip input (vs comma-separated text) — would unify with the suggestion menu component.
