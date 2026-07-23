package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/redact"
)

type ledgerEntry struct {
	done   chan struct{}
	result Result
}

const maximumObserverPayloadBytes = 4 << 20

// Executor applies the common tool protocol and owns the exact-one ledger.
type Executor struct {
	registry    *Registry
	authorizer  Authorizer
	hooks       []Hook
	store       *ResultStore
	progress    func(Progress)
	sanitize    func(string) string
	credentials *redact.Set
	mu          sync.Mutex
	ledger      map[string]*ledgerEntry
}

// ExecutorOptions supplies session-scoped services.
type ExecutorOptions struct {
	Registry    *Registry
	Authorizer  Authorizer
	Hooks       []Hook
	ResultStore *ResultStore
	Progress    func(Progress)
	// Sanitize removes configured credential material before tool text reaches
	// hooks, result persistence, the transcript, or model continuation.
	// Deprecated: exact credentials should use CredentialSanitizer so a tool's
	// source-scoped set can be merged safely.
	Sanitize func(string) string
	// CredentialSanitizer is the session-owned exact-literal set.
	CredentialSanitizer *redact.Set
}

func NewExecutor(options ExecutorOptions) (*Executor, error) {
	if options.Registry == nil {
		return nil, errors.New("tool registry is required")
	}
	if err := validateToolCredentialCompatibility(options.CredentialSanitizer); err != nil {
		return nil, err
	}
	if (options.CredentialSanitizer == nil || options.CredentialSanitizer.Empty()) &&
		options.Sanitize != nil {
		if err := validateToolOpaqueCompatibility(options.Sanitize); err != nil {
			return nil, err
		}
	}
	for _, descriptor := range options.Registry.Descriptors() {
		if err := validateToolCredentialCompatibility(redact.Union(
			options.CredentialSanitizer, descriptor.CredentialSanitizer,
		)); err != nil {
			return nil, err
		}
	}
	return &Executor{
		registry: options.Registry, authorizer: options.Authorizer,
		hooks: append([]Hook(nil), options.Hooks...), store: options.ResultStore,
		progress: options.Progress, sanitize: options.Sanitize,
		credentials: options.CredentialSanitizer, ledger: make(map[string]*ledgerEntry),
	}, nil
}

// Execute returns the same immutable terminal result to duplicate observers of
// one accepted ID and never executes that ID twice.
func (e *Executor) Execute(ctx context.Context, request Request) Result {
	identitySet := e.credentials
	identitySanitize := e.sanitizeText
	identityForceSuppress := false
	resolvedName := ""
	if descriptor, ok := e.registry.Resolve(request.Name); ok {
		identitySet, identitySanitize, identityForceSuppress = e.sanitizerFor(descriptor.CredentialSanitizer)
		resolvedName = descriptor.Name
	}
	if err := credentialSafeToolRoutingIdentity(
		request, resolvedName, identitySet, e.sanitize, identityForceSuppress,
	); err != nil {
		// A rejected routing envelope was never accepted into the exactly-once
		// ledger. Do not echo either untrusted identity: immutable identifiers
		// cannot be rewritten safely while preserving correlation.
		result := Result{
			IsError: true, Code: "structural_invalid",
			Content: "tool routing identity reflected configured credential material",
		}
		return sanitizeResultForScope(result, identitySet, identitySanitize, identityForceSuppress)
	}
	if request.ID == "" {
		result := Result{ToolUseID: request.ID, Name: request.Name, IsError: true, Code: "structural_invalid", Content: "tool-use ID is required", OriginalInput: cloneRaw(request.Input)}
		set := e.credentials
		sanitize := e.sanitizeText
		forceSuppress := false
		if descriptor, ok := e.registry.Resolve(request.Name); ok {
			set, sanitize, forceSuppress = e.sanitizerFor(descriptor.CredentialSanitizer)
		}
		return sanitizeResultForScope(result, set, sanitize, forceSuppress)
	}
	e.mu.Lock()
	if existing, ok := e.ledger[request.ID]; ok {
		e.mu.Unlock()
		<-existing.done
		return cloneResult(existing.result)
	}
	entry := &ledgerEntry{done: make(chan struct{})}
	e.ledger[request.ID] = entry
	e.mu.Unlock()

	result := e.executeOnce(ctx, request)
	e.mu.Lock()
	entry.result = cloneResult(result)
	close(entry.done)
	e.mu.Unlock()
	return result
}

