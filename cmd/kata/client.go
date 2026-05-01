package main

import (
	"context"
	"net/http"
	"time"

	"github.com/wesm/kata/internal/daemonclient"
)

// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one
// if none is found. Thin wrapper over daemonclient.EnsureRunning so the CLI
// and TUI share one resolution path; tests still inject a base URL via
// daemonclient.BaseURLKey{} on the context.
func ensureDaemon(ctx context.Context) (string, error) {
	return daemonclient.EnsureRunning(ctx)
}

// httpClientFor returns an *http.Client whose transport understands the
// unix-socket base URL emitted by ensureDaemon. The TUI calls into
// daemonclient directly; this wrapper exists only because every existing
// CLI command site is already named for it.
//
//nolint:unused // consumed by upcoming command implementations (Tasks 22-27)
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
