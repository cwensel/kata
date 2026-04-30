package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// initOptions holds the flags specific to `kata init`.
type initOptions struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

// callInitOpts is the parameter bag passed to callInit.
type callInitOpts struct {
	Project  string
	Name     string
	Replace  bool
	Reassign bool
}

// cliError is a structured error that carries an exit code for main().
type cliError struct {
	Message  string
	Code     string
	ExitCode int
}

func (e *cliError) Error() string { return e.Message }

// newInitCmd returns the cobra.Command for `kata init`.
func newInitCmd() *cobra.Command {
	var opts initOptions

	cmd := &cobra.Command{
		Use:   "init",
		Short: "bind workspace to a project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			baseURL, err := ensureDaemon(cmd.Context())
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			startPath, err := resolveStartPath(flags.Workspace)
			if err != nil {
				return fmt.Errorf("resolve workspace: %w", err)
			}
			out, err := callInit(cmd.Context(), baseURL, startPath, callInitOpts(opts))
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}

	cmd.Flags().StringVar(&opts.Project, "project", "", "project identity (default: derived from git remote)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "human name for the project (default: last path segment)")
	cmd.Flags().BoolVar(&opts.Replace, "replace", false, "overwrite .kata.toml binding when it conflicts")
	cmd.Flags().BoolVar(&opts.Reassign, "reassign", false, "move an existing alias to this project")

	return cmd
}

// callInit calls POST /api/v1/projects and returns the formatted output string.
// The daemon is responsible for writing .kata.toml in the workspace.
func callInit(ctx context.Context, baseURL, startPath string, opts callInitOpts) (string, error) {
	reqBody := map[string]any{
		"start_path": startPath,
	}
	if opts.Project != "" {
		reqBody["project_identity"] = opts.Project
	}
	if opts.Name != "" {
		reqBody["name"] = opts.Name
	}
	if opts.Replace {
		reqBody["replace"] = true
	}
	if opts.Reassign {
		reqBody["reassign"] = true
	}

	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return "", fmt.Errorf("client: %w", err)
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodPost, baseURL+"/api/v1/projects", reqBody)
	if err != nil {
		return "", fmt.Errorf("POST /api/v1/projects: %w", err)
	}
	if status >= 300 {
		return "", apiErrFromBody(status, bs)
	}

	// Decode the response to extract project identity and name for display.
	var resp struct {
		Project struct {
			Identity string `json:"identity"`
			Name     string `json:"name"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(bs, &resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if flags.JSON {
		// Route JSON output through emitJSON so kata_api_version is present
		// (CLI JSON contract per spec §5.1). The daemon's response body is
		// already a JSON object, so we can pass it as a json.RawMessage
		// directly without re-marshaling field-by-field.
		var buf bytes.Buffer
		if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
			return "", fmt.Errorf("emit json: %w", err)
		}
		return buf.String(), nil
	}

	action := "bound"
	if resp.Created {
		action = "created and bound"
	}
	return fmt.Sprintf("%s project %s (%s)\n", action, resp.Project.Identity, resp.Project.Name), nil
}

// resolveStartPath returns the absolute path to use as the daemon's
// start_path. Relative paths are resolved against the CLI's current working
// directory so the daemon (which may have a different cwd) doesn't end up
// binding or writing .kata.toml in the wrong place.
func resolveStartPath(workspace string) (string, error) {
	if workspace == "" {
		return os.Getwd()
	}
	return filepath.Abs(workspace)
}

// apiErrFromBody decodes a daemon ErrorEnvelope and returns a *cliError with
// the appropriate exit code. Falls back to a raw-body error when the envelope
// can't be decoded so the caller still has debugging context.
func apiErrFromBody(status int, bs []byte) *cliError {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bs, &env); err != nil {
		return &cliError{
			Message:  errors.New(string(bs)).Error(),
			Code:     "",
			ExitCode: mapStatusToExit(status, ""),
		}
	}
	return &cliError{
		Message:  env.Error.Message,
		Code:     env.Error.Code,
		ExitCode: mapStatusToExit(status, env.Error.Code),
	}
}

// mapStatusToExit maps an HTTP status to a CLI exit code. The code parameter
// is reserved for future per-code overrides (e.g. distinguishing
// project_not_found from project_not_initialized within 404s).
func mapStatusToExit(status int, _ string) int {
	switch status {
	case http.StatusBadRequest:
		return ExitValidation
	case http.StatusNotFound:
		return ExitNotFound
	case http.StatusConflict:
		return ExitConflict
	case http.StatusPreconditionFailed:
		return ExitConfirm
	default:
		return ExitInternal
	}
}
