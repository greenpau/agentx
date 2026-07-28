//go:build !unix && !windows

package sessionlock

import (
	"os"
)

func sessionLocksSupported() bool { return false }

func tryLockFile(*os.File) (bool, error) {
	return false, ErrUnsupported
}

func unlockFile(*os.File) error { return nil }

// tryLockFile always rejects this platform before a lock can be returned. A
// neutral count keeps the user-facing failure attributed to missing lock
// support rather than incorrectly describing the path as unsafe.
func openedFileLinkCount(*os.File, os.FileInfo) (uint64, error) { return 1, nil }
