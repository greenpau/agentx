package sessionlock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const lockHelperPathEnv = "AGENTX_TEST_SESSION_LOCK_PATH"

func TestAcquireRejectsNilContextAndIndirectLockFiles(t *testing.T) {
	directory := t.TempDir()
	if _, err := Acquire(nil, filepath.Join(directory, "nil.lock")); err == nil {
		t.Fatal("nil acquisition context was accepted")
	}

	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("do not mutate"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink.lock")
	if err := os.Symlink(target, symlink); err == nil {
		if _, err := Acquire(context.Background(), symlink); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("symlink lock acquisition = %v, want ErrUnsafePath", err)
		}
	}

	hardlink := filepath.Join(directory, "hardlink.lock")
	if err := os.Link(target, hardlink); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := Acquire(context.Background(), hardlink); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("hard-linked lock acquisition = %v, want ErrUnsafePath", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("hard-link target mode changed before rejection: %o", info.Mode().Perm())
		}
	}
}

func TestAcquireRejectsIndirectParentWithoutCreatingLock(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "session")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	indirect := filepath.Join(directory, "session-link")
	if err := os.Symlink(target, indirect); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := Acquire(context.Background(), filepath.Join(indirect, ".session.lock")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("indirect-parent acquisition = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".session.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock created through indirect parent: %v", err)
	}
}

