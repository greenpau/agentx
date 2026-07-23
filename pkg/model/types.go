// Package model defines the provider-neutral model transport boundary and the
// Azure OpenAI Responses API adapter.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Provider starts one model response stream. A Stream is bound to ctx for its
// entire lifetime; callers must close it when they stop receiving events.
type Provider interface {
	Stream(ctx context.Context, request Request) (Stream, error)
}

// Stream yields provider-neutral semantic events in wire order. Next is a
// single-consumer operation and must not be called concurrently. Close is safe
// to call concurrently and is idempotent.
type Stream interface {
	Next() (Event, error)
	Close() error
}

var (
	// ErrClosed reports that a response stream was explicitly closed.
	ErrClosed = errors.New("model stream closed")
	// ErrIncompleteStream reports EOF or [DONE] before a successful or failed
	// terminal Responses API event.
	ErrIncompleteStream = errors.New("model stream ended without a terminal response event")
	// ErrStreamWatchdog reports that no stream activity arrived before the
	// configured idle deadline.
	ErrStreamWatchdog = errors.New("model stream idle watchdog expired")
	// ErrRequestTimeout is an adapter-owned per-attempt deadline. It is distinct
	// from caller cancellation so the session cannot misreport provider latency
	// as a user cancellation.
	ErrRequestTimeout = errors.New("model provider request timed out")
	// ErrProtocol reports malformed or contradictory provider wire data.
	ErrProtocol = errors.New("invalid model protocol")
)

// Role is a message author's Responses API role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ItemType identifies a provider-neutral conversation item.
type ItemType string

const (
	ItemMessage            ItemType = "message"
	ItemFunctionCall       ItemType = "function_call"
	ItemFunctionCallOutput ItemType = "function_call_output"
	ItemReasoning          ItemType = "reasoning"
)

// ContentType identifies one message or reasoning content part.
type ContentType string

const (
	ContentInputText   ContentType = "input_text"
	ContentOutputText  ContentType = "output_text"
	ContentRefusal     ContentType = "refusal"
	ContentSummaryText ContentType = "summary_text"
)

// Content is one text-like item. The model layer deliberately does not infer
// media filesystem access; future media adapters should add explicit bounded
// content fields rather than smuggling paths through Text.
type Content struct {
	Type ContentType `json:"type"`
	Text string      `json:"text"`
}

// Item is a semantic Responses API input or output item. Only fields relevant
// to Type are used. Arguments remain a string because model-produced function
// arguments are untrusted and must be validated by the capability runtime.
type Item struct {
	Type ItemType `json:"type"`
	ID   string   `json:"id,omitempty"`
	// APIResponseID is local projection provenance used to keep streamed
	// response siblings together during context reduction. Provider adapters
	// deliberately omit it from the wire request.
	APIResponseID    string    `json:"api_response_id,omitempty"`
	Role             Role      `json:"role,omitempty"`
	Status           string    `json:"status,omitempty"`
	Phase            string    `json:"phase,omitempty"`
	Content          []Content `json:"content,omitempty"`
	CallID           string    `json:"call_id,omitempty"`
	Name             string    `json:"name,omitempty"`
	Arguments        string    `json:"arguments,omitempty"`
	Output           string    `json:"output,omitempty"`
	EncryptedContent string    `json:"encrypted_content,omitempty"`
	Summary          []Content `json:"summary,omitempty"`
}

// TextMessage constructs one provider-neutral text message. Assistant text is
// represented as output_text so manually replayed Responses items retain their
// original role semantics; all other roles use input_text.
func TextMessage(role Role, text string) Item {
	contentType := ContentInputText
	if role == RoleAssistant {
		contentType = ContentOutputText
	}
	return Item{
		Type:    ItemMessage,
		Role:    role,
		Content: []Content{{Type: contentType, Text: text}},
	}
}

