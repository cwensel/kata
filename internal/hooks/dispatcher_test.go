package hooks

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesm/kata/internal/db"
)

func mustNewDispatcher(t *testing.T, hooks []ResolvedHook, cfg Config) (*Dispatcher, *strings.Builder, string) {
	t.Helper()
	root := t.TempDir()
	dbHash := "testdbhash01"
	logBuf := &strings.Builder{}
	deps := DispatcherDeps{
		DBHash:          dbHash,
		KataHome:        root,
		DaemonLog:       log.New(logBuf, "", 0),
		AliasResolver:   func(_ db.Event) (AliasSnapshot, bool, error) { return AliasSnapshot{}, false, nil },
		IssueResolver:   func(_ context.Context, _ int64) (IssueSnapshot, error) { return IssueSnapshot{}, nil },
		CommentResolver: func(_ context.Context, _ int64) (CommentSnapshot, error) { return CommentSnapshot{}, nil },
		ProjectResolver: func(_ context.Context, _ int64) (ProjectSnapshot, error) { return ProjectSnapshot{}, nil },
		Now:             time.Now,
		GraceWindow:     50 * time.Millisecond,
	}
	loaded := LoadedConfig{Snapshot: Snapshot{Hooks: hooks}, Config: cfg}
	d, err := New(loaded, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	})
	return d, logBuf, dbHash
}

func TestDispatcher_NewNoop_ImplementsSink(t *testing.T) {
	s := NewNoop()
	s.Enqueue(db.Event{ID: 1, Type: "issue.created"}) // must not panic
	if _, ok := s.(*Dispatcher); ok {
		t.Fatal("NewNoop should not return *Dispatcher")
	}
}

func TestDispatcher_Enqueue_RoutesToMatchingHooks(t *testing.T) {
	bin := hookprobePath(t)
	dir := t.TempDir()
	hookA := ResolvedHook{Index: 0, Event: "issue.created", Match: matchExact("issue.created"), Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: dir}
	hookB := ResolvedHook{Index: 1, Event: "issue.updated", Match: matchExact("issue.updated"), Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: dir}
	cfg := defaultConfig()
	cfg.PoolSize = 2
	cfg.QueueCap = 8
	d, _, dbHash := mustNewDispatcher(t, []ResolvedHook{hookA, hookB}, cfg)
	d.Enqueue(db.Event{ID: 100, Type: "issue.created", ProjectID: 1, ProjectIdentity: "x"})
	// Wait for runs.jsonl to receive a line.
	runsPath := filepath.Join(d.deps.KataHome, "hooks", dbHash, "runs.jsonl")
	if !waitForLines(t, runsPath, 1, 2*time.Second) {
		t.Fatal("expected 1 run for hookA")
	}
	d.Enqueue(db.Event{ID: 101, Type: "issue.updated", ProjectID: 1, ProjectIdentity: "x"})
	if !waitForLines(t, runsPath, 2, 2*time.Second) {
		t.Fatal("expected 2 runs after second event")
	}
}

func TestDispatcher_Enqueue_QueueFullDropsAndCounts(t *testing.T) {
	bin := hookprobePath(t)
	dir := t.TempDir()
	slow := ResolvedHook{Index: 0, Event: "*", Match: matchAlways(), Command: bin, Args: []string{"sleep", "200ms"}, Timeout: 2 * time.Second, WorkingDir: dir}
	cfg := defaultConfig()
	cfg.PoolSize = 1
	cfg.QueueCap = 1
	cfg.QueueFullLogInterval = 10 * time.Millisecond
	d, _, _ := mustNewDispatcher(t, []ResolvedHook{slow}, cfg)
	for i := 0; i < 10; i++ {
		d.Enqueue(db.Event{ID: int64(200 + i), Type: "issue.created", ProjectID: 1, ProjectIdentity: "x"})
	}
	// At least N-2 should drop (1 in queue + 1 in flight + N-2 dropped).
	if got := d.dropped.Load(); got < 5 {
		t.Fatalf("dropped=%d, want >=5", got)
	}
}

