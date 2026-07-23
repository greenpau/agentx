package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

const defaultEditCycles = 2

// Config is normalized before constructing an Evaluator.
type Config struct {
	Workspace        string
	AdditionalRoots  []string
	ProtectedPaths   []string
	Mode             Mode
	Rules            []Rule
	Approver         Approver
	PromptSuppressed bool
	BypassAvailable  bool
	MaxEditCycles    int
}

// Evaluator is immutable after construction and safe for concurrent calls.
type Evaluator struct {
	mode             Mode
	rules            []Rule
	approver         Approver
	promptSuppressed bool
	bypassAvailable  bool
	maxEditCycles    int
	paths            *Resolver
}

// NewEvaluator validates a permission generation.
func NewEvaluator(config Config) (*Evaluator, error) {
	if len(config.Rules) > maximumPermissionRules {
		return nil, errors.New("permission rule count exceeds its limit")
	}
	for _, rule := range config.Rules {
		if !validConfiguredRule(rule) {
			return nil, errors.New("invalid configured permission rule")
		}
	}
	paths, err := NewResolver(config.Workspace, config.AdditionalRoots, config.ProtectedPaths...)
	if err != nil {
		return nil, err
	}
	mode := config.Mode
	if mode == "" {
		mode = ModeDefault
	}
	if mode != ModeDefault && mode != ModeAcceptEdits && mode != ModePlan && mode != ModeDontAsk && mode != ModeBypass {
		return nil, errors.New("unsupported permission mode")
	}
	if mode == ModeBypass && !config.BypassAvailable {
		return nil, errors.New("bypass permissions mode is unavailable")
	}
	maxEdits := config.MaxEditCycles
	if maxEdits <= 0 {
		maxEdits = defaultEditCycles
	}
	if maxEdits > maximumPermissionEditCycles {
		return nil, errors.New("permission edit-cycle limit exceeds its maximum")
	}
	return &Evaluator{
		mode: mode, rules: append([]Rule(nil), config.Rules...), approver: config.Approver,
		promptSuppressed: config.PromptSuppressed, bypassAvailable: config.BypassAvailable,
		maxEditCycles: maxEdits, paths: paths,
	}, nil
}

