# Remote Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in `--listen host:port` flag for the daemon (admin-only, non-public addresses) and a three-step client server-resolution helper (`KATA_SERVER` env → `.kata.local.toml` → existing local discovery) so kata clients can target a remote daemon over a private network.

**Architecture:** Five small, additive changes:
1. New `TCPEndpointAny` constructor in `internal/daemon/endpoint.go` with a `requireNonPublic` validator that accepts loopback, RFC1918, CGNAT, link-local, and ULA.
2. New `--listen` flag on `kata daemon start` that builds a `TCPEndpointAny` instead of the default `UnixEndpoint`.
3. Optional `[server]` block on `ProjectConfig`, plus a new `internal/config/local_config.go` reading `.kata.local.toml` (same struct, optional `[project]`, with a `MergeLocal` helper).
4. New precedence head in `daemonclient.EnsureRunning` that checks `KATA_SERVER`, then `.kata.local.toml`, probes the URL, and either returns it or fails loudly (no fallback to local).
5. `kata init` appends `.kata.local.toml` to the workspace's `.gitignore`.

Default behavior (no env, no local file, no `--listen`) is byte-identical to today.

**Tech Stack:** Go 1.26, `BurntSushi/toml`, `spf13/cobra`, `stretchr/testify`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-04-kata-remote-client-design.md`.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `internal/daemon/endpoint.go` | Modify | Add `TCPEndpointAny` + `requireNonPublic` alongside existing strict `TCPEndpoint`/`requireLoopback`. |
| `internal/daemon/endpoint_test.go` | Modify | Cover `TCPEndpointAny` accept/reject matrix, prove existing strict cases unchanged. |
| `internal/config/project_config.go` | Modify | Add `Server` struct, `Server.URL` field; existing reader unchanged. |
| `internal/config/project_config_test.go` | Modify | Cover the new optional `[server]` block on `.kata.toml`. |
| `internal/config/local_config.go` | Create | `ReadLocalConfig` and `MergeLocal` for `.kata.local.toml`. |
| `internal/config/local_config_test.go` | Create | Cover read-missing/malformed/valid plus all merge rules. |
| `internal/daemonclient/ensure.go` | Modify | New `resolveRemote` precedence head ahead of existing local discovery. |
| `internal/daemonclient/remote.go` | Create | `resolveRemote` helper + URL validation. (Splitting keeps `ensure.go` focused on its existing local-discovery responsibility.) |
| `internal/daemonclient/remote_test.go` | Create | Env > file precedence, probe failure → error, malformed inputs. |
| `cmd/kata/daemon_cmd.go` | Modify | Add `--listen` flag wiring and stderr log line. |
| `cmd/kata/daemon_cmd_test.go` | Modify | Cover `--listen` validation and runtime-file address. |
| `cmd/kata/init.go` | Modify | After successful `callInit`, append `.kata.local.toml` to `.gitignore`. |
| `cmd/kata/init_test.go` | Modify | Cover `.gitignore` create/append/idempotent. |
| `e2e/remote_client_test.go` | Create | End-to-end: spawn `--listen 127.0.0.1:0` daemon, run client subprocess against it via `KATA_SERVER`. |

Each file has one focused job. The split between `ensure.go` (existing local discovery + auto-start) and a new `remote.go` (env/file resolution + probe) keeps either side under 200 lines and means existing `ensure_test.go` does not need to grow further.

---

## Task 1: Daemon endpoint accepts non-public addresses

**Files:**
- Modify: `internal/daemon/endpoint.go`
- Modify: `internal/daemon/endpoint_test.go`

This task adds a sibling endpoint constructor that accepts any non-public IP, leaving the existing `TCPEndpoint` (loopback-only) and `ParseAddress` untouched.

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/endpoint_test.go`:

```go
func TestTCPEndpointAny_AcceptsLoopback(t *testing.T) {
	ep := daemon.TCPEndpointAny("127.0.0.1:0")
	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
}

func TestTCPEndpointAny_AcceptsPrivateRanges(t *testing.T) {
	cases := []string{
		"10.0.0.1:0",
		"172.16.5.5:0",
		"192.168.1.1:0",
		"100.64.0.5:0",   // CGNAT
		"169.254.1.1:0",  // link-local IPv4
		"[fe80::1]:0",    // link-local IPv6
		"[fc00::1]:0",    // ULA
		"[::1]:0",        // loopback IPv6
	}
	for _, addr := range cases {
		_, err := daemon.TCPEndpointAny(addr).Listen()
		// We do NOT require Listen() to succeed (binding 10.x without
		// a configured interface fails with "cannot assign requested
		// address"), only that it does not fail with our validator's
		// "non-public" rejection.
		if err != nil {
			assert.NotContains(t, err.Error(), "non-public", addr)
			assert.NotContains(t, err.Error(), "literal IP", addr)
		}
	}
}

func TestTCPEndpointAny_RejectsPublicIPv4(t *testing.T) {
	_, err := daemon.TCPEndpointAny("8.8.8.8:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestTCPEndpointAny_RejectsGUAIPv6(t *testing.T) {
	_, err := daemon.TCPEndpointAny("[2001:4860:4860::8888]:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestTCPEndpointAny_RejectsUnspecified(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0"} {
		_, err := daemon.TCPEndpointAny(addr).Listen()
		require.Error(t, err, addr)
		assert.Contains(t, err.Error(), "non-public", addr)
	}
}

func TestTCPEndpointAny_RejectsHostname(t *testing.T) {
	_, err := daemon.TCPEndpointAny("example.com:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "literal IP")
}

func TestTCPEndpoint_StillRejectsPrivateNonLoopback(t *testing.T) {
	// Guards against an accidental refactor that loosens TCPEndpoint.
	_, err := daemon.TCPEndpoint("10.0.0.1:0").Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/daemon/ -run 'TCPEndpointAny|TCPEndpoint_StillRejects' -v
```

Expected: compile error or FAIL because `TCPEndpointAny` does not exist yet.

- [ ] **Step 3: Implement `TCPEndpointAny` and `requireNonPublic`**

