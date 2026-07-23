//go:build windows

package platform

import "os/exec"

// Windows CommandContext retains its native child cancellation. A job-object
// adapter can replace this when the build includes one.
func configureOwnedProcess(_ *exec.Cmd) {}
