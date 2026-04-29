// Package main is the kata CLI entry point.
package main

import "github.com/spf13/cobra"

// rootCmd is the base command; filled in by Task 20.
var rootCmd = &cobra.Command{
	Use:   "kata",
	Short: "kata — local issue tracker",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		rootCmd.PrintErrln(err)
	}
}
