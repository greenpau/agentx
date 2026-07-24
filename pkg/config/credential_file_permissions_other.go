//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import "os"

// Unknown platforms have no adapter proving that os.FileMode represents
// authoritative owner-only access. Fail closed until one is implemented.
const credentialFileAccessControlVerified = false

func credentialFileModePermitsUse(os.FileMode) bool { return false }

func credentialFileOwnerPermitsUse(os.FileInfo) bool { return false }
