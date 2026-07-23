package distributed

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestControlCorrelationIsGenerationAndEpochFenced(t *testing.T) {
	registry := NewControlRegistry()
	key := ControlKey{Request: "request-1", Session: "session-1", Generation: 4, Epoch: 2}
	if err := registry.Accept(key, "can_use_tool", "tool-1"); err != nil {
		t.Fatal(err)
	}
	stale := key
	stale.Generation = 3
	if err := registry.Resolve(stale, ControlSucceeded, json.RawMessage(`{"ok":true}`), ""); !errors.Is(err, ErrControlNotFound) {
		t.Fatalf("stale response = %v", err)
	}
	if err := registry.Advance(key, ControlValidating); err != nil {
		t.Fatal(err)
	}
	if err := registry.Advance(key, ControlAwaitingLocal); err != nil {
		t.Fatal(err)
	}
	if err := registry.Cancel(key, "remote cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Resolve(key, ControlSucceeded, json.RawMessage(`{"ok":true}`), ""); !errors.Is(err, ErrControlTerminal) {
		t.Fatalf("late success = %v", err)
	}
	record, _ := registry.Get(key)
	if record.State != ControlCancelled {
		t.Fatalf("terminal state overwritten: %+v", record)
	}
}

func TestControlSuccessRequiresEvidenceAndCancelAllSettlesKnownWaiters(t *testing.T) {
	registry := NewControlRegistry()
	for index, request := range []ControlRequestID{"one", "two"} {
		key := ControlKey{Request: request, Session: "session", Generation: 1, Epoch: 1}
		if err := registry.Accept(key, "interrupt", ""); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if err := registry.Resolve(key, ControlSucceeded, nil, ""); err == nil {
				t.Fatal("success without result evidence accepted")
			}
		}
	}
	cancelled := registry.CancelAll("disconnect")
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	for _, record := range cancelled {
		if record.State != ControlCancelled || record.Message != "disconnect" {
			t.Fatalf("cancel record = %+v", record)
		}
	}
}
