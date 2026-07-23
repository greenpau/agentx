// Package compact models context pressure and summary projection without
// mutating the authoritative transcript.
package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	DefaultReserveCap   = 20_000
	AutoSafetyTokens    = 13_000
	WarningSafetyTokens = 20_000
	HardSafetyTokens    = 3_000
	MaxAutoFailures     = 3
)

type Limits struct {
	ContextWindow   int
	MaxOutputTokens int
}

type Thresholds struct {
	Effective int
	Warning   int
	Auto      int
	Hard      int
}

func ComputeThresholds(limits Limits) (Thresholds, error) {
	if limits.ContextWindow <= 0 || limits.MaxOutputTokens <= 0 {
		return Thresholds{}, errors.New("context and output limits must be positive")
	}
	reserve := limits.MaxOutputTokens
	if reserve > DefaultReserveCap {
		reserve = DefaultReserveCap
	}
	effective := limits.ContextWindow - reserve
	if effective <= HardSafetyTokens {
		return Thresholds{}, errors.New("model context window is too small")
	}
	clamp := func(value int) int {
		if value < 0 {
			return 0
		}
		return value
	}
	return Thresholds{Effective: effective, Warning: clamp(effective - WarningSafetyTokens), Auto: clamp(effective - AutoSafetyTokens), Hard: clamp(effective - HardSafetyTokens)}, nil
}

type Level string

const (
	LevelOK      Level = "ok"
	LevelWarning Level = "warning"
	LevelAuto    Level = "auto_compact"
	LevelHard    Level = "hard_limit"
)

func (t Thresholds) Level(tokens int) Level {
	// Hard is closest to the effective ceiling and therefore checked first.
	if tokens >= t.Hard {
		return LevelHard
	}
	if tokens >= t.Auto {
		return LevelAuto
	}
	if tokens >= t.Warning {
		return LevelWarning
	}
	return LevelOK
}

type Message struct {
	Role    string
	Content string
}

type Summary struct {
	Text                 string
	Preserved            []Message
	OriginalMessageCount int
	EstimatedTokens      int
}

type Summarizer interface {
	Summarize(context.Context, []Message) (string, error)
}

type Controller struct {
	// operationMu keeps independent callers from summarizing overlapping
	// projections against the same controller generation. The engine already
	// serializes turns, but Controller is a public package boundary and must
	// preserve that invariant on its own.
	operationMu sync.Mutex
	mu          sync.Mutex
	failures    int
}

var ErrCircuitOpen = errors.New("automatic compaction disabled after repeated failures")

// Compact invokes an isolated, no-tool summarizer supplied by the caller. The
// newest preserve messages remain verbatim after the summary boundary. It is
// the automatic path and therefore observes and updates the failure circuit.
func (c *Controller) Compact(ctx context.Context, summarizer Summarizer, messages []Message, preserve int) (Summary, error) {
	return c.compact(ctx, summarizer, messages, preserve, true)
}

// CompactManual is the explicit recovery path. It remains available after the
// automatic circuit opens, does not count a failed manual attempt against that
// circuit, and resets the circuit after a successful projection.
func (c *Controller) CompactManual(ctx context.Context, summarizer Summarizer, messages []Message, preserve int) (Summary, error) {
	return c.compact(ctx, summarizer, messages, preserve, false)
}

func (c *Controller) compact(ctx context.Context, summarizer Summarizer, messages []Message, preserve int, automatic bool) (Summary, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if summarizer == nil {
		return Summary{}, errors.New("summarizer is nil")
	}
	c.mu.Lock()
	if automatic && c.failures >= MaxAutoFailures {
		c.mu.Unlock()
		return Summary{}, ErrCircuitOpen
	}
	c.mu.Unlock()
	if preserve < 0 {
		preserve = 0
	}
	if preserve > len(messages) {
		preserve = len(messages)
	}
	cut := len(messages) - preserve
	if cut == 0 {
		return Summary{}, errors.New("nothing to compact")
	}
	text, err := summarizer.Summarize(ctx, append([]Message(nil), messages[:cut]...))
	if err != nil || strings.TrimSpace(text) == "" {
		if automatic {
			c.mu.Lock()
			c.failures++
			c.mu.Unlock()
		}
		if err == nil {
			err = errors.New("summarizer returned empty output")
		}
		return Summary{}, fmt.Errorf("compact context: %w", err)
	}
	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
	return Summary{Text: strings.TrimSpace(text), Preserved: append([]Message(nil), messages[cut:]...), OriginalMessageCount: len(messages), EstimatedTokens: EstimateTokens(text)}, nil
}

func (c *Controller) Reset() {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
}

// EstimateTokens is deliberately conservative and provider-neutral. Exact
// usage reported by Azure supersedes it after a response.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len([]byte(text)) + 2) / 3
}
