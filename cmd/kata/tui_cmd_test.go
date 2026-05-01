package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TUI requires a real PTY, so the test mode short-circuits before
// starting the program. We just verify the command exists and accepts
// the expected flags.
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
