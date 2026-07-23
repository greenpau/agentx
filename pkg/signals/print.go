package signals

import (
	"context"
	"os"
	ossignal "os/signal"
	"sync"
)

// WithPrintInterrupt gives a raw-print surface semantic ownership of SIGINT.
// The first globally recognized signal cancels graceful work; any later signal
// or the shared process failsafe requests immediate exit using the winning
// request's code. Exactly one print monitor may own a shutdown state.
func WithPrintInterrupt(parent context.Context) (context.Context, func(), error) {
	if parent == nil {
		parent = context.Background()
	}
	state := ProcessShutdownFromContext(parent)
	if state == nil {
		state = NewProcessShutdown(os.Exit, DefaultFailsafe)
		parent = WithProcessShutdown(parent, state)
	}
	ctx, cancel := context.WithCancel(parent)
	if err := state.acquire(printMonitorOwner, InterruptOwnedByPrint); err != nil {
		cancel()
		return parent, nil, err
	}
	state.setPrintCancel(cancel)

	// A process monitor acquires OS delivery before application startup and
	// forwards SIGINT to this semantic owner. A standalone print adapter owns
	// os/signal directly because no process monitor exists.
	if state.processMonitorActive() {
		var once sync.Once
		return ctx, func() {
			once.Do(func() {
				state.clearPrintCancel()
				cancel()
				state.release(printMonitorOwner)
			})
		}, nil
	}

	interrupts := make(chan os.Signal, 2)
	ossignal.Notify(interrupts, os.Interrupt)
	completed := make(chan struct{})
	done := monitorPrintInterrupts(interrupts, completed, cancel, state)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			close(completed)
			ossignal.Stop(interrupts)
			cancel()
			<-done
			state.clearPrintCancel()
			state.release(printMonitorOwner)
		})
	}, nil
}

func monitorPrintInterrupts(input <-chan os.Signal, completed <-chan struct{}, cancel context.CancelFunc, state *ProcessShutdown) <-chan struct{} {
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
				if value != os.Interrupt {
					continue
				}
				switch state.handleSignal(0, "sigint") {
				case signalFirst:
					if cancel != nil {
						cancel()
					}
				case signalSubsequent:
					return
				}
			}
		}
	}()
	return done
}
