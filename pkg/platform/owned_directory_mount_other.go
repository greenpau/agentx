//go:build !unix && !windows

package platform

import (
	"os"
)

func ownedDirectoryMountIdentityForHandle(*os.File, os.FileInfo) (ownedDirectoryMountIdentity, error) {
	return ownedDirectoryMountIdentity{}, ErrOwnedDirectoryFilesystemBoundary
}

func ownedDirectoryEntryIsFilesystemLink(os.FileInfo) bool {
	return true
}
