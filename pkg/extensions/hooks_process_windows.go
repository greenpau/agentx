//go:build windows

package extensions

import (
	"os/exec"
	"time"
)

func configureHookCommand(command *exec.Cmd) {
	command.WaitDelay = 250 * time.Millisecond
}
