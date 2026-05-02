# kata かた

Small issue tracking for humans and agents working on code.

kata is a local-first issue tracker for AI-assisted software work. It gives
agents a structured place to record tasks, decisions, links, comments, and state
changes without turning GitHub Issues, markdown plans, or chat transcripts into
the source of truth.

The CLI is built for agents and automation: stable commands, JSON output, and
predictable failure modes. The TUI is built for people: browse, triage, edit,
and supervise agent-written work without reading raw JSON. Both talk to the same
local daemon and SQLite database.

Status: early public preview. The CLI, daemon, and TUI are usable, but command
contracts and UI details may still change before a stable release.

## What kata does today

- Tracks issues separately per project, with issue numbers that restart per
  project.
- Binds a workspace to a project through `.kata.toml`, falling back to a git
  remote URL when no binding file exists.
- Stores data locally in SQLite under `KATA_HOME` and talks to it through a
  long-running daemon.
- Supports creating, listing, editing, closing, reopening, commenting,
  labeling, assigning, and linking issues.
- Records state changes as durable events for auditing, polling, live streams,
  hooks, and TUI updates.
- Provides search, idempotent create, soft delete, restore, and irreversible
  purge.
- Exposes JSON output for successful commands so agents can parse results
  reliably.
- Provides `kata tui` for human review and triage of the same issue ledger.

kata is intentionally small. It is not a project-management suite, a git
workflow engine, or an agent worker pool. It is a durable task ledger that
humans and agents can both understand.

## Goals

kata is designed around three priorities:

- Agent ergonomics: stable commands, JSON-first workflows, explicit workspace
  binding, search-before-create, idempotency keys, and predictable exit codes.
- Human oversight: a TUI that helps people browse, triage, edit, and supervise
  agent activity without reading raw JSON.
- Auditability: append-only comments, event history, actor attribution, and
  explicit destructive operations.

Longer term, kata should support a shared server mode for teams, CI, and
multiple agents. That future mode should be a real authenticated deployment,
not the local daemon exposed on a public interface.

## Install

```sh
make build
make install
```

`make install` places `kata` in `~/.local/bin`.

For development:

```sh
make test
```

## Quick Start

Initialize kata in a repository:

```sh
kata init
```

`kata init` creates or resolves the project and writes `.kata.toml` when needed.
With a git remote, the default project identity is derived from the remote URL.

For a non-git workspace or an explicit shared identity:

```sh
kata init --project github.com/example/product --name product
```

Create and inspect issues:

```sh
kata create "fix login race" --body "Safari can double-submit the callback."
kata list
kata show 1
kata comment 1 --body "Reproduced on macOS."
kata close 1 --reason done
```

Open the TUI for human triage:

```sh
kata tui
```

Press `?` inside the TUI for keybindings.

Use `--workspace <path>` when running from outside the project directory:

```sh
kata --workspace ~/code/product list --status all
```

Set the actor for a session:

```sh
export KATA_AUTHOR=codex-wesm-laptop
kata whoami
```

Actor precedence is `--as` > `KATA_AUTHOR` > `git config user.name` >
`anonymous`.

## Core Commands

Common issue commands:

```sh
kata create <title> [--body TEXT | --body-file PATH | --body-stdin]
                  [--label LABEL] [--owner NAME]
                  [--parent N] [--blocks N] [--idempotency-key KEY]
kata list [--status open|closed|all] [--limit N]
kata show <number>
kata edit <number> [--title TEXT] [--body TEXT] [--owner NAME]
kata comment <number> [--body TEXT | --body-file PATH | --body-stdin]
kata close <number> [--reason done|wontfix|duplicate]
kata reopen <number>
```

Labels, ownership, and relationships:

```sh
kata label add <number> <label>
kata label rm <number> <label>
kata labels
kata assign <number> <owner>
kata unassign <number>

kata parent <child> <parent> [--replace]
kata unparent <child>
kata block <blocker> <blocked>
kata unblock <blocker> <blocked>
kata relate <a> <b>
kata unrelate <a> <b>
kata link <from> <parent|blocks|related> <to>
kata unlink <from> <parent|blocks|related> <to>
```

Search, readiness, events, and project inspection:

```sh
kata search <query> [--limit N] [--include-deleted]
kata ready [--limit N]
kata events [--after N] [--limit N]
kata events --tail [--last-event-id N]
kata projects list
kata projects show <id>
```

Destructive operations are explicit:

```sh
kata delete <number> --force --confirm "DELETE #<number>"
kata restore <number>
kata purge <number> --force --confirm "PURGE #<number>"
```

`delete` is reversible. `purge` is not.

Daemon, diagnostics, and agent instructions:

```sh
kata daemon status
kata daemon stop
kata daemon reload
kata daemon logs --hooks [--tail]
kata health
kata whoami
kata quickstart
kata tui
```

## Agent Quickstart

This is the short version to give any coding agent, regardless of whether that
agent supports skills, memories, or custom instructions. It is also shipped with
the CLI:

```sh
kata quickstart
kata agent-instructions   # alias
```

1. Run from the project workspace, or pass `--workspace <path>`.
2. Set `KATA_AUTHOR` once at session start.
3. Prefer `--json` for reads and writes when you need to parse output.
4. Never create a project implicitly. If the workspace is not initialized,
   report that `kata init` is needed.
