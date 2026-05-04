package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/jsonl"
)

func newImportCmd() *cobra.Command {
	var input string
	var target string
	var force bool
	var newInstance bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "import a JSONL kata database export",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input == "" {
				return &cliError{Message: "import requires --input", Kind: kindValidation, ExitCode: ExitValidation}
			}
			if target == "" {
				return &cliError{Message: "import requires --target", Kind: kindValidation, ExitCode: ExitValidation}
			}
			if err := refuseRunningDaemonWithMessage(cmd.Context(),
				"daemon is running for this database; stop it before importing"); err != nil {
				return err
			}
			if _, err := os.Stat(target); err == nil && !force {
				return &cliError{
					Message:  "target already exists; pass --force to replace it",
					Kind:     kindValidation,
					ExitCode: ExitValidation,
				}
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("stat import target: %w", err)
			}
			if force {
				if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove import target: %w", err)
				}
			}
			in, err := os.Open(input) //nolint:gosec // import path is user-provided CLI input
			if err != nil {
				return fmt.Errorf("open import input: %w", err)
			}
			defer func() { _ = in.Close() }()
			d, err := db.Open(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer func() { _ = d.Close() }()
			if err := jsonl.ImportWithOptions(cmd.Context(), in, d, jsonl.ImportOptions{
				NewInstance: newInstance,
			}); err != nil {
				return err
			}
			if !flags.Quiet && !flags.JSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "imported %s\n", target)
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "path to JSONL export")
	cmd.Flags().StringVar(&target, "target", "", "database path to create")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing target database")
	cmd.Flags().BoolVar(&newInstance, "new-instance", false,
		"keep the target's fresh meta.instance_uid instead of overwriting it with the source's; preserves imported events' origin_instance_uid for federation loop-detection")
	return cmd
}
