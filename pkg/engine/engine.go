// Package engine owns the shared, presentation-independent conversation turn
// loop. Provider, capability, transcript, and surface boundaries are injected.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/compact"
	"github.com/greenpau/agentx/pkg/identity"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/transcript"
)

const (
	DefaultMaxTurns        = 100
	DefaultMaxOutputTokens = 128_000
	// DefaultInputContextTokens is a conservative AgentX request-policy ceiling
	// for the deployment-backed gpt-5.6-sol model. It is not a claim about the
	// provider's maximum; callers must configure an explicit value for any other
	// model so the runtime never guesses an unknown deployment's capacity.
	DefaultInputContextTokens = 128_000
	DefaultSettlementTimeout  = 2 * time.Second
	maximumModelStreamEvents  = 100_000
	// Provider output is persisted as JSONL. Four MiB leaves enough headroom
	// for worst-case JSON escaping inside the 32 MiB durable-record boundary.
	maximumModelTextBytes     = 4 << 20
	maximumModelResponseItems = 4_096
	maximumModelToolCalls     = 256
	maximumModelCallArguments = 1 << 20
	maximumModelResponseBytes = 4 << 20
	providerOutputKey         = "provider_response_output"
	contextClearKey           = "context_clear"
	contextProjectionKey      = "context_projection"
	reasoningEffortKey        = "reasoning_effort"
	contextProjectionVersion  = 1
	maximumProjectionBytes    = 4 << 20
	projectionSummaryBytes    = 24 << 10
	projectionTargetTokens    = 40_000
	minimumPreservedItems     = 8
)

var (
	ErrBusy            = errors.New("session already has an active turn")
	ErrDuplicatePrompt = errors.New("prompt identifier was already accepted")
	ErrMaxTurns        = errors.New("maximum model turns reached")
	ErrContextLimit    = errors.New("model input context limit reached")
)

type eventDeliveryError struct {
	inspection engineErrorInspection
}

func (e *eventDeliveryError) Error() string { return "deliver committed event" }

func isEventDeliveryError(err error) bool {
	return inspectEngineError(err).delivery
}

// eventPersistenceError means the complete event record reached the append
// stream but its durability acknowledgement or post-write identity check
// failed. The event must not be executed, yet its stable identity still belongs
// in the exact-settlement set because recovery may observe it as accepted.
type eventPersistenceError struct {
	inspection engineErrorInspection
}

func (e *eventPersistenceError) Error() string {
	return "acknowledge appended event"
}

func isEventPersistenceUncertain(err error) bool {
	return inspectEngineError(err).persistence
}

type CapabilityCall struct {
	ID             protocol.ToolUseID
	ProviderItemID string
	Name           string
	Arguments      json.RawMessage
}

type CapabilityResult struct {
	ID                protocol.ToolUseID
	Name              string
	Status            protocol.ToolResultStatus
	Content           string
	ContentSuppressed bool
	Code              string
	IsError           bool
	Synthetic         bool
	Duration          time.Duration
	PermissionDenial  *PermissionDenial
}

// PermissionDenial is ephemeral surface evidence for a rejected permission
// decision. The terminal tool result remains the durable/model-visible record.
type PermissionDenial struct {
	ToolName  string             `json:"tool_name"`
	ToolUseID protocol.ToolUseID `json:"tool_use_id"`
	ToolInput json.RawMessage    `json:"tool_input"`
}

type Capabilities interface {
	Schemas() []model.Tool
	Execute(context.Context, []CapabilityCall) []CapabilityResult
}

type EventSink interface {
	Publish(context.Context, protocol.Event) error
}

type EventSinkFunc func(context.Context, protocol.Event) error

func (f EventSinkFunc) Publish(ctx context.Context, event protocol.Event) error { return f(ctx, event) }

type Store interface {
	AppendEvent(context.Context, protocol.Event) (protocol.Event, bool, error)
	Flush(context.Context) error
}

type Config struct {
	SessionID       protocol.SessionID
	Model           string
	ReasoningEffort string
	Instructions    string
	MaxTurns        int
	MaxOutputTokens int
	// InputContextTokens is an application-owned upper bound for model input.
	// Zero selects the conservative default only for gpt-5.6-sol.
	InputContextTokens int
	// SettlementTimeout bounds settlement of the complete accepted-result
	// batch. Sequential writes divide the remaining budget fairly so one
	// stalled result cannot prevent later siblings from being attempted.
	SettlementTimeout time.Duration
	Provider          model.Provider
	Capabilities      Capabilities
	Transcript        Store
	Sink              EventSink
	// Sanitize removes host-owned credential material from every model-visible
	// or presentation-visible text boundary. It must be deterministic. New
	// callers should also supply CredentialSanitizer so structured JSON can be
	// inspected semantically rather than only as its provider wire spelling.
	Sanitize func(string) string
	// CredentialSanitizer is the exact immutable credential set for this
	// session. It is always applied after Sanitize and owns semantic inspection
	// of model-produced JSON before any durable acceptance.
	CredentialSanitizer *redact.Set
	Now                 func() time.Time
}

type Engine struct {
	config Config

	turnMu            sync.Mutex
	clockActive       atomic.Bool
	mu                sync.Mutex
	promptMu          sync.RWMutex
	statusMu          sync.RWMutex
	status            Status
	active            bool
	promptIDs         map[string]struct{}
	history           []model.Item
	lastEvent         *protocol.EventID
	sequence          uint64
	usage             protocol.Usage
	permissionDenials []PermissionDenial
	thresholds        compact.Thresholds
	compactor         compact.Controller
}

type contextProjection struct {
	Version         int          `json:"version"`
	Trigger         string       `json:"trigger"`
	EstimatedBefore int          `json:"estimated_before"`
	EstimatedAfter  int          `json:"estimated_after"`
	DroppedItems    int          `json:"dropped_items"`
	Items           []model.Item `json:"items"`
}

type Outcome struct {
	SessionID         protocol.SessionID
	TurnID            protocol.TurnID
	Status            protocol.TurnResultStatus
	Text              string
	StopReason        string
	ModelTurns        int
	Usage             protocol.Usage
	Duration          time.Duration
	APIDuration       time.Duration
	PermissionDenials []PermissionDenial
}

type Status struct {
	SessionID       protocol.SessionID
	Model           string
	ReasoningEffort string
	Active          bool
	ProjectedItems  int
	Usage           protocol.Usage
}

