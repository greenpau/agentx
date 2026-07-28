//go:build windows

package platform

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	windowsDeleteAccess            = 0x00010000
	windowsSynchronizeAccess       = 0x00100000
	windowsFileRenameInfoClass     = 3
	windowsErrorInvalidFunction    = syscall.Errno(1)
	windowsErrorNotSupported       = syscall.Errno(50)
	windowsErrorInvalidParameter   = syscall.Errno(87)
	windowsErrorCallNotImplemented = syscall.Errno(120)
	windowsErrorFileExists         = syscall.Errno(80)
	windowsErrorAlreadyExists      = syscall.Errno(183)
)

var (
	renameKernel32DLL                    = syscall.NewLazyDLL("kernel32.dll")
	renameReOpenFileProc                 = renameKernel32DLL.NewProc("ReOpenFile")
	renameSetFileInformationByHandleProc = renameKernel32DLL.NewProc("SetFileInformationByHandle")
)

type windowsFileRenameInfo struct {
	ReplaceIfExists uint32
	RootDirectory   syscall.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameRootNoReplace(root *os.Root, source, destination string) (committed bool, resultErr error) {
	parent, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, parent.Close())
	}()
	sourceFile, err := root.Open(source)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, sourceFile.Close())
	}()

	reopenedRaw, _, callErr := renameReOpenFileProc.Call(
		sourceFile.Fd(),
		windowsDeleteAccess|windowsSynchronizeAccess,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
	)
	reopened := syscall.Handle(reopenedRaw)
	if reopened == 0 || reopened == syscall.InvalidHandle {
		return false, normalizeWindowsRenameError(callErr)
	}
	defer func() {
		resultErr = errors.Join(resultErr, syscall.CloseHandle(reopened))
	}()

	name, err := syscall.UTF16FromString(destination)
	if err != nil {
		return false, err
	}
	name = name[:len(name)-1]
	headerSize := int(unsafe.Offsetof(windowsFileRenameInfo{}.FileName))
	buffer := make([]byte, headerSize+len(name)*2)
	info := (*windowsFileRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = syscall.Handle(parent.Fd())
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(info), headerSize)), len(name)), name)

	result, _, callErr := renameSetFileInformationByHandleProc.Call(
		uintptr(reopened),
		windowsFileRenameInfoClass,
		uintptr(unsafe.Pointer(info)),
		uintptr(len(buffer)),
	)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(sourceFile)
	runtime.KeepAlive(buffer)
	if result == 0 {
		return false, normalizeWindowsRenameError(callErr)
	}
	return true, nil
}

func preflightRootNoReplace(root *os.Root, existingName string) error {
	committed, err := renameRootNoReplace(root, existingName, existingName)
	if committed || errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func normalizeWindowsRenameError(err error) error {
	errno, ok := err.(syscall.Errno)
	if !ok || errno == 0 {
		return syscall.EINVAL
	}
	switch errno {
	case windowsErrorFileExists, windowsErrorAlreadyExists:
		return errors.Join(os.ErrExist, errno)
	case windowsErrorInvalidFunction,
		windowsErrorNotSupported,
		windowsErrorInvalidParameter,
		windowsErrorCallNotImplemented:
		return errors.Join(ErrAtomicRenameNoReplaceUnsupported, errno)
	default:
		return errno
	}
}
