//go:build windows

package attachment

import (
	"os"
	"syscall"
)

func regularFileLinkCount(file *os.File, _ os.FileInfo) (uint64, bool) {
	if file == nil {
		return 0, false
	}
	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &data); err != nil {
		return 0, false
	}
	return uint64(data.NumberOfLinks), true
}
