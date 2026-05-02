package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var status string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list issues in this project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit <= 0 {
				return &cliError{Message: "--limit must be a positive integer", Kind: kindValidation, ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
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
			// "all" is a CLI sentinel meaning "no filter"; the server expects
			// an empty status to return both open and closed.
			apiStatus := status
			if apiStatus == "all" {
				apiStatus = ""
			}
			url := fmt.Sprintf("%s/api/v1/projects/%d/issues?status=%s&limit=%d", baseURL, pid, apiStatus, limit)
			httpStatus, bs, err := httpDoJSON(ctx, client, http.MethodGet, url, nil)
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
				Issues []struct {
					Number int64  `json:"number"`
					Title  string `json:"title"`
					Status string `json:"status"`
					Author string `json:"author"`
				} `json:"issues"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, i := range b.Issues {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "#%-4d  %-8s  %s  (%s)\n", i.Number, i.Status, i.Title, i.Author); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "open", "filter by status: open|closed|all")
	cmd.Flags().IntVar(&limit, "limit", 50, "max rows")
	return cmd
}
