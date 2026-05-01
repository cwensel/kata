// Package tui implements the kata terminal UI built on Bubble Tea.
package tui

import (
	"context"
	"errors"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Options controls TUI behavior. Stable across versions; new fields
// must be optional.
type Options struct {
	AllProjects    bool
	IncludeDeleted bool
	Stdout         io.Writer // typically os.Stdout
	Stderr         io.Writer // typically os.Stderr
}

// Run starts the TUI. Blocks until the user quits or ctx is cancelled.
// Returns nil on clean exit. Returns errNotATTY when stdin/stdout are
// not a terminal so callers can print a friendly message.
func Run(ctx context.Context, opts Options) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errNotATTY
	}
	m := initialModel(opts)
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(), // future-proof; ignored by current handlers
	)
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// errNotATTY indicates the TUI was launched outside a terminal.
var errNotATTY = errors.New("kata tui requires a terminal (stdin/stdout must be a tty)")

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
