//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tool

import (
	"errors"
	"os"
	"syscall"
)

func openedFileLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("regular-file link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}

func openedFileDevice(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem identity is unavailable")
	}
	return uint64(stat.Dev), nil
}
