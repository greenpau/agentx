package protocol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
)

var testTime = time.Date(2026, 7, 21, 12, 0, 0, 123, time.UTC)

func validEvent(kind EventKind) Event {
	event := Event{
		Version:     CurrentVersion,
		ID:          "evt_test",
		SessionID:   "ses_test",
		TurnID:      "turn_test",
		Sequence:    1,
		Timestamp:   testTime,
		Kind:        kind,
		Visibility:  VisibilityBoth,
		Persistence: PersistenceDurable,
		Origin:      OriginRuntime,
	}
	switch kind {
	case EventKindMessage:
		event.Message = &Message{Role: RoleUser, Content: []ContentBlock{TextBlock("hello")}}
	case EventKindToolCall:
		event.ToolCall = &ToolCall{ID: "tool_test", Name: "Read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	case EventKindToolResult:
		event.ToolResult = &ToolResult{
			ToolUseID: "tool_test",
			ToolName:  "Read",
			Status:    ToolResultSuccess,
			Content:   []ContentBlock{TextBlock("ok")},
		}
	case EventKindSessionMetadata:
		event.Visibility = VisibilityInternal
		event.Metadata = &MetadataEvent{Key: "title", Value: json.RawMessage(`"session"`)}
	case EventKindUsage:
		event.Visibility = VisibilityInternal
		event.Usage = &Usage{Model: "gpt-5.6-sol", InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	case EventKindTurnResult:
		event.Visibility = VisibilityUser
		event.TurnResult = &TurnResult{Status: TurnResultSuccess, Message: "done", Turns: 1}
	case EventKindProgress:
		event.Visibility = VisibilityUser
		event.Persistence = PersistenceEphemeral
		event.Progress = &ProgressEvent{Phase: "tool", Message: "working"}
	case EventKindDiagnostic:
		event.Visibility = VisibilityInternal
		event.Persistence = PersistenceEphemeral
		event.Diagnostic = &DiagnosticEvent{Code: "retry", Message: "retrying", Retryable: true}
	case EventKindPermission:
		event.Visibility = VisibilityInternal
		event.Permission = &PermissionEvent{RequestID: "req_test", ToolUseID: "tool_test", ToolName: "Read", Stage: "requested"}
	case EventKindTaskLifecycle:
		event.Visibility = VisibilityUser
		event.Task = &TaskEvent{TaskID: "task_test", Stage: "started", Status: "running"}
	case EventKindRetry:
		event.Visibility = VisibilityUser
		event.Persistence = PersistenceEphemeral
		event.Retry = &RetryEvent{Attempt: 1, MaxAttempts: 3, Category: "server_error"}
	case EventKindConnection:
		event.Visibility = VisibilityUser
		event.Persistence = PersistenceEphemeral
		event.Connection = &ConnectionEvent{Provider: "mcp", Name: "local", State: "connected"}
	case EventKindHookLifecycle:
		event.Visibility = VisibilityInternal
		event.Hook = &HookEvent{HookID: "hook_test", Name: "guard", Event: "PreToolUse", State: "success"}
	case EventKindCompaction:
		event.Visibility = VisibilityInternal
		event.Compaction = &CompactionEvent{Trigger: "auto", State: "completed", PreTokens: 100}
	case EventKindCancellation:
		event.Visibility = VisibilityUser
		event.Cancellation = &CancellationEvent{Scope: "turn", TargetID: "turn_test", State: "requested"}
	case EventKindLocalCommand:
		event.Visibility = VisibilityUser
		event.LocalCommand = &LocalCommandEvent{Command: "status", Status: "success"}
	}
	return event
}

func TestEventValidateKinds(t *testing.T) {
	for _, kind := range []EventKind{
		EventKindMessage,
		EventKindToolCall,
		EventKindToolResult,
		EventKindSessionMetadata,
		EventKindUsage,
		EventKindTurnResult,
		EventKindProgress,
		EventKindDiagnostic,
		EventKindPermission,
		EventKindTaskLifecycle,
		EventKindRetry,
		EventKindConnection,
		EventKindHookLifecycle,
		EventKindCompaction,
		EventKindCancellation,
		EventKindLocalCommand,
	} {
		t.Run(string(kind), func(t *testing.T) {
			if err := validEvent(kind).ValidateStored(); err != nil {
				t.Fatalf("valid %s event rejected: %v", kind, err)
			}
		})
	}
}

func TestUsageValidateRejectsIncoherentAndOverflowingCounts(t *testing.T) {
	tests := []Usage{
		{Model: "gpt-5.6-sol", InputTokens: 1, CachedInputTokens: 2, TotalTokens: 2},
		{Model: "gpt-5.6-sol", OutputTokens: 1, ReasoningTokens: 2, TotalTokens: 2},
		{Model: "gpt-5.6-sol", InputTokens: math.MaxInt64, OutputTokens: 1, TotalTokens: math.MaxInt64},
		{Model: "gpt-5.6-sol", InputTokens: 2, OutputTokens: 1, TotalTokens: 2},
	}
	for index, usage := range tests {
		if err := usage.Validate(); err == nil {
			t.Fatalf("invalid usage %d was accepted: %#v", index, usage)
		}
	}
}

func TestEventValidateRejectsInvalidUnionsAndPolicies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"version", func(e *Event) { e.Version++ }},
		{"self parent", func(e *Event) { parent := e.ID; e.ParentID = &parent }},
		{"missing payload", func(e *Event) { e.Message = nil }},
		{"multiple payloads", func(e *Event) { e.Diagnostic = &DiagnosticEvent{Code: "x"} }},
		{"durable progress", func(e *Event) {
			e.Kind = EventKindProgress
			e.Message = nil
			e.Progress = &ProgressEvent{Phase: "work"}
		}},
		{"model-visible metadata", func(e *Event) {
			e.Kind = EventKindSessionMetadata
			e.Message = nil
			e.Metadata = &MetadataEvent{Key: "title", Value: json.RawMessage(`"x"`)}
		}},
		{"reasoning user", func(e *Event) { e.Message.Content = []ContentBlock{{Type: ContentReasoning, Text: "hidden"}} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validEvent(EventKindMessage)
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestToolCallArgumentsUnionAndDeferredRawValidation(t *testing.T) {
	for _, raw := range []string{"", "null", `[]`, `"text"`, `{broken}`} {
		call := ToolCall{ID: "tool_test", Name: "Read", Arguments: json.RawMessage(raw)}
		if err := call.Validate(); err == nil {
			t.Fatalf("expected parsed form %q to be rejected", raw)
		}
	}
	call := ToolCall{ID: "tool_test", Name: "Read", Arguments: json.RawMessage(`{}`)}
	if err := call.Validate(); err != nil {
		t.Fatalf("empty object should be valid: %v", err)
	}
	if parsed, err := call.ParseArguments(); err != nil || string(parsed) != `{}` {
		t.Fatalf("parsed arguments = %q, %v", parsed, err)
	}

	for _, raw := range []string{"", "null", `[]`, `{broken}`, `{"path":"README.md"}`} {
		rawCall := NewRawToolCall("tool_raw", "Read", raw)
		if err := rawCall.Validate(); err != nil {
			t.Fatalf("raw provider call %q must remain persistable: %v", raw, err)
		}
		parsed, parseErr := rawCall.ParseArguments()
		if raw == `{"path":"README.md"}` {
			if parseErr != nil || string(parsed) != raw {
				t.Fatalf("valid raw arguments = %q, %v", parsed, parseErr)
			}
		} else if parseErr == nil {
			t.Fatalf("malformed raw arguments %q unexpectedly parsed", raw)
		}
	}

	raw := `{}`
	ambiguous := ToolCall{ID: "tool_test", Name: "Read", Arguments: json.RawMessage(`{}`), RawArguments: &raw}
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("ambiguous parsed/raw union was accepted")
	}
}

func TestToolResultStatusAndErrorMustAgree(t *testing.T) {
	base := ToolResult{
		ToolUseID: "tool_test",
		ToolName:  "Read",
		Content:   []ContentBlock{TextBlock("result")},
	}
	tests := []struct {
		status  ToolResultStatus
		isError bool
		valid   bool
	}{
		{ToolResultSuccess, false, true},
		{ToolResultSuccess, true, false},
		{ToolResultDenied, true, true},
		{ToolResultInterrupted, false, false},
		{"future", true, false},
	}
	for _, test := range tests {
		result := base
		result.Status = test.status
		result.IsError = test.isError
		err := result.Validate()
		if (err == nil) != test.valid {
			t.Fatalf("status=%q error=%t: got %v", test.status, test.isError, err)
		}
	}
}

func TestUsageRejectsNonFiniteCost(t *testing.T) {
	for _, cost := range []float64{-1, math.NaN(), math.Inf(1)} {
		usage := Usage{Model: "gpt-5.6-sol", CostUSD: &cost}
		if err := usage.Validate(); err == nil {
			t.Fatalf("expected cost %v to be rejected", cost)
		}
	}
}

func TestJSONEnvelopeUsesOnePhysicalLine(t *testing.T) {
	event := validEvent(EventKindMessage)
	event.Message.Content[0].Text = "line one\nline two\u2028line three\u2029"
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(encoded), '\n') || strings.ContainsRune(string(encoded), '\u2028') || strings.ContainsRune(string(encoded), '\u2029') {
		t.Fatalf("encoded event contains a physical line separator: %q", encoded)
	}
	var roundTrip Event
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.Message.Content[0].Text; got != event.Message.Content[0].Text {
		t.Fatalf("round trip changed text: %q", got)
	}
}

func TestConstructorsProduceValidDistinctIDs(t *testing.T) {
	first, err := NewMessageEvent("ses_test", "turn_test", RoleUser, TextBlock("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewMessageEvent("ses_test", "turn_test", RoleAssistant, TextBlock("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Timestamp.IsZero() || second.Timestamp.IsZero() {
		t.Fatalf("constructors did not create stable envelope fields: %#v %#v", first, second)
	}
	if first.Origin != OriginUser || second.Origin != OriginModel {
		t.Fatalf("unexpected origins %q %q", first.Origin, second.Origin)
	}
}

func TestPromptIDIsBoundedAndUserOnly(t *testing.T) {
	user := Message{Role: RoleUser, Content: []ContentBlock{TextBlock("run once")}, PromptID: "host-prompt-1"}
	if err := user.Validate(); err != nil {
		t.Fatalf("valid prompt id rejected: %v", err)
	}
	assistant := Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock("done")}, PromptID: "host-prompt-1"}
	if err := assistant.Validate(); err == nil {
		t.Fatal("assistant prompt id was accepted")
	}
	for _, promptID := range []string{"contains space", strings.Repeat("x", 257)} {
		user.PromptID = promptID
		if err := user.Validate(); err == nil {
			t.Fatalf("invalid prompt id %q was accepted", promptID)
		}
	}
}

func TestVisibilityProjection(t *testing.T) {
	tests := []struct {
		value Visibility
		user  bool
		model bool
	}{
		{VisibilityUser, true, false},
		{VisibilityModel, false, true},
		{VisibilityBoth, true, true},
		{VisibilityInternal, false, false},
	}
	for _, test := range tests {
		if test.value.UserVisible() != test.user || test.value.ModelVisible() != test.model {
			t.Fatalf("unexpected projection for %q", test.value)
		}
	}
}

func TestAttachmentMessageIsOrderedAndMetadataOnly(t *testing.T) {
	manifest := attachment.Manifest{
		AttachmentID: "att_0123456789abcdef", Kind: attachment.KindImage,
		Name: "screen.png", MIMEType: attachment.MIMEPNG, SizeBytes: 42,
		SHA256:    strings.Repeat("a", 64),
		StorageID: "blob_sha256_" + strings.Repeat("a", 64),
	}
	message := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			TextBlock("inspect"),
			AttachmentBlock(manifest),
			TextBlock("carefully"),
		},
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := message.Content[1].AttachmentManifest(); got != manifest {
		t.Fatalf("attachment manifest = %#v, want %#v", got, manifest)
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"base64", "file://", "/tmp/"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("metadata-only message contains %q: %s", forbidden, data)
		}
	}

	attachmentOnly := Message{Role: RoleUser, Content: []ContentBlock{AttachmentBlock(manifest)}}
	if err := attachmentOnly.Validate(); err != nil {
		t.Fatalf("attachment-only Validate() error = %v", err)
	}
}

