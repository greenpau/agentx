//go:build !unix && !windows

package signals

import "os"

func platformSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func signalDisposition(value os.Signal) (int, string, bool) {
	if value == os.Interrupt {
		return 130, "interrupt", true
	}
	return 0, "", false
}
