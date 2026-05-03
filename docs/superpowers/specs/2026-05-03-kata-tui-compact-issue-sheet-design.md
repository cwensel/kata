# Kata TUI Compact Issue Sheet Design

Status: Approved direction from visual mockup V4.

## Goal

Replace the current detail-page treatment with a compact issue sheet that feels composed, dense, and readable on wide and normal terminals. The previous document-page direction improved hierarchy but still looked scattered because it used blank lines and long rules as primary structure.

## Direction

- Render the issue as one compact object near the top of the screen.
- Use subtle adaptive background bands for grouping:
  - one metadata band for owner/labels and parent/children
  - one compact section-header band for Body
  - one compact section-header band for Activity
- Remove most blank-line separators. Metadata should flow directly into Body; Body should flow directly into Activity unless content itself creates spacing.
- Keep spare terminal height below the composed issue sheet, not between sections.
- Avoid full-width decorative rules in the detail content.
- Keep status near the title as a small word or pill.
- Keep labels visually lighter than status.
- Support dark and light terminal themes through adaptive colors. `NO_COLOR` must remain readable through labels, indentation, and text hierarchy.

## Markdown

Issue bodies and comment bodies continue to use Glamour, but the style must be tuned for scanability:

- Inline code remains visibly distinct in `NO_COLOR`.
- Fenced code blocks render with subtle adaptive background in color modes.
- Code blocks do not use heavy painted backgrounds.
- Headings and lists should help structure body text without adding excessive margins.
- Markdown output must respect the actual body width and not overflow.

## Acceptance

- `80x50` detail snapshot shows the compact sheet with minimal internal blank lines.
- Color-mode unit tests prove section bands/code blocks have adaptive backgrounds in color modes and no backgrounds in `colorNone`.
- Existing no-color snapshots remain plain and readable.
- Full `go test -count=1 ./...` and `make build` pass.
