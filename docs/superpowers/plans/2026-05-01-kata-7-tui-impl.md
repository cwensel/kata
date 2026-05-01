# Plan 7 — TUI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Pair this with the design doc** at `2026-05-01-kata-7-tui-design-sprint.md`. The design doc holds the rationale, mockups, and locked decisions. This file is operational: per-milestone tasks, files to touch, tests to add, acceptance criteria.

## Standing directives

- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
- Run `make lint` (golangci-lint) and `go test ./...` before each commit. Zero warnings.
- Hard limits: ≤100 lines/function, cyclomatic complexity ≤8, 100-char lines.
- Each milestone (M0, M1, M2, M3a, M3b, M4, M5, M6) is a separate commit.
- Roborev-fix checkpoints after **M3b** (input plumbing replaced) and **M6** (split layout shipped). Run `roborev fix --open --list` before committing the milestone, address findings, and only then commit + close the reviews.
- Snapshot fixtures live under `internal/tui/testdata/golden/`. Use `-update-goldens` to regenerate after deliberate visual changes; review the diff before committing the new fixtures.

## Hard invariants (must hold through every milestone)

These are **tested-and-shipped** behaviors from Plan 6 that the redesign must not regress. Every milestone's acceptance criteria includes "all hard invariants still hold" — verified by running the existing test suite.

| Invariant | Owning code | Test that proves it |
|---|---|---|
| List rendering is viewported by terminal height | `windowIssues` in `list_render.go` | `TestEdge_ListViewport_KeepsCursorVisible` in `edge_test.go` |
| Cursor follows issue identity, not row index | `selectedNumber` + `applyFetched` in `list.go` | `TestEdge_IdentitySelection_FollowsIssueAcrossReorder` and `..._FallsBackWhenIssueDisappears` in `edge_test.go` |
| Stale list refetches dropped before clobbering | `dispatchKey` + `cacheKeysEqual` + `isStaleListFetch` in `messages.go`/`model.go` | `TestEdge_StaleRefetch_DroppedAfterFilterChange` and `..._DroppedAcrossScopeToggle` |
| Detail-fetch generation monotonic | `Model.nextGen`, `Model.handleJumpDetail`, `applyFetched`'s gen check | `TestModel_GenMonotonicAcrossJumpBackOpen`, `TestDetail_StaleFetch_DroppedAcrossJump` |
| Mutation routing to originating model after view-switch | `Model.routeMutation` | `TestEdge_ListMutation_CompletesAfterDetailOpen`, `..._DetailMutation_CompletesAfterPopToList` |
| reset_required refreshes open detail too | `Model.refetchOpenDetail` called from `handleResetRequired` | `TestRefetchOpenDetail_*`, `TestHandleResetRequired_DropsCacheAndShowsToast` |
| SSE invalidation refetches all four detail tabs + cross-project guard | `Model.maybeRefetchOpenDetail` | `TestHandleEventReceived_DetailViewRefetchesAllTabs`, `..._CrossProjectMismatch_NoRefetch` |
| Sanitization at every render boundary | `sanitizeForDisplay` calls in `list_render.go`, `detail_render.go`, `detail_tabs.go` | `TestSanitizeForDisplay_*`, `TestListView_SanitizesMaliciousTitle`, `TestDetailView_SanitizesMaliciousBody`, `TestCommentsTab_SanitizesMaliciousAuthorAndBody` |
| `--all-projects` and R-toggle gated until daemon ships cross-project list | `cmd/kata/tui_cmd.go` (no flag), `bootResolveScope` (no fallback), `handleScopeToggle` (toast no-op) | `TestTUI_CommandRegistered`, `TestBoot_UnboundCwd_LandsInEmptyState`, `TestScopeToggle_GatedNoOp`, `TestScopeToggle_RKeyDispatch_Gated` |
| Title whitespace preserved on create | `submitNewIssue` in `list.go` | `TestList_NewIssue_PreservesTitleWhitespace` |
| Comment-only sentinel strip + CRLF | `trimComments` in `editor.go` | `TestTrimComments_*` |
| Help-during-overlay sync via `populateCache` at top level | `Model.Update`'s `populateCache` call | `TestHelp_RefetchWhileOpen_KeepsListInSync`, `TestHelp_InitialFetchAfterScopeToggle_KeepsListInSync` |

If a milestone's work would require touching one of these, **port the test forward** rather than removing it. The invariants encode regressions worth keeping.

## Files to preserve (transport/state layer)

These files are **not** in scope for the redesign. Edit surgically only when a hard invariant requires it; never rewrite wholesale:

- `internal/tui/client.go` — typed daemon HTTP wrapper
- `internal/tui/client_types.go` — wire types
- `internal/tui/events_sse.go`, `events_sse_parse.go` — SSE consumer
- `internal/tui/messages.go` — message-type definitions (may add new types but don't reshape existing)
- `internal/tui/cache.go` — single-slot list cache
- `internal/daemonclient/` — daemon discovery + HTTP client construction

## Files in scope for redesign (chrome + input shell)

Render and input plumbing — these are fair game for restructuring:

- `internal/tui/theme.go` — color palette + style vars
- `internal/tui/list_render.go` — list view chrome (M1, M3a, M6)
- `internal/tui/list.go` — list view state + key handlers (M3a, M6)
- `internal/tui/detail.go` — detail view orchestration (M2, M3b, M6)
- `internal/tui/detail_render.go` — detail view chrome (M2, M6)
- `internal/tui/detail_tabs.go` — tab content rendering (M2)
- `internal/tui/detail_mutation.go`, `modal.go` — replaced by panel-local prompts (M3b)
- `internal/tui/detail_editor.go`, `editor.go` — kept; rewired into centered forms (M4)
- `internal/tui/help.go` — refreshed under new palette (M5)
- `internal/tui/scope.go` — empty-state + narrow-terminal hint render (M5)
- `internal/tui/model.go` — top-level routing + layout discriminator (M6)
- `internal/tui/run.go` — alt-screen + program options (probably unchanged)

New files this plan creates:

- `internal/tui/input.go` (M3a) — `inputState` + `inputField` + dispatch
- `internal/tui/inputs_render.go` (M3a, M3b, M4) — render shells for command bar / panel-local prompt / centered form
- `internal/tui/layout.go` (M6) — breakpoint detection + layout-mode discriminator
- `internal/tui/split_render.go` (M6) — split-pane composition

---

## M3a preflight: bubbles compatibility gate (BLOCKING)

> **Hard gate.** No M3a work begins until this is green. If `bubbles` doesn't work against the locked `bubbletea v1.3.10`, the entire input-shell plan needs re-evaluation.

- [ ] **Run** `go get github.com/charmbracelet/bubbles@latest` from the repo root.
- [ ] **Confirm** `go.mod` shows a `bubbles` line at a version that imports cleanly with `bubbletea v1.3.10`. If `go mod tidy` complains about a `bubbletea` version mismatch, downgrade `bubbles` until it's clean — do **not** upgrade `bubbletea` (that's a cross-cutting change, out of scope).
- [ ] **Smoke test** by writing a throwaway `cmd/kata/internal/tuitemp/main.go` that imports `github.com/charmbracelet/bubbles/textinput` and `.../textarea`, constructs each, and prints the rendered output. Run `go run ./cmd/kata/internal/tuitemp` and verify it prints something non-empty without panicking. Delete the throwaway file before committing.
- [ ] **Document** the chosen `bubbles` version in M3a's commit message. If the version had to be downgraded, note why.

