//go:build unix || windows

package signals

import (
	"syscall"
	"testing"
)

func TestSIGTERMUsesConventionalExitCode(t *testing.T) {
	code, reason, ok := signalDisposition(syscall.SIGTERM)
	if !ok || code != 143 || reason != "sigterm" {
		t.Fatalf("SIGTERM disposition = %d %q %v", code, reason, ok)
	}
}
