package distributed

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrGateActive      = errors.New("history flush gate is already active")
	ErrGateInactive    = errors.New("history flush gate is inactive")
	ErrGateClosed      = errors.New("history flush gate is closed")
	ErrSendNotAccepted = errors.New("transport did not accept outbound event")
	ErrGateCapacity    = errors.New("history flush gate capacity exceeded")
)

const DefaultGateCapacity = 100_000

type GateState string

const (
	GateInactive    GateState = "inactive"
	GateCollecting  GateState = "collecting_history"
	GateReady       GateState = "ready"
	GateDraining    GateState = "draining"
	GateDeactivated GateState = "deactivated"
	GateClosed      GateState = "closed"
)

// OutboundEvent is a selected bridge projection, not an authoritative local
// transcript message. Payload is already normalized and credential-free.
type OutboundEvent struct {
	MessageID MessageID `json:"message_id"`
	Type      string    `json:"type"`
	Payload   []byte    `json:"payload"`
}

// Acceptance is local writer evidence only. It does not imply a remote ACK or
// durable processing.
type Acceptance struct {
	Accepted        bool   `json:"accepted"`
	QueueIdentity   string `json:"queue_identity,omitempty"`
	RemoteAckID     string `json:"remote_ack_id,omitempty"`
	DurabilityKnown bool   `json:"durability_known"`
}

// Sender is normally a serial transport writer. Its receipt must identify
// local acceptance; a nil error alone is intentionally insufficient.
type Sender interface {
	Send(context.Context, OutboundEvent) (Acceptance, error)
}

type SubmitResult struct {
	Queued     bool       `json:"queued"`
	Acceptance Acceptance `json:"acceptance"`
}

// FlushGate guarantees initial history precedes live events. sendMu serializes
// begin/direct-send/drain boundaries; mu protects only the process-local queue.
type FlushGate struct {
	sendMu   sync.Mutex
	mu       sync.Mutex
	state    GateState
	pending  []OutboundEvent
	capacity int
}

func NewFlushGate() *FlushGate {
	gate, _ := NewFlushGateWithCapacity(DefaultGateCapacity)
	return gate
}

func NewFlushGateWithCapacity(capacity int) (*FlushGate, error) {
	if capacity <= 0 {
		return nil, errors.New("flush gate capacity must be positive")
	}
	return &FlushGate{state: GateInactive, capacity: capacity}, nil
}

func (g *FlushGate) State() GateState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// BeginHistory must run before the caller freezes its history snapshot. Live
// submissions are queued from this point onward.
func (g *FlushGate) BeginHistory() error {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == GateClosed {
		return ErrGateClosed
	}
	if g.state != GateInactive {
		return ErrGateActive
	}
	g.state = GateCollecting
	return nil
}

// InstallHistory prepends the frozen history to live messages collected since
// BeginHistory, preserving history-before-live order.
func (g *FlushGate) InstallHistory(history []OutboundEvent) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == GateClosed {
		return ErrGateClosed
	}
	if g.state != GateCollecting {
		return fmt.Errorf("%w: cannot install from %s", ErrGateInactive, g.state)
	}
	if len(history)+len(g.pending) > g.capacity {
		return ErrGateCapacity
	}
	combined := make([]OutboundEvent, 0, len(history)+len(g.pending))
	combined = append(combined, cloneEvents(history)...)
	combined = append(combined, g.pending...)
	g.pending = combined
	g.state = GateReady
	return nil
}

