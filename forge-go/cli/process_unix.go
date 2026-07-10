//go:build !windows

package cli

import (
	"log/slog"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group so that the whole
// tree can be signalled together during teardown.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the child's entire process group. It is best-effort:
// failures are logged rather than returned, since it runs during teardown.
func killProcessGroup(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		slog.Debug("failed to signal server process group", "error", err)
	}
}
