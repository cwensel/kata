# Plan 8 Design — Detail Rework, Project Label Cache, New-Issue Form, Filter Modal

**Status**: Approved design (2026-05-02). Implementation plan to follow via writing-plans skill.

**Author**: Wes McKinney + Claude (interactive brainstorm).

## Goal

Address three converging gaps in the kata TUI:

1. The detail view drops labels because the TUI Issue projection has no Labels field and `handleOpenDetail` doesn't dispatch `fetchIssue` (the existing `fetchIssue` helper is already wired into `handleJumpDetail` but the open-from-list path only fetches comments/events/links). Header is also unstructured (single line conflating issue number / status / author / timing) with no owner/labels visibility.
2. The new-issue workflow (M3.5c inline title row → M4 post-create body editor) is two steps for one operation. Users want a single multi-field modal that captures title + body + labels + owner in one go.
3. The list view's filter UX is a per-axis keymap (`s` / `o` / `/`) with no consolidated entry point. Roborev's filter modal is the reference; kata wants the same shape with a Labels axis once the wire supports it.

## Architecture overview

Six commits in dependency order. Commits 1–5a require no daemon changes; 5b requires a daemon DB query + API DTO addition.

| # | Commit | Adds | Daemon change? |
|---|--------|------|---|
| 1 | Decode show labels + bring detail-open to parity with jump path | `Issue.Labels`; `handleOpenDetail` reuses existing `fetchIssue` helper | no |
| 2 | Detail header restructure + section dividers + tab-strip polish | new chrome shape | no |
| 3 | Project label cache + autocomplete on `+` and `-` + SSE invalidation | `m.projectLabels`; suggestion menu | no |
| 4 | New-issue multi-field modal form; drop inline row + post-create chain | `inputNewIssueForm` | no |
| 5a | Filter modal `f` (Status/Owner/Search axes); drop `o` key | `inputFilterForm` | no |
| 5b | List-side per-issue labels + Labels axis on filter modal | daemon `LabelsByIssues` batch query, `IssueOut` wire DTO, modal Labels axis | **yes** |

## Wire shape conventions

### TUI `Issue` struct

`internal/tui/client_types.go` Issue struct gains:

```go
Labels []string `json:"labels,omitempty"`
```

The tag matters: list decode (5b) populates this field directly via wire decode; detail decode (1) manually copies from the showIssue response's top-level `body.Labels` array because the show wire shape carries labels at body root, not on the issue object. The `omitempty` tag means the absence of `labels` in show responses doesn't blank the field.

### `CreateIssueBody` (commit 4)

Currently has Title/Body/Actor only. Add:

```go
Owner  *string  `json:"owner,omitempty"`
Labels []string `json:"labels,omitempty"`
```

Daemon side already supports both (verified at `internal/api/types.go:89` and `internal/daemon/handlers_issues.go:56`). No daemon change for commit 4.

### Daemon `IssueOut` (commit 5b)

```go
// Don't mutate db.Issue — keep persistence and wire types separate.
type IssueOut struct {
    db.Issue
    Labels []string `json:"labels,omitempty"`
}

// ListIssuesResponse.Body.Issues becomes []IssueOut.
```

Daemon list handler runs the existing `db.ListIssues` query, then a new `db.LabelsByIssues(ctx, projectID, issueIDs []int64) (map[int64][]string, error)` batch query. The TUI's existing `Issue.Labels` field tag is correct — list decode picks the field up directly.

`db.LabelsByIssues` requirements:
- Returns empty map on empty `issueIDs` (no SQL roundtrip).
- SQL constrains by both `project_id` AND `id IN (...)` (defense against accidentally returning labels from another project on issueID collision).
- ORDER BY `issue_id ASC, label ASC` so per-issue slices are stable and alphabetical.

## Project label cache (commit 3)

Lives on `Model` keyed by `projectID`:

```go
type labelCache struct {
    byProject map[int64]labelCacheEntry
}

type labelCacheEntry struct {
    labels   []LabelCount  // wire shape: {label, count}
    gen      int64         // dispatch generation tag
    pid      int64         // pid this entry is for (defensive, redundant with map key)
    err      error         // last fetch error (surfaced by suggestion menu)
    fetching bool          // in-flight indicator
}
```

