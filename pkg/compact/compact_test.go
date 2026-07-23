package compact

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeSummarizer struct {
	text string
	err  error
}

func (f fakeSummarizer) Summarize(context.Context, []Message) (string, error) { return f.text, f.err }

func TestThresholdOrdering(t *testing.T) {
	thresholds, err := ComputeThresholds(Limits{ContextWindow: 1_050_000, MaxOutputTokens: 128_000})
	if err != nil {
		t.Fatal(err)
	}
	if thresholds.Effective != 1_030_000 || thresholds.Warning != 1_010_000 || thresholds.Auto != 1_017_000 || thresholds.Hard != 1_027_000 {
		t.Fatalf("thresholds=%#v", thresholds)
	}
	if thresholds.Level(1_028_000) != LevelHard || thresholds.Level(1_018_000) != LevelAuto {
		t.Fatal("wrong levels")
	}
}

func TestCompactionPreservesTailAndCircuitBreaks(t *testing.T) {
	c := &Controller{}
	messages := []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}
	result, err := c.Compact(context.Background(), fakeSummarizer{text: "summary"}, messages, 1)
	if err != nil || len(result.Preserved) != 1 || result.Preserved[0].Content != "c" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for i := 0; i < MaxAutoFailures; i++ {
		_, _ = c.Compact(context.Background(), fakeSummarizer{err: errors.New("no")}, messages, 1)
	}
	if _, err := c.Compact(context.Background(), fakeSummarizer{text: "x"}, messages, 1); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err=%v", err)
	}
}

func TestManualCompactionRemainsAvailableAndRecoversAutomaticCircuit(t *testing.T) {
	c := &Controller{}
	messages := []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
	for i := 0; i < MaxAutoFailures; i++ {
		_, _ = c.Compact(context.Background(), fakeSummarizer{err: errors.New("no")}, messages, 1)
	}
	if _, err := c.Compact(context.Background(), fakeSummarizer{text: "automatic"}, messages, 1); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("automatic err=%v", err)
	}
	if _, err := c.CompactManual(context.Background(), fakeSummarizer{text: "manual"}, messages, 1); err != nil {
		t.Fatalf("manual recovery: %v", err)
	}
	if _, err := c.Compact(context.Background(), fakeSummarizer{text: "automatic"}, messages, 1); err != nil {
		t.Fatalf("automatic after manual recovery: %v", err)
	}
}

type serialSummarizer struct {
	mu        sync.Mutex
	active    int
	maxActive int
	release   <-chan struct{}
}

func (s *serialSummarizer) Summarize(ctx context.Context, _ []Message) (string, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return "summary", nil
}

func TestControllerSerializesConcurrentSummarizers(t *testing.T) {
	release := make(chan struct{})
	summarizer := &serialSummarizer{release: release}
	c := &Controller{}
	messages := []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.Compact(context.Background(), summarizer, messages, 1)
			results <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	summarizer.mu.Lock()
	maxActive := summarizer.maxActive
	summarizer.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum concurrent summarizers=%d", maxActive)
	}
}

func TestNilSummarizerFailsClosed(t *testing.T) {
	if _, err := (&Controller{}).Compact(context.Background(), nil, []Message{{Content: "a"}}, 0); err == nil {
		t.Fatal("expected nil summarizer error")
	}
}
