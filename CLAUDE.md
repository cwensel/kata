# kata — agent guidance

## Project management

This project tracks its own work in **kata**. Run `kata quickstart` at the
start of each session for the agent contract; the short version:

- Set `KATA_AUTHOR` once per session.
- `kata list --json` to see open work; `kata show <N> --json` for detail.
- Search before creating: `kata search "<keywords>" --json`.
- Update existing issues over creating duplicates (`kata comment`,
  `kata label add`, `kata block`, `kata parent`).
- Close only when the work is actually complete: `kata close <N> --reason done`.
- Never `kata delete` or `kata purge` without explicit user authorization.

For long-running work, `kata events --tail` streams NDJSON.

## Specs and plans

- Design specs: `docs/superpowers/specs/`
- Implementation plans: `docs/superpowers/plans/`

The master spec is `docs/superpowers/specs/2026-04-29-kata-design.md`.
Future shared-server-mode design lives in
`docs/superpowers/specs/2026-04-29-kata-shared-server-mode.md`.