func (e *Executor) executeOnce(ctx context.Context, request Request) (result Result) {
	result = Result{ToolUseID: request.ID, Name: request.Name, OriginalInput: cloneRaw(request.Input)}
	sanitize := e.sanitizeText
	var scopedSet *redact.Set
	if e.credentials != nil && !e.credentials.Empty() {
		scopedSet = e.credentials
		sanitize = scopedSet.Apply
	}
	forceSuppress := false
	defer func() {
		if recover() != nil {
			result.IsError = true
			result.Code = "execution_failed"
			result.Content = "tool implementation panic was contained"
			e.runFailureHooksWith(ctx, request, result, scopedSet, sanitize, forceSuppress)
		}
		result = sanitizeResultForScope(result, scopedSet, sanitize, forceSuppress)
	}()
	descriptor, ok := e.registry.Resolve(request.Name)
	if !ok {
		result.IsError = true
		result.Code = "unknown_tool"
		result.Content = fmt.Sprintf("unknown or disabled tool %q", request.Name)
		return result
	}
	scopedSet, sanitize, forceSuppress = e.sanitizerFor(descriptor.CredentialSanitizer)
	result.Name = descriptor.Name
	if err := ctx.Err(); err != nil {
		return contextCancellationResult(result, ctx)
	}
	raw := cloneRaw(request.Input)
	if err := credentialSafeToolInputForScope(raw, scopedSet, e.sanitize, forceSuppress); err != nil {
		return failedResult(result, "structural_invalid", safeToolErrorText(err))
	}
	if _, err := descriptor.Validate(cloneRaw(raw)); err != nil {
		return failedResult(result, "structural_invalid", "input validation failed: "+safeToolErrorText(err))
	}
	hookAsk := ""
	for _, hook := range e.hooks {
		observedRequest, safe := observerRequest(request, descriptor.Name, scopedSet, sanitize, forceSuppress)
		if !safe {
			result = failedResult(result, "hook_failed", "pre-tool hook request could not be safely projected")
			return result
		}
		hookResult, err := callPreHook(hook, ctx, observedRequest, descriptor.Name)
		if err != nil {
			result = failedResult(result, "hook_failed", "pre-tool hook failed: "+safeToolErrorText(err))
			e.runFailureHooksWith(ctx, request, result, scopedSet, sanitize, forceSuppress)
			return result
		}
		for _, progress := range hookResult.Progress {
			e.publishProgressForScope(progress, request.ID, scopedSet, sanitize, forceSuppress)
		}
		if hookResult.DenyReason != "" {
			reason, suppressed := sanitizeScopedText(hookResult.DenyReason, scopedSet, sanitize)
			result = failedResult(result, "denied", reason)
			result.ContentSuppressed = suppressed
			result.PermissionRejected = true
			result.PermissionInput = cloneRaw(raw)
			e.runPermissionDeniedHooksWith(ctx, request, result, scopedSet, sanitize, forceSuppress)
			return result
		}
		if hookAsk == "" && hookResult.AskReason != "" {
			if forceSuppress {
				result = failedResult(result, "hook_failed", "")
				result.ContentSuppressed = true
				return result
			}
			var suppressed bool
			hookAsk, suppressed = sanitizeScopedText(hookResult.AskReason, scopedSet, sanitize)
			if suppressed {
				result = failedResult(result, "hook_failed", "")
				result.ContentSuppressed = true
				return result
			}
		}
		if len(hookResult.UpdatedInput) > 0 {
			raw = cloneRaw(hookResult.UpdatedInput)
			if err := credentialSafeToolInputForScope(raw, scopedSet, e.sanitize, forceSuppress); err != nil {
				return failedResult(result, "structural_invalid", safeToolErrorText(err))
			}
			if _, err := descriptor.Validate(cloneRaw(raw)); err != nil {
				return failedResult(result, "structural_invalid", "hook-updated input validation failed: "+safeToolErrorText(err))
			}
		}
	}
	build := func(candidate json.RawMessage) (permission.Request, any, error) {
		// Validation, classification, and permission projection are descriptor
		// extension points. Keep their byte and parsed-value aliases away from
		// both the canonical authorization evidence and the value later passed
		// to semantic validation and execution.
		canonical := cloneRaw(candidate)
		if err := credentialSafeToolInputForScope(canonical, scopedSet, e.sanitize, forceSuppress); err != nil {
			return permission.Request{}, nil, err
		}
		classificationInput, err := descriptor.Validate(cloneRaw(canonical))
		if err != nil {
			return permission.Request{}, nil, err
		}
		classification := descriptor.classification(classificationInput)
		projected := permission.Request{
			Tool: descriptor.Name, ToolUseID: request.ID, Input: cloneRaw(canonical),
			Classification: classification,
		}
		if descriptor.ProjectPermission != nil {
			projectionInput, err := descriptor.Validate(cloneRaw(canonical))
			if err != nil {
				return permission.Request{}, nil, err
			}
			projected, err = descriptor.ProjectPermission(projectionInput, cloneRaw(canonical))
			if err != nil {
				return permission.Request{}, nil, err
			}
			projected.Tool = descriptor.Name
			projected.ToolUseID = request.ID
			projected.Input = cloneRaw(canonical)
			projected.Classification = classification
		}
		if err := credentialSafePermissionRequestForScope(projected, scopedSet, e.sanitize, forceSuppress); err != nil {
			return permission.Request{}, nil, err
		}
		executionInput, err := descriptor.Validate(cloneRaw(canonical))
		if err != nil {
			return permission.Request{}, nil, err
		}
		return projected, executionInput, nil
	}
	permissionRequest, input, err := build(raw)
	if err != nil {
		return failedResult(result, "semantic_invalid", "permission projection failed: "+safeToolErrorText(err))
	}
	permissionRequest.HookAsk = hookAsk
	if err := credentialSafePermissionRequestForScope(permissionRequest, scopedSet, e.sanitize, forceSuppress); err != nil {
		return failedResult(result, "semantic_invalid", "permission projection failed: "+safeToolErrorText(err))
	}
	decision := permission.Decision{Kind: permission.DecisionDeny, Reason: "permission service unavailable", Input: raw, OriginalInput: request.Input}
	if e.authorizer != nil {
		decision, err = e.authorizer.Authorize(ctx, permissionRequest, func(updated json.RawMessage) (permission.Request, error) {
			rebuilt, _, err := build(updated)
			rebuilt.HookAsk = hookAsk
			if err == nil {
				err = credentialSafePermissionRequestForScope(rebuilt, scopedSet, e.sanitize, forceSuppress)
			}
			return rebuilt, err
		})
		if err != nil {
			return failedResult(result, "permission_failed", "permission evaluation failed: "+safeToolErrorText(err))
		}
	}
	switch decision.Kind {
	case permission.DecisionDeny:
		result = failedResult(result, "denied", decision.Reason)
		result.PermissionRejected = true
		deniedRequest := request
		if len(decision.Input) > 0 {
			deniedRequest.Input = cloneRaw(decision.Input)
		} else {
			deniedRequest.Input = cloneRaw(raw)
		}
		result.PermissionInput = cloneRaw(deniedRequest.Input)
		e.runPermissionDeniedHooksWith(ctx, deniedRequest, result, scopedSet, sanitize, forceSuppress)
		return result
	case permission.DecisionCancel:
		result = failedResult(result, "cancelled", decision.Reason)
		result.PermissionRejected = true
		if len(decision.Input) > 0 {
			result.PermissionInput = cloneRaw(decision.Input)
		} else {
			result.PermissionInput = cloneRaw(raw)
		}
		return result
	case permission.DecisionAllow:
	default:
		return failedResult(result, "permission_failed", "permission service returned a nonterminal decision")
	}
	selectedRaw := cloneRaw(decision.Input)
	if len(selectedRaw) == 0 {
		selectedRaw = cloneRaw(raw)
	}
	if err := credentialSafeToolInputForScope(selectedRaw, scopedSet, e.sanitize, forceSuppress); err != nil {
		return failedResult(result, "structural_invalid", safeToolErrorText(err))
	}
	if string(selectedRaw) != string(raw) {
		input, err = descriptor.Validate(cloneRaw(selectedRaw))
		if err != nil {
			return failedResult(result, "structural_invalid", "authorized input validation failed: "+safeToolErrorText(err))
		}
	}
	// Filesystem- and task-dependent semantic checks run only after the exact
	// input has been authorized. This prevents protected-resource existence or
	// content oracles before path policy and also hardens edited approvals.
	if descriptor.Semantic != nil {
		if err := descriptor.Semantic(input); err != nil {
			code := "semantic_invalid"
			if typed, ok := err.(*InvocationError); ok && typed != nil && typed.semantic {
				code = canonicalErrorCode(typed.Code, "semantic_invalid")
			}
			return failedResult(result, code, "authorized input semantic validation failed: "+safeToolErrorText(err))
		}
	}
	result.ExecutedInput = cloneRaw(selectedRaw)
	result.UserModified = decision.UserModified
	callContext := CallContext{
		ToolUseID: request.ID, AssistantID: request.AssistantID, OriginalInput: cloneRaw(request.Input),
		ExecutedInput: cloneRaw(selectedRaw), UserModified: decision.UserModified,
		Progress: func(progress Progress) {
			e.publishProgressForScope(progress, request.ID, scopedSet, sanitize, forceSuppress)
		},
	}
	if scopedSet != nil && !scopedSet.Empty() {
		lookahead := scopedSet.MaxLiteralBytes()
		if lookahead > 1 {
			callContext.CredentialLookahead = lookahead - 1
		}
		callContext.ProjectOutput = func(value string, rawTruncated bool, limit int) (string, bool, bool) {
			return projectCredentialOutput(scopedSet, value, rawTruncated, limit)
		}
	}
	output, err := descriptor.Call(ctx, callContext, input)
	if err != nil {
		if ctx.Err() != nil {
			result = contextCancellationResult(result, ctx)
		} else {
			result = failedResult(result, errorCode(err, "execution_failed"), safeToolErrorText(err))
		}
		result.ExecutedInput = cloneRaw(selectedRaw)
		result.UserModified = decision.UserModified
		e.runFailureHooksWith(ctx, request, result, scopedSet, sanitize, forceSuppress)
		return result
	}
	content := output.Content
	if output.ContentSuppressed {
		content = ""
		result.ContentSuppressed = true
	} else if strings.TrimSpace(content) == "" {
		content = fmt.Sprintf("(%s completed with no output)", descriptor.Name)
	}
	if scopedSet != nil {
		var suppressed bool
		content, _, suppressed = scopedSet.RedactBounded(content, maximumPersistedResultBytes)
		if suppressed {
			content = ""
			result.ContentSuppressed = true
		}
	} else {
		hadContent := content != ""
		content = sanitize(content)
		if hadContent && content == "" {
			result.ContentSuppressed = true
		}
	}
	limit := descriptor.MaxResultChars
	if limit == 0 || limit > DefaultResultLimit {
		limit = DefaultResultLimit
	}
	if e.store != nil {
		content = e.store.apply(request.ID, content, limit)
	} else if limit >= 0 && len(content) > limit {
		content = content[:limit] + "\n[tool output truncated because result persistence is unavailable]"
	}
	if scopedSet != nil {
		if e.store == nil && limit >= 0 {
			var suppressed bool
			content, _, suppressed = scopedSet.RedactBounded(content, limit)
			if suppressed {
				content = ""
				result.ContentSuppressed = true
			}
		} else {
			content = scopedSet.Apply(content)
		}
	} else {
		hadContent := content != ""
		content = sanitize(content)
		if hadContent && content == "" {
			result.ContentSuppressed = true
		}
	}
	result.Content = content
	result.Metadata = output.Metadata
	result = sanitizeResultForScope(result, scopedSet, sanitize, forceSuppress)
	for _, hook := range e.hooks {
		if result.observerSuppressed {
			break
		}
		observedRequest, safe := observerRequest(request, result.Name, scopedSet, sanitize, forceSuppress)
		if !safe {
			break
		}
		if err := callPostHook(hook, ctx, observedRequest, cloneResult(result)); err != nil {
			// The capability effect already happened. A post-hook failure is
			// diagnostic and must never rewrite earned success into failure.
			if result.Metadata == nil {
				result.Metadata = make(map[string]any)
			}
			warnings, _ := result.Metadata["post_hook_warnings"].([]string)
			message, suppressed := boundedScopedDiagnostic(safeToolErrorText(err), 2000, scopedSet, sanitize, forceSuppress)
			if !suppressed && message != "" {
				result.Metadata["post_hook_warnings"] = append(warnings, message)
			}
			// The next observer and the returned result must never see an
			// unsanitized warning emitted by a preceding post hook.
			result = sanitizeResultForScope(result, scopedSet, sanitize, forceSuppress)
		}
	}
	return sanitizeResultForScope(result, scopedSet, sanitize, forceSuppress)
}

