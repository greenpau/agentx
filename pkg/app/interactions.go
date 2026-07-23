package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/surface"
	"github.com/greenpau/agentx/pkg/tool"
)

type terminalInteractions struct {
	operationMu  sync.Mutex
	stateMu      sync.RWMutex
	writerActive atomic.Bool
	reader       *linePump
	out          io.Writer
	guard        *redact.Set
}

var errTerminalInteractionWriterActive = errors.New("terminal interaction writer callback is active")

func newTerminalInteractions(reader io.Reader, out io.Writer) *terminalInteractions {
	return &terminalInteractions{reader: newLinePump(reader), out: out}
}

func (t *terminalInteractions) SetCredentialSanitizer(credentials *redact.Set) {
	t.stateMu.Lock()
	t.guard = credentials
	t.stateMu.Unlock()
}

func (t *terminalInteractions) writeRecord(value string) error {
	t.stateMu.RLock()
	out, guard := t.out, t.guard
	t.stateMu.RUnlock()
	if !t.writerActive.CompareAndSwap(false, true) {
		return errTerminalInteractionWriterActive
	}
	defer t.writerActive.Store(false)
	return writeTerminalRecord(out, guard, value)
}

func (t *terminalInteractions) Approve(ctx context.Context, request permission.ApprovalRequest) (permission.ApprovalResponse, error) {
	if t.writerActive.Load() {
		return permission.ApprovalResponse{}, errTerminalInteractionWriterActive
	}
	t.operationMu.Lock()
	defer t.operationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return permission.ApprovalResponse{}, err
	}
	prompt := fmt.Sprintf("\nPermission required for %s: %s\nInput: %s\nAllow? [y]es/[n]o/[e]dit JSON: ", request.Tool, request.Reason, string(request.Input))
	if err := t.writeRecord(prompt); err != nil {
		return permission.ApprovalResponse{}, err
	}
	answer, err := t.reader.Next(ctx)
	if err != nil {
		return permission.ApprovalResponse{}, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return permission.ApprovalResponse{Kind: permission.DecisionAllow}, nil
	case "e", "edit":
		if err := t.writeRecord("Complete replacement JSON: "); err != nil {
			return permission.ApprovalResponse{}, err
		}
		updated, err := t.reader.Next(ctx)
		if err != nil {
			return permission.ApprovalResponse{}, err
		}
		updated = strings.TrimSpace(updated)
		if !json.Valid([]byte(updated)) {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "replacement was not valid JSON"}, nil
		}
		return permission.ApprovalResponse{Kind: permission.DecisionAllow, UpdatedInput: json.RawMessage(updated)}, nil
	default:
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "user denied permission"}, nil
	}
}

func (t *terminalInteractions) Ask(ctx context.Context, questions []tool.Question) (map[string][]string, error) {
	if t.writerActive.Load() {
		return nil, errTerminalInteractionWriterActive
	}
	t.operationMu.Lock()
	defer t.operationMu.Unlock()
	answers := make(map[string][]string, len(questions))
	for _, question := range questions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var prompt strings.Builder
		fmt.Fprintf(&prompt, "\n%s — %s\n", question.Header, question.Question)
		for index, option := range question.Options {
			fmt.Fprintf(&prompt, "  %d) %s — %s\n", index+1, option.Label, option.Description)
		}
		prompt.WriteString("Answer (number, label, or free text): ")
		if err := t.writeRecord(prompt.String()); err != nil {
			return nil, err
		}
		answer, err := t.reader.Next(ctx)
		if err != nil {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return nil, errors.New("question cancelled")
		}
		selected := answer
		for index, option := range question.Options {
			if answer == fmt.Sprint(index+1) || strings.EqualFold(answer, option.Label) {
				selected = option.Label
				break
			}
		}
		answers[question.Question] = []string{selected}
	}
	return answers, nil
}

