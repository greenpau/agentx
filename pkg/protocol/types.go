// Package protocol defines the versioned, presentation-independent event
// vocabulary exchanged by the session engine, capability runtime, transcript,
// and user-surface adapters.
package protocol

import (
	"encoding/json"
	"time"

	"github.com/greenpau/agentx/pkg/identity"
)

// CurrentVersion is the canonical event schema version emitted by this build.
const CurrentVersion = 1

// Identifier aliases retain distinct compile-time domains while allowing
// protocol consumers to use one import for the common wire vocabulary.
type (
	SessionID = identity.SessionID
	TurnID    = identity.TurnID
	MessageID = identity.MessageID
	ToolUseID = identity.ToolUseID
	TaskID    = identity.TaskID
	RequestID = identity.RequestID
)

// EventID identifies one canonical event. It is distinct from provider message
// and tool-use identifiers because conflating them breaks replay deduplication.
type EventID string

// EventKind selects the single payload carried by an Event.
type EventKind string

const (
	EventKindMessage         EventKind = "message"
	EventKindToolCall        EventKind = "tool_call"
	EventKindToolResult      EventKind = "tool_result"
	EventKindSessionMetadata EventKind = "session_metadata"
	EventKindUsage           EventKind = "usage"
	EventKindTurnResult      EventKind = "turn_result"
	EventKindProgress        EventKind = "progress"
	EventKindDiagnostic      EventKind = "diagnostic"
	EventKindPermission      EventKind = "permission"
	EventKindTaskLifecycle   EventKind = "task_lifecycle"
	EventKindRetry           EventKind = "retry"
	EventKindConnection      EventKind = "connection"
	EventKindHookLifecycle   EventKind = "hook_lifecycle"
	EventKindCompaction      EventKind = "compaction"
	EventKindCancellation    EventKind = "cancellation"
	EventKindLocalCommand    EventKind = "local_command"
)

// Visibility describes which semantic projections may consume an event. It is
// independent of persistence: a recovered tool result can be model-visible but
// deliberately ephemeral, while internal metadata can be durable but hidden.
type Visibility string

const (
	VisibilityUser     Visibility = "user"
	VisibilityModel    Visibility = "model"
	VisibilityBoth     Visibility = "both"
	VisibilityInternal Visibility = "internal"
)

// UserVisible reports whether a presentation adapter may show the event.
func (v Visibility) UserVisible() bool { return v == VisibilityUser || v == VisibilityBoth }

// ModelVisible reports whether the event belongs in a provider request
// projection. Presentation-only progress and internal metadata return false.
func (v Visibility) ModelVisible() bool { return v == VisibilityModel || v == VisibilityBoth }

// Persistence declares whether an event is authoritative history or derived,
// replaceable process state. Transcript stores must never write Ephemeral data.
type Persistence string

const (
	PersistenceDurable   Persistence = "durable"
	PersistenceEphemeral Persistence = "ephemeral"
)

// Origin describes the authority that produced an event. It is intentionally
// bounded so it is safe for diagnostics and low-cardinality metrics.
type Origin string

const (
	OriginUser       Origin = "user"
	OriginModel      Origin = "model"
	OriginCapability Origin = "capability"
	OriginRuntime    Origin = "runtime"
	OriginRecovery   Origin = "recovery"
)

// SessionMetadata is repeated on durable records so copied or forked events
// can be restamped with destination ownership. It contains no credentials.
type SessionMetadata struct {
	ParentSessionID     SessionID `json:"parent_session_id,omitempty"`
	WorkingDirectory    string    `json:"working_directory,omitempty"`
	Entrypoint          string    `json:"entrypoint,omitempty"`
	Surface             string    `json:"surface,omitempty"`
	UserType            string    `json:"user_type,omitempty"`
	ProductVersion      string    `json:"product_version,omitempty"`
	SourceControlBranch string    `json:"source_control_branch,omitempty"`
	PlanSlug            string    `json:"plan_slug,omitempty"`
}

