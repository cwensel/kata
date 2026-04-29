package config

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// DiscoveredPaths is the result of walking upward from a start path.
// Both fields may be empty (no .kata.toml and no .git ancestor).
type DiscoveredPaths struct {
	WorkspaceRoot string // first ancestor with .kata.toml (inclusive)
	GitRoot       string // first ancestor with .git (inclusive)
}

// AliasInfo is the alias-identity record derived from a workspace.
type AliasInfo struct {
	Identity string // git remote (normalized) or "local://<abs path>"
	Kind     string // "git" | "local"
	RootPath string // GitRoot when present, else WorkspaceRoot
}

// DiscoverPaths walks upward from startPath looking for .kata.toml (W) and
// .git (G). Both lookups are independent and inclusive of startPath itself.
// A missing path returns ("", nil); resolution errors are returned.
func DiscoverPaths(startPath string) (DiscoveredPaths, error) {
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return DiscoveredPaths{}, fmt.Errorf("abs %s: %w", startPath, err)
	}
	d := DiscoveredPaths{}
	d.WorkspaceRoot = walkUp(abs, ProjectConfigFilename, false)
	d.GitRoot = walkUp(abs, ".git", true)
	return d, nil
}

// walkUp returns the first ancestor (inclusive) containing the named entry,
// or "" if none. allowDir lets the entry be either a file or directory.
func walkUp(start, entry string, allowDir bool) string {
	dir := start
	for {
		path := filepath.Join(dir, entry)
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				if allowDir {
					return dir
				}
			} else {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ComputeAliasIdentity derives the alias for a workspace per spec §2.4. Order:
// 1. GitRoot with remote → normalized origin URL
// 2. GitRoot without remote → local://<abs(GitRoot)>
// 3. WorkspaceRoot only → local://<abs(WorkspaceRoot)>
// 4. Neither → error
func ComputeAliasIdentity(d DiscoveredPaths) (AliasInfo, error) {
	if d.GitRoot != "" {
		remote, err := readGitRemote(d.GitRoot)
		if err != nil {
			return AliasInfo{}, err
		}
		if remote != "" {
			id, err := NormalizeRemoteURL(remote)
			if err != nil {
				return AliasInfo{}, err
			}
			return AliasInfo{Identity: id, Kind: "git", RootPath: d.GitRoot}, nil
		}
		return AliasInfo{
			Identity: "local://" + d.GitRoot,
			Kind:     "local",
			RootPath: d.GitRoot,
		}, nil
	}
	if d.WorkspaceRoot != "" {
		return AliasInfo{
			Identity: "local://" + d.WorkspaceRoot,
			Kind:     "local",
			RootPath: d.WorkspaceRoot,
		}, nil
	}
	return AliasInfo{}, fmt.Errorf("no workspace or git root discovered")
}

// readGitRemote returns the URL of "origin" (or the first remote listed by
// `git remote` when no origin exists). Returns ("", nil) if no remotes.
func readGitRemote(gitRoot string) (string, error) {
	out, err := runGit(gitRoot, "remote")
	if err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	remotes := strings.Fields(strings.TrimSpace(out))
	if len(remotes) == 0 {
		return "", nil
	}
	target := "origin"
	hasOrigin := false
	for _, r := range remotes {
		if r == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		target = remotes[0]
	}
	url, err := runGit(gitRoot, "remote", "get-url", target)
	if err != nil {
		return "", fmt.Errorf("git remote get-url %s: %w", target, err)
	}
	return strings.TrimSpace(url), nil
}

func runGit(dir string, args ...string) (string, error) {
	//nolint:gosec // git binary is fixed; args are caller-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// scpLikeRE matches "user@host:path[/...]" SCP-style git URLs.
var scpLikeRE = regexp.MustCompile(`^([^@\s]+)@([^:\s]+):(.+)$`)

// NormalizeRemoteURL strips credentials, normalizes SSH↔HTTPS, drops trailing
// .git, and returns "host/path" form (e.g. "github.com/wesm/kata").
func NormalizeRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty remote url")
	}
	if m := scpLikeRE.FindStringSubmatch(raw); m != nil {
		host := m[2]
		path := strings.TrimSuffix(m[3], ".git")
		return host + "/" + path, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("parse remote url %q: not a recognized form", raw)
	}
	host := u.Host
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if path == "" {
		return host, nil
	}
	return host + "/" + path, nil
}

var identityCharsetRE = regexp.MustCompile(`^[A-Za-z0-9._:/\-]+$`)

// ValidateIdentity enforces the spec §2.4 charset and forbids whitespace and
// embedded URL credentials.
func ValidateIdentity(id string) error {
	if id == "" {
		return fmt.Errorf("identity must be non-empty")
	}
	for _, r := range id {
		if unicode.IsSpace(r) {
			return fmt.Errorf("identity contains whitespace: %q", id)
		}
	}
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		// reject embedded credentials.
		if strings.Contains(id, "@") {
			return fmt.Errorf("identity must not embed credentials: %q", id)
		}
	}
	if !identityCharsetRE.MatchString(stripLocalScheme(id)) {
		return fmt.Errorf("identity contains disallowed characters: %q", id)
	}
	return nil
}

// stripLocalScheme allows local://<abs path> identities through the charset
// check by ignoring the scheme prefix and validating the remainder.
func stripLocalScheme(id string) string {
	const prefix = "local://"
	if strings.HasPrefix(id, prefix) {
		return strings.ReplaceAll(id[len(prefix):], "/", "")
	}
	return id
}
