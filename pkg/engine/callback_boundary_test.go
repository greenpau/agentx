package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
)

type boundaryProviderFunc func(context.Context, model.Request) (model.Stream, error)

func (f boundaryProviderFunc) Stream(ctx context.Context, request model.Request) (model.Stream, error) {
	return f(ctx, request)
}

type boundaryStream struct {
	next  func() (model.Event, error)
	close func() error
}

func (s *boundaryStream) Next() (model.Event, error) { return s.next() }
func (s *boundaryStream) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}

type boundaryCapabilities struct {
	schemas func() []model.Tool
	execute func(context.Context, []CapabilityCall) []CapabilityResult
}

func (c *boundaryCapabilities) Schemas() []model.Tool {
	return c.schemas()
}

func (c *boundaryCapabilities) Execute(ctx context.Context, calls []CapabilityCall) []CapabilityResult {
	return c.execute(ctx, calls)
}

type hostileSingleError struct {
	cause       error
	message     string
	errorCalls  int
	isCalls     int
	asCalls     int
	unwrapCalls int
	panicError  bool
	panicUnwrap bool
}

func (e *hostileSingleError) Error() string {
	e.errorCalls++
	if e.panicError {
		panic("hostile Error")
	}
	if e.message == "" {
		return "hostile error"
	}
	return fmt.Sprintf("%s-%d", e.message, e.errorCalls)
}

func (e *hostileSingleError) Is(error) bool {
	e.isCalls++
	panic("custom Is must not run")
}

func (e *hostileSingleError) As(any) bool {
	e.asCalls++
	panic("custom As must not run")
}

func (e *hostileSingleError) Unwrap() error {
	e.unwrapCalls++
	if e.panicUnwrap {
		panic("hostile Unwrap")
	}
	return e.cause
}

type hostileMultiError struct {
	children    []error
	errorCalls  int
	isCalls     int
	asCalls     int
	unwrapCalls int
}

func (e *hostileMultiError) Error() string {
	e.errorCalls++
	return "hostile multi error"
}

func (e *hostileMultiError) Is(error) bool {
	e.isCalls++
	panic("custom Is must not run")
}

func (e *hostileMultiError) As(any) bool {
	e.asCalls++
	panic("custom As must not run")
}

func (e *hostileMultiError) Unwrap() []error {
	e.unwrapCalls++
	return e.children
}

type blockingEngineUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingEngineUnwrapError) Error() string { return "foreign provider failure" }
func (e *blockingEngineUnwrapError) Unwrap() error {
	e.once.Do(func() { close(e.called) })
	<-e.release
	return context.Canceled
}

