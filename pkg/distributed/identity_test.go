package distributed

import (
	"errors"
	"testing"
)

func TestCursorAndEpochAreObservationAndFencingEvidence(t *testing.T) {
	cursor := Cursor{}
	first, err := cursor.Observe(4)
	if err != nil || !first.Advanced || first.Gap {
		t.Fatalf("first observation = %+v, %v", first, err)
	}
	// Payload decoding could fail here; observed high-water intentionally stays 4.
	duplicate, err := cursor.Observe(4)
	if err != nil || !duplicate.Duplicate || duplicate.Advanced {
		t.Fatalf("duplicate observation = %+v, %v", duplicate, err)
	}
	gap, err := cursor.Observe(7)
	if err != nil || !gap.Gap || cursor.Sequence != 7 {
		t.Fatalf("gap observation = %+v cursor=%+v err=%v", gap, cursor, err)
	}
	if _, err := cursor.Observe(0); !errors.Is(err, ErrInvalidSequence) {
		t.Fatalf("zero sequence error = %v", err)
	}

	fence, err := NewEpochFence(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Check(1); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale check = %v", err)
	}
	if err := fence.Advance(2); !errors.Is(err, ErrEpochNotAdvanced) {
		t.Fatalf("same epoch advance = %v", err)
	}
	if err := fence.Advance(3); err != nil || fence.Current() != 3 {
		t.Fatalf("advance = %v current=%d", err, fence.Current())
	}
}

func TestIdentityValidationDoesNotNormalizeAcrossClasses(t *testing.T) {
	identity := IdentityTuple{Session: RemoteSessionID("cse_same"), Epoch: 1, Generation: 1, Work: WorkID("cse_same")}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePathIdentifier("work ID", "unsafe/id"); err == nil {
		t.Fatal("unsafe path identifier accepted")
	}
}

func TestIdentityTupleValidatesEveryOptionalIdentityAxis(t *testing.T) {
	identity := IdentityTuple{
		BridgeInstance: BridgeInstanceID("bridge\nspoof"),
		Session:        RemoteSessionID("session"),
		Epoch:          1,
		Generation:     1,
	}
	if err := identity.Validate(); err == nil {
		t.Fatal("expected unsafe bridge instance identity to be rejected")
	}
	identity.BridgeInstance = BridgeInstanceID("bridge-1")
	identity.Environment = EnvironmentID("environment\x00spoof")
	if err := identity.Validate(); err == nil {
		t.Fatal("expected unsafe environment identity to be rejected")
	}
	identity.Environment = EnvironmentID("environment-1")
	identity.Work = WorkID("work\rspoof")
	if err := identity.Validate(); err == nil {
		t.Fatal("expected unsafe work identity to be rejected")
	}
}
