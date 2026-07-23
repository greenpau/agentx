package distributed

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type recordingSender struct {
	mu       sync.Mutex
	events   []OutboundEvent
	failNext bool
	accept   bool
}

func (s *recordingSender) Send(_ context.Context, event OutboundEvent) (Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return Acceptance{}, errors.New("offline")
	}
	if !s.accept {
		return Acceptance{}, nil
	}
	s.events = append(s.events, cloneEvent(event))
	return Acceptance{Accepted: true, QueueIdentity: string(event.MessageID)}, nil
}

func (s *recordingSender) IDs() []MessageID {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]MessageID, len(s.events))
	for index, event := range s.events {
		result[index] = event.MessageID
	}
	return result
}

func outbound(id string) OutboundEvent {
	return OutboundEvent{MessageID: MessageID(id), Type: "message", Payload: []byte(id)}
}

func TestFlushGateOrdersHistoryBeforeConcurrentLive(t *testing.T) {
	gate := NewFlushGate()
	sender := &recordingSender{accept: true}
	if err := gate.BeginHistory(); err != nil {
		t.Fatal(err)
	}
	result, err := gate.Submit(context.Background(), outbound("live-1"), sender)
	if err != nil || !result.Queued {
		t.Fatalf("live submit = %+v, %v", result, err)
	}
	if err := gate.InstallHistory([]OutboundEvent{outbound("history-1"), outbound("history-2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Submit(context.Background(), outbound("live-2"), sender); err != nil {
		t.Fatal(err)
	}
	if err := gate.Drain(context.Background(), sender); err != nil {
		t.Fatal(err)
	}
	want := []MessageID{"history-1", "history-2", "live-1", "live-2"}
	got := sender.IDs()
	if len(got) != len(want) {
		t.Fatalf("order = %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v", got)
		}
	}
}

func TestFlushGateRetainsFailedHeadAndReportsDrop(t *testing.T) {
	gate, err := NewFlushGateWithCapacity(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.BeginHistory(); err != nil {
		t.Fatal(err)
	}
	if err := gate.InstallHistory([]OutboundEvent{outbound("one"), outbound("two")}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Submit(context.Background(), outbound("overflow"), &recordingSender{accept: true}); !errors.Is(err, ErrGateCapacity) {
		t.Fatalf("overflow error = %v", err)
	}
	sender := &recordingSender{accept: true, failNext: true}
	if err := gate.Drain(context.Background(), sender); err == nil {
		t.Fatal("failed send was reported successful")
	}
	if pending := gate.Pending(); len(pending) != 2 || pending[0].MessageID != "one" {
		t.Fatalf("failed head not retained: %+v", pending)
	}
	if err := gate.Drain(context.Background(), sender); err != nil {
		t.Fatal(err)
	}
	if got := sender.IDs(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("retry order = %v", got)
	}

	if err := gate.BeginHistory(); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Submit(context.Background(), outbound("lost"), sender); err != nil {
		t.Fatal(err)
	}
	if dropped := gate.Drop(true); dropped != 1 || gate.State() != GateClosed {
		t.Fatalf("drop evidence=%d state=%s", dropped, gate.State())
	}
}

func TestDirectSendRequiresAcceptanceEvidence(t *testing.T) {
	gate := NewFlushGate()
	_, err := gate.Submit(context.Background(), outbound("message"), &recordingSender{})
	if !errors.Is(err, ErrSendNotAccepted) {
		t.Fatalf("unaccepted send error = %v", err)
	}
}