func New(config Config) (*Engine, error) {
	if config.SessionID == "" || strings.TrimSpace(config.Model) == "" || config.Provider == nil || config.Capabilities == nil {
		return nil, publicConfigError(config, errors.New("engine configuration is incomplete"), "engine configuration is incomplete")
	}
	if config.ReasoningEffort != "" && !validReasoningEffort(config.ReasoningEffort) {
		return nil, publicConfigError(config, errors.New("unsupported reasoning effort"), "unsupported reasoning effort")
	}
	if config.MaxTurns <= 0 {
		config.MaxTurns = DefaultMaxTurns
	}
	if config.MaxOutputTokens <= 0 {
		config.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if config.InputContextTokens <= 0 {
		if config.Model != "gpt-5.6-sol" {
			return nil, publicConfigError(config, errors.New("engine requires an explicit input context limit"), "engine requires an explicit input context limit")
		}
		config.InputContextTokens = DefaultInputContextTokens
	}
	thresholds, err := compact.ComputeThresholds(compact.Limits{
		ContextWindow: config.InputContextTokens, MaxOutputTokens: config.MaxOutputTokens,
	})
	if err != nil {
		return nil, publicConfigError(config, err, "engine context policy is invalid")
	}
	if config.SettlementTimeout <= 0 {
		config.SettlementTimeout = DefaultSettlementTimeout
	}
	if config.CredentialSanitizer != nil && !config.CredentialSanitizer.Empty() &&
		config.CredentialSanitizer.TerminalMarker() == "" {
		return nil, publicConfigError(config, errors.New("engine credential material has no safe streaming projection"), "engine credential material has no safe streaming projection")
	}
	if err := validateEngineIdentityProjection(config); err != nil {
		return nil, publicConfigError(config, err, "engine identity reflects configured credential material")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Sanitize != nil {
		config.Instructions = applyTextSanitizer(config.Sanitize, config.Instructions)
	}
	if config.CredentialSanitizer != nil {
		config.Instructions = config.CredentialSanitizer.Apply(config.Instructions)
	}
	usage := protocol.Usage{Model: config.Model}
	return &Engine{
		config:     config,
		promptIDs:  make(map[string]struct{}),
		usage:      usage,
		status:     Status{SessionID: config.SessionID, Model: config.Model, ReasoningEffort: config.ReasoningEffort, Usage: usage},
		thresholds: thresholds,
	}, nil
}

func (e *Engine) SessionID() protocol.SessionID { return e.config.SessionID }

func (e *Engine) Status() Status {
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	status := e.status
	status.Usage = cloneUsage(status.Usage)
	return e.publicStatus(status)
}

// HasPromptID reports whether a host idempotency key is already represented by
// an accepted user event. It is a presentation optimization only;
// SubmitPrompt repeats the check under the serialized turn boundary.
func (e *Engine) HasPromptID(promptID string) bool {
	if promptID == "" {
		return false
	}
	e.promptMu.RLock()
	_, exists := e.promptIDs[promptID]
	e.promptMu.RUnlock()
	return exists
}

func (e *Engine) lockTurn() error {
	if e.clockActive.Load() {
		return ErrBusy
	}
	e.turnMu.Lock()
	if e.clockActive.Load() {
		e.turnMu.Unlock()
		return ErrBusy
	}
	return nil
}

func (e *Engine) currentTime() (now time.Time) {
	if !e.clockActive.CompareAndSwap(false, true) {
		return time.Now()
	}
	defer e.clockActive.Store(false)
	return e.invokeClock()
}

// currentTimeOutsideTurnLock is called only by the active turn owner. Claim
// the clock boundary before releasing serialization so a reentrant callback
// receives ErrBusy rather than entering the session, and reacquire ownership
// even when a test callback terminates its goroutine.
func (e *Engine) currentTimeOutsideTurnLock() (now time.Time) {
	if !e.clockActive.CompareAndSwap(false, true) {
		return time.Now()
	}
	e.turnMu.Unlock()
	defer func() {
		e.turnMu.Lock()
		e.clockActive.Store(false)
	}()
	return e.invokeClock()
}

func (e *Engine) invokeClock() (now time.Time) {
	defer func() {
		if recover() != nil || now.IsZero() {
			now = time.Now()
		}
	}()
	return e.config.Now()
}

func (e *Engine) hasActiveTurn() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// ClearContext records a durable projection boundary before dropping provider
// history. The authoritative transcript remains intact.
func (e *Engine) ClearContext(ctx context.Context) error {
	clearedAt := e.currentTime()
	if err := e.lockTurn(); err != nil {
		return err
	}
	defer e.turnMu.Unlock()
	if e.lastEvent == nil && len(e.history) == 0 {
		e.compactor.Reset()
		e.publishStatus()
		return nil
	}
	value, _ := json.Marshal(map[string]any{"cleared": true, "at": clearedAt.UTC()})
	event, err := protocol.NewBaseEvent(e.config.SessionID, "", protocol.EventKindSessionMetadata)
	if err != nil {
		return err
	}
	event.Visibility = protocol.VisibilityInternal
	event.Metadata = &protocol.MetadataEvent{Key: contextClearKey, Value: value}
	if _, err = e.record(ctx, event); err != nil {
		return e.publicError(err)
	}
	e.history = nil
	e.compactor.Reset()
	e.publishStatus()
	return e.publicError(e.flush())
}

// CompactContext deliberately projects older provider context into a bounded,
// deterministic summary while retaining the authoritative transcript. It is
// serialized with model turns and records the exact projected items before
// returning, so resume observes the same context boundary.
func (e *Engine) CompactContext(ctx context.Context) error {
	if err := e.lockTurn(); err != nil {
		return err
	}
	defer e.turnMu.Unlock()
	if len(e.history) <= 1 {
		return errNothingToProject
	}
	tools, err := e.capabilitySchemas()
	if err != nil {
		return e.publicError(err)
	}
	estimated := estimateRequestTokens(e.config.Instructions, e.history, tools)
	if err = e.projectContextWithPolicy(ctx, "", tools, estimated, "manual", 1, 0); err != nil {
		return e.publicError(err)
	}
	return e.publicError(e.flush())
}

func (e *Engine) SetReasoningEffort(ctx context.Context, effort string) error {
	if !validReasoningEffort(effort) {
		return e.publicError(errors.New("unsupported reasoning effort"))
	}
	candidate := e.config
	candidate.ReasoningEffort = effort
	if err := validateEngineIdentityProjection(candidate); err != nil {
		return e.publicError(err)
	}
	if err := e.lockTurn(); err != nil {
		return err
	}
	defer e.turnMu.Unlock()
	if e.lastEvent == nil && len(e.history) == 0 {
		e.config.ReasoningEffort = effort
		e.publishStatus()
		return nil
	}
	value, _ := json.Marshal(effort)
	event, err := protocol.NewBaseEvent(e.config.SessionID, "", protocol.EventKindSessionMetadata)
	if err != nil {
		return err
	}
	event.Visibility = protocol.VisibilityInternal
	event.Metadata = &protocol.MetadataEvent{Key: reasoningEffortKey, Value: value}
	if _, err = e.record(ctx, event); err != nil {
		return e.publicError(err)
	}
	e.config.ReasoningEffort = effort
	e.publishStatus()
	return e.publicError(e.flush())
}

// Restore rebuilds only the provider projection from a defensively recovered
// snapshot. Fully unresolved response groups are omitted, mixed groups receive
// in-memory interrupted results, and no capability runs.
func (e *Engine) Restore(snapshot transcript.Snapshot) error {
	if err := e.lockTurn(); err != nil {
		return err
	}
	defer e.turnMu.Unlock()
	snapshot = snapshot.ActiveConversation().ReconcileUnresolved()
	if snapshot.SessionID != "" && snapshot.SessionID != e.config.SessionID {
		return e.publicError(errors.New("restore session mismatch"))
	}
	e.history = nil
	e.permissionDenials = nil
	e.compactor.Reset()
	e.lastEvent = nil
	e.sequence = snapshot.MaxSequence
	restoredUsage := protocol.Usage{Model: e.config.Model}
	restoredEffort := e.config.ReasoningEffort
	promptIDs := make(map[string]struct{})
	// Provider response metadata is written before capability acceptance. Build
	// the accepted-call index first so replay can preserve provider item order
	// while filtering every raw call that never crossed the durable boundary.
	acceptedCalls := make(map[protocol.ToolUseID]protocol.Event)
	credentialUnsafeCalls := make(map[protocol.ToolUseID]struct{})
	for _, event := range snapshot.Events {
		if event.Kind != protocol.EventKindToolCall || event.ToolCall == nil {
			continue
		}
		if _, err := e.credentialSafeToolArguments(toolCallRawArguments(event.ToolCall)); err != nil {
			credentialUnsafeCalls[event.ToolCall.ID] = struct{}{}
			continue
		}
		if event.Visibility.ModelVisible() && e.validateRestoredToolCallMetadata(event.ToolCall) == nil {
			acceptedCalls[event.ToolCall.ID] = event
		}
	}
	creditedCalls := make(map[protocol.ToolUseID]struct{})
	creditedMessageIDs := make(map[string]int)
	var assistantTextCredits []string
	for _, event := range snapshot.Events {
		if event.Kind == protocol.EventKindMessage && event.Message != nil && event.Message.Role == protocol.RoleUser && event.Message.PromptID != "" {
			promptIDs[event.Message.PromptID] = struct{}{}
		}
		// A small set of internal durable metadata intentionally controls the
		// provider projection. Every semantic message/call/result must carry
		// explicit model visibility; user-only presentation records never enter
		// a resumed model request.
		if event.Kind == protocol.EventKindUsage {
			if event.Usage == nil || event.Usage.Model != e.config.Model {
				return errors.New("restore usage has missing or mismatched model identity")
			}
			nextUsage, err := accumulateProtocolUsage(restoredUsage, *event.Usage)
			if err != nil {
				return fmt.Errorf("restore usage: %w", err)
			}
			restoredUsage = nextUsage
			continue
		}
		switch event.Kind {
		case protocol.EventKindSessionMetadata:
			if event.Metadata.Key == contextClearKey {
				e.history = nil
				creditedCalls = make(map[protocol.ToolUseID]struct{})
				creditedMessageIDs = make(map[string]int)
				assistantTextCredits = nil
				continue
			}
			if event.Metadata.Key == contextProjectionKey {
				var projection contextProjection
				if err := json.Unmarshal(event.Metadata.Value, &projection); err != nil || validateContextProjection(projection, e.thresholds) != nil || e.validateProviderItemsMetadata(projection.Items) != nil {
					continue
				}
				e.history = e.filterCredentialSafeReplayItems(projection.Items, credentialUnsafeCalls)
				creditedCalls = make(map[protocol.ToolUseID]struct{})
				creditedMessageIDs = make(map[string]int)
				assistantTextCredits = nil
				continue
			}
			if event.Metadata.Key == reasoningEffortKey {
				var effort string
				if len(event.Metadata.Value) <= 32 && json.Unmarshal(event.Metadata.Value, &effort) == nil && validReasoningEffort(effort) {
					restoredEffort = effort
				}
				continue
			}
			if event.Metadata.Key != providerOutputKey {
				continue
			}
			var items []model.Item
			if err := json.Unmarshal(event.Metadata.Value, &items); err != nil {
				continue
			}
			for _, item := range items {
				if err := validateModelOutputItem(item); err != nil || e.validateProviderItemMetadata(item) != nil {
					continue
				}
				if item.Type == model.ItemFunctionCall {
					safeArguments, err := e.credentialSafeToolArguments(item.Arguments)
					if err != nil {
						credentialUnsafeCalls[protocol.ToolUseID(item.CallID)] = struct{}{}
						continue
					}
					item.Arguments = safeArguments
				}
				item = e.sanitizeItem(item)
				if item.Type == model.ItemFunctionCall {
					toolID := protocol.ToolUseID(item.CallID)
					accepted, ok := acceptedCalls[toolID]
					acceptedArguments, _ := e.credentialSafeToolArguments(toolCallRawArguments(accepted.ToolCall))
					if !ok || accepted.TurnID != event.TurnID || accepted.ToolCall.Name != item.Name || e.sanitizeText(acceptedArguments) != item.Arguments {
						continue
					}
					creditedCalls[toolID] = struct{}{}
				}
				e.history = append(e.history, item)
				if item.Type == model.ItemMessage && item.Role == model.RoleAssistant {
					if item.ID != "" {
						creditedMessageIDs[item.ID]++
					} else if text := itemText(item); text != "" {
						assistantTextCredits = append(assistantTextCredits, text)
					}
				}
			}
			continue
		}
		if !event.Visibility.ModelVisible() {
			continue
		}
		switch event.Kind {
		case protocol.EventKindMessage:
			text := e.sanitizeText(blockText(event.Message.Content))
			switch event.Message.Role {
			case protocol.RoleUser:
				e.history = append(e.history, model.TextMessage(model.RoleUser, text))
			case protocol.RoleAssistant:
				metadataSafe := e.validateRestoredMessageMetadata(event.Message) == nil
				if metadataSafe && event.Message.APIMessageID != "" && creditedMessageIDs[event.Message.APIMessageID] > 0 {
					creditedMessageIDs[event.Message.APIMessageID]--
					continue
				}
				if consumeTextCredit(&assistantTextCredits, text) {
					continue
				}
				item := model.TextMessage(model.RoleAssistant, text)
				if metadataSafe {
					item.ID = event.Message.APIMessageID
					item.Phase = event.Message.Phase
				}
				e.history = append(e.history, item)
			case protocol.RoleSystem:
				e.history = append(e.history, model.TextMessage(model.RoleSystem, text))
			}
		case protocol.EventKindToolCall:
			if e.validateRestoredToolCallMetadata(event.ToolCall) != nil {
				continue
			}
			safeArguments, err := e.credentialSafeToolArguments(toolCallRawArguments(event.ToolCall))
			if err != nil {
				credentialUnsafeCalls[event.ToolCall.ID] = struct{}{}
				continue
			}
			if _, credited := creditedCalls[event.ToolCall.ID]; credited {
				continue
			}
			e.history = append(e.history, model.FunctionCall("", string(event.ToolCall.ID), event.ToolCall.Name, e.sanitizeText(safeArguments)))
		case protocol.EventKindToolResult:
			if e.validateRestoredToolResultMetadata(event.ToolResult) != nil {
				continue
			}
			if _, unsafe := credentialUnsafeCalls[event.ToolResult.ToolUseID]; unsafe {
				continue
			}
			e.history = append(e.history, model.FunctionCallOutput(string(event.ToolResult.ToolUseID), e.sanitizeText(blockText(event.ToolResult.Content))))
		}
	}
	if snapshot.ResumeCursor != "" {
		cursor := snapshot.ResumeCursor
		e.lastEvent = &cursor
	}
	e.promptMu.Lock()
	e.promptIDs = promptIDs
	e.promptMu.Unlock()
	e.usage = restoredUsage
	e.config.ReasoningEffort = restoredEffort
	e.publishStatus()
	return nil
}

func toolCallRawArguments(call *protocol.ToolCall) string {
	if call == nil {
		return ""
	}
	if call.RawArguments != nil {
		return *call.RawArguments
	}
	return string(call.Arguments)
}

func (e *Engine) Submit(ctx context.Context, text string) (Outcome, error) {
	return e.SubmitPrompt(ctx, text, "")
}

// SubmitPrompt runs one serialized semantic turn and durably binds promptID to
// the accepted user event before any provider or capability side effect. A
// restored or in-process duplicate is rejected without invoking the provider.
func (e *Engine) SubmitPrompt(ctx context.Context, text, promptID string) (Outcome, error) {
	text = e.sanitizeText(text)
	if strings.TrimSpace(text) == "" {
		return Outcome{}, errors.New("empty user input")
	}
	if err := protocol.ValidatePromptID(promptID); err != nil {
		return Outcome{}, err
	}
	if err := e.lockTurn(); err != nil {
		return Outcome{}, err
	}
	defer e.turnMu.Unlock()
	if e.HasPromptID(promptID) {
		return Outcome{SessionID: e.config.SessionID}, e.publicError(ErrDuplicatePrompt)
	}
	e.mu.Lock()
	if e.active {
		e.mu.Unlock()
		return Outcome{}, ErrBusy
	}
	e.active = true
	e.mu.Unlock()
	e.publishStatus()
	defer func() {
		e.mu.Lock()
		e.active = false
		e.mu.Unlock()
		e.publishStatus()
	}()

	turnValue, err := identity.NewTurn()
	if err != nil {
		return Outcome{}, err
	}
	turnID := protocol.TurnID(turnValue)
	started := e.currentTimeOutsideTurnLock()
	outcome := Outcome{SessionID: e.config.SessionID, TurnID: turnID, Status: protocol.TurnResultError}

	userEvent, err := protocol.NewMessageEvent(e.config.SessionID, turnID, protocol.RoleUser, protocol.TextBlock(text))
	if err != nil {
		return outcome, err
	}
	userEvent.Message.PromptID = promptID
	if _, err := e.record(ctx, userEvent); err != nil {
		if isEventDeliveryError(err) {
			e.rememberPromptID(promptID)
			e.history = append(e.history, model.TextMessage(model.RoleUser, text))
			e.publishStatus()
			return e.finish(ctx, outcome, protocol.TurnResultError, "presentation_error", err, started)
		}
		return e.finish(ctx, outcome, protocol.TurnResultError, "transcript_error", fmt.Errorf("persist accepted input: %w", err), started)
	}
	e.rememberPromptID(promptID)
	e.history = append(e.history, model.TextMessage(model.RoleUser, text))
	e.publishStatus()

	for modelTurn := 1; modelTurn <= e.config.MaxTurns; modelTurn++ {
		outcome.ModelTurns = modelTurn
		if err := ctx.Err(); err != nil {
			return e.finish(ctx, outcome, protocol.TurnResultCancelled, "cancelled", err, started)
		}
		schemas, schemaErr := e.capabilitySchemas()
		if schemaErr != nil {
			return e.finish(ctx, outcome, protocol.TurnResultError, "capability_error", schemaErr, started)
		}
		projected, err := e.prepareRequestContext(ctx, turnID, schemas)
		if err != nil {
			stop := recordFailureStop(err)
			if engineErrorIs(err, ErrContextLimit) {
				stop = "context_limit"
			}
			return e.finish(ctx, outcome, protocol.TurnResultError, stop, err, started)
		}
		request := model.Request{Model: e.config.Model, Instructions: e.config.Instructions, Input: projected, Tools: schemas, Reasoning: model.Reasoning{Effort: e.config.ReasoningEffort}, MaxOutputTokens: e.config.MaxOutputTokens, Metadata: map[string]string{"session_id": string(e.config.SessionID), "turn_id": string(turnID)}}
		parallel := true
		request.ParallelToolCalls = &parallel
		apiStarted := e.currentTimeOutsideTurnLock()
		response, _, calls, usage, err := e.runModel(ctx, turnID, request)
		// API duration is the wall-clock time spent opening and consuming model
		// streams, including bounded provider retries. Tool execution, transcript
		// writes, prompt projection, and presentation remain part of Duration but
		// not APIDuration. time.Time subtraction preserves Go's monotonic clock
		// when the production clock supplies it.
		if elapsed := e.currentTimeOutsideTurnLock().Sub(apiStarted); elapsed > 0 {
			outcome.APIDuration += elapsed
		}
		if err != nil {
			status := classifyTurnError(err)
			stop := "provider_error"
			if status == protocol.TurnResultCancelled {
				stop = "cancelled"
			}
			return e.finish(ctx, outcome, status, stop, err, started)
		}
		response.Output, err = e.credentialSafeProviderItems(response.Output)
		if err != nil {
			return e.finish(ctx, outcome, protocol.TurnResultError, "provider_error", err, started)
		}
		calls = functionCalls(response.Output)
		response.Output = e.sanitizeItems(response.Output)
		if err := e.validateProviderOutputComposition(response.Output); err != nil {
			return e.finish(ctx, outcome, protocol.TurnResultError, "provider_error", err, started)
		}
		for index := range calls {
			calls[index].Arguments = e.sanitizeText(calls[index].Arguments)
		}
		if usage != nil {
			if err := e.addUsage(*usage); err != nil {
				return e.finish(ctx, outcome, protocol.TurnResultError, "provider_error", err, started)
			}
			if err := e.recordUsage(ctx, turnID, *usage); err != nil {
				return e.finish(ctx, outcome, protocol.TurnResultError, recordFailureStop(err), err, started)
			}
		}
		if len(response.Output) > 0 {
			if err := e.recordProviderOutput(ctx, turnID, response.Output); err != nil {
				if isEventDeliveryError(err) {
					e.history = append(e.history, nonCallOutput(response.Output)...)
					e.publishStatus()
				}
				return e.finish(ctx, outcome, protocol.TurnResultError, recordFailureStop(err), err, started)
			}
			if err := e.recordAssistantOutput(ctx, turnID, response.Output); err != nil {
				e.history = append(e.history, nonCallOutput(response.Output)...)
				e.publishStatus()
				return e.finish(ctx, outcome, protocol.TurnResultError, recordFailureStop(err), err, started)
			}
		}
		if len(calls) == 0 {
			e.history = append(e.history, response.Output...)
			e.publishStatus()
			outcome.Text = finalAnswerText(response.Output)
			outcome.StopReason = "completed"
			return e.finish(ctx, outcome, protocol.TurnResultSuccess, outcome.StopReason, nil, started)
		}

		accepted, callParents, err := e.acceptCalls(ctx, turnID, calls)
		e.history = append(e.history, acceptedOutput(response.Output, accepted)...)
		e.publishStatus()
		if err != nil {
			settlementErr := e.settleUnexecutedCalls(ctx, turnID, accepted, callParents, "Tool call was accepted but not executed because the complete call batch could not be recorded.")
			return e.finish(ctx, outcome, protocol.TurnResultError, recordFailureStop(err), errors.Join(err, settlementErr), started)
		}
		results := e.executeExactlyOnce(ctx, accepted)
		e.capturePermissionDenials(results)
		if err := e.recordCapabilityResults(ctx, turnID, results, callParents); err != nil {
			return e.finish(ctx, outcome, protocol.TurnResultError, recordFailureStop(err), err, started)
		}
		e.publishStatus()
		if modelTurn == e.config.MaxTurns {
			return e.finish(ctx, outcome, protocol.TurnResultMaxTurns, "max_turns", ErrMaxTurns, started)
		}
	}
	return e.finish(ctx, outcome, protocol.TurnResultMaxTurns, "max_turns", ErrMaxTurns, started)
}

func (e *Engine) rememberPromptID(promptID string) {
	if promptID == "" {
		return
	}
	e.promptMu.Lock()
	e.promptIDs[promptID] = struct{}{}
	e.promptMu.Unlock()
}

func (e *Engine) prepareRequestContext(ctx context.Context, turnID protocol.TurnID, tools []model.Tool) ([]model.Item, error) {
	estimated := estimateRequestTokens(e.config.Instructions, e.history, tools)
	level := e.thresholds.Level(estimated)
	if level == compact.LevelAuto || level == compact.LevelHard {
		if err := e.projectContext(ctx, turnID, tools, estimated); err != nil {
			if level == compact.LevelHard && engineErrorIs(err, errNothingToProject) {
				return nil, fmt.Errorf("%w: the current request cannot be reduced without discarding its only active context", ErrContextLimit)
			}
			if !engineErrorIs(err, errNothingToProject) {
				return nil, err
			}
		}
		estimated = estimateRequestTokens(e.config.Instructions, e.history, tools)
	}
	if estimated >= e.thresholds.Hard {
		return nil, fmt.Errorf("%w: estimated input %d tokens reaches the %d-token hard request ceiling (configured model window %d)", ErrContextLimit, estimated, e.thresholds.Hard, e.config.InputContextTokens)
	}
	return cloneItems(e.history), nil
}

var errNothingToProject = errors.New("no safe context prefix is available for projection")

func (e *Engine) projectContext(ctx context.Context, turnID protocol.TurnID, tools []model.Tool, estimatedBefore int) error {
	return e.projectContextWithPolicy(ctx, turnID, tools, estimatedBefore, "auto", minimumPreservedItems, projectionTargetTokens)
}

func (e *Engine) projectContextWithPolicy(ctx context.Context, turnID protocol.TurnID, tools []model.Tool, estimatedBefore int, trigger string, preserveMinimum, target int) error {
	fixed := estimateRequestTokens(e.config.Instructions, nil, tools)
	if trigger == "auto" && e.thresholds.Warning > 0 && target > e.thresholds.Warning {
		target = e.thresholds.Warning
	}
	if fixed >= e.thresholds.Hard {
		return fmt.Errorf("%w: instructions and capability schemas alone require an estimated %d tokens", ErrContextLimit, fixed)
	}
	if trigger == "auto" && target <= fixed {
		target = fixed + minInt(8_000, e.thresholds.Hard-fixed-1)
	}
	// Leave room for the bounded derived summary itself. The exact estimate is
	// recomputed before the projection is installed or sent.
	summaryAllowance := conservativeTokens(projectionSummaryBytes)
	tailTarget := 0
	if trigger == "auto" {
		tailTarget = target - fixed - summaryAllowance
	}
	if trigger == "auto" && tailTarget < 1_000 {
		tailTarget = 1_000
	}

	var projected []model.Item
	var dropped int
	lastCut := -1
	for attempt := 0; attempt < 4; attempt++ {
		cut := chooseProjectionCutWithMinimum(e.history, tailTarget, preserveMinimum)
		if cut <= 0 || cut == lastCut {
			break
		}
		lastCut = cut
		messages := make([]compact.Message, len(e.history))
		for index, item := range e.history {
			messages[index] = compact.Message{Role: projectionRole(item), Content: projectionContent(item)}
		}
		var summary compact.Summary
		var err error
		if trigger == "manual" {
			summary, err = e.compactor.CompactManual(ctx, projectionSummarizer{maximumBytes: projectionSummaryBytes}, messages, len(messages)-cut)
		} else {
			summary, err = e.compactor.Compact(ctx, projectionSummarizer{maximumBytes: projectionSummaryBytes}, messages, len(messages)-cut)
		}
		if err != nil {
			return err
		}
		summaryItem := model.TextMessage(model.RoleDeveloper, formatProjectionSummary(summary.Text, cut))
		projected = append([]model.Item{summaryItem}, cloneItems(e.history[cut:])...)
		dropped = cut
		after := estimateRequestTokens(e.config.Instructions, projected, tools)
		if after < e.thresholds.Hard {
			break
		}
		projected = nil
		tailTarget /= 2
		if trigger == "auto" && tailTarget < 500 {
			tailTarget = 500
		}
	}
	if len(projected) == 0 || dropped <= 0 {
		return errNothingToProject
	}
	estimatedAfter := estimateRequestTokens(e.config.Instructions, projected, tools)
	if estimatedAfter >= estimatedBefore {
		return fmt.Errorf("%w: projection would not reduce the estimated request size", errNothingToProject)
	}
	if estimatedAfter >= e.thresholds.Hard {
		return fmt.Errorf("%w: compacted projection still estimates %d tokens at a %d-token hard ceiling", ErrContextLimit, estimatedAfter, e.thresholds.Hard)
	}
	record := contextProjection{
		Version: contextProjectionVersion, Trigger: trigger, EstimatedBefore: estimatedBefore,
		EstimatedAfter: estimatedAfter, DroppedItems: dropped, Items: cloneItems(projected),
	}
	if err := validateContextProjection(record, e.thresholds); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode context projection: %w", err)
	}
	if len(data) > maximumProjectionBytes {
		return fmt.Errorf("%w: durable context projection exceeds %d bytes", ErrContextLimit, maximumProjectionBytes)
	}
	event, err := protocol.NewBaseEvent(e.config.SessionID, turnID, protocol.EventKindSessionMetadata)
	if err != nil {
		return err
	}
	event.Visibility = protocol.VisibilityInternal
	event.Origin = protocol.OriginRuntime
	event.Metadata = &protocol.MetadataEvent{Key: contextProjectionKey, Value: data}
	recorded, err := e.record(ctx, event)
	if err != nil {
		if isEventDeliveryError(err) {
			e.history = projected
			e.publishStatus()
		}
		return err
	}
	e.history = projected
	e.publishStatus()
	compaction, createErr := protocol.NewBaseEvent(e.config.SessionID, turnID, protocol.EventKindCompaction)
	if createErr != nil {
		return createErr
	}
	compaction.Persistence = protocol.PersistenceEphemeral
	compaction.Visibility = protocol.VisibilityUser
	compaction.Compaction = &protocol.CompactionEvent{Trigger: trigger, State: "completed", PreTokens: estimatedBefore, SummaryID: &recorded.ID}
	_, err = e.record(ctx, compaction)
	return err
}

type projectionSummarizer struct{ maximumBytes int }

func (s projectionSummarizer) Summarize(_ context.Context, messages []compact.Message) (string, error) {
	if len(messages) == 0 {
		return "", errNothingToProject
	}
	limit := s.maximumBytes
	if limit <= 0 {
		limit = projectionSummaryBytes
	}
	header := "This is a deterministic, lossy projection of earlier conversation context. Verify details against the preserved recent messages and workspace before acting.\n"
	remaining := limit - len(header)
	if remaining <= 0 {
		return "", errors.New("context projection summary budget is too small")
	}

	// Retain the earliest user intent when possible, then spend the remaining
	// budget on the most recent dropped evidence. This is explicitly an excerpt,
	// not a model-authored semantic summary.
	firstUser := -1
	for index, message := range messages {
		if message.Role == string(model.RoleUser) && strings.TrimSpace(message.Content) != "" {
			firstUser = index
			break
		}
	}
	entries := make([]string, 0, len(messages))
	used := 0
	if firstUser >= 0 {
		entry := projectionEntry("original user", messages[firstUser].Content, minInt(4<<10, remaining))
		if entry != "" {
			entries = append(entries, entry)
			used += len(entry)
		}
	}
	var recent []string
	for index := len(messages) - 1; index >= 0 && used < remaining; index-- {
		if index == firstUser {
			continue
		}
		available := remaining - used
		entry := projectionEntry(messages[index].Role, messages[index].Content, minInt(2<<10, available))
		if entry == "" {
			continue
		}
		recent = append(recent, entry)
		used += len(entry)
	}
	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	entries = append(entries, recent...)
	return boundedUTF8(header+strings.Join(entries, "\n"), limit), nil
}

func projectionEntry(role, content string, maximum int) string {
	content = strings.TrimSpace(content)
	if content == "" || maximum <= 0 {
		return ""
	}
	prefix := "[" + role + "] "
	if maximum <= len(prefix) {
		return ""
	}
	return prefix + boundedUTF8(content, maximum-len(prefix))
}

func formatProjectionSummary(summary string, dropped int) string {
	return fmt.Sprintf("<context-projection dropped-items=%q>\n%s\n</context-projection>", fmt.Sprint(dropped), summary)
}

func chooseProjectionCut(history []model.Item, targetTailTokens int) int {
	return chooseProjectionCutWithMinimum(history, targetTailTokens, minimumPreservedItems)
}

func chooseProjectionCutWithMinimum(history []model.Item, targetTailTokens, preserveMinimum int) int {
	if len(history) <= 1 {
		return 0
	}
	minimum := preserveMinimum
	if minimum < 1 {
		minimum = 1
	}
	if minimum > len(history)-1 {
		minimum = len(history) - 1
	}
	running := 0
	cut := 0
	for index := len(history) - 1; index >= 0; index-- {
		itemTokens := estimateItemTokens(history[index])
		preserved := len(history) - index
		if preserved <= minimum {
			running = saturatingAdd(running, itemTokens)
			continue
		}
		if saturatingAdd(running, itemTokens) > targetTailTokens {
			cut = index + 1
			break
		}
		running = saturatingAdd(running, itemTokens)
	}
	if cut <= 0 {
		return 0
	}
	// Terminal output items from one provider response are an API round. Do not
	// split reasoning, commentary, calls, and final text that share its identity.
	if responseID := history[cut].APIResponseID; responseID != "" {
		for cut > 0 && history[cut-1].APIResponseID == responseID {
			cut--
		}
		if cut <= 0 {
			return 0
		}
	}
	// A retained tool output must retain its accepted call. Move the boundary
	// backward until no function-call pair crosses it.
	callIndex := make(map[string]int)
	for index, item := range history {
		if item.Type == model.ItemFunctionCall && item.CallID != "" {
			callIndex[item.CallID] = index
		}
	}
	for {
		adjusted := cut
		for index := cut; index < len(history); index++ {
			item := history[index]
			if item.Type != model.ItemFunctionCallOutput {
				continue
			}
			if paired, exists := callIndex[item.CallID]; exists && paired < adjusted {
				adjusted = paired
			}
		}
		if adjusted < len(history) {
			if responseID := history[adjusted].APIResponseID; responseID != "" {
				for adjusted > 0 && history[adjusted-1].APIResponseID == responseID {
					adjusted--
				}
			}
		}
		if adjusted == cut {
			break
		}
		cut = adjusted
		if cut <= 0 {
			return 0
		}
	}
	return cut
}

func projectionRole(item model.Item) string {
	if item.Type == model.ItemMessage {
		return string(item.Role)
	}
	return string(item.Type)
}

func projectionContent(item model.Item) string {
	switch item.Type {
	case model.ItemMessage:
		return itemText(item)
	case model.ItemFunctionCall:
		return fmt.Sprintf("%s(%s): %s", item.Name, item.CallID, item.Arguments)
	case model.ItemFunctionCallOutput:
		return fmt.Sprintf("result(%s): %s", item.CallID, item.Output)
	case model.ItemReasoning:
		var summaries []string
		for _, part := range item.Summary {
			summaries = append(summaries, part.Text)
		}
		return strings.Join(summaries, " ")
	default:
		return ""
	}
}

func estimateRequestTokens(instructions string, history []model.Item, tools []model.Tool) int {
	tokens := compact.EstimateTokens(instructions)
	for _, item := range history {
		tokens = saturatingAdd(tokens, estimateItemTokens(item))
	}
	for _, tool := range tools {
		tokens = saturatingAdd(tokens, 24)
		tokens = saturatingAdd(tokens, compact.EstimateTokens(tool.Name))
		tokens = saturatingAdd(tokens, compact.EstimateTokens(tool.Description))
		tokens = saturatingAdd(tokens, conservativeTokens(len(tool.Parameters)))
	}
	// MC-003 applies a 4/3 safety multiplier to rough content estimates.
	if tokens > int(^uint(0)>>1)/4 {
		return int(^uint(0) >> 1)
	}
	return (tokens*4 + 2) / 3
}

func estimateItemTokens(item model.Item) int {
	tokens := 24
	for _, value := range []string{string(item.Type), item.ID, string(item.Role), item.Status, item.Phase, item.CallID, item.Name, item.Arguments, item.Output, item.EncryptedContent} {
		tokens = saturatingAdd(tokens, compact.EstimateTokens(value))
	}
	for _, content := range item.Content {
		tokens = saturatingAdd(tokens, compact.EstimateTokens(content.Text))
	}
	for _, content := range item.Summary {
		tokens = saturatingAdd(tokens, compact.EstimateTokens(content.Text))
	}
	return tokens
}

func conservativeTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 2) / 3
}

