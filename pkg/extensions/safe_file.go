package extensions

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadRegularFile takes a bounded, pinned snapshot of executable extension
// configuration. It rejects symlinks, hardlinks, group/other-writable files,
// and identity/content changes during the read.
func ReadRegularFile(path string, maximumBytes int64) ([]byte, error) {
	if maximumBytes <= 0 {
		return nil, errors.New("extension file byte limit must be positive")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("extension file is not a regular non-symlink file")
	}
	if before.Size() < 0 || before.Size() > maximumBytes {
		return nil, fmt.Errorf("extension file exceeds %d bytes", maximumBytes)
	}
	if before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("extension file is writable by group or others")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return readOpenedRegularFile(file, before, maximumBytes)
}

// readOpenedRegularFile owns and closes file. Keeping the descriptor-based
// checks together makes the pre-open and post-read snapshot comparison
// explicit and lets tests exercise mutation without timing-dependent races.
func readOpenedRegularFile(file *os.File, before os.FileInfo, maximumBytes int64) ([]byte, error) {
	opened, err := file.Stat()
	if err != nil || !sameExtensionFileSnapshot(before, opened) {
		_ = file.Close()
		return nil, errors.New("extension file changed while opening")
	}
	links, linkErr := openedExtensionFileLinkCount(file, opened)
	if linkErr != nil || links != 1 {
		_ = file.Close()
		if linkErr != nil {
			return nil, fmt.Errorf("inspect extension file link count: %w", linkErr)
		}
		return nil, errors.New("extension file must have exactly one filesystem link")
	}
	readLimit := maximumBytes
	if maximumBytes < int64(^uint64(0)>>1) {
		readLimit++
	}
	data, readErr := io.ReadAll(io.LimitReader(file, readLimit))
	final, finalErr := file.Stat()
	var finalLinks uint64
	var finalLinkErr error
	if finalErr == nil {
		finalLinks, finalLinkErr = openedExtensionFileLinkCount(file, final)
	}
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("extension file exceeds %d bytes", maximumBytes)
	}
	if finalErr != nil || finalLinkErr != nil || finalLinks != 1 || !sameExtensionFileSnapshot(before, final) || final.Size() != int64(len(data)) {
		return nil, errors.New("extension file changed while reading")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func sameExtensionFileSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil &&
		os.SameFile(left, right) &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) &&
		left.Mode() == right.Mode()
}