In `internal/daemon/endpoint.go`, add below the existing `tcpEndpoint`:

```go
type tcpAnyEndpoint struct{ addr string }

func (t tcpAnyEndpoint) Listen() (net.Listener, error) {
	if err := requireNonPublic(t.addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", t.addr)
}

func (t tcpAnyEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if err := requireNonPublic(t.addr); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", t.addr)
}

func (t tcpAnyEndpoint) Address() string { return t.addr }
func (t tcpAnyEndpoint) Kind() string    { return "tcp" }

// TCPEndpointAny constructs a TCP endpoint that accepts any non-public
// address (loopback, RFC1918, CGNAT, link-local, ULA). Public IPv4,
// GUA IPv6, and unspecified (0.0.0.0 / ::) are rejected. Hostnames are
// rejected — callers must resolve to a literal IP.
func TCPEndpointAny(addr string) DaemonEndpoint { return tcpAnyEndpoint{addr: addr} }

// cgnatBlock is RFC6598 100.64.0.0/10 — the carrier-grade NAT range
// commonly used by tailscale and similar private overlays. Go's
// net.IP.IsPrivate() does not include it.
var cgnatBlock = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// requireNonPublic accepts loopback, RFC1918 (via IsPrivate), CGNAT,
// link-local, and ULA. Rejects public IPv4, GUA IPv6, the unspecified
// address (0.0.0.0 / ::), and any hostname.
func requireNonPublic(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("address %q is not a literal IP (resolve hostnames before calling)", addr)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("address %q is non-public reject: unspecified bind not allowed (use a private address: loopback, RFC1918, CGNAT, link-local, ULA)", addr)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnatBlock.Contains(ip) {
		return nil
	}
	return fmt.Errorf("address %q is non-public reject: only loopback, RFC1918, CGNAT (100.64.0.0/10), link-local, or ULA are allowed", addr)
}
```

`net.IP.IsPrivate()` (Go 1.17+) covers RFC1918 IPv4 (`10/8`, `172.16/12`, `192.168/16`) and ULA IPv6 (`fc00::/7`). `IsLinkLocalUnicast()` covers IPv4 `169.254/16` and IPv6 `fe80::/10`. CGNAT is the only block we add manually.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/daemon/ -v
```

Expected: all endpoint tests pass, including the existing strict-loopback ones.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/endpoint.go internal/daemon/endpoint_test.go
git commit -m "feat(daemon): TCPEndpointAny for non-public addresses

Adds a sibling endpoint constructor that accepts loopback, RFC1918,
CGNAT, link-local, and ULA. Rejects public IPv4, GUA IPv6, the
unspecified bind, and hostnames. Existing TCPEndpoint (loopback-only)
and ParseAddress are unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

After committing, run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 2: `kata daemon start --listen` flag

**Files:**
- Modify: `cmd/kata/daemon_cmd.go`
- Modify: `cmd/kata/daemon_cmd_test.go`

Adds an admin-only flag that switches the daemon from a Unix socket to a TCP listener at the given address, using the validator from Task 1.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/kata/daemon_cmd_test.go`:

```go
func TestDaemonStart_ListenFlagRejectsPublicAddress(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "start", "--listen", "8.8.8.8:7777"})

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public")
}

func TestDaemonStart_ListenFlagRejectsMalformed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "start", "--listen", "not-a-host-port"})

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--listen")
}
```

These tests prove the validator wires through the cobra layer; the actual successful-bind path is covered by the e2e test in Task 7.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/kata/ -run TestDaemonStart_Listen -v
```

Expected: FAIL because `--listen` flag does not exist.

- [ ] **Step 3: Implement the flag**

In `cmd/kata/daemon_cmd.go`, replace `daemonStartCmd` with:

```go
func daemonStartCmd() *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "start the daemon in foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return runDaemonWithListen(ctx, listen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "",
		"bind TCP at host:port (admin-only; non-public addresses only). "+
			"Default: Unix socket under $KATA_HOME/runtime.")
	return cmd
}
```

Then split `runDaemon` to take the listen value. Replace the function with:

```go
// runDaemon is the foreground daemon entry point. Used by `kata daemon start`
// (no --listen, default Unix socket) and by the auto-start child process
// spawned by ensureDaemon.
func runDaemon(ctx context.Context) error {
	return runDaemonWithListen(ctx, "")
}

// runDaemonWithListen is the variant used by `kata daemon start --listen`.
// An empty listen string preserves the existing Unix-socket path exactly.
func runDaemonWithListen(ctx context.Context, listen string) error {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if err := ns.EnsureDirs(); err != nil {
		return err
	}
	dbPath, err := config.KataDB()
	if err != nil {
		return err
	}
	if ver, err := db.PeekSchemaVersion(ctx, dbPath); err == nil && ver < db.CurrentSchemaVersion() {
		if err := jsonl.AutoCutover(ctx, dbPath); err != nil {
			return err
		}
	}
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	disp, daemonLog, hookCfgPath, err := setupHooks(store, dbPath)
	if err != nil {
		return err
	}
	defer shutdownHooks(disp)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go runReloadLoop(ctx, sigs, hookCfgPath, disp, daemonLog)

	endpoint, err := chooseEndpoint(ns, listen)
	if err != nil {
		return err
	}

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        store,
		StartedAt: time.Now().UTC(),
		Endpoint:  endpoint,
		Hooks:     disp,
	})
	defer func() { _ = srv.Close() }()

	rec := daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   endpoint.Address(),
		DBPath:    dbPath,
		Version:   version.Version,
		StartedAt: time.Now().UTC(),
	}
	if _, err := daemon.WriteRuntimeFile(ns.DataDir, rec); err != nil {
		return err
	}
	runtimeFile := filepath.Join(ns.DataDir, fmt.Sprintf("daemon.%d.json", os.Getpid()))
	defer func() { _ = os.Remove(runtimeFile) }()

	if listen != "" {
		fmt.Fprintf(os.Stderr, "kata daemon: listening on %s\n", endpoint.Address())
	}

	return srv.Run(ctx)
}

