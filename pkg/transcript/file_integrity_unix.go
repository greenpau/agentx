//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package transcript

import (
	"errors"
	"os"
	"syscall"
)

func openedTranscriptLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("transcript link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}

func syncTranscriptDirectory(directory *os.File) error { return directory.Sync() }