func (e *Executor) runPermissionDeniedHooks(ctx context.Context, request Request, result Result) {
	e.runPermissionDeniedHooksWith(ctx, request, result, e.credentials, e.sanitizeText, false)
}

func (e *Executor) runPermissionDeniedHooksWith(ctx context.Context, request Request, result Result, set *redact.Set, sanitize func(string) string, forceSuppress bool) {
	result = sanitizeResultForScope(result, set, sanitize, forceSuppress)
	if result.observerSuppressed {
		return
	}
	var safe bool
	request, safe = observerRequest(request, result.Name, set, sanitize, forceSuppress)
	if !safe {
		return
	}
	for _, hook := range e.hooks {
		if observer, ok := hook.(PermissionDeniedHook); ok {
			callPermissionDeniedHook(observer, ctx, cloneRequest(request), cloneResult(result))
		}
	}
}

func (e *Executor) runFailureHooks(ctx context.Context, request Request, result Result) {
	e.runFailureHooksWith(ctx, request, result, e.credentials, e.sanitizeText, false)
}

func (e *Executor) runFailureHooksWith(ctx context.Context, request Request, result Result, set *redact.Set, sanitize func(string) string, forceSuppress bool) {
	result = sanitizeResultForScope(result, set, sanitize, forceSuppress)
	if result.observerSuppressed {
		return
	}
	var safe bool
	request, safe = observerRequest(request, result.Name, set, sanitize, forceSuppress)
	if !safe {
		return
	}
	for _, hook := range e.hooks {
		callFailureHook(hook, ctx, cloneRequest(request), cloneResult(result))
	}
}

