//go:build windows

package memory

import "os"

const memoryPOSIXPermissionsEnforced = false

// Windows os.FileMode permission bits are synthesized from file attributes;
// they do not describe the file or directory DACL. Store still pins root/file
// identities, rejects symlinks and multi-link files, and uses bounded I/O. The
// portable implementation cannot prove or establish owner-only ACLs.
func memoryModePermitsPrivateUse(os.FileMode) bool { return true }
