//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package platform

import "os"

const privateDirectoryAccessControlVerified = false

func privateDirectoryAccessPermitsUse(os.FileInfo) bool { return false }

func privateDirectoryOwnerPermitsUse(os.FileInfo) bool { return false }
