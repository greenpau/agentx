//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package platform

import (
	"os"
	"syscall"
)

const privateDirectoryAccessControlVerified = true

func privateDirectoryAccessPermitsUse(info os.FileInfo) bool {
	return privateDirectoryOwnerPermitsUse(info) && info.Mode().Perm()&0o077 == 0
}

func privateDirectoryOwnerPermitsUse(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Uid) == uint64(os.Geteuid())
}
