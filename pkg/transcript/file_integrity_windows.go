//go:build windows

package transcript

import (
	"fmt"
	"os"
	"syscall"
)

func openedTranscriptLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	_, links, err := openedFilesystemIdentity(file, nil)
	return links, err
}

func openedFilesystemIdentity(file *os.File, _ os.FileInfo) (string, uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return "", 0, err
	}
	identity := fmt.Sprintf(
		"%x:%x:%x",
		uint64(info.VolumeSerialNumber),
		uint64(info.FileIndexHigh),
		uint64(info.FileIndexLow),
	)
	return identity, uint64(info.NumberOfLinks), nil
}

// Windows does not expose a portable directory-fsync operation through
// os.File. The transcript file itself is flushed before this no-op boundary.
func syncTranscriptDirectory(*os.File) error { return nil }
