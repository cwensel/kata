package main

import (
	"fmt"
	"net/http"

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
		Use:   "restore <issue-ref>",
		Short: "restore a soft-deleted issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, baseURL, pid, issue, err := resolveIssueRefForCommandWithOptions(cmd, args[0], true)
			if err != nil {
				return err
			}
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/restore", baseURL, pid, issue.Number),
				map[string]any{"actor": actor})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if !flags.Quiet && !flags.JSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d restored\n", issue.Number)
				return err
			}
			return printMutation(cmd, bs)
		},
	}
}