func saturatingAdd(left, right int) int {
	maximum := int(^uint(0) >> 1)
	if right > maximum-left {
		return maximum
	}
	return left + right
}

func cloneItems(items []model.Item) []model.Item {
	cloned := make([]model.Item, len(items))
	for index, item := range items {
		cloned[index] = item
		cloned[index].Content = append([]model.Content(nil), item.Content...)
		cloned[index].Summary = append([]model.Content(nil), item.Summary...)
	}
	return cloned
}

func (e *Engine) sanitizeText(value string) string {
	if value == "" {
		return value
	}
	if e.config.Sanitize != nil {
		value = applyTextSanitizer(e.config.Sanitize, value)
	}
	if e.config.CredentialSanitizer != nil {
		value = e.config.CredentialSanitizer.Apply(value)
	}
	return value
}

func applyTextSanitizer(sanitize func(string) string, value string) (result string) {
	if sanitize == nil {
		return value
	}
	result = value
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	return sanitize(value)
}

func (e *Engine) publicStatus(status Status) Status {
	status.SessionID = protocol.SessionID(e.sanitizeText(string(status.SessionID)))
	status.Model = e.sanitizeText(status.Model)
	status.ReasoningEffort = e.sanitizeText(status.ReasoningEffort)
	status.Usage.Model = e.sanitizeText(status.Usage.Model)
	credentials := e.config.CredentialSanitizer
	if credentials != nil && credentials.ContainsAcrossPermutations([]string{
		string(status.SessionID), status.Model, status.ReasoningEffort, status.Usage.Model,
	}) {
		status.SessionID = ""
		status.Model = ""
		status.ReasoningEffort = ""
		status.Usage.Model = ""
	}
	return status
}

