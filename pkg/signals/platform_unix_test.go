//go:build unix

package signals

import (
	"syscall"
	"testing"
)

func TestSIGHUPUsesConventionalExitCode(t *testing.T) {
	code, reason, ok := signalDisposition(syscall.SIGHUP)
	if !ok || code != 129 || reason != "sighup" {
		t.Fatalf("SIGHUP disposition = %d %q %v", code, reason, ok)
	}
}
