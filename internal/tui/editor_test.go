package tui

import (
	"reflect"
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

// TestEditorTemplate_CommentHasPrompt: comment kind seeds an instructive
// "# Write your comment above" line so first-time users know what to do.
func TestEditorTemplate_CommentHasPrompt(t *testing.T) {
	got := editorTemplate("comment", "")
	if len(got) == 0 || got[0] != '#' {
		t.Fatalf("editorTemplate(comment) = %q, want a leading # prompt", got)
	}
}

// TestEditorTemplate_CreateIsEmpty: create kind seeds nothing so the
// buffer opens blank.
func TestEditorTemplate_CreateIsEmpty(t *testing.T) {
	if got := editorTemplate("create", ""); got != "" {
		t.Fatalf("editorTemplate(create) = %q, want empty", got)
	}
}

// TestTrimComments_StripsHashLines: lines that begin with '#' (after
// whitespace) are dropped per git/hg convention.
func TestTrimComments_StripsHashLines(t *testing.T) {
	in := "hello\n# comment\nworld"
	want := "hello\nworld"
	if got := trimComments(in); got != want {
		t.Fatalf("trimComments() = %q, want %q", got, want)
	}
}

// TestTrimComments_StripsLeadingWhitespaceHash: indented '#' lines are
// also comments — matches git's strictness.
func TestTrimComments_StripsLeadingWhitespaceHash(t *testing.T) {
	in := "  # comment\nfoo"
	want := "foo"
	if got := trimComments(in); got != want {
		t.Fatalf("trimComments() = %q, want %q", got, want)
	}
}

// TestTrimComments_EmptyResultAfterStrip: a buffer that is only comments
// trims to "" so the caller can cancel the operation.
func TestTrimComments_EmptyResultAfterStrip(t *testing.T) {
	in := "# only comments\n# more"
	if got := trimComments(in); got != "" {
		t.Fatalf("trimComments() = %q, want empty", got)
	}
}
