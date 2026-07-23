//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package memory

import (
	"errors"
	"os"
)

func openedMemoryFileLinkCount(_ *os.File, _ os.FileInfo) (uint64, error) {
	return 0, errors.New("regular-file link count is unavailable on this platform")
}

func syncMemoryDirectory(*os.File) error { return nil }
