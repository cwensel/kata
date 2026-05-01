package hooks

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wesm/kata/internal/db"
)

// Sink is the minimal interface mutation handlers depend on. cfg.Hooks
// has type Sink so the daemon can swap in a Noop in tests / disabled
// builds without exposing the full Dispatcher surface.
type Sink interface {
	Enqueue(evt db.Event)
}

// DispatcherDeps wires the dispatcher to its environment. Resolvers
// are pluggable so tests can inject deterministic snapshots without
// exercising the DB.
type DispatcherDeps struct {
	DBHash          string
	KataHome        string
	DaemonLog       *log.Logger
	AliasResolver   func(ctx context.Context, evt db.Event) (AliasSnapshot, bool, error)
	IssueResolver   func(ctx context.Context, issueID int64) (IssueSnapshot, error)
	CommentResolver func(ctx context.Context, commentID int64) (CommentSnapshot, error)
	ProjectResolver func(ctx context.Context, projectID int64) (ProjectSnapshot, error)
	Now             func() time.Time
	GraceWindow     time.Duration
}

// Dispatcher is the live hook runtime. The exported API is
// Enqueue/Reload/CurrentConfig/Shutdown; unexported state is held
// by-value (no need for a separate state struct).
type Dispatcher struct {
	deps        DispatcherDeps
	cfg         Config
	queue       chan HookJob
	done        chan struct{}
	waited      chan struct{} // closed when wg.Wait returns; shared by all Shutdown callers
	stopped     atomic.Bool
	wg          sync.WaitGroup
	snapshot    atomic.Pointer[Snapshot]
	dropped     atomic.Int64
	lastFullLog atomic.Int64 // unix nanos of last hook_queue_full log
	appender    *runsAppender
	pruner      *pruner
	outputDir   string
	inflight    atomic.Int32
	// active maps groupKey -> struct{} for runs whose .out/.err are
	// still being written. The pruner consults this via isActive so a
	// finishing job's pruner.AddRun never unlinks a peer worker's
	// in-progress capture.
	active sync.Map
}

// noopSink is the unexported implementation of Sink returned by
// NewNoop. It records nothing and never panics.
type noopSink struct{}

func (noopSink) Enqueue(_ db.Event) {}

// NewNoop returns a Sink whose Enqueue is a no-op. Used by tests that
// don't want to wire a full dispatcher and by builds where hooks are
// disabled.
func NewNoop() Sink { return noopSink{} }

// New constructs a Dispatcher. It MkdirAll's the hook root + output
// directories under deps.KataHome, seeds the prune running total via
// WalkDir, opens the runs.jsonl file for append, and starts the worker
// pool.
func New(loaded LoadedConfig, deps DispatcherDeps) (*Dispatcher, error) {
	deps = applyDispatcherDefaults(deps)
	root := filepath.Join(deps.KataHome, "hooks", deps.DBHash)
	outputDir := filepath.Join(root, "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir hook output dir: %w", err)
	}
	runsPath := filepath.Join(root, "runs.jsonl")
	app, err := newRunsAppender(runsPath, loaded.Config.RunsLogMaxBytes, loaded.Config.RunsLogKeep)
	if err != nil {
		return nil, fmt.Errorf("open runs.jsonl: %w", err)
	}
	pr := newPruner(outputDir, loaded.Config.OutputDiskCap, deps.DaemonLog)
	if err := pr.Seed(); err != nil {
		_ = app.Close()
		return nil, fmt.Errorf("seed pruner: %w", err)
	}
	d := &Dispatcher{
		deps:      deps,
		cfg:       loaded.Config,
		queue:     make(chan HookJob, loaded.Config.QueueCap),
		done:      make(chan struct{}),
		waited:    make(chan struct{}),
		appender:  app,
		pruner:    pr,
		outputDir: outputDir,
	}
	pr.SetActiveCheck(d.isGroupActive)
	snap := loaded.Snapshot
	d.snapshot.Store(&snap)
	for i := 0; i < loaded.Config.PoolSize; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d, nil
}

// isGroupActive is the predicate the pruner consults to skip
// in-flight (event_id, hook_index) groups during a sweep.
func (d *Dispatcher) isGroupActive(k groupKey) bool {
	_, ok := d.active.Load(k)
	return ok
}

func applyDispatcherDefaults(deps DispatcherDeps) DispatcherDeps {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.GraceWindow == 0 {
		deps.GraceWindow = 5 * time.Second
	}
	if deps.DaemonLog == nil {
		deps.DaemonLog = log.New(os.Stderr, "", log.LstdFlags)
	}
	return deps
}

// Enqueue is the Sink-side fan-out point. Non-blocking by contract: a
// full queue increments dropped and rate-limit-logs.
func (d *Dispatcher) Enqueue(evt db.Event) {
	if d.stopped.Load() {
		return
	}
	snap := d.snapshot.Load()
	if snap == nil {
		return
	}
	for _, h := range snap.Hooks {
		if !h.Match(evt.Type) {
			continue
		}
		// Two-phase send: bail out if Shutdown started between hooks.
		select {
		case <-d.done:
			return
		default:
		}
		job := HookJob{Event: evt, Hook: h, EnqueuedAt: d.deps.Now()}
		select {
		case d.queue <- job:
		default:
			d.dropped.Add(1)
			d.maybeLogQueueFull()
		}
	}
}

