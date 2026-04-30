package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
	var (
		title string
		body  string
		owner string
	)
	cmd := &cobra.Command{
		Use:   "edit <number>",
		Short: "edit issue title/body/owner",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&body, "body", "", "new body")
	cmd.Flags().StringVar(&owner, "owner", "", "new owner")

	// RunE is set after flag registration so we can reference cmd.Flags().Changed.
	// This lets --body "" explicitly clear the body rather than being ignored.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
		}
		payload := map[string]any{}
		if cmd.Flags().Changed("title") {
			payload["title"] = title
		}
		if cmd.Flags().Changed("body") {
			payload["body"] = body
		}
		if cmd.Flags().Changed("owner") {
			payload["owner"] = owner
		}
		if len(payload) == 0 {
			return &cliError{Message: "pass at least one of --title, --body, --owner", ExitCode: ExitValidation}
		}
		actor, _ := resolveActor(flags.As, nil)
		payload["actor"] = actor

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
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}
		status, bs, err := httpDoJSON(ctx, client, http.MethodPatch,
			fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, n),
			payload)
		if err != nil {
			return err
		}
		if status >= 400 {
			return apiErrFromBody(status, bs)
		}
		return printMutation(cmd, bs)
	}
	return cmd
}
