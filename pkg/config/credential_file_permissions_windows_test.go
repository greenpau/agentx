//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsCredentialFileModeDoesNotStandInForDACL(t *testing.T) {
	if credentialFileAccessControlVerified {
		t.Fatal("Windows must not claim POSIX permission-bit enforcement")
	}
	for _, mode := range []os.FileMode{0o600, 0o644, 0o666} {
		if credentialFileModePermitsUse(mode) {
			t.Fatalf("synthesized Windows mode %04o was accepted as DACL evidence", mode.Perm())
		}
	}
}

func TestWindowsCredentialFileLoadFailsClosedWithoutDACLInspection(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultAuthFile)
	if err := os.WriteFile(path, []byte(AuthFilePlaceholder), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil, Overrides{}); err == nil ||
		!strings.Contains(err.Error(), "cannot verify owner-only") {
		t.Fatalf("Windows credential-file load error = %v", err)
	}
}
