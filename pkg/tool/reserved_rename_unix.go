//go:build unix

package tool

import "os"

func renameIntoReservedRoot(root *os.Root, source, destination string, _ os.FileInfo) error {
	// POSIX rename atomically replaces the placeholder that this process created
	// with O_EXCL, so the backup destination itself cannot be clobbered by a
	// normal concurrent creator.
	return root.Rename(source, destination)
}
