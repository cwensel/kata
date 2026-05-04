package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newPurgeCmd returns the cobra.Command for `kata purge`.
//
// Spec §3.5 step 5: purge is irreversible and gated by --force plus an
// X-Kata-Confirm header whose value is the exact string "PURGE #N". The
// interactive friction is intentionally higher than delete — the prompt
// requires typing the full "PURGE #N" string, not just the number.
func newPurgeCmd() *cobra.Command {
	var force bool
	var confirm string
	var reason string
	cmd := &cobra.Command{
		Use:   "purge <issue-ref>",
		Short: "irreversibly remove an issue + all its rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return &cliError{
					Message: "purge requires --force; this is irreversible",
					Code:    "validation",
					Kind:    kindValidation, ExitCode: ExitValidation,
				}
			}
			_, _, _, issue, err := resolveIssueRefForCommandWithOptions(cmd, args[0], true)
			if err != nil {
				return err
			}
			expected := fmt.Sprintf("PURGE #%d", issue.Number)
			confirm, err = resolveConfirm(cmd, confirm, expected,
				fmt.Sprintf("Type %q to confirm: ", expected), confirmPromptFull)
			if err != nil {
				return err
			}
			var extra map[string]any
			if reason != "" {
				extra = map[string]any{"reason": reason}
			}
			return runDestructive(cmd, issue.Number, "purge", confirm, extra)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to perform the purge")
	cmd.Flags().StringVar(&confirm, "confirm", "", `exact confirmation string ("PURGE #N")`)
	cmd.Flags().StringVar(&reason, "reason", "", "free-text reason recorded in purge_log.reason")
	return cmd
}
