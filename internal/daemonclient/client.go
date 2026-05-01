// Package daemonclient resolves a running kata daemon and builds matching
// *http.Clients for both unix-socket and tcp endpoints. Both the kata CLI
// (cmd/kata) and the kata TUI (internal/tui) consume this so the discovery
// rules — runtime-file scan, alive-pid filter, /ping handshake, magic
// http://kata.invalid base URL for unix transport — stay in one place.
package daemonclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wesm/kata/internal/daemon"
)

// UnixBase is the synthetic base URL used when the daemon listens on a Unix
// socket. NewHTTPClient/NewStreamingClient detect this prefix and route
// requests through a unix-socket transport instead of TCP DNS.
const UnixBase = "http://kata.invalid"

// Discover scans the namespace's runtime files and returns the base URL of
// the first daemon that passes /api/v1/ping. The bool is false when none
// respond — auto-start logic lives separately in EnsureRunning so callers
// that should never spawn (e.g. health probes) can opt out.
func Discover(ctx context.Context, dataDir string) (string, bool) {
	recs, err := daemon.ListRuntimeFiles(dataDir)
	if err != nil {
		return "", false
	}
	for _, r := range recs {
		if !daemon.ProcessAlive(r.PID) {
			continue
		}
		if url, ok := pingAddress(ctx, r.Address); ok {
			return url, true
		}
	}
	return "", false
}

// pingAddress probes /api/v1/ping at a runtime-file address. Returns the
// base URL the caller should use to reach the daemon. Only 200 succeeds, so
// a wrong service that bound the same port is rejected.
func pingAddress(ctx context.Context, address string) (string, bool) {
	if strings.HasPrefix(address, "unix://") {
		path := strings.TrimPrefix(address, "unix://")
		client := &http.Client{Transport: UnixTransport(path), Timeout: 1 * time.Second}
		if Ping(ctx, client, UnixBase) {
			return UnixBase, true
		}
		return "", false
	}
	url := "http://" + address
	client := &http.Client{Timeout: 1 * time.Second}
	if Ping(ctx, client, url) {
		return url, true
	}
	return "", false
}

// Ping is true when GET base+/api/v1/ping returns 200.
func Ping(ctx context.Context, client *http.Client, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/ping", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req) //nolint:gosec // G107: base built from our own runtime file
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// UnixTransport builds a *http.Transport whose DialContext talks to the
// named Unix socket. Used by both the discovery probe and NewHTTPClient.
func UnixTransport(path string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}
}

// SSEHandshakeTimeout caps how long NewStreamingClient waits for response
// headers. Wired onto the transport so SSE body reads stay unbounded; only
// a stalled handshake is bounded.
const SSEHandshakeTimeout = 10 * time.Second

// Opts shapes both NewHTTPClient and NewStreamingClient. ResponseHeaderTimeout
// is non-zero only for SSE clients.
type Opts struct {
	Timeout               time.Duration
	ResponseHeaderTimeout time.Duration
}

// NewHTTPClient returns an *http.Client whose transport matches baseURL —
// unix-socket dialing when baseURL == UnixBase, plain TCP otherwise. Pair
// with the URL returned by Discover/EnsureRunning. We re-scan and re-probe
// runtime files for unix endpoints so a stale record listed before a live
// one cannot redirect us to a dead socket.
func NewHTTPClient(ctx context.Context, baseURL string, opts Opts) (*http.Client, error) {
	if !strings.HasPrefix(baseURL, UnixBase) {
		return tcpClient(opts)
	}
	return unixClientFromRuntime(ctx, opts)
}

func tcpClient(opts Opts) (*http.Client, error) {
	c := &http.Client{Timeout: opts.Timeout}
	if opts.ResponseHeaderTimeout == 0 {
		return c, nil
	}
	// Clone http.DefaultTransport instead of building a bare *http.Transport
	// so we keep ProxyFromEnvironment, dial timeouts, TLS handshake timeout,
	// and HTTP/2 negotiation. Streaming clients have no overall Client.Timeout,
	// so a missing default could let DNS/TCP/TLS phases hang indefinitely
	// before ResponseHeaderTimeout could fire.
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("http.DefaultTransport is not *http.Transport")
	}
	clone := t.Clone()
	clone.ResponseHeaderTimeout = opts.ResponseHeaderTimeout
	c.Transport = clone
	return c, nil
}

func unixClientFromRuntime(ctx context.Context, opts Opts) (*http.Client, error) {
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
		probe := &http.Client{Transport: UnixTransport(path), Timeout: 1 * time.Second}
		if !Ping(ctx, probe, UnixBase) {
			continue
		}
		t := UnixTransport(path)
		if opts.ResponseHeaderTimeout > 0 {
			t.ResponseHeaderTimeout = opts.ResponseHeaderTimeout
		}
		return &http.Client{Transport: t, Timeout: opts.Timeout}, nil
	}
	return nil, errors.New("no unix-socket daemon found")
}
