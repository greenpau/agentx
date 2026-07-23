package distributed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrRecoveryClosed = errors.New("reconnect coordinator is closed")
	ErrRecovering     = errors.New("transport is recovering")
)

const DefaultTransportCloseTimeout = 3 * time.Second

// ResumePoint carries only evidence actually retained by this process. Cursor
// is observed high-water, not a processing or durability checkpoint.
type ResumePoint struct {
	Session RemoteSessionID `json:"remote_session_id"`
	Epoch   Epoch           `json:"previous_epoch"`
	Cursor  Cursor          `json:"cursor"`
}

type ConnectAttempt func(context.Context, ResumePoint) (Transport, Epoch, error)

type recoveryResult struct {
	transport Transport
	epoch     Epoch
	err       error
}

type recoveryFlight struct {
	done   chan struct{}
	result recoveryResult
}

// ReconnectCoordinator serializes proactive refresh and reactive reconnect so
// one live generation advances its epoch at most once. It owns the installed
// transport and closes a late replacement when teardown wins.
type ReconnectCoordinator struct {
	ownerCtx context.Context
	cancel   context.CancelFunc
	session  RemoteSessionID
	cursor   *Cursor

	mu            sync.Mutex
	lifecycle     *Lifecycle
	fence         *EpochFence
	current       Transport
	flight        *recoveryFlight
	closed        bool
	closeDone     chan struct{}
	closeEvidence CloseEvidence
	closeErr      error
}

func NewReconnectCoordinator(parent context.Context, session RemoteSessionID, epoch Epoch, cursor Cursor, current Transport) (*ReconnectCoordinator, error) {
	if parent == nil {
		return nil, errors.New("owner context is nil")
	}
	if err := ValidateOpaqueID("remote session ID", string(session)); err != nil {
		return nil, err
	}
	fence, err := NewEpochFence(epoch)
	if err != nil {
		return nil, err
	}
	ownerCtx, cancel := context.WithCancel(parent)
	lifecycle := NewLifecycle()
	if err := lifecycle.Transition(TransportConnecting, "initial transport"); err != nil {
		cancel()
		return nil, err
	}
	if current != nil {
		if err := lifecycle.Transition(TransportConnected, "initial transport ready"); err != nil {
			cancel()
			return nil, err
		}
	}
	return &ReconnectCoordinator{
		ownerCtx: ownerCtx, cancel: cancel, session: session, cursor: &cursor,
		lifecycle: lifecycle, fence: fence, current: current,
	}, nil
}

// Recover joins an existing recovery or starts one owned by the coordinator,
// not by the first waiter's cancellation context.
func (r *ReconnectCoordinator) Recover(ctx context.Context, reason string, attempt ConnectAttempt) (Transport, Epoch, error) {
	if attempt == nil {
		return nil, 0, errors.New("connect attempt is nil")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, 0, ErrRecoveryClosed
	}
	flight := r.flight
	if flight == nil {
		flight = &recoveryFlight{done: make(chan struct{})}
		r.flight = flight
		state := r.lifecycle.State()
		if state == TransportConnected {
			_ = r.lifecycle.Transition(TransportReplacing, reason)
		} else if state == TransportConnecting || state == TransportReconnectWait {
			_ = r.lifecycle.Transition(TransportReplacing, reason)
		}
		resume := ResumePoint{Session: r.session, Epoch: r.fence.Current(), Cursor: *r.cursor}
		go r.runRecovery(flight, resume, attempt)
	}
	r.mu.Unlock()
	select {
	case <-flight.done:
		return flight.result.transport, flight.result.epoch, flight.result.err
	case <-ctx.Done():
		return nil, 0, fmt.Errorf("wait for reconnect: %w", ctx.Err())
	}
}