// FunctionCall creates a replayable assistant function-call item.
func FunctionCall(id, callID, name, arguments string) Item {
	return Item{Type: ItemFunctionCall, ID: id, CallID: callID, Name: name, Arguments: arguments}
}

// FunctionCallOutput creates a tool-result input correlated to callID.
func FunctionCallOutput(callID, output string) Item {
	return Item{Type: ItemFunctionCallOutput, CallID: callID, Output: output}
}

// Tool is one model-callable function schema. Parameters must encode a JSON
// object schema. The capability runtime remains responsible for semantic input
// validation and authorization after the model selects a tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

// Reasoning configures a model's reasoning policy. Empty Effort delegates to
// the configured provider default.
type Reasoning struct {
	Effort string `json:"effort,omitempty"`
}

// Request is the provider-neutral projection for one Responses API call.
// Model is the logical model identity used by the engine; a cloud adapter may
// route it through a configured deployment name on the wire.
type Request struct {
	Model              string            `json:"model,omitempty"`
	Instructions       string            `json:"instructions,omitempty"`
	Input              []Item            `json:"input"`
	Tools              []Tool            `json:"tools,omitempty"`
	Reasoning          Reasoning         `json:"reasoning,omitempty"`
	MaxOutputTokens    int               `json:"max_output_tokens,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// Usage is cumulative for one accepted provider response.
type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens,omitempty"`
}

