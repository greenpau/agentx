package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrDirectoryIdentityChanged reports that an application-owned directory
	// no longer names the filesystem object that was originally acquired.
	// Callers must stop before writing through, locking beneath, or recursively
	// removing that pathname.
	ErrDirectoryIdentityChanged = errors.New("owned directory identity changed")

	// ErrAtomicRenameNoReplaceUnsupported reports a platform or filesystem
	// that cannot atomically rename without replacing an existing destination.
	// Safety-sensitive callers must fail closed instead of using an
	// absence-check followed by an ordinary replacing rename.
	ErrAtomicRenameNoReplaceUnsupported = errors.New("atomic no-replace rename is unsupported")

	// ErrOwnedDirectoryFilesystemBoundary reports that recursive cleanup
	// reached a child on a different filesystem or mount identity, or that the
	// adapter could not prove the identity. Cleanup fails closed instead of
	// crossing the boundary.
	ErrOwnedDirectoryFilesystemBoundary = errors.New("owned directory filesystem boundary is unsafe")

	// ErrOwnedDirectorySyncUnsupported reports that the adapter cannot provide
	// a real directory durability boundary. Mutation callers must not
	// translate this into a successful durable commit.
	ErrOwnedDirectorySyncUnsupported = errors.New("owned directory sync is unsupported")

	// ErrOwnedDirectoryCleanupBound reports that recursive cleanup exhausted
	// its entry, depth, or rescan ceiling. Any identity-safe progress remains
	// committed and the detached owner can be retried.
	ErrOwnedDirectoryCleanupBound = errors.New("owned directory cleanup bound exceeded")
)

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
	private  bool
	parent   *OwnedDirectory

	// beforeRemoveContents is a deterministic race seam for package tests. A
	// production directory leaves it nil. It runs only after descriptor roots
	// for both the parent and owned directory have been identity-verified.
	beforeRemoveContents func() error

	// beforeCreateChild is a deterministic first-creator race seam for package
	// tests. A production directory leaves it nil. It runs only after the child
	// was observed absent and before its exclusive directory creation attempt.
	beforeCreateChild func(string) error

	// beforeDetachRename and afterDetachRename are deterministic race and
	// post-commit seams for package tests. Production directories leave them
	// nil. The first runs after initial rooted validation but before the final
	// pre-rename identity checks. The second runs only after Rename succeeds.
	beforeDetachRename func(string, string) error
	afterDetachRename  func(string, string) error

	// beforeDetachCommit runs only in package tests after every final identity,
	// destination-absence, and caller-authority check, immediately before the
	// atomic no-replace rename.
	beforeDetachCommit func(string, string) error
}

// DetachResult reports the state of an identity-verified child detach.
//
// Committed is true once the same-parent rename has succeeded. Owner is then
// bound to the destination name and original child identity, even if a later
// verification or durability step fails. Callers must use this distinction to
// recover cleanup without treating a committed rename as an untouched source.
type DetachResult struct {
	Owner     *OwnedDirectory
	Committed bool
}

type ownedDirectoryMountIdentity struct {
	device     uint64
	mount      uint64
	mountKnown bool
}

func (identity ownedDirectoryMountIdentity) equal(other ownedDirectoryMountIdentity) bool {
	return identity.device == other.device &&
		identity.mount == other.mount &&
		identity.mountKnown == other.mountKnown
}

func ownedDirectoryRootMountIdentity(root *os.Root) (identity ownedDirectoryMountIdentity, resultErr error) {
	if root == nil {
		return ownedDirectoryMountIdentity{}, fmt.Errorf("%w: missing directory root", ErrOwnedDirectoryFilesystemBoundary)
	}
	handle, err := root.Open(".")
	if err != nil {
		return ownedDirectoryMountIdentity{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, handle.Close())
	}()
	info, err := handle.Stat()
	if err != nil {
		return ownedDirectoryMountIdentity{}, err
	}
	if !info.IsDir() {
		return ownedDirectoryMountIdentity{}, errors.New("filesystem identity target is not a directory")
	}
	return ownedDirectoryMountIdentityForHandle(handle, info)
}

func verifyRootedChildMount(parentRoot, childRoot *os.Root) error {
	parentMount, err := ownedDirectoryRootMountIdentity(parentRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect parent mount: %v", ErrOwnedDirectoryFilesystemBoundary, err)
	}
	childMount, err := ownedDirectoryRootMountIdentity(childRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect child mount: %v", ErrOwnedDirectoryFilesystemBoundary, err)
	}
	if !parentMount.equal(childMount) {
		return ErrOwnedDirectoryFilesystemBoundary
	}
	return nil
}