func callPreHook(hook Hook, ctx context.Context, request Request, name string) (result HookResult, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("pre-tool hook panicked")
		}
	}()
	return hook.Pre(ctx, request, name)
}

func callPostHook(hook Hook, ctx context.Context, request Request, result Result) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("post-tool hook panicked")
		}
	}()
	return hook.Post(ctx, request, result)
}

func callFailureHook(hook Hook, ctx context.Context, request Request, result Result) {
	defer func() { _ = recover() }()
	_ = hook.Failure(ctx, request, result)
}

func callPermissionDeniedHook(hook PermissionDeniedHook, ctx context.Context, request Request, result Result) {
	defer func() { _ = recover() }()
	_ = hook.PermissionDenied(ctx, request, result)
}

func (e *Executor) sanitizeText(value string) string {
	if e.credentials != nil && !e.credentials.Empty() {
		return e.credentials.Apply(value)
	}
	return safeSanitizeText(value, e.sanitize)
}

func (e *Executor) sanitizerFor(source *redact.Set) (*redact.Set, func(string) string, bool) {
	hasSource := source != nil && !source.Empty()
	hasSession := e.credentials != nil && !e.credentials.Empty()
	switch {
	case hasSession:
		union := redact.Union(e.credentials, source)
		return union, union.Apply, false
	case hasSource && e.sanitize != nil:
		// An opaque callback cannot be unioned with source-owned literals.
		// Retain source semantic checks, but suppress every result payload field
		// rather than chain marker strategies that can recreate credentials.
		// Keep the original callback solely for complete-frame validation after
		// suppression; forceSuppress prevents it from transforming payloads.
		return source, e.sanitize, true
	case hasSource:
		return source, source.Apply, false
	default:
		return nil, e.sanitizeText, false
	}
}

