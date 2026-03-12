//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
	"time"
)

func configureCommandForProcessGroup(cmd *exec.Cmd, detach bool) {
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return nil
		}
		return
	}

	cmd.SysProcAttr = nil
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return terminateAttachedProcessTree(cmd.Process.Pid)
		}
		return nil
	}
}

func terminateProcessTree(pid int, detach bool) error {
	if !detach {
		return terminateAttachedProcessTree(pid)
	}

	pgid, err := syscall.Getpgid(pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if syscall.Kill(pid, 0) == nil {
		if pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	return nil
}

func terminateAttachedProcessTree(pid int) error {
	descendants := descendantPIDs(pid)
	for _, childPID := range descendants {
		_ = syscall.Kill(childPID, syscall.SIGTERM)
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)

	for i := 0; i < 50; i++ {
		if !processExists(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, childPID := range descendants {
		if processExists(childPID) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	}
	if processExists(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	return nil
}

func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
