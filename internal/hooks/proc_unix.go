//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupAttrs sets Setpgid so the child becomes the leader
// of its own process group. signalGroup uses negative pid to deliver
// the signal to every process in that group, so hooks that fork
// children get torn down cleanly.
func applyProcessGroupAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the process group led by cmd.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

func syscallSIGTERM() syscall.Signal { return syscall.SIGTERM }
func syscallSIGKILL() syscall.Signal { return syscall.SIGKILL }
func syscallSignal0() syscall.Signal { return syscall.Signal(0) }
