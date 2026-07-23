package observability

import (
	"math"
	"testing"
)

func TestUsageLedgerCountsCumulativeMessageOnceAndPreservesUnknownCost(t *testing.T) {
	ledger := NewUsageLedger()
	knownZero, err := KnownCost(0)
	if err != nil {
		t.Fatal(err)
	}
	added, err := ledger.Complete("message-a", "gpt-5.6-sol", Usage{InputTokens: 10, OutputTokens: 25}, knownZero)
	if err != nil || !added {
		t.Fatalf("first completion = %v, %v", added, err)
	}
	added, err = ledger.Complete("message-a", "gpt-5.6-sol", Usage{InputTokens: 10, OutputTokens: 55}, knownZero)
	if err != nil || added {
		t.Fatalf("duplicate completion = %v, %v", added, err)
	}
	if _, err := ledger.Complete("message-b", "unknown-priced-model", Usage{OutputTokens: 3}, UnknownCost()); err != nil {
		t.Fatal(err)
	}
	snapshot := ledger.Snapshot()
	if snapshot.Overall.Usage.OutputTokens != 28 || snapshot.Overall.CompletedMessages != 2 {
		t.Fatalf("overall usage = %+v", snapshot.Overall)
	}
	if snapshot.Overall.Cost.Known {
		t.Fatalf("unknown aggregate cost became known zero: %+v", snapshot.Overall.Cost)
	}
	knownModel := snapshot.Models["gpt-5.6-sol"]
	if !knownModel.Cost.Known || knownModel.Cost.USDMicros != 0 {
		t.Fatalf("known zero cost was lost: %+v", knownModel.Cost)
	}
}

func TestUsageLedgerRejectsOverflowWithoutConsumingIdentity(t *testing.T) {
	ledger := NewUsageLedger()
	cost, err := KnownCost(math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := ledger.Complete("message-a", "gpt-5.6-sol", Usage{InputTokens: math.MaxInt64}, cost); err != nil || !added {
		t.Fatalf("initial maximum usage = %v, %v", added, err)
	}
	oneMicro, err := KnownCost(1)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := ledger.Complete("message-cost", "gpt-5.6-sol", Usage{}, oneMicro); err == nil || added {
		t.Fatalf("overflowing cost = %v, %v", added, err)
	}
	if added, err := ledger.Complete("message-b", "gpt-5.6-sol", Usage{InputTokens: 1}, UnknownCost()); err == nil || added {
		t.Fatalf("overflowing usage = %v, %v", added, err)
	}
	if added, err := ledger.Complete("message-b", "other-model", Usage{InputTokens: 1}, UnknownCost()); err == nil || added {
		// Overall accounting still overflows because message-a consumed the
		// maximum input-token range; changing model cannot bypass that ledger.
		t.Fatalf("cross-model overflow = %v, %v", added, err)
	}
	snapshot := ledger.Snapshot()
	if snapshot.Overall.CompletedMessages != 1 || snapshot.Overall.Usage.InputTokens != math.MaxInt64 {
		t.Fatalf("rejected completion mutated ledger: %+v", snapshot.Overall)
	}
}
