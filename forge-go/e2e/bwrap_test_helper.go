package e2e

import (
	"os/exec"
)

// bubblewrapUsable returns true when bubblewrap can actually create the requested
// namespace sandbox in the current environment.
func bubblewrapUsable() bool {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return false
	}
	cmd := exec.Command(
		"bwrap",
		"--unshare-all",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--",
		"true",
	)
	return cmd.Run() == nil
}
