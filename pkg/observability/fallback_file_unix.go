//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package observability

import (
	"errors"
	"os"
	"syscall"
)

func fallbackPlatformSupported() bool { return true }

func fallbackFileLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("fallback file link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}

func fallbackFileModePrivate(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	permissions := info.Mode().Perm()
	return permissions&0o077 == 0 && permissions&0o400 != 0
}

func fallbackDirectoryModePrivate(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0o077 == 0
}

func syncFallbackDirectory(directory *os.File) error {
	return directory.Sync()
}