// Authorize runs deny-first composed policy and, if input is edited, mandates a
// bounded validate-and-reauthorize loop. This is an intentional hardened
// divergence from the recovered one-shot edited-approval compatibility gap.
func (e *Evaluator) Authorize(ctx context.Context, initial Request, rebuild Rebuild) (Decision, error) {
	if e == nil {
		return Decision{}, errors.New("permission evaluator is unavailable")
	}
	if ctx == nil {
		return Decision{}, errors.New("permission context is nil")
	}
	if ctx.Err() != nil {
		return Decision{Kind: DecisionCancel, Reason: "permission request cancelled"}, nil
	}
	if !validPermissionRequest(initial) {
		return Decision{}, errors.New("invalid permission request projection")
	}
	current := cloneRequest(initial)
	original := append(json.RawMessage(nil), current.Input...)
	for edits := 0; ; {
		if err := ctx.Err(); err != nil {
			return Decision{Kind: DecisionCancel, Reason: "permission request cancelled", OriginalInput: original, Input: append(json.RawMessage(nil), current.Input...), EditCycles: edits}, nil
		}
		decision := e.evaluate(current)
		decision.OriginalInput = original
		decision.Input = append(json.RawMessage(nil), current.Input...)
		decision.EditCycles = edits
		if decision.Kind != DecisionAsk {
			decision.UserModified = edits > 0
			return decision, nil
		}
		if e.mode == ModeDontAsk || e.promptSuppressed || e.approver == nil {
			decision.Kind = DecisionDeny
			decision.Source = "mode"
			if e.mode == ModeDontAsk {
				decision.Reason = "permission prompt required but dontAsk mode is active"
			} else {
				decision.Reason = "permission prompt required but no approval surface is available"
			}
			return decision, nil
		}
		response, err := callApprover(e.approver, ctx, ApprovalRequest{
			Tool: current.Tool, ToolUseID: current.ToolUseID, Input: append(json.RawMessage(nil), current.Input...),
			Reason: decision.Reason, Mandatory: current.MandatoryAsk != "" || current.Classification.Interaction,
			MatchedRule: decision.MatchedRule,
		})
		if err != nil {
			if ctx.Err() != nil {
				return Decision{Kind: DecisionCancel, Reason: "permission request cancelled", OriginalInput: original, Input: append(json.RawMessage(nil), current.Input...), EditCycles: edits}, nil
			}
			// Approval transports are external boundaries. Their error object,
			// unwrap chain, and panic payload are not safe public diagnostics.
			return Decision{}, errors.New("permission approval failed")
		}
		switch response.Kind {
		case DecisionDeny:
			return Decision{Kind: DecisionDeny, Reason: fallback(response.Reason, "user denied tool use"), Source: "user", OriginalInput: original, Input: append(json.RawMessage(nil), current.Input...), EditCycles: edits}, nil
		case DecisionCancel:
			return Decision{Kind: DecisionCancel, Reason: fallback(response.Reason, "approval dismissed"), Source: "user", OriginalInput: original, Input: append(json.RawMessage(nil), current.Input...), EditCycles: edits}, nil
		case DecisionAllow:
		default:
			return Decision{}, errors.New("invalid permission approval response")
		}
		if len(response.UpdatedInput) == 0 || jsonEqual(response.UpdatedInput, current.Input) {
			return Decision{Kind: DecisionAllow, Reason: fallback(response.Reason, "approved once"), Source: "user", OriginalInput: original, Input: append(json.RawMessage(nil), current.Input...), UserModified: edits > 0, EditCycles: edits}, nil
		}
		if rebuild == nil {
			return Decision{Kind: DecisionDeny, Reason: "edited input cannot be revalidated", Source: "hardened_revalidation", OriginalInput: original, Input: current.Input, EditCycles: edits}, nil
		}
		edits++
		if edits > e.maxEditCycles {
			return Decision{Kind: DecisionDeny, Reason: "edited-input approval cycle limit reached", Source: "hardened_revalidation", OriginalInput: original, Input: current.Input, UserModified: true, EditCycles: edits}, nil
		}
		rebuilt, err := callRebuild(rebuild, response.UpdatedInput)
		if err != nil {
			return Decision{Kind: DecisionDeny, Reason: "edited input failed revalidation", Source: "hardened_revalidation", OriginalInput: original, Input: append(json.RawMessage(nil), response.UpdatedInput...), UserModified: true, EditCycles: edits}, nil
		}
		current = cloneRequest(rebuilt)
	}
}

