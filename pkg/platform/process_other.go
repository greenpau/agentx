//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package platform

import "os/exec"

func configureOwnedProcess(_ *exec.Cmd) {}