func (r *ReconnectCoordinator) runRecovery(flight *recoveryFlight, resume ResumePoint, attempt ConnectAttempt) {
	transport, epoch, err := attempt(r.ownerCtx, resume)
	if err == nil && transport == nil {
		err = errors.New("connect attempt returned nil transport")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		if transport != nil {
			closeTransportBounded(transport)
		}
		r.completeFlight(flight, recoveryResult{err: ErrRecoveryClosed})
		return
	}
	if err != nil {
		state := r.lifecycle.State()
		if state == TransportReplacing {
			_ = r.lifecycle.Transition(TransportReconnectWait, "reconnect failed")
		}
		r.mu.Unlock()
		r.completeFlight(flight, recoveryResult{err: err})
		return
	}
	if err := r.fence.Advance(epoch); err != nil {
		r.mu.Unlock()
		closeTransportBounded(transport)
		r.completeFlight(flight, recoveryResult{err: err})
		return
	}
	old := r.current
	r.current = transport
	if state := r.lifecycle.State(); state == TransportReplacing {
		_ = r.lifecycle.Transition(TransportConnected, "replacement connected")
	}
	r.mu.Unlock()
	if old != nil && old != transport {
		closeTransportBounded(old)
	}
	r.completeFlight(flight, recoveryResult{transport: transport, epoch: epoch})
}

func (r *ReconnectCoordinator) completeFlight(flight *recoveryFlight, result recoveryResult) {
	r.mu.Lock()
	flight.result = result
	if r.flight == flight {
		r.flight = nil
	}
	close(flight.done)
	r.mu.Unlock()
}

// Send refuses direct writes during replacement and checks the active epoch.
func (r *ReconnectCoordinator) Send(ctx context.Context, epoch Epoch, event OutboundEvent) (Acceptance, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Acceptance{}, ErrRecoveryClosed
	}
	if r.lifecycle.State() != TransportConnected || r.flight != nil {
		r.mu.Unlock()
		return Acceptance{}, ErrRecovering
	}
	if err := r.fence.Check(epoch); err != nil {
		r.mu.Unlock()
		return Acceptance{}, err
	}
	transport := r.current
	r.mu.Unlock()
	if transport == nil {
		return Acceptance{}, &UnavailableError{State: UnavailableImplementation, Reason: "no connected transport"}
	}
	acceptance, err := transport.Send(ctx, event)
	if err == nil && !acceptance.Accepted {
		return acceptance, ErrSendNotAccepted
	}
	return acceptance, err
}

// ObserveSequence updates the reconnect high-water before payload admission.
func (r *ReconnectCoordinator) ObserveSequence(sequence Sequence) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor.Observe(sequence)
}

func (r *ReconnectCoordinator) State() TransportState { return r.lifecycle.State() }

// Close fences future writes, cancels recovery, closes the installed transport,
// and classifies any exact orderly drop evidence returned by that transport.
func (r *ReconnectCoordinator) Close(ctx context.Context) (CloseEvidence, error) {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		select {
		case <-done:
			r.mu.Lock()
			evidence, err := r.closeEvidence, r.closeErr
			r.mu.Unlock()
			return evidence, err
		case <-ctx.Done():
			return CloseEvidence{}, ctx.Err()
		}
	}
	r.closed = true
	r.closeDone = make(chan struct{})
	r.cancel()
	state := r.lifecycle.State()
	if !state.Terminal() && state != TransportDraining {
		_ = r.lifecycle.Transition(TransportDraining, "teardown")
	}
	transport := r.current
	r.current = nil
	flight := r.flight
	r.mu.Unlock()
	var evidence CloseEvidence
	var closeErr error
	if transport != nil {
		evidence, closeErr = transport.Close(ctx)
	}
	if flight != nil {
		select {
		case <-flight.done:
		case <-ctx.Done():
			if closeErr == nil {
				closeErr = ctx.Err()
			}
		}
	}
	if r.lifecycle.State() == TransportDraining {
		_ = r.lifecycle.Transition(TransportClosed, "teardown complete")
	}
	r.mu.Lock()
	r.closeEvidence, r.closeErr = evidence, closeErr
	close(r.closeDone)
	r.mu.Unlock()
	return evidence, closeErr
}

func closeTransportBounded(transport Transport) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTransportCloseTimeout)
	defer cancel()
	_, _ = transport.Close(ctx)
}