If the smoke test panics or `go mod tidy` won't resolve, **stop**. Open a discussion with Wes about whether to upgrade `bubbletea` (cross-cutting), pin to a specific older `bubbles`, or roll our own input components. Do not proceed to M3a until this is resolved.

---

## Milestone M0 — Adopt roborev color palette

**Goal:** Replace the existing style vars in `theme.go` with the roborev palette (verbatim codes, semantic remap for kata). No layout change. The render shape stays identical; only the colors shift.

**Files:**
- Modify: `internal/tui/theme.go`

**Invariants touched:** None. Pure color change.

- [ ] **Step 1: Audit existing styles.** `grep -n "lipgloss.NewStyle\|AdaptiveColor" internal/tui/theme.go` to list every style var. Map each to the closest roborev equivalent (see "Color palette" §"Visual language" in the design doc). Where kata has a chip kata doesn't (deletedStyle), use `failStyle`'s red with `Faint(true)`.
- [ ] **Step 2: Replace AdaptiveColor codes** with the locked palette. Keep the Go variable names stable (`titleStyle`, `statusStyle`, `selectedStyle`, `openStyle`, `closedStyle`, `deletedStyle`, `helpStyle`, `helpKeyStyle`, `helpDescStyle`, `errorStyle`, `chipStyle`, `chipActive`, `tabActive`, `tabInactive`, `subtleStyle`, `toastStyle`).
- [ ] **Step 3: Add new vars** for panel borders: `panelActiveBorder = AdaptiveColor{Light:"125", Dark:"205"}` (magenta) and `panelInactiveBorder = AdaptiveColor{Light:"242", Dark:"246"}` (gray). Used by M1 onward; declare in M0 so the palette is complete.
- [ ] **Step 4: Verify color-mode logic.** `applyDefaultColorMode` already handles `KATA_COLOR_MODE`/`NO_COLOR`; the new palette should drop through it unchanged. Run `KATA_COLOR_MODE=none go test ./internal/tui/` to confirm `none` mode still strips foregrounds.
- [ ] **Step 5: Regenerate snapshot goldens.** `go test ./internal/tui/ -run TestSnapshot -update-goldens`. Diff each golden — the palette change should be invisible under `KATA_COLOR_MODE=none` (the snapshot mode), so most goldens should be byte-identical. If a golden changed, investigate before accepting.
- [ ] **Step 6: Lint + test.** `make lint` clean, `go test ./...` green, all hard invariants still hold.

**Acceptance criteria:**
- `golangci-lint run ./...` reports 0 issues.
- `go test ./...` passes.
- Snapshot goldens are unchanged or have been regenerated with a deliberate, reviewed diff.
- No new test failures in any package.

**Risks:** Low. If a snapshot diff appears unexpectedly, it's because `applyDefaultColorMode(io.Discard)` doesn't strip something it should — investigate `theme.go::applyColorMode` before regenerating.

---

## Milestone M1 — List view chrome

**Goal:** Replace the bare `joinNonEmpty(header / table / footer)` composition with the layered chrome from the design doc: title bar, status line, hairline-rule headered table, footer status + scroll indicator, footer help row.

**Files:**
- Modify: `internal/tui/list_render.go`
- Modify: `internal/tui/list.go` (only if `View()` signature changes)
- Possibly: `internal/tui/help.go` (if `reflowHelpRows` lives here vs `list_render.go`)
- Test: `internal/tui/snapshot_test.go` (new fixtures), possibly `list_test.go`

**Invariants touched:**
- Sanitization (preserve all existing `sanitizeForDisplay` calls in `buildRows` and `renderChips`).
- Viewport (`windowIssues` keeps its current contract; only the rendered chrome around it changes).
- Identity selection (`selectedNumber` and `applyFetched` are state, not render — should be untouched).

- [ ] **Step 1: Lift `reflowHelpRows`** from `/Users/wesm/code/roborev/cmd/roborev/tui/util.go:80-150` (verbatim per design decision §5). Place in `internal/tui/help.go`. Adapt the input shape from roborev's `[]helpItem` to a kata equivalent — define `type helpItem struct{ key, desc string }` if not already present.
- [ ] **Step 2: Define list-view help rows** as a `[][]helpItem` in `list_render.go` or `help.go`: the keys for the list view's default state. (Inline command bar gets its own help row in M3a.)
- [ ] **Step 3: Compose the title bar.** New helper `renderTitleBar(width int, scope scope, counts issueCounts) string` returning the `kata · project · open:N closed:N all:N · vX.Y.Z` line. `vX.Y.Z` reads from a build-time variable or hardcoded constant for now (no version package exists yet — use `"v0.1.0"` or the git short SHA from `internal/version` if you add one; checking whether a version package exists is part of this step).
- [ ] **Step 4: Compose the status line.** New helper `renderStatusLine(width int, sse sseConnState, pending int, actor string) string`. SSE state on the left, actor on the right, separated by padding. Active actor comes from `lm.actor`.
- [ ] **Step 5: Wire title bar + status line + chip strip + table + footer status + scroll indicator + help row** into `lm.View(width, height)`. Each section gets a width budget; total height stays bounded by `height` (use `listBodyHeight` already in place). Chip strip is hidden when no filters active (current behavior).
- [ ] **Step 6: Add scroll indicator** to the footer line. Format: `[start-end of total issues]` like roborev. Compute `start`/`end` from `windowIssues`'s offset (will need a small refactor — `windowIssues` should return start/end too, or expose them via a separate helper).
- [ ] **Step 7: Use `lipgloss.Border{Top:"─", Bottom:"─", Middle:"─"}` on the table** (no left/right/box borders). Header row enabled via `BorderHeader(true)` so column titles get a visible underline. Match roborev's `render_queue.go:444-456` shape.
- [ ] **Step 8: Snapshot-test the new chrome.**
    - `TestSnapshot_List_DefaultMixedStatus` — regenerate (chrome changed; the existing fixture name is fine).
    - New `TestSnapshot_List_ScrollIndicator` — fixture with 50 issues, cursor at row 25, asserts the `[start-end of N]` text appears in the right place.
    - New `TestSnapshot_List_EmptyAfterFilterWithChrome` — the existing `list-empty-after-filter` regenerated; chrome should still render even when the table is empty.
- [ ] **Step 9: Lint + test.**

**Acceptance criteria:**
- All hard invariants still hold (run the full `internal/tui/` test suite).
- New snapshot fixtures committed alongside the code.
- Title bar, status line, chip strip, headered table with hairline rules, footer status+scroll, and footer help row are all visible in the regenerated golden.
- 80-col terminal still renders without truncation issues (snapshot a 80×24 render to verify).

**Risks:** Medium-low. Easiest pitfall is the table's column-width math drifting under the new border config. Test at three widths: 80, 120, 160.

---

## Milestone M2 — Detail view chrome

**Goal:** Apply the same chrome treatment to the detail view: title bar (same), status line (same), header strip (`#N · status · author · created Xago · updated Yago`), title row (bold, full-width), body with rule separators, tab strip with `[ … ]` bracket on active, tab content with rule separators, footer status + scroll indicator, footer help row contextual to detail.

**Files:**
- Modify: `internal/tui/detail.go`
- Modify: `internal/tui/detail_render.go`
- Modify: `internal/tui/detail_tabs.go` (tab strip rendering)
- Test: `internal/tui/snapshot_test.go`

