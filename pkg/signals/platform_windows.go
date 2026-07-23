//go:build windows

package signals

import (
	"os"
	"syscall"
)

func platformSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func signalDisposition(value os.Signal) (int, string, bool) {
	switch value {
	case os.Interrupt:
		return 130, "sigint", true
	case syscall.SIGTERM:
		return 143, "sigterm", true
	default:
		return 0, "", false
	}
}
