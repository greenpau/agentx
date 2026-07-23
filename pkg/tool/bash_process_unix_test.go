//go:build unix

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestForegroundBashCancellationKillsDescendants(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	tests := []struct {
		name        string
		timeout     int
		cancel      bool
		wantCode    string
		resultLimit time.Duration
	}{
		{name: "caller cancellation", timeout: 10_000, cancel: true, wantCode: "cancelled", resultLimit: 10 * time.Second},
		{name: "tool timeout", timeout: 2_000, wantCode: "timeout", resultLimit: 15 * time.Second},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			descriptor := bashDescriptor(workspace, "/bin/bash", nil, []string{"LANG=C"})
			registry, err := NewRegistry(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
			if err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(bashInput{
				Command: "echo $$ > leader.pid; sh -c 'echo $$ > descendant.pid; sleep 30 & echo $! > descendant-child.pid; wait; printf survived > descendant-survived' & wait",
				Timeout: test.timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Result, 1)
			go func() {
				done <- executor.Execute(ctx, Request{ID: "descendant-" + test.name, Name: "Bash", Input: input})
			}()

			leaderPID := waitForRecordedPID(t, filepath.Join(workspace, "leader.pid"))
			descendantPID := waitForRecordedPID(t, filepath.Join(workspace, "descendant.pid"))
			childPID := waitForRecordedPID(t, filepath.Join(workspace, "descendant-child.pid"))
			leaderGroup, _ := syscall.Getpgid(leaderPID)
			descendantGroup, _ := syscall.Getpgid(descendantPID)
			childGroup, _ := syscall.Getpgid(childPID)
			if leaderGroup != leaderPID || descendantGroup != leaderGroup || childGroup != leaderGroup {
				t.Fatalf("test process escaped owned group: leader=%d/%d descendant=%d/%d child=%d/%d", leaderPID, leaderGroup, descendantPID, descendantGroup, childPID, childGroup)
			}
			if test.cancel {
				cancel()
			}
			select {
			case result := <-done:
				if !result.IsError || result.Code != test.wantCode {
					t.Fatalf("cancelled Bash result = %#v, want code %q", result, test.wantCode)
				}
			case <-time.After(test.resultLimit):
				t.Fatal("foreground Bash did not kill and reap after cancellation")
			}

			waitForProcessExit(t, descendantPID)
			waitForProcessExit(t, childPID)
			if _, err := os.Stat(filepath.Join(workspace, "descendant-survived")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("descendant survived cancellation; marker stat error=%v", err)
			}
		})
	}
}

func waitForRecordedPID(t *testing.T, path string) int {
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

func waitForProcessExit(t *testing.T, pid int) {
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
			t.Fatalf("descendant process %d survived cancellation", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
