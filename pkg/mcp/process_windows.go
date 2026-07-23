//go:build windows

package mcp

import (
	"os/exec"
	"time"
)

func configureMCPCommand(command *exec.Cmd) {
	command.WaitDelay = 500 * time.Millisecond
}

func forceKillMCPCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
