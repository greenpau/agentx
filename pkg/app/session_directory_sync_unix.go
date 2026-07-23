//go:build unix

package app

import (
	"errors"
	"os"
)

func syncSessionDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
