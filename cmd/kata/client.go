package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/daemonclient"
)

// defaultHTTPTimeout is the per-request budget for non-streaming CLI calls.
// Override at runtime with KATA_HTTP_TIMEOUT (any time.ParseDuration string).
const defaultHTTPTimeout = 5 * time.Second

// envHTTPTimeout reads KATA_HTTP_TIMEOUT, falling back to def on empty or
// unparseable input. Bulk imports against an FTS-indexed DB can take longer
// than the default per request, so this knob lets callers extend the budget
// without rebuilding the binary. A non-empty but unparseable value writes a
// warning to stderr — silently using the default would defeat the point of
// setting the env var ("KATA_HTTP_TIMEOUT=30" misses the unit and would
// otherwise look like the bump took effect).
func envHTTPTimeout(def time.Duration) time.Duration {
	v := os.Getenv("KATA_HTTP_TIMEOUT")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		fmt.Fprintf(os.Stderr,
			"kata: ignoring invalid KATA_HTTP_TIMEOUT=%q (expected a Go duration like 30s or 2m); using default %s\n",
			v, def)
		return def
	}
	return d
}

// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one
// if none is found. Thin wrapper over daemonclient.EnsureRunning so the CLI
// and TUI share one resolution path; tests still inject a base URL via
// daemonclient.BaseURLKey{} on the context.
func ensureDaemon(ctx context.Context) (string, error) {
	return daemonclient.EnsureRunning(ctx)
}

// discoverDaemon returns the live daemon URL without auto-starting one.
// Used by health probes and any other surface where "no daemon running"
// is a meaningful answer rather than a state to paper over. Honors the
// BaseURLKey context shortcut so tests can still inject. Returns a
// kindDaemonUnavail cliError when no live daemon is found, matching
// hammer-test finding #1's expectation that `kata health` doesn't lie
// about the daemon's actual state.
func discoverDaemon(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(daemonclient.BaseURLKey{}).(string); ok && v != "" {
		return v, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, ok := daemonclient.Discover(ctx, ns.DataDir); ok {
		return url, nil
	}
	return "", &cliError{
		Message:  "no daemon running (start one with `kata daemon start`)",
		Kind:     kindDaemonUnavail,
		ExitCode: ExitDaemonUnavail,
	}
}

// httpClientFor returns an *http.Client whose transport understands the
// unix-socket base URL emitted by ensureDaemon. The TUI calls into
// daemonclient directly; this wrapper exists only because every existing
// CLI command site is already named for it.
func httpClientFor(ctx context.Context, baseURL string) (*http.Client, error) {
	return daemonclient.NewHTTPClient(ctx, baseURL,
		daemonclient.Opts{Timeout: envHTTPTimeout(defaultHTTPTimeout)})
}

// streamingClientFor builds the SSE-friendly variant: no overall
// Client.Timeout (so long-lived bodies don't get torn down) but a transport
// ResponseHeaderTimeout so a stalled handshake can't hang forever. Body
// cancellation comes from the request context.
func streamingClientFor(ctx context.Context, baseURL string) (*http.Client, error) {
	return daemonclient.NewHTTPClient(ctx, baseURL, daemonclient.Opts{
		ResponseHeaderTimeout: daemonclient.SSEHandshakeTimeout,
	})
}

type resolvedIssueRef struct {
	Number    int64
	UID       string
	ProjectID int64
}

func resolveIssueRef(ctx context.Context, baseURL string, projectID int64, ref string) (resolvedIssueRef, error) {
	return resolveIssueRefWithOptions(ctx, baseURL, projectID, ref, false)
}

func resolveIssueRefWithOptions(ctx context.Context, baseURL string, projectID int64, ref string, includeDeleted bool) (resolvedIssueRef, error) {
	if n, ok, err := parseIssueNumberRef(ref); ok || err != nil {
		return resolvedIssueRef{Number: n, ProjectID: projectID}, err
	}
	client, err := httpClientFor(ctx, baseURL)
	if err != nil {
		return resolvedIssueRef{}, err
	}
	path := fmt.Sprintf("%s/api/v1/issues/%s", baseURL, url.PathEscape(ref))
	if includeDeleted {
		path += "?include_deleted=true"
	}
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
		path, nil)
	if err != nil {
		return resolvedIssueRef{}, err
	}
	if status >= 400 {
		return resolvedIssueRef{}, apiErrFromBody(status, bs)
	}
	var out struct {
		Issue struct {
			Number    int64  `json:"number"`
			UID       string `json:"uid"`
			ProjectID int64  `json:"project_id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(bs, &out); err != nil {
		return resolvedIssueRef{}, err
	}
	if projectID != 0 && out.Issue.ProjectID != projectID {
		return resolvedIssueRef{}, &cliError{
			Message:  "issue UID does not belong to the current project",
			Code:     "issue_not_found",
			Kind:     kindNotFound,
			ExitCode: ExitNotFound,
		}
	}
	return resolvedIssueRef{Number: out.Issue.Number, UID: out.Issue.UID, ProjectID: out.Issue.ProjectID}, nil
}

func parseIssueNumberRef(ref string) (int64, bool, error) {
	s := strings.TrimPrefix(ref, "#")
	if s == "" {
		return 0, true, &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if strings.HasPrefix(ref, "#") {
			return 0, true, &cliError{Message: "issue number must be an integer", Kind: kindValidation, ExitCode: ExitValidation}
		}
		return 0, false, nil
	}
	return n, true, nil
}
