package textsafe

import (
	"strings"
	"testing"
)

// TestBlock_StripsAnsi covers the headline malicious case: a string
// containing a screen-clear escape (ESC [ 2 J) is rendered without
// the escape so the agent can't blank the user's terminal.
func TestBlock_StripsAnsi(t *testing.T) {
	in := "before\x1b[2Jafter"
	got := Block(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived sanitization: %q", got)
	}
	if got != "beforeafter" {
		t.Fatalf("got %q, want %q", got, "beforeafter")
	}
}

// TestBlock_StripsOSC covers the OSC family — title sets and OSC-8
// hyperlinks, which can render as legitimate-looking text linking
// to an attacker URL.
func TestBlock_StripsOSC(t *testing.T) {
	in := "click \x1b]8;;https://evil.example/\x1b\\here\x1b]8;;\x1b\\!"
	got := Block(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived OSC sanitization: %q", got)
	}
}

// TestBlock_StripsBidiOverride covers the U+202E (RIGHT-TO-LEFT
// OVERRIDE) attack: a title can visually invert subsequent text.
func TestBlock_StripsBidiOverride(t *testing.T) {
	in := "safe" + string(rune(0x202E)) + "evil"
	got := Block(in)
	if strings.ContainsRune(got, 0x202E) {
		t.Fatalf("U+202E survived sanitization: %q", got)
	}
	if got != "safeevil" {
		t.Fatalf("got %q, want %q", got, "safeevil")
	}
}

// TestBlock_PreservesNewlinesAndTabs: Block is for multi-line
// contexts, so legitimate body content with newlines and tabs
// survives.
func TestBlock_PreservesNewlinesAndTabs(t *testing.T) {
	in := "line one\nline two\tindented"
	got := Block(in)
	if got != in {
		t.Fatalf("Block changed legitimate body content: got %q, want %q", got, in)
	}
}

// TestBlock_EmptyString returns empty without allocating.
func TestBlock_EmptyString(t *testing.T) {
	if got := Block(""); got != "" {
		t.Fatalf("Block(\"\") = %q, want empty", got)
	}
}

// TestLine_ReplacesNewlinesWithLiteralEscape: a title with embedded
// newlines breaks single-line list-row layout. Line replaces them
// with the literal `\n` so the row stays one visual line.
func TestLine_ReplacesNewlinesWithLiteralEscape(t *testing.T) {
	in := "title with\nembedded newline"
	got := Line(in)
	if strings.Contains(got, "\n") {
		t.Fatalf("Line preserved a literal newline: %q", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Fatalf("Line did not replace newline with `\\n`: %q", got)
	}
}

// TestLine_ReplacesTabsWithSpace: tabs render at variable widths
// across terminals and break column alignment. Replace with a
// single space.
func TestLine_ReplacesTabsWithSpace(t *testing.T) {
	in := "title\twith\ttabs"
	got := Line(in)
	if strings.ContainsRune(got, '\t') {
		t.Fatalf("Line preserved a literal tab: %q", got)
	}
	if got != "title with tabs" {
		t.Fatalf("got %q, want %q", got, "title with tabs")
	}
}

// TestLine_AlsoStripsAnsi: Line stacks on top of Block so ANSI is
// stripped here too.
func TestLine_AlsoStripsAnsi(t *testing.T) {
	in := "title\x1b[31mred\x1b[0mback"
	got := Line(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived Line sanitization: %q", got)
	}
}

// TestLine_StripsCarriageReturn: \r in a single-row context can
// rewind the cursor and overwrite the rest of the row. Block
// already strips it; verify here for the single-line context.
func TestLine_StripsCarriageReturn(t *testing.T) {
	in := "good\rbad"
	got := Line(in)
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("\\r survived: %q", got)
	}
}
