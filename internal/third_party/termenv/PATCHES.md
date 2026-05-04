# Kata Local Patch

This directory vendors `github.com/muesli/termenv` v0.16.0 via the root
`go.mod` replace directive.

Local change:

- `termenv_unix.go`: skip OSC terminal status reports when `$TMUX` is set, even
  if `$TERM` does not start with `screen` or `tmux`.

Reason:

Some agent/superset shells run inside tmux while preserving an `xterm-*` TERM.
The upstream guard only checks TERM, so Bubble Tea's package init can trigger
an OSC 11 background-color query and a cursor-position query during ordinary
`kata --help` startup. In those shells the responses leak into the prompt and
make the bottom of command output appear swallowed.

