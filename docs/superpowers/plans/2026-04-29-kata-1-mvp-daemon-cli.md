# Plan 1 — MVP Daemon + Minimal CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the bottom half of kata: a single-binary daemon (Unix socket / TCP loopback) with SQLite persistence, project resolution via `.kata.toml`, and a minimal CLI exercising init / create / show / list / edit / comment / close / reopen / whoami / health / projects.

**Architecture:** Cobra CLI auto-starts a Huma-on-net/http daemon over a per-PID Unix socket (TCP loopback fallback). Daemon owns path discovery (walks up for `.kata.toml`/`.git`), alias-identity computation, and all DB writes against `modernc.org/sqlite` in WAL mode. `.kata.toml` is the workspace binding contract; `kata init` is the only path that creates project rows.

**Tech Stack:** Go 1.26 · `modernc.org/sqlite` v1.49.1 · `huma/v2` v2.37.3 · `cobra` v1.10.2 · `BurntSushi/toml` v1.6.0 · `mattn/go-isatty` v0.0.21 · `testify` v1.11.1.

**Reference spec:** `docs/superpowers/specs/2026-04-29-kata-design.md` (commits `f52df47` + `df6b28a`). Plan 1 covers §2.1–2.5, §3.1–3.2 (issues/comments/projects/aliases/events/meta only — links/labels/purge_log/issues_fts schema is laid down in 0001_init.sql but not exercised), §4 (subset: ping, health, projects, projects/resolve, issues, comments, actions/close, actions/reopen), §6.1–6.4 (init/create/show/list/edit/close/reopen/comment/whoami/health/projects), §9.

**Bootstrap state:** Module + tooling already committed (`0a8f83b` Bootstrap, `40e4955` Makefile lint-ci fix). `go.mod` declares all deps as `// indirect`; the first `go build` after each new import will promote them. When a task introduces a new dep, run `go get <pkg>@<version>` *before* writing the file that imports it so `go mod tidy` doesn't strip an unused indirect.

**Out of scope for Plan 1:** relationships (parent/blocks/related), labels, owners, search, idempotency, soft-delete/purge, SSE, polling, hooks, TUI, skills, doctor, agent-instructions. The schema is laid down in 0001_init.sql verbatim from spec §3.2 to avoid migration churn, but no code paths exercise the unused tables in this plan.

**Conventions for every task:**

- TDD: write the failing test first, run it to confirm it fails, implement, run to confirm pass, commit.
- Use `testify/require` for setup/preconditions and `testify/assert` for non-blocking checks; never `t.Fatal`/`t.Error` directly.
- Table-driven tests where multiple cases exist.
- `t.TempDir()` for any filesystem state. Never write to `~/.kata` from tests.
- Tests run with `-shuffle=on` (already set in `Makefile`); never pass `-count=1`; never pass `-v` unless asked.
- Commit messages: conventional (`feat:`, `fix:`, `chore:`, `test:`); subject ≤72 chars; one logical change per commit. Co-author trailer is not required for plan commits.
- Pre-commit hook (`prek`) will run `make lint`. Run `make lint` locally before committing if you've touched Go files.
- Never amend commits; always create new ones for fixes.
- Tests must hit `make test`. Don't run `go test -v` or `-count=1`.

---

### Task 1: `internal/config/paths.go` — `KATA_HOME`, `KATA_DB`, dbhash, runtime dir resolution

Spec refs: §2.3, §9.1. The CLI and daemon both call into this package; it owns env-var precedence and path derivation.

**Files:**
- Create: `internal/config/paths.go`
- Test: `internal/config/paths_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/config/paths_test.go
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestKataHome_PrefersEnvOverDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, tmp, got)
}

func TestKataHome_DefaultsToUserHomeDotKata(t *testing.T) {
	t.Setenv("KATA_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	got, err := config.KataHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".kata"), got)
}

func TestKataDB_PrefersEnvOverHomeJoin(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "custom.db"))

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmp, "custom.db"), got)
}

func TestKataDB_DefaultsToHomeKataDB(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", "")

	got, err := config.KataDB()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmp, "kata.db"), got)
}

func TestDBHash_StableTwelveLowerHex(t *testing.T) {
	a := config.DBHash("/Users/foo/.kata/kata.db")
	b := config.DBHash("/Users/foo/.kata/kata.db")
	c := config.DBHash("/Users/foo/.kata/other.db")

	assert.Len(t, a, 12)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, strings.ToLower(a), a)
}

func TestRuntimeDir_NamespaceIsDBHashUnderHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	got, err := config.RuntimeDir()
	require.NoError(t, err)
	hash := config.DBHash(filepath.Join(tmp, "kata.db"))
	assert.Equal(t, filepath.Join(tmp, "runtime", hash), got)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/config/...`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Implement `paths.go`**

```go
// internal/config/paths.go
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// KataHome returns the resolved data directory honoring $KATA_HOME, falling back
// to $HOME/.kata. The directory is not created here; callers materialize it.
func KataHome() (string, error) {
	if v := os.Getenv("KATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".kata"), nil
}

// KataDB returns the effective DB path honoring $KATA_DB, falling back to
// <KataHome>/kata.db. Returned path is not validated for existence.
func KataDB() (string, error) {
	if v := os.Getenv("KATA_DB"); v != "" {
		return v, nil
	}
	home, err := KataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "kata.db"), nil
}

// DBHash returns the first 12 lower-hex chars of sha256(absolute(dbPath)).
// Used to namespace runtime files, sockets, and hook output per database.
func DBHash(dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// RuntimeDir returns <KataHome>/runtime/<dbhash>. The directory is not created.
func RuntimeDir() (string, error) {
	home, err := KataHome()
	if err != nil {
		return "", err
	}
	db, err := KataDB()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runtime", DBHash(db)), nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/config/...`
Expected: PASS, all five tests green.

- [ ] **Step 5: Commit**

```bash
go mod tidy
make lint
git add internal/config/ go.mod go.sum
git commit -m "feat(config): add paths.go for KATA_HOME/KATA_DB/dbhash"
```

---

### Task 2: `internal/config/project_config.go` — parse and write `.kata.toml`

Spec refs: §6.3. v1 schema: `version=1; [project] identity=<str>, name=<str?>`.

**Files:**
- Create: `internal/config/project_config.go`
- Test: `internal/config/project_config_test.go`

- [ ] **Step 1: Add `BurntSushi/toml` dependency before importing it**

```bash
go get github.com/BurntSushi/toml@v1.6.0
```

- [ ] **Step 2: Write failing test**

```go
// internal/config/project_config_test.go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestReadProjectConfig_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kata.toml")
	require.NoError(t, os.WriteFile(path, []byte(`version = 1

[project]
identity = "github.com/wesm/kata"
name     = "kata"
`), 0o644))

	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, "github.com/wesm/kata", cfg.Project.Identity)
	assert.Equal(t, "kata", cfg.Project.Name)
}

func TestReadProjectConfig_Missing(t *testing.T) {
	cfg, err := config.ReadProjectConfig(t.TempDir())
	assert.Nil(t, cfg)
	assert.ErrorIs(t, err, config.ErrProjectConfigMissing)
}

func TestReadProjectConfig_RejectsBadVersion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"),
		[]byte(`version = 2

[project]
identity = "x"
name = "y"
`), 0o644))

	_, err := config.ReadProjectConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported .kata.toml version")
}

func TestReadProjectConfig_RejectsBlankIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"),
		[]byte(`version = 1

[project]
identity = "   "
name = "x"
`), 0o644))

	_, err := config.ReadProjectConfig(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project.identity")
}

func TestWriteProjectConfig_DerivesNameFromLastSegment(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.WriteProjectConfig(dir, "github.com/wesm/kata", ""))

	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "kata", cfg.Project.Name)
}

func TestWriteProjectConfig_PreservesExplicitName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.WriteProjectConfig(dir, "github.com/wesm/kata", "Kata Tracker"))

	cfg, err := config.ReadProjectConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "Kata Tracker", cfg.Project.Name)
}
```

- [ ] **Step 3: Run test (expect failure)**

Run: `go test ./internal/config/...`
Expected: FAIL — symbols don't exist.

- [ ] **Step 4: Implement `project_config.go`**

```go
// internal/config/project_config.go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrProjectConfigMissing is returned by ReadProjectConfig when the workspace
// has no .kata.toml at the given path.
var ErrProjectConfigMissing = errors.New(".kata.toml not found")

// ProjectConfigFilename is the canonical filename committed at workspace roots.
const ProjectConfigFilename = ".kata.toml"

// ProjectConfig is the parsed contents of a workspace .kata.toml.
type ProjectConfig struct {
	Version int             `toml:"version"`
	Project ProjectBindings `toml:"project"`
}

// ProjectBindings carries the [project] block.
type ProjectBindings struct {
	Identity string `toml:"identity"`
	Name     string `toml:"name,omitempty"`
}

// ReadProjectConfig parses <workspaceRoot>/.kata.toml and validates v1 fields.
// Returns (nil, ErrProjectConfigMissing) when the file does not exist; other
// I/O or validation errors are returned as-is.
func ReadProjectConfig(workspaceRoot string) (*ProjectConfig, error) {
	path := filepath.Join(workspaceRoot, ProjectConfigFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrProjectConfigMissing
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ProjectConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported .kata.toml version %d (expected 1)", cfg.Version)
	}
	if strings.TrimSpace(cfg.Project.Identity) == "" {
		return nil, fmt.Errorf("project.identity must be a non-empty string")
	}
	cfg.Project.Identity = strings.TrimSpace(cfg.Project.Identity)
	cfg.Project.Name = strings.TrimSpace(cfg.Project.Name)
	return &cfg, nil
}

// WriteProjectConfig writes a v1 .kata.toml at <workspaceRoot>/.kata.toml.
// If name is empty the last `/` or `:` segment of identity is used.
func WriteProjectConfig(workspaceRoot, identity, name string) error {
	if strings.TrimSpace(identity) == "" {
		return fmt.Errorf("identity must be non-empty")
	}
	if name == "" {
		name = lastSegment(identity)
	}
	body := fmt.Sprintf("version = 1\n\n[project]\nidentity = %q\nname     = %q\n",
		identity, name)
	path := filepath.Join(workspaceRoot, ProjectConfigFilename)
	return os.WriteFile(path, []byte(body), 0o644)
}

func lastSegment(identity string) string {
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' || identity[i] == ':' {
			return identity[i+1:]
		}
	}
	return identity
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./internal/config/...`
Expected: all six tests pass plus the four from Task 1.

- [ ] **Step 6: Commit**

```bash
go mod tidy
make lint
git add internal/config/ go.mod go.sum
git commit -m "feat(config): parse and write .kata.toml workspace binding"
```

---

### Task 3: `internal/config/project_identity.go` — path discovery + alias identity

Spec refs: §2.4 (path discovery, alias identity, identity validation). Walk upward from a `start_path` to find `W` (first `.kata.toml` ancestor) and `G` (first `.git` ancestor). Compute `alias_identity` from `G`'s git remote (normalized) or fall back to `local://`.

**Files:**
- Create: `internal/config/project_identity.go`
- Test: `internal/config/project_identity_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/config/project_identity_test.go
package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
)

func TestDiscoverPaths_FindsKataTomlAndGit(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".kata.toml"), []byte("version = 1\n\n[project]\nidentity = \"x\"\nname = \"x\"\n"), 0o644))
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, root, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_KataTomlInSubdirOfGit(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	sub := filepath.Join(root, "subproject")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".kata.toml"), []byte("version = 1\n\n[project]\nidentity = \"x\"\nname = \"x\"\n"), 0o644))

	d, err := config.DiscoverPaths(sub)
	require.NoError(t, err)
	assert.Equal(t, sub, d.WorkspaceRoot)
	assert.Equal(t, root, d.GitRoot)
}

func TestDiscoverPaths_NeitherFound(t *testing.T) {
	d, err := config.DiscoverPaths(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, d.WorkspaceRoot)
	assert.Empty(t, d.GitRoot)
}

func TestNormalizeRemoteURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"https://github.com/wesm/kata", "github.com/wesm/kata"},
		{"https://user:pass@github.com/wesm/kata.git", "github.com/wesm/kata"},
		{"git@github.com:wesm/kata.git", "github.com/wesm/kata"},
		{"ssh://git@gitlab.com/team/repo.git", "gitlab.com/team/repo"},
	}
	for _, tc := range cases {
		got, err := config.NormalizeRemoteURL(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func TestComputeAliasIdentity_GitWithRemote(t *testing.T) {
	dir := initGitRepo(t)
	requireGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", a.Identity)
	assert.Equal(t, "git", a.Kind)
	assert.Equal(t, dir, a.RootPath)
}

func TestComputeAliasIdentity_GitNoRemote(t *testing.T) {
	dir := initGitRepo(t)

	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{GitRoot: dir})
	require.NoError(t, err)
	assert.Equal(t, "local://"+dir, a.Identity)
	assert.Equal(t, "local", a.Kind)
}

func TestComputeAliasIdentity_NonGitWorkspace(t *testing.T) {
	ws := t.TempDir()
	a, err := config.ComputeAliasIdentity(config.DiscoveredPaths{WorkspaceRoot: ws})
	require.NoError(t, err)
	assert.Equal(t, "local://"+ws, a.Identity)
	assert.Equal(t, "local", a.Kind)
	assert.Equal(t, ws, a.RootPath)
}

func TestComputeAliasIdentity_Neither(t *testing.T) {
	_, err := config.ComputeAliasIdentity(config.DiscoveredPaths{})
	require.Error(t, err)
}

func TestValidateIdentity(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		hint string
	}{
		{"github.com/wesm/kata", true, ""},
		{"local:///abs/path", true, ""},
		{"a_b.c-d:foo/bar", true, ""},
		{"", false, "non-empty"},
		{"  spaces in middle  ", false, "whitespace"},
		{"has space", false, "whitespace"},
		{"https://u:p@host/x", false, "credential"},
	}
	for _, tc := range cases {
		err := config.ValidateIdentity(tc.in)
		if tc.ok {
			assert.NoError(t, err, tc.in)
		} else {
			require.Error(t, err, tc.in)
			assert.Contains(t, err.Error(), tc.hint, tc.in)
		}
	}
}

// helpers

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	requireGit(t, dir, "init", "--quiet")
	requireGit(t, dir, "config", "user.email", "x@example.com")
	requireGit(t, dir, "config", "user.name", "x")
	return dir
}

func requireGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/config/...`
Expected: FAIL — symbols don't exist.

- [ ] **Step 3: Implement `project_identity.go`**

```go
// internal/config/project_identity.go
package config

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// DiscoveredPaths is the result of walking upward from a start path.
// Both fields may be empty (no .kata.toml and no .git ancestor).
type DiscoveredPaths struct {
	WorkspaceRoot string // first ancestor with .kata.toml (inclusive)
	GitRoot       string // first ancestor with .git (inclusive)
}

// AliasInfo is the alias-identity record derived from a workspace.
type AliasInfo struct {
	Identity string // git remote (normalized) or "local://<abs path>"
	Kind     string // "git" | "local"
	RootPath string // GitRoot when present, else WorkspaceRoot
}

// DiscoverPaths walks upward from startPath looking for .kata.toml (W) and
// .git (G). Both lookups are independent and inclusive of startPath itself.
// A missing path returns ("", nil); resolution errors are returned.
func DiscoverPaths(startPath string) (DiscoveredPaths, error) {
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return DiscoveredPaths{}, fmt.Errorf("abs %s: %w", startPath, err)
	}
	d := DiscoveredPaths{}
	d.WorkspaceRoot = walkUp(abs, ProjectConfigFilename, false)
	d.GitRoot = walkUp(abs, ".git", true)
	return d, nil
}

// walkUp returns the first ancestor (inclusive) containing the named entry,
// or "" if none. allowDir lets the entry be either a file or directory.
func walkUp(start, entry string, allowDir bool) string {
	dir := start
	for {
		path := filepath.Join(dir, entry)
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				if allowDir {
					return dir
				}
			} else {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ComputeAliasIdentity derives the alias for a workspace per spec §2.4. Order:
// 1. GitRoot with remote → normalized origin URL
// 2. GitRoot without remote → local://<abs(GitRoot)>
// 3. WorkspaceRoot only → local://<abs(WorkspaceRoot)>
// 4. Neither → error
func ComputeAliasIdentity(d DiscoveredPaths) (AliasInfo, error) {
	if d.GitRoot != "" {
		remote, err := readGitRemote(d.GitRoot)
		if err != nil {
			return AliasInfo{}, err
		}
		if remote != "" {
			id, err := NormalizeRemoteURL(remote)
			if err != nil {
				return AliasInfo{}, err
			}
			return AliasInfo{Identity: id, Kind: "git", RootPath: d.GitRoot}, nil
		}
		return AliasInfo{
			Identity: "local://" + d.GitRoot,
			Kind:     "local",
			RootPath: d.GitRoot,
		}, nil
	}
	if d.WorkspaceRoot != "" {
		return AliasInfo{
			Identity: "local://" + d.WorkspaceRoot,
			Kind:     "local",
			RootPath: d.WorkspaceRoot,
		}, nil
	}
	return AliasInfo{}, fmt.Errorf("no workspace or git root discovered")
}

// readGitRemote returns the URL of "origin" (or the first remote listed by
// `git remote` when no origin exists). Returns ("", nil) if no remotes.
func readGitRemote(gitRoot string) (string, error) {
	out, err := runGit(gitRoot, "remote")
	if err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	remotes := strings.Fields(strings.TrimSpace(out))
	if len(remotes) == 0 {
		return "", nil
	}
	target := "origin"
	hasOrigin := false
	for _, r := range remotes {
		if r == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		target = remotes[0]
	}
	url, err := runGit(gitRoot, "remote", "get-url", target)
	if err != nil {
		return "", fmt.Errorf("git remote get-url %s: %w", target, err)
	}
	return strings.TrimSpace(url), nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// scpLikeRE matches "user@host:path[/...]" SCP-style git URLs.
var scpLikeRE = regexp.MustCompile(`^([^@\s]+)@([^:\s]+):(.+)$`)

// NormalizeRemoteURL strips credentials, normalizes SSH↔HTTPS, drops trailing
// .git, and returns "host/path" form (e.g. "github.com/wesm/kata").
func NormalizeRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty remote url")
	}
	if m := scpLikeRE.FindStringSubmatch(raw); m != nil {
		host := m[2]
		path := strings.TrimSuffix(m[3], ".git")
		return host + "/" + path, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("parse remote url %q: not a recognized form", raw)
	}
	host := u.Host
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if path == "" {
		return host, nil
	}
	return host + "/" + path, nil
}

var identityCharsetRE = regexp.MustCompile(`^[A-Za-z0-9._:/\-]+$`)

// ValidateIdentity enforces the spec §2.4 charset and forbids whitespace and
// embedded URL credentials.
func ValidateIdentity(id string) error {
	if id == "" {
		return fmt.Errorf("identity must be non-empty")
	}
	for _, r := range id {
		if unicode.IsSpace(r) {
			return fmt.Errorf("identity contains whitespace: %q", id)
		}
	}
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		// reject embedded credentials.
		if strings.Contains(id, "@") {
			return fmt.Errorf("identity must not embed credentials: %q", id)
		}
	}
	if !identityCharsetRE.MatchString(stripLocalScheme(id)) {
		return fmt.Errorf("identity contains disallowed characters: %q", id)
	}
	return nil
}

// stripLocalScheme allows local://<abs path> identities through the charset
// check by ignoring the scheme prefix and validating the remainder.
func stripLocalScheme(id string) string {
	const prefix = "local://"
	if strings.HasPrefix(id, prefix) {
		return strings.ReplaceAll(id[len(prefix):], "/", "")
	}
	return id
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/config/...`
Expected: all tests in `internal/config/` pass.

