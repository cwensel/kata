# Plan 5 — Hooks design

**Date:** 2026-04-30
**Companion to master spec §8** (`docs/superpowers/specs/2026-04-29-kata-design.md`).
**Status:** approved for implementation.

## 1. Goal

Local automation on persisted `issue.*` events. The daemon dispatches each committed event to a fixed set of operator-configured commands declared in `$KATA_HOME/hooks.toml`. Hooks observe events; they never block, retry, or roll back state changes.

## 2. Delivery contract

- **At-most-once.** Hooks see only events handed to the in-process hook queue after commit.
- **No replay.** Missed events from daemon crash, restart, reload, or queue overflow are not replayed.
- **Post-commit, best-effort.** Hook dispatch never blocks the mutation request, the SSE broadcast, or any other event consumer.
- **Independent of SSE.** Hooks do not subscribe to the Plan 4 `EventBroadcaster`; they receive a sibling `Hooks.Enqueue(evt)` call from each mutation handler.
- **Synthetic events excluded.** `sync.reset_required` is never enqueued and is rejected at config-load time as a hook `event` value.

The mutation handler call site:
```go
cfg.Broadcaster.Broadcast(daemon.StreamMsg{Kind: "event", Event: &evt, ProjectID: evt.ProjectID})
cfg.Hooks.Enqueue(evt)
```

`Enqueue` is non-blocking by contract: a full queue increments a drop counter and rate-limit-logs `hook_queue_full`. Mutation handlers do not see hook errors.

## 3. Architecture

```
mutation handler                      mutation handler
   │                                       │
   │ post-commit:                          │ post-commit:
   │   cfg.Broadcaster.Broadcast(evt)      │   cfg.Broadcaster.Broadcast(evt)
   │   cfg.Hooks.Enqueue(evt)              │   cfg.Hooks.Enqueue(evt)
   ▼                                       ▼
┌─────────────────────────────────────────────────┐
│  cfg.Hooks  (hooks.Dispatcher)                  │
│   atomic.Pointer[Snapshot]    (live-reloadable) │
│   Config                       (startup-only)   │
│   queue chan HookJob           (cap=QueueCap)   │
│   pool of N goroutines         (PoolSize)       │
│   single appender mutex for runs.jsonl          │
└─────────────────────────────────────────────────┘
        │
        ▼
   exec hook → write .out / .err → append runs.jsonl → prune output if over cap
```

### 3.1 New packages and files

| Path | Responsibility |
|---|---|
| `internal/hooks/config.go` | TOML schema, parsing, validation; types `Snapshot`, `Config`, `LoadedConfig`, `ResolvedHook`; `LoadStartup`, `LoadReload` |
| `internal/hooks/dispatcher.go` | `Sink` interface; `Dispatcher` type with `Enqueue`, `Reload`, `Shutdown`, `CurrentConfig`; `NewNoop()` returning a `Sink`; queue, `done` channel, `stopped` flag, snapshot pointer, queue-full counter |
| `internal/hooks/runner.go` | Worker exec path: process spawn, stdin write, output capture, timeout SIGTERM/grace/SIGKILL, runs.jsonl record |
| `internal/hooks/payload.go` | Stdin JSON envelope construction + truncation; calls `ProjectResolver`/`IssueResolver`/`CommentResolver`/`AliasResolver` and merges results into the envelope |
| `internal/hooks/prune.go` | Output-dir disk-cap rescan + run-group delete; `runs.jsonl` rotation |
| `internal/hooks/proc_unix.go` | `Setpgid: true` on `cmd.SysProcAttr` (build tag `!windows`) |
| `internal/hooks/proc_windows.go` | No-op stub (build tag `windows`) |
| `internal/hooks/hookprobe/main.go` | Test helper binary (only built/run by tests; placement under `internal/` keeps it out of user-facing builds) |
| `internal/config/paths.go` (additions) | `HookConfigPath()`, `HookRootDir(dbhash)`, `HookOutputDir(dbhash)`, `HookRunsPath(dbhash)` |

### 3.2 Modified files

| Path | Change |
|---|---|
| `internal/daemon/server.go` | Load hooks at startup (fatal on malformed); `MkdirAll` the hook root + output dir; construct `*Dispatcher`; run a SIGHUP loop that calls `disp.Reload(...)`; `defer disp.Shutdown(ctx)`; assign `cfg.Hooks = disp` (typed as `hooks.Sink`) |
| `internal/daemon/handlers_*.go` (every mutation handler that already broadcasts) | Add `cfg.Hooks.Enqueue(evt)` immediately after the existing `Broadcaster.Broadcast(...)` call |
| `cmd/kata/daemon_cmd.go` | Register `daemon reload` and `daemon logs --hooks` subcommands |

## 4. Config

### 4.1 File

Single global location: `$KATA_HOME/hooks.toml`. Workspace-local hook config is out of scope for v1 (master spec §10 backlog).

### 4.2 Schema

```toml
[hooks]
pool_size               = 4         # int, [1, 16], default 4
queue_cap               = 1000      # int, [1, 10000], default 1000
output_disk_cap         = "100MB"   # size string or int bytes; default 100MB
runs_log_max            = "50MB"    # rotation threshold per file; default 50MB
runs_log_keep           = 5         # rotated files retained; default 5
queue_full_log_interval = "60s"     # rate-limit hook_queue_full log; default 60s

[[hook]]
event       = "issue.created"           # required; see §4.3
command     = "/usr/local/bin/notify"   # required; absolute path or bare name (no '/')
args        = ["--title", "kata"]       # optional; default []; literal strings
timeout     = "30s"                     # optional; (0, 5m]; default 30s
working_dir = "/var/log/kata"           # optional; absolute after filepath.Clean; default $KATA_HOME

[hook.env]
EXTRA = "value"                         # optional; keys matching ^KATA_ rejected at load
```

