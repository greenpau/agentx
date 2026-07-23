//go:build windows

package task

import (
	"os"
	"syscall"
)

func openedFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}

// Windows does not expose portable directory fsync through os.File. The
// replace remains atomic; the file itself was flushed before the rename.
func syncTaskDirectory(string) error { return nil }
