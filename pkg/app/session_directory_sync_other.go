//go:build !unix

package app

import "os"

// Windows and other non-Unix targets do not expose a portable directory-fsync
// contract through os.File. Opening and statting the directory still verifies
// that publication is directed at an extant directory; file contents are
// flushed separately before marker activation.
func syncSessionDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := directory.Stat(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