**Size units (binary):** `k`/`kb` = 1024, `m`/`mb` = 1024². Case-insensitive. Bare integer = bytes.

### 4.3 Event matching

`event` is one of:

- An exact value from the persisted-events set (master spec §3.3): `issue.created`, `issue.updated`, `issue.closed`, `issue.reopened`, `issue.commented`, `issue.linked`, `issue.unlinked`, `issue.labeled`, `issue.unlabeled`, `issue.assigned`, `issue.unassigned`, `issue.soft_deleted`, `issue.restored`.
- `issue.*` — matches all 13 above.
- `*` — matches all persisted events (currently identical to `issue.*`; reserves room for future event families).
- `sync.reset_required` is rejected at load: hooks never receive synthetic control events.
- Any other value (e.g., `*.created`, `bogus.*`, empty string) is a load-time error.

### 4.4 Validation rules (load-time)

1. TOML parses with strict-keys: unknown keys cite line and key in the error.
2. `event` matches §4.3.
3. `command` is non-empty and either an absolute path (`filepath.IsAbs`) OR a bare name with no path separators (rejects both `/` and the platform `filepath.Separator` so Windows `bin\foo` and `.\foo` are also rejected). Relative paths like `./foo` or `bin/foo` are rejected. Internal whitespace is allowed inside absolute paths (`/Applications/Some App/bin/x`) but not in bare names — `exec.Command` PATH-looks up bare names literally.
4. `args` is `[]string`. `$VAR`-style entries are accepted as **literal strings**; the executor never expands them (`exec.Command` does no shell processing).
5. `timeout` parses as Go duration, `(0, 5m]`.
6. `working_dir` (if set) is absolute after `filepath.Clean`. Existence is **not** checked at load.
7. `[hook.env]` keys do not match `^KATA_`.
8. `[hooks].pool_size ∈ [1, 16]`, `queue_cap ∈ [1, 10000]`, sizes/intervals parse, integers ≥ 1.
9. Total `[[hook]]` entries ≤ 256.

### 4.5 Resolved snapshot

```go
type ResolvedHook struct {
    Event       string                 // canonical literal: "issue.created", "issue.*", or "*"
    Match       func(string) bool      // precompiled; tested against evt.Type
    Command     string
    Args        []string
    Timeout     time.Duration
    WorkingDir  string                 // resolved at load to absolute path; default = config.KataHome()
    UserEnv     []string               // ["KEY=VAL", ...]; sorted by key for determinism
    Index       int                    // 0-based ordinal in source file
}

type Snapshot struct {
    Hooks []ResolvedHook
}

type Config struct {
    PoolSize             int
    QueueCap             int
    OutputDiskCap        int64
    RunsLogMaxBytes      int64
    RunsLogKeep          int
    QueueFullLogInterval time.Duration
}

type LoadedConfig struct {
    Snapshot           Snapshot
    Config             Config
    UnchangedTunables  []string  // populated only on reload (see §4.6)
}
```

### 4.6 Lifecycle

| Trigger | hooks.toml state | Behavior |
|---|---|---|
| Daemon startup | missing | `LoadStartup` returns empty `Snapshot` and default `Config`. |
| Daemon startup | malformed | `LoadStartup` returns error; daemon exits non-zero. Operator misconfigured. |
| Daemon startup | valid | Load + construct dispatcher. |
| `SIGHUP` / `kata daemon reload` | missing | `LoadReload` returns empty `Snapshot`; dispatcher swaps to it (effectively disables future hooks). |
| `SIGHUP` / `kata daemon reload` | malformed | `LoadReload` returns error; dispatcher keeps current `Snapshot`; daemon log records the parse error. |
| `SIGHUP` / `kata daemon reload` | valid | `LoadReload` returns new `Snapshot`. Tunable diffs vs current `Config` are reported in `LoadedConfig.UnchangedTunables` (e.g., `pool_size: requested 8, active 4 (restart required)`). Dispatcher swaps `Snapshot` only. |
| Daemon shutdown (`ctx.Done()`) | — | Dispatcher stops accepting new `Enqueue`s; queued jobs dropped; in-flight finishes up to `Shutdown(ctx)` deadline. |

**Live-reloadable scope:** only `[[hook]]` entries. All `[hooks]` tunables are startup-only in v1; live resize is YAGNI.

## 5. Dispatcher

### 5.1 Public API

```go
// Sink is the minimal interface mutation handlers depend on. cfg.Hooks
// has type Sink so the daemon can swap in a Noop in tests / disabled
// builds without exposing the full Dispatcher surface.
type Sink interface {
    Enqueue(evt db.Event)
}

// Dispatcher is the live hook runtime. Daemon wires the *Dispatcher
// directly and stores it as Sink on ServerConfig; reload/shutdown are
// daemon-side concerns and not part of the Sink contract.
type Dispatcher struct { /* unexported */ }

type IssueSnapshot struct {
    Number int64
    Title  string
    Status string
    Labels []string  // sorted for determinism
    Owner  string
    Author string
}

type CommentSnapshot struct {
    ID   int64
    Body string
}

type ProjectSnapshot struct {
    Name string
}

type DispatcherDeps struct {
    DBHash           string
    KataHome         string
    DaemonLog        *log.Logger
    AliasResolver    func(evt db.Event) (AliasSnapshot, bool, error)
    IssueResolver    func(ctx context.Context, issueID int64) (IssueSnapshot, error)
    CommentResolver  func(ctx context.Context, commentID int64) (CommentSnapshot, error)
    ProjectResolver  func(ctx context.Context, projectID int64) (ProjectSnapshot, error)
    Now              func() time.Time   // tests inject
    GraceWindow      time.Duration      // SIGTERM→SIGKILL; default 5s; tests shorten
}

// New constructs a Dispatcher. It MkdirAll's the hook root + output
// directories under deps.KataHome / deps.DBHash (mode 0o700), seeds the
// prune running total via filepath.WalkDir, opens the runs.jsonl file
// for append, and starts the worker pool. Returns an error if any of
// these prerequisites fail.
func New(loaded LoadedConfig, deps DispatcherDeps) (*Dispatcher, error)
func NewNoop() Sink
func (d *Dispatcher) Enqueue(evt db.Event)
func (d *Dispatcher) CurrentConfig() Config              // for LoadReload diff
func (d *Dispatcher) Reload(loaded LoadedConfig)         // logs UnchangedTunables; swaps Snapshot
func (d *Dispatcher) Shutdown(ctx context.Context) error // see §5.4
```