**Invariants touched:**
- Sanitization (preserve every `sanitizeForDisplay` in `renderHeader`, `renderBody`, `renderCommentsTab`, `renderEventsTab`, `renderLinksTab`).
- Detail-fetch staleness via `dm.gen` (no state changes; only render).

- [ ] **Step 1: Reuse the title bar + status line helpers from M1.** Detail view shows the same top two lines so the user has continuity.
- [ ] **Step 2: Build the detail header strip.** New helper `renderDetailHeader(width int, iss Issue) string` formatting `#N · status · author · created Xago · updated Yago`. Title row separately, on the next line, bold and full-width. Both use `humanizeRelative` (already in `list_render.go`).
- [ ] **Step 3: Add hairline rule separators** above and below the body, above the tab content. Use `strings.Repeat("─", width)` directly — no need to involve `lipgloss.Border` here since these are standalone lines, not table edges.
- [ ] **Step 4: Update tab strip** to wrap the active tab in `[ … ]` literal brackets. Replace the existing `tabActive`/`tabInactive` styling with a more visible composition: `tabActive` adds `[ ` and ` ]` wrapping plus bold; `tabInactive` is just the name in normal weight. Include the count in parens (already done).
- [ ] **Step 5: Add tab-content scroll indicator** to the footer. Same `[start-end of total]` shape as the list. Per-tab (comments/events/links) — `len(dm.comments)`, `len(dm.events)`, `len(dm.links)` give the totals.
- [ ] **Step 6: Build the detail-view help rows.** Different keys from list view (e/c/+/-/a/A/L/p/b/x/r/tab/shift-tab/enter/esc). Pass into the lifted `reflowHelpRows`.
- [ ] **Step 7: Snapshot-test the new chrome.**
    - `TestSnapshot_Detail_CommentsTab` — regenerate.
    - `TestSnapshot_Detail_EventsTab` — regenerate.
    - `TestSnapshot_Detail_LinksTab` — regenerate.
    - New `TestSnapshot_Detail_BodyScroll` — fixture with a long body, scroll offset > 0, asserts the body-area scroll indicator is present.
    - New `TestSnapshot_Detail_LongCommentsList` — 50 comments, cursor at 25, asserts the per-tab scroll indicator.
- [ ] **Step 8: Lint + test.**

**Acceptance criteria:**
- All hard invariants still hold.
- Detail view at 80×24 fits without overflow (snapshot to verify).
- Tab strip clearly shows the active tab.
- Scroll indicators appear when applicable, not when the content fits.

**Risks:** Low. Bounded to render code.

---

## Milestone M3a — Input infrastructure + inline command bar

**Goal:** Land the input infrastructure (the shared `inputState`/`inputField` types) and migrate the existing `searchState` (`/` search, `o` owner) to the new inline command bar shell. Filters stay client-side and apply live, undebounced.

**Files:**
- Create: `internal/tui/input.go` — `inputState`, `inputField`, `inputKind`, dispatch helpers
- Create: `internal/tui/inputs_render.go` — bar/prompt/form render shells (only bar implemented in M3a; prompt scaffolding stub for M3b)
- Modify: `internal/tui/list.go` — replace `searchState` with the new `inputState` for `/`/`o`
- Modify: `internal/tui/list_render.go` — wire bar render into the chrome where chip strip currently lives
- Modify: `internal/tui/keymap.go` — only if a key changes (probably not)
- Modify: `go.mod`, `go.sum` — add `bubbles` (already done by the preflight)
- Create: `internal/tui/input_test.go` — dispatch + commit/cancel + focus tests
- Modify: `internal/tui/list_filter_test.go` — port existing `searchState` tests to the new shell

