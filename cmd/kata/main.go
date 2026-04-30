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
		// Operational RunE failures (daemon startup, HTTP transport, filesystem)
		// and cobra parsing failures both reach here. Default to ExitInternal so
		// a network or daemon failure isn't masked as ExitUsage. Subcommands that
		// detect explicit usage problems (bad flag value, missing required input)
		// should return *cliError{ExitCode: ExitUsage} themselves.
		fmt.Fprintln(os.Stderr, "kata:", err)
		os.Exit(ExitInternal)
	}
}
