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

func TestOwnedDirectoryCleanupPinsOriginalAcrossReplacementRace(t *testing.T) {
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
	if entries, err := os.ReadDir(original); err != nil || len(entries) != 0 {
		t.Fatalf("pinned original was not cleared: entries=%v err=%v", entries, err)
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
