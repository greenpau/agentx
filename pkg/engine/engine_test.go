package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/transcript"
)

type fakeProvider struct {
	mu        sync.Mutex
	responses [][]model.Event
	requests  []model.Request
}

type failingProvider struct{ err error }

func (p failingProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, p.err
}

func (p *fakeProvider) Stream(_ context.Context, request model.Request) (model.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return nil, errors.New("no response")
	}
	events := p.responses[0]
	p.responses = p.responses[1:]
	return &fakeStream{events: events}, nil
}

type fakeStream struct {
	events []model.Event
	index  int
}

func (s *fakeStream) Next() (model.Event, error) {
	if s.index >= len(s.events) {
		return model.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (s *fakeStream) Close() error { return nil }

type fakeCapabilities struct {
	results []CapabilityResult
	calls   []CapabilityCall
}

type faultStore struct {
	mu                    sync.Mutex
	events                []protocol.Event
	sequence              uint64
	failToolCallID        protocol.ToolUseID
	failToolCallRemaining int
	failResultID          protocol.ToolUseID
	failResultRemaining   int
}

func (s *faultStore) AppendEvent(_ context.Context, event protocol.Event) (protocol.Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Kind == protocol.EventKindToolCall && event.ToolCall.ID == s.failToolCallID && s.failToolCallRemaining > 0 {
		s.failToolCallRemaining--
		return protocol.Event{}, false, errors.New("injected call append failure")
	}
	if event.Kind == protocol.EventKindToolResult && event.ToolResult.ToolUseID == s.failResultID && s.failResultRemaining > 0 {
		s.failResultRemaining--
		return protocol.Event{}, false, errors.New("injected result append failure")
	}
	s.sequence++
	event.Sequence = s.sequence
	s.events = append(s.events, event)
	return event, true, nil
}
func (s *faultStore) Flush(context.Context) error { return nil }

type ambiguousAcceptanceStore struct {
	mu        sync.Mutex
	events    []protocol.Event
	sequence  uint64
	callID    protocol.ToolUseID
	callTries int
}

func (s *ambiguousAcceptanceStore) AppendEvent(_ context.Context, event protocol.Event) (protocol.Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Kind == protocol.EventKindToolCall && event.ToolCall.ID == s.callID {
		s.callTries++
		if s.callTries == 1 {
			s.sequence++
			event.Sequence = s.sequence
			s.events = append(s.events, event)
			return event, true, errors.New("injected acknowledgement failure after append")
		}
		return event, false, errors.New("injected retry failure")
	}
	s.sequence++
	event.Sequence = s.sequence
	s.events = append(s.events, event)
	return event, true, nil
}

func (s *ambiguousAcceptanceStore) Flush(context.Context) error { return nil }

type boundedBatchSettlementStore struct {
	mu        sync.Mutex
	events    []protocol.Event
	sequence  uint64
	blocked   map[protocol.ToolUseID]struct{}
	attempts  map[protocol.ToolUseID]int
	deadlines map[protocol.ToolUseID][]time.Time
}

func (s *boundedBatchSettlementStore) AppendEvent(ctx context.Context, event protocol.Event) (protocol.Event, bool, error) {
	if event.Kind == protocol.EventKindToolResult {
		toolID := event.ToolResult.ToolUseID
		s.mu.Lock()
		s.attempts[toolID]++
		if deadline, ok := ctx.Deadline(); ok {
			s.deadlines[toolID] = append(s.deadlines[toolID], deadline)
		}
		_, blocked := s.blocked[toolID]
		s.mu.Unlock()
		if blocked {
			<-ctx.Done()
			return protocol.Event{}, false, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return protocol.Event{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	event.Sequence = s.sequence
	s.events = append(s.events, event)
	return event, true, nil
}

func (s *boundedBatchSettlementStore) Flush(context.Context) error { return nil }

type finalizationFaultStore struct {
	appendErr error
	flushErr  error
}

func (s *finalizationFaultStore) AppendEvent(context.Context, protocol.Event) (protocol.Event, bool, error) {
	return protocol.Event{}, false, s.appendErr
}

func (s *finalizationFaultStore) Flush(context.Context) error { return s.flushErr }

type toolCallFailingSink struct{}

func (toolCallFailingSink) Publish(_ context.Context, event protocol.Event) error {
	if event.Kind == protocol.EventKindToolCall {
		return errors.New("output disconnected")
	}
	return nil
}

type capturingSink struct {
	events []protocol.Event
}

func (s *capturingSink) Publish(_ context.Context, event protocol.Event) error {
	s.events = append(s.events, event)
	return nil
}

type blockingProvider struct {
	entered chan struct{}
	release chan struct{}
	events  []model.Event
	once    sync.Once
}

func (p *blockingProvider) Stream(ctx context.Context, _ model.Request) (model.Stream, error) {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return &fakeStream{events: append([]model.Event(nil), p.events...)}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type gatedCapabilities struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *gatedCapabilities) Schemas() []model.Tool {
	return []model.Tool{{Name: "Read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`), Strict: true}}
}

func (c *gatedCapabilities) Execute(ctx context.Context, calls []CapabilityCall) []CapabilityResult {
	c.once.Do(func() { close(c.entered) })
	select {
	case <-c.release:
		return []CapabilityResult{{ID: calls[0].ID, Name: calls[0].Name, Status: protocol.ToolResultSuccess, Content: "ok"}}
	case <-ctx.Done():
		return []CapabilityResult{{ID: calls[0].ID, Name: calls[0].Name, Status: protocol.ToolResultCancelled, Content: "cancelled", IsError: true}}
	}
}

func (f *fakeCapabilities) Schemas() []model.Tool {
	return []model.Tool{{Name: "Read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`), Strict: true}}
}
func (f *fakeCapabilities) Execute(_ context.Context, calls []CapabilityCall) []CapabilityResult {
	f.calls = append(f.calls, calls...)
	return append([]CapabilityResult(nil), f.results...)
}

func completed(output []model.Item, usage model.Usage) []model.Event {
	return []model.Event{{Type: model.EventResponseCompleted, Response: &model.Response{ID: "resp", Status: "completed", Output: output, Usage: usage}}}
}

func newTestEngine(t *testing.T, provider model.Provider, capabilities Capabilities) (*Engine, *transcript.Store) {
	t.Helper()
	session := protocol.SessionID("ses_test")
	store, err := transcript.Open(context.Background(), transcript.Config{Path: filepath.Join(t.TempDir(), "session.jsonl"), SessionID: session, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(Config{SessionID: session, Model: "gpt-5.6-sol", ReasoningEffort: "high", Instructions: "test", Provider: provider, Capabilities: capabilities, Transcript: store, MaxTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func TestLocalMetadataCommandsDoNotMaterializeEmptySession(t *testing.T) {
	query, store := newTestEngine(t, &fakeProvider{}, &fakeCapabilities{})
	defer store.Close()
	if err := query.ClearContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := query.SetReasoningEffort(t.Context(), "xhigh"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata-only command materialized a resumable transcript: %v", err)
	}
}

func TestNewRejectsInvalidReasoningEffort(t *testing.T) {
	if _, err := New(Config{
		SessionID: "ses_invalid_effort", Model: "gpt-5.6-sol", ReasoningEffort: "ultra",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
	}); err == nil {
		t.Fatal("invalid initial reasoning effort was accepted")
	}
}

func TestEnginePreTurnErrorsDoNotExposeConfiguredCredentialValues(t *testing.T) {
	assertSafeError := func(t *testing.T, err error, secret string) {
		t.Helper()
		if err == nil {
			t.Fatal("expected error")
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
			if rendered := fmt.Sprintf(format, err); strings.Contains(rendered, secret) {
				t.Fatalf("format %s exposed configured credential: %q", format, rendered)
			}
		}
	}

	t.Run("initial effort", func(t *testing.T) {
		const secret = "invalid-production-effort"
		_, err := New(Config{
			SessionID: "ses_initial_effort_guard", Model: "gpt-5.6-sol", ReasoningEffort: secret,
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
			CredentialSanitizer: redact.New(secret),
		})
		assertSafeError(t, err, secret)
	})

	t.Run("unknown model", func(t *testing.T) {
		const secret = "credential-shaped-model"
		_, err := New(Config{
			SessionID: "ses_unknown_model_guard", Model: secret,
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
			CredentialSanitizer: redact.New(secret),
		})
		assertSafeError(t, err, secret)
	})

	t.Run("split identity", func(t *testing.T) {
		const (
			session = "ses_identity_prefix"
			model   = "gpt-5.6-sol"
			secret  = session + model
		)
		_, err := New(Config{
			SessionID: session, Model: model,
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
			CredentialSanitizer: redact.New(secret),
		})
		assertSafeError(t, err, secret)
	})

	query, err := New(Config{
		SessionID: "ses_pre_turn_guard", Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
		CredentialSanitizer: redact.New("max", "snapshot-secret", "prompt-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("live effort", func(t *testing.T) {
		assertSafeError(t, query.SetReasoningEffort(t.Context(), "max"), "max")
	})
	t.Run("restore mismatch", func(t *testing.T) {
		assertSafeError(t, query.Restore(transcript.Snapshot{SessionID: "snapshot-secret"}), "snapshot-secret")
	})
	t.Run("duplicate prompt", func(t *testing.T) {
		query.promptMu.Lock()
		query.promptIDs["prompt-secret"] = struct{}{}
		query.promptMu.Unlock()
		_, err := query.SubmitPrompt(t.Context(), "must not run", "prompt-secret")
		if !errors.Is(err, ErrDuplicatePrompt) {
			t.Fatalf("duplicate prompt lost classification: %v", err)
		}
		assertSafeError(t, err, "prompt-secret")
	})
}

func TestEngineStatusSuppressesCredentialReconstructionAcrossFields(t *testing.T) {
	const secret = "status-split-secret"
	query, err := New(Config{
		SessionID: "ses_status_guard", Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	query.statusMu.Lock()
	query.status.Model = "status-split-"
	query.status.ReasoningEffort = "secret"
	query.status.Usage.Model = secret
	query.statusMu.Unlock()

	status := query.Status()
	for _, value := range []string{string(status.SessionID), status.Model, status.ReasoningEffort, status.Usage.Model} {
		if strings.Contains(value, secret) {
			t.Fatalf("status exposed credential in a direct field: %#v", status)
		}
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("status JSON exposed credential: %s", encoded)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if rendered := fmt.Sprintf(format, status); strings.Contains(rendered, secret) {
			t.Fatalf("status format %s exposed credential: %q", format, rendered)
		}
	}
}

func TestEngineContainsPanickingCustomSanitizer(t *testing.T) {
	query, err := New(Config{
		SessionID: "ses_panicking_sanitizer", Model: "gpt-5.6-sol", Instructions: "private instructions",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
		Sanitize: func(string) string { panic("untrusted sanitizer panic") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if query.config.Instructions != "" {
		t.Fatalf("panicking sanitizer retained instructions: %q", query.config.Instructions)
	}
	if status := query.Status(); status.SessionID != "" || status.Model != "" || status.Usage.Model != "" {
		t.Fatalf("panicking sanitizer retained public status: %#v", status)
	}
	if _, err := query.Submit(t.Context(), "private prompt"); err == nil || err.Error() != "empty user input" {
		t.Fatalf("panicking sanitizer submit error = %v", err)
	}
}

func TestOutcomeMeasuresModelAPITimeSeparatelyFromTotalTurnTime(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	times := []time.Time{
		base,
		base.Add(100 * time.Millisecond),
		base.Add(850 * time.Millisecond),
		base.Add(1200 * time.Millisecond),
	}
	clockIndex := 0
	clock := func() time.Time {
		if clockIndex >= len(times) {
			t.Fatalf("engine clock read %d times, want at most %d", clockIndex+1, len(times))
		}
		value := times[clockIndex]
		clockIndex++
		return value
	}
	query, err := New(Config{
		SessionID: "ses_api_duration", Model: "gpt-5.6-sol", ReasoningEffort: "high",
		Provider:     &fakeProvider{responses: [][]model.Event{completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{})}},
		Capabilities: &fakeCapabilities{}, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "measure")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Duration != 1200*time.Millisecond || outcome.APIDuration != 750*time.Millisecond {
		t.Fatalf("durations = total %s, API %s", outcome.Duration, outcome.APIDuration)
	}
}

func TestEngineClockBoundaryContainsPanicZeroAndReentry(t *testing.T) {
	for _, test := range []struct {
		name  string
		clock func() time.Time
	}{
		{name: "panic", clock: func() time.Time { panic("clock unavailable") }},
		{name: "zero", clock: func() time.Time { return time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, err := New(Config{
				SessionID: protocol.SessionID("ses_clock_" + test.name), Model: "gpt-5.6-sol", ReasoningEffort: "high",
				Provider: &fakeProvider{responses: [][]model.Event{
					completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
				}},
				Capabilities: &fakeCapabilities{},
				Now:          test.clock,
			})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := query.Submit(t.Context(), "clock boundary")
			if err != nil || outcome.Status != protocol.TurnResultSuccess || outcome.Duration < 0 || outcome.APIDuration < 0 {
				t.Fatalf("Submit = outcome %#v err %v", outcome, err)
			}
		})
	}

	var query *Engine
	var calls atomic.Int32
	var clearErr error
	var submitErr error
	base := time.Unix(1_700_000_000, 0)
	clock := func() time.Time {
		call := calls.Add(1)
		switch call {
		case 1:
			clearErr = query.ClearContext(context.Background())
		case 2:
			_, submitErr = query.Submit(context.Background(), "reentrant")
		}
		return base.Add(time.Duration(call) * time.Second)
	}
	var err error
	query, err = New(Config{
		SessionID: "ses_clock_reentry", Model: "gpt-5.6-sol", ReasoningEffort: "high",
		Provider: &fakeProvider{responses: [][]model.Event{
			completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
		}},
		Capabilities: &fakeCapabilities{},
		Now:          clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		outcome Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, submitErr := query.Submit(t.Context(), "outer")
		done <- result{outcome: outcome, err: submitErr}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.outcome.Status != protocol.TurnResultSuccess {
			t.Fatalf("outer Submit = outcome %#v err %v", got.outcome, got.err)
		}
		if got.outcome.Duration != 3*time.Second || got.outcome.APIDuration != time.Second {
			t.Fatalf("durations = total %s API %s", got.outcome.Duration, got.outcome.APIDuration)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant engine clock deadlocked the serialized turn")
	}
	if !errors.Is(clearErr, ErrBusy) || !errors.Is(submitErr, ErrBusy) {
		t.Fatalf("reentrant results = ClearContext %v Submit %v, want ErrBusy", clearErr, submitErr)
	}
}

func TestModelToolContinuationAndDurability(t *testing.T) {
	reasoning := model.Item{Type: model.ItemReasoning, ID: "reason_1", EncryptedContent: "opaque"}
	call := model.FunctionCall("fc_1", "call_1", "Read", `{"file_path":"x"}`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{reasoning, model.TextMessage(model.RoleAssistant, "checking"), call}, model.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}), completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{InputTokens: 20, OutputTokens: 2, TotalTokens: 22})}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{ID: "call_1", Name: "Read", Status: protocol.ToolResultSuccess, Content: "contents"}}}
	engine, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	outcome, err := engine.Submit(context.Background(), "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "done" || outcome.ModelTurns != 2 || len(capabilities.calls) != 1 {
		t.Fatalf("outcome=%#v calls=%#v", outcome, capabilities.calls)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	second := provider.requests[1].Input
	foundReasoning, foundOutput := false, false
	for _, item := range second {
		if item.Type == model.ItemReasoning && item.EncryptedContent == "opaque" {
			foundReasoning = true
		}
		if item.Type == model.ItemFunctionCallOutput && item.CallID == "call_1" && item.Output == "contents" {
			foundOutput = true
		}
	}
	if !foundReasoning || !foundOutput {
		t.Fatalf("continuation lost: %#v", second)
	}
	snapshot, err := store.LoadAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil || len(pairs) != 1 || pairs[0].Result.ToolResult.Status != protocol.ToolResultSuccess {
		t.Fatalf("pairs=%#v err=%v", pairs, err)
	}
}

func TestCredentialSuppressionSurvivesContinuationPersistenceAndRestore(t *testing.T) {
	call := model.FunctionCall("fc_suppressed", "call_suppressed", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{
		ID: "call_suppressed", Name: "Read", Status: protocol.ToolResultSuccess,
		Content: "adapter content must be discarded", ContentSuppressed: true,
	}}}
	query, store := newTestEngine(t, provider, capabilities)
	defer store.Close()

	if _, err := query.Submit(t.Context(), "exercise suppression"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	foundContinuation := false
	for _, item := range provider.requests[1].Input {
		if item.Type == model.ItemFunctionCallOutput && item.CallID == "call_suppressed" {
			foundContinuation = true
			if item.Output != "" {
				t.Fatalf("suppressed continuation output = %q", item.Output)
			}
		}
	}
	if !foundContinuation {
		t.Fatal("suppressed result was not paired in the model continuation")
	}

	snapshot, err := store.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil || len(pairs) != 1 {
		t.Fatalf("durable pairs = %#v, %v", pairs, err)
	}
	if content := blockText(pairs[0].Result.ToolResult.Content); content != "" {
		t.Fatalf("suppressed durable content = %q", content)
	}

	restored, err := New(Config{
		SessionID: snapshot.SessionID, Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	foundRestored := false
	for _, item := range restored.history {
		if item.Type == model.ItemFunctionCallOutput && item.CallID == "call_suppressed" {
			foundRestored = true
			if item.Output != "" {
				t.Fatalf("restored suppressed output = %q", item.Output)
			}
		}
	}
	if !foundRestored {
		t.Fatal("restore lost the suppressed tool-result pairing")
	}
}

func TestCapabilityCompletionOrderSurvivesContinuationPersistenceAndPairing(t *testing.T) {
	slow := model.FunctionCall("fc_slow", "call_slow", "Read", `{}`)
	fast := model.FunctionCall("fc_fast", "call_fast", "Read", `{}`)
	missing := model.FunctionCall("fc_missing", "call_missing", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{slow, fast, missing}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{
		{ID: "call_fast", Name: "Read", Status: protocol.ToolResultSuccess, Content: "fast completed"},
		{ID: "call_unknown", Name: "Read", Status: protocol.ToolResultSuccess, Content: "must be ignored"},
		{ID: "call_fast", Name: "Read", Status: protocol.ToolResultSuccess, Content: "duplicate must be ignored"},
		{ID: "call_slow", Name: "Read", Status: protocol.ToolResultSuccess, Content: "slow completed"},
	}}
	query, store := newTestEngine(t, provider, capabilities)
	defer store.Close()

	if _, err := query.Submit(t.Context(), "run concurrently"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	var continued []model.Item
	for _, item := range provider.requests[1].Input {
		if item.Type == model.ItemFunctionCallOutput {
			continued = append(continued, item)
		}
	}
	if len(continued) != 3 ||
		continued[0].CallID != "call_fast" || continued[0].Output != "fast completed" ||
		continued[1].CallID != "call_slow" || continued[1].Output != "slow completed" ||
		continued[2].CallID != "call_missing" {
		t.Fatalf("model continuation result order = %v, want [call_fast call_slow call_missing]", continued)
	}

	snapshot, err := store.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var recorded []protocol.ToolUseID
	for _, event := range snapshot.ModelEvents() {
		if event.Kind == protocol.EventKindToolResult {
			recorded = append(recorded, event.ToolResult.ToolUseID)
		}
	}
	if len(recorded) != 3 || recorded[0] != "call_fast" || recorded[1] != "call_slow" || recorded[2] != "call_missing" {
		t.Fatalf("durable terminal result order = %v, want [call_fast call_slow call_missing]", recorded)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 ||
		pairs[0].Call.ToolCall.ID != "call_slow" || pairs[0].Result.ToolResult.ToolUseID != "call_slow" ||
		pairs[1].Call.ToolCall.ID != "call_fast" || pairs[1].Result.ToolResult.ToolUseID != "call_fast" ||
		pairs[2].Call.ToolCall.ID != "call_missing" || pairs[2].Result.ToolResult.ToolUseID != "call_missing" {
		t.Fatalf("accepted-order correlation changed: %#v", pairs)
	}
	if !pairs[2].Result.ToolResult.Synthetic || pairs[2].Result.ToolResult.Status != protocol.ToolResultInterrupted {
		t.Fatalf("missing result did not receive exactly one synthetic settlement: %#v", pairs[2].Result.ToolResult)
	}

	restored, err := New(Config{
		SessionID: snapshot.SessionID, Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	var recovered []string
	for _, item := range restored.history {
		if item.Type == model.ItemFunctionCallOutput {
			recovered = append(recovered, item.CallID)
		}
	}
	if len(recovered) != 3 || recovered[0] != "call_fast" || recovered[1] != "call_slow" || recovered[2] != "call_missing" {
		t.Fatalf("recovered model result order = %v, want [call_fast call_slow call_missing]", recovered)
	}
}

func TestPhasedAssistantItemsPreserveReplayAndOnlyFinalAnswerIsOutcome(t *testing.T) {
	commentary := model.TextMessage(model.RoleAssistant, "checking")
	commentary.ID, commentary.Phase = "msg_commentary", "commentary"
	call := model.FunctionCall("fc_phase", "call_phase", "Read", `{}`)
	progress := model.TextMessage(model.RoleAssistant, "almost")
	progress.ID, progress.Phase = "msg_progress", "commentary"
	finalOne := model.TextMessage(model.RoleAssistant, "answer one")
	finalOne.ID, finalOne.Phase = "msg_final_1", "final_answer"
	finalTwo := model.TextMessage(model.RoleAssistant, "answer two")
	finalTwo.ID, finalTwo.Phase = "msg_final_2", "final_answer"
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{commentary, call}, model.Usage{}),
		completed([]model.Item{progress, finalOne, finalTwo}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{ID: "call_phase", Name: "Read", Status: protocol.ToolResultSuccess, Content: "ok"}}}
	query, store := newTestEngine(t, provider, capabilities)
	outcome, err := query.Submit(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "answer one\nanswer two" {
		t.Fatalf("terminal outcome included commentary or lost boundaries: %q", outcome.Text)
	}
	before := append([]model.Item(nil), query.history...)
	snapshot, err := store.LoadAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(Config{SessionID: query.SessionID(), Model: "gpt-5.6-sol", Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(restored.history) != len(before) {
		t.Fatalf("restore duplicated or lost provider items: before=%#v after=%#v", before, restored.history)
	}
	for index := range before {
		if before[index].Type != restored.history[index].Type || before[index].ID != restored.history[index].ID || before[index].CallID != restored.history[index].CallID || itemText(before[index]) != itemText(restored.history[index]) {
			t.Fatalf("restore item %d mismatch: before=%#v after=%#v", index, before[index], restored.history[index])
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsCumulativeUsageOverflow(t *testing.T) {
	query, err := New(Config{
		SessionID: "ses_usage_overflow", Model: "gpt-5.6-sol", ReasoningEffort: "high",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	usageEvent := func(id protocol.EventID, sequence uint64, usage protocol.Usage) protocol.Event {
		return protocol.Event{
			Version: protocol.CurrentVersion, ID: id, SessionID: "ses_usage_overflow", Sequence: sequence,
			Timestamp: time.Unix(int64(sequence), 0).UTC(), Kind: protocol.EventKindUsage,
			Visibility: protocol.VisibilityInternal, Persistence: protocol.PersistenceDurable, Origin: protocol.OriginModel,
			Usage: &usage,
		}
	}
	snapshot := transcript.Snapshot{SessionID: "ses_usage_overflow", MaxSequence: 2, Events: []protocol.Event{
		usageEvent("evt_usage_max", 1, protocol.Usage{Model: "gpt-5.6-sol", InputTokens: math.MaxInt64, TotalTokens: math.MaxInt64}),
		usageEvent("evt_usage_overflow", 2, protocol.Usage{Model: "gpt-5.6-sol", InputTokens: 1, TotalTokens: 1}),
	}}
	if err := query.Restore(snapshot); err == nil {
		t.Fatal("overflowing restored usage was accepted")
	}
	if status := query.Status(); status.Usage.InputTokens != 0 || status.Usage.TotalTokens != 0 {
		t.Fatalf("failed restore corrupted published usage: %+v", status.Usage)
	}
}

func TestRestoreIgnoresInvalidReasoningEffortMetadata(t *testing.T) {
	query, err := New(Config{
		SessionID: "ses_effort_restore", Model: "gpt-5.6-sol", ReasoningEffort: "high",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadataEvent := func(id protocol.EventID, sequence uint64, raw string) protocol.Event {
		return protocol.Event{
			Version: protocol.CurrentVersion, ID: id, SessionID: "ses_effort_restore", Sequence: sequence,
			Timestamp: time.Unix(int64(sequence), 0).UTC(), Kind: protocol.EventKindSessionMetadata,
			Visibility: protocol.VisibilityInternal, Persistence: protocol.PersistenceDurable, Origin: protocol.OriginRuntime,
			Metadata: &protocol.MetadataEvent{Key: reasoningEffortKey, Value: json.RawMessage(raw)},
		}
	}
	if err := query.Restore(transcript.Snapshot{SessionID: "ses_effort_restore", MaxSequence: 3, Events: []protocol.Event{
		metadataEvent("evt_effort_valid", 1, `"xhigh"`),
		metadataEvent("evt_effort_invalid", 2, `"ultra"`),
		metadataEvent("evt_effort_oversized", 3, `"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`),
	}}); err != nil {
		t.Fatal(err)
	}
	if status := query.Status(); status.ReasoningEffort != "xhigh" {
		t.Fatalf("invalid restored effort replaced validated state: %+v", status)
	}
}

func TestConfiguredSanitizerProtectsModelTranscriptAndLegacyRestore(t *testing.T) {
	const secret = "production-subscription-secret"
	sanitize := func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") }
	answer := model.TextMessage(model.RoleAssistant, "answer "+secret)
	answer.ID, answer.Phase = "msg_secret", "final_answer"
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{answer}, model.Usage{})}}
	store, err := transcript.Open(t.Context(), transcript.Config{Path: filepath.Join(t.TempDir(), "sanitized.jsonl"), SessionID: "ses_sanitized", SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	query, err := New(Config{
		SessionID: "ses_sanitized", Model: "gpt-5.6-sol", Instructions: "instructions " + secret,
		Provider: provider, Capabilities: &fakeCapabilities{}, Transcript: store, Sanitize: sanitize,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "prompt "+secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outcome.Text, secret) || len(provider.requests) != 1 {
		t.Fatalf("secret escaped outcome/request count: %#v requests=%d", outcome, len(provider.requests))
	}
	requestBytes, _ := json.Marshal(provider.requests[0])
	if strings.Contains(string(requestBytes), secret) {
		t.Fatalf("secret escaped model request: %s", requestBytes)
	}
	snapshot, err := store.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes, _ := json.Marshal(snapshot)
	if strings.Contains(string(snapshotBytes), secret) {
		t.Fatalf("secret escaped durable transcript: %s", snapshotBytes)
	}

	legacy, err := protocol.NewMessageEvent("ses_legacy_secret", "turn_legacy", protocol.RoleUser, protocol.TextBlock("legacy "+secret))
	if err != nil {
		t.Fatal(err)
	}
	legacyProvider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{})}}
	restored, err := New(Config{SessionID: "ses_legacy_secret", Model: "gpt-5.6-sol", Provider: legacyProvider, Capabilities: &fakeCapabilities{}, Sanitize: sanitize})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(transcript.Snapshot{SessionID: "ses_legacy_secret", Events: []protocol.Event{legacy}}); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Submit(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	legacyRequest, _ := json.Marshal(legacyProvider.requests[0])
	if strings.Contains(string(legacyRequest), secret) {
		t.Fatalf("legacy transcript secret escaped restored request: %s", legacyRequest)
	}
}

func TestSemanticCredentialInModelToolArgumentsFailsBeforePersistenceOrExecution(t *testing.T) {
	const secret = "secret"
	const escapedArguments = `{"value":"\u0073ecret"}`
	call := model.FunctionCall("fc_escaped", "call_escaped", "Read", escapedArguments)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{call}, model.Usage{})}}
	capabilities := &fakeCapabilities{}
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_semantic_credential", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: capabilities, Transcript: store,
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "safe prompt"); !errors.Is(err, model.ErrProtocol) {
		t.Fatalf("semantic credential error = %v", err)
	}
	if len(capabilities.calls) != 0 {
		t.Fatalf("credential-bearing call executed: %#v", capabilities.calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, event := range store.events {
		if event.Kind == protocol.EventKindToolCall ||
			event.Kind == protocol.EventKindSessionMetadata && event.Metadata != nil && event.Metadata.Key == providerOutputKey {
			t.Fatalf("credential-bearing provider output crossed durable boundary: %#v", event)
		}
	}
}

func TestDuplicateModelToolArgumentMembersFailBeforePersistenceOrExecution(t *testing.T) {
	const arguments = `{"value":"\u0073ecret","value":"safe"}`
	call := model.FunctionCall("fc_duplicate", "call_duplicate", "Read", arguments)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{call}, model.Usage{})}}
	capabilities := &fakeCapabilities{}
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_duplicate_arguments", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: capabilities, Transcript: store,
		CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "safe prompt"); !errors.Is(err, model.ErrProtocol) {
		t.Fatalf("duplicate argument error = %v", err)
	}
	if len(capabilities.calls) != 0 {
		t.Fatalf("duplicate-key call executed: %#v", capabilities.calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, event := range store.events {
		if event.Kind == protocol.EventKindToolCall {
			t.Fatalf("duplicate-key call crossed durable boundary: %#v", event)
		}
	}
}

func TestEngineRedactsCredentialAcrossUntrustedProviderTextDeltas(t *testing.T) {
	const secret = "alternate-provider-credential"
	split := len(secret) / 2
	provider := &fakeProvider{responses: [][]model.Event{{
		{Type: model.EventTextDelta, Delta: "before " + secret[:split]},
		{Type: model.EventTextDelta, Delta: secret[split:] + " after"},
		{Type: model.EventResponseCompleted, Response: &model.Response{
			ID: "resp_split_credential", Status: "completed",
			Output: []model.Item{model.TextMessage(model.RoleAssistant, "before "+secret+" after")},
		}},
	}}}
	sink := &capturingSink{}
	query, err := New(Config{
		SessionID: "ses_split_provider_credential", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{}, Sink: sink,
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "safe prompt")
	if err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	for _, event := range sink.events {
		if event.Progress != nil && event.Progress.Phase == "model_text" {
			progress.WriteString(event.Progress.Message)
		}
	}
	want := "before " + redact.Mask(secret) + " after"
	if outcome.Text != want || progress.String() != want {
		t.Fatalf("split provider credential projection: outcome=%q progress=%q want=%q", outcome.Text, progress.String(), want)
	}
	encoded, err := json.Marshal(sink.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("split provider credential reached sink: %s", encoded)
	}
}

func TestEngineDiscardsAmbiguousCredentialSuffixOnIncompleteStream(t *testing.T) {
	provider := &fakeProvider{responses: [][]model.Event{{
		{Type: model.EventTextDelta, Delta: "sec"},
	}}}
	sink := &capturingSink{}
	query, err := New(Config{
		SessionID: "ses_incomplete_provider_credential", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{}, Sink: sink,
		CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "safe prompt"); !errors.Is(err, model.ErrIncompleteStream) {
		t.Fatalf("incomplete provider stream error = %v", err)
	}
	for _, event := range sink.events {
		if event.Progress != nil && event.Progress.Phase == "model_text" && event.Progress.Message != "" {
			t.Fatalf("ambiguous credential suffix reached progress: %#v", event.Progress)
		}
	}
}

func TestEngineDiscardsAmbiguousCredentialSuffixOnInvalidTerminalResponse(t *testing.T) {
	provider := &fakeProvider{responses: [][]model.Event{{
		{Type: model.EventTextDelta, Delta: "sec"},
		{Type: model.EventResponseCompleted, Response: &model.Response{Status: "completed"}},
	}}}
	sink := &capturingSink{}
	query, err := New(Config{
		SessionID: "ses_invalid_terminal_credential", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{}, Sink: sink,
		CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "safe prompt"); !errors.Is(err, model.ErrProtocol) {
		t.Fatalf("invalid terminal response error = %v", err)
	}
	for _, event := range sink.events {
		if event.Progress != nil && event.Progress.Phase == "model_text" && event.Progress.Message != "" {
			t.Fatalf("ambiguous credential suffix reached progress before terminal validation: %#v", event.Progress)
		}
	}
}

func TestMalformedSemanticCredentialArgumentsUseSafeSettlementPlaceholder(t *testing.T) {
	const malformedArguments = `{"value":"\u0073ecret"`
	call := model.FunctionCall("fc_malformed_escaped", "call_malformed_escaped", "Read", malformedArguments)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{
		ID: "call_malformed_escaped", Name: "Read", Status: protocol.ToolResultMalformed,
		Content: "malformed input", IsError: true,
	}}}
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_malformed_semantic_credential", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: capabilities, Transcript: store,
		CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "safe prompt")
	if err != nil || outcome.Status != protocol.TurnResultSuccess {
		t.Fatalf("safe placeholder settlement outcome = %#v, %v", outcome, err)
	}
	if len(capabilities.calls) != 1 || string(capabilities.calls[0].Arguments) != `""` {
		t.Fatalf("capability received unsafe malformed arguments: %#v", capabilities.calls)
	}
	store.mu.Lock()
	durable, marshalErr := json.Marshal(store.events)
	store.mu.Unlock()
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(durable), `\u0073ecret`) || strings.Contains(string(durable), "secret") {
		t.Fatalf("durable placeholder retained malformed semantic credential: %s", durable)
	}
}

func TestRestoreFiltersSemanticCredentialToolCallsAndResults(t *testing.T) {
	const secret = "secret"
	const escapedArguments = `{"value":"\u0073ecret"}`
	sessionID := protocol.SessionID("ses_restore_semantic_credential")
	turnID := protocol.TurnID("turn_restore_semantic_credential")
	user, err := protocol.NewMessageEvent(sessionID, turnID, protocol.RoleUser, protocol.TextBlock("legacy context"))
	if err != nil {
		t.Fatal(err)
	}
	providerCall := model.FunctionCall("fc_legacy", "call_legacy", "Read", escapedArguments)
	providerData, err := json.Marshal([]model.Item{providerCall})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := protocol.NewBaseEvent(sessionID, turnID, protocol.EventKindSessionMetadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Visibility = protocol.VisibilityInternal
	metadata.Metadata = &protocol.MetadataEvent{Key: providerOutputKey, Value: providerData}
	call, err := protocol.NewToolCallEvent(sessionID, turnID, protocol.NewRawToolCall("call_legacy", "Read", escapedArguments))
	if err != nil {
		t.Fatal(err)
	}
	result, err := protocol.NewToolResultEvent(sessionID, turnID, protocol.ToolResult{
		ToolUseID: "call_legacy", ToolName: "Read", Status: protocol.ToolResultSuccess,
		Content: []protocol.ContentBlock{protocol.TextBlock("legacy result")},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []*protocol.Event{&user, &metadata, &call, &result}
	for index, event := range events {
		event.Sequence = uint64(index + 1)
		if index > 0 {
			parent := events[index-1].ID
			event.ParentID = &parent
		}
	}

	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	query, err := New(Config{
		SessionID: sessionID, Model: "gpt-5.6-sol", Provider: provider,
		Capabilities: &fakeCapabilities{}, CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := query.Restore(transcript.Snapshot{
		SessionID: sessionID, Events: []protocol.Event{user, metadata, call, result},
		MaxSequence: 4, ResumeCursor: result.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	for _, item := range provider.requests[0].Input {
		if item.Type == model.ItemFunctionCall || item.Type == model.ItemFunctionCallOutput {
			t.Fatalf("legacy credential-bearing tool pair re-entered provider context: %#v", provider.requests[0].Input)
		}
	}
}

func TestRestoreReplacesMalformedCredentialAliasesWithSafePlaceholder(t *testing.T) {
	const malformedArguments = `{"value":"\u0073ecret"`
	sessionID := protocol.SessionID("ses_restore_malformed_credential")
	turnID := protocol.TurnID("turn_restore_malformed_credential")
	user, _ := protocol.NewMessageEvent(sessionID, turnID, protocol.RoleUser, protocol.TextBlock("legacy"))
	call, _ := protocol.NewToolCallEvent(sessionID, turnID, protocol.NewRawToolCall("call_legacy", "Read", malformedArguments))
	result, _ := protocol.NewToolResultEvent(sessionID, turnID, protocol.ToolResult{
		ToolUseID: "call_legacy", ToolName: "Read", Status: protocol.ToolResultMalformed,
		Content: []protocol.ContentBlock{protocol.TextBlock("malformed input")}, IsError: true,
	})
	events := []*protocol.Event{&user, &call, &result}
	for index, event := range events {
		event.Sequence = uint64(index + 1)
		if index > 0 {
			parent := events[index-1].ID
			event.ParentID = &parent
		}
	}
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	query, err := New(Config{
		SessionID: sessionID, Model: "gpt-5.6-sol", Provider: provider,
		Capabilities: &fakeCapabilities{}, CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := query.Restore(transcript.Snapshot{
		SessionID: sessionID, Events: []protocol.Event{user, call, result},
		MaxSequence: 3, ResumeCursor: result.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "next"); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(provider.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(request), `\u0073ecret`) || strings.Contains(string(request), "secret") {
		t.Fatalf("restored request retained malformed semantic credential: %s", request)
	}
	foundPlaceholder := false
	for _, item := range provider.requests[0].Input {
		if item.Type == model.ItemFunctionCall && item.CallID == "call_legacy" {
			foundPlaceholder = item.Arguments == `""`
		}
	}
	if !foundPlaceholder {
		t.Fatalf("restored malformed call did not use safe placeholder: %#v", provider.requests[0].Input)
	}
}

func TestStatusDoesNotWaitForBlockedSubmit(t *testing.T) {
	provider := &blockingProvider{
		entered: make(chan struct{}), release: make(chan struct{}),
		events: completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}),
	}
	engine, store := newTestEngine(t, provider, &fakeCapabilities{})
	defer store.Close()
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.Submit(context.Background(), "inspect")
		submitDone <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("submit never entered provider")
	}

	statusDone := make(chan Status, 1)
	go func() { statusDone <- engine.Status() }()
	select {
	case status := <-statusDone:
		if !status.Active || status.ProjectedItems != 1 || status.Model != "gpt-5.6-sol" {
			t.Fatalf("in-flight status = %+v", status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Status waited for Submit's turn lock")
	}
	close(provider.release)
	if err := <-submitDone; err != nil {
		t.Fatal(err)
	}
	status := engine.Status()
	if status.Active || status.ProjectedItems != 2 || status.Usage.TotalTokens != 3 {
		t.Fatalf("terminal status = %+v", status)
	}
}

func TestStatusSnapshotsAreRaceSafeDuringToolTurn(t *testing.T) {
	call := model.FunctionCall("fc", "call", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "complete")}, model.Usage{InputTokens: 13, OutputTokens: 4, TotalTokens: 17}),
	}}
	capabilities := &gatedCapabilities{entered: make(chan struct{}), release: make(chan struct{})}
	engine, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.Submit(context.Background(), "go")
		submitDone <- err
	}()
	select {
	case <-capabilities.entered:
	case <-time.After(time.Second):
		t.Fatal("submit never entered capability runtime")
	}

	const readers = 24
	var readersDone sync.WaitGroup
	readersDone.Add(readers)
	for range readers {
		go func() {
			defer readersDone.Done()
			for range 500 {
				status := engine.Status()
				if status.SessionID != engine.SessionID() || status.Model != "gpt-5.6-sol" || status.Usage.TotalTokens < 0 {
					t.Errorf("invalid status snapshot: %+v", status)
					return
				}
			}
		}()
	}
	close(capabilities.release)
	if err := <-submitDone; err != nil {
		t.Fatal(err)
	}
	readersDone.Wait()
	status := engine.Status()
	if status.Active || status.Usage.TotalTokens != 29 || status.ProjectedItems != 4 {
		t.Fatalf("final status = %+v", status)
	}
}

func TestMalformedArgumentsPersistThenSettle(t *testing.T) {
	call := model.FunctionCall("fc_bad", "call_bad", "Read", `{"file_path":`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{call}, model.Usage{}), completed([]model.Item{model.TextMessage(model.RoleAssistant, "handled")}, model.Usage{})}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{ID: "call_bad", Name: "Read", Status: protocol.ToolResultMalformed, Content: "malformed", Code: "malformed_input", IsError: true}}}
	engine, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	if _, err := engine.Submit(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs=%#v err=%v", pairs, err)
	}
	if pairs[0].Call.ToolCall.RawArguments == nil || *pairs[0].Call.ToolCall.RawArguments != `{"file_path":` || pairs[0].Result.ToolResult.Status != protocol.ToolResultMalformed {
		t.Fatalf("pair=%#v", pairs[0])
	}
}

func TestMissingCapabilityResultIsSynthetic(t *testing.T) {
	call := model.FunctionCall("fc", "call_missing", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{call}, model.Usage{}), completed([]model.Item{model.TextMessage(model.RoleAssistant, "reported")}, model.Usage{})}}
	engine, store := newTestEngine(t, provider, &fakeCapabilities{})
	defer store.Close()
	if _, err := engine.Submit(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Load(context.Background())
	pairs, err := snapshot.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if !pairs[0].Result.ToolResult.Synthetic || pairs[0].Result.ToolResult.Status != protocol.ToolResultInterrupted {
		t.Fatalf("result=%#v", pairs[0].Result.ToolResult)
	}
}

func TestCapabilityCannotSpoofAcceptedToolName(t *testing.T) {
	call := model.FunctionCall("fc", "call_name", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{ID: "call_name", Name: "Write", Status: protocol.ToolResultSuccess, Content: "ok"}}}
	query, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	if _, err := query.Submit(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil || len(pairs) != 1 || pairs[0].Result.ToolResult.ToolName != "Read" {
		t.Fatalf("spoofed result escaped accepted ledger: pairs=%#v err=%v", pairs, err)
	}
}

func TestLargeToolErrorRetainsResultWithBoundedErrorMetadata(t *testing.T) {
	call := model.FunctionCall("fc", "call_large", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "reported")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{ID: "call_large", Name: "Read", Status: protocol.ToolResultError, Content: strings.Repeat("é", 20_000), Code: "execution_failed", IsError: true}}}
	query, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	if _, err := query.Submit(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Result.ToolResult.Error == nil || len(pairs[0].Result.ToolResult.Error.Message) > 16*1024 || !utf8.ValidString(pairs[0].Result.ToolResult.Error.Message) {
		t.Fatalf("invalid bounded error result: %#v", pairs)
	}
}

func TestRestoreDoesNotExecuteCapabilities(t *testing.T) {
	provider := &fakeProvider{}
	capabilities := &fakeCapabilities{}
	engine, store := newTestEngine(t, provider, capabilities)
	defer store.Close()
	turn := protocol.TurnID("turn_old")
	user, _ := protocol.NewMessageEvent(engine.SessionID(), turn, protocol.RoleUser, protocol.TextBlock("old"))
	if err := store.Append(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	call, _ := protocol.NewToolCallEvent(engine.SessionID(), turn, protocol.NewRawToolCall("call_old", "Read", `{}`))
	if err := store.Append(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(capabilities.calls) != 0 {
		t.Fatal("restore executed a tool")
	}
}

func TestRestoreFiltersVisibilityAndUnacceptedRawCalls(t *testing.T) {
	provider := &fakeProvider{}
	capabilities := &fakeCapabilities{}
	query, err := New(Config{SessionID: "ses_visibility", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	turn := protocol.TurnID("turn_visibility")
	hidden, _ := protocol.NewMessageEvent(query.SessionID(), turn, protocol.RoleUser, protocol.TextBlock("presentation only"))
	hidden.Visibility = protocol.VisibilityUser
	visible, _ := protocol.NewMessageEvent(query.SessionID(), turn, protocol.RoleUser, protocol.TextBlock("model context"))
	orphan := model.FunctionCall("provider_item", "orphan_call", "Read", `{}`)
	data, _ := json.Marshal([]model.Item{orphan})
	metadata, _ := protocol.NewBaseEvent(query.SessionID(), turn, protocol.EventKindSessionMetadata)
	metadata.Visibility = protocol.VisibilityInternal
	metadata.Metadata = &protocol.MetadataEvent{Key: providerOutputKey, Value: data}
	// Build a coherent durable chain explicitly. Independent zero-sequence roots
	// with near-identical timestamps make ActiveConversation correctly choose one
	// leaf, which is not what this visibility test intends to exercise.
	hidden.Sequence = 1
	visible.Sequence = 2
	visibleParent := hidden.ID
	visible.ParentID = &visibleParent
	metadata.Sequence = 3
	metadataParent := visible.ID
	metadata.ParentID = &metadataParent
	if err := query.Restore(transcript.Snapshot{
		SessionID: query.SessionID(), Events: []protocol.Event{hidden, visible, metadata},
		MaxSequence: 3, ResumeCursor: metadata.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(query.history) != 1 || itemText(query.history[0]) != "model context" {
		t.Fatalf("restore leaked hidden or unaccepted input: %#v", query.history)
	}
}

func TestPromptIDDeduplicationSurvivesTranscriptReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	sessionID := protocol.SessionID("ses_prompt_dedup")
	firstStore, err := transcript.Open(t.Context(), transcript.Config{Path: path, SessionID: sessionID, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	firstProvider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	first, err := New(Config{SessionID: sessionID, Model: "gpt-5.6-sol", Provider: firstProvider, Capabilities: &fakeCapabilities{}, Transcript: firstStore})
	if err != nil {
		t.Fatal(err)
	}
	const promptID = "host-prompt-5d5b3a2e"
	if _, err := first.SubmitPrompt(t.Context(), "perform once", promptID); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := transcript.Open(t.Context(), transcript.Config{Path: path, SessionID: sessionID, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	snapshot, err := secondStore.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondProvider := &fakeProvider{}
	second, err := New(Config{SessionID: sessionID, Model: "gpt-5.6-sol", Provider: secondProvider, Capabilities: &fakeCapabilities{}, Transcript: secondStore})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if !second.HasPromptID(promptID) {
		t.Fatal("restored engine lost prompt idempotency key")
	}
	if _, err := second.SubmitPrompt(t.Context(), "must not run", promptID); !errors.Is(err, ErrDuplicatePrompt) {
		t.Fatalf("duplicate submit error = %v", err)
	}
	if len(secondProvider.requests) != 0 {
		t.Fatalf("duplicate prompt invoked provider %d times", len(secondProvider.requests))
	}
}

func TestPartialCallAcceptanceSettlesEveryDurableCallWithoutExecution(t *testing.T) {
	first := model.FunctionCall("fc_1", "call_1", "Read", `{}`)
	second := model.FunctionCall("fc_2", "call_2", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{first, second}, model.Usage{})}}
	capabilities := &fakeCapabilities{}
	store := &faultStore{failToolCallID: "call_2", failToolCallRemaining: 2}
	query, err := New(Config{SessionID: "ses_partial", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(context.Background(), "go"); err == nil {
		t.Fatal("injected call persistence failure was hidden")
	}
	if len(capabilities.calls) != 0 {
		t.Fatalf("partially accepted batch executed: %#v", capabilities.calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	calls, results := 0, 0
	for _, event := range store.events {
		if event.Kind == protocol.EventKindToolCall {
			calls++
		}
		if event.Kind == protocol.EventKindToolResult {
			results++
			if !event.ToolResult.Synthetic || event.ToolResult.Status != protocol.ToolResultInterrupted {
				t.Fatalf("unexpected settlement: %#v", event.ToolResult)
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("durable exact-one settlement calls=%d results=%d events=%#v", calls, results, store.events)
	}
	for _, item := range query.history {
		if item.Type == model.ItemFunctionCall && item.CallID == "call_2" {
			t.Fatalf("unaccepted raw call poisoned live history: %#v", query.history)
		}
	}
}

func TestAmbiguousCallCommitIsSettledWithoutExecutionInCurrentProcess(t *testing.T) {
	call := model.FunctionCall("fc_ambiguous", "call_ambiguous", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{call}, model.Usage{})}}
	capabilities := &fakeCapabilities{}
	store := &ambiguousAcceptanceStore{callID: "call_ambiguous"}
	query, err := New(Config{SessionID: "ses_ambiguous", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(t.Context(), "go"); err == nil {
		t.Fatal("ambiguous call persistence acknowledgement was hidden")
	}
	if len(capabilities.calls) != 0 {
		t.Fatalf("ambiguously committed call executed: %#v", capabilities.calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var calls, results int
	for _, event := range store.events {
		switch event.Kind {
		case protocol.EventKindToolCall:
			calls++
		case protocol.EventKindToolResult:
			if event.ToolResult.ToolUseID == "call_ambiguous" {
				results++
				if !event.ToolResult.Synthetic || event.ToolResult.Status != protocol.ToolResultInterrupted {
					t.Fatalf("ambiguous call received nonterminal settlement: %#v", event.ToolResult)
				}
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("ambiguous settlement calls=%d results=%d events=%#v", calls, results, store.events)
	}
}

func TestResultPersistenceRetriesSameEffectAndSettlesSiblings(t *testing.T) {
	first := model.FunctionCall("fc_1", "call_1", "Read", `{}`)
	second := model.FunctionCall("fc_2", "call_2", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{first, second}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{
		{ID: "call_1", Name: "Read", Status: protocol.ToolResultSuccess, Content: "one"},
		{ID: "call_2", Name: "Read", Status: protocol.ToolResultSuccess, Content: "two"},
	}}
	store := &faultStore{failResultID: "call_1", failResultRemaining: 1}
	query, err := New(Config{SessionID: "ses_results", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities, Transcript: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	resolved := make(map[protocol.ToolUseID]int)
	for _, event := range store.events {
		if event.Kind == protocol.EventKindToolResult {
			resolved[event.ToolResult.ToolUseID]++
		}
	}
	if resolved["call_1"] != 1 || resolved["call_2"] != 1 || len(capabilities.calls) != 2 {
		t.Fatalf("terminal results=%#v executions=%#v", resolved, capabilities.calls)
	}
}

func TestPermissionDenialsAccumulateInSessionOutcome(t *testing.T) {
	call := model.FunctionCall("fc_denied", "call_denied", "Read", `{"file_path":"blocked.txt"}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "recovered")}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "next")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{
		ID: "call_denied", Name: "Read", Status: protocol.ToolResultDenied,
		Content: "policy denied", Code: "denied", IsError: true,
		PermissionDenial: &PermissionDenial{ToolName: "untrusted", ToolUseID: "untrusted", ToolInput: json.RawMessage(`{"file_path":"blocked.txt"}`)},
	}}}
	query, store := newTestEngine(t, provider, capabilities)
	defer store.Close()

	first, err := query.Submit(t.Context(), "try")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PermissionDenials) != 1 {
		t.Fatalf("first permission denials = %#v", first.PermissionDenials)
	}
	denial := first.PermissionDenials[0]
	if denial.ToolName != "Read" || denial.ToolUseID != "call_denied" || string(denial.ToolInput) != `{"file_path":"blocked.txt"}` {
		t.Fatalf("normalized permission denial = %#v", denial)
	}

	second, err := query.Submit(t.Context(), "continue")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.PermissionDenials) != 1 || second.PermissionDenials[0].ToolUseID != "call_denied" {
		t.Fatalf("session denial accumulation = %#v", second.PermissionDenials)
	}
	first.PermissionDenials[0].ToolInput[0] = '['
	if string(second.PermissionDenials[0].ToolInput) != `{"file_path":"blocked.txt"}` {
		t.Fatal("outcome permission denial aliases engine state")
	}
}

func TestPermissionDenialInputIsSanitizedBeforeOutcome(t *testing.T) {
	const secret = "provider-credential-value"
	call := model.FunctionCall("fc_denied", "call_denied", "Read", `{"value":"`+secret+`"}`)
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{call}, model.Usage{}),
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{}),
	}}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{
		ID: "call_denied", Name: "Read", Status: protocol.ToolResultDenied,
		Content: "denied", Code: "denied", IsError: true,
		PermissionDenial: &PermissionDenial{ToolInput: json.RawMessage(`{"value":"` + secret + `"}`)},
	}}}
	query, err := New(Config{
		SessionID: "ses_denial_redaction", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities,
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.PermissionDenials) != 1 || strings.Contains(string(outcome.PermissionDenials[0].ToolInput), secret) || !strings.Contains(string(outcome.PermissionDenials[0].ToolInput), "[REDACTED]") {
		t.Fatalf("unsanitized permission denial = %#v", outcome.PermissionDenials)
	}
}

func TestDeliveryFailureSettlesAcceptedCallsWithoutExecution(t *testing.T) {
	first := model.FunctionCall("fc_1", "call_1", "Read", `{}`)
	second := model.FunctionCall("fc_2", "call_2", "Read", `{}`)
	provider := &fakeProvider{responses: [][]model.Event{completed([]model.Item{first, second}, model.Usage{})}}
	capabilities := &fakeCapabilities{}
	store := &faultStore{}
	query, err := New(Config{SessionID: "ses_delivery", Model: "gpt-5.6-sol", Provider: provider, Capabilities: capabilities, Transcript: store, Sink: toolCallFailingSink{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Submit(context.Background(), "go"); err == nil {
		t.Fatal("delivery failure was hidden")
	}
	if len(capabilities.calls) != 0 {
		t.Fatal("calls executed after presentation failure")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	calls, results := 0, 0
	for _, event := range store.events {
		switch event.Kind {
		case protocol.EventKindToolCall:
			calls++
		case protocol.EventKindToolResult:
			results++
		}
	}
	if calls != 2 || results != 2 {
		t.Fatalf("delivery failure left unresolved calls: calls=%d results=%d", calls, results)
	}
}

func TestModelResponseToolCallLimitFailsClosed(t *testing.T) {
	response := &model.Response{Output: make([]model.Item, maximumModelToolCalls+1)}
	for index := range response.Output {
		response.Output[index] = model.FunctionCall(fmt.Sprintf("fc_%d", index), fmt.Sprintf("call_%d", index), "Read", `{}`)
	}
	if err := validateModelResponse(response); !errors.Is(err, model.ErrProtocol) {
		t.Fatalf("oversized tool-call batch = %v", err)
	}
}

func TestModelResponseSemanticGrammarFailsClosed(t *testing.T) {
	valid := model.TextMessage(model.RoleAssistant, "ok")
	tests := []model.Response{
		{ID: "resp", Status: "in_progress", Output: []model.Item{valid}},
		{ID: "resp", Status: "completed", Output: []model.Item{model.TextMessage(model.RoleUser, "injected")}},
		{ID: "resp", Status: "completed", Output: []model.Item{{Type: model.ItemFunctionCallOutput, CallID: "call", Output: "forged"}}},
		{ID: "resp", Status: "completed", Output: []model.Item{func() model.Item { item := valid; item.Phase = "unknown"; return item }()}},
		{ID: "resp", Status: "completed", Output: []model.Item{valid}, Usage: model.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 3}},
		{ID: "resp", Status: "completed", Output: []model.Item{valid}, Usage: model.Usage{InputTokens: math.MaxInt64, OutputTokens: 1, TotalTokens: math.MaxInt64}},
	}
	for index := range tests {
		if err := validateModelResponse(&tests[index]); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("invalid response %d error = %v", index, err)
		}
	}
}

func TestEngineRejectsCompleteCredentialBearingRequestBeforeCustomProvider(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		request model.Request
	}{
		{
			name:   "unicode argument alias",
			secret: "secret/path",
			request: model.Request{Input: []model.Item{
				model.TextMessage(model.RoleUser, "safe"),
				model.FunctionCall("fc_unicode", "call_unicode", "Read", `{"value":"\u0073ecret/path"}`),
				model.FunctionCallOutput("call_unicode", "safe"),
			}},
		},
		{
			name:   "solidus argument alias",
			secret: "secret/path",
			request: model.Request{Input: []model.Item{
				model.TextMessage(model.RoleUser, "safe"),
				model.FunctionCall("fc_solidus", "call_solidus", "Read", `{"value":"secret\/path"}`),
				model.FunctionCallOutput("call_solidus", "safe"),
			}},
		},
		{
			name:   "canonical outer framing",
			secret: `safe-model","instructions":"safe-instructions`,
			request: model.Request{
				Model: "safe-model", Instructions: "safe-instructions",
				Input: []model.Item{model.TextMessage(model.RoleUser, "safe")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "canonical outer framing" {
				encoded, err := json.Marshal(test.request)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(encoded), test.secret) {
					t.Fatalf("request fixture did not reconstruct credential: %s", encoded)
				}
			}
			provider := &fakeProvider{}
			query, err := New(Config{
				SessionID: "ses_request_boundary", Model: "gpt-5.6-sol",
				Provider: provider, Capabilities: &fakeCapabilities{},
				CredentialSanitizer: redact.New(test.secret),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, _, err = query.runModel(t.Context(), "turn_request_boundary", test.request)
			if !errors.Is(err, model.ErrProtocol) || strings.Contains(err.Error(), test.secret) {
				t.Fatalf("request boundary error = %v", err)
			}
			provider.mu.Lock()
			requests := len(provider.requests)
			provider.mu.Unlock()
			if requests != 0 {
				t.Fatalf("credential-bearing request reached custom provider %d times", requests)
			}
		})
	}
}

func TestEngineRejectsUnsafeProviderMetadataBeforePersistenceOrExecution(t *testing.T) {
	const secret = "credential-reflection-test-value"
	validEvent := func() model.Event {
		message := model.TextMessage(model.RoleAssistant, "safe answer")
		message.ID = "msg_safe"
		message.Status = "completed"
		message.Phase = "final_answer"
		return model.Event{
			Type: model.EventResponseCompleted,
			Response: &model.Response{
				ID: "resp_safe", Model: "gpt-5.6-sol", Status: "completed", PreviousResponseID: "resp_previous",
				Output: []model.Item{message},
			},
		}
	}
	tests := map[string]func(*model.Event){
		"event type":           func(event *model.Event) { event.Type = model.EventType(secret) },
		"raw event type":       func(event *model.Event) { event.RawType = secret },
		"request id":           func(event *model.Event) { event.RequestID = secret },
		"envelope response id": func(event *model.Event) { event.ResponseID = secret },
		"envelope item id":     func(event *model.Event) { event.ItemID = secret },
		"response id":          func(event *model.Event) { event.Response.ID = secret },
		"response model":       func(event *model.Event) { event.Response.Model = secret },
		"response status":      func(event *model.Event) { event.Response.Status = secret },
		"previous response id": func(event *model.Event) { event.Response.PreviousResponseID = secret },
		"item type":            func(event *model.Event) { event.Response.Output[0].Type = model.ItemType(secret) },
		"item id":              func(event *model.Event) { event.Response.Output[0].ID = secret },
		"item status":          func(event *model.Event) { event.Response.Output[0].Status = secret },
		"item phase":           func(event *model.Event) { event.Response.Output[0].Phase = secret },
		"content type":         func(event *model.Event) { event.Response.Output[0].Content[0].Type = model.ContentType(secret) },
		"call id": func(event *model.Event) {
			event.Response.Output = []model.Item{model.FunctionCall("fc_safe", secret, "Read", `{}`)}
		},
		"tool name": func(event *model.Event) {
			event.Response.Output = []model.Item{model.FunctionCall("fc_safe", "call_safe", secret, `{}`)}
		},
		"encrypted reasoning": func(event *model.Event) {
			event.Response.Output = []model.Item{{Type: model.ItemReasoning, ID: "reason_safe", EncryptedContent: secret}}
		},
		"bidi item id": func(event *model.Event) { event.Response.Output[0].ID = "safe\u202eevil" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			provider := &fakeProvider{responses: [][]model.Event{{event}}}
			capabilities := &fakeCapabilities{}
			store := &faultStore{}
			sink := &capturingSink{}
			query, err := New(Config{
				SessionID: "ses_provider_metadata", Model: "gpt-5.6-sol", Provider: provider,
				Capabilities: capabilities, Transcript: store, Sink: sink,
				Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = query.Submit(t.Context(), "safe prompt")
			if !errors.Is(err, model.ErrProtocol) {
				t.Fatalf("unsafe provider metadata error = %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "safe\u202eevil") {
				t.Fatalf("returned error exposed provider metadata: %q", err)
			}
			if len(capabilities.calls) != 0 {
				t.Fatalf("unsafe provider call executed: %#v", capabilities.calls)
			}
			store.mu.Lock()
			durable, marshalErr := json.Marshal(store.events)
			store.mu.Unlock()
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			projected, marshalErr := json.Marshal(sink.events)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for label, data := range map[string][]byte{"transcript": durable, "structured events": projected} {
				if strings.Contains(string(data), secret) || strings.Contains(string(data), "safe\u202eevil") {
					t.Fatalf("%s exposed unsafe provider metadata: %s", label, data)
				}
			}
		})
	}
}

func TestEngineReturnsSanitizedClassificationPreservingProviderErrors(t *testing.T) {
	const secret = "credential-reflection-test-value"
	classification := errors.New("provider classification")
	cause := fmt.Errorf("provider echoed %s: %w", secret, classification)
	store := &faultStore{}
	sink := &capturingSink{}
	query, err := New(Config{
		SessionID: "ses_provider_error", Model: "gpt-5.6-sol", Provider: failingProvider{err: cause},
		Capabilities: &fakeCapabilities{}, Transcript: store, Sink: sink,
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Submit(t.Context(), "safe prompt")
	if err == nil || !errors.Is(err, classification) {
		t.Fatalf("returned error lost classification: %v", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("returned error exposed its provider cause through Unwrap")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("returned error was not sanitized: %q", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formatted := fmt.Sprintf(format, err)
		if strings.Contains(formatted, secret) || !strings.Contains(formatted, "[REDACTED]") {
			t.Fatalf("format %q exposed provider cause: %q", format, formatted)
		}
	}
	store.mu.Lock()
	durable, marshalErr := json.Marshal(store.events)
	store.mu.Unlock()
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	projected, marshalErr := json.Marshal(sink.events)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(durable), secret) || strings.Contains(string(projected), secret) {
		t.Fatalf("provider error leaked: transcript=%s events=%s", durable, projected)
	}
}

func TestEngineResanitizesPublicErrorsAfterControlNormalization(t *testing.T) {
	const (
		secret = "a\uFFFDb"
		raw    = "a\x01b"
	)
	classification := errors.New("provider classification")
	cause := fmt.Errorf("%s: %w", raw, classification)
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_normalized_error", Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}, Transcript: store,
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSafe := func(label string, err error) {
		t.Helper()
		if err == nil || !errors.Is(err, classification) || errors.Unwrap(err) != nil {
			t.Fatalf("%s lost protected classification: %T %v", label, err, err)
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			rendered := fmt.Sprintf(format, err)
			if strings.Contains(rendered, secret) || strings.ContainsRune(rendered, '\x01') {
				t.Fatalf("%s %s exposed normalized credential: %q", label, format, rendered)
			}
		}
	}
	assertSafe("publicError", query.publicError(cause))

	_, returned := query.finish(
		t.Context(),
		Outcome{SessionID: query.SessionID(), TurnID: "turn_normalized_error"},
		protocol.TurnResultError,
		"provider_error",
		cause,
		time.Now(),
	)
	assertSafe("finish", returned)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 || store.events[0].TurnResult == nil {
		t.Fatalf("recorded events = %#v", store.events)
	}
	message := store.events[0].TurnResult.Message
	if strings.Contains(message, secret) || strings.ContainsRune(message, '\x01') {
		t.Fatalf("turn-result message exposed normalized credential: %q", message)
	}
}

func TestEngineRejectsCredentialReassembledFromStructuredProviderErrorFields(t *testing.T) {
	const secret = "credential-reflection-test-value"
	split := len(secret) / 2
	for name, provider := range map[string]model.Provider{
		"stream start": failingProvider{err: &model.ProviderError{Code: secret[:split], Message: secret[split:]}},
		"error event": &fakeProvider{responses: [][]model.Event{{{
			Type: model.EventError, Error: &model.ProviderError{Code: secret[:split], Message: secret[split:]},
		}}}},
	} {
		t.Run(name, func(t *testing.T) {
			store := &faultStore{}
			query, err := New(Config{
				SessionID: "ses_provider_error_parts", Model: "gpt-5.6-sol", Provider: provider,
				Capabilities: &fakeCapabilities{}, Transcript: store,
				Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = query.Submit(t.Context(), "safe prompt")
			if !errors.Is(err, model.ErrProtocol) {
				t.Fatalf("structured provider error = %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[:split]) || strings.Contains(err.Error(), secret[split:]) {
				t.Fatalf("returned error retained credential fragments: %q", err)
			}
			store.mu.Lock()
			durable, marshalErr := json.Marshal(store.events)
			store.mu.Unlock()
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(durable), secret) || strings.Contains(string(durable), secret[:split]) || strings.Contains(string(durable), secret[split:]) {
				t.Fatalf("structured provider error entered transcript: %s", durable)
			}
		})
	}
}

func TestEngineSanitizesCredentialAcrossProviderContentPartBoundaries(t *testing.T) {
	const secret = "credential-reflection-test-value"
	split := len(secret) / 2
	message := model.Item{
		Type: model.ItemMessage, ID: "msg_safe", Role: model.RoleAssistant, Status: "completed", Phase: "final_answer",
		Content: []model.Content{
			{Type: model.ContentOutputText, Text: "before " + secret[:split]},
			{Type: model.ContentOutputText, Text: secret[split:] + " after"},
		},
	}
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_cross_part_redaction", Model: "gpt-5.6-sol",
		Provider:     &fakeProvider{responses: [][]model.Event{completed([]model.Item{message}, model.Usage{})}},
		Capabilities: &fakeCapabilities{}, Transcript: store,
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "safe prompt")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "before [REDACTED] after" {
		t.Fatalf("outcome text = %q", outcome.Text)
	}
	store.mu.Lock()
	durable, marshalErr := json.Marshal(store.events)
	store.mu.Unlock()
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(durable), secret) {
		t.Fatalf("cross-part credential entered transcript: %s", durable)
	}
}

func TestEngineRejectsCredentialAcrossAssistantMessageNewlineCompositionWithoutTranscript(t *testing.T) {
	const secret = "abc\ndef"
	first := model.TextMessage(model.RoleAssistant, "abc")
	first.ID, first.Phase = "msg_first", "final_answer"
	second := model.TextMessage(model.RoleAssistant, "def")
	second.ID, second.Phase = "msg_second", "final_answer"
	assertProviderOutputRejectedBeforeAcceptance(t, "ses_joined_output_credential", secret, []model.Item{first, second})
}

func TestEngineRejectsCredentialAcrossSelectedFinalAnswerNewlineCompositionWithoutTranscript(t *testing.T) {
	const secret = "abc\ndef"
	first := model.TextMessage(model.RoleAssistant, "abc")
	first.ID, first.Phase = "msg_first_final", "final_answer"
	commentary := model.TextMessage(model.RoleAssistant, "safe")
	commentary.ID, commentary.Phase = "msg_commentary", "commentary"
	second := model.TextMessage(model.RoleAssistant, "def")
	second.ID, second.Phase = "msg_second_final", "final_answer"
	assertProviderOutputRejectedBeforeAcceptance(t, "ses_joined_final_credential", secret, []model.Item{first, commentary, second})
}

func TestEngineRejectsCredentialAcrossCompleteProviderOutputArrayWithoutTranscript(t *testing.T) {
	const secret = `abc"}]},{"type":"message"`
	first := model.TextMessage(model.RoleAssistant, "abc")
	first.ID, first.Phase = "msg_array_first", "commentary"
	second := model.TextMessage(model.RoleAssistant, "def")
	second.ID, second.Phase = "msg_array_second", "final_answer"
	output := []model.Item{first, second}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), secret) {
		t.Fatalf("provider-output fixture did not reconstruct credential: %s", encoded)
	}
	assertProviderOutputRejectedBeforeAcceptance(t, "ses_array_output_credential", secret, output)
}

func assertProviderOutputRejectedBeforeAcceptance(t *testing.T, sessionID, secret string, output []model.Item) {
	t.Helper()
	sink := &capturingSink{}
	query, err := New(Config{
		SessionID: protocol.SessionID(sessionID), Model: "gpt-5.6-sol",
		Provider:     &fakeProvider{responses: [][]model.Event{completed(output, model.Usage{})}},
		Capabilities: &fakeCapabilities{}, Sink: sink,
		// Deliberately omit Transcript: this engine boundary must not depend on
		// an application-owned transcript record validator.
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.Submit(t.Context(), "safe prompt")
	if !errors.Is(err, model.ErrProtocol) || outcome.Status != protocol.TurnResultError {
		t.Fatalf("provider composition outcome = %#v, err = %v", outcome, err)
	}
	if outcome.Text != "" {
		t.Fatalf("unsafe provider composition reached outcome: %q", outcome.Text)
	}
	if len(query.history) != 1 || query.history[0].Type != model.ItemMessage || query.history[0].Role != model.RoleUser {
		t.Fatalf("unsafe provider composition entered model history: %#v", query.history)
	}
	for _, event := range sink.events {
		if event.Origin == protocol.OriginModel {
			t.Fatalf("unsafe provider composition reached sink: %#v", event)
		}
	}
	encoded, marshalErr := json.Marshal(sink.events)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("credential reached sink encoding: %s", encoded)
	}
}

func TestProviderOwnedTimeoutIsNotClassifiedAsUserCancellation(t *testing.T) {
	if status := classifyTurnError(fmt.Errorf("wrapped: %w", model.ErrRequestTimeout)); status != protocol.TurnResultError {
		t.Fatalf("provider timeout status = %q", status)
	}
}

func TestContextPressureProjectsHistoryDurablyBeforeRequest(t *testing.T) {
	provider := &fakeProvider{responses: [][]model.Event{
		completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{InputTokens: 25_000, OutputTokens: 10, TotalTokens: 25_010}),
	}}
	store, err := transcript.Open(t.Context(), transcript.Config{
		Path: filepath.Join(t.TempDir(), "context.jsonl"), SessionID: "ses_context", SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	query, err := New(Config{
		SessionID: "ses_context", Model: "gpt-5.6-sol", Provider: provider,
		Capabilities: &fakeCapabilities{}, Transcript: store,
		InputContextTokens: 60_000, MaxOutputTokens: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 90; index++ {
		role := model.RoleUser
		if index%2 == 1 {
			role = model.RoleAssistant
		}
		item := model.TextMessage(role, fmt.Sprintf("history-%03d %s", index, strings.Repeat("x", 2_000)))
		if role == model.RoleAssistant {
			item.ID = fmt.Sprintf("msg_%03d", index)
		}
		query.history = append(query.history, item)
	}
	if _, err := query.Submit(t.Context(), "latest request"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d", len(provider.requests))
	}
	request := provider.requests[0]
	if len(request.Input) >= 92 || request.Input[0].Type != model.ItemMessage || request.Input[0].Role != model.RoleDeveloper || !strings.Contains(itemText(request.Input[0]), "<context-projection") {
		t.Fatalf("request was not projected: items=%d first=%#v", len(request.Input), request.Input[0])
	}
	if estimate := estimateRequestTokens(request.Instructions, request.Input, request.Tools); estimate >= query.thresholds.Hard {
		t.Fatalf("projected request estimate %d reaches hard threshold %d", estimate, query.thresholds.Hard)
	}

	snapshot, err := store.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	foundBoundary := false
	for _, event := range snapshot.Events {
		if event.Kind == protocol.EventKindSessionMetadata && event.Metadata.Key == contextProjectionKey {
			foundBoundary = true
		}
	}
	if !foundBoundary {
		t.Fatal("durable context projection boundary was not recorded")
	}
	restored, err := New(Config{
		SessionID: "ses_context", Model: "gpt-5.6-sol", Provider: &fakeProvider{},
		Capabilities: &fakeCapabilities{}, InputContextTokens: 60_000, MaxOutputTokens: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(restored.history) != len(query.history) {
		t.Fatalf("restored projection items=%d live=%d", len(restored.history), len(query.history))
	}
	for index := range query.history {
		if query.history[index].Type != restored.history[index].Type || query.history[index].ID != restored.history[index].ID || itemText(query.history[index]) != itemText(restored.history[index]) {
			t.Fatalf("restored projection item %d mismatch", index)
		}
	}
}

func TestManualCompactionIsDurableAndObservable(t *testing.T) {
	store, err := transcript.Open(t.Context(), transcript.Config{
		Path: filepath.Join(t.TempDir(), "manual-context.jsonl"), SessionID: "ses_manual_context", SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var observed []protocol.Event
	query, err := New(Config{
		SessionID: "ses_manual_context", Model: "gpt-5.6-sol", Provider: &fakeProvider{},
		Capabilities: &fakeCapabilities{}, Transcript: store,
		Sink: EventSinkFunc(func(_ context.Context, event protocol.Event) error {
			observed = append(observed, event)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 24; index++ {
		role := model.RoleUser
		if index%2 == 1 {
			role = model.RoleAssistant
		}
		item := model.TextMessage(role, fmt.Sprintf("manual-%02d %s", index, strings.Repeat("context ", 250)))
		if role == model.RoleAssistant {
			item.ID = fmt.Sprintf("msg_manual_%02d", index)
		}
		query.history = append(query.history, item)
	}
	before := len(query.history)
	if err := query.CompactContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(query.history) >= before || len(query.history) < 2 || query.history[0].Role != model.RoleDeveloper {
		t.Fatalf("manual projection items=%d before=%d first=%#v", len(query.history), before, query.history[0])
	}
	seenLifecycle := false
	for _, event := range observed {
		if event.Kind == protocol.EventKindCompaction && event.Compaction != nil && event.Compaction.Trigger == "manual" && event.Compaction.State == "completed" {
			seenLifecycle = true
		}
	}
	if !seenLifecycle {
		t.Fatal("manual compaction did not publish a completed lifecycle event")
	}
	snapshot, err := store.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seenBoundary := false
	for _, event := range snapshot.Events {
		if event.Kind != protocol.EventKindSessionMetadata || event.Metadata == nil || event.Metadata.Key != contextProjectionKey {
			continue
		}
		var projection contextProjection
		if err := json.Unmarshal(event.Metadata.Value, &projection); err != nil {
			t.Fatal(err)
		}
		seenBoundary = projection.Trigger == "manual"
	}
	if !seenBoundary {
		t.Fatal("manual projection boundary was not durable")
	}
	restored, err := New(Config{SessionID: "ses_manual_context", Model: "gpt-5.6-sol", Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(restored.history) != len(query.history) {
		t.Fatalf("restored manual projection items=%d live=%d", len(restored.history), len(query.history))
	}
}

func TestProjectionCutRetainsConfiguredMinimum(t *testing.T) {
	history := make([]model.Item, 10)
	for index := range history {
		history[index] = model.TextMessage(model.RoleUser, fmt.Sprintf("item-%d", index))
	}
	cut := chooseProjectionCutWithMinimum(history, 0, 8)
	if cut != 2 {
		t.Fatalf("cut=%d, want 2 so eight items remain", cut)
	}
}

func TestContextPressureRejectsIrreducibleRequestBeforeProvider(t *testing.T) {
	provider := &fakeProvider{}
	query, err := New(Config{
		SessionID: "ses_context_hard", Model: "gpt-5.6-sol", Provider: provider,
		Capabilities: &fakeCapabilities{}, InputContextTokens: 50_000, MaxOutputTokens: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Submit(t.Context(), strings.Repeat("oversized ", 20_000))
	if !errors.Is(err, ErrContextLimit) {
		t.Fatalf("oversized request error = %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("oversized request reached provider %d times", len(provider.requests))
	}
}

func TestProjectionCutPreservesProviderResponseSiblingsAndToolPairs(t *testing.T) {
	history := make([]model.Item, 12)
	for index := range history {
		history[index] = model.TextMessage(model.RoleAssistant, fmt.Sprintf("item-%d", index))
	}
	for index := 3; index <= 5; index++ {
		history[index].APIResponseID = "resp_group"
	}
	if cut := chooseProjectionCut(history, 0); cut != 3 {
		t.Fatalf("provider response group split at %d, want 3", cut)
	}

	history[2] = model.FunctionCall("fc_pair", "call_pair", "Read", `{}`)
	history[2].APIResponseID = "resp_call"
	history[9] = model.FunctionCallOutput("call_pair", "result")
	if cut := chooseProjectionCut(history, 0); cut != 2 {
		t.Fatalf("tool pair split at %d, want 2", cut)
	}
}

func TestUnknownModelRequiresExplicitInputContextLimit(t *testing.T) {
	base := Config{SessionID: "ses_custom", Model: "custom-deployment", Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}}
	if _, err := New(base); err == nil || !strings.Contains(err.Error(), "explicit input context limit") {
		t.Fatalf("unknown model without limit = %v", err)
	}
	base.InputContextTokens = 128_000
	if _, err := New(base); err != nil {
		t.Fatalf("unknown model with explicit limit: %v", err)
	}
}

func TestRestoreRejectsIncoherentContextProjectionMetadata(t *testing.T) {
	query, err := New(Config{SessionID: "ses_projection_tamper", Model: "gpt-5.6-sol", Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := protocol.NewMessageEvent(query.SessionID(), "turn_tamper", protocol.RoleUser, protocol.TextBlock("trusted visible history"))
	if err != nil {
		t.Fatal(err)
	}
	poison := contextProjection{
		Version: contextProjectionVersion, Trigger: "auto", EstimatedBefore: 10,
		EstimatedAfter: 1, DroppedItems: 1,
		Items: []model.Item{model.TextMessage(model.RoleDeveloper, strings.Repeat("poison", 10_000))},
	}
	data, _ := json.Marshal(poison)
	metadata, err := protocol.NewBaseEvent(query.SessionID(), "turn_tamper", protocol.EventKindSessionMetadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Visibility = protocol.VisibilityInternal
	metadata.Metadata = &protocol.MetadataEvent{Key: contextProjectionKey, Value: data}
	if err := query.Restore(transcript.Snapshot{SessionID: query.SessionID(), Events: []protocol.Event{user, metadata}}); err != nil {
		t.Fatal(err)
	}
	if len(query.history) != 1 || itemText(query.history[0]) != "trusted visible history" {
		t.Fatalf("incoherent projection poisoned restore: %#v", query.history)
	}
}

func TestToolResultBatchSettlementIsFairAndBounded(t *testing.T) {
	const settlementTimeout = 200 * time.Millisecond
	store := &boundedBatchSettlementStore{
		blocked: map[protocol.ToolUseID]struct{}{
			"call_slow_1": {},
			"call_slow_2": {},
			"call_slow_3": {},
		},
		attempts:  make(map[protocol.ToolUseID]int),
		deadlines: make(map[protocol.ToolUseID][]time.Time),
	}
	query, err := New(Config{
		SessionID: "ses_result_deadlines", Model: "gpt-5.6-sol", Provider: &fakeProvider{},
		Capabilities: &fakeCapabilities{}, Transcript: store, SettlementTimeout: settlementTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	results := []CapabilityResult{
		{ID: "call_slow_1", Name: "Read", Status: protocol.ToolResultSuccess, Content: "slow one"},
		{ID: "call_slow_2", Name: "Read", Status: protocol.ToolResultSuccess, Content: "slow two"},
		{ID: "call_slow_3", Name: "Read", Status: protocol.ToolResultSuccess, Content: "slow three"},
		{ID: "call_fast", Name: "Read", Status: protocol.ToolResultSuccess, Content: "fast"},
	}
	parents := map[protocol.ToolUseID]protocol.EventID{
		"call_slow_1": "evt_parent_slow_1",
		"call_slow_2": "evt_parent_slow_2",
		"call_slow_3": "evt_parent_slow_3",
		"call_fast":   "evt_parent_fast",
	}
	started := time.Now()
	if err := query.recordCapabilityResults(t.Context(), "turn_deadlines", results, parents); err == nil {
		t.Fatal("blocked results unexpectedly succeeded")
	}
	elapsed := time.Since(started)
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, result := range results {
		if store.attempts[result.ID] == 0 {
			t.Fatalf("result %s was never attempted: %#v", result.ID, store.attempts)
		}
		for _, deadline := range store.deadlines[result.ID] {
			if latest := started.Add(settlementTimeout + 75*time.Millisecond); deadline.After(latest) {
				t.Fatalf("result %s received a deadline beyond the batch budget: %s > %s", result.ID, deadline, latest)
			}
		}
	}
	foundFast := false
	for _, event := range store.events {
		if event.Kind == protocol.EventKindToolResult && event.ToolResult.ToolUseID == "call_fast" {
			foundFast = true
		}
	}
	if !foundFast {
		t.Fatal("later result did not receive a fair persistence opportunity")
	}
	foundFastHistory := false
	for _, item := range query.history {
		if item.Type != model.ItemFunctionCallOutput {
			continue
		}
		switch item.CallID {
		case "call_fast":
			if item.Output != "fast" {
				t.Fatalf("fast result entered live history with wrong content: %#v", item)
			}
			foundFastHistory = true
		case "call_slow_1", "call_slow_2", "call_slow_3":
			t.Fatalf("uncommitted blocked result entered live history: %#v", item)
		}
	}
	if !foundFastHistory {
		t.Fatal("durably committed fast result did not enter live model history")
	}
	if elapsed > 450*time.Millisecond {
		t.Fatalf("batch settlement took %s, want at most 450ms for a %s batch budget", elapsed, settlementTimeout)
	}
}

func TestNormalizeCapabilityResultPreservesCredentialSuppression(t *testing.T) {
	result := normalizeCapabilityResult(
		CapabilityCall{ID: "call_suppressed", Name: "Scoped"},
		CapabilityResult{Content: "must never survive", ContentSuppressed: true},
	)
	if result.Content != "" || !result.ContentSuppressed {
		t.Fatalf("suppressed result gained synthetic content: %+v", result)
	}
}

func TestCanonicalCapabilityErrorCodeUsesClosedVocabulary(t *testing.T) {
	admitted := []string{
		"call_batch_interrupted",
		"cancelled",
		"denied",
		"execution_failed",
		"hook_failed",
		"interrupted",
		"malformed_input",
		"malformed_result",
		"missing_terminal_result",
		"permission_denied",
		"permission_failed",
		"semantic_invalid",
		"sibling_error",
		"stale_file",
		"structural_invalid",
		"timeout",
		"unavailable",
		"unknown_tool",
		"user_interrupted",
	}
	for _, code := range admitted {
		t.Run("admitted_"+code, func(t *testing.T) {
			if got := canonicalCapabilityErrorCode(code); got != code {
				t.Fatalf("canonicalCapabilityErrorCode(%q) = %q, want exact admitted code", code, got)
			}
		})
	}
	for name, code := range map[string]string{
		"empty":      "",
		"unknown":    "provider_specific_failure",
		"control":    "denied\nforged_code",
		"credential": "production-secret-error-code",
	} {
		t.Run("rejected_"+name, func(t *testing.T) {
			if got := canonicalCapabilityErrorCode(code); got != "tool_error" {
				t.Fatalf("canonicalCapabilityErrorCode(%q) = %q, want tool_error", code, got)
			}
		})
	}
}

func TestToolResultFramingCredentialIsSuppressedBeforeTranscriptAndContinuation(t *testing.T) {
	const secret = `foo"}],"is_error":false`
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_result_framing", Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
		Transcript: store, CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := protocol.EventID("evt_parent")
	result := CapabilityResult{
		ID: "call_framing", Name: "Read", Status: protocol.ToolResultSuccess,
		Content: "foo",
	}
	fixture, err := protocol.NewToolResultEvent(query.SessionID(), "turn_framing", protocol.ToolResult{
		ToolUseID: result.ID, ToolName: result.Name, Status: result.Status,
		Content: []protocol.ContentBlock{protocol.TextBlock(result.Content)},
		IsError: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.ParentID = &parent
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), secret) {
		t.Fatalf("fixture did not reconstruct credential: %s", encoded)
	}
	if err := query.recordCapabilityResults(t.Context(), "turn_framing", []CapabilityResult{result}, map[protocol.ToolUseID]protocol.EventID{result.ID: parent}); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 || store.events[0].ToolResult == nil ||
		store.events[0].ToolResult.Content[0].Text != "" {
		t.Fatalf("unsafe result was not suppressed before persistence: %#v", store.events)
	}
	if len(query.history) != 1 || query.history[0].Output != "" {
		t.Fatalf("unsafe result entered continuation history: %#v", query.history)
	}
}

func TestFinishJoinsPrimaryAppendAndFlushErrors(t *testing.T) {
	primary := errors.New("primary failure")
	appendFailure := errors.New("turn-result append failure")
	flushFailure := errors.New("transcript flush failure")
	query, err := New(Config{
		SessionID: "ses_finish_errors", Model: "gpt-5.6-sol", Provider: &fakeProvider{},
		Capabilities: &fakeCapabilities{}, Transcript: &finalizationFaultStore{appendErr: appendFailure, flushErr: flushFailure},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.finish(t.Context(), Outcome{SessionID: query.SessionID(), TurnID: "turn_finish"}, protocol.TurnResultError, "provider_error", primary, time.Now())
	for label, target := range map[string]error{"primary": primary, "append": appendFailure, "flush": flushFailure} {
		if !errors.Is(err, target) {
			t.Fatalf("finish lost %s error: %v", label, err)
		}
	}
}

func TestFinishSanitizesTurnResultErrorBeforeBounding(t *testing.T) {
	const secret = "production-subscription-secret"
	store := &faultStore{}
	query, err := New(Config{
		SessionID: "ses_finish_redaction", Model: "gpt-5.6-sol", Provider: &fakeProvider{},
		Capabilities: &fakeCapabilities{}, Transcript: store,
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Place the credential across the old 2,000-byte truncation boundary. A
	// truncate-before-redact implementation exposes its leading bytes because
	// the sanitizer can no longer recognize the complete literal.
	cause := errors.New(strings.Repeat("x", 1990) + secret + " trailing diagnostic")
	_, returned := query.finish(t.Context(), Outcome{SessionID: query.SessionID(), TurnID: "turn_redacted"}, protocol.TurnResultError, "provider_error", cause, time.Now())
	if !errors.Is(returned, cause) {
		t.Fatalf("finish returned %v, want original cause", returned)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 || store.events[0].TurnResult == nil {
		t.Fatalf("recorded events = %#v", store.events)
	}
	message := store.events[0].TurnResult.Message
	if strings.Contains(message, secret) || strings.Contains(message, secret[:10]) {
		t.Fatalf("turn-result event leaked credential material: %q", message)
	}
	if !strings.Contains(message, "[REDACTED]") || len(message) > 2000 || !utf8.ValidString(message) {
		t.Fatalf("turn-result message was not safely normalized: len=%d valid=%v message=%q", len(message), utf8.ValidString(message), message)
	}
}