func (e *Evaluator) evaluate(request Request) Decision {
	if request.HardDeny != "" {
		return Decision{Kind: DecisionDeny, Reason: request.HardDeny, Source: "tool_safety"}
	}
	denyContents := append([]string{request.Content}, request.MatchContents...)
	denyContents = append(denyContents, request.DenyContents...)
	allowContents := append([]string{request.Content}, request.AllowContents...)
	if rule := e.firstRule(EffectDeny, request.Tool, denyContents); rule != nil {
		return Decision{Kind: DecisionDeny, Reason: "request matches a deny rule", Source: rule.Source, MatchedRule: rule.String()}
	}
	// Plan mode is an execution boundary, not a prompt default. Reject every
	// mutation before ask rules, path prompts, mandatory interaction, or user
	// approval can authorize it. The same check runs after edited-input rebuild.
	if e.mode == ModePlan && requestMutates(request) {
		return Decision{Kind: DecisionDeny, Reason: "plan mode forbids mutating tool use", Source: "mode"}
	}
	askRule := e.firstRule(EffectAsk, request.Tool, denyContents)
	pathAsk := ""
	pathSafetyAsk := false
	acceptsFileEdits := e.mode == ModeAcceptEdits && isCanonicalFileEditTool(request.Tool)
	for _, access := range request.Paths {
		pathDecision := e.paths.Inspect(access.Path, access.Operation, acceptsFileEdits)
		if pathDecision.Kind == DecisionDeny {
			return Decision{Kind: DecisionDeny, Reason: pathDecision.Reason, Source: "path"}
		}
		if pathDecision.Kind == DecisionAsk && pathAsk == "" {
			pathAsk = pathDecision.Reason + ": " + access.Path
			// Protected resources are a safety boundary rather than an
			// ordinary unresolved permission. They must still reach an
			// approval surface in bypass mode; an in-scope mutation or an
			// otherwise unruled path may fall through to bypass/allow below.
			pathSafetyAsk = pathDecision.Protected
		} else if pathDecision.Kind == DecisionAsk && pathDecision.Protected {
			// Preserve safety precedence when an earlier ordinary path ask
			// was followed by a protected path in the same request.
			pathAsk = pathDecision.Reason + ": " + access.Path
			pathSafetyAsk = true
		}
	}
	if askRule != nil {
		return Decision{Kind: DecisionAsk, Reason: "request matches an ask rule", Source: askRule.Source, MatchedRule: askRule.String()}
	}
	if pathSafetyAsk {
		return Decision{Kind: DecisionAsk, Reason: pathAsk, Source: "path"}
	}
	if request.Shell != nil {
		for _, target := range request.Shell.RemovalTargets {
			if DangerousRemoval(target, e.paths.workspace, e.paths.roots...) {
				return Decision{Kind: DecisionAsk, Reason: "removal targets an approved working-directory boundary or its ancestor", Source: "shell_safety"}
			}
		}
		if request.Shell.Dangerous {
			return Decision{Kind: DecisionAsk, Reason: request.Shell.DangerReason, Source: "shell_safety"}
		}
		if request.Shell.RequiresReview {
			return Decision{Kind: DecisionAsk, Reason: request.Shell.ReviewReason, Source: "shell_safety"}
		}
	}
	if request.MandatoryAsk != "" {
		return Decision{Kind: DecisionAsk, Reason: request.MandatoryAsk, Source: "mandatory_interaction"}
	}
	if request.HookAsk != "" {
		return Decision{Kind: DecisionAsk, Reason: request.HookAsk, Source: "hook"}
	}
	if request.Classification.Interaction {
		return Decision{Kind: DecisionAsk, Reason: "tool requires synchronous user interaction", Source: "mandatory_interaction"}
	}
	if e.mode == ModeBypass && e.bypassAvailable {
		return Decision{Kind: DecisionAllow, Reason: "bypass mode allows request after mandatory safety checks", Source: "mode"}
	}
	if rule := e.allowRule(request, allowContents); rule != nil {
		return Decision{Kind: DecisionAllow, Reason: "request matches an allow rule", Source: rule.Source, MatchedRule: rule.String()}
	}
	if request.Classification.ReadOnly && !request.Classification.OpenWorld {
		return Decision{Kind: DecisionAllow, Reason: "validated local read-only operation", Source: "classification"}
	}
	if acceptsFileEdits && !request.Classification.Destructive && !request.Classification.OpenWorld && len(request.Paths) > 0 {
		allWrites := true
		for _, access := range request.Paths {
			if access.Operation != PathWrite {
				allWrites = false
				break
			}
		}
		if allWrites {
			return Decision{Kind: DecisionAllow, Reason: "acceptEdits allows in-scope file mutation", Source: "mode"}
		}
	}
	if pathAsk != "" {
		return Decision{Kind: DecisionAsk, Reason: pathAsk, Source: "path"}
	}
	return Decision{Kind: DecisionAsk, Reason: "no rule or safe mode authorizes this request", Source: "default"}
}