func verifyRootedChildFileMount(parentRoot *os.Root, child *os.File, childInfo os.FileInfo) error {
	parentMount, err := ownedDirectoryRootMountIdentity(parentRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect parent mount: %v", ErrOwnedDirectoryFilesystemBoundary, err)
	}
	childMount, err := ownedDirectoryMountIdentityForHandle(child, childInfo)
	if err != nil {
		return fmt.Errorf("%w: inspect child mount: %v", ErrOwnedDirectoryFilesystemBoundary, err)
	}
	if !parentMount.equal(childMount) {
		return ErrOwnedDirectoryFilesystemBoundary
	}
	return nil
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
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || ownedDirectoryEntryIsFilesystemLink(info) {
			return nil, fmt.Errorf("%w: %s is not a direct directory", ErrDirectoryIdentityChanged, abs)
		}
		observed, captureErr := captureDirectory(abs)
		if captureErr != nil {
			return nil, fmt.Errorf("acquire private directory identity: %w", captureErr)
		}
		physicalParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve private directory parent: %w", resolveErr)
		}
		physicalPath := filepath.Join(physicalParent, filepath.Base(abs))
		parent, captureErr := captureDirectory(physicalParent)
		if captureErr != nil {
			return nil, fmt.Errorf("acquire private directory parent: %w", captureErr)
		}
		return securePrivateDirectory(physicalPath, parent, observed.identity)
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
	if err := directory.verifySelf(); err != nil {
		return err
	}
	if directory.parent == nil {
		return nil
	}
	component := filepath.Base(directory.path)
	if !simplePathComponent(component) ||
		filepath.Clean(filepath.Dir(directory.path)) != directory.parent.path ||
		filepath.Clean(filepath.Join(directory.parent.path, component)) != directory.path {
		return fmt.Errorf("%w: %s is not a direct child of its retained parent", ErrDirectoryIdentityChanged, directory.path)
	}
	parentRoot, err := directory.parent.openVerifiedRoot()
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	if err := verifyRootedOwnedDirectory(parentRoot, component, directory); err != nil {
		return err
	}
	if err := directory.verifySelf(); err != nil {
		return err
	}
	return directory.parent.Verify()
}

