//go:build windows && 386

package platform

// jobObjectBasicLimitInformation mirrors the Windows ABI. The native 32-bit
// structure is padded to an eight-byte boundary before the following IO
// counters in JOB_OBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	_                       uint32
}
