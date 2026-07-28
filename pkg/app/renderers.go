package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/surface"
)

type discardSink struct{}

func (discardSink) Publish(context.Context, protocol.Event) error { return nil }

type interactiveSink struct {
	mu           sync.Mutex
	credentialMu sync.RWMutex
	writerActive atomic.Bool
	out          io.Writer
	streamed     map[protocol.TurnID]bool
	credentials  *redact.Set
	redactors    map[protocol.TurnID]*redact.SetStream
	writeErr     error
}

var errInteractiveSinkWriterActive = errors.New("interactive sink writer callback is active")

func newInteractiveSink(out io.Writer) *interactiveSink {
	return &interactiveSink{
		out: out, streamed: make(map[protocol.TurnID]bool),
		redactors: make(map[protocol.TurnID]*redact.SetStream),
	}
}
func (s *interactiveSink) SetCredentialSanitizer(credentials *redact.Set) {
	s.credentialMu.Lock()
	s.credentials = credentials
	s.credentialMu.Unlock()
}
func (s *interactiveSink) Publish(_ context.Context, event protocol.Event) error {
	if event.Kind != protocol.EventKindProgress || event.Progress == nil || event.Progress.Phase != "model_text" {
		return nil
	}
	if s.writerActive.Load() {
		return errInteractiveSinkWriterActive
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.streamed[event.TurnID] = true
	safe := TerminalSafeText(event.Progress.Message)
	s.credentialMu.RLock()
	credentials := s.credentials
	s.credentialMu.RUnlock()
	if credentials != nil && !credentials.Empty() {
		stream := s.redactors[event.TurnID]
		if stream == nil {
			stream = redact.NewSetStream(credentials)
			s.redactors[event.TurnID] = stream
		}
		safe = stream.Write(safe)
	}
	if err := s.write(safe); err != nil {
		s.writeErr = err
		return err
	}
	return nil
}
func (s *interactiveSink) finish(outcome engine.Outcome) error {
	if s.writerActive.Load() {
		return errInteractiveSinkWriterActive
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if !s.streamed[outcome.TurnID] && outcome.Text != "" {
		s.credentialMu.RLock()
		credentials := s.credentials
		s.credentialMu.RUnlock()
		safe, err := terminalRecord(outcome.Text+"\n", credentials)
		if err != nil {
			return err
		}
		if err := s.write(safe); err != nil {
			s.writeErr = err
			return err
		}
	} else if s.streamed[outcome.TurnID] {
		value := "\n"
		if stream := s.redactors[outcome.TurnID]; stream != nil {
			value = stream.Write(value) + stream.Flush()
		}
		if err := s.write(value); err != nil {
			s.writeErr = err
			return err
		}
	}
	delete(s.streamed, outcome.TurnID)
	delete(s.redactors, outcome.TurnID)
	return nil
}

func (s *interactiveSink) write(value string) error {
	if !s.writerActive.CompareAndSwap(false, true) {
		return errInteractiveSinkWriterActive
	}
	defer s.writerActive.Store(false)
	return writeStringExact(s.out, value)
}

type streamSink struct {
	encoder            *surface.Encoder
	includePartial     bool
	replayUserMessages bool
	model              string
}

func (s *streamSink) Publish(_ context.Context, event protocol.Event) error {
	if !event.Visibility.UserVisible() {
		return nil
	}
	uuid, err := surface.NewUUID()
	if err != nil {
		return err
	}
	base := map[string]any{"session_id": event.SessionID, "uuid": uuid}
	switch event.Kind {
	case protocol.EventKindMessage:
		if event.Message == nil {
			return nil
		}
		content := apiTextContent(event.Message.Content)
		switch event.Message.Role {
		case protocol.RoleUser:
			if !s.replayUserMessages {
				return nil
			}
			base["type"] = "user"
			message := map[string]any{"role": "user", "content": content}
			if messageContentHasAttachments(event.Message.Content) {
				message["content_version"] = attachment.ProtocolVersion
			}
			base["message"] = message
			base["parent_tool_use_id"] = nil
			base["isReplay"] = true
			if event.Message.PromptID != "" {
				base["uuid"] = event.Message.PromptID
			}
			return s.encoder.Encode(base)
		case protocol.RoleAssistant:
			base["type"] = "assistant"
			messageID := event.Message.APIMessageID
			if messageID == "" {
				messageID = string(event.ID)
			}
			message := map[string]any{
				"id": messageID, "type": "message", "role": "assistant",
				"model": s.model, "content": content, "stop_reason": nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
			}
			if event.Message.Phase != "" {
				message["phase"] = event.Message.Phase
			}
			base["message"] = message
			base["parent_tool_use_id"] = nil
			return s.encoder.Encode(base)
		default:
			return nil
		}
	case protocol.EventKindToolCall:
		if event.ToolCall == nil {
			return nil
		}
		input := any(map[string]any{})
		raw := event.ToolCall.Arguments
		if event.ToolCall.RawArguments != nil {
			raw = json.RawMessage(*event.ToolCall.RawArguments)
		}
		if len(raw) != 0 {
			var decoded any
			if json.Unmarshal(raw, &decoded) == nil {
				input = decoded
			}
		}
		base["type"] = "assistant"
		base["parent_tool_use_id"] = nil
		base["message"] = map[string]any{
			"id": string(event.ID), "type": "message", "role": "assistant", "model": s.model,
			"content":     []map[string]any{{"type": "tool_use", "id": event.ToolCall.ID, "name": event.ToolCall.Name, "input": input}},
			"stop_reason": "tool_use", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		}
		return s.encoder.Encode(base)
	case protocol.EventKindToolResult:
		if event.ToolResult == nil {
			return nil
		}
		base["type"] = "user"
		base["parent_tool_use_id"] = nil
		base["isSynthetic"] = true
		base["message"] = map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type": "tool_result", "tool_use_id": event.ToolResult.ToolUseID,
				"content": apiTextContent(event.ToolResult.Content), "is_error": event.ToolResult.IsError,
			}},
		}
		return s.encoder.Encode(base)
	case protocol.EventKindProgress:
		if event.Progress == nil || !s.includePartial {
			return nil
		}
		if event.Progress.Phase == "model_text" {
			base["type"] = "stream_event"
			base["parent_tool_use_id"] = nil
			// The configured model speaks the Azure/OpenAI Responses stream.
			// The semantic engine intentionally does not retain raw provider
			// frames, so this is the smallest faithful normalized raw event.
			base["event"] = map[string]any{"type": "response.output_text.delta", "delta": event.Progress.Message}
			return s.encoder.Encode(base)
		}
		base["type"] = "tool_progress"
		base["tool_use_id"] = event.Progress.ToolUseID
		base["tool_name"] = event.Progress.Phase
		base["parent_tool_use_id"] = nil
		base["elapsed_time_seconds"] = float64(event.Progress.ElapsedMillis) / 1000
		return s.encoder.Encode(base)
	case protocol.EventKindDiagnostic:
		// There is no public SDK diagnostic discriminator in this protocol
		// version. Diagnostics remain on stderr rather than inventing a system
		// subtype that closed-schema consumers would reject.
		return nil
	case protocol.EventKindCompaction:
		if event.Compaction == nil || event.Compaction.State != "completed" {
			return nil
		}
		base["type"] = "system"
		base["subtype"] = "compact_boundary"
		base["compact_metadata"] = map[string]any{"trigger": event.Compaction.Trigger, "pre_tokens": event.Compaction.PreTokens}
		return s.encoder.Encode(base)
	case protocol.EventKindUsage, protocol.EventKindTurnResult, protocol.EventKindSessionMetadata:
		// Usage and terminal state are projected once by the result envelope;
		// metadata is either represented in init or remains internal.
		return nil
	default:
		return nil
	}
}

func apiTextContent(blocks []protocol.ContentBlock) []map[string]any {
	content := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case protocol.ContentText, protocol.ContentReasoning:
			content = append(content, map[string]any{"type": "text", "text": block.Text})
		case protocol.ContentAttachment:
			content = append(content, map[string]any{
				"type":          "attachment_ref",
				"attachment_id": block.AttachmentID,
				"kind":          block.Kind,
				"name":          block.Name,
				"mime_type":     block.MIMEType,
				"size_bytes":    block.SizeBytes,
				"sha256":        block.SHA256,
				"storage_id":    block.StorageID,
			})
		}
	}
	return content
}

func messageContentHasAttachments(blocks []protocol.ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == protocol.ContentAttachment {
			return true
		}
	}
	return false
}

func writeJSONResult(writer io.Writer, outcome engine.Outcome, runErr error, credentialSets ...*redact.Set) error {
	result, err := sdkResultRecord(outcome, runErr)
	if err != nil {
		return err
	}
	encoder := surface.NewEncoder(writer)
	if len(credentialSets) > 0 && credentialSets[0] != nil {
		if err := encoder.SetValidator(credentialJSONValidator(credentialSets[0])); err != nil {
			return err
		}
	}
	return encoder.Encode(result)
}
