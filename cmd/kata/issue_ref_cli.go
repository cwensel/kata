package main

import (
	"context"

	"github.com/spf13/cobra"
)

func resolveIssueRefForCommand(cmd *cobra.Command, ref string) (context.Context, string, int64, resolvedIssueRef, error) {
	return resolveIssueRefForCommandWithOptions(cmd, ref, false)
}

func resolveIssueRefForCommandWithOptions(cmd *cobra.Command, ref string, includeDeleted bool) (context.Context, string, int64, resolvedIssueRef, error) {
	ctx := cmd.Context()
	start, err := resolveStartPath(flags.Workspace)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	pid, err := resolveProjectID(ctx, baseURL, start)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	issue, err := resolveIssueRefWithOptions(ctx, baseURL, pid, ref, includeDeleted)
	if err != nil {
		return nil, "", 0, resolvedIssueRef{}, err
	}
	return ctx, baseURL, pid, issue, nil
}
