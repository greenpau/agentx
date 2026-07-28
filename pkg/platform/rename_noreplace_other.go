//go:build !darwin && !linux && !windows

package platform

import "os"

func renameRootNoReplace(*os.Root, string, string) (bool, error) {
	return false, ErrAtomicRenameNoReplaceUnsupported
}

func preflightRootNoReplace(*os.Root, string) error {
	return ErrAtomicRenameNoReplaceUnsupported
}
