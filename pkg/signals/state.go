// Package signals owns process signal acquisition, first-request shutdown
// state, surface-specific interrupt routing, and bounded force-exit behavior.
// An application must use one ProcessShutdown for every monitor in a process;
// separate coordinators would receive broadcast OS events independently.
// Generic ordered cleanup remains in package platform.
package signals

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
)

// DefaultFailsafe is the outer process deadline used when a signal-owned
// graceful shutdown does not complete.
const DefaultFailsafe = 6 * time.Second

var (
	// ErrNilShutdownState reports a monitor started without an observable,
	// process-owned shutdown coordinator.
	ErrNilShutdownState = errors.New("shutdown state is required")
	// ErrMonitorActive reports duplicate ownership of one OS signal class.
	ErrMonitorActive = errors.New("signal monitor is already active")
	// ErrShutdownComplete reports an attempt to attach a new signal monitor
	// after every prior owner completed.
	ErrShutdownComplete = errors.New("process shutdown monitoring is complete")
	// ErrInterruptOwnership reports incompatible process and print claims over
	// SIGINT delivery on the same coordinator.
	ErrInterruptOwnership = errors.New("conflicting SIGINT ownership")
	// ErrMonitorOrder reports an attempt to add the process monitor after a
	// standalone print monitor has already acquired OS signal delivery.
	ErrMonitorOrder = errors.New("process signal monitor must start before and stop after the print monitor")
)

// InterruptOwnership selects the sole semantic owner of SIGINT. Process owns
// interactive, informational, invalid, and standalone-MCP invocations; Print
// owns raw text, JSON, and NDJSON model turns.
type InterruptOwnership uint8

const (
	InterruptOwnedByProcess InterruptOwnership = iota + 1
	InterruptOwnedByPrint
)

type monitorOwner uint8

const (
	processMonitorOwner monitorOwner = iota + 1
	printMonitorOwner
)

type signalAction uint8

const (
	signalIgnored signalAction = iota
	signalFirst
	signalSubsequent
)

// ProcessShutdown coordinates every signal owner for one process. The first
// recognized request wins globally, starts one failsafe, and supplies the
// exit code used by every later signal. The second request and the failsafe
// race through one exact-once force gate. Monitor release disarms that gate
// and joins a force callback that has already begun.
//
// A nil forceExit deliberately disables forced termination for embedded or
// test use. A nonpositive failsafe is normalized to DefaultFailsafe.
type ProcessShutdown struct {
	request atomic.Pointer[platform.ShutdownRequest]

	mu              sync.Mutex
	forceExit       func(int)
	failsafe        time.Duration
	timer           *time.Timer
	owners          map[monitorOwner]bool
	interrupt       InterruptOwnership
	printStop       context.CancelFunc
	processStopping bool
	completed       bool
	forcing         bool
	forceWG         sync.WaitGroup

	// beforeForceInvoke is an immutable test seam used to hold the interval
	// after force reservation and before an injected callback runs.
	beforeForceInvoke func()
}

// NewProcessShutdown constructs a dormant first-request-wins shutdown
// coordinator. The force callback must be non-reentrant: it must not call a
// monitor stop function or another coordinator lifecycle method. It must
// either return or terminate the process; the final monitor stop waits for an
// in-flight callback before returning.
func NewProcessShutdown(forceExit func(int), failsafe time.Duration) *ProcessShutdown {
	if failsafe <= 0 {
		failsafe = DefaultFailsafe
	}
	return &ProcessShutdown{
		forceExit: forceExit,
		failsafe:  failsafe,
		owners:    make(map[monitorOwner]bool, 2),
	}
}

// Snapshot returns the immutable winning request, when one exists.
func (state *ProcessShutdown) Snapshot() (platform.ShutdownRequest, bool) {
	if state == nil {
		return platform.ShutdownRequest{}, false
	}
	request := state.request.Load()
	if request == nil {
		return platform.ShutdownRequest{}, false
	}
	return *request, true
}

// ExitCode returns the winning signal/surface code or fallback when shutdown
// was not requested.
func (state *ProcessShutdown) ExitCode(fallback int) int {
	if request, ok := state.Snapshot(); ok {
		return request.ExitCode
	}
	return fallback
}

func (state *ProcessShutdown) acquire(owner monitorOwner, interrupt InterruptOwnership) error {
	if state == nil {
		return ErrNilShutdownState
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.completed {
		return ErrShutdownComplete
	}
	if state.processStopping {
		return ErrShutdownComplete
	}
	if state.owners == nil {
		state.owners = make(map[monitorOwner]bool, 2)
	}
	if state.owners[owner] {
		return fmt.Errorf("%w: %s", ErrMonitorActive, owner)
	}
	if owner == processMonitorOwner && state.owners[printMonitorOwner] {
		return ErrMonitorOrder
	}
	if interrupt != InterruptOwnedByProcess && interrupt != InterruptOwnedByPrint {
		return fmt.Errorf("%w: invalid owner %d", ErrInterruptOwnership, interrupt)
	}
	if state.interrupt != 0 && state.interrupt != interrupt {
		return fmt.Errorf("%w: existing=%s requested=%s", ErrInterruptOwnership, state.interrupt, interrupt)
	}
	state.owners[owner] = true
	state.interrupt = interrupt
	return nil
}

func (state *ProcessShutdown) beginProcessStop() (bool, error) {
	if state == nil {
		return false, ErrNilShutdownState
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.completed || !state.owners[processMonitorOwner] {
		return false, nil
	}
	if state.owners[printMonitorOwner] {
		return false, ErrMonitorOrder
	}
	state.processStopping = true
	return true, nil
}

func (state *ProcessShutdown) release(owner monitorOwner) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if !state.owners[owner] {
		state.mu.Unlock()
		return
	}
	delete(state.owners, owner)
	if owner == printMonitorOwner {
		state.printStop = nil
	}
	if len(state.owners) != 0 {
		state.mu.Unlock()
		return
	}
	state.completed = true
	timer := state.timer
	state.timer = nil
	state.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	// forceWG.Add happens while holding mu and only before completed becomes
	// true, so no Add can race this Wait after the final owner is released.
	state.forceWG.Wait()
}

func (state *ProcessShutdown) processMonitorActive() bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.owners[processMonitorOwner]
}

