// Package tool implements the model-callable capability boundary: deterministic
// lookup, validation, authorization, execution, result pairing, and scheduling.
package tool

import (
	"context"
	"encoding/json"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/redact"
)

const (
	DefaultResultLimit = 50_000
	DefaultConcurrency = 10
)

// Source records descriptor provenance.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourcePlugin  Source = "plugin"
	SourceMCP     Source = "mcp"
)

// Interruption controls submit-interrupt behavior. Hard turn cancellation is
// always propagated regardless of this value.
type Interruption string

const (
	InterruptionBlock  Interruption = "block"
	InterruptionCancel Interruption = "cancel"
)

// Request is untrusted model protocol input.
type Request struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Input                  json.RawMessage `json:"input"`
	AssistantID            string          `json:"assistant_id,omitempty"`
	projectObserverPayload func([]byte) ([]byte, error)
}

// ProjectObserverPayload applies the descriptor/session scoped exact JSON
// boundary without exposing its credential set. The boolean reports whether a
// projector was installed.
func (request Request) ProjectObserverPayload(raw []byte) ([]byte, bool, error) {
	if request.projectObserverPayload == nil {
		return append([]byte(nil), raw...), false, nil
	}
	projected, err := request.projectObserverPayload(raw)
	return projected, true, err
}

// Result is the exactly-once terminal protocol projection for an accepted ID.
type Result struct {
	ToolUseID string `json:"tool_use_id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	// ContentSuppressed prevents downstream layers from adding synthetic prose
	// after fail-closed credential suppression.
	ContentSuppressed bool            `json:"content_suppressed,omitempty"`
	IsError           bool            `json:"is_error"`
	Code              string          `json:"code,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
	OriginalInput     json.RawMessage `json:"original_input,omitempty"`
	ExecutedInput     json.RawMessage `json:"executed_input,omitempty"`
	UserModified      bool            `json:"user_modified,omitempty"`
	// PermissionRejected marks a deny or permission-owned cancellation. It is
	// separate from IsError because validation and execution failures are not
	// permission denials in the terminal SDK result.
	PermissionRejected bool            `json:"permission_rejected,omitempty"`
	PermissionInput    json.RawMessage `json:"permission_input,omitempty"`
	observerSuppressed bool
}

// Output is a successful domain result before common result policy.
type Output struct {
	Content string
	// ContentSuppressed is a fail-closed terminal state. It prevents the
	// common executor and presentation layers from replacing deliberately
	// omitted sensitive output with synthetic success prose.
	ContentSuppressed bool
	Metadata          map[string]any
}

// CallContext contains invocation-local channels and no presentation state.
type CallContext struct {
	ToolUseID     string
	AssistantID   string
	OriginalInput json.RawMessage
	ExecutedInput json.RawMessage
	UserModified  bool
	Progress      func(Progress)
	// CredentialLookahead lets bounded producers retain enough raw bytes to
	// project a match that starts before their visible limit. ProjectOutput is
	// a capability-only seam: unlike a Set, it cannot be reflected to recover
	// the configured literals.
	CredentialLookahead int
	ProjectOutput       func(value string, rawTruncated bool, limit int) (safe string, truncated, suppressed bool)
}

// Progress is ephemeral and must not be persisted as a terminal result.
type Progress struct {
	ToolUseID string `json:"tool_use_id"`
	Message   string `json:"message"`
	Percent   int    `json:"percent,omitempty"`
}

// Descriptor is the language-neutral contract for one canonical tool.
type Descriptor struct {
	Name              string
	Aliases           []string
	Description       string
	InputSchema       map[string]any
	Source            Source
	Enabled           func() bool
	Validate          func(json.RawMessage) (any, error)
	Semantic          func(any) error
	Classify          func(any) permission.Classification
	ProjectPermission func(any, json.RawMessage) (permission.Request, error)
	Call              func(context.Context, CallContext, any) (Output, error)
	// CredentialSanitizer contains source-scoped literals that must be unioned
	// with the session sanitizer before any result transformation or observer.
	CredentialSanitizer *redact.Set
	MaxResultChars      int // -1 opts out; 0 uses the global limit.
	Interruption        Interruption
}

func (d Descriptor) enabled() bool { return d.Enabled == nil || d.Enabled() }

func (d Descriptor) classification(input any) permission.Classification {
	if d.Classify == nil {
		return permission.Classification{}
	}
	return d.Classify(input)
}

// HookResult may replace a validated input or deny before authorization.
type HookResult struct {
	UpdatedInput json.RawMessage
	DenyReason   string
	AskReason    string
	Progress     []Progress
}

// Hook observes the common lifecycle. Hook errors fail the invocation.
type Hook interface {
	Pre(context.Context, Request, string) (HookResult, error)
	Post(context.Context, Request, Result) error
	Failure(context.Context, Request, Result) error
}

// PermissionDeniedHook is an optional lifecycle extension. A denial hook is
// observational and cannot replace the already-terminal policy decision.
type PermissionDeniedHook interface {
	PermissionDenied(context.Context, Request, Result) error
}

// Authorizer is consumer-shaped so tests and alternate policy generations can
// participate without widening the tool package dependency surface.
type Authorizer interface {
	Authorize(context.Context, permission.Request, permission.Rebuild) (permission.Decision, error)
}
