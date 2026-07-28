//go:build windows

package platform

import (
	"errors"
	"os"
	"syscall"
)

func ownedDirectoryMountIdentityForHandle(file *os.File, _ os.FileInfo) (ownedDirectoryMountIdentity, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return ownedDirectoryMountIdentity{}, err
	}
	if info.VolumeSerialNumber == 0 {
		return ownedDirectoryMountIdentity{}, errors.New("directory volume identity is unavailable")
	}
	return ownedDirectoryMountIdentity{device: uint64(info.VolumeSerialNumber)}, nil
}

func ownedDirectoryEntryIsFilesystemLink(info os.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
