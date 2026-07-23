//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package task

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

func syncTaskDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
