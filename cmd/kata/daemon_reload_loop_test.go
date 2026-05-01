package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/wesm/kata/internal/hooks"
)

// recordingDispatcher captures Reload calls so the SIGHUP loop test can
// observe what was dispatched without spawning a real *hooks.Dispatcher.
type recordingDispatcher struct {
	mu          sync.Mutex
	reloadCalls []hooks.LoadedConfig
}

func (r *recordingDispatcher) CurrentConfig() hooks.Config {
	return hooks.Config{
		PoolSize:             4,
		QueueCap:             1000,
		OutputDiskCap:        100 << 20,
		RunsLogMaxBytes:      50 << 20,
		RunsLogKeep:          5,
		QueueFullLogInterval: 60 * time.Second,
	}
}

func (r *recordingDispatcher) Reload(lc hooks.LoadedConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadCalls = append(r.reloadCalls, lc)
}

// nopLogger satisfies loopLogger without writing anything.
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

func TestRunReloadLoop_DispatchesOnSignal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KATA_HOME", dir)
	path := filepath.Join(dir, "hooks.toml")
	if err := os.WriteFile(path, []byte(`[[hook]]
event = "issue.created"
command = "/bin/true"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recordingDispatcher{}
	sigs := make(chan os.Signal, 1)
	logBuf := nopLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runReloadLoop(ctx, sigs, path, rec, logBuf)
		close(done)
	}()

	sigs <- syscall.SIGHUP
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		n := len(rec.reloadCalls)
		rec.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reloadCalls) != 1 {
		t.Fatalf("Reload calls = %d, want 1", len(rec.reloadCalls))
	}
	if len(rec.reloadCalls[0].Snapshot.Hooks) != 1 {
		t.Fatal("expected one hook in reloaded snapshot")
	}
}
