# Kata TUI Document Detail Design

**Status:** Draft for review.

**Goal:** Replace the current issue detail screen with a document-page layout that instills confidence at a normal working terminal size. The primary target viewport is **80 columns by 50 rows**. This spec supersedes the Detail View subsection of `2026-05-02-kata-tui-professional-workspace-design.md`; the queue, hierarchy DTO, forms, and SSE decisions from that spec still stand unless explicitly revised here.

---

## Design Principles

- The issue detail screen reads top-to-bottom like a work item page, not like a boxed dashboard.
- The issue title, status, body, children, and activity are the content. Chrome supports them but does not compete.
- Use rules and whitespace for structure. Avoid large filled backgrounds and heavy nested boxes.
- Metadata should be readable immediately after the title: ownership, labels, parent, children, timestamps, and all-projects disambiguation.
- Every layout must remain deterministic under `NO_COLOR`.

## Primary Wireframe

Target: stacked detail at `80x50`.

```text
 Project: kata                                               kata かた · dev

 issue #42

 fix login bug on Safari                                      [open]

 authored by wesm · created Apr 30 10:00 · updated 3h ago
 owner: alice                         labels: [bug] [prio-1] [needs-design]
 parent: #12 workspace polish         children: 2 open / 5 total

 Body ----------------------------------------------------------------------
 Reproduces in Safari 17 only.

 Click the login button twice. The second click races the OAuth callback
 and leaves the browser in a half-authenticated state.

 Expected:
   User lands on the dashboard once.

 Actual:
   Login button stays disabled until refresh.

 Children ------------------------------------------------------------------
 > #43  open    detail hint bars incomplete          claude     1h ago
   #44  open    comment form should be centered      wesm       2h ago
   #45  closed  new child form parent lock           alice      Apr 30

 Activity ------------------------------------------------------------------
 [ Comments (4) ]   Events (12)   Links (2)

 > alice  Apr 30 10:00
   I can repro on macOS.

   bob    Apr 30 11:00
   Looks like a race in oauth.

 ↑↓ move │ tab section │ enter open │ e edit │ c comment │ N child │ p parent │ ? help
```

The ASCII rule characters above are illustrative for the spec text. The renderer may use the existing terminal rule glyphs if snapshots remain stable and `NO_COLOR` remains readable.

## Header And Metadata

### Top Strip

The global app frame remains outside the issue document:

- Left: current project or all-projects state.
- Right: `kata かた · <version>`, styled as dim chrome.
- Do not repeat the project inside the issue body for single-project mode.

In all-projects mode, the issue metadata block starts with a full-width project row:

```text
project: kata
owner: alice                         labels: [bug] [prio-1]
parent: #12 workspace polish         children: 2 open / 5 total
```

### Issue Number

`issue #42` renders as a small lead-in above the title. It is not the main heading.

### Title And Status

The title row reserves a fixed right gutter for status:

```text
fix login bug on Safari                                      [open]
```

Rules:

- Reserve enough right-side width for the widest v1 status pill, for example 8 cells for `[closed]` plus spacing.
- The status pill is always visible.
- Long titles truncate with an ellipsis before colliding with status.
- Status is visually heavier than labels: open is green, closed is cyan-dim, and `NO_COLOR` keeps bracketed text.
- Color only the status word/pill, never the full row background.

### Timestamps

At `80x50`, render:

```text
authored by wesm · created Apr 30 10:00 · updated 3h ago
```

Drop order as width tightens:

1. Drop `created ...`.
2. Drop `authored by ...` only if the row still does not fit.
3. Keep `updated ...` longest because recency is the most useful glanceable signal.

### Labels

Labels render as lightweight pills:

```text
labels: [bug] [prio-1] [needs-design]
```

Labels use one shared dim accent or neutral style. They must not compete visually with status.

## Sections

### Body

Body always renders. If the issue body is empty, show:

```text
(no description)
```

The placeholder is dim and left-aligned under the Body rule.

### Children

Children renders only when direct children exist. When no children exist, do not render an empty Children section; the metadata line already says `children: none`.

Child rows are previews, not a second queue table. Columns:

- cursor marker
- issue number
- status
- title
- owner
- updated

Do not render labels or child-count columns in this section.

### Activity

Activity renders when at least one of comments, events, or links exists. If all three are empty, omit the Activity section and use the freed space for Body.

Activity rule and tabs are separate rows:

```text
Activity ------------------------------------------------------------------
[ Comments (4) ]   Events (12)   Links (2)
```

