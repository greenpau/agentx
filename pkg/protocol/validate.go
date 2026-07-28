package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/greenpau/agentx/pkg/attachment"
)

const (
	maxIdentifierLength = 256
	maxNameLength       = 128
)

// Validate checks the semantic envelope. Sequence zero is accepted for a new
// event because the transcript owner assigns durable order atomically.
func (e Event) Validate() error {
	if e.Version != LegacyVersion && e.Version != CurrentVersion {
		return fmt.Errorf("unsupported protocol version %d", e.Version)
	}
	if err := validateIdentifier("event id", string(e.ID)); err != nil {
		return err
	}
	if err := validateIdentifier("session id", string(e.SessionID)); err != nil {
		return err
	}
	if e.TurnID != "" {
		if err := validateIdentifier("turn id", string(e.TurnID)); err != nil {
			return err
		}
	}
	if e.ParentID != nil {
		if err := validateIdentifier("parent id", string(*e.ParentID)); err != nil {
			return err
		}
		if *e.ParentID == e.ID {
			return errors.New("event cannot be its own parent")
		}
	}
	if e.LogicalParentID != nil {
		if err := validateIdentifier("logical parent id", string(*e.LogicalParentID)); err != nil {
			return err
		}
	}
	if e.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if y := e.Timestamp.Year(); y < 1 || y > 9999 {
		return fmt.Errorf("timestamp year %d is outside the portable range", y)
	}
	if !validVisibility(e.Visibility) {
		return fmt.Errorf("unknown visibility %q", e.Visibility)
	}
	if e.Persistence != PersistenceDurable && e.Persistence != PersistenceEphemeral {
		return fmt.Errorf("unknown persistence %q", e.Persistence)
	}
	if !validOrigin(e.Origin) {
		return fmt.Errorf("unknown origin %q", e.Origin)
	}
	if err := validateSessionMetadata(e.Session); err != nil {
		return err
	}

	payloads := 0
	for _, present := range []bool{
		e.Message != nil,
		e.ToolCall != nil,
		e.ToolResult != nil,
		e.Metadata != nil,
		e.Usage != nil,
		e.TurnResult != nil,
		e.Progress != nil,
		e.Diagnostic != nil,
		e.Permission != nil,
		e.Task != nil,
		e.Retry != nil,
		e.Connection != nil,
		e.Hook != nil,
		e.Compaction != nil,
		e.Cancellation != nil,
		e.LocalCommand != nil,
	} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("event must carry exactly one payload, got %d", payloads)
	}

	switch e.Kind {
	case EventKindMessage:
		if e.Message == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Version == LegacyVersion {
			for _, block := range e.Message.Content {
				if block.Type == ContentAttachment {
					return errors.New("attachment content requires protocol version 2")
				}
			}
		}
		return e.Message.Validate()
	case EventKindToolCall:
		if e.ToolCall == nil {
			return payloadMismatch(e.Kind)
		}
		return e.ToolCall.Validate()
	case EventKindToolResult:
		if e.ToolResult == nil {
			return payloadMismatch(e.Kind)
		}
		return e.ToolResult.Validate()
	case EventKindSessionMetadata:
		if e.Metadata == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Visibility.ModelVisible() {
			return errors.New("session metadata cannot be model-visible")
		}
		return e.Metadata.Validate()
	case EventKindUsage:
		if e.Usage == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Visibility.ModelVisible() {
			return errors.New("usage accounting cannot be model-visible")
		}
		return e.Usage.Validate()
	case EventKindTurnResult:
		if e.TurnResult == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Visibility.ModelVisible() {
			return errors.New("turn result cannot be model-visible")
		}
		return e.TurnResult.Validate()
	case EventKindProgress:
		if e.Progress == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Persistence != PersistenceEphemeral {
			return errors.New("progress must be ephemeral")
		}
		if e.Visibility.ModelVisible() {
			return errors.New("progress cannot be model-visible")
		}
		return e.Progress.Validate()
	case EventKindDiagnostic:
		if e.Diagnostic == nil {
			return payloadMismatch(e.Kind)
		}
		if e.Visibility.ModelVisible() {
			return errors.New("diagnostics cannot be model-visible")
		}
		if e.Persistence != PersistenceEphemeral {
			return errors.New("diagnostics must be ephemeral")
		}
		return e.Diagnostic.Validate()
	case EventKindPermission:
		if e.Permission == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.Permission.Validate()
	case EventKindTaskLifecycle:
		if e.Task == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.Task.Validate()
	case EventKindRetry:
		if e.Retry == nil || e.Visibility.ModelVisible() || e.Persistence != PersistenceEphemeral {
			return payloadMismatch(e.Kind)
		}
		return e.Retry.Validate()
	case EventKindConnection:
		if e.Connection == nil || e.Visibility.ModelVisible() || e.Persistence != PersistenceEphemeral {
			return payloadMismatch(e.Kind)
		}
		return e.Connection.Validate()
	case EventKindHookLifecycle:
		if e.Hook == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.Hook.Validate()
	case EventKindCompaction:
		if e.Compaction == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.Compaction.Validate()
	case EventKindCancellation:
		if e.Cancellation == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.Cancellation.Validate()
	case EventKindLocalCommand:
		if e.LocalCommand == nil || e.Visibility.ModelVisible() {
			return payloadMismatch(e.Kind)
		}
		return e.LocalCommand.Validate()
	default:
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
}