func (e *Engine) credentialSafeProviderItems(items []model.Item) ([]model.Item, error) {
	result := cloneItems(items)
	for index := range result {
		item := &result[index]
		if item.Type != model.ItemFunctionCall {
			continue
		}
		safeArguments, err := e.credentialSafeToolArguments(item.Arguments)
		if err != nil {
			return nil, err
		}
		item.Arguments = safeArguments
	}
	return result, nil
}

func (e *Engine) credentialSafeToolArguments(raw string) (string, error) {
	if e.config.CredentialSanitizer == nil || e.config.CredentialSanitizer.Empty() {
		return raw, nil
	}
	if !json.Valid([]byte(raw)) {
		// Exact credential safety cannot be proven for a malformed escape
		// sequence. Preserve exact call settlement with a safe, structurally
		// invalid placeholder instead of retaining the provider's raw spelling.
		placeholder, suppressed := e.config.CredentialSanitizer.Redact(`""`)
		if suppressed {
			return "", nil
		}
		return placeholder, nil
	}
	reflected, err := e.config.CredentialSanitizer.JSONContains([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("%w: model tool arguments could not be safely inspected", model.ErrProtocol)
	}
	if reflected {
		return "", fmt.Errorf("%w: model tool arguments reflected configured credential material", model.ErrProtocol)
	}
	return raw, nil
}

func (e *Engine) filterCredentialSafeReplayItems(items []model.Item, unsafeCalls map[protocol.ToolUseID]struct{}) []model.Item {
	result := make([]model.Item, 0, len(items))
	for _, item := range items {
		if item.Type == model.ItemFunctionCall {
			safeArguments, err := e.credentialSafeToolArguments(item.Arguments)
			if err != nil {
				unsafeCalls[protocol.ToolUseID(item.CallID)] = struct{}{}
				continue
			}
			item.Arguments = safeArguments
		}
		if item.Type == model.ItemFunctionCallOutput {
			if _, unsafe := unsafeCalls[protocol.ToolUseID(item.CallID)]; unsafe {
				continue
			}
		}
		result = append(result, e.sanitizeItem(item))
	}
	return result
}

func (e *Engine) sanitizeItem(item model.Item) model.Item {
	item.Content = e.sanitizeContentParts(item.Content)
	item.Summary = e.sanitizeContentParts(item.Summary)
	item.Arguments = e.sanitizeText(item.Arguments)
	item.Output = e.sanitizeText(item.Output)
	// EncryptedContent is opaque replay state. Metadata validation rejects a
	// reflected credential before this point because modifying the blob would
	// corrupt provider replay.
	return item
}

func (e *Engine) sanitizeContentParts(parts []model.Content) []model.Content {
	result := append([]model.Content(nil), parts...)
	if len(result) == 0 {
		return result
	}
	var original, independentlySanitized strings.Builder
	for index := range result {
		original.WriteString(parts[index].Text)
		result[index].Text = e.sanitizeText(parts[index].Text)
		independentlySanitized.WriteString(result[index].Text)
	}
	combined := e.sanitizeText(original.String())
	if combined == independentlySanitized.String() {
		return result
	}
	// A sanitizer match crossed a provider-controlled part boundary. Collapse
	// the semantic text into one part so the exact safe concatenation is retained
	// without inventing a correlation-preserving rewrite of opaque metadata.
	result[0].Text = combined
	return result[:1]
}

func (e *Engine) sanitizeItems(items []model.Item) []model.Item {
	result := make([]model.Item, len(items))
	for index, item := range items {
		result[index] = e.sanitizeItem(item)
	}
	return result
}

// validateProviderOutputComposition closes seams introduced after individual
// item projection. Provider output is later accepted in three complete forms:
// the replayable JSON array, all assistant text joined with newlines, and the
// selected final-answer text joined with newlines. A credential that appears
// only after one of those compositions cannot be removed from the individual
// items without corrupting provider correlation, so reject the whole response.
func (e *Engine) validateProviderOutputComposition(items []model.Item) error {
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("%w: model provider output could not be safely encoded", model.ErrProtocol)
	}
	if e.sanitizeText(string(encoded)) != string(encoded) {
		return fmt.Errorf("%w: model provider output reflected configured credential material", model.ErrProtocol)
	}
	if e.config.CredentialSanitizer != nil && !e.config.CredentialSanitizer.Empty() {
		reflected, inspectErr := e.config.CredentialSanitizer.JSONContains(encoded)
		if inspectErr != nil {
			return fmt.Errorf("%w: model provider output could not be safely inspected", model.ErrProtocol)
		}
		if reflected {
			return fmt.Errorf("%w: model provider output reflected configured credential material", model.ErrProtocol)
		}
	}
	for _, projection := range []struct {
		name string
		text string
	}{
		{name: "assistant text", text: outputText(items)},
		{name: "final answer", text: finalAnswerText(items)},
	} {
		if e.sanitizeText(projection.text) != projection.text {
			return fmt.Errorf("%w: model %s composition reflected configured credential material", model.ErrProtocol, projection.name)
		}
	}
	return nil
}

