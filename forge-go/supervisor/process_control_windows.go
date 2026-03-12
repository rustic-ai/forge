//go:build windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

func configureCommandForProcessGroup(cmd *exec.Cmd, detach bool) {
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
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

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}

	// Try a graceful interrupt first.
	_ = proc.Signal(os.Interrupt)

	for i := 0; i < 50; i++ {
		alive, err := gopsprocess.PidExists(int32(pid))
		if err != nil || !alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	return nil
}

func terminateAttachedProcessTree(pid int) error {
	descendants := descendantPIDs(pid)
	for _, childPID := range descendants {
		if proc, err := os.FindProcess(childPID); err == nil {
			_ = proc.Signal(os.Interrupt)
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	_ = proc.Signal(os.Interrupt)

	for i := 0; i < 50; i++ {
		alive, err := gopsprocess.PidExists(int32(pid))
		if err != nil || !alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, childPID := range descendants {
		alive, err := gopsprocess.PidExists(int32(childPID))
		if err == nil && alive {
			if childProc, findErr := os.FindProcess(childPID); findErr == nil {
				_ = childProc.Kill()
			}
		}
	}

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	return nil
}
