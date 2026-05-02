package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

// newRestoreCmd returns the cobra.Command for `kata restore`.
//
// Restore is the simplest of the destructive verbs (spec §3.5 step 4): no
// --force, no --confirm, no TTY prompt. POSTs to /actions/restore with just
// an actor; the daemon-side RestoreIssue is idempotent and returns
// changed=false when the issue isn't deleted.
func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <number>",
		Short: "restore a soft-deleted issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
			start, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return err
			}
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			pid, err := resolveProjectID(ctx, baseURL, start)
			if err != nil {
				return err
			}
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/restore", baseURL, pid, n),
				map[string]any{"actor": actor})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if !flags.Quiet && !flags.JSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d restored\n", n)
				return err
			}
			return printMutation(cmd, bs)
		},
	}
}
