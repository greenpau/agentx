//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixCredentialFileModeRequiresPrivatePermissionBits(t *testing.T) {
	if !credentialFileAccessControlVerified {
		t.Fatal("Unix must enforce POSIX permission bits")
	}
	for _, test := range []struct {
		mode os.FileMode
		want bool
	}{
		{mode: 0o600, want: true},
		{mode: 0o400, want: true},
		{mode: 0o640, want: false},
		{mode: 0o604, want: false},
		{mode: 0o666, want: false},
	} {
		if got := credentialFileModePermitsUse(test.mode); got != test.want {
			t.Errorf("mode %04o: got %t want %t", test.mode.Perm(), got, test.want)
		}
	}
}

func TestUnixCredentialFileOwnerMatchesEffectiveUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultAuthFile)
	if err := os.WriteFile(path, []byte(AuthFilePlaceholder), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !credentialFileOwnerPermitsUse(info) {
		t.Fatal("effective user's private credential file was rejected")
	}
}
