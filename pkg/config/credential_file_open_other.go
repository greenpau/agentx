//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

import "os"

func openCredentialFile(root *os.Root, name, path string) (*os.File, error) {
	if root != nil {
		return root.OpenFile(name, os.O_RDONLY, 0)
	}
	return os.Open(path)
}
