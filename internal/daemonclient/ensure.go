package daemonclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/wesm/kata/internal/daemon"
)

// BaseURLKey is the context key for injecting a daemon base URL during
// tests, bypassing both Discover and the auto-start path. CLI and TUI
// callers honor it via EnsureRunning.
type BaseURLKey struct{}

// EnsureRunning returns a live daemon's base URL, auto-starting the daemon
// if no live record is found. Callers that should never spawn a daemon
// (health probes, list commands that should fail loudly) should call
// Discover directly instead.
//
// Test callers can short-circuit discovery by stashing a base URL on ctx
// under BaseURLKey{}.
func EnsureRunning(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(BaseURLKey{}).(string); ok && v != "" {
		return v, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, ok := Discover(ctx, ns.DataDir); ok {
		return url, nil
	}
	return autoStart(ctx, ns.DataDir)
}

func autoStart(ctx context.Context, dataDir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	//nolint:gosec // G204: exe is os.Executable()
	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	// Detach the child into its own process group so SIGINT delivered to the
	// foreground caller (e.g. ctrl-C on `kata create` or `kata tui`) is not
	// propagated to the daemon we just spawned.
	detachChild(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("auto-start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if url, ok := Discover(ctx, dataDir); ok {
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