// Event is the canonical semantic envelope. ParentID captures the persisted
// graph edge; LogicalParentID preserves provenance when a compaction or branch
// deliberately starts a new physical root.
//
// Exactly one payload pointer must be non-nil and must match Kind. Unknown JSON
// fields are ignored by encoding/json for forward compatibility, but an unknown
// version or discriminator is rejected by Validate.
type Event struct {
	Version         int             `json:"version"`
	ID              EventID         `json:"id"`
	SessionID       SessionID       `json:"session_id"`
	TurnID          TurnID          `json:"turn_id,omitempty"`
	ParentID        *EventID        `json:"parent_id"`
	LogicalParentID *EventID        `json:"logical_parent_id,omitempty"`
	Sequence        uint64          `json:"sequence"`
	Timestamp       time.Time       `json:"timestamp"`
	Kind            EventKind       `json:"kind"`
	Visibility      Visibility      `json:"visibility"`
	Persistence     Persistence     `json:"persistence"`
	Origin          Origin          `json:"origin"`
	Session         SessionMetadata `json:"session"`
	Sidechain       bool            `json:"sidechain,omitempty"`
	AgentName       string          `json:"agent_name,omitempty"`
	AgentID         string          `json:"agent_id,omitempty"`

	Message      *Message           `json:"message,omitempty"`
	ToolCall     *ToolCall          `json:"tool_call,omitempty"`
	ToolResult   *ToolResult        `json:"tool_result,omitempty"`
	Metadata     *MetadataEvent     `json:"metadata,omitempty"`
	Usage        *Usage             `json:"usage,omitempty"`
	TurnResult   *TurnResult        `json:"turn_result,omitempty"`
	Progress     *ProgressEvent     `json:"progress,omitempty"`
	Diagnostic   *DiagnosticEvent   `json:"diagnostic,omitempty"`
	Permission   *PermissionEvent   `json:"permission,omitempty"`
	Task         *TaskEvent         `json:"task,omitempty"`
	Retry        *RetryEvent        `json:"retry,omitempty"`
	Connection   *ConnectionEvent   `json:"connection,omitempty"`
	Hook         *HookEvent         `json:"hook,omitempty"`
	Compaction   *CompactionEvent   `json:"compaction,omitempty"`
	Cancellation *CancellationEvent `json:"cancellation,omitempty"`
	LocalCommand *LocalCommandEvent `json:"local_command,omitempty"`
}

// Role is a model-conversation role. Capability outputs use ToolResult events
// rather than overloading a user Message with provider-specific wire details.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message contains provider-independent semantic content. APIMessageID is
// optional provider correlation and never substitutes for the canonical event
// identifier.
type Message struct {
	Role          Role           `json:"role"`
	Content       []ContentBlock `json:"content"`
	APIMessageID  string         `json:"api_message_id,omitempty"`
	APIResponseID string         `json:"api_response_id,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	PromptID      string         `json:"prompt_id,omitempty"`
	Synthetic     bool           `json:"synthetic,omitempty"`
}

// ContentType discriminates a message or tool-result content block.
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentReasoning  ContentType = "reasoning"
	ContentAttachment ContentType = "attachment"
)

// ContentBlock carries either text/reasoning or attachment metadata. Binary
// attachment bytes live in the owning file-transfer subsystem, not JSONL.
type ContentBlock struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	Name     string      `json:"name,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
	URI      string      `json:"uri,omitempty"`
}

// TextBlock constructs a plain text block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: ContentText, Text: text} }

// ToolCall records an accepted model request before tool-schema validation.
// Exactly one argument representation is present: Arguments for an already
// validated object, or RawArguments for the provider string preserved exactly
// even when it is empty or malformed. This lets every accepted provider call ID
// receive a terminal malformed result instead of disappearing during parsing.
type ToolCall struct {
	ID            ToolUseID       `json:"id"`
	Name          string          `json:"name"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	RawArguments  *string         `json:"raw_arguments,omitempty"`
	APIResponseID string          `json:"api_response_id,omitempty"`
}

// NewRawToolCall preserves an untrusted provider argument string for later
// structural and tool-schema validation.
func NewRawToolCall(id ToolUseID, name, rawArguments string) ToolCall {
	return ToolCall{ID: id, Name: name, RawArguments: &rawArguments}
}

// ToolResultStatus is terminal. There is deliberately no pending/running value
// in the result union; progress is a separate ephemeral event.
type ToolResultStatus string

const (
	ToolResultSuccess     ToolResultStatus = "success"
	ToolResultError       ToolResultStatus = "error"
	ToolResultDenied      ToolResultStatus = "denied"
	ToolResultCancelled   ToolResultStatus = "cancelled"
	ToolResultTimedOut    ToolResultStatus = "timed_out"
	ToolResultInterrupted ToolResultStatus = "interrupted"
	ToolResultUnavailable ToolResultStatus = "unavailable"
	ToolResultMalformed   ToolResultStatus = "malformed"
)

// ErrorInfo is a bounded semantic error classification. Message is intended
// for the user/model and must already be secret-safe at the producing boundary.
type ErrorInfo struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ToolResult settles exactly one accepted ToolCall ID.
type ToolResult struct {
	ToolUseID      ToolUseID        `json:"tool_use_id"`
	ToolName       string           `json:"tool_name"`
	Status         ToolResultStatus `json:"status"`
	Content        []ContentBlock   `json:"content"`
	IsError        bool             `json:"is_error"`
	DurationMillis int64            `json:"duration_millis,omitempty"`
	Error          *ErrorInfo       `json:"error,omitempty"`
	Synthetic      bool             `json:"synthetic,omitempty"`
}

// MetadataEvent represents append/last-wins session metadata without giving it
// model visibility. Value must be valid JSON and its key is a stable namespace.
type MetadataEvent struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Usage is a completed provider-call accounting snapshot. Exporters consume a
// copy; the session state remains authoritative for aggregate budget decisions.
type Usage struct {
	Model             string   `json:"model"`
	InputTokens       int64    `json:"input_tokens"`
	CachedInputTokens int64    `json:"cached_input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	ReasoningTokens   int64    `json:"reasoning_tokens"`
	TotalTokens       int64    `json:"total_tokens"`
	CostUSD           *float64 `json:"cost_usd,omitempty"`
}