**Invariants touched:**
- Sanitization (continue calling `sanitizeForDisplay` on the rendered input buffer).
- Identity selection (no state-shape change — the bar's commit dispatches a refetch via `lm.refetchCmd` which already dispatchKey-tags).

> **Block 0:** M3a preflight gate (above) must be green before this milestone starts.

- [ ] **Step 1: Define types** in `input.go`.
    ```go
    type inputKind int
    const (
        inputNone inputKind = iota
        inputSearchBar
        inputOwnerBar
        // M3b adds: inputLabelPrompt, inputOwnerPrompt, inputLinkPrompt, inputParentPrompt, inputBlockerPrompt
        // M4 adds:  inputNewIssueForm, inputEditBodyForm, inputCommentForm
    )

    type fieldKind int
    const (
        fieldSingleLine fieldKind = iota
        fieldMultiLine
    )

    type inputField struct {
        label    string                  // form-only
        kind     fieldKind
        input    textinput.Model         // populated when kind == fieldSingleLine
        area     textarea.Model          // populated when kind == fieldMultiLine
        required bool
    }

    type inputState struct {
        kind   inputKind
        title  string                    // bar/prompt/form chrome title
        fields []inputField
        active int
        err    string
        saving bool
    }
    ```
- [ ] **Step 2: Helpers** for dispatch:
    - `(inputState).Active() *inputField`
    - `(inputState).Update(msg tea.KeyMsg) (inputState, action)` where `action` is one of `actionNone`, `actionCommit`, `actionCancel`. Pure function over `inputState`; the caller routes the action to the right handler (filter commit, mutation dispatch, etc.).
    - `newSearchBar() inputState` — pre-builds an `inputState{kind: inputSearchBar, title: "search", fields: [singleLineField()]}`.
    - `newOwnerBar() inputState`.
- [ ] **Step 3: Render shell** in `inputs_render.go`:
    - `renderInputBar(s inputState, width int) string` — single-line bordered box, title in the top border, magenta when focused (always focused while open).
    - Stub `renderPanelPrompt` and `renderCenteredForm` returning `""` for now; M3b/M4 fill them in.
- [ ] **Step 4: Replace `searchState` in `listModel`.** Drop the `searchState` field and `searchFieldNone/Query/Owner/NewTitle` enum (NewTitle moves to M4 with the form). Add `lm.input inputState` (or hoist input to `Model.input` if list and detail share — design doc puts it on `Model`, do that).
    - Wait — design doc says state lives on `Model.input`. Move it there. List and detail render code reads `m.input` to know whether to overlay a bar/prompt/form on top of their normal render.
- [ ] **Step 5: Wire `/` and `o`** in `list.go::applyPromptKey`: instead of `lm.startPrompt(searchFieldQuery)`, return a sentinel that the parent Model translates into `m.input = newSearchBar()`. Cleanest: have `applyPromptKey` return a `tea.Cmd` that emits `openInputMsg{kind: inputSearchBar}`, and `Model.Update` handles the message at top level.
- [ ] **Step 6: Live filter on every keystroke.** When `m.input.kind == inputSearchBar` or `inputOwnerBar`, mirror the buffer into `lm.filter.Search` / `lm.filter.Owner` on every keystroke. `filteredIssues` re-applies on next render. No refetch needed (Owner/Search are client-side).
- [ ] **Step 7: Commit/cancel handling.** On `enter`: bar closes, the filter stays applied (it was already applied live). On `esc`: bar closes, the filter reverts to whatever was set before the bar opened (cache the pre-open value and restore).
- [ ] **Step 8: Render integration.** In `list_render.go::renderHeader`, when `m.input.kind == inputSearchBar || inputOwnerBar`, render the bar in place of the chip strip; otherwise render the chip strip as before. This means `renderHeader` needs the `m.input` value — adjust the signature or compose at the `Model.View` level.
- [ ] **Step 9: Update help row.** When the bar is open, the footer help row swaps to `enter commit  esc cancel  ctrl+u clear`. Otherwise it stays the list's default.
- [ ] **Step 10: Tests.**
    - `TestInput_SearchBarTyping_AppliesFilterLive`: open search bar, type "lo", assert `lm.filter.Search == "lo"` and `filteredIssues` is narrowed.
    - `TestInput_SearchBarEsc_RevertsFilter`: pre-set `lm.filter.Search = "old"`, open search bar, type "new", press esc, assert `lm.filter.Search == "old"`.
    - `TestInput_SearchBarEnter_KeepsFilter`: open bar, type, press enter, assert filter stays + bar closes.
    - `TestInput_OwnerBarSameBehavior`: mirror of search for owner.
    - `TestInput_OwnerBarSanitizesBuffer`: type a string with `\x1b[31m`, assert the rendered bar doesn't contain ESC.
    - Snapshot: `list-search-bar-active`, `list-owner-bar-active`.
- [ ] **Step 11: Port existing `list_filter_test.go` tests.** The tests that drove the old `searchState` flow (`TestList_SearchPrompt_*`, `TestList_OwnerPrompt_*`, `TestList_BackspaceTrimsBuffer`, etc.) need to exercise the new bar instead. Keep the assertions; rewrite the driver code.
- [ ] **Step 12: Remove dead `searchState` code** from `list.go`. Don't leave the type or the helpers around as dead weight.
- [ ] **Step 13: Lint + test.** All hard invariants still hold.

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `searchState` no longer exists in the codebase (`grep -r "searchState" internal/tui/` returns nothing).
- New `bubbles` dependency in `go.mod` at the version chosen during preflight.
- Inline command bar renders correctly in stacked layout (M6 will add split-mode rendering).
- All hard invariants hold.

**Risks:** Medium. New dependency; large code-touch surface in `list.go` and `list_filter_test.go`. The `Model.input` hoisting is a small refactor that affects routing.

---

## Milestone M3b — Panel-local prompts replace `dm.modal`

**Goal:** Migrate the existing `dm.modal` machinery (label, owner, link, parent, blocker prompts) to the new panel-local prompt presentation under the existing detail chrome. Same data flow, new chrome.

**Files:**
- Modify: `internal/tui/detail.go` — replace `dm.modal` with `m.input` overlay
- Modify: `internal/tui/detail_render.go` — render panel-local prompt at the bottom of the detail panel when active
- Modify: `internal/tui/detail_mutation.go` — adapt the dispatch path to consume the new commit signal
- Modify: `internal/tui/inputs_render.go` — flesh out `renderPanelPrompt`
- Delete or shrink: `internal/tui/modal.go` — most of it goes; keep only what's still useful (likely nothing)
- Modify: `internal/tui/input.go` — add `inputLabelPrompt`/`inputOwnerPrompt`/`inputLinkPrompt`/`inputParentPrompt`/`inputBlockerPrompt` kinds + constructors
- Modify: `internal/tui/keymap.go` — no key changes; the binding-to-action mapping is unchanged
- Modify: existing detail mutation tests (`detail_mutation_test.go`) to drive the new shell
- New tests: snapshot fixtures per prompt kind

**Invariants touched:**
- Sanitization (panel-local prompts render under the existing detail chrome which already sanitizes).
- Detail-mutation routing (`mutationDoneMsg{origin:"detail",gen}`) — the dispatch path is unchanged; only the input collection differs.

- [ ] **Step 1: Add the prompt input kinds** to `inputKind` enum.
- [ ] **Step 2: Constructors** for each: `newLabelPrompt(issueNum int64) inputState`, `newOwnerPrompt`, `newLinkPrompt`, `newParentPrompt`, `newBlockerPrompt`. Each has `title` set to `"add label to #N"` etc.
- [ ] **Step 3: Render shell** `renderPanelPrompt(s inputState, width int) string` — small bordered box with the prompt title in the top border, single-line input below. Magenta border (always focused while open). Used inside the detail panel rendering.
- [ ] **Step 4: Wire in `detail_render.go::View`.** When `m.input.kind` is one of the prompt kinds, render the panel-local prompt at the bottom of the detail panel (after the tab content, before the footer). Reduce the tab-content height budget by the prompt's line count.
- [ ] **Step 5: Replace `dm.modal` triggers.** In `detail.go::handleMutationKey` and `detail_mutation.go::handleModalOpenKey`, the `+`/`-`/`a`/`p`/`b`/`L` keys open the corresponding `inputState` instead of opening a `dm.modal`. Emit `openInputMsg{kind: inputLabelPrompt, ...}` so the parent Model handles it.
- [ ] **Step 6: Replace `commitModal` path.** When the prompt commits, route through the existing `dispatchForKind` machinery (it knows label/owner/link semantics); the only change is where the buffer comes from (`m.input.fields[0].input.Value()` instead of `dm.modal.buffer`).
- [ ] **Step 7: Delete `dm.modal` field** from `detailModel`. Remove the `modal` type from `modal.go` if nothing else uses it. `grep` to confirm.
- [ ] **Step 8: Tests.**
    - `TestDetail_AddLabel_PanelLocalPromptCommit`: presses `+`, types `priority-high`, presses enter; asserts `api.AddLabel` called with `"priority-high"`.
    - `TestDetail_AddLabel_PanelLocalPromptEsc`: cancel path.
    - Mirror for owner/link/parent/blocker.
    - Snapshot: `detail-with-label-prompt`, `detail-with-link-prompt`.
    - Existing `detail_mutation_test.go` modal-driven tests get rewritten to the new flow.
- [ ] **Step 9: Lint + test.** All hard invariants still hold.

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `dm.modal` is gone (`grep -r "dm.modal\|type modal " internal/tui/` returns nothing).
- Snapshots prove the panel-local prompt renders inside the detail chrome.
- All hard invariants hold.

**Risks:** Medium. Bounded to detail-side; no cross-cutting changes. The `commitModal`/`dispatchForKind` glue is the trickiest piece — make sure the existing mutation-routing (origin/gen) is preserved.

---

## Roborev-fix checkpoint #1 (after M3b) — completed pre-M3.5

> Done in commit `9d5f6a8` (pre-M3.5 surgical fixes). Internals/correctness findings from jobs 89, 90 landed as small fixes; chrome/input findings (jobs 94/95/96/97) are folded into M3.5 below; plan-doc findings (91, 92) close as superseded once M3.5 lands.

---

## Milestone M3.5 — msgvault-style layout shell refactor

> **Why this exists.** The chrome shipped in M0–M3b is a structural mismatch with what daily-use TUIs look like (msgvault, cf. `~/code/msgvault/internal/tui/`). The footer doesn't pin to the terminal bottom; the search bar pops above the table; the SSE-status line is debugger noise; the brand strip reads `kata · kata · ...` (project name collision); `q` quits without confirmation. Wes pushed back hard after looking at M0–M3b live: "it is very bad. There is no TUI chrome, it doesn't feel like a real application."
>
> M3.5 reorganises the layout shell along msgvault's patterns. It is not optional polish — it is what the M0–M3b chrome should have been.

**Reference**: msgvault TUI (`~/code/msgvault/internal/tui/{view,model,keys}.go`). Memory: `feedback_kata_tui_msgvault.md`.

**Files in scope:**
- Modify: `internal/tui/list_render.go` (layout shell rewrite, drop SSE line, title-bar restyle, footer-pin via fillScreen, info-line search, simplified table chrome)
- Modify: `internal/tui/detail_render.go` (same chrome treatment + reserve toast/prompt height — addresses job 95/97 budget overflow)
- Modify: `internal/tui/inputs_render.go` (renderInputBar takes width arg — addresses job 96 hardcoded 80; renderPanelPrompt reserves footer slot)
- Modify: `internal/tui/model.go` (synchronous input open instead of openInputCmd async race — addresses job 96/97 race; quit-confirm modal routing)
- Modify: `internal/tui/list.go` (inline new-issue title row at top of table; drop the searchFieldNewTitle inline-prompt vestige once the row replaces it)
- Modify: `internal/tui/keymap.go` (Quit help text becomes "quit (confirm)"; drop ToggleScope from help row entirely until daemon lands; new-issue help labels)
- Modify: `internal/tui/help.go` (renderHelpBar swap to `│`-separated joining; two-level rows when overflow)
- Modify: `internal/tui/theme.go` (titleBarStyle / statsStyle / cursorRowStyle / altRowStyle / separatorStyle / footerStyle / modalStyle — borrow msgvault's exact style shapes)
- New: `internal/tui/layout_fill.go` (fillScreen helper for the bottom-pin pattern)
- New: `internal/tui/quit_modal.go` (or fold into modal.go-redux — quit-confirm modal: `[Y] Yes  [N] No`)
- Test: `internal/tui/snapshot_test.go` (new fixtures: `list-msgvault-chrome`, `list-search-bar-info-line`, `list-new-issue-row`, `quit-confirm-modal`, `title-bar-narrow-80`, `title-bar-wide-200` — last two for the `kata かた` wide-character alignment guard)

**Invariants touched:**
- Sanitization (preserve every existing call; new title-bar `kata かた` text is daemon-side-known so no sanitize needed there).
- Identity selection (untouched — state, not chrome).
- Stale-fetch guards (untouched — preserved internals).
- Help-during-overlay sync (extended: the new quit-confirm modal must not steal keys when the inline command bar is open).

### M3.5 Step 1: Layout shell with `fillScreen` (footer pinning)

- [ ] **Adopt msgvault's `fillScreen` pattern.** New `layout_fill.go` with `fillScreen(content string, usedLines, totalHeight int) string` that pads blank lines so the footer + info line always render at the absolute bottom of the terminal. Mirror `view.go::fillScreen` in msgvault.
- [ ] **Restructure `lm.View`** to compose: `header (2 lines)` + `body (table with header + separator + rows, padded to fill)` + `info line (search bar OR scroll indicator OR blank)` + `footer (help row)`. Total height == terminal height; no overflow possible because the body absorbs the slack.
- [ ] **Drop `listBodyHeight`'s reserve-then-render approach**; replace with the fillScreen pad-to-bottom pattern.
- [ ] **Verify on 80×24, 100×30, 120×40** that footer is on the bottom row regardless of issue count.

### M3.5 Step 2: Bottom utility line for search/owner

- [ ] **Move the inline command bar from above-the-table to the info line above the footer.** msgvault renders search as `/` + textinput on the info line via `m.searchInput.View()`. Mirror that.
- [ ] **Drop `chromeHeaderForList`'s switch-on-input-kind branch**; the chip strip stays where the inline bar used to sit (between status line and table) — but the bar itself moves to the info line.
- [ ] **Update `listHelpItemsFor(input)`** to keep swapping the help row when bar is active (`enter commit · esc cancel · ctrl+u clear`).
- [ ] **Pass actual terminal width into `renderInputBar`** instead of the hardcoded 80 — addresses roborev #96 finding 2.

### M3.5 Step 3: Title bar + branding

- [ ] **Restyle title bar with adaptive background.** New `titleBarStyle` in `theme.go` mirroring msgvault's: bold + adaptive bg + 1-cell horizontal padding. The bar reads as a window-chrome strip.
- [ ] **Brand text becomes `kata かた · project: $name`** when scope.projectName is set. Falls back to `kata かた` alone when no project. Right-aligned `vX.Y.Z` slot stays as-is.
- [ ] **Rendered counts move to the second header line** (alongside breadcrumb-equivalent — for kata that's the active filter chips). msgvault uses `breadcrumbStyled + gap + statsStyled`.
- [ ] **Drop the persistent `SSE: connected · 0 pending events` line.** The SSE state surfaces ONLY when degraded — `sseReconnecting` / `sseDisconnected` becomes a flash on the info line, brief and dismissible.
- [ ] **Drop the `actor` / `anonymous` slot.** `lm.actor` stays for mutation dispatch, but it doesn't render as chrome text.
- [ ] **Wide-character alignment snapshots**: lock the title-bar render at widths 80, 100, 140, 200. The `かた` glyphs are 2 cells each; the right-aligned version text must not drift.

### M3.5 Step 4: Table chrome simplification

- [ ] **Drop the top + bottom hairline rules from the lipgloss table.** Keep ONLY the under-header separator (msgvault's `separatorStyle.Render(strings.Repeat("─", m.width))` immediately after the header row).
- [ ] **Add alternating row backgrounds** via `normalRowStyle` (even rows) + `altRowStyle` (odd rows). Cursor row uses `cursorRowStyle` (a brighter background). Three-tier styling matches msgvault's table.
- [ ] **Header row uses `tableHeaderStyle`** (bold + adaptive bg).
- [ ] **Cursor glyph stays `▶`** (msgvault uses this; matches our existing `›` semantically but `▶` is more visible).

### M3.5 Step 5: Quit-confirm modal

- [ ] **New `quit_modal.go`** with `renderQuitConfirmModal()` returning a centered bordered panel with `Are you sure you want to quit? [Y] Yes [N] No`. Mirror msgvault's pattern.
- [ ] **`q` opens the quit modal** instead of immediately quitting. `ctrl+c` keeps fast-quit semantics for power users.
- [ ] **Modal overlay rendering**: `lipgloss.Place(width, height, Center, Center, ...)` with the modal box on top of whatever view was active. The underlying body stays painted (dimmed not strictly required — msgvault doesn't dim either).
- [ ] **Modal key dispatch**: `y`/`Y` → `tea.Quit`; `n`/`N`/`esc` → close modal, no quit. Other keys ignored while modal is open.
- [ ] **`canQuit()` extended**: while the quit modal is open, no other keys (including `q` again) propagate; the modal owns input.

### M3.5 Step 6: Inline new-issue title row

- [ ] **`n` opens a dedicated new-issue row at the top of the table** — NOT at the cursor position (recency-sorted lists make cursor-position fake; the row would jump after create anyway).
- [ ] **The row hosts a `bubbles/textinput`** styled to look like the other table rows (same column widths). Cursor sits in the title cell; status/owner/updated cells render as `--`/blank/blank placeholders.
- [ ] **`enter` commits the title immediately** — `api.CreateIssue(title, "", actor)` — and on success opens the centered body form for optional refinement (M4 wires the body form; M3.5 just leaves an empty body and shows the row at the top of the refreshed list).
- [ ] **`esc` cancels** without creating.
- [ ] **The row replaces** the current `n` → `searchFieldNewTitle` → `submitNewIssue` → `editorCmd("create")` chain. M4 layers the post-create body form on top.
- [ ] **Footer help row swaps** when the inline new-issue row is active: `enter create · esc cancel`.
- [ ] **Drop the now-dead `searchFieldNewTitle`, `lm.search`, `submitNewIssue`, and `pendingTitle` fields** from listModel.

### M3.5 Step 7: Footer two-level + ` │ ` separators

- [ ] **`renderHelpBar` joins items with ` │ `** (vertical bar + spaces) instead of `  ` (two spaces). msgvault's denser, easier-to-parse style.
- [ ] **Position indicator on the right of the footer** — `[N/M issues]` or `N of M` — separated from the keys by gap. msgvault's pattern (`footerView`).
- [ ] **Help labels are descriptive**: `c clear filters` not `c clear`; `s cycle status` not `s status`. The `│` separators give room.
- [ ] **When the help overflows one line at 80 cols**, the existing `reflowHelpRows` already wraps to a second row. Verify under M3.5 with the new label lengths.

### M3.5 tests + acceptance

- [ ] **Snapshot fixtures** (regenerated under the new chrome):
    - `list-msgvault-chrome` (80×24, 120×30, 200×40 — three width snapshots so the title-bar `かた` alignment is locked)
    - `list-search-bar-info-line` (search bar active on the info line)
    - `list-new-issue-row` (inline new-issue row at top of table, cursor in title field)
    - `quit-confirm-modal` (centered overlay)
    - `detail-toast-reserves-height` (regression for jobs 94/95 — footer must stay visible when toast is active)
- [ ] **Behavior tests:**
    - `TestQuit_QPressed_OpensConfirm`: q opens the modal, doesn't quit immediately.
    - `TestQuit_CtrlCFastQuits`: ctrl+c bypasses the modal.
    - `TestQuit_YConfirms`: y/Y inside the modal returns tea.Quit.
    - `TestQuit_NCancels`: n/N/esc closes the modal without quitting.
    - `TestNewIssueRow_EnterCreatesImmediately`: typing in the inline row + Enter calls api.CreateIssue with the title and empty body.
    - `TestNewIssueRow_EscCancels`: Esc closes the row, no API call.
    - `TestSearch_BarRendersOnInfoLine`: snapshot confirms the search bar is below the table, not above.
    - `TestFooter_PinnedToBottom`: rendering at height 30 with 5 issues — the footer is on row 30, not row 8.
    - `TestTitleBar_KataKanaAlignment`: render at 80, 100, 140 — verify the right-aligned version text appears at column == width-len(version), accounting for `かた` being 2 cells each.
- [ ] **Synchronous input open**: drop `openInputCmd` indirection for `/`, `o`, the panel prompts, and `n`. The inputState constructor runs in the same Update tick that handled the key. Addresses roborev #96/#97 async-race findings.
- [ ] **Lint clean, `go test ./...` green, all hard invariants hold.**

**Acceptance criteria:**
- `make build && ./kata tui` shows: bordered title bar at top with `kata かた · project: $name · vX.Y.Z`; second line with counts/filters; table flush against bg with one separator under header; alternating row stripes; footer pinned to absolute bottom row of terminal with `│`-separated descriptive keys; search bar shows on info line just above footer when `/` pressed; quit-confirm modal on `q`; new-issue row at top of table on `n`.
- All hard invariants still hold (re-run the full TUI test suite + manual smoke).
- Folded roborev reviews (94/95/96/97) close as superseded with comment pointing at the M3.5 commit.
- Plan-doc reviews (91/92) close as superseded by the pre-M3.5 fixes (jobs 89/90) which removed the "preserved as-is" routing bugs.

**Risks:** High. Largest single milestone. Touches every render file and the input plumbing. The `fillScreen` rewrite is the structural change most likely to regress snapshot tests. Manual smoke on a real terminal at three widths (80, 120, 160) is mandatory before commit.

---

## Milestone M4 — Centered body/comment forms (post-M3.5 scope)

> **Scope-narrowed in M3.5.** The new-issue title flow moved to the inline new-issue row in M3.5 (top-of-table). M4 is now strictly about the centered **body** and **comment** forms — the post-create body refinement, the `e` edit-body form, and the `c` add-comment form. No new-issue title prompt here.

**Goal:** Replace the `$EDITOR`-driven edit-body / add-comment flows with in-app centered forms (bubbles/textarea). Add the post-create body form that opens immediately after the M3.5 inline row commits a title. `ctrl+e` is the explicit `$EDITOR` escape hatch from any multiLine field. The existing `editorCmd` machinery in `editor.go` stays intact.

**Files:**
- Modify: `internal/tui/input.go` — add `inputNewIssueForm`/`inputEditBodyForm`/`inputCommentForm` kinds + constructors with `bubbles/textarea`
- Modify: `internal/tui/inputs_render.go` — flesh out `renderCenteredForm` (multi-field aware)
- Modify: `internal/tui/list.go` — `n` opens the new-issue form instead of an inline title prompt
- Modify: `internal/tui/detail.go`, `detail_editor.go` — `e` and `c` open the edit-body / comment forms instead of `$EDITOR` shell-out
- Modify: `internal/tui/editor.go` — keep `editorCmd` and `trimComments`; add a helper that hands a buffer back to a form on resume
- Modify: `internal/tui/keymap.go` — add `ctrl+s` (`Submit`?) and `ctrl+e` (`OpenInEditor`?) bindings if they don't exist as keymap entries. Could also leave them inline since they're modal-only.
- Tests: new `form_create_test.go`, `form_edit_body_test.go`, `form_comment_test.go`, `form_editor_handoff_test.go`
- Tests: existing `editor_test.go` and `detail_editor_test.go` get extended for the `ctrl+e` handoff path

**Invariants touched:**
- Title whitespace preservation (the form's title field commits to `lm.pendingTitle` directly, no `TrimSpace`).
- Comment-only sentinel strip (`trimComments(kind, content)` still gates by kind; the form's `ctrl+e` handoff produces a `kind="edit"` or `kind="comment"` buffer per the original kind).
- CRLF handling in `stripSentinelBlock`.

- [ ] **Step 1: Add form input kinds + constructors.**
    - `newNewIssueForm() inputState` — two fields: `title` (singleLine, required) + `body` (multiLine).
    - `newEditBodyForm(issueNum int64, current string) inputState` — single multiLine field pre-filled with the current body.
    - `newCommentForm(issueNum int64) inputState` — single multiLine field, empty.
- [ ] **Step 2: Render shell.** `renderCenteredForm(s inputState, width, height int) string` — bordered panel via `lipgloss.Place`, fields stacked vertically with labels above each input box. Active field's input box gets the magenta border; others gray. Footer hint inside the panel: `tab switch field   ctrl+s save   esc cancel   ctrl+e $EDITOR` (only show ctrl+e when active field is multiLine).
- [ ] **Step 3: Field-cycling logic** in `inputState.Update` for multi-field forms.
    - `tab` / `shift-tab`: advance `s.active`, calling `Blur()` on the old field's input/area and `Focus()` on the new one.
    - `enter` on singleLine field with non-empty buffer: advance to next field (if any). Empty buffer: no-op (or could highlight the field).
    - `enter` on multiLine field: insert newline (delegate to textarea).
- [ ] **Step 4: Commit handler.** `ctrl+s` validates required fields (non-empty); on success, returns `actionCommit` with the field values. The Model-level handler dispatches the appropriate `tea.Cmd`:
    - `inputNewIssueForm` → `api.CreateIssue(title, body, actor)`. Title whitespace preserved (no `TrimSpace` on the staged value; only check `TrimSpace == ""` for the required-field gate).
    - `inputEditBodyForm` → `api.EditBody(...)`.
    - `inputCommentForm` → `api.AddComment(...)`.
- [ ] **Step 5: $EDITOR handoff (ctrl+e).** When the active field is multiLine, `ctrl+e` triggers the existing `editorCmd("edit", currentBuffer)` (or `"comment"` for the comment form). On `editorReturnedMsg` arrival, write the returned content back into the active field's `textarea.Model` (`area.SetValue(content)`) — the form re-opens with the edited buffer pre-loaded. The user can then tweak and `ctrl+s`. `editor.go::trimComments` still applies on commit (the `ctrl+e` round-trip doesn't bypass sanitization).
- [ ] **Step 6: Replace `n` (new issue) trigger** in `list.go`. Currently it opens an inline title prompt then dispatches `editorCmd("create", "")`. Replace with: `n` opens `newNewIssueForm()` via `openInputMsg`. Drop the `pendingTitle` field on `listModel` (the form holds it directly until commit).
- [ ] **Step 7: Replace `e` (edit body) trigger** in `detail.go`. Currently dispatches `editorCmd("edit", dm.issue.Body)`. Replace with: `e` opens `newEditBodyForm(dm.issue.Number, dm.issue.Body)`.
- [ ] **Step 8: Replace `c` (add comment) trigger** in `detail.go`. Currently dispatches `editorCmd("comment", commentTemplate())`. Replace with: `c` opens `newCommentForm(dm.issue.Number)`.
- [ ] **Step 9: Sanitize the form buffers at render time.** Every field's rendered value flows through `sanitizeForDisplay` so a paste of an ANSI sequence can't paint the modal. (`bubbles/textinput`/`textarea` are usually safe but pasting via `bracketed-paste` is a known vector.)
- [ ] **Step 10: Tests.**
    - `TestForm_NewIssue_TitleAndBody_Commit`: type title, tab to body, type body, ctrl+s; asserts `CreateIssue` called with both values intact.
    - `TestForm_NewIssue_PreservesTitleWhitespace`: title `"  spaced  "` reaches the wire untrimmed.
    - `TestForm_NewIssue_EmptyTitle_BlocksCommit`: ctrl+s with blank title shows error in modal, no API call.
    - `TestForm_NewIssue_EnterAdvancesFromTitle`: enter on title field with content moves focus to body.
    - `TestForm_NewIssue_Esc_CancelsWithoutSave`: form closes, no API call, no `pendingTitle` leftover.
    - `TestForm_EditBody_PrefilledWithCurrentBody`: opens the form, asserts `area.Value()` equals the issue body.
    - `TestForm_EditBody_CtrlSDispatchesEditBody`.
    - `TestForm_Comment_CtrlSDispatchesAddComment`.
    - `TestForm_EditorHandoff_RoundTripsBuffer`: ctrl+e from edit-body form, simulate `editorReturnedMsg{kind:"edit", content:"new"}`, assert form reopens with body=`"new"`. ctrl+s then dispatches edit with `"new"`.
    - `TestForm_EditorHandoff_TrimCommentsAppliesOnCommit`: ctrl+e from comment form returns content with sentinel block, ctrl+s on the reopened form dispatches with sentinel-stripped body.
    - `TestForm_SanitizesPastedAnsi`: paste a value with `\x1b[31m`, render the form, assert the rendered output has no ESC.
- [ ] **Step 11: Remove the inline `n` title prompt** from `list.go` (`searchFieldNewTitle`, the prompt branch in `applyPromptKey`, `submitNewIssue` in its current form, and the `lm.pendingTitle` field). Title now lives on the form until commit.
- [ ] **Step 12: Lint + test.** All hard invariants still hold.

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- `n` from list, `e`/`c` from detail open centered forms — verified by snapshot.
- `ctrl+e` from a multiLine form successfully suspends to `$EDITOR` and re-loads the buffer on resume — verified by `TestForm_EditorHandoff_RoundTripsBuffer`.
- `searchFieldNewTitle` and `lm.pendingTitle` are gone (`grep` confirms).
- All hard invariants hold, including title-whitespace preservation and comment-only sentinel strip.

**Risks:** High. Largest surface change. The `editor.go` machinery has to keep working through `ctrl+e` — don't delete it. The `bubbles/textarea` `enter`-vs-newline semantics may need testing against the actual library version (preflight in M3a).

---

## Milestone M5 — Empty state, help overlay, narrow-terminal hint

**Goal:** Polish the auxiliary screens under the new palette. Ship the below-80-cols degraded hint. Status-flash priority over scroll indicator (already implicit in the footer render, codify).

**Files:**
- Modify: `internal/tui/scope.go` — empty-state render under new palette + new narrow-terminal hint render
- Modify: `internal/tui/help.go` — refresh under new palette
- Modify: `internal/tui/list_render.go` and `detail_render.go` — narrow-terminal short-circuit
- Modify: `internal/tui/model.go::View` — dispatch to the narrow-terminal hint when `m.width < 80`
- Tests: `narrow-terminal-hint` snapshot, refreshed `empty-state` and `help-narrow`/`help-wide`

**Invariants touched:** None (pure render polish).

- [ ] **Step 1: Empty state polish.** `renderEmpty` already exists in `scope.go`; update its color usage to the new palette and centering. Add a centered bordered panel inside `lipgloss.Place` to match the design doc mockup.
- [ ] **Step 2: Help overlay refresh.** `renderHelp` already exists in `help.go`. Update color usage; verify the layout still works with the lifted `reflowHelpRows`.
- [ ] **Step 3: Narrow-terminal hint.** New helper `renderTooNarrow(width, height int) string` returning a centered "kata tui needs ≥80 columns; resize and try again. press q to quit" panel. Bordered, magenta border, centered via `lipgloss.Place`.
- [ ] **Step 4: Hook narrow-terminal short-circuit** in `Model.View()`: when `m.width < 80`, render `renderTooNarrow(m.width, m.height)` regardless of the active view. `q` still routes through `routeGlobalKey` so quit works.
- [ ] **Step 5: Status-flash priority.** In the footer-render code (`renderFooter` in list/detail), explicitly prefer the flash message over the scroll indicator when both would render. Match roborev's `render_review.go:251-256`.
- [ ] **Step 6: Snapshot tests.**
    - Regenerate `empty-state`, `help-narrow`, `help-wide` under new palette.
    - New `narrow-terminal-hint` snapshot at width 60, height 10.
    - New `list-flash-overrides-scroll` snapshot proving the priority rule.
- [ ] **Step 7: Lint + test.**

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- Resizing a TUI session below 80 cols mid-flight surfaces the hint without crashing — verified by feeding `tea.WindowSizeMsg{Width:60}` to a model in tests and asserting `View()` contains the hint text.
- All hard invariants hold.

**Risks:** Low.

---

## Milestone M6 — Hybrid responsive layout (split mode)

**Goal:** Add the `layoutMode` discriminator on `Model`, render split-pane (list pane fixed 60–64 cells + detail pane flex) when terminal `width≥140 && height≥30`, focus indicator on the active pane, list cursor changes immediately repaint detail when cached and dispatch a 75ms-debounced fetch when not. Cross-pane focus moves: `tab`/`enter` list→detail, `esc` detail→list. Resize across the breakpoint preserves selection by `selectedNumber` and the focused pane.

**Files:**
- Create: `internal/tui/layout.go` — `layoutMode` discriminator, breakpoint helper, focus state
- Create: `internal/tui/split_render.go` — split-pane composition
- Modify: `internal/tui/model.go` — add `m.layout`, `m.focus`, route key dispatch through focus
- Modify: `internal/tui/list_render.go`, `detail_render.go` — add a "narrow column" mode for split view
- Modify: `internal/tui/list.go`, `detail.go` — focus-aware key handling
- Tests: `layout_test.go`, `split_test.go`, new snapshots `list-detail-split-wide`, `list-detail-split-focus-detail`, `list-detail-split-resize-collapse`

**Invariants touched:**
- All of them. M6 is the architectural change; double-check every hard invariant after each step.

- [ ] **Step 1: Layout discriminator.** `type layoutMode int { layoutStacked / layoutSplit }`. Helper `pickLayout(width, height int) layoutMode` returning `layoutSplit` iff `width >= 140 && height >= 30`. Belongs in `layout.go`.
- [ ] **Step 2: Focus state.** `type focusPane int { focusList / focusDetail }`. Add `m.focus focusPane` to `Model`. Default `focusList`.
- [ ] **Step 3: WindowSize handler.** In `routeTopLevel`, on `tea.WindowSizeMsg`: update `m.width`, `m.height`, then re-pick `m.layout`. If `m.layout` flipped, run the resize-preservation logic per the design doc:
    - split→stacked while focusDetail: preserve `m.view = viewDetail`, `m.detail` intact
    - split→stacked while focusList: preserve `m.view = viewList`
    - stacked→split: preserve `m.focus` from the prior view (`viewList → focusList`, `viewDetail → focusDetail`)
- [ ] **Step 4: Split render.** New `renderSplit(m Model) string` composing list pane (60–64 col fixed) + detail pane (flex). Each pane is bordered (`panelActiveBorder` for the focused pane, `panelInactiveBorder` for the other). Both panes share a single top-line title bar + status line + footer help row.
- [ ] **Step 5: Narrow-column list rendering.** `renderListPane(width int, focused bool, lm listModel)` drops the owner column (it's redundant in split — owner shows in detail header), shortens the title column. Same `windowIssues` math.
- [ ] **Step 6: Detail-follows-cursor.** When in split layout, every cursor change in the list pane retargets `m.detail` to point at the highlighted issue. Uses `lm.selectedNumber` (already identity-stable). If the detail data for that issue is in the cache (M6's cache may need expanding — currently we have a single-slot list cache; detail data per issue isn't cached. Open question: do we add a small detail-cache, or accept "cache miss = always fetch with debounce"?). Decision: **accept always-fetch**, since the debounce is short. If profiling shows it's a problem, add a detail LRU later.
- [ ] **Step 7: 75ms debounce.** `tea.Tick(75*time.Millisecond, ...)` after the last cursor change before dispatching detail fetches. The existing `Model.nextGen` counter drops stale fetches. New message type `detailFollowTickMsg{gen int64}` to gate the dispatch.
- [ ] **Step 8: Focus moves.** In `routeGlobalKey` (or a new `routeLayoutKey`), when `m.layout == layoutSplit`:
    - `tab` or `enter` from `focusList` → `focusDetail`
    - `esc` from `focusDetail` → `focusList` (only when no input/prompt is active inside the detail pane; otherwise let the input handle the esc)
- [ ] **Step 9: Help row swaps with focus.** When `focusList`, render the list help row; when `focusDetail`, render the detail help row.
- [ ] **Step 10: Tests.**
    - `TestLayout_PickLayout_Stacked`: width 100 height 30 → layoutStacked. Width 140 height 25 → layoutStacked (height fails).
    - `TestLayout_PickLayout_Split`: width 140 height 30 → layoutSplit.
    - `TestLayout_ResizeSplitToStacked_PreservesSelection`: in split with `selectedNumber=42`, focus=detail, resize down to 100 cols → assert `m.view==viewDetail` and `selectedNumber==42`.
    - `TestLayout_ResizeStackedToSplit_PreservesFocus`: in stacked detail, resize up to 160 cols → assert `m.layout==layoutSplit` and `m.focus==focusDetail`.
    - `TestSplit_CursorMoveRetargetsDetail`: split layout, list focused, j three times → assert detail pane reflects the new cursor's issue (after debounce tick fires).
    - `TestSplit_TabMovesFocusToDetail`: split layout, focus=list, press tab → focus=detail, list pane border switches to inactive style.
    - `TestSplit_EscReturnsFocusToList`: focus=detail, press esc → focus=list. (Only when no panel-local prompt is active.)
    - `TestSplit_EscDoesNotEscapeWhilePromptActive`: focus=detail with a label prompt open, press esc → prompt closes, focus stays on detail. Second esc: focus moves to list.
    - Snapshots: `list-detail-split-wide` (focus=list), `list-detail-split-focus-detail` (focus=detail).
- [ ] **Step 11: Lint + test.** Run **all** hard invariants tests; M6 is the highest-risk milestone for regressing them.

**Acceptance criteria:**
- `make lint` clean, `go test ./...` green.
- Split mode renders correctly at 140×30, 160×40, 200×50 (snapshot at each).
- Resize across the breakpoint in either direction preserves selection and focus per the spec.
- All hard invariants hold (especially identity selection, viewporting, gen monotonicity — M6 changes how detail is targeted).

**Risks:** High. Real architectural change to `Model.View` and key dispatch. Manual smoke test on an actual terminal (`tmux split-pane`) is mandatory before committing — snapshot tests don't cover real-terminal rendering quirks.

---

## Roborev-fix checkpoint #2 (after M6)

> Final cross-cutting review. The redesign is shipped; address any findings before considering the plan complete.

- [ ] Run `roborev fix --open --list --all-branches`. Review every finding.
- [ ] Fix what's fixable; close as accepted-tradeoff with a comment what isn't.
- [ ] Verify all snapshots are intentional (regenerate any that drifted accidentally).

---

## Final acceptance gate (post-M6)

Before the plan is considered complete:

- [ ] Manual smoke test on a real terminal at 80×24, 120×30, 160×40, 200×50. Verify chrome, split layout, focus indicators, all input flows (search bar, owner bar, label/owner/link prompts, new-issue form, edit-body form, comment form, `ctrl+e` handoff).
- [ ] `go test ./...` green.
- [ ] `golangci-lint run ./...` 0 issues.
- [ ] All hard invariants tested and passing — re-run the table from the top of this doc as a checklist.
- [ ] No file exceeds its budget (≤100 lines/function, cyclomatic ≤8, 100-char lines, file budgets per Plan 6 §39-58).
- [ ] `make build` produces a working `kata` binary at the repo root.
- [ ] Design doc `2026-05-01-kata-7-tui-design-sprint.md` and this impl plan are both committed and consistent.

## Out of scope (future plans)

These belong in plan 8+, not here:

- Daemon `GET /issues` cross-project endpoint + un-gating `--all-projects` and R toggle
- Mouse support (`tea.WithMouseAllMotion`) — keyboard-first stays
- Glamour markdown rendering for issue bodies
- Detail data LRU cache (only if M6's always-fetch + debounce proves too churny)
- Saved searches / pinned filters
- Multi-issue selection + bulk operations
- Theme customization via `KATA_COLOR_MODE=theme:nord`-style env var
