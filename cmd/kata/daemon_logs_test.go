package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesm/kata/internal/config"
)

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
	dbHash = config.DBHash(filepath.Join(home, "kata.db"))
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

// TestDaemonLogs_Hooks_Tail_RotatedOnlyWaitsForActive guards the
// awaitActiveFile contract that --tail will not latch onto a rotated
// runs.jsonl.N when the active runs.jsonl is missing. Before the fix,
// the tail loop would early-return with the smallest-numbered rotated
// file and never observe future writes to runs.jsonl.
func TestDaemonLogs_Hooks_Tail_RotatedOnlyWaitsForActive(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only a rotated file exists at startup.
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl.1"),
		[]byte(`{"event_id":99,"result":"ok"}`+"\n"), 0o600); err != nil {
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
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "runs.jsonl"),
			[]byte(`{"event_id":7,"result":"ok"}`+"\n"), 0o600)
	}()

	_ = cmd.Execute()
	out := buf.String()
	if !strings.Contains(out, `"event_id":7`) {
		t.Fatalf("tail must follow runs.jsonl after it appears: %q", out)
	}
}

// TestEmitNewLines_PartialTrailingLine_NotConsumed pins the contract
// that emitNewLines does NOT advance the caller's offset across a
// partial trailing line. Before the fix, every scanned record advanced
// `read` by len(line)+1, which over-counted the unflushed mid-line by
// 1 byte and caused later ticks to miss content.
func TestEmitNewLines_PartialTrailingLine_NotConsumed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	first := `{"event_id":1,"result":"ok"}` + "\n"
	partial := `{"event_id":2`
	if err := os.WriteFile(path, []byte(first+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &hookLogFilter{hookIndex: -1}
	var stdout, stderr bytes.Buffer
	n, err := emitNewLines(path, 0, &stdout, &stderr, f)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(first)) {
		t.Fatalf("read=%d, want %d (partial line must not advance offset)", n, len(first))
	}
	if !strings.Contains(stdout.String(), `"event_id":1`) {
		t.Fatalf("first line should print, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), `"event_id":2`) {
		t.Fatalf("partial line must not print, got %q", stdout.String())
	}

	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-controlled temp path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(`,"result":"ok"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()

	stdout.Reset()
	stderr.Reset()
	n2, err := emitNewLines(path, n, &stdout, &stderr, f)
	if err != nil {
		t.Fatal(err)
	}
	if n2 == 0 {
		t.Fatalf("second tick should consume the now-completed line, got n2=%d", n2)
	}
	if !strings.Contains(stdout.String(), `"event_id":2`) {
		t.Fatalf("second line should print after completion: %q", stdout.String())
	}
}

// TestFollowActive_NoMark_EmitsExistingContent pins the contract that
// when runHookLogOnce did not see runs.jsonl (mark.set=false), follow
// emits everything currently in the file. This covers the case where
// the active file appears between one-shot and follow.
func TestFollowActive_NoMark_EmitsExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(path, []byte(`{"event_id":42,"result":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	f := &hookLogFilter{hookIndex: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := followActive(ctx, &buf, io.Discard, path, activeMark{}, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"event_id":42`) {
		t.Fatalf("unset mark should emit existing content: %q", buf.String())
	}
}

// TestFollowActive_MarkAtSize_DoesNotReEmit pins the contract that
// when runHookLogOnce already consumed the file up to mark.size,
// follow resumes there and does NOT re-print prior content.
func TestFollowActive_MarkAtSize_DoesNotReEmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	initial := []byte(`{"event_id":1,"result":"ok"}` + "\n")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	f := &hookLogFilter{hookIndex: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	mark := activeMark{set: true, info: info, size: info.Size()}
	if err := followActive(ctx, &buf, io.Discard, path, mark, f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"event_id":1`) {
		t.Fatalf("mark at size should suppress re-emission: %q", buf.String())
	}
}

// TestRunHookLogOnce_Mark_ReportsActiveFileSize verifies that the
// one-shot pass reports a set mark with the size of runs.jsonl after
// it was read, so tail can resume at the exact byte offset.
func TestRunHookLogOnce_Mark_ReportsActiveFileSize(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := `{"event_id":1,"result":"ok"}` + "\n"
	path := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	mark, err := runHookLogOnce(&stdout, &stderr, 100, &hookLogFilter{hookIndex: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !mark.set {
		t.Fatal("mark should be set when runs.jsonl was in the snapshot")
	}
	if mark.size != int64(len(contents)) {
		t.Fatalf("mark.size = %d, want %d", mark.size, len(contents))
	}
}

// TestRunHookLogOnce_Mark_UnsetWhenActiveAbsent verifies that the
// one-shot mark stays unset when runs.jsonl is absent — so follow
// later starts at offset 0 once the file appears.
func TestRunHookLogOnce_Mark_UnsetWhenActiveAbsent(t *testing.T) {
	_, dir, _ := setupHooksDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only a rotated file exists.
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl.1"),
		[]byte(`{"event_id":99,"result":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	mark, err := runHookLogOnce(&stdout, &stderr, 100, &hookLogFilter{hookIndex: -1})
	if err != nil {
		t.Fatal(err)
	}
	if mark.set {
		t.Fatal("mark must stay unset when runs.jsonl wasn't in snapshot")
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
