package signals

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestSignalMonitorLatchesFirstAndForcesOnSecond(t *testing.T) {
	input := make(chan os.Signal, 2)
	completed := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	done := monitorSignals(input, completed, cancel, state, 130)
	_, expectedReason, recognized := signalDisposition(os.Interrupt)
	if !recognized {
		t.Fatal("platform interrupt is not recognized")
	}
	input <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first signal did not cancel the application")
	}
	request, ok := state.Snapshot()
	if !ok || request.ExitCode != 130 || request.Reason != expectedReason {
		t.Fatalf("latched request = %+v, %v", request, ok)
	}
	input <- os.Interrupt
	select {
	case code := <-forced:
		if code != 130 {
			t.Fatalf("second signal replaced first code with %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second signal did not force shutdown")
	}
	<-done
}

func TestSignalMonitorFailsafeAndCompletedProcess(t *testing.T) {
	t.Run("failsafe", func(t *testing.T) {
		input := make(chan os.Signal, 1)
		completed := make(chan struct{})
		forced := make(chan int, 1)
		state := NewProcessShutdown(func(code int) { forced <- code }, 20*time.Millisecond)
		done := monitorSignals(input, completed, func() {}, state, 130)
		input <- os.Interrupt
		select {
		case code := <-forced:
			if code != 130 {
				t.Fatalf("failsafe code = %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown failsafe did not fire")
		}
		close(completed)
		<-done
	})

	t.Run("already completed", func(t *testing.T) {
		input := make(chan os.Signal, 1)
		completed := make(chan struct{})
		close(completed)
		forced := make(chan int, 1)
		state := NewProcessShutdown(func(code int) { forced <- code }, 10*time.Millisecond)
		done := monitorSignals(input, completed, func() { t.Error("completed process was cancelled") }, state, 130)
		<-done
		select {
		case code := <-forced:
			t.Fatalf("completed process forced with code %d", code)
		default:
		}
	})

	t.Run("completion plus release disarms active failsafe", func(t *testing.T) {
		input := make(chan os.Signal, 1)
		completed := make(chan struct{})
		cancelled := make(chan struct{}, 1)
		forced := make(chan int, 1)
		state := NewProcessShutdown(func(code int) { forced <- code }, 20*time.Millisecond)
		if err := state.acquire(processMonitorOwner, InterruptOwnedByProcess); err != nil {
			t.Fatal(err)
		}
		done := monitorSignals(input, completed, func() { cancelled <- struct{}{} }, state, 130)
		input <- os.Interrupt
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("first signal did not begin graceful shutdown")
		}
		close(completed)
		<-done
		state.release(processMonitorOwner)
		select {
		case code := <-forced:
			t.Fatalf("completed process forced with code %d", code)
		case <-time.After(40 * time.Millisecond):
		}
	})
}

func TestSignalMonitorAllowsInteractiveZeroExitCode(t *testing.T) {
	input := make(chan os.Signal, 2)
	completed := make(chan struct{})
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	done := monitorSignals(input, completed, func() {}, state, 0)
	input <- os.Interrupt
	request, ok := awaitRequest(state, time.Second)
	if !ok || request.ExitCode != 0 {
		t.Fatalf("interactive SIGINT request = %+v, %v", request, ok)
	}
	input <- os.Interrupt
	if code := <-forced; code != 0 {
		t.Fatalf("second signal replaced interactive code with %d", code)
	}
	<-done
}

