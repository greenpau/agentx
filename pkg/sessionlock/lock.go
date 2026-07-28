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
	// ErrUnsupported reports a target without a cross-process session-lock
	// implementation. Acquisition returns this before opening or creating any
	// filesystem object.
	ErrUnsupported = errors.New("cross-process session locks are unsupported on this platform")
)

// Lock retains an advisory operating-system lock for its file descriptor's
// lifetime. The lock file itself intentionally remains after Close: unlinking
// an advisory-lock inode can let a new process lock a replacement inode while
// an older process still owns the original.
type Lock struct {
	mu             sync.Mutex
	file           *os.File
	parentHandle   *os.File
	path           string
	parentPath     string
	name           string
	parentIdentity os.FileInfo
	fileIdentity   os.FileInfo
	closed         bool
	closeErr       error
}

type acquisitionMode uint8

const (
	acquireCreateOrOpen acquisitionMode = iota
	acquireExistingOnly
)

// Acquire obtains a nonblocking exclusive lock. The parent directory must
// already exist so session-layout ownership remains with the application. It
// preserves the runtime behavior of creating a missing lock file and securing
// the opened file to owner-only access.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	return acquire(ctx, path, acquireCreateOrOpen)
}

// AcquireExisting obtains a nonblocking exclusive lock only when a stable,
// direct lock file already exists. It never creates the file or changes its
// permissions.
func AcquireExisting(ctx context.Context, path string) (*Lock, error) {
	return acquire(ctx, path, acquireExistingOnly)
}

func acquire(ctx context.Context, path string, mode acquisitionMode) (*Lock, error) {
	if ctx == nil {
		return nil, errors.New("session lock context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("session lock path is required")
	}
	if !sessionLocksSupported() {
		return nil, ErrUnsupported
	}
	path = filepath.Clean(path)
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "" || name == "." || name == ".." || !filepath.IsLocal(name) || filepath.Dir(name) != "." {
		return nil, ErrUnsafePath
	}
	parentHandle, parent, err := openStableParent(parentPath)
	if err != nil {
		return nil, err
	}
	closeParent := func(cause error) (*Lock, error) {
		return nil, errors.Join(cause, parentHandle.Close())
	}
	parentRoot, err := openStableParentRoot(parentHandle, parentPath, parent)
	if err != nil {
		return closeParent(err)
	}
	file, err := openStableFile(parentRoot, name, mode)
	if err != nil {
		return nil, errors.Join(err, parentRoot.Close(), parentHandle.Close())
	}
	failWithRoot := func(cause error) (*Lock, error) {
		return nil, errors.Join(cause, file.Close(), parentRoot.Close(), parentHandle.Close())
	}
	identity, err := verifyStableLockPath(
		parentRoot,
		parentHandle,
		file,
		path,
		parentPath,
		name,
		parent,
		nil,
	)
	if err != nil {
		return failWithRoot(err)
	}
	if mode == acquireCreateOrOpen {
		if err := file.Chmod(0o600); err != nil {
			return failWithRoot(fmt.Errorf("secure session lock: %w", err))
		}
		identity, err = verifyStableLockPath(
			parentRoot,
			parentHandle,
			file,
			path,
			parentPath,
			name,
			parent,
			identity,
		)
		if err != nil {
			return failWithRoot(err)
		}
	}
	if err := parentRoot.Close(); err != nil {
		return nil, errors.Join(err, file.Close(), parentHandle.Close())
	}
	fail := func(cause error) (*Lock, error) {
		return nil, errors.Join(cause, file.Close(), parentHandle.Close())
	}
	acquired, err := tryLockFile(file)
	if err != nil {
		return fail(fmt.Errorf("acquire session lock: %w", err))
	}
	if !acquired {
		return fail(fmt.Errorf("%w: %s", ErrContended, path))
	}
	identity, err = verifyStableLock(
		parentHandle,
		file,
		path,
		parentPath,
		name,
		parent,
		identity,
	)
	if err != nil {
		_ = unlockFile(file)
		return fail(err)
	}
	return &Lock{
		file:           file,
		parentHandle:   parentHandle,
		path:           path,
		parentPath:     parentPath,
		name:           name,
		parentIdentity: parent,
		fileIdentity:   identity,
	}, nil
}

// openStableParent makes a descriptor opened beneath a temporary rooted handle
// the first authoritative parent identity. Root.Open uses no-follow,
// delete-sharing relative opens on Windows, so the retained descriptor does
// not prevent the owning session directory from being atomically detached.
// Plain os.Lstat FileInfo values on Windows load their file ID lazily during
// os.SameFile and therefore cannot be the initial retained identity.
func openStableParent(parentPath string) (*os.File, os.FileInfo, error) {
	parentRoot, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open session lock directory: %w", err)
	}
	parentHandle, err := parentRoot.Open(".")
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open stable session lock directory handle: %w", err),
			parentRoot.Close(),
		)
	}
	fail := func(cause error) (*os.File, os.FileInfo, error) {
		return nil, nil, errors.Join(cause, parentHandle.Close(), parentRoot.Close())
	}
	parent, err := parentHandle.Stat()
	if err != nil || !parent.IsDir() {
		return fail(errors.Join(ErrUnsafePath, err))
	}
	if err := parentRoot.Close(); err != nil {
		return nil, nil, errors.Join(err, parentHandle.Close())
	}
	if err := verifyStableParentHandle(parentHandle, parentPath, parent); err != nil {
		_ = parentHandle.Close()
		return nil, nil, err
	}
	return parentHandle, parent, nil
}

