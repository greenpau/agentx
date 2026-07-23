package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrDirectoryIdentityChanged reports that an application-owned directory no
// longer names the filesystem object that was originally acquired. Callers
// must stop before writing through, locking beneath, or recursively removing
// that pathname.
var ErrDirectoryIdentityChanged = errors.New("owned directory identity changed")

// OwnedDirectory is identity evidence for one application-owned private
// directory. The path is physical with respect to pre-existing external
// ancestors, while the final component and every application-created child
// are required to be real directories rather than symbolic links.
//
// The retained FileInfo is deliberately immutable. Every sensitive path-based
// operation rechecks it with os.SameFile; this is not an authorization token
// and must not be used for a different pathname.
type OwnedDirectory struct {
	path     string
	identity os.FileInfo

	// beforeRemoveContents is a deterministic race seam for package tests. A
	// production directory leaves it nil. It runs only after descriptor roots
	// for both the parent and owned directory have been identity-verified.
	beforeRemoveContents func() error
}

// AcquirePrivateDirectory resolves the deepest pre-existing external
// ancestor, then creates missing application-owned components one at a time.
// A pre-existing final component is accepted only when it is a direct,
// ordinary directory. Direct symlinks are never followed or chmodded.
func AcquirePrivateDirectory(path string) (*OwnedDirectory, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("private directory path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("resolve private directory: %w", err)
	}
	if filepath.Dir(abs) == abs {
		return nil, errors.New("filesystem root cannot be application-owned")
	}

	info, err := os.Lstat(abs)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s is not a direct directory", ErrDirectoryIdentityChanged, abs)
		}
		physicalParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve private directory parent: %w", resolveErr)
		}
		physicalPath := filepath.Join(physicalParent, filepath.Base(abs))
		physicalInfo, statErr := os.Lstat(physicalPath)
		if statErr != nil || !physicalInfo.IsDir() || physicalInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, physicalInfo) {
			return nil, fmt.Errorf("%w: %s changed while resolving its parent", ErrDirectoryIdentityChanged, abs)
		}
		return securePrivateDirectory(physicalPath, nil)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect private directory: %w", err)
	}

	existing := abs
	var missing []string
	for {
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil, errors.New("no existing ancestor for private directory")
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
		ancestorInfo, statErr := os.Lstat(existing)
		if statErr == nil {
			if !ancestorInfo.IsDir() && ancestorInfo.Mode()&os.ModeSymlink == 0 {
				return nil, fmt.Errorf("private directory ancestor %s is not a directory", existing)
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect private directory ancestor: %w", statErr)
		}
	}
	physicalAncestor, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return nil, fmt.Errorf("resolve private directory ancestor: %w", err)
	}
	parent, err := captureDirectory(physicalAncestor)
	if err != nil {
		return nil, fmt.Errorf("acquire private directory ancestor: %w", err)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		parent, err = parent.ensurePrivateChild(missing[index], false)
		if err != nil {
			return nil, err
		}
	}
	return parent, nil
}

// Path returns the physical pathname owned by this identity.
func (directory *OwnedDirectory) Path() string {
	if directory == nil {
		return ""
	}
	return directory.path
}

// Verify confirms that the pathname still denotes the originally acquired
// direct directory.
func (directory *OwnedDirectory) Verify() error {
	if directory == nil || directory.path == "" || directory.identity == nil {
		return fmt.Errorf("%w: missing identity", ErrDirectoryIdentityChanged)
	}
	current, err := os.Lstat(directory.path)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrDirectoryIdentityChanged, directory.path, err)
	}
	if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(directory.identity, current) {
		return fmt.Errorf("%w: %s", ErrDirectoryIdentityChanged, directory.path)
	}
	return nil
}

