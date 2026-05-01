//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// spawnOrphanAndExit forks a child that ignores SIGTERM and sleeps. The
// parent exits immediately, so the child survives unless its process
// group is killed. Used by the runner test suite to verify Setpgid.
func spawnOrphanAndExit(d time.Duration) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Re-exec ourselves; exe path is from os.Executable, not user input.
	cmd := exec.Command(exe, "term-ignore", d.String()) //nolint:gosec // self-exec for orphan-spawn test helper
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	time.Sleep(50 * time.Millisecond)
}