- [ ] **Step 5: Commit**

```bash
go mod tidy
make lint
git add internal/config/ go.mod go.sum
git commit -m "feat(config): discover paths + compute alias identity"
```

---

### Task 4: `internal/db/migrations/0001_init.sql` + `internal/db/db.go`

Spec refs: §3.1 (PRAGMAs), §3.2 (full schema), §9.2 (`internal/db/`). Embed all migration files via `//go:embed migrations/*.sql`. Run them in lex order at `Open` time. Schema timestamp columns use `DATETIME` so the driver scans into `time.Time`.

**Files:**
- Create: `internal/db/migrations/0001_init.sql`
- Create: `internal/db/db.go`
- Test: `internal/db/db_test.go`

- [ ] **Step 1: Add `modernc.org/sqlite` dependency**

```bash
go get modernc.org/sqlite@v1.49.1
```

- [ ] **Step 2: Write the migration SQL verbatim from spec §3.2**

Create `internal/db/migrations/0001_init.sql` with the exact SQL from spec §3.2 (projects, project_aliases, issues, comments, links, links triggers, issue_labels, events, purge_log, meta inserts, issues_fts virtual table). Do not omit any table — Plan 1 lays the full schema even though only projects/aliases/issues/comments/events/meta are exercised here.

- [ ] **Step 3: Write failing test**

```go
// internal/db/db_test.go
package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestOpen_AppliesPragmasAndMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	var fk int
	require.NoError(t, d.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)

	var mode string
	require.NoError(t, d.QueryRow("PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode)

	var version string
	require.NoError(t, d.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version))
	assert.Equal(t, "1", version)
}

func TestOpen_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.db")
	d1, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, d1.Close())

	d2, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d2.Close() })

	var version string
	require.NoError(t, d2.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version))
	assert.Equal(t, "1", version)
}

func TestOpen_TimestampColumnsScanIntoTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kata.db")
	d, err := db.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	_, err = d.Exec(`INSERT INTO projects(identity, name) VALUES('x','x')`)
	require.NoError(t, err)

	rows, err := d.Query(`SELECT created_at FROM projects`)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var ts interface{}
	require.NoError(t, rows.Scan(&ts))
	// modernc.org/sqlite returns time.Time for DATETIME columns
	_, ok := ts.(interface{ Year() int })
	assert.True(t, ok, "expected time.Time, got %T", ts)
}
```

- [ ] **Step 4: Run test (expect failure)**

Run: `go test ./internal/db/...`
Expected: FAIL — package does not exist.

- [ ] **Step 5: Implement `db.go`**

```go
// internal/db/db.go
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps *sql.DB. Use Open to construct one with PRAGMAs applied.
type DB struct {
	*sql.DB
	path string
}

// Open opens (and if needed initializes) the kata SQLite database at path.
// PRAGMAs are applied for every connection (via the connection string and
// post-open exec) and pending migrations are run inside a transaction.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)",
		path,
	)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Single writer is fine for v1; SetMaxOpenConns left at default for reads.
	if err := sdb.PingContext(ctx); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	d := &DB{DB: sdb, path: path}
	if err := d.migrate(ctx); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return d, nil
}

// Path returns the resolved database path.
func (d *DB) Path() string { return d.path }

func (d *DB) migrate(ctx context.Context) error {
	current, err := d.currentVersion(ctx)
	if err != nil {
		return err
	}
	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embed: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		ver, err := parseMigrationVersion(f.Name())
		if err != nil {
			return err
		}
		if ver <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name(), err)
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key,value) VALUES('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(ver)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record version %d: %w", ver, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", f.Name(), err)
		}
	}
	return nil
}

// currentVersion returns 0 when the meta table doesn't exist yet (fresh DB).
func (d *DB) currentVersion(ctx context.Context) (int, error) {
	exists, err := d.tableExists(ctx, "meta")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var v string
	err = d.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", v, err)
	}
	return n, nil
}

func (d *DB) tableExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// parseMigrationVersion extracts the leading integer from filenames like
// "0001_init.sql" → 1.
func parseMigrationVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid migration filename: %s", name)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("parse version in %s: %w", name, err)
	}
	return n, nil
}
```

- [ ] **Step 6: Run test (expect pass)**

Run: `go test ./internal/db/...`
Expected: all three tests pass.

- [ ] **Step 7: Commit**

```bash
go mod tidy
make lint
git add internal/db/ go.mod go.sum
git commit -m "feat(db): open SQLite with PRAGMAs and run embedded migrations"
```

---

### Task 5: `internal/db/types.go` — model types

Spec refs: §9.2. Plan 1 needs Project, ProjectAlias, Issue, Comment, Event types. Other types (Link, Label, PurgeLog) are stubbed with their column shapes for later plans, but can be skipped if not exercised here. Keep this minimal: declare only what Plan 1 reads/writes.

**Files:**
- Create: `internal/db/types.go`

- [ ] **Step 1: Implement (no test — pure type declarations exercised by later tasks)**

```go
// internal/db/types.go
package db

import "time"

// Project mirrors a row in projects.
type Project struct {
	ID              int64     `json:"id"`
	Identity        string    `json:"identity"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"created_at"`
	NextIssueNumber int64     `json:"next_issue_number"`
}

// ProjectAlias mirrors a row in project_aliases.
type ProjectAlias struct {
	ID             int64     `json:"id"`
	ProjectID      int64     `json:"project_id"`
	AliasIdentity  string    `json:"alias_identity"`
	AliasKind      string    `json:"alias_kind"`
	RootPath       string    `json:"root_path"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

// Issue mirrors a row in issues.
type Issue struct {
	ID           int64      `json:"id"`
	ProjectID    int64      `json:"project_id"`
	Number       int64      `json:"number"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	Status       string     `json:"status"`
	ClosedReason *string    `json:"closed_reason,omitempty"`
	Owner        *string    `json:"owner,omitempty"`
	Author       string     `json:"author"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// Comment mirrors a row in comments.
type Comment struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Event mirrors a row in events.
type Event struct {
	ID              int64     `json:"id"`
	ProjectID       int64     `json:"project_id"`
	ProjectIdentity string    `json:"project_identity"`
	IssueID         *int64    `json:"issue_id,omitempty"`
	IssueNumber     *int64    `json:"issue_number,omitempty"`
	RelatedIssueID  *int64    `json:"related_issue_id,omitempty"`
	Type            string    `json:"type"`
	Actor           string    `json:"actor"`
	Payload         string    `json:"payload"`
	CreatedAt       time.Time `json:"created_at"`
}
```

- [ ] **Step 2: Verify compile**

Run: `go build ./...`
Expected: compiles cleanly.

- [ ] **Step 3: Commit**

```bash
git add internal/db/types.go
git commit -m "feat(db): declare model types"
```

---

### Task 6: `internal/db/queries.go` — projects + aliases CRUD

Spec refs: §2.4, §3.2. The handlers in §4.2 need: create project; lookup by identity; lookup by id; list; attach alias to project; update last_seen on alias; lookup alias by identity. Use row-by-row testify-style tests; isolate via `t.TempDir()` per test.

**Files:**
- Create: `internal/db/queries.go`
- Test: `internal/db/queries_projects_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_projects_test.go
package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestCreateProject_RoundTrips(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)
	assert.Equal(t, "github.com/wesm/kata", p.Identity)
	assert.Equal(t, "kata", p.Name)
	assert.Equal(t, int64(1), p.NextIssueNumber)
	assert.False(t, p.CreatedAt.IsZero())

	got, err := d.ProjectByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
}

func TestProjectByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.ProjectByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestCreateProject_DuplicateIdentity(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	_, err = d.CreateProject(ctx, "x", "x")
	require.Error(t, err)
}

func TestAttachAlias_AndLookup(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "github.com/wesm/kata", "kata")
	require.NoError(t, err)

	a, err := d.AttachAlias(ctx, p.ID, "github.com/wesm/kata", "git", "/tmp/x")
	require.NoError(t, err)
	assert.Equal(t, p.ID, a.ProjectID)
	assert.Equal(t, "git", a.AliasKind)

	got, err := d.AliasByIdentity(ctx, "github.com/wesm/kata")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
}

func TestAliasByIdentity_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.AliasByIdentity(context.Background(), "missing")
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestTouchAlias_UpdatesLastSeen(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, "x", "x")
	require.NoError(t, err)
	a, err := d.AttachAlias(ctx, p.ID, "x", "git", "/tmp/x")
	require.NoError(t, err)

	require.NoError(t, d.TouchAlias(ctx, a.ID, "/tmp/y"))
	got, err := d.AliasByIdentity(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/y", got.RootPath)
	assert.True(t, !got.LastSeenAt.Before(a.LastSeenAt))
}

func TestListProjects_Empty(t *testing.T) {
	d := openTestDB(t)
	got, err := d.ListProjects(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListProjects_OrdersByIDAsc(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, _ = d.CreateProject(ctx, "a", "a")
	_, _ = d.CreateProject(ctx, "b", "b")

	got, err := d.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Identity)
	assert.Equal(t, "b", got[1].Identity)
}

func TestProjectAliases_ReturnsAllForProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-a", "git", "/tmp/a")
	_, _ = d.AttachAlias(ctx, p.ID, "alias-b", "git", "/tmp/b")

	got, err := d.ProjectAliases(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/db/...`
Expected: FAIL — symbols missing.

- [ ] **Step 3: Implement queries**

```go
// internal/db/queries.go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a single-row lookup matches zero rows.
var ErrNotFound = errors.New("not found")

// CreateProject inserts a new projects row with default next_issue_number=1.
func (d *DB) CreateProject(ctx context.Context, identity, name string) (Project, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO projects(identity, name) VALUES(?, ?)`, identity, name)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("last id: %w", err)
	}
	return d.ProjectByID(ctx, id)
}

// ProjectByID fetches one project by its rowid.
func (d *DB) ProjectByID(ctx context.Context, id int64) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, id)
	return scanProject(row)
}

// ProjectByIdentity fetches one project by its UNIQUE identity.
func (d *DB) ProjectByIdentity(ctx context.Context, identity string) (Project, error) {
	row := d.QueryRowContext(ctx, projectSelect+` WHERE identity = ?`, identity)
	return scanProject(row)
}

// ListProjects returns every project ordered by id ASC.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.QueryContext(ctx, projectSelect+` ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AttachAlias inserts a project_aliases row.
func (d *DB) AttachAlias(ctx context.Context, projectID int64, identity, kind, rootPath string) (ProjectAlias, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO project_aliases(project_id, alias_identity, alias_kind, root_path)
		 VALUES(?, ?, ?, ?)`, projectID, identity, kind, rootPath)
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("insert alias: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ProjectAlias{}, err
	}
	return d.aliasByID(ctx, id)
}

// AliasByIdentity returns the alias for a given alias_identity.
func (d *DB) AliasByIdentity(ctx context.Context, identity string) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE alias_identity = ?`, identity)
	return scanAlias(row)
}

func (d *DB) aliasByID(ctx context.Context, id int64) (ProjectAlias, error) {
	row := d.QueryRowContext(ctx, aliasSelect+` WHERE id = ?`, id)
	return scanAlias(row)
}

// TouchAlias updates last_seen_at to now and rewrites root_path.
func (d *DB) TouchAlias(ctx context.Context, aliasID int64, rootPath string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE project_aliases
		 SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     root_path    = ?
		 WHERE id = ?`, rootPath, aliasID)
	if err != nil {
		return fmt.Errorf("touch alias: %w", err)
	}
	return nil
}

// ProjectAliases returns every alias attached to a project ordered by id ASC.
func (d *DB) ProjectAliases(ctx context.Context, projectID int64) ([]ProjectAlias, error) {
	rows, err := d.QueryContext(ctx, aliasSelect+` WHERE project_id = ? ORDER BY id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	var out []ProjectAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const projectSelect = `SELECT id, identity, name, created_at, next_issue_number FROM projects`

type rowScanner interface {
	Scan(...any) error
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.Identity, &p.Name, &p.CreatedAt, &p.NextIssueNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("scan project: %w", err)
	}
	return p, nil
}

const aliasSelect = `SELECT id, project_id, alias_identity, alias_kind, root_path, created_at, last_seen_at FROM project_aliases`

func scanAlias(r rowScanner) (ProjectAlias, error) {
	var a ProjectAlias
	err := r.Scan(&a.ID, &a.ProjectID, &a.AliasIdentity, &a.AliasKind, &a.RootPath, &a.CreatedAt, &a.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAlias{}, ErrNotFound
	}
	if err != nil {
		return ProjectAlias{}, fmt.Errorf("scan alias: %w", err)
	}
	return a, nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/db/...`
Expected: all tests pass (DB tests + project/alias tests).

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries.go internal/db/queries_projects_test.go
git commit -m "feat(db): project + alias CRUD with last_seen tracking"
```

---

### Task 7: `internal/db/queries.go` — issues, comments, events, close/reopen

Spec refs: §3.2 (issues, comments, events), §3.4 (lifecycle). Plan 1 needs: CreateIssue (atomically allocates `number` from `projects.next_issue_number`, appends `issue.created` event), GetIssueByNumber, ListIssuesByProject, CreateComment (+event), CloseIssue (+event), ReopenIssue (+event), EventsByIssue (helpful for `kata show --include-events` later, but Plan 1 leaves it unused).

**Files:**
- Modify: `internal/db/queries.go` (append; don't restructure existing project/alias funcs)
- Test: `internal/db/queries_issues_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/db/queries_issues_test.go
package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

func TestCreateIssue_AllocatesNumberAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")

	issue, evt, err := d.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: p.ID,
		Title:     "first",
		Body:      "details",
		Author:    "agent-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), issue.Number)
	assert.Equal(t, "open", issue.Status)
	assert.Equal(t, "agent-1", issue.Author)
	assert.Equal(t, "issue.created", evt.Type)
	assert.NotNil(t, evt.IssueID)
	require.NotNil(t, evt.IssueNumber)
	assert.Equal(t, int64(1), *evt.IssueNumber)

	p2, err := d.ProjectByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), p2.NextIssueNumber)
}

func TestCreateIssue_NumbersAreSequentialPerProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")

	for i := 1; i <= 3; i++ {
		issue, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: "x", Author: "a",
		})
		require.NoError(t, err)
		assert.EqualValues(t, i, issue.Number)
	}
}

func TestGetIssueByNumber_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	_, err := d.IssueByNumber(ctx, p.ID, 99)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestListIssues_DefaultsToOpenOnlyAndExcludesDeleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	for _, title := range []string{"a", "b", "c"} {
		_, _, err := d.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: p.ID, Title: title, Author: "x",
		})
		require.NoError(t, err)
	}

	got, err := d.ListIssues(ctx, db.ListIssuesParams{ProjectID: p.ID, Status: "open"})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestCreateComment_EmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

	cmt, evt, err := d.CreateComment(ctx, db.CreateCommentParams{
		IssueID: issue.ID, Author: "agent", Body: "hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "hi", cmt.Body)
	assert.Equal(t, "issue.commented", evt.Type)
}

func TestCloseIssue_SetsStatusAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

	updated, evt, changed, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "closed", updated.Status)
	require.NotNil(t, updated.ClosedReason)
	assert.Equal(t, "done", *updated.ClosedReason)
	assert.NotNil(t, updated.ClosedAt)
	assert.Equal(t, "issue.closed", evt.Type)
}

func TestCloseIssue_OnAlreadyClosedIsNoOp(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})
	_, _, _, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)

	_, evt, changed, err := d.CloseIssue(ctx, issue.ID, "done", "agent")
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, evt)
}

func TestReopenIssue_ClearsStatusAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})
	_, _, _, _ = d.CloseIssue(ctx, issue.ID, "done", "agent")

	updated, evt, changed, err := d.ReopenIssue(ctx, issue.ID, "agent")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "open", updated.Status)
	assert.Nil(t, updated.ClosedAt)
	assert.Nil(t, updated.ClosedReason)
	assert.Equal(t, "issue.reopened", evt.Type)
}

func TestEditIssue_SetsFieldsAndEmitsEvent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "old", Body: "ob", Author: "x"})

	newTitle := "new"
	updated, evt, changed, err := d.EditIssue(ctx, db.EditIssueParams{
		IssueID: issue.ID, Title: &newTitle, Actor: "agent",
	})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "new", updated.Title)
	require.NotNil(t, evt)
	assert.Equal(t, "issue.updated", evt.Type)
}

func TestEditIssue_NoFieldsIsValidationError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, "p", "p")
	issue, _, _ := d.CreateIssue(ctx, db.CreateIssueParams{ProjectID: p.ID, Title: "x", Author: "x"})

	_, _, _, err := d.EditIssue(ctx, db.EditIssueParams{IssueID: issue.ID, Actor: "agent"})
	assert.ErrorIs(t, err, db.ErrNoFields)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/db/...`
Expected: FAIL — symbols missing.

- [ ] **Step 3: Implement issues/comments/events/close/reopen/edit**

```go
// append to internal/db/queries.go

// ErrNoFields is returned by EditIssue when no field is set.
var ErrNoFields = errors.New("no fields to update")

// CreateIssueParams carries inputs for CreateIssue.
type CreateIssueParams struct {
	ProjectID int64
	Title     string
	Body      string
	Author    string
}

// CreateIssue inserts an issue, allocates the next number atomically, and
// appends an issue.created event in the same transaction.
func (d *DB) CreateIssue(ctx context.Context, p CreateIssueParams) (Issue, Event, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Atomic number allocation: bump next_issue_number first and capture the
	// pre-bump value via RETURNING. Concurrent CreateIssue calls serialize on
	// this UPDATE, so each one gets a distinct number.
	var (
		identity string
		nextNum  int64
	)
	if err := tx.QueryRowContext(ctx,
		`UPDATE projects
		 SET next_issue_number = next_issue_number + 1
		 WHERE id = ?
		 RETURNING next_issue_number - 1, identity`, p.ProjectID).
		Scan(&nextNum, &identity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, Event{}, ErrNotFound
		}
		return Issue{}, Event{}, fmt.Errorf("allocate issue number: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO issues(project_id, number, title, body, author)
		 VALUES(?, ?, ?, ?, ?)`,
		p.ProjectID, nextNum, p.Title, p.Body, p.Author)
	if err != nil {
		return Issue{}, Event{}, fmt.Errorf("insert issue: %w", err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return Issue{}, Event{}, err
	}

	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       p.ProjectID,
		ProjectIdentity: identity,
		IssueID:         &issueID,
		IssueNumber:     &nextNum,
		Type:            "issue.created",
		Actor:           p.Author,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Issue{}, Event{}, fmt.Errorf("commit: %w", err)
	}

	issue, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, Event{}, err
	}
	return issue, evt, nil
}

// IssueByID fetches an issue by rowid.
func (d *DB) IssueByID(ctx context.Context, id int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE id = ?`, id)
	return scanIssue(row)
}

// IssueByNumber fetches an issue by (project_id, number).
func (d *DB) IssueByNumber(ctx context.Context, projectID, number int64) (Issue, error) {
	row := d.QueryRowContext(ctx, issueSelect+` WHERE project_id = ? AND number = ?`, projectID, number)
	return scanIssue(row)
}

// ListIssuesParams filters list output. Status="" → all. Empty struct → all.
type ListIssuesParams struct {
	ProjectID int64
	Status    string // "open" | "closed" | "" (any)
	Limit     int    // 0 = no limit
}

// ListIssues returns issues in the given project, excluding soft-deleted rows.
func (d *DB) ListIssues(ctx context.Context, p ListIssuesParams) ([]Issue, error) {
	q := issueSelect + ` WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{p.ProjectID}
	if p.Status != "" {
		q += ` AND status = ?`
		args = append(args, p.Status)
	}
	q += ` ORDER BY updated_at DESC, id DESC`
	if p.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, p.Limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CreateCommentParams carries inputs for CreateComment.
type CreateCommentParams struct {
	IssueID int64
	Author  string
	Body    string
}

// CreateComment appends a comment + issue.commented event in one tx, bumping
// issues.updated_at.
func (d *DB) CreateComment(ctx context.Context, p CreateCommentParams) (Comment, Event, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, Event{}, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Comment{}, Event{}, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO comments(issue_id, author, body) VALUES(?, ?, ?)`,
		p.IssueID, p.Author, p.Body)
	if err != nil {
		return Comment{}, Event{}, fmt.Errorf("insert comment: %w", err)
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		return Comment{}, Event{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		p.IssueID); err != nil {
		return Comment{}, Event{}, fmt.Errorf("touch issue: %w", err)
	}

	payload := fmt.Sprintf(`{"comment_id":%d}`, commentID)
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.commented",
		Actor:           p.Author,
		Payload:         payload,
	})
	if err != nil {
		return Comment{}, Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Comment{}, Event{}, err
	}

	var c Comment
	if err := d.QueryRowContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE id = ?`,
		commentID).Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return Comment{}, Event{}, fmt.Errorf("read comment: %w", err)
	}
	return c, evt, nil
}