func TestDispatcher_Enqueue_AfterShutdown_NoOp(t *testing.T) {
	cfg := defaultConfig()
	d, _, _ := mustNewDispatcher(t, nil, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	d.Enqueue(db.Event{ID: 1, Type: "issue.created"}) // must not panic
}

func TestDispatcher_Shutdown_Idempotent(t *testing.T) {
	cfg := defaultConfig()
	d, _, _ := mustNewDispatcher(t, nil, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown should return nil, got %v", err)
	}
}

func TestDispatcher_Shutdown_Timeout_ReportsInflight(t *testing.T) {
	bin := hookprobePath(t)
	dir := t.TempDir()
	stuck := ResolvedHook{Index: 0, Event: "*", Match: matchAlways(), Command: bin, Args: []string{"term-ignore", "10s"}, Timeout: 5 * time.Second, WorkingDir: dir}
	cfg := defaultConfig()
	cfg.PoolSize = 1
	cfg.QueueCap = 4
	d, logBuf, _ := mustNewDispatcher(t, []ResolvedHook{stuck}, cfg)
	d.Enqueue(db.Event{ID: 300, Type: "issue.created", ProjectID: 1, ProjectIdentity: "x"})
	// Give the worker a moment to start.
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := d.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown with 50ms ctx vs term-ignore should return error")
	}
	if !strings.Contains(logBuf.String(), "timed out") {
		t.Fatalf("daemon log missing 'timed out': %q", logBuf.String())
	}
}

func TestDispatcher_Shutdown_DropsQueued(t *testing.T) {
	bin := hookprobePath(t)
	dir := t.TempDir()
	hold := ResolvedHook{Index: 0, Event: "*", Match: matchAlways(), Command: bin, Args: []string{"sleep", "500ms"}, Timeout: 2 * time.Second, WorkingDir: dir}
	cfg := defaultConfig()
	cfg.PoolSize = 1
	cfg.QueueCap = 4
	d, _, dbHash := mustNewDispatcher(t, []ResolvedHook{hold}, cfg)
	for i := 0; i < 5; i++ {
		d.Enqueue(db.Event{ID: int64(400 + i), Type: "issue.created", ProjectID: 1, ProjectIdentity: "x"})
	}
	time.Sleep(50 * time.Millisecond) // worker has popped one
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	runsPath := filepath.Join(d.deps.KataHome, "hooks", dbHash, "runs.jsonl")
	data, _ := os.ReadFile(runsPath) //nolint:gosec // G304: test-controlled path under t.TempDir()
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	// Only the in-flight job should have produced a line. (4xx all
	// completing would be > 1.) Allow <=2 to tolerate edge timing.
	if lines > 2 {
		t.Fatalf("expected <=2 runs after shutdown, got %d", lines)
	}
}

func TestDispatcher_Reload_AtomicWithEnqueue(t *testing.T) {
	bin := hookprobePath(t)
	dir := t.TempDir()
	first := ResolvedHook{Index: 0, Event: "*", Match: matchAlways(), Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: dir}
	cfg := defaultConfig()
	d, _, _ := mustNewDispatcher(t, []ResolvedHook{first}, cfg)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.Enqueue(db.Event{ID: int64(i), Type: "issue.created", ProjectID: 1, ProjectIdentity: "x"})
		}
	}()
	for i := 0; i < 50; i++ {
		newHook := ResolvedHook{Index: 0, Event: "issue.created", Match: matchExact("issue.created"), Command: bin, Args: []string{"exit", "0"}, Timeout: 2 * time.Second, WorkingDir: dir}
		d.Reload(LoadedConfig{Snapshot: Snapshot{Hooks: []ResolvedHook{newHook}}, Config: cfg})
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
}

func waitForLines(t *testing.T, path string, n int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled path under t.TempDir()
		if err == nil && strings.Count(strings.TrimSpace(string(data)), "\n")+1 >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func matchExact(want string) func(string) bool { return func(s string) bool { return s == want } }
func matchAlways() func(string) bool           { return func(string) bool { return true } }

// Sentinel keepalive (matches existing hooks-package test convention)
// so that future tests can reference errors without re-importing.
var _ = errors.New
