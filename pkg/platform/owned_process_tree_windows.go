//go:build windows

package platform

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	kernel32DLL              = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32DLL.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32DLL.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32DLL.NewProc("AssignProcessToJobObject")
	getCurrentProcess        = kernel32DLL.NewProc("GetCurrentProcess")
	closeHandle              = kernel32DLL.NewProc("CloseHandle")
	ownedProcessTreeMu       sync.Mutex
	ownedProcessTreeJob      syscall.Handle
)

// EnableOwnedProcessTree assigns AgentX to a Windows Job Object configured to
// terminate every associated process when its final handle closes. The handle
// is intentionally retained in process-global state and must not be closed by
// ordinary session cleanup: an abrupt AgentX exit is the containment signal.
//
// A parent job that disallows nested assignment or any unavailable Job Object
// primitive makes this requested startup guarantee fail closed.
func EnableOwnedProcessTree() error {
	ownedProcessTreeMu.Lock()
	defer ownedProcessTreeMu.Unlock()
	if ownedProcessTreeJob != 0 {
		return nil
	}

	job, _, callErr := createJobObjectW.Call(0, 0)
	if job == 0 {
		return windowsCallError("create Windows Job Object", callErr)
	}
	jobHandle := syscall.Handle(job)
	closeOnFailure := func() {
		_, _, _ = closeHandle.Call(uintptr(jobHandle))
	}

	limits := jobObjectExtendedLimitInformation{}
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	set, _, callErr := setInformationJobObject.Call(
		uintptr(jobHandle),
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&limits)),
		unsafe.Sizeof(limits),
	)
	runtime.KeepAlive(&limits)
	if set == 0 {
		closeOnFailure()
		return windowsCallError("set kill-on-close Job Object limit", callErr)
	}

	process, _, callErr := getCurrentProcess.Call()
	if process == 0 {
		closeOnFailure()
		return windowsCallError("get current Windows process", callErr)
	}
	assigned, _, callErr := assignProcessToJobObject.Call(uintptr(jobHandle), process)
	if assigned == 0 {
		closeOnFailure()
		return windowsCallError("assign AgentX to Windows Job Object", callErr)
	}

	ownedProcessTreeJob = jobHandle
	return nil
}

func windowsCallError(operation string, callErr error) error {
	if callErr == nil || callErr == syscall.Errno(0) {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, callErr)
}