// TurnResultStatus names terminal turn outcomes independent of presentation.
type TurnResultStatus string

const (
	TurnResultSuccess   TurnResultStatus = "success"
	TurnResultError     TurnResultStatus = "error"
	TurnResultCancelled TurnResultStatus = "cancelled"
	TurnResultMaxTurns  TurnResultStatus = "max_turns"
	TurnResultMaxBudget TurnResultStatus = "max_budget"
)

// TurnResult records terminal turn semantics and bounded accounting.
type TurnResult struct {
	Status         TurnResultStatus `json:"status"`
	IsError        bool             `json:"is_error"`
	StopReason     string           `json:"stop_reason,omitempty"`
	Message        string           `json:"message,omitempty"`
	Turns          int              `json:"turns"`
	DurationMillis int64            `json:"duration_millis"`
}

// ProgressEvent is presentation-only and therefore must be ephemeral.
type ProgressEvent struct {
	Phase         string    `json:"phase"`
	Message       string    `json:"message,omitempty"`
	ToolUseID     ToolUseID `json:"tool_use_id,omitempty"`
	ElapsedMillis int64     `json:"elapsed_millis,omitempty"`
}

// DiagnosticEvent carries a bounded classification without raw prompt, source,
// command, path, credentials, or arbitrary external response bodies.
type DiagnosticEvent struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// PermissionEvent carries both halves of an authorization exchange through
// the same semantic bus used by every surface. Input is deliberately omitted:
// the accepted ToolCall already owns it and diagnostics must not duplicate
// potentially sensitive arguments.
type PermissionEvent struct {
	RequestID RequestID `json:"request_id"`
	ToolUseID ToolUseID `json:"tool_use_id"`
	ToolName  string    `json:"tool_name"`
	Stage     string    `json:"stage"` // requested or decided
	Decision  string    `json:"decision,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// TaskEvent is the canonical lifecycle projection for finite asynchronous
// work. The task store remains authoritative for complete task records.
type TaskEvent struct {
	TaskID      TaskID `json:"task_id"`
	Stage       string `json:"stage"` // started, progress, notification
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	OutputPath  string `json:"output_path,omitempty"`
}

type RetryEvent struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	DelayMillis int64  `json:"delay_millis"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	Category    string `json:"category"`
}

type ConnectionEvent struct {
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Generation uint64 `json:"generation,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type HookEvent struct {
	HookID   string `json:"hook_id"`
	Name     string `json:"name"`
	Event    string `json:"event"`
	State    string `json:"state"` // started, progress, success, error, cancelled
	Output   string `json:"output,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type CompactionEvent struct {
	Trigger       string   `json:"trigger"` // manual or auto
	State         string   `json:"state"`   // started, completed, failed
	PreTokens     int      `json:"pre_tokens"`
	SummaryID     *EventID `json:"summary_id,omitempty"`
	PreservedHead *EventID `json:"preserved_head,omitempty"`
	Anchor        *EventID `json:"anchor,omitempty"`
	PreservedTail *EventID `json:"preserved_tail,omitempty"`
}

type CancellationEvent struct {
	Scope    string `json:"scope"`
	TargetID string `json:"target_id,omitempty"`
	State    string `json:"state"` // requested, acknowledged, completed
	Reason   string `json:"reason,omitempty"`
}

type LocalCommandEvent struct {
	Command string `json:"command"`
	Status  string `json:"status"`
	Output  string `json:"output,omitempty"`
}