// ValidateStored additionally requires the monotonic sequence assigned by a
// durable transcript. Ephemeral events are valid with sequence zero.
func (e Event) ValidateStored() error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Persistence == PersistenceDurable && e.Sequence == 0 {
		return errors.New("durable event sequence is required")
	}
	return nil
}

func payloadMismatch(kind EventKind) error {
	return fmt.Errorf("payload does not match event kind %q", kind)
}

func validVisibility(v Visibility) bool {
	switch v {
	case VisibilityUser, VisibilityModel, VisibilityBoth, VisibilityInternal:
		return true
	default:
		return false
	}
}

func validOrigin(v Origin) bool {
	switch v {
	case OriginUser, OriginModel, OriginCapability, OriginRuntime, OriginRecovery:
		return true
	default:
		return false
	}
}

func validateIdentifier(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maxIdentifierLength)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%s contains whitespace or control characters", name)
		}
	}
	return nil
}

// ValidatePromptID validates a host-supplied idempotency key before it can be
// retained in the session ledger. Prompt identifiers intentionally use the
// same bounded, whitespace-free wire shape as the other correlation IDs.
func ValidatePromptID(value string) error {
	if value == "" {
		return nil
	}
	return validateIdentifier("prompt id", value)
}

func validateName(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxNameLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maxNameLength)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

func validateSessionMetadata(v SessionMetadata) error {
	if v.ParentSessionID != "" {
		if err := validateIdentifier("parent session id", string(v.ParentSessionID)); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"entrypoint", v.Entrypoint},
		{"surface", v.Surface},
		{"user type", v.UserType},
		{"product version", v.ProductVersion},
		{"plan slug", v.PlanSlug},
	} {
		if len(field.value) > 512 {
			return fmt.Errorf("%s exceeds 512 bytes", field.name)
		}
	}
	if len(v.WorkingDirectory) > 16*1024 || len(v.SourceControlBranch) > 16*1024 {
		return errors.New("session path or branch metadata exceeds 16384 bytes")
	}
	return nil
}

