// Package main is the kata CLI entry point.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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

// runEEntered is set by PersistentPreRunE before any subcommand's RunE fires.
// It stays false when cobra fails during argument/flag parsing, allowing main()
// to distinguish a parse error (ExitUsage) from an operational failure (ExitInternal).
var runEEntered bool

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "kata",
		Short:         "kata — lightweight issue tracker for agents",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			runEEntered = true
			return nil
		},
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
		newDeleteCmd(),
		newRestoreCmd(),
		newPurgeCmd(),
		newLinkCmd(),
		newUnlinkCmd(),
		newParentCmd(),
		newUnparentCmd(),
		newBlockCmd(),
		newUnblockCmd(),
		newRelateCmd(),
		newUnrelateCmd(),
		newLabelCmd(),
		newLabelsCmd(),
		newAssignCmd(),
		newUnassignCmd(),
		newReadyCmd(),
		newWhoamiCmd(),
		newHealthCmd(),
		newProjectsCmd(),
	}
	cmd.AddCommand(subs...)
	return cmd
}

func main() {
	// Wire SIGINT/SIGTERM into cobra's command context so long-running
	// subcommands (notably `kata daemon start`) can shut down gracefully via
	// their deferred cleanups instead of being torn down mid-syscall. Once the
	// first signal arrives, restore default handling so a second ctrl-C
	// escalates to a hard kill (e.g. if a deferred cleanup hangs).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		var cli *cliError
		if errors.As(err, &cli) {
			fmt.Fprintln(os.Stderr, "kata:", cli.Message)
			os.Exit(cli.ExitCode)
		}
		fmt.Fprintln(os.Stderr, "kata:", err)
		os.Exit(exitCodeFor(err, runEEntered))
	}
}

// exitCodeFor maps a non-cliError ExecuteContext error to a CLI exit code
// based on whether RunE was reached. PersistentPreRunE flips runEEntered to
// true before any subcommand's RunE runs, so a false value means cobra
// rejected the invocation during arg/flag parsing.
func exitCodeFor(_ error, runEReached bool) int {
	if !runEReached {
		// Cobra failed before PersistentPreRunE — unknown command, missing
		// positional arg (cobra.ExactArgs / NoArgs), or bad flag value.
		return ExitUsage
	}
	// RunE entered and returned a plain error — operational failure (daemon
	// startup, HTTP transport, filesystem).
	return ExitInternal
}
