// Package hooks implements the daemon's post-commit hook dispatcher. See
// docs/superpowers/specs/2026-04-30-kata-hooks-design.md for the full
// design (delivery contract, dispatcher lifecycle, runner sequence, and
// stdin payload shape).
package hooks

import (
	"time"

	"github.com/wesm/kata/internal/db"
)

// ResolvedHook is one [[hook]] entry after parsing and validation. The
// Match func is precompiled at load so the dispatcher's hot path does no
// string comparisons beyond what Match does internally.
type ResolvedHook struct {
	Event      string            // canonical literal from TOML: "issue.created" | "issue.*" | "*"
	Match      func(string) bool // precompiled; tested against evt.Type
	Command    string            // absolute path or bare name (no '/')
	Args       []string
	Timeout    time.Duration
	WorkingDir string   // absolute, defaulted to $KATA_HOME at load
	UserEnv    []string // ["KEY=VAL", ...]; sorted by key for determinism
	Index      int      // 0-based source-file ordinal (used in output filenames)
}

// Snapshot is the live-reloadable hook set. Stored behind atomic.Pointer
// in the dispatcher; replaced wholesale on Reload.
type Snapshot struct {
	Hooks []ResolvedHook
}

// Config holds the [hooks] tunables. Startup-only in v1 — Reload may
// observe diffs but never applies them; see LoadedConfig.UnchangedTunables.
type Config struct {
	PoolSize             int
	QueueCap             int
	OutputDiskCap        int64
	RunsLogMaxBytes      int64
	RunsLogKeep          int
	QueueFullLogInterval time.Duration
}

// LoadedConfig bundles a parse result. UnchangedTunables is populated by
// LoadReload only — it lists human-readable strings like
// "pool_size: requested 8, active 4 (restart required)" so the daemon
// can log them without recomputing diffs itself.
type LoadedConfig struct {
	Snapshot          Snapshot
	Config            Config
	UnchangedTunables []string
}

// HookJob is the unit pushed onto the dispatcher queue. The hook is
// captured by value at enqueue time so the worker never re-reads the
// snapshot pointer; this is what makes Reload safe with in-flight jobs.
type HookJob struct {
	Event      db.Event
	Hook       ResolvedHook
	EnqueuedAt time.Time
}

// IssueSnapshot is the resolver output that fills the issue block of
// the stdin payload. Read at fire time, not enqueue time.
type IssueSnapshot struct {
	Number int64
	Title  string
	Status string
	Labels []string // sorted for determinism
	Owner  string
	Author string
}

// CommentSnapshot fills payload.comment_body for issue.commented events.
type CommentSnapshot struct {
	ID   int64
	Body string
}

// ProjectSnapshot fills project.name. project.id and project.identity
// come from the event itself, so a project resolver failure only drops
// the human-readable name.
type ProjectSnapshot struct {
	Name string
}

// AliasSnapshot fills the alias block when the event has a single
// well-defined workspace.
type AliasSnapshot struct {
	Identity string // marshalled as alias_identity
	Kind     string // marshalled as alias_kind ("git" | "local")
	RootPath string // marshalled as root_path
}
