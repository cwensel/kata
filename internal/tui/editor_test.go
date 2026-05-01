package tui

import (
	"reflect"
	"strings"
	"testing"
)

// TestResolveEditor_VisualWinsOverEditor: $VISUAL takes precedence over
// $EDITOR per POSIX so a user with both set sees their preferred GUI.
func TestResolveEditor_VisualWinsOverEditor(t *testing.T) {
	t.Setenv("VISUAL", "code -w")
	t.Setenv("EDITOR", "vim")
	got := resolveEditor()
	want := []string{"code", "-w"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEditor() = %v, want %v", got, want)
	}
}

// TestResolveEditor_EditorUsedWhenVisualEmpty: with $VISUAL unset and
// $EDITOR set, the latter is used.
func TestResolveEditor_EditorUsedWhenVisualEmpty(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "vim")
	got := resolveEditor()
	want := []string{"vim"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEditor() = %v, want %v", got, want)
	}
}

// TestResolveEditor_FallsBackToVi: with neither set, vi is the safe
// default — every POSIX system has it.
func TestResolveEditor_FallsBackToVi(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	got := resolveEditor()
	want := []string{"vi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEditor() = %v, want %v", got, want)
	}
}

// TestResolveEditor_HandlesArgs: $EDITOR can carry args (e.g. "emacs
// -nw") and resolveEditor splits them into argv form.
func TestResolveEditor_HandlesArgs(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "emacs -nw")
	got := resolveEditor()
	want := []string{"emacs", "-nw"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEditor() = %v, want %v", got, want)
	}
}

// TestEditorTemplate_EditUsesExisting: edit kind seeds the buffer with
// the issue's current body so the user can revise in place.
func TestEditorTemplate_EditUsesExisting(t *testing.T) {
	if got := editorTemplate("edit", "foo"); got != "foo" {
		t.Fatalf("editorTemplate(edit, foo) = %q, want foo", got)
	}
}

// TestEditorTemplate_CommentHasPrompt: comment kind seeds the buffer
// with a sentinel-bracketed instruction block so trimComments can
// remove the prompt without touching legitimate Markdown headings the
// user may type into the body.
func TestEditorTemplate_CommentHasPrompt(t *testing.T) {
	got := editorTemplate("comment", "")
	if !strings.Contains(got, promptStartSentinel) ||
		!strings.Contains(got, promptEndSentinel) {
		t.Fatalf("editorTemplate(comment) missing sentinels: %q", got)
	}
	if !strings.Contains(got, "Write your comment above") {
		t.Fatalf("editorTemplate(comment) missing instruction text: %q", got)
	}
}

// TestEditorTemplate_CreateIsEmpty: create kind seeds nothing so the
// buffer opens blank.
func TestEditorTemplate_CreateIsEmpty(t *testing.T) {
	if got := editorTemplate("create", ""); got != "" {
		t.Fatalf("editorTemplate(create) = %q, want empty", got)
	}
}

// TestTrimComments_PreservesMarkdownHeadings: lines starting with #
// outside the sentinel block are user-authored Markdown headings and
// must NOT be stripped. This is the regression the sentinel scheme
// fixes.
func TestTrimComments_PreservesMarkdownHeadings(t *testing.T) {
	in := "# Heading\nbody"
	want := "# Heading\nbody"
	if got := trimComments(in); got != want {
		t.Fatalf("trimComments() = %q, want %q", got, want)
	}
}

// TestTrimComments_PreservesIndentedMarkdown: indented '#' is part of
// a code block or quote in Markdown; it must survive the strip.
func TestTrimComments_PreservesIndentedMarkdown(t *testing.T) {
	in := "  # not a comment\nfoo"
	want := "# not a comment\nfoo"
	if got := trimComments(in); got != want {
		t.Fatalf("trimComments() = %q, want %q", got, want)
	}
}

// TestTrimComments_StripsSentinelBlock: only the bracketed block is
// removed; surrounding user content (including headings) survives.
// The trailing newline after END is consumed so the surrounding text
// does not gain an extra blank line.
func TestTrimComments_StripsSentinelBlock(t *testing.T) {
	in := "user body\n" +
		promptStartSentinel + "\nignore me\n" + promptEndSentinel + "\nmore"
	want := "user body\nmore"
	if got := trimComments(in); got != want {
		t.Fatalf("trimComments() = %q, want %q", got, want)
	}
}

// TestTrimComments_HeadingAndSentinelBlock: a real Markdown heading
// outside the sentinel block survives even when the block is stripped.
// This is the combined regression: the comment template seeds a block,
// the user writes a heading at the top, and we must keep the heading.
func TestTrimComments_HeadingAndSentinelBlock(t *testing.T) {
	in := "# My summary\nactual content\n" +
		promptStartSentinel + "\nremoved\n" + promptEndSentinel
	got := trimComments(in)
	if !strings.Contains(got, "# My summary") {
		t.Fatalf("trimComments dropped Markdown heading: %q", got)
	}
	if strings.Contains(got, "removed") {
		t.Fatalf("trimComments did not strip sentinel block: %q", got)
	}
}

// TestTrimComments_OnlySentinelBlockTrimsToEmpty: a buffer of nothing
// but the sentinel block (and surrounding whitespace) trims to "" so
// the caller can cancel the operation.
func TestTrimComments_OnlySentinelBlockTrimsToEmpty(t *testing.T) {
	in := "\n" + promptStartSentinel + "\nprompt\n" + promptEndSentinel + "\n"
	if got := trimComments(in); got != "" {
		t.Fatalf("trimComments() = %q, want empty", got)
	}
}

// TestTrimComments_OrphanSentinelLeavesBufferAlone: if only the START
// sentinel is present (e.g. user deleted the END line), no stripping
// is attempted and the buffer is returned trimmed of trailing
// whitespace. Better to send the sentinel through than silently lose
// content.
func TestTrimComments_OrphanSentinelLeavesBufferAlone(t *testing.T) {
	in := "real text\n" + promptStartSentinel + "\nrest"
	got := trimComments(in)
	if !strings.Contains(got, promptStartSentinel) {
		t.Fatalf("orphan sentinel should pass through, got %q", got)
	}
	if !strings.Contains(got, "rest") {
		t.Fatalf("content after orphan sentinel dropped: %q", got)
	}
}
