package tui

import (
	"strings"
	"testing"
)

// TestSanitizeForDisplay_StripsCSI: ANSI Color/cursor sequences must
// be removed so an agent-authored title can't paint the terminal.
func TestSanitizeForDisplay_StripsCSI(t *testing.T) {
	in := "\x1b[31mDANGER\x1b[0m fix login"
	got := sanitizeForDisplay(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived: %q", got)
	}
	if got != "DANGER fix login" {
		t.Fatalf("got %q, want %q", got, "DANGER fix login")
	}
}

// TestSanitizeForDisplay_StripsOSCWithBEL: an OSC sequence terminated
// by BEL (set window title) must be removed entirely.
func TestSanitizeForDisplay_StripsOSCWithBEL(t *testing.T) {
	in := "before\x1b]0;evil title\x07after"
	got := sanitizeForDisplay(in)
	if strings.Contains(got, "evil title") {
		t.Fatalf("OSC payload leaked: %q", got)
	}
	if got != "beforeafter" {
		t.Fatalf("got %q, want %q", got, "beforeafter")
	}
}

// TestSanitizeForDisplay_StripsOSCWithST: OSC terminated by the
// String Terminator (ESC \) — the other legal end marker.
func TestSanitizeForDisplay_StripsOSCWithST(t *testing.T) {
	in := "x\x1b]8;;file:///etc/passwd\x1b\\link\x1b]8;;\x1b\\y"
	got := sanitizeForDisplay(in)
	if strings.Contains(got, "file:///etc/passwd") {
		t.Fatalf("OSC hyperlink payload leaked: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived: %q", got)
	}
}

// TestSanitizeForDisplay_PreservesNewlineAndTab: bodies legitimately
// contain newlines and tabs; both must survive so multi-line content
// renders correctly.
func TestSanitizeForDisplay_PreservesNewlineAndTab(t *testing.T) {
	in := "line one\nline two\tindented"
	got := sanitizeForDisplay(in)
	if got != in {
		t.Fatalf("newline/tab dropped: got %q, want %q", got, in)
	}
}

// TestSanitizeForDisplay_StripsCarriageReturn: \r is not whitelisted —
// stripping it prevents an agent from overwriting the prior column on
// the same row.
func TestSanitizeForDisplay_StripsCarriageReturn(t *testing.T) {
	in := "real\rINJECTED"
	got := sanitizeForDisplay(in)
	if strings.Contains(got, "\r") {
		t.Fatalf("CR survived: %q", got)
	}
	if got != "realINJECTED" {
		t.Fatalf("got %q, want realINJECTED", got)
	}
}

// TestSanitizeForDisplay_StripsBareEsc: a bare ESC (no following
// CSI/OSC bracket) is dropped via the IsControl pass.
func TestSanitizeForDisplay_StripsBareEsc(t *testing.T) {
	in := "be\x1bfore"
	got := sanitizeForDisplay(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived: %q", got)
	}
	if got != "before" {
		t.Fatalf("got %q, want before", got)
	}
}

// TestSanitizeForDisplay_NoOpForPlainText: pure text is returned
// unchanged and no allocation is forced (the fast path returns s
// directly when no controls are present).
func TestSanitizeForDisplay_NoOpForPlainText(t *testing.T) {
	in := "fix login bug on Safari"
	if got := sanitizeForDisplay(in); got != in {
		t.Fatalf("plain text changed: got %q, want %q", got, in)
	}
}

// TestSanitizeForDisplay_PreservesUnicode: regular printable Unicode
// (CJK, emoji, accented Latin) must survive — sanitization only
// targets controls and escapes.
func TestSanitizeForDisplay_PreservesUnicode(t *testing.T) {
	in := "修复 login 🐛 résumé"
	if got := sanitizeForDisplay(in); got != in {
		t.Fatalf("unicode dropped: got %q, want %q", got, in)
	}
}

// TestSanitizeForDisplay_EmptyInput: empty string short-circuits.
func TestSanitizeForDisplay_EmptyInput(t *testing.T) {
	if got := sanitizeForDisplay(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestListView_SanitizesMaliciousTitle: an issue title with embedded
// ANSI escapes must not reach the rendered list view. Regression for
// the sanitize-at-render boundary in buildRows.
func TestListView_SanitizesMaliciousTitle(t *testing.T) {
	lm := newListModel()
	lm.loading = false
	lm.issues = []Issue{
		{Number: 1, Title: "\x1b]0;HIJACK\x07normal title", Status: "open"},
	}
	out := lm.View(120, 30)
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ESC reached rendered list: %q", out)
	}
	if strings.Contains(out, "HIJACK") {
		t.Fatalf("OSC payload reached rendered list: %q", out)
	}
	if !strings.Contains(out, "normal title") {
		t.Fatalf("legitimate title content dropped: %q", out)
	}
}

// TestDetailView_SanitizesMaliciousBody: an issue body containing a
// CSI sequence must be stripped before reaching the body window.
func TestDetailView_SanitizesMaliciousBody(t *testing.T) {
	dm := detailModel{
		issue: &Issue{
			Number: 42, Title: "x", Status: "open",
			Body: "first line\n\x1b[2Joverwrite-attack\nthird",
		},
	}
	out := dm.View(120, 30)
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ESC reached rendered detail body: %q", out)
	}
	if !strings.Contains(out, "overwrite-attack") {
		t.Fatalf("body text dropped (CSI strip should leave the payload): %q", out)
	}
}

// TestCommentsTab_SanitizesMaliciousAuthorAndBody: comment author and
// body are agent-supplied; both render paths must sanitize.
func TestCommentsTab_SanitizesMaliciousAuthorAndBody(t *testing.T) {
	cs := []CommentEntry{{
		ID: 1, Author: "alice\x1b[31m",
		Body: "body line\rOVERWRITE",
	}}
	out := renderCommentsTab(cs, 120, 20, 0, tabState{})
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ESC in comment author reached render: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("CR in comment body reached render: %q", out)
	}
}
