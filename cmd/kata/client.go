package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wesm/kata/internal/daemon"
)

// baseURLKey is the context key used to inject a daemon base URL in tests,
// bypassing the real discovery/auto-start flow.
type baseURLKey struct{}

// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one if
// none is found.
func ensureDaemon(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(baseURLKey{}).(string); ok && v != "" {
		return v, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, ok := tryDiscover(ctx, ns.DataDir); ok {
		return url, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	//nolint:gosec // G204: exe is os.Executable()
	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	// Detach the child into its own process group so that a SIGINT delivered
	// to the foreground CLI (e.g. ctrl-C on `kata create`) is not propagated
	// to the daemon we just spawned.
	detachChild(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("auto-start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if url, ok := tryDiscover(ctx, ns.DataDir); ok {
			return url, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "", errors.New("daemon failed to start within 5s")
}

func tryDiscover(ctx context.Context, dataDir string) (string, bool) {
	recs, err := daemon.ListRuntimeFiles(dataDir)
	if err != nil {
		return "", false
	}
	for _, r := range recs {
		if !daemon.ProcessAlive(r.PID) {
			continue
		}
		url, ok := pingAddress(ctx, r.Address)
		if ok {
			return url, true
		}
	}
	return "", false
}

// pingAddress probes /api/v1/ping at the given runtime-file address. Returns
// the base URL the caller should use to reach it. The probe only succeeds on
// a 200 response, so a 404/500 from a wrong service that happened to bind
// the same port is rejected as not-our-daemon.
func pingAddress(ctx context.Context, address string) (string, bool) {
	if strings.HasPrefix(address, "unix://") {
		path := strings.TrimPrefix(address, "unix://")
		client := &http.Client{Transport: unixTransport(path), Timeout: 1 * time.Second}
		const base = "http://kata.invalid"
		if pingOK(ctx, client, base) {
			return base, true
		}
		return "", false
	}
	url := "http://" + address
	client := &http.Client{Timeout: 1 * time.Second}
	if pingOK(ctx, client, url) {
		return url, true
	}
	return "", false
}

// pingOK is true when GET base+/api/v1/ping returns 200.
func pingOK(ctx context.Context, client *http.Client, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/ping", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req) //nolint:gosec // G704: base built from our own runtime file
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// unixTransport builds a *http.Transport whose DialContext talks to the named
// Unix socket. Used by both pingAddress and httpClientFor.
func unixTransport(path string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}
}

// sseHandshakeTimeout caps how long streamingClientFor will wait for the
// server's response headers. It is wired onto the transport, not the client,
// so SSE body reads remain unbounded; only a stalled handshake is bounded.
const sseHandshakeTimeout = 10 * time.Second

// httpClientFor returns an *http.Client whose transport understands unix://
// addresses. Pair with the URL returned by ensureDaemon. We filter by
// ProcessAlive and prefer the first record that also passes a /ping probe so
// a stale unix:// runtime file listed before the live one cannot redirect us
// to a dead socket.
//
//nolint:unused // consumed by upcoming command implementations (Tasks 22-27)
func httpClientFor(ctx context.Context, baseURL string) (*http.Client, error) {
	return newDaemonClient(ctx, baseURL, daemonClientOpts{timeout: 5 * time.Second})
}

// streamingClientFor is httpClientFor but with no overall Client.Timeout, so
// long-lived SSE bodies are not torn down mid-stream. A transport-level
// ResponseHeaderTimeout caps the handshake phase so a daemon that accepts a
// connection but stalls before sending headers cannot hang the consumer
// indefinitely. Body cancellation comes from the request context.
func streamingClientFor(ctx context.Context, baseURL string) (*http.Client, error) {
	return newDaemonClient(ctx, baseURL, daemonClientOpts{
		responseHeaderTimeout: sseHandshakeTimeout,
	})
}

type daemonClientOpts struct {
	timeout               time.Duration
	responseHeaderTimeout time.Duration
}

func newDaemonClient(ctx context.Context, baseURL string, opts daemonClientOpts) (*http.Client, error) {
	if !strings.HasPrefix(baseURL, "http://kata.invalid") {
		c := &http.Client{Timeout: opts.timeout}
		if opts.responseHeaderTimeout > 0 {
			// Clone http.DefaultTransport instead of building a bare
			// *http.Transport so we keep ProxyFromEnvironment, dial
			// timeouts, TLS handshake timeout, and HTTP/2 negotiation —
			// streaming clients have no overall Client.Timeout, so a
			// dropped DNS/TCP/TLS default would let those phases hang
			// indefinitely before ResponseHeaderTimeout could fire.
			t, ok := http.DefaultTransport.(*http.Transport)
			if !ok {
				return nil, errors.New("http.DefaultTransport is not *http.Transport")
			}
			clone := t.Clone()
			clone.ResponseHeaderTimeout = opts.responseHeaderTimeout
			c.Transport = clone
		}
		return c, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return nil, err
	}
	recs, err := daemon.ListRuntimeFiles(ns.DataDir)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if !daemon.ProcessAlive(r.PID) || !strings.HasPrefix(r.Address, "unix://") {
			continue
		}
		path := strings.TrimPrefix(r.Address, "unix://")
		probe := &http.Client{Transport: unixTransport(path), Timeout: 1 * time.Second}
		if !pingOK(ctx, probe, "http://kata.invalid") {
			continue
		}
		t := unixTransport(path)
		if opts.responseHeaderTimeout > 0 {
			t.ResponseHeaderTimeout = opts.responseHeaderTimeout
		}
		return &http.Client{Transport: t, Timeout: opts.timeout}, nil
	}
	return nil, errors.New("no unix-socket daemon found")
}