func newBoundaryTestEngine(t *testing.T, provider model.Provider, capabilities Capabilities) *Engine {
	t.Helper()
	query, err := New(Config{
		SessionID:    "ses_callback_boundary",
		Model:        "gpt-5.6-sol",
		Provider:     provider,
		Capabilities: capabilities,
		MaxTurns:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func TestEngineErrorInspectionDoesNotExecuteForeignErrorMethods(t *testing.T) {
	t.Run("public classification retains only exact foreign root", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		cause := &hostileSingleError{cause: fmt.Errorf("wrapped: %w", sentinel), message: "stateful"}
		query := newBoundaryTestEngine(t, &fakeProvider{}, &fakeCapabilities{})

		public := query.publicError(cause)
		if errors.Is(public, sentinel) || !errors.Is(public, cause) {
			t.Fatalf("public classification was not retained: %v", public)
		}
		if got := public.Error(); got != "engine operation failed" {
			t.Fatalf("public error projection = %q", got)
		}
		if got := fmt.Sprintf("%v", public); got != "engine operation failed" {
			t.Fatalf("formatted public error changed projection: %q", got)
		}
		if cause.errorCalls != 0 || cause.unwrapCalls != 0 || cause.isCalls != 0 || cause.asCalls != 0 {
			t.Fatalf("hostile method calls = Error:%d Unwrap:%d Is:%d As:%d", cause.errorCalls, cause.unwrapCalls, cause.isCalls, cause.asCalls)
		}

		trusted := query.publicError(fmt.Errorf("wrapped: %w", sentinel))
		if !errors.Is(trusted, sentinel) || trusted.Error() != "wrapped: sentinel" {
			t.Fatalf("standard wrapper classification = %v", trusted)
		}
	})

	t.Run("panicking error and unwrap", func(t *testing.T) {
		query := newBoundaryTestEngine(t, &fakeProvider{}, &fakeCapabilities{})
		panicText := &hostileSingleError{panicError: true, panicUnwrap: true}
		public := query.publicError(panicText)
		if got := public.Error(); got != "engine operation failed" {
			t.Fatalf("panicking error projection = %q", got)
		}
		if !errors.Is(public, panicText) {
			t.Fatal("panicking error lost exact identity classification")
		}
		if panicText.errorCalls != 0 || panicText.unwrapCalls != 0 || panicText.isCalls != 0 || panicText.asCalls != 0 {
			t.Fatalf("panicking method calls = Error:%d Unwrap:%d Is:%d As:%d", panicText.errorCalls, panicText.unwrapCalls, panicText.isCalls, panicText.asCalls)
		}
	})

	t.Run("foreign cycle and width are opaque", func(t *testing.T) {
		cycle := &hostileSingleError{message: "cycle"}
		cycle.cause = cycle
		inspection := inspectEngineError(cycle)
		if len(inspection.matches) != 1 || cycle.unwrapCalls != 0 {
			t.Fatalf("cycle inspection = matches:%d unwraps:%d", len(inspection.matches), cycle.unwrapCalls)
		}

		late := errors.New("outside bounded prefix")
		children := make([]error, 10_000)
		children[0] = context.DeadlineExceeded
		for index := 1; index < len(children)-1; index++ {
			children[index] = errors.New("filler")
		}
		children[len(children)-1] = late
		wide := &hostileMultiError{children: children}
		inspection = inspectEngineError(wide)
		if inspection.deadline || len(inspection.matches) != 1 || wide.unwrapCalls != 0 {
			t.Fatalf("wide inspection = deadline:%v matches:%d unwraps:%d", inspection.deadline, len(inspection.matches), wide.unwrapCalls)
		}
		query := newBoundaryTestEngine(t, &fakeProvider{}, &fakeCapabilities{})
		public := query.publicError(wide)
		if errors.Is(public, late) {
			t.Fatal("bounded inspection retained a node beyond its fixed budget")
		}
		if wide.isCalls != 0 || wide.asCalls != 0 {
			t.Fatalf("wide error custom methods ran: Is:%d As:%d", wide.isCalls, wide.asCalls)
		}
	})

	t.Run("classification ignores custom Is", func(t *testing.T) {
		falseCancellation := &hostileSingleError{message: "not cancelled"}
		if got := classifyTurnError(falseCancellation); got != protocol.TurnResultError {
			t.Fatalf("custom Is manufactured cancellation: %s", got)
		}
		if inspectEngineError(falseCancellation).eof {
			t.Fatal("custom Is manufactured EOF")
		}
		if engineErrorIs(falseCancellation, ErrContextLimit) {
			t.Fatal("custom Is manufactured an internal sentinel match")
		}
		if falseCancellation.isCalls != 0 || falseCancellation.errorCalls != 0 {
			t.Fatalf("classification invoked hostile methods: Is:%d Error:%d", falseCancellation.isCalls, falseCancellation.errorCalls)
		}

		foreignCancellation := &hostileSingleError{cause: context.Canceled}
		if got := classifyTurnError(foreignCancellation); got != protocol.TurnResultError {
			t.Fatalf("foreign unwrap manufactured cancellation: %s", got)
		}
		if got := classifyTurnError(fmt.Errorf("wrapped: %w", context.Canceled)); got != protocol.TurnResultCancelled {
			t.Fatalf("trusted wrapper lost cancellation: %s", got)
		}
		if !inspectEngineError(io.EOF).eof {
			t.Fatal("exact EOF lost classification")
		}
		if !inspectEngineErrorWithContext(foreignCancellation, context.Canceled).cancelled {
			t.Fatal("owned context state did not preserve cancellation")
		}
	})
}

func TestEngineProviderErrorDiscoveryAvoidsCustomAs(t *testing.T) {
	const secret = "provider-field-credential"
	split := len(secret) / 2
	providerError := &model.ProviderError{Code: secret[:split], Message: secret[split:]}
	wrapper := &hostileSingleError{cause: providerError, message: "provider wrapper"}
	query, err := New(Config{
		SessionID: "ses_provider_discovery", Model: "gpt-5.6-sol",
		Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	checked := query.validateProviderErrorCause(wrapper)
	if errors.Is(checked, model.ErrProtocol) || checked.Error() != "engine operation failed" {
		t.Fatalf("foreign provider wrapper received nested classification: %v", checked)
	}
	if wrapper.errorCalls != 0 || wrapper.isCalls != 0 || wrapper.asCalls != 0 || wrapper.unwrapCalls != 0 {
		t.Fatalf("provider inspection method calls = Error:%d Is:%d As:%d Unwrap:%d", wrapper.errorCalls, wrapper.isCalls, wrapper.asCalls, wrapper.unwrapCalls)
	}

	checked = query.validateProviderErrorCause(providerError)
	if !errors.Is(checked, model.ErrProtocol) {
		t.Fatalf("exact provider error metadata was not rejected: %v", checked)
	}
}

func TestEngineFreezesProviderErrorBeforeTurnClassification(t *testing.T) {
	cause := &hostileSingleError{cause: context.Canceled, message: "provider state"}
	query := newBoundaryTestEngine(t, failingProvider{err: cause}, &fakeCapabilities{})
	outcome, returned := query.Submit(t.Context(), "hello")
	if returned == nil || errors.Is(returned, context.Canceled) || outcome.Status != protocol.TurnResultError {
		t.Fatalf("provider classification outcome=%+v err=%v", outcome, returned)
	}
	if cause.errorCalls != 0 || cause.unwrapCalls != 0 || cause.isCalls != 0 || cause.asCalls != 0 {
		t.Fatalf("provider error methods = Error:%d Unwrap:%d Is:%d As:%d", cause.errorCalls, cause.unwrapCalls, cause.isCalls, cause.asCalls)
	}
	if returned.Error() != "engine operation failed" || cause.errorCalls != 0 {
		t.Fatalf("provider error snapshot changed: %q calls=%d", returned, cause.errorCalls)
	}

	query = newBoundaryTestEngine(t, failingProvider{err: context.Canceled}, &fakeCapabilities{})
	outcome, returned = query.Submit(t.Context(), "hello")
	if returned == nil || !errors.Is(returned, context.Canceled) ||
		outcome.Status != protocol.TurnResultCancelled || outcome.StopReason != "cancelled" {
		t.Fatalf("exact provider cancellation outcome=%+v err=%v", outcome, returned)
	}
}

func TestEngineSubmitDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingEngineUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	query := newBoundaryTestEngine(t, failingProvider{err: cause}, &fakeCapabilities{})
	type result struct {
		outcome Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := query.Submit(context.Background(), "hello")
		done <- result{outcome: outcome, err: err}
	}()

	select {
	case returned := <-done:
		if returned.err == nil || returned.err.Error() != "engine operation failed" ||
			returned.outcome.Status != protocol.TurnResultError {
			t.Fatalf("blocking error projection outcome=%+v err=%v", returned.outcome, returned.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Engine.Submit blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Engine.Submit invoked foreign Unwrap")
	default:
	}
}

func TestEngineProviderCallbacksContainPanicsAndNilStreams(t *testing.T) {
	t.Run("provider stream panic", func(t *testing.T) {
		provider := boundaryProviderFunc(func(context.Context, model.Request) (model.Stream, error) {
			panic("provider panic")
		})
		query := newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("provider panic error = %v", err)
		}
	})

	t.Run("nil stream", func(t *testing.T) {
		provider := boundaryProviderFunc(func(context.Context, model.Request) (model.Stream, error) {
			return nil, nil
		})
		query := newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("nil stream error = %v", err)
		}
	})

	t.Run("typed nil provider and stream", func(t *testing.T) {
		var nilProvider boundaryProviderFunc
		query := newBoundaryTestEngine(t, nilProvider, &fakeCapabilities{})
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("typed nil provider error = %v", err)
		}

		provider := boundaryProviderFunc(func(context.Context, model.Request) (model.Stream, error) {
			var stream *boundaryStream
			return stream, nil
		})
		query = newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("typed nil stream error = %v", err)
		}
	})

	t.Run("next and close panic", func(t *testing.T) {
		closeCalls := 0
		provider := boundaryProviderFunc(func(context.Context, model.Request) (model.Stream, error) {
			return &boundaryStream{
				next: func() (model.Event, error) { panic("next panic") },
				close: func() error {
					closeCalls++
					panic("close panic")
				},
			}, nil
		})
		query := newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("stream panic error = %v", err)
		}
		if closeCalls != 1 {
			t.Fatalf("stream close calls = %d, want 1", closeCalls)
		}
	})

	t.Run("successful stream ignores close panic", func(t *testing.T) {
		events := completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{})
		index := 0
		closeCalls := 0
		provider := boundaryProviderFunc(func(context.Context, model.Request) (model.Stream, error) {
			return &boundaryStream{
				next: func() (model.Event, error) {
					if index >= len(events) {
						return model.Event{}, io.EOF
					}
					event := events[index]
					index++
					return event, nil
				},
				close: func() error {
					closeCalls++
					panic("close panic")
				},
			}, nil
		})
		query := newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		outcome, err := query.Submit(t.Context(), "hello")
		if err != nil || outcome.Status != protocol.TurnResultSuccess || outcome.Text != "done" {
			t.Fatalf("successful close-panic outcome=%+v err=%v", outcome, err)
		}
		if closeCalls != 1 {
			t.Fatalf("stream close calls = %d, want 1", closeCalls)
		}
	})
}