func (directory *OwnedDirectory) verifySelf() error {
	if directory == nil || directory.path == "" || directory.identity == nil {
		return fmt.Errorf("%w: missing identity", ErrDirectoryIdentityChanged)
	}
	current, err := os.Lstat(directory.path)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrDirectoryIdentityChanged, directory.path, err)
	}
	if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(current) || !os.SameFile(directory.identity, current) {
		return fmt.Errorf("%w: %s", ErrDirectoryIdentityChanged, directory.path)
	}
	if directory.private && privateDirectoryAccessControlVerified &&
		!privateDirectoryAccessPermitsUse(current) {
		return fmt.Errorf("%w: %s is not owner-private", ErrDirectoryIdentityChanged, directory.path)
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

// InspectPrivateChild acquires existing simple child components without
// creating or chmodding them. On platforms where owner-private access can be
// verified, an existing child whose permissions would require repair is
// rejected. This is the read-only acquisition path for inventory operations.
func (directory *OwnedDirectory) InspectPrivateChild(components ...string) (*OwnedDirectory, error) {
	current := directory
	var err error
	for _, component := range components {
		current, err = current.inspectPrivateChild(component)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

// CreatePrivateChild creates exactly one absent simple child beneath an owned
// directory. Unlike EnsurePrivateChild, it never reacquires an entry created by
// another actor after the absence check, allowing callers to know that cleanup
// authority applies only to the directory identity they created.
func (directory *OwnedDirectory) CreatePrivateChild(component string) (*OwnedDirectory, error) {
	if !simplePathComponent(component) {
		return nil, fmt.Errorf("invalid private directory component %q", component)
	}
	root, err := directory.openVerifiedRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	childPath := filepath.Join(directory.path, component)
	if _, err := root.Lstat(component); err == nil {
		return nil, fmt.Errorf("create private directory %s: %w", childPath, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect private directory %s: %w", childPath, err)
	}
	if directory.beforeCreateChild != nil {
		if err := directory.beforeCreateChild(childPath); err != nil {
			return nil, fmt.Errorf("before creating private directory %s: %w", childPath, err)
		}
	}
	if err := root.Mkdir(component, 0o700); err != nil {
		return nil, fmt.Errorf("create private directory %s: %w", childPath, err)
	}
	child, err := securePrivateChild(directory, root, component)
	if err != nil {
		return nil, err
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	return child, nil
}

// Sync persists prior directory-entry mutations through a descriptor rooted in
// this exact directory identity when the platform exposes that operation.
func (directory *OwnedDirectory) Sync() (resultErr error) {
	root, err := directory.openVerifiedRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	return syncOwnedDirectoryRoot(directory, root)
}

// OpenRoot returns a descriptor-rooted view of this exact directory identity.
// The caller must close the root. Operations through the returned root remain
// pinned to the acquired directory across a pathname rename; callers should
// still Verify afterward when the textual path must remain authoritative.
func (directory *OwnedDirectory) OpenRoot() (*os.Root, error) {
	return directory.openVerifiedRoot()
}

// PreflightPrivateChildDetach verifies the immutable parent/child relationship
// and the platform primitives needed by DetachPrivateChild without changing
// either namespace entry. Callers that persist deletion intent should run this
// first so an unsupported no-replace rename or durability boundary fails
// before intent is recorded.
func (directory *OwnedDirectory) PreflightPrivateChildDetach(child *OwnedDirectory) (resultErr error) {
	if directory == nil || child == nil {
		return fmt.Errorf("%w: missing detach identity", ErrDirectoryIdentityChanged)
	}
	source := filepath.Base(child.path)
	if !simplePathComponent(source) ||
		filepath.Clean(filepath.Dir(child.path)) != directory.path ||
		filepath.Clean(filepath.Join(directory.path, source)) != child.path {
		return fmt.Errorf("%w: %s is not a direct child of %s", ErrDirectoryIdentityChanged, child.path, directory.path)
	}
	if err := directory.Verify(); err != nil {
		return err
	}
	if err := child.Verify(); err != nil {
		return err
	}
	root, err := directory.openVerifiedRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if err := verifyRootedOwnedDirectory(root, source, child); err != nil {
		return err
	}
	if err := preflightRootNoReplace(root, source); err != nil {
		return fmt.Errorf("preflight private directory detach: %w", err)
	}
	if err := syncOwnedDirectoryRoot(directory, root); err != nil {
		return fmt.Errorf("preflight private directory durability: %w", err)
	}
	if err := verifyRootedOwnedDirectory(root, source, child); err != nil {
		return err
	}
	return directory.Verify()
}

// DetachPrivateChild atomically renames an acquired direct child to another
// simple name beneath the same identity-verified parent. It never creates the
// destination and refuses a destination that is already present.
//
// After a successful rename, the returned Owner retains the child's immutable
// filesystem identity at its destination path. Committed remains true if a
// subsequent source/destination check, parent-directory sync, or textual-path
// verification fails, allowing the caller to distinguish cleanup/recovery
// work from a pre-commit failure.
func (directory *OwnedDirectory) DetachPrivateChild(child *OwnedDirectory, destination string) (result DetachResult, resultErr error) {
	return directory.detachPrivateChild(child, destination, nil)
}

// DetachPrivateChildVerified is DetachPrivateChild with an additional
// caller-owned identity check executed after the final rooted source,
// destination, child, and parent checks and immediately before Rename. It is
// used when another retained capability, such as a session lock, must remain
// authoritative at the exact mutation boundary.
func (directory *OwnedDirectory) DetachPrivateChildVerified(
	child *OwnedDirectory,
	destination string,
	verify func() error,
) (result DetachResult, resultErr error) {
	if verify == nil {
		return DetachResult{}, errors.New("detach verifier is required")
	}
	return directory.detachPrivateChild(child, destination, verify)
}

func (directory *OwnedDirectory) detachPrivateChild(
	child *OwnedDirectory,
	destination string,
	verify func() error,
) (result DetachResult, resultErr error) {
	if directory == nil || directory.path == "" || directory.identity == nil {
		return DetachResult{}, fmt.Errorf("%w: missing parent identity", ErrDirectoryIdentityChanged)
	}
	if child == nil || child.path == "" || child.identity == nil {
		return DetachResult{}, fmt.Errorf("%w: missing child identity", ErrDirectoryIdentityChanged)
	}
	if !simplePathComponent(destination) {
		return DetachResult{}, fmt.Errorf("invalid detach destination component %q", destination)
	}
	source := filepath.Base(child.path)
	if !simplePathComponent(source) ||
		filepath.Clean(filepath.Dir(child.path)) != directory.path ||
		filepath.Clean(filepath.Join(directory.path, source)) != child.path {
		return DetachResult{}, fmt.Errorf("%w: %s is not a direct child of %s", ErrDirectoryIdentityChanged, child.path, directory.path)
	}
	if source == destination {
		return DetachResult{}, errors.New("detach destination must differ from source")
	}
	if err := directory.Verify(); err != nil {
		return DetachResult{}, err
	}
	if err := child.Verify(); err != nil {
		return DetachResult{}, err
	}

	root, err := directory.openVerifiedRoot()
	if err != nil {
		return DetachResult{}, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close detach parent root: %w", closeErr))
		}
	}()
	if err := verifyRootedOwnedDirectory(root, source, child); err != nil {
		return DetachResult{}, err
	}
	if err := requireAbsentRootEntry(root, destination); err != nil {
		return DetachResult{}, err
	}
	if directory.beforeDetachRename != nil {
		if err := directory.beforeDetachRename(
			filepath.Join(directory.path, source),
			filepath.Join(directory.path, destination),
		); err != nil {
			return DetachResult{}, fmt.Errorf("before detaching private directory: %w", err)
		}
	}

	// Repeat every pathname-sensitive check immediately before Rename. The
	// opened root remains pinned to the acquired parent if an external actor
	// moves it, while Verify prevents intentionally mutating through a parent
	// pathname already known to have been replaced.
	if err := directory.Verify(); err != nil {
		return DetachResult{}, err
	}
	if err := child.Verify(); err != nil {
		return DetachResult{}, err
	}
	if err := verifyRootedOwnedDirectory(root, source, child); err != nil {
		return DetachResult{}, err
	}
	if err := requireAbsentRootEntry(root, destination); err != nil {
		return DetachResult{}, err
	}
	if verify != nil {
		if err := verify(); err != nil {
			return DetachResult{}, fmt.Errorf("verify private directory detach authority: %w", err)
		}
	}
	if directory.beforeDetachCommit != nil {
		if err := directory.beforeDetachCommit(
			filepath.Join(directory.path, source),
			filepath.Join(directory.path, destination),
		); err != nil {
			return DetachResult{}, fmt.Errorf("before committing private directory detach: %w", err)
		}
	}
	committed, renameErr := renameRootNoReplace(root, source, destination)
	if !committed {
		return DetachResult{}, fmt.Errorf("detach private directory %s: %w", child.path, renameErr)
	}

	detached := &OwnedDirectory{
		path:     filepath.Clean(filepath.Join(directory.path, destination)),
		identity: child.identity,
		private:  child.private,
		parent:   directory,
	}
	result = DetachResult{Owner: detached, Committed: true}
	if renameErr != nil {
		return result, fmt.Errorf("complete private directory detach %s: %w", child.path, renameErr)
	}
	if directory.afterDetachRename != nil {
		if err := directory.afterDetachRename(child.path, detached.path); err != nil {
			return result, fmt.Errorf("after detaching private directory: %w", err)
		}
	}
	if _, err := root.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("source name still exists")
		}
		return result, fmt.Errorf("%w: verify detached source %s: %v", ErrDirectoryIdentityChanged, child.path, err)
	}
	if err := verifyRootedOwnedDirectory(root, destination, detached); err != nil {
		return result, err
	}
	if err := syncOwnedDirectoryRoot(directory, root); err != nil {
		return result, fmt.Errorf("sync detached directory parent: %w", err)
	}
	if err := directory.Verify(); err != nil {
		return result, err
	}
	if err := detached.Verify(); err != nil {
		return result, err
	}
	return result, nil
}

func (directory *OwnedDirectory) ensurePrivateChild(component string, requireExisting bool) (*OwnedDirectory, error) {
	if !simplePathComponent(component) {
		return nil, fmt.Errorf("invalid private directory component %q", component)
	}
	root, err := directory.openVerifiedRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	childPath := filepath.Join(directory.path, component)
	_, err = root.Lstat(component)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && requireExisting:
		return nil, err
	case errors.Is(err, os.ErrNotExist):
		if directory.beforeCreateChild != nil {
			if err := directory.beforeCreateChild(childPath); err != nil {
				return nil, fmt.Errorf("before creating private directory %s: %w", childPath, err)
			}
		}
		if err := root.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create private directory %s: %w", childPath, err)
		}
		// Another creator may win after Lstat. EEXIST is only permission to
		// reacquire the child below: securePrivateChild must still prove it is
		// one stable direct directory and protect it through a verified
		// descriptor rooted in the original parent. A symlink, non-directory,
		// or replacement fails closed.
	default:
		return nil, fmt.Errorf("inspect private directory %s: %w", childPath, err)
	}
	child, err := securePrivateChild(directory, root, component)
	if err != nil {
		return nil, err
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	return child, nil
}

func (directory *OwnedDirectory) inspectPrivateChild(component string) (result *OwnedDirectory, resultErr error) {
	if !simplePathComponent(component) {
		return nil, fmt.Errorf("invalid private directory component %q", component)
	}
	root, err := directory.openVerifiedRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	childPath := filepath.Join(directory.path, component)
	before, err := root.Lstat(component)
	if err != nil {
		return nil, fmt.Errorf("inspect private directory %s: %w", childPath, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(before) ||
		(privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(before)) {
		return nil, fmt.Errorf("%w: %s is not a direct owner-private directory", ErrDirectoryIdentityChanged, childPath)
	}
	childRoot, err := root.OpenRoot(component)
	if err != nil {
		return nil, fmt.Errorf("open private directory %s: %w", childPath, err)
	}
	opened, statErr := childRoot.Stat(".")
	var mountErr error
	if statErr == nil && opened.IsDir() {
		mountErr = verifyRootedChildMount(root, childRoot)
	}
	closeErr := childRoot.Close()
	if statErr != nil || mountErr != nil || closeErr != nil {
		if statErr != nil {
			statErr = fmt.Errorf("inspect opened private directory %s: %w", childPath, statErr)
		}
		if mountErr != nil {
			mountErr = fmt.Errorf("%w: inspect private directory %s mount: %v", ErrDirectoryIdentityChanged, childPath, mountErr)
		}
		return nil, errors.Join(statErr, mountErr, closeErr)
	}
	after, err := root.Lstat(component)
	if err != nil || !opened.IsDir() || !after.IsDir() ||
		after.Mode()&os.ModeSymlink != 0 || ownedDirectoryEntryIsFilesystemLink(after) ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) ||
		(privateDirectoryAccessControlVerified &&
			(!privateDirectoryAccessPermitsUse(opened) || !privateDirectoryAccessPermitsUse(after))) {
		return nil, fmt.Errorf("%w: %s changed while inspecting", ErrDirectoryIdentityChanged, childPath)
	}
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	return &OwnedDirectory{path: filepath.Clean(childPath), identity: opened, private: true, parent: directory}, nil
}

func (directory *OwnedDirectory) openVerifiedRoot() (*os.Root, error) {
	if err := directory.Verify(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory.path)
	if err != nil {
		return nil, fmt.Errorf("open private directory root %s: %w", directory.path, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(directory.identity, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%w: %s changed while opening its root", ErrDirectoryIdentityChanged, directory.path)
	}
	if err := directory.Verify(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func verifyRootedOwnedDirectory(root *os.Root, name string, expected *OwnedDirectory) (resultErr error) {
	if root == nil || expected == nil || expected.identity == nil || !simplePathComponent(name) {
		return fmt.Errorf("%w: rooted directory identity is unavailable", ErrDirectoryIdentityChanged)
	}
	before, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect rooted private directory %s: %w", name, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(before) || !os.SameFile(expected.identity, before) ||
		(expected.private && privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(before)) {
		return fmt.Errorf("%w: rooted private directory %s does not match", ErrDirectoryIdentityChanged, name)
	}
	opened, err := root.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open rooted private directory %s: %w", name, err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, opened.Close())
	}()
	info, err := opened.Stat(".")
	if err != nil || !info.IsDir() || !os.SameFile(expected.identity, info) ||
		(expected.private && privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(info)) {
		return fmt.Errorf("%w: rooted private directory %s changed while opening", ErrDirectoryIdentityChanged, name)
	}
	if err := verifyRootedChildMount(root, opened); err != nil {
		return fmt.Errorf("%w: rooted private directory %s: %v", ErrDirectoryIdentityChanged, name, err)
	}
	after, err := root.Lstat(name)
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(after) ||
		!os.SameFile(info, after) ||
		(expected.private && privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(after)) {
		return fmt.Errorf("%w: rooted private directory %s changed after opening", ErrDirectoryIdentityChanged, name)
	}
	return nil
}

func requireAbsentRootEntry(root *os.Root, name string) error {
	if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect detach destination %s: %w", name, err)
	}
	return fmt.Errorf("detach destination %s: %w", name, os.ErrExist)
}

func syncOwnedDirectoryRoot(directory *OwnedDirectory, root *os.Root) (resultErr error) {
	handle, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open owned directory for sync: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, handle.Close())
	}()
	before, err := handle.Stat()
	if err != nil || !before.IsDir() || !os.SameFile(directory.identity, before) {
		return fmt.Errorf("%w: owned directory changed before sync", ErrDirectoryIdentityChanged)
	}
	if err := syncOwnedDirectoryHandle(handle); err != nil {
		return err
	}
	after, err := handle.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(directory.identity, after) {
		return fmt.Errorf("%w: owned directory changed after sync", ErrDirectoryIdentityChanged)
	}
	return nil
}

func securePrivateChild(parent *OwnedDirectory, root *os.Root, component string) (*OwnedDirectory, error) {
	return securePrivateChildExpected(parent, root, component, nil)
}

func securePrivateChildExpected(
	parent *OwnedDirectory,
	root *os.Root,
	component string,
	expected os.FileInfo,
) (*OwnedDirectory, error) {
	childPath := filepath.Join(parent.path, component)
	before, err := root.Lstat(component)
	if err != nil {
		return nil, fmt.Errorf("inspect private directory %s: %w", childPath, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || ownedDirectoryEntryIsFilesystemLink(before) {
		return nil, fmt.Errorf("%w: %s is not a direct directory", ErrDirectoryIdentityChanged, childPath)
	}
	if expected != nil && !os.SameFile(expected, before) {
		return nil, fmt.Errorf("%w: %s changed before rooted acquisition", ErrDirectoryIdentityChanged, childPath)
	}
	handle, err := root.Open(component)
	if err != nil {
		return nil, fmt.Errorf("open private directory %s: %w", childPath, err)
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) ||
		(expected != nil && !os.SameFile(expected, opened)) {
		return nil, fmt.Errorf("%w: %s changed while opening", ErrDirectoryIdentityChanged, childPath)
	}
	if err := verifyRootedChildFileMount(root, handle, opened); err != nil {
		return nil, fmt.Errorf("%w: %s crosses a filesystem or mount identity: %v", ErrDirectoryIdentityChanged, childPath, err)
	}
	if privateDirectoryAccessControlVerified && !privateDirectoryOwnerPermitsUse(opened) {
		return nil, fmt.Errorf("%w: %s is not owned by the effective user", ErrDirectoryIdentityChanged, childPath)
	}
	if err := handle.Chmod(0o700); err != nil {
		return nil, fmt.Errorf("secure private directory %s: %w", childPath, err)
	}
	afterHandle, err := handle.Stat()
	if err != nil || !afterHandle.IsDir() || !os.SameFile(opened, afterHandle) ||
		(privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(afterHandle)) {
		return nil, fmt.Errorf("%w: %s changed or is not owner-private after securing", ErrDirectoryIdentityChanged, childPath)
	}
	afterRoot, err := root.Lstat(component)
	if err != nil || !afterRoot.IsDir() || afterRoot.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(afterRoot) ||
		!os.SameFile(afterHandle, afterRoot) ||
		(privateDirectoryAccessControlVerified && !privateDirectoryAccessPermitsUse(afterRoot)) {
		return nil, fmt.Errorf("%w: %s changed after securing", ErrDirectoryIdentityChanged, childPath)
	}
	return &OwnedDirectory{path: filepath.Clean(childPath), identity: afterHandle, private: true, parent: parent}, nil
}

// RemoveAll recursively clears this directory through a verified os.Root
// handle, then removes the now-empty directory with a descriptor-relative,
// nonrecursive operation. If the pathname is replaced at any point, recursive
// traversal remains pinned to the originally acquired directory and the
// replacement is never recursively deleted.
func (directory *OwnedDirectory) RemoveAll() (resultErr error) {
	return directory.removeAll(false)
}

// RemoveAllExisting is the strict form of RemoveAll for callers whose success
// claim depends on removing this exact acquired identity. A missing textual
// path is an identity failure rather than idempotent success.
func (directory *OwnedDirectory) RemoveAllExisting() (resultErr error) {
	return directory.removeAll(true)
}

func (directory *OwnedDirectory) removeAll(requireExisting bool) (resultErr error) {
	if directory == nil {
		if requireExisting {
			return fmt.Errorf("%w: missing cleanup identity", ErrDirectoryIdentityChanged)
		}
		return nil
	}
	if _, err := os.Lstat(directory.path); errors.Is(err, os.ErrNotExist) {
		if requireExisting {
			return fmt.Errorf("%w: %s is absent before cleanup", ErrDirectoryIdentityChanged, directory.path)
		}
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
	var parentRoot *os.Root
	var err error
	if directory.parent != nil {
		if directory.parent.path != parentPath {
			return fmt.Errorf("%w: retained parent of %s does not match", ErrDirectoryIdentityChanged, directory.path)
		}
		parentRoot, err = directory.parent.openVerifiedRoot()
		if err != nil {
			return fmt.Errorf("open retained owned directory parent %s: %w", parentPath, err)
		}
	} else {
		var parentIdentity os.FileInfo
		parentRoot, parentIdentity, err = openStableDirectDirectoryRoot(parentPath)
		if err != nil {
			return fmt.Errorf("open owned directory parent %s: %w", parentPath, err)
		}
		if err := verifyDirectDirectoryPath(parentPath, parentIdentity); err != nil {
			_ = parentRoot.Close()
			return fmt.Errorf("%w: parent of %s changed while opening", ErrDirectoryIdentityChanged, directory.path)
		}
	}
	defer func() { resultErr = errors.Join(resultErr, parentRoot.Close()) }()
	child, err := parentRoot.Lstat(base)
	if err != nil || !child.IsDir() || child.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(child) || !os.SameFile(directory.identity, child) {
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
	if err := directory.Verify(); err != nil {
		_ = ownedRoot.Close()
		return err
	}
	if err := verifyRootedOwnedDirectory(parentRoot, base, directory); err != nil {
		_ = ownedRoot.Close()
		return err
	}
	removeErr := removeRootContents(ownedRoot)
	closeErr := ownedRoot.Close()
	if removeErr != nil || closeErr != nil {
		return errors.Join(removeErr, closeErr)
	}
	current, err := parentRoot.Lstat(base)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(current) || !os.SameFile(directory.identity, current) {
		return fmt.Errorf("%w: %s changed before final removal", ErrDirectoryIdentityChanged, directory.path)
	}
	if err := directory.Verify(); err != nil {
		return err
	}
	// Root.Remove is intentionally nonrecursive. Even if the final component
	// changes after the identity check, a nonempty replacement cannot be
	// traversed or deleted.
	if err := parentRoot.Remove(base); err != nil {
		return fmt.Errorf("remove empty owned directory %s: %w", directory.path, err)
	}
	if directory.parent != nil {
		if err := directory.parent.Verify(); err != nil {
			return err
		}
	}
	return nil
}

const (
	ownedDirectoryCleanupBatchSize  = 128
	ownedDirectoryCleanupEntryLimit = 100_000
	ownedDirectoryCleanupDepthLimit = 128
	ownedDirectoryCleanupPassLimit  = 8
)

type ownedDirectoryCleanupBudget struct {
	remaining int
}

func removeRootContents(root *os.Root) error {
	mount, err := ownedDirectoryRootMountIdentity(root)
	if err != nil {
		return fmt.Errorf("%w: cleanup root: %v", ErrOwnedDirectoryFilesystemBoundary, err)
	}
	budget := ownedDirectoryCleanupBudget{remaining: ownedDirectoryCleanupEntryLimit}
	return removeRootContentsOnMount(root, mount, &budget, 0)
}

func removeRootContentsOnMount(
	root *os.Root,
	expectedMount ownedDirectoryMountIdentity,
	budget *ownedDirectoryCleanupBudget,
	depth int,
) error {
	if depth > ownedDirectoryCleanupDepthLimit {
		return fmt.Errorf("%w: maximum depth %d", ErrOwnedDirectoryCleanupBound, ownedDirectoryCleanupDepthLimit)
	}
	for pass := 0; pass < ownedDirectoryCleanupPassLimit; pass++ {
		directory, err := root.Open(".")
		if err != nil {
			return fmt.Errorf("open owned directory contents: %w", err)
		}
		processed := 0
		for {
			entries, readErr := directory.ReadDir(ownedDirectoryCleanupBatchSize)
			for _, entry := range entries {
				if budget == nil || budget.remaining <= 0 {
					return errors.Join(
						fmt.Errorf("%w: maximum entries %d", ErrOwnedDirectoryCleanupBound, ownedDirectoryCleanupEntryLimit),
						directory.Close(),
					)
				}
				budget.remaining--
				processed++
				name := entry.Name()
				if !simplePathComponent(name) {
					return errors.Join(fmt.Errorf("unsafe directory entry %q", name), directory.Close())
				}
				if err := removeRootEntryOnMount(root, name, expectedMount, budget, depth); err != nil {
					return errors.Join(fmt.Errorf("remove owned entry %s: %w", name, err), directory.Close())
				}
			}
			if readErr != nil {
				closeErr := directory.Close()
				if errors.Is(readErr, io.EOF) {
					if closeErr != nil {
						return closeErr
					}
					break
				}
				return errors.Join(fmt.Errorf("read owned directory contents: %w", readErr), closeErr)
			}
		}
		if processed == 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: contents changed across %d cleanup passes", ErrOwnedDirectoryCleanupBound, ownedDirectoryCleanupPassLimit)
}

func removeRootEntryOnMount(
	root *os.Root,
	name string,
	expectedMount ownedDirectoryMountIdentity,
	budget *ownedDirectoryCleanupBudget,
	depth int,
) (resultErr error) {
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if ownedDirectoryEntryIsFilesystemLink(before) && before.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: entry %s is a filesystem link", ErrOwnedDirectoryFilesystemBoundary, name)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		current, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr == nil &&
			ownedDirectoryEntryIsFilesystemLink(current) &&
			current.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: entry %s is a filesystem link", ErrOwnedDirectoryFilesystemBoundary, name)
		}
		if statErr != nil || !os.SameFile(before, current) ||
			current.IsDir() || current.Mode()&os.ModeSymlink != before.Mode()&os.ModeSymlink {
			return fmt.Errorf("%w: entry %s changed before nonrecursive removal", ErrDirectoryIdentityChanged, name)
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	childRoot, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer func() {
		if childRoot != nil {
			resultErr = errors.Join(resultErr, childRoot.Close())
		}
	}()
	opened, err := childRoot.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return fmt.Errorf("%w: directory %s changed while opening", ErrDirectoryIdentityChanged, name)
	}
	after, err := root.Lstat(name)
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(after) ||
		!os.SameFile(opened, after) {
		return fmt.Errorf("%w: directory %s changed after opening", ErrDirectoryIdentityChanged, name)
	}
	childMount, err := ownedDirectoryRootMountIdentity(childRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect child %s mount: %v", ErrOwnedDirectoryFilesystemBoundary, name, err)
	}
	if !expectedMount.equal(childMount) {
		return fmt.Errorf("%w: child %s crosses a filesystem or mount identity", ErrOwnedDirectoryFilesystemBoundary, name)
	}
	if err := removeRootContentsOnMount(childRoot, expectedMount, budget, depth+1); err != nil {
		return err
	}
	if err := childRoot.Close(); err != nil {
		childRoot = nil
		return err
	}
	current, err := root.Lstat(name)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(current) ||
		!os.SameFile(opened, current) {
		return fmt.Errorf("%w: directory %s changed before nonrecursive removal", ErrDirectoryIdentityChanged, name)
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func securePrivateDirectory(
	path string,
	parent *OwnedDirectory,
	expected os.FileInfo,
) (_ *OwnedDirectory, resultErr error) {
	if parent == nil || expected == nil {
		return nil, fmt.Errorf("%w: private directory identity is unavailable", ErrDirectoryIdentityChanged)
	}
	if err := parent.Verify(); err != nil {
		return nil, err
	}
	component := filepath.Base(path)
	if !simplePathComponent(component) ||
		filepath.Clean(filepath.Dir(path)) != parent.path ||
		filepath.Clean(filepath.Join(parent.path, component)) != filepath.Clean(path) {
		return nil, fmt.Errorf("%w: %s is not a direct child of %s", ErrDirectoryIdentityChanged, path, parent.path)
	}
	root, err := parent.openVerifiedRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	child, err := securePrivateChildExpected(parent, root, component, expected)
	if err != nil {
		return nil, err
	}
	if err := parent.Verify(); err != nil {
		return nil, err
	}
	return child, nil
}

func openStableDirectDirectoryRoot(path string) (*os.Root, os.FileInfo, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	fail := func(cause error) (*os.Root, os.FileInfo, error) {
		return nil, nil, errors.Join(cause, root.Close())
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() {
		return fail(errors.Join(
			fmt.Errorf("%w: %s is not a direct directory", ErrDirectoryIdentityChanged, path),
			err,
		))
	}
	if err := verifyDirectDirectoryPath(path, opened); err != nil {
		return fail(err)
	}
	return root, opened, nil
}

func verifyDirectDirectoryPath(path string, expected os.FileInfo) error {
	if expected == nil || !expected.IsDir() {
		return fmt.Errorf("%w: %s has no directory identity", ErrDirectoryIdentityChanged, path)
	}
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		ownedDirectoryEntryIsFilesystemLink(current) ||
		!os.SameFile(expected, current) {
		return fmt.Errorf("%w: %s is not a stable direct directory", ErrDirectoryIdentityChanged, path)
	}
	return nil
}

func captureDirectory(path string) (*OwnedDirectory, error) {
	root, info, err := openStableDirectDirectoryRoot(path)
	if err != nil {
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	return &OwnedDirectory{path: filepath.Clean(path), identity: info}, nil
}

func simplePathComponent(component string) bool {
	return component != "" && component != "." && component != ".." && filepath.Base(component) == component && !strings.ContainsAny(component, `/\\`) && !strings.ContainsRune(component, 0)
}
