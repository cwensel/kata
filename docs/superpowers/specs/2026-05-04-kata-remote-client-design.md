# Remote Client — Design

> **Status:** design / spec.
> **Date:** 2026-05-04.
> **Companions:** `docs/superpowers/specs/2026-04-29-kata-design.md` (master), `docs/superpowers/specs/2026-04-29-kata-shared-server-mode.md` (future shared-mode guardrails).
>
> This spec is the smallest viable step from "v1 local-only daemon" to "one shared kata daemon on a private network, multiple thin clients on other hosts." It does **not** introduce auth, sync, or schema changes. It only relaxes daemon binding (opt-in, admin-only) and teaches the client how to discover a remote server via a workspace-local override.

## 1. Goals

1. A kata daemon, started by an admin under launchd/systemd/equivalent, can bind to a private-network IP and serve other hosts on the same network.
2. The kata CLI and the kata TUI on a *different* host can target that remote daemon without auto-starting a local one.
3. The mechanism honors a per-workspace override (`.kata.local.toml`) and an environment variable (`KATA_SERVER`), in that precedence with env winning.
4. Default behavior (no flags, no env, no local file) is byte-identical to today: loopback Unix-socket daemon, auto-started on demand.
5. `.kata.toml` remains committed, secret-free, and unchanged in shape — a misconfigured remote URL cannot leak into git.

## 2. Non-goals (deferred)

- **Authentication.** No bearer tokens, no shared secrets, no per-user identity. Network-level ACLs (firewall, VPN, tailnet) are the access boundary in v1. A follow-up will add a coarse shared token; see §10.
- **TLS / HTTPS.** Deployments that need encryption put a reverse proxy in front of `--listen`. kata speaks plain HTTP.
- **Per-user fallback config (`~/.kata/config.toml`).** Deferred until interaction with `.kata.local.toml` and per-machine vs per-workspace precedence is understood. See §10.
- **VPN-specific code.** No interface enumeration, no MagicDNS resolution, no tailscale/wireguard/zerotier integration. The network is a deployment concern; kata depends on no specific VPN.
- **Sync, federation, multi-daemon coordination.** Out of scope.

## 3. Locked decisions

These are settled and not re-litigated by the implementation plan.

1. **Daemon `--listen host:port` flag is admin-only.** Auto-start never produces a `--listen`-bound daemon. The flag is consumed only by `kata daemon start`.
2. **Default daemon binding is unchanged.** Without `--listen`, the daemon binds a Unix socket exactly as today. TCP endpoints produced by `ParseAddress` accept the same non-public address set as `--listen` (`requireNonPublic`); see §4.1 for why the runtime-file readback path can't be loopback-only once `--listen` is in play.
3. **`--listen` rejects clearly-public addresses.** Loopback, RFC1918 (`10/8`, `172.16/12`, `192.168/16`), CGNAT (`100.64/10`), link-local (`169.254/16`, `fe80::/10`), and ULA (`fc00::/7`) are accepted. Anything else (public IPv4, GUA IPv6, `0.0.0.0`, `::`) is rejected with a clear error. This catches typos like `--listen 0.0.0.0:7777` and prevents accidentally exposing kata on a public interface; it deliberately does not encode any specific VPN's address ranges.
4. **`KATA_SERVER` is the env name** (value: full URL like `http://100.64.0.5:7777`). Env wins over file so CI and ad-hoc overrides do not require editing checked-in or developer-local files.
5. **`.kata.local.toml` reuses the `.kata.toml` struct.** Same `version = 1`, same `[project]`, same optional `[server]` block. One parser, one struct, one merge step. Validation differs slightly: in the local file `[project]` is optional (since a developer may want to set only `[server]`), while in `.kata.toml` it remains required.
6. **`[server]` is the only meaningful new key.** It is optional in *both* files. In `.kata.toml` it would be ignored-but-not-rejected (the field is simply optional); in practice it lives in `.kata.local.toml`.
7. **Remote URL never falls back to local.** If `KATA_SERVER` is set or `.kata.local.toml` has `[server].url`, and the daemon at that URL does not answer, the client returns a `kindDaemonUnavail` error. Silent fallback would mask misconfiguration.
8. **`kata init` writes `.kata.local.toml` to `.gitignore`** (creating `.gitignore` if absent, idempotent on rerun). Cheap insurance against committing a server URL or future per-developer override.

