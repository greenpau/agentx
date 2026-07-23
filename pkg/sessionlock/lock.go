// Package sessionlock owns the cross-process lease that prevents two runtimes
// from mutating one session's transcript and task state concurrently.
package sessionlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	// ErrContended reports a healthy lock held by another process.
	ErrContended = errors.New("session is already active in another process")
	// ErrUnsafePath reports a lock destination that is not a stable regular
	// file in its expected private session directory.
	ErrUnsafePath = errors.New("session lock path is unsafe")
)

// Lock retains an advisory operating-system lock for its file descriptor's
// lifetime. The lock file itself intentionally remains after Close: unlinking
// an advisory-lock inode can let a new process lock a replacement inode while
// an older process still owns the original.
type Lock struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	closed   bool
	closeErr error
}

// Acquire obtains a nonblocking exclusive lock. The parent directory must
// already exist so session-layout ownership remains with the application.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if ctx == nil {
		return nil, errors.New("session lock context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("session lock path is required")
	}
	path = filepath.Clean(path)
	parentPath := filepath.Dir(path)
	parent, err := os.Stat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("inspect session lock directory: %w", err)
	}
	if !parent.IsDir() {
		return nil, ErrUnsafePath
	}
	file, err := openStableFile(path)
	if err != nil {
		return nil, err
	}
	if err := verifyStableLockPath(file, path, parentPath, parent); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure session lock: %w", err)
	}
	acquired, err := tryLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire session lock: %w", err)
	}
	if !acquired {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s", ErrContended, path)
	}
	if err := verifyStableLockPath(file, path, parentPath, parent); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	return &Lock{file: file, path: path}, nil
}

func verifyStableLockPath(file *os.File, path, parentPath string, expectedParent os.FileInfo) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return ErrUnsafePath
	}
	links, err := openedFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return ErrUnsafePath
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return ErrUnsafePath
	}
	parent, err := os.Stat(parentPath)
	if err != nil || !parent.IsDir() || !os.SameFile(expectedParent, parent) {
		return ErrUnsafePath
	}
	return nil
}

func openStableFile(path string) (*os.File, error) {
	for range 3 {
		before, err := os.Lstat(path)
		switch {
		case err == nil:
			if !before.Mode().IsRegular() {
				return nil, ErrUnsafePath
			}
			file, openErr := os.OpenFile(path, os.O_RDWR, 0o600)
			if openErr != nil {
				return nil, fmt.Errorf("open session lock: %w", openErr)
			}
			after, statErr := file.Stat()
			if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
				_ = file.Close()
				return nil, ErrUnsafePath
			}
			return file, nil
		case errors.Is(err, os.ErrNotExist):
			file, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, fmt.Errorf("create session lock: %w", createErr)
			}
			return file, nil
		default:
			return nil, fmt.Errorf("inspect session lock: %w", err)
		}
	}
	return nil, errors.New("session lock path changed repeatedly during acquisition")
}

// Path returns the stable lock-file path for diagnostics.
func (lock *Lock) Path() string {
	if lock == nil {
		return ""
	}
	return lock.path
}

// Close releases the operating-system lock and descriptor exactly once.
func (lock *Lock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return lock.closeErr
	}
	lock.closed = true
	lock.closeErr = errors.Join(unlockFile(lock.file), lock.file.Close())
	return lock.closeErr
}
