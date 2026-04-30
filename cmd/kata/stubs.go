package main

import "github.com/spf13/cobra"

func newCreateCmd() *cobra.Command {
	return &cobra.Command{Use: "create <title>", Short: "create issue"}
}

func newShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show <number>", Short: "show issue"}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "list issues"}
}

func newEditCmd() *cobra.Command {
	return &cobra.Command{Use: "edit <number>", Short: "edit issue"}
}

func newCommentCmd() *cobra.Command {
	return &cobra.Command{Use: "comment <number>", Short: "comment on issue"}
}

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
