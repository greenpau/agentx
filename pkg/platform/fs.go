package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const defaultReadLimit int64 = 1 << 20

// PathEvidence exposes lexical and physical forms to an authorization owner.
// No field by itself is an authorization decision.
type PathEvidence struct {
	Original                string `json:"original"`
	LexicalAbsolute         string `json:"lexical_absolute"`
	DeepestExistingPhysical string `json:"deepest_existing_physical,omitempty"`
	Physical                string `json:"physical,omitempty"`
	Exists                  bool   `json:"exists"`
	Symlink                 bool   `json:"symlink"`
}

// InspectPath obtains conservative path evidence, including for a target that
// does not yet exist. A resolution failure never broadens the path.
func InspectPath(pathname, base string) (PathEvidence, error) {
	result := PathEvidence{Original: pathname}
	if pathname == "" {
		return result, errors.New("path is empty")
	}
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return result, fmt.Errorf("current directory: %w", err)
		}
	}
	if !filepath.IsAbs(pathname) {
		pathname = filepath.Join(base, pathname)
	}
	abs, err := filepath.Abs(filepath.Clean(pathname))
	if err != nil {
		return result, fmt.Errorf("absolute path: %w", err)
	}
	result.LexicalAbsolute = abs

	info, lstatErr := os.Lstat(abs)
	if lstatErr == nil {
		result.Exists = true
		result.Symlink = info.Mode()&os.ModeSymlink != 0
		if physical, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			result.Physical = filepath.Clean(physical)
			result.DeepestExistingPhysical = result.Physical
		}
		return result, nil
	}
	if !errors.Is(lstatErr, os.ErrNotExist) {
		return result, fmt.Errorf("inspect path %q: %w", abs, lstatErr)
	}

	ancestor := abs
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
		if _, statErr := os.Lstat(ancestor); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return result, fmt.Errorf("inspect ancestor %q: %w", ancestor, statErr)
		}
		physical, evalErr := filepath.EvalSymlinks(ancestor)
		if evalErr != nil {
			return result, fmt.Errorf("resolve ancestor %q: %w", ancestor, evalErr)
		}
		rel, relErr := filepath.Rel(ancestor, abs)
		if relErr != nil {
			return result, fmt.Errorf("reattach absent path: %w", relErr)
		}
		result.DeepestExistingPhysical = filepath.Clean(filepath.Join(physical, rel))
		break
	}
	return result, nil
}

// AtomicReplace writes a same-directory temporary file and renames it over
// the target. Failure before rename leaves the previous target intact.
func AtomicReplace(pathname string, data []byte, mode os.FileMode) error {
	if pathname == "" {
		return errors.New("atomic replace target is empty")
	}
	if mode.Perm() == 0 {
		return errors.New("atomic replace mode grants no owner access")
	}
	dir := filepath.Dir(pathname)
	name := filepath.Base(pathname)
	token := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return fmt.Errorf("temporary name: %w", err)
	}
	temporary := filepath.Join(dir, "."+name+"."+hex.EncodeToString(token)+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return fmt.Errorf("create replacement: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if err := writeAll(file, data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write replacement: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush replacement: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close replacement: %w", err)
	}
	if err := os.Rename(temporary, pathname); err != nil {
		return fmt.Errorf("activate replacement: %w", err)
	}
	remove = false
	return nil
}

// AppendFile appends through one descriptor. If the target is new, it is
// created atomically with the requested mode; an existing mode is unchanged.
func AppendFile(pathname string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(pathname, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, mode.Perm())
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(pathname, os.O_WRONLY|os.O_APPEND, 0)
	}
	if err != nil {
		return fmt.Errorf("open append target: %w", err)
	}
	defer file.Close()
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}

// ReadResult is a byte-bounded diagnostic view of a file snapshot.
type ReadResult struct {
	Data         []byte `json:"data"`
	Offset       int64  `json:"offset"`
	SnapshotSize int64  `json:"snapshot_size"`
	OmittedBytes int64  `json:"omitted_bytes"`
}

// ReadRange reads at most limit bytes from offset using the size observed at
// open time. Concurrent appends are deliberately outside the snapshot.
func ReadRange(pathname string, offset, limit int64) (ReadResult, error) {
	if offset < 0 {
		return ReadResult{}, errors.New("offset must not be negative")
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}
	file, err := os.Open(pathname)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ReadResult{}, err
	}
	size := info.Size()
	return readRangeSnapshot(file, offset, limit, size)
}

func readRangeSnapshot(file *os.File, offset, limit, size int64) (ReadResult, error) {
	result := ReadResult{Offset: offset, SnapshotSize: size}
	if offset >= size {
		return result, nil
	}
	remaining := size - offset
	count := min(remaining, limit)
	result.Data = make([]byte, count)
	n, err := file.ReadAt(result.Data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return ReadResult{}, err
	}
	result.Data = result.Data[:n]
	result.OmittedBytes = max(int64(0), remaining-int64(n))
	return result, nil
}

// ReadTail returns the final byte-bounded portion of a file snapshot.
func ReadTail(pathname string, limit int64) (ReadResult, error) {
	if limit <= 0 {
		limit = defaultReadLimit
	}
	file, err := os.Open(pathname)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ReadResult{}, err
	}
	offset := max(int64(0), info.Size()-limit)
	result, err := readRangeSnapshot(file, offset, limit, info.Size())
	if err != nil {
		return ReadResult{}, err
	}
	result.OmittedBytes = offset
	return result, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
