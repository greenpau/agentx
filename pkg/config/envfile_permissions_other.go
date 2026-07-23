//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import "os"

// Unknown platforms have no adapter proving that os.FileMode represents
// authoritative owner-only access. Fail closed until one is implemented.
const envFilePOSIXPermissionsEnforced = false

func envFileModePermitsCredentialUse(os.FileMode) bool { return false }

func envFileOwnerPermitsCredentialUse(os.FileInfo) bool { return false }