func validateContextProjection(projection contextProjection, thresholds compact.Thresholds) error {
	if projection.Version != contextProjectionVersion || projection.Trigger != "auto" && projection.Trigger != "manual" || projection.DroppedItems <= 0 || projection.EstimatedBefore <= projection.EstimatedAfter || projection.EstimatedAfter < 0 {
		return errors.New("invalid durable context projection metadata")
	}
	if len(projection.Items) == 0 || projection.EstimatedAfter >= thresholds.Hard {
		return errors.New("durable context projection reaches the hard context ceiling")
	}
	if actual := estimateRequestTokens("", projection.Items, nil); actual > projection.EstimatedAfter || actual >= thresholds.Hard {
		return errors.New("durable context projection token accounting is incoherent")
	}
	request := model.Request{Input: cloneItems(projection.Items)}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("invalid durable context projection: %w", err)
	}
	for _, item := range projection.Items {
		if err := validateProjectedItem(item); err != nil {
			return fmt.Errorf("invalid durable context projection: %w", err)
		}
	}
	return nil
}

func validateProjectedItem(item model.Item) error {
	switch item.Type {
	case model.ItemMessage:
		if item.Role == model.RoleAssistant {
			return validateModelOutputItem(item)
		}
		if item.ID != "" || item.APIResponseID != "" || item.Status != "" || item.Phase != "" || item.CallID != "" || item.Name != "" || item.Arguments != "" || item.Output != "" || item.EncryptedContent != "" || len(item.Summary) != 0 {
			return errors.New("input message has invalid union fields")
		}
	case model.ItemFunctionCall, model.ItemReasoning:
		return validateModelOutputItem(item)
	case model.ItemFunctionCallOutput:
		if item.CallID == "" || item.ID != "" || item.APIResponseID != "" || item.Role != "" || item.Status != "" || item.Phase != "" || len(item.Content) != 0 || item.Name != "" || item.Arguments != "" || item.EncryptedContent != "" || len(item.Summary) != 0 {
			return errors.New("function call output has invalid union fields")
		}
	}
	return nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (e *Engine) runModel(ctx context.Context, turnID protocol.TurnID, request model.Request) (*model.Response, string, []model.Item, *model.Usage, error) {
	if err := e.validateProviderRequestEnvelope(request); err != nil {
		return nil, "", nil, nil, err
	}
	stream, err := openModelStream(ctx, e.config.Provider, request)
	if err != nil {
		return nil, "", nil, nil, e.validateProviderErrorCause(err)
	}
	defer closeModelStream(stream)
	var text strings.Builder
	var completedCalls []model.Item
	var response *model.Response
	var usage *model.Usage
	eventCount := 0
	var textRedactor *redact.SetStream
	if e.config.CredentialSanitizer != nil && !e.config.CredentialSanitizer.Empty() {
		textRedactor = redact.NewSetStream(e.config.CredentialSanitizer)
	}
	textFlushed := false
	appendText := func(raw string) error {
		delta := raw
		if textRedactor != nil {
			delta = textRedactor.Write(delta)
		}
		delta = e.sanitizeText(delta)
		if len(delta) > maximumModelTextBytes-text.Len() {
			return fmt.Errorf("%w: streamed model text exceeded %d bytes", model.ErrProtocol, maximumModelTextBytes)
		}
		text.WriteString(delta)
		if delta != "" {
			if err := e.publishProgress(ctx, turnID, "model_text", delta, ""); err != nil {
				return err
			}
		}
		return nil
	}
	flushText := func() error {
		if textRedactor == nil || textFlushed {
			return nil
		}
		textFlushed = true
		safe := textRedactor.Flush()
		textRedactor = nil
		return appendText(safe)
	}
	for {
		event, nextErr := nextModelStream(stream)
		if inspectEngineError(nextErr).eof {
			break
		}
		if nextErr != nil {
			return response, text.String(), completedCalls, usage, e.validateProviderErrorCause(nextErr)
		}
		if err := e.validateProviderEventMetadata(event); err != nil {
			return response, text.String(), completedCalls, usage, err
		}
		eventCount++
		if eventCount > maximumModelStreamEvents {
			return response, text.String(), completedCalls, usage, fmt.Errorf("%w: model stream exceeded %d events", model.ErrProtocol, maximumModelStreamEvents)
		}
		switch event.Type {
		case model.EventTextDelta:
			if textFlushed {
				return response, text.String(), completedCalls, usage, fmt.Errorf("%w: model text arrived after terminal response", model.ErrProtocol)
			}
			if err := appendText(event.Delta); err != nil {
				return response, text.String(), completedCalls, usage, err
			}
		case model.EventFunctionCallCompleted:
			if event.Call != nil {
				if len(completedCalls) >= maximumModelToolCalls || len(event.Call.Arguments) > maximumModelCallArguments {
					return response, text.String(), completedCalls, usage, fmt.Errorf("%w: model tool-call limit exceeded", model.ErrProtocol)
				}
				completedCalls = append(completedCalls, e.sanitizeItem(*event.Call))
			}
		case model.EventUsage:
			if event.Usage != nil {
				copy := *event.Usage
				usage = &copy
			}
		case model.EventResponseCompleted:
			if event.Response != nil {
				responseCopy := *event.Response
				responseCopy.Output = e.sanitizeItems(event.Response.Output)
				if err := validateModelResponse(&responseCopy); err != nil {
					return nil, text.String(), completedCalls, usage, err
				}
				for index := range responseCopy.Output {
					responseCopy.Output[index].APIResponseID = responseCopy.ID
				}
				if err := e.validateProviderOutputComposition(responseCopy.Output); err != nil {
					return nil, text.String(), completedCalls, usage, err
				}
				if err := flushText(); err != nil {
					return response, text.String(), completedCalls, usage, err
				}
				response = &responseCopy
				usageCopy := response.Usage
				usage = &usageCopy
			}
		case model.EventError:
			if event.Error != nil {
				return response, text.String(), completedCalls, usage, event.Error
			}
			return response, text.String(), completedCalls, usage, model.ErrProtocol
		}
	}
	if response == nil {
		return nil, text.String(), completedCalls, usage, model.ErrIncompleteStream
	}
	if responseText := outputText(response.Output); responseText != "" {
		text.Reset()
		text.WriteString(responseText)
	}
	calls := functionCalls(response.Output)
	if err := validateCompletedCallProjection(completedCalls, calls); err != nil {
		return response, text.String(), calls, usage, err
	}
	return response, text.String(), calls, usage, nil
}

func validateModelResponse(response *model.Response) error {
	if response == nil {
		return fmt.Errorf("%w: terminal response is nil", model.ErrProtocol)
	}
	if strings.TrimSpace(response.ID) == "" || len(response.ID) > 256 {
		return fmt.Errorf("%w: terminal response has an invalid id", model.ErrProtocol)
	}
	if response.Status != "completed" {
		return fmt.Errorf("%w: terminal response has an invalid status", model.ErrProtocol)
	}
	if err := validateModelUsage(response.Usage); err != nil {
		return err
	}
	if len(response.Output) > maximumModelResponseItems {
		return fmt.Errorf("%w: model response exceeded %d output items", model.ErrProtocol, maximumModelResponseItems)
	}
	remaining := maximumModelResponseBytes
	toolCalls := 0
	seenItemIDs := make(map[string]struct{}, len(response.Output))
	seenCallIDs := make(map[string]struct{})
	consume := func(value string) bool {
		if len(value) > remaining {
			return false
		}
		remaining -= len(value)
		return true
	}
	for index, item := range response.Output {
		if err := validateModelOutputItem(item); err != nil {
			return fmt.Errorf("%w: output item %d: %v", model.ErrProtocol, index, err)
		}
		if item.ID != "" {
			if _, exists := seenItemIDs[item.ID]; exists {
				return fmt.Errorf("%w: duplicate model output item id", model.ErrProtocol)
			}
			seenItemIDs[item.ID] = struct{}{}
		}
		if item.Type == model.ItemFunctionCall {
			toolCalls++
			if toolCalls > maximumModelToolCalls || len(item.Arguments) > maximumModelCallArguments {
				return fmt.Errorf("%w: model response tool-call limit exceeded", model.ErrProtocol)
			}
			if _, exists := seenCallIDs[item.CallID]; exists {
				return fmt.Errorf("%w: duplicate model tool call id", model.ErrProtocol)
			}
			seenCallIDs[item.CallID] = struct{}{}
		}
		if !consume(item.ID) || !consume(item.CallID) || !consume(item.Name) || !consume(item.Arguments) || !consume(item.Output) || !consume(item.EncryptedContent) {
			return fmt.Errorf("%w: model response exceeded %d aggregate bytes", model.ErrProtocol, maximumModelResponseBytes)
		}
		for _, content := range item.Content {
			if !consume(content.Text) {
				return fmt.Errorf("%w: model response exceeded %d aggregate bytes", model.ErrProtocol, maximumModelResponseBytes)
			}
		}
		for _, summary := range item.Summary {
			if !consume(summary.Text) {
				return fmt.Errorf("%w: model response exceeded %d aggregate bytes", model.ErrProtocol, maximumModelResponseBytes)
			}
		}
	}
	return nil
}

func validateModelOutputItem(item model.Item) error {
	validStatus := item.Status == "" || item.Status == "completed"
	if !validStatus {
		return errors.New("item has a nonterminal status")
	}
	if len(item.ID) > 256 || len(item.APIResponseID) > 256 || len(item.CallID) > 256 || len(item.Name) > 128 || len(item.Phase) > 64 {
		return errors.New("item identity or name exceeds its wire bound")
	}
	switch item.Type {
	case model.ItemMessage:
		if item.Role != model.RoleAssistant {
			return errors.New("output message has a forbidden role")
		}
		if item.Phase != "" && item.Phase != "commentary" && item.Phase != "final_answer" {
			return errors.New("output message has an unsupported phase")
		}
		if len(item.Content) == 0 || item.CallID != "" || item.Name != "" || item.Arguments != "" || item.Output != "" || item.EncryptedContent != "" || len(item.Summary) != 0 {
			return errors.New("output message has invalid union fields")
		}
		for _, content := range item.Content {
			if content.Type != model.ContentOutputText && content.Type != model.ContentRefusal {
				return errors.New("output message has an unsupported content type")
			}
		}
	case model.ItemFunctionCall:
		if item.CallID == "" || strings.TrimSpace(item.Name) == "" || item.Role != "" || item.Phase != "" || len(item.Content) != 0 || item.Output != "" || item.EncryptedContent != "" || len(item.Summary) != 0 {
			return errors.New("function call has invalid or missing union fields")
		}
	case model.ItemReasoning:
		if item.Role != "" || item.Phase != "" || len(item.Content) != 0 || item.CallID != "" || item.Name != "" || item.Arguments != "" || item.Output != "" {
			return errors.New("reasoning item has invalid union fields")
		}
		if item.ID == "" && item.EncryptedContent == "" && len(item.Summary) == 0 {
			return errors.New("reasoning item has no replayable content")
		}
		for _, summary := range item.Summary {
			if summary.Type != model.ContentSummaryText {
				return errors.New("reasoning item has an unsupported summary type")
			}
		}
	default:
		return errors.New("provider output contains a forbidden item type")
	}
	return nil
}

func validateModelUsage(usage model.Usage) error {
	values := []int64{usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningOutputTokens, usage.TotalTokens}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%w: model usage contains a negative token count", model.ErrProtocol)
		}
	}
	if usage.CachedInputTokens > usage.InputTokens || usage.ReasoningOutputTokens > usage.OutputTokens {
		return fmt.Errorf("%w: model usage detail exceeds its parent token count", model.ErrProtocol)
	}
	if usage.InputTokens > math.MaxInt64-usage.OutputTokens {
		return fmt.Errorf("%w: model input and output token sum overflows", model.ErrProtocol)
	}
	if usage.TotalTokens < usage.InputTokens+usage.OutputTokens {
		return fmt.Errorf("%w: model total token count is incoherent", model.ErrProtocol)
	}
	return nil
}

