//go:build !windows

package wailsbridge

import (
	"os/exec"
	"syscall"
)

// setBackendProcessGroup puts the supervised backend into its own process
// group so Stop() can signal the whole tree: a real canopyd child that spawns
// helper processes must never survive the desktop window, and an orphaned
// grandchild holding the inherited stdio pipes would otherwise wedge the
// supervisor goroutine after the child exits.
func setBackendProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalBackendGroup delivers sig to the backend's process group (falling
// back to the bare process when the group is gone).
func signalBackendGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}
