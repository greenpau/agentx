//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package platform

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureOwnedProcess places the child in an owned process group so context
// cancellation cannot leave descendants behind. SIGKILL is deliberate here:
// CommandContext has already reached its terminal cancellation boundary.
func configureOwnedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
}
