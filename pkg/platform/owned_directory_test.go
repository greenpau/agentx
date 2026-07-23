package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
