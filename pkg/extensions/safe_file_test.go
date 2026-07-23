package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadRegularFileAcceptsPrivateBoundedFile(t *testing.T) {
	path := writeRegularFileFixture(t, "extension.md", []byte("trusted extension"), 0o600)

	data, err := ReadRegularFile(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "trusted extension" {
		t.Fatalf("content = %q", data)
	}
}

func TestReadRegularFileRejectsInvalidLimit(t *testing.T) {
	path := writeRegularFileFixture(t, "extension.md", []byte("x"), 0o600)
	for _, limit := range []int64{-1, 0} {
		if _, err := ReadRegularFile(path, limit); err == nil || !strings.Contains(err.Error(), "limit must be positive") {
			t.Fatalf("limit %d: expected validation error, got %v", limit, err)
		}
	}
}

func TestReadRegularFileRejectsSymlink(t *testing.T) {
	target := writeRegularFileFixture(t, "target.md", []byte("trusted extension"), 0o600)
	link := filepath.Join(filepath.Dir(target), "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := ReadRegularFile(link, 64); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestReadRegularFileRejectsHardlink(t *testing.T) {
	target := writeRegularFileFixture(t, "target.md", []byte("trusted extension"), 0o600)
	link := filepath.Join(filepath.Dir(target), "hardlink.md")
	if err := os.Link(target, link); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}

	if _, err := ReadRegularFile(target, 64); err == nil || !strings.Contains(err.Error(), "exactly one filesystem link") {
		t.Fatalf("expected hardlink rejection, got %v", err)
	}
}

func TestReadRegularFileRejectsGroupOrOtherWritableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o620, 0o602} {
		t.Run(mode.String(), func(t *testing.T) {
			path := writeRegularFileFixture(t, "extension.md", []byte("trusted extension"), mode)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o022 == 0 {
				t.Skip("filesystem does not preserve group/other write mode bits")
			}

			if _, err := ReadRegularFile(path, 64); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
				t.Fatalf("expected writable-file rejection, got %v", err)
			}
		})
	}
}

func TestReadRegularFileRejectsOversizeFile(t *testing.T) {
	path := writeRegularFileFixture(t, "extension.md", []byte("12345"), 0o600)

	if _, err := ReadRegularFile(path, 4); err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func TestReadOpenedRegularFileRejectsChangedSnapshot(t *testing.T) {
	path := writeRegularFileFixture(t, "extension.md", []byte("original"), 0o600)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("modified")
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	changedTime := before.ModTime().Add(2 * time.Hour)
	if err := os.Chtimes(path, changedTime, changedTime); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}

	if _, err := readOpenedRegularFile(file, before, 64); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected snapshot-change rejection, got %v", err)
	}
}

func TestReadOpenedRegularFileRejectsIdentitySwap(t *testing.T) {
	first := writeRegularFileFixture(t, "first.md", []byte("first"), 0o600)
	second := writeRegularFileFixture(t, "second.md", []byte("second"), 0o600)
	before, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := readOpenedRegularFile(file, before, 64); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected identity-change rejection, got %v", err)
	}
}

func writeRegularFileFixture(t *testing.T, name string, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