// chooseEndpoint picks the daemon's listener: Unix socket when listen is
// empty (default, auto-start path) or TCPEndpointAny otherwise. The
// validation lives in TCPEndpointAny.Listen so this helper does not
// duplicate the rules.
func chooseEndpoint(ns *daemon.Namespace, listen string) (daemon.DaemonEndpoint, error) {
	if listen == "" {
		socketPath := filepath.Join(ns.SocketDir, "daemon.sock")
		return daemon.UnixEndpoint(socketPath), nil
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return nil, fmt.Errorf("--listen %q: %w", listen, err)
	}
	ep := daemon.TCPEndpointAny(listen)
	// Pre-flight the validator so the error surfaces before we attempt to
	// Listen — easier to read in a CLI error.
	if l, err := ep.Listen(); err != nil {
		return nil, fmt.Errorf("--listen %s: %w", listen, err)
	} else {
		_ = l.Close()
	}
	return ep, nil
}
```

Then add `"net"` to the imports in `cmd/kata/daemon_cmd.go` if it is not already imported.

The pre-flight is small and worth it: validation errors come back via cobra's normal error path before launchd sees a "started then died" log line.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/kata/ -run TestDaemonStart_Listen -v
```

Expected: PASS.

- [ ] **Step 5: Verify nothing else broke**

```bash
go test ./cmd/kata/ ./internal/daemon/ -v
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/daemon_cmd.go cmd/kata/daemon_cmd_test.go
git commit -m "feat(daemon): --listen host:port admin flag

When set, the daemon binds TCPEndpointAny at the given address
instead of the default Unix socket. Validation rejects public
addresses, hostnames, and unspecified binds. Default behavior
(no flag) is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 3: `[server]` block on `ProjectConfig`

**Files:**
- Modify: `internal/config/project_config.go`
- Modify: `internal/config/project_config_test.go`

Extends the existing struct with an optional `[server].url` field. No behavior change in `ReadProjectConfig`; just a structural addition so the same struct can be reused for `.kata.local.toml`.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/project_config_test.go`:

```go
func TestReadProjectConfig_AcceptsOptionalServerBlock(t *testing.T) {
	dir := t.TempDir()
	writeKataTOML(t, dir, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"

[server]
url = "http://127.0.0.1:7777"
`)
	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:7777", cfg.Server.URL)
}

func TestReadProjectConfig_NoServerBlockYieldsZeroValue(t *testing.T) {
	dir := t.TempDir()
	writeKataTOML(t, dir, `version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"
`)
	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Empty(t, cfg.Server.URL)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/ -run 'OptionalServerBlock|NoServerBlock' -v
```

Expected: compile error — `cfg.Server` does not exist.

- [ ] **Step 3: Add the field**

In `internal/config/project_config.go`, replace the `ProjectConfig` and `ProjectBindings` definitions and add `ServerConfig`:

```go
// ProjectConfig is the parsed contents of a workspace .kata.toml or
// .kata.local.toml. The same struct serves both files; readers differ
// only in which validations they enforce.
type ProjectConfig struct {
	Version int             `toml:"version"`
	Project ProjectBindings `toml:"project"`
	Server  ServerConfig    `toml:"server,omitempty"`
}

// ProjectBindings carries the [project] block.
type ProjectBindings struct {
	Identity string `toml:"identity"`
	Name     string `toml:"name,omitempty"`
}

// ServerConfig carries the [server] block. Optional in both committed
// and local config files. URL is the daemon base URL (e.g.
// http://100.64.0.5:7777). When set on .kata.local.toml it directs
// the client to a remote daemon; ignored if it appears in committed
// .kata.toml in v1, but parsed without error.
type ServerConfig struct {
	URL string `toml:"url,omitempty"`
}
```

`ReadProjectConfig` itself does not change.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/config/ -v
```

Expected: all config tests pass, including the new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/config/project_config.go internal/config/project_config_test.go
git commit -m "feat(config): optional [server] block on ProjectConfig

Adds an optional Server.URL field to the existing struct. The same
struct will serve both .kata.toml and the new .kata.local.toml.
ReadProjectConfig validation is unchanged; an absent block is the
zero value and is the legitimate \"no server\" state.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 4: `.kata.local.toml` reader and merge

**Files:**
- Create: `internal/config/local_config.go`
- Create: `internal/config/local_config_test.go`

Reads the local file (same struct, optional `[project]`) and merges it onto a base `ProjectConfig` per the spec rules.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/local_config_test.go`:

```go
package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func writeKataLocal(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, ".kata.local.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600)) //nolint:gosec
}

func TestReadLocalConfig_Missing(t *testing.T) {
	cfg, err := config.ReadLocalConfig(t.TempDir())
	assert.Nil(t, cfg)
	assert.ErrorIs(t, err, config.ErrLocalConfigMissing)
}

func TestReadLocalConfig_ServerOnly(t *testing.T) {
	dir := t.TempDir()
	writeKataLocal(t, dir, `version = 1

[server]
url = "http://100.64.0.5:7777"
`)
	cfg, err := config.ReadLocalConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Empty(t, cfg.Project.Identity)
	assert.Equal(t, "http://100.64.0.5:7777", cfg.Server.URL)
}

func TestReadLocalConfig_RejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	writeKataLocal(t, dir, `version = 2

[server]
url = "http://x"
`)
	_, err := config.ReadLocalConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported .kata.local.toml version")
}

func TestReadLocalConfig_EmptyServerURLIsZeroValue(t *testing.T) {
	dir := t.TempDir()
	writeKataLocal(t, dir, `version = 1

[server]
url = ""
`)
	cfg, err := config.ReadLocalConfig(dir)
	require.NoError(t, err)
	assert.Empty(t, cfg.Server.URL)
}

func TestReadLocalConfig_Malformed(t *testing.T) {
	dir := t.TempDir()
	writeKataLocal(t, dir, `version = 1
[server
url = "http://x"
`)
	_, err := config.ReadLocalConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".kata.local.toml")
}