// CloseIssue sets status=closed unless already closed.
func (d *DB) CloseIssue(ctx context.Context, issueID int64, reason, actor string) (Issue, *Event, bool, error) {
	if reason == "" {
		reason = "done"
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "closed" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'closed',
		     closed_reason = ?,
		     closed_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, reason, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("close: %w", err)
	}
	payload := fmt.Sprintf(`{"reason":%q}`, reason)
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.closed",
		Actor:           actor,
		Payload:         payload,
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// ReopenIssue clears status=closed unless already open.
func (d *DB) ReopenIssue(ctx context.Context, issueID int64, actor string) (Issue, *Event, bool, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	if issue.Status == "open" {
		return issue, nil, false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issues
		 SET status        = 'open',
		     closed_reason = NULL,
		     closed_at     = NULL,
		     updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ?`, issueID); err != nil {
		return Issue{}, nil, false, fmt.Errorf("reopen: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.reopened",
		Actor:           actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, issueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

// EditIssueParams carries the optional fields for an edit. Nil = leave alone.
type EditIssueParams struct {
	IssueID int64
	Title   *string
	Body    *string
	Owner   *string
	Actor   string
}

// EditIssue mutates title/body/owner. ErrNoFields if none are set.
func (d *DB) EditIssue(ctx context.Context, p EditIssueParams) (Issue, *Event, bool, error) {
	if p.Title == nil && p.Body == nil && p.Owner == nil {
		return Issue{}, nil, false, ErrNoFields
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	issue, projectIdentity, err := lookupIssueForEvent(ctx, tx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}

	sets := []string{`updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`}
	args := []any{}
	if p.Title != nil {
		sets = append(sets, `title = ?`)
		args = append(args, *p.Title)
	}
	if p.Body != nil {
		sets = append(sets, `body = ?`)
		args = append(args, *p.Body)
	}
	if p.Owner != nil {
		sets = append(sets, `owner = ?`)
		args = append(args, *p.Owner)
	}
	args = append(args, p.IssueID)
	q := fmt.Sprintf(`UPDATE issues SET %s WHERE id = ?`, joinComma(sets))
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return Issue{}, nil, false, fmt.Errorf("update issue: %w", err)
	}
	evt, err := insertEventTx(ctx, tx, eventInsert{
		ProjectID:       issue.ProjectID,
		ProjectIdentity: projectIdentity,
		IssueID:         &issue.ID,
		IssueNumber:     &issue.Number,
		Type:            "issue.updated",
		Actor:           p.Actor,
		Payload:         "{}",
	})
	if err != nil {
		return Issue{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, nil, false, err
	}
	updated, err := d.IssueByID(ctx, p.IssueID)
	if err != nil {
		return Issue{}, nil, false, err
	}
	return updated, &evt, true, nil
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// lookupIssueForEvent fetches the issue + its project's identity for event
// snapshotting. Used inside transactions. Soft-deleted issues are excluded so
// lifecycle mutations (close/reopen/edit/comment) cannot operate on hidden
// rows; callers see ErrNotFound for both nonexistent and deleted issues.
func lookupIssueForEvent(ctx context.Context, tx *sql.Tx, issueID int64) (Issue, string, error) {
	const q = `
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at, p.identity
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = ? AND i.deleted_at IS NULL`
	var i Issue
	var identity string
	err := tx.QueryRowContext(ctx, q, issueID).
		Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", ErrNotFound
	}
	if err != nil {
		return Issue{}, "", fmt.Errorf("lookup issue: %w", err)
	}
	return i, identity, nil
}

const issueSelect = `SELECT i.id, i.project_id, i.number, i.title, i.body, i.status, i.closed_reason, i.owner, i.author, i.created_at, i.updated_at, i.closed_at, i.deleted_at FROM issues i`

func scanIssue(r rowScanner) (Issue, error) {
	var i Issue
	err := r.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status, &i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("scan issue: %w", err)
	}
	return i, nil
}

// eventInsert is the tx-internal payload used by insertEventTx.
type eventInsert struct {
	ProjectID       int64
	ProjectIdentity string
	IssueID         *int64
	IssueNumber     *int64
	RelatedIssueID  *int64
	Type            string
	Actor           string
	Payload         string
}

func insertEventTx(ctx context.Context, tx *sql.Tx, in eventInsert) (Event, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events(project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.ProjectIdentity, in.IssueID, in.IssueNumber, in.RelatedIssueID, in.Type, in.Actor, in.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	var e Event
	err = tx.QueryRowContext(ctx,
		`SELECT id, project_id, project_identity, issue_id, issue_number, related_issue_id, type, actor, payload, created_at FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.ProjectID, &e.ProjectIdentity, &e.IssueID, &e.IssueNumber, &e.RelatedIssueID, &e.Type, &e.Actor, &e.Payload, &e.CreatedAt)
	if err != nil {
		return Event{}, fmt.Errorf("read event: %w", err)
	}
	return e, nil
}
```

Note: `CreateIssue` allocates the issue number atomically via `UPDATE projects ... RETURNING next_issue_number - 1, identity`. Concurrent calls serialize on that UPDATE so each one gets a unique number even though `BeginTx` starts a deferred transaction. There is no separate "bump" UPDATE — the allocation and bump happen in one statement.

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/db/...`
Expected: all DB tests pass.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/db/queries.go internal/db/queries_issues_test.go
git commit -m "feat(db): issue/comment lifecycle with event emission"
```

---

### Task 8: `internal/daemon/namespace.go` — runtime + socket dirs per dbhash

Spec refs: §2.3, §9.1. Resolve `<KataHome>/runtime/<dbhash>/` (data dir, contains `daemon.<pid>.json` + `daemon.log`) and `<XDG_RUNTIME_DIR or fallback>/kata/<dbhash>/` (socket location).

**Files:**
- Create: `internal/daemon/namespace.go`
- Test: `internal/daemon/namespace_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/namespace_test.go
package daemon_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
)

func TestNamespace_DataDirIsKataHomeRuntime(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	hash := config.DBHash(filepath.Join(tmp, "kata.db"))
	assert.Equal(t, filepath.Join(tmp, "runtime", hash), ns.DataDir)
	assert.Equal(t, hash, ns.DBHash)
}

func TestNamespace_SocketDirHonorsXDGRuntimeDir(t *testing.T) {
	tmp := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "kata", ns.DBHash), ns.SocketDir)
}

func TestNamespace_SocketDirFallsBackToTmpDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", "/var/folders/xy")

	ns, err := daemon.NewNamespace()
	require.NoError(t, err)
	assert.Contains(t, ns.SocketDir, "kata-")
	assert.Contains(t, ns.SocketDir, ns.DBHash)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/daemon/namespace.go
package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wesm/kata/internal/config"
)

// Namespace bundles per-dbhash directories used by daemon runtime files and
// (on Unix) the listening socket.
type Namespace struct {
	DBHash    string // 12-char dbhash
	DataDir   string // <KataHome>/runtime/<dbhash>
	SocketDir string // <XDG_RUNTIME_DIR>/kata/<dbhash> or fallback
}

// NewNamespace resolves directories from $KATA_HOME / $KATA_DB / $XDG_RUNTIME_DIR / $TMPDIR.
// Directories are not created — call EnsureDirs at startup.
func NewNamespace() (*Namespace, error) {
	dbPath, err := config.KataDB()
	if err != nil {
		return nil, fmt.Errorf("resolve KATA_DB: %w", err)
	}
	dataRoot, err := config.RuntimeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime dir: %w", err)
	}
	hash := config.DBHash(dbPath)

	socketDir := socketParent(hash)

	return &Namespace{
		DBHash:    hash,
		DataDir:   dataRoot,
		SocketDir: socketDir,
	}, nil
}

func socketParent(dbhash string) string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return filepath.Join(v, "kata", dbhash)
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	return filepath.Join(tmp, fmt.Sprintf("kata-%d", os.Getuid()), dbhash)
}

// EnsureDirs materializes DataDir (0700) and SocketDir (0700).
func (n *Namespace) EnsureDirs() error {
	if err := os.MkdirAll(n.DataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}
	if err := os.MkdirAll(n.SocketDir, 0o700); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/ go.mod go.sum
git commit -m "feat(daemon): resolve per-dbhash data and socket dirs"
```

---

### Task 9: `internal/daemon/runtime.go` — `daemon.<pid>.json` lifecycle

Spec refs: §2.3. Atomic write/rename of `daemon.<pid>.json` containing `{pid, addr, started_at, db_path}`. List + clean stale files (where the PID is dead). Reading walks `runtime/<dbhash>/daemon.*.json`.

**Files:**
- Create: `internal/daemon/runtime.go`
- Test: `internal/daemon/runtime_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/runtime_test.go
package daemon_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestRuntimeFile_RoundTripWriteRead(t *testing.T) {
	dir := t.TempDir()
	rec := daemon.RuntimeRecord{
		PID:       4242,
		Address:   "unix:///tmp/kata.sock",
		DBPath:    "/tmp/kata.db",
		StartedAt: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
	}
	path, err := daemon.WriteRuntimeFile(dir, rec)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "daemon.4242.json"), path)

	got, err := daemon.ReadRuntimeFile(path)
	require.NoError(t, err)
	assert.Equal(t, rec.PID, got.PID)
	assert.Equal(t, rec.Address, got.Address)
}

func TestListRuntimeFiles_FindsAllInDir(t *testing.T) {
	dir := t.TempDir()
	for _, pid := range []int{1, 2, 3} {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "daemon."+strconv.Itoa(pid)+".json"),
			[]byte(`{"pid":`+strconv.Itoa(pid)+`,"address":"x","db_path":"x","started_at":"2026-01-01T00:00:00Z"}`), 0o644))
	}

	got, err := daemon.ListRuntimeFiles(dir)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestRuntimeFile_AtomicViaTempRename(t *testing.T) {
	// Two concurrent writes shouldn't produce a half-written file.
	// We assert by writing once and then reading — the value must match.
	dir := t.TempDir()
	rec := daemon.RuntimeRecord{PID: 7, Address: "x", DBPath: "x", StartedAt: time.Now().UTC()}
	_, err := daemon.WriteRuntimeFile(dir, rec)
	require.NoError(t, err)
	got, err := daemon.ReadRuntimeFile(filepath.Join(dir, "daemon.7.json"))
	require.NoError(t, err)
	assert.Equal(t, rec.PID, got.PID)
}