`Model.nextLabelsGen` is a monotonic counter. Every dispatch:
1. Increments `nextLabelsGen`.
2. Stamps `cache.byProject[pid].gen = nextLabelsGen` AND `cache.byProject[pid].fetching = true` **at dispatch time** (before issuing the HTTP request). Without this, two concurrent dispatches (gen=5 then gen=6) where the older response arrives first would see stale cache.gen and accept gen=5 wrongly.
3. Tags the request with the dispatch gen.

Acceptance check on response:
- `response.gen >= cache.byProject[pid].gen` (no newer dispatch in flight).
- `response.pid == targetPID(m)` where `targetPID(m)` = `m.detail.scopePID` when a detail is open and `m.scope.projectID` otherwise. Forward-compatible with all-projects/split-mode where the two can diverge.

Cache invalidation triggers (each calls `dispatchLabelFetch(pid)`):
- Lazy: first read from a consumer that **actually uses the suggestion menu**. In commit 3 that is the detail `+` prompt; in commit 5b that is the filter modal Labels axis when not in all-projects scope. The new-issue form (commit 4) accepts free-typed comma-separated labels only and does NOT trigger a lazy fetch — the suggestion-menu wire-up for the form is deferred (see "What gets dropped / deferred" in commit 4).
- Local mutation success: label add, label remove, create-with-labels.
- SSE: `issue.labeled` / `issue.unlabeled` events whose project matches an entry in `byProject`. Reuses existing SSE event-routing path. **Important**: this invalidates the project label suggestion cache only — list/detail refetch on label events is a separate existing SSE behavior and shouldn't be coupled.

## Detail header & chip rendering (commit 2)

Three rows replace the current single header line. Renders inside the existing `statsLineStyle` chrome strip family.

```
 #42 · open · wesm · created 3h ago · updated 1h ago             ← meta line
 Owner: alice                          [bug] [prio-1] [needs-design] +2  ← assignment line
 fix login bug on Safari                                                 ← title row (bold, full width)
── body ──────────────────────────────────────────────────────────
 (body content)
── activity ──────────────────────────────────────────────────────
[ Comments (4) ]  Events (2)  Links (1)
 (tab content)
```

### Fixed-row budget

`detail_render.go` fixed-row budget bumps from 7 to 9. Explicit row breakdown:

| Row | Content |
|-----|---------|
| 1 | title bar (`kata かた · …`) |
| 2 | meta (`#42 · open · author · created · updated`) |
| 3 | assignment (`Owner: alice          [bug] [prio-1] +N`) |
| 4 | title row (bold, full width) |
| 5 | body rule (`── body ──────────────`) |
| —  | (body content, variable height) |
| 6 | activity rule (`── activity ──────────`) |
| 7 | tab strip (`[ Comments (4) ]  Events (2)  Links (1)`) |
| —  | (tab content, variable height) |
| 8 | info line (panel prompt or scroll indicator or flash) |
| 9 | footer (help row) |

Total fixed = 9 rows. Variable content (body + tab content) splits the remaining `height - 9` rows. **No separate tab rule** — the activity rule above the tab strip is the visual frame; adding a tab rule below would push the budget to 10 and isn't necessary. Without the budget bump from 7 to 9, the new rows push the footer off-screen.

### Assignment line

- Owner left: `Owner: <name>` or `Owner: —` placeholder when nil.
- Label chips right: alphabetical, packed left-to-right, `+N` overflow indicator. Placeholder when no labels: `(no labels)`.

### `renderLabelChips(labels []string, available int) string`

- Sort `labels` alphabetically.
- For each label: **rendered text uses `textsafe.Block(label)`** so terminal control chars / ANSI / Unicode-format runes never reach the header. Width measure uses `runewidth.StringWidth(textsafe.Block(label))` (sanitized first, then measured) — measuring a stripped label while rendering raw label would still leak control text into the header even if width math is correct.
- Each chip = `[<sanitized-label>]` with one space separator: chip width = sanitized-rune-width + 3.
- Pack until the next chip would push over `available`. The `+N` token (`+%d` chars) reserves its own width in the budget calculation so we don't loop trying to fit it.
- If `available < shortestChipWidth`, collapse to `[N labels]` ultra-narrow degraded render.

### Labeled rule helper