func (state *ProcessShutdown) setPrintCancel(cancel context.CancelFunc) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if !state.completed && state.owners[printMonitorOwner] {
		state.printStop = cancel
	}
	state.mu.Unlock()
}

func (state *ProcessShutdown) clearPrintCancel() {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.printStop = nil
	state.mu.Unlock()
}

func (state *ProcessShutdown) handleSignal(exitCode int, reason string) signalAction {
	if state == nil {
		return signalIgnored
	}
	state.mu.Lock()
	action := state.handleSignalLocked(exitCode, reason)
	forceExit, forceCode, forceReserved := state.reserveForceForActionLocked(action)
	state.mu.Unlock()
	state.invokeReservedForce(forceExit, forceCode, forceReserved)
	return action
}

// handleProcessSignal atomically selects the semantic cancellation owner and
// records the signal. Print-owned SIGINT is ignored until a print handler is
// registered; TERM/HUP and process-owned SIGINT always use fallback.
func (state *ProcessShutdown) handleProcessSignal(exitCode int, reason string, isInterrupt bool, fallback context.CancelFunc) (signalAction, context.CancelFunc) {
	if state == nil {
		return signalIgnored, nil
	}
	state.mu.Lock()
	selectedCancel := fallback
	if isInterrupt && state.interrupt == InterruptOwnedByPrint {
		if state.printStop == nil && state.request.Load() == nil {
			state.mu.Unlock()
			return signalIgnored, nil
		}
		selectedCancel = state.printStop
	}
	action := state.handleSignalLocked(exitCode, reason)
	forceExit, forceCode, forceReserved := state.reserveForceForActionLocked(action)
	state.mu.Unlock()
	state.invokeReservedForce(forceExit, forceCode, forceReserved)
	return action, selectedCancel
}

func (state *ProcessShutdown) reserveForceForActionLocked(action signalAction) (func(int), int, bool) {
	if action != signalSubsequent {
		return nil, 0, false
	}
	return state.reserveForceLocked()
}

func (state *ProcessShutdown) handleSignalLocked(exitCode int, reason string) signalAction {
	if state.completed || state.processStopping {
		return signalIgnored
	}
	if state.request.Load() != nil {
		return signalSubsequent
	}
	request := &platform.ShutdownRequest{ExitCode: exitCode, Reason: reason}
	state.request.Store(request)
	if state.forceExit != nil {
		state.timer = time.AfterFunc(state.failsafe, func() {
			state.forceWinningRequest()
		})
	}
	return signalFirst
}

func (state *ProcessShutdown) forceWinningRequest() bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	forceExit, exitCode, reserved := state.reserveForceLocked()
	state.mu.Unlock()
	return state.invokeReservedForce(forceExit, exitCode, reserved)
}

func (state *ProcessShutdown) reserveForceLocked() (func(int), int, bool) {
	request := state.request.Load()
	if state.completed || state.processStopping || state.forcing || request == nil || state.forceExit == nil {
		return nil, 0, false
	}
	state.forcing = true
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	state.forceWG.Add(1)
	return state.forceExit, request.ExitCode, true
}

func (state *ProcessShutdown) invokeReservedForce(forceExit func(int), exitCode int, reserved bool) bool {
	if state == nil || !reserved {
		return false
	}
	defer state.forceWG.Done()
	if state.beforeForceInvoke != nil {
		state.beforeForceInvoke()
	}
	forceExit(exitCode)
	return true
}

func (owner monitorOwner) String() string {
	switch owner {
	case processMonitorOwner:
		return "process"
	case printMonitorOwner:
		return "print"
	default:
		return "unknown"
	}
}

func (owner InterruptOwnership) String() string {
	switch owner {
	case InterruptOwnedByProcess:
		return "process"
	case InterruptOwnedByPrint:
		return "print"
	default:
		return "unknown"
	}
}

type processShutdownContextKey struct{}

// WithProcessShutdown attaches process-owned shutdown state to a context.
func WithProcessShutdown(ctx context.Context, state *ProcessShutdown) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, processShutdownContextKey{}, state)
}

// ProcessShutdownFromContext retrieves process-owned shutdown state without
// inventing a replacement owner when the context has none.
func ProcessShutdownFromContext(ctx context.Context) *ProcessShutdown {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(processShutdownContextKey{}).(*ProcessShutdown)
	return state
}
