# Plan 7 — TUI Design Sprint (Visual + Interaction Redesign)

> **Status:** Design v3 — locked. Wes locked the high-level shape (v2); Codex resolved the eight open questions and signed off on the locked shape (v3, 2026-05-01). Implementation tracked in `2026-05-01-kata-7-tui-impl.md`. This doc stays as the rationale + visual reference + decision log.

## Decisions locked (Wes review)

These are no longer open. Codex should challenge only with strong evidence.

1. **Layout: hybrid responsive.** Stacked on ordinary terminals; split (list + always-on detail) on genuinely wide ones. The split breakpoint and detail-pane behavior are specified in §"Layout sketches — split mode" below.
2. **In split mode, the detail pane always renders and follows the highlighted list row** — no `enter` required just to see detail. `enter` (or `tab`) moves *focus* into the detail pane; `esc` returns focus to the list. On narrow terminals (below the split breakpoint), `enter` opens full-screen detail as it does today.
3. **Input model: shared bubbles components, distinct interaction shells.** Quick filters / search / owner use an **inline command bar** (top of the list, single-line). Create / edit-body / add-comment use centered bordered forms. Single-field detail prompts (label/owner/link) use a small panel-local prompt rather than a full modal. One reusable input wrapper around `bubbles/textinput` and `bubbles/textarea`; the visual presentation differs by context.
4. **`$EDITOR` stays as `ctrl+e` escape hatch.** Default for create/edit/comment is in-TUI fields. `ctrl+e` from inside a multiLine field hands off to `$EDITOR` for power-user long-form writing.
5. **Preserve the working internals.** The transport/state layer is not the problem — the typed client, SSE consumer, gen guards, identity-based selection, viewport math, and sanitization stay. The rewrite is scoped to the chrome + input shell. (Memory: `feedback_kata_tui_internals.md`.)

## Hard constraints (must hold through the rewrite)

These are non-negotiable; any milestone that violates one is a regression.

- **List viewporting by terminal height.** Already in `windowIssues`. Keep.
- **Identity-based selection.** Cursor follows `selectedNumber`, not row index. Already in `applyFetched`. Keep.
- **Active pane has obvious border + focus treatment.** Magenta border + bold when focused; gray + normal when not. Mirrors roborev's fix-prompt pattern.
- **Persistent help row on every main screen.** Not just behind `?`. Updates contextually to the active pane / form / prompt.
- **Sanitize agent/user text at every render boundary.** Already in `sanitizeForDisplay`. Keep applying it on new render paths.
- **`--all-projects` and R stay gated** until the daemon ships `GET /issues` for cross-project reads.

## Why this exists

Plan 6 shipped a working TUI with the right *plumbing* (SSE invalidation, generation-tagged fetches, identity-based selection, sanitization, viewport, help overlay, modal infrastructure for label/owner/link prompts) but the *visible layer* is a `lipgloss.HiddenBorder()` table with a chip strip and a footer line. It looks like a debugging harness, not a daily-driver application. Specific failures from the user review:

- "No TUI chrome, doesn't feel like a real application."
- The new-issue flow opens a one-line inline prompt for the title, then suspends Bubble Tea to launch `$EDITOR` for the body. Both pieces are wrong: the inline prompt looks like a shell, and dropping into vim breaks the "this is an app" illusion.
- Edit-body and add-comment use the same `$EDITOR` shell-out pattern.
- Visually flat: no panel borders, no active/inactive panel highlighting, no persistent status row, no header/footer chrome.

The reference is **roborev's TUI** (`/Users/wesm/code/roborev/cmd/roborev/tui/`) — title line at top, status line below, hairline-rule separators around a properly-headered table, persistent help row at bottom, bordered focus panels with magenta-when-active / gray-when-inactive treatment. We borrow that visual language directly.

## Non-goals (out of scope for this sprint)

- Mouse support. Keyboard-first.
- Glamour markdown rendering for issue bodies. Plain text + line wrap stays for now.
- True color/theme customization. Two adaptive palettes (light terminal / dark terminal) is enough.
- Cross-project mode. Already gated off until the daemon ships `GET /issues`; the redesign assumes single-project scope.
- Any new daemon endpoint or wire change. This is purely a TUI rewrite.

## Design principles

1. **Polish over minimalism.** The bar is "real desktop application," not "terminal scratchpad." Borders, status bars, and footer help are non-negotiable.
2. **No `$EDITOR` for primary flows.** Title, body, and comment editing all happen in-app via `bubbles/textinput` and `bubbles/textarea`. `$EDITOR` stays as an opt-in escape hatch (`ctrl+e` from a body/comment form) for power users.
3. **Active-vs-inactive panel highlighting.** When a panel has focus, its border is magenta and bold. When it doesn't, gray and normal. Mirrors roborev's fix-prompt panel pattern; gives the user an unmistakable focus indicator.
4. **Persistent chrome.** Title at top, status/count line below it, help row at bottom — visible in every view. Empty space is a sign of "nothing to do," not "work in progress."
5. **Information density without clutter.** roborev's compact mode, scroll indicator (`[42-89 of 230]`), and `\x1b[K\n` line clearing are good models. We adopt them.
6. **Identity-stable, not flicker-prone.** Async refetches must never reflow the user's selection or scroll position. (Plan 6 already does identity-based selection; we preserve it through the redesign.)