func sanitizeResultForScope(result Result, set *redact.Set, sanitize func(string) string, forceSuppress bool) Result {
	if set != nil {
		result = sanitizeResultWithSet(result, set)
	} else {
		result = sanitizeResultWith(result, sanitize)
	}
	if forceSuppress {
		result.Content = ""
		result.ContentSuppressed = true
		result.OriginalInput = nil
		result.ExecutedInput = nil
		result.PermissionInput = nil
		result.Metadata = nil
	}
	if credentialSafeProjection(result, set, sanitize, forceSuppress) {
		// The accepted routing identity was preflighted with this exact fixed
		// fallback before execution, so an effectful call remains correlated
		// even when every extension-controlled terminal field is quarantined.
		fallback := terminalSuppressionResult(result.ToolUseID, result.Name)
		if credentialSafeProjection(fallback, set, sanitize, forceSuppress) {
			// A stateful legacy callback can violate its own preflight result.
			// Security wins over correlation in that untrusted, deprecated seam.
			return Result{}
		}
		return fallback
	}
	return result
}

func sanitizeScopedText(value string, set *redact.Set, sanitize func(string) string) (string, bool) {
	if set != nil {
		return set.Redact(value)
	}
	return safeSanitizeText(value, sanitize), false
}

func boundedScopedDiagnostic(value string, limit int, set *redact.Set, sanitize func(string) string, forceSuppress bool) (string, bool) {
	if forceSuppress || limit < 0 {
		return "", true
	}
	if set != nil {
		safe, _, suppressed := set.RedactBounded(value, limit)
		return safe, suppressed
	}
	safe := safeSanitizeText(value, sanitize)
	if value != "" && safe == "" {
		return "", true
	}
	// An opaque sanitizer provides no set-safe truncation marker. Sanitize the
	// complete value first, then omit an oversized diagnostic rather than cut a
	// replacement into a credential-reconstructing boundary.
	if len(safe) > limit {
		return "", true
	}
	return safe, false
}

func observerRequest(request Request, canonicalName string, set *redact.Set, sanitize func(string) string, forceSuppress bool) (Request, bool) {
	request = cloneRequest(request)
	if canonicalName != "" {
		request.Name = canonicalName
	}
	if set != nil {
		request.projectObserverPayload = func(raw []byte) ([]byte, error) {
			return set.JSONBounded(raw, maximumObserverPayloadBytes)
		}
	}
	if forceSuppress {
		request.Input = nil
		request.AssistantID = ""
		return request, !credentialSafeProjection(request, set, sanitize, forceSuppress)
	}
	if set != nil {
		request.Input = sanitizeRawWithSet(request.Input, set)
		assistantID, suppressed := set.Redact(request.AssistantID)
		if suppressed {
			assistantID = ""
		}
		request.AssistantID = assistantID
		return request, !credentialSafeProjection(request, set, sanitize, forceSuppress)
	}
	request.Input = sanitizeRawWith(request.Input, sanitize)
	request.AssistantID = safeSanitizeText(request.AssistantID, sanitize)
	return request, !credentialSafeProjection(request, set, sanitize, forceSuppress)
}

func safeSanitizeText(value string, sanitize func(string) string) string {
	if sanitize == nil || value == "" {
		return value
	}
	// An opaque sanitizer cannot prove that any fixed fallback is distinct from
	// its hidden sensitive set. Empty is the only fail-closed result if it
	// panics; result callers propagate that as explicit content suppression.
	result := ""
	func() {
		defer func() { _ = recover() }()
		result = sanitize(value)
	}()
	return result
}

func (e *Executor) sanitizeResult(result Result) Result {
	return sanitizeResultWith(result, e.sanitizeText)
}

func sanitizeResultWith(result Result, sanitize func(string) string) Result {
	if result.Code != "" {
		result.Code = canonicalErrorCode(result.Code, "execution_failed")
	}
	hadContent := result.Content != ""
	result.Content = safeSanitizeText(result.Content, sanitize)
	if hadContent && result.Content == "" {
		result.ContentSuppressed = true
	}
	result.OriginalInput = sanitizeRawWith(result.OriginalInput, sanitize)
	result.ExecutedInput = sanitizeRawWith(result.ExecutedInput, sanitize)
	result.PermissionInput = sanitizeRawWith(result.PermissionInput, sanitize)
	if result.Metadata != nil {
		result.Metadata = sanitizeMetadataWith(result.Metadata, sanitize)
	}
	return result
}

