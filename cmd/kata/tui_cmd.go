package main

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/tui"
)

// newTUICmd registers the TUI command. --all-projects is intentionally
// absent today: the daemon has no cross-project list endpoint
// (handlers_issues.go only registers the project-scoped route), so
// advertising the flag would land the user on a 404. The all-projects
// surface is gated end-to-end (this flag, the R toggle, and the boot-
// fallback path) until the daemon ships GET /issues for cross-project
// reads.
func newTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "open the interactive issue browser",
		Long: `kata tui opens a Bubble Tea TUI scoped to the current project (per .kata.toml).
Press ? for help, q to quit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return tui.Run(ctx, tui.Options{
				Stdout: cmd.OutOrStdout(),
				Stderr: cmd.ErrOrStderr(),
			})
		},
	}
	return cmd
}