Width-safe: if `width < utf8.RuneCountInString("── label ──")`, fallback to a plain dash run or `── lbl ──` truncation. Never invokes `strings.Repeat` with a negative count.

```
── body ──────────────────────────────────────────────────────────
── activity ──────────────────────────────────────────────────────
```

### Tab strip polish

No structural change to `renderTabStrip` — the labeled `── activity ──` rule above gives it the visual frame it currently lacks.

## Autocomplete UX (commit 3)

Vertical menu, right-anchored, rendering on rows above the info-line prompt. Splices over the body bg via a new placement helper (`overlayAtCorner` or similar) — does NOT reuse `overlayModal` directly because that helper centers, and the suggestion menu is right-anchored.

```
                            ┌──────────────────┐
                            │ bug      (12)    │  ← highlighted
                            │ blocker  (3)     │
                            │ needs-design (1) │
                            └──────────────────┘
 add label to #42: b▌                                 ← info-line prompt
 enter commit · esc cancel · ↑↓ pick · ⇥ complete    ← footer
```

### `inputState` additions for autocomplete

```go
// Added to inputState (existing fields preserved).
target           formTarget  // generalized from M4; carries projectID + issueNumber + detailGen.
suggestHighlight int         // index into filtered suggestions slice.
suggestScroll    int         // scroll offset when filtered list > visible window.
```

`formTarget` (already defined in input.go for M4) generalized to apply to label prompts as well — so `-` carries issue identity and detailGen for stale safety, not only `+`.

### Update routing

Autocomplete keys intercepted **BEFORE** `textinput.Update` delegation:

- `↑` / `↓`: move highlight; wrap at top/bottom; buffer unchanged.
- `tab`: complete buffer to highlighted suggestion's full text; cursor lands at end.
- `enter`: commit current buffer (suggestion or free-typed or empty; empty is a no-op per existing behavior).
- `esc`: close prompt and menu.
- Everything else: delegate to textinput.Update for cursor/paste/backspace.

### Filtering & sources

- `+` source: `m.projectLabels.byProject[targetPID].labels` (project cache).
- `-` source: `dm.issue.Labels` (currently-attached labels). Relies on commit 1's `Issue.Labels` wiring + post-mutation issue refetch keeping labels fresh.
- Filter: prefix-match (`strings.HasPrefix`, case-insensitive).
- Sort: count desc, label asc (most-used first; alphabetical tiebreak).
- Empty buffer: shows top project labels (no filter, sorted by count desc).
- All-projects scope: `+` menu disabled (cache lookup ambiguous); free-typed only.

### Loading & error states

Menu placeholder text:
- `cache.fetching && len(cache.labels) == 0`: `loading…`
- `cache.err != nil`: `(error: <message>)`
- `len(cache.labels) == 0 && !cache.fetching && cache.err == nil`: `(no labels in project — type to add)`

### Layout: menu height counts against budget

The menu overlays body/tab rows. Detail layout subtracts the **actual rendered menu height** (including border rows top+bottom and any placeholder rows for loading/error/empty states) from the tab/body budget. NOT `min(menuHeight, displayedSuggestions)` — that would undercount by 2 (border rows) for a bordered menu and the scroll indicator would still lie about overflow by those rows.

Concretely: `actualMenuHeight = topBorder(1) + max(displayedSuggestions, placeholderRows) + bottomBorder(1)`. This number is what gets subtracted. Without this:
- Scroll indicator can lie about overflow.
- The labeled `── activity ──` rule can get pushed off-screen when the menu opens.

## New-issue form (commit 4)

Replaces M3.5c inline row + M4 post-create chain. New `inputKind`: `inputNewIssueForm`. Drops `inputNewIssueRow` and `inputBodyEditPostCreate` along with their constructors, render paths, tests, and the post-create chain in `routeMutation`.

### Render shape

Centered modal, single column (Q3: A). Bordered via `modalBoxStyle`.

```
        ┌─ new issue ──────────────────────────────────────────────────┐
        │                                                              │
        │  Title  *                                                    │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ fix login bug on Safari▌                                 ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  Body                                                        │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ Reproduces in Safari 17 only.                            ││
        │  │ Click the login button twice.                            ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  Labels                                                      │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ bug, prio-1                                              ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  Owner                                                       │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ alice                                                    ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  ⇥ next field · ⌃s save · ⌃e $EDITOR (body only) · esc       │
        └──────────────────────────────────────────────────────────────┘
```