func openStableParentRoot(
	parentHandle *os.File,
	parentPath string,
	expected os.FileInfo,
) (*os.Root, error) {
	if err := verifyStableParentHandle(parentHandle, parentPath, expected); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open rooted session lock directory: %w", err)
	}
	if err := verifyStableParent(root, parentHandle, parentPath, expected); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func verifyStableLock(
	parentHandle *os.File,
	file *os.File,
	path, parentPath, name string,
	expectedParent, expectedFile os.FileInfo,
) (os.FileInfo, error) {
	parentRoot, err := openStableParentRoot(parentHandle, parentPath, expectedParent)
	if err != nil {
		return nil, err
	}
	identity, verifyErr := verifyStableLockPath(
		parentRoot,
		parentHandle,
		file,
		path,
		parentPath,
		name,
		expectedParent,
		expectedFile,
	)
	return identity, errors.Join(verifyErr, parentRoot.Close())
}

func verifyStableLockPath(
	parentRoot *os.Root,
	parentHandle *os.File,
	file *os.File,
	path, parentPath, name string,
	expectedParent, expectedFile os.FileInfo,
) (os.FileInfo, error) {
	if err := verifyStableParent(parentRoot, parentHandle, parentPath, expectedParent); err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	if expectedFile != nil && !os.SameFile(expectedFile, opened) {
		return nil, ErrUnsafePath
	}
	links, err := openedFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return nil, ErrUnsafePath
	}
	rooted, err := parentRoot.Lstat(name)
	if err != nil || !rooted.Mode().IsRegular() || rooted.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, rooted) {
		return nil, ErrUnsafePath
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return nil, ErrUnsafePath
	}
	if err := verifyStableParent(parentRoot, parentHandle, parentPath, expectedParent); err != nil {
		return nil, err
	}
	return opened, nil
}

func verifyStableParent(
	root *os.Root,
	parentHandle *os.File,
	parentPath string,
	expected os.FileInfo,
) error {
	if root == nil || parentHandle == nil || expected == nil {
		return ErrUnsafePath
	}
	if err := verifyStableParentHandle(parentHandle, parentPath, expected); err != nil {
		return err
	}
	rootHandle, err := root.Open(".")
	if err != nil {
		return ErrUnsafePath
	}
	opened, statErr := rootHandle.Stat()
	closeErr := rootHandle.Close()
	if statErr != nil || closeErr != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		return ErrUnsafePath
	}
	return verifyStableParentHandle(parentHandle, parentPath, expected)
}

func verifyStableParentHandle(
	parentHandle *os.File,
	parentPath string,
	expected os.FileInfo,
) error {
	if parentHandle == nil || expected == nil {
		return ErrUnsafePath
	}
	opened, err := parentHandle.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		return ErrUnsafePath
	}
	current, err := os.Lstat(parentPath)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return ErrUnsafePath
	}
	after, err := parentHandle.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(expected, after) {
		return ErrUnsafePath
	}
	return nil
}

func openStableFile(root *os.Root, name string, mode acquisitionMode) (*os.File, error) {
	for range 3 {
		before, err := root.Lstat(name)
		switch {
		case err == nil:
			if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
				return nil, ErrUnsafePath
			}
			file, openErr := root.OpenFile(name, os.O_RDWR, 0o600)
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
			if mode == acquireExistingOnly {
				return nil, fmt.Errorf("open existing session lock: %w", err)
			}
			file, createErr := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
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

// Verify confirms that the retained handle is still locked against the direct,
// single-link file and parent directory identities acquired by Acquire or
// AcquireExisting. It is intended for the final mutation boundary immediately
// before detaching the owning session directory.
func (lock *Lock) Verify() error {
	if lock == nil {
		return ErrUnsafePath
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.file == nil || lock.parentHandle == nil {
		return ErrUnsafePath
	}
	_, err := verifyStableLock(
		lock.parentHandle,
		lock.file,
		lock.path,
		lock.parentPath,
		lock.name,
		lock.parentIdentity,
		lock.fileIdentity,
	)
	return err
}

// Sync flushes the retained lock inode after verifying that both the direct
// lock entry and its parent still name the acquired identities. Callers that
// require durable lock-file creation must separately sync their owned parent
// directory after this succeeds.
func (lock *Lock) Sync() error {
	if lock == nil {
		return ErrUnsafePath
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.file == nil || lock.parentHandle == nil {
		return ErrUnsafePath
	}
	if _, err := verifyStableLock(
		lock.parentHandle,
		lock.file,
		lock.path,
		lock.parentPath,
		lock.name,
		lock.parentIdentity,
		lock.fileIdentity,
	); err != nil {
		return err
	}
	if err := lock.file.Sync(); err != nil {
		return fmt.Errorf("sync session lock: %w", err)
	}
	_, err := verifyStableLock(
		lock.parentHandle,
		lock.file,
		lock.path,
		lock.parentPath,
		lock.name,
		lock.parentIdentity,
		lock.fileIdentity,
	)
	return err
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
	var unlockErr error
	var fileCloseErr error
	if lock.file != nil {
		unlockErr = unlockFile(lock.file)
		fileCloseErr = lock.file.Close()
	}
	var parentCloseErr error
	if lock.parentHandle != nil {
		parentCloseErr = lock.parentHandle.Close()
	}
	lock.closeErr = errors.Join(unlockErr, fileCloseErr, parentCloseErr)
	return lock.closeErr
}