`*Dispatcher` and the unexported noop type both satisfy `Sink`. `cfg.Hooks Sink` is always non-nil. Daemon code that needs `Reload`/`Shutdown` keeps a private `*Dispatcher` reference; mutation handlers only see `Sink`.

`AliasResolver`/`IssueResolver`/`CommentResolver` enrich the stdin payload (§7); they are pluggable so tests can inject deterministic snapshots without exercising the DB. The dispatcher passes `daemonCtx` to them so a daemon shutdown cancels in-progress lookups.

### 5.2 Enqueue

```
if d.stopped.Load(): return                         // no-op after Shutdown
snap := d.snapshot.Load()
matches := []ResolvedHook{} for h in snap.Hooks where h.Match(evt.Type)
for h in matches in source order:
    // Two-phase send: bail out if Shutdown started between iterations.
    select {
    case <-d.done: return
    default:
    }
    job := HookJob{Event: evt, Hook: h, EnqueuedAt: d.now()}
    select {
    case d.queue <- job: /* ok */
    default:
        atomic.AddInt64(&d.dropped, 1)
        d.maybeLogQueueFull()  // rate-limited per QueueFullLogInterval
    }
```

Drop-newest is per-job, not per-event. If an event matches N hooks but only M ≤ N slots are free, the first M (in source order) enqueue and the remaining drop. Partial delivery is acceptable because hooks are independent observers.

### 5.3 Reload

`Dispatcher.Reload(loaded)` does:
1. For each `s` in `loaded.UnchangedTunables`: `daemonLog.Printf("hooks: %s", s)`.
2. `d.snapshot.Store(&loaded.Snapshot)` — atomic pointer swap.
3. `daemonLog.Printf("hooks reload ok: %d hook(s) active", len(loaded.Snapshot.Hooks))`.

Queued and in-flight `HookJob`s are unaffected: each carries its own `ResolvedHook` so workers never consult the snapshot.

The signal-to-load loop lives **on the daemon side**, not in the dispatcher (separation: daemon owns signal wiring, dispatcher owns swap):

```go
// internal/daemon/server.go (sketch)
go func() {
    sigs := make(chan os.Signal, 1)
    signal.Notify(sigs, syscall.SIGHUP)
    defer signal.Stop(sigs)
    for {
        select {
        case <-ctx.Done(): return
        case <-sigs:
            loaded, err := hooks.LoadReload(config.HookConfigPath(), disp.CurrentConfig())
            if err != nil {
                daemonLog.Printf("hooks reload failed: %v (keeping previous config)", err)
                continue
            }
            disp.Reload(loaded)
        }
    }
}()
```

Tests bypass signals entirely and call `disp.Reload(loaded)` directly. `Dispatcher.CurrentConfig() Config` returns the active startup-only `Config` so `LoadReload` can compute the `UnchangedTunables` diff.

### 5.4 Shutdown

```go
type Dispatcher struct {
    queue    chan HookJob
    done     chan struct{}      // closed by Shutdown (signals "drop queued, exit")
    stopped  atomic.Bool        // gates Enqueue + first-call Shutdown
    waited   chan struct{}      // closed when wg.Wait returns; shared by all Shutdown callers
    wg       sync.WaitGroup     // tracks workers
    snapshot atomic.Pointer[Snapshot]
    cfg      Config             // startup-only, captured at New
    // ... other unexported fields
}

func (d *Dispatcher) worker() {
    defer d.wg.Done()
    for {
        // Go's select has no case priority — when both d.done and
        // d.queue are ready, selection is uniformly random. Two
        // stacked selects don't help because the second select faces
        // the same race. Correctness comes from the post-pop re-check
        // below: a worker that wins the queue race after Shutdown
        // observes d.done on the second select and drops the job
        // before runJob runs. At most one job per worker leaks past
        // Shutdown, and it never starts execution.
        select {
        case <-d.done:
            return
        case job, ok := <-d.queue:
            if !ok {
                return
            }
            // Re-check after pop: if Shutdown raced in between the
            // case selection and the runJob call, drop the job rather
            // than start it.
            select {
            case <-d.done:
                return
            default:
            }
            d.runJob(job) // long-running; uses d.done as exec ctx (§6.1)
        }
    }
}

func (d *Dispatcher) Shutdown(ctx context.Context) error {
    // First caller wins the close(d.done); subsequent callers fall
    // through to the wait below so they observe the same completion
    // signal (`waited`) under their own context. A second call after
    // the first returned timeout still gets to wait.
    if d.stopped.CompareAndSwap(false, true) {
        close(d.done)
        go func() { d.wg.Wait(); close(d.waited) }()
    }
    select {
    case <-d.waited:
        return nil
    case <-ctx.Done():
        d.daemonLog.Printf("hooks shutdown timed out: %d in-flight", d.inflightCount())
        return fmt.Errorf("hooks shutdown timed out: %w", ctx.Err())
    }
}
```

