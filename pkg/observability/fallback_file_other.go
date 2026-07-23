//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package observability

import (
	"errors"
	"os"
)

func fallbackPlatformSupported() bool { return false }

// Some targets do not expose portable owner-only or link-count evidence.
// Never synthesize affirmative evidence on those platforms.
func fallbackFileLinkCount(*os.File, os.FileInfo) (uint64, error) {
	return 0, errors.New("fallback file link count is unavailable")
}

func fallbackFileModePrivate(os.FileInfo) bool { return false }

func fallbackDirectoryModePrivate(os.FileInfo) bool { return false }

func syncFallbackDirectory(*os.File) error {
	return nil
}
