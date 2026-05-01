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
| `internal/hooks/dispatcher.go` | `Dispatcher` type with `Enqueue`, `Reload`, `Shutdown`; `NoopDispatcher`; queue, snapshot pointer, queue-full counter |
| `internal/hooks/runner.go` | Worker exec path: process spawn, stdin write, output capture, timeout SIGTERM/grace/SIGKILL, runs.jsonl record |
| `internal/hooks/payload.go` | Stdin JSON envelope construction + truncation; alias enrichment |
| `internal/hooks/prune.go` | Output-dir disk-cap rescan + run-group delete; `runs.jsonl` rotation |
| `internal/hooks/proc_unix.go` | `Setpgid: true` on `cmd.SysProcAttr` (build tag `!windows`) |
| `internal/hooks/proc_windows.go` | No-op stub (build tag `windows`) |
| `internal/hooks/hookprobe/main.go` | Test helper binary (only built/run by tests; placement under `internal/` keeps it out of user-facing builds) |
| `internal/config/paths.go` (additions) | `HookConfigPath()`, `HookRootDir(dbhash)`, `HookOutputDir(dbhash)`, `HookRunsPath(dbhash)` |

### 3.2 Modified files

| Path | Change |
|---|---|
| `internal/daemon/server.go` | Load hooks at startup (fatal on malformed); construct `Dispatcher`; wire SIGHUP bridge → `ReloadCh`; `defer Dispatcher.Shutdown(ctx)`; expose `cfg.Hooks` on `ServerConfig` |
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
3. `command` is non-empty and either an absolute path OR a bare name with no `/` characters. Relative paths like `./foo` or `bin/foo` are rejected.
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
type Dispatcher struct { /* unexported */ }

type DispatcherDeps struct {
    DBHash         string
    KataHome       string
    DaemonLog      *log.Logger
    AliasResolver  func(evt db.Event) (AliasSnapshot, bool, error)
    ReloadCh       <-chan struct{}     // tests send/close; daemon wires SIGHUP into it
    Now            func() time.Time    // tests inject
    GraceWindow    time.Duration       // SIGTERM→SIGKILL window; default 5s; tests shorten
}

func New(loaded LoadedConfig, deps DispatcherDeps) (*Dispatcher, error)
func (d *Dispatcher) Enqueue(evt db.Event)
func (d *Dispatcher) Reload(loaded LoadedConfig)         // logs UnchangedTunables; swaps Snapshot
func (d *Dispatcher) Shutdown(ctx context.Context) error // see §5.4
```

`hooks.NoopDispatcher` satisfies the same surface but every method is a no-op. `cfg.Hooks` is always non-nil; default server configs (e.g., older daemon tests) wire `NoopDispatcher`.

### 5.2 Enqueue

```
snap := atomic snapshot
matches := []ResolvedHook{} for h in snap.Hooks where h.Match(evt.Type)
if shutdown started: return  // no-op, no panic, no block
for h in matches in source order:
    job := HookJob{Event: evt, Hook: h, EnqueuedAt: now()}
    select {
    case queue <- job: /* ok */
    default:
        atomic.AddInt64(&d.dropped, 1)
        d.maybeLogQueueFull()  // rate-limited per QueueFullLogInterval
    }
```

Drop-newest is per-job, not per-event. If an event matches N hooks but only M ≤ N slots are free, the first M (in source order) enqueue and the remaining drop. Partial delivery is acceptable because hooks are independent observers.

### 5.3 Reload

`Dispatcher.Reload(loaded)` does:
1. For each `s` in `loaded.UnchangedTunables`: `daemonLog.Printf("hooks: %s", s)`.
2. `snapshot.Store(&loaded.Snapshot)`.
3. `daemonLog.Printf("hooks reload ok: %d hook(s) active", len(loaded.Snapshot.Hooks))`.

Queued and in-flight `HookJob`s are unaffected: each carries its own `ResolvedHook` so workers never consult the snapshot.

### 5.4 Shutdown

`Dispatcher.Shutdown(ctx context.Context) error`:
1. Set `shutdown` flag (subsequent `Enqueue` calls are no-ops).
2. Close the queue (workers see channel close after draining items they've already popped).
3. Drop queued (un-popped) jobs.
4. Wait for in-flight workers up to `ctx` deadline.
5. On timeout, log `hooks shutdown timed out: N in-flight` and return a non-nil error. Daemon proceeds with shutdown either way.

## 6. Runner (per-job execution)

### 6.1 Sequence

```go
job := <-queue
out := <KataHome>/hooks/<dbhash>/output/<event_id>.<hook_index>.out
err := <KataHome>/hooks/<dbhash>/output/<event_id>.<hook_index>.err