func TestAttachmentMessageRejectsInvalidUnionAndDuplicates(t *testing.T) {
	digest := strings.Repeat("b", 64)
	manifest := attachment.Manifest{
		AttachmentID: "att_0123456789abcdef", Kind: attachment.KindDocument,
		Name: "input.pdf", MIMEType: attachment.MIMEPDF, SizeBytes: 100,
		SHA256: digest, StorageID: "blob_sha256_" + digest,
	}
	block := AttachmentBlock(manifest)
	tests := []Message{
		{Role: RoleAssistant, Content: []ContentBlock{block}},
		{Role: RoleUser, Content: []ContentBlock{block, block}},
		{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: "x", AttachmentID: manifest.AttachmentID}}},
	}
	for index, message := range tests {
		if err := message.Validate(); err == nil {
			t.Fatalf("case %d unexpectedly validated", index)
		}
	}
}

func TestProtocolVersionMigrationAcceptsLegacyTextAndRequiresV2ForAttachments(t *testing.T) {
	legacy, err := NewMessageEvent("ses_legacy", "turn_legacy", RoleUser, TextBlock("hello"))
	if err != nil {
		t.Fatal(err)
	}
	legacy.Version = LegacyVersion
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy text event rejected: %v", err)
	}

	digest := strings.Repeat("c", 64)
	media, err := NewMessageEvent(
		"ses_media", "turn_media", RoleUser,
		AttachmentBlock(attachment.Manifest{
			AttachmentID: "att_0123456789abcdef", Kind: attachment.KindImage,
			Name: "screen.png", MIMEType: attachment.MIMEPNG, SizeBytes: 10,
			SHA256: digest, StorageID: "blob_sha256_" + digest,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	media.Version = LegacyVersion
	if err := media.Validate(); err == nil {
		t.Fatal("legacy event version accepted attachment content")
	}
}
