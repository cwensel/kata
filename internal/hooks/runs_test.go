package hooks

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunsAppender_OneLinePerRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	app, err := newRunsAppender(path, 1<<20, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Append(runRecord{Version: 1, EventID: 1, Result: "ok"})
	app.Append(runRecord{Version: 1, EventID: 2, Result: "ok"})
	app.Append(runRecord{Version: 1, EventID: 3, Result: "ok"})
	data, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled path under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for scanner.Scan() {
		var r runRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("line %d not JSON: %v", count, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("got %d lines, want 3", count)
	}
}

func TestRunsAppender_RotatesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	// 1KB threshold; each runRecord is well over 100B, so a few writes rotate.
	app, err := newRunsAppender(path, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	for i := 0; i < 50; i++ {
		app.Append(runRecord{Version: 1, EventID: int64(i), Result: "ok",
			HookCommand: "/usr/local/bin/something/longer"})
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active file missing: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated .1 missing: %v", err)
	}
}

func TestRunsAppender_KeepsAtMostKeepFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	app, err := newRunsAppender(path, 256, 2) // keep .1 and .2
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	for i := 0; i < 200; i++ {
		app.Append(runRecord{Version: 1, EventID: int64(i), Result: "ok",
			HookCommand: "/usr/local/bin/notify"})
	}
	for _, n := range []string{".1", ".2"} {
		if _, err := os.Stat(path + n); err != nil {
			t.Fatalf("expected %s to exist: %v", path+n, err)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("runs.jsonl.3 should have been dropped")
	}
}

func TestRunsAppender_ConcurrentAppends_NoInterleave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	app, err := newRunsAppender(path, 1<<20, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				app.Append(runRecord{Version: 1, EventID: int64(id*1000 + i), Result: "ok"})
			}
		}(w)
	}
	wg.Wait()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled path under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r runRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d invalid JSON: %v (line=%q)", i, err, line)
		}
	}
}