func TestProcessAlive_TrueForSelfFalseForGarbagePID(t *testing.T) {
	assert.True(t, daemon.ProcessAlive(os.Getpid()))
	assert.False(t, daemon.ProcessAlive(99999999))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/runtime.go
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RuntimeRecord is the on-disk shape of daemon.<pid>.json.
type RuntimeRecord struct {
	PID       int       `json:"pid"`
	Address   string    `json:"address"`     // unix:///path or 127.0.0.1:7474
	DBPath    string    `json:"db_path"`
	StartedAt time.Time `json:"started_at"`
}

// WriteRuntimeFile writes <dir>/daemon.<pid>.json atomically (write to .tmp,
// fsync-ish, rename). Returns the resolved file path.
func WriteRuntimeFile(dir string, rec RuntimeRecord) (string, error) {
	if rec.PID <= 0 {
		return "", fmt.Errorf("pid must be > 0")
	}
	final := filepath.Join(dir, fmt.Sprintf("daemon.%d.json", rec.PID))
	tmp := final + ".tmp"
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	return final, nil
}

// ReadRuntimeFile parses one file.
func ReadRuntimeFile(path string) (RuntimeRecord, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return RuntimeRecord{}, fmt.Errorf("read %s: %w", path, err)
	}
	var rec RuntimeRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return RuntimeRecord{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return rec, nil
}

// ListRuntimeFiles returns RuntimeRecords for each daemon.*.json in dir.
// Garbage / parse-failed files are skipped silently.
func ListRuntimeFiles(dir string) ([]RuntimeRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var out []RuntimeRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "daemon.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		// must parse the pid out of the filename to filter .tmp etc.
		mid := strings.TrimSuffix(strings.TrimPrefix(name, "daemon."), ".json")
		if _, err := strconv.Atoi(mid); err != nil {
			continue
		}
		rec, err := ReadRuntimeFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// ProcessAlive returns true if a kill(0, pid) succeeds. Best-effort signal
// probe; doesn't distinguish "not ours" vs "alive".
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// CleanupStaleFiles removes any daemon.<pid>.json whose PID is dead.
func CleanupStaleFiles(dir string) error {
	recs, err := ListRuntimeFiles(dir)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !ProcessAlive(r.PID) {
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("daemon.%d.json", r.PID)))
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/runtime.go internal/daemon/runtime_test.go
git commit -m "feat(daemon): runtime file write/read/list/cleanup"
```

---

### Task 10: `internal/daemon/endpoint.go` — `DaemonEndpoint` (Unix socket / TCP loopback)

Spec refs: §2.2. `DaemonEndpoint.Listen()` returns a `net.Listener`. `DaemonEndpoint.Dial(ctx)` returns a connected `net.Conn`. The `Address()` string serializes as `unix:///path` or `127.0.0.1:7474`.

**Files:**
- Create: `internal/daemon/endpoint.go`
- Test: `internal/daemon/endpoint_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/endpoint_test.go
package daemon_test

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestUnixEndpoint_RoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on windows")
	}
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	ep := daemon.UnixEndpoint(sock)

	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		c, _ := l.Accept()
		if c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	conn, err := ep.Dial(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(buf))
	assert.Equal(t, "unix://"+sock, ep.Address())
}

func TestTCPEndpoint_RoundTrip(t *testing.T) {
	ep := daemon.TCPEndpoint("127.0.0.1:0")
	l, err := ep.Listen()
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// Hand the actually-bound address back into a fresh endpoint for Dial.
	addr := l.Addr().(*net.TCPAddr).String()
	dialEP := daemon.TCPEndpoint(addr)
	go func() {
		c, _ := l.Accept()
		if c != nil {
			_, _ = c.Write([]byte("ok"))
			_ = c.Close()
		}
	}()

	conn, err := dialEP.Dial(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(buf))
}

func TestTCPEndpoint_RejectsNonLoopback(t *testing.T) {
	ep := daemon.TCPEndpoint("8.8.8.8:7474")
	_, err := ep.Listen()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in   string
		kind string
	}{
		{"unix:///tmp/foo.sock", "unix"},
		{"127.0.0.1:7474", "tcp"},
		{"localhost:7474", "tcp"},
	}
	for _, tc := range cases {
		ep, err := daemon.ParseAddress(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.kind, ep.Kind(), tc.in)
	}
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/endpoint.go
package daemon

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// DaemonEndpoint abstracts the listen / dial pair for either a Unix socket or
// TCP loopback. Address() returns a stable string representation.
type DaemonEndpoint interface {
	Listen() (net.Listener, error)
	Dial(ctx context.Context) (net.Conn, error)
	Address() string
	Kind() string // "unix" | "tcp"
}

type unixEndpoint struct{ path string }

func (u unixEndpoint) Listen() (net.Listener, error) {
	return net.Listen("unix", u.path)
}

func (u unixEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "unix", u.path)
}

func (u unixEndpoint) Address() string { return "unix://" + u.path }
func (u unixEndpoint) Kind() string    { return "unix" }

// UnixEndpoint constructs a Unix-socket endpoint at the given path.
func UnixEndpoint(path string) DaemonEndpoint { return unixEndpoint{path: path} }

type tcpEndpoint struct{ addr string }

func (t tcpEndpoint) Listen() (net.Listener, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", t.addr)
}

func (t tcpEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if err := requireLoopback(t.addr); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", t.addr)
}

func (t tcpEndpoint) Address() string { return t.addr }
func (t tcpEndpoint) Kind() string    { return "tcp" }

// TCPEndpoint constructs a TCP-loopback endpoint at the given host:port.
func TCPEndpoint(addr string) DaemonEndpoint { return tcpEndpoint{addr: addr} }

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w", err)
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("address %q is not loopback", addr)
}

// ParseAddress decodes a serialized form (unix:///path or host:port).
func ParseAddress(s string) (DaemonEndpoint, error) {
	if strings.HasPrefix(s, "unix://") {
		return UnixEndpoint(strings.TrimPrefix(s, "unix://")), nil
	}
	if strings.Contains(s, ":") {
		return TCPEndpoint(s), nil
	}
	return nil, fmt.Errorf("unrecognized address: %q", s)
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/endpoint.go internal/daemon/endpoint_test.go
git commit -m "feat(daemon): unix-socket + tcp-loopback DaemonEndpoint"
```

---

### Task 11: `internal/api/types.go` — request/response DTOs (Plan 1 subset)

Spec refs: §4.1, §4.2, §4.5. Plan 1 needs DTOs for: `/ping` & `/health`; `POST /projects` (init), `POST /projects/resolve`, `GET /projects`, `GET /projects/{id}`; `POST/GET /projects/{id}/issues`, `GET /projects/{id}/issues/{number}`, `PATCH /projects/{id}/issues/{number}`; `POST /projects/{id}/issues/{number}/comments`; `POST /projects/{id}/issues/{number}/actions/close|reopen`.

Use Huma struct tags. Keep wire types in `internal/api`; CLI imports them so the client stays type-safe.

**Files:**
- Create: `internal/api/types.go`

- [ ] **Step 1: Add Huma dep**

```bash
go get github.com/danielgtaylor/huma/v2@v2.37.3
```

- [ ] **Step 2: Implement (no test — the types are compile-checked, exercised by handler tests later)**

```go
// internal/api/types.go
package api

import (
	"time"

	"github.com/wesm/kata/internal/db"
)

// PingResponse mirrors the cheapest liveness response.
type PingResponse struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// HealthResponse mirrors /api/v1/health.
type HealthResponse struct {
	Body struct {
		OK            bool      `json:"ok"`
		DBPath        string    `json:"db_path"`
		SchemaVersion int       `json:"schema_version"`
		Uptime        string    `json:"uptime"`
		StartedAt     time.Time `json:"started_at"`
	}
}

// ResolveProjectRequest is POST /api/v1/projects/resolve.
type ResolveProjectRequest struct {
	Body struct {
		StartPath string `json:"start_path" doc:"absolute path to resolve from" required:"true"`
	}
}

// ProjectResolveBody is the JSON body field of a successful resolve response.
type ProjectResolveBody struct {
	Project       db.Project       `json:"project"`
	Alias         db.ProjectAlias  `json:"alias"`
	WorkspaceRoot string           `json:"workspace_root,omitempty"`
}

// ResolveProjectResponse wraps ProjectResolveBody.
type ResolveProjectResponse struct {
	Body ProjectResolveBody
}

// InitProjectRequest is POST /api/v1/projects (used by `kata init`).
type InitProjectRequest struct {
	Body struct {
		StartPath       string `json:"start_path" required:"true"`
		ProjectIdentity string `json:"project_identity,omitempty"`
		Name            string `json:"name,omitempty"`
		Replace         bool   `json:"replace,omitempty"`
		Reassign        bool   `json:"reassign,omitempty"`
	}
}

// InitProjectResponse uses ProjectResolveBody plus a "created" flag.
type InitProjectResponse struct {
	Body struct {
		ProjectResolveBody
		Created bool `json:"created"`
	}
}

// ListProjectsResponse is GET /api/v1/projects.
type ListProjectsResponse struct {
	Body struct {
		Projects []db.Project `json:"projects"`
	}
}

// ShowProjectResponse is GET /api/v1/projects/{id}.
type ShowProjectResponse struct {
	Body struct {
		Project db.Project        `json:"project"`
		Aliases []db.ProjectAlias `json:"aliases"`
	}
}

// CreateIssueRequest is POST /api/v1/projects/{id}/issues.
type CreateIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Title string `json:"title" required:"true"`
		Body  string `json:"body,omitempty"`
	}
}

// MutationResponse is the standard mutation envelope (§4.5).
type MutationResponse struct {
	Body struct {
		Issue   db.Issue  `json:"issue"`
		Event   *db.Event `json:"event"`
		Changed bool      `json:"changed"`
		Reused  bool      `json:"reused,omitempty"`
	}
}

// ListIssuesRequest is GET /api/v1/projects/{id}/issues.
type ListIssuesRequest struct {
	ProjectID int64  `path:"project_id" required:"true"`
	Status    string `query:"status,omitempty" enum:"open,closed,"`
	Limit     int    `query:"limit,omitempty"`
}

// ListIssuesResponse is the list payload.
type ListIssuesResponse struct {
	Body struct {
		Issues []db.Issue `json:"issues"`
	}
}

// ShowIssueRequest is GET /api/v1/projects/{id}/issues/{number}.
type ShowIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
}

// ShowIssueResponse is the per-issue read payload (Plan 1: issue + comments).
type ShowIssueResponse struct {
	Body struct {
		Issue    db.Issue     `json:"issue"`
		Comments []db.Comment `json:"comments"`
	}
}

// EditIssueRequest is PATCH /api/v1/projects/{id}/issues/{number}.
type EditIssueRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string  `json:"actor" required:"true"`
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
		Owner *string `json:"owner,omitempty"`
	}
}

// CommentRequest is POST /api/v1/projects/{id}/issues/{number}/comments.
type CommentRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor string `json:"actor" required:"true"`
		Body  string `json:"body" required:"true"`
	}
}

// CommentResponse mirrors MutationResponse but adds the new comment row.
type CommentResponse struct {
	Body struct {
		Issue   db.Issue    `json:"issue"`
		Comment db.Comment  `json:"comment"`
		Event   *db.Event   `json:"event"`
		Changed bool        `json:"changed"`
	}
}

// ActionRequest is POST /api/v1/projects/{id}/issues/{number}/actions/close|reopen.
type ActionRequest struct {
	ProjectID int64 `path:"project_id" required:"true"`
	Number    int64 `path:"number" required:"true"`
	Body      struct {
		Actor  string `json:"actor" required:"true"`
		Reason string `json:"reason,omitempty"` // close only; "done"|"wontfix"|"duplicate"
	}
}
```

- [ ] **Step 3: Verify compile**

Run: `go build ./...`
Expected: compiles cleanly.

- [ ] **Step 4: Commit**

```bash
go mod tidy
git add internal/api/ go.mod go.sum
git commit -m "feat(api): request/response DTOs for Plan 1 endpoints"
```

---

### Task 12: `internal/api/errors.go` — stable error envelope + Huma wiring

Spec refs: §4.6, §4.7. Define `ErrorEnvelope` and an `APIError` constructor used by handlers. Wire Huma's error formatter so non-2xx returns this envelope.

**Files:**
- Create: `internal/api/errors.go`
- Test: `internal/api/errors_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/api/errors_test.go
package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/api"
)

func TestAPIError_StatusAndBodyShape(t *testing.T) {
	err := api.NewError(404, "issue_not_found", "issue #42 does not exist", "kata search", nil)
	assert.Equal(t, 404, err.Status)

	body := err.Envelope()
	assert.Equal(t, "issue_not_found", body.Error.Code)
	assert.Equal(t, "issue #42 does not exist", body.Error.Message)
	assert.Equal(t, "kata search", body.Error.Hint)

	js, err2 := json.Marshal(body)
	require.NoError(t, err2)
	assert.Contains(t, string(js), `"code":"issue_not_found"`)
}

func TestAPIError_DataPropagates(t *testing.T) {
	err := api.NewError(409, "duplicate_candidates", "x", "", map[string]any{
		"candidates": []int{1, 2},
	})
	body := err.Envelope()
	assert.Equal(t, []int{1, 2}, body.Error.Data["candidates"])
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/api/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/api/errors.go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// ErrorBody is the inner payload of an error envelope.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// ErrorEnvelope is the stable wire shape for non-2xx responses.
type ErrorEnvelope struct {
	Status int       `json:"status"`
	Error  ErrorBody `json:"error"`
}

// APIError is the Go representation that handlers return; satisfies Huma's
// HTTPError interface so the framework serializes the envelope verbatim.
type APIError struct {
	Status  int
	Code    string
	Message string
	Hint    string
	Data    map[string]any
}

// NewError constructs an APIError. Hint and data are optional.
func NewError(status int, code, message, hint string, data map[string]any) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Hint: hint, Data: data}
}

// Error implements the standard error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
}

// GetStatus implements huma.StatusError so the framework picks the right code.
func (e *APIError) GetStatus() int { return e.Status }

// Envelope returns the JSON body shape used in responses.
func (e *APIError) Envelope() ErrorEnvelope {
	return ErrorEnvelope{
		Status: e.Status,
		Error: ErrorBody{
			Code:    e.Code,
			Message: e.Message,
			Hint:    e.Hint,
			Data:    e.Data,
		},
	}
}

// MarshalJSON serializes the envelope so Huma's default response writer emits
// our wire shape rather than the framework default.
func (e *APIError) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Envelope())
}

// InstallErrorFormatter wires Huma so non-API-typed errors (panics, validation
// failures) also serialize to ErrorEnvelope. Call once at server startup.
func InstallErrorFormatter() {
	huma.NewError = func(status int, message string, _ ...error) huma.StatusError {
		code := codeForStatus(status)
		return &APIError{Status: status, Code: code, Message: message}
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "validation"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "confirm_required"
	case http.StatusInternalServerError:
		return "internal"
	default:
		return "error"
	}
}

// EnsureCancelled is a small helper so handlers can early-return when ctx is
// cancelled without producing a 500 envelope.
func EnsureCancelled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return NewError(499, "client_closed", err.Error(), "", nil)
	}
	return nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/api/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/api/errors.go internal/api/errors_test.go
git commit -m "feat(api): stable error envelope and Huma formatter"
```

---

### Task 13: `internal/daemon/server.go` — http server lifecycle + signal handling

Spec refs: §2.2, §2.9. Build a `*http.Server` with a Huma adapter mounted on the configured `DaemonEndpoint`. CSRF: reject any non-empty `Origin`; require `Content-Type: application/json` on mutations. Provide `Run(ctx)` that listens until ctx is cancelled, and `WriteRuntimeFile()` integration for handler discovery.

**Files:**
- Create: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/server_test.go
package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestServer_PingReturnsOK(t *testing.T) {
	d, _ := openTestDB(t).(testDBHandle)
	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        d.db,
		StartedAt: d.now,
	})
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `"ok":true`)
}

