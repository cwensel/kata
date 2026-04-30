package main

import (
	"os"

	"golang.org/x/term"
)

// isTTY reports whether f is a terminal device. Used to gate interactive
// prompts in delete/purge. Defaults to term.IsTerminal so non-terminal
// character devices (e.g. /dev/null, named pipes) are correctly classified
// as noninteractive — a plain os.ModeCharDevice check would treat /dev/null
// as a TTY and silently prompt under `kata delete --force < /dev/null`.
//
// Exposed as a package-level var so tests can swap a deterministic stub;
// running `go test` from a developer terminal otherwise sees the real
// stdin as a TTY, which would make non-TTY assertions flaky.
var isTTY = func(f *os.File) bool {
	//nolint:gosec // G115: file descriptors fit in int on every platform Go targets;
	// this is the canonical term.IsTerminal call shape.
	return term.IsTerminal(int(f.Fd()))
}
