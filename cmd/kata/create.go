package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var src BodySources
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "create a new issue",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&src.Body, "body", "", "issue body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")

	// RunE is set after flag registration so we can reference cmd.Flags().Changed.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		src.BodySet = cmd.Flags().Changed("body")
		src.FileSet = cmd.Flags().Changed("body-file")

		ctx := cmd.Context()
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return err
		}
		baseURL, err := ensureDaemon(ctx)
		if err != nil {
			return err
		}
		projectID, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return err
		}
		body, err := resolveBody(src, cmd.InOrStdin())
		if err != nil {
			return &cliError{Message: err.Error(), ExitCode: ExitValidation}
		}
		actor, _ := resolveActor(flags.As, nil)
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}
		status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
			fmt.Sprintf("%s/api/v1/projects/%d/issues", baseURL, projectID),
			map[string]any{"actor": actor, "title": args[0], "body": body})
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

// resolveProjectID resolves the project ID for a given workspace start path
// by calling POST /api/v1/projects/resolve on the daemon.
func resolveProjectID(ctx context.Context, baseURL, startPath string) (int64, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return 0, err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
		baseURL+"/api/v1/projects/resolve",
		map[string]any{"start_path": startPath})
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, apiErrFromBody(status, bs)
	}
	var b struct {
		Project struct{ ID int64 } `json:"project"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return 0, err
	}
	return b.Project.ID, nil
}

// printMutation formats a mutation response (issue create/edit/close/reopen)
// according to the active output mode: JSON envelope, quiet (issue number
// only), or human-readable one-liner.
func printMutation(cmd *cobra.Command, bs []byte) error {
	var b struct {
		Issue struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"issue"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	if flags.Quiet {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), b.Issue.Number)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d %s [%s]\n", b.Issue.Number, b.Issue.Title, b.Issue.Status)
	return err
}
