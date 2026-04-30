package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <from> <type> <to>",
		Short: "create a link between two issues (type: parent|blocks|related)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "from must be an integer", ExitCode: ExitValidation}
			}
			linkType := args[1]
			to, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return &cliError{Message: "to must be an integer", ExitCode: ExitValidation}
			}
			return runLinkCreate(cmd, from, linkType, to, false)
		},
	}
}

func newParentCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "parent <child> <parent>",
		Short: "set the parent link of <child> to <parent>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			child, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "child must be an integer", ExitCode: ExitValidation}
			}
			parent, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return &cliError{Message: "parent must be an integer", ExitCode: ExitValidation}
			}
			return runLinkCreate(cmd, child, "parent", parent, replace)
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "swap the existing parent if any")
	return cmd
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink <from> <type> <to>",
		Short: "remove a link between two issues",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "from must be an integer", ExitCode: ExitValidation}
			}
			linkType := args[1]
			to, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return &cliError{Message: "to must be an integer", ExitCode: ExitValidation}
			}
			return runUnlinkByEndpoints(cmd, from, linkType, to)
		},
	}
}

func newUnparentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unparent <child>",
		Short: "remove the parent link of <child>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			child, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &cliError{Message: "child must be an integer", ExitCode: ExitValidation}
			}
			return runUnlinkByType(cmd, child, "parent")
		},
	}
}

func runLinkCreate(cmd *cobra.Command, fromNumber int64, linkType string, toNumber int64, replace bool) error {
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
	payload := map[string]any{
		"actor":     actor,
		"type":      linkType,
		"to_number": toNumber,
	}
	if replace {
		payload["replace"] = true
	}
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links", baseURL, pid, fromNumber)
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printLinkMutation(cmd, bs)
}

func runUnlinkByEndpoints(cmd *cobra.Command, fromNumber int64, linkType string, toNumber int64) error {
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
	link, err := lookupLink(ctx, baseURL, pid, fromNumber, linkType, &toNumber)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, link)
}

func runUnlinkByType(cmd *cobra.Command, fromNumber int64, linkType string) error {
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
	link, err := lookupLink(ctx, baseURL, pid, fromNumber, linkType, nil)
	if err != nil {
		return err
	}
	return runDeleteLink(cmd, baseURL, pid, link)
}

// linkRow is the wire shape of a single link entry inside ShowIssueResponse.
// Mirrors api.LinkOut for the fields the CLI needs.
type linkRow struct {
	ID         int64  `json:"id"`
	Type       string `json:"type"`
	FromNumber int64  `json:"from_number"`
	ToNumber   int64  `json:"to_number"`
}

// lookupLink resolves a (from, type [, to]) tuple to the matching link by
// reading the issue's links via GET /issues/{from}. Returns 404 link_not_found
// when no link matches. The returned linkRow carries enough context for the
// post-DELETE print line.
func lookupLink(ctx context.Context, baseURL string, pid, fromNumber int64, linkType string, toNumber *int64) (linkRow, error) {
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return linkRow{}, err
	}
	showURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d", baseURL, pid, fromNumber)
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, showURL, nil)
	if err != nil {
		return linkRow{}, err
	}
	if status >= 400 {
		return linkRow{}, apiErrFromBody(status, bs)
	}
	var b struct {
		Links []linkRow `json:"links"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return linkRow{}, err
	}
	for _, l := range b.Links {
		if l.Type != linkType {
			continue
		}
		if l.FromNumber != fromNumber {
			continue
		}
		if toNumber != nil && l.ToNumber != *toNumber {
			continue
		}
		return l, nil
	}
	return linkRow{}, &cliError{Message: "link not found", Code: "link_not_found", ExitCode: ExitNotFound}
}

func runDeleteLink(cmd *cobra.Command, baseURL string, pid int64, link linkRow) error {
	ctx := cmd.Context()
	actor, _ := resolveActor(flags.As, nil)
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return err
	}
	deleteURL := fmt.Sprintf("%s/api/v1/projects/%d/issues/%d/links/%d?actor=%s",
		baseURL, pid, link.FromNumber, link.ID, url.QueryEscape(actor))
	status, bs, err := httpDoJSON(ctx, client, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiErrFromBody(status, bs)
	}
	return printUnlinkMutation(cmd, bs, link)
}

// printUnlinkMutation formats the DELETE-link response. The MutationResponse
// body carries only {issue, event, changed} so the unlink line is built from
// the link the CLI fetched up-front (its from/to/type).
func printUnlinkMutation(cmd *cobra.Command, bs []byte, link linkRow) error {
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
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already unlinked: %s → #%d (no-op)\n",
			link.FromNumber, link.Type, link.ToNumber)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d unlinked: %s → #%d\n",
		link.FromNumber, link.Type, link.ToNumber)
	return err
}

// printLinkMutation formats a CreateLinkResponse for the three output modes.
// Reuses emitJSON for the JSON branch (the daemon body already includes the
// shape we want under the kata_api_version envelope).
func printLinkMutation(cmd *cobra.Command, bs []byte) error {
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
		Link struct {
			Type       string `json:"type"`
			FromNumber int64  `json:"from_number"`
			ToNumber   int64  `json:"to_number"`
		} `json:"link"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal(bs, &b); err != nil {
		return err
	}
	if flags.Quiet {
		return nil
	}
	if !b.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d already linked: %s → #%d (no-op)\n",
			b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "#%d linked: %s → #%d\n",
		b.Link.FromNumber, b.Link.Type, b.Link.ToNumber)
	return err
}
