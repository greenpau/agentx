//go:build !unix && !windows

package signals

import (
	"os"
	"testing"
)

func TestFallbackPlatformInterruptDisposition(t *testing.T) {
	code, reason, ok := signalDisposition(os.Interrupt)
	if !ok || code != 130 || reason != "interrupt" {
		t.Fatalf("interrupt disposition = %d %q %v", code, reason, ok)
	}
}
