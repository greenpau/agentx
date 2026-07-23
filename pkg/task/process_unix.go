//go:build unix

package task

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = processWaitDelay
}

func verifyProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("owned process has not started")
	}
	group, err := syscall.Getpgid(cmd.Process.Pid)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		// The child can finish between Start and verification. Setpgid was part
		// of the pre-exec child setup, so a vanished leader is not evidence that
		// containment failed and Wait must still reap it exactly once.
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect owned process group: %w", err)
	}
	if group != cmd.Process.Pid {
		return fmt.Errorf("owned process group %d does not match leader %d", group, cmd.Process.Pid)
	}
	return nil
}

func stopProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Freeze the entire group before killing it. A direct group SIGKILL can be
	// observed member-by-member: a shell may wake after its child dies and run a
	// final command before the kernel delivers SIGKILL to the shell. SIGSTOP
	// removes that side-effect window, then SIGKILL makes termination final.
	freezeErr := normalizeProcessSignalError(syscall.Kill(-cmd.Process.Pid, syscall.SIGSTOP))
	killErr := normalizeProcessSignalError(syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
	if killErr != nil {
		return errors.Join(freezeErr, killErr)
	}
	return freezeErr
}

func normalizeProcessSignalError(err error) error {
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
