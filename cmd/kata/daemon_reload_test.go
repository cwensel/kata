package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonReload_NoRunningDaemon_ExitUsage(t *testing.T) {
	resetFlags(t)
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "reload"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no daemon running")
	}
	var ce *cliError
	if !errors.As(err, &ce) || ce.ExitCode != ExitUsage {
		t.Fatalf("err = %v, want ExitUsage cliError", err)
	}
	if !strings.Contains(strings.ToLower(ce.Message), "no daemon") {
		t.Fatalf("message should mention no daemon: %q", ce.Message)
	}
}