// Validate checks message role and block-level union constraints.
func (m Message) Validate() error {
	switch m.Role {
	case RoleSystem, RoleUser, RoleAssistant:
	default:
		return fmt.Errorf("unknown message role %q", m.Role)
	}
	if m.PromptID != "" {
		if m.Role != RoleUser {
			return errors.New("prompt id requires user role")
		}
		if err := ValidatePromptID(m.PromptID); err != nil {
			return err
		}
	}
	if m.APIMessageID != "" {
		if err := validateIdentifier("API message id", m.APIMessageID); err != nil {
			return err
		}
	}
	if m.APIResponseID != "" {
		if err := validateIdentifier("API response id", m.APIResponseID); err != nil {
			return err
		}
	}
	if m.Phase != "" {
		if m.Role != RoleAssistant {
			return errors.New("message phase requires assistant role")
		}
		if m.Phase != "commentary" && m.Phase != "final_answer" {
			return fmt.Errorf("unsupported assistant message phase %q", m.Phase)
		}
	}
	if len(m.Content) == 0 {
		return errors.New("message content is required")
	}
	limits := attachment.DefaultLimits()
	seenAttachments := make(map[attachment.ID]struct{})
	var attachmentBytes int64
	for i, block := range m.Content {
		if err := block.Validate(); err != nil {
			return fmt.Errorf("content block %d: %w", i, err)
		}
		if block.Type == ContentReasoning && m.Role != RoleAssistant {
			return fmt.Errorf("content block %d: reasoning requires assistant role", i)
		}
		if block.Type != ContentAttachment {
			continue
		}
		if m.Role != RoleUser {
			return fmt.Errorf("content block %d: attachment requires user role", i)
		}
		if len(seenAttachments) >= limits.MaxAttachmentsPerMessage {
			return fmt.Errorf("message exceeds %d attachments", limits.MaxAttachmentsPerMessage)
		}
		if _, duplicate := seenAttachments[block.AttachmentID]; duplicate {
			return fmt.Errorf("content block %d: duplicate attachment id", i)
		}
		seenAttachments[block.AttachmentID] = struct{}{}
		if block.SizeBytes > limits.MaxAggregateBytes-attachmentBytes {
			return fmt.Errorf("message attachment bytes exceed %d", limits.MaxAggregateBytes)
		}
		attachmentBytes += block.SizeBytes
	}
	return nil
}

// Validate checks the content-block discriminator and required fields.
func (b ContentBlock) Validate() error {
	switch b.Type {
	case ContentText, ContentReasoning:
		if b.AttachmentID != "" || b.Kind != "" || b.Name != "" ||
			b.MIMEType != "" || b.SizeBytes != 0 || b.SHA256 != "" ||
			b.StorageID != "" {
			return errors.New("text/reasoning block contains attachment fields")
		}
	case ContentAttachment:
		if b.Text != "" {
			return errors.New("attachment block contains inline text")
		}
		if err := b.AttachmentManifest().Validate(attachment.DefaultLimits()); err != nil {
			return fmt.Errorf("invalid attachment manifest: %w", err)
		}
	default:
		return fmt.Errorf("unknown content type %q", b.Type)
	}
	return nil
}

// Validate checks tool identity, name, and the argument-representation union.
// RawArguments deliberately remains unparsed at acceptance time.
func (c ToolCall) Validate() error {
	if err := validateIdentifier("tool-use id", string(c.ID)); err != nil {
		return err
	}
	if err := validateName("tool name", c.Name); err != nil {
		return err
	}
	if c.APIResponseID != "" {
		if err := validateIdentifier("API response id", c.APIResponseID); err != nil {
			return err
		}
	}
	hasParsed := c.Arguments != nil
	hasRaw := c.RawArguments != nil
	if hasParsed == hasRaw {
		return errors.New("tool call requires exactly one parsed or raw argument representation")
	}
	if hasRaw {
		return nil
	}
	_, err := parseArgumentObject(c.Arguments)
	return err
}

// ParseArguments performs the structural validation deliberately deferred for
// provider-originated calls. The returned bytes are an independent copy.
func (c ToolCall) ParseArguments() (json.RawMessage, error) {
	if err := validateIdentifier("tool-use id", string(c.ID)); err != nil {
		return nil, err
	}
	if err := validateName("tool name", c.Name); err != nil {
		return nil, err
	}
	if c.APIResponseID != "" {
		if err := validateIdentifier("API response id", c.APIResponseID); err != nil {
			return nil, err
		}
	}
	if (c.Arguments != nil) == (c.RawArguments != nil) {
		return nil, errors.New("tool call requires exactly one parsed or raw argument representation")
	}
	if c.RawArguments != nil {
		return parseArgumentObject([]byte(*c.RawArguments))
	}
	return parseArgumentObject(c.Arguments)
}