5. Search before creating:

```sh
kata search "login race" --json
```

6. If no existing issue fits, create with an idempotency key:

```sh
kata create "fix login race" \
  --body "Observed double-submit in Safari callback." \
  --idempotency-key "login-race-2026-05-02" \
  --json
```

7. Prefer updating existing issues over creating duplicates:

```sh
kata show 12 --json
kata comment 12 --body "Found another reproduction path." --json
kata label add 12 safari --json
kata block 12 18 --json
```

8. Use relationships deliberately. The link types mean:

| Type | Meaning |
|---|---|
| `parent` | This issue is part of a larger issue. |
| `blocks` | The first issue must be resolved before the second can proceed. |
| `related` | Useful context, but not ordering. |

9. Close only when the work is actually complete:

```sh
kata close 12 --reason done --json
```

10. Do not run `delete` or `purge` unless the user explicitly asks for that
    exact destructive action and issue number.

For long-running agents, poll events:

```sh
kata events --after 0 --limit 100 --json
```

Remember the returned cursor and resume from it. If a response says
`reset_required`, discard cached kata state and resume from the reset cursor.
For live streams:

```sh
kata events --tail
```

`--tail` emits newline-delimited JSON.

## Should agents learn kata through skills?

A quickstart should be the canonical teaching path because it works everywhere:
Codex, Claude, Cursor, shell scripts, CI jobs, and humans can all read the same
instructions.

Skills still make sense as an optional layer for agents that support them. A
good skill can remind the agent to use `--json`, search before create, set
`KATA_AUTHOR`, and avoid destructive commands. But skills should package the
same rules as the quickstart, not replace it. The durable source of truth should
stay in this README and the installed `kata quickstart` command.

## Sharing and multi-user workflows

Today kata is local-first:

- one local daemon;
- one local SQLite database;
- no authentication;
- trusted same-user CLI and TUI clients.

Multiple checkouts or repositories can share one kata project when they use the
same `.kata.toml` project identity and run `kata init` in each checkout. That
shares issue numbering, labels, links, and events across those workspaces in the
same local database.

Future shared mode should be a distinct deployment:

- a shared kata server reachable over HTTPS, SSH tunnel, or a private network;
- authenticated users and service tokens;
- server-derived actor identity;
- server-side hooks and backups;
- the same project, issue, event, and relationship model.

The local daemon should not be exposed directly to a LAN or public network.

## Why kata, and how is it different from Beads?

[Beads](https://github.com/gastownhall/beads) is a substantial tool in the same
space: a Dolt-powered distributed graph issue tracker for AI agents. Its default
shape is project-local: `bd init` creates a `.beads/` Dolt database alongside
the code, with native Dolt history, branching, merging, push/pull, and optional
server mode for concurrent writers. That does not mean Beads requires git; it
also supports stealth and git-free workflows.

kata exists because it makes a different architectural bet: the issue ledger
should be a local service adjacent to workspaces, not a database owned by each
repository. A repository that uses kata gets only a small, secret-free
`.kata.toml` binding; the canonical state lives in `KATA_HOME` behind a daemon
API. That keeps task state out of code history while still giving agents a
structured coordination layer and giving humans a TUI over the same event
stream.

It also has a different complexity budget. Beads is a large, capable system with
distributed database semantics, merge behavior, federation, MCP integration, and
agent workflow machinery. kata is deliberately smaller: one daemon, one local
store, one HTTP API, one TUI, and a narrow issue model that should stay easy to
understand, operate, and teach to agents.

| Design choice | Beads | kata |
|---|---|---|
| Storage boundary | Project-local `.beads/` Dolt database by default | User-local `KATA_HOME` SQLite database behind a daemon |
| Repository footprint | Owns issue state near the repo by default; can sync via Dolt remotes | Repo stores only `.kata.toml` project binding |
| Collaboration model | Dolt push/pull, Dolt server mode, federation, MCP tooling | Local daemon today; future authenticated shared server |
| IDs | Hash-based IDs by default; counter IDs optional | Per-project sequential numbers (`#12`) |
| Workflow shape | Rich graph tasks, priorities, claiming, messages, dependencies | Deliberately small issue ledger: status, comments, labels, owner, links, events |
| Git relationship | Git integration is optional but first-class; commit conventions and doctor checks can connect code history to issues | Git can help identify workspaces; kata does not infer issue state from commits |

Both approaches are useful. Beads is strongest when you want distributed,
database-versioned task memory that can travel with a project and merge across
branches or agents. kata is aimed at a smaller, API-first issue system that can
span workspaces and eventually teams without forcing every user and agent to
understand the repository, git remote, or distributed database that carries the
issue state.

## Configuration

Useful environment variables:

- `KATA_HOME`: data directory. Defaults to `~/.kata`.
- `KATA_DB`: explicit SQLite database path.
- `KATA_AUTHOR`: default actor for mutations.
- `XDG_RUNTIME_DIR`: runtime socket parent on Unix.

The workspace binding file is intentionally secret-free:

```toml
version = 1

[project]
identity = "github.com/example/product"
name = "product"
```

Commit `.kata.toml` when multiple agents, clones, or worktrees should resolve
to the same kata project.
