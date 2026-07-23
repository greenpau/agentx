package task

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func launchLifecycleSleeper(t *testing.T, manager *Manager) Record {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("lifecycle shell fixture requires /bin/bash")
	}
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "exec sleep 30", Dir: t.TempDir(), Env: os.Environ(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func liveForTest(t *testing.T, manager *Manager, id ID) *liveTask {
	t.Helper()
	manager.mu.RLock()
	live := manager.live[id]
	manager.mu.RUnlock()
	if live == nil {
		t.Fatalf("task %s has no live process", id)
	}
	return live
}

func TestStopSignalsReapsAndJoinsPersistenceAndSignalFailures(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	record := launchLifecycleSleeper(t, manager)
	live := liveForTest(t, manager, record.ID)

	persistFailure := errors.New("injected terminal persistence failure")
	signalFailure := errors.New("injected signal observation failure")
	originalSignal := live.signal
	live.signal = func() error {
		return errors.Join(originalSignal(), signalFailure)
	}
	manager.mu.Lock()
	manager.persistHook = func() error { return persistFailure }
	manager.mu.Unlock()

	err = manager.Stop(record.ID)
	if !errors.Is(err, errTaskPersistenceCallbackFailed) ||
		!errors.Is(err, errTaskSignalCallbackFailed) ||
		errors.Is(err, persistFailure) || errors.Is(err, signalFailure) {
		t.Fatalf("Stop error did not seal callback failures: %v", err)
	}
	select {
	case <-live.done:
	default:
		t.Fatal("Stop returned before process waiter completed")
	}
	if live.cmd.ProcessState == nil {
		t.Fatal("Stop returned without reaping the process")
	}
	current, getErr := manager.Get(record.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if current.Status != StatusKilled || current.EndedAt == nil {
		t.Fatalf("terminal state = %#v", current)
	}
	manager.mu.RLock()
	_, stillLive := manager.live[record.ID]
	manager.mu.RUnlock()
	if stillLive {
		t.Fatal("reaped task remained in live registry")
	}
	if err := manager.Stop(record.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("idempotent second Stop = %v", err)
	}
	manager.mu.Lock()
	manager.persistHook = nil
	manager.mu.Unlock()
	if err := manager.Close(); err != nil {
		t.Fatalf("Close did not flush dirty terminal state: %v", err)
	}
	restored, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredRecord, err := restored.Get(record.ID)
	if err != nil || restoredRecord.Status != StatusKilled {
		t.Fatalf("terminal state was stranded after persistence recovery: %#v, %v", restoredRecord, err)
	}
}

func TestStopDoesNotPublishKilledBeforeTermination(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record := launchLifecycleSleeper(t, manager)
	live := liveForTest(t, manager, record.ID)
	originalSignal := live.signal
	entered := make(chan struct{})
	release := make(chan struct{})
	live.signal = func() error {
		close(entered)
		<-release
		return originalSignal()
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- manager.Stop(record.ID) }()
	<-entered

	whileBlocked, err := manager.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if whileBlocked.Status != StatusRunning || whileBlocked.EndedAt != nil {
		t.Fatalf("Stop fabricated termination before signal/Wait: %#v", whileBlocked)
	}
	close(release)
	if err := <-stopResult; err != nil {
		t.Fatal(err)
	}
	after, err := manager.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusKilled || live.cmd.ProcessState == nil {
		t.Fatalf("confirmed terminal state = %#v process=%#v", after, live.cmd.ProcessState)
	}
}

func TestConcurrentStopHasOneClaimAndNoResurrection(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record := launchLifecycleSleeper(t, manager)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- manager.Stop(record.ID)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, notRunning int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrNotRunning):
			notRunning++
		default:
			t.Fatalf("unexpected concurrent Stop error: %v", err)
		}
	}
	if successes != 1 || notRunning != 1 {
		t.Fatalf("concurrent Stop outcomes: success=%d not-running=%d", successes, notRunning)
	}
	current, err := manager.Get(record.ID)
	if err != nil || current.Status != StatusKilled {
		t.Fatalf("terminal task resurrected: %#v, %v", current, err)
	}
}

