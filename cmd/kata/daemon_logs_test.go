package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesm/kata/internal/config"
)

func computeDBHashForTest(dbPath string) string { return config.DBHash(dbPath) }

func writeRuns(t *testing.T, dir string, files map[string][]map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, lines := range files {
		var buf bytes.Buffer
		for _, l := range lines {
			b, _ := json.Marshal(l)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func setupHooksDir(t *testing.T) (home, hooksDir, dbHash string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))
	if err := os.WriteFile(filepath.Join(home, "kata.db"), []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "kata.db")
	dbHash = computeDBHashForTest(dbPath)
	hooksDir = filepath.Join(home, "hooks", dbHash)
	return
}

func TestDaemonLogs_Hooks_PrintsChronological(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	writeRuns(t, dir, map[string][]map[string]any{
		"runs.jsonl.2": {{"event_id": 1, "result": "ok"}},
		"runs.jsonl.1": {{"event_id": 2, "result": "ok"}},
		"runs.jsonl":   {{"event_id": 3, "result": "ok"}, {"event_id": 4, "result": "ok"}},
	})
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"daemon", "logs", "--hooks"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	idx1 := strings.Index(out, `"event_id":1`)
	idx2 := strings.Index(out, `"event_id":2`)
	idx3 := strings.Index(out, `"event_id":3`)
	idx4 := strings.Index(out, `"event_id":4`)
	if idx1 >= idx2 || idx2 >= idx3 || idx3 >= idx4 {
		t.Fatalf("chronological order violated: %v %v %v %v", idx1, idx2, idx3, idx4)
	}
}

func TestDaemonLogs_Hooks_FailedOnly(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	writeRuns(t, dir, map[string][]map[string]any{
		"runs.jsonl": {
			{"event_id": 1, "result": "ok", "exit_code": 0},
			{"event_id": 2, "result": "ok", "exit_code": 7},
			{"event_id": 3, "result": "timed_out", "exit_code": -1},
		},
	})
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"daemon", "logs", "--hooks", "--failed-only"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, `"event_id":1`) {
		t.Fatal("--failed-only should exclude ok exit_code=0")
	}
	if !strings.Contains(out, `"event_id":2`) || !strings.Contains(out, `"event_id":3`) {
		t.Fatal("--failed-only should include nonzero exit and timed_out")
	}
}

func TestDaemonLogs_Hooks_MalformedLineSkippedWithStderrWarning(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "{\"event_id\":1,\"result\":\"ok\"}\nnot-json\n{\"event_id\":2,\"result\":\"ok\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	resetFlags(t)
	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"daemon", "logs", "--hooks"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"event_id":1`) || !strings.Contains(stdout.String(), `"event_id":2`) {
		t.Fatal("valid lines should still print")
	}
	if !strings.Contains(stderr.String(), "skipping malformed line") {
		t.Fatalf("stderr should warn about malformed line: %q", stderr.String())
	}
}

func TestDaemonLogs_Hooks_Tail_PicksUpNewLines(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(path, []byte(`{"event_id":1,"result":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetFlags(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"daemon", "logs", "--hooks", "--tail"})
	cmd.SetContext(ctx)

	go func() {
		time.Sleep(200 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-controlled temp path
		_, _ = f.WriteString(`{"event_id":2,"result":"ok"}` + "\n")
		_ = f.Close()
	}()

	_ = cmd.Execute()
	out := buf.String()
	if !strings.Contains(out, `"event_id":1`) || !strings.Contains(out, `"event_id":2`) {
		t.Fatalf("tail should print initial + appended: %q", out)
	}
}
