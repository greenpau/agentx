//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package memory

import (
	"os"
	"testing"
)

func TestUnixMemoryModeRequiresPrivatePermissionBits(t *testing.T) {
	if !memoryPOSIXPermissionsEnforced {
		t.Fatal("Unix must enforce POSIX permission bits")
	}
	for _, test := range []struct {
		mode os.FileMode
		want bool
	}{
		{mode: 0o700, want: true},
		{mode: 0o600, want: true},
		{mode: 0o750, want: false},
		{mode: 0o604, want: false},
		{mode: 0o777, want: false},
	} {
		if got := memoryModePermitsPrivateUse(test.mode); got != test.want {
			t.Errorf("mode %04o: got %t want %t", test.mode.Perm(), got, test.want)
		}
	}
}