func cloneRequest(request Request) Request {
	request.Input = append(json.RawMessage(nil), request.Input...)
	request.MatchContents = append([]string(nil), request.MatchContents...)
	request.DenyContents = append([]string(nil), request.DenyContents...)
	request.AllowContents = append([]string(nil), request.AllowContents...)
	request.Paths = append([]PathAccess(nil), request.Paths...)
	if request.Shell != nil {
		shell := *request.Shell
		shell.Segments = append([]string(nil), shell.Segments...)
		shell.DenyCandidates = append([]string(nil), shell.DenyCandidates...)
		shell.AllowCandidates = append([]string(nil), shell.AllowCandidates...)
		shell.Paths = append([]PathAccess(nil), shell.Paths...)
		shell.RemovalTargets = append([]string(nil), shell.RemovalTargets...)
		request.Shell = &shell
	}
	return request
}

func callApprover(approver Approver, ctx context.Context, request ApprovalRequest) (response ApprovalResponse, err error) {
	defer func() {
		if recover() != nil {
			response = ApprovalResponse{}
			err = errors.New("permission approval failed")
		}
	}()
	response, err = approver(ctx, request)
	if err != nil {
		return ApprovalResponse{}, errors.New("permission approval failed")
	}
	if len(response.UpdatedInput) > maximumPermissionInputBytes ||
		len(response.Reason) > maximumPermissionTextBytes {
		return ApprovalResponse{}, errors.New("permission approval failed")
	}
	response.UpdatedInput = append(json.RawMessage(nil), response.UpdatedInput...)
	return response, nil
}

func callRebuild(rebuild Rebuild, input json.RawMessage) (request Request, err error) {
	defer func() {
		if recover() != nil {
			request = Request{}
			err = errors.New("permission input rebuild failed")
		}
	}()
	request, err = rebuild(append(json.RawMessage(nil), input...))
	if err != nil {
		return Request{}, errors.New("permission input rebuild failed")
	}
	if !validPermissionRequest(request) {
		return Request{}, errors.New("permission input rebuild failed")
	}
	return cloneRequest(request), nil
}

// isCanonicalFileEditTool deliberately names the small built-in capability
// boundary whose validated inputs have file-edit semantics. Permission modes
// must not infer that an arbitrary tool is an edit merely because it projects
// write paths: Bash, MCP tools, plugins, and future general tools can have
// materially broader effects than those paths describe.
func isCanonicalFileEditTool(tool string) bool {
	switch CanonicalTool(tool) {
	case "Write", "Edit":
		return true
	default:
		return false
	}
}

func requestMutates(request Request) bool {
	if !request.Classification.ReadOnly || request.Classification.Destructive {
		return true
	}
	if request.Shell != nil && !request.Shell.ReadOnly {
		return true
	}
	for _, access := range request.Paths {
		if access.Operation == PathWrite {
			return true
		}
	}
	return false
}

func (e *Evaluator) allowRule(request Request, contents []string) *Rule {
	// A whole-tool rule or a pattern matching the exact complete command may
	// deliberately authorize the entire compound request.
	for i := range e.rules {
		rule := &e.rules[i]
		if rule.Effect == EffectAllow && (rule.Pattern == "" || isExactCommandPattern(rule.Pattern)) && rule.matches(request.Tool, []string{request.Content}) {
			return rule
		}
	}
	if request.Shell == nil || len(request.Shell.Segments) == 0 {
		return e.firstRule(EffectAllow, request.Tool, contents)
	}
	var first *Rule
	for index, segment := range request.Shell.Segments {
		candidate := segment
		if index < len(request.Shell.AllowCandidates) {
			candidate = request.Shell.AllowCandidates[index]
		}
		matched := e.firstRule(EffectAllow, request.Tool, []string{candidate})
		if matched == nil {
			return nil
		}
		if first == nil {
			first = matched
		}
	}
	return first
}

func (e *Evaluator) firstRule(effect Effect, tool string, contents []string) *Rule {
	for i := range e.rules {
		if e.rules[i].Effect == effect && e.rules[i].matches(tool, contents) {
			return &e.rules[i]
		}
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func fallback(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}
