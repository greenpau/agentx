//go:build windows

package platform

import "os"

// os.FileMode does not expose the owner or DACL on Windows. Directory
// acquisition retains identity and no-follow guarantees, but cannot claim or
// recheck owner-only access until a native ACL adapter is available. The
// credential-file reader separately fails closed before reading auth.json.
const privateDirectoryAccessControlVerified = false

func privateDirectoryAccessPermitsUse(os.FileInfo) bool { return false }

func privateDirectoryOwnerPermitsUse(os.FileInfo) bool { return false }
