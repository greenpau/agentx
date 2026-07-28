//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package transcript

import (
	"errors"
	"os"
)

func openedTranscriptLinkCount(_ *os.File, _ os.FileInfo) (uint64, error) {
	return 0, errors.New("transcript link count is unsupported on this platform")
}

func openedFilesystemIdentity(_ *os.File, _ os.FileInfo) (string, uint64, error) {
	return "", 0, errors.New("filesystem identity is unsupported on this platform")
}

func syncTranscriptDirectory(*os.File) error { return nil }
