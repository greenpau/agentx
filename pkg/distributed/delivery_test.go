package distributed

import (
	"errors"
	"testing"
)

func TestDedupeEvictionIsNotAcknowledgement(t *testing.T) {
	deduper, err := NewDeduper(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []MessageID{"m1", "m2", "m3"} {
		duplicate, err := deduper.SeenOrAdd(id)
		if err != nil || duplicate {
			t.Fatalf("add %s: duplicate=%v err=%v", id, duplicate, err)
		}
	}
	duplicate, err := deduper.SeenOrAdd("m1")
	if err != nil || duplicate {
		t.Fatalf("evicted ID must be absent, duplicate=%v err=%v", duplicate, err)
	}

	tracker := NewDeliveryTracker()
	if err := tracker.Observe("d1", "m1", 9); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Transition("d1", DeliveryReceived, ""); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Transition("d1", DeliveryProcessed, ""); err != nil {
		t.Fatal(err)
	}
	record, _ := tracker.Get("d1")
	if record.TransportAckID != "" || record.TransportAckedAt != nil {
		t.Fatalf("processed fabricated acknowledgement: %+v", record)
	}
	if err := tracker.Acknowledge("d1", "ack-1"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Acknowledge("d1", "ack-2"); err == nil {
		t.Fatal("conflicting acknowledgement accepted")
	}
	if err := tracker.Transition("d1", DeliveryFailed, "late"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func TestLifecycleAndWorkOwnershipTransitions(t *testing.T) {
	lifecycle := NewLifecycle()
	if err := lifecycle.Transition(TransportConnected, "skip connect"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition = %v", err)
	}
	if err := lifecycle.Transition(TransportConnecting, "start"); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Transition(TransportConnected, "ready"); err != nil {
		t.Fatal(err)
	}

	work, err := NewWorkLifecycle("work-1")
	if err != nil {
		t.Fatal(err)
	}
	if work.Owned() {
		t.Fatal("observed work was treated as owned")
	}
	for _, state := range []WorkState{WorkSecretValidated, WorkAcknowledging, WorkOwned} {
		if err := work.Transition(state); err != nil {
			t.Fatal(err)
		}
	}
	if !work.Owned() {
		t.Fatal("acknowledged work was not marked owned")
	}
}
