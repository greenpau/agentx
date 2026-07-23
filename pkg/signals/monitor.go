package signals

import (
	"context"
	"os"
	ossignal "os/signal"
	"sync"
)

// StartProcessMonitor acquires the platform signal set and returns an
// idempotent stop function that disarms notification, joins the monitor, and
// releases process-signal ownership. This monitor always acquires OS delivery
// early; ownership selects whether SIGINT semantically cancels the process
// context or the registered print context. Exactly one process monitor may own
// a state, and it must start before a composed print monitor and stop after it.
// Its stop function returns ErrMonitorOrder without disarming OS delivery when
// a print owner is still active; callers may stop the print owner and retry.
// cancel must be a nonblocking, non-reentrant context cancellation function
// and must not call the returned stop function.
func StartProcessMonitor(cancel context.CancelFunc, state *ProcessShutdown, ownership InterruptOwnership) (func() error, error) {
	if state == nil {
		return nil, ErrNilShutdownState
	}
	if err := state.acquire(processMonitorOwner, ownership); err != nil {
		return nil, err
	}

	watched := platformSignals()
	if len(watched) == 0 {
		var stopMu sync.Mutex
		stopped := false
		return func() error {
			stopMu.Lock()
			defer stopMu.Unlock()
			if stopped {
				return nil
			}
			ready, err := state.beginProcessStop()
			if err != nil {
				return err
			}
			if ready {
				state.release(processMonitorOwner)
			}
			stopped = true
			return nil
		}, nil
	}

	input := make(chan os.Signal, 2)
	ossignal.Notify(input, watched...)
	completed := make(chan struct{})
	done := monitorSignals(input, completed, cancel, state, 0)
	var stopMu sync.Mutex
	stopped := false
	return func() error {
		stopMu.Lock()
		defer stopMu.Unlock()
		if stopped {
			return nil
		}
		ready, err := state.beginProcessStop()
		if err != nil {
			return err
		}
		if !ready {
			stopped = true
			return nil
		}
		// Completion becomes observable before notification is disarmed, so a
		// queued or concurrently delivered signal cannot start late shutdown.
		close(completed)
		ossignal.Stop(input)
		<-done
		state.release(processMonitorOwner)
		stopped = true
		return nil
	}, nil
}

// monitorSignals maps platform events and delegates first/second ownership to
// the shared state. interruptCode lets interactive root SIGINT request the
// graceful surface exit code without changing TERM/HUP conventions.
func monitorSignals(
	input <-chan os.Signal,
	completed <-chan struct{},
	cancel context.CancelFunc,
	state *ProcessShutdown,
	interruptCode int,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-completed:
				return
			case value, ok := <-input:
				if !ok {
					return
				}
				select {
				case <-completed:
					return
				default:
				}
				code, reason, recognized := signalDisposition(value)
				if !recognized {
					continue
				}
				isInterrupt := reason == "sigint" || reason == "interrupt"
				if isInterrupt {
					code = interruptCode
				}
				action, selectedCancel := state.handleProcessSignal(code, reason, isInterrupt, cancel)
				switch action {
				case signalFirst:
					if selectedCancel != nil {
						selectedCancel()
					}
				case signalSubsequent:
					return
				}
			}
		}
	}()
	return done
}