func (t *terminalInteractions) ReadLine(ctx context.Context, prompt string) (string, error) {
	if t.writerActive.Load() {
		return "", errTerminalInteractionWriterActive
	}
	t.operationMu.Lock()
	defer t.operationMu.Unlock()
	if err := t.writeRecord(prompt); err != nil {
		return "", err
	}
	line, err := t.reader.Next(ctx)
	if err != nil && len(line) > 0 {
		return strings.TrimRight(line, "\r\n"), nil
	}
	return strings.TrimRight(line, "\r\n"), err
}

func (t *terminalInteractions) Close() { t.reader.Close() }

type structuredInteractions struct {
	broker    *surface.ControlBroker
	encoder   *surface.Encoder
	sessionID string
}

func (s *structuredInteractions) Approve(ctx context.Context, request permission.ApprovalRequest) (permission.ApprovalResponse, error) {
	if err := ctx.Err(); err != nil {
		return permission.ApprovalResponse{}, err
	}
	var input map[string]any
	if err := json.Unmarshal(request.Input, &input); err != nil || input == nil {
		return permission.ApprovalResponse{}, errors.New("permission request input must be an object")
	}
	fields := map[string]any{
		"tool_name": request.Tool, "input": input, "tool_use_id": request.ToolUseID,
		"description": "Allow " + request.Tool + "?",
	}
	if request.MatchedRule == "" && strings.TrimSpace(request.Reason) != "" {
		fields["decision_reason"] = request.Reason
	}
	wireRequest, err := surface.NewControlRequest("can_use_tool", fields)
	if err != nil {
		return permission.ApprovalResponse{}, err
	}
	if err := encodeSessionState(s.encoder, protocol.SessionID(s.sessionID), "requires_action"); err != nil {
		return permission.ApprovalResponse{}, err
	}
	defer func() {
		if s.broker.Pending() == 0 {
			_ = encodeSessionState(s.encoder, protocol.SessionID(s.sessionID), "running")
		}
	}()
	response, err := s.broker.Request(ctx, wireRequest, func(event surface.OutputEnvelope) error { return s.encoder.Encode(event) })
	if err != nil {
		// A local turn interruption is a cancellation outcome, not a remote
		// permission denial. Propagate it so the evaluator and tool settlement
		// produce a cancelled terminal result. Transport/protocol failures while
		// the turn remains live still fail closed as denials below.
		if ctx.Err() != nil {
			return permission.ApprovalResponse{}, ctx.Err()
		}
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: " + err.Error()}, nil
	}
	if response.Subtype == "error" {
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: " + fallback(response.Error, "host returned a protocol error")}, nil
	}
	return decodePermissionDecision(response.Response)
}

func (s *structuredInteractions) Ask(ctx context.Context, questions []tool.Question) (map[string][]string, error) {
	request, err := surface.NewControlRequest("ask_user_question", map[string]any{"questions": questions})
	if err != nil {
		return nil, err
	}
	if err := encodeSessionState(s.encoder, protocol.SessionID(s.sessionID), "requires_action"); err != nil {
		return nil, err
	}
	defer func() {
		if s.broker.Pending() == 0 {
			_ = encodeSessionState(s.encoder, protocol.SessionID(s.sessionID), "running")
		}
	}()
	response, err := s.broker.Request(ctx, request, func(event surface.OutputEnvelope) error { return s.encoder.Encode(event) })
	if err != nil {
		return nil, err
	}
	if response.Subtype == "error" {
		return nil, errors.New(fallback(response.Error, "question denied"))
	}
	var answers map[string][]string
	if err := json.Unmarshal(response.Response, &answers); err != nil {
		return nil, fmt.Errorf("decode question response: %w", err)
	}
	return answers, nil
}