func validateCompletedCallProjection(streamed, terminal []model.Item) error {
	if len(streamed) == 0 {
		return nil
	}
	if len(streamed) != len(terminal) {
		return fmt.Errorf("%w: streamed and terminal tool-call sets differ", model.ErrProtocol)
	}
	byID := make(map[string]model.Item, len(terminal))
	for _, call := range terminal {
		byID[call.CallID] = call
	}
	for _, call := range streamed {
		terminalCall, ok := byID[call.CallID]
		if !ok || terminalCall.Name != call.Name || terminalCall.Arguments != call.Arguments {
			return fmt.Errorf("%w: streamed and terminal tool calls differ", model.ErrProtocol)
		}
	}
	return nil
}

func (e *Engine) acceptCalls(ctx context.Context, turnID protocol.TurnID, items []model.Item) ([]CapabilityCall, map[protocol.ToolUseID]protocol.EventID, error) {
	accepted := make([]CapabilityCall, 0, len(items))
	parents := make(map[protocol.ToolUseID]protocol.EventID, len(items))
	seen := make(map[protocol.ToolUseID]struct{})
	type prepared struct {
		call  CapabilityCall
		event protocol.Event
	}
	ready := make([]prepared, 0, len(items))
	for _, item := range items {
		if err := e.validateProviderItemMetadata(item); err != nil {
			return nil, nil, err
		}
		item = e.sanitizeItem(item)
		id := protocol.ToolUseID(item.CallID)
		if id == "" {
			generated, err := protocol.NewToolUseID()
			if err != nil {
				return nil, nil, err
			}
			id = generated
		}
		if _, exists := seen[id]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate model tool call id", model.ErrProtocol)
		}
		seen[id] = struct{}{}
		arguments := json.RawMessage(item.Arguments)
		rawCall := protocol.NewRawToolCall(id, item.Name, item.Arguments)
		rawCall.APIResponseID = item.APIResponseID
		callEvent, err := protocol.NewToolCallEvent(e.config.SessionID, turnID, rawCall)
		if err != nil {
			return nil, nil, fmt.Errorf("accept model tool call: %w", err)
		}
		ready = append(ready, prepared{call: CapabilityCall{ID: id, ProviderItemID: item.ID, Name: item.Name, Arguments: arguments}, event: callEvent})
	}
	var recordErrors []error
	for _, item := range ready {
		callEvent := item.event
		recorded, err := e.recordAcceptedEvent(ctx, callEvent)
		if err == nil || isEventDeliveryError(err) || isEventPersistenceUncertain(err) {
			// The model-issued identity is stable before persistence starts. Keep
			// an ambiguously committed call in the settlement ledger so it is never
			// executed but still receives an attempted terminal interrupted result.
			if recorded.ID == "" {
				recorded = callEvent
			}
			parents[item.call.ID] = recorded.ID
			accepted = append(accepted, item.call)
		}
		if err != nil {
			if !isEventDeliveryError(err) {
				recordErrors = append(recordErrors, err)
				break
			}
			recordErrors = append(recordErrors, err)
		}
	}
	return accepted, parents, errors.Join(recordErrors...)
}

