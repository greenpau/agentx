//go:build windows

package observability

import (
	"os"
	"syscall"
)

// Go's portable Windows file metadata does not prove that a directory or file
// has an owner-only DACL. Durable fallback therefore fails at construction
// instead of treating Unix-style permission bits as ACL evidence.
func fallbackPlatformSupported() bool { return false }

func fallbackFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}

func fallbackFileModePrivate(info os.FileInfo) bool {
	return false
}

func fallbackDirectoryModePrivate(info os.FileInfo) bool {
	return false
}

// Windows does not expose a portable directory-fsync operation through
// os.File. Each fallback file is still flushed before activation.
func syncFallbackDirectory(*os.File) error {
	return nil
}
