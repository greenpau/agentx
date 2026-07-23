package signals

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
)

func TestWithPrintInterruptOwnsStateAndStopsIdempotently(t *testing.T) {
	ctx, stop, err := WithPrintInterrupt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := ProcessShutdownFromContext(ctx)
	if state == nil {
		t.Fatal("print interrupt context did not carry shutdown state")
	}
	if _, _, err := WithPrintInterrupt(ctx); !errors.Is(err, ErrMonitorActive) {
		t.Fatalf("duplicate print owner error = %v", err)
	}
	if _, err := StartProcessMonitor(func() {}, state, InterruptOwnedByPrint); !errors.Is(err, ErrMonitorOrder) {
		t.Fatalf("late process-monitor error = %v", err)
	}
	stop()
	stop()
	if !contextCanceled(ctx) {
		t.Fatal("stopping print interrupt ownership did not cancel child context")
	}
}

func TestWithPrintInterruptRejectsProcessOwnedSIGINT(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	stop, err := StartProcessMonitor(func() {}, state, InterruptOwnedByProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop() }()
	ctx := WithProcessShutdown(context.Background(), state)
	if _, _, err := WithPrintInterrupt(ctx); !errors.Is(err, ErrInterruptOwnership) {
		t.Fatalf("process-owned SIGINT conflict = %v", err)
	}
}

func TestProcessStopRejectsActivePrintAndSucceedsAfterPrintStop(t *testing.T) {
	state := NewProcessShutdown(nil, time.Second)
	stopProcess, err := StartProcessMonitor(func() {}, state, InterruptOwnedByPrint)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProcessShutdown(context.Background(), state)
	_, stopPrint, err := WithPrintInterrupt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stopProcess(); !errors.Is(err, ErrMonitorOrder) {
		t.Fatalf("early process stop error = %v", err)
	}
	if !state.processMonitorActive() {
		t.Fatal("rejected process stop detached OS signal ownership")
	}
	stopPrint()
	if err := stopProcess(); err != nil {
		t.Fatalf("process stop after print stop: %v", err)
	}
	if err := stopProcess(); err != nil {
		t.Fatalf("idempotent process stop: %v", err)
	}
	if !state.completed {
		t.Fatal("ordered monitor stops did not complete the coordinator")
	}
}

func contextCanceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func TestPrintInterruptMonitorUsesGracefulZeroThenForces(t *testing.T) {
	input := make(chan os.Signal, 2)
	completed := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	forced := make(chan int, 1)
	state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
	done := monitorPrintInterrupts(input, completed, func() { cancelled <- struct{}{} }, state)
	input <- os.Interrupt
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("first print interrupt did not cancel graceful work")
	}
	request, ok := state.Snapshot()
	if !ok || request.ExitCode != 0 || request.Reason != "sigint" {
		t.Fatalf("print shutdown request = %+v, %v", request, ok)
	}
	input <- os.Interrupt
	select {
	case code := <-forced:
		if code != 0 {
			t.Fatalf("forced print exit code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second print interrupt did not force shutdown")
	}
	<-done
}

func TestPrintAndProcessMonitorsShareOneGlobalWinner(t *testing.T) {
	for _, test := range []struct {
		name         string
		processFirst bool
		wantCode     int
	}{
		{name: "process first", processFirst: true, wantCode: 143},
		{name: "print first", processFirst: false, wantCode: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			processInput := make(chan os.Signal, 1)
			printInput := make(chan os.Signal, 1)
			processCompleted := make(chan struct{})
			printCompleted := make(chan struct{})
			processCancelled := make(chan struct{}, 1)
			printCancelled := make(chan struct{}, 1)
			forced := make(chan int, 1)
			state := NewProcessShutdown(func(code int) { forced <- code }, time.Second)
			processDone := monitorSignals(processInput, processCompleted, func() { processCancelled <- struct{}{} }, state, 143)
			printDone := monitorPrintInterrupts(printInput, printCompleted, func() { printCancelled <- struct{}{} }, state)

			if test.processFirst {
				processInput <- os.Interrupt
				awaitSignal(t, processCancelled, "process cancellation")
				printInput <- os.Interrupt
				awaitDone(t, printDone, "print monitor")
				close(processCompleted)
				awaitDone(t, processDone, "process monitor")
			} else {
				printInput <- os.Interrupt
				awaitSignal(t, printCancelled, "print cancellation")
				processInput <- os.Interrupt
				awaitDone(t, processDone, "process monitor")
				close(printCompleted)
				awaitDone(t, printDone, "print monitor")
			}
			select {
			case code := <-forced:
				if code != test.wantCode {
					t.Fatalf("forced code = %d, want %d", code, test.wantCode)
				}
			case <-time.After(time.Second):
				t.Fatal("globally second signal did not force")
			}
			request, ok := state.Snapshot()
			if !ok || request.ExitCode != test.wantCode {
				t.Fatalf("winning request = %+v, %v", request, ok)
			}
		})
	}
}

func TestPrintInterruptMonitorFailsafeAndCompletion(t *testing.T) {
	t.Run("failsafe", func(t *testing.T) {
		input := make(chan os.Signal, 1)
		completed := make(chan struct{})
		forced := make(chan int, 1)
		state := NewProcessShutdown(func(code int) { forced <- code }, 20*time.Millisecond)
		done := monitorPrintInterrupts(input, completed, func() {}, state)
		input <- os.Interrupt
		select {
		case code := <-forced:
			if code != 0 {
				t.Fatalf("failsafe exit code = %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("print shutdown failsafe did not fire")
		}
		close(completed)
		<-done
	})

	t.Run("completion disarms", func(t *testing.T) {
		input := make(chan os.Signal, 1)
		completed := make(chan struct{})
		cancelled := make(chan struct{}, 1)
		forced := make(chan int, 1)
		state := NewProcessShutdown(func(code int) { forced <- code }, 20*time.Millisecond)
		if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
			t.Fatal(err)
		}
		done := monitorPrintInterrupts(input, completed, func() { cancelled <- struct{}{} }, state)
		input <- os.Interrupt
		awaitSignal(t, cancelled, "print cancellation")
		close(completed)
		<-done
		state.release(printMonitorOwner)
		select {
		case code := <-forced:
			t.Fatalf("completed print shutdown forced with %d", code)
		case <-time.After(40 * time.Millisecond):
		}
	})
}

func awaitRequest(state *ProcessShutdown, timeout time.Duration) (platform.ShutdownRequest, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if request, ok := state.Snapshot(); ok {
			return request, true
		}
		select {
		case <-deadline.C:
			return platform.ShutdownRequest{}, false
		case <-ticker.C:
		}
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitDone(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