func (d *Dispatcher) maybeLogQueueFull() {
	now := d.deps.Now().UnixNano()
	last := d.lastFullLog.Load()
	if now-last < int64(d.cfg.QueueFullLogInterval) {
		return
	}
	if d.lastFullLog.CompareAndSwap(last, now) {
		d.deps.DaemonLog.Printf("hooks: hook_queue_full (dropped=%d)", d.dropped.Load())
	}
}

// CurrentConfig returns the active startup-only Config. Used by
// LoadReload to compute the UnchangedTunables diff.
func (d *Dispatcher) CurrentConfig() Config { return d.cfg }

// Reload swaps the live Snapshot. Tunables are not applied (v1: startup
// only); UnchangedTunables messages are logged.
func (d *Dispatcher) Reload(loaded LoadedConfig) {
	for _, msg := range loaded.UnchangedTunables {
		d.deps.DaemonLog.Printf("hooks: %s", msg)
	}
	snap := loaded.Snapshot
	d.snapshot.Store(&snap)
	d.deps.DaemonLog.Printf("hooks reload ok: %d hook(s) active", len(loaded.Snapshot.Hooks))
}

// Shutdown closes the done channel, waits for workers up to ctx, and
// closes the runs appender. The first call closes d.done and starts
// the wg.Wait goroutine; later calls share the same `waited` channel
// so a retry after the first call timed out blocks until completion
// (or its own ctx expires) — never spuriously returns nil while
// workers are still running.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	if d.stopped.CompareAndSwap(false, true) {
		close(d.done)
		go func() {
			d.wg.Wait()
			close(d.waited)
		}()
	}
	select {
	case <-d.waited:
		// Close appender once; appender.Close is itself idempotent.
		_ = d.appender.Close()
		return nil
	case <-ctx.Done():
		d.deps.DaemonLog.Printf("hooks shutdown timed out: %d in-flight", d.inflight.Load())
		// Drain best-effort: don't close appender — workers may still
		// be writing. Caller proceeds with daemon shutdown anyway.
		return fmt.Errorf("hooks shutdown timed out: %w", ctx.Err())
	}
}

// worker pops one HookJob at a time and runs it. The two-phase select
// pattern (re-check done after pop) ensures that a job sitting in the
// queue at Shutdown time is not started — Go's select chooses cases
// uniformly among ready ones, so the post-pop check drops at most one
// just-popped job per worker.
func (d *Dispatcher) worker() {
	defer d.wg.Done()
	rd := d.runDeps()
	for {
		select {
		case <-d.done:
			return
		case job, ok := <-d.queue:
			if !ok {
				return
			}
			select {
			case <-d.done:
				return // drop the just-popped job; do not start runJob
			default:
			}
			d.runOne(rd, job)
		}
	}
}

// runOne executes one job under inflight + active-group accounting
// that survives a runJob panic. The deferred recover keeps the worker
// alive and the counters accurate so Shutdown's "in-flight" log
// doesn't drift, and so the pruner's isActive predicate never strands
// a stale "still writing" entry.
func (d *Dispatcher) runOne(rd runDeps, job HookJob) {
	key := groupKey{eventID: job.Event.ID, hookIndex: job.Hook.Index}
	d.active.Store(key, struct{}{})
	d.inflight.Add(1)
	defer func() {
		d.inflight.Add(-1)
		d.active.Delete(key)
	}()
	defer func() {
		if r := recover(); r != nil {
			d.deps.DaemonLog.Printf("hooks: runJob panic: %v", r)
		}
	}()
	ctx, cancel := contextFromShutdown(d.done)
	defer cancel()
	runJob(ctx, d.done, job, rd)
}

// contextFromShutdown returns a context that is canceled the moment
// d.done is closed. Resolver calls inside runJob (project, issue,
// comment, alias) honor this context, so a stuck DB query during
// stdin payload assembly does not delay Shutdown beyond ctx.
func contextFromShutdown(done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// runDeps builds the per-job runDeps from the dispatcher's state.
// Pulled out of worker so the worker body stays under CC 8.
func (d *Dispatcher) runDeps() runDeps {
	return runDeps{
		OutputDir:   d.outputDir,
		DaemonLog:   d.deps.DaemonLog,
		Now:         d.deps.Now,
		GraceWindow: d.deps.GraceWindow,
		Project:     d.deps.ProjectResolver,
		Issue:       d.deps.IssueResolver,
		Comment:     d.deps.CommentResolver,
		Alias:       d.deps.AliasResolver,
		AppendRun: func(r runRecord) {
			// Drop the active marker BEFORE asking the pruner whether to
			// sweep so MaybeSweep can prune the just-finished group too.
			// The deferred cleanup in runOne acts as a safety net for panic
			// paths that bypass AppendRun.
			d.active.Delete(groupKey{eventID: r.EventID, hookIndex: r.HookIndex})
			d.appender.Append(r)
			d.pruner.AddRun(r.EventID, r.HookIndex, r.StdoutBytes, r.StderrBytes)
		},
	}
}
