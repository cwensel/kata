package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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

type eventsTailOptions struct {
	ProjectIDArg int64
	AllProjects  bool
	LastEventID  int64
}

const (
	tailBackoffStart = 1 * time.Second
	tailBackoffMax   = 30 * time.Second
)

func runEventsTail(cmd *cobra.Command, opts eventsTailOptions) error {
	ctx := cmd.Context()
	baseURL, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	url, err := tailURL(ctx, baseURL, opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	cursor := opts.LastEventID
	backoff := tailBackoffStart

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		readAny, err := streamOnce(ctx, client, url, cursor, out)
		if err != nil {
			if !flags.Quiet {
				fmt.Fprintln(os.Stderr, "kata: stream error:", err, "(reconnecting in", backoff.Round(time.Second), ")")
			}
		}
		switch v := readAny.(type) {
		case streamResetSignal:
			cursor = v.newCursor
			backoff = tailBackoffStart
			continue
		case streamProgress:
			if v.lastID > cursor {
				cursor = v.lastID
				backoff = tailBackoffStart
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < tailBackoffMax {
			backoff *= 2
			if backoff > tailBackoffMax {
				backoff = tailBackoffMax
			}
		}
	}
}

type streamResetSignal struct{ newCursor int64 }
type streamProgress struct{ lastID int64 }

func streamOnce(ctx context.Context, client *http.Client, baseURL string, cursor int64, out io.Writer) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return streamProgress{lastID: cursor}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if cursor > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	}
	resp, err := client.Do(req) //nolint:gosec // baseURL comes from daemon discovery
	if err != nil {
		return streamProgress{lastID: cursor}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		bs, _ := io.ReadAll(resp.Body)
		return streamProgress{lastID: cursor}, fmt.Errorf("http %d: %s", resp.StatusCode, string(bs))
	}

	rd := bufio.NewReader(resp.Body)
	var (
		curID    string
		curEvent string
		curData  string
	)
	flushFrame := func() (any, bool, error) {
		defer func() { curID, curEvent, curData = "", "", "" }()
		if curEvent == "" && curData == "" && curID == "" {
			return nil, false, nil
		}
		switch curEvent {
		case "sync.reset_required":
			var r struct {
				ResetAfterID int64 `json:"reset_after_id"`
			}
			if err := json.Unmarshal([]byte(curData), &r); err != nil {
				return nil, false, fmt.Errorf("parse reset frame: %w", err)
			}
			env := map[string]any{
				"reset_required": true,
				"reset_after_id": r.ResetAfterID,
			}
			line, _ := json.Marshal(env)
			if _, err := fmt.Fprintln(out, string(line)); err != nil {
				return nil, false, err
			}
			return streamResetSignal{newCursor: r.ResetAfterID}, true, nil
		default:
			if _, err := fmt.Fprintln(out, curData); err != nil {
				return nil, false, err
			}
			n, _ := strconv.ParseInt(curID, 10, 64)
			return streamProgress{lastID: n}, false, nil
		}
	}

	progress := streamProgress{lastID: cursor}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return progress, nil
			}
			return progress, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			res, terminal, err := flushFrame()
			if err != nil {
				return progress, err
			}
			if reset, ok := res.(streamResetSignal); ok {
				return reset, nil
			}
			if p, ok := res.(streamProgress); ok && p.lastID > 0 {
				progress = p
			}
			if terminal {
				return progress, nil
			}
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat — ignore
		case strings.HasPrefix(line, "id: "):
			curID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			curData = strings.TrimPrefix(line, "data: ")
		}
	}
}

func tailURL(ctx context.Context, baseURL string, opts eventsTailOptions) (string, error) {
	switch {
	case opts.AllProjects:
		return baseURL + "/api/v1/events/stream", nil
	case opts.ProjectIDArg != 0:
		return fmt.Sprintf("%s/api/v1/events/stream?project_id=%d", baseURL, opts.ProjectIDArg), nil
	default:
		start, err := resolveStartPath(flags.Workspace)
		if err != nil {
			return "", err
		}
		pid, err := resolveProjectID(ctx, baseURL, start)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s/api/v1/events/stream?project_id=%d", baseURL, pid), nil
	}
}