func TestMergeLocal_NilLocalReturnsBase(t *testing.T) {
	base := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Identity: "github.com/wesm/kata", Name: "kata"},
	}
	got := config.MergeLocal(base, nil)
	assert.Same(t, base, got)
}

func TestMergeLocal_LocalServerWins(t *testing.T) {
	base := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Identity: "github.com/wesm/kata", Name: "kata"},
	}
	local := &config.ProjectConfig{
		Version: 1,
		Server:  config.ServerConfig{URL: "http://100.64.0.5:7777"},
	}
	var stderr bytes.Buffer
	got := config.MergeLocalWithStderr(base, local, &stderr)
	assert.Equal(t, "github.com/wesm/kata", got.Project.Identity)
	assert.Equal(t, "kata", got.Project.Name)
	assert.Equal(t, "http://100.64.0.5:7777", got.Server.URL)
	assert.Empty(t, stderr.String())
}

func TestMergeLocal_LocalNameOverridesBase(t *testing.T) {
	base := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Identity: "github.com/wesm/kata", Name: "kata"},
	}
	local := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Name: "Kata Local"},
	}
	got := config.MergeLocal(base, local)
	assert.Equal(t, "Kata Local", got.Project.Name)
}

func TestMergeLocal_DivergentIdentityWarnsAndIgnoresLocal(t *testing.T) {
	base := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Identity: "github.com/wesm/kata", Name: "kata"},
	}
	local := &config.ProjectConfig{
		Version: 1,
		Project: config.ProjectBindings{Identity: "github.com/other/repo"},
	}
	var stderr bytes.Buffer
	got := config.MergeLocalWithStderr(base, local, &stderr)
	assert.Equal(t, "github.com/wesm/kata", got.Project.Identity)
	assert.Contains(t, stderr.String(), "ignoring divergent project.identity")
	assert.Contains(t, stderr.String(), "github.com/other/repo")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/ -run 'LocalConfig|MergeLocal' -v
```

Expected: compile error — `ReadLocalConfig`, `MergeLocal`, `MergeLocalWithStderr`, `ErrLocalConfigMissing` do not exist.

- [ ] **Step 3: Implement the reader and merge helpers**

Create `internal/config/local_config.go`:

```go
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// LocalConfigFilename is the per-developer override file. Gitignored.
const LocalConfigFilename = ".kata.local.toml"

// ErrLocalConfigMissing is returned by ReadLocalConfig when the file
// is absent. Other I/O and parse errors are returned as-is.
var ErrLocalConfigMissing = errors.New(".kata.local.toml not found")

// ReadLocalConfig parses <workspaceRoot>/.kata.local.toml and validates
// version == 1. Unlike ReadProjectConfig, [project] is optional — a
// developer may set only [server]. Empty [server].url is treated as
// the zero value.
func ReadLocalConfig(workspaceRoot string) (*ProjectConfig, error) {
	path := filepath.Join(workspaceRoot, LocalConfigFilename)
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrLocalConfigMissing
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ProjectConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported .kata.local.toml version %d (expected 1)", cfg.Version)
	}
	cfg.Project.Identity = strings.TrimSpace(cfg.Project.Identity)
	cfg.Project.Name = strings.TrimSpace(cfg.Project.Name)
	cfg.Server.URL = strings.TrimSpace(cfg.Server.URL)
	return &cfg, nil
}

// MergeLocal overlays non-empty fields from local onto a copy of base.
// Pass nil for "no local file" — base is returned unchanged. Identity
// from .kata.toml is canonical: a divergent local identity is ignored
// with a one-line warning to stderr (use MergeLocalWithStderr in tests).
func MergeLocal(base, local *ProjectConfig) *ProjectConfig {
	return MergeLocalWithStderr(base, local, os.Stderr)
}

// MergeLocalWithStderr is MergeLocal with an explicit warning sink so
// tests can capture the divergent-identity warning.
func MergeLocalWithStderr(base, local *ProjectConfig, stderr io.Writer) *ProjectConfig {
	if local == nil {
		return base
	}
	merged := *base
	if local.Project.Identity != "" && local.Project.Identity != base.Project.Identity {
		fmt.Fprintf(stderr, "kata: ignoring divergent project.identity %q in .kata.local.toml (canonical is %q in .kata.toml)\n",
			local.Project.Identity, base.Project.Identity)
	}
	if local.Project.Name != "" {
		merged.Project.Name = local.Project.Name
	}
	if local.Server.URL != "" {
		merged.Server.URL = local.Server.URL
	}
	return &merged
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/config/ -v
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add internal/config/local_config.go internal/config/local_config_test.go
git commit -m "feat(config): .kata.local.toml reader and MergeLocal

Same struct as .kata.toml, [project] optional. Reader validates
version == 1 and trims whitespace. MergeLocal overlays non-empty
fields onto base (server.url, project.name); divergent
project.identity is ignored with a stderr warning so the canonical
committed identity is never silently retargeted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 5: Client server-resolution helper (env + .kata.local.toml + probe)

**Files:**
- Create: `internal/daemonclient/remote.go`
- Create: `internal/daemonclient/remote_test.go`
- Modify: `internal/daemonclient/ensure.go`

`resolveRemote` is the new precedence head consulted before `Discover`. It looks at `KATA_SERVER`, walks for `.kata.local.toml`, probes the URL, and either returns it or returns a `kindDaemonUnavail`-shaped error.

We do not import the CLI's `cliError` here (it lives in `cmd/kata` package main); the daemonclient layer returns plain errors with stable message prefixes that the CLI's existing error-classification (in `cmd/kata/client.go`) handles via the kind/exit-code wrapping it already does. To keep the contract explicit, `resolveRemote` returns a sentinel-wrapped error that `cmd/kata` can detect.

- [ ] **Step 1: Write the failing tests**

Create `internal/daemonclient/remote_test.go`:

```go
package daemonclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pingingServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "kata",
			"version": "test",
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestResolveRemote_NoEnvNoFile(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, url)
}

func TestResolveRemote_EnvWinsAndProbes(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", srv.URL)

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url)
}

func TestResolveRemote_EnvUnreachableErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "http://127.0.0.1:1") // closed port

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KATA_SERVER")
	assert.Contains(t, err.Error(), "http://127.0.0.1:1")
	assert.ErrorIs(t, err, ErrRemoteUnavailable)
}

func TestResolveRemote_EnvMalformedErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "::not-a-url::")

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KATA_SERVER")
}

func TestResolveRemote_FileWhenNoEnv(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "`+srv.URL+`"
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url)
}

func TestResolveRemote_EnvWinsOverFile(t *testing.T) {
	srv := pingingServer(t)
	t.Setenv("KATA_SERVER", srv.URL)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "http://10.255.255.1:9"
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, srv.URL, url, "env URL must win over file URL")
}

func TestResolveRemote_FileEmptyURLFallsThrough(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = ""
`), 0o600))

	url, ok, err := resolveRemote(context.Background())
	require.NoError(t, err)
	assert.False(t, ok, "empty server URL must be treated as no remote configured")
	assert.Empty(t, url)
}

