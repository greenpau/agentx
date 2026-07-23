//go:build windows

package platform

import (
	"testing"
	"unsafe"
)

func TestWindowsJobObjectLimitLayout(t *testing.T) {
	var limits jobObjectExtendedLimitInformation
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if limits.BasicLimitInformation.LimitFlags != 0x00002000 {
		t.Fatalf("kill-on-close limit = %#x", limits.BasicLimitInformation.LimitFlags)
	}

	wantSize := uintptr(112)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantSize = 144
	}
	if got := unsafe.Sizeof(limits); got != wantSize {
		t.Fatalf("JOB_OBJECT_EXTENDED_LIMIT_INFORMATION size = %d, want %d", got, wantSize)
	}
	wantIOOffset := uintptr(48)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantIOOffset = 64
	}
	if got := unsafe.Offsetof(limits.IOInfo); got != wantIOOffset {
		t.Fatalf("IO_COUNTERS offset = %d, want %d", got, wantIOOffset)
	}
}
