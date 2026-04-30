package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "projects", Short: "list and inspect kata projects"}
	cmd.AddCommand(projectsListCmd(), projectsShowCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list known projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, baseURL+"/api/v1/projects", nil)
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
			var b struct {
				Projects []struct {
					ID              int64  `json:"id"`
					Identity        string `json:"identity"`
					Name            string `json:"name"`
					NextIssueNumber int64  `json:"next_issue_number"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, p := range b.Projects {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%d  %s  (%s, next #%d)\n",
					p.ID, p.Identity, p.Name, p.NextIssueNumber); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func projectsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "project id must be an integer", ExitCode: ExitValidation}
			}
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d", baseURL, id), nil)
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
			var b struct {
				Project struct {
					ID              int64  `json:"id"`
					Identity        string `json:"identity"`
					Name            string `json:"name"`
					NextIssueNumber int64  `json:"next_issue_number"`
				} `json:"project"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d %s (%s, next #%d)\n",
				b.Project.ID, b.Project.Identity, b.Project.Name, b.Project.NextIssueNumber)
			return err
		},
	}
}