## 4. Component changes

### 4.1 `internal/daemon/endpoint.go`

Add a sibling constructor that bypasses the loopback check, with its own validator:

```go
// TCPEndpointAny constructs a TCP endpoint that may bind to any
// non-public address (loopback, RFC1918, CGNAT, link-local, ULA).
// Public addresses are rejected.
func TCPEndpointAny(addr string) DaemonEndpoint { return tcpAnyEndpoint{addr: addr} }
```

`tcpAnyEndpoint.Listen()` calls a new `requireNonPublic(addr)` instead of `requireLoopback(addr)`. `requireNonPublic` accepts loopback, RFC1918, CGNAT, link-local, ULA. Rejects:

- public IPv4 (anything not in the accepted private ranges),
- GUA IPv6,
- `0.0.0.0` and `::` (unspecified — wildcard bind),
- non-IP hostnames (same rule as `requireLoopback`: callers must resolve hostnames to literal IPs).

The existing `TCPEndpoint` and `requireLoopback` are unchanged for callers that explicitly construct them — they remain the strict form for any future code path that wants loopback-only TCP. `ParseAddress`, however, returns `TCPEndpointAny` for TCP inputs: it serves the runtime-file readback path, where the daemon writes its own `--listen` address (which may be a non-loopback private address like `100.64.x.x`) and a same-host client must be able to dial that record back. Strict loopback-only would silently break local discovery on hosts that opted into `--listen`. The runtime file lives under `$KATA_HOME` (mode 0700) so it's a trusted source; public addresses still get rejected on Listen/Dial.

### 4.2 `cmd/kata/daemon_cmd.go`

`daemonStartCmd` grows a `--listen string` flag. Behavior:

- empty (default) → existing path: `daemon.UnixEndpoint(<runtime>/daemon.sock)`.
- non-empty → `daemon.TCPEndpointAny(value)`. Logs `kata daemon: listening on <addr>` to stderr at startup so launchd journals show the bound address.

Address-rule preflight goes through the non-binding `daemon.ValidateNonPublicAddress(addr)` helper rather than a listen-then-close, so the CLI surfaces a clear error before the server starts without a TOCTOU window where another process could claim the bound port (or, with port `0`, where the validating bind would discard the OS-allocated port). The actual bind happens once inside `server.Run`.

The runtime file written under `<KATA_HOME>/runtime/<dbhash>/daemon.<pid>.json` records the actual bound address (via `endpoint.Address()`), exactly as it does today for Unix sockets. Local discovery on the *server* host therefore picks up the listening daemon with no further work.

### 4.3 `internal/config/project_config.go`

Extend `ProjectConfig` with an optional `[server]` block:

```go
type ProjectConfig struct {
    Version int             `toml:"version"`
    Project ProjectBindings `toml:"project"`
    Server  ServerConfig    `toml:"server,omitempty"`
}

type ServerConfig struct {
    URL string `toml:"url,omitempty"`
}
```

The existing `ReadProjectConfig` continues to validate `version == 1` and a non-empty `project.identity`. `[server]` is optional; an empty struct is the legitimate "no server" state.

A new `FindProjectConfig(startPath)` helper walks upward from `startPath` looking for a readable `.kata.toml` and returns the parsed config plus the directory it lives in. Path-free remote-client resolution (`cmd/kata/create.go`'s `resolveProjectID`, `internal/tui/client.go`'s `ResolveProject`) uses this helper so a CLI invocation from a workspace subdirectory still sends `project_identity` instead of falling back to the daemon-side `start_path` walk — required for remote daemons that cannot stat the client's filesystem. Bootstrap callers (`kata init`) keep the exact-dir `ReadProjectConfig` since `.kata.toml` may not exist yet.

### 4.4 `internal/config/local_config.go` (new)

Add a parallel reader for `.kata.local.toml`:

```go
const LocalConfigFilename = ".kata.local.toml"

var ErrLocalConfigMissing = errors.New(".kata.local.toml not found")

func ReadLocalConfig(workspaceRoot string) (*ProjectConfig, error)
```

Reuses the same struct and the same TOML decoder. Validates `version == 1`. Treats an empty `[server].url` as absent (so a developer can keep the file around for future fields). Does **not** require `[project]` (the local file is allowed to carry only `[server]`).

Add a merge helper:

```go
// MergeLocal overlays non-empty fields from local onto base. Both must
// be valid ProjectConfig values; pass nil for "no local file".
func MergeLocal(base, local *ProjectConfig) *ProjectConfig
```

Merge rules:

- `Project.Identity` — `.kata.toml` wins (committed identity is canonical; if local sets a different one, it is ignored, with a one-line warning to stderr).
- `Project.Name` — local wins if non-empty.
- `Server.URL` — local wins if non-empty.

The "ignore divergent identity" rule is a guardrail: a developer's local override should never silently retarget which project they're filing into.

### 4.5 `internal/daemonclient/ensure.go`

`EnsureRunning` gains a precedence head before the existing `Discover` / auto-start path. The workspace-aware variant `EnsureRunningInWorkspace(ctx, workspaceStart)` takes the absolute path that anchors the `.kata.local.toml` walk; `EnsureRunning(ctx)` is a thin wrapper that passes `""` (CWD anchor):

```go
func EnsureRunning(ctx context.Context) (string, error) {
    return EnsureRunningInWorkspace(ctx, "")
}

func EnsureRunningInWorkspace(ctx context.Context, workspaceStart string) (string, error) {
    if v, ok := ctx.Value(BaseURLKey{}).(string); ok && v != "" {
        return v, nil // test injection, unchanged
    }
    if url, ok, err := resolveRemote(ctx, workspaceStart); err != nil {
        return "", err
    } else if ok {
        return url, nil
    }
    // existing local discovery + auto-start, unchanged
}
```

CLI commands that already accept `--workspace` plumb the resolved absolute path through `EnsureRunningInWorkspace`. The TUI starts in CWD and uses `EnsureRunning` directly.

`resolveRemote` is the new helper:

1. If `KATA_SERVER` is set and non-empty, treat it as the URL.
2. Else, walk up from the workspace anchor to find `.kata.local.toml`. The anchor is the absolute `--workspace` path when set, otherwise CWD — without this, running `kata --workspace /repo create` from outside the repo would silently miss `/repo/.kata.local.toml`. If found and parses with `[server].url` non-empty, treat that as the URL.
3. Else, return `("", false, nil)` — no remote configured, fall through.

When a URL is found, **probe it** via the existing `Probe(ctx, client, url)`. If probe succeeds, return the URL. If probe fails, return a `kindDaemonUnavail` error citing both the URL and the source ("KATA_SERVER" or path to `.kata.local.toml`). No fallback to local.

Discovery runtime files are not consulted when a remote URL is in effect, so the bot hosts never see "stale daemon" errors and never spawn a local daemon.

### 4.6 `cmd/kata/init.go`

`kata init` appends a single line `.kata.local.toml` to the workspace root's `.gitignore` after the daemon writes `.kata.toml`. The CLI uses the `workspace_root` field from the init response (already part of `ProjectResolveBody`) so the `.gitignore` lands beside `.kata.toml` even when init is invoked from a subdirectory of the workspace. Behavior:

- `.gitignore` does not exist → create it with one line.
- `.gitignore` exists and already contains `.kata.local.toml` → no-op.
- `.gitignore` exists without that line → append it (preserving existing newline style).

If an older daemon doesn't return `workspace_root`, the CLI falls back to the resolved start path; this preserves the original single-directory behavior. New daemons always populate the field.

This mirrors how most tools that produce both committed and local config files behave.

## 5. Resolution order (final)

For a kata client (CLI or TUI) on any host:

1. `ctx.Value(BaseURLKey{})` — test-only injection, unchanged.
2. `KATA_SERVER` env, if non-empty → probe, return on success, error on failure.
3. `.kata.local.toml` walked from the workspace anchor (absolute `--workspace` if set, else CWD), if it exists and `[server].url` is non-empty → probe, return on success, error on failure.
4. Existing `Discover` over local runtime files, then auto-start if none found — unchanged.

Steps 2 and 3 deliberately bypass local discovery and auto-start. A bot host with `KATA_SERVER` set creates no Unix socket and never spawns a daemon; if the remote is down, the command fails loudly.

## 6. Deployment topology (worked example)

The case this design serves directly:

- **Host A (admin / kata server).** Private-network IP `100.64.0.5` (CGNAT range, e.g. tailscale). Runs `kata daemon start --listen 100.64.0.5:7777` under launchd. Holds the SQLite DB. Local CLI/TUI on this host either find the daemon via the runtime file or set `KATA_SERVER=http://127.0.0.1:7777` for a direct path.
- **Host B (filing bot).** Different machine, different repo (a test harness checkout). Sets `KATA_SERVER=http://100.64.0.5:7777` in the bot's process env, or commits a `.kata.local.toml` (gitignored) with `[server] url = "http://100.64.0.5:7777"`. Runs `kata create ...` to file bugs. No local daemon, no Unix socket.
- **Host C (implementing bot).** Different machine again, different repo (the implementation checkout). Same setup as Host B. Runs `kata list`, `kata show`, `kata close` to consume and resolve issues.

`.kata.toml` on hosts B and C carries the **same `[project].identity`** as on host A, because the project namespace is shared. Only the local override / env differs per host.

## 7. Error handling

| Situation | Behavior |
|---|---|
| `--listen` value not parseable as `host:port` | Daemon refuses to start. Stderr: `kata daemon: invalid --listen value %q: %v`. |
| `--listen` value parses but address is public | Daemon refuses to start. Stderr names the address and says "use a private address (loopback, RFC1918, CGNAT, link-local, ULA)". |
| `KATA_SERVER` set but URL malformed | `EnsureRunning` returns a `kindUsage` error citing the value. |
| `KATA_SERVER` set, URL valid, daemon not responding | Returns a `kindDaemonUnavail` error: `kata server at <url> not responding (KATA_SERVER)`. |
| `.kata.local.toml` exists but is malformed | Returns a parse error with the file path and the TOML decoder's message. No fallback. |
| `.kata.local.toml` exists, `[server].url` empty | Treated as absent. Falls through to local discovery. |
| `.kata.local.toml` exists, URL valid, daemon not responding | Returns a `kindDaemonUnavail` error: `kata server at <url> not responding (.kata.local.toml at <path>)`. |
| `.kata.local.toml` `project.identity` differs from `.kata.toml` | Identity from `.kata.toml` is used. One-line warning to stderr. |

## 8. Testing

### 8.1 Unit

- `internal/daemon/endpoint_test.go` — `TCPEndpointAny` accepts loopback, RFC1918, CGNAT, link-local, ULA; rejects public IPv4, GUA IPv6, `0.0.0.0`, `::`, hostnames. Existing `TCPEndpoint` cases remain green and assert that strict loopback behavior is unchanged.
- `internal/config/local_config_test.go` — `ReadLocalConfig` returns sentinel on missing, parse error on malformed, valid struct on well-formed input. Empty `[server].url` is treated as absent. Identity-only and server-only files both parse.
- `internal/config/project_config_test.go` — extended to assert that an empty `[server]` block is fine and that adding `[server].url` to `.kata.toml` does not break existing readers.
- `MergeLocal` — covers all merge rules including the "ignore divergent identity" guardrail (with a captured stderr).
- `internal/daemonclient/ensure_test.go` — env URL wins over file URL; file URL wins over discovery; missing env / missing file → existing path; remote URL that does not probe → error, not local fallback. The existing version-skew restart test stays untouched.

### 8.2 CLI

- `cmd/kata/daemon_cmd_test.go` — `--listen` parses, builds the right endpoint kind, writes the bound address into the runtime file, refuses an invalid value.
- `cmd/kata/init_test.go` — first run creates `.gitignore` with `.kata.local.toml`; second run is idempotent; existing `.gitignore` with unrelated lines is preserved.

