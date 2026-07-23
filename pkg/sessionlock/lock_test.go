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