// Submit queues while a history/recovery gate is active and sends directly
// otherwise. BeginHistory cannot overtake a direct send already in progress.
func (g *FlushGate) Submit(ctx context.Context, event OutboundEvent, sender Sender) (SubmitResult, error) {
	if sender == nil {
		return SubmitResult{}, errors.New("sender is nil")
	}
	if err := validateOutbound(event); err != nil {
		return SubmitResult{}, err
	}
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	switch g.state {
	case GateClosed:
		g.mu.Unlock()
		return SubmitResult{}, ErrGateClosed
	case GateCollecting, GateReady, GateDraining, GateDeactivated:
		if len(g.pending) >= g.capacity {
			g.mu.Unlock()
			return SubmitResult{}, ErrGateCapacity
		}
		g.pending = append(g.pending, cloneEvent(event))
		g.mu.Unlock()
		return SubmitResult{Queued: true}, nil
	case GateInactive:
		g.mu.Unlock()
		acceptance, err := sender.Send(ctx, cloneEvent(event))
		if err != nil {
			return SubmitResult{}, err
		}
		if !acceptance.Accepted {
			return SubmitResult{Acceptance: acceptance}, ErrSendNotAccepted
		}
		return SubmitResult{Acceptance: acceptance}, nil
	default:
		g.mu.Unlock()
		return SubmitResult{}, fmt.Errorf("unknown gate state %q", g.state)
	}
}

// Drain sends retained items serially. A failed or unaccepted head stays at
// the front for explicit same-process retry; later messages cannot overtake it.
func (g *FlushGate) Drain(ctx context.Context, sender Sender) error {
	if sender == nil {
		return errors.New("sender is nil")
	}
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	if g.state == GateClosed {
		g.mu.Unlock()
		return ErrGateClosed
	}
	if g.state != GateReady && g.state != GateDeactivated {
		state := g.state
		g.mu.Unlock()
		return fmt.Errorf("%w: cannot drain from %s", ErrGateInactive, state)
	}
	g.state = GateDraining
	g.mu.Unlock()
	for {
		g.mu.Lock()
		if g.state == GateDeactivated {
			g.mu.Unlock()
			return ErrGateInactive
		}
		if len(g.pending) == 0 {
			g.state = GateInactive
			g.mu.Unlock()
			return nil
		}
		event := cloneEvent(g.pending[0])
		g.mu.Unlock()

		acceptance, err := sender.Send(ctx, event)
		if err != nil || !acceptance.Accepted {
			g.mu.Lock()
			if g.state != GateClosed {
				g.state = GateDeactivated
			}
			g.mu.Unlock()
			if err != nil {
				return err
			}
			return ErrSendNotAccepted
		}
		g.mu.Lock()
		g.pending = g.pending[1:]
		deactivated := g.state == GateDeactivated
		g.mu.Unlock()
		if deactivated {
			return ErrGateInactive
		}
	}
}

// FlushHistory performs the required start-snapshot-install-drain sequence.
func (g *FlushGate) FlushHistory(ctx context.Context, snapshot func() []OutboundEvent, sender Sender) error {
	if snapshot == nil {
		return errors.New("history snapshot function is nil")
	}
	if err := g.BeginHistory(); err != nil {
		return err
	}
	history := snapshot()
	if err := g.InstallHistory(history); err != nil {
		g.Deactivate()
		return err
	}
	return g.Drain(ctx, sender)
}

// Deactivate retains queued events for a replacement transport.
func (g *FlushGate) Deactivate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GateClosed && g.state != GateInactive {
		g.state = GateDeactivated
	}
}

// Drop is reserved for final teardown or an explicitly unrecoverable path and
// returns the exact still-observable loss count.
func (g *FlushGate) Drop(closeGate bool) int {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	count := len(g.pending)
	g.pending = nil
	if closeGate {
		g.state = GateClosed
	} else {
		g.state = GateInactive
	}
	return count
}

func (g *FlushGate) Pending() []OutboundEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneEvents(g.pending)
}

func validateOutbound(event OutboundEvent) error {
	if err := ValidateOpaqueID("message ID", string(event.MessageID)); err != nil {
		return err
	}
	if event.Type == "" {
		return errors.New("outbound event type is empty")
	}
	return nil
}

func cloneEvent(event OutboundEvent) OutboundEvent {
	event.Payload = append([]byte(nil), event.Payload...)
	return event
}

func cloneEvents(events []OutboundEvent) []OutboundEvent {
	result := make([]OutboundEvent, len(events))
	for index, event := range events {
		result[index] = cloneEvent(event)
	}
	return result
}
