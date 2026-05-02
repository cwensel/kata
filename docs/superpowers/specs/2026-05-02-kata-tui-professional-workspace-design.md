# Kata TUI Professional Workspace Design

**Status:** Approved high-level direction by Wes on 2026-05-02. Implementation plan to follow.

**Goal:** Make `kata tui` feel like a polished professional issue workspace, with consistent chrome, complete contextual hints, stronger detail hierarchy, in-app forms, and a shallow expandable queue for child issues.

**Visual references:**

- `.superpowers/brainstorm/14736-1777756042/tui-target-experience-v1.html`
- `.superpowers/brainstorm/14736-1777756042/tui-child-issues-v1.html`

These companion mockups are local design artifacts, not production assets. They show the target shape for wide split mode, stacked detail, centered forms, label autocomplete, and expandable child rows.

---

## Locked Decisions

1. **Use the professional workspace direction.** The TUI should feel like a quiet, focused issue management application. It should not become a dense cockpit or command center yet, and it should not stop at minor cosmetic cleanup.
2. **Keep the working internals.** The Bubble Tea model, typed daemon client, SSE invalidation, identity-based cursor behavior, sanitization, and split/stacked layout machinery remain the foundation.
3. **Adopt a shallow expandable queue for child issues.** The queue defaults to top-level issues. Rows with children show a disclosure glyph and child progress count. Expanding a row reveals direct children inline. Deeper descendants are handled by expanding one level at a time.
4. **Show hierarchy in detail too.** Detail view always shows parent context when present and a dedicated Children section for direct children.
5. **Complete contextual hint bars.** Every main screen, pane focus, tab, form, prompt, and modal must render a complete relevant hint row. `?` remains the full help screen, but the footer must not be incomplete.
6. **Use in-app input as the primary flow.** `$EDITOR` remains available only as an escape hatch for long body/comment fields.

## Problems To Fix

The current TUI has good data plumbing but still reads like an implementation harness in places:

- The application frame is inconsistent. List, split, detail, help, prompts, and forms do not all share the same visual language.
- The issue detail view has useful sections, but it lacks a complete information architecture for hierarchy, parent/child relationships, and context-specific actions.
- Footer hint bars are incomplete in detail and prompts. Users cannot trust the persistent help row as the source of what works now.
- Split mode lacks enough visual structure: pane titles, gutter, focus state, detail follow behavior, and footer semantics should feel deliberate.
- The list is flat. Parent/child decomposition is invisible during triage unless the user opens individual issues and inspects links.
- Input shells are uneven. Forms, panel prompts, and command bars need one consistent grammar.
- The palette and spacing are serviceable but still too close to test fixture output.

## Product Shape

The redesigned TUI has four core surfaces.

### Application Frame

Every main view renders the same frame:

1. **Top title strip:** `kata / project <name>` on the left, counts and version on the right.
2. **State strip:** live/SSE state, last sync age, actor, active view mode, and filter chips.
3. **Body:** list, split panes, detail, empty state, or help overlay.
4. **Info line:** flash, error, reconnect state, stale/refetch state, scroll range, or active prompt.
5. **Footer hint row:** context-specific key hints for the focused pane or active input.

The frame should behave like window chrome. It should be visually stable, width-safe, and present even when there is no data.

### Issue Queue

The queue is the main triage surface. It should be readable as a table, but not look like a raw ASCII dump.

Default queue behavior:

- Show top-level issues by default, where "top-level" means no `parent` link from this issue to another issue.
- Rows with direct children show `▸` when collapsed and `▾` when expanded.
- Expanded rows show direct children immediately below the parent, indented one level.
- Child rows can themselves show `▸/▾` if they have children, but the queue only expands one level per user action.
- Cursor movement treats visible parent and child rows as a single flat visible row list.
- Selection remains identity-based, not index-based. The identity is `(project_id, issue_number)`.
- Parent expansion state is keyed by `(project_id, issue_number)` and survives refetches while the issue still exists.

Suggested columns:

```text
  tree  #     status   title                         owner      labels     updated   kids
  ▾     #42   open     TUI polish pass               wesm       ux,tui     5m ago    2/5
    ├   #43   open     detail hint bars incomplete   claude     ux         1h ago    -
    └   #45   open     split-pane focus polish       wesm       tui        2h ago    1/2
  ▸     #38   open     hooks event stream hardening  -          daemon     1d ago    0/3
        #37   open     search dedupe behavior        wesm       search     2d ago    -
```

Narrow and split-list variants may drop owner and labels first, then compress title, but they must keep:

- disclosure glyph
- issue number
- status
- title
- updated time
- child progress when a row has children

Child progress:

- Render `openChildren/totalChildren`, for example `2/5`.
- Use `-` or blank only when total children is zero.
- Progress counts include direct children only in the first implementation.
- Do not block rendering on recursive counts.

Filtering:

- Tree mode should fetch an all-status working set for the current project and apply status/search/owner/label filters client-side. Ancestor context cannot be rendered correctly if the daemon returns only open rows and a matching child's parent is closed.
- Filtering applies to issue rows, but ancestors of matched children should remain visible as context when tree mode is active.
- If a child matches a search/filter but its parent does not, render the parent as a context row and auto-expand enough to show the match.
- Context rows should be visually subdued if they do not themselves match.
- `c clear` resets filters, not expansion state.
- Add a future `z collapse all` or `Z expand matching` only if needed; do not include in the first pass unless implementation proves simple.

Keyboard:

```text
j/k or up/down     move visible row cursor
space              expand/collapse row with children
enter              stacked: open detail; split: focus detail
tab                switch pane in split mode
esc                return focus to list from detail pane
n                  new issue
N                  new child of selected issue
p                  set/change parent from detail
f                  filter form
/                  search
s                  cycle status
c                  clear filters
x/r                close/reopen selected issue
?                  help
q                  quit
```

`N new child` should open the same new issue form, prefilled with an initial parent link to the selected issue. The form title should read `new child issue`.

### Split Mode

Split mode remains the wide-terminal experience:

- Breakpoint should stay conservative: use the existing split breakpoint unless visual testing shows it is too cramped.
- Left pane is the expandable issue queue.
- Right pane is detail for the highlighted row.
- Detail follows cursor in list focus.
- `enter` or `tab` moves focus to detail. `esc` returns focus to the queue.
- Focus is indicated redundantly: active border, pane title, and footer hint text.
- The footer always reflects the focused pane.

Pane chrome:

- Both panes get titles.
- List pane title: `issues` plus `tree focus` or `list focus`.
- Detail pane title: `#42 · open · owner wesm` plus updated age.
- Use a visible gutter between panes. Do not let borders touch.

### Detail View

Detail view should be structured around issue understanding, not just activity tabs.

Header:

```text
#42 · open · author wesm · created 3h ago · updated 5m ago
Owner: alice                                      [bug] [prio-1] [needs-design]
Parent: #12 workspace polish                     Children: 2 open / 5 total
fix login bug on Safari
```

Rules and sections:

```text
-- body ------------------------------------------------------------
...
-- children 2 open / 5 total --------------------------------------
> #43 open    detail hint bars incomplete             alice   1h ago
  #44 closed  new issue form labels                   wesm    2h ago
  #45 open    split-pane focus polish                 wesm    3h ago
-- activity --------------------------------------------------------
[ Comments (4) ]  Events (12)  Links (7)
...
```

The Children section:

- Shows direct children of the current issue.
- Appears between body and activity.
- Uses a compact table with issue number, status, title, owner, updated, and optional child progress.
- Has its own cursor when focused through the detail pane's row navigation.
- `enter` on a child jumps to that child detail, using the existing detail navigation stack.
- Empty state reads `(no child issues)`, but only if vertical space allows; otherwise omit the section body and keep the parent/children summary in the header.

Activity tabs:

