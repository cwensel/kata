package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newCommentCmd() *cobra.Command {
	var src BodySources
	cmd := &cobra.Command{
		Use:   "comment <number>",
		Short: "append a comment to an issue",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&src.Body, "body", "", "comment body")
	cmd.Flags().StringVar(&src.File, "body-file", "", "read body from file")
	cmd.Flags().BoolVar(&src.Stdin, "body-stdin", false, "read body from stdin")

	// RunE is set after flag registration so we can reference cmd.Flags().Changed.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		src.BodySet = cmd.Flags().Changed("body")
		src.FileSet = cmd.Flags().Changed("body-file")

		ctx := cmd.Context()
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
		}
		body, err := resolveBody(src, cmd.InOrStdin())
		if err != nil {
			code := ExitValidation
			if strings.HasPrefix(err.Error(), "must pass exactly one of") {
				code = ExitUsage
			}
			return &cliError{Message: err.Error(), Kind: kindForExit(code), ExitCode: code}
		}
		if strings.TrimSpace(body) == "" {
			return &cliError{Message: "comment body is required (--body, --body-file, --body-stdin)", Kind: kindValidation, ExitCode: ExitValidation}
		}
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
		actor, _ := resolveActor(flags.As, nil)
		client, err := httpClientFor(ctx, baseURL)
		if err != nil {
			return err
		}
		status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
			fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/comments", baseURL, pid, n),
			map[string]any{"actor": actor, "body": body})
		if err != nil {
			return err
		}
		if status >= 400 {
			return apiErrFromBody(status, bs)
		}
		if flags.JSON {
			var buf bytes.Buffer
			if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
				return err
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
			return err
		}
		if !flags.Quiet {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "comment appended")
			return err
		}
		return nil
	}
	return cmd
}