func TestResolveRemote_FileUnreachableErrors(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.local.toml"),
		[]byte(`version = 1
[server]
url = "http://127.0.0.1:1"
`), 0o600))

	_, _, err := resolveRemote(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".kata.local.toml")
	assert.ErrorIs(t, err, ErrRemoteUnavailable)
}
```

`t.Chdir` was added in Go 1.24; this repo is on 1.26, so it is available.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/daemonclient/ -run ResolveRemote -v
```

Expected: compile error — `resolveRemote` and `ErrRemoteUnavailable` do not exist.

- [ ] **Step 3: Implement `resolveRemote`**

Create `internal/daemonclient/remote.go`:

```go
package daemonclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/kata/internal/config"
)

// remoteServerEnvVar is the environment variable that names a kata
// daemon URL. When set, it takes precedence over .kata.local.toml.
const remoteServerEnvVar = "KATA_SERVER"

// ErrRemoteUnavailable wraps probe failures against an explicitly
// configured remote URL (env or .kata.local.toml). Callers translate
// this into a daemon-unavailable CLI error; we keep the package free
// of CLI-layer types so this package stays importable from the TUI.
var ErrRemoteUnavailable = errors.New("kata server not responding")

// resolveRemote checks the two opt-in remote sources, in order:
//
//  1. KATA_SERVER env (highest precedence)
//  2. .kata.local.toml [server].url walked up from CWD
//
// If neither is set, returns ("", false, nil) and the caller falls
// through to local Discover/auto-start. If a URL is configured, the
// helper probes /api/v1/ping; on success it returns (url, true, nil),
// on failure it returns ("", false, ErrRemoteUnavailable wrapped with
// the URL and the source name) so the user sees which input is wrong.
func resolveRemote(ctx context.Context) (string, bool, error) {
	if v := os.Getenv(remoteServerEnvVar); v != "" {
		u, err := normalizeRemoteURL(v)
		if err != nil {
			return "", false, fmt.Errorf("KATA_SERVER %q: %w", v, err)
		}
		if !probeRemote(ctx, u) {
			return "", false, fmt.Errorf("%w: %s (KATA_SERVER)", ErrRemoteUnavailable, u)
		}
		return u, true, nil
	}
	root, path, ok := findLocalConfig()
	if !ok {
		return "", false, nil
	}
	cfg, err := config.ReadLocalConfig(root)
	if err != nil {
		if errors.Is(err, config.ErrLocalConfigMissing) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	if cfg.Server.URL == "" {
		return "", false, nil
	}
	u, err := normalizeRemoteURL(cfg.Server.URL)
	if err != nil {
		return "", false, fmt.Errorf("%s server.url %q: %w", path, cfg.Server.URL, err)
	}
	if !probeRemote(ctx, u) {
		return "", false, fmt.Errorf("%w: %s (%s)", ErrRemoteUnavailable, u, path)
	}
	return u, true, nil
}

// findLocalConfig walks upward from CWD looking for .kata.local.toml.
// Returns the directory containing it, the full path (for error
// messages), and ok=true. Stops at the filesystem root.
func findLocalConfig() (root, path string, ok bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		candidate := filepath.Join(dir, config.LocalConfigFilename)
		if _, err := os.Stat(candidate); err == nil {
			return dir, candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// normalizeRemoteURL parses a value as an http(s) URL and returns the
// canonical scheme://host[:port] form (no path, no query). Empty path
// matches the daemon's expectation: callers append /api/v1/... themselves.
func normalizeRemoteURL(v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("url must include host")
	}
	return u.Scheme + "://" + u.Host, nil
}

// probeRemote does a 1-second /api/v1/ping check against base. We keep
// the budget tight: a misconfigured remote should fail fast, not stall
// the user behind the 5-second auto-start deadline.
func probeRemote(ctx context.Context, base string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 1 * time.Second}
	return Ping(probeCtx, client, base)
}
```

Then wire it into `EnsureRunning`. In `internal/daemonclient/ensure.go`, replace the body of `EnsureRunning` with:

```go
func EnsureRunning(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(BaseURLKey{}).(string); ok && v != "" {
		return v, nil
	}
	if url, ok, err := resolveRemote(ctx); err != nil {
		return "", err
	} else if ok {
		return url, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, compatible, ok := discoverForEnsure(ctx, ns.DataDir); ok {
		if compatible {
			return url, nil
		}
		if err := stopRunningDaemonsForEnsure(ctx, ns.DataDir); err != nil {
			return "", err
		}
		return startDaemonForEnsure(ctx, ns.DataDir)
	}
	return startDaemonForEnsure(ctx, ns.DataDir)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/daemonclient/ -v
```

