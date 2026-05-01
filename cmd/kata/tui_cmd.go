package main

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/tui"
)

func newTUICmd() *cobra.Command {
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "open the interactive issue browser",
		Long: `kata tui opens a Bubble Tea TUI scoped to the current project (per .kata.toml)
or, with --all-projects, across every registered project. Press ? for help, q to quit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return tui.Run(ctx, tui.Options{
				AllProjects: allProjects,
				Stdout:      cmd.OutOrStdout(),
				Stderr:      cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().BoolVar(&allProjects, "all-projects", false,
		"browse across every registered project")
	return cmd
}
