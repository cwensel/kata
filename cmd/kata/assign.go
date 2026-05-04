package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newAssignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <issue-ref> <owner>",
		Short: "set the owner of an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssign(cmd, args[0], args[1], false)
		},
	}
}

func newUnassignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unassign <issue-ref>",
		Short: "clear the owner of an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssign(cmd, args[0], "", true)
		},
	}
}

func runAssign(cmd *cobra.Command, raw, owner string, unassign bool) error {
	ctx, baseURL, pid, issue, err := resolveIssueRefForCommand(cmd, raw)
	if err != nil {
		return err
	}
	actor, _ := resolveActor(flags.As, nil)
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	action := "assign"
	body := map[string]any{"actor": actor, "owner": owner}
	if unassign {
		action = "unassign"
		body = map[string]any{"actor": actor}
	}
	postURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/actions/%s", baseURL, pid, issue.Number, action)
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, postURL, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printAssignMutation(cmd, bs, unassign)
}

// printAssignMutation formats the assign/unassign response. Quiet mode prints
// nothing; JSON mode emits the daemon body under the kata_api_version
// envelope; otherwise prints a single human-readable line.
func printAssignMutation(cmd *cobra.Command, bs []byte, unassign bool) error {
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
			Number int64   `json:"number"`
			Owner  *string `json:"owner"`
		} `json:"issue"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		state := "unassigned"
		if b.Issue.Owner != nil {
			state = "assigned to " + *b.Issue.Owner
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already %s (no-op)\n", b.Issue.Number, state)
		return err
	}
	if unassign {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d unassigned\n", b.Issue.Number)
		return err
	}
	owner := ""
	if b.Issue.Owner != nil {
		owner = *b.Issue.Owner
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d assigned to %s\n", b.Issue.Number, owner)
	return err
}
