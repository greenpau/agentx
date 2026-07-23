//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package task

import (
	"errors"
	"os"
)

func openedFileLinkCount(_ *os.File, _ os.FileInfo) (uint64, error) {
	return 0, errors.New("regular-file link count is unsupported on this platform")
}

func syncTaskDirectory(string) error { return nil }
