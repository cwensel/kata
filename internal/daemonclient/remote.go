package daemonclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/kata/internal/config"
)

// remoteServerEnvVar is the environment variable that names a kata
// daemon URL. When set, it takes precedence over .kata.local.toml.
const remoteServerEnvVar = "KATA_SERVER"

// ErrRemoteUnavailable wraps probe failures against an explicitly
// configured remote URL (env or .kata.local.toml). Callers translate
// this into a daemon-unavailable CLI error; we keep the package free
// of CLI-layer types so this package stays importable from the TUI.
var ErrRemoteUnavailable = errors.New("kata server not responding")

// resolveRemote checks the two opt-in remote sources, in order:
//
//  1. KATA_SERVER env (highest precedence)
//  2. .kata.local.toml [server].url walked up from CWD
//
// If neither is set, returns ("", false, nil) and the caller falls
// through to local Discover/auto-start. If a URL is configured, the
// helper probes /api/v1/ping; on success it returns (url, true, nil),
// on failure it returns ("", false, ErrRemoteUnavailable wrapped with
// the URL and the source name) so the user sees which input is wrong.
func resolveRemote(ctx context.Context) (string, bool, error) {
	if v := os.Getenv(remoteServerEnvVar); v != "" {
		u, err := normalizeRemoteURL(v)
		if err != nil {
			return "", false, fmt.Errorf("KATA_SERVER %q: %w", v, err)
		}
		if !probeRemote(ctx, u) {
			return "", false, fmt.Errorf("%w: %s (KATA_SERVER)", ErrRemoteUnavailable, u)
		}
		return u, true, nil
	}
	root, path, ok := findLocalConfig()
	if !ok {
		return "", false, nil
	}
	cfg, err := config.ReadLocalConfig(root)
	if err != nil {
		if errors.Is(err, config.ErrLocalConfigMissing) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	if cfg.Server.URL == "" {
		return "", false, nil
	}
	u, err := normalizeRemoteURL(cfg.Server.URL)
	if err != nil {
		return "", false, fmt.Errorf("%s server.url %q: %w", path, cfg.Server.URL, err)
	}
	if !probeRemote(ctx, u) {
		return "", false, fmt.Errorf("%w: %s (%s)", ErrRemoteUnavailable, u, path)
	}
	return u, true, nil
}

// findLocalConfig walks upward from CWD looking for .kata.local.toml.
// Returns the directory containing it, the full path (for error
// messages), and ok=true. Stops at the filesystem root.
func findLocalConfig() (root, path string, ok bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		candidate := filepath.Join(dir, config.LocalConfigFilename)
		if _, err := os.Stat(candidate); err == nil {
			return dir, candidate, true
		} else if !errors.Is(err, os.ErrNotExist) {
			// Permission denied, broken symlink, etc. — surface to stderr
			// so the user is not silently routed past their config file.
			fmt.Fprintf(os.Stderr, "kata: warning: cannot stat %s: %v\n", candidate, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// normalizeRemoteURL parses a value as an http(s) URL and returns the
// canonical scheme://host[:port] form (no path, no query). Empty path
// matches the daemon's expectation: callers append /api/v1/... themselves.
func normalizeRemoteURL(v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("url must include host")
	}
	return u.Scheme + "://" + u.Host, nil
}

// probeRemote does a 1-second /api/v1/ping check against base. We keep
// the budget tight: a misconfigured remote should fail fast, not stall
// the user behind the 5-second auto-start deadline.
func probeRemote(ctx context.Context, base string) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	return Ping(ctx, client, base)
}