Expected: all green, including the existing `TestEnsureRunningRestartsWhenDaemonVersionDiffers` (because none of the new tests set `KATA_SERVER` or write `.kata.local.toml` on those paths, the new precedence head returns `(_, false, nil)` and the existing logic runs unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/daemonclient/remote.go internal/daemonclient/remote_test.go internal/daemonclient/ensure.go
git commit -m "feat(daemonclient): KATA_SERVER and .kata.local.toml resolution

EnsureRunning now consults two opt-in remote sources before falling
through to local discovery: KATA_SERVER env (highest), then a walked
.kata.local.toml. A configured-but-unreachable remote returns
ErrRemoteUnavailable rather than silently falling back to local —
misconfiguration is loud, not papered over.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 6: CLI surfaces remote-unreachable as kindDaemonUnavail

**Files:**
- Modify: `cmd/kata/client.go`
- Modify: `cmd/kata/client_test.go`

`ensureDaemon` is the CLI's wrapper around `daemonclient.EnsureRunning`. We translate `daemonclient.ErrRemoteUnavailable` into a `cliError{Kind: kindDaemonUnavail, ExitCode: ExitDaemonUnavail}` so `kata create` against a dead remote exits 7 with the existing daemon-unavailable shape.

- [ ] **Step 1: Write the failing test**

Append to `cmd/kata/client_test.go`:

```go
func TestEnsureDaemon_RemoteUnavailableMapsToCLIError(t *testing.T) {
	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("KATA_SERVER", "http://127.0.0.1:1") // closed port

	_, err := ensureDaemon(context.Background())
	require.Error(t, err)

	var ce *cliError
	require.True(t, errors.As(err, &ce), "expected *cliError, got %T (%v)", err, err)
	assert.Equal(t, kindDaemonUnavail, ce.Kind)
	assert.Equal(t, ExitDaemonUnavail, ce.ExitCode)
	assert.Contains(t, ce.Message, "127.0.0.1:1")
}
```

Add `"errors"` and `"context"` to the file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./cmd/kata/ -run RemoteUnavailable -v
```

Expected: FAIL — `ensureDaemon` returns the raw error today.

- [ ] **Step 3: Translate the error**

Replace `ensureDaemon` in `cmd/kata/client.go`:

```go
// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one
// if none is found. Thin wrapper over daemonclient.EnsureRunning so the CLI
// and TUI share one resolution path; tests still inject a base URL via
// daemonclient.BaseURLKey{} on the context.
//
// If a remote is explicitly configured (via KATA_SERVER or
// .kata.local.toml) but does not respond, the CLI surfaces this as a
// daemon-unavailable error so callers see a stable exit code and shape.
func ensureDaemon(ctx context.Context) (string, error) {
	url, err := daemonclient.EnsureRunning(ctx)
	if err == nil {
		return url, nil
	}
	if errors.Is(err, daemonclient.ErrRemoteUnavailable) {
		return "", &cliError{
			Message:  err.Error(),
			Kind:     kindDaemonUnavail,
			ExitCode: ExitDaemonUnavail,
		}
	}
	return "", err
}
```

Add `"errors"` to the imports of `cmd/kata/client.go` if not already present.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./cmd/kata/ -run RemoteUnavailable -v
```

Expected: PASS.

- [ ] **Step 5: Verify nothing else broke**

```bash
go test ./... -count=1
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add cmd/kata/client.go cmd/kata/client_test.go
git commit -m "feat(cli): map ErrRemoteUnavailable to kindDaemonUnavail

A configured-but-unreachable remote (KATA_SERVER or .kata.local.toml)
now exits with the standard daemon-unavailable code (7) and a
cliError carrying the URL and source for human readers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 7: `kata init` writes `.kata.local.toml` to `.gitignore`

**Files:**
- Modify: `cmd/kata/init.go`
- Modify: `cmd/kata/init_test.go`

After the daemon successfully writes `.kata.toml`, the CLI appends `.kata.local.toml` to the workspace's `.gitignore` so a developer cannot commit a server URL by accident.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/kata/init_test.go`:

```go
func TestInit_AddsLocalToGitignoreWhenAbsent(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), ".kata.local.toml")
}

func TestInit_GitignoreIsIdempotent(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("node_modules/\n.kata.local.toml\n"), 0o644))

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	// Exactly one occurrence — no duplication on re-run.
	assert.Equal(t, 1, strings.Count(string(content), ".kata.local.toml"))
	assert.Contains(t, string(content), "node_modules/")
}

func TestInit_GitignoreAppendsToExisting(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("dist/\n"), 0o644))

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	_, err := callInit(context.Background(), env.URL, dir, callInitOpts{})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "dist/")
	assert.Contains(t, string(content), ".kata.local.toml")
}
```

Add `"os"`, `"strings"` to the imports of `cmd/kata/init_test.go` if absent.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/kata/ -run 'Init_.*Gitignore' -v
```

Expected: FAIL — `.gitignore` is not touched today.

- [ ] **Step 3: Add the gitignore writer**

In `cmd/kata/init.go`, replace the `RunE` of the `init` cobra command to call a new helper after the daemon call returns success:

```go
RunE: func(cmd *cobra.Command, _ []string) error {
	baseURL, err := ensureDaemon(cmd.Context())
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	startPath, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	out, err := callInit(cmd.Context(), baseURL, startPath, callInitOpts(opts))
	if err != nil {
		return err
	}
	if err := ensureGitignoreEntry(startPath, ".kata.local.toml"); err != nil {
		// Non-fatal: a gitignore failure shouldn't abort init. Log to
		// stderr so the operator notices and can fix it.
		fmt.Fprintf(os.Stderr, "kata: warning: could not update .gitignore: %v\n", err)
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), out)
	return err
},
```

Add the helper at the bottom of `cmd/kata/init.go`:

```go
// ensureGitignoreEntry appends a single line to <dir>/.gitignore if
// the entry is not already present. Creates the file if absent.
// Idempotent: re-running on a file that already lists the entry is a
// no-op.
func ensureGitignoreEntry(dir, entry string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path) //nolint:gosec
	switch {
	case err == nil:
		// Walk lines so we don't false-match a substring inside a longer
		// pattern (e.g. ".kata.local.toml.bak").
		for _, line := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(line) == entry {
				return nil
			}
		}
		// Preserve trailing-newline convention: if the file ends without
		// a newline, add one before appending so we don't merge our line
		// into theirs.
		var prefix string
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			prefix = "\n"
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
			return err
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(entry+"\n"), 0o644) //nolint:gosec
	default:
		return err
	}
}
```

Add `"strings"` to the imports if absent (`os`, `errors`, `filepath`, `fmt` are already imported).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/kata/ -run 'Init_' -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/kata/init.go cmd/kata/init_test.go
git commit -m "feat(cli): kata init adds .kata.local.toml to .gitignore

After successful init, append .kata.local.toml to the workspace's
.gitignore (creating it if needed). Idempotent — re-running does not
duplicate the line. Failure is non-fatal: the daemon already wrote
.kata.toml; we warn but do not abort.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 8: End-to-end remote-client test

**Files:**
- Create: `e2e/remote_client_test.go`

Boots a daemon under a temp `KATA_HOME` with `--listen 127.0.0.1:0`, extracts the bound port from the runtime file, then runs a client subprocess in a *different* temp `KATA_HOME` and a *different* workspace directory with `KATA_SERVER` pointing at the daemon. Exercises a real CLI end-to-end and verifies the client process never wrote a runtime file (proving no local daemon got auto-started).

- [ ] **Step 1: Build the test binary path helper (if not present)**

Check whether `e2e/e2e_test.go` or another existing e2e file has a helper that returns the path to the compiled `kata` binary. If yes, reuse it. If no, the test below creates its own with `go build`.

```bash
grep -rn "go build\|kata-binary\|katabin" e2e/ | head
```

If a helper exists, prefer it.

- [ ] **Step 2: Write the failing test**

Create `e2e/remote_client_test.go`:

```go
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildKataBinary compiles the kata CLI into a temp file and returns
// its path. The binary is reused across the test's subprocesses.
func buildKataBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "kata-bin")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/kata")
	cmd.Dir = projectRoot(t)
	combined, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "go build failed: %s", combined)
	return out
}