func TestEngineCapabilityCallbacksContainPanicsAndSettleCalls(t *testing.T) {
	t.Run("schema panic", func(t *testing.T) {
		capabilities := &boundaryCapabilities{
			schemas: func() []model.Tool { panic("schema panic") },
			execute: func(context.Context, []CapabilityCall) []CapabilityResult { return nil },
		}
		provider := &fakeProvider{}
		query := newBoundaryTestEngine(t, provider, capabilities)
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("schema panic error = %v", err)
		}
		provider.mu.Lock()
		requests := len(provider.requests)
		provider.mu.Unlock()
		if requests != 0 {
			t.Fatalf("provider received %d requests after schema panic", requests)
		}
	})

	t.Run("typed nil capability callbacks", func(t *testing.T) {
		var capabilities *boundaryCapabilities
		provider := &fakeProvider{}
		query := newBoundaryTestEngine(t, provider, capabilities)
		if _, err := query.Submit(t.Context(), "hello"); !errors.Is(err, model.ErrProtocol) {
			t.Fatalf("typed nil schema error = %v", err)
		}
		results := query.executeExactlyOnce(t.Context(), []CapabilityCall{{
			ID: "call_typed_nil", Name: "Read", Arguments: json.RawMessage(`{}`),
		}})
		if len(results) != 1 || !results[0].Synthetic || results[0].Status != protocol.ToolResultInterrupted {
			t.Fatalf("typed nil execute results = %#v", results)
		}
	})

	t.Run("execute panic synthesizes one terminal result", func(t *testing.T) {
		call := model.FunctionCall("fc_panic", "call_panic", "Read", `{}`)
		provider := &fakeProvider{responses: [][]model.Event{
			completed([]model.Item{call}, model.Usage{}),
			completed([]model.Item{model.TextMessage(model.RoleAssistant, "recovered")}, model.Usage{}),
		}}
		capabilities := &boundaryCapabilities{
			schemas: func() []model.Tool {
				return []model.Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}}
			},
			execute: func(context.Context, []CapabilityCall) []CapabilityResult {
				panic("execute panic")
			},
		}
		sink := &capturingSink{}
		query, err := New(Config{
			SessionID: "ses_capability_panic", Model: "gpt-5.6-sol",
			Provider: provider, Capabilities: capabilities, Sink: sink, MaxTurns: 3,
		})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := query.Submit(t.Context(), "hello")
		if err != nil || outcome.Status != protocol.TurnResultSuccess || outcome.Text != "recovered" {
			t.Fatalf("capability-panic outcome=%+v err=%v", outcome, err)
		}
		var terminal []protocol.ToolResult
		for _, event := range sink.events {
			if event.ToolResult != nil && event.ToolResult.ToolUseID == "call_panic" {
				terminal = append(terminal, *event.ToolResult)
			}
		}
		if len(terminal) != 1 || !terminal[0].Synthetic || terminal[0].Status != protocol.ToolResultInterrupted || terminal[0].Error == nil || terminal[0].Error.Code != "missing_terminal_result" {
			t.Fatalf("synthetic terminal results = %#v", terminal)
		}
	})
}

