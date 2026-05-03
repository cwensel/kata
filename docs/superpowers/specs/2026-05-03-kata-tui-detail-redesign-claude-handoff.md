# Kata TUI Detail Redesign Handoff

Status: handoff brief after failed implementation attempts.

## Context

The kata TUI has had several recent passes:

- `61e2bd4 feat(tui): port adaptive contextual hint tables`
- `272da23 feat(tui): redesign issue detail as document page`
- `c3154e7 docs: specify compact TUI issue sheet`
- `78ea512 feat(tui): compact issue detail sheet`

Those changes improved some mechanics, but the issue detail view is still not good enough. Treat the current detail UI as a failed iteration, not a foundation to polish lightly.

Primary reference screenshot:

`/Users/wesm/Desktop/Screenshot 2026-05-03 at 12.34.08.png`

That screenshot is more important than the current goldens. It shows what the user actually sees in the running binary.

## Observed Failures

1. The project bar is missing.

   The stacked detail view is supposed to keep the global project/title bar at the top, e.g. `Project: kata` on the left and `kata かた` on the right. The live screenshot starts directly with `#1 fix the tui...`, so the global chrome was lost somewhere in the actual runtime path. `internal/tui/detail_render.go` currently calls `renderTitleBar`, but do not trust that alone. Verify the running binary and the `Model.View`/layout path.

2. The page starts at column zero and feels raw.

   The issue number, title, metadata, body, and activity all begin at the extreme left edge. It reads like debug output rather than a designed workspace. The detail content needs a small, consistent left gutter.

3. The title is too loud.

   The title is bright magenta, very large visually, and dominates the screen. It should be prominent, but not theatrical. The issue number, title, and status should read as a composed header, not three scattered fields.

4. Metadata is scattered and incomplete.

   `owner: none` and `parent: none` appear as large left-side labels, while other metadata either disappears or produces garbled `��` artifacts far to the right. Empty metadata should not consume attention, and no row may emit replacement-character artifacts.

5. Section backgrounds are heavy and awkward.

   The Body header is a dark full-width slab, then the word `Body` appears again immediately below it as body content. This creates visual noise and makes the first body line look like a duplicate heading. Background surfaces should be subtle and content-width-limited, not full terminal-width blocks.

6. Activity defaults to an empty tab.

   The screenshot shows `Comments (0)` active and `(no comments)` rendered while `Events (1)` exists. On first open, the detail view should select the first non-empty activity tab. Empty comments should not be the default when useful activity exists.

7. The footer dominates the page.

   The adaptive hint table is better than the old footer, but in the detail screenshot it still reads as a heavy bottom toolbar. It should be quieter and should not compete with the issue content.

8. Wide-terminal behavior is not professional.

   The screenshot is wide. The current output leaves large areas of unstructured emptiness and occasional right-side artifacts. The detail content should have a max readable measure, with spare width left quiet and clean.

## Non-Negotiables

- The top project/title bar must always render in stacked detail mode, unless the terminal is in an explicit tiny fallback.
- The detail view must look acceptable in the actual running binary, not just in no-color golden snapshots.
- No replacement-character artifacts (`�`) may appear due to width math, truncation, ANSI handling, or missing glyph fallbacks.
- Empty data should not create visual clutter. Prefer omission or a quiet placeholder over repeating `none` everywhere.
- Comments must not be the active Activity tab on first open when comments are empty and another activity tab has entries.
- `NO_COLOR` must remain readable through text hierarchy and spacing.
- Light and dark terminals must both work. Avoid assuming a black background.

## Recommended Direction

Replace the current detail rendering with a stricter compact card/sheet layout.

At a high level:

```text
 Project: kata                                                   kata かた

  #1  fix the tui to be less shitty                         open
      anonymous · created May 2 19:15 · updated 21h ago
      owner none   parent none

  Body
      enter some stuff here

  Activity    Comments 0    [ Events 1 ]    Links 0
      May 2 19:16  issue.created  anonymous created this issue


  ↑↓ move   ↹ section   ↵ open   esc back   ? help
```

This is intentionally restrained:

- The global project bar is separate from issue content.
- The issue content has a left gutter.
- The title line is compact and not full-width shouting.
- Metadata appears only when useful, and uses a compact row.
- Section labels are simple. If background is used, it should be subtle and limited to the content measure.
- Activity chooses the first useful tab.
- The footer is lower contrast and short.

## Layout Rules

Use a content measure rather than the full terminal width:

