//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package transcript

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openedTranscriptLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	_, links, err := openedFilesystemIdentity(nil, info)
	return links, err
}

func openedFilesystemIdentity(_ *os.File, info os.FileInfo) (string, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, errors.New("filesystem identity is unavailable")
	}
	return fmt.Sprintf("%x:%x", uint64(stat.Dev), uint64(stat.Ino)), uint64(stat.Nlink), nil
}

func syncTranscriptDirectory(directory *os.File) error { return directory.Sync() }