### Field shapes

| Index | Field | Backing | Required | Notes |
|-------|-------|---------|----------|-------|
| 0 | Title | `textinput.Model` | yes | Non-empty after TrimSpace |
| 1 | Body | `textarea.Model` | no | `ctrl+e` opens $EDITOR |
| 2 | Labels | `textinput.Model` | no | Comma-separated; normalized on submit |
| 3 | Owner | `textinput.Model` | no | Whitespace-only → omitted; non-empty raw |

Constructor blurs all fields, then focuses field 0 explicitly. `inputNewIssueForm` added to `isCenteredForm()` so the form renders via the existing centered overlay path.

### Update routing

- `tab` / `shift+tab`: cycle `active` with wrap. Calls `Blur()` on leaving field, `Focus()` on entering field.
- `ctrl+s`: commit. Validates Title non-empty after TrimSpace; if empty → `s.err = "title is required"`, no dispatch. Otherwise: `s.saving = true`, dispatch with normalized payload, block subsequent `ctrl+s` while saving (existing M4 saving-gate pattern).
- `esc`: cancel — closes form, returns to list. No auto-detail-open (different from M4 post-create chain). Discards typed content.
- `ctrl+e`: only when `active == 1` (Body). Other fields ignore. Editor return writes back into textarea per existing M4 routing. `editorKindFor` adds entry for `inputNewIssueForm` body field.
- `enter` in single-line fields (Title/Labels/Owner): advances to next field; does NOT commit. Avoids accidental mid-form submits.
- `enter` in Body (multi-line): inserts newline, delegated to textarea.

### Normalization (NOT sanitization — wire payloads stay raw)

- Title: TrimSpace for the empty-check gate, but the wire value is sent untrimmed (preserves intentional whitespace, matching M3.5c behavior).
- Body: sent raw.
- Labels: `strings.Split(buf, ",")`, then per-token TrimSpace, then drop empty entries. The resulting slice is the wire value (no display sanitization on the wire).
- Owner: TrimSpace; if empty after trim, omit the `owner` key from the payload (`*string` nil); otherwise send raw.

### Wire dispatch

`dispatchCreateIssue` extended:

```go
func (lm listModel) dispatchCreateIssue(
    api listAPI, sc scope,
    title, body string, labels []string, owner *string,
) (listModel, tea.Cmd)
```

### Commit-path routing — `commitInput` and `routeFormMutation` are different layers

Two different events to route, and they happen at different layers — getting this wrong was a real risk in earlier drafts. Be explicit:

**Layer A: key-driven commit (`commitInput`)** — fires when the user presses `ctrl+s` (or `enter` in non-form inputs). The active `inputState` decides what to do. Branch order matters because `inputFilterForm` is in `isCenteredForm()` and would otherwise fall through to the saving/mutation path:

```go
func (m Model) commitInput() (Model, tea.Cmd) {
    switch {
    case m.input.kind == inputFilterForm:
        // Filter apply — no daemon mutation; sets local filter, refetches list.
        return m.commitFilterForm(m.input)
    case m.input.kind.isCenteredForm():
        // Body editor / comment form / new-issue form — has a daemon mutation;
        // sets saving=true, dispatches mutation, awaits routeFormMutation.
        return m.commitFormInput()
    case m.input.kind.isPanelPrompt():
        // Existing M3b behavior.
        return m.commitPanelPrompt()
    case m.input.kind.isCommandBar():
        // Existing M3a behavior.
        return m.commitCommandBar()
    }
    return m, nil
}
```

**Layer B: mutation-response routing (`routeFormMutation`)** — fires when a `mutationDoneMsg` arrives for a form-originated mutation. Filter form has NO mutation, so it never reaches this layer. Branch order:

```go
func (m Model) routeFormMutation(mut mutationDoneMsg) (Model, tea.Cmd) {
    switch m.input.kind {
    case inputNewIssueForm:
        // Clear form; route through list create handling
        // (lm.applyMutation(kind="create") to seed selectedNumber + refetch).
        // Does NOT reclassify as detail.
    case inputBodyEditForm, inputCommentForm:
        // Existing M4 behavior — reclassify as detail-side mutation.
    }
    // inputFilterForm intentionally absent from this switch — filter has
    // no daemon mutation; it shouldn't arrive here.
}
```

