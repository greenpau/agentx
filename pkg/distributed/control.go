package distributed

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrControlNotFound = errors.New("control request not found")
	ErrControlTerminal = errors.New("control request is already terminal")
)

type ControlState string

const (
	ControlReceived      ControlState = "received"
	ControlValidating    ControlState = "validating"
	ControlAwaitingLocal ControlState = "awaiting_local_action"
	ControlResponding    ControlState = "responding"
	ControlSucceeded     ControlState = "succeeded"
	ControlDenied        ControlState = "denied"
	ControlErrored       ControlState = "errored"
	ControlCancelled     ControlState = "cancelled"
	ControlSuperseded    ControlState = "superseded"
)

func (s ControlState) Terminal() bool {
	return s == ControlSucceeded || s == ControlDenied || s == ControlErrored || s == ControlCancelled || s == ControlSuperseded
}

type ControlKey struct {
	Request    ControlRequestID `json:"request_id"`
	Session    RemoteSessionID  `json:"remote_session_id"`
	Generation Generation       `json:"surface_generation"`
	Epoch      Epoch            `json:"worker_epoch"`
}

func (k ControlKey) Validate() error {
	if err := ValidateOpaqueID("control request ID", string(k.Request)); err != nil {
		return err
	}
	if err := ValidateOpaqueID("remote session ID", string(k.Session)); err != nil {
		return err
	}
	if k.Generation == 0 || k.Epoch == 0 {
		return errors.New("control generation and epoch must be positive")
	}
	return nil
}

func (k ControlKey) mapKey() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", k.Request, k.Session, k.Generation, k.Epoch)
}

// ControlRecord is process-local correlation evidence. It deliberately uses a
// generation fence instead of accepting unscoped orphan permission responses.
type ControlRecord struct {
	Key       ControlKey      `json:"key"`
	ToolUseID ToolUseID       `json:"tool_use_id,omitempty"`
	Subtype   string          `json:"subtype"`
	State     ControlState    `json:"state"`
	Value     json.RawMessage `json:"value,omitempty"`
	Message   string          `json:"message,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type ControlRegistry struct {
	mu      sync.Mutex
	records map[string]ControlRecord
	now     func() time.Time
}

func NewControlRegistry() *ControlRegistry {
	return &ControlRegistry{records: make(map[string]ControlRecord), now: time.Now}
}

func (r *ControlRegistry) Accept(key ControlKey, subtype string, toolUseID ToolUseID) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if subtype == "" {
		return errors.New("control subtype is empty")
	}
	if toolUseID != "" {
		if err := ValidateOpaqueID("tool-use ID", string(toolUseID)); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index := key.mapKey()
	if _, exists := r.records[index]; exists {
		return errors.New("control request already accepted")
	}
	r.records[index] = ControlRecord{Key: key, ToolUseID: toolUseID, Subtype: subtype, State: ControlReceived, UpdatedAt: r.now()}
	return nil
}

func (r *ControlRegistry) Advance(key ControlKey, state ControlState) error {
	if state != ControlValidating && state != ControlAwaitingLocal && state != ControlResponding {
		return errors.New("advance target is not an intermediate control state")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index := key.mapKey()
	record, ok := r.records[index]
	if !ok {
		return ErrControlNotFound
	}
	if record.State.Terminal() {
		return ErrControlTerminal
	}
	valid := (record.State == ControlReceived && state == ControlValidating) ||
		(record.State == ControlValidating && state == ControlAwaitingLocal) ||
		(record.State == ControlAwaitingLocal && state == ControlResponding)
	if !valid {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, record.State, state)
	}
	record.State, record.UpdatedAt = state, r.now()
	r.records[index] = record
	return nil
}

func (r *ControlRegistry) Resolve(key ControlKey, state ControlState, value json.RawMessage, message string) error {
	if !state.Terminal() {
		return errors.New("control resolution must be terminal")
	}
	if state == ControlSucceeded && (len(value) == 0 || !json.Valid(value)) {
		return errors.New("successful control requires valid result evidence")
	}
	if (state == ControlDenied || state == ControlErrored || state == ControlCancelled || state == ControlSuperseded) && message == "" {
		return errors.New("non-successful control requires a reason")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index := key.mapKey()
	record, ok := r.records[index]
	if !ok {
		return ErrControlNotFound
	}
	if record.State.Terminal() {
		return ErrControlTerminal
	}
	record.State = state
	record.Value = append(json.RawMessage(nil), value...)
	record.Message = message
	record.UpdatedAt = r.now()
	r.records[index] = record
	return nil
}

func (r *ControlRegistry) Cancel(key ControlKey, reason string) error {
	return r.Resolve(key, ControlCancelled, nil, reason)
}

// CancelAll settles every known nonterminal waiter during disconnect or
// teardown and returns deterministic terminal evidence.
func (r *ControlRegistry) CancelAll(reason string) []ControlRecord {
	if reason == "" {
		reason = "owning transport closed"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]ControlRecord, 0)
	for index, record := range r.records {
		if record.State.Terminal() {
			continue
		}
		record.State, record.Message, record.UpdatedAt = ControlCancelled, reason, r.now()
		r.records[index] = record
		result = append(result, cloneControl(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key.mapKey() < result[j].Key.mapKey() })
	return result
}

func (r *ControlRegistry) Get(key ControlKey) (ControlRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[key.mapKey()]
	return cloneControl(record), ok
}

func cloneControl(record ControlRecord) ControlRecord {
	record.Value = append(json.RawMessage(nil), record.Value...)
	return record
}
