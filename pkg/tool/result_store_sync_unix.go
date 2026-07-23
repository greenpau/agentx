//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tool

import "os"

func syncResultDirectory(directory *os.File) error {
	return directory.Sync()
}
