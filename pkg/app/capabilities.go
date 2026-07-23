package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/tool"
)

type capabilityAdapter struct {
	registry    *tool.Registry
	scheduler   *tool.Scheduler
	scope       *skillPermissionScope
	credentials *redact.Set
}

// skillPermissionScope is a turn-local narrowing layer. It can only deny; it
// never grants authority that the composed permission evaluator refused.
type skillPermissionScope struct {
	mu          sync.RWMutex
	active      bool
	rules       []permission.Rule
	parseErrors []string
}

func (s *skillPermissionScope) Reset() {
	s.mu.Lock()
	s.active = false
	s.rules = nil
	s.parseErrors = nil
	s.mu.Unlock()
}

func (s *skillPermissionScope) Install(rawRules []string) error {
	parsed := make([]permission.Rule, 0, len(rawRules))
	var parseErrors []string
	for _, raw := range rawRules {
		rule, err := permission.ParseRule(raw, permission.EffectAllow, "skill_scope", false)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("%q: %v", raw, err))
			continue
		}
		parsed = append(parsed, rule)
	}
	s.mu.Lock()
	s.active = len(rawRules) > 0
	s.rules = parsed
	s.parseErrors = parseErrors
	s.mu.Unlock()
	if len(parseErrors) > 0 {
		return fmt.Errorf("invalid skill allowed-tools rule: %s", strings.Join(parseErrors, "; "))
	}
	return nil
}

func (s *skillPermissionScope) Allows(request permission.Request) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.active || permission.CanonicalTool(request.Tool) == "Skill" {
		return true
	}
	if len(s.parseErrors) > 0 || len(s.rules) == 0 {
		return false
	}
	// A whole-tool rule, or an exact content rule matching the complete input,
	// authorizes the narrowed scope in one step.
	for _, rule := range s.rules {
		if rule.Pattern == "" && rule.Matches(request.Tool, request.Content) {
			return true
		}
		if rule.ExactPattern() && rule.Matches(request.Tool, request.Content) {
			return true
		}
	}
	// Compound shell requests must have every independently executable segment
	// covered; matching only one segment cannot expand the active skill scope.
	if request.Shell != nil && len(request.Shell.Segments) > 0 {
		for index, segment := range request.Shell.Segments {
			candidate := segment
			if index < len(request.Shell.AllowCandidates) {
				candidate = request.Shell.AllowCandidates[index]
			}
			if !matchesAnyScopeRule(s.rules, request.Tool, candidate) {
				return false
			}
		}
		return true
	}
	candidates := append([]string{request.Content}, request.MatchContents...)
	for _, candidate := range candidates {
		if matchesAnyScopeRule(s.rules, request.Tool, candidate) {
			return true
		}
	}
	return false
}

func matchesAnyScopeRule(rules []permission.Rule, tool, content string) bool {
	for _, rule := range rules {
		if rule.Matches(tool, content) {
			return true
		}
	}
	return false
}

type scopedAuthorizer struct {
	base  tool.Authorizer
	scope *skillPermissionScope
}

func (a scopedAuthorizer) Authorize(ctx context.Context, request permission.Request, rebuild permission.Rebuild) (permission.Decision, error) {
	if a.scope != nil && !a.scope.Allows(request) {
		return permission.Decision{Kind: permission.DecisionDeny, Reason: "active skill scope does not allow this tool", Source: "skill_scope", Input: request.Input, OriginalInput: request.Input}, nil
	}
	return a.base.Authorize(ctx, request, rebuild)
}

type skillInput struct {
	Skill     string   `json:"skill"`
	Arguments []string `json:"arguments,omitempty"`
}

func skillDescriptor(snapshot extensions.Snapshot, scope *skillPermissionScope, fixedModel, fixedEffort string) tool.Descriptor {
	return tool.Descriptor{
		Name: "Skill", Source: tool.SourceBuiltin, Description: "Load one discovered skill's instructions with literal arguments.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"skill": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"skill"}},
		Validate: func(raw json.RawMessage) (any, error) {
			var input skillInput
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				return nil, err
			}
			if strings.TrimSpace(input.Skill) == "" {
				return nil, errors.New("skill is required")
			}
			if _, ok := snapshot.Lookup(input.Skill, true); !ok {
				return nil, errors.New("skill is unknown, unavailable, or disabled for model invocation")
			}
			skill, _ := snapshot.Lookup(input.Skill, true)
			if skill.Model != "" && !strings.EqualFold(skill.Model, fixedModel) {
				return nil, fmt.Errorf("skill requires model %q but this session is fixed to %q", skill.Model, fixedModel)
			}
			if skill.Effort != "" && !strings.EqualFold(skill.Effort, fixedEffort) {
				return nil, fmt.Errorf("skill requires reasoning effort %q but this turn uses %q", skill.Effort, fixedEffort)
			}
			if skill.Agent != "" || strings.EqualFold(skill.Context, "fork") {
				return nil, errors.New("skill requires an isolated agent context, unavailable in this local placement")
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			// Skill installation is a scheduling barrier so a newly narrowed tool
			// scope applies before any later call in the same provider batch.
			return permission.Classification{ReadOnly: true}
		},
		Call: func(_ context.Context, _ tool.CallContext, value any) (tool.Output, error) {
			input := value.(skillInput)
			skill, _ := snapshot.Lookup(input.Skill, true)
			content := extensions.Expand(skill, input.Arguments)
			if len(skill.AllowedTools) > 0 {
				if err := scope.Install(skill.AllowedTools); err != nil {
					return tool.Output{}, err
				}
				content = "Temporary enforced skill tool scope: " + strings.Join(skill.AllowedTools, ", ") + ".\n\n" + content
			} else {
				// A skill invocation replaces, rather than composes with, the prior
				// invocation's temporary tool scope. Without this reset, an
				// unrestricted skill selected after a restricted one would inherit
				// permissions it did not declare.
				scope.Reset()
			}
			return tool.Output{Content: content, Metadata: map[string]any{"skill": skill.CanonicalName, "source": skill.Source, "generation": snapshot.Generation}}, nil
		},
		MaxResultChars: 100_000,
	}
}

