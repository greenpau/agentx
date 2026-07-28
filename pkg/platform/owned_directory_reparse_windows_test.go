//go:build windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOwnedDirectoryStrictCleanupRejectsJunction(t *testing.T) {
	parent := t.TempDir()
	directory, err := AcquirePrivateDirectory(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(parent, "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	junction := filepath.Join(directory.Path(), "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, external).CombinedOutput(); err != nil {
		t.Skipf("directory junctions unavailable: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = os.Remove(junction)
	})

	if err := directory.RemoveAllExisting(); !errors.Is(err, ErrOwnedDirectoryFilesystemBoundary) {
		t.Fatalf("strict cleanup across junction = %v, want filesystem boundary failure", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("junction target was modified: content=%q err=%v", content, err)
	}
	if _, err := os.Lstat(junction); err != nil {
		t.Fatalf("junction was removed after boundary failure: %v", err)
	}
}
