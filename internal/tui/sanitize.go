package tui

import (
	"regexp"
	"strings"
	"unicode"
)

// ansiEscapePattern matches ANSI escape sequences. It covers two
// shapes that can reach the terminal from agent-authored content:
//
//   - CSI: ESC [ ... <final byte> — colors, cursor moves, scroll regions.
//   - OSC: ESC ] ... (BEL | ESC \) — title sets, hyperlinks, palette.
//
// Other escape families (DCS, SOS, PM, APC) are rare in practice and
// the OSC alternation here would not catch them, but the
// IsControl-strip below removes their introducer (ESC) so they cannot
// initiate. Stripping the introducer alone leaves the payload as
// plain text in the buffer, which is the conservative outcome.
var ansiEscapePattern = regexp.MustCompile(
	`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\]([^\x07\x1b]|\x1b[^\\])*(\x07|\x1b\\)`,
)

// sanitizeForDisplay strips ANSI escape sequences and Unicode control
// characters from s so agent-authored text can't push the terminal
// into an arbitrary state (set the title, hyperlink to a malicious
// URL, redraw rows, suppress the cursor) when rendered in the TUI.
//
// Newlines and tabs are preserved because issue bodies and comments
// legitimately contain them; every other control rune is dropped. The
// output is safe to pass through lipgloss styling — lipgloss adds its
// own escape sequences after this point and they survive intact.
//
// Apply at every render-time boundary that touches user-supplied text:
// titles, bodies, comment bodies, authors, owners, event payloads,
// link authors. Daemon-generated structural tokens (status keywords,
// timestamps, issue numbers) are trusted and don't need this.
func sanitizeForDisplay(s string) string {
	if s == "" {
		return s
	}
	s = ansiEscapePattern.ReplaceAllString(s, "")
	if !strings.ContainsFunc(s, isStripControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isStripControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isStripControl reports whether r is a control character that should
// be stripped. Newline and tab survive so multi-line bodies render
// correctly; everything else (including \r, ESC, and Unicode format
// controls) is removed.
func isStripControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r)
}
