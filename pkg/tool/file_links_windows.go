//go:build windows

package tool

import (
	"os"
	"syscall"
)

func openedFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	info, err := openedWindowsFileInformation(file)
	if err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}

func openedFileDevice(file *os.File, _ os.FileInfo) (uint64, error) {
	info, err := openedWindowsFileInformation(file)
	if err != nil {
		return 0, err
	}
	return uint64(info.VolumeSerialNumber), nil
}

func openedWindowsFileInformation(file *os.File) (syscall.ByHandleFileInformation, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return syscall.ByHandleFileInformation{}, err
	}
	return info, nil
}
