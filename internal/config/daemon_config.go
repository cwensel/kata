package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// DaemonConfig is the parsed contents of <KATA_HOME>/config.toml. The
// file is optional; an absent file yields a zero-value DaemonConfig and
// no error so callers can use this unconditionally at daemon start.
//
// Only daemon-side fields belong here. Client-side overrides
// (KATA_SERVER, .kata.local.toml) live in their own resolution path.
type DaemonConfig struct {
	// Listen is the bind address used by `kata daemon start` when no
	// --listen flag is supplied. Same syntax as the flag (host:port).
	// An empty value (or a missing file) means "default Unix socket".
	Listen string `toml:"listen"`
}

// ReadDaemonConfig parses <KATA_HOME>/config.toml. Returns a zero-value
// DaemonConfig and nil error when the file is absent — daemon startup
// should not fail just because the file isn't there. Other I/O or parse
// errors are returned so a typo doesn't silently fall back to defaults.
func ReadDaemonConfig() (*DaemonConfig, error) {
	path, err := DaemonConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from KATA_HOME, not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DaemonConfig{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg DaemonConfig
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if u := meta.Undecoded(); len(u) > 0 {
		keys := make([]string, len(u))
		for i, k := range u {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("parse %s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	return &cfg, nil
}
