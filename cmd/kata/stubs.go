package main

import "github.com/spf13/cobra"

func newCloseCmd() *cobra.Command {
	return &cobra.Command{Use: "close <number>", Short: "close issue"}
}

func newReopenCmd() *cobra.Command {
	return &cobra.Command{Use: "reopen <number>", Short: "reopen issue"}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{Use: "whoami", Short: "show resolved actor"}
}

func newHealthCmd() *cobra.Command {
	return &cobra.Command{Use: "health", Short: "daemon health"}
}

func newProjectsCmd() *cobra.Command {
	c := &cobra.Command{Use: "projects", Short: "list projects"}
	c.AddCommand(&cobra.Command{Use: "list"}, &cobra.Command{Use: "show"})
	return c
}
