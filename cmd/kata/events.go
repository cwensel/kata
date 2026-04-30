package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

func newEventsCmd() *cobra.Command {
	var (
		tail         bool
		projectIDArg int64
		allProjects  bool
		afterID      int64
		lastEventID  int64
		limit        int
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "list or stream events",
		Long: `kata events lists recent events. With --tail, it streams them live over SSE.

Without --tail, prints up to --limit events ordered by id ASC and exits.
With --tail, opens an SSE connection and emits one NDJSON envelope per line
until SIGINT/SIGTERM. Reconnects with exponential backoff on disconnect.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if allProjects && projectIDArg != 0 {
				return &cliError{Message: "--all-projects and --project-id are mutually exclusive", ExitCode: ExitUsage}
			}
			if tail {
				return runEventsTail(cmd, eventsTailOptions{
					ProjectIDArg: projectIDArg,
					AllProjects:  allProjects,
					LastEventID:  lastEventID,
				})
			}
			return runEventsPoll(cmd, eventsPollOptions{
				ProjectIDArg: projectIDArg,
				AllProjects:  allProjects,
				AfterID:      afterID,
				Limit:        limit,
			})
		},
	}
	cmd.Flags().BoolVar(&tail, "tail", false, "stream events live over SSE")
	cmd.Flags().Int64Var(&projectIDArg, "project-id", 0, "scope to a specific project id")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "use the cross-project endpoint")
	cmd.Flags().Int64Var(&afterID, "after", 0, "polling cursor (one-shot mode)")
	cmd.Flags().Int64Var(&lastEventID, "last-event-id", 0, "resume cursor (--tail mode)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows in one-shot mode")
	return cmd
}

type eventsPollOptions struct {
	ProjectIDArg int64
	AllProjects  bool
	AfterID      int64
	Limit        int
}

func runEventsPoll(cmd *cobra.Command, opts eventsPollOptions) error {
	ctx := cmd.Context()
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	url, err := pollURL(ctx, baseURL, opts)
	if err != nil {
		return err
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, url, nil)
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
	return printEventsHuman(cmd, bs)
}

func pollURL(ctx context.Context, baseURL string, opts eventsPollOptions) (string, error) {
	switch {
	case opts.AllProjects:
		return fmt.Sprintf("%s/api/v1/events?after_id=%d&limit=%d", baseURL, opts.AfterID, opts.Limit), nil
	case opts.ProjectIDArg != 0:
		return fmt.Sprintf("%s/api/v1/projects/%d/events?after_id=%d&limit=%d",
			baseURL, opts.ProjectIDArg, opts.AfterID, opts.Limit), nil
	default:
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return "", err
		}
		pid, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s/api/v1/projects/%d/events?after_id=%d&limit=%d",
			baseURL, pid, opts.AfterID, opts.Limit), nil
	}
}

func printEventsHuman(cmd *cobra.Command, bs []byte) error {
	var b struct {
		ResetRequired bool  `json:"reset_required"`
		ResetAfterID  int64 `json:"reset_after_id"`
		Events        []struct {
			EventID     int64  `json:"event_id"`
			Type        string `json:"type"`
			ProjectID   int64  `json:"project_id"`
			IssueNumber *int64 `json:"issue_number"`
			Actor       string `json:"actor"`
			CreatedAt   string `json:"created_at"`
		} `json:"events"`
		NextAfterID int64 `json:"next_after_id"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if b.ResetRequired {
		_, err := fmt.Fprintf(cmd.OutOrStdout(),
			"reset_required: refetch state and resume from %d\n", b.ResetAfterID)
		return err
	}
	for _, e := range b.Events {
		issueStr := "-"
		if e.IssueNumber != nil {
			issueStr = "#" + strconv.FormatInt(*e.IssueNumber, 10)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(),
			"%-6d  %-22s  proj=%-3d  %-6s  by %s  %s\n",
			e.EventID, e.Type, e.ProjectID, issueStr, e.Actor, e.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// eventsTailOptions and runEventsTail are stubs filled in by Task 11.
type eventsTailOptions struct {
	ProjectIDArg int64
	AllProjects  bool
	LastEventID  int64
}

func runEventsTail(_ *cobra.Command, _ eventsTailOptions) error {
	return &cliError{Message: "kata events --tail not yet implemented", ExitCode: ExitInternal}
}
