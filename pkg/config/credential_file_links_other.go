//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"errors"
	"os"
)

func openedCredentialFileLinkCount(_ *os.File, _ os.FileInfo) (uint64, error) {
	return 0, errors.New("regular-file link count is unsupported on this platform")
}