- Keep Comments, Events, Links.
- Links tab remains the generic relationship escape hatch.
- Parent and children are promoted out of Links because they are core structure, not miscellaneous links.
- The Links tab still shows `parent`, `blocks`, and `related` raw link rows for complete auditability until a future relationship-specific UI replaces it.

Footer hints must change by tab and focus:

- Comments tab: `j/k move`, `c comment`, `e edit`, `x close`, `+ label`, `a owner`, `tab next`, `esc back`.
- Children section focus: `j/k child`, `enter open child`, `N new child`, `p parent`, `tab activity`, `esc back`.
- Links tab: `j/k move`, `enter jump`, `L link`, `p parent`, `b blocker`, `tab next`, `esc back`.
- Active prompts and forms replace the row with commit/cancel/navigation hints.

### Forms And Prompts

Forms must look and behave like part of the app:

- Centered forms for new issue, new child, edit body, and comment.
- Panel-local or info-line prompts for short actions like add label, remove label, assign owner, set parent, add blocker, add link.
- Inline command bar for search.
- Filter form for status, owner, search, and labels.

New issue form fields:

```text
Title *
Body
Labels
Owner
Parent
```

For `n new issue`, Parent is empty by default.

For `N new child`, Parent is prefilled with the selected issue and shown as a fixed field unless the user explicitly clears it. If the current selection is itself a child, this still creates a child under the selected issue, not under the selected issue's parent.

The existing create endpoint already supports initial links. The TUI `CreateIssueBody` should grow a `Links []CreateInitialLinkBody` or equivalent client-side DTO so `N` can create the child and parent link in one request.

## Data And API Implications

Current state:

- `parent` links are stored as `child -> parent`.
- Each child has at most one parent.
- `show issue` returns links touching the issue.
- The list endpoint is flat but now includes labels.

The queue needs relationship metadata without per-row N+1 detail fetches.

Preferred daemon addition:

```go
type IssueOut struct {
    db.Issue
    Labels []string `json:"labels,omitempty"`
    Parent *IssueRef `json:"parent,omitempty"`
    ChildCounts *ChildCounts `json:"child_counts,omitempty"`
}

type IssueRef struct {
    Number int64 `json:"number"`
    Title string `json:"title"`
    Status string `json:"status"`
}

type ChildCounts struct {
    Open int `json:"open"`
    Total int `json:"total"`
}
```

This keeps the existing list response flat while giving the TUI enough data to:

- hide non-matching children in collapsed top-level mode
- compute top-level rows
- show disclosure glyphs
- render direct child progress
- preserve expansion across refetches
- filter tree rows client-side while keeping ancestor context

Detail needs direct child rows. Preferred addition to `show issue`:

```go
type ShowIssueResponse struct {
    Body struct {
        Issue db.Issue `json:"issue"`
        Comments []db.Comment `json:"comments"`
        Links []LinkOut `json:"links"`
        Labels []db.IssueLabel `json:"labels"`
        Parent *IssueRef `json:"parent,omitempty"`
        Children []IssueOut `json:"children,omitempty"`
    }
}
```

If daemon changes are too large for the first implementation, acceptable fallback:

- Build tree metadata client-side from list rows only after adding `ParentNumber` to list rows.
- Keep the detail Children section behind the `Links` data initially, but only as a short-lived bridge. Do not fetch every linked issue one-by-one in the steady-state renderer.

DB helpers likely needed:

- `ParentRefsByIssues(ctx, projectID, issueIDs []int64) map[int64]IssueRef`
- `ChildCountsByParents(ctx, projectID, parentIssueIDs []int64) map[int64]ChildCounts`
- `ChildrenOfIssue(ctx, projectID, parentIssueID int64) []IssueOut`

All helpers must constrain by `project_id`.

SSE invalidation:

- `issue.linked` and `issue.unlinked` with `type=parent` invalidates list tree metadata.
- Parent link changes for the open detail issue refetch detail.
- Parent link changes where the open detail issue is the parent also refetch detail, so the Children section stays fresh.
- Existing list refetch debounce remains; do not add per-child ad hoc refetch loops.

