package main

import (
	"context"
	"net/http"
	"time"

	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/daemonclient"
)

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
		daemonclient.Opts{Timeout: 5 * time.Second})
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