func TestServer_RejectsNonEmptyOrigin(t *testing.T) {
	d, _ := openTestDB(t).(testDBHandle)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/ping", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "https://attacker.example.com")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestServer_MutationRequiresJSON(t *testing.T) {
	d, _ := openTestDB(t).(testDBHandle)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/v1/projects/resolve", "text/plain",
		strings.NewReader(`{"start_path":"/x"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
}
```

Helper for the daemon-package tests. Add to `internal/daemon/testhelpers_test.go`:

```go
// internal/daemon/testhelpers_test.go
package daemon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/db"
)

type testDBHandle struct {
	db  *db.DB
	now time.Time
}

func openTestDB(t *testing.T) testDBHandle {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return testDBHandle{db: d, now: time.Now().UTC()}
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/server.go
package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

// ServerConfig wires the daemon's runtime dependencies.
type ServerConfig struct {
	DB        *db.DB
	StartedAt time.Time
	Endpoint  DaemonEndpoint // optional; used by Run
}

// Server bundles the http handler and lifecycle.
type Server struct {
	cfg     ServerConfig
	handler http.Handler
	api     huma.API
}

// NewServer wires routes onto a fresh http.ServeMux. The returned handler is
// safe to mount in tests via httptest.NewServer.
func NewServer(cfg ServerConfig) *Server {
	api.InstallErrorFormatter()

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("kata", "0.1.0")
	humaConfig.OpenAPIPath = "" // Plan 1: no /openapi.json
	humaAPI := humago.New(mux, humaConfig)

	s := &Server{cfg: cfg, api: humaAPI}
	registerRoutes(humaAPI, cfg)

	s.handler = withCSRFGuards(mux)
	return s
}

// Handler returns the http.Handler suitable for httptest.NewServer.
func (s *Server) Handler() http.Handler { return s.handler }

// API returns the underlying huma.API for handler registration in tests.
func (s *Server) API() huma.API { return s.api }

// Close releases server-owned resources. Currently a no-op since the DB is
// owned by the caller.
func (s *Server) Close() error { return nil }

// Run listens on the configured endpoint until ctx is cancelled. The caller
// is responsible for writing the runtime file once Run has started.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.Endpoint == nil {
		return errors.New("server: endpoint is required for Run")
	}
	l, err := s.cfg.Endpoint.Listen()
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	if err := httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// withCSRFGuards rejects browser-borne requests and enforces JSON content type
// for state-changing methods.
func withCSRFGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			http.Error(w, "Origin header forbidden", http.StatusForbidden)
			return
		}
		if isMutation(r.Method) && r.ContentLength != 0 {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// registerRoutes is implemented by per-resource handler files (handlers_health,
// handlers_projects, handlers_issues, handlers_comments). Stub here; concrete
// registrations land in their own tasks.
func registerRoutes(humaAPI huma.API, cfg ServerConfig) {
	registerHealth(humaAPI, cfg)
	registerProjects(humaAPI, cfg)
	registerIssues(humaAPI, cfg)
	registerComments(humaAPI, cfg)
	registerActions(humaAPI, cfg)
}

// dummy stubs so this file compiles in isolation; each is replaced when the
// handler task lands.
func registerHealth(humaAPI huma.API, cfg ServerConfig)   {}
func registerProjects(humaAPI huma.API, cfg ServerConfig) {}
func registerIssues(humaAPI huma.API, cfg ServerConfig)   {}
func registerComments(humaAPI huma.API, cfg ServerConfig) {}
func registerActions(humaAPI huma.API, cfg ServerConfig)  {}

// silences unused import warnings until the handler tasks add real consumers.
var _ = net.IPv4
```

- [ ] **Step 4: Run test (expect pass for `Origin` and content-type checks; ping test fails until Task 14 lands handlers_health)**

Run: `go test ./internal/daemon/...`
Expected: `TestServer_RejectsNonEmptyOrigin` and `TestServer_MutationRequiresJSON` pass; `TestServer_PingReturnsOK` will fail with 404 — that's expected and fixed in Task 14. Comment that test out temporarily (or leave it failing and re-enable in Task 14).

To keep CI green between tasks, mark the ping test with `t.Skip("registered in Task 14")` for now.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/server.go internal/daemon/server_test.go internal/daemon/testhelpers_test.go
git commit -m "feat(daemon): http server with CSRF + content-type guards"
```

---

### Task 14: Handlers — `/ping` and `/health`

Spec refs: §4.1. `/ping` is the cheap liveness probe (no DB touch). `/health` reads `meta.schema_version` and reports uptime.

**Files:**
- Create: `internal/daemon/handlers_health.go`
- Modify: `internal/daemon/server_test.go` (un-skip the ping test)
- Test: `internal/daemon/handlers_health_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_health_test.go
package daemon_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func TestHealth_ReportsSchemaAndUptime(t *testing.T) {
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	var body struct {
		OK            bool   `json:"ok"`
		SchemaVersion int    `json:"schema_version"`
		Uptime        string `json:"uptime"`
	}
	bs, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.True(t, body.OK)
	assert.Equal(t, 1, body.SchemaVersion)
	assert.NotEmpty(t, body.Uptime)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL — handler not registered.

- [ ] **Step 3: Implement**

```go
// internal/daemon/handlers_health.go
package daemon

import (
	"context"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/kata/internal/api"
)

// installHealthHandlers replaces the registerHealth stub from server.go.
func init() { /* keeps the package init clean */ }

// override registerHealth at link time via re-declaration via this file's
// own helper. Go doesn't permit two `func registerHealth` declarations in
// the same package, so this file overwrites the stub by being added later.

// Replace the stub by deleting it from server.go before merging this task.
// (Spec compliance reviewer will catch the duplicate and prompt the
// implementer to delete the placeholder.)

func registerHealthHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "ping",
		Method:      "GET",
		Path:        "/api/v1/ping",
	}, func(ctx context.Context, _ *struct{}) (*api.PingResponse, error) {
		out := &api.PingResponse{}
		out.Body.OK = true
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "health",
		Method:      "GET",
		Path:        "/api/v1/health",
	}, func(ctx context.Context, _ *struct{}) (*api.HealthResponse, error) {
		var v string
		if err := cfg.DB.QueryRowContext(ctx,
			`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		schema, _ := strconv.Atoi(v)
		out := &api.HealthResponse{}
		out.Body.OK = true
		out.Body.DBPath = cfg.DB.Path()
		out.Body.SchemaVersion = schema
		out.Body.StartedAt = cfg.StartedAt
		out.Body.Uptime = time.Since(cfg.StartedAt).Round(time.Second).String()
		return out, nil
	})
}
```

Then in `server.go`, replace the stub:

```go
// internal/daemon/server.go (modify registerHealth)
func registerHealth(humaAPI huma.API, cfg ServerConfig) {
	registerHealthHandlers(humaAPI, cfg)
}
```

Un-skip the ping test in `server_test.go` (delete the `t.Skip` line).

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: ping + health tests pass.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_health.go internal/daemon/server.go internal/daemon/server_test.go internal/daemon/handlers_health_test.go
git commit -m "feat(daemon): /ping and /health handlers"
```

---

### Task 15: Handlers — `POST /projects/resolve`, `POST /projects` (init), `GET /projects`, `GET /projects/{id}`

Spec refs: §2.4, §4.1, §4.2. The daemon owns path discovery, alias-identity computation, and `.kata.toml` parsing.

**Files:**
- Create: `internal/daemon/handlers_projects.go`
- Test: `internal/daemon/handlers_projects_test.go`

- [ ] **Step 1: Write failing test (ResolveStrictPolicy + InitFromGitRemote + InitFreshClone)**

```go
// internal/daemon/handlers_projects_test.go
package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(js))
	require.NoError(t, err)
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}

func TestResolve_FailsOutsideKataTomlAndWithoutAlias(t *testing.T) {
	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{
		"start_path": t.TempDir(),
	})
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(bs), "project_not_initialized")
}

func TestInit_FromGitRemoteCreatesProject(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project       struct{ ID int64; Identity, Name string } `json:"project"`
		Alias         struct{ AliasIdentity, AliasKind string } `json:"alias"`
		WorkspaceRoot string                                    `json:"workspace_root"`
		Created       bool                                      `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/kata", body.Project.Identity)
	assert.Equal(t, "kata", body.Project.Name)
	assert.True(t, body.Created)
	assert.Equal(t, "github.com/wesm/kata", body.Alias.AliasIdentity)

	// .kata.toml must have been written
	_, err := os.Stat(filepath.Join(dir, ".kata.toml"))
	assert.NoError(t, err)
}

func TestInit_FreshCloneFromExistingKataToml(t *testing.T) {
	// Simulate "git clone, kata init" on a repo that already had .kata.toml.
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"),
		[]byte(`version = 1

[project]
identity = "github.com/wesm/system"
name     = "system"
`), 0o644))

	ts := newTestServer(t)
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Project struct{ Identity string } `json:"project"`
		Created bool                      `json:"created"`
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.Equal(t, "github.com/wesm/system", body.Project.Identity)
	assert.True(t, body.Created)
}

func TestResolve_AfterInitSucceeds(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, bs := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	assert.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/kata"`)
}

func TestInit_AliasConflictWithoutReassign(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")
	ts := newTestServer(t)

	// First init binds the alias to "github.com/wesm/kata".
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	// .kata.toml now declares a different identity.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kata.toml"),
		[]byte(`version = 1

[project]
identity = "github.com/wesm/other"
name     = "other"
`), 0o644))

	// Re-init without --replace must fail.
	resp, bs := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, string(bs), "project_alias_conflict")

	// With --reassign + --replace, succeeds and rewrites alias.
	resp2, bs2 := postJSON(t, ts, "/api/v1/projects", map[string]any{
		"start_path": dir,
		"replace":    true,
		"reassign":   true,
	})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
}

func TestListProjectsAndShow(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/x.git")
	ts := newTestServer(t)
	_, _ = postJSON(t, ts, "/api/v1/projects", map[string]any{"start_path": dir})

	resp, err := http.Get(ts.URL + "/api/v1/projects")
	require.NoError(t, err)
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"identity":"github.com/wesm/x"`)

	// pull project_id from the resolve flow then GET the show endpoint.
	_, rb := postJSON(t, ts, "/api/v1/projects/resolve", map[string]any{"start_path": dir})
	var rbody struct{ Project struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(rb, &rbody))
	resp2, err := http.Get(ts.URL + "/api/v1/projects/" + intToStr(rbody.Project.ID))
	require.NoError(t, err)
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(body2), `"aliases":`)
}

func intToStr(n int64) string {
	return string([]byte{byte('0' + n)})
}

var _ = context.Background // suppress unused import lint when tests evolve
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/handlers_projects.go
package daemon

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/db"
)

func registerProjectsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "resolveProject",
		Method:      "POST",
		Path:        "/api/v1/projects/resolve",
	}, func(ctx context.Context, in *api.ResolveProjectRequest) (*api.ResolveProjectResponse, error) {
		out, err := resolveProject(ctx, cfg.DB, in.Body.StartPath)
		if err != nil {
			return nil, err
		}
		return &api.ResolveProjectResponse{Body: *out}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "initProject",
		Method:      "POST",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, in *api.InitProjectRequest) (*api.InitProjectResponse, error) {
		out, created, err := initProject(ctx, cfg.DB, in)
		if err != nil {
			return nil, err
		}
		resp := &api.InitProjectResponse{}
		resp.Body.ProjectResolveBody = *out
		resp.Body.Created = created
		return resp, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listProjects",
		Method:      "GET",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, _ *struct{}) (*api.ListProjectsResponse, error) {
		ps, err := cfg.DB.ListProjects(ctx)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ListProjectsResponse{}
		out.Body.Projects = ps
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showProject",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}",
	}, func(ctx context.Context, in *struct {
		ProjectID int64 `path:"project_id"`
	}) (*api.ShowProjectResponse, error) {
		p, err := cfg.DB.ProjectByID(ctx, in.ProjectID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		aliases, err := cfg.DB.ProjectAliases(ctx, p.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowProjectResponse{}
		out.Body.Project = p
		out.Body.Aliases = aliases
		return out, nil
	})
}

// resolveProject implements the strict resolution flow per spec §2.4.
func resolveProject(ctx context.Context, store *db.DB, startPath string) (*api.ProjectResolveBody, error) {
	if startPath == "" {
		return nil, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}

	if disc.WorkspaceRoot != "" {
		cfg, err := config.ReadProjectConfig(disc.WorkspaceRoot)
		if err != nil {
			if !errors.Is(err, config.ErrProjectConfigMissing) {
				return nil, api.NewError(400, "validation", err.Error(), "", nil)
			}
		}
		if cfg != nil {
			project, err := store.ProjectByIdentity(ctx, cfg.Project.Identity)
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_initialized",
					"project "+cfg.Project.Identity+" is bound by .kata.toml but not registered",
					`run "kata init" in this workspace`, nil)
			}
			if err != nil {
				return nil, api.NewError(500, "internal", err.Error(), "", nil)
			}
			alias, err := upsertAliasFor(ctx, store, project.ID, disc, false)
			if err != nil {
				return nil, err
			}
			return &api.ProjectResolveBody{Project: project, Alias: alias, WorkspaceRoot: disc.WorkspaceRoot}, nil
		}
	}

	if disc.GitRoot != "" {
		info, err := config.ComputeAliasIdentity(disc)
		if err != nil {
			return nil, api.NewError(400, "validation", err.Error(), "", nil)
		}
		alias, err := store.AliasByIdentity(ctx, info.Identity)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_initialized",
				"no kata project is attached to this workspace",
				`run "kata init" in this workspace`, nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		_ = store.TouchAlias(ctx, alias.ID, info.RootPath)
		project, err := store.ProjectByID(ctx, alias.ProjectID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		return &api.ProjectResolveBody{Project: project, Alias: alias, WorkspaceRoot: ""}, nil
	}

	return nil, api.NewError(404, "project_not_initialized",
		"no .kata.toml ancestor and no git ancestor",
		`run "kata init" inside a workspace`, nil)
}

// initProject implements `kata init` on the daemon side.
func initProject(ctx context.Context, store *db.DB, req *api.InitProjectRequest) (*api.ProjectResolveBody, bool, error) {
	if req.Body.StartPath == "" {
		return nil, false, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(req.Body.StartPath)
	if err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
	}

	// Only read .kata.toml when a workspace root was actually discovered;
	// passing "" to ReadProjectConfig would resolve to the daemon's cwd.
	var tomlCfg *config.ProjectConfig
	if disc.WorkspaceRoot != "" {
		cfg, err := config.ReadProjectConfig(disc.WorkspaceRoot)
		if err != nil && !errors.Is(err, config.ErrProjectConfigMissing) {
			return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
		}
		tomlCfg = cfg
	}

	// Decide identity + name.
	var identity, name string
	switch {
	case tomlCfg != nil && req.Body.ProjectIdentity != "" && tomlCfg.Project.Identity != req.Body.ProjectIdentity:
		if !req.Body.Replace {
			return nil, false, api.NewError(http.StatusConflict, "project_binding_conflict",
				".kata.toml declares a different identity",
				"pass replace=true to overwrite", nil)
		}
		identity = req.Body.ProjectIdentity
		name = pickName(req.Body.Name, identity)
	case tomlCfg != nil:
		identity = tomlCfg.Project.Identity
		name = pickName(req.Body.Name, tomlCfg.Project.Name)
		if name == "" {
			name = pickName("", identity)
		}
	case req.Body.ProjectIdentity != "":
		identity = req.Body.ProjectIdentity
		name = pickName(req.Body.Name, identity)
	default:
		// derive from git remote
		if disc.GitRoot == "" {
			return nil, false, api.NewError(400, "validation",
				"cannot derive project identity outside a git workspace",
				`pass project_identity or run inside a git repo`, nil)
		}
		info, err := config.ComputeAliasIdentity(disc)
		if err != nil {
			return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
		}
		identity = info.Identity
		name = pickName(req.Body.Name, identity)
	}

	if err := config.ValidateIdentity(identity); err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}

	// When --project was supplied outside any git/workspace ancestor, synthesize
	// a local-alias rooted at the start path so upsertAliasFor has something to
	// attach. This is the explicit escape hatch documented in spec §2.4.
	if disc.GitRoot == "" && disc.WorkspaceRoot == "" {
		disc.WorkspaceRoot = abs
	}

	project, created, err := upsertProject(ctx, store, identity, name)
	if err != nil {
		return nil, false, err
	}

	alias, err := upsertAliasFor(ctx, store, project.ID, disc, req.Body.Reassign)
	if err != nil {
		return nil, false, err
	}

	// Write .kata.toml at workspace root (or git root, or start path).
	dest := disc.WorkspaceRoot
	if dest == "" {
		if disc.GitRoot != "" {
			dest = disc.GitRoot
		} else {
			dest = abs
		}
	}
	if tomlCfg == nil || tomlCfg.Project.Identity != identity {
		if err := config.WriteProjectConfig(dest, identity, name); err != nil {
			return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
		}
	}

	return &api.ProjectResolveBody{
		Project:       project,
		Alias:         alias,
		WorkspaceRoot: dest,
	}, created, nil
}

func upsertProject(ctx context.Context, store *db.DB, identity, name string) (db.Project, bool, error) {
	got, err := store.ProjectByIdentity(ctx, identity)
	if err == nil {
		return got, false, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	created, err := store.CreateProject(ctx, identity, name)
	if err != nil {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return created, true, nil
}

// upsertAliasFor attaches the discovered alias to projectID. If the alias is
// already attached to a *different* project, returns a 409 unless reassign
// is true (in which case we move it).
func upsertAliasFor(ctx context.Context, store *db.DB, projectID int64, disc config.DiscoveredPaths, reassign bool) (db.ProjectAlias, error) {
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return db.ProjectAlias{}, api.NewError(400, "validation", err.Error(), "", nil)
	}
	existing, err := store.AliasByIdentity(ctx, info.Identity)
	if err == nil {
		if existing.ProjectID == projectID {
			_ = store.TouchAlias(ctx, existing.ID, info.RootPath)
			refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
			return refreshed, nil
		}
		if !reassign {
			return db.ProjectAlias{}, api.NewError(http.StatusConflict, "project_alias_conflict",
				"alias already attached to a different project",
				"pass reassign=true to move it", map[string]any{
					"alias_identity":      info.Identity,
					"existing_project_id": existing.ProjectID,
				})
		}
		if _, execErr := store.ExecContext(ctx,
			`UPDATE project_aliases SET project_id = ?, root_path = ?, last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			projectID, info.RootPath, existing.ID); execErr != nil {
			return db.ProjectAlias{}, api.NewError(500, "internal", execErr.Error(), "", nil)
		}
		refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
		return refreshed, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	a, err := store.AttachAlias(ctx, projectID, info.Identity, info.Kind, info.RootPath)
	if err != nil {
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return a, nil
}

func pickName(explicit, identity string) string {
	if explicit != "" {
		return explicit
	}
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' || identity[i] == ':' {
			return identity[i+1:]
		}
	}
	return identity
}
```

Then in `server.go`, replace `registerProjects` stub with `registerProjectsHandlers(humaAPI, cfg)`.

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: project resolve/init/list/show tests pass.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_projects.go internal/daemon/handlers_projects_test.go internal/daemon/server.go
git commit -m "feat(daemon): project resolve/init/list/show handlers"
```

---

### Task 16: Handlers — issues (`POST/GET/GET-one/PATCH /projects/{id}/issues`)

Spec refs: §4.1, §4.5, §6.4. Plan 1 doesn't ship idempotency / look-alike — those are Plan 3. So `POST /issues` is just create.

