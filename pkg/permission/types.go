// Package permission owns composed tool authorization. Presentation adapters
// may collect approval responses, but they do not decide policy truth.
package permission

import (
	"context"
	"encoding/json"
)

// Mode controls unresolved permission requests.
type Mode string

const (
	ModeDefault     Mode = "default"
	ModeAcceptEdits Mode = "acceptEdits"
	ModePlan        Mode = "plan"
	ModeDontAsk     Mode = "dontAsk"
	ModeBypass      Mode = "bypassPermissions"
)

// Effect is a configured rule result.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectAsk   Effect = "ask"
	EffectDeny  Effect = "deny"
)

// DecisionKind is a terminal authorization behavior.
type DecisionKind string

const (
	DecisionAllow  DecisionKind = "allow"
	DecisionAsk    DecisionKind = "ask"
	DecisionDeny   DecisionKind = "deny"
	DecisionCancel DecisionKind = "cancel"
)

// Classification is computed from validated invocation input.
type Classification struct {
	ReadOnly        bool `json:"read_only"`
	Destructive     bool `json:"destructive"`
	OpenWorld       bool `json:"open_world"`
	Interaction     bool `json:"requires_interaction"`
	ConcurrencySafe bool `json:"concurrency_safe"`
}

// PathOperation selects read or mutation policy.
type PathOperation string

const (
	PathRead  PathOperation = "read"
	PathWrite PathOperation = "write"
)

// PathAccess is one filesystem resource used by a validated request.
type PathAccess struct {
	Path      string        `json:"path"`
	Operation PathOperation `json:"operation"`
}

// Request is the complete policy projection for one validated tool input.
type Request struct {
	Tool           string
	ToolUseID      string
	Input          json.RawMessage
	Content        string
	MatchContents  []string
	DenyContents   []string
	AllowContents  []string
	Classification Classification
	Paths          []PathAccess
	Shell          *ShellAnalysis
	MandatoryAsk   string
	HookAsk        string
	HardDeny       string
}

// Decision records why and over which exact input execution was authorized or
// refused. Input is selected execution evidence; OriginalInput remains audit
// evidence and is never overwritten.
type Decision struct {
	Kind          DecisionKind    `json:"kind"`
	Reason        string          `json:"reason"`
	Source        string          `json:"source,omitempty"`
	MatchedRule   string          `json:"matched_rule,omitempty"`
	OriginalInput json.RawMessage `json:"original_input,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	UserModified  bool            `json:"user_modified"`
	EditCycles    int             `json:"edit_cycles"`
}

// ApprovalRequest is safe, structured input to a terminal or SDK adapter.
type ApprovalRequest struct {
	Tool        string          `json:"tool"`
	ToolUseID   string          `json:"tool_use_id"`
	Input       json.RawMessage `json:"input"`
	Reason      string          `json:"reason"`
	Mandatory   bool            `json:"mandatory"`
	MatchedRule string          `json:"matched_rule,omitempty"`
}

// ApprovalResponse is the first decisive response from an approval surface.
// UpdatedInput is a complete replacement, never a merge patch.
type ApprovalResponse struct {
	Kind         DecisionKind
	UpdatedInput json.RawMessage
	Reason       string
}

// Approver adapts interactive, headless, or remote permission round trips.
type Approver func(context.Context, ApprovalRequest) (ApprovalResponse, error)

// Rebuild validates a complete replacement input and recomputes every dynamic
// classifier and path projection. It is the hardened edited-input boundary.
type Rebuild func(json.RawMessage) (Request, error)

// Rule preserves source attribution for a parsed permission rule.
type Rule struct {
	Tool    string `json:"tool"`
	Pattern string `json:"pattern,omitempty"`
	Effect  Effect `json:"effect"`
	Source  string `json:"source"`
	Managed bool   `json:"managed"`
	Raw     string `json:"raw"`
}