// Response is the provider-neutral terminal Responses API object. Output
// preserves reasoning and function-call items needed for stateless replay.
type Response struct {
	ID                 string `json:"id"`
	Model              string `json:"model,omitempty"`
	Status             string `json:"status,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	Output             []Item `json:"output,omitempty"`
	Usage              Usage  `json:"usage"`
}

// EventType identifies one normalized stream event.
type EventType string

const (
	EventResponseCreated            EventType = "response_created"
	EventResponseInProgress         EventType = "response_in_progress"
	EventTextDelta                  EventType = "text_delta"
	EventReasoningDelta             EventType = "reasoning_delta"
	EventFunctionCallArgumentsDelta EventType = "function_call_arguments_delta"
	EventFunctionCallCompleted      EventType = "function_call_completed"
	EventUsage                      EventType = "usage"
	EventResponseCompleted          EventType = "response_completed"
	EventError                      EventType = "error"
)

// ReasoningDeltaKind distinguishes hidden reasoning content from a model-safe
// reasoning summary when a provider supplies either form.
type ReasoningDeltaKind string

const (
	ReasoningContent ReasoningDeltaKind = "content"
	ReasoningSummary ReasoningDeltaKind = "summary"
)

// Event is a canonical stream event. SequenceNumber and item indexes preserve
// provider ordering and correlation without exposing the raw wire envelope.
type Event struct {
	Type           EventType          `json:"type"`
	RawType        string             `json:"raw_type,omitempty"`
	SequenceNumber int64              `json:"sequence_number,omitempty"`
	RequestID      string             `json:"request_id,omitempty"`
	ResponseID     string             `json:"response_id,omitempty"`
	ItemID         string             `json:"item_id,omitempty"`
	OutputIndex    int                `json:"output_index,omitempty"`
	ContentIndex   int                `json:"content_index,omitempty"`
	Delta          string             `json:"delta,omitempty"`
	ReasoningKind  ReasoningDeltaKind `json:"reasoning_kind,omitempty"`
	Call           *Item              `json:"call,omitempty"`
	Usage          *Usage             `json:"usage,omitempty"`
	Response       *Response          `json:"response,omitempty"`
	Error          *ProviderError     `json:"error,omitempty"`
}

// ProviderError is a safe, structured provider failure. Error intentionally
// omits response bodies, request inputs, headers, and credentials.
type ProviderError struct {
	StatusCode int    `json:"status_code,omitempty"`
	Code       string `json:"code,omitempty"`
	Type       string `json:"error_type,omitempty"`
	Param      string `json:"param,omitempty"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`

	retryDelay    time.Duration
	hasRetryDelay bool
	display       string
	displaySet    bool
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "model provider error"
	}
	if e.displaySet {
		return e.display
	}
	return e.composeError()
}

func (e *ProviderError) composeError() string {
	parts := []string{"model provider error"}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", e.StatusCode))
	}
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.RequestID != "" {
		parts = append(parts, "request_id="+e.RequestID)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}

func (e *ProviderError) String() string   { return e.Error() }
func (e *ProviderError) GoString() string { return e.Error() }
func (e *ProviderError) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", e.Error())
	default:
		_, _ = fmt.Fprint(state, e.Error())
	}
}

// Validate checks only provider-neutral request shape. Tool argument and output
// semantics remain untrusted data for the capability runtime.
func (r Request) Validate() error {
	if len(r.Input) == 0 {
		return fmt.Errorf("%w: request input is empty", ErrProtocol)
	}
	if r.MaxOutputTokens < 0 {
		return fmt.Errorf("%w: max_output_tokens must not be negative", ErrProtocol)
	}
	if r.Reasoning.Effort != "" && !validEffort(r.Reasoning.Effort) {
		return fmt.Errorf("%w: unsupported reasoning effort %q", ErrProtocol, r.Reasoning.Effort)
	}
	for i, item := range r.Input {
		if err := validateItem(item); err != nil {
			return fmt.Errorf("%w: input item %d: %v", ErrProtocol, i, err)
		}
	}
	seenTools := make(map[string]struct{}, len(r.Tools))
	for i, tool := range r.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("%w: tool %d has an empty name", ErrProtocol, i)
		}
		if _, exists := seenTools[tool.Name]; exists {
			return fmt.Errorf("%w: duplicate tool name %q", ErrProtocol, tool.Name)
		}
		seenTools[tool.Name] = struct{}{}
		if !jsonObject(tool.Parameters) {
			return fmt.Errorf("%w: tool %q parameters must be a JSON object", ErrProtocol, tool.Name)
		}
	}
	return nil
}

func validateItem(item Item) error {
	switch item.Type {
	case ItemMessage:
		switch item.Role {
		case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("message has unsupported role %q", item.Role)
		}
		if len(item.Content) == 0 {
			return errors.New("message has no content")
		}
		for _, content := range item.Content {
			switch content.Type {
			case ContentInputText:
				if item.Role == RoleAssistant {
					return errors.New("assistant replay must use output_text")
				}
			case ContentOutputText, ContentRefusal:
				if item.Role != RoleAssistant {
					return fmt.Errorf("%s is valid only for assistant messages", content.Type)
				}
			default:
				return fmt.Errorf("unsupported message content type %q", content.Type)
			}
		}
	case ItemFunctionCall:
		if item.CallID == "" || item.Name == "" {
			return errors.New("function call requires call_id and name")
		}
	case ItemFunctionCallOutput:
		if item.CallID == "" {
			return errors.New("function call output requires call_id")
		}
	case ItemReasoning:
		if item.ID == "" && item.EncryptedContent == "" && len(item.Summary) == 0 {
			return errors.New("reasoning item has no replayable content")
		}
		for _, content := range item.Summary {
			if content.Type != ContentSummaryText {
				return fmt.Errorf("unsupported reasoning summary type %q", content.Type)
			}
		}
	default:
		return fmt.Errorf("unsupported item type %q", item.Type)
	}
	return nil
}

func validEffort(effort string) bool {
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value.(map[string]any)
	return ok
}

// Drain consumes a stream through an explicit terminal event. It is useful for
// bounded auxiliary calls and tests; the shared query engine normally handles
// events incrementally.
func Drain(stream Stream) ([]Event, error) {
	defer stream.Close()
	var events []Event
	for {
		event, err := stream.Next()
		if inspectModelError(err).eof {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}
