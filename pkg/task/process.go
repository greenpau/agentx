package task

import "os/exec"

// PrepareOwnedProcess configures cmd so descendants share the cancellation
// boundary implemented by StopOwnedProcess. The caller remains responsible for
// calling Wait exactly once after every successful Start.
func PrepareOwnedProcess(cmd *exec.Cmd) {
	prepareProcess(cmd)
}

// VerifyOwnedProcess confirms after Start that the platform established the
// containment boundary requested by PrepareOwnedProcess. Callers must stop and
// reap the command if verification fails.
func VerifyOwnedProcess(cmd *exec.Cmd) error {
	return verifyProcess(cmd)
}

// StopOwnedProcess terminates cmd and the descendants covered by the active
// platform process boundary. It is idempotent after process termination.
func StopOwnedProcess(cmd *exec.Cmd) error {
	return stopProcess(cmd)
}