func TestPostStartPersistenceFailureAlwaysReapsAndRetriesCleanupPersistence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lifecycle shell fixture requires /bin/bash")
	}
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	var calls atomic.Int32
	runningPersistFailure := errors.New("injected running-state persistence failure")
	cleanupPersistFailure := errors.New("injected cleanup persistence failure")
	var captured *liveTask
	manager.persistHook = func() error {
		switch calls.Add(1) {
		case 2:
			for _, live := range manager.live {
				captured = live
			}
			return runningPersistFailure
		case 3:
			return cleanupPersistFailure
		default:
			return nil
		}
	}
	_, err = manager.LaunchShell(context.Background(), ShellSpec{
		Command: "exec sleep 30", Dir: t.TempDir(), Env: os.Environ(), Shell: "/bin/bash",
	})
	if !errors.Is(err, errTaskPersistenceCallbackFailed) ||
		errors.Is(err, runningPersistFailure) || errors.Is(err, cleanupPersistFailure) {
		t.Fatalf("launch cleanup did not recover from a transient terminal persistence failure: %v", err)
	}
	if captured == nil || captured.cmd.ProcessState == nil {
		t.Fatal("post-Start persistence failure did not reap child")
	}
	select {
	case <-captured.done:
	default:
		t.Fatal("post-Start cleanup did not publish completion")
	}
	manager.mu.RLock()
	liveCount := len(manager.live)
	manager.mu.RUnlock()
	if liveCount != 0 {
		t.Fatalf("post-Start cleanup left %d live tasks", liveCount)
	}
	tasks := manager.List()
	if len(tasks) != 1 || tasks[0].Status != StatusFailed || tasks[0].Status == StatusKilled {
		t.Fatalf("failed launch claimed wrong terminal state: %#v", tasks)
	}
	if calls.Load() < 4 {
		t.Fatalf("terminal persistence was not retried, hook calls=%d", calls.Load())
	}
}

func TestConcurrentCloseReturnsSameJoinedTerminalFailure(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = launchLifecycleSleeper(t, manager)
	persistFailure := errors.New("injected close persistence failure")
	manager.mu.Lock()
	manager.persistHook = func() error { return persistFailure }
	manager.mu.Unlock()
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- manager.Close()
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if !errors.Is(first, errTaskPersistenceCallbackFailed) ||
		!errors.Is(second, errTaskPersistenceCallbackFailed) ||
		errors.Is(first, persistFailure) || errors.Is(second, persistFailure) {
		t.Fatalf("concurrent Close results differ: first=%v second=%v", first, second)
	}
	select {
	case <-manager.closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close completion latch did not settle")
	}
}

func TestTerminalValidatorPanicDoesNotStrandAsyncShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lifecycle shell fixture requires /bin/bash")
	}
	const panicPayload = "validator-panic-payload-must-not-escape"
	var terminalValidations atomic.Int32
	manager, err := Open(t.TempDir(), Options{
		ValidateState: func(encoded []byte) error {
			if bytes.Contains(encoded, []byte(`"status": "completed"`)) {
				terminalValidations.Add(1)
				panic(panicPayload)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseDir := t.TempDir()
	releasePath := releaseDir + "/release-shell"
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command:   `while [ ! -f "$TMPDIR/release-shell" ]; do sleep 0.01; done`,
		Dir:       t.TempDir(),
		Env:       []string{"PATH=/usr/bin:/bin", "TMPDIR=" + releaseDir},
		Shell:     "/bin/bash",
		ToolUseID: "tool-validator-panic",
		Owner:     "agent-validator-panic",
	})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	live := liveForTest(t, manager, record.ID)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	select {
	case <-live.done:
	case <-time.After(5 * time.Second):
		_ = manager.Close()
		t.Fatal("validator panic stranded shell completion")
	}
	if live.cmd.ProcessState == nil {
		_ = manager.Close()
		t.Fatal("validator panic prevented child reaping")
	}
	live.fileMu.Lock()
	output := live.output
	live.fileMu.Unlock()
	if output != nil {
		_ = manager.Close()
		t.Fatal("validator panic left task output open")
	}
	manager.mu.RLock()
	_, stillLive := manager.live[record.ID]
	manager.mu.RUnlock()
	if stillLive {
		_ = manager.Close()
		t.Fatal("validator panic left task in live registry")
	}
	if terminalValidations.Load() < 2 {
		_ = manager.Close()
		t.Fatalf("terminal validator calls = %d, want full and minimal preflights", terminalValidations.Load())
	}
	if live.terminalErr == nil || strings.Contains(live.terminalErr.Error(), panicPayload) {
		_ = manager.Close()
		t.Fatalf("terminal validator panic error = %v", live.terminalErr)
	}
	current, err := manager.Get(record.ID)
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	if current.Status != StatusRunning || current.ID != record.ID ||
		current.ToolUseID != record.ToolUseID || current.Owner != record.Owner {
		_ = manager.Close()
		t.Fatalf("validator failure lost stable task correlation: %#v", current)
	}
	if _, err := manager.CreateWork("manager remains usable", "after validator panic", "working", nil); err != nil {
		_ = manager.Close()
		t.Fatalf("validator panic stranded manager lock: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}
