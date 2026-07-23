//go:build unix

package signals

import (
	"os"
	"syscall"
)

func platformSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

func signalDisposition(value os.Signal) (int, string, bool) {
	switch value {
	case syscall.SIGINT:
		return 128 + int(syscall.SIGINT), "sigint", true
	case syscall.SIGTERM:
		return 128 + int(syscall.SIGTERM), "sigterm", true
	case syscall.SIGHUP:
		return 128 + int(syscall.SIGHUP), "sighup", true
	default:
		return 0, "", false
	}
}
