//go:build !windows

package hooks

import (
	"errors"
	"os/exec"
	"syscall"
)

// applyProcessGroupAttrs sets Setpgid so the child becomes the leader
// of its own process group. terminateGroup/killGroup target the group
// via negative pid, so hooks that fork children get torn down cleanly.
func applyProcessGroupAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup sends SIGTERM to every process in the leader's group.
func terminateGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killGroup sends SIGKILL to every process in the leader's group.
// Called after the SIGTERM grace window when the group still has
// members. Errors are not fatal — the caller logs and proceeds.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// groupAlive reports whether the leader's process group still has any
// member. syscall.Kill with signal 0 returns ESRCH when no process in
// the group exists (because the leader's pid is also the pgid).
func groupAlive(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
