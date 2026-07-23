//go:build windows

package memory

import (
	"os"
	"testing"
)

func TestWindowsMemoryModeDoesNotStandInForDACL(t *testing.T) {
	if memoryPOSIXPermissionsEnforced {
		t.Fatal("Windows must not claim POSIX permission-bit enforcement")
	}
	for _, mode := range []os.FileMode{0o600, 0o644, 0o666} {
		if !memoryModePermitsPrivateUse(mode) {
			t.Fatalf("synthesized Windows mode %04o was treated as DACL evidence", mode.Perm())
		}
	}
}
