package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestAcquirePrivateDirectoryRejectsDirectSymlinkWithoutChmoddingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "owned")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquirePrivateDirectory(link); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("direct symlink acquisition = %v", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestCaptureDirectoryRetainsOpenedIdentityAcrossReplacement(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "captured")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := captureDirectory(path)
	if err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(parent, "captured-original")
	if err := os.Rename(path, original); err != nil {
		t.Skipf("directory replacement unavailable: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalInfo, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(directory.identity, originalInfo) {
		t.Fatal("captured identity did not remain bound to the opened directory")
	}
	if err := directory.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("Verify() replacement = %v, want ErrDirectoryIdentityChanged", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement directory was modified: content=%q err=%v", content, err)
	}
}

func TestSecurePrivateDirectoryRejectsReplacementBeforeChmod(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := captureDirectory(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parentPath, "owned")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	observed, err := captureDirectory(path)
	if err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(parentPath, "owned-original")
	if err := os.Rename(path, original); err != nil {
		t.Skipf("directory replacement unavailable: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := securePrivateDirectory(path, parent, observed.identity); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("secure replacement = %v, want ErrDirectoryIdentityChanged", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement directory was modified: content=%q err=%v", content, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("replacement mode = %o, want 755", info.Mode().Perm())
		}
	}
}

func TestAcquirePrivateDirectoryResolvesLegitimateAncestorSymlink(t *testing.T) {
	container := t.TempDir()
	physical := filepath.Join(container, "physical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(container, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	directory, err := AcquirePrivateDirectory(filepath.Join(alias, "agentx", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	physicalResolved, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalResolved, "agentx", "sessions")
	if directory.Path() != want {
		t.Fatalf("physical path = %q, want %q", directory.Path(), want)
	}
	if err := directory.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnedDirectoryRejectsDirectChildSymlink(t *testing.T) {
	root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root.Path(), "child")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsurePrivateChild("child"); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("symlink child = %v", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("child symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestOwnedDirectoryConcurrentChildCreationReacquiresWinner(t *testing.T) {
	root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
	if err != nil {
		t.Fatal(err)
	}

	const creators = 2
	ready := make(chan struct{}, creators)
	release := make(chan struct{})
	root.beforeCreateChild = func(string) error {
		ready <- struct{}{}
		<-release
		return nil
	}

	type result struct {
		directory *OwnedDirectory
		err       error
	}
	results := make(chan result, creators)
	var group sync.WaitGroup
	group.Add(creators)
	for range creators {
		go func() {
			defer group.Done()
			directory, createErr := root.EnsurePrivateChild("sessions")
			results <- result{directory: directory, err: createErr}
		}()
	}
	for range creators {
		<-ready
	}
	close(release)
	group.Wait()
	close(results)

	var acquired []*OwnedDirectory
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent child creation: %v", result.err)
		}
		if err := result.directory.Verify(); err != nil {
			t.Fatalf("verify concurrently acquired child: %v", err)
		}
		acquired = append(acquired, result.directory)
	}
	if len(acquired) != creators {
		t.Fatalf("acquired directories = %d, want %d", len(acquired), creators)
	}
	if acquired[0].Path() != acquired[1].Path() || !os.SameFile(acquired[0].identity, acquired[1].identity) {
		t.Fatal("concurrent creators did not acquire the same child directory")
	}
	info, err := os.Lstat(acquired[0].Path())
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("concurrently acquired child is not a direct directory: %v", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("concurrently acquired child mode = %o, want 700", info.Mode().Perm())
	}
}

func TestOwnedDirectoryRacedChildCreationRejectsUnsafeWinner(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string)
	}{
		{
			name: "regular file",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
			if err != nil {
				t.Fatal(err)
			}
			root.beforeCreateChild = func(path string) error {
				test.create(t, path)
				return nil
			}
			if _, err := root.EnsurePrivateChild("sessions"); !errors.Is(err, ErrDirectoryIdentityChanged) {
				t.Fatalf("raced unsafe winner = %v", err)
			}
		})
	}
}