// 1. Open output files first so every recordRun references valid paths,
//    even for early-exit failure modes.
outFile, _ := os.OpenFile(out, O_CREATE|O_WRONLY|O_TRUNC, 0o600)
errFile, _ := os.OpenFile(err, O_CREATE|O_WRONLY|O_TRUNC, 0o600)

// 2. Pre-spawn working_dir check.
if st, e := os.Stat(job.Hook.WorkingDir); e != nil {
    if errors.Is(e, fs.ErrNotExist):
        recordRun(result="working_dir_missing", spawn_error=e.Error())
        return
    recordRun(result="spawn_failed", spawn_error=e.Error())
    return
} else if !st.IsDir():
    recordRun(result="spawn_failed", spawn_error="working_dir is not a directory")
    return

// 3. Build command — exec.Command, NOT exec.CommandContext.
cmd := exec.Command(job.Hook.Command, job.Hook.Args...)
cmd.Dir = job.Hook.WorkingDir
cmd.Env = buildEnv(job.Hook.UserEnv, job.Event, alias)  // §6.3
cmd.Stdin = bytes.NewReader(stdinPayload)               // §7
cmd.Stdout = outFile
cmd.Stderr = errFile
applyProcessGroupAttrs(cmd)                             // unix Setpgid; windows no-op

// 4. Spawn.
if e := cmd.Start(); e != nil:
    recordRun(result="spawn_failed", spawn_error=e.Error())
    return

// 5. Wait with two contexts: hook timeout + daemon shutdown.
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
case <-daemonCtx.Done():
    result = "daemon_shutdown"
    killTreeWithGrace(cmd, deps.GraceWindow)
    <-done
}

// 6. Close output files; record run.
recordRun(result, exit_code, durations, file paths and sizes, payload_truncated)

// 7. Best-effort prune.
prune.MaybeSweep(deps.OutputDir, deps.OutputDiskCap)
```

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
| `issue.body` | 8 KB | Same shape on `issue`. |
| `payload.comment_body` | 8 KB | Sibling `payload._truncated:true`, `payload._full_size:N`. |

After per-field caps, if total bytes still > 256 KB:
1. Set top-level `payload_truncated: true`.
2. Drop optional fields in priority order until under cap: `payload` → `issue.body` → `issue.title`.
3. Field-level `_truncated` flags persist across the top-level fallback.

### 7.3 Alias enrichment

```go
type AliasSnapshot struct {
    Identity string  // marshalled as "alias_identity"
    Kind     string  // "alias_kind"
    RootPath string  // "root_path"
}
```

`AliasResolver` is `func(evt db.Event) (AliasSnapshot, bool, error)`:
- `(snap, true, nil)` → embed `alias` block in the payload.
- `(_, false, nil)` → omit `alias` (events emitted from contexts without a single workspace; rare).
- `(_, _, err)` → omit `alias`, log via `daemonLog`. Run the hook anyway.

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
| Dispatcher | `NoopDispatcher` Enqueue/Reload/Shutdown all no-op |
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
| Prune | startup walk seeds running total |
| Prune | run-group delete: `81237.2.out` + `81237.2.err` deleted together |
| Prune | best-effort: injected delete error → run still records, daemon log rate-limited |
| Prune | partial group (only `.out` exists) → both paths attempted; deletion of one missing file is not fatal |
| Runs.jsonl | rotation at threshold: `RunsLogMaxBytes=1KB` → `runs.jsonl` + `runs.jsonl.1` after enough writes |
| Runs.jsonl | `RunsLogKeep=2` → only `.1` and `.2` survive multiple rotations |
| Server wiring | startup with malformed config → daemon exits non-zero with clear error |
| Server wiring | issue.created HTTP create → mock dispatcher's `Enqueue` called once with the right event |
| Server wiring | default server config (no hook config supplied) → `NoopDispatcher` wired; mutations don't panic |
| SIGHUP bridge | inject `ReloadCh`, send signal, observe `Reload` called |
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
