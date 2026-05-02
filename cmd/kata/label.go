package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func newLabelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "add or remove a label on an issue",
	}
	cmd.AddCommand(labelAddCmd(), labelRmCmd())
	return cmd
}

func labelAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <number> <label>",
		Short: "attach a label to an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
			}
			label := args[1]
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
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			payload := map[string]string{"actor": actor, "label": label}
			postURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/labels", baseURL, pid, n)
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost, postURL, payload)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printLabelMutation(cmd, bs)
		},
	}
}

func labelRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <number> <label>",
		Short: "detach a label from an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
			}
			label := args[1]
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
			actor, _ := resolveActor(flags.As, nil)
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			deleteURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/labels/%s?actor=%s",
				baseURL, pid, n, url.PathEscape(label), url.QueryEscape(actor))
			status, bs, err := httpDoJSON(ctx, client, http.MethodDelete, deleteURL, nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			return printLabelRemoved(cmd, bs, n, label)
		},
	}
}

func newLabelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "labels",
		Short: "list label counts in this project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d/labels", baseURL, pid), nil)
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
				Labels []struct {
					Label string `json:"label"`
					Count int64  `json:"count"`
				} `json:"labels"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, c := range b.Labels {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %d\n", c.Label, c.Count); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// printLabelMutation formats AddLabelResponse for the three output modes.
func printLabelMutation(cmd *cobra.Command, bs []byte) error {
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
			Number int64 `json:"number"`
		} `json:"issue"`
		Label struct {
			Label string `json:"label"`
		} `json:"label"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already labeled %q (no-op)\n", b.Issue.Number, b.Label.Label)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d labeled %q\n", b.Issue.Number, b.Label.Label)
	return err
}

// printLabelRemoved formats the DELETE-label response. The MutationResponse
// body carries only {issue, event, changed} so the line is built from the
// (issue number, label) the CLI used to call DELETE.
func printLabelRemoved(cmd *cobra.Command, bs []byte, number int64, label string) error {
	if flags.JSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
		return err
	}
	var b struct {
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d label %q already removed (no-op)\n", number, label)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d unlabeled %q\n", number, label)
	return err
}
