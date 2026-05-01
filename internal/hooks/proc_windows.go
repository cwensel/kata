//go:build windows

package hooks

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupAttrs is a no-op on Windows.
func applyProcessGroupAttrs(cmd *exec.Cmd) {}

// signalGroup signals only the process on Windows.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}

func syscallSIGTERM() syscall.Signal { return syscall.SIGTERM }
func syscallSIGKILL() syscall.Signal { return syscall.SIGKILL }
func syscallSignal0() syscall.Signal { return syscall.Signal(0) }