**Files:**
- Create: `internal/daemon/handlers_issues.go`
- Test: `internal/daemon/handlers_issues_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_issues_test.go
package daemon_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bootstrapProject(t *testing.T) (*httptestServerHandle, int64) {
	t.Helper()
	h := newServerWithGitWorkspace(t, "https://github.com/wesm/kata.git")
	_, bs := postJSON(t, h.ts, "/api/v1/projects", map[string]any{"start_path": h.dir})
	var resp struct{ Project struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(bs, &resp))
	return h, resp.Project.ID
}

type httptestServerHandle struct {
	ts  any // *httptest.Server, but kept generic to avoid import cycles in helpers
	dir string
}

func TestIssues_CreateRoundtrip(t *testing.T) {
	h, projectID := bootstrapProject(t)
	resp, bs := postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(projectID, 10)+"/issues",
		map[string]any{"actor": "agent-1", "title": "first", "body": "details"})
	require.Equal(t, 200, resp.StatusCode, string(bs))

	var body struct {
		Issue struct {
			Number int64
			Title  string
			Status string
		}
		Event struct{ Type string }
	}
	require.NoError(t, json.Unmarshal(bs, &body))
	assert.EqualValues(t, 1, body.Issue.Number)
	assert.Equal(t, "first", body.Issue.Title)
	assert.Equal(t, "open", body.Issue.Status)
	assert.Equal(t, "issue.created", body.Event.Type)
}

func TestIssues_ListAndShow(t *testing.T) {
	h, pid := bootstrapProject(t)
	for _, title := range []string{"a", "b"} {
		_, _ = postJSON(t, h.ts.(*httptest.Server),
			"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
			map[string]any{"actor": "x", "title": title})
	}

	resp, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues?status=open")
	require.NoError(t, err)
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"title":"a"`)
	assert.Contains(t, string(bs), `"title":"b"`)

	resp2, err := http.Get(h.ts.(*httptest.Server).URL +
		"/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues/1")
	require.NoError(t, err)
	defer resp2.Body.Close()
	bs2, _ := io.ReadAll(resp2.Body)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Contains(t, string(bs2), `"comments":`)
}

func TestIssues_PatchEditTitleAndBody(t *testing.T) {
	h, pid := bootstrapProject(t)
	_, _ = postJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "old"})

	resp, bs := patchJSON(t, h.ts.(*httptest.Server),
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1",
		map[string]any{"actor": "x", "title": "new"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"title":"new"`)
}
```

Add helpers in `testhelpers_test.go`:

```go
// internal/daemon/testhelpers_test.go (append)
import (
	"net/http/httptest"
)

func newServerWithGitWorkspace(t *testing.T, originURL string) *httptestServerHandle {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	if originURL != "" {
		runGit(t, dir, "remote", "add", "origin", originURL)
	}
	d := openTestDB(t)
	srv := daemon.NewServer(daemon.ServerConfig{DB: d.db, StartedAt: d.now})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &httptestServerHandle{ts: ts, dir: dir}
}

func patchJSON(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPatch, ts.URL+path, bytes.NewReader(js))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp, bs
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/handlers_issues.go
package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerIssuesHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.CreateIssueRequest) (*api.MutationResponse, error) {
		if _, err := cfg.DB.ProjectByID(ctx, in.ProjectID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
			}
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		issue, evt, err := cfg.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: in.ProjectID,
			Title:     in.Body.Title,
			Body:      in.Body.Body,
			Author:    in.Body.Actor,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = issue
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listIssues",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues",
	}, func(ctx context.Context, in *api.ListIssuesRequest) (*api.ListIssuesResponse, error) {
		issues, err := cfg.DB.ListIssues(ctx, db.ListIssuesParams{
			ProjectID: in.ProjectID,
			Status:    in.Status,
			Limit:     in.Limit,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ListIssuesResponse{}
		out.Body.Issues = issues
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showIssue",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.ShowIssueRequest) (*api.ShowIssueResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		comments, err := listComments(ctx, cfg.DB, issue.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowIssueResponse{}
		out.Body.Issue = issue
		out.Body.Comments = comments
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "editIssue",
		Method:      "PATCH",
		Path:        "/api/v1/projects/{project_id}/issues/{number}",
	}, func(ctx context.Context, in *api.EditIssueRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.EditIssue(ctx, db.EditIssueParams{
			IssueID: issue.ID,
			Title:   in.Body.Title,
			Body:    in.Body.Body,
			Owner:   in.Body.Owner,
			Actor:   in.Body.Actor,
		})
		if errors.Is(err, db.ErrNoFields) {
			return nil, api.NewError(400, "validation", "no fields to update", "pass at least one of title, body, owner", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})
}

// listComments is a thin wrapper for the show handler.
func listComments(ctx context.Context, store *db.DB, issueID int64) ([]db.Comment, error) {
	rows, err := store.QueryContext(ctx,
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE issue_id = ? ORDER BY created_at ASC, id ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []db.Comment
	for rows.Next() {
		var c db.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

Replace `registerIssues` stub in server.go with `registerIssuesHandlers(humaAPI, cfg)`.

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: PASS for all issues tests + previous handler tests.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_issues.go internal/daemon/handlers_issues_test.go internal/daemon/testhelpers_test.go internal/daemon/server.go
git commit -m "feat(daemon): create/list/show/patch issue handlers"
```

---

### Task 17: Handlers — comments + actions (close, reopen)

Spec refs: §4.1, §4.5, §3.4.

**Files:**
- Create: `internal/daemon/handlers_comments.go`
- Create: `internal/daemon/handlers_actions.go`
- Test: `internal/daemon/handlers_comments_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/handlers_comments_test.go
package daemon_test

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommentEndpoint_AppendsAndEmitsEvent(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "first comment"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"body":"first comment"`)
	assert.Contains(t, string(bs), `"type":"issue.commented"`)
}

func TestActionsClose_ReopenRoundtrip(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent", "reason": "wontfix"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"status":"closed"`)
	assert.Contains(t, string(bs), `"closed_reason":"wontfix"`)

	resp2, bs2 := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/reopen",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp2.StatusCode, string(bs2))
	assert.Contains(t, string(bs2), `"status":"open"`)
}

func TestActionsClose_AlreadyClosedIsNoOpEnvelope(t *testing.T) {
	h, pid := bootstrapProject(t)
	ts := h.ts.(*httptest.Server)
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "x", "title": "x"})
	_, _ = postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent"})

	resp, bs := postJSON(t, ts,
		"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent"})
	require.Equal(t, 200, resp.StatusCode, string(bs))
	assert.Contains(t, string(bs), `"changed":false`)
	assert.Contains(t, string(bs), `"event":null`)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/daemon/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/daemon/handlers_comments.go
package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerCommentsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "createComment",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/comments",
	}, func(ctx context.Context, in *api.CommentRequest) (*api.CommentResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		c, evt, err := cfg.DB.CreateComment(ctx, db.CreateCommentParams{
			IssueID: issue.ID,
			Author:  in.Body.Actor,
			Body:    in.Body.Body,
		})
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, _ := cfg.DB.IssueByID(ctx, issue.ID)
		out := &api.CommentResponse{}
		out.Body.Issue = updated
		out.Body.Comment = c
		out.Body.Event = &evt
		out.Body.Changed = true
		return out, nil
	})
}
```

```go
// internal/daemon/handlers_actions.go
package daemon

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/db"
)

func registerActionsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "closeIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/close",
	}, func(ctx context.Context, in *api.ActionRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.CloseIssue(ctx, issue.ID, in.Body.Reason, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "reopenIssue",
		Method:      "POST",
		Path:        "/api/v1/projects/{project_id}/issues/{number}/actions/reopen",
	}, func(ctx context.Context, in *api.ActionRequest) (*api.MutationResponse, error) {
		issue, err := cfg.DB.IssueByNumber(ctx, in.ProjectID, in.Number)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "issue_not_found", "issue not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		updated, evt, changed, err := cfg.DB.ReopenIssue(ctx, issue.ID, in.Body.Actor)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.MutationResponse{}
		out.Body.Issue = updated
		out.Body.Event = evt
		out.Body.Changed = changed
		return out, nil
	})
}
```

Replace stubs in server.go: `registerComments(humaAPI, cfg)` → `registerCommentsHandlers(humaAPI, cfg)` and `registerActions(humaAPI, cfg)` → `registerActionsHandlers(humaAPI, cfg)`.

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/daemon/...`
Expected: PASS for comments, close, reopen, including the "already closed" no-op envelope.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/daemon/handlers_comments.go internal/daemon/handlers_actions.go internal/daemon/handlers_comments_test.go internal/daemon/server.go
git commit -m "feat(daemon): comment + close/reopen action handlers"
```

---

### Task 18: `internal/testenv/testenv.go` — spawn daemon for integration tests

Spec refs: §9.2 (`internal/testenv/testenv.go`). Builds a fresh per-test environment: temp `KATA_HOME`, fresh DB, daemon listening on a per-test loopback port (using TCP loopback for portability — it's harder to leak Unix sockets across test cleanup on macOS). Returns a thin HTTP client tied to the daemon's address.

**Files:**
- Create: `internal/testenv/testenv.go`
- Test: `internal/testenv/testenv_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/testenv/testenv_test.go
package testenv_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestEnv_BootsDaemonAndAnswersPing(t *testing.T) {
	env := testenv.New(t)
	resp, err := env.HTTP.Get(env.URL + "/api/v1/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(bs), `"ok":true`)
	_ = context.Background
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./internal/testenv/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/testenv/testenv.go
package testenv

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

// Env is a per-test daemon + DB + HTTP client bundle.
type Env struct {
	URL  string
	HTTP *http.Client
	DB   *db.DB
	Home string
}

// New launches a daemon listening on a free loopback port. The DB lives under
// a temp KATA_HOME. Cleanup is wired via t.Cleanup.
func New(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", filepath.Join(home, "kata.db"))

	d, err := db.Open(context.Background(), filepath.Join(home, "kata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	// Pick a free port up front so we have a stable URL.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr).String()
	require.NoError(t, l.Close())

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        d,
		StartedAt: time.Now().UTC(),
		Endpoint:  daemon.TCPEndpoint(addr),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(cancel)

	// Wait briefly for /ping to answer.
	url := "http://" + addr
	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/ping")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return &Env{URL: url, HTTP: client, DB: d, Home: home}
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./internal/testenv/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add internal/testenv/ go.mod go.sum
git commit -m "feat(testenv): boot daemon over TCP loopback for tests"
```

---

### Task 19: `cmd/kata/helpers.go` — body sources, JSON output, exit codes

Spec refs: §4.7, §5.1. Body source resolution (`--body`/`--body-file`/`--body-stdin`), JSON formatter that emits `kata_api_version=1`, exit code constants, daemon address discovery, HTTP client helpers.

**Files:**
- Create: `cmd/kata/helpers.go`
- Test: `cmd/kata/helpers_test.go`

- [ ] **Step 1: Add Cobra dependency**

```bash
go get github.com/spf13/cobra@v1.10.2
```

- [ ] **Step 2: Write failing test**

```go
// cmd/kata/helpers_test.go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBody_FlagWins(t *testing.T) {
	got, err := resolveBody(BodySources{Body: "hello"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestResolveBody_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/b.txt"
	require.NoError(t, writeStringFile(path, "from file"))
	got, err := resolveBody(BodySources{File: path}, nil)
	require.NoError(t, err)
	assert.Equal(t, "from file", got)
}

func TestResolveBody_FromStdin(t *testing.T) {
	in := bytes.NewBufferString("from stdin")
	got, err := resolveBody(BodySources{Stdin: true}, in)
	require.NoError(t, err)
	assert.Equal(t, "from stdin", got)
}

func TestResolveBody_TwoSourcesIsError(t *testing.T) {
	_, err := resolveBody(BodySources{Body: "x", Stdin: true}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestResolveActor_Precedence(t *testing.T) {
	t.Setenv("KATA_AUTHOR", "")
	got, src := resolveActor("flag-actor", nil)
	assert.Equal(t, "flag-actor", got)
	assert.Equal(t, "flag", src)

	t.Setenv("KATA_AUTHOR", "env-actor")
	got, src = resolveActor("", nil)
	assert.Equal(t, "env-actor", got)
	assert.Equal(t, "env", src)

	t.Setenv("KATA_AUTHOR", "")
	got, src = resolveActor("", func() (string, error) { return "git-user", nil })
	assert.Equal(t, "git-user", got)
	assert.Equal(t, "git", src)

	got, src = resolveActor("", func() (string, error) { return "", nil })
	assert.Equal(t, "anonymous", got)
	assert.Equal(t, "fallback", src)
}

func TestEmitJSON_AddsAPIVersion(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitJSON(&buf, map[string]string{"x": "y"}))
	out := buf.String()
	assert.Contains(t, out, `"kata_api_version":1`)
	assert.Contains(t, out, `"x":"y"`)
	assert.True(t, strings.HasSuffix(out, "\n"))
}
```

- [ ] **Step 3: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 4: Implement**

```go
// cmd/kata/helpers.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Exit codes per spec §4.7.
const (
	ExitOK              = 0
	ExitInternal        = 1
	ExitUsage           = 2
	ExitValidation      = 3
	ExitNotFound        = 4
	ExitConflict        = 5
	ExitConfirm         = 6
	ExitDaemonUnavail   = 7
)

// BodySources is the parsed --body / --body-file / --body-stdin trio.
type BodySources struct {
	Body  string
	File  string
	Stdin bool
}

// gitUserFn is a function signature for resolveActor's git fallback so tests
// can inject a stub instead of touching the real `git config user.name`.
type gitUserFn func() (string, error)

// resolveBody returns the resolved body text. Mutually exclusive sources;
// returns error otherwise. Empty result allowed when no source set.
func resolveBody(b BodySources, stdin io.Reader) (string, error) {
	count := 0
	if b.Body != "" {
		count++
	}
	if b.File != "" {
		count++
	}
	if b.Stdin {
		count++
	}
	if count > 1 {
		return "", errors.New("must pass exactly one of --body, --body-file, --body-stdin")
	}
	switch {
	case b.Body != "":
		return b.Body, nil
	case b.File != "":
		bs, err := os.ReadFile(b.File)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", b.File, err)
		}
		return strings.TrimRight(string(bs), "\n"), nil
	case b.Stdin:
		if stdin == nil {
			stdin = os.Stdin
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, stdin); err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimRight(buf.String(), "\n"), nil
	default:
		return "", nil
	}
}

// resolveActor implements precedence flag > env > git > "anonymous". Returns
// (actor, source) where source is one of "flag"|"env"|"git"|"fallback".
func resolveActor(flag string, gitUser gitUserFn) (string, string) {
	if flag != "" {
		return flag, "flag"
	}
	if v := os.Getenv("KATA_AUTHOR"); v != "" {
		return v, "env"
	}
	if gitUser == nil {
		gitUser = readGitUserName
	}
	if name, _ := gitUser(); name != "" {
		return name, "git"
	}
	return "anonymous", "fallback"
}

func readGitUserName() (string, error) {
	cmd := exec.Command("git", "config", "user.name")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// emitJSON marshals v with a "kata_api_version":1 wrapper and a trailing
// newline.
func emitJSON(w io.Writer, v any) error {
	wrapped := map[string]any{"kata_api_version": 1}
	for k, val := range structToMap(v) {
		wrapped[k] = val
	}
	bs, err := json.Marshal(wrapped)
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	_, err = w.Write(bs)
	return err
}

func structToMap(v any) map[string]any {
	bs, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(bs, &m)
	if m == nil {
		m = map[string]any{"value": v}
	}
	return m
}

// writeStringFile is a tiny wrapper used by tests.
func writeStringFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

// httpDoJSON sends a request body, returns (status, response body bytes).
func httpDoJSON(ctx context.Context, client *http.Client, method, url string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(bs)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, bs, nil
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
go mod tidy
make lint
git add cmd/kata/ go.mod go.sum
git commit -m "feat(cli): body sources, actor precedence, exit codes, JSON emit"
```

---

### Task 20: `cmd/kata/main.go` — root cobra command + universal flags

Spec refs: §6 (universal flags `--json`, `--quiet`, `--as`, `--workspace`). Wire each subcommand as a stub returning ExitNotImplemented for now; concrete commands land in subsequent tasks.

**Files:**
- Create: `cmd/kata/main.go`
- Test: `cmd/kata/main_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/main_test.go
package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoot_HelpListsUniversalFlags(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "--json")
	assert.Contains(t, out, "--quiet")
	assert.Contains(t, out, "--as")
	assert.Contains(t, out, "--workspace")
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/main.go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// globalFlags carries the universal flags applied on every command.
type globalFlags struct {
	JSON      bool
	Quiet     bool
	As        string
	Workspace string
}

var flags globalFlags

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "kata",
		Short:         "kata — lightweight issue tracker for agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().BoolVar(&flags.JSON, "json", false, "emit machine-readable JSON")
	cmd.PersistentFlags().BoolVarP(&flags.Quiet, "quiet", "q", false, "suppress non-essential output")
	cmd.PersistentFlags().StringVar(&flags.As, "as", "", "override actor (default: $KATA_AUTHOR > git > anonymous)")
	cmd.PersistentFlags().StringVar(&flags.Workspace, "workspace", "", "path used for project resolution (default: cwd)")

	cmd.AddCommand(
		newDaemonCmd(),
		newInitCmd(),
		newCreateCmd(),
		newShowCmd(),
		newListCmd(),
		newEditCmd(),
		newCommentCmd(),
		newCloseCmd(),
		newReopenCmd(),
		newWhoamiCmd(),
		newHealthCmd(),
		newProjectsCmd(),
	)
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kata:", err)
		os.Exit(ExitInternal)
	}
}
```

Stub the new command constructors (they return placeholder cobra commands so the build compiles; concrete implementations land in their own tasks):

```go
// cmd/kata/stubs.go
package main

import "github.com/spf13/cobra"

func newDaemonCmd() *cobra.Command  { return &cobra.Command{Use: "daemon", Short: "manage the kata daemon"} }
func newInitCmd() *cobra.Command    { return &cobra.Command{Use: "init", Short: "bind workspace to a project"} }
func newCreateCmd() *cobra.Command  { return &cobra.Command{Use: "create <title>", Short: "create issue"} }
func newShowCmd() *cobra.Command    { return &cobra.Command{Use: "show <number>", Short: "show issue"} }
func newListCmd() *cobra.Command    { return &cobra.Command{Use: "list", Short: "list issues"} }
func newEditCmd() *cobra.Command    { return &cobra.Command{Use: "edit <number>", Short: "edit issue"} }
func newCommentCmd() *cobra.Command { return &cobra.Command{Use: "comment <number>", Short: "comment on issue"} }
func newCloseCmd() *cobra.Command   { return &cobra.Command{Use: "close <number>", Short: "close issue"} }
func newReopenCmd() *cobra.Command  { return &cobra.Command{Use: "reopen <number>", Short: "reopen issue"} }
func newWhoamiCmd() *cobra.Command  { return &cobra.Command{Use: "whoami", Short: "show resolved actor"} }
func newHealthCmd() *cobra.Command  { return &cobra.Command{Use: "health", Short: "daemon health"} }
func newProjectsCmd() *cobra.Command {
	c := &cobra.Command{Use: "projects", Short: "list projects"}
	c.AddCommand(&cobra.Command{Use: "list"}, &cobra.Command{Use: "show"})
	return c
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go mod tidy
make lint
git add cmd/kata/ go.mod go.sum
git commit -m "feat(cli): cobra root and universal flags"
```

---

### Task 21: `cmd/kata/daemon_cmd.go` — `daemon start|stop|status|logs`

Spec refs: §6.1 ("Daemon" row). For Plan 1 we only need `daemon start` (foreground) + `daemon status` + auto-start helper used by other commands. `daemon stop` sends SIGTERM to the PID from `daemon.<pid>.json`. `daemon logs` is deferred to Plan 5 (hooks).

Auto-start helper used by every CLI command: discover an existing daemon via `runtime/<dbhash>/daemon.*.json`, probe `/ping`, on miss spawn a child `kata daemon start` and wait for liveness.

**Files:**
- Replace stub: `cmd/kata/daemon_cmd.go`
- Create: `cmd/kata/client.go` (auto-start logic + ApiClient)
- Test: `cmd/kata/daemon_cmd_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/daemon_cmd_test.go
package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonStatus_NoDaemonReportsAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	cmd := newDaemonCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "no daemon")
}

func TestEnsureDaemon_ReturnsExistingURL(t *testing.T) {
	// Use the testenv harness to stand a daemon up, then ask ensureDaemon to
	// discover it. Skip in -short.
	if testing.Short() {
		t.Skip()
	}
	tmp := t.TempDir()
	t.Setenv("KATA_HOME", tmp)
	t.Setenv("KATA_DB", filepath.Join(tmp, "kata.db"))

	// Pretend a daemon is running by writing a runtime file pointing at a real
	// listener. (This avoids spawning a child binary in unit tests.)
	addr, cleanup := pipeServer(t)
	t.Cleanup(cleanup)
	require.NoError(t, writeRuntimeFor(tmp, addr))

	url, err := ensureDaemon(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "http://"))
}
```

`pipeServer` and `writeRuntimeFor` are tiny test helpers that construct a TCP server answering `/api/v1/ping` with `{"ok":true}` and write the matching `daemon.<pid>.json`. Place them in a `cmd/kata/testhelpers_test.go` file.

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement `daemon_cmd.go`**

Delete the stub `newDaemonCmd` from `stubs.go`. Implement here:

```go
// cmd/kata/daemon_cmd.go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "manage the kata daemon"}
	cmd.AddCommand(daemonStartCmd(), daemonStatusCmd(), daemonStopCmd())
	return cmd
}

func daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the daemon in foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return runDaemon(ctx)
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "report whether a daemon is running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := daemon.NewNamespace()
			if err != nil {
				return err
			}
			recs, err := daemon.ListRuntimeFiles(ns.DataDir)
			if err != nil {
				return err
			}
			alive := 0
			for _, r := range recs {
				if daemon.ProcessAlive(r.PID) {
					fmt.Fprintf(cmd.OutOrStdout(), "daemon pid=%d address=%s db=%s started_at=%s\n",
						r.PID, r.Address, r.DBPath, r.StartedAt.Format(time.RFC3339))
					alive++
				}
			}
			if alive == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no daemon running")
			}
			return nil
		},
	}
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "send SIGTERM to a running daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := daemon.NewNamespace()
			if err != nil {
				return err
			}
			recs, err := daemon.ListRuntimeFiles(ns.DataDir)
			if err != nil {
				return err
			}
			for _, r := range recs {
				if daemon.ProcessAlive(r.PID) {
					p, _ := os.FindProcess(r.PID)
					_ = p.Signal(syscall.SIGTERM)
					fmt.Fprintf(cmd.OutOrStdout(), "stopped pid=%d\n", r.PID)
				}
			}
			return nil
		},
	}
}

