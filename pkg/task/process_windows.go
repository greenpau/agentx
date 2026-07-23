//go:build windows

package task

import (
	"errors"
	"os"
	"os/exec"
)

func prepareProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = processWaitDelay
}

func verifyProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("owned process has not started")
	}
	return nil
}

func stopProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
