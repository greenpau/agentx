//go:build unix

package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundShellStopFreezesAndKillsProcessGroup(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	workspace := t.TempDir()
	manager, err := Open(filepath.Join(workspace, "state"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "sh -c 'echo $$ > descendant.pid; sleep 10 & echo $! > descendant-child.pid; wait; printf survived > descendant-survived' & wait",
		Dir:     workspace, Env: os.Environ(), Shell: "/bin/bash", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	live := liveForTest(t, manager, record.ID)
	if live.cmd.WaitDelay != processWaitDelay {
		t.Fatalf("owned process WaitDelay=%s, want %s", live.cmd.WaitDelay, processWaitDelay)
	}
	leaderPID := live.cmd.Process.Pid
	descendantPID := waitForTaskPID(t, filepath.Join(workspace, "descendant.pid"))
	childPID := waitForTaskPID(t, filepath.Join(workspace, "descendant-child.pid"))
	leaderGroup, _ := syscall.Getpgid(leaderPID)
	descendantGroup, _ := syscall.Getpgid(descendantPID)
	childGroup, _ := syscall.Getpgid(childPID)
	if leaderGroup != leaderPID || descendantGroup != leaderGroup || childGroup != leaderGroup {
		t.Fatalf("test process escaped owned group: leader=%d/%d descendant=%d/%d child=%d/%d", leaderPID, leaderGroup, descendantPID, descendantGroup, childPID, childGroup)
	}
	if err := manager.Stop(record.ID); err != nil {
		t.Fatal(err)
	}
	waitForTaskProcessExit(t, descendantPID)
	waitForTaskProcessExit(t, childPID)
	if _, err := os.Stat(filepath.Join(workspace, "descendant-survived")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant ran after task stop; marker stat error=%v", err)
	}
	terminal, err := manager.Get(record.ID)
	if err != nil || terminal.Status != StatusKilled {
		t.Fatalf("stopped task state=%#v err=%v", terminal, err)
	}
}

func TestWaitForProcessIsBounded(t *testing.T) {
	start := time.Now()
	err := waitForProcess(make(chan struct{}), 20*time.Millisecond)
	if !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("waitForProcess error=%v, want ErrStopTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("bounded process wait took %s", elapsed)
	}
}

func waitForTaskPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		content, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("process identifier was not recorded in %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForTaskProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived task stop", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