// EnsurePrivateChild creates or acquires simple child components beneath an
// already-owned directory. Components are processed individually and both
// parent and child identities are checked around every path mutation.
func (directory *OwnedDirectory) EnsurePrivateChild(components ...string) (*OwnedDirectory, error) {
	current := directory
	var err error
	for _, component := range components {
		current, err = current.ensurePrivateChild(component, false)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

// OpenPrivateChild acquires existing simple child components without creating
// them. A direct symlink, non-directory, missing component, or replaced parent
// fails closed.
func (directory *OwnedDirectory) OpenPrivateChild(components ...string) (*OwnedDirectory, error) {
	current := directory
	var err error
	for _, component := range components {
		current, err = current.ensurePrivateChild(component, true)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (directory *OwnedDirectory) ensurePrivateChild(component string, requireExisting bool) (*OwnedDirectory, error) {
	if !simplePathComponent(component) {
		return nil, fmt.Errorf("invalid private directory component %q", component)
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	childPath := filepath.Join(directory.path, component)
	_, err := os.Lstat(childPath)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && requireExisting:
		return nil, err
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(childPath, 0o700); err != nil {
			return nil, fmt.Errorf("create private directory %s: %w", childPath, err)
		}
	default:
		return nil, fmt.Errorf("inspect private directory %s: %w", childPath, err)
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	return securePrivateDirectory(childPath, directory)
}

// RemoveAll recursively clears this directory through a verified os.Root
// handle, then removes the now-empty directory with a descriptor-relative,
// nonrecursive operation. If the pathname is replaced at any point, recursive
// traversal remains pinned to the originally acquired directory and the
// replacement is never recursively deleted.
func (directory *OwnedDirectory) RemoveAll() (resultErr error) {
	if directory == nil {
		return nil
	}
	if _, err := os.Lstat(directory.path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect owned directory before cleanup: %w", err)
	}
	if err := directory.Verify(); err != nil {
		return err
	}
	if filepath.Dir(directory.path) == directory.path {
		return errors.New("refusing to remove filesystem root")
	}
	parentPath := filepath.Dir(directory.path)
	base := filepath.Base(directory.path)
	parentBefore, err := os.Lstat(parentPath)
	if err != nil || !parentBefore.IsDir() || parentBefore.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("inspect owned directory parent %s: %w", parentPath, fallbackDirectoryError(err))
	}
	parentRoot, err := os.OpenRoot(parentPath)
	if err != nil {
		return fmt.Errorf("open owned directory parent %s: %w", parentPath, err)
	}
	defer func() { resultErr = errors.Join(resultErr, parentRoot.Close()) }()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !parentOpened.IsDir() || !os.SameFile(parentBefore, parentOpened) {
		return fmt.Errorf("%w: parent of %s changed while opening", ErrDirectoryIdentityChanged, directory.path)
	}
	child, err := parentRoot.Lstat(base)
	if err != nil || !child.IsDir() || child.Mode()&os.ModeSymlink != 0 || !os.SameFile(directory.identity, child) {
		return fmt.Errorf("%w: %s changed before rooted cleanup", ErrDirectoryIdentityChanged, directory.path)
	}
	ownedRoot, err := parentRoot.OpenRoot(base)
	if err != nil {
		return fmt.Errorf("open owned directory root %s: %w", directory.path, err)
	}
	opened, err := ownedRoot.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(directory.identity, opened) {
		_ = ownedRoot.Close()
		return fmt.Errorf("%w: %s changed while opening cleanup root", ErrDirectoryIdentityChanged, directory.path)
	}
	if directory.beforeRemoveContents != nil {
		if err := directory.beforeRemoveContents(); err != nil {
			_ = ownedRoot.Close()
			return fmt.Errorf("before owned directory cleanup: %w", err)
		}
	}
	removeErr := removeRootContents(ownedRoot)
	closeErr := ownedRoot.Close()
	if removeErr != nil || closeErr != nil {
		return errors.Join(removeErr, closeErr)
	}
	current, err := parentRoot.Lstat(base)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(directory.identity, current) {
		return fmt.Errorf("%w: %s changed before final removal", ErrDirectoryIdentityChanged, directory.path)
	}
	// Root.Remove is intentionally nonrecursive. Even if the final component
	// changes after the identity check, a nonempty replacement cannot be
	// traversed or deleted.
	if err := parentRoot.Remove(base); err != nil {
		return fmt.Errorf("remove empty owned directory %s: %w", directory.path, err)
	}
	return nil
}

func removeRootContents(root *os.Root) error {
	const maximumPasses = 8
	for pass := 0; pass < maximumPasses; pass++ {
		directory, err := root.Open(".")
		if err != nil {
			return fmt.Errorf("open owned directory contents: %w", err)
		}
		entries, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if len(entries) == 0 {
			return nil
		}
		var removeErrors []error
		for _, entry := range entries {
			name := entry.Name()
			if !simplePathComponent(name) {
				removeErrors = append(removeErrors, fmt.Errorf("unsafe directory entry %q", name))
				continue
			}
			if err := root.RemoveAll(name); err != nil {
				removeErrors = append(removeErrors, fmt.Errorf("remove owned entry %s: %w", name, err))
			}
		}
		if err := errors.Join(removeErrors...); err != nil {
			return err
		}
	}
	return errors.New("owned directory contents changed repeatedly during cleanup")
}

func fallbackDirectoryError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("path is not a direct directory")
}

func securePrivateDirectory(path string, parent *OwnedDirectory) (*OwnedDirectory, error) {
	if parent != nil {
		if err := parent.Verify(); err != nil {
			return nil, err
		}
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect private directory %s: %w", path, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is not a direct directory", ErrDirectoryIdentityChanged, path)
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open private directory %s: %w", path, err)
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%w: %s changed while opening", ErrDirectoryIdentityChanged, path)
	}
	// Chmod through the verified descriptor, never through a pathname that can
	// be swapped to a direct symlink between Lstat and chmod.
	if err := handle.Chmod(0o700); err != nil {
		return nil, fmt.Errorf("secure private directory %s: %w", path, err)
	}
	afterHandle, err := handle.Stat()
	if err != nil || !afterHandle.IsDir() || !os.SameFile(opened, afterHandle) {
		return nil, fmt.Errorf("%w: %s changed while securing", ErrDirectoryIdentityChanged, path)
	}
	afterPath, err := os.Lstat(path)
	if err != nil || !afterPath.IsDir() || afterPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(afterHandle, afterPath) {
		return nil, fmt.Errorf("%w: %s changed after securing", ErrDirectoryIdentityChanged, path)
	}
	if parent != nil {
		if err := parent.Verify(); err != nil {
			return nil, err
		}
	}
	return &OwnedDirectory{path: filepath.Clean(path), identity: afterHandle}, nil
}

func captureDirectory(path string) (*OwnedDirectory, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a direct directory", path)
	}
	return &OwnedDirectory{path: filepath.Clean(path), identity: info}, nil
}

func simplePathComponent(component string) bool {
	return component != "" && component != "." && component != ".." && filepath.Base(component) == component && !strings.ContainsAny(component, `/\\`) && !strings.ContainsRune(component, 0)
}
