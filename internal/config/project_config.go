package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrProjectConfigMissing is returned by ReadProjectConfig when the workspace
// has no .kata.toml at the given path.
var ErrProjectConfigMissing = errors.New(".kata.toml not found")

// ProjectConfigFilename is the canonical filename committed at workspace roots.
const ProjectConfigFilename = ".kata.toml"

// ProjectConfig is the parsed contents of a workspace .kata.toml.
type ProjectConfig struct {
	Version int             `toml:"version"`
	Project ProjectBindings `toml:"project"`
}

// ProjectBindings carries the [project] block.
type ProjectBindings struct {
	Identity string `toml:"identity"`
	Name     string `toml:"name,omitempty"`
}

// ReadProjectConfig parses <workspaceRoot>/.kata.toml and validates v1 fields.
// Returns (nil, ErrProjectConfigMissing) when the file does not exist; other
// I/O or validation errors are returned as-is.
func ReadProjectConfig(workspaceRoot string) (*ProjectConfig, error) {
	path := filepath.Join(workspaceRoot, ProjectConfigFilename)
	data, err := os.ReadFile(path) //nolint:gosec // workspace-supplied path is the documented input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrProjectConfigMissing
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ProjectConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported .kata.toml version %d (expected 1)", cfg.Version)
	}
	if strings.TrimSpace(cfg.Project.Identity) == "" {
		return nil, fmt.Errorf("project.identity must be a non-empty string")
	}
	cfg.Project.Identity = strings.TrimSpace(cfg.Project.Identity)
	cfg.Project.Name = strings.TrimSpace(cfg.Project.Name)
	return &cfg, nil
}

// WriteProjectConfig writes a v1 .kata.toml at <workspaceRoot>/.kata.toml.
// If name is empty the last `/` or `:` segment of identity is used.
func WriteProjectConfig(workspaceRoot, identity, name string) error {
	if strings.TrimSpace(identity) == "" {
		return fmt.Errorf("identity must be non-empty")
	}
	if name == "" {
		name = lastSegment(identity)
	}
	body := fmt.Sprintf("version = 1\n\n[project]\nidentity = %q\nname     = %q\n",
		identity, name)
	path := filepath.Join(workspaceRoot, ProjectConfigFilename)
	return os.WriteFile(path, []byte(body), 0o644) //nolint:gosec // committed project file, world-readable by design
}

func lastSegment(identity string) string {
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' || identity[i] == ':' {
			return identity[i+1:]
		}
	}
	return identity
}