**Contract:**
- After `Shutdown` is called, `Enqueue` is a no-op (returns immediately, no panic, no send).
- Workers idle at the moment `done` closes return without popping further jobs. Workers running a job complete it (subject to `runJob`'s own response to `daemonCtx.Done()`/`done`, which resolves into a `daemon_shutdown` result per §6.4).
- A worker that already won a `case job := <-d.queue` race re-checks `d.done` immediately and drops the job before invoking `runJob`. At most one job per worker can leak past `Shutdown`, and it never starts execution.
- Queued (un-popped) jobs are not run.
- `Shutdown` returns `nil` if all workers exit before its caller's `ctx` deadline; otherwise returns an error and the caller proceeds with daemon shutdown anyway. Every later `Shutdown` call observes the same `waited` channel under its own context, so a retry after the first timed out blocks until completion or the new context expires — it does not spuriously return `nil`.
- The queue channel is **not** closed; `Enqueue` gates on `stopped` rather than relying on closed-channel send (which would panic on race).

## 6. Runner (per-job execution)

### 6.1 Sequence

```go
// Precondition: at New(), the dispatcher MkdirAll's
//   <KataHome>/hooks/<dbhash>/output/   (mode 0o700)
// and seeds the prune running total. Workers assume the dir exists.

job := <-queue
out := <KataHome>/hooks/<dbhash>/output/<event_id>.<hook_index>.out
err := <KataHome>/hooks/<dbhash>/output/<event_id>.<hook_index>.err

// 1. Open output files first so every recordRun references valid paths,
//    even for early-exit failure modes.
outFile, oErr := os.OpenFile(out, O_CREATE|O_WRONLY|O_TRUNC, 0o600)
errFile, eErr := os.OpenFile(err, O_CREATE|O_WRONLY|O_TRUNC, 0o600)
if oErr != nil || eErr != nil {
    // Output dir was deleted, permissions changed, or fs is full. We
    // cannot reference paths we couldn't create — recordRun with empty
    // path/size fields and exit. No defer-close needed here since
    // partial-success files are explicitly closed and removed on this
    // branch so we don't leave an untracked artifact behind.
    msg := fmt.Sprintf("open output files: out=%v err=%v", oErr, eErr)
    if outFile != nil {
        _ = outFile.Close()
        _ = os.Remove(out) // remove the half-created file
    }
    if errFile != nil {
        _ = errFile.Close()
        _ = os.Remove(err)
    }
    recordRun(
        result="spawn_failed", spawn_error=msg,
        stdout_path="", stderr_path="",
        stdout_bytes=0, stderr_bytes=0,
    )
    return
}
// From this point both files exist as 0-byte handles. Install
// best-effort cleanup defers so a panic between here and recordRun
// still releases the file descriptors. Closing an already-closed
// *os.File returns ErrClosed, which we ignore.
defer func() { _ = outFile.Close() }()
defer func() { _ = errFile.Close() }()

// recordRunWithFiles is the authoritative finalization path:
//   1. close outFile/errFile (idempotent — the deferred closes above
//      will also fire and tolerate the already-closed state),
//   2. stat the on-disk paths to populate stdout_bytes / stderr_bytes
//      (so we measure flushed bytes, not in-memory buffered ones),
//   3. append one line to runs.jsonl (under the appender mutex).
// Every reachable exit below calls this exactly once, so paths and
// byte counts are recorded uniformly and the deferred closes become
// no-op safety nets.

// 2. Pre-spawn working_dir check.
if st, e := os.Stat(job.Hook.WorkingDir); e != nil {
    if errors.Is(e, fs.ErrNotExist) {
        recordRunWithFiles(result="working_dir_missing", spawn_error=e.Error())
        return
    }
    recordRunWithFiles(result="spawn_failed", spawn_error=e.Error())
    return
} else if !st.IsDir() {
    recordRunWithFiles(result="spawn_failed", spawn_error="working_dir is not a directory")
    return
}

// 3. Resolve enrichment data at fire time. Failures are tolerated: each
//    block is omitted with a rate-limited log line. The hook still runs.
proj, projErr := deps.ProjectResolver(daemonCtx, evt.ProjectID)
issue, issueErr := deps.IssueResolver(daemonCtx, deref(evt.IssueID))
alias, hasAlias, aliasErr := deps.AliasResolver(evt)
var commentBody string
if evt.Type == "issue.commented" {
    if cid, ok := parseCommentID(evt.Payload); ok {
        if c, cErr := deps.CommentResolver(daemonCtx, cid); cErr == nil {
            commentBody = c.Body
        }
    }
}
stdinPayload, payloadTruncated := buildStdinJSON(
    evt, proj, projErr, issue, issueErr,
    alias, hasAlias, aliasErr, commentBody)

// 4. Build command — exec.Command, NOT exec.CommandContext.
cmd := exec.Command(job.Hook.Command, job.Hook.Args...)
cmd.Dir = job.Hook.WorkingDir
cmd.Env = buildEnv(job.Hook.UserEnv, evt, alias, hasAlias)  // §6.3
cmd.Stdin = bytes.NewReader(stdinPayload)                   // §7
cmd.Stdout = outFile
cmd.Stderr = errFile
applyProcessGroupAttrs(cmd)                                 // unix Setpgid; windows no-op

// 5. Spawn.
if e := cmd.Start(); e != nil {
    recordRunWithFiles(result="spawn_failed", spawn_error=e.Error())
    return
}

// 6. Wait with two contexts: hook timeout + daemon shutdown (d.done).
done := make(chan error, 1)
go func() { done <- cmd.Wait() }()

timer := time.NewTimer(job.Hook.Timeout)
defer timer.Stop()
var result string
select {
case e := <-done:
    result = "ok"
    exit_code = exitCodeOf(e)
case <-timer.C:
    result = "timed_out"
    killTreeWithGrace(cmd, deps.GraceWindow)
    <-done
case <-d.done:    // dispatcher Shutdown signal (§5.4)
    result = "daemon_shutdown"
    killTreeWithGrace(cmd, deps.GraceWindow)
    <-done
}

// 7. Record run. recordRunWithFiles closes the deferred files,
//    stats the paths, and appends to runs.jsonl.
recordRunWithFiles(result, exit_code, payload_truncated)

// 8. Best-effort prune (§9).
prune.MaybeSweep(deps.OutputDir, deps.OutputDiskCap)
```

**Recording uniformity**: every exit path that reaches the deferred files calls `recordRunWithFiles` so `runs.jsonl` always carries `stdout_path` / `stderr_path` (the resolved on-disk filenames) and `stdout_bytes` / `stderr_bytes` (the final sizes — zero on early-exit before any process ran). The single pre-defer error path (output-file open failure) is the only case that records empty paths and zero bytes; that's intentional because we have no valid file to point at.

### 6.2 `killTreeWithGrace`

Send `SIGTERM` to the process group (Unix) or just the process (Windows). Wait up to `GraceWindow` (default 5 s). If still running, send `SIGKILL` and wait. Any `os.FindProcess`/`Signal`/`Wait` errors logged via `daemonLog`, never surfaced to mutation handlers.

### 6.3 Environment construction

Final env is `os.Environ()` baseline ⊕ `UserEnv` ⊕ `KATA_*`, last writer wins. Since `^KATA_` keys are rejected from `[hook.env]` at load, `KATA_*` always wins in practice.

`KATA_*` keys (master spec §8.5):
```
KATA_HOOK_VERSION       always "1"
KATA_EVENT_ID           strconv.FormatInt(evt.ID, 10)
KATA_EVENT_TYPE         evt.Type
KATA_ACTOR              evt.Actor
KATA_CREATED_AT         evt.CreatedAt.UTC().Format(time.RFC3339Nano)
KATA_PROJECT_ID         strconv.FormatInt(evt.ProjectID, 10)
KATA_PROJECT_IDENTITY   evt.ProjectIdentity
KATA_ISSUE_NUMBER       only if evt.IssueNumber != nil
KATA_ALIAS_IDENTITY     only if alias resolved
KATA_ROOT_PATH          only if alias resolved
```

### 6.4 Result categories

| `result` | Meaning |
|---|---|
| `ok` | Process spawned, stdin written, exited cleanly. `exit_code` is the process's status (any int). |
| `spawn_failed` | `cmd.Start` failed, or `working_dir` exists but is not a directory / inaccessible. `exit_code = -1`. |
| `working_dir_missing` | Pre-spawn `os.Stat` returned `ENOENT`. `exit_code = -1`. |
| `timed_out` | Hook timeout expired; killed via SIGTERM/grace/SIGKILL. `exit_code` reflects the signal disposition. |
| `daemon_shutdown` | Daemon ctx done before completion; killed similarly. `exit_code` reflects signal disposition. |

`--failed-only` filter (CLI): `result != "ok" || exit_code != 0`.

### 6.5 Process group on Unix vs Windows

- Unix (`internal/hooks/proc_unix.go`, `//go:build !windows`): `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`. SIGTERM/SIGKILL targets the process group via `syscall.Kill(-pid, sig)`, so hooks that fork children are torn down cleanly.
- Windows (`internal/hooks/proc_windows.go`, `//go:build windows`): no-op; signal sent only to the process. Job-object support deferred.

## 7. Stdin payload

### 7.1 Envelope shape

Per master spec §8.4, with the wire field renamed for clarity (the "stdin" is generated by us; what gets truncated is the payload, not IO):

```json
{
  "kata_hook_version": 1,
  "event_id": 81237,
  "type": "issue.commented",
  "actor": "claude-4.7-wesm-laptop",
  "created_at": "2026-04-29T14:22:11.482Z",
  "project": { "id": 3, "identity": "github.com/wesm/kata", "name": "kata" },
  "alias": {
    "alias_identity": "github.com/wesm/kata",
    "alias_kind": "git",
    "root_path": "/Users/wesm/code/kata"
  },
  "issue": {
    "number": 42,
    "title": "fix login crash on Safari",
    "status": "open",
    "labels": ["bug", "safari"],
    "owner": "claude-4.7-wesm-laptop",
    "author": "claude-4.7-wesm-laptop",
    "_truncated": false
  },
  "payload": { "comment_id": 104, "comment_body": "..." }
}
```

### 7.2 Truncation

Hard cap on total stdin: **256 KB**. Per-field caps:

| Field | Cap | On exceed |
|---|---|---|
| `issue.title` | 1 KB | Truncate; sibling `issue._truncated:true`, `issue._full_size:N`. |
| `payload.comment_body` | 8 KB | Sibling `payload._truncated:true`, `payload._full_size:N`. |

`issue.body` is not part of the v1 `IssueSnapshot` and is reserved for a future extension; do not list a per-field cap or fallback entry until the snapshot exposes it. Truncation of titles cuts on a UTF-8 rune boundary so the result is always valid UTF-8.

After per-field caps, if total bytes still > 256 KB:
1. Set top-level `payload_truncated: true`.
2. Drop optional fields in priority order until under cap: `payload` → `issue.title`.
3. Field-level `_truncated` flags persist across the top-level fallback.

### 7.3 Sources of payload data

`db.Event` rows carry only event-level fields (id, type, actor, project_id, project_identity, issue_id, issue_number, related_issue_id, payload JSON, created_at). The richer `issue` block (title, status, labels, owner, author) and the `payload.comment_body` for `issue.commented` come from sibling tables and are loaded via dispatcher resolvers:

| Payload section | Source | Resolver |
|---|---|---|
| `event_id`, `type`, `actor`, `created_at`, `project.id`, `project.identity`, `issue.number` | `evt` directly | — |
| `project.name` | DB `projects.name` | `ProjectResolver(ctx, evt.ProjectID)` |
| `issue.title`/`status`/`labels`/`owner`/`author` | DB `issues` + `issue_labels` | `IssueResolver(ctx, evt.IssueID)` |
| `payload.comment_id` | `evt.Payload` JSON (already there for `issue.commented`) | — |
| `payload.comment_body` | DB `comments.body` (when event type is `issue.commented`) | `CommentResolver(ctx, commentID)` |
| `alias` | resolved from event context | `AliasResolver(evt)` |

Resolver behavior:

- **`IssueResolver`**: returns `(IssueSnapshot, error)`. On error, the entire `issue` block is omitted from the payload and a rate-limited line is logged. On success the snapshot fills the block. Snapshots are read at hook-fire time (not enqueue time) so a recently-edited issue reflects current state.
- **`CommentResolver`**: only invoked when `evt.Type == "issue.commented"`. On error, `payload.comment_body` is omitted (the `comment_id` from the event payload is preserved). On success the body fills the field, subject to per-field truncation.
- **`ProjectResolver`**: returns `(ProjectSnapshot, error)`. On error, `project.name` is omitted (`project.id` and `project.identity` come from `evt` and are always present). Logged rate-limited.
- **`AliasResolver`**: `func(evt db.Event) (AliasSnapshot, bool, error)`:
  - `(snap, true, nil)` → embed `alias` block.
  - `(_, false, nil)` → omit `alias` (events emitted from contexts without a single workspace; rare).
  - `(_, _, err)` → omit `alias`, log via `daemonLog`. Run the hook anyway.

```go
type AliasSnapshot struct {
    Identity string  // marshalled as "alias_identity"
    Kind     string  // "alias_kind"
    RootPath string  // "root_path"
}

type IssueSnapshot struct {
    Number int64
    Title  string
    Status string
    Labels []string  // sorted
    Owner  string
    Author string
}

type CommentSnapshot struct {
    ID   int64
    Body string
}
```

All four resolvers receive `daemonCtx` so a daemon shutdown cancels DB calls in flight; canceled lookups are treated as "missing" with a rate-limited log line.

## 8. `runs.jsonl`

### 8.1 Record shape

```json
{
  "kata_hook_runs_version": 1,
  "event_id": 81237,
  "event_type": "issue.commented",
  "hook_index": 2,
  "hook_command": "/usr/local/bin/notify",
  "started_at": "2026-04-30T14:22:11.482Z",
  "ended_at":   "2026-04-30T14:22:11.633Z",
  "duration_ms": 151,
  "exit_code": 0,
  "result": "ok",
  "stdout_path": "/Users/wesm/.kata/hooks/<dbhash>/output/81237.2.out",
  "stderr_path": "/Users/wesm/.kata/hooks/<dbhash>/output/81237.2.err",
  "stdout_bytes": 47,
  "stderr_bytes": 0,
  "spawn_error": "",
  "payload_truncated": false
}
```

### 8.2 Append + rotation

The dispatcher owns one `*os.File` for the active `runs.jsonl`, guarded by a single `sync.Mutex`. After each append:
- If size ≥ `RunsLogMaxBytes` (default 50 MB), rotate under the same mutex: rename current → `runs.jsonl.1`, shift `.1 → .2 …`, drop the file beyond `RunsLogKeep` (default 5), open a fresh `runs.jsonl`.

A brief stall during rotation is acceptable for v1 — the pool is at most 16, lines are short, rotation is `os.Rename` + `os.OpenFile`.

## 9. Output-dir disk cap

Files: `<KataHome>/hooks/<dbhash>/output/<event_id>.<hook_index>.{out,err}`.

Algorithm (`prune.MaybeSweep`):
1. Maintain in-memory running total of bytes in the dir, seeded once at startup via `filepath.WalkDir`.
2. After each finished run, add the just-written `.out` + `.err` sizes via `os.Stat`.
3. If total > `OutputDiskCap`, rescan the directory:
   - List entries; parse `<event_id>.<hook_index>` from filenames.
   - Group by `(event_id, hook_index)`; sort groups oldest-first by `(event_id, hook_index)` ascending.
   - Delete `.out` + `.err` as a unit, subtracting both sizes from the running total, until total ≤ cap.
4. `os.Stat` / `os.Remove` errors are rate-limit-logged via `daemonLog` and never fail the run.

**Concurrency:** all access to the running total and any in-progress sweep is guarded by a single `sync.Mutex` on the pruner. Up to 16 workers may finish concurrently and call into `MaybeSweep`; the mutex serializes total updates and ensures only one sweep runs at a time. The mutex is released before file I/O when possible so a slow `os.Remove` doesn't stall every other worker, but `AddRun` (size accumulation) and the sweep critical sections are non-overlapping. Tests inject ≥2 concurrent finishers under `-race` to pin the contract.

Run-group atomic delete prevents `runs.jsonl` references from degrading into "half-present" output.

## 10. CLI

### 10.1 `kata daemon reload`

```
kata daemon reload
```

Resolves running daemon via `Namespace`/runtime files (same path as `daemon stop`). Sends `SIGHUP` to the daemon's PID. Prints `reload signal sent to pid=N (check daemon log for result)` and exits 0.

Exit codes:
- `0` — signal sent.
- `ExitUsage` — no running daemon found.
- `ExitInternal` — `os.Process.Signal` returned an error.

The CLI **does not** poll for the result; validation success/failure surfaces in the daemon's `daemon.log`.

### 10.2 `kata daemon logs --hooks`

```
kata daemon logs --hooks [--tail] [--limit N] [--failed-only] [--event-type STR] [--hook-index N]
```

Direct read of `config.HookRunsPath(dbhash)`. No daemon involvement; works when the daemon is dead (the most likely time you want hook logs).

Behavior:
- File-rotation aware: includes `runs.jsonl.K ... runs.jsonl.1 runs.jsonl` in **chronological** order. With `--limit N`, the last N **matching** records across all files are printed.
- Malformed JSONL line: skipped, single-line warning to stderr (`kata: skipping malformed line N in <path>`), valid records still printed.
- `--tail`: prints last N matching records (per `--limit`, default 100), then follows current `runs.jsonl` via 200ms-poll loop. Detects rotation by size-decrease or file-identity change (`os.SameFile` on Unix; portable fallback uses size delta) and reopens.
- Filters are CLI-local; applied after JSONL parse.
- Missing file: non-tail prints nothing, exits 0. `--tail` waits for the file to appear.
- DB hash resolution: `config.KataDB()` → `config.DBHash()`. Independent of `daemon.Namespace` (works with daemon offline).

## 11. Defaults and tunables (v1)

| Tunable | Default | Range | Live-reloadable? |
|---|---|---|---|
| `pool_size` | 4 | [1, 16] | No (restart) |
| `queue_cap` | 1000 | [1, 10000] | No |
| `output_disk_cap` | 100 MB | ≥ 1 KB | No |
| `runs_log_max` | 50 MB | ≥ 1 KB | No |
| `runs_log_keep` | 5 | [1, 100] | No |
| `queue_full_log_interval` | 60 s | ≥ 1 s | No |
| Per-hook `timeout` | 30 s | (0, 5 m] | Yes (next enqueue uses new value) |
| Per-hook `working_dir` | `$KATA_HOME` | absolute path | Yes |
| `[[hook]]` entries | n/a | ≤ 256 total | Yes |

## 12. Testing

### 12.1 `hookprobe` helper

`internal/hooks/hookprobe/main.go` — built once via `TestMain` per test package that needs it.

Subcommands:
| Subcommand | Behavior |
|---|---|
| `hookprobe stdin` | Copy stdin → stdout, exit 0. |
| `hookprobe env KEY` | Print `os.Getenv(KEY)` → stdout, exit 0. |
| `hookprobe both OUT_LINE ERR_LINE` | Write `OUT_LINE` to stdout and `ERR_LINE` to stderr, exit 0. |
| `hookprobe exit N` | Exit with code `N`. |
| `hookprobe sleep DURATION` | `time.Sleep(DURATION)`, exit 0. |
| `hookprobe term-delay DURATION` | Trap SIGTERM → sleep `DURATION` → exit 0. |
| `hookprobe term-ignore` | Ignore SIGTERM (force SIGKILL fallback). |
| `hookprobe spawn-orphan DURATION` | Unix-only (build-tagged): fork child that ignores parent exit and sleeps `DURATION`; verifies process-group kill. |

### 12.2 Test plan

| Component | Case |
|---|---|
| Config | startup file missing → empty snapshot, default config |
| Config | startup file malformed → error returned |
| Config | reload missing → empty snapshot, hooks disabled |
| Config | reload malformed → keep current snapshot, error logged |
| Config | startup-only tunable changed on reload → `UnchangedTunables` populated, dispatcher unchanged |
| Config | unknown TOML key → load error names line + key |
| Config | `event = "*.created"` → load error |
| Config | `event = "sync.reset_required"` → load error |
| Config | `command = "./foo"` → load error; `bin/foo` → load error; `notify` → ok; `/abs/path` → ok |
| Config | `working_dir = "relative/path"` → load error |
| Config | `[hook.env]` key `KATA_FOO` → load error |
| Config | `[[hook]]` count = 257 → load error |
| Config | size-unit parser: `100k`, `1MB`, `100`, `100mb` all binary-base |
| Config | `UserEnv` sorted by key |
| Dispatcher | `Enqueue` non-blocking when queue full; drop counter increments |
| Dispatcher | partial-capacity drop-newest in source order |
| Dispatcher | duplicate hooks (same `command`, different `args`) both enqueue |
| Dispatcher | deterministic source-index fanout |
| Dispatcher | `Reload` atomic with concurrent `Enqueue` (-race) |
| Dispatcher | in-flight `HookJob` uses old config across reload |
| Dispatcher | `Shutdown(ctx)` with deadline shorter than in-flight `term-ignore` → returns error, logs "timed out" |
| Dispatcher | post-Shutdown `Enqueue` is no-op (no panic, no block) |
| Dispatcher | queued jobs present at Shutdown time are not executed (assertion: configure `pool_size=1`, hold the worker on a long-running job, fill the queue with N more jobs, call Shutdown; observe N un-popped jobs are dropped, only the in-flight one finishes or times out) |
| Dispatcher | `NewNoop()` returns a `Sink`; `Enqueue` is a no-op (no panic, no block); type-assert to `*Dispatcher` fails (interface-only) |
| Runner | `hookprobe exit 0` → `result=ok, exit_code=0` |
| Runner | `hookprobe exit 7` → `result=ok, exit_code=7` |
| Runner | nonexistent command → `result=spawn_failed`; empty `.out`/`.err` written |
| Runner | `working_dir` missing at fire time → `result=working_dir_missing` |
| Runner | `working_dir` exists but is a file → `result=spawn_failed` |
| Runner | `term-delay 10ms`, hook timeout 50ms, grace 200ms → `result=timed_out`, clean SIGTERM exit |
| Runner | `term-ignore`, hook timeout 50ms, grace 50ms → `result=timed_out`, killed |
| Runner | daemon ctx done while running → `result=daemon_shutdown` (not `timed_out`) |
| Runner | output capture: `hookprobe both` → `.out`/`.err` byte-exact |
| Runner | env: `hookprobe env KATA_EVENT_ID` outputs the id |
| Runner | env: user `EXTRA=value` visible |
| Runner | env precedence: `os.Environ` < user env < `KATA_*` |
| Runner | Unix process-group: `hookprobe spawn-orphan` — both parent and child killed by timeout (build-tagged unix-only) |
| Runner | concurrent workers append to `runs.jsonl`; every line parses; no record interleaving |
| Payload | structural unmarshal; required fields present |
| Payload | title 2 KB → `issue._truncated:true`, `_full_size:2048` |
| Payload | body 16 KB → field truncated to 8 KB |
| Payload | comment_body 16 KB → `payload._truncated:true` |
| Payload | construct event with all fields oversized → top-level `payload_truncated:true`, optional fields dropped in priority order, field flags persist |
| Payload | `AliasResolver` returns `(_, false, nil)` → no `alias` key |
| Payload | `AliasResolver` returns error → no `alias` key, daemon log captures |
| Payload | `IssueResolver` returns error → no `issue` block, daemon log captures, hook still runs |
| Payload | `CommentResolver` returns error on `issue.commented` → `payload.comment_id` preserved, `comment_body` omitted, hook still runs |
| Payload | non-`issue.commented` events → `CommentResolver` not called |
| Payload | `ProjectResolver` returns error → no `project.name`, but `project.id` and `project.identity` still present (sourced from event), hook still runs |
| Runner | output file open fails (dir deleted between New and fire) → `result=spawn_failed`, empty `stdout_path`/`stderr_path` in runs.jsonl, hook not spawned |
| Runner | `working_dir_missing` after files opened → `runs.jsonl` line carries non-empty `stdout_path`/`stderr_path` (zero-byte files) and `stdout_bytes=stderr_bytes=0` |
| Runner | `cmd.Start` fails after files opened → same: `runs.jsonl` carries valid paths + zero sizes |
| Dispatcher | `Shutdown` is idempotent — calling it twice does not panic and the second call returns nil immediately |
| Prune | startup walk seeds running total |
| Prune | run-group delete: `81237.2.out` + `81237.2.err` deleted together |
| Prune | best-effort: injected delete error → run still records, daemon log rate-limited |
| Prune | partial group (only `.out` exists) → both paths attempted; deletion of one missing file is not fatal |
| Runs.jsonl | rotation at threshold: `RunsLogMaxBytes=1KB` → `runs.jsonl` + `runs.jsonl.1` after enough writes |
| Runs.jsonl | `RunsLogKeep=2` → only `.1` and `.2` survive multiple rotations |
| Server wiring | startup with malformed config → daemon exits non-zero with clear error |
| Server wiring | issue.created HTTP create → mock dispatcher's `Enqueue` called once with the right event |
| Server wiring | default server config (no hook config supplied) → `NewNoop()` wired; mutations don't panic |
| SIGHUP bridge | unit-test daemon's reload loop by feeding a synthetic signal channel + a fake `Dispatcher` (records Reload calls); send signal, observe `Reload(loaded)` invoked exactly once with the parsed snapshot |
| CLI reload | no running daemon → `ExitUsage` |
| CLI reload | running daemon → exit 0 + stdout `reload signal sent` |
| CLI logs | reads `runs.jsonl.{3,2,1}` + `runs.jsonl` in chronological order; `--limit 100` is global last 100 |
| CLI logs | malformed line → skipped, stderr warning, valid lines still print |
| CLI logs | `--tail` rotation: rotate file mid-tail → reopens and continues |
| CLI logs | `--failed-only`: `result=ok exit_code=0` excluded; everything else included |
| CLI logs | missing file: non-tail exits 0 with no output; `--tail` waits then prints when file appears |
| e2e | spawn daemon, create issue via HTTP, hook fires `hookprobe`, `runs.jsonl` line appears, `kata daemon logs --hooks` shows it |

### 12.3 Race coverage

`go test -race` exercises:
- Concurrent `Enqueue` from multiple goroutines + `Reload` swap mid-stream.
- Concurrent `Enqueue` + `Shutdown`.
- Concurrent worker writes to `runs.jsonl` + rotation.

## 13. Out of scope (v1)

- Workspace-local `[[hook]]` blocks (master spec §10 backlog).
- Live resize of `pool_size` / `queue_cap` / disk caps.
- Webhooks (HTTP outbound) — operators wrap a hook script if needed.
- Hook output retention by event id (e.g., "keep last K runs of hook 2 regardless of total bytes").
- Per-hook isolated queues and worker counts (was Section 1 option C; deferred).
- Windows process-group / job-object semantics — Unix process-group only in v1; Windows is best-effort.

## 14. Cross-references

- Plan 4 spec (`docs/superpowers/specs/2026-04-30-kata-events-design.md`) — the `EventBroadcaster` is unchanged; hooks live alongside it, not as a subscriber.
- Master spec §3.3 (event types), §8 (hooks normative source), §10 (out-of-scope backlog).
