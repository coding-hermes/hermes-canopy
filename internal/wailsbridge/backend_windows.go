//go:build windows

package wailsbridge

import (
	"os/exec"
	"syscall"
)

// setBackendProcessGroup on Windows: no POSIX process groups. The Taskkill
// tree-kill in signalBackendGroup covers child teardown; process-group setup
// is a no-op here.
func setBackendProcessGroup(cmd *exec.Cmd) {}

// signalBackendGroup on Windows: kill the process tree via taskkill /T, the
// documented Windows equivalent of signalling a process group, falling back
// to the bare process handle when taskkill is unavailable.
func signalBackendGroup(cmd *exec.Cmd, _ syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if taskkill, err := exec.LookPath("taskkill"); err == nil {
		if err := exec.Command(taskkill, "/PID", itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}

// itoa is a dependency-free int→string for the taskkill argument.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