func decodePermissionDecision(raw json.RawMessage) (permission.ApprovalResponse, error) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: invalid permission response"}, nil
	}
	var behavior string
	if json.Unmarshal(fields["behavior"], &behavior) != nil {
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: invalid permission behavior"}, nil
	}
	if value, present := fields["toolUseID"]; present {
		var toolUseID string
		if string(value) == "null" || json.Unmarshal(value, &toolUseID) != nil {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: toolUseID must be a string"}, nil
		}
	}
	if classification, present := fields["decisionClassification"]; present && string(classification) == "null" {
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: decisionClassification must not be null"}, nil
	}
	if classification, present := fields["decisionClassification"]; present {
		var value string
		if json.Unmarshal(classification, &value) != nil || value != "user_temporary" && value != "user_permanent" && value != "user_reject" {
			// Compatibility behavior: invalid telemetry classification is absent.
			delete(fields, "decisionClassification")
		}
	}
	switch behavior {
	case "allow":
		updated, present := fields["updatedInput"]
		if !present || string(updated) == "null" {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: allow requires updatedInput"}, nil
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(updated, &object) != nil || object == nil {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: updatedInput must be an object"}, nil
		}
		if updates, present := fields["updatedPermissions"]; present {
			if string(updates) == "null" {
				return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: updatedPermissions must not be null"}, nil
			}
			valid, nonempty := validPermissionUpdates(updates)
			if valid && nonempty {
				return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: permission updates are unavailable in this runtime profile"}, nil
			}
			// A malformed update array is ignored by the compatibility contract.
		}
		if len(object) == 0 {
			updated = nil
		}
		return permission.ApprovalResponse{Kind: permission.DecisionAllow, UpdatedInput: updated}, nil
	case "deny":
		var message string
		if json.Unmarshal(fields["message"], &message) != nil || strings.TrimSpace(message) == "" {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: deny requires message"}, nil
		}
		interrupt := false
		if value, present := fields["interrupt"]; present {
			if string(value) == "null" || json.Unmarshal(value, &interrupt) != nil {
				return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: interrupt must be boolean"}, nil
			}
		}
		if interrupt {
			return permission.ApprovalResponse{Kind: permission.DecisionCancel, Reason: message}, nil
		}
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: message}, nil
	default:
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "Tool permission request failed: invalid permission behavior"}, nil
	}
}

func validPermissionUpdates(raw json.RawMessage) (valid, nonempty bool) {
	var updates []map[string]json.RawMessage
	if json.Unmarshal(raw, &updates) != nil || updates == nil {
		return false, false
	}
	validDestination := func(raw json.RawMessage) bool {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
		switch value {
		case "userSettings", "projectSettings", "localSettings", "session", "cliArg":
			return true
		default:
			return false
		}
	}
	for _, update := range updates {
		var kind string
		if json.Unmarshal(update["type"], &kind) != nil || !validDestination(update["destination"]) {
			return false, false
		}
		switch kind {
		case "addRules", "replaceRules", "removeRules":
			var behavior string
			if json.Unmarshal(update["behavior"], &behavior) != nil || behavior != "allow" && behavior != "deny" && behavior != "ask" {
				return false, false
			}
			var rules []map[string]json.RawMessage
			if json.Unmarshal(update["rules"], &rules) != nil || rules == nil {
				return false, false
			}
			for _, rule := range rules {
				var toolName string
				if json.Unmarshal(rule["toolName"], &toolName) != nil {
					return false, false
				}
				if content, present := rule["ruleContent"]; present {
					var value string
					if string(content) == "null" || json.Unmarshal(content, &value) != nil {
						return false, false
					}
				}
			}
		case "setMode":
			var mode string
			if json.Unmarshal(update["mode"], &mode) != nil || mode != "default" && mode != "acceptEdits" && mode != "bypassPermissions" && mode != "plan" && mode != "dontAsk" {
				return false, false
			}
		case "addDirectories", "removeDirectories":
			var directories []string
			if json.Unmarshal(update["directories"], &directories) != nil || directories == nil {
				return false, false
			}
		default:
			return false, false
		}
	}
	return true, len(updates) > 0
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}
