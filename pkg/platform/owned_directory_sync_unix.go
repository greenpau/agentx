//go:build unix

package platform

import "os"

func syncOwnedDirectoryHandle(directory *os.File) error {
	return directory.Sync()
}
