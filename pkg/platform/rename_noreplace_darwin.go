//go:build darwin

package platform

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	darwinRenameatxNP                     = 488
	darwinRenameExcl                      = 0x4
	darwinAttributeBitmapCount            = 5
	darwinAttributeVolumeInfo             = 0x80000000
	darwinAttributeVolumeCapabilities     = 0x00020000
	darwinVolumeCapabilitiesInterfaces    = 1
	darwinVolumeCapabilityRenameExclusive = 0x00080000
	darwinFSOptionNoFollow                = 0x00000001
)

type darwinAttributeList struct {
	BitmapCount uint16
	Reserved    uint16
	Common      uint32
	Volume      uint32
	Directory   uint32
	File        uint32
	Fork        uint32
}

type darwinVolumeCapabilities struct {
	Capabilities [4]uint32
	Valid        [4]uint32
}

type darwinVolumeCapabilitiesBuffer struct {
	Length       uint32
	Capabilities darwinVolumeCapabilities
}

func renameRootNoReplace(root *os.Root, source, destination string) (committed bool, resultErr error) {
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
		darwinRenameatxNP,
		parent.Fd(),
		uintptr(unsafe.Pointer(sourcePointer)),
		parent.Fd(),
		uintptr(unsafe.Pointer(destinationPointer)),
		darwinRenameExcl,
		0,
	)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(sourcePointer)
	runtime.KeepAlive(destinationPointer)
	if callErr != 0 {
		switch callErr {
		case syscall.ENOSYS, syscall.EINVAL, syscall.ENOTSUP:
			return false, errors.Join(ErrAtomicRenameNoReplaceUnsupported, callErr)
		default:
			return false, callErr
		}
	}
	return true, nil
}

func preflightRootNoReplace(root *os.Root, existingName string) (resultErr error) {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, parent.Close())
	}()
	attributes := darwinAttributeList{
		BitmapCount: darwinAttributeBitmapCount,
		Volume:      darwinAttributeVolumeInfo | darwinAttributeVolumeCapabilities,
	}
	var capabilities darwinVolumeCapabilitiesBuffer
	_, _, callErr := syscall.Syscall6(
		syscall.SYS_FGETATTRLIST,
		parent.Fd(),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&capabilities)),
		unsafe.Sizeof(capabilities),
		darwinFSOptionNoFollow,
		0,
	)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(&attributes)
	runtime.KeepAlive(&capabilities)
	if callErr != 0 {
		return errors.Join(ErrAtomicRenameNoReplaceUnsupported, callErr)
	}
	if capabilities.Length < uint32(unsafe.Sizeof(capabilities)) {
		return ErrAtomicRenameNoReplaceUnsupported
	}
	const index = darwinVolumeCapabilitiesInterfaces
	const capability = darwinVolumeCapabilityRenameExclusive
	if capabilities.Capabilities.Valid[index]&capability == 0 ||
		capabilities.Capabilities.Capabilities[index]&capability == 0 {
		return ErrAtomicRenameNoReplaceUnsupported
	}
	committed, err := renameRootNoReplace(root, existingName, existingName)
	if committed || errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