func TestStartProcessMonitorRejectsMissingDuplicateAndCompletedOwnership(t *testing.T) {
	if _, err := StartProcessMonitor(func() {}, nil, InterruptOwnedByPrint); !errors.Is(err, ErrNilShutdownState) {
		t.Fatalf("nil-state error = %v", err)
	}
	state := NewProcessShutdown(nil, time.Second)
	if _, err := StartProcessMonitor(func() {}, state, InterruptOwnership(99)); !errors.Is(err, ErrInterruptOwnership) {
		t.Fatalf("invalid-ownership error = %v", err)
	}
	stop, err := StartProcessMonitor(func() {}, state, InterruptOwnedByPrint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartProcessMonitor(func() {}, state, InterruptOwnedByPrint); !errors.Is(err, ErrMonitorActive) {
		t.Fatalf("duplicate-owner error = %v", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := StartProcessMonitor(func() {}, state, InterruptOwnedByPrint); !errors.Is(err, ErrShutdownComplete) {
		t.Fatalf("completed-state error = %v", err)
	}
}

func TestPrintOwnedSIGINTIsIgnoredUntilPrintRegistration(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	rootCancelled := make(chan struct{}, 1)
	action, selectedCancel := state.handleProcessSignal(0, "sigint", true, func() { rootCancelled <- struct{}{} })
	if action != signalIgnored || selectedCancel != nil {
		t.Fatalf("pre-registration SIGINT = action %v, cancel %v", action, selectedCancel != nil)
	}
	if _, ok := state.Snapshot(); ok {
		t.Fatal("pre-registration SIGINT latched a shutdown request")
	}
	select {
	case <-rootCancelled:
		t.Fatal("pre-registration print SIGINT cancelled the root")
	default:
	}

	printCtx, cancelPrint := context.WithCancel(context.Background())
	if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	state.setPrintCancel(cancelPrint)
	action, selectedCancel = state.handleProcessSignal(0, "sigint", true, func() { rootCancelled <- struct{}{} })
	if action != signalFirst || selectedCancel == nil {
		t.Fatalf("registered print SIGINT = action %v, cancel %v", action, selectedCancel != nil)
	}
	selectedCancel()
	if !contextCanceled(printCtx) {
		t.Fatal("registered print SIGINT did not cancel the print context")
	}
	request, ok := state.Snapshot()
	if !ok || request.ExitCode != 0 || request.Reason != "sigint" {
		t.Fatalf("registered print request = %+v, %v", request, ok)
	}
	state.clearPrintCancel()
	state.release(printMonitorOwner)
	state.release(processMonitorOwner)
}

func TestPrintOwnedTERMUsesRootWithoutPrintRegistration(t *testing.T) {
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	rootCancelled := make(chan struct{}, 1)
	action, selectedCancel := state.handleProcessSignal(143, "sigterm", false, func() { rootCancelled <- struct{}{} })
	if action != signalFirst || selectedCancel == nil {
		t.Fatalf("TERM routing = action %v, cancel %v", action, selectedCancel != nil)
	}
	selectedCancel()
	awaitSignal(t, rootCancelled, "TERM root cancellation")
	request, ok := state.Snapshot()
	if !ok || request.ExitCode != 143 || request.Reason != "sigterm" {
		t.Fatalf("TERM request = %+v, %v", request, ok)
	}
	action, selectedCancel = state.handleProcessSignal(0, "sigint", true, func() { rootCancelled <- struct{}{} })
	if action != signalSubsequent || selectedCancel != nil {
		t.Fatalf("post-TERM print SIGINT = action %v, cancel %v", action, selectedCancel != nil)
	}
	select {
	case code := <-forced:
		if code != 143 {
			t.Fatalf("post-TERM print SIGINT forced %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("post-TERM print SIGINT did not force the winning request")
	}
	state.release(processMonitorOwner)
}

func TestPrintOwnedSIGINTAfterHandlerTeardownForcesWinningRequest(t *testing.T) {
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
		t.Fatal(err)
	}
	_, cancelPrint := context.WithCancel(context.Background())
	state.setPrintCancel(cancelPrint)
	action, selectedCancel := state.handleProcessSignal(0, "sigint", true, func() {})
	if action != signalFirst || selectedCancel == nil {
		t.Fatalf("first print SIGINT = action %v, cancel %v", action, selectedCancel != nil)
	}
	selectedCancel()
	state.clearPrintCancel()
	state.release(printMonitorOwner)
	action, selectedCancel = state.handleProcessSignal(0, "sigint", true, func() {})
	if action != signalSubsequent || selectedCancel != nil {
		t.Fatalf("post-teardown print SIGINT = action %v, cancel %v", action, selectedCancel != nil)
	}
	select {
	case code := <-forced:
		if code != 0 {
			t.Fatalf("post-teardown print SIGINT forced %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("post-teardown print SIGINT did not force")
	}
	state.release(processMonitorOwner)
}

func TestPrintRegistrationAndSIGINTHandoffIsAtomic(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		state := NewProcessShutdown(nil, time.Second)
		if err := state.acquire(processMonitorOwner, InterruptOwnedByPrint); err != nil {
			t.Fatal(err)
		}
		if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
			t.Fatal(err)
		}
		printCtx, cancelPrint := context.WithCancel(context.Background())
		rootCancelled := make(chan struct{}, 1)
		start := make(chan struct{})
		registered := make(chan struct{})
		type outcome struct {
			action signalAction
			cancel context.CancelFunc
		}
		result := make(chan outcome, 1)
		go func() {
			<-start
			state.setPrintCancel(cancelPrint)
			close(registered)
		}()
		go func() {
			<-start
			action, selected := state.handleProcessSignal(0, "sigint", true, func() { rootCancelled <- struct{}{} })
			result <- outcome{action: action, cancel: selected}
		}()
		close(start)
		got := <-result
		<-registered
		switch got.action {
		case signalIgnored:
			if got.cancel != nil {
				t.Fatalf("iteration %d ignored signal selected a cancel", iteration)
			}
			if _, ok := state.Snapshot(); ok {
				t.Fatalf("iteration %d ignored signal latched", iteration)
			}
		case signalFirst:
			if got.cancel == nil {
				t.Fatalf("iteration %d accepted signal omitted cancel", iteration)
			}
			got.cancel()
			if !contextCanceled(printCtx) {
				t.Fatalf("iteration %d accepted signal missed print context", iteration)
			}
		default:
			t.Fatalf("iteration %d unexpected action %v", iteration, got.action)
		}
		select {
		case <-rootCancelled:
			t.Fatalf("iteration %d routed print SIGINT to root", iteration)
		default:
		}
		cancelPrint()
		state.clearPrintCancel()
		state.release(printMonitorOwner)
		state.release(processMonitorOwner)
	}
}
