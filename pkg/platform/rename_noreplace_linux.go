//go:build linux

package platform

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const linuxRenameNoReplace = 1

func renameRootNoReplace(root *os.Root, source, destination string) (committed bool, resultErr error) {
	trap := linuxRenameat2Trap()
	if trap == 0 {
		return false, ErrAtomicRenameNoReplaceUnsupported
	}
	parent, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, parent.Close())
	}()
	sourcePointer, err := syscall.BytePtrFromString(source)
	if err != nil {
		return false, err
	}
	destinationPointer, err := syscall.BytePtrFromString(destination)
	if err != nil {
		return false, err
	}
	_, _, callErr := syscall.Syscall6(
		trap,
		parent.Fd(),
		uintptr(unsafe.Pointer(sourcePointer)),
		parent.Fd(),
		uintptr(unsafe.Pointer(destinationPointer)),
		linuxRenameNoReplace,
		0,
	)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(sourcePointer)
	runtime.KeepAlive(destinationPointer)
	if callErr != 0 {
		switch callErr {
		case syscall.ENOSYS, syscall.EINVAL, syscall.EOPNOTSUPP:
			return false, errors.Join(ErrAtomicRenameNoReplaceUnsupported, callErr)
		default:
			return false, callErr
		}
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

func linuxRenameat2Trap() uintptr {
	switch runtime.GOARCH {
	case "amd64":
		return 316
	case "arm64":
		return 276
	default:
		return 0
	}
}