### 8.3 End-to-end

- `e2e/remote_client_test.go` — boots a daemon under a temp `KATA_HOME` with `--listen 127.0.0.1:0` (the test extracts the bound port from the runtime file), then runs a client subprocess in a different temp workspace with `KATA_SERVER=http://127.0.0.1:<port>` and exercises `kata create`, `kata list`, `kata close`. Verifies the client process never starts a local daemon (no runtime files appear in its `KATA_HOME`).

## 9. Backwards compatibility

- Every checkout without `.kata.local.toml` and without `KATA_SERVER` behaves identically to today.
- Every existing test that does not touch the new flag or env continues to pass.
- The `.kata.toml` schema is a strict superset of v1: adding `[server]` is optional, version stays `1`. `BurntSushi/toml.Decode` (already used by `ReadProjectConfig`) silently ignores unknown fields, so older binaries reading a `.kata.toml` that happens to include `[server]` will not error.
- The runtime file format is unchanged. A `--listen`-bound daemon writes the same JSON shape with a TCP `address` instead of a Unix path; existing readers handle both already.

## 10. Follow-ups (explicitly out of v1 scope)

These are captured here so the implementation plan does not silently absorb them.

1. **Shared-token auth.** A coarse bearer token: server reads it from `~/.kata/credentials.toml` (or env) at start, clients send it via `Authorization: Bearer ...`. Mirrors the shared-server-mode spec §4.4. Adds an `auth_required` error code. Probably a small follow-up plan once this lands.
2. **Per-machine fallback `~/.kata/config.toml`.** Same shape as `.kata.local.toml`, lower precedence. Defer until we see whether multiple repos on one machine actually want different defaults — if not, the workspace-local file plus env is enough.
3. **TLS / HTTPS.** Deployments that need it use a reverse proxy. Native TLS would require cert handling and opens questions (LetsEncrypt? user-supplied?) we do not need to answer yet.
4. **Identity-divergence handling.** Currently a warning. If field experience shows accidental divergence is a real source of bug-misfiling, escalate to a hard error.
5. **`.kata.local.toml` discovery semantics.** Today: walk up from the workspace anchor (absolute `--workspace` if set, else CWD). If we ever support nested kata workspaces (one repo, sub-projects each with its own `.kata.toml`), the walk semantics will need a more careful spec.

## 11. Relationship to federation foundation

Orthogonal. The federation foundation spec (`2026-05-04-kata-federation-foundation-design.md`) adds `meta.instance_uid`, event/purge UIDs, and `origin_instance_uid` so a future sync protocol has stable cross-instance identity. This spec changes only transport (daemon binding + client server resolution); nothing here touches schema, identity, or sync.

Hosts running in remote-client mode have no local DB and therefore no local `meta.instance_uid` — every event they file inherits the *server's* instance_uid, which is correct under federation's hub-authoritative model. If a host later wants its own identity (running a local daemon for offline work, then syncing back to the hub), it switches off `KATA_SERVER` / `.kata.local.toml` and federation does its job.

## 12. Implementation pointers

- The `requireLoopback` private range tables exist in Go's `net` package only partially. The implementation can use `net.IP.IsPrivate()` (Go 1.17+, covers RFC1918 and ULA), `IsLoopback()`, `IsLinkLocalUnicast()`, plus an explicit CGNAT check (`100.64.0.0/10`). Each predicate is small and worth a unit test.
- The `.gitignore` writer is a small append-with-dedup; reuse no helper, write it inline in `init.go` with a focused test. It is the kind of utility that grows wrong if abstracted prematurely.
- `EnsureRunning`'s new precedence head should probe with the same 1s-Timeout client used by `Discover`. A misconfigured remote should fail in around a second, not five (the auto-start deadline) — the failure is a misconfiguration, not a startup race.
- The merge between `.kata.toml` and `.kata.local.toml` is done at *read* time in a new exported helper, not at *use* time scattered across consumers. One read site, one merge result, fewer places to forget the override.
