//go:build windows

package platform

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

const (
	windowsGenericWriteAccess   = 0x40000000
	windowsFileFlagWriteThrough = 0x80000000
)

var (
	syncKernel32DLL          = syscall.NewLazyDLL("kernel32.dll")
	syncReOpenFileProc       = syncKernel32DLL.NewProc("ReOpenFile")
	syncFlushFileBuffersProc = syncKernel32DLL.NewProc("FlushFileBuffers")
)

type windowsDirectoryIdentity struct {
	volume    uint32
	indexHigh uint32
	indexLow  uint32
}

func syncOwnedDirectoryHandle(directory *os.File) (resultErr error) {
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("owned directory sync target is not a directory")
	}
	original, err := windowsOwnedDirectoryIdentity(syscall.Handle(directory.Fd()))
	if err != nil {
		return errors.Join(ErrOwnedDirectorySyncUnsupported, err)
	}
	reopenedRaw, _, callErr := syncReOpenFileProc.Call(
		directory.Fd(),
		windowsGenericWriteAccess,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|
			syscall.FILE_FLAG_OPEN_REPARSE_POINT|
			windowsFileFlagWriteThrough,
	)
	reopened := syscall.Handle(reopenedRaw)
	if reopened == 0 || reopened == syscall.InvalidHandle {
		return errors.Join(ErrOwnedDirectorySyncUnsupported, normalizeWindowsCallError(callErr))
	}
	defer func() {
		resultErr = errors.Join(resultErr, syscall.CloseHandle(reopened))
	}()
	reopenedIdentity, err := windowsOwnedDirectoryIdentity(reopened)
	if err != nil {
		return errors.Join(ErrOwnedDirectorySyncUnsupported, err)
	}
	if original != reopenedIdentity {
		return ErrDirectoryIdentityChanged
	}
	result, _, callErr := syncFlushFileBuffersProc.Call(uintptr(reopened))
	runtime.KeepAlive(directory)
	if result == 0 {
		return errors.Join(ErrOwnedDirectorySyncUnsupported, normalizeWindowsCallError(callErr))
	}
	return nil
}

func windowsOwnedDirectoryIdentity(handle syscall.Handle) (windowsDirectoryIdentity, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return windowsDirectoryIdentity{}, err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return windowsDirectoryIdentity{}, ErrDirectoryIdentityChanged
	}
	return windowsDirectoryIdentity{
		volume:    info.VolumeSerialNumber,
		indexHigh: info.FileIndexHigh,
		indexLow:  info.FileIndexLow,
	}, nil
}

func normalizeWindowsCallError(err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno != 0 {
		return errno
	}
	return syscall.EINVAL
}