// projectRoot walks up from the test's working directory looking for
// go.mod. Tests run from the package directory, so go.mod is in a parent.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestRemoteClient_DaemonOnTCPClientViaKATA_SERVER(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := buildKataBinary(t)

	// Server-side state: its own KATA_HOME, runs the daemon.
	serverHome := t.TempDir()
	serverDB := filepath.Join(serverHome, "kata.db")

	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()

	daemon := exec.CommandContext(daemonCtx, bin, "daemon", "start", "--listen", "127.0.0.1:0")
	daemon.Env = append(os.Environ(),
		"KATA_HOME="+serverHome,
		"KATA_DB="+serverDB,
	)
	stderr, err := daemon.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, daemon.Start())
	t.Cleanup(func() {
		cancelDaemon()
		_ = daemon.Wait()
	})

	// Wait for the runtime file to appear so we can read the bound address.
	addr := waitForRuntimeAddress(t, serverHome, 5*time.Second)
	t.Logf("daemon bound at %s", addr)
	_ = stderr // keep the pipe alive

	// Client-side state: separate KATA_HOME, separate workspace.
	clientHome := t.TempDir()
	clientWS := t.TempDir()
	require.NoError(t, runGitInit(clientWS))
	require.NoError(t, runGitRemote(clientWS, "https://github.com/wesm/system.git"))

	clientEnv := append(os.Environ(),
		"KATA_HOME="+clientHome,
		"KATA_DB="+filepath.Join(clientHome, "kata.db"),
		"KATA_SERVER=http://"+addr,
		"KATA_AUTHOR=e2e-bot",
	)

	// init through the remote.
	require.NoError(t, runKata(t, bin, clientWS, clientEnv, "init"))

	// create an issue.
	require.NoError(t, runKata(t, bin, clientWS, clientEnv,
		"create", "--title", "hello from remote", "--body", "via KATA_SERVER"))

	// list and confirm.
	out, err := runKataOutput(t, bin, clientWS, clientEnv, "list", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, "hello from remote")

	// close it.
	require.NoError(t, runKata(t, bin, clientWS, clientEnv, "close", "1"))

	// Critical assertion: the client wrote no runtime files into its own
	// KATA_HOME — proving no local daemon was auto-started.
	clientRuntime := filepath.Join(clientHome, "runtime")
	if entries, err := os.ReadDir(clientRuntime); err == nil {
		// The directory may exist (config.RuntimeDir() lazily resolves
		// it) but must be empty of daemon.*.json files.
		for _, e := range entries {
			require.NoError(t, walkAssertNoDaemonFiles(t, filepath.Join(clientRuntime, e.Name())))
		}
	} else {
		require.True(t, errors.Is(err, os.ErrNotExist), "unexpected runtime read error: %v", err)
	}
}

func waitForRuntimeAddress(t *testing.T, home string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(home, "runtime"))
		for _, dirEntry := range entries {
			runDir := filepath.Join(home, "runtime", dirEntry.Name())
			files, _ := os.ReadDir(runDir)
			for _, f := range files {
				if !strings.HasPrefix(f.Name(), "daemon.") || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(runDir, f.Name()))
				if err != nil {
					continue
				}
				var rec struct {
					Address string `json:"address"`
				}
				if err := json.Unmarshal(data, &rec); err == nil && rec.Address != "" {
					return rec.Address
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon runtime file did not appear within %s", timeout)
	return ""
}

func walkAssertNoDaemonFiles(t *testing.T, dir string) error {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "daemon.") && strings.HasSuffix(f.Name(), ".json") {
			t.Errorf("client unexpectedly wrote runtime file %s/%s", dir, f.Name())
		}
	}
	return nil
}

func runKata(t *testing.T, bin, workdir string, env, args []string) error {
	t.Helper()
	cmd := exec.Command(bin, args[0], args[1:]...)
	cmd.Dir = workdir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("kata %v output:\n%s", args, out)
	}
	return err
}

