package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// newSearchCmd returns the cobra.Command for `kata search`. It calls the
// daemon's GET /search endpoint and prints either the JSON envelope (under
// --json) or one line per hit in `#N <score> <status>  <title>  (<matched_in>)`.
func newSearchCmd() *cobra.Command {
	var limit int
	var includeDeleted bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "search issues by title/body/comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			if strings.TrimSpace(query) == "" {
				return &cliError{Message: "query must be non-empty", ExitCode: ExitValidation}
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
			searchURL := buildSearchURL(baseURL, pid, query, limit, includeDeleted)
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, searchURL, nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printSearchResults(cmd, bs)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include soft-deleted issues")
	return cmd
}

// buildSearchURL assembles the GET /search request URL with q, optional limit,
// and optional include_deleted query params.
func buildSearchURL(baseURL string, pid int64, query string, limit int, includeDeleted bool) string {
	q := url.Values{}
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if includeDeleted {
		q.Set("include_deleted", "true")
	}
	return fmt.Sprintf("%s/api/v1/projects/%d/search?%s", baseURL, pid, q.Encode())
}

// printSearchResults renders a search response in the active output mode:
// JSON envelope, human-readable list, or "no matches" when empty.
func printSearchResults(cmd *cobra.Command, bs []byte) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Results []struct {
			Issue struct {
				Number int64  `json:"number"`
				Title  string `json:"title"`
				Status string `json:"status"`
			} `json:"issue"`
			Score     float64  `json:"score"`
			MatchedIn []string `json:"matched_in"`
		} `json:"results"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if len(b.Results) == 0 {
		if flags.Quiet {
			return nil
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no matches")
		return err
	}
	for _, r := range b.Results {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "#%-4d  %.2f  %-8s  %s  (%s)\n",
			r.Issue.Number, r.Score, r.Issue.Status, r.Issue.Title,
			strings.Join(r.MatchedIn, ",")); err != nil {
			return err
		}
	}
	return nil
}
