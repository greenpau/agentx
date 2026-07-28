//go:build unix && !linux

package platform

import (
	"errors"
	"os"
	"syscall"
)

func ownedDirectoryMountIdentityForHandle(_ *os.File, info os.FileInfo) (ownedDirectoryMountIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return ownedDirectoryMountIdentity{}, errors.New("directory device identity is unavailable")
	}
	return ownedDirectoryMountIdentity{device: uint64(stat.Dev)}, nil
}

func ownedDirectoryEntryIsFilesystemLink(os.FileInfo) bool {
	return false
}
