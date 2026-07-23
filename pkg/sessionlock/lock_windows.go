//go:build windows

package sessionlock

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusive       = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32DLL      = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32DLL.NewProc("LockFileEx")
	unlockFileExProc = kernel32DLL.NewProc("UnlockFileEx")
)

func tryLockFile(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileExProc.Call(
		file.Fd(), lockFileFailImmediately|lockFileExclusive, 0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	runtime.KeepAlive(&overlapped)
	if result != 0 {
		return true, nil
	}
	if callErr == errorLockViolation {
		return false, nil
	}
	return false, callErr
}

func unlockFile(file *os.File) error {
	if file == nil {
		return nil
	}
	var overlapped syscall.Overlapped
	result, _, callErr := unlockFileExProc.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(&overlapped)
	if result == 0 {
		return callErr
	}
	return nil
}

func openedFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
