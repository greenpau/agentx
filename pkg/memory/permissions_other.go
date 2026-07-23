//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package memory

import "os"

const memoryPOSIXPermissionsEnforced = true

func memoryModePermitsPrivateUse(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}