func TestOpenStableParentRetainsOpenedIdentityAcrossReplacement(t *testing.T) {
	container := t.TempDir()
	path := filepath.Join(container, "session")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	parentHandle, identity, err := openStableParent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer parentHandle.Close()

	original := filepath.Join(container, "session-original")
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

	opened, err := parentHandle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(identity, opened) {
		t.Fatal("retained parent identity did not remain bound to the opened directory")
	}
	if err := verifyStableParentHandle(parentHandle, path, identity); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("verifyStableParentHandle() replacement = %v, want ErrUnsafePath", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "replacement" {
		t.Fatalf("replacement directory was modified: content=%q err=%v", content, err)
	}
}

func TestAcquireExistingRequiresExistingLockWithoutMutation(t *testing.T) {
	t.Run("missing lock", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, ".session.lock")

		if _, err := AcquireExisting(context.Background(), path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("AcquireExisting() missing lock = %v, want os.ErrNotExist", err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("AcquireExisting() created entries: %v", entries)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		directory := t.TempDir()
		parent := filepath.Join(directory, "missing-session")
		path := filepath.Join(parent, ".session.lock")

		if _, err := AcquireExisting(context.Background(), path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("AcquireExisting() missing parent = %v, want os.ErrNotExist", err)
		}
		if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("AcquireExisting() created missing parent: %v", err)
		}
	})

	t.Run("existing lock", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, ".session.lock")
		const contents = "legacy lock contents"
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		lock, err := AcquireExisting(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Verify(); err != nil {
			_ = lock.Close()
			t.Fatalf("Verify() existing lock: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != contents {
			t.Fatalf("AcquireExisting() changed contents: got %q", got)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && after.Mode().Perm() != before.Mode().Perm() {
			t.Fatalf("AcquireExisting() changed mode from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})
}

func TestAcquireRetainsCreateAndSecureBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".session.lock")
	lock, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("Acquire() created mode %v, want regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("Acquire() created permissions %o, want 600", info.Mode().Perm())
	}
}

func TestAcquireExistingRejectsUnsafeIdentityWithoutMutation(t *testing.T) {
	t.Run("direct symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target")
		const contents = "do not mutate"
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, ".session.lock")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}

		if _, err := AcquireExisting(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("AcquireExisting() symlink = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != contents {
			t.Fatalf("symlink target mutated: got %q", got)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target")
		const contents = "do not mutate"
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, ".session.lock")
		if err := os.Link(target, path); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		before, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := AcquireExisting(context.Background(), path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("AcquireExisting() hard link = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != contents {
			t.Fatalf("hard-link target mutated: got %q", got)
		}
		after, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && after.Mode().Perm() != before.Mode().Perm() {
			t.Fatalf("hard-link target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})

	t.Run("indirect parent", func(t *testing.T) {
		directory := t.TempDir()
		targetParent := filepath.Join(directory, "session")
		if err := os.Mkdir(targetParent, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(targetParent, ".session.lock")
		const contents = "do not mutate"
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		indirectParent := filepath.Join(directory, "session-link")
		if err := os.Symlink(targetParent, indirectParent); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}

		if _, err := AcquireExisting(
			context.Background(),
			filepath.Join(indirectParent, ".session.lock"),
		); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("AcquireExisting() indirect parent = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != contents {
			t.Fatalf("indirect-parent target mutated: got %q", got)
		}
	})
}

func TestAcquireExistingVerifyRejectsReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".session.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireExisting(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Errorf("close lock: %v", err)
		}
	}()

	if err := os.Rename(path, filepath.Join(directory, "old-lock")); err != nil {
		t.Fatal(err)
	}
	const replacement = "replacement must remain untouched"
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lock.Verify(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Verify() after replacement = %v, want ErrUnsafePath", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != replacement {
		t.Fatalf("replacement lock mutated: got %q", got)
	}
}

func TestVerifyRejectsLockAndParentIdentityDrift(t *testing.T) {
	t.Run("additional hard link", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, ".session.lock")
		lock, err := Acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := lock.Close(); err != nil {
				t.Errorf("close lock: %v", err)
			}
		}()
		if err := os.Link(path, filepath.Join(directory, "lock-alias")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		if err := lock.Verify(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Verify() after hard link = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("direct path replacement", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, ".session.lock")
		lock, err := Acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := lock.Close(); err != nil {
				t.Errorf("close lock: %v", err)
			}
		}()
		if err := os.Rename(path, filepath.Join(directory, "detached-lock")); err != nil {
			t.Fatal(err)
		}
		const replacement = "replacement lock must remain untouched"
		if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lock.Verify(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Verify() after lock replacement = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != replacement {
			t.Fatalf("replacement lock mutated: got %q", got)
		}
	})

	t.Run("parent replacement", func(t *testing.T) {
		workspace := t.TempDir()
		session := filepath.Join(workspace, "session")
		if err := os.Mkdir(session, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(session, ".session.lock")
		lock, err := Acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := lock.Close(); err != nil {
				t.Errorf("close lock: %v", err)
			}
		}()
		if err := os.Rename(session, filepath.Join(workspace, "old-session")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(session, 0o700); err != nil {
			t.Fatal(err)
		}
		const marker = "replacement parent must remain untouched"
		if err := os.WriteFile(filepath.Join(session, "marker"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lock.Verify(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Verify() after parent replacement = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(filepath.Join(session, "marker"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != marker {
			t.Fatalf("replacement parent mutated: got %q", got)
		}
	})
}

func TestLockAllowsOwningDirectoryDetachWhileHeld(t *testing.T) {
	workspace := t.TempDir()
	session := filepath.Join(workspace, "session")
	if err := os.Mkdir(session, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(context.Background(), filepath.Join(session, ".session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Verify(); err != nil {
		t.Fatalf("Verify() before detach: %v", err)
	}
	workspaceRoot, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer workspaceRoot.Close()
	if err := workspaceRoot.Rename("session", ".deleted-session"); err != nil {
		t.Fatalf("rename owning directory while lock held: %v", err)
	}
	if err := lock.Verify(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Verify() after detach = %v, want ErrUnsafePath", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".deleted-session", ".session.lock")); err != nil {
		t.Fatalf("detached lock file missing: %v", err)
	}
}

func TestAcquireContendsWithLegacyPathOpenedLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".session.lock")
	legacy, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := tryLockFile(legacy)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if !acquired {
		_ = legacy.Close()
		t.Fatal("legacy lock unexpectedly contended")
	}
	if _, err := Acquire(context.Background(), path); !errors.Is(err, ErrContended) {
		_ = unlockFile(legacy)
		_ = legacy.Close()
		t.Fatalf("Acquire() against legacy lock = %v, want ErrContended", err)
	}
	if _, err := AcquireExisting(context.Background(), path); !errors.Is(err, ErrContended) {
		_ = unlockFile(legacy)
		_ = legacy.Close()
		t.Fatalf("AcquireExisting() against legacy lock = %v, want ErrContended", err)
	}
	if err := unlockFile(legacy); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireExisting(context.Background(), path)
	if err != nil {
		t.Fatalf("AcquireExisting() after legacy release: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLockHelperProcess(t *testing.T) {
	path := os.Getenv(lockHelperPathEnv)
	if path == "" {
		return
	}
	lock, err := Acquire(context.Background(), path)
	if err != nil {
		fmt.Fprintf(os.Stdout, "error:%v\n", err)
		return
	}
	fmt.Fprintln(os.Stdout, "acquired")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = lock.Close()
}

func TestExclusiveLockContendsAcrossProcessesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".session.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestSessionLockHelperProcess$")
	command.Env = append(os.Environ(), lockHelperPathEnv+"="+path)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	select {
	case line := <-ready:
		if line != "acquired" {
			t.Fatalf("lock helper startup = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock helper did not acquire promptly")
	}
	if _, err := Acquire(context.Background(), path); !errors.Is(err, ErrContended) {
		t.Fatalf("second process lock = %v, want contention", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("lock remained contended after owner exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
