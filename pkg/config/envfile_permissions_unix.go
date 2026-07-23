//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"os"
	"syscall"
)

const envFilePOSIXPermissionsEnforced = true

func envFileModePermitsCredentialUse(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}

func envFileOwnerPermitsCredentialUse(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Uid) == uint64(os.Geteuid())
}
