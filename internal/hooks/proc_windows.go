//go:build windows

package hooks

import (
	"os/exec"
)

// applyProcessGroupAttrs is a no-op on Windows; job-object support is
// out of scope for v1.
func applyProcessGroupAttrs(_ *exec.Cmd) {}

// terminateGroup is a best-effort first stop on Windows. Process.Signal
// with POSIX signals is unsupported there, so we treat termination as
// a request to start the kill sequence. groupAlive returns false after
// killGroup runs, so killTreeWithGrace will not block.
func terminateGroup(_ *exec.Cmd) error { return nil }

// killGroup forcibly terminates the process via TerminateProcess. There
// is no portable way to reach descendants without a job object; v1
// accepts that limitation and documents it.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// groupAlive reports whether the leader is still running. Windows
// lacks Signal(0) liveness; the conservative answer is "alive" until
// killGroup runs, which is what the caller (waitGroupGone) needs to
// trigger a force kill after the grace window.
func groupAlive(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	if cmd.ProcessState != nil {
		return false
	}
	return true
}
