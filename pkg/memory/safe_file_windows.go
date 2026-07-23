//go:build windows

package memory

import (
	"errors"
	"os"
	"syscall"
)

func openedMemoryFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &information); err != nil {
		return 0, err
	}
	if information.NumberOfLinks == 0 {
		return 0, errors.New("regular-file link count is invalid")
	}
	return uint64(information.NumberOfLinks), nil
}

// Windows does not expose a portable directory fsync through os.File. The
// replacement file itself is flushed before this boundary.
func syncMemoryDirectory(*os.File) error { return nil }
