// Package distributed defines transport- and placement-neutral contracts for
// work that crosses a process or machine boundary. It never redefines message,
// permission, tool, task, or transcript semantics.
package distributed

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"unicode"
	"unicode/utf8"
)

type (
	BridgeInstanceID string
	EnvironmentID    string
	WorkID           string
	RemoteSessionID  string
	MessageID        string
	ControlRequestID string
	ToolUseID        string
	DeliveryID       string
	Epoch            uint64
	Sequence         uint64
	Generation       uint64
)

var safePathIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateOpaqueID rejects empty, oversized, non-UTF-8, and control-bearing
// identifiers. It deliberately does not treat one ID class as another.
func ValidateOpaqueID(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	if len(value) > 256 {
		return fmt.Errorf("%s exceeds 256 bytes", kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", kind)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", kind)
		}
	}
	return nil
}

// ValidatePathIdentifier applies the stricter grammar required before an
// opaque service identifier is interpolated into a URL path.
func ValidatePathIdentifier(kind, value string) error {
	if err := ValidateOpaqueID(kind, value); err != nil {
		return err
	}
	if !safePathIdentifier.MatchString(value) {
		return fmt.Errorf("%s is not a safe URL path identifier", kind)
	}
	return nil
}

// IdentityTuple names every correlation scope used by a live remote surface.
// Fields remain distinct even if their string spellings happen to match.
type IdentityTuple struct {
	BridgeInstance BridgeInstanceID `json:"bridge_instance_id,omitempty"`
	Environment    EnvironmentID    `json:"environment_id,omitempty"`
	Work           WorkID           `json:"work_id,omitempty"`
	Session        RemoteSessionID  `json:"remote_session_id"`
	Epoch          Epoch            `json:"worker_epoch"`
	Generation     Generation       `json:"surface_generation"`
}

// Validate checks the minimum identity needed by a control-capable session.
func (i IdentityTuple) Validate() error {
	if err := ValidateOpaqueID("remote session ID", string(i.Session)); err != nil {
		return err
	}
	if i.Epoch == 0 {
		return errors.New("worker epoch must be positive")
	}
	if i.Generation == 0 {
		return errors.New("surface generation must be positive")
	}
	if i.BridgeInstance != "" {
		if err := ValidateOpaqueID("bridge instance ID", string(i.BridgeInstance)); err != nil {
			return err
		}
	}
	if i.Environment != "" {
		if err := ValidateOpaqueID("environment ID", string(i.Environment)); err != nil {
			return err
		}
	}
	if i.Work != "" {
		if err := ValidateOpaqueID("work ID", string(i.Work)); err != nil {
			return err
		}
	}
	return nil
}

var (
	ErrStaleEpoch       = errors.New("stale worker epoch")
	ErrEpochNotAdvanced = errors.New("worker epoch did not advance")
	ErrInvalidSequence  = errors.New("invalid sequence")
)

// EpochFence ensures stale workers cannot continue writing after replacement.
type EpochFence struct {
	mu      sync.Mutex
	current Epoch
}

func NewEpochFence(initial Epoch) (*EpochFence, error) {
	if initial == 0 {
		return nil, errors.New("initial epoch must be positive")
	}
	return &EpochFence{current: initial}, nil
}

func (f *EpochFence) Current() Epoch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *EpochFence) Check(epoch Epoch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch != f.current {
		return fmt.Errorf("%w: got %d, active %d", ErrStaleEpoch, epoch, f.current)
	}
	return nil
}

func (f *EpochFence) Advance(epoch Epoch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch <= f.current {
		return fmt.Errorf("%w: got %d after %d", ErrEpochNotAdvanced, epoch, f.current)
	}
	f.current = epoch
	return nil
}

// Cursor is an observed transport position, not proof of dispatch,
// persistence, processing, or acknowledgement.
type Cursor struct {
	Sequence Sequence `json:"observed_sequence"`
}

// Observation describes how a parsed frame ID changed the cursor. Callers
// perform this before decoding the payload so malformed payloads cannot make a
// reconnect loop repeatedly observe the same frame.
type Observation struct {
	Previous  Sequence `json:"previous"`
	Current   Sequence `json:"current"`
	Advanced  bool     `json:"advanced"`
	Duplicate bool     `json:"duplicate"`
	Gap       bool     `json:"gap"`
}

// Observe advances only to a greater positive sequence.
func (c *Cursor) Observe(sequence Sequence) (Observation, error) {
	if sequence == 0 {
		return Observation{}, ErrInvalidSequence
	}
	result := Observation{Previous: c.Sequence, Current: c.Sequence}
	if sequence <= c.Sequence {
		result.Duplicate = sequence == c.Sequence
		return result, nil
	}
	result.Advanced = true
	result.Gap = c.Sequence != 0 && sequence > c.Sequence+1
	c.Sequence = sequence
	result.Current = sequence
	return result, nil
}