func (a *capabilityAdapter) Schemas() []model.Tool {
	descriptors := a.registry.Descriptors()
	result := make([]model.Tool, 0, len(descriptors))
	for _, descriptor := range descriptors {
		schema, err := json.Marshal(descriptor.InputSchema)
		if err != nil {
			continue
		}
		// Azure strict mode additionally requires every property to be listed
		// as required. Core schemas intentionally expose optional fields, while
		// local structural validation remains strict and authoritative.
		if a.credentials != nil && !a.credentials.Empty() {
			// Names are provider/registry routing identities and therefore cannot
			// be rewritten. A contributed descriptor that reflects a configured
			// credential anywhere in its identity or documentation is omitted.
			if a.credentials.Contains(descriptor.Name) || a.credentials.Contains(descriptor.Description) {
				continue
			}
			reflected, inspectionErr := a.credentials.JSONContains(schema)
			if inspectionErr != nil || reflected {
				continue
			}
		}
		candidate := model.Tool{Name: descriptor.Name, Description: descriptor.Description, Parameters: schema, Strict: false}
		if a.credentials != nil && !a.credentials.Empty() {
			encoded, encodeErr := json.Marshal(candidate)
			if encodeErr != nil {
				continue
			}
			reflected, inspectionErr := a.credentials.JSONContains(encoded)
			if inspectionErr != nil || reflected {
				continue
			}
		}
		result = append(result, candidate)
	}
	return result
}

func (a *capabilityAdapter) Execute(ctx context.Context, calls []engine.CapabilityCall) []engine.CapabilityResult {
	requests := make([]tool.Request, 0, len(calls))
	for _, call := range calls {
		requests = append(requests, tool.Request{ID: string(call.ID), Name: call.Name, Input: append(json.RawMessage(nil), call.Arguments...), AssistantID: call.ProviderItemID})
	}
	toolResults := a.scheduler.Execute(ctx, requests)
	results := make([]engine.CapabilityResult, 0, len(toolResults))
	for _, result := range toolResults {
		status := protocol.ToolResultSuccess
		if result.IsError {
			status = statusForToolCode(result.Code)
		}
		var denial *engine.PermissionDenial
		if result.PermissionRejected {
			input := result.PermissionInput
			if len(input) == 0 {
				input = result.OriginalInput
			}
			denial = &engine.PermissionDenial{
				ToolName: result.Name, ToolUseID: protocol.ToolUseID(result.ToolUseID),
				ToolInput: append(json.RawMessage(nil), input...),
			}
		}
		results = append(results, engine.CapabilityResult{
			ID: protocol.ToolUseID(result.ToolUseID), Name: result.Name, Status: status,
			Content: result.Content, ContentSuppressed: result.ContentSuppressed,
			Code: normalizeErrorCode(result.Code), IsError: result.IsError, PermissionDenial: denial,
		})
	}
	return results
}

func statusForToolCode(code string) protocol.ToolResultStatus {
	switch code {
	case "denied", "permission_denied":
		return protocol.ToolResultDenied
	case "structural_invalid", "semantic_invalid", "malformed_input", "malformed_result":
		return protocol.ToolResultMalformed
	case "cancelled", "user_interrupted", "sibling_error":
		return protocol.ToolResultCancelled
	case "timeout":
		return protocol.ToolResultTimedOut
	case "unknown_tool", "unavailable":
		return protocol.ToolResultUnavailable
	case "interrupted":
		return protocol.ToolResultInterrupted
	default:
		return protocol.ToolResultError
	}
}

func normalizeErrorCode(code string) string {
	switch code {
	case "call_batch_interrupted", "cancelled", "denied", "execution_failed",
		"hook_failed", "interrupted", "malformed_input", "malformed_result",
		"missing_terminal_result", "permission_denied", "permission_failed",
		"semantic_invalid", "sibling_error", "stale_file", "structural_invalid",
		"timeout", "unavailable", "unknown_tool", "user_interrupted":
		return code
	}
	return "tool_error"
}