## Reference: what we borrow from roborev

Read `cmd/roborev/tui/render_queue.go`, `render_review.go`, and `tui.go` lines 35–77 (the style palette) before reviewing the rest of this doc.

| Pattern | File | What we copy |
|---|---|---|
| Title bar with embedded filter/state indicators | `render_queue.go:122-141` | `kata · kata-project · open:N closed:N all:N · vX.Y.Z` style header, with `[filter:value]` chips in the same line |
| Status line with workers/counts | `render_queue.go:152-188` | The same idea, but kata-specific: SSE state, pending event count, last sync indicator |
| Horizontal-rule table separators | `render_queue.go:444-456` | `Border(lipgloss.Border{Top:"─", Bottom:"─", Middle:"─"})` only on top/bottom/header — no full box |
| Selected-row background highlight | `render_queue.go:494-502` | Uniform background (`Light:153 / Dark:24`), no per-cell color override on the selected row |
| Bordered focus panel (magenta active, gray inactive) | `render_review.go:213-247` | `lipgloss.NormalBorder()` with `BorderForeground(magenta-or-gray)` based on `m.focus` |
| Help footer | `render_review.go:259` | `renderHelpTable` style at the bottom, contextual to the current view |
| Status flash with priority | `render_review.go:251-256` | "flash takes priority over scroll indicator" idea |
| Adaptive light/dark palette | `tui.go:38-77` | The exact palette (we don't need to invent colors) |
| `\x1b[K\n` line clearing | throughout | Avoid render artifacts when content shrinks between frames |
| `bubbles/textinput`-style prompt panel | `render_review.go:200-247` | Bordered input area, focused style, single-line for now |

What does **NOT** transfer:

- Roborev's `viewQueue` / `viewReview` split is around *jobs* and *reviews*. Kata's analog is *list* and *detail*, but the data shape is different (issues have tabs, reviews don't). We borrow the layout ideas, not the file structure.
- Roborev's `viewKindPrompt` / `viewKindComment` are separate views. For kata we use **centered forms** (overlay via `lipgloss.Place`) for create/edit/comment and **panel-local prompts** for short single-field actions, on top of the underlying list/detail view rather than dedicated views — feels more app-like for short-lived input.
- Roborev's `compact` mode toggles based on terminal height. Kata can defer compact mode until we see real usage; the chrome should fit at 80×24 by default.

## Information architecture

What appears where:

- **Top line (always visible):** project name + scope + open/closed/all counts + version. One line. `kata · kata-project · open:23 closed:8 all:31 · v0.1.0`
- **Second line (always visible):** SSE state + pending invalidation indicator + last refetch age. `SSE: connected · 0 pending`
- **Third line (when filters active or prompts open):** filter chip strip OR active inline prompt (mutually exclusive). Empty when neither applies.
- **Body area (view-specific):** the list table, the detail panel, or the empty-state hint.
- **Footer status line (always visible, can be empty):** flash messages (mutation results, errors), scroll indicators, cache-staleness markers.
- **Footer help row (always visible):** 6–10 most relevant keybindings for the active view + context. Updates when an inline command bar, panel-local prompt, or centered form is active.

This is one more line of chrome than roborev (we add the SSE state line) because kata's SSE story is more first-class — the user needs to know when their view is fresh.

## Layout sketches

> ASCII mockups assume an 80-column dark terminal. The real renderer uses `lipgloss.AdaptiveColor` so the same shapes work on light terminals.

### Stacked layout — list view (narrow terminal, < split breakpoint)

```
kata · kata-project · open: 23 · closed: 8 · all: 31                           v0.1.0
SSE: connected · 0 pending events                                              wesm

[status:open] [owner:wesm]
──────────────────────────────────────────────────────────────────────────────────────
   #   status     title                                       owner          updated
──────────────────────────────────────────────────────────────────────────────────────
›  42  open       fix login bug on Safari                     claude-4.7     3h ago
   41  open       Tab key doesn't switch detail tabs          wesm           5h ago
   40  closed     rebuild search index                        wesm           1d ago
   39  deleted    purge stale tokens                          —              2d ago
   ...
──────────────────────────────────────────────────────────────────────────────────────
 closed #40                                                       [42-89 of 230 issues]
──────────────────────────────────────────────────────────────────────────────────────
 j/k move  enter open  n new  / search  o owner  s status  c clear  x close  ? help  q quit
```

Notes:
- `›` cursor glyph in column 0; the entire row gets a background highlight (the cursor glyph is a redundant signal so a screen reader / no-color terminal still has a visible marker).
- Status column uses the existing color chips (green = open, cyan = closed, dim red = deleted).
- Owner column elides empty values to `—` rather than blank, so the column always carries something.
- Updated column is right-aligned within its 12-cell budget so deltas line up.
- Filter chip strip wraps to a second line when it overflows; help footer wraps via `reflowHelpRows` from roborev.
- `enter` opens detail full-screen (current behavior preserved for narrow terminals).

### Split layout — list + always-on detail (wide terminal, ≥ split breakpoint)

Split breakpoint: **width ≥ 140 columns AND height ≥ 30 rows**. Below either threshold, fall back to stacked. (Rationale: 140 cols is the lower bound where a 60-col list gives readable titles AND a 60-col detail gives a comfortable comments column. 30 rows ensures both panes have enough vertical space for the tab strip + ~10 content rows.)

In split mode, the **detail pane always reflects the highlighted list row**. Cursor changes in the list retarget the detail pane: when the new row's data is already cached, repaint **immediately** with no fetch; when a fetch is needed, wait for a **75ms debounce** so rapid `j`/`k` doesn't spam the network. The existing `Model.nextGen` counter drops in-flight fetches from a prior row on arrival. On focus-into-detail (`enter` or `tab`), the detail pane's border becomes magenta + bold; the list pane's border goes gray. `esc` returns focus to the list.

```
kata · kata-project · open: 23 · closed: 8 · all: 31                                                                                v0.1.0
SSE: connected · 0 pending events                                                                                                   wesm

[status:open] [owner:wesm]
╭─ issues ────────────────────────────────────────────────╮  ╭─ #42 · open · alice · updated 5m ago ─────────────────────────────╮
│   #   status     title                  updated         │  │ fix login bug on Safari                                            │
│ ────────────────────────────────────────────────────── │  │                                                                    │
│ › 42  open       fix login bug on Saf…  3h ago          │  │ ──────────────────────────────────────────────────────────────── │
│   41  open       Tab key doesn't swit…  5h ago          │  │  Reproduces in Safari 17 only.                                     │
│   40  closed     rebuild search index   1d ago          │  │  Click the login button twice and see a 500 error.                 │
│   39  deleted    purge stale tokens     2d ago          │  │                                                                    │
│   38  open       investigate flaky test 2d ago          │  │  Stack trace:                                                      │
│   37  open       refactor cache layer   3d ago          │  │    POST /auth/callback → 500                                       │
│   36  closed     bump deps to v2        4d ago          │  │ ──────────────────────────────────────────────────────────────── │
│   35  open       redesign nav bar       5d ago          │  │  [ Comments (4) ]   Events (12)   Links (1)                        │
│   ...                                                   │  │ ──────────────────────────────────────────────────────────────── │
│                                                         │  │ › [alice]  10:00  I can repro on macOS Sonoma.                     │
│                                                         │  │   [bob]    11:00  Looks like a race in oauth handler.              │
│                                                         │  │   [claude] 12:30  Fixed in #45.                                    │
╰─────────────────────────────────────────────────────────╯  ╰────────────────────────────────────────────────────────────────────╯
 closed #40                                                                                                  [1-9 of 230 issues]
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 j/k move  tab focus detail  enter focus detail  n new  / search  o owner  s status  c clear  x close  ? help  q quit
```

Footer help row above shows the **list-focused** keys (default state). When focus moves to detail (via `tab` or `enter`), the same row swaps to detail-focused keys:

```
 j/k move  tab next  shift-tab prev  enter jump  esc back to list  e edit  c comment  + label  a owner  ? help  q quit
```

Notes on split mode:
- Both panes are bordered (`lipgloss.NormalBorder()`) with a title in the top border. The currently-focused pane has a magenta+bold border; the other is gray.
- **Pane sizing.** List pane is fixed at **60–64 cells** wide; the detail pane flexes to fill the remaining width. A 50/50 split would shortchange both; a fixed list keeps title-column width predictable across terminal sizes.
- List columns compress in split mode: drop the owner column (it's already shown in the detail header), shorten the title column to fit. The fixed columns (`#`, `status`, `updated`) keep their widths.
- Detail pane shows: header line (`#N · status · author · updated`), title (bold, full-width inside the pane), body (scrollable, with hairline rule above and below), tab strip, tab content. Same composition as full-screen detail, just narrower.
- When focus is on the list, the detail pane's tab cursor is greyed (visible but not interactable). When focus moves to detail, the cursor color brightens.
- The footer help row updates to show keys for the focused pane: list-focused row shown above, detail-focused row shown immediately below it. Only one renders at a time.
- **Detail follow on cursor change.** Cursor changes in the list retarget the detail pane. If the new row's issue is already in the cache (the issue projection from the list response carries Title/Status/Author/UpdatedAt; the daemon may also have full detail cached from a recent open), repaint **immediately** with no fetch. If detail data is missing (comments / events / links not yet fetched, or the body needs to come from `GetIssue`), schedule a fetch after a **75ms debounce** so rapid `j`/`k` doesn't churn the network. The existing `Model.nextGen` counter drops fetches from a prior row on arrival.
- Empty-detail state when the list is empty: detail pane renders a centered "select an issue" hint inside its border, no fetch dispatched.

#### Resize across the breakpoint

When the terminal crosses the breakpoint (140×30) mid-session, the layout flips without losing user state:

- **Split → stacked while detail-focused**: collapse to **stacked detail** (full-screen) for the same `selectedNumber`. The list cursor + filter state survive; the user is exactly where they were, just minus the list-pane chrome.
- **Split → stacked while list-focused**: collapse to **stacked list**. Cursor + filter unchanged; the detail pane simply stops rendering.
- **Stacked detail → split**: render split with detail-focused (the user was looking at detail; preserve that focus).
- **Stacked list → split**: render split with list-focused (the default).

Resize is driven by `tea.WindowSizeMsg`; the layout transition happens in `Model.Update` without a refetch (cache stays intact). Selection follows by `selectedNumber`, never by index.

### Stacked layout — detail view (narrow terminal, after `enter` from list)

```
kata · kata-project · open: 23 · closed: 8 · all: 31                           v0.1.0
SSE: connected · last sync 2s ago                                              wesm

#42 · open · alice · created 3h ago · updated 5m ago
fix login bug on Safari
──────────────────────────────────────────────────────────────────────────────────────
 Reproduces in Safari 17 only.
 Click the login button twice and see a 500 error.

 Stack trace:
   POST /auth/callback → 500
   handler: github.com/foo/bar/auth.callbackHandler:142
──────────────────────────────────────────────────────────────────────────────────────
 [ Comments (4) ]   Events (12)   Links (1)
──────────────────────────────────────────────────────────────────────────────────────
› [alice]    2026-04-30 10:00
    I can repro on macOS Sonoma. Same Safari version.

  [bob]      2026-04-30 11:00
    Looks like a race in oauth handler.

  [claude]   2026-04-30 12:30
    Fixed in #45. Should land later today.
──────────────────────────────────────────────────────────────────────────────────────
                                                                  [1-3 of 4 comments]
──────────────────────────────────────────────────────────────────────────────────────
 j/k move  tab next  shift-tab prev  enter jump  esc back  e edit  c comment  x close  ? help
```

Notes:
- Header strip: `#N · status · author · created Xago · updated Yago`. Title on a separate line, full-width, bold.
- Body area is bounded (existing `bodyHeight` math). Scroll indicator merges into the footer when the body scrolls.
- Tab strip uses brackets around the active tab (`[ Comments (4) ]`) plus colored underline (already in `tabActive`/`tabInactive`).
- Tab content scroll indicator is per-tab and merges into the footer.
- Cursor glyph in the tab content shows which row is selected (for jump-to-referenced-issue).

### Inline command bar (search / owner filter)

Quick filters use an **inline command bar** at the top of the list, not a centered modal. Activates on `/` (search), `o` (owner). Replaces the chip strip while active. `enter` commits, `esc` cancels.

```
kata · kata-project · open: 23 · closed: 8 · all: 31                           v0.1.0
SSE: connected · 0 pending events                                              wesm

╭─ search ───────────────────────────────────────────────────────────────────────────╮
│ login bug_                                                                         │
╰────────────────────────────────────────────────────────────────────────────────────╯
──────────────────────────────────────────────────────────────────────────────────────
   #   status     title                                       owner          updated
──────────────────────────────────────────────────────────────────────────────────────
›  42  open       fix login bug on Safari                     claude-4.7     3h ago
   ...
──────────────────────────────────────────────────────────────────────────────────────
 enter commit  esc cancel  ctrl+u clear
```

Notes:
- Single-line bordered input replaces the chip strip row (it doesn't push the table down).
- Active border is magenta + bold (it owns focus while open).
- **Live filtering, no debounce.** Search and owner filters are applied **client-side** against the already-fetched issue slice (`filteredIssues` in `list.go`). Each keystroke re-applies the filter and repaints — no network call, so a debounce would just feel laggy. Status cycling (`s`) and clear-filters (`c`) still dispatch a refetch because Status is server-side.
- Built on `bubbles/textinput` (the same component that backs the centered forms below — the *shell* differs, the *engine* doesn't).

### Centered form — new issue (multi-field)

```
kata · kata-project · open: 23 · closed: 8 · all: 31                           v0.1.0
SSE: connected · 0 pending events                                              wesm

(list dimmed underneath)

                ╭── new issue ──────────────────────────────╮
                │                                           │
                │  Title                                    │
                │  ┌─────────────────────────────────────┐  │
                │  │ fix login bug on Safari_            │  │
                │  └─────────────────────────────────────┘  │
                │                                           │
                │  Body                                     │
                │  ┌─────────────────────────────────────┐  │
                │  │ Reproduces in Safari 17 only.       │  │
                │  │ Click the login button twice and    │  │
                │  │ see a 500 error.                    │  │
                │  │                                     │  │
                │  │                                     │  │
                │  │                                     │  │
                │  └─────────────────────────────────────┘  │
                │                                           │
                ╰───────────────────────────────────────────╯

 tab: switch field   ctrl+s: create   esc: cancel   ctrl+e: open body in $EDITOR
```

Notes:
- Modal sits centered via `lipgloss.Place(width, height, Center, Center)`.
- Active field's input box border is magenta + bold; inactive is gray + normal.
- The list view underneath stays painted (the form is an overlay, not a replacement view) so the user keeps spatial context.
- `ctrl+e` is the explicit escape hatch into `$EDITOR` for the body field; the title field never goes to `$EDITOR`.
- Empty title disables `ctrl+s` and shows a hint in the footer.

### Centered form — edit body (single field)

```
                ╭── edit body of #42 ───────────────────────╮
                │                                           │
                │  ┌─────────────────────────────────────┐  │
                │  │ Reproduces in Safari 17 only.       │  │
                │  │ Click the login button twice and    │  │
                │  │ see a 500 error.                    │  │
                │  │                                     │  │
                │  │ Stack trace:                        │  │
                │  │   POST /auth/callback → 500         │  │
                │  └─────────────────────────────────────┘  │
                │                                           │
                ╰───────────────────────────────────────────╯

 ctrl+s: save   esc: cancel   ctrl+e: open in $EDITOR
```

### Centered form — add comment (single field)

```
                ╭── add comment to #42 ─────────────────────╮
                │                                           │
                │  ┌─────────────────────────────────────┐  │
                │  │ Looks like a race in oauth          │  │
                │  │ handler. Going to take a look this  │  │
                │  │ afternoon._                         │  │
                │  │                                     │  │
                │  │                                     │  │
                │  └─────────────────────────────────────┘  │
                │                                           │
                ╰───────────────────────────────────────────╯

 ctrl+s: post   esc: cancel   ctrl+e: open in $EDITOR
```

### Panel-local prompt (label / owner / link target — single short input)

Single-field detail prompts (`+` add label, `a` assign owner, `L` add link, `p` set parent, `b` add blocker) use a **panel-local prompt** anchored to the bottom of the detail panel — not a centered overlay. Lighter than a full modal because the action is short and contextually tied to the detail issue. The detail content stays visible above; the prompt occupies the last few lines of the panel.

Stacked layout:

```
... detail content above ...
──────────────────────────────────────────────────────────────────────────────────────
╭─ add label to #42 ─────────────────────────────────────────────────────────────────╮
│ priority-high_                                                                     │
╰────────────────────────────────────────────────────────────────────────────────────╯
 enter add  esc cancel
```

Split layout (prompt anchors to the detail pane only, list pane keeps rendering):

```
╭─ issues ─────────────────────────────╮  ╭─ #42 · open ───────────────────────────╮
│   #   status     title    updated    │  │ ... detail above ...                   │
│ ───────────────────────────────────  │  │ ────────────────────────────────────── │
│ › 42  open       fix l…   3h ago     │  │ ╭─ add label ──────────────────────╮   │
│   41  open       Tab k…   5h ago     │  │ │ priority-high_                  │   │
│   ...                                │  │ ╰──────────────────────────────────╯   │
│                                      │  │  enter add  esc cancel                 │
╰──────────────────────────────────────╯  ╰────────────────────────────────────────╯
```

Same data flow as the existing `dm.modal` plumbing; new chrome.

### Empty state

```
kata                                                                            v0.1.0





                          ┌────────────────────────────────────────────┐
                          │                                            │
                          │   no kata projects registered yet          │
                          │                                            │
                          │   run `kata init` in a repo to get started │
                          │                                            │
                          │   press q to quit                          │
                          │                                            │
                          └────────────────────────────────────────────┘





 q quit
```

The header keeps just `kata · vX.Y.Z` since there's no project to summarize.

### Help overlay

Already exists; the redesign tightens it up to match the new color palette and adds a "press ? to return" footer. No structural change.

## Interaction model

### Focus model

A view has one or more **focusable regions**. At most one region is focused at a time, indicated by a magenta + bold border (or, for the list table, a brighter selection background). The focused region receives all non-global keystrokes.

| View | Layout | Focusable regions | Default focus |
|---|---|---|---|
| List+Detail (split) | wide | List pane, detail pane, inline command bar (when active), panel-local prompt (when active) | List pane |
| List (stacked) | narrow | Table, inline command bar (when active) | Table |
| Detail (stacked, after `enter`) | narrow | Tab content, panel-local prompt (when active) | Tab content |
| Centered form (overlay, any width) | n/a | Form owns focus; underlying view stays painted but inactive | Form's first field |
| Help overlay | n/a | Help panel (just `?` / `esc` to dismiss) | Help panel |

**Cross-region focus moves** (split layout): `tab` from list → detail; `esc` from detail → list. Keeps the list's cursor unchanged so going back-and-forth doesn't lose place.

**Within-region tab cycling**: in detail, `tab` / `shift-tab` cycle the Comments/Events/Links tabs *only when detail is focused*. Within the list, `tab` from the table moves focus to detail (split mode) or no-ops (stacked).

### Input-shell taxonomy

Three distinct presentations, **one shared component family** (`bubbles/textinput` for single-line, `bubbles/textarea` for multi-line, wrapped by a thin `inputField` adapter that handles focus styling, validation hooks, and the `ctrl+e` → `$EDITOR` escape hatch).

| Presentation | Used for | Where it renders | Lifetime |
|---|---|---|---|
| **Inline command bar** | `/` search, `o` owner filter | Top of the list pane, replaces the chip strip row | Open until `enter` (commit) or `esc` (cancel) |
| **Panel-local prompt** | `+` label, `-` unlabel, `a` assign owner, `L` add link, `p` set parent, `b` add blocker | Bottom of the detail pane, anchored under the detail content | Open until `enter` (commit) or `esc` (cancel) |
| **Centered form** | `n` new issue (multi-field), `e` edit body, `c` comment | Centered overlay via `lipgloss.Place`; underlying view stays painted but inactive | Open until `ctrl+s` (commit), `esc` (cancel), or `ctrl+e` (handoff to `$EDITOR`) |

State lives on `Model.input` with a discriminator on the kind:

```go
type inputState struct {
    kind     inputKind     // none | searchBar | ownerBar | newIssueForm | editBodyForm | commentForm | labelPrompt | ownerPrompt | linkPrompt | parentPrompt | blockerPrompt
    title    string        // "search", "new issue", "add label to #42", etc. — used by the renderer
    fields   []inputField  // 1 for bars/prompts, 2 for new-issue form
    active   int           // index of focused field
    err      string        // last validation error
    saving   bool          // disables commit while a dispatch is in flight
}

type inputField struct {
    label    string         // used only by centered forms; bars/prompts inline the title in the border
    kind     fieldKind      // singleLine | multiLine
    input    textinput.Model // populated when kind == singleLine
    area     textarea.Model  // populated when kind == multiLine
    required bool
}
```

The renderer dispatches on `inputState.kind` to pick the chrome (bar / prompt / form). The data path is uniform — `commitInput` validates, dispatches a `tea.Cmd`, and clears the state on success.

### Keybindings (canonical, post-redesign)

Global (always honored when no inline prompt is open):

| Key | Action |
|---|---|
| `q` / `ctrl+c` | quit |
| `?` | toggle help overlay |
| `R` | (gated, toast no-op until daemon supports cross-project) |

List view:

| Key | Action |
|---|---|
| `j` / `↓` | next row |
| `k` / `↑` | prev row |
| `g` / `home` | first row |
| `G` / `end` | last row |
| `pgdn` / `pgup` | page down/up |
| `enter` | stacked: open detail full-screen. Split: focus the detail pane. |
| `tab` | split only: focus the detail pane (mirrors `enter`). Stacked: no-op on the list. |
| `n` | new issue (opens centered form) |
| `/` | search (opens inline command bar at top of list) |
| `o` | owner filter (opens inline command bar) |
| `s` | cycle status filter |
| `c` | clear filters |
| `x` | close highlighted issue |
| `r` | reopen highlighted issue |

Detail view:

| Key | Action |
|---|---|
| `j` / `↓` | next row in active tab (or scroll body if tab empty) |
| `k` / `↑` | prev row in active tab (or scroll body) |
| `tab` / `shift-tab` | next/prev tab (when detail is focused) |
| `enter` | jump to referenced issue (events/links tabs) |
| `esc` / `backspace` | stacked: back to list. Split: return focus to list pane. |
| `e` | edit body (opens centered form) |
| `c` | add comment (opens centered form) |
| `x` | close issue |
| `r` | reopen issue |
| `+` | add label (opens panel-local prompt) |
| `-` | remove label (opens panel-local prompt) |
| `a` | assign owner (opens panel-local prompt) |
| `A` | clear owner |
| `p` | set parent (opens panel-local prompt) |
| `b` | add blocker (opens panel-local prompt) |
| `L` | add link (opens panel-local prompt) |

Inline command bar (search / owner):

| Key | Action |
|---|---|
| `enter` | commit + close |
| `esc` | cancel + close (clears uncommitted buffer) |
| `ctrl+u` | clear field |

Panel-local prompt (label / owner / link / parent / blocker):

| Key | Action |
|---|---|
| `enter` | commit + close |
| `esc` | cancel + close |

Centered form — single-field (edit body, add comment):

| Key | Action |
|---|---|
| `enter` | inserts newline (the field is multiLine) |
| `ctrl+s` | commit |
| `esc` | cancel |
| `ctrl+e` | hand off the buffer to `$EDITOR`, re-enter the form on resume with the edited content |

Centered form — multi-field (new issue):

| Key | Action |
|---|---|
| `tab` / `shift-tab` | next/prev field |
| `enter` | inserts newline if active field is multiLine; advances field if active field is singleLine and non-empty |
| `ctrl+s` | commit (any field, any cursor position) |
| `esc` | cancel |
| `ctrl+e` | hand off the body field to `$EDITOR` (no-op when on the title field) |

The `enter` semantic on the title field of the new-issue form (singleLine) advances to the body field on commit-style press. This matches GitHub's issue form behavior and avoids requiring `tab` for the common "type title, press enter, type body" flow.

## Visual language

### Color palette (lifted directly from roborev with kata-specific additions)

```go
// Adopted from roborev's tui.go:38-77 — keep the exact codes for parity.
titleStyle      = NewStyle().Bold(true).Foreground(AdaptiveColor{Light:"125", Dark:"205"}) // magenta
statusStyle     = NewStyle().Foreground(AdaptiveColor{Light:"242", Dark:"246"})            // gray
selectedStyle   = NewStyle().Background(AdaptiveColor{Light:"153", Dark:"24"})             // light blue bg
helpStyle       = NewStyle().Foreground(AdaptiveColor{Light:"242", Dark:"246"})            // gray
helpKeyStyle    = NewStyle().Foreground(AdaptiveColor{Light:"242", Dark:"246"})            // gray
helpDescStyle   = NewStyle().Foreground(AdaptiveColor{Light:"248", Dark:"240"})            // dimmer gray
errorStyle      = NewStyle().Bold(true).Foreground(AdaptiveColor{Light:"124", Dark:"196"}) // red bold
flashStyle      = NewStyle().Foreground(AdaptiveColor{Light:"28",  Dark:"46"})             // green

// Kata-specific status chips
openStyle       = NewStyle().Foreground(AdaptiveColor{Light:"28",  Dark:"46"})             // green
closedStyle     = NewStyle().Foreground(AdaptiveColor{Light:"30",  Dark:"51"})             // cyan
deletedStyle    = NewStyle().Foreground(AdaptiveColor{Light:"124", Dark:"196"}).Faint(true)// dim red

// Form / prompt / pane border colors
panelActiveBorder   = AdaptiveColor{Light:"125", Dark:"205"} // magenta
panelInactiveBorder = AdaptiveColor{Light:"242", Dark:"246"} // gray
```

### Box-drawing characters

- Hairline rules: `─` (U+2500). Roborev uses these for table top/bottom/header separators.
- Modal box: `lipgloss.NormalBorder()` (single-line `┌┐└┘─│`).
- Active panel emphasis: same border, magenta foreground.
- Cursor glyph in lists: `›` (U+203A).
- Tab-active brackets: `[ … ]` literal ASCII.
- Filter chip framing: `[…]` literal ASCII (current code already does this).

### Typography (text style)

- Bold for titles only (top header, form/prompt title, tab headers).
- Italic and underline are NOT used. They render inconsistently across terminals.
- Faint (dim) for secondary metadata: timestamps, counts, deleted indicator.
- Status chip text is plain weight; color carries the meaning.

## Build plan (after design lock)

Each milestone is a separate commit, lints + tests green. Roborev-fix checkpoint after M3 (input infrastructure ships) and M6 (split layout ships). Internals to **preserve** through every milestone: `client.go`, `events_sse.go`, `messages.go` shapes, `Model.nextGen`, `dispatchKey` staleness guards, `selectedNumber` identity selection, `windowIssues`, `sanitizeForDisplay`, the `--all-projects`/R gate, the test seam (`fakeListAPI`/`fakeDetailAPI`/`drainCmd`).

| Milestone | Scope | Tests added | Risk |
|---|---|---|---|
| M0 | **Adopt roborev color palette** in `theme.go`. Replace existing styles with the new constants. No layout change yet — should produce a near-identical render with new colors. | Snapshot regen (existing goldens). | Low. |
| M1 | **List view chrome.** Title bar (project + counts + version), SSE/status line, hairline-rule headered table, footer status + scroll indicator, footer help row via lifted `reflowHelpRows`. Drop the bare `joinNonEmpty` composition. | New snapshots: `list-header-chrome`, `list-scroll-indicator`, `list-empty-chrome`. | Low. |
| M2 | **Detail view chrome.** Same header strip, `[ … ]` tab brackets, tab content with rule separators, contextual footer help row. Stacked-only for now (full-screen detail). | Snapshots: `detail-comments-chrome`, `detail-events-chrome`, `detail-links-chrome`. | Low. |
| M3a | **Input infrastructure + inline command bar.** Bubbles preflight (`go get bubbles`, smoke-test `textinput` against `bubbletea v1.3.10`). New `inputState` + `inputField` + `inputs_render.go` scaffolding. Migrate `searchState` (list `/`/`o`) to inline command bar. Live undebounced filter results. | `input_test.go` covering kind dispatch + focus + commit/cancel; snapshots for command bar in stacked and split. | Medium. New dependency; bounded to list-side. |
| M3b | **Panel-local prompts replace `dm.modal`.** Migrate label/owner/link/parent/blocker prompts to the panel-local presentation under the existing detail chrome. Same data flow, new chrome only. | Snapshot `detail-with-label-prompt` (and one per prompt kind); existing `dm.modal` tests rebuilt around the new shell. | Medium. Bounded to detail-side; no cross-cutting changes. |
| M4 | **Centered forms** for create / edit-body / add-comment. Adds `bubbles/textarea`. New-issue form is two-field (title singleLine + body multiLine; `enter` on title advances to body). `ctrl+s` commits, `ctrl+e` hands off to `$EDITOR` from any multiLine field; reuses `editorCmd` so `editor.go` stays intact. | `form_create_test.go`, `form_edit_body_test.go`, `form_comment_test.go`, `form_editor_handoff_test.go`. | High. Largest surface change; touches list.go, detail.go, detail_editor.go. |
| M5 | **Empty state, help overlay, narrow-terminal hint.** Empty state under the new palette. Help overlay refreshed. Below-80-col degraded hint (centered "too narrow; resize" panel; `q quit` still works; recovers via `tea.WindowSizeMsg`). Status-flash priority over scroll indicator. | Refresh empty/help goldens; new `narrow-terminal-hint` snapshot. | Low. |
| M6 | **Hybrid responsive layout (split mode).** Add a `layoutMode` discriminator on `Model` driven by terminal `width≥140 && height≥30`. In split mode: render list pane (fixed 60–64 cells) + detail pane (flex), focus indicator on the active pane, list cursor changes immediately repaint detail when cached and dispatch a **75ms-debounced** fetch when not. Cross-pane focus: `tab`/`enter` list→detail, `esc` detail→list. Resize across the breakpoint preserves selection by `selectedNumber` and (when applicable) the focused pane (per "Resize across the breakpoint" §). | `layout_test.go` for breakpoint detection + resize transitions; `split_test.go` for cursor-cache-immediate vs cursor-debounce-fetch and focus-move flows; new snapshots `list-detail-split-wide`, `list-detail-split-focus-detail`. | High. Real architectural change to `Model.View` and `Model.Update`'s focus dispatch. |

Estimated total: ~8 commits (M0, M1, M2, M3a, M3b, M4, M5, M6). Net new code ~2–2.5k LOC; tests +1k LOC.

**Why this order:** chrome (M0–M2) lands first as the most visible improvement and the lowest risk. Input infrastructure (M3a) is foundational for M3b/M4 and replaces existing list plumbing without changing behavior. M3b is the bounded detail-side migration. Centered forms (M4) is the single biggest UX win — it eliminates `$EDITOR`-by-default. Polish (M5) bridges to the architectural change. Split mode (M6) lands last because it depends on M1/M2 chrome and M3a/M3b/M4 input shells already being right; doing it earlier would mean re-doing the split work each time the chrome changes.

## Resolved decisions (Codex review, 2026-05-01)

All eight v2 open questions resolved. Calls captured here so the implementation plan inherits them without re-litigation.

1. **Split breakpoint = 140×30.** Locked. No second breakpoint at 200×40 unless real usage demands.
2. **Cursor-debounce in split mode = 75ms for *remote fetches only*.** When the highlighted issue's detail data is already cached, repaint immediately — the debounce only applies when a fetch needs to dispatch.
3. **`enter` on the title field (singleLine) of the new-issue form advances to body.** `ctrl+s` remains the only commit. This matches GitHub-form convention.
4. **Lift roborev's color palette verbatim**, with semantic remap for kata: `passStyle` (green) → `openStyle`, `closedStyle` (cyan) keeps name + meaning, `failStyle` (red) → `deletedStyle` (faint variant). Other colors carry over as-is.
5. **Lift `reflowHelpRows` verbatim.** "Not the place to get clever." Adapt the input shape to take a `[]helpItem` slice; don't reshape kata's `helpSections` to fit.
6. **Bubbles is an M3 preflight.** Current `go.mod`: `bubbletea v1.3.10`, no `bubbles` pin yet. M3 starts with `go get github.com/charmbracelet/bubbles` and a smoke test confirming `textinput` + `textarea` work against `bubbletea v1.3.10` before any other M3 work lands.
7. **Inline command bar is live, undebounced.** Search and owner filters are client-side (`filteredIssues`); each keystroke re-applies and repaints. No network, no debounce. The earlier draft saying "debounced 150ms" was wrong — corrected.
8. **Below 80 cols → degraded "too narrow" hint, do not refuse to start.** A centered "kata tui needs ≥80 columns; resize and try again" panel renders inside whatever space is available, with `q quit` still honored. The user can resize and the TUI recovers (via `tea.WindowSizeMsg`) without restarting.

## File budget plausibility (Codex flag on M3)

Codex flagged M3 as probably too large for a 3–5 file budget. Acknowledged. M3 plans to touch:

- `theme.go` (existing) — palette additions for input borders
- `input.go` (new) — `inputState` + `inputField` + dispatch
- `inputs_render.go` (new) — three render shells (bar / prompt / form scaffolding without forms)
- `list.go` / `list_render.go` (existing) — wire inline command bar in place of `searchState`
- `detail.go` / `detail_mutation.go` / `modal.go` (existing) — wire panel-local prompt in place of `dm.modal`
- `input_test.go` (new) — dispatch + commit/cancel coverage
- new snapshot fixtures

That's 7+ files. Two paths: **(a)** accept the larger M3, **(b)** split it into M3a (infrastructure + inline command bar replaces `searchState`) and M3b (panel-local prompt replaces `dm.modal`). Recommendation: **split** — M3a is bounded to list-side input plumbing, M3b touches detail-side. Smaller, easier to review, easier to roll back independently. Build plan below reflects the split.

## Out-of-scope ideas to consider for a future plan

- **Tag/label viewer** as a sub-panel in detail view.
- **Diff/patch view** for issues that link to a PR.
- **Multi-issue selection + bulk operations** (close N issues at once).
- **Saved searches / pinned filters.**
- **Color theming via `KATA_COLOR_MODE=theme:nord` style env var.**

## Status: design locked

Wes locked the layout/input/internals decisions in the v2 review. Codex's v3 review (2026-05-01) signed off on the shape, called out doc contradictions (now fixed), and resolved all eight open questions (see "Resolved decisions"). The mockups, key tables, and build plan are now internally consistent and reflect both reviews.

Next step: convert this into a per-task implementation plan with checkbox steps in `2026-05-XX-kata-7-tui-impl.md` next to this doc. The impl plan is the file the work follows; this design doc stays as the rationale + visual reference + decision log.

## File-budget compliance

Same as Plan 6: ≤100 LOC/function, cyclomatic ≤8, 100-col lines. The redesign will likely create new files (`input.go`, `inputs_render.go`, `layout.go`, `form_*.go`); each should stay under ~600 LOC. If a milestone looks like it'll bust budget, split it.
