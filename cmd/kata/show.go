package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <number>",
		Short: "show issue + comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", ExitCode: ExitValidation}
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
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			httpStatus, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, n), nil)
			if err != nil {
				return err
			}
			if httpStatus >= 400 {
				return apiErrFromBody(httpStatus, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Issue struct {
					Number int64  `json:"number"`
					Title  string `json:"title"`
					Body   string `json:"body"`
					Status string `json:"status"`
					Author string `json:"author"`
				} `json:"issue"`
				Comments []struct {
					Author string `json:"author"`
					Body   string `json:"body"`
				} `json:"comments"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "#%d  %s  [%s]  by %s\n",
				b.Issue.Number, b.Issue.Title, b.Issue.Status, b.Issue.Author); err != nil {
				return err
			}
			if b.Issue.Body != "" {
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, b.Issue.Body); err != nil {
					return err
				}
			}
			if len(b.Comments) > 0 {
				if _, err := fmt.Fprintln(out, "\n--- comments ---"); err != nil {
					return err
				}
				for _, c := range b.Comments {
					if _, err := fmt.Fprintf(out, "%s: %s\n", c.Author, c.Body); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}