func parseArgumentObject(arguments []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || !json.Valid(trimmed) || trimmed[0] != '{' {
		return nil, errors.New("tool arguments must be a valid JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("tool arguments must be a non-null JSON object")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

// Validate checks that a ToolResult is terminal and error classification agrees
// with status. Every non-success terminal outcome is model-visible as an error.
func (r ToolResult) Validate() error {
	if err := validateIdentifier("tool-use id", string(r.ToolUseID)); err != nil {
		return err
	}
	if err := validateName("tool name", r.ToolName); err != nil {
		return err
	}
	if r.DurationMillis < 0 {
		return errors.New("tool duration cannot be negative")
	}
	if len(r.Content) == 0 {
		return errors.New("tool result content is required")
	}
	for i, block := range r.Content {
		if block.Type == ContentReasoning {
			return fmt.Errorf("content block %d: tool result cannot contain reasoning", i)
		}
		if err := block.Validate(); err != nil {
			return fmt.Errorf("content block %d: %w", i, err)
		}
	}
	valid := false
	switch r.Status {
	case ToolResultSuccess:
		valid = !r.IsError
	case ToolResultError, ToolResultDenied, ToolResultCancelled, ToolResultTimedOut,
		ToolResultInterrupted, ToolResultUnavailable, ToolResultMalformed:
		valid = r.IsError
	}
	if !valid {
		return fmt.Errorf("tool result status %q and is_error=%t are inconsistent", r.Status, r.IsError)
	}
	if r.Error != nil {
		if err := r.Error.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks bounded error classification fields.
func (e ErrorInfo) Validate() error {
	if err := validateName("error code", e.Code); err != nil {
		return err
	}
	if len(e.Message) > 16*1024 {
		return errors.New("error message exceeds 16384 bytes")
	}
	return nil
}

// Validate checks append metadata syntax without interpreting its owner-specific
// schema.
func (m MetadataEvent) Validate() error {
	if err := validateName("metadata key", m.Key); err != nil {
		return err
	}
	if len(m.Value) == 0 || !json.Valid(m.Value) {
		return errors.New("metadata value must be valid JSON")
	}
	return nil
}

// Validate checks nonnegative completed usage values and optional cost.
func (u Usage) Validate() error {
	if err := validateName("usage model", u.Model); err != nil {
		return err
	}
	for name, value := range map[string]int64{
		"input tokens":        u.InputTokens,
		"cached input tokens": u.CachedInputTokens,
		"output tokens":       u.OutputTokens,
		"reasoning tokens":    u.ReasoningTokens,
		"total tokens":        u.TotalTokens,
	} {
		if value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	if u.CachedInputTokens > u.InputTokens || u.ReasoningTokens > u.OutputTokens {
		return errors.New("usage detail exceeds its parent token count")
	}
	if u.InputTokens > math.MaxInt64-u.OutputTokens {
		return errors.New("usage input and output token sum overflows")
	}
	if u.TotalTokens < u.InputTokens+u.OutputTokens {
		return errors.New("usage total token count is incoherent")
	}
	if u.CostUSD != nil {
		if *u.CostUSD < 0 || math.IsNaN(*u.CostUSD) || math.IsInf(*u.CostUSD, 0) {
			return errors.New("usage cost must be finite and nonnegative")
		}
	}
	return nil
}

// Validate checks turn-result terminal state and accounting.
func (r TurnResult) Validate() error {
	if r.Turns < 0 || r.DurationMillis < 0 {
		return errors.New("turn count and duration cannot be negative")
	}
	switch r.Status {
	case TurnResultSuccess:
		if r.IsError {
			return errors.New("successful turn result cannot be an error")
		}
	case TurnResultError, TurnResultCancelled, TurnResultMaxTurns, TurnResultMaxBudget:
		if !r.IsError {
			return fmt.Errorf("turn result status %q must be an error", r.Status)
		}
	default:
		return fmt.Errorf("unknown turn result status %q", r.Status)
	}
	return nil
}

// Validate checks bounded progress fields.
func (p ProgressEvent) Validate() error {
	if err := validateName("progress phase", p.Phase); err != nil {
		return err
	}
	if p.ElapsedMillis < 0 {
		return errors.New("progress elapsed time cannot be negative")
	}
	if p.ToolUseID != "" {
		if err := validateIdentifier("tool-use id", string(p.ToolUseID)); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a bounded, already-sanitized diagnostic.
func (d DiagnosticEvent) Validate() error {
	if err := validateName("diagnostic code", d.Code); err != nil {
		return err
	}
	if len(d.Message) > 4*1024 {
		return errors.New("diagnostic message exceeds 4096 bytes")
	}
	return nil
}

func (p PermissionEvent) Validate() error {
	if err := validateIdentifier("permission request id", string(p.RequestID)); err != nil {
		return err
	}
	if err := validateIdentifier("permission tool-use id", string(p.ToolUseID)); err != nil {
		return err
	}
	if err := validateName("permission tool name", p.ToolName); err != nil {
		return err
	}
	if p.Stage != "requested" && p.Stage != "decided" {
		return errors.New("permission stage must be requested or decided")
	}
	if p.Stage == "requested" && p.Decision != "" || p.Stage == "decided" && p.Decision == "" {
		return errors.New("permission decision is inconsistent with stage")
	}
	if p.Decision != "" && p.Decision != "allow" && p.Decision != "ask" && p.Decision != "deny" && p.Decision != "cancel" {
		return errors.New("unknown permission decision")
	}
	return boundedSemanticText("permission reason", p.Reason, 4096)
}

func (t TaskEvent) Validate() error {
	if err := validateIdentifier("task id", string(t.TaskID)); err != nil {
		return err
	}
	if t.Stage != "started" && t.Stage != "progress" && t.Stage != "notification" {
		return errors.New("unknown task lifecycle stage")
	}
	if t.Status != "" && t.Status != "pending" && t.Status != "running" && t.Status != "completed" && t.Status != "failed" && t.Status != "killed" && t.Status != "stopped" {
		return errors.New("unknown task status")
	}
	if err := boundedSemanticText("task description", t.Description, 4096); err != nil {
		return err
	}
	return boundedSemanticText("task output path", t.OutputPath, 16*1024)
}

func (r RetryEvent) Validate() error {
	if r.Attempt < 1 || r.MaxAttempts < r.Attempt || r.MaxAttempts > 100 || r.DelayMillis < 0 || r.HTTPStatus < 0 || r.HTTPStatus > 999 {
		return errors.New("retry accounting is invalid")
	}
	return validateName("retry category", r.Category)
}

func (c ConnectionEvent) Validate() error {
	if err := validateName("connection provider", c.Provider); err != nil {
		return err
	}
	if err := validateName("connection name", c.Name); err != nil {
		return err
	}
	if err := validateName("connection state", c.State); err != nil {
		return err
	}
	return boundedSemanticText("connection reason", c.Reason, 4096)
}

func (h HookEvent) Validate() error {
	if err := validateIdentifier("hook id", h.HookID); err != nil {
		return err
	}
	for label, value := range map[string]string{"hook name": h.Name, "hook event": h.Event, "hook state": h.State} {
		if err := validateName(label, value); err != nil {
			return err
		}
	}
	if h.ExitCode != nil && (*h.ExitCode < -1 || *h.ExitCode > 255) {
		return errors.New("hook exit code is invalid")
	}
	return boundedSemanticText("hook output", h.Output, 64*1024)
}

func (c CompactionEvent) Validate() error {
	if c.Trigger != "manual" && c.Trigger != "auto" || c.State != "started" && c.State != "completed" && c.State != "failed" || c.PreTokens < 0 {
		return errors.New("compaction lifecycle is invalid")
	}
	for label, id := range map[string]*EventID{"summary id": c.SummaryID, "preserved head": c.PreservedHead, "anchor": c.Anchor, "preserved tail": c.PreservedTail} {
		if id != nil {
			if err := validateIdentifier(label, string(*id)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c CancellationEvent) Validate() error {
	if err := validateName("cancellation scope", c.Scope); err != nil {
		return err
	}
	if c.TargetID != "" {
		if err := validateIdentifier("cancellation target", c.TargetID); err != nil {
			return err
		}
	}
	if c.State != "requested" && c.State != "acknowledged" && c.State != "completed" {
		return errors.New("unknown cancellation state")
	}
	return boundedSemanticText("cancellation reason", c.Reason, 4096)
}

func (c LocalCommandEvent) Validate() error {
	if err := validateName("local command", c.Command); err != nil {
		return err
	}
	if c.Status != "success" && c.Status != "error" && c.Status != "cancelled" {
		return errors.New("unknown local command status")
	}
	return boundedSemanticText("local command output", c.Output, 64*1024)
}

func boundedSemanticText(name, value string, maximum int) error {
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", name, maximum)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains NUL", name)
	}
	return nil
}