type panicAppendStore struct {
	events []protocol.Event
}

func (s *panicAppendStore) AppendEvent(_ context.Context, event protocol.Event) (protocol.Event, bool, error) {
	s.events = append(s.events, cloneProtocolEvent(event))
	panic("append acknowledgement panic")
}

func (s *panicAppendStore) Flush(context.Context) error { return nil }

type panicFlushStore struct{}

func (*panicFlushStore) AppendEvent(_ context.Context, event protocol.Event) (protocol.Event, bool, error) {
	return event, true, nil
}

func (*panicFlushStore) Flush(context.Context) error { panic("flush panic") }

func TestEnginePersistenceAndSinkCallbacksContainPanics(t *testing.T) {
	t.Run("append panic remains an uncertain accepted write", func(t *testing.T) {
		store := &panicAppendStore{}
		query, err := New(Config{
			SessionID: "ses_append_panic", Model: "gpt-5.6-sol",
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}, Transcript: store,
		})
		if err != nil {
			t.Fatal(err)
		}
		call := protocol.NewRawToolCall("call_uncertain", "Read", `{}`)
		event, err := protocol.NewToolCallEvent(query.SessionID(), "turn_uncertain", call)
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := query.record(t.Context(), event)
		if !isEventPersistenceUncertain(err) {
			t.Fatalf("append panic lost uncertainty classification: %v", err)
		}
		if recorded.ID != event.ID || len(store.events) != 1 || store.events[0].ID != event.ID {
			t.Fatalf("stable accepted identity was lost: recorded=%s stored=%#v", recorded.ID, store.events)
		}
	})

	t.Run("flush panic", func(t *testing.T) {
		query, err := New(Config{
			SessionID: "ses_flush_panic", Model: "gpt-5.6-sol",
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}, Transcript: &panicFlushStore{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := query.flush(); err == nil || !strings.Contains(err.Error(), "flush callback panicked") {
			t.Fatalf("flush panic error = %v", err)
		}
	})

	t.Run("sink panic is a committed delivery failure", func(t *testing.T) {
		query, err := New(Config{
			SessionID: "ses_sink_panic", Model: "gpt-5.6-sol",
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{},
			Sink: EventSinkFunc(func(context.Context, protocol.Event) error { panic("sink panic") }),
		})
		if err != nil {
			t.Fatal(err)
		}
		event, err := protocol.NewMessageEvent(query.SessionID(), "turn_sink", protocol.RoleUser, protocol.TextBlock("hello"))
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := query.record(t.Context(), event)
		if !isEventDeliveryError(err) || recorded.ID != event.ID {
			t.Fatalf("sink panic classification=%v recorded=%s", err, recorded.ID)
		}
	})

	t.Run("callback error methods remain isolated", func(t *testing.T) {
		hostile := &hostileSingleError{panicError: true, panicUnwrap: true}
		store := &finalizationFaultStore{appendErr: hostile}
		query, err := New(Config{
			SessionID: "ses_hostile_store_error", Model: "gpt-5.6-sol",
			Provider: &fakeProvider{}, Capabilities: &fakeCapabilities{}, Transcript: store,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, returned := query.Submit(t.Context(), "hello")
		if returned == nil || !errors.Is(returned, hostile) {
			t.Fatalf("hostile store classification was lost: %v", returned)
		}
		if hostile.errorCalls != 0 || hostile.isCalls != 0 || hostile.asCalls != 0 || hostile.unwrapCalls != 0 {
			t.Fatalf("hostile callback error methods = Error:%d Is:%d As:%d Unwrap:%d", hostile.errorCalls, hostile.isCalls, hostile.asCalls, hostile.unwrapCalls)
		}
	})
}

func TestEngineDeepCopiesMutableCallbackBoundaries(t *testing.T) {
	t.Run("provider request", func(t *testing.T) {
		parallel := true
		request := model.Request{
			Model:             "gpt-5.6-sol",
			Input:             []model.Item{model.TextMessage(model.RoleUser, "original input")},
			Tools:             []model.Tool{{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}},
			ParallelToolCalls: &parallel,
			Metadata:          map[string]string{"owner": "engine"},
		}
		provider := boundaryProviderFunc(func(_ context.Context, callback model.Request) (model.Stream, error) {
			callback.Input[0].Content[0].Text = "mutated input"
			callback.Tools[0].Parameters[0] = '['
			callback.Metadata["owner"] = "provider"
			*callback.ParallelToolCalls = false
			return &fakeStream{events: completed([]model.Item{model.TextMessage(model.RoleAssistant, "done")}, model.Usage{})}, nil
		})
		query := newBoundaryTestEngine(t, provider, &fakeCapabilities{})
		if _, _, _, _, err := query.runModel(t.Context(), "turn_copy", request); err != nil {
			t.Fatal(err)
		}
		if got := request.Input[0].Content[0].Text; got != "original input" {
			t.Fatalf("provider mutated caller input: %q", got)
		}
		if got := string(request.Tools[0].Parameters); got != `{"type":"object"}` {
			t.Fatalf("provider mutated caller schema: %q", got)
		}
		if request.Metadata["owner"] != "engine" || !parallel {
			t.Fatalf("provider mutated metadata or pointer: metadata=%v parallel=%v", request.Metadata, parallel)
		}
	})

	t.Run("returned schemas and capability arguments", func(t *testing.T) {
		parameters := json.RawMessage(`{"type":"object"}`)
		var callbackCalls []CapabilityCall
		capabilities := &boundaryCapabilities{
			schemas: func() []model.Tool {
				return []model.Tool{{Name: "Read", Parameters: parameters}}
			},
			execute: func(_ context.Context, calls []CapabilityCall) []CapabilityResult {
				callbackCalls = calls
				calls[0].Arguments[0] = '['
				return []CapabilityResult{{ID: calls[0].ID, Status: protocol.ToolResultSuccess, Content: "ok"}}
			},
		}
		query := newBoundaryTestEngine(t, &fakeProvider{}, capabilities)
		schemas, err := query.capabilitySchemas()
		if err != nil {
			t.Fatal(err)
		}
		schemas[0].Parameters[0] = '['
		if got := string(parameters); got != `{"type":"object"}` {
			t.Fatalf("schema callback retained mutable Parameters alias: %q", got)
		}

		calls := []CapabilityCall{{ID: "call_copy", Name: "Read", Arguments: json.RawMessage(`{"path":"README.md"}`)}}
		results := query.executeExactlyOnce(t.Context(), calls)
		if got := string(calls[0].Arguments); got != `{"path":"README.md"}` {
			t.Fatalf("capability mutated accepted arguments: %q", got)
		}
		if len(callbackCalls) != 1 || len(results) != 1 || results[0].ID != calls[0].ID {
			t.Fatalf("callback calls=%#v results=%#v", callbackCalls, results)
		}
	})
}

func TestEngineFinishSnapshotsHostileErrorTextOnce(t *testing.T) {
	cause := &hostileSingleError{message: "changing"}
	query := newBoundaryTestEngine(t, &fakeProvider{}, &fakeCapabilities{})
	_, returned := query.finish(
		t.Context(),
		Outcome{SessionID: query.SessionID(), TurnID: "turn_error_snapshot"},
		protocol.TurnResultError,
		"provider_error",
		cause,
		query.config.Now(),
	)
	if returned == nil || returned.Error() != "engine operation failed" || fmt.Sprintf("%v", returned) != "engine operation failed" {
		t.Fatalf("returned public error = %v", returned)
	}
	if cause.errorCalls != 0 || cause.isCalls != 0 || cause.asCalls != 0 || cause.unwrapCalls != 0 {
		t.Fatalf("finish invoked hostile methods = Error:%d Is:%d As:%d", cause.errorCalls, cause.isCalls, cause.asCalls)
	}
}