// runDaemon is the foreground daemon entry. Used by `kata daemon start` and
// also by the auto-start child process spawned by ensureDaemon.
func runDaemon(ctx context.Context) error {
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
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	socketPath := filepath.Join(ns.SocketDir, "daemon.sock")
	endpoint := daemon.UnixEndpoint(socketPath)

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        store,
		StartedAt: time.Now().UTC(),
		Endpoint:  endpoint,
	})
	defer srv.Close()

	rec := daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   endpoint.Address(),
		DBPath:    dbPath,
		StartedAt: time.Now().UTC(),
	}
	if _, err := daemon.WriteRuntimeFile(ns.DataDir, rec); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(ns.DataDir, fmt.Sprintf("daemon.%d.json", os.Getpid())))

	return srv.Run(ctx)
}
```

- [ ] **Step 4: Implement `client.go` (ensureDaemon + ApiClient)**

```go
// cmd/kata/client.go
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wesm/kata/internal/daemon"
)

// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one if
// none is found. Returns "http://127.0.0.1:PORT" or "http+unix://<sock>".
func ensureDaemon(ctx context.Context) (string, error) {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, ok := tryDiscover(ns.DataDir); ok {
		return url, nil
	}
	// Spawn child: kata daemon start (foreground, detached).
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("auto-start daemon: %w", err)
	}
	// Don't wait — let the child outlive us.
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if url, ok := tryDiscover(ns.DataDir); ok {
			return url, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", errors.New("daemon failed to start within 5s")
}

func tryDiscover(dataDir string) (string, bool) {
	recs, err := daemon.ListRuntimeFiles(dataDir)
	if err != nil {
		return "", false
	}
	for _, r := range recs {
		if !daemon.ProcessAlive(r.PID) {
			continue
		}
		url, ok := pingAddress(r.Address)
		if ok {
			return url, true
		}
	}
	return "", false
}

func pingAddress(address string) (string, bool) {
	if strings.HasPrefix(address, "unix://") {
		path := strings.TrimPrefix(address, "unix://")
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", path)
				},
			},
			Timeout: 1 * time.Second,
		}
		// Unix sockets don't have a host, but http.Get needs one.
		const base = "http://kata"
		resp, err := client.Get(base + "/api/v1/ping")
		if err != nil {
			return "", false
		}
		_ = resp.Body.Close()
		return base, true // caller must reuse this same transport via httpClientFor()
	}
	url := "http://" + address
	resp, err := http.Get(url + "/api/v1/ping")
	if err != nil {
		return "", false
	}
	_ = resp.Body.Close()
	return url, true
}

// httpClientFor returns an *http.Client whose transport understands unix://
// addresses. The base url returned by ensureDaemon is paired with this client.
func httpClientFor(baseURL string) (*http.Client, error) {
	if !strings.HasPrefix(baseURL, "http://kata") {
		return &http.Client{Timeout: 5 * time.Second}, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return nil, err
	}
	recs, err := daemon.ListRuntimeFiles(ns.DataDir)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if strings.HasPrefix(r.Address, "unix://") {
			path := strings.TrimPrefix(r.Address, "unix://")
			return &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", path)
					},
				},
				Timeout: 5 * time.Second,
			}, nil
		}
	}
	return nil, errors.New("no unix-socket daemon found")
}
```

- [ ] **Step 5: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
make lint
git add cmd/kata/ internal/daemon/ go.mod go.sum
git commit -m "feat(cli): daemon start/status/stop and auto-discovery"
```

---

### Task 22: `cmd/kata/init.go` — `kata init` command

Spec refs: §2.4, §6.1. Walks the user's `--workspace`/cwd through `POST /api/v1/projects` and prints the resolved binding. Supports `--project`, `--name`, `--replace`, `--reassign`. Exit 5 on `project_alias_conflict`/`project_binding_conflict`.

**Files:**
- Replace stub: `cmd/kata/init.go`
- Test: `cmd/kata/init_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/init_test.go
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestInit_FreshGitRepoBindsViaRemote(t *testing.T) {
	env := testenv.New(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/wesm/kata.git")

	ctx := context.Background()
	out, err := callInit(ctx, env.URL, dir, callInitOpts{})
	require.NoError(t, err)
	assert.Contains(t, out, `"identity":"github.com/wesm/kata"`)
	assert.FileExists(t, filepath.Join(dir, ".kata.toml"))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

Remove `newInitCmd` from `stubs.go`. Add:

```go
// cmd/kata/init.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type initOptions struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

func newInitCmd() *cobra.Command {
	var opts initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "bind the current workspace to a kata project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			out, err := callInit(ctx, baseURL, start, callInitOpts{
				Project:  opts.Project,
				Name:     opts.Name,
				Replace:  opts.Replace,
				Reassign: opts.Reassign,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Project, "project", "", "project identity (default: derive from .kata.toml or git remote)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "display name (default: last segment of identity)")
	cmd.Flags().BoolVar(&opts.Replace, "replace", false, "overwrite a conflicting .kata.toml binding")
	cmd.Flags().BoolVar(&opts.Reassign, "reassign", false, "move an existing alias to this project")
	return cmd
}

type callInitOpts struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

func callInit(ctx context.Context, baseURL, startPath string, opts callInitOpts) (string, error) {
	client, err := httpClientFor(baseURL)
	if err != nil {
		return "", err
	}
	body := map[string]any{"start_path": startPath}
	if opts.Project != "" {
		body["project_identity"] = opts.Project
	}
	if opts.Name != "" {
		body["name"] = opts.Name
	}
	if opts.Replace {
		body["replace"] = true
	}
	if opts.Reassign {
		body["reassign"] = true
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, baseURL+"/api/v1/projects", body)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", apiErrFromBody(status, bs)
	}
	if flags.JSON {
		return string(bs), nil
	}
	var b struct {
		Project struct{ Identity, Name string }
		Created bool
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return "", err
	}
	verb := "bound"
	if b.Created {
		verb = "created and bound"
	}
	return fmt.Sprintf("%s project %s (%s)", verb, b.Project.Identity, b.Project.Name), nil
}

func resolveStartPath(workspace string) (string, error) {
	if workspace != "" {
		return workspace, nil
	}
	return os.Getwd()
}

// apiErrFromBody decodes the standard error envelope and exits with the right code.
func apiErrFromBody(status int, bs []byte) error {
	var env struct {
		Error struct {
			Code    string
			Message string
			Hint    string
		}
	}
	if err := json.Unmarshal(bs, &env); err != nil {
		return errors.New(string(bs))
	}
	msg := env.Error.Message
	if env.Error.Hint != "" {
		msg += "; hint: " + env.Error.Hint
	}
	exitCode := mapStatusToExit(status, env.Error.Code)
	return &cliError{Message: msg, ExitCode: exitCode, Code: env.Error.Code}
}

type cliError struct {
	Message  string
	Code     string
	ExitCode int
}

func (e *cliError) Error() string { return e.Message }

func mapStatusToExit(status int, code string) int {
	switch status {
	case 400:
		return ExitValidation
	case 404:
		return ExitNotFound
	case 409:
		return ExitConflict
	case 412:
		return ExitConfirm
	}
	_ = code
	return ExitInternal
}
```

Add error-aware exit handling at the top of `main.go`:

```go
// in main.go's main()
func main() {
	cmd := newRootCmd()
	if err := cmd.Execute(); err != nil {
		var cli *cliError
		if errors.As(err, &cli) {
			fmt.Fprintln(os.Stderr, "kata:", cli.Message)
			os.Exit(cli.ExitCode)
		}
		fmt.Fprintln(os.Stderr, "kata:", err)
		os.Exit(ExitInternal)
	}
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/ go.mod go.sum
git commit -m "feat(cli): kata init binds workspace via fresh-clone or --project"
```

---

### Task 23: `cmd/kata/create.go` — `kata create <title>`

Spec refs: §6.1, §6.4. Resolve project via `/projects/resolve`, then `POST /projects/{id}/issues`. Body sources: `--body` / `--body-file` / `--body-stdin`. `--quiet` without `--json` prints just the issue number.

**Files:**
- Replace stub: `cmd/kata/create.go`
- Test: `cmd/kata/create_test.go`

- [ ] **Step 1: Write failing test (e2e against testenv)**

```go
// cmd/kata/create_test.go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCreate_PrintsIssueNumberInQuietMode(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "--quiet", "create", "first issue", "--body", "details"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "1", strings.TrimSpace(buf.String()))
}
```

`initBoundWorkspace` is a helper that creates a temp git repo with the named origin and runs `kata init` once. `contextWithBaseURL` injects the testenv URL into a context the CLI's `ensureDaemon` consults; in production this is a no-op and the discovery code runs unchanged.

Add to `cmd/kata/testhelpers_test.go`:

```go
// cmd/kata/testhelpers_test.go
package main

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type baseURLKey struct{}

func contextWithBaseURL(ctx context.Context, url string) context.Context {
	return context.WithValue(ctx, baseURLKey{}, url)
}

func baseURLFromContext(ctx context.Context) string {
	v, _ := ctx.Value(baseURLKey{}).(string)
	return v
}

func initBoundWorkspace(t *testing.T, baseURL, origin string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "remote", "add", "origin", origin)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	body := []byte(`{"start_path":"` + filepath.ToSlash(dir) + `"}`)
	resp, err := http.Post(baseURL+"/api/v1/projects", "application/json", bytesNewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	return dir
}

// bytesNewReader avoids importing bytes from yet another file.
func bytesNewReader(b []byte) *bytesReader { return &bytesReader{b: b} }

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, errEOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

var errEOF = ioEOF()

func ioEOF() error {
	type eofErr struct{}
	return &eofImpl{}
}

type eofImpl struct{}

func (e *eofImpl) Error() string { return "EOF" }
```

(For implementer simplicity: just import `bytes` and use `bytes.NewReader` rather than this elaborate workaround. The above is illustrative — replace with `bytes.NewReader(body)` + `import "bytes"`.)

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

Wire `ensureDaemon` to consult the context-injected URL (so tests can override). Update `client.go`:

```go
// in client.go: ensureDaemon
func ensureDaemon(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(baseURLKey{}).(string); ok && v != "" {
		return v, nil
	}
	// ... existing discovery logic ...
}
```

(Move the `baseURLKey{}` type into `client.go` so it's accessible from tests.)

Replace stub `newCreateCmd`:

```go
// cmd/kata/create.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var src BodySources
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "create a new issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			body, err := resolveBody(src, cmd.InOrStdin())
			if err != nil {
				return &cliError{Message: err.Error(), ExitCode: ExitValidation}
			}
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues", baseURL, projectID),
				map[string]any{"actor": actor, "title": args[0], "body": body})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printMutation(cmd, bs)
		},
	}
	cmd.Flags().StringVar(&src.Body, "body", "", "issue body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")
	return cmd
}

// resolveProjectID hits POST /projects/resolve and returns the project id.
func resolveProjectID(ctx context.Context, baseURL, startPath string) (int64, error) {
	client, err := httpClientFor(baseURL)
	if err != nil {
		return 0, err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		baseURL+"/api/v1/projects/resolve",
		map[string]any{"start_path": startPath})
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, apiErrFromBody(status, bs)
	}
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return 0, err
	}
	return b.Project.ID, nil
}

