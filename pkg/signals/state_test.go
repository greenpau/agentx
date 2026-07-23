package signals

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProcessShutdownIsFirstRequestWinsAndContextCarried(t *testing.T) {
	state := NewProcessShutdown(nil, 0)
	ctx := WithProcessShutdown(context.Background(), state)
	if ProcessShutdownFromContext(ctx) != state {
		t.Fatal("shutdown state was not carried through context")
	}
	if action := state.handleSignal(143, "sigterm"); action != signalFirst {
		t.Fatalf("first shutdown action = %v", action)
	}
	if action := state.handleSignal(0, "later"); action != signalSubsequent {
		t.Fatalf("later shutdown action = %v", action)
	}
	request, ok := state.Snapshot()
	if !ok || request.ExitCode != 143 || request.Reason != "sigterm" {
		t.Fatalf("snapshot = %+v, %v", request, ok)
	}
	if got := state.ExitCode(1); got != 143 {
		t.Fatalf("winning exit code = %d", got)
	}
	if state.failsafe != DefaultFailsafe {
		t.Fatalf("normalized failsafe = %s", state.failsafe)
	}
}

func TestProcessShutdownFallbackAndNilContext(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	if got := state.ExitCode(17); got != 17 {
		t.Fatalf("fallback exit code = %d", got)
	}
	if ProcessShutdownFromContext(nil) != nil {
		t.Fatal("nil context unexpectedly carried shutdown state")
	}
	if got := (*ProcessShutdown)(nil).ExitCode(19); got != 19 {
		t.Fatalf("nil state fallback exit code = %d", got)
	}
}

func TestProcessShutdownEnforcesOneOwnerPerSignalClass(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); !errors.Is(err, ErrMonitorActive) {
		t.Fatalf("duplicate process owner error = %v", err)
	}
	if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatalf("independent print owner: %v", err)
	}
	state.release(printMonitorOwner)
	if state.completed {
		t.Fatal("releasing one of two owners completed the coordinator")
	}
	state.release(processMonitorOwner)
	if !state.completed {
		t.Fatal("releasing the final owner did not complete the coordinator")
	}
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); !errors.Is(err, ErrShutdownComplete) {
		t.Fatalf("post-completion acquisition error = %v", err)
	}
}

func TestProcessShutdownRejectsConflictingInterruptOwners(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
		t.Fatal(err)
	}
	if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); !errors.Is(err, ErrInterruptOwnership) {
		t.Fatalf("conflicting interrupt owner error = %v", err)
	}
	state.release(processMonitorOwner)
}

func TestProcessShutdownConcurrentSignalsForceWinningCodeExactlyOnce(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		forced := make(chan int, 2)
		state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
		start := make(chan struct{})
		actions := make(chan signalAction, 2)
		var wg sync.WaitGroup
		for _, request := range []struct {
			code   int
			reason string
		}{{0, "sigint"}, {143, "sigterm"}} {
			wg.Add(1)
			go func(code int, reason string) {
				defer wg.Done()
				<-start
				actions <- state.handleSignal(code, reason)
			}(request.code, request.reason)
		}
		close(start)
		wg.Wait()
		close(actions)
		first, subsequent := 0, 0
		for action := range actions {
			switch action {
			case signalFirst:
				first++
			case signalSubsequent:
				subsequent++
			}
		}
		if first != 1 || subsequent != 1 {
			t.Fatalf("iteration %d actions: first=%d subsequent=%d", iteration, first, subsequent)
		}
		request, ok := state.Snapshot()
		if !ok {
			t.Fatalf("iteration %d has no winning request", iteration)
		}
		select {
		case code := <-forced:
			if code != request.ExitCode {
				t.Fatalf("iteration %d forced %d, winner %d", iteration, code, request.ExitCode)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d did not force", iteration)
		}
		select {
		case code := <-forced:
			t.Fatalf("iteration %d forced more than once with %d", iteration, code)
		default:
		}
	}
}

func TestSecondSignalReservesForceBeforeConcurrentProcessStop(t *testing.T) {
	reserved := make(chan struct{})
	allowInvoke := make(chan struct{})
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	state.beforeForceInvoke = func() {
		close(reserved)
		<-allowInvoke
	}
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
		t.Fatal(err)
	}
	if action := state.handleSignal(143, "sigterm"); action != signalFirst {
		t.Fatalf("first action = %v", action)
	}
	secondAction := make(chan signalAction, 1)
	go func() {
		action, _ := state.handleProcessSignal(0, "sigint", true, func() {})
		secondAction <- action
	}()
	select {
	case <-reserved:
	case <-time.After(time.Second):
		t.Fatal("second signal did not reserve force")
	}
	stopResult := make(chan error, 1)
	go func() {
		ready, err := state.beginProcessStop()
		if err == nil && ready {
			state.release(processMonitorOwner)
		}
		stopResult <- err
	}()
	select {
	case err := <-stopResult:
		t.Fatalf("process stop returned before reserved force: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowInvoke)
	select {
	case action := <-secondAction:
		if action != signalSubsequent {
			t.Fatalf("second action = %v", action)
		}
	case <-time.After(time.Second):
		t.Fatal("second signal did not finish")
	}
	select {
	case code := <-forced:
		if code != 143 {
			t.Fatalf("reserved force used code %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("reserved force callback did not run")
	}
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("process stop after force: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("process stop did not join reserved force")
	}
}

func TestProcessStopLinearizedBeforeSecondSignalSuppressesForce(t *testing.T) {
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
		t.Fatal(err)
	}
	if action := state.handleSignal(143, "sigterm"); action != signalFirst {
		t.Fatalf("first action = %v", action)
	}
	ready, err := state.beginProcessStop()
	if err != nil || !ready {
		t.Fatalf("begin process stop = %v, %v", ready, err)
	}
	action, _ := state.handleProcessSignal(0, "sigint", true, func() {})
	if action != signalIgnored {
		t.Fatalf("post-stop signal action = %v", action)
	}
	state.release(processMonitorOwner)
	select {
	case code := <-forced:
		t.Fatalf("post-stop signal forced with %d", code)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestFinalOwnerReleaseJoinsInFlightForce(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	state := NewProcessShutdown(func(int) {
		close(started)
		<-unblock
	}, 5*time.Millisecond)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
		t.Fatal(err)
	}
	if action := state.handleSignal(143, "sigterm"); action != signalFirst {
		t.Fatalf("first action = %v", action)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("failsafe callback did not begin")
	}
	released := make(chan struct{})
	go func() {
		state.release(processMonitorOwner)
		close(released)
	}()
	select {
	case <-released:
		t.Fatal("final release returned before the force callback")
	case <-time.After(20 * time.Millisecond):
	}
	close(unblock)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("final release did not join the returned force callback")
	}
}

func TestFinalOwnerReleaseDisarmsFailsafe(t *testing.T) {
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, 20*time.Millisecond)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
		t.Fatal(err)
	}
	state.handleSignal(143, "sigterm")
	state.release(processMonitorOwner)
	select {
	case code := <-forced:
		t.Fatalf("completed coordinator forced with %d", code)
	case <-time.After(50 * time.Millisecond):
	}
}
