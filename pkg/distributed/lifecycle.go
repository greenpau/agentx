package distributed

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrInvalidTransition = errors.New("invalid lifecycle transition")

type TransportState string

const (
	TransportNew           TransportState = "new"
	TransportConnecting    TransportState = "connecting"
	TransportConnected     TransportState = "connected"
	TransportReconnectWait TransportState = "reconnect_wait"
	TransportReplacing     TransportState = "replacing"
	TransportDraining      TransportState = "draining"
	TransportClosed        TransportState = "closed"
	TransportFailed        TransportState = "failed"
)

func (s TransportState) Terminal() bool { return s == TransportClosed || s == TransportFailed }

var transportTransitions = map[TransportState]map[TransportState]bool{
	TransportNew:           {TransportConnecting: true, TransportDraining: true, TransportClosed: true},
	TransportConnecting:    {TransportConnected: true, TransportReconnectWait: true, TransportDraining: true, TransportFailed: true},
	TransportConnected:     {TransportReconnectWait: true, TransportReplacing: true, TransportDraining: true, TransportFailed: true},
	TransportReconnectWait: {TransportConnecting: true, TransportReplacing: true, TransportDraining: true, TransportFailed: true},
	TransportReplacing:     {TransportConnecting: true, TransportConnected: true, TransportReconnectWait: true, TransportDraining: true, TransportFailed: true},
	TransportDraining:      {TransportClosed: true, TransportFailed: true},
}

type Transition struct {
	From   TransportState `json:"from"`
	To     TransportState `json:"to"`
	At     time.Time      `json:"at"`
	Reason string         `json:"reason,omitempty"`
}

// Lifecycle is a synchronized transport state machine. It records state, not
// whether any semantic event succeeded.
type Lifecycle struct {
	mu      sync.Mutex
	state   TransportState
	history []Transition
	now     func() time.Time
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{state: TransportNew, now: time.Now}
}

func (l *Lifecycle) State() TransportState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *Lifecycle) Transition(to TransportState, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !transportTransitions[l.state][to] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, l.state, to)
	}
	l.history = append(l.history, Transition{From: l.state, To: to, At: l.now(), Reason: reason})
	l.state = to
	return nil
}

func (l *Lifecycle) History() []Transition {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Transition(nil), l.history...)
}

type WorkState string

const (
	WorkObserved         WorkState = "observed"
	WorkSecretValidated  WorkState = "secret_validated"
	WorkAcknowledging    WorkState = "acknowledging"
	WorkUnowned          WorkState = "unowned"
	WorkOwned            WorkState = "owned"
	WorkSpawning         WorkState = "spawning"
	WorkRunning          WorkState = "running"
	WorkCompleting       WorkState = "completing"
	WorkFailing          WorkState = "failing"
	WorkInterrupting     WorkState = "interrupting"
	WorkTerminalReported WorkState = "terminal_reported"
	WorkReleased         WorkState = "released"
)

var workTransitions = map[WorkState]map[WorkState]bool{
	WorkObserved:         {WorkSecretValidated: true, WorkUnowned: true},
	WorkSecretValidated:  {WorkAcknowledging: true, WorkUnowned: true},
	WorkAcknowledging:    {WorkOwned: true, WorkUnowned: true},
	WorkOwned:            {WorkSpawning: true, WorkFailing: true, WorkInterrupting: true},
	WorkSpawning:         {WorkRunning: true, WorkFailing: true, WorkInterrupting: true},
	WorkRunning:          {WorkCompleting: true, WorkFailing: true, WorkInterrupting: true},
	WorkCompleting:       {WorkTerminalReported: true},
	WorkFailing:          {WorkTerminalReported: true},
	WorkInterrupting:     {WorkTerminalReported: true},
	WorkTerminalReported: {WorkReleased: true},
}

// WorkLifecycle proves that observed work is not owned until acknowledgement.
type WorkLifecycle struct {
	mu    sync.Mutex
	ID    WorkID
	state WorkState
}

func NewWorkLifecycle(id WorkID) (*WorkLifecycle, error) {
	if err := ValidateOpaqueID("work ID", string(id)); err != nil {
		return nil, err
	}
	return &WorkLifecycle{ID: id, state: WorkObserved}, nil
}

func (w *WorkLifecycle) State() WorkState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *WorkLifecycle) Transition(to WorkState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !workTransitions[w.state][to] {
		return fmt.Errorf("%w: work %s: %s -> %s", ErrInvalidTransition, w.ID, w.state, to)
	}
	w.state = to
	return nil
}

func (w *WorkLifecycle) Owned() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state == WorkOwned || w.state == WorkSpawning || w.state == WorkRunning || w.state == WorkCompleting || w.state == WorkFailing || w.state == WorkInterrupting || w.state == WorkTerminalReported
}