Post-create chain in `routeMutation` (model.go:306) **removed** — otherwise a successful create still opens `inputBodyEditPostCreate` on top of the cleared form.

### All-projects gate

`n` is a no-op when `m.scope.allProjects` is set; show toast hint matching the existing `list.go:353` pattern.

### What gets dropped

- `inputNewIssueRow` enum value, `newNewIssueRow()` constructor, `renderBodyWithNewIssueRow()` (~70 lines of `list_render.go`).
- `inputBodyEditPostCreate` enum value, `newBodyEditPostCreate()` constructor.
- Post-create chain in `routeMutation` (model.go:306).
- `openDetailFromTarget` helper in `cancelInput`.
- Tests: `TestList_NewIssueRow_*` (5 tests), `TestPostCreate_*` (3 tests), `TestSnapshot_List_NewIssueRow`.
- `dispatchCreateIssue`'s old 3-arg signature.

### Label autocomplete in this form

Deferred. The Labels field is comma-separated text only in v1. When commit 3's suggestion menu lands first, commit 4's Labels field becomes the third consumer of the same menu component (rendering above/below the active field) once we wire it. Per "don't build a separate autocomplete path just for create."

## Filter modal (commits 5a + 5b)

### Commit 5a — modal-ify Status/Owner/Search; drop `o` key

New `inputKind`: `inputFilterForm`. Three fields, single column.

```
        ┌─ filter issues ──────────────────────────────────────────────┐
        │  Status                                                      │
        │  ◯ all   ◉ open   ◯ closed                                   │
        │                                                              │
        │  Owner                                                       │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ alice▌                                                   ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  Search                                                      │
        │  ┌──────────────────────────────────────────────────────────┐│
        │  │ login bug                                                ││
        │  └──────────────────────────────────────────────────────────┘│
        │                                                              │
        │  ⇥ next field · ⌃s apply · ⌃r reset · esc cancel             │
        └──────────────────────────────────────────────────────────────┘
```

**Status**: tri-state radio (all / open / closed). `←`/`→` cycle when active; `space` toggles to next state. `◉` / `◯` glyphs; falls back to `[X]` / `[ ]` under `KATA_COLOR_MODE=none`.

**Owner** and **Search**: single-line textinputs. Pre-filled from `lm.filter` so user can refine without retyping. Snapshot preserved in `s.preFilter` for `esc` restore.

### Update routing

- `tab` / `shift+tab`: cycle 3 fields with wrap.
- `ctrl+s`: apply via dedicated `commitFilterForm(form inputState) (Model, tea.Cmd)` path. Sets full `lm.filter` from form fields, **resets cursor to 0 and clears `selectedNumber`** (matches existing `s` cycle / `c` clear convention — a filter change loses prior selection because the prior selected issue may no longer match), clears `lm.status`, dispatches `refetchCmd`. Does NOT call `applyLiveBarFilter` (which only mirrors a single active field). The filter-modal apply does NOT preserve cursor index — it's a deliberate filter change, treat the result list as a fresh view.
- `ctrl+r`: reset form fields only; `s.preFilter` intact so `esc` still restores filter to its at-open snapshot.
- `esc`: restore `lm.filter` to `preFilter`; close form.
- **`commitInput` branches `inputFilterForm` BEFORE the centered-form (`isCenteredForm`) check** — see "Commit-path routing" above. The filter form is in `isCenteredForm()` for render purposes (overlay panel), but its commit path is filter apply, not mutation dispatch. Without the explicit branch in `commitInput`, ctrl+s would fall into `commitFormInput` and set `saving=true`, then sit forever waiting for a mutation that never arrives.

### Keymap changes

- New: `f` opens filter modal.
- Removed: `o` (owner is now a modal axis only).
- Kept: `s` (status cycle), `/` (search bar), `c` (clear all filters).
- List footer: replace `o owner` with `f filter`. `listFooterItems`, `listFooterItemsFor`, and help screen updated.

### Commit 5b — Labels axis (needs daemon work)

Adds a fourth field to the filter modal. Depends on:

1. **Daemon** (`internal/daemon/handlers_issues.go`):
   - `db.LabelsByIssues(ctx, projectID, issueIDs)` (specs above).
   - `api.IssueOut` struct embeds `db.Issue`, adds `Labels []string json:"labels,omitempty"`.
   - List handler builds `[]IssueOut` from `ListIssues` result + `LabelsByIssues` lookup.

2. **API wire** (`internal/api/types.go`):
   - `ListIssuesResponse.Body.Issues` becomes `[]IssueOut`.

3. **TUI**:
   - `Issue.Labels` field tag (already added in commit 1) — list decode populates naturally.
   - Filter modal Labels field: multi-select chip input. Renders selected labels as removable chips; tab into the field to add (uses suggestion menu from commit 3, sourced from project label cache).
   - `ListFilter.Labels` (already exists in client_types.go:41) starts being used by `matchesFilter` for client-side filtering. Multi-label semantics: any-of (issue matches if it has ≥1 of selected labels).
   - **`filteredIssues` fast path**: include `len(f.Labels) == 0` in the early-return condition (currently ignores Labels because it never expected the field to be populated).
   - **`renderChips`**: remove the "label chips intentionally omitted" comment; add `[label]` chips to the chrome strip alongside the others. Same chip styling as the detail header.
   - **All-projects scope**: disable the label suggestion menu (cache lookup ambiguous when `m.scope.allProjects`). Labels field still accepts free-typed values and filters client-side via the per-issue `Labels` from list decode.
   - **Terminology**: filter-modal context calls them "selected labels" (the user's filter selection), not "attached labels" (which refers to issue-side state).

### CLI follow-up (defer)

`kata list` could show labels in human output now that they're available. Defer; separate cleanup outside this plan.

## Test surface

### Commit 1 (decode show labels + parity dispatch on detail-open)

- Unit: `Client.showIssue` populates `resp.Issue.Labels` from `body.Labels`; alphabetical sort.
- Unit: `handleOpenDetail` dispatches the existing `fetchIssue` helper alongside the three tab fetches (gen-tagged), bringing it to parity with `handleJumpDetail` (which already dispatches it). Confirms no NEW fetch helper is introduced.
- Unit: `applyFetched` on `detailFetchedMsg` replaces `dm.issue.Labels` with the show-response slice.
- Race: stale `detailFetchedMsg` (gen mismatch) drops cleanly.

### Commit 2 (header + dividers + chip rendering)

- Unit: `renderLabelChips` sorting (alphabetical), packing, `+N` overflow, ultra-narrow `[N labels]` fallback.
- Unit: chip width math uses `runewidth.StringWidth(textsafe.StripANSI(label))` so wide-glyph and ANSI-injected labels are measured correctly.
- Unit: labeled rule helper exact-width; narrow-fallback when width < min.
- Snapshot: full detail view with labels (chips visible), without labels (placeholder), wide-glyph label rendering, ultra-narrow degraded render.
- Existing detail snapshots updated for the new fixed-row count (9 instead of 7) + assignment line + activity divider.

### Commit 3 (project label cache + autocomplete)

- Unit: cache populates from `LabelsListResponse`; sort is **count desc, label asc**.
- Unit: dispatch stamps `entry.gen` and `fetching = true` BEFORE the HTTP request.
- Race: stale fetch (`response.gen < cache.gen`) dropped on arrival.
- Race: project switch in flight (`response.pid != targetPID`) dropped.
- Race: SSE `issue.labeled` / `issue.unlabeled` invalidates **the project-label suggestion cache** (marks stale or refetches it). List/detail refetch on label events is a separate existing SSE behavior, distinct from this test.
- Unit: prefix filter (case-insensitive); count-desc sort; tiebreak alphabetical.
- Unit: `↑`/`↓` move highlight, wrap at boundaries.
- Unit: `tab` completes buffer to highlighted suggestion; cursor at end.
- Unit: `enter` commits with current buffer (suggestion or free-typed).
- Unit: `esc` closes prompt and menu.
- Unit: empty buffer shows top project labels (no filter).
- Unit: `-` source is `dm.issue.Labels`, not project cache.
- Snapshot: prompt with menu open (5 suggestions, highlighted first).
- Snapshot: prompt with `loading…` placeholder, `(error)` placeholder, `(no labels)` placeholder.
- Snapshot: scroll within menu when >8 matches.
- Layout: menu height counted against tab/body budget — scroll indicator reflects what's actually visible.

### Commit 4 (new-issue form)

- Unit: `n` opens `inputNewIssueForm`; all-projects scope is no-op (toast hint).
- Unit: form constructor blurs all fields, focuses field 0.
- Unit: `tab` / `shift+tab` cycle 4 fields with wrap; focus shifts (Blur/Focus delegated to bubbles models).
- Unit: `enter` in single-line fields advances; `enter` in body inserts newline.
- Unit: `ctrl+s` with empty title sets in-form err, no dispatch.
- Unit: `ctrl+s` with title only succeeds; `CreateIssueBody` has Title set, Owner/Labels nil/empty.
- Unit: `ctrl+s` with all fields populated; payload normalized (labels TrimSpace + drop empty; whitespace owner → omitted; non-empty owner → raw).
- Unit: `ctrl+e` only when `active == 1` (Body); other fields ignore.
- Race: stale editorReturnedMsg (formGen mismatch) dropped (existing form pattern).
- Unit: form mutation success routes through list create handling, NOT detail; `selectedNumber` seeded; refetch dispatched.
- Unit: form mutation failure leaves form open with `s.err`, `saving = false`.
- Snapshot: form rendered (4 fields, footer hint, narrow-collapse hint).
- Negative: `inputNewIssueRow`, `inputBodyEditPostCreate`, related code/tests removed from codebase (`grep` confirms gone).
- Negative: post-create chain in `routeMutation` removed (`grep` for `openBodyEditPostCreate` returns no callers).

### Commit 5a (filter modal)

- Unit: `f` opens `inputFilterForm`; all-projects scope rendering still works.
- Unit: 3 fields cycle via `tab` with wrap; `←`/`→`/`space` cycle Status tri-state.
- Unit: `ctrl+s` builds full `ListFilter` from fields, calls dedicated `commitFilterForm` path (not `applyLiveBarFilter`); zeroes `selectedNumber`; **resets cursor to 0**; clears status; dispatches refetch.
- Unit: `ctrl+r` resets form fields only; `preFilter` intact for `esc` restore.
- Unit: `esc` restores `lm.filter` to `preFilter`.
- Unit: **`commitInput` branches `inputFilterForm` BEFORE the centered-form `commitFormInput` path**, so ctrl+s on the filter modal does NOT set `saving=true`. (Distinct from `routeFormMutation` — filter form has no mutation; it never reaches that layer.)
- Unit: `o` key gone from keymap; help screen no longer mentions it.
- Snapshot: filter modal rendered (3 fields, status radio, footer hint).
- Snapshot: chip strip in chrome reflects modal-applied filters.

### Commit 5b (label axis + daemon)

- Daemon unit: `db.LabelsByIssues` returns empty on empty input; constrains by projectID; orders by issue+label.
- Daemon integration: list handler builds `[]IssueOut` with per-issue labels; verified via JSON shape test.
- TUI unit: `Client.listIssues` decode populates `Issue.Labels` from new `IssueOut` wire shape.
- Unit: `filteredIssues` fast path includes `len(f.Labels) == 0` in early-return.
- Unit: `matchesFilter` any-of label semantics (issue with `[bug, prio-1]` matches filter `[bug]`, filter `[bug, foo]`; doesn't match `[foo]`).
- Unit: filter modal Labels field uses suggestion menu in single-project; disabled in all-projects scope (free-typed only).
- Unit: `renderChips` includes label chips (alphabetical).
- Snapshot: chrome chip strip with label filter active; filter-modal with selected label chips.
- (Note: no snapshot of "list view with label chips on rows" — the list table is not gaining a label column in 5b. List behavior is exercised by decode + filter unit tests.)

## Open follow-ups (out of scope)

- Author and `include_deleted` filter axes (need wire support).
- CLI `kata list` showing labels in human output.
- Owner autocomplete in new-issue form / filter modal (would need a `GET /owners` daemon endpoint; defer).
- Draft preservation on Esc for the new-issue form.
- Rich label chip input in the form (vs comma-separated text) — would unify with the suggestion menu component.
