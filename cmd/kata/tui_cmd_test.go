package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// kata tui needs a TTY, so we exercise the registration via --help;
// cobra prints help text and returns before RunE is invoked.
func TestTUI_CommandRegistered(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"tui", "--help"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"--all-projects", "--include-deleted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected --help to mention %q, got: %s", want, out)
		}
	}
}

// TestTUI_RejectsExtraArgs guards the cobra.NoArgs constraint: a typo'd
// positional must error out before RunE so the user sees a usage
// failure (and the TTY check in tui.Run is never reached, which would
// be inappropriate for an arg-parse failure).
func TestTUI_RejectsExtraArgs(t *testing.T) {
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"tui", "extra-positional"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for extra positional arg")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown command") &&
		!strings.Contains(msg, "accepts no args") {
		t.Fatalf("unexpected error: %v", err)
	}
}
