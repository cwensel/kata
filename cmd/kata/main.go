// Package main is the kata CLI entry point.
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

	subs := []*cobra.Command{
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
	}
	cmd.AddCommand(subs...)
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kata:", err)
		os.Exit(ExitInternal)
	}
}
