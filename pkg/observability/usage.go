package observability

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

// Usage is one completed provider message's final cumulative snapshot.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	WebSearchRequests int64 `json:"web_search_requests"`
}

func (u Usage) valid() bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CacheReadTokens >= 0 && u.CacheCreateTokens >= 0 && u.WebSearchRequests >= 0
}

// Cost stores millionths of a US dollar to avoid floating-point aggregation.
// Known=false is not equivalent to a known zero price.
type Cost struct {
	Known     bool  `json:"known"`
	USDMicros int64 `json:"usd_micros,omitempty"`
}

func UnknownCost() Cost { return Cost{} }

func KnownCost(usdMicros int64) (Cost, error) {
	if usdMicros < 0 {
		return Cost{}, errors.New("cost must not be negative")
	}
	return Cost{Known: true, USDMicros: usdMicros}, nil
}

type ModelUsage struct {
	Usage             Usage `json:"usage"`
	Cost              Cost  `json:"cost"`
	CompletedMessages int64 `json:"completed_messages"`
}

type UsageSnapshot struct {
	Overall ModelUsage            `json:"overall"`
	Models  map[string]ModelUsage `json:"models"`
}

type usageAccumulator struct {
	usage       Usage
	knownMicros int64
	unknownCost bool
	messages    int64
}

func (a usageAccumulator) snapshot() ModelUsage {
	cost := Cost{}
	if a.messages > 0 && !a.unknownCost {
		cost = Cost{Known: true, USDMicros: a.knownMicros}
	}
	return ModelUsage{Usage: a.usage, Cost: cost, CompletedMessages: a.messages}
}

// UsageLedger is authoritative local accounting and is independent of every
// exporter. Completing the same provider message twice is idempotent.
type UsageLedger struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	overall usageAccumulator
	models  map[string]usageAccumulator
}

func NewUsageLedger() *UsageLedger {
	return &UsageLedger{seen: make(map[string]struct{}), models: make(map[string]usageAccumulator)}
}

// Complete adds a completed message's final cumulative usage exactly once.
// It returns false for an already-accounted message.
func (l *UsageLedger) Complete(messageID, model string, usage Usage, cost Cost) (bool, error) {
	if messageID == "" || model == "" {
		return false, errors.New("usage completion requires message and model identity")
	}
	if !usage.valid() || (cost.Known && cost.USDMicros < 0) {
		return false, errors.New("usage or cost contains a negative value")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.seen[messageID]; exists {
		return false, nil
	}
	nextOverall, err := addUsage(l.overall, usage, cost)
	if err != nil {
		return false, fmt.Errorf("aggregate overall usage: %w", err)
	}
	nextModel, err := addUsage(l.models[model], usage, cost)
	if err != nil {
		return false, fmt.Errorf("aggregate model usage: %w", err)
	}
	l.seen[messageID] = struct{}{}
	l.overall = nextOverall
	l.models[model] = nextModel
	return true, nil
}

func addUsage(accumulator usageAccumulator, usage Usage, cost Cost) (usageAccumulator, error) {
	add := func(name string, left, right int64) (int64, error) {
		if left < 0 || right < 0 || left > math.MaxInt64-right {
			return 0, fmt.Errorf("%s overflows", name)
		}
		return left + right, nil
	}
	next := accumulator
	var err error
	if next.usage.InputTokens, err = add("input tokens", accumulator.usage.InputTokens, usage.InputTokens); err != nil {
		return accumulator, err
	}
	if next.usage.OutputTokens, err = add("output tokens", accumulator.usage.OutputTokens, usage.OutputTokens); err != nil {
		return accumulator, err
	}
	if next.usage.CacheReadTokens, err = add("cache-read tokens", accumulator.usage.CacheReadTokens, usage.CacheReadTokens); err != nil {
		return accumulator, err
	}
	if next.usage.CacheCreateTokens, err = add("cache-create tokens", accumulator.usage.CacheCreateTokens, usage.CacheCreateTokens); err != nil {
		return accumulator, err
	}
	if next.usage.WebSearchRequests, err = add("web-search requests", accumulator.usage.WebSearchRequests, usage.WebSearchRequests); err != nil {
		return accumulator, err
	}
	if next.messages, err = add("completed messages", accumulator.messages, 1); err != nil {
		return accumulator, err
	}
	if cost.Known {
		if next.knownMicros, err = add("known cost", accumulator.knownMicros, cost.USDMicros); err != nil {
			return accumulator, err
		}
	} else {
		next.unknownCost = true
	}
	return next, nil
}

func (l *UsageLedger) Snapshot() UsageSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	models := make(map[string]ModelUsage, len(l.models))
	for model, usage := range l.models {
		models[model] = usage.snapshot()
	}
	return UsageSnapshot{Overall: l.overall.snapshot(), Models: models}
}
