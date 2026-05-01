package tui

import (
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// editorCmd suspends Bubble Tea, runs $EDITOR on a tmpfile pre-seeded
// with `template`, and returns editorReturnedMsg with the final
// content. kind is one of "create" | "edit" | "comment" so the caller
// knows which mutation to dispatch.
//
// tea.ExecProcess tears down the renderer, runs the child process, and
// re-establishes the renderer when the child exits. While the child is
// running, the terminal belongs to $EDITOR — Bubble Tea is paused, so
// keys (including 'q') do not reach our handlers.
func editorCmd(kind, template string) tea.Cmd {
	tmp, err := os.CreateTemp("", "kata-*.md")
	if err != nil {
		return func() tea.Msg { return editorReturnedMsg{kind: kind, err: err} }
	}
	// tmp.Name() is os.CreateTemp's tmp path, not user input — nolint:gosec
	// silences G703 (path-traversal via taint) for the os.Remove/ReadFile
	// calls below.
	name := tmp.Name()
	if _, err := tmp.WriteString(template); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name) //nolint:gosec // path is os.CreateTemp output
		return func() tea.Msg { return editorReturnedMsg{kind: kind, err: err} }
	}
	_ = tmp.Close()
	editor := resolveEditor()
	//nolint:gosec // user-controlled $EDITOR is intentional
	cmd := exec.Command(editor[0], append(editor[1:], name)...)
	return tea.ExecProcess(cmd, func(execErr error) tea.Msg {
		defer func() { _ = os.Remove(name) }() //nolint:gosec // path is os.CreateTemp output
		if execErr != nil {
			return editorReturnedMsg{kind: kind, err: execErr}
		}
		content, err := os.ReadFile(name) //nolint:gosec // path is os.CreateTemp output
		if err != nil {
			return editorReturnedMsg{kind: kind, err: err}
		}
		return editorReturnedMsg{kind: kind, content: string(content)}
	})
}

// resolveEditor returns the command (with args) to invoke. Honors
// $VISUAL > $EDITOR per POSIX convention; falls back to vi.
func resolveEditor() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := os.Getenv(env); v != "" {
			parts := strings.Fields(v)
			if len(parts) > 0 {
				return parts
			}
		}
	}
	return []string{"vi"}
}

// promptStartSentinel and promptEndSentinel bracket the seeded prompt
// inside the editor buffer. trimComments removes only the bracketed
// region; user-authored Markdown headings (like "# Heading") outside
// the sentinel block are preserved. The sentinels live on their own
// lines so a user who edits inside the block does not accidentally
// disable the strip.
const (
	promptStartSentinel = "<!-- ---KATA-PROMPT-START--- -->"
	promptEndSentinel   = "<!-- ---KATA-PROMPT-END--- -->"
)

// editorTemplate composes the seed text for `kind`. For edit it's the
// existing body; for comment it's a sentinel-bracketed prompt block
// that trimComments strips on save; for create body it's empty.
//
// Earlier the comment template seeded a single "# Write your comment
// above…" line and trimComments stripped every line starting with #.
// That destroyed legitimate Markdown headings in user content. The
// sentinel block (HTML comments) is invisible in Markdown rendering
// and survives unmolested if the user accidentally moves text inside
// it; trimComments removes only the bracketed region.
func editorTemplate(kind, existing string) string {
	switch kind {
	case "edit":
		return existing
	case "comment":
		return commentTemplate()
	}
	return "" // create: empty
}

// commentTemplate seeds the comment buffer with a sentinel-bracketed
// instruction block. The block sits below the (empty) author area so
// the user types above it; trimComments removes the block on save.
func commentTemplate() string {
	return strings.Join([]string{
		"",
		promptStartSentinel,
		"Write your comment above. This block is removed on save;",
		"Markdown headings (lines starting with #) outside this block",
		"are preserved.",
		promptEndSentinel,
		"",
	}, "\n")
}

// trimComments removes the sentinel-bracketed prompt region (if any)
// from user content. Lines outside the START/END markers are
// preserved verbatim — including legitimate Markdown headings. An
// empty result after stripping + trimSpace signals the caller to
// cancel the operation.
//
// If only one sentinel is present (or neither), no stripping happens
// and the buffer is returned trimmed of trailing whitespace.
func trimComments(s string) string {
	stripped := stripSentinelBlock(s)
	return strings.TrimSpace(stripped)
}

// stripSentinelBlock removes the first START..END bracketed block
// from s. The function preserves a leading newline pattern so a buffer
// of the form "user text\n<block>" becomes "user text\n" rather than
// "user text<empty>", keeping the trailing newline ergonomics that
// callers expect when joining with other text.
func stripSentinelBlock(s string) string {
	startIdx := strings.Index(s, promptStartSentinel)
	if startIdx < 0 {
		return s
	}
	endIdx := strings.Index(s[startIdx:], promptEndSentinel)
	if endIdx < 0 {
		return s
	}
	endIdx += startIdx + len(promptEndSentinel)
	// Consume one trailing newline after END so the surrounding text
	// doesn't gain an extra blank line from the strip.
	if endIdx < len(s) && s[endIdx] == '\n' {
		endIdx++
	}
	return s[:startIdx] + s[endIdx:]
}
