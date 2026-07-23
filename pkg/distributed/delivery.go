package distributed

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// DeliveryState describes adapter handling only. Processed never claims model
// completion, transcript durability, or semantic success.
type DeliveryState string

const (
	DeliveryObserved   DeliveryState = "observed"
	DeliveryReceived   DeliveryState = "received"
	DeliveryProcessing DeliveryState = "processing"
	DeliveryProcessed  DeliveryState = "processed"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryCancelled  DeliveryState = "cancelled"
)

func (s DeliveryState) Terminal() bool {
	return s == DeliveryProcessed || s == DeliveryFailed || s == DeliveryCancelled
}

var deliveryTransitions = map[DeliveryState]map[DeliveryState]bool{
	DeliveryObserved:   {DeliveryReceived: true, DeliveryFailed: true, DeliveryCancelled: true},
	DeliveryReceived:   {DeliveryProcessing: true, DeliveryProcessed: true, DeliveryFailed: true, DeliveryCancelled: true},
	DeliveryProcessing: {DeliveryProcessed: true, DeliveryFailed: true, DeliveryCancelled: true},
}

// DeliveryEvidence keeps acknowledgement independent of delivery state and
// observed cursor position.
type DeliveryEvidence struct {
	ID               DeliveryID    `json:"delivery_id"`
	Message          MessageID     `json:"message_id"`
	Sequence         Sequence      `json:"observed_sequence,omitempty"`
	State            DeliveryState `json:"state"`
	TransportAckID   string        `json:"transport_ack_id,omitempty"`
	TransportAckedAt *time.Time    `json:"transport_acked_at,omitempty"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Failure          string        `json:"failure,omitempty"`
}

type DeliveryTracker struct {
	mu      sync.Mutex
	records map[DeliveryID]DeliveryEvidence
	now     func() time.Time
}

func NewDeliveryTracker() *DeliveryTracker {
	return &DeliveryTracker{records: make(map[DeliveryID]DeliveryEvidence), now: time.Now}
}

func (t *DeliveryTracker) Observe(id DeliveryID, message MessageID, sequence Sequence) error {
	if err := ValidateOpaqueID("delivery ID", string(id)); err != nil {
		return err
	}
	if err := ValidateOpaqueID("message ID", string(message)); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.records[id]; exists {
		return errors.New("delivery ID already observed")
	}
	t.records[id] = DeliveryEvidence{ID: id, Message: message, Sequence: sequence, State: DeliveryObserved, UpdatedAt: t.now()}
	return nil
}

func (t *DeliveryTracker) Transition(id DeliveryID, state DeliveryState, failure string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, exists := t.records[id]
	if !exists {
		return errors.New("delivery not found")
	}
	if !deliveryTransitions[record.State][state] {
		return fmt.Errorf("%w: delivery %s: %s -> %s", ErrInvalidTransition, id, record.State, state)
	}
	record.State = state
	record.UpdatedAt = t.now()
	if state == DeliveryFailed {
		record.Failure = failure
	}
	t.records[id] = record
	return nil
}

// Acknowledge records explicit remote transport evidence. Neither dedupe nor a
// processed callback can call this implicitly.
func (t *DeliveryTracker) Acknowledge(id DeliveryID, ackID string) error {
	if err := ValidateOpaqueID("transport acknowledgement ID", ackID); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	record, exists := t.records[id]
	if !exists {
		return errors.New("delivery not found")
	}
	if record.TransportAckID != "" {
		if record.TransportAckID == ackID {
			return nil
		}
		return errors.New("delivery already has a different acknowledgement")
	}
	now := t.now()
	record.TransportAckID = ackID
	record.TransportAckedAt = &now
	record.UpdatedAt = now
	t.records[id] = record
	return nil
}

func (t *DeliveryTracker) Get(id DeliveryID) (DeliveryEvidence, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.records[id]
	return record, ok
}
