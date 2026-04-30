package main

import "github.com/spf13/cobra"

func newReopenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <number>",
		Short: "reopen an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(cmd, args[0], "reopen", nil)
		},
	}
}
