package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	cmd := &cobra.Command{
		Use:   "purge <number>",
		Short: "irreversibly remove an issue + all its rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
			}
			if !force {
				return &cliError{
					Message:  "purge requires --force; this is irreversible",
					Code:     "validation",
					ExitCode: ExitValidation,
				}
			}
			expected := fmt.Sprintf("PURGE #%d", n)
			confirm, err = resolvePurgeConfirm(cmd, confirm, expected)
			if err != nil {
				return err
			}
			return runDestructive(cmd, n, "purge", confirm, nil)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to perform the purge")
	cmd.Flags().StringVar(&confirm, "confirm", "", `exact confirmation string ("PURGE #N")`)
	return cmd
}

// resolvePurgeConfirm is like resolveConfirm (delete.go) but the interactive
// prompt requires the full "PURGE #N" string per spec §3.5; the asymmetry
// with delete (which only prompts for the bare number) is by design.
func resolvePurgeConfirm(cmd *cobra.Command, flagVal, expected string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if !isTTY(os.Stdin) {
		return "", &cliError{
			Message:  "no TTY: pass --confirm \"" + expected + "\" to proceed noninteractively",
			Code:     "confirm_required",
			ExitCode: ExitConfirm,
		}
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Type %q to confirm: ", expected); err != nil {
		return "", err
	}
	r := bufio.NewReader(cmd.InOrStdin())
	//nolint:errcheck // ReadString returns the data read up to EOF; an EOF here
	// just means the user closed stdin, which we treat as an empty mismatch.
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line != expected {
		return "", &cliError{
			Message:  "confirmation input did not match",
			Code:     "confirm_mismatch",
			ExitCode: ExitConfirm,
		}
	}
	return expected, nil
}