func sanitizeResultWithSet(result Result, sanitize *redact.Set) Result {
	if result.Code != "" {
		result.Code = canonicalErrorCode(result.Code, "execution_failed")
	}
	hadContent := result.Content != ""
	result.Content = sanitize.Apply(result.Content)
	if hadContent && result.Content == "" {
		result.ContentSuppressed = true
	}
	result.OriginalInput = sanitizeRawWithSet(result.OriginalInput, sanitize)
	result.ExecutedInput = sanitizeRawWithSet(result.ExecutedInput, sanitize)
	result.PermissionInput = sanitizeRawWithSet(result.PermissionInput, sanitize)
	if result.Metadata != nil {
		result.Metadata, _ = sanitizeMetadataWithSet(result.Metadata, sanitize)
	}
	encoded, err := json.Marshal(result)
	reflected := false
	if err == nil {
		reflected, err = sanitize.JSONContains(encoded)
	}
	if err != nil || reflected || len(encoded) > maximumObserverPayloadBytes {
		// Per-field projection cannot prove safety for a credential reconstructed
		// by later JSON framing. JSONBounded is a transforming operation, not a
		// validator, so a successful redacted projection cannot prove that the
		// original Result is safe to return.
		result.Content = ""
		result.ContentSuppressed = true
		result.Code = ""
		result.OriginalInput = nil
		result.ExecutedInput = nil
		result.PermissionInput = nil
		result.Metadata = nil
		result.observerSuppressed = true
	}
	identity := struct {
		ToolUseID string `json:"tool_use_id"`
		Name      string `json:"name"`
	}{ToolUseID: result.ToolUseID, Name: result.Name}
	if credentialSafeProjection(identity, sanitize, nil, false) {
		result.ToolUseID = ""
		result.Name = ""
		result.observerSuppressed = true
	}
	return result
}

func terminalSuppressionResult(toolUseID, name string) Result {
	return Result{
		ToolUseID: toolUseID, Name: name,
		ContentSuppressed: true, IsError: true, Code: "execution_failed",
		observerSuppressed: true,
	}
}

func credentialSafeToolRoutingIdentity(request Request, resolvedName string, set *redact.Set, opaque func(string) string, forceSuppress bool) error {
	names := []string{request.Name}
	if resolvedName != "" && resolvedName != request.Name {
		names = append(names, resolvedName)
	}
	for _, name := range names {
		projection := struct {
			ToolUseID string `json:"tool_use_id"`
			Name      string `json:"name"`
		}{ToolUseID: request.ID, Name: name}
		if credentialSafeProjection(projection, set, opaque, forceSuppress) {
			return errors.New("tool routing identity reflected configured credential material")
		}
		if credentialSafeProjection(
			terminalSuppressionResult(request.ID, name),
			set, opaque, forceSuppress,
		) {
			return errors.New("tool routing identity is incompatible with terminal result framing")
		}
	}
	return nil
}

func validateToolCredentialCompatibility(set *redact.Set) error {
	if set == nil || set.Empty() {
		return nil
	}
	return validateToolFrameCompatibility(set, nil)
}

func validateToolOpaqueCompatibility(sanitize func(string) string) error {
	if sanitize == nil {
		return nil
	}
	return validateToolFrameCompatibility(nil, sanitize)
}

func validateToolFrameCompatibility(set *redact.Set, sanitize func(string) string) error {
	frames := []any{
		Result{},
		Result{IsError: true},
		Progress{},
		map[string]any{"value": nil},
	}
	for _, frame := range frames {
		if credentialSafeProjection(frame, set, sanitize, false) {
			return errors.New("tool credential set is incompatible with mandatory protocol framing")
		}
	}
	return nil
}

func credentialSafePermissionRequestForScope(request permission.Request, set *redact.Set, opaque func(string) string, forceSuppress bool) error {
	if credentialSafeProjection(request, set, opaque, forceSuppress) {
		return errors.New("permission projection reflected configured credential material")
	}
	return nil
}

func credentialSafeProjection(value any, set *redact.Set, opaque func(string) string, forceSuppress bool) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return true
	}
	if set != nil && !set.Empty() {
		reflected, inspectErr := set.JSONContains(encoded)
		if inspectErr != nil || reflected {
			return true
		}
	}
	if opaque == nil {
		return false
	}
	if safeSanitizeText(string(encoded), opaque) != string(encoded) {
		return true
	}
	canonical, err := redact.New().JSON(encoded)
	if err != nil || safeSanitizeText(string(canonical), opaque) != string(canonical) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return true
	}
	return opaqueJSONValueChanged(decoded, opaque)
}

