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

// editorTemplate composes the seed text for `kind`. For edit it's the
// existing body; for comment it's a "# write above" prompt; for create
// body it's empty.
func editorTemplate(kind, existing string) string {
	switch kind {
	case "edit":
		return existing
	case "comment":
		return "# Write your comment above. Lines starting with # are ignored.\n"
	}
	return "" // create: empty
}

// trimComments strips lines starting with `#` from user content,
// matching git/hg conventions. Leading whitespace before `#` is also
// treated as a comment marker. Trailing whitespace is trimmed; an empty
// result signals the caller to cancel the operation.
func trimComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