## Visual Language

Palette:

- Keep adaptive light/dark support.
- Move away from large background slabs that make snapshots look like filled test output.
- Use restrained surfaces: title strip, state strip, pane backgrounds, active row, and focused border.
- Use color to encode state, not decoration.

Status colors:

- open: green
- closed: cyan
- deleted: muted red
- live/synced: green dot
- reconnecting/stale: gold
- errors: red
- focus/accent: magenta or purple, matching the existing roborev-inspired focus color

Spacing:

- Keep dense operational layouts.
- Use one blank line only where it improves section separation.
- Prefer titled rules over repeated heavy borders in detail.
- Use a gutter between split panes.

No-color mode:

- Active row must still show `>` or `›`.
- Active tab must still use brackets.
- Expanded/collapsed state must use visible glyphs or ASCII fallback.
- Focused pane must have textual title/focus indication, not only colored border.

## Help System

The keymap and help screen must be treated as a contract.

Requirements:

- Add a focused-pane/footer help matrix and test every context.
- The full `?` help screen must group keys by Global, Queue, Tree, Detail, Children, Forms, and Filters.
- The persistent footer is not a subset generated by hand in each renderer. It should come from context-aware helpers so new bindings do not drift.
- Snapshot tests should cover list, split list focus, split detail focus, detail comments, detail children, label prompt, parent prompt, new issue form, new child form, and filter form.

## Suggested Implementation Phases

These phases are intentionally larger than the eventual implementation plan tasks. The plan doc should split each phase into test-first commits.

1. **Frame and visual system pass**
   - Normalize title strip, state strip, info line, footer row, pane titles, split gutter, and focus treatment.
   - Introduce a context-aware footer hint helper.
   - Update help screen structure.

2. **Queue row model and tree metadata**
   - Add relationship metadata to list DTOs.
   - Build visible queue rows from flat issue data plus expansion state.
   - Preserve cursor identity and expansion state through refetches.

3. **Expandable queue rendering and input**
   - Render disclosure glyphs, indentation, child progress, context rows for filtered children, and footer hints.
   - Add `space` expand/collapse and `N` new child.

4. **Detail hierarchy**
   - Add parent and children data to show issue response.
   - Render parent summary and Children section.
   - Add child row navigation and enter-to-jump.

5. **Forms and prompts polish**
   - Add Parent field to new issue form.
   - Make `N` prefill parent link and submit through create-with-initial-link.
   - Audit every prompt and footer hint.

6. **Aesthetic final pass**
   - Update golden snapshots.
   - Verify no text overlap at narrow, normal, and wide sizes.
   - Run color-none snapshots.
   - Manually compare against the visual companion mockups.

## Acceptance Criteria

The redesign is complete when:

- The queue feels like the primary workspace, not a raw list.
- Parent issues with children can be expanded and collapsed in the queue.
- Child progress counts render without N+1 detail fetches.
- Detail shows parent context and direct children without forcing users into the Links tab.
- The footer hint row is complete and correct in every visible context.
- Split mode has clear pane titles, focus state, gutter, and context-specific hints.
- Forms are in-app by default and visually consistent.
- No rendered agent/user text can inject terminal control sequences.
- No tested viewport has overlapping text, clipped controls, or footer drift.
- `go test ./internal/tui ./internal/daemon ./internal/db ./cmd/kata` passes before the implementation branch is considered ready.

## Open Questions For The Implementation Plan

1. Exact daemon DTO shape for parent and child count metadata.
2. Whether `Parent` in the new issue form is editable in the first pass or fixed only for `N new child`.
3. Whether detail Children section gets its own explicit focus mode or shares the active tab cursor machinery.
4. Whether filtered child matches auto-expand parents immediately or render a separate "matched children" mode.

Recommendation for the plan: resolve these conservatively. Prefer direct children only, fixed prefilled parent for `N`, and reuse existing detail cursor/windowing primitives where possible.