func runKataOutput(t *testing.T, bin, workdir string, env, args []string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runGitInit(dir string) error {
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	return cmd.Run()
}

func runGitRemote(dir, url string) error {
	cmd := exec.Command("git", "remote", "add", "origin", url)
	cmd.Dir = dir
	return cmd.Run()
}
```

If `e2e_test.go` already defines `runKata`, `runKataOutput`, `buildKataBinary`, `projectRoot`, or `waitForRuntimeAddress` with compatible signatures, drop the duplicates and reuse the existing helpers (a quick grep at the top of this task tells you).

The `runKata` helper splits args into `args[0]` and `args[1:]` because `exec.Command` flattens variadic args back into a single argv anyway, but the call site reads cleanly when args are passed as a `...string` from the test.

- [ ] **Step 3: Run the test to verify it passes**

```bash
go test ./e2e/ -run TestRemoteClient -v -count=1
```

Expected: PASS. The test takes a few seconds (build + daemon startup + a handful of CLI roundtrips).

- [ ] **Step 4: Run the full suite to verify nothing regressed**

```bash
go test ./... -count=1
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add e2e/remote_client_test.go
git commit -m "test(e2e): remote client over --listen + KATA_SERVER

Spawns a daemon bound to 127.0.0.1:0, extracts its port from the
runtime file, then runs a client subprocess in a separate
KATA_HOME/workspace pointed at the remote via KATA_SERVER. Exercises
init/create/list/close end-to-end and asserts the client process
wrote no runtime files locally — proving no local daemon was
auto-started.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Task 9: Documentation pointer in CLAUDE.md / README

**Files:**
- Modify: `CLAUDE.md` (or `README.md` if more appropriate per project convention)

A one-paragraph note pointing operators at the new flag and env var. Helps future agents and humans find the feature without grepping the code.

- [ ] **Step 1: Read CLAUDE.md to find a suitable insertion point**

```bash
sed -n '1,80p' CLAUDE.md
```

Find a section that documents env-var or operator-facing flags (or, if none exists, decide whether the snippet belongs here or in README.md).

- [ ] **Step 2: Append the snippet**

Add (under an appropriate heading — match existing style):

```markdown
## Remote daemon (opt-in)

A kata daemon can serve clients on other hosts over a private network:

- Admin: `kata daemon start --listen 100.64.0.5:7777` (only non-public
  IPs are accepted: loopback, RFC1918, CGNAT, link-local, ULA).
- Client: set `KATA_SERVER=http://100.64.0.5:7777` *or* commit a
  gitignored `.kata.local.toml` next to `.kata.toml`:

  ```toml
  version = 1
  [server]
  url = "http://100.64.0.5:7777"
  ```

Default behavior (no flag, no env, no local file) is unchanged: a
local Unix-socket daemon is auto-started on demand. See
`docs/superpowers/specs/2026-05-04-kata-remote-client-design.md`.
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: remote-daemon usage snippet (--listen, KATA_SERVER)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Run `git verify-commit HEAD` and confirm "Good signature".

---

## Final verification

- [ ] **Run the whole test suite**

```bash
go test ./... -count=1
```

Expected: all green.

- [ ] **Run lint / vet**

```bash
go vet ./...
```

Expected: no diagnostics.

- [ ] **Manually exercise the happy path**

Open two shells.

Shell 1 (server):

```bash
export KATA_HOME=$(mktemp -d)
go run ./cmd/kata daemon start --listen 127.0.0.1:7777
```

Shell 2 (client):

```bash
export KATA_HOME=$(mktemp -d)
export KATA_SERVER=http://127.0.0.1:7777
export KATA_AUTHOR=manual
mkdir /tmp/kata-client-ws && cd /tmp/kata-client-ws
git init -q && git remote add origin https://github.com/wesm/system.git
go run /path/to/kata init
go run /path/to/kata create --title "manual smoke" --body "from a remote client"
go run /path/to/kata list
ls $KATA_HOME    # should NOT contain a 'runtime' directory with daemon files
```

Confirm the issue lands in the server's DB and no local daemon appeared on the client side.

---

## Plan self-review checklist (run after writing, before handoff)

This is a reviewer's pass over the plan, not a step in the implementation.

**Spec coverage** (every section of the spec maps to at least one task):

- §3.1 admin-only `--listen` → Task 2.
- §3.2 default binding unchanged → Task 1 (negative test for strict TCPEndpoint), Task 2 (default path).
- §3.3 reject public addresses → Task 1.
- §3.4 `KATA_SERVER` env precedence → Task 5.
- §3.5 same struct, optional `[project]` in local → Task 3 (struct), Task 4 (reader).
- §3.6 optional `[server]` in both files → Task 3.
- §3.7 no fallback to local on remote failure → Task 5 (resolve), Task 6 (CLI mapping).
- §3.8 `kata init` writes `.gitignore` → Task 7.
- §4.1–4.6 component changes → Tasks 1–7 cover each.
- §5 resolution order → Task 5.
- §7 error handling table → Tasks 2, 5, 6 (each row is a test or production path).
- §8.1 unit tests → Tasks 1, 3, 4, 5.
- §8.2 CLI tests → Tasks 2, 6, 7.
- §8.3 e2e → Task 8.

**Placeholder scan:** every `RunE`, helper, and merge function shows the actual code. No "implement appropriate handler" or "similar to" — Task 5's `resolveRemote` is fully spelled out, the `MergeLocal` rules are concrete, the `.gitignore` writer's edge cases are enumerated.

**Type consistency:** `ProjectConfig.Server.URL` (Task 3) is the field referenced by `MergeLocal` (Task 4) and by `cfg.Server.URL` in `resolveRemote` (Task 5). `ErrRemoteUnavailable` is defined in Task 5 and consumed by Task 6 via `errors.Is`. `kindDaemonUnavail` and `ExitDaemonUnavail` are pre-existing constants used unchanged in Task 6.

**Out-of-scope items not silently absorbed:** no auth handling, no TLS, no `~/.kata/config.toml`, no VPN-specific code, no schema changes — each was deferred in spec §10 and remains absent here.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-04-kata-remote-client.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
