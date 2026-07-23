//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

import "os"

func openEnvFile(path string) (*os.File, error) {
	return os.Open(path)
}