func TestOwnedDirectoryChildCreationPinsOriginalAcrossParentReplacement(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent, "owned-original")
	directory.beforeCreateChild = func(string) error {
		if err := os.Rename(directory.Path(), original); err != nil {
			return err
		}
		return os.Rename(replacement, directory.Path())
	}

	if _, err := directory.EnsurePrivateChild("sessions"); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("child creation after parent replacement = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory.Path(), "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child was created beneath replacement: %v", err)
	}
	info, err := os.Lstat(filepath.Join(original, "sessions"))
	if err != nil {
		t.Fatalf("pinned original did not receive child: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("pinned child is not a direct directory: %v", info.Mode())
	}
}

func TestOwnedDirectoryDetectsPrivacyDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows FileMode cannot represent DACL privacy drift")
	}
	directory, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := directory.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("privacy drift verification = %v", err)
	}
}

func TestOwnedDirectoryDetectsReplacementAndWillNotCleanIt(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent, "original")
	if err := os.Rename(directory.Path(), original); err != nil {
		t.Skipf("directory replacement unavailable: %v", err)
	}
	if err := os.Mkdir(directory.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory.Path(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := directory.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("replacement verification = %v", err)
	}
	if _, err := directory.EnsurePrivateChild("must-not-create"); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("child creation beneath replacement = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Path(), "must-not-create")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child appeared beneath replacement: %v", err)
	}
	if err := directory.RemoveAll(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("replacement cleanup = %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("replacement was modified: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectoryCleanupRejectsReplacementRace(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	ownedNested := filepath.Join(directory.Path(), "nested")
	if err := os.Mkdir(ownedNested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownedNested, "owned"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementNested := filepath.Join(replacement, "nested")
	if err := os.Mkdir(replacementNested, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(replacementNested, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent, "owned-original")
	directory.beforeRemoveContents = func() error {
		if err := os.Rename(directory.Path(), original); err != nil {
			return err
		}
		return os.Rename(replacement, directory.Path())
	}
	err = directory.RemoveAll()
	if !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("cleanup after in-flight replacement = %v", err)
	}
	replacementSentinel := filepath.Join(directory.Path(), "nested", filepath.Base(sentinel))
	if content, err := os.ReadFile(replacementSentinel); err != nil || string(content) != "keep" {
		t.Fatalf("replacement was recursively deleted: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(original, "nested", "owned")); err != nil || string(content) != "remove" {
		t.Fatalf("moved original was modified after identity loss: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectoryCleanupRemovesOwnedTreeWithoutFollowingExternalLink(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(directory.Path(), "one", "two")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "owned"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(directory.Path(), "external-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := directory.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned directory still exists: %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("external symlink target was modified: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectoryStrictCleanupRejectsMovedIdentity(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory.Path(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(directory.Path(), moved); err != nil {
		t.Skipf("directory rename unavailable: %v", err)
	}
	if err := directory.RemoveAllExisting(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("strict cleanup after move = %v, want identity failure", err)
	}
	content, err := os.ReadFile(filepath.Join(moved, "sentinel"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("strict cleanup modified moved identity: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectoryStrictCleanupRejectsNilIdentity(t *testing.T) {
	var directory *OwnedDirectory
	if err := directory.RemoveAll(); err != nil {
		t.Fatalf("idempotent nil cleanup = %v", err)
	}
	if err := directory.RemoveAllExisting(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("strict nil cleanup = %v, want ErrDirectoryIdentityChanged", err)
	}
}

func TestOwnedDirectoryStrictCleanupRejectsRetainedParentReplacement(t *testing.T) {
	container := t.TempDir()
	parent, err := AcquirePrivateDirectory(filepath.Join(container, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	stage, err := parent.CreatePrivateChild(".agentx-delete-v1.stage")
	if err != nil {
		t.Fatal(err)
	}
	originalSentinel := filepath.Join(stage.Path(), "original")
	if err := os.WriteFile(originalSentinel, []byte("keep original"), 0o600); err != nil {
		t.Fatal(err)
	}
	movedParent := filepath.Join(container, "sessions-moved")
	replacementSentinel := filepath.Join(parent.Path(), filepath.Base(stage.Path()), "replacement")
	stage.beforeRemoveContents = func() error {
		if err := os.Rename(parent.Path(), movedParent); err != nil {
			return err
		}
		if err := os.Mkdir(parent.Path(), 0o700); err != nil {
			return err
		}
		replacementStage := filepath.Join(parent.Path(), filepath.Base(stage.Path()))
		if err := os.Mkdir(replacementStage, 0o700); err != nil {
			return err
		}
		return os.WriteFile(replacementSentinel, []byte("keep replacement"), 0o600)
	}

	if err := stage.RemoveAllExisting(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("strict cleanup with replaced retained parent = %v, want identity failure", err)
	}
	if content, err := os.ReadFile(filepath.Join(movedParent, filepath.Base(stage.Path()), "original")); err != nil || string(content) != "keep original" {
		t.Fatalf("moved original was modified: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(replacementSentinel); err != nil || string(content) != "keep replacement" {
		t.Fatalf("replacement was modified: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectoryVerifyRejectsChildMovedIntoReplacementParent(t *testing.T) {
	container := t.TempDir()
	parent, err := AcquirePrivateDirectory(filepath.Join(container, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.CreatePrivateChild("workspace")
	if err != nil {
		t.Fatal(err)
	}
	movedParent := filepath.Join(container, "sessions-moved")
	if err := os.Rename(parent.Path(), movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(movedParent, "workspace"), child.Path()); err != nil {
		t.Fatal(err)
	}
	if err := child.verifySelf(); err != nil {
		t.Fatalf("child self identity should remain stable after move: %v", err)
	}
	if err := child.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("child beneath replacement parent = %v, want identity failure", err)
	}
}

func TestRemoveRootContentsRejectsFilesystemBoundaryAndBoundsWork(t *testing.T) {
	t.Run("filesystem identity", func(t *testing.T) {
		path := t.TempDir()
		nested := filepath.Join(path, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(nested, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		mount, err := ownedDirectoryRootMountIdentity(root)
		if err != nil {
			t.Fatal(err)
		}
		mount.device++
		budget := ownedDirectoryCleanupBudget{remaining: 8}
		if err := removeRootContentsOnMount(root, mount, &budget, 0); !errors.Is(err, ErrOwnedDirectoryFilesystemBoundary) {
			t.Fatalf("cleanup across filesystem identity = %v, want boundary failure", err)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
			t.Fatalf("boundary child was modified: content=%q err=%v", content, err)
		}
	})

	t.Run("entry ceiling", func(t *testing.T) {
		path := t.TempDir()
		for _, name := range []string{"one", "two"} {
			if err := os.WriteFile(filepath.Join(path, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		mount, err := ownedDirectoryRootMountIdentity(root)
		if err != nil {
			t.Fatal(err)
		}
		budget := ownedDirectoryCleanupBudget{remaining: 1}
		if err := removeRootContentsOnMount(root, mount, &budget, 0); !errors.Is(err, ErrOwnedDirectoryCleanupBound) {
			t.Fatalf("cleanup past entry ceiling = %v, want bound failure", err)
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("bounded cleanup left %d entries, want one retryable remainder", len(entries))
		}
	})
}

func TestOwnedDirectoryProcessesComponentsIndividually(t *testing.T) {
	root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "root"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := root.EnsurePrivateChild("one", "two", "three")
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsurePrivateChild("one/two"); err == nil {
		t.Fatal("multi-component child name was accepted")
	}
}

func TestOwnedDirectoryInspectPrivateChildIsReadOnly(t *testing.T) {
	root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root.Path(), "session")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(childPath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(childPath)
	if err != nil {
		t.Fatal(err)
	}

	child, err := root.InspectPrivateChild("session")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Verify(); err != nil {
		t.Fatalf("verify inspected child: %v", err)
	}
	after, err := os.Lstat(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("inspection replaced the child directory")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("inspection changed child mode from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("inspection changed child content: content=%q err=%v", content, err)
	}

	missing := filepath.Join(root.Path(), "missing")
	if _, err := root.InspectPrivateChild("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing child inspection = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created missing child: %v", err)
	}
}

func TestOwnedDirectoryInspectPrivateChildRejectsUnsafeEntries(t *testing.T) {
	t.Run("insecure permissions remain unchanged", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows FileMode cannot represent DACL privacy")
		}
		root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		childPath := filepath.Join(root.Path(), "session")
		if err := os.Mkdir(childPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(childPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := root.InspectPrivateChild("session"); !errors.Is(err, ErrDirectoryIdentityChanged) {
			t.Fatalf("insecure child inspection = %v, want ErrDirectoryIdentityChanged", err)
		}
		info, err := os.Lstat(childPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("inspection repaired insecure mode to %o", info.Mode().Perm())
		}
	})

	t.Run("replaced with regular file", func(t *testing.T) {
		root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		child, err := root.EnsurePrivateChild("session")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(child.Path(), filepath.Join(root.Path(), "session-original")); err != nil {
			t.Fatal(err)
		}
		const replacement = "replacement must remain untouched"
		if err := os.WriteFile(child.Path(), []byte(replacement), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := root.InspectPrivateChild("session"); !errors.Is(err, ErrDirectoryIdentityChanged) {
			t.Fatalf("replaced child inspection = %v, want ErrDirectoryIdentityChanged", err)
		}
		if content, err := os.ReadFile(child.Path()); err != nil || string(content) != replacement {
			t.Fatalf("replacement was modified: content=%q err=%v", content, err)
		}
	})

	t.Run("direct symlink", func(t *testing.T) {
		root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root.Path(), "session")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := root.InspectPrivateChild("session"); !errors.Is(err, ErrDirectoryIdentityChanged) {
			t.Fatalf("symlink child inspection = %v, want ErrDirectoryIdentityChanged", err)
		}
		after, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("symlink target was replaced")
		}
		if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})

	t.Run("replaced parent", func(t *testing.T) {
		container := t.TempDir()
		root, err := AcquirePrivateDirectory(filepath.Join(container, "owned"))
		if err != nil {
			t.Fatal(err)
		}
		replacement := filepath.Join(container, "replacement")
		if err := os.Mkdir(replacement, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(replacement, "session"), 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(replacement, "session", "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(root.Path(), filepath.Join(container, "owned-original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, root.Path()); err != nil {
			t.Fatal(err)
		}
		if _, err := root.InspectPrivateChild("session"); !errors.Is(err, ErrDirectoryIdentityChanged) {
			t.Fatalf("inspection beneath replaced parent = %v, want ErrDirectoryIdentityChanged", err)
		}
		if content, err := os.ReadFile(filepath.Join(root.Path(), "session", "sentinel")); err != nil || string(content) != "keep" {
			t.Fatalf("replacement parent was modified: content=%q err=%v", content, err)
		}
	})
}

func TestOwnedDirectoryCreatePrivateChildIsExclusive(t *testing.T) {
	t.Run("preexisting child", func(t *testing.T) {
		root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		existing, err := root.EnsurePrivateChild("session")
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(existing.Path())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := root.CreatePrivateChild("session"); !errors.Is(err, os.ErrExist) {
			t.Fatalf("preexisting child creation = %v, want os.ErrExist", err)
		}
		after, err := os.Lstat(existing.Path())
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("exclusive creation replaced the preexisting child")
		}
	})

	t.Run("concurrent creators", func(t *testing.T) {
		root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		const creators = 2
		ready := make(chan struct{}, creators)
		release := make(chan struct{})
		root.beforeCreateChild = func(string) error {
			ready <- struct{}{}
			<-release
			return nil
		}
		type result struct {
			directory *OwnedDirectory
			err       error
		}
		results := make(chan result, creators)
		var group sync.WaitGroup
		group.Add(creators)
		for range creators {
			go func() {
				defer group.Done()
				directory, createErr := root.CreatePrivateChild("session")
				results <- result{directory: directory, err: createErr}
			}()
		}
		for range creators {
			<-ready
		}
		close(release)
		group.Wait()
		close(results)

		var created, contended int
		for result := range results {
			switch {
			case result.err == nil:
				created++
				if result.directory == nil {
					t.Fatal("successful creator returned no owner")
				}
				if err := result.directory.Verify(); err != nil {
					t.Fatalf("verify created child: %v", err)
				}
			case errors.Is(result.err, os.ErrExist):
				contended++
				if result.directory != nil {
					t.Fatal("contended creator returned an owner")
				}
			default:
				t.Fatalf("concurrent creation = %v", result.err)
			}
		}
		if created != 1 || contended != 1 {
			t.Fatalf("concurrent results: created=%d contended=%d, want 1 each", created, contended)
		}
	})
}

func TestOwnedDirectoryCreatePrivateChildRejectsFirstCreatorWinner(t *testing.T) {
	root, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
	if err != nil {
		t.Fatal(err)
	}
	var winner os.FileInfo
	const sentinelContent = "first creator retains ownership"
	root.beforeCreateChild = func(path string) error {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte(sentinelContent), 0o600); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(path, 0o755); err != nil {
				return err
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		winner = info
		return nil
	}
	if _, err := root.CreatePrivateChild("session"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("creation against first-creator winner = %v, want os.ErrExist", err)
	}
	path := filepath.Join(root.Path(), "session")
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if winner == nil || !os.SameFile(winner, after) {
		t.Fatal("first-creator winner was replaced")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o755 {
		t.Fatalf("first-creator winner mode changed to %o", after.Mode().Perm())
	}
	if content, err := os.ReadFile(filepath.Join(path, "sentinel")); err != nil || string(content) != sentinelContent {
		t.Fatalf("first-creator winner was modified: content=%q err=%v", content, err)
	}
}

func TestOwnedDirectorySyncVerifiesIdentity(t *testing.T) {
	t.Run("stable identity", func(t *testing.T) {
		directory, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "owned"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory.Path(), "entry"), []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := directory.Sync(); err != nil {
			t.Fatalf("sync stable directory: %v", err)
		}
		if err := directory.Verify(); err != nil {
			t.Fatalf("verify after sync: %v", err)
		}
	})

	t.Run("replaced identity", func(t *testing.T) {
		container := t.TempDir()
		directory, err := AcquirePrivateDirectory(filepath.Join(container, "owned"))
		if err != nil {
			t.Fatal(err)
		}
		original := filepath.Join(container, "owned-original")
		if err := os.Rename(directory.Path(), original); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(directory.Path(), 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(directory.Path(), "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := directory.Sync(); !errors.Is(err, ErrDirectoryIdentityChanged) {
			t.Fatalf("sync replaced directory = %v, want ErrDirectoryIdentityChanged", err)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
			t.Fatalf("sync modified replacement: content=%q err=%v", content, err)
		}
		originalInfo, err := os.Lstat(original)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(directory.identity, originalInfo) {
			t.Fatal("original directory identity was lost")
		}
	})
}

func TestOwnedDirectoryDetachPrivateChildCommitsAndCleansReturnedOwner(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.EnsurePrivateChild("ses_detach")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(child.Path(), "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "transcript.jsonl"), []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const staging = ".agentx-delete-v1.ses_detach.revision"
	result, err := parent.DetachPrivateChild(child, staging)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Owner == nil {
		t.Fatalf("detach result = %#v, want committed destination owner", result)
	}
	if got, want := result.Owner.Path(), filepath.Join(parent.Path(), staging); got != want {
		t.Fatalf("detached path = %q, want %q", got, want)
	}
	if err := result.Owner.Verify(); err != nil {
		t.Fatalf("verify detached owner: %v", err)
	}
	if err := child.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("source owner remained reachable after detach: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(result.Owner.Path(), "nested", "transcript.jsonl")); err != nil || string(content) != "owned\n" {
		t.Fatalf("detached content = %q, err=%v", content, err)
	}
	if err := result.Owner.RemoveAll(); err != nil {
		t.Fatalf("clean detached owner: %v", err)
	}
	if _, err := os.Lstat(result.Owner.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached owner remains after cleanup: %v", err)
	}
	if err := parent.Verify(); err != nil {
		t.Fatalf("parent changed during detach and cleanup: %v", err)
	}
}

func TestOwnedDirectoryPreflightPrivateChildDetachDoesNotMutateNamespace(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.CreatePrivateChild("ses_preflight")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(child.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.PreflightPrivateChildDetach(child); err != nil {
		t.Fatalf("preflight stable detach: %v", err)
	}
	after, err := os.Lstat(child.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("preflight changed the source namespace identity")
	}
	entries, err := os.ReadDir(parent.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(child.Path()) {
		t.Fatalf("preflight changed parent entries: %v", entries)
	}
}

func TestOwnedDirectoryDetachPrivateChildVerifiedRunsAuthorityAtMutationBoundary(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.CreatePrivateChild("ses_verified")
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("authority changed")
	called := false
	result, err := parent.DetachPrivateChildVerified(
		child,
		".agentx-delete-v1.verified",
		func() error {
			called = true
			return denied
		},
	)
	if !called || !errors.Is(err, denied) {
		t.Fatalf("verified detach = %#v, %v; verifier called=%t", result, err, called)
	}
	if result.Committed || result.Owner != nil {
		t.Fatalf("failed verifier committed detach: %#v", result)
	}
	if err := child.Verify(); err != nil {
		t.Fatalf("failed verifier changed source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent.Path(), ".agentx-delete-v1.verified")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed verifier created destination: %v", err)
	}
}

func TestOwnedDirectoryDetachPrivateChildRejectsDestinationCollision(t *testing.T) {
	t.Run("preexisting", func(t *testing.T) {
		parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
		if err != nil {
			t.Fatal(err)
		}
		child, err := parent.EnsurePrivateChild("ses_source")
		if err != nil {
			t.Fatal(err)
		}
		collision, err := parent.EnsurePrivateChild(".agentx-delete-v1.collision")
		if err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(collision.Path(), "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}

		result, err := parent.DetachPrivateChild(child, filepath.Base(collision.Path()))
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("destination collision = %v, want os.ErrExist", err)
		}
		if result.Committed || result.Owner != nil {
			t.Fatalf("destination collision committed: %#v", result)
		}
		if err := child.Verify(); err != nil {
			t.Fatalf("source changed after collision: %v", err)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
			t.Fatalf("collision destination was modified: content=%q err=%v", content, err)
		}
	})

	t.Run("appears before commit", func(t *testing.T) {
		parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
		if err != nil {
			t.Fatal(err)
		}
		child, err := parent.EnsurePrivateChild("ses_source")
		if err != nil {
			t.Fatal(err)
		}
		const destination = ".agentx-delete-v1.raced"
		parent.beforeDetachRename = func(_, path string) error {
			return os.Mkdir(path, 0o700)
		}

		result, err := parent.DetachPrivateChild(child, destination)
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("raced destination collision = %v, want os.ErrExist", err)
		}
		if result.Committed || result.Owner != nil {
			t.Fatalf("raced destination collision committed: %#v", result)
		}
		if err := child.Verify(); err != nil {
			t.Fatalf("source changed after raced collision: %v", err)
		}
		info, statErr := os.Lstat(filepath.Join(parent.Path(), destination))
		if statErr != nil || !info.IsDir() {
			t.Fatalf("raced destination was removed or replaced: info=%v err=%v", info, statErr)
		}
	})

	t.Run("appears at atomic boundary", func(t *testing.T) {
		parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
		if err != nil {
			t.Fatal(err)
		}
		child, err := parent.EnsurePrivateChild("ses_source")
		if err != nil {
			t.Fatal(err)
		}
		const destination = ".agentx-delete-v1.atomic-race"
		var collisionIdentity os.FileInfo
		parent.beforeDetachCommit = func(_, path string) error {
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			collisionIdentity = info
			return nil
		}

		result, err := parent.DetachPrivateChild(child, destination)
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("atomic-boundary collision = %v, want os.ErrExist", err)
		}
		if result.Committed || result.Owner != nil {
			t.Fatalf("atomic-boundary collision committed: %#v", result)
		}
		if err := child.Verify(); err != nil {
			t.Fatalf("source changed after atomic-boundary collision: %v", err)
		}
		info, statErr := os.Lstat(filepath.Join(parent.Path(), destination))
		if statErr != nil || collisionIdentity == nil || !os.SameFile(collisionIdentity, info) {
			t.Fatalf("atomic collision destination was overwritten: info=%v err=%v", info, statErr)
		}
	})
}

func TestOwnedDirectoryDetachPrivateChildRejectsSourceReplacement(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.EnsurePrivateChild("ses_source")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := parent.EnsurePrivateChild("replacement")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement.Path(), "sentinel"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent.Path(), "source-original")
	parent.beforeDetachRename = func(source, _ string) error {
		if err := os.Rename(source, original); err != nil {
			return err
		}
		return os.Rename(replacement.Path(), source)
	}

	const destination = ".agentx-delete-v1.replaced-source"
	result, err := parent.DetachPrivateChild(child, destination)
	if !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("source replacement = %v, want ErrDirectoryIdentityChanged", err)
	}
	if result.Committed || result.Owner != nil {
		t.Fatalf("source replacement committed: %#v", result)
	}
	if err := child.Verify(); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("replaced source passed old identity verification: %v", err)
	}
	if info, err := os.Lstat(original); err != nil || !info.IsDir() {
		t.Fatalf("original source identity was lost: info=%v err=%v", info, err)
	}
	if content, err := os.ReadFile(filepath.Join(parent.Path(), "ses_source", "sentinel")); err != nil || string(content) != "keep" {
		t.Fatalf("replacement source was modified: content=%q err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(parent.Path(), destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detach destination appeared after source replacement: %v", err)
	}
}

func TestOwnedDirectoryDetachPrivateChildRejectsParentReplacement(t *testing.T) {
	container := t.TempDir()
	parent, err := AcquirePrivateDirectory(filepath.Join(container, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.EnsurePrivateChild("ses_source")
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(container, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(replacement, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(container, "sessions-original")
	parent.beforeDetachRename = func(_, _ string) error {
		if err := os.Rename(parent.Path(), original); err != nil {
			return err
		}
		return os.Rename(replacement, parent.Path())
	}

	const destination = ".agentx-delete-v1.replaced-parent"
	result, err := parent.DetachPrivateChild(child, destination)
	if !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("parent replacement = %v, want ErrDirectoryIdentityChanged", err)
	}
	if result.Committed || result.Owner != nil {
		t.Fatalf("parent replacement committed: %#v", result)
	}
	if content, err := os.ReadFile(filepath.Join(parent.Path(), "sentinel")); err != nil || string(content) != "keep" {
		t.Fatalf("replacement parent was modified: content=%q err=%v", content, err)
	}
	if info, err := os.Lstat(filepath.Join(original, "ses_source")); err != nil || !info.IsDir() {
		t.Fatalf("original parent lost its source child: info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(original, destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original parent received detach destination: %v", err)
	}
}

func TestOwnedDirectoryDetachPrivateChildReportsPostCommitFailure(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.EnsurePrivateChild("ses_source")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("post-rename verification failed")
	parent.afterDetachRename = func(_, _ string) error {
		return sentinel
	}

	const destination = ".agentx-delete-v1.committed-error"
	result, err := parent.DetachPrivateChild(child, destination)
	if !errors.Is(err, sentinel) {
		t.Fatalf("post-commit failure = %v, want sentinel", err)
	}
	if !result.Committed || result.Owner == nil {
		t.Fatalf("post-commit failure lost committed state: %#v", result)
	}
	if err := result.Owner.Verify(); err != nil {
		t.Fatalf("post-commit owner is unusable: %v", err)
	}
	if _, err := os.Lstat(child.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains after committed rename: %v", err)
	}
	if err := result.Owner.RemoveAll(); err != nil {
		t.Fatalf("cleanup after post-commit failure: %v", err)
	}
}
