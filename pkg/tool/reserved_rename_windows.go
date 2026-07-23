//go:build windows

package tool

import "os"

func renameIntoReservedRoot(root *os.Root, source, destination string, placeholder os.FileInfo) error {
	// Windows rename does not replace an existing destination. The 128-bit
	// reserved name remains unguessable to ordinary concurrent writers; remove
	// only our verified placeholder before moving the target into it.
	if err := removeRootIfSame(root, destination, placeholder); err != nil {
		return err
	}
	return root.Rename(source, destination)
}