// recordAcceptedEvent retries the same stable event identity once when the
// persistence outcome is uncertain. Transcript stores are idempotent by event
// ID, so this cannot accept a capability twice. Presentation failures are not
// retried because the durable acceptance already succeeded.
func (e *Engine) recordAcceptedEvent(ctx context.Context, event protocol.Event) (protocol.Event, error) {
	recorded, err := e.record(ctx, event)
	if err == nil || isEventDeliveryError(err) {
		return recorded, err
	}
	retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	retried, retryErr := e.record(retryCtx, event)
	if retryErr == nil {
		return retried, nil
	}
	if retried.ID == "" {
		retried = recorded
	}
	if retried.ID == "" {
		retried = event
	}
	return retried, errors.Join(err, retryErr)
}

func (e *Engine) executeExactlyOnce(ctx context.Context, calls []CapabilityCall) []CapabilityResult {
	accepted := make(map[protocol.ToolUseID]CapabilityCall, len(calls))
	for _, call := range calls {
		cloned := call
		cloned.Arguments = append(json.RawMessage(nil), call.Arguments...)
		accepted[call.ID] = cloned
	}
	returned := e.executeCapabilities(ctx, calls)
	settled := make(map[protocol.ToolUseID]struct{}, len(calls))
	results := make([]CapabilityResult, 0, len(calls))
	for _, result := range returned {
		call, ok := accepted[result.ID]
		if !ok {
			continue
		}
		if _, duplicate := settled[result.ID]; duplicate {
			continue
		}
		results = append(results, normalizeCapabilityResult(call, result))
		settled[result.ID] = struct{}{}
	}
	for _, call := range calls {
		if _, ok := settled[call.ID]; ok {
			continue
		}
		results = append(results, normalizeCapabilityResult(call, CapabilityResult{
			Status:    protocol.ToolResultInterrupted,
			Content:   "Tool execution did not produce a terminal result; it was treated as interrupted.",
			Code:      "missing_terminal_result",
			IsError:   true,
			Synthetic: true,
		}))
	}
	return results
}

func normalizeCapabilityResult(call CapabilityCall, result CapabilityResult) CapabilityResult {
	// Correlation identity and capability name are owned by the accepted call
	// ledger, never by an untrusted executor result.
	result.ID = call.ID
	result.Name = call.Name
	if result.IsError {
		result.Code = canonicalCapabilityErrorCode(result.Code)
	} else {
		result.Code = ""
	}
	if result.ContentSuppressed {
		// Suppression is a fail-closed terminal state, not merely a hint that
		// synthetic empty-output prose should be skipped. Discard any content a
		// buggy or stale capability adapter returned alongside the marker.
		result.Content = ""
	}
	if result.PermissionDenial != nil {
		denial := *result.PermissionDenial
		denial.ToolName = call.Name
		denial.ToolUseID = call.ID
		denial.ToolInput = append(json.RawMessage(nil), denial.ToolInput...)
		result.PermissionDenial = &denial
	}
	if result.Content == "" && !result.ContentSuppressed {
		if result.IsError {
			result.Content = "Tool execution failed without output."
		} else {
			result.Content = "(" + call.Name + " completed with no output)"
		}
	}
	if result.Status == "" {
		if result.IsError {
			result.Status = protocol.ToolResultError
		} else {
			result.Status = protocol.ToolResultSuccess
		}
	}
	return result
}

func canonicalCapabilityErrorCode(code string) string {
	switch code {
	case "call_batch_interrupted", "cancelled", "denied", "execution_failed",
		"hook_failed", "interrupted", "malformed_input", "malformed_result",
		"missing_terminal_result", "permission_denied", "permission_failed",
		"semantic_invalid", "sibling_error", "stale_file", "structural_invalid",
		"timeout", "unavailable", "unknown_tool", "user_interrupted":
		return code
	default:
		return "tool_error"
	}
}

func (e *Engine) capturePermissionDenials(results []CapabilityResult) {
	for _, result := range results {
		if result.PermissionDenial == nil || !result.IsError || (result.Status != protocol.ToolResultDenied && result.Status != protocol.ToolResultCancelled) {
			continue
		}
		denial := *result.PermissionDenial
		denial.ToolName = result.Name
		denial.ToolUseID = result.ID
		denial.ToolInput = json.RawMessage(e.sanitizeText(string(denial.ToolInput)))
		if e.config.CredentialSanitizer != nil && !e.config.CredentialSanitizer.Empty() {
			encoded, err := json.Marshal(denial)
			reflected, inspectErr := e.config.CredentialSanitizer.JSONContains(encoded)
			if err != nil || inspectErr != nil || reflected {
				denial.ToolInput = json.RawMessage(`{}`)
				encoded, err = json.Marshal(denial)
				reflected, inspectErr = e.config.CredentialSanitizer.JSONContains(encoded)
				if err != nil || inspectErr != nil || reflected {
					continue
				}
			}
		}
		e.permissionDenials = append(e.permissionDenials, denial)
	}
}

func (e *Engine) recordToolResult(ctx context.Context, turnID protocol.TurnID, result CapabilityResult, parent protocol.EventID) (string, bool, error) {
	info := (*protocol.ErrorInfo)(nil)
	if result.IsError {
		code := canonicalCapabilityErrorCode(result.Code)
		info = &protocol.ErrorInfo{Code: code, Message: boundedUTF8(result.Content, 16*1024)}
	}
	event, err := protocol.NewToolResultEvent(e.config.SessionID, turnID, protocol.ToolResult{ToolUseID: result.ID, ToolName: result.Name, Status: result.Status, Content: []protocol.ContentBlock{protocol.TextBlock(result.Content)}, IsError: result.IsError, DurationMillis: result.Duration.Milliseconds(), Error: info, Synthetic: result.Synthetic})
	if err != nil {
		return "", false, err
	}
	event.ParentID = &parent
	if e.config.CredentialSanitizer != nil && !e.config.CredentialSanitizer.Empty() {
		encoded, encodeErr := json.Marshal(event)
		reflected, inspectErr := e.config.CredentialSanitizer.JSONContains(encoded)
		if encodeErr != nil || inspectErr != nil || reflected {
			event.ToolResult.Content = []protocol.ContentBlock{protocol.TextBlock("")}
			if event.ToolResult.Error != nil {
				event.ToolResult.Error.Message = ""
			}
			encoded, encodeErr = json.Marshal(event)
			reflected, inspectErr = e.config.CredentialSanitizer.JSONContains(encoded)
			if encodeErr != nil || inspectErr != nil || reflected {
				return "", false, errors.New("tool result could not be safely encoded")
			}
		}
	}
	recordedContent := event.ToolResult.Content[0].Text
	recorded, err := e.record(ctx, event)
	if err == nil {
		return recordedContent, true, nil
	}
	if isEventDeliveryError(err) {
		return recordedContent, true, err
	}
	// Retry the same event ID once. If the first append reached durable storage
	// but its acknowledgement failed, transcript idempotency suppresses a
	// duplicate; if it did not, this closes a transient exact-one gap.
	recorded, retryErr := e.record(ctx, event)
	_ = recorded
	if retryErr == nil {
		return recordedContent, true, nil
	}
	if isEventDeliveryError(retryErr) {
		return recordedContent, true, errors.Join(err, retryErr)
	}
	return "", false, errors.Join(err, retryErr)
}

func (e *Engine) recordCapabilityResults(ctx context.Context, turnID protocol.TurnID, results []CapabilityResult, parents map[protocol.ToolUseID]protocol.EventID) error {
	var recordErrors []error
	batchCtx, cancelBatch := context.WithTimeout(context.WithoutCancel(ctx), e.config.SettlementTimeout)
	defer cancelBatch()
	batchDeadline, _ := batchCtx.Deadline()
	for index, result := range results {
		result.Content = e.sanitizeText(result.Content)
		parent, ok := parents[result.ID]
		if !ok || parent == "" {
			recordErrors = append(recordErrors, fmt.Errorf("accepted tool %s has no durable call parent", result.ID))
			continue
		}
		// Keep transcript and sink writes serialized, but reserve an equal share
		// of the remaining batch budget for every result still to be attempted.
		remainingResults := len(results) - index
		remainingBudget := time.Until(batchDeadline)
		if remainingBudget < 0 {
			remainingBudget = 0
		}
		recordCtx, cancel := context.WithTimeout(batchCtx, remainingBudget/time.Duration(remainingResults))
		recordedContent, committed, err := e.recordToolResult(recordCtx, turnID, result, parent)
		cancel()
		if committed {
			e.history = append(e.history, model.FunctionCallOutput(string(result.ID), recordedContent))
		}
		if err != nil {
			recordErrors = append(recordErrors, fmt.Errorf("record terminal result for %s: %w", result.ID, err))
		}
	}
	e.publishStatus()
	return errors.Join(recordErrors...)
}

func (e *Engine) settleUnexecutedCalls(ctx context.Context, turnID protocol.TurnID, calls []CapabilityCall, parents map[protocol.ToolUseID]protocol.EventID, reason string) error {
	results := make([]CapabilityResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, CapabilityResult{
			ID: call.ID, Name: call.Name, Status: protocol.ToolResultInterrupted,
			Content: e.sanitizeText(reason), Code: "call_batch_interrupted", IsError: true, Synthetic: true,
		})
	}
	return e.recordCapabilityResults(ctx, turnID, results, parents)
}

func (e *Engine) recordProviderOutput(ctx context.Context, turnID protocol.TurnID, items []model.Item) error {
	if err := e.validateProviderItemsMetadata(items); err != nil {
		return err
	}
	items = e.sanitizeItems(items)
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	event, err := protocol.NewBaseEvent(e.config.SessionID, turnID, protocol.EventKindSessionMetadata)
	if err != nil {
		return err
	}
	event.Visibility = protocol.VisibilityInternal
	event.Origin = protocol.OriginModel
	event.Metadata = &protocol.MetadataEvent{Key: providerOutputKey, Value: data}
	_, err = e.record(ctx, event)
	return err
}