func credentialSafeToolInput(raw json.RawMessage, sanitize *redact.Set) error {
	if sanitize == nil || sanitize.Empty() || len(raw) == 0 {
		return nil
	}
	canonical, err := redact.New().JSON(raw)
	if err != nil {
		return errors.New("tool input is not valid JSON")
	}
	safe, err := sanitize.JSON(raw)
	if err != nil || string(safe) != string(canonical) {
		return errors.New("tool input reflected configured credential material")
	}
	return nil
}

func credentialSafeToolInputForScope(raw json.RawMessage, set *redact.Set, opaque func(string) string, forceSuppress bool) error {
	if err := credentialSafeToolInput(raw, set); err != nil {
		return err
	}
	if opaque == nil || len(raw) == 0 {
		return nil
	}
	canonical, err := redact.New().JSON(raw)
	if err != nil {
		return errors.New("tool input is not valid JSON")
	}
	if safeSanitizeText(string(canonical), opaque) != string(canonical) {
		return errors.New("tool input reflected configured credential material")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		opaqueJSONValueChanged(value, opaque) {
		return errors.New("tool input reflected configured credential material")
	}
	return nil
}

func opaqueJSONValueChanged(value any, sanitize func(string) string) bool {
	changed := func(value string) bool { return safeSanitizeText(value, sanitize) != value }
	switch typed := value.(type) {
	case string:
		return changed(typed)
	case map[string]any:
		for key, child := range typed {
			if changed(key) || opaqueJSONValueChanged(child, sanitize) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if opaqueJSONValueChanged(child, sanitize) {
				return true
			}
		}
	case nil:
		return changed("null")
	case bool:
		if typed {
			return changed("true")
		}
		return changed("false")
	case json.Number:
		return changed(typed.String())
	default:
		return true
	}
	return false
}

func projectCredentialOutput(sanitize *redact.Set, value string, rawTruncated bool, limit int) (string, bool, bool) {
	safe, projected, suppressed := sanitize.RedactBounded(value, limit)
	if suppressed {
		return "", true, true
	}
	if rawTruncated && !projected {
		marker := sanitize.TerminalMarker()
		if marker == "" {
			return "", true, true
		}
		safe += marker
	}
	return safe, rawTruncated || projected, false
}

func sanitizeRawWithSet(raw json.RawMessage, sanitize *redact.Set) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	canonical, err := redact.New().JSON(raw)
	if err != nil {
		return nil
	}
	safe, err := sanitize.JSON(raw)
	if err != nil {
		return nil
	}
	if bytes.Equal(safe, canonical) {
		return cloneRaw(raw)
	}
	return json.RawMessage(safe)
}

const maximumMetadataProjectionDepth = 64

func sanitizeMetadataWithSet(metadata map[string]any, sanitize *redact.Set) (map[string]any, bool) {
	return sanitizeMetadataMapWithSet(metadata, sanitize, 0)
}

func sanitizeMetadataMapWithSet(metadata map[string]any, sanitize *redact.Set, depth int) (map[string]any, bool) {
	if depth > maximumMetadataProjectionDepth {
		return nil, true
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		safeKey, suppressed := sanitize.Redact(key)
		if suppressed {
			return nil, true
		}
		if _, exists := result[safeKey]; exists {
			return nil, true
		}
		safeValue, suppressed := sanitizeMetadataValueWithSet(value, sanitize, depth+1)
		if suppressed {
			return nil, true
		}
		result[safeKey] = safeValue
	}
	return result, false
}

func sanitizeMetadataValueWithSet(value any, sanitize *redact.Set, depth int) (any, bool) {
	if depth > maximumMetadataProjectionDepth {
		return nil, true
	}
	switch typed := value.(type) {
	case string:
		return sanitize.Redact(typed)
	case []string:
		result := make([]string, len(typed))
		for index, child := range typed {
			safe, suppressed := sanitize.Redact(child)
			if suppressed {
				return nil, true
			}
			result[index] = safe
		}
		return result, false
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, child := range typed {
			safeKey, suppressed := sanitize.Redact(key)
			if suppressed {
				return nil, true
			}
			safeValue, suppressed := sanitize.Redact(child)
			if suppressed {
				return nil, true
			}
			if _, exists := result[safeKey]; exists {
				return nil, true
			}
			result[safeKey] = safeValue
		}
		return result, false
	case map[string]any:
		return sanitizeMetadataMapWithSet(typed, sanitize, depth)
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			safe, suppressed := sanitizeMetadataValueWithSet(child, sanitize, depth+1)
			if suppressed {
				return nil, true
			}
			result[index] = safe
		}
		return result, false
	case json.RawMessage:
		safe, err := sanitize.JSON(typed)
		if err != nil {
			return nil, true
		}
		return json.RawMessage(safe), false
	case []byte:
		safe, suppressed := sanitize.Redact(string(typed))
		return []byte(safe), suppressed
	case nil:
		return nil, sanitize.Contains("null")
	case bool:
		spelling := "false"
		if typed {
			spelling = "true"
		}
		return typed, sanitize.Contains(spelling)
	case json.Number:
		return typed, sanitize.Contains(typed.String())
	default:
		// Metadata is an extension boundary: named maps, structs, aliases, and
		// custom marshalers must not retain an opaque object that a later hook
		// or serializer can expand into credential-bearing JSON.
		encoded, err := marshalMetadataJSON(typed)
		if err != nil {
			return nil, true
		}
		safe, err := sanitize.JSON(encoded)
		if err != nil {
			return nil, true
		}
		decoder := json.NewDecoder(bytes.NewReader(safe))
		decoder.UseNumber()
		var projected any
		if err := decoder.Decode(&projected); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, true
		}
		return projected, false
	}
}

