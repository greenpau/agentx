package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// readMemorySnapshot pins a bounded memory file to one descriptor. Memory is
// model-visible context, so files introduced outside Remember must satisfy the
// same private-file assumptions and cannot be pathname-swapped while read.
func readMemorySnapshot(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("memory is not a regular non-symlink file")
	}
	if before.Size() < 0 || before.Size() > maxMemoryFileBytes {
		return nil, nil, fmt.Errorf("memory exceeds %d bytes", maxMemoryFileBytes)
	}
	if !memoryModePermitsPrivateUse(before.Mode()) {
		return nil, nil, errors.New("memory permissions must not grant group or other access")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, errors.New("memory changed while opening")
	}
	links, linkErr := openedMemoryFileLinkCount(file, opened)
	if linkErr != nil || links != 1 {
		_ = file.Close()
		if linkErr != nil {
			return nil, nil, fmt.Errorf("inspect memory link count: %w", linkErr)
		}
		return nil, nil, errors.New("memory must have exactly one filesystem link")
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxMemoryFileBytes+1))
	final, finalErr := file.Stat()
	var finalLinks uint64
	var finalLinkErr error
	if finalErr == nil {
		finalLinks, finalLinkErr = openedMemoryFileLinkCount(file, final)
	}
	closeErr := file.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if len(data) > maxMemoryFileBytes {
		return nil, nil, fmt.Errorf("memory exceeds %d bytes", maxMemoryFileBytes)
	}
	if finalErr != nil || finalLinkErr != nil || finalLinks != 1 || !os.SameFile(before, final) || before.Size() != final.Size() || final.Size() != int64(len(data)) || !before.ModTime().Equal(final.ModTime()) || before.Mode() != final.Mode() {
		return nil, nil, errors.New("memory changed while reading")
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	return data, final, nil
}
