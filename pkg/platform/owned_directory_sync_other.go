//go:build !unix && !windows

package platform

import (
	"errors"
	"os"
)

// A profile without a real directory durability primitive fails closed.
func syncOwnedDirectoryHandle(directory *os.File) error {
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("owned directory sync target is not a directory")
	}
	return ErrOwnedDirectorySyncUnsupported
}