func marshalMetadataJSON(value any) (encoded []byte, err error) {
	defer func() {
		if recover() != nil {
			encoded = nil
			err = errors.New("metadata JSON projection panicked")
		}
	}()
	return json.Marshal(value)
}

func (e *Executor) sanitizeRaw(raw json.RawMessage) json.RawMessage {
	return sanitizeRawWith(raw, e.sanitizeText)
}

func sanitizeRawWith(raw json.RawMessage, sanitize func(string) string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(safeSanitizeText(string(raw), sanitize))
}

func (e *Executor) sanitizeMetadata(metadata map[string]any) map[string]any {
	return sanitizeMetadataWith(metadata, e.sanitizeText)
}

func sanitizeMetadataWith(metadata map[string]any, sanitize func(string) string) map[string]any {
	return sanitizeMetadataMapWith(metadata, sanitize, 0)
}

func sanitizeMetadataMapWith(metadata map[string]any, sanitize func(string) string, depth int) map[string]any {
	if depth > maximumMetadataProjectionDepth {
		return nil
	}
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		result[safeSanitizeText(key, sanitize)] = sanitizeMetadataValueWith(value, sanitize, depth+1)
	}
	return result
}

func (e *Executor) sanitizeMetadataValue(value any) any {
	return sanitizeMetadataValueWith(value, e.sanitizeText, 0)
}

func sanitizeMetadataValueWith(value any, sanitize func(string) string, depth int) any {
	if depth > maximumMetadataProjectionDepth {
		return nil
	}
	switch typed := value.(type) {
	case string:
		return safeSanitizeText(typed, sanitize)
	case []string:
		result := make([]string, len(typed))
		for index, child := range typed {
			result[index] = safeSanitizeText(child, sanitize)
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, child := range typed {
			result[safeSanitizeText(key, sanitize)] = safeSanitizeText(child, sanitize)
		}
		return result
	case map[string]any:
		return sanitizeMetadataMapWith(typed, sanitize, depth)
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = sanitizeMetadataValueWith(child, sanitize, depth+1)
		}
		return result
	case json.RawMessage:
		// An opaque callback cannot prove semantic safety across alternate JSON
		// wire spellings. Drop the value instead of forwarding raw bytes.
		return nil
	case []byte:
		return []byte(safeSanitizeText(string(typed), sanitize))
	default:
		// Unknown structs, named aliases, custom marshalers, and scalar types may
		// expand into credential-bearing output later. Exact-set mode projects
		// them semantically; legacy opaque mode has no safe composition proof.
		return nil
	}
}

func (e *Executor) publishProgressForScope(progress Progress, toolUseID string, set *redact.Set, sanitize func(string) string, forceSuppress bool) {
	if e.progress == nil || progress.Percent < 0 || progress.Percent > 100 {
		return
	}
	progress.ToolUseID = toolUseID
	if set != nil {
		message, suppressed := set.Redact(progress.Message)
		if suppressed {
			return
		}
		progress.Message = message
	} else {
		progress.Message = safeSanitizeText(progress.Message, sanitize)
	}
	if forceSuppress || credentialSafeProjection(progress, set, sanitize, forceSuppress) {
		return
	}
	callProgressSink(e.progress, progress)
}

func callProgressSink(sink func(Progress), progress Progress) {
	defer func() { _ = recover() }()
	sink(progress)
}

func failedResult(result Result, code, content string) Result {
	result.IsError = true
	result.Code = canonicalErrorCode(code, "execution_failed")
	result.Content = content
	return result
}

func cancelledResult(result Result, err error) Result {
	return failedResult(result, "cancelled", "tool invocation cancelled: "+safeToolErrorText(err))
}

func contextCancellationResult(result Result, ctx context.Context) Result {
	if exactToolError(context.Cause(ctx), errSiblingCapabilityFailed) {
		return siblingErrorResult(result.ToolUseID, result.Name)
	}
	return cancelledResult(result, ctx.Err())
}

func cloneRaw(raw json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), raw...) }

func cloneResult(result Result) Result {
	result.OriginalInput = cloneRaw(result.OriginalInput)
	result.ExecutedInput = cloneRaw(result.ExecutedInput)
	result.PermissionInput = cloneRaw(result.PermissionInput)
	if result.Metadata != nil {
		result.Metadata = cloneMap(result.Metadata)
	}
	return result
}

func cloneRequest(request Request) Request {
	request.Input = cloneRaw(request.Input)
	return request
}
