//go:build !unix && !windows

package tool

import "os"

func renameIntoReservedRoot(root *os.Root, source, destination string, placeholder os.FileInfo) error {
	if err := removeRootIfSame(root, destination, placeholder); err != nil {
		return err
	}
	return root.Rename(source, destination)
}
