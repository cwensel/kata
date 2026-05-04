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
	"strings"

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
//
// Kind is the coarse classification used by the --json error envelope so
// scripts can branch on a stable taxonomy instead of grepping the
// human-readable message. Code is the daemon-supplied per-error tag
// (e.g. "issue_not_found"); empty when the error originated client-side.
// Message is the human-readable text. ExitCode is what main() exits with.
type cliError struct {
	Message  string
	Kind     errKind
	Code     string
	ExitCode int
}

func (e *cliError) Error() string { return e.Message }

// errKind is the coarse classification surfaced in the --json error
// envelope. Maps roughly onto the spec §4.7 exit codes but is named
// for the kind of failure rather than the numeric exit, so JSON
// consumers can branch on a stable identifier.
type errKind string

const (
	kindUsage         errKind = "usage"
	kindValidation    errKind = "validation"
	kindNotFound      errKind = "not_found"
	kindConflict      errKind = "conflict"
	kindConfirm       errKind = "confirm"
	kindDaemonUnavail errKind = "daemon_unavailable"
	kindInternal      errKind = "internal"
)

// kindForExit maps an exit code to the conventional errKind. Used when
// a non-cliError reaches main and we still want to emit a JSON
// envelope under --json.
func kindForExit(exit int) errKind {
	switch exit {
	case ExitUsage:
		return kindUsage
	case ExitValidation:
		return kindValidation
	case ExitNotFound:
		return kindNotFound
	case ExitConflict:
		return kindConflict
	case ExitConfirm:
		return kindConfirm
	case ExitDaemonUnavail:
		return kindDaemonUnavail
	}
	return kindInternal
}

// kindForStatus maps an HTTP status to the conventional errKind. The
// daemon-supplied error code is reserved for future per-code overrides.
func kindForStatus(status int) errKind {
	switch status {
	case http.StatusBadRequest:
		return kindValidation
	case http.StatusNotFound:
		return kindNotFound
	case http.StatusConflict:
		return kindConflict
	case http.StatusPreconditionFailed:
		return kindConfirm
	}
	return kindInternal
}

// newInitCmd returns the cobra.Command for `kata init`.
func newInitCmd() *cobra.Command {
	var opts initOptions

	cmd := &cobra.Command{
		Use:   "init",
		Short: "bind workspace to a project",
		Long: `Initialize kata in this workspace.

Writes a committed .kata.toml that binds the workspace to a project
identity. The daemon derives the identity from a git remote when one
is present; pass --project to override, or --name to set the
human-readable name.

Also adds .kata.local.toml to .gitignore so a developer's per-machine
overrides (e.g., a remote daemon URL via [server] url = "...") never
get committed.`,
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

	if err := ensureGitignoreEntry(startPath, ".kata.local.toml"); err != nil {
		fmt.Fprintf(os.Stderr, "kata: warning: could not update .gitignore: %v\n", err)
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
			Kind:     kindForStatus(status),
			ExitCode: mapStatusToExit(status, ""),
		}
	}
	return &cliError{
		Message:  env.Error.Message,
		Code:     env.Error.Code,
		Kind:     kindForStatus(status),
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

// ensureGitignoreEntry appends a single line to <dir>/.gitignore if
// the entry is not already present. Creates the file if absent.
// Idempotent: re-running on a file that already lists the entry is a
// no-op.
func ensureGitignoreEntry(dir, entry string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path) //nolint:gosec
	switch {
	case err == nil:
		// Walk lines so we don't false-match a substring inside a longer
		// pattern (e.g. ".kata.local.toml.bak").
		for _, line := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(line) == entry {
				return nil
			}
		}
		// Preserve trailing-newline convention: if the file ends without
		// a newline, add one before appending so we don't merge our line
		// into theirs.
		var prefix string
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			prefix = "\n"
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
			return err
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(entry+"\n"), 0o644) //nolint:gosec
	default:
		return err
	}
}