// printMutation emits either the JSON envelope or a human-readable summary.
func printMutation(cmd *cobra.Command, bs []byte) error {
	var b struct {
		Issue struct {
			Number int64
			Title  string
			Status string
		}
		Changed bool
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.JSON {
		fmt.Fprintln(cmd.OutOrStdout(), string(bs))
		return nil
	}
	if flags.Quiet {
		fmt.Fprintln(cmd.OutOrStdout(), b.Issue.Number)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "#%d %s [%s]\n", b.Issue.Number, b.Issue.Title, b.Issue.Status)
	return nil
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/ go.mod go.sum
git commit -m "feat(cli): kata create with body sources and quiet mode"
```

---

### Task 24: `cmd/kata/show.go` + `cmd/kata/list.go`

Spec refs: §6.1. Both commands resolve the project then call the daemon. Default list filters to `status=open`, sort `updated_at DESC`, limit 50.

**Files:**
- Replace stubs: `cmd/kata/show.go`, `cmd/kata/list.go`
- Test: `cmd/kata/list_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/list_test.go
package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestList_DefaultsToOpenIssuesInProject(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")

	// Create two issues via direct HTTP for speed.
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	for _, title := range []string{"alpha", "beta"} {
		body := []byte(`{"actor":"x","title":"` + title + `"}`)
		resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues", "application/json", bytesNewReader(body))
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		_ = resp.Body.Close()
	}

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "list"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.True(t, strings.Contains(out, "alpha"))
	assert.True(t, strings.Contains(out, "beta"))
}

// resolvePIDViaHTTP and itoa are helpers; place in testhelpers_test.go alongside the others.
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement `list.go` and `show.go`**

```go
// cmd/kata/list.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var status string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list issues in this project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			// "all" is a CLI-only sentinel meaning "no filter"; the server
			// expects an empty status to return both open and closed.
			apiStatus := status
			if apiStatus == "all" {
				apiStatus = ""
			}
			url := fmt.Sprintf("%s/api/v1/projects/%d/issues?status=%s&limit=%d", baseURL, pid, apiStatus, limit)
			status2, bs, err := httpDoJSON(ctx, client, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			if status2 >= 400 {
				return apiErrFromBody(status2, bs)
			}
			if flags.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			var b struct {
				Issues []struct {
					Number int64
					Title  string
					Status string
					Author string
				}
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, i := range b.Issues {
				fmt.Fprintf(cmd.OutOrStdout(), "#%-4d  %-8s  %s  (%s)\n", i.Number, i.Status, i.Title, i.Author)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "open", "filter by status: open|closed|all")
	cmd.Flags().IntVar(&limit, "limit", 50, "max rows")
	return cmd
}
```

```go
// cmd/kata/show.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <number>",
		Short: "show issue + comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, n), nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			var b struct {
				Issue struct {
					Number int64
					Title  string
					Body   string
					Status string
					Author string
				}
				Comments []struct {
					Author string
					Body   string
				}
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "#%d  %s  [%s]  by %s\n", b.Issue.Number, b.Issue.Title, b.Issue.Status, b.Issue.Author)
			if b.Issue.Body != "" {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), b.Issue.Body)
			}
			if len(b.Comments) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\n--- comments ---")
				for _, c := range b.Comments {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", c.Author, c.Body)
				}
			}
			return nil
		},
	}
	return cmd
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/show.go cmd/kata/list.go cmd/kata/list_test.go cmd/kata/testhelpers_test.go
git commit -m "feat(cli): kata show + kata list"
```

---

### Task 25: `cmd/kata/edit.go` + `cmd/kata/comment.go`

Spec refs: §6.1. Both follow the same shape: resolve project → resolve issue number → PATCH/POST → emit response.

**Files:**
- Replace stubs: `cmd/kata/edit.go`, `cmd/kata/comment.go`
- Test: `cmd/kata/comment_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/comment_test.go
package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestComment_AppendsToIssue(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	body := []byte(`{"actor":"x","title":"x"}`)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues", "application/json", bytesNewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workspace", dir, "comment", "1", "--body", "looks good"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "looks good") || strings.Contains(buf.String(), "comment"))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/comment.go
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newCommentCmd() *cobra.Command {
	var src BodySources
	cmd := &cobra.Command{
		Use:   "comment <number>",
		Short: "append a comment to an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			body, err := resolveBody(src, cmd.InOrStdin())
			if err != nil {
				return &cliError{Message: err.Error(), ExitCode: ExitValidation}
			}
			if body == "" {
				return &cliError{Message: "comment body is required (--body, --body-file, --body-stdin)", ExitCode: ExitValidation}
			}
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/comments", baseURL, pid, n),
				map[string]any{"actor": actor, "body": body})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "comment appended")
			return nil
		},
	}
	cmd.Flags().StringVar(&src.Body, "body", "", "comment body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")
	return cmd
}
```

```go
// cmd/kata/edit.go
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
	var (
		title string
		body  string
		owner string
	)
	cmd := &cobra.Command{
		Use:   "edit <number>",
		Short: "edit issue title/body/owner",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			payload := map[string]any{"actor": ""}
			if title != "" {
				payload["title"] = title
			}
			if body != "" {
				payload["body"] = body
			}
			if owner != "" {
				payload["owner"] = owner
			}
			if len(payload) == 1 {
				return &cliError{Message: "pass at least one of --title, --body, --owner", ExitCode: ExitValidation}
			}
			actor, _ := resolveActor(flags.As, nil)
			payload["actor"] = actor

			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPatch,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, n),
				payload)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printMutation(cmd, bs)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&body, "body", "", "new body")
	cmd.Flags().StringVar(&owner, "owner", "", "new owner")
	return cmd
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/edit.go cmd/kata/comment.go cmd/kata/comment_test.go
git commit -m "feat(cli): kata edit + kata comment"
```

---

### Task 26: `cmd/kata/close.go` + `cmd/kata/reopen.go`

Spec refs: §3.4, §6.1. Both call the corresponding `actions/{close,reopen}` endpoint with `{actor, reason?}`.

**Files:**
- Replace stubs: `cmd/kata/close.go`, `cmd/kata/reopen.go`
- Test: `cmd/kata/close_reopen_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/close_reopen_test.go
package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestCloseReopen_RoundTrip(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	pid := resolvePIDViaHTTP(t, env.URL, dir)
	resp, err := http.Post(env.URL+"/api/v1/projects/"+itoa(pid)+"/issues",
		"application/json", bytesNewReader([]byte(`{"actor":"x","title":"x"}`)))
	require.NoError(t, err)
	_ = resp.Body.Close()

	close := newRootCmd()
	var bclose bytes.Buffer
	close.SetOut(&bclose)
	close.SetArgs([]string{"--workspace", dir, "close", "1", "--reason", "wontfix"})
	close.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, close.Execute())
	assert.True(t, strings.Contains(bclose.String(), "closed"))

	reopen := newRootCmd()
	var bo bytes.Buffer
	reopen.SetOut(&bo)
	reopen.SetArgs([]string{"--workspace", dir, "reopen", "1"})
	reopen.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, reopen.Execute())
	assert.True(t, strings.Contains(bo.String(), "open"))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/close.go
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newCloseCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "close <number>",
		Short: "close an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd, args[0], "close", map[string]any{"reason": reason})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "done", "done|wontfix|duplicate")
	return cmd
}

// reopen.go
func newReopenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reopen <number>",
		Short: "reopen an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd, args[0], "reopen", nil)
		},
	}
	return cmd
}

func runAction(cmd *cobra.Command, raw, action string, extra map[string]any) error {
	ctx := cmd.Context()
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
	}
	start, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return err
	}
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	pid, err := resolveProjectID(ctx, baseURL, start)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	body := map[string]any{"actor": actor}
	for k, v := range extra {
		body[k] = v
	}
	client, err := httpClientFor(baseURL)
	if err != nil {
		return err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/%s", baseURL, pid, n, action),
		body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printMutation(cmd, bs)
}
```

`close.go` and `reopen.go` may share `runAction` — declare it once in `close.go` or pull into `helpers.go` if you prefer. Don't duplicate.

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/close.go cmd/kata/reopen.go cmd/kata/close_reopen_test.go
git commit -m "feat(cli): kata close + reopen"
```

---

### Task 27: `cmd/kata/whoami.go` + `cmd/kata/health.go` + `cmd/kata/projects.go`

Spec refs: §5.2 (whoami), §6.1 (health, projects).

**Files:**
- Replace stubs: `cmd/kata/whoami.go`, `cmd/kata/health.go`, `cmd/kata/projects.go`
- Test: `cmd/kata/diagnostic_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/kata/diagnostic_test.go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestWhoami_FlagOverride(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"whoami", "--as", "claude-4.7"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "claude-4.7")
	assert.Contains(t, out, "flag")
}

func TestHealth_PrintsSchemaVersion(t *testing.T) {
	env := testenv.New(t)
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"health"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "schema_version=1")
}

func TestProjectsList_PrintsKnown(t *testing.T) {
	env := testenv.New(t)
	dir := initBoundWorkspace(t, env.URL, "https://github.com/wesm/kata.git")
	_ = dir

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "list"})
	cmd.SetContext(contextWithBaseURL(context.Background(), env.URL))
	require.NoError(t, cmd.Execute())
	assert.True(t, strings.Contains(buf.String(), "github.com/wesm/kata"))
}
```

- [ ] **Step 2: Run test (expect failure)**

Run: `go test ./cmd/kata/...`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// cmd/kata/whoami.go
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "show resolved actor and source",
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, source := resolveActor(flags.As, nil)
			if flags.JSON {
				bs, _ := json.Marshal(map[string]string{"actor": actor, "source": source})
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "actor=%s source=%s\n", actor, source)
			return nil
		},
	}
}

// health.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "report daemon health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, baseURL+"/api/v1/health", nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			var b struct {
				OK            bool
				SchemaVersion int    `json:"schema_version"`
				Uptime        string `json:"uptime"`
				DBPath        string `json:"db_path"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok=%v schema_version=%d uptime=%s db=%s\n",
				b.OK, b.SchemaVersion, b.Uptime, b.DBPath)
			return nil
		},
	}
}

// projects.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "projects", Short: "list and inspect kata projects"}
	cmd.AddCommand(projectsListCmd(), projectsShowCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list known projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, baseURL+"/api/v1/projects", nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), string(bs))
				return nil
			}
			var b struct {
				Projects []struct {
					ID              int64
					Identity, Name  string
					NextIssueNumber int64 `json:"next_issue_number"`
				}
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, p := range b.Projects {
				fmt.Fprintf(cmd.OutOrStdout(), "%d  %s  (%s, next #%d)\n",
					p.ID, p.Identity, p.Name, p.NextIssueNumber)
			}
			return nil
		},
	}
}

func projectsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "project id must be an integer", ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d", baseURL, id), nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(bs))
			return nil
		},
	}
}
```

- [ ] **Step 4: Run test (expect pass)**

Run: `go test ./cmd/kata/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
make lint
git add cmd/kata/whoami.go cmd/kata/health.go cmd/kata/projects.go cmd/kata/diagnostic_test.go
git commit -m "feat(cli): kata whoami + health + projects list/show"
```

---

### Task 28: End-to-end smoke test

Spec refs: all of Plan 1. One test exercising the full lifecycle: bootstrap → init → create → list → show → comment → close → reopen → projects list.

**Files:**
- Create: `e2e/e2e_test.go`

- [ ] **Step 1: Write the failing test**

```go
// e2e/e2e_test.go
package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/kata/internal/testenv"
)

func TestSmoke_FullLifecycle(t *testing.T) {
	env := testenv.New(t)
	dir := initRepo(t, "https://github.com/wesm/system.git")

	// 1. init via HTTP (instead of spawning the kata binary).
	requireOK(t, postJSON(t, env.URL+"/api/v1/projects",
		map[string]any{"start_path": dir}))

	// 2. resolve to learn project id.
	pid := resolvePID(t, env.URL, dir)

	// 3. create issue.
	resp := postJSON(t, env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues",
		map[string]any{"actor": "agent", "title": "first", "body": "details"})
	requireOK(t, resp)

	// 4. list.
	listResp, err := http.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues")
	require.NoError(t, err)
	defer listResp.Body.Close()
	listBody := readAll(t, listResp.Body)
	assert.Contains(t, listBody, `"title":"first"`)

	// 5. comment.
	requireOK(t, postJSON(t, env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/comments",
		map[string]any{"actor": "agent", "body": "looks good"}))

	// 6. close.
	requireOK(t, postJSON(t, env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/close",
		map[string]any{"actor": "agent", "reason": "done"}))

	// 7. reopen.
	requireOK(t, postJSON(t, env.URL+"/api/v1/projects/"+strconv.FormatInt(pid, 10)+"/issues/1/actions/reopen",
		map[string]any{"actor": "agent"}))

	// 8. show with comments.
	showResp, err := http.Get(env.URL + "/api/v1/projects/" + strconv.FormatInt(pid, 10) + "/issues/1")
	require.NoError(t, err)
	defer showResp.Body.Close()
	showBody := readAll(t, showResp.Body)
	assert.Contains(t, showBody, `"body":"looks good"`)
	assert.Contains(t, showBody, `"status":"open"`)

	_ = filepath.Base(dir) // silence unused if path utilities trim
	_ = strings.Contains   // ditto
	_ = context.Background
}

// helpers

func initRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "--quiet").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", origin).Run())
	return dir
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	bs := mustJSON(t, body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(bs))
	require.NoError(t, err)
	return resp
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	bs, err := jsonMarshal(v)
	require.NoError(t, err)
	return bs
}

func jsonMarshal(v any) ([]byte, error) { return jsonMarshaller(v) }

var jsonMarshaller = func(v any) ([]byte, error) {
	// inline import-free wrapper around encoding/json to keep imports in one place.
	type m = any
	return jsonInner(v)
}

// stub; implementer should just `import "encoding/json"` and use json.Marshal directly.
func jsonInner(v any) ([]byte, error) { panic("replace with json.Marshal") }

func requireOK(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	require.Equalf(t, 200, resp.StatusCode, "body: %s", readAll(t, resp.Body))
}

func resolvePID(t *testing.T, baseURL, dir string) int64 {
	t.Helper()
	resp := postJSON(t, baseURL+"/api/v1/projects/resolve", map[string]any{"start_path": dir})
	defer resp.Body.Close()
	bs := readAll(t, resp.Body)
	idx := strings.Index(bs, `"id":`)
	require.NotEqual(t, -1, idx, bs)
	rest := bs[idx+len(`"id":`):]
	end := strings.IndexAny(rest, ",}")
	require.NotEqual(t, -1, end)
	pid, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	require.NoError(t, err)
	return pid
}

func readAll(t *testing.T, r interface{ Read(p []byte) (int, error) }) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
```

The implementer should clean this up using `encoding/json` and `io.ReadAll`; the stubs above are illustrative because the plan can't import these at planning time. The implementer should drop the stubs and use the standard library directly.

- [ ] **Step 2: Run test (expect failure if symbols missing, otherwise pass)**

Run: `go test ./e2e/...`
Expected: PASS once the test compiles cleanly.

- [ ] **Step 3: Commit**

```bash
make lint
make test
git add e2e/
git commit -m "test(e2e): full lifecycle smoke test"
```

---

### Task 29: Final self-review and tidy

Spec refs: writing-plans skill self-review checklist.

- [ ] **Step 1: Run the full suite**

```bash
make lint
make test
```

Expected: green.

- [ ] **Step 2: Verify deferred work didn't bit-rot**

The schema in 0001_init.sql contains tables (`links`, `issue_labels`, `purge_log`, `issues_fts`) and triggers that Plan 1 doesn't exercise. Smoke them with a trivial sanity check that they exist and accept their declared columns. Add a single test:

```go
// internal/db/schema_completeness_test.go
package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllSchemaTablesExist(t *testing.T) {
	d := openTestDB(t)
	wanted := []string{"projects", "project_aliases", "issues", "comments", "links", "issue_labels", "events", "purge_log", "meta", "issues_fts"}
	for _, name := range wanted {
		var n int
		err := d.QueryRowContext(context.Background(),
			`SELECT 1 FROM sqlite_master WHERE name = ?`, name).Scan(&n)
		require.NoError(t, err, name)
		assert.Equal(t, 1, n, name)
	}
}
```

Run: `go test ./internal/db/...`
Expected: PASS.

- [ ] **Step 3: Smoke-test the binary directly**

```bash
make build
./kata daemon status
./kata --help
```

Expected: clean exit, no usage errors. Daemon-status should report "no daemon" (we didn't start one).

- [ ] **Step 4: Verify go.mod has no `// indirect` for deps Plan 1 actively uses**

```bash
go mod tidy
git diff go.mod go.sum
```

`testify`, `cobra`, `huma/v2`, `BurntSushi/toml`, `modernc.org/sqlite`, `mattn/go-isatty` (from CSRF guard if we ended up needing it; otherwise still indirect — that's fine) should all be **direct** at this point.

- [ ] **Step 5: Commit any tidy changes**

```bash
git add go.mod go.sum internal/db/schema_completeness_test.go
git commit -m "chore: tidy module file and verify schema completeness" || true
```

If there's nothing to commit, skip.

---

## Self-review checklist

Before declaring Plan 1 complete:

1. **Spec coverage:** every endpoint in §4.1 used by Plan 1 (`/ping`, `/health`, `POST /projects`, `POST /projects/resolve`, `GET /projects`, `GET /projects/{id}`, `POST/GET/PATCH /projects/{id}/issues`, `GET /projects/{id}/issues/{number}`, `POST /projects/{id}/issues/{number}/comments`, `POST /projects/{id}/issues/{number}/actions/close|reopen`) is registered and tested.
2. **Project resolution:** §2.4's strict-no-auto-create policy is enforced — only `kata init` creates project rows. Verified by `TestResolve_FailsOutsideKataTomlAndWithoutAlias` and `TestInit_FreshCloneFromExistingKataToml`.
3. **Identity binding:** `.kata.toml` v1 (version=1, [project] identity, name) round-trips through read/write. Alias verification triggers `project_alias_conflict` when `.kata.toml` declares P but the alias already points to Q.
4. **`KATA_HOME` precedence:** env > `~/.kata` default. `KATA_DB` overrides DB path independently.
5. **DATETIME columns:** all timestamp columns typed `DATETIME`; the round-trip-into-time.Time test passes.
6. **CSRF defense:** `Origin` rejection and `Content-Type` enforcement under test.
7. **CLI error → exit code mapping:** `mapStatusToExit` covers 400/404/409/412 → 3/4/5/6 with the rest as 1.
8. **Conventions:** all tests use testify (`require`/`assert`); no `t.Fatal`/`t.Error`. `t.TempDir()` everywhere. `make lint` clean.

---

## Execution Handoff

Plan complete. Two execution options:

**1. Subagent-Driven (recommended).** Dispatch a fresh subagent per task with the implementer/spec-reviewer/code-quality-reviewer loop. After every five completed tasks invoke `/roborev-fix` to clean up post-commit review findings.

**2. Inline Execution.** Execute tasks in this session using `superpowers:executing-plans` with batch checkpoints.

The user has authorized **option 1** ("perfect, yes, use opus with subagents and invoke roborev fix every 5 tasks for code review fixes"). Begin execution with Task 1 dispatched to a fresh implementer subagent.