func (e *Engine) recordAssistantOutput(ctx context.Context, turnID protocol.TurnID, items []model.Item) error {
	for _, item := range items {
		if err := e.validateProviderItemMetadata(item); err != nil {
			return err
		}
		item = e.sanitizeItem(item)
		if item.Type != model.ItemMessage || item.Role != model.RoleAssistant {
			continue
		}
		event, err := protocol.NewMessageEvent(e.config.SessionID, turnID, protocol.RoleAssistant, protocol.TextBlock(itemText(item)))
		if err != nil {
			return err
		}
		event.Message.APIMessageID = item.ID
		event.Message.APIResponseID = item.APIResponseID
		event.Message.Phase = item.Phase
		if _, err := e.record(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) recordUsage(ctx context.Context, turnID protocol.TurnID, usage model.Usage) error {
	event, err := protocol.NewBaseEvent(e.config.SessionID, turnID, protocol.EventKindUsage)
	if err != nil {
		return err
	}
	event.Visibility = protocol.VisibilityUser
	event.Origin = protocol.OriginModel
	event.Usage = &protocol.Usage{Model: e.config.Model, InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens, OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningOutputTokens, TotalTokens: usage.TotalTokens}
	_, err = e.record(ctx, event)
	return err
}

func (e *Engine) addUsage(usage model.Usage) error {
	incoming := protocol.Usage{
		Model:             e.config.Model,
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   usage.ReasoningOutputTokens,
		TotalTokens:       usage.TotalTokens,
	}
	next, err := accumulateProtocolUsage(e.usage, incoming)
	if err != nil {
		return fmt.Errorf("%w: cumulative model usage is invalid: %v", model.ErrProtocol, err)
	}
	e.usage = next
	e.publishStatus()
	return nil
}

func accumulateProtocolUsage(current, incoming protocol.Usage) (protocol.Usage, error) {
	if err := incoming.Validate(); err != nil {
		return current, err
	}
	if current.Model == "" || current.Model != incoming.Model {
		return current, errors.New("usage model identity changed")
	}
	add := func(name string, left, right int64) (int64, error) {
		if left < 0 || right < 0 || left > math.MaxInt64-right {
			return 0, fmt.Errorf("%s overflows", name)
		}
		return left + right, nil
	}
	next := current
	var err error
	if next.InputTokens, err = add("input tokens", current.InputTokens, incoming.InputTokens); err != nil {
		return current, err
	}
	if next.CachedInputTokens, err = add("cached input tokens", current.CachedInputTokens, incoming.CachedInputTokens); err != nil {
		return current, err
	}
	if next.OutputTokens, err = add("output tokens", current.OutputTokens, incoming.OutputTokens); err != nil {
		return current, err
	}
	if next.ReasoningTokens, err = add("reasoning tokens", current.ReasoningTokens, incoming.ReasoningTokens); err != nil {
		return current, err
	}
	if next.TotalTokens, err = add("total tokens", current.TotalTokens, incoming.TotalTokens); err != nil {
		return current, err
	}
	return next, nil
}

func validReasoningEffort(effort string) bool {
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (e *Engine) finish(ctx context.Context, outcome Outcome, status protocol.TurnResultStatus, stop string, cause error, started time.Time) (Outcome, error) {
	outcome.Status = status
	outcome.StopReason = stop
	var finishedAt time.Time
	if e.hasActiveTurn() {
		finishedAt = e.currentTimeOutsideTurnLock()
	} else {
		finishedAt = e.currentTime()
	}
	outcome.Duration = finishedAt.Sub(started)
	outcome.Usage = cloneUsage(e.usage)
	outcome.PermissionDenials = clonePermissionDenials(e.permissionDenials)
	message := ""
	causeMessage := ""
	if cause != nil {
		// Redact before bounding. Truncating first could retain a credential
		// prefix while discarding the bytes the literal sanitizer needs to
		// recognize and replace the complete value.
		causeMessage = safeEngineErrorString(cause)
		message = boundedUTF8(e.normalizedPublicErrorMessage(causeMessage), 2000)
	}
	event, err := protocol.NewBaseEvent(e.config.SessionID, outcome.TurnID, protocol.EventKindTurnResult)
	if err == nil {
		event.Visibility = protocol.VisibilityUser
		event.Origin = protocol.OriginRuntime
		event.TurnResult = &protocol.TurnResult{Status: status, IsError: status != protocol.TurnResultSuccess, StopReason: stop, Message: message, Turns: outcome.ModelTurns, DurationMillis: outcome.Duration.Milliseconds()}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, err = e.record(cleanupCtx, event)
		cancel()
	}
	err = errors.Join(err, e.flush())
	if err != nil {
		finalizationMessage := safeEngineErrorString(err)
		combinedMessage := causeMessage
		if combinedMessage != "" && finalizationMessage != "" {
			combinedMessage += "\n" + finalizationMessage
		} else if combinedMessage == "" {
			combinedMessage = finalizationMessage
		}
		return outcome, e.publicErrorWithMessage(errors.Join(cause, err), combinedMessage)
	}
	return outcome, e.publicErrorWithMessage(cause, causeMessage)
}

func clonePermissionDenials(source []PermissionDenial) []PermissionDenial {
	result := make([]PermissionDenial, len(source))
	for index, denial := range source {
		result[index] = denial
		result[index].ToolInput = append(json.RawMessage(nil), denial.ToolInput...)
	}
	return result
}

// publishStatus copies presentation-facing state while the caller owns the
// serialized turn mutation boundary. Status readers never wait for turnMu and
// never observe the mutable history slice itself.
func (e *Engine) publishStatus() {
	e.mu.Lock()
	active := e.active
	e.mu.Unlock()
	snapshot := Status{
		SessionID:       e.config.SessionID,
		Model:           e.config.Model,
		ReasoningEffort: e.config.ReasoningEffort,
		Active:          active,
		ProjectedItems:  len(e.history),
		Usage:           cloneUsage(e.usage),
	}
	e.statusMu.Lock()
	e.status = snapshot
	e.statusMu.Unlock()
}

func cloneUsage(usage protocol.Usage) protocol.Usage {
	if usage.CostUSD != nil {
		value := *usage.CostUSD
		usage.CostUSD = &value
	}
	return usage
}

func (e *Engine) flush() error {
	if e.config.Transcript == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return flushTranscriptStore(ctx, e.config.Transcript)
}

func (e *Engine) record(ctx context.Context, event protocol.Event) (protocol.Event, error) {
	if event.ParentID == nil && e.lastEvent != nil {
		parent := *e.lastEvent
		event.ParentID = &parent
	}
	if e.config.Transcript != nil {
		normalized, written, err := appendTranscriptEvent(ctx, e.config.Transcript, event)
		if normalized.ID != "" {
			event = normalized
		}
		if err != nil {
			if written {
				return event, &eventPersistenceError{inspection: inspectEngineErrorWithContext(err, ctx.Err())}
			}
			return event, err
		}
		if written {
			event = normalized
		}
	} else {
		e.sequence++
		event.Sequence = e.sequence
	}
	if event.Persistence == protocol.PersistenceDurable {
		id := event.ID
		e.lastEvent = &id
	}
	if e.config.Sink != nil {
		if err := publishSinkEvent(ctx, e.config.Sink, event); err != nil {
			return event, &eventDeliveryError{inspection: inspectEngineErrorWithContext(err, ctx.Err())}
		}
	}
	return event, nil
}

func recordFailureStop(err error) string {
	if isEventDeliveryError(err) {
		return "presentation_error"
	}
	return "transcript_error"
}

func (e *Engine) publishProgress(ctx context.Context, turnID protocol.TurnID, phase, message string, toolID protocol.ToolUseID) error {
	event, err := protocol.NewBaseEvent(e.config.SessionID, turnID, protocol.EventKindProgress)
	if err != nil {
		return err
	}
	event.Persistence = protocol.PersistenceEphemeral
	event.Visibility = protocol.VisibilityUser
	event.Progress = &protocol.ProgressEvent{Phase: phase, Message: e.sanitizeText(message), ToolUseID: toolID}
	_, err = e.record(ctx, event)
	return err
}

func classifyTurnError(err error) protocol.TurnResultStatus {
	inspection := inspectEngineError(err)
	if inspection.cancelled || inspection.deadline {
		return protocol.TurnResultCancelled
	}
	return protocol.TurnResultError
}
func boundedUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func functionCalls(items []model.Item) []model.Item {
	var result []model.Item
	for _, item := range items {
		if item.Type == model.ItemFunctionCall {
			result = append(result, item)
		}
	}
	return result
}
func outputText(items []model.Item) string {
	var parts []string
	for _, item := range items {
		if item.Type == model.ItemMessage && item.Role == model.RoleAssistant {
			if text := itemText(item); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func finalAnswerText(items []model.Item) string {
	var explicit, unphased []string
	for _, item := range items {
		if item.Type != model.ItemMessage || item.Role != model.RoleAssistant {
			continue
		}
		text := itemText(item)
		if text == "" {
			continue
		}
		switch item.Phase {
		case "final_answer":
			explicit = append(explicit, text)
		case "":
			unphased = append(unphased, text)
		}
	}
	if len(explicit) > 0 {
		return strings.Join(explicit, "\n")
	}
	return strings.Join(unphased, "\n")
}

func nonCallOutput(items []model.Item) []model.Item {
	result := make([]model.Item, 0, len(items))
	for _, item := range items {
		if item.Type != model.ItemFunctionCall {
			result = append(result, item)
		}
	}
	return result
}

func acceptedOutput(items []model.Item, accepted []CapabilityCall) []model.Item {
	acceptedByID := make(map[protocol.ToolUseID]CapabilityCall, len(accepted))
	for _, call := range accepted {
		acceptedByID[call.ID] = call
	}
	result := make([]model.Item, 0, len(items))
	for _, item := range items {
		if item.Type != model.ItemFunctionCall {
			result = append(result, item)
			continue
		}
		if call, ok := acceptedByID[protocol.ToolUseID(item.CallID)]; ok {
			item.CallID = string(call.ID)
			result = append(result, item)
		}
	}
	return result
}
func itemText(item model.Item) string {
	var b strings.Builder
	for _, content := range item.Content {
		if content.Type == model.ContentOutputText || content.Type == model.ContentInputText || content.Type == model.ContentRefusal {
			b.WriteString(content.Text)
		}
	}
	return b.String()
}
func blockText(blocks []protocol.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == protocol.ContentText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
func consumeTextCredit(credits *[]string, text string) bool {
	for i, credit := range *credits {
		if credit == text {
			*credits = append((*credits)[:i], (*credits)[i+1:]...)
			return true
		}
	}
	// Version-1 writers projected all assistant items into one concatenated
	// semantic message. Consume a matching prefix so those transcripts do not
	// replay A, B, and a duplicate AB after an upgrade.
	var joined strings.Builder
	for i, credit := range *credits {
		joined.WriteString(credit)
		if joined.String() == text {
			*credits = append([]string(nil), (*credits)[i+1:]...)
			return true
		}
		if joined.Len() >= len(text) {
			break
		}
	}
	return false
}
