//go:build windows

package cli

import (
	"log/slog"
	"os/exec"
)

// setProcessGroup is a no-op on Windows, which has no POSIX process groups.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the server process on Windows. Without POSIX
// process groups any grandchildren are not reaped here; it is best-effort and
// runs during teardown, so failures are only logged.
func killProcessGroup(cmd *exec.Cmd) {
	if err := cmd.Process.Kill(); err != nil {
		slog.Debug("failed to kill server process", "error", err)
	}
}
