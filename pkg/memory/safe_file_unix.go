//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package memory

import (
	"errors"
	"os"
	"syscall"
)

func openedMemoryFileLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("regular-file link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}

func syncMemoryDirectory(directory *os.File) error { return directory.Sync() }