- Content gutter: 2 cells at normal widths.
- Content max width: around 96 cells.
- At widths below 80, keep the gutter if possible but prioritize fitting content.
- Do not right-align metadata across the whole terminal. Right alignment should only happen inside the content measure.
- Spare terminal width should stay empty, not filled with section bands.

Title row:

- Render `#N`, title, and status on one row.
- Status must stay visible.
- Long titles truncate before status.
- Status should be a word or very light chip. It should not look like a label chip.
- Use less aggressive title color/weight than the current magenta treatment.

Metadata:

- Prefer one compact row under the title/byline.
- Show `owner none` and `parent none` only if the absence is useful.
- Hide `labels none` and `children none`.
- Show labels only when labels exist.
- Show children only when children exist.
- In all-projects mode, include project metadata near the top of the sheet; in single-project mode, rely on the global project bar.

Body:

- Always render a Body section.
- Empty body uses a quiet `(no description)` placeholder.
- If the body markdown itself starts with a heading like `# Body`, avoid making the UI look duplicated. Either render markdown headings with less visual weight or add enough distinction between UI section labels and body content.
- Glamour markdown support should remain, but tune margins down.

Activity:

- Render Activity when comments/events/links exist or are loading/error.
- On first open, choose the first non-empty tab in this order: Comments, Events, Links. If Comments is empty and Events has entries, default to Events.
- Empty active tabs may show `(no comments)`, `(no events)`, or `(no links)`, but only when there is no better non-empty default or the user explicitly navigated there.
- Active tab must be visible in `NO_COLOR`, e.g. brackets.

Footer:

- Keep only core persistent hints in detail mode:
  - `↑↓ move`
  - `↹ section`
  - `↵ open`
  - `esc back`
  - `? help`
- Secondary actions (`e edit`, `c comment`, `x close`, `+ label`, `a owner`, `q quit`) can move to full help or appear contextually only when they do not make the footer dominate.
- Do not use a heavy filled footer in detail mode.

## Visual Style Rules

- Use three levels only:
  - Dim: metadata labels, timestamps, footer text.
  - Normal: issue body, comment/event text.
  - Accent: active tab, focused row, status.
- Avoid full-width dark slabs.
- If using backgrounds, make them subtle and content-width-limited.
- Do not let magenta dominate the page. The current title and active tab treatment is too loud.
- Labels should be lighter than status.
- No UI text should look randomly stranded in the middle or right side of the terminal.

## Code Areas To Inspect

- `internal/tui/detail_render.go`
  - Main stacked detail rendering.
  - Current `renderTitleBar` call exists here, but the live screenshot still lacks the project bar.
  - Current metadata rendering and section header rendering are likely the first things to replace.

- `internal/tui/split_render.go`
  - Split detail pane should share the same detail grammar, minus the global top bar owned by split layout.

- `internal/tui/detail.go`
  - Focus and active-tab initialization likely need adjustment so first open selects a useful tab.

- `internal/tui/footer_hints.go`
  - Footer hint set should be reduced for detail mode or styled less heavily.

- `internal/tui/theme.go`
  - Current detail backgrounds and title/accent weights need recalibration.

- `internal/tui/markdown_render.go`
  - Keep Glamour, but reduce margins and ensure markdown headings do not fight UI section labels.

- `internal/tui/list_render.go`
  - `renderTitleBar` is the expected global project bar helper.

## Suggested Test Coverage

Add or update tests before implementation:

- A live-ish stacked detail snapshot at a wide size similar to the screenshot, e.g. `160x32`.
- Existing `80x50` detail snapshot.
- A no-color detail snapshot.
- A detail snapshot with:
  - no comments
  - one event
  - no links
  - expected active Activity tab is Events.
- A wide detail test that fails if `�` appears.
- A stacked detail test that asserts the first line contains `Project:`.
- A test that `labels none` and `children none` do not render in single-project detail when empty.

Run:

```sh
go test ./internal/tui -count=1
go test -count=1 ./...
make build
```

Then run the actual binary and compare against the screenshot-scale terminal, because the prior attempts overfit golden snapshots and still failed visually.

## Acceptance Criteria

The next implementation is acceptable only if:

- The first visible row in stacked detail is the project/title bar.
- The issue content has a clear left gutter and no column-zero dump.
- No `�` characters appear.
- The title/status/header area reads as one composed object.
- Empty metadata is quiet or omitted.
- Body and Activity sections are visually distinct without heavy slabs.
- Activity defaults to useful content.
- The footer is quieter than the issue content.
- The result looks good in a real wide terminal screenshot, not only in golden text fixtures.