Active tab uses brackets for `NO_COLOR`. Counts must update after SSE-driven detail invalidation, including comment events, link events, and issue events that affect the open detail.

Empty active-tab placeholders:

- Comments: `(no comments)`
- Events: `(no events)`
- Links: `(no links)`

Placeholders are dim and left-aligned.

### Comments

Comments use spacing and typography, not separator rules:

```text
> alice  Apr 30 10:00
  I can repro on macOS.

  bob    Apr 30 11:00
  Looks like a race in oauth.
```

Author names are always bold. In color-capable modes, they also use the app accent. Timestamps are dim. This gives `NO_COLOR` a visible hierarchy without requiring a separate rule.

Do not add a dim rule between comments by default. During implementation, create a 12-comment snapshot fixture; if the long-thread view looks too loose, reduce spacing before adding separators.

## Markdown Rendering

Issue bodies and comment bodies render Markdown.

Use Charmbracelet Glamour, scoped to body/comment rendering only:

- Pin an explicit Glamour version in `go.mod`; do not leave it as a floating `@latest` upgrade.
- Select the Glamour style from the existing kata color mode:
  - `KATA_COLOR_MODE=dark` -> dark style
  - `KATA_COLOR_MODE=light` -> light style
  - `NO_COLOR` or `KATA_COLOR_MODE=none` -> notty/plain style
  - auto -> match the resolved app color mode
- Use a custom style config or post-style setup that guarantees **no painted backgrounds**, including code blocks.
- Pass the actual body/comment width every render. Re-render on resize.
- Fenced code blocks, inline code, bold, headings, blockquotes, links, and lists are in scope.
- Tables degrade to a width-safe plain-text grid rather than overflowing the detail pane.
- Horizontal rules are ignored; section rules already provide page structure.
- Images render as `[image: alt]`, falling back to `[image]` when no alt text exists.
- Fallback to sanitized plain text only when renderer construction fails, the terminal style is unsupported, or panic recovery catches a renderer panic. Do not fallback merely because the rendered Markdown is aesthetically imperfect.

## Focus And Navigation

Initial focus lands on the first non-empty interactive section:

1. Children, if direct children exist.
2. Comments, if comments exist.
3. Events, if events exist.
4. Links, if links exist.
5. Comments as the default activity target when no interactive rows exist.

For a brand-new issue with no children, comments, events, or links, focus still targets the empty Comments tab. The cursor is invisible until the first comment exists; pressing `c` opens the comment form.

`tab` cycles through visible interactive sections. Skip Children when empty. Keep the existing detail navigation stack for `enter` on children, events, and links.

Persistent footer movement labels use arrows:

```text
↑↓ move │ tab section │ enter open │ e edit │ c comment │ N child │ p parent │ ? help
```

The full `?` help screen lists aliases, including `j/k` and arrow keys. Persistent footers do not use `j/k` notation.

## Responsive Behavior

### At 80 Columns

Use the two-column metadata block:

```text
owner: alice                         labels: [bug] [prio-1]
parent: #12 workspace polish         children: 2 open / 5 total
```

### Below 80 Columns

Collapse metadata to a stacked single-column block:

```text
owner: alice
labels: [bug] [prio-1]
parent: #12 workspace polish
children: 2 open / 5 total
```

Keep status visible by truncating title first.

### Wide Split Detail

The split-pane detail should use the same document-page grammar within the right pane. It may reduce vertical spacing, but it must not revert to the old loose header rows.

## Color And Weight

Use three tiers:

- Dim: metadata labels, timestamps, footer hints, separators.
- Normal: body text, comment text, child titles, label values.
- Accent: status, active tab brackets, cursor row marker, issue-number links, focused pane border, comment author names.

Rules:

- One app accent, matching the existing magenta/purple direction.
- Status: open green, closed cyan-dim.
- Labels: lightweight shared-accent or neutral pills.
- Section rule line is dim; section name is normal.
- No filled body blocks, no painted code-block backgrounds, no large boxed issue summary.

## Acceptance Criteria

- `80x50` stacked detail snapshot matches the document-page layout.
- Detail snapshots cover: no children, with children, all-projects metadata row, empty body, empty activity, long title truncation with visible status, long body Markdown, fenced code block with no background, 12-comment thread, and `NO_COLOR`.
- Split detail snapshot uses the same document-page grammar.
- Narrow snapshot below 80 columns stacks metadata and keeps status visible.
- Footer matrix tests use arrow movement labels in persistent footers and help-screen tests include `j/k` aliases.
- SSE tests or existing detail invalidation tests cover live tab counts after comment/link/issue events.
