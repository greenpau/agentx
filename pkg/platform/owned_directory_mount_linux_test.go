//go:build linux

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOwnedDirectoryRejectsSameDeviceBindMountChild(t *testing.T) {
	parent, err := AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	target := filepath.Join(parent.Path(), "session")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(parent.Path())
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	parentStat, parentOK := parentInfo.Sys().(*syscall.Stat_t)
	sourceStat, sourceOK := sourceInfo.Sys().(*syscall.Stat_t)
	if !parentOK || !sourceOK || parentStat.Dev != sourceStat.Dev {
		t.Skip("temporary directories do not share one device")
	}
	sentinel := filepath.Join(source, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("bind mounts unavailable: %v", err)
	}
	defer func() {
		if err := syscall.Unmount(target, 0); err != nil {
			t.Errorf("unmount bind target: %v", err)
		}
	}()

	if _, err := parent.InspectPrivateChild("session"); !errors.Is(err, ErrDirectoryIdentityChanged) {
		t.Fatalf("inspect bind-mounted child = %v, want identity failure", err)
	}
	if err := parent.RemoveAllExisting(); !errors.Is(err, ErrOwnedDirectoryFilesystemBoundary) {
		t.Fatalf("cleanup across bind mount = %v, want filesystem boundary failure", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("bind-mounted contents were modified: content=%q err=%v", content, err)
	}
}
