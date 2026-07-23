//go:build windows

package config

import (
	"os"
	"testing"
)

func TestWindowsEnvFileModeDoesNotStandInForDACL(t *testing.T) {
	if envFilePOSIXPermissionsEnforced {
		t.Fatal("Windows must not claim POSIX permission-bit enforcement")
	}
	for _, mode := range []os.FileMode{0o600, 0o644, 0o666} {
		if envFileModePermitsCredentialUse(mode) {
			t.Fatalf("synthesized Windows mode %04o was accepted as DACL evidence", mode.Perm())
		}
	}
}
