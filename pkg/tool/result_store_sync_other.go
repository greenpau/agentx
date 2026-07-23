//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package tool

import "os"

// Directory fsync is not portable on these platforms. Result and index files
// are still flushed before this durability boundary.
func syncResultDirectory(*os.File) error {
	return nil
}
