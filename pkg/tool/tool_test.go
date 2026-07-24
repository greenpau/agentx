package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/task"
)

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
	return permission.Decision{Kind: permission.DecisionAllow, Input: request.Input, OriginalInput: request.Input, Reason: "test"}, nil
}

type countingAuthorizer struct {
	calls    atomic.Int32
	decision permission.Decision
}

type authorizerFunc func(context.Context, permission.Request, permission.Rebuild) (permission.Decision, error)

func (authorize authorizerFunc) Authorize(ctx context.Context, request permission.Request, rebuild permission.Rebuild) (permission.Decision, error) {
	return authorize(ctx, request, rebuild)
}

type staticInputUpdateHook struct {
	updated json.RawMessage
}

type countingHook struct {
	pre func(context.Context, Request, string) (HookResult, error)
}

func (hook *countingHook) Pre(ctx context.Context, request Request, name string) (HookResult, error) {
	if hook.pre == nil {
		return HookResult{}, nil
	}
	return hook.pre(ctx, request, name)
}
func (*countingHook) Post(context.Context, Request, Result) error    { return nil }
func (*countingHook) Failure(context.Context, Request, Result) error { return nil }

func (hook *staticInputUpdateHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{UpdatedInput: hook.updated}, nil
}
func (*staticInputUpdateHook) Post(context.Context, Request, Result) error    { return nil }
func (*staticInputUpdateHook) Failure(context.Context, Request, Result) error { return nil }

type postFailureHook struct{}

func (postFailureHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{}, nil
}
func (postFailureHook) Post(context.Context, Request, Result) error {
	return errors.New("post sink unavailable")
}
func (postFailureHook) Failure(context.Context, Request, Result) error { return nil }

type panicHook struct {
	pre      bool
	post     bool
	failure  bool
	denial   bool
	payload  any
	failures atomic.Int32
	denials  atomic.Int32
}

type panickingFormatPanicPayload struct {
	calls  *atomic.Int32
	secret string
}

func (payload *panickingFormatPanicPayload) String() string {
	payload.calls.Add(1)
	panic("panic payload String must not be called: " + payload.secret)
}

func (payload *panickingFormatPanicPayload) Format(fmt.State, rune) {
	payload.calls.Add(1)
	panic("panic payload Format must not be called: " + payload.secret)
}

func (h *panicHook) panic(value string) {
	if h.payload != nil {
		panic(h.payload)
	}
	panic(value)
}

type recordingShellRunner struct {
	calls atomic.Int32
}

func (runner *recordingShellRunner) Command(ctx context.Context, program string, arguments ...string) *exec.Cmd {
	runner.calls.Add(1)
	return exec.CommandContext(ctx, program, arguments...)
}

func (h *panicHook) Pre(context.Context, Request, string) (HookResult, error) {
	if h.pre {
		h.panic("pre hook panic")
	}
	return HookResult{}, nil
}

func (h *panicHook) Post(context.Context, Request, Result) error {
	if h.post {
		h.panic("post hook panic")
	}
	return nil
}

func (h *panicHook) Failure(context.Context, Request, Result) error {
	h.failures.Add(1)
	if h.failure {
		h.panic("failure hook panic")
	}
	return nil
}

func (h *panicHook) PermissionDenied(context.Context, Request, Result) error {
	h.denials.Add(1)
	if h.denial {
		h.panic("denial hook panic")
	}
	return nil
}

type resultCaptureHook struct {
	post    string
	failure string
}

type resultPayloadCaptureHook struct {
	postRequest    Request
	postResult     Result
	failureRequest Request
	failureResult  Result
	postErr        error
}

type denialPayloadCaptureHook struct {
	request Request
	result  Result
}

func (*denialPayloadCaptureHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{}, nil
}
func (*denialPayloadCaptureHook) Post(context.Context, Request, Result) error    { return nil }
func (*denialPayloadCaptureHook) Failure(context.Context, Request, Result) error { return nil }
func (hook *denialPayloadCaptureHook) PermissionDenied(_ context.Context, request Request, result Result) error {
	hook.request, hook.result = request, result
	return nil
}

func (*resultPayloadCaptureHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{}, nil
}
func (hook *resultPayloadCaptureHook) Post(_ context.Context, request Request, result Result) error {
	hook.postRequest, hook.postResult = request, result
	return hook.postErr
}
func (hook *resultPayloadCaptureHook) Failure(_ context.Context, request Request, result Result) error {
	hook.failureRequest, hook.failureResult = request, result
	return nil
}

type progressHook struct {
	message string
}

func (h progressHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{Progress: []Progress{{Message: h.message}}}, nil
}
func (progressHook) Post(context.Context, Request, Result) error    { return nil }
func (progressHook) Failure(context.Context, Request, Result) error { return nil }

func (*resultCaptureHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{}, nil
}
func (h *resultCaptureHook) Post(_ context.Context, _ Request, result Result) error {
	h.post = result.Content
	return nil
}
func (h *resultCaptureHook) Failure(_ context.Context, _ Request, result Result) error {
	h.failure = result.Content
	return nil
}

func (a *countingAuthorizer) Authorize(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
	a.calls.Add(1)
	decision := a.decision
	if len(decision.Input) == 0 {
		decision.Input = request.Input
	}
	return decision, nil
}

func TestObjectSchemaRequiredEncoding(t *testing.T) {
	properties := map[string]any{
		"alpha": stringSchema("optional alpha"),
		"beta":  stringSchema("required beta"),
	}
	optionalOnly := objectSchema(properties)
	if required, exists := optionalOnly["required"]; exists {
		t.Fatalf("optional-only object schema exported required = %#v, want keyword omitted", required)
	}
	if got := optionalOnly["properties"]; !reflect.DeepEqual(got, properties) {
		t.Fatalf("optional-only object schema lost properties: %#v", got)
	}
	if additional, ok := optionalOnly["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("optional-only object schema broadened structural validation: %#v", optionalOnly)
	}
	encoded, err := json.Marshal(optionalOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"required":`) {
		t.Fatalf("optional-only object schema exported a required keyword: %s", encoded)
	}

	mixed := objectSchema(properties, "beta")
	required, ok := mixed["required"].([]string)
	if !ok || !reflect.DeepEqual(required, []string{"beta"}) {
		t.Fatalf("mixed object schema required = %#v, want exactly [beta]", mixed["required"])
	}
	if got := mixed["properties"]; !reflect.DeepEqual(got, properties) {
		t.Fatalf("mixed object schema lost properties: %#v", got)
	}
	if additional, ok := mixed["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("mixed object schema broadened structural validation: %#v", mixed)
	}
}

func TestTaskStopNormalizesEqualCanonicalAndLegacyIDs(t *testing.T) {
	descriptor := taskStopDescriptor(nil)
	for _, test := range []struct {
		name    string
		input   string
		wantID  string
		wantErr string
	}{
		{name: "canonical", input: `{"task_id":"b12345678"}`, wantID: "b12345678"},
		{name: "legacy", input: `{"shell_id":"b12345678"}`, wantID: "b12345678"},
		{name: "equal aliases", input: `{"task_id":"b12345678","shell_id":"b12345678"}`, wantID: "b12345678"},
		{name: "empty", input: `{}`, wantErr: "provide exactly one"},
		{name: "conflict", input: `{"task_id":"b12345678","shell_id":"b87654321"}`, wantErr: "different tasks"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := descriptor.Validate(json.RawMessage(test.input))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Validate() error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := value.(taskStopInput)
			if got.TaskID != test.wantID || got.ShellID != "" {
				t.Fatalf("normalized input = %#v, want task_id %q only", got, test.wantID)
			}
		})
	}
}

func TestTaskOutputSchemaRepresentsFullDurableOffsetRange(t *testing.T) {
	descriptor := taskOutputDescriptor(nil)
	properties := descriptor.InputSchema["properties"].(map[string]any)
	offset := properties["offset"].(map[string]any)
	if maximum, ok := offset["maximum"].(int64); !ok || maximum != task.MaximumOutputFileBytes {
		t.Fatalf("TaskOutput offset maximum = %#v, want full capped output file", offset["maximum"])
	}
	value, err := descriptor.Validate(json.RawMessage(`{"task_id":"b12345678","offset":5368709120,"block":false}`))
	if err != nil {
		t.Fatalf("valid 5 GiB byte offset was rejected: %v", err)
	}
	if got := value.(taskOutputInput).Offset; got != int64(5<<30) {
		t.Fatalf("decoded offset = %d, want 5 GiB", got)
	}
}

func TestRegistryDeterminismAliasesAndBuiltinCollision(t *testing.T) {
	t.Parallel()
	call := func(context.Context, CallContext, any) (Output, error) { return Output{Content: "ok"}, nil }
	validate := func(json.RawMessage) (any, error) { return struct{}{}, nil }
	registry, err := NewRegistry(
		Descriptor{Name: "Zulu", Source: SourcePlugin, Validate: validate, Call: call},
		Descriptor{Name: "Alpha", Source: SourceMCP, Validate: validate, Call: call},
		Descriptor{Name: "Zulu", Aliases: []string{"OldZulu"}, Source: SourceBuiltin, Validate: validate, Call: call},
		Descriptor{Name: "Beta", Source: SourceBuiltin, Validate: validate, Call: call},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	got := make([]string, len(descriptors))
	for i := range descriptors {
		got[i] = descriptors[i].Name
	}
	if strings.Join(got, ",") != "Beta,Zulu,Alpha" {
		t.Fatalf("registry order = %v", got)
	}
	alias, ok := registry.Resolve("OldZulu")
	if !ok || alias.Name != "Zulu" || alias.Source != SourceBuiltin {
		t.Fatalf("alias did not resolve to built-in: %+v, %v", alias, ok)
	}
}

func TestRegistrySnapshotsCannotMutateRegisteredCapability(t *testing.T) {
	t.Parallel()
	schema := objectSchema(map[string]any{"value": stringSchema("original")}, "value")
	descriptor := Descriptor{
		Name: "Stable", Aliases: []string{"OldStable"}, Source: SourceBuiltin, InputSchema: schema,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: func(any) permission.Classification { return permission.Classification{} },
		Call:     func(context.Context, CallContext, any) (Output, error) { return Output{Content: "original call"}, nil },
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}

	// Mutating both the caller-owned descriptor and public registry snapshots
	// must not replace code, aliases, safety classification, or nested schema.
	descriptor.Aliases[0] = "PlantedAlias"
	descriptor.InputSchema["properties"].(map[string]any)["value"].(map[string]any)["description"] = "caller mutation"
	resolved, ok := registry.Resolve("Stable")
	if !ok {
		t.Fatal("registered tool did not resolve")
	}
	resolved.Name = "Hijacked"
	resolved.Aliases[0] = "AnotherAlias"
	resolved.Classify = func(any) permission.Classification { return permission.Classification{ConcurrencySafe: true} }
	resolved.Call = func(context.Context, CallContext, any) (Output, error) { return Output{Content: "hijacked call"}, nil }
	resolved.InputSchema["properties"].(map[string]any)["value"].(map[string]any)["description"] = "snapshot mutation"
	listed := registry.Descriptors()
	listed[0].InputSchema["required"].([]string)[0] = "mutated"

	stable, ok := registry.Resolve("OldStable")
	if !ok || stable.Name != "Stable" || stable.Aliases[0] != "OldStable" || stable.classification(struct{}{}).ConcurrencySafe {
		t.Fatalf("registry capability was mutable through a snapshot: %+v, %v", stable, ok)
	}
	properties := stable.InputSchema["properties"].(map[string]any)
	if description := properties["value"].(map[string]any)["description"]; description != "original" {
		t.Fatalf("nested schema mutation escaped snapshot: %v", description)
	}
	if required := stable.InputSchema["required"].([]string); len(required) != 1 || required[0] != "value" {
		t.Fatalf("schema slice mutation escaped snapshot: %v", required)
	}
	if _, planted := registry.Resolve("PlantedAlias"); planted {
		t.Fatal("caller mutation planted a registry alias")
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "stable", Name: "Stable", Input: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "original call" {
		t.Fatalf("snapshot mutation replaced execution: %+v", result)
	}
}

func TestExecutorFailsBeforePermissionAndCall(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	descriptor := Descriptor{
		Name: "Strict", Source: SourceBuiltin,
		Validate: func(raw json.RawMessage) (any, error) {
			var input struct {
				Value string `json:"value"`
			}
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.Value == "" {
				return nil, errors.New("value is required")
			}
			return input, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			return Output{Content: "called"}, nil
		},
	}
	registry, _ := NewRegistry(descriptor)
	authorizer := &countingAuthorizer{decision: permission.Decision{Kind: permission.DecisionAllow}}
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: authorizer})
	result := executor.Execute(context.Background(), Request{ID: "bad", Name: "Strict", Input: json.RawMessage(`{"unknown":1}`)})
	if !result.IsError || result.Code != "structural_invalid" || result.PermissionRejected || calls.Load() != 0 || authorizer.calls.Load() != 0 {
		t.Fatalf("invalid request crossed boundary: %+v calls=%d auth=%d", result, calls.Load(), authorizer.calls.Load())
	}

	authorizer.decision = permission.Decision{Kind: permission.DecisionDeny, Reason: "policy"}
	result = executor.Execute(context.Background(), Request{ID: "denied", Name: "Strict", Input: json.RawMessage(`{"value":"x"}`)})
	if !result.IsError || result.Code != "denied" || !result.PermissionRejected || string(result.PermissionInput) != `{"value":"x"}` || calls.Load() != 0 || authorizer.calls.Load() != 1 {
		t.Fatalf("denied request executed: %+v calls=%d auth=%d", result, calls.Load(), authorizer.calls.Load())
	}

	authorizer.decision = permission.Decision{Kind: permission.DecisionCancel, Reason: "approval dismissed"}
	result = executor.Execute(context.Background(), Request{ID: "cancelled-permission", Name: "Strict", Input: json.RawMessage(`{"value":"y"}`)})
	if !result.IsError || result.Code != "cancelled" || !result.PermissionRejected || string(result.PermissionInput) != `{"value":"y"}` || calls.Load() != 0 || authorizer.calls.Load() != 2 {
		t.Fatalf("permission cancellation lost denial evidence: %+v calls=%d auth=%d", result, calls.Load(), authorizer.calls.Load())
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	result = executor.Execute(cancelledContext, Request{ID: "cancelled-before-permission", Name: "Strict", Input: json.RawMessage(`{"value":"z"}`)})
	if !result.IsError || result.Code != "cancelled" || result.PermissionRejected || calls.Load() != 0 || authorizer.calls.Load() != 2 {
		t.Fatalf("turn cancellation was mislabeled as a permission denial: %+v calls=%d auth=%d", result, calls.Load(), authorizer.calls.Load())
	}
}

func TestExecutorUntrustedInputBoundariesPreserveAuthorizedEvidence(t *testing.T) {
	type parsedInput struct {
		Value string `json:"value"`
	}
	type boundaryCapture struct {
		projectorInputs []string
		projectorRaws   []string
		callInput       string
		callContext     CallContext
		calls           int
	}
	newDescriptor := func(capture *boundaryCapture) Descriptor {
		mutateRaw := func(raw json.RawMessage, value string) {
			if index := strings.Index(string(raw), value); index >= 0 {
				raw[index] = '!'
			}
		}
		return Descriptor{
			Name: "Adversarial", Source: SourcePlugin,
			Validate: func(raw json.RawMessage) (any, error) {
				var input parsedInput
				if err := decodeStrict(raw, &input); err != nil {
					return nil, err
				}
				if input.Value == "" {
					return nil, errors.New("value is required")
				}
				// A descriptor validator must never receive the canonical bytes.
				mutateRaw(raw, input.Value)
				return &input, nil
			},
			Classify: func(value any) permission.Classification {
				input := value.(*parsedInput)
				classification := permission.Classification{
					ReadOnly:    input.Value != "classifier-corrupted",
					Destructive: input.Value == "classifier-corrupted",
				}
				// Classification and permission projection must not share a
				// parsed object with each other or with eventual execution.
				input.Value = "classifier-corrupted"
				return classification
			},
			ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
				input := value.(*parsedInput)
				capture.projectorInputs = append(capture.projectorInputs, input.Value)
				capture.projectorRaws = append(capture.projectorRaws, string(raw))
				mutateRaw(raw, input.Value)
				projectedValue := input.Value
				input.Value = "projector-corrupted"
				return permission.Request{Content: projectedValue}, nil
			},
			Call: func(_ context.Context, call CallContext, value any) (Output, error) {
				input := value.(*parsedInput)
				capture.calls++
				capture.callInput = input.Value
				call.OriginalInput = cloneRaw(call.OriginalInput)
				call.ExecutedInput = cloneRaw(call.ExecutedInput)
				capture.callContext = call
				return Output{Content: input.Value}, nil
			},
		}
	}
	newExecutor := func(t *testing.T, descriptor Descriptor, authorizer Authorizer, hooks ...Hook) *Executor {
		t.Helper()
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: authorizer, Hooks: hooks})
		if err != nil {
			t.Fatal(err)
		}
		return executor
	}
	clonePermissionRequest := func(request permission.Request) permission.Request {
		request.Input = cloneRaw(request.Input)
		return request
	}
	assertProjection := func(t *testing.T, request permission.Request, raw, value string) {
		t.Helper()
		if string(request.Input) != raw {
			t.Fatalf("permission input = %s, want exact %s", request.Input, raw)
		}
		if !request.Classification.ReadOnly || request.Classification.Destructive {
			t.Fatalf("classification was derived from mutated parsed input: %+v", request.Classification)
		}
		if request.Content != value {
			t.Fatalf("permission projection content = %q, want %q", request.Content, value)
		}
	}

	t.Run("initial input", func(t *testing.T) {
		const originalText = `{"value":"initial"}`
		original := json.RawMessage(originalText)
		capture := &boundaryCapture{}
		var authorized permission.Request
		authorizer := authorizerFunc(func(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
			authorized = clonePermissionRequest(request)
			return permission.Decision{Kind: permission.DecisionAllow, Input: request.Input}, nil
		})
		executor := newExecutor(t, newDescriptor(capture), authorizer)
		result := executor.Execute(t.Context(), Request{ID: "initial", Name: "Adversarial", Input: original})
		if result.IsError || result.Content != "initial" || capture.calls != 1 || capture.callInput != "initial" {
			t.Fatalf("authorized execution used mutated input: result=%+v capture=%+v", result, capture)
		}
		assertProjection(t, authorized, originalText, "initial")
		if len(capture.projectorInputs) != 1 || capture.projectorInputs[0] != "initial" ||
			len(capture.projectorRaws) != 1 || capture.projectorRaws[0] != originalText {
			t.Fatalf("permission projector observed mutated state: %+v", capture)
		}
		if string(original) != originalText || string(result.OriginalInput) != originalText ||
			string(result.ExecutedInput) != originalText || len(result.PermissionInput) != 0 {
			t.Fatalf("terminal evidence changed: source=%s result=%+v", original, result)
		}
		if string(capture.callContext.OriginalInput) != originalText ||
			string(capture.callContext.ExecutedInput) != originalText || capture.callContext.UserModified {
			t.Fatalf("call context evidence changed: %+v", capture.callContext)
		}
	})

	t.Run("hook-updated input", func(t *testing.T) {
		const originalText = `{"value":"initial"}`
		const updatedText = `{"value":"hooked"}`
		original := json.RawMessage(originalText)
		hook := &staticInputUpdateHook{updated: json.RawMessage(updatedText)}
		capture := &boundaryCapture{}
		var authorized permission.Request
		authorizer := authorizerFunc(func(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
			authorized = clonePermissionRequest(request)
			return permission.Decision{Kind: permission.DecisionDeny, Reason: "test denial", Input: request.Input}, nil
		})
		executor := newExecutor(t, newDescriptor(capture), authorizer, hook)
		result := executor.Execute(t.Context(), Request{ID: "hooked", Name: "Adversarial", Input: original})
		if !result.IsError || result.Code != "denied" || !result.PermissionRejected || capture.calls != 0 {
			t.Fatalf("denied hook update crossed execution boundary: result=%+v capture=%+v", result, capture)
		}
		assertProjection(t, authorized, updatedText, "hooked")
		if len(capture.projectorInputs) != 1 || capture.projectorInputs[0] != "hooked" ||
			len(capture.projectorRaws) != 1 || capture.projectorRaws[0] != updatedText {
			t.Fatalf("hook-updated projection observed mutated state: %+v", capture)
		}
		if string(original) != originalText || string(hook.updated) != updatedText ||
			string(result.OriginalInput) != originalText || string(result.PermissionInput) != updatedText ||
			len(result.ExecutedInput) != 0 {
			t.Fatalf("hook-updated evidence changed: source=%s hook=%s result=%+v", original, hook.updated, result)
		}
	})

	t.Run("user-updated authorization input", func(t *testing.T) {
		const originalText = `{"value":"initial"}`
		const approvedText = `{"value":"approved"}`
		original := json.RawMessage(originalText)
		approved := json.RawMessage(approvedText)
		capture := &boundaryCapture{}
		var initialRequest, rebuiltRequest permission.Request
		authorizer := authorizerFunc(func(_ context.Context, request permission.Request, rebuild permission.Rebuild) (permission.Decision, error) {
			initialRequest = clonePermissionRequest(request)
			rebuilt, err := rebuild(approved)
			if err != nil {
				return permission.Decision{}, err
			}
			rebuiltRequest = clonePermissionRequest(rebuilt)
			return permission.Decision{
				Kind: permission.DecisionAllow, Input: approved, OriginalInput: request.Input, UserModified: true,
			}, nil
		})
		executor := newExecutor(t, newDescriptor(capture), authorizer)
		result := executor.Execute(t.Context(), Request{ID: "approved", Name: "Adversarial", Input: original})
		if result.IsError || result.Content != "approved" || capture.calls != 1 || capture.callInput != "approved" {
			t.Fatalf("approved edit used mutated input: result=%+v capture=%+v", result, capture)
		}
		assertProjection(t, initialRequest, originalText, "initial")
		assertProjection(t, rebuiltRequest, approvedText, "approved")
		if len(capture.projectorInputs) != 2 || capture.projectorInputs[0] != "initial" || capture.projectorInputs[1] != "approved" ||
			len(capture.projectorRaws) != 2 || capture.projectorRaws[0] != originalText || capture.projectorRaws[1] != approvedText {
			t.Fatalf("edited projection observed mutated state: %+v", capture)
		}
		if string(original) != originalText || string(approved) != approvedText ||
			string(result.OriginalInput) != originalText || string(result.ExecutedInput) != approvedText ||
			len(result.PermissionInput) != 0 || !result.UserModified {
			t.Fatalf("approved evidence changed: source=%s approved=%s result=%+v", original, approved, result)
		}
		if string(capture.callContext.OriginalInput) != originalText ||
			string(capture.callContext.ExecutedInput) != approvedText || !capture.callContext.UserModified {
			t.Fatalf("edited call context evidence changed: %+v", capture.callContext)
		}
	})
}

func TestSemanticIOOccursOnlyAfterAuthorization(t *testing.T) {
	t.Parallel()
	var semanticCalls, executionCalls atomic.Int32
	descriptor := Descriptor{
		Name: "Protected", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Semantic: func(any) error { semanticCalls.Add(1); return nil },
		Classify: func(any) permission.Classification { return permission.Classification{ConcurrencySafe: true} },
		Call: func(context.Context, CallContext, any) (Output, error) {
			executionCalls.Add(1)
			return Output{Content: "done"}, nil
		},
	}
	registry, _ := NewRegistry(descriptor)
	authorizer := &countingAuthorizer{decision: permission.Decision{Kind: permission.DecisionDeny, Reason: "protected"}}
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: authorizer})
	scheduler := NewScheduler(executor, registry, 2)
	result := scheduler.Execute(context.Background(), []Request{{ID: "protected", Name: "Protected", Input: json.RawMessage(`{}`)}})[0]
	if result.Code != "denied" || semanticCalls.Load() != 0 || executionCalls.Load() != 0 {
		t.Fatalf("preauthorization semantic check ran: result=%#v semantic=%d calls=%d", result, semanticCalls.Load(), executionCalls.Load())
	}
}

func TestPostHookFailureDoesNotUndoEarnedSuccess(t *testing.T) {
	t.Parallel()
	descriptor := Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil }, Call: func(context.Context, CallContext, any) (Output, error) {
		return Output{Content: "effect completed"}, nil
	}}
	registry, _ := NewRegistry(descriptor)
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{postFailureHook{}}})
	result := executor.Execute(context.Background(), Request{ID: "effect", Name: "Effect", Input: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "effect completed" {
		t.Fatalf("post hook erased success: %#v", result)
	}
	warnings, ok := result.Metadata["post_hook_warnings"].([]string)
	if !ok || len(warnings) != 1 {
		t.Fatalf("missing hook diagnostic: %#v", result.Metadata)
	}
}

func TestHookPanicsAreContainedAtTheirLifecycleBoundary(t *testing.T) {
	t.Parallel()
	call := func(context.Context, CallContext, any) (Output, error) {
		return Output{Content: "effect completed"}, nil
	}
	validate := func(json.RawMessage) (any, error) { return struct{}{}, nil }

	t.Run("pre panic fails before execution and reaches failure observers", func(t *testing.T) {
		var executions atomic.Int32
		panicking := &panicHook{pre: true}
		observer := &panicHook{}
		registry, _ := NewRegistry(Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: func(context.Context, CallContext, any) (Output, error) {
			executions.Add(1)
			return Output{Content: "unexpected"}, nil
		}})
		executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{panicking, observer}})
		result := executor.Execute(t.Context(), Request{ID: "pre-panic", Name: "Effect", Input: json.RawMessage(`{}`)})
		if result.Code != "hook_failed" || executions.Load() != 0 || observer.failures.Load() != 1 {
			t.Fatalf("pre-hook panic crossed boundary: result=%+v executions=%d failures=%d", result, executions.Load(), observer.failures.Load())
		}
	})

	t.Run("post panic cannot undo an earned result", func(t *testing.T) {
		registry, _ := NewRegistry(Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: call})
		executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{&panicHook{post: true}}})
		result := executor.Execute(t.Context(), Request{ID: "post-panic", Name: "Effect", Input: json.RawMessage(`{}`)})
		warnings, _ := result.Metadata["post_hook_warnings"].([]string)
		if result.IsError || result.Content != "effect completed" || len(warnings) != 1 || !strings.Contains(warnings[0], "panicked") {
			t.Fatalf("post-hook panic rewrote earned success: %+v", result)
		}
	})

	t.Run("failure and denial observer panics preserve terminal outcomes", func(t *testing.T) {
		failingRegistry, _ := NewRegistry(Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{}, errors.New("original failure")
		}})
		failureHook := &panicHook{failure: true}
		executor, _ := NewExecutor(ExecutorOptions{Registry: failingRegistry, Authorizer: allowAuthorizer{}, Hooks: []Hook{failureHook}})
		failed := executor.Execute(t.Context(), Request{ID: "failure-panic", Name: "Effect", Input: json.RawMessage(`{}`)})
		if failed.Code != "execution_failed" || failed.Content != "original failure" || failureHook.failures.Load() != 1 {
			t.Fatalf("failure observer panic replaced terminal failure: %+v", failed)
		}

		denialHook := &panicHook{denial: true}
		deniedRegistry, _ := NewRegistry(Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: call})
		authorizer := &countingAuthorizer{decision: permission.Decision{Kind: permission.DecisionDeny, Reason: "policy denied"}}
		executor, _ = NewExecutor(ExecutorOptions{Registry: deniedRegistry, Authorizer: authorizer, Hooks: []Hook{denialHook}})
		denied := executor.Execute(t.Context(), Request{ID: "denial-panic", Name: "Effect", Input: json.RawMessage(`{}`)})
		if denied.Code != "denied" || denied.Content != "policy denied" || !denied.PermissionRejected || denialHook.denials.Load() != 1 {
			t.Fatalf("denial observer panic replaced terminal denial: %+v", denied)
		}
	})

	t.Run("tool panic invokes failure lifecycle", func(t *testing.T) {
		observer := &panicHook{}
		registry, _ := NewRegistry(Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: func(context.Context, CallContext, any) (Output, error) {
			panic("tool panic")
		}})
		executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{observer}})
		result := executor.Execute(t.Context(), Request{ID: "tool-panic", Name: "Effect", Input: json.RawMessage(`{}`)})
		if result.Code != "execution_failed" || !strings.Contains(result.Content, "panic was contained") || observer.failures.Load() != 1 {
			t.Fatalf("tool panic skipped failure lifecycle: %+v failures=%d", result, observer.failures.Load())
		}
	})

	t.Run("panic payload formatters are never invoked", func(t *testing.T) {
		const secret = "panic-payload-format-secret"
		for _, stage := range []string{"validator", "call", "pre hook", "post hook"} {
			t.Run(stage, func(t *testing.T) {
				var formatterCalls atomic.Int32
				payload := &panickingFormatPanicPayload{calls: &formatterCalls, secret: secret}
				descriptor := Descriptor{Name: "Effect", Source: SourceBuiltin, Validate: validate, Call: call}
				var hooks []Hook
				wantError := true
				wantCode := "execution_failed"
				switch stage {
				case "validator":
					descriptor.Validate = func(json.RawMessage) (any, error) { panic(payload) }
				case "call":
					descriptor.Call = func(context.Context, CallContext, any) (Output, error) { panic(payload) }
				case "pre hook":
					hooks = []Hook{&panicHook{pre: true, payload: payload}}
					wantCode = "hook_failed"
				case "post hook":
					hooks = []Hook{&panicHook{post: true, payload: payload}}
					wantError = false
					wantCode = ""
				}
				registry, err := NewRegistry(descriptor)
				if err != nil {
					t.Fatal(err)
				}
				executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: hooks})
				if err != nil {
					t.Fatal(err)
				}
				result := executor.Execute(t.Context(), Request{
					ID: "payload-" + strings.ReplaceAll(stage, " ", "-"), Name: "Effect", Input: json.RawMessage(`{}`),
				})
				if result.IsError != wantError || (wantCode != "" && result.Code != wantCode) {
					t.Fatalf("panic payload outcome = %#v, want error=%t code=%q", result, wantError, wantCode)
				}
				if strings.Contains(result.Content, secret) || formatterCalls.Load() != 0 {
					t.Fatalf("panic payload formatter was observed: calls=%d content=%q", formatterCalls.Load(), result.Content)
				}
				if stage == "post hook" {
					warnings, _ := result.Metadata["post_hook_warnings"].([]string)
					if len(warnings) != 1 || warnings[0] != "post-tool hook panicked" {
						t.Fatalf("post-hook panic warning = %#v", warnings)
					}
				}
			})
		}
	})
}

func TestExecutorSanitizesResultsBeforeHooksAndPersistence(t *testing.T) {
	const secret = "production-subscription-secret"
	sanitize := func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") }
	storeRoot := t.TempDir()
	store, err := NewResultStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	capture := &resultCaptureHook{}
	descriptor := Descriptor{
		Name: "Leaky", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: strings.Repeat("value="+secret+" ", 20)}, nil
		},
		MaxResultChars: 32,
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture}, ResultStore: store, Sanitize: sanitize})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "secret_output", Name: "Leaky", Input: json.RawMessage(`{}`)})
	if result.IsError || strings.Contains(result.Content, secret) || strings.Contains(capture.post, secret) {
		t.Fatalf("secret escaped successful result boundary: result=%q hook=%q", result.Content, capture.post)
	}
	err = filepath.WalkDir(storeRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	failing := Descriptor{
		Name: "Failing", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{}, invocationError("execution_failed", "failure echoed %s", secret)
		},
	}
	registry, _ = NewRegistry(failing)
	capture = &resultCaptureHook{}
	executor, _ = NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture}, Sanitize: sanitize})
	result = executor.Execute(t.Context(), Request{ID: "secret_failure", Name: "Failing", Input: json.RawMessage(`{}`)})
	if !result.IsError || strings.Contains(result.Content, secret) || strings.Contains(capture.failure, secret) {
		t.Fatalf("secret escaped failure boundary: result=%q hook=%q", result.Content, capture.failure)
	}
}

func TestExecutorUnionsSessionAndSourceCredentialsAcrossResultTransforms(t *testing.T) {
	const (
		sessionSecret = "R"
		sourceSecret  = "*"
	)
	validate := func(json.RawMessage) (any, error) { return struct{}{}, nil }
	makeDescriptor := func(limit int) Descriptor {
		return Descriptor{
			Name: "Scoped", Source: SourceMCP, Validate: validate,
			Call: func(context.Context, CallContext, any) (Output, error) {
				return Output{Content: "before " + sessionSecret + " and " + sourceSecret + " after"}, nil
			},
			CredentialSanitizer: redact.New(sourceSecret),
			MaxResultChars:      limit,
		}
	}
	assertSafe := func(t *testing.T, value string) {
		t.Helper()
		if strings.Contains(value, sessionSecret) || strings.Contains(value, sourceSecret) {
			t.Fatalf("independent-set marker cycle escaped: %q", value)
		}
	}

	t.Run("no store every limit", func(t *testing.T) {
		for limit := 0; limit <= 40; limit++ {
			registry, err := NewRegistry(makeDescriptor(limit))
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewExecutor(ExecutorOptions{
				Registry: registry, Authorizer: allowAuthorizer{},
				CredentialSanitizer: redact.New(sessionSecret),
			})
			if err != nil {
				t.Fatal(err)
			}
			result := executor.Execute(t.Context(), Request{
				ID: "cycle_" + strconv.Itoa(limit), Name: "Scoped", Input: json.RawMessage(`{}`),
			})
			if result.IsError {
				t.Fatalf("limit %d failed: %+v", limit, result)
			}
			assertSafe(t, result.Content)
		}
	})

	t.Run("persisted and replacement", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		descriptor := makeDescriptor(1)
		descriptor.Call = func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: strings.Repeat("R*", 2000)}, nil
		}
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{
			Registry: registry, Authorizer: allowAuthorizer{}, ResultStore: store,
			CredentialSanitizer: redact.New(sessionSecret),
		})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), Request{ID: "cycle_store", Name: "Scoped", Input: json.RawMessage(`{}`)})
		assertSafe(t, result.Content)
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".bin" {
				return walkErr
			}
			content, readErr := os.ReadFile(path)
			if readErr == nil {
				assertSafe(t, string(content))
			}
			return readErr
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("scoped progress", func(t *testing.T) {
		registry, err := NewRegistry(makeDescriptor(100))
		if err != nil {
			t.Fatal(err)
		}
		var observed []string
		executor, err := NewExecutor(ExecutorOptions{
			Registry: registry, Authorizer: allowAuthorizer{},
			Hooks: []Hook{progressHook{message: "R*"}},
			Progress: func(progress Progress) {
				observed = append(observed, progress.Message)
			},
			CredentialSanitizer: redact.New(sessionSecret),
		})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), Request{ID: "cycle_progress", Name: "Scoped", Input: json.RawMessage(`{}`)})
		if result.IsError || len(observed) != 1 {
			t.Fatalf("progress execution = %+v observed=%q", result, observed)
		}
		assertSafe(t, observed[0])
	})

	t.Run("guard exhaustion rejects unsafe routing identity", func(t *testing.T) {
		const candidates = "*#~!@$%^&_-+=:;,.?0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz/|"
		literals := make([]string, 0, len(candidates))
		for index := 0; index < len(candidates); index++ {
			literals = append(literals, candidates[index:index+1])
		}
		var calls atomic.Int32
		descriptor := makeDescriptor(100)
		descriptor.CredentialSanitizer = redact.New(literals...)
		descriptor.Call = func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			return Output{Content: "*"}, nil
		}
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
		if err == nil || executor != nil || !strings.Contains(err.Error(), "mandatory protocol framing") {
			t.Fatalf("guard-exhausted credential set was accepted: executor=%#v err=%v", executor, err)
		}
		if calls.Load() != 0 {
			t.Fatalf("guard-exhausted credential set reached implementation %d times", calls.Load())
		}
	})
}

func TestCallContextDoesNotExposeCredentialSetOrLiteral(t *testing.T) {
	const secret = "session-credential-reflection-sentinel"
	var observed CallContext
	descriptor := Descriptor{
		Name: "InspectContext", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
			observed = call
			return Output{Content: "ok"}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "inspect_context", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if result.IsError {
		t.Fatalf("execution failed: %+v", result)
	}
	setType := reflect.TypeOf((*redact.Set)(nil))
	contextType := reflect.TypeOf(observed)
	for index := 0; index < contextType.NumField(); index++ {
		if contextType.Field(index).Type == setType {
			t.Fatalf("CallContext field %q exposes a credential Set", contextType.Field(index).Name)
		}
	}
	if strings.Contains(fmt.Sprintf("%#v", observed), secret) {
		t.Fatal("CallContext formatting exposed the session credential")
	}
	if observed.ProjectOutput == nil || observed.CredentialLookahead != len(secret)-1 {
		t.Fatalf("bounded projection capability was not installed: %#v", observed)
	}
}

func TestExecutorRejectsDuplicateInputMembersBeforeValidationOrExecution(t *testing.T) {
	var validateCalls, executeCalls int
	descriptor := Descriptor{
		Name: "UniqueInput", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) {
			validateCalls++
			return struct{}{}, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			executeCalls++
			return Output{Content: "unexpected"}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		CredentialSanitizer: redact.New("secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{
		ID: "duplicate_input", Name: descriptor.Name,
		Input: json.RawMessage(`{"value":"\u0073ecret","value":"safe"}`),
	})
	if !result.IsError || result.Code != "structural_invalid" || validateCalls != 0 || executeCalls != 0 {
		t.Fatalf("duplicate input result=%+v validate=%d execute=%d", result, validateCalls, executeCalls)
	}
}

func TestExecutorSanitizesMissingIDBeforeEarlyReturn(t *testing.T) {
	const secret = "a/b"
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{
		Name: "Unknown", Input: json.RawMessage(`{"value":"a\/b"}`),
	})
	if !result.IsError || result.Code != "structural_invalid" ||
		strings.Contains(string(result.OriginalInput), secret) {
		t.Fatalf("missing-ID result exposed credential: %#v", result)
	}
}

func TestExecutorSanitizesAuthorizerDenialInputBeforeObserverHooks(t *testing.T) {
	const secret = "denial-observer-credential"
	descriptor := Descriptor{
		Name: "DenialObserver", Source: SourceBuiltin,
		Validate: func(raw json.RawMessage) (any, error) {
			var input map[string]string
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			return input, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			t.Fatal("denied request executed")
			return Output{}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	capture := &denialPayloadCaptureHook{}
	authorizer := authorizerFunc(func(context.Context, permission.Request, permission.Rebuild) (permission.Decision, error) {
		return permission.Decision{
			Kind:   permission.DecisionDeny,
			Reason: "policy",
			Input:  json.RawMessage(`{"value":"` + secret + `"}`),
		}, nil
	})
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: authorizer, Hooks: []Hook{capture},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{
		ID: "denial_observer", Name: descriptor.Name,
		AssistantID: "assistant-" + secret,
		Input:       json.RawMessage(`{"value":"safe"}`),
	})
	if !result.IsError || result.Code != "denied" || !result.PermissionRejected {
		t.Fatalf("denial result = %#v", result)
	}
	for label, value := range map[string]any{
		"returned result":  result,
		"observer request": capture.request,
		"observer result":  capture.result,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", label, err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("%s exposed authorizer-controlled credential input: %s", label, encoded)
		}
	}
	if capture.request.AssistantID == "" || strings.Contains(capture.request.AssistantID, secret) {
		t.Fatalf("observer assistant identity was not safely projected: %q", capture.request.AssistantID)
	}
}

func TestExecutorOpaqueSanitizerPanicSuppressesContent(t *testing.T) {
	descriptor := Descriptor{
		Name: "OpaquePanic", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "sensitive output"}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		Sanitize: func(string) string { panic("sanitizer unavailable") },
	})
	if err == nil || executor != nil || !strings.Contains(err.Error(), "mandatory protocol framing") {
		t.Fatalf("panicking opaque sanitizer was accepted: executor=%#v err=%v", executor, err)
	}
}

func TestExecutorCanonicalizesCredentialBearingInvocationErrorCode(t *testing.T) {
	const secret = "credential_code_value"
	descriptor := Descriptor{
		Name: "CredentialCode", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{}, &InvocationError{Code: secret, Err: errors.New("ordinary failure")}
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	capture := &resultPayloadCaptureHook{}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{
		ID: "credential_code", Name: descriptor.Name, Input: json.RawMessage(`{}`),
	})
	if !result.IsError || result.Code != "execution_failed" ||
		capture.failureResult.Code != "execution_failed" {
		t.Fatalf("credential-bearing error code was not canonicalized: result=%#v hook=%#v", result, capture.failureResult)
	}
	encoded, err := json.Marshal([]Result{result, capture.failureResult})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("credential-bearing error code reached observer/result boundary: %s", encoded)
	}
}

func TestCanonicalErrorCodeUsesClosedVocabulary(t *testing.T) {
	admitted := []string{
		"cancelled",
		"denied",
		"execution_failed",
		"hook_failed",
		"malformed_result",
		"permission_failed",
		"semantic_invalid",
		"sibling_error",
		"stale_file",
		"structural_invalid",
		"timeout",
		"unavailable",
		"unknown_tool",
	}
	for _, code := range admitted {
		t.Run("admitted_"+code, func(t *testing.T) {
			if got := canonicalErrorCode(code, "execution_failed"); got != code {
				t.Fatalf("canonicalErrorCode(%q) = %q, want exact admitted code", code, got)
			}
		})
	}
	for name, code := range map[string]string{
		"empty":      "",
		"unknown":    "provider_specific_failure",
		"control":    "denied\nforged_code",
		"credential": "production-secret-error-code",
	} {
		t.Run("rejected_"+name, func(t *testing.T) {
			if got := canonicalErrorCode(code, "execution_failed"); got != "execution_failed" {
				t.Fatalf("canonicalErrorCode(%q) = %q, want execution_failed", code, got)
			}
		})
	}
}

type cyclicUnwrapError struct{}

func (err *cyclicUnwrapError) Error() string { return "cycle" }
func (err *cyclicUnwrapError) Unwrap() error { return err }

type wideUnwrapError struct {
	children []error
}

func (*wideUnwrapError) Error() string       { return "wide" }
func (err *wideUnwrapError) Unwrap() []error { return err.children }

type blockingToolUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingToolUnwrapError) Error() string { return "foreign tool failure" }
func (err *blockingToolUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return &InvocationError{Code: "timeout"}
}

func TestErrorCodeBoundsCyclicAndOversizedErrorGraphs(t *testing.T) {
	if got := errorCode(&cyclicUnwrapError{}, "execution_failed"); got != "execution_failed" {
		t.Fatalf("cyclic error code = %q", got)
	}

	var err error = &InvocationError{Code: "denied", Err: errors.New("leaf")}
	for range 256 {
		err = fmt.Errorf("wrapped: %w", err)
	}
	if got := errorCode(err, "execution_failed"); got != "execution_failed" {
		t.Fatalf("oversized error graph escaped traversal bound: %q", got)
	}
	joined := errors.Join(errors.New("ordinary"), &InvocationError{Code: "timeout"})
	if got := errorCode(joined, "execution_failed"); got != "timeout" {
		t.Fatalf("joined invocation code = %q, want timeout", got)
	}

	children := make([]error, 10_000)
	for index := range children {
		children[index] = errors.New("ordinary")
	}
	children[len(children)-1] = &InvocationError{Code: "denied"}
	if got := errorCode(&wideUnwrapError{children: children}, "execution_failed"); got != "execution_failed" {
		t.Fatalf("wide error graph exceeded traversal width bound: %q", got)
	}
}

func TestExecutorDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingToolUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	descriptor := Descriptor{
		Name: "BlockingError", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) {
			return struct{}{}, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{}, cause
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(context.Background(), Request{
			ID: "blocking_error", Name: descriptor.Name, Input: json.RawMessage(`{}`),
		})
	}()
	select {
	case result := <-done:
		if !result.IsError || result.Code != "execution_failed" || result.Content != "tool operation failed" {
			t.Fatalf("blocking tool result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Executor.Execute blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Executor.Execute invoked foreign Unwrap")
	default:
	}
}

func TestExecutorCancellationDoesNotInspectBlockingForeignCause(t *testing.T) {
	cause := &blockingToolUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	descriptor := Descriptor{
		Name: "CancelledWithForeignCause", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) {
			return struct{}{}, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "unexpected"}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(ctx, Request{
			ID: "cancelled_foreign_cause", Name: descriptor.Name, Input: json.RawMessage(`{}`),
		})
	}()
	select {
	case result := <-done:
		if !result.IsError || result.Code != "cancelled" || result.Content != "tool invocation cancelled: context canceled" {
			t.Fatalf("cancelled tool result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Executor.Execute blocked while classifying a foreign cancellation cause")
	}
	select {
	case <-cause.called:
		t.Fatal("Executor.Execute invoked foreign cancellation cause Unwrap")
	default:
	}
}

func TestExecutorPreservesCredentialFreeAuthorizationEvidenceBytes(t *testing.T) {
	raw := json.RawMessage("{ \n  \"z\": 1, \"a\": [ true, null ] \n}")
	descriptor := Descriptor{
		Name: "Evidence", Source: SourceBuiltin,
		Validate: func(input json.RawMessage) (any, error) {
			var value map[string]any
			return value, json.Unmarshal(input, &value)
		},
		Call: func(context.Context, CallContext, any) (Output, error) { return Output{Content: "ok"}, nil },
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		CredentialSanitizer: redact.New("unrelated-session-credential"),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "evidence", Name: descriptor.Name, Input: raw})
	if result.IsError || !bytes.Equal(result.OriginalInput, raw) || !bytes.Equal(result.ExecutedInput, raw) {
		t.Fatalf("credential-free evidence bytes changed: input=%q original=%q executed=%q result=%+v",
			raw, result.OriginalInput, result.ExecutedInput, result)
	}
}

func TestExecutorRejectsCredentialBearingRoutingIdentityBeforeLedgerAcceptance(t *testing.T) {
	const secret = "credential-routing-identity"
	var authorizeCalls, toolCalls atomic.Int32
	descriptor := Descriptor{
		Name: "RoutingIdentity", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			toolCalls.Add(1)
			return Output{Content: "unexpected"}, nil
		},
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry,
		Authorizer: authorizerFunc(func(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
			authorizeCalls.Add(1)
			return permission.Decision{Kind: permission.DecisionAllow, Input: request.Input}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: secret, Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if !result.IsError || result.Code != "structural_invalid" || result.ToolUseID != "" || result.Name != "" {
		t.Fatalf("unsafe routing identity result = %#v", result)
	}
	if strings.Contains(fmt.Sprintf("%+v", result), secret) || authorizeCalls.Load() != 0 || toolCalls.Load() != 0 {
		t.Fatalf("unsafe identity crossed the acceptance boundary: result=%+v authorize=%d tool=%d", result, authorizeCalls.Load(), toolCalls.Load())
	}
}

func TestExecutorRejectsCredentialAcrossActualResultIdentityFraming(t *testing.T) {
	const secret = `id","name":"IdentityFrame`
	var calls atomic.Int32
	descriptor := Descriptor{
		Name: "IdentityFrame", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			return Output{Content: "unexpected"}, nil
		},
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "id", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if !result.IsError || result.ToolUseID != "" || result.Name != "" || calls.Load() != 0 {
		t.Fatalf("actual Result identity frame crossed acceptance: result=%#v calls=%d", result, calls.Load())
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("rejected Result identity reconstructed credential: %s", encoded)
	}
}

func TestExecutorRejectsUnsafeFixedTerminalFallbackBeforeExecution(t *testing.T) {
	const secret = `abc","name":"Echo","content":"","content_suppressed":true,"is_error":true,"code":"execution_failed`
	var calls atomic.Int32
	descriptor := Descriptor{
		Name: "Echo", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			return Output{ContentSuppressed: true}, nil
		},
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "abc", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Code != "structural_invalid" || result.ToolUseID != "" ||
		result.Name != "" || calls.Load() != 0 || strings.Contains(string(encoded), secret) {
		t.Fatalf("unsafe terminal fallback reached execution: result=%#v calls=%d json=%s", result, calls.Load(), encoded)
	}
}

func TestExecutorAppliesLegacySanitizerToCompleteProtocolFrames(t *testing.T) {
	const secret = `abc","name":"Echo","content":"","content_suppressed":true,"is_error":false`
	var calls atomic.Int32
	descriptor := Descriptor{
		Name: "Echo", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			return Output{ContentSuppressed: true}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[redacted]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "abc", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Code != "execution_failed" || result.ToolUseID != "abc" ||
		result.Name != "Echo" || !result.ContentSuppressed || calls.Load() != 1 ||
		strings.Contains(string(encoded), secret) {
		t.Fatalf("legacy sanitizer missed complete terminal frame: result=%#v calls=%d json=%s", result, calls.Load(), encoded)
	}
}

func TestExecutorRejectsCredentialSetsIncompatibleWithMandatoryFrames(t *testing.T) {
	descriptor := Descriptor{
		Name: "FrameCompatibility", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call:     func(context.Context, CallContext, any) (Output, error) { return Output{Content: "ok"}, nil },
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"false", "true", "null"} {
		t.Run(secret, func(t *testing.T) {
			executor, err := NewExecutor(ExecutorOptions{
				Registry: registry, Authorizer: allowAuthorizer{},
				CredentialSanitizer: redact.New(secret),
			})
			if err == nil || executor != nil || !strings.Contains(err.Error(), "mandatory protocol framing") {
				t.Fatalf("mandatory-frame credential %q was accepted: executor=%#v err=%v", secret, executor, err)
			}
		})
	}

	source := descriptor
	source.Source = SourceMCP
	source.CredentialSanitizer = redact.New("false")
	sourceRegistry, err := NewRegistry(source)
	if err != nil {
		t.Fatal(err)
	}
	if executor, err := NewExecutor(ExecutorOptions{Registry: sourceRegistry, Authorizer: allowAuthorizer{}}); err == nil || executor != nil {
		t.Fatalf("source-scoped mandatory-frame credential was accepted: executor=%#v err=%v", executor, err)
	}
}

func TestExecutorOwnsCompleteProgressCredentialBoundary(t *testing.T) {
	t.Run("overwrites extension identity", func(t *testing.T) {
		const secret = "source-progress-credential"
		var observed Progress
		descriptor := Descriptor{
			Name: "ProgressIdentity", Source: SourceMCP,
			Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
			Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
				call.Progress(Progress{ToolUseID: secret, Message: "working", Percent: 25})
				return Output{Content: "ok"}, nil
			},
			CredentialSanitizer: redact.New(secret),
		}
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{
			Registry: registry, Authorizer: allowAuthorizer{},
			Progress: func(progress Progress) { observed = progress },
		})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), Request{ID: "accepted_progress", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
		if result.IsError || observed.ToolUseID != "accepted_progress" || observed.Message != "working" || observed.Percent != 25 {
			t.Fatalf("owned progress projection = %#v result=%#v", observed, result)
		}
		if strings.Contains(fmt.Sprintf("%+v", observed), secret) {
			t.Fatalf("progress retained extension-controlled credential identity: %#v", observed)
		}
	})

	t.Run("complete framing suppresses callback", func(t *testing.T) {
		const secret = `accepted_progress","message":"alpha`
		var calls atomic.Int32
		descriptor := Descriptor{
			Name: "ProgressFraming", Source: SourceMCP,
			Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
			Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
				call.Progress(Progress{Message: "alpha"})
				call.Progress(Progress{Message: "safe", Percent: 101})
				return Output{Content: "ok"}, nil
			},
			CredentialSanitizer: redact.New(secret),
		}
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{
			Registry: registry, Authorizer: allowAuthorizer{},
			Progress: func(Progress) { calls.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), Request{ID: "accepted_progress", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
		if result.IsError || calls.Load() != 0 {
			t.Fatalf("unsafe or invalid progress reached callback: result=%#v calls=%d", result, calls.Load())
		}
	})

	t.Run("observer panic cannot rewrite earned execution", func(t *testing.T) {
		const secret = "progress-observer-panic-secret"
		var formatterCalls atomic.Int32
		payload := &panickingFormatPanicPayload{calls: &formatterCalls, secret: secret}
		failureObserver := &panicHook{}
		descriptor := Descriptor{
			Name: "ProgressObserverPanic", Source: SourceBuiltin,
			Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
			Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
				call.Progress(Progress{Message: "working", Percent: 50})
				return Output{Content: "effect completed"}, nil
			},
		}
		registry, err := NewRegistry(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		executor, err := NewExecutor(ExecutorOptions{
			Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{failureObserver},
			Progress: func(Progress) { panic(payload) },
		})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.Execute(t.Context(), Request{
			ID: "progress-observer-panic", Name: descriptor.Name, Input: json.RawMessage(`{}`),
		})
		if result.IsError || result.Content != "effect completed" || failureObserver.failures.Load() != 0 {
			t.Fatalf("progress observer panic rewrote execution: result=%#v failures=%d", result, failureObserver.failures.Load())
		}
		if formatterCalls.Load() != 0 || strings.Contains(result.Content, secret) {
			t.Fatalf("progress panic payload was formatted: calls=%d content=%q", formatterCalls.Load(), result.Content)
		}
	})
}

func TestExecutorSkipsObserverWhoseCompleteRequestFrameIsCredentialBearing(t *testing.T) {
	const secret = `ObserverFrame","input":{}`
	var hookCalls, toolCalls atomic.Int32
	hook := &countingHook{pre: func(context.Context, Request, string) (HookResult, error) {
		hookCalls.Add(1)
		return HookResult{}, nil
	}}
	descriptor := Descriptor{
		Name: "ObserverFrame", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			toolCalls.Add(1)
			return Output{Content: "unexpected"}, nil
		},
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{hook},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "observer_id", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if !result.IsError || result.Code != "hook_failed" || hookCalls.Load() != 0 || toolCalls.Load() != 0 {
		t.Fatalf("unsafe observer request crossed hook boundary: result=%#v hooks=%d tools=%d", result, hookCalls.Load(), toolCalls.Load())
	}
}

func TestExecutorRejectsCompleteCredentialBearingPermissionProjection(t *testing.T) {
	const secret = `alpha","MatchContents":["omega`
	var authorizeCalls, toolCalls atomic.Int32
	descriptor := Descriptor{
		Name: "PermissionProjection", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		ProjectPermission: func(any, json.RawMessage) (permission.Request, error) {
			return permission.Request{Content: "alpha", MatchContents: []string{"omega"}}, nil
		},
		Call: func(context.Context, CallContext, any) (Output, error) {
			toolCalls.Add(1)
			return Output{Content: "unexpected"}, nil
		},
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry,
		Authorizer: authorizerFunc(func(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
			authorizeCalls.Add(1)
			return permission.Decision{Kind: permission.DecisionAllow, Input: request.Input}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "permission_projection", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if !result.IsError || result.Code != "semantic_invalid" ||
		strings.Contains(fmt.Sprintf("%+v", result), secret) {
		t.Fatalf("unsafe permission projection result = %#v", result)
	}
	if authorizeCalls.Load() != 0 || toolCalls.Load() != 0 {
		t.Fatalf("unsafe permission projection reached authorizer/tool: authorize=%d tool=%d", authorizeCalls.Load(), toolCalls.Load())
	}
}

func TestExecutorSuppressesCredentialReconstructedByCompleteResultFraming(t *testing.T) {
	const secret = `alpha","is_error":false`
	capture := &resultPayloadCaptureHook{}
	descriptor := Descriptor{
		Name: "ResultFraming", Source: SourceMCP,
		Validate:            func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call:                func(context.Context, CallContext, any) (Output, error) { return Output{Content: "alpha"}, nil },
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "result_framing", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if result.Content != "" || !result.ContentSuppressed || strings.Contains(fmt.Sprintf("%+v", result), secret) {
		t.Fatalf("complete unsafe result was returned: %#v", result)
	}
	if capture.postResult.ToolUseID != "" {
		t.Fatalf("complete unsafe result reached post hook: %#v", capture.postResult)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("returned terminal result reconstructed credential: %s", encoded)
	}
}

func TestBashGuardExhaustionPropagatesExplicitSuppression(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("foreground shell assertion is Unix-specific")
	}
	const candidates = "*#~!@$%^&_-+=:;,.?0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz/|"
	literals := make([]string, 0, len(candidates))
	for index := range candidates {
		literals = append(literals, candidates[index:index+1])
	}
	set := redact.New(literals...)
	descriptor := bashDescriptor(t.TempDir(), "/bin/sh", nil, []string{"PATH=/usr/bin:/bin"})
	value, err := descriptor.Validate(json.RawMessage(`{"command":"printf '*'"}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := descriptor.Call(t.Context(), CallContext{
		ProjectOutput: func(value string, rawTruncated bool, limit int) (string, bool, bool) {
			return projectCredentialOutput(set, value, rawTruncated, limit)
		},
	}, value)
	if err != nil {
		t.Fatal(err)
	}
	if output.Content != "" || !output.ContentSuppressed {
		t.Fatalf("guard-exhausted Bash output = %#v", output)
	}
}

func TestExecutorSemanticallySanitizesOpaqueMetadataBeforePostHook(t *testing.T) {
	const secret = "metadata-credential-sentinel"
	type opaqueMetadata struct {
		Secret string `json:"secret"`
	}
	capture := &resultPayloadCaptureHook{}
	descriptor := Descriptor{
		Name: "OpaqueMetadata", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "ok", Metadata: map[string]any{
				"struct": opaqueMetadata{Secret: secret},
				"alias":  mapAlias{"nested": secret},
			}}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "opaque_metadata", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if result.IsError || result.Metadata == nil {
		t.Fatalf("metadata suppression rewrote successful execution: %+v", result)
	}
	for label, value := range map[string]any{"returned": result.Metadata, "post hook": capture.postResult.Metadata} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("%s metadata exposed credential: %s", label, encoded)
		}
	}
}

type mapAlias map[string]string

type panickingMetadata struct{}

func (panickingMetadata) MarshalJSON() ([]byte, error) {
	panic("credential-bearing marshaler panic")
}

func TestExecutorSuppressesPanickingAndCyclicMetadataWithoutChangingSuccess(t *testing.T) {
	cycle := make(map[string]any)
	cycle["self"] = cycle
	descriptor := Descriptor{
		Name: "UnsafeMetadata", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "ok", Metadata: map[string]any{
				"cycle": cycle,
				"panic": panickingMetadata{},
			}}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		CredentialSanitizer: redact.New("metadata-credential"),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "unsafe_metadata", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "ok" || result.Metadata != nil {
		t.Fatalf("unsafe metadata did not fail closed independently: %+v", result)
	}
}

func TestExecutorSourceSetWithOpaqueSessionSanitizerSuppressesEveryPayload(t *testing.T) {
	const (
		sessionSecret = "<opaque&session>"
		sourceSecret  = "source-provider-credential"
	)
	capture := &resultPayloadCaptureHook{}
	descriptor := Descriptor{
		Name: "OpaqueComposition", Source: SourceMCP,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{
				Content: sessionSecret + sourceSecret,
				Metadata: map[string]any{"opaque": struct {
					Secret string `json:"secret"`
				}{Secret: sourceSecret}},
			}, nil
		},
		CredentialSanitizer: redact.New(sourceSecret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture},
		Sanitize: func(value string) string { return strings.ReplaceAll(value, sessionSecret, "[legacy-redacted]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "opaque_composition", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if result.Content != "" || !result.ContentSuppressed || result.Metadata != nil ||
		result.OriginalInput != nil || result.ExecutedInput != nil || result.PermissionInput != nil {
		t.Fatalf("returned payload was not completely suppressed: %#v", result)
	}
	if capture.postResult.ToolUseID != "opaque_composition" ||
		capture.postResult.Content != "" || !capture.postResult.ContentSuppressed ||
		capture.postResult.Metadata != nil || capture.postResult.OriginalInput != nil ||
		capture.postResult.ExecutedInput != nil {
		t.Fatalf("observer did not receive the fully suppressed projection: %#v", capture.postResult)
	}
	if capture.postRequest.Input != nil || capture.postRequest.AssistantID != "" {
		t.Fatalf("post observer request retained payload: %#v", capture.postRequest)
	}

	var calls atomic.Int32
	descriptor.Call = func(context.Context, CallContext, any) (Output, error) {
		calls.Add(1)
		return Output{Content: "unexpected"}, nil
	}
	registry, _ = NewRegistry(descriptor)
	executor, _ = NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		Sanitize: func(value string) string { return strings.ReplaceAll(value, sessionSecret, "[legacy-redacted]") },
	})
	reflected := executor.Execute(t.Context(), Request{
		ID: "source_input", Name: descriptor.Name,
		Input: json.RawMessage(`{"value":"` + sourceSecret + `"}`),
	})
	if !reflected.IsError || reflected.Code != "structural_invalid" || calls.Load() != 0 ||
		reflected.OriginalInput != nil || !reflected.ContentSuppressed {
		t.Fatalf("source credential input was not rejected and suppressed: %#v calls=%d", reflected, calls.Load())
	}
	escapedLegacy := executor.Execute(t.Context(), Request{
		ID: "legacy_escaped_input", Name: descriptor.Name,
		Input: json.RawMessage(`{"value":"\u003copaque\u0026session\u003e"}`),
	})
	if !escapedLegacy.IsError || escapedLegacy.Code != "structural_invalid" || calls.Load() != 0 ||
		escapedLegacy.OriginalInput != nil || !escapedLegacy.ContentSuppressed {
		t.Fatalf("escaped opaque credential input was not rejected: %#v calls=%d", escapedLegacy, calls.Load())
	}
}

func TestExecutorLegacySanitizerDropsOpaqueMetadata(t *testing.T) {
	const secret = "legacy-metadata-credential"
	capture := &resultPayloadCaptureHook{}
	descriptor := Descriptor{
		Name: "LegacyMetadata", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "ok", Metadata: map[string]any{
				"struct": struct {
					Secret string `json:"secret"`
				}{Secret: secret},
				"raw": json.RawMessage(`{"value":"legacy-metadata-credential"}`),
			}}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{}, Hooks: []Hook{capture},
		Sanitize: func(value string) string { return strings.ReplaceAll(value, secret, "[legacy-redacted]") },
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "legacy_metadata", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	for label, metadata := range map[string]map[string]any{"returned": result.Metadata, "post hook": capture.postResult.Metadata} {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("%s legacy metadata exposed credential: %s", label, encoded)
		}
	}
}

func TestExecutorSanitizesPostHookWarningsAndAskReason(t *testing.T) {
	const secret = "source-hook-credential"
	capture := &resultPayloadCaptureHook{postErr: errors.New(strings.Repeat("x", 1995) + secret + " trailing")}
	var observedAsk string
	askHook := &staticAuthorityHook{ask: "approve " + secret}
	descriptor := Descriptor{
		Name: "HookAuthority", Source: SourceMCP,
		Validate:            func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call:                func(context.Context, CallContext, any) (Output, error) { return Output{Content: "ok"}, nil },
		CredentialSanitizer: redact.New(secret),
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry,
		Authorizer: authorizerFunc(func(_ context.Context, request permission.Request, _ permission.Rebuild) (permission.Decision, error) {
			observedAsk = request.HookAsk
			return permission.Decision{Kind: permission.DecisionAllow, Input: request.Input}, nil
		}),
		Hooks: []Hook{askHook, capture},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{ID: "hook_authority", Name: descriptor.Name, Input: json.RawMessage(`{}`)})
	if strings.Contains(observedAsk, secret) {
		t.Fatalf("approval projection exposed source credential: %q", observedAsk)
	}
	encoded, err := json.Marshal(result.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), secret[:len(secret)/2]) {
		t.Fatalf("post-hook warning exposed source credential: %s", encoded)
	}
}

type staticAuthorityHook struct {
	ask  string
	deny string
}

func (hook *staticAuthorityHook) Pre(context.Context, Request, string) (HookResult, error) {
	return HookResult{AskReason: hook.ask, DenyReason: hook.deny}, nil
}
func (*staticAuthorityHook) Post(context.Context, Request, Result) error    { return nil }
func (*staticAuthorityHook) Failure(context.Context, Request, Result) error { return nil }

func TestShellEnvironmentUsesClosedNonSecretAllowlist(t *testing.T) {
	toolchain := t.TempDir()
	input := []string{
		"PATH=/attacker", "BASH_ENV=/tmp/inject", "ENV=/tmp/inject", "BASH_FUNC_evil%%=() { touch /tmp/owned; }",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib", "LD_PRELOAD=/tmp/evil.so", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_GLOBAL=/tmp/evil", "GIT_SSH_COMMAND=/tmp/evil", "PYTHONPATH=/tmp/evil",
		"NODE_OPTIONS=--require=/tmp/evil", "DOTENV_CONFIG_PATH=/tmp/credentials.env", "NPM_CONFIG_USERCONFIG=/tmp/npmrc",
		"AZURE_OPENAI_ENDPOINT=https://private.example", "AZURE_OPENAI_SUBSCRIPTION_KEY=secret",
		"AWS_ACCESS_KEY_ID=access", "AWS_SECRET_ACCESS_KEY=secret", "AWS_SESSION_TOKEN=session",
		"AZURE_CLIENT_SECRET=secret", "GOOGLE_APPLICATION_CREDENTIALS=/tmp/google.json", "AGENTX_API_KEY=secret",
		"OPENAI_API_KEY=secret", "SSH_AUTH_SOCK=/tmp/agent.sock", "GPG_AGENT_INFO=/tmp/gpg.sock",
		"DOCKER_HOST=unix:///tmp/docker.sock", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus", "XDG_RUNTIME_DIR=/tmp/runtime",
		"KUBECONFIG=/tmp/kubeconfig", "AGENTX_CONFIG_DIR=/tmp/agentx", "CODEX_HOME=/tmp/codex", "SAFE=value",
		"LANG=C", "LANG=en_US.UTF-8", "LC_ALL=C", "TERM=xterm-256color", "NO_COLOR=1",
		"GOOS=darwin", "GOARCH=arm64", "NODE_ENV=test", "PYTEST_DISABLE_PLUGIN_AUTOLOAD=1",
		"HOME=" + toolchain, "GOCACHE=" + toolchain, "GOMODCACHE=relative/cache", "PATH=" + toolchain + string(os.PathListSeparator) + ".:/attacker",
	}
	result := sanitizedEnvironment(input)
	actual := make(map[string]string, len(result))
	for _, entry := range result {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed sanitized entry %q", entry)
		}
		if _, duplicate := actual[name]; duplicate {
			t.Fatalf("duplicate sanitized variable %q: %q", name, result)
		}
		actual[name] = value
	}
	for _, forbidden := range []string{
		"BASH_ENV", "ENV", "BASH_FUNC_evil%%", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "PYTHONPATH", "NODE_OPTIONS", "DOTENV_CONFIG_PATH",
		"NPM_CONFIG_USERCONFIG", "AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_SUBSCRIPTION_KEY", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION_CREDENTIALS",
		"AGENTX_API_KEY", "OPENAI_API_KEY", "SSH_AUTH_SOCK", "GPG_AGENT_INFO", "DOCKER_HOST",
		"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "KUBECONFIG", "AGENTX_CONFIG_DIR", "CODEX_HOME", "SAFE",
	} {
		if _, retained := actual[forbidden]; retained {
			t.Errorf("sanitized environment retained hostile variable %q", forbidden)
		}
	}
	wantAllowed := map[string]string{
		"LANG": "en_US.UTF-8", "LC_ALL": "C", "TERM": "xterm-256color", "NO_COLOR": "1",
		"GOOS": "darwin", "GOARCH": "arm64", "NODE_ENV": "test", "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1",
		"HOME": toolchain, "GOCACHE": toolchain,
	}
	for name, want := range wantAllowed {
		if got := actual[name]; got != want {
			t.Errorf("sanitized %s = %q, want %q", name, got, want)
		}
	}
	if got := actual["PATH"]; !strings.Contains(got, toolchain) || strings.Contains(got, "/attacker") || strings.Contains(got, string(os.PathListSeparator)+"."+string(os.PathListSeparator)) {
		t.Fatalf("safe developer PATH = %q", got)
	}
	if _, retained := actual["GOMODCACHE"]; retained {
		t.Fatal("relative GOMODCACHE was retained")
	}
}

func TestBashToolSuppressesAmbientStartupCode(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "startup-ran")
	startup := filepath.Join(workspace, "startup.sh")
	if err := os.WriteFile(startup, []byte("touch "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor := bashDescriptor(workspace, "/bin/bash", nil, []string{"BASH_ENV=" + startup, "PATH=/bin"})
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(context.Background(), Request{ID: "startup", Name: "Bash", Input: json.RawMessage(`{"command":"printf safe"}`)})
	if result.IsError || result.Content != "safe" {
		t.Fatalf("bash execution failed: %#v", result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambient startup code executed; stat error=%v", err)
	}
}

func TestBackgroundBashRetainsSelectedCommandRunner(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	workspace := t.TempDir()
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks"), task.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	runner := &recordingShellRunner{}
	descriptor := bashDescriptor(workspace, "/bin/bash", manager, []string{"PATH=/bin:/usr/bin"}, runner)
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(t.Context(), Request{
		ID: "background-runner", Name: "Bash",
		Input: json.RawMessage(`{"command":"printf background","run_in_background":true}`),
	})
	if result.IsError {
		t.Fatalf("background launch failed: %+v", result)
	}
	var launched task.Record
	if err := json.Unmarshal([]byte(result.Content), &launched); err != nil {
		t.Fatal(err)
	}
	var poll task.PollResult
	var output strings.Builder
	for attempts := 0; attempts < 3; attempts++ {
		poll, err = manager.Poll(t.Context(), launched.ID, poll.NextOffset, true, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(poll.Output)
		if poll.Task.Status.Terminal() {
			break
		}
	}
	if runner.calls.Load() != 1 || poll.Task.Status != task.StatusCompleted || output.String() != "background" {
		t.Fatalf("background command runner was dropped: calls=%d poll=%+v", runner.calls.Load(), poll)
	}
}

func TestBashNeverClaimsGenericReadOnlyAuthorization(t *testing.T) {
	descriptor := bashDescriptor(t.TempDir(), "/bin/bash", nil, nil)
	for _, command := range []string{"git status", "file -f/etc/passwd", "file --files-from=/etc/passwd", "pwd"} {
		value, err := descriptor.Validate(json.RawMessage(`{"command":` + strconv.Quote(command) + `}`))
		if err != nil {
			t.Fatalf("validate %q: %v", command, err)
		}
		classification := descriptor.Classify(value)
		if classification.ReadOnly || classification.ConcurrencySafe {
			t.Fatalf("%q was auto-authorizable: %#v", command, classification)
		}
	}
}

func TestExactScalarCompatibilityRepair(t *testing.T) {
	t.Parallel()
	type input struct {
		Flag  bool    `json:"flag"`
		Count int     `json:"count"`
		Ratio float64 `json:"ratio"`
	}
	tests := []struct {
		raw     string
		want    input
		wantErr bool
	}{
		{raw: `{"flag":"true","count":"30","ratio":"-5.25"}`, want: input{Flag: true, Count: 30, Ratio: -5.25}},
		{raw: `{"flag":false,"count":30,"ratio":1}`, want: input{Count: 30, Ratio: 1}},
		{raw: `{"flag":"False","count":30,"ratio":1}`, wantErr: true},
		{raw: `{"flag":true,"count":"1e3","ratio":1}`, wantErr: true},
		{raw: `{"flag":true,"count":"+3","ratio":1}`, wantErr: true},
		{raw: `null`, wantErr: true},
	}
	for _, test := range tests {
		var got input
		err := decodeStrict(json.RawMessage(test.raw), &got)
		if (err != nil) != test.wantErr {
			t.Errorf("decodeStrict(%s) error = %v, wantErr %v", test.raw, err, test.wantErr)
			continue
		}
		if err == nil && got != test.want {
			t.Errorf("decodeStrict(%s) = %+v, want %+v", test.raw, got, test.want)
		}
	}
}

func TestEmptyObjectToolsRejectJSONNull(t *testing.T) {
	descriptor := taskListDescriptor(nil)
	if _, err := descriptor.Validate(json.RawMessage(`null`)); err == nil {
		t.Fatal("object-shaped tool accepted JSON null")
	}
	if _, err := descriptor.Validate(json.RawMessage(`{}`)); err != nil {
		t.Fatalf("empty JSON object was rejected: %v", err)
	}
}

func TestExecutorExactlyOnceForConcurrentDuplicateID(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	descriptor := Descriptor{
		Name: "Once", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			calls.Add(1)
			time.Sleep(30 * time.Millisecond)
			return Output{Content: "one"}, nil
		},
	}
	registry, _ := NewRegistry(descriptor)
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	request := Request{ID: "same", Name: "Once", Input: json.RawMessage(`{}`)}
	results := make(chan Result, 2)
	go func() { results <- executor.Execute(context.Background(), request) }()
	go func() { results <- executor.Execute(context.Background(), request) }()
	first, second := <-results, <-results
	if calls.Load() != 1 || first.Content != "one" || second.Content != "one" || first.ToolUseID != second.ToolUseID {
		t.Fatalf("duplicate executed more than once: calls=%d first=%+v second=%+v", calls.Load(), first, second)
	}
}

func TestExecutorDuplicateObserversReceiveDeeplyImmutableResults(t *testing.T) {
	t.Parallel()
	descriptor := Descriptor{
		Name: "Metadata", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, CallContext, any) (Output, error) {
			return Output{Content: "one", Metadata: map[string]any{
				"nested": map[string]any{"value": "authoritative"},
				"items":  []any{map[string]any{"value": "stable"}},
			}}, nil
		},
	}
	registry, _ := NewRegistry(descriptor)
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	request := Request{ID: "immutable-result", Name: "Metadata", Input: json.RawMessage(`{}`)}
	first := executor.Execute(t.Context(), request)
	first.Metadata["nested"].(map[string]any)["value"] = "mutated"
	first.Metadata["items"].([]any)[0].(map[string]any)["value"] = "mutated"
	second := executor.Execute(t.Context(), request)
	if got := second.Metadata["nested"].(map[string]any)["value"]; got != "authoritative" {
		t.Fatalf("nested metadata mutation crossed observer boundary: %v", got)
	}
	if got := second.Metadata["items"].([]any)[0].(map[string]any)["value"]; got != "stable" {
		t.Fatalf("slice metadata mutation crossed observer boundary: %v", got)
	}
}

func TestSchedulerSafeGroupsAndUnsafeBarriers(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var events []string
	record := func(event string) { mu.Lock(); events = append(events, event); mu.Unlock() }
	descriptor := func(name string, safe bool, delay time.Duration) Descriptor {
		return Descriptor{
			Name: name, Source: SourceBuiltin,
			Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
			Classify: func(any) permission.Classification {
				return permission.Classification{ReadOnly: safe, ConcurrencySafe: safe}
			},
			Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
				record("start:" + call.ToolUseID)
				time.Sleep(delay)
				record("end:" + call.ToolUseID)
				return Output{Content: call.ToolUseID}, nil
			},
		}
	}
	registry, err := NewRegistry(descriptor("Safe", true, 30*time.Millisecond), descriptor("Unsafe", false, 5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	scheduler := NewScheduler(executor, registry, 10)
	requests := []Request{
		{ID: "a", Name: "Safe", Input: json.RawMessage(`{}`)}, {ID: "b", Name: "Safe", Input: json.RawMessage(`{}`)},
		{ID: "c", Name: "Unsafe", Input: json.RawMessage(`{}`)}, {ID: "d", Name: "Safe", Input: json.RawMessage(`{}`)},
	}
	results := scheduler.Execute(context.Background(), requests)
	if len(results) != len(requests) {
		t.Fatalf("result count = %d, want %d: %+v", len(results), len(requests), results)
	}
	unsettled := map[string]struct{}{"a": {}, "b": {}, "c": {}, "d": {}}
	for _, result := range results {
		if _, ok := unsettled[result.ToolUseID]; !ok {
			t.Fatalf("unexpected or duplicate terminal result: %+v", results)
		}
		delete(unsettled, result.ToolUseID)
	}
	if len(unsettled) != 0 {
		t.Fatalf("accepted requests without terminal results: %v", unsettled)
	}
	mu.Lock()
	joined := strings.Join(events, ",")
	mu.Unlock()
	endA, endB := strings.Index(joined, "end:a"), strings.Index(joined, "end:b")
	startC, endC, startD := strings.Index(joined, "start:c"), strings.Index(joined, "end:c"), strings.Index(joined, "start:d")
	if endA < 0 || endB < 0 || startC < endA || startC < endB || startD < endC {
		t.Fatalf("barrier order violated: %s", joined)
	}
}

func TestSchedulerReturnsSafeResultsInCompletionOrder(t *testing.T) {
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	witnessStarted := make(chan struct{})
	descriptor := Descriptor{
		Name: "Safe", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(_ context.Context, call CallContext, _ any) (Output, error) {
			switch call.ToolUseID {
			case "slow":
				close(slowStarted)
				<-releaseSlow
			case "fast":
				<-slowStarted
			case "witness":
				// With two workers, this request cannot start until the fast
				// worker has published its result and asked for another job.
				close(witnessStarted)
				close(releaseSlow)
			}
			return Output{Content: call.ToolUseID}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(executor, registry, 2)
	results := scheduler.Execute(t.Context(), []Request{
		{ID: "slow", Name: "Safe", Input: json.RawMessage(`{}`)},
		{ID: "fast", Name: "Safe", Input: json.RawMessage(`{}`)},
		{ID: "witness", Name: "Safe", Input: json.RawMessage(`{}`)},
	})
	select {
	case <-witnessStarted:
	default:
		t.Fatal("witness request did not execute")
	}
	if len(results) != 3 || results[0].ToolUseID != "fast" {
		t.Fatalf("terminal results lost completion order: %+v", results)
	}
	seen := make(map[string]int, len(results))
	for _, result := range results {
		seen[result.ToolUseID]++
	}
	if seen["slow"] != 1 || seen["fast"] != 1 || seen["witness"] != 1 || len(seen) != 3 {
		t.Fatalf("terminal result pairing changed: %+v", results)
	}
}

func TestSchedulerPreflightPanicsSettleEveryRequest(t *testing.T) {
	tests := []struct {
		name  string
		stage string
	}{
		{name: "validator", stage: "validate"},
		{name: "classifier", stage: "classify"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var panickingCalls atomic.Int32
			var followingCalls atomic.Int32
			panicking := Descriptor{
				Name: "Panicking", Source: SourcePlugin,
				Validate: func(json.RawMessage) (any, error) {
					if test.stage == "validate" {
						panic("validator panic")
					}
					return struct{}{}, nil
				},
				Classify: func(any) permission.Classification {
					if test.stage == "classify" {
						panic("classifier panic")
					}
					return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
				},
				Call: func(context.Context, CallContext, any) (Output, error) {
					panickingCalls.Add(1)
					return Output{Content: "must not execute"}, nil
				},
			}
			following := Descriptor{
				Name: "Following", Source: SourceBuiltin,
				Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
				Classify: func(any) permission.Classification {
					return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
				},
				Call: func(context.Context, CallContext, any) (Output, error) {
					followingCalls.Add(1)
					return Output{Content: "following completed"}, nil
				},
			}
			registry, err := NewRegistry(panicking, following)
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
			if err != nil {
				t.Fatal(err)
			}
			scheduler := NewScheduler(executor, registry, 2)
			results := scheduler.Execute(t.Context(), []Request{
				{ID: "panicking", Name: "Panicking", Input: json.RawMessage(`{}`)},
				{ID: "following", Name: "Following", Input: json.RawMessage(`{}`)},
			})
			if len(results) != 2 || results[0].ToolUseID != "panicking" || results[1].ToolUseID != "following" {
				t.Fatalf("accepted requests were not paired exactly once: %+v", results)
			}
			if !results[0].IsError || results[0].Code != "execution_failed" || !strings.Contains(results[0].Content, "panic was contained") {
				t.Fatalf("preflight panic escaped common terminal mapping: %+v", results[0])
			}
			if results[1].IsError || results[1].Content != "following completed" {
				t.Fatalf("request after preflight panic did not settle normally: %+v", results[1])
			}
			if panickingCalls.Load() != 0 || followingCalls.Load() != 1 {
				t.Fatalf("unexpected executions: panicking=%d following=%d", panickingCalls.Load(), followingCalls.Load())
			}
		})
	}
}

func TestSchedulerPreflightCannotMutateAcceptedInput(t *testing.T) {
	var validations atomic.Int32
	descriptor := Descriptor{
		Name: "Safe", Source: SourcePlugin,
		Validate: func(raw json.RawMessage) (any, error) {
			if validations.Add(1) == 1 {
				index := strings.Index(string(raw), "safe")
				if index < 0 {
					return nil, errors.New("preflight fixture lost its original value")
				}
				raw[index] = 'X'
			}
			var input map[string]string
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, err
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(_ context.Context, _ CallContext, input any) (Output, error) {
			return Output{Content: input.(map[string]string)["value"]}, nil
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(executor, registry, 1)
	request := Request{ID: "immutable", Name: "Safe", Input: json.RawMessage(`{"value":"safe"}`)}
	results := scheduler.Execute(t.Context(), []Request{request})
	if len(results) != 1 || results[0].IsError || results[0].Content != "safe" {
		t.Fatalf("preflight mutation reached execution: %+v", results)
	}
	if string(request.Input) != `{"value":"safe"}` || string(results[0].OriginalInput) != `{"value":"safe"}` {
		t.Fatalf("accepted input bytes changed: request=%s result=%s", request.Input, results[0].OriginalInput)
	}
}

func TestSchedulerBashFailureMarksRunningAndQueuedSiblings(t *testing.T) {
	runningStarted := make(chan struct{})
	safeClassification := func(any) permission.Classification {
		return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
	}
	bash := Descriptor{
		Name: "Bash", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: safeClassification,
		Call: func(ctx context.Context, _ CallContext, _ any) (Output, error) {
			select {
			case <-runningStarted:
				return Output{}, invocationError("execution_failed", "shell failed")
			case <-ctx.Done():
				return Output{}, ctx.Err()
			}
		},
	}
	peer := Descriptor{
		Name: "Peer", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: safeClassification,
		Call: func(ctx context.Context, call CallContext, _ any) (Output, error) {
			if call.ToolUseID == "running" {
				close(runningStarted)
			}
			<-ctx.Done()
			return Output{}, ctx.Err()
		},
	}
	registry, err := NewRegistry(bash, peer)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(executor, registry, 2)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	results := scheduler.Execute(ctx, []Request{
		{ID: "bash", Name: "Bash", Input: json.RawMessage(`{}`)},
		{ID: "running", Name: "Peer", Input: json.RawMessage(`{}`)},
		{ID: "queued", Name: "Peer", Input: json.RawMessage(`{}`)},
	})
	if len(results) != 3 || results[0].Code != "execution_failed" {
		t.Fatalf("Bash failure did not retain its terminal result: %+v", results)
	}
	for _, index := range []int{1, 2} {
		if result := results[index]; !result.IsError || result.Code != "sibling_error" || result.Content != siblingErrorContent {
			t.Fatalf("sibling %q terminal result = %+v", result.ToolUseID, result)
		}
	}
}

func TestSchedulerCachesAndSanitizesUndispatchedSiblingCancellation(t *testing.T) {
	const secret = "sibling"
	runningStarted := make(chan struct{})
	safeClassification := func(any) permission.Classification {
		return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
	}
	bash := Descriptor{
		Name: "Bash", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: safeClassification,
		Call: func(ctx context.Context, _ CallContext, _ any) (Output, error) {
			select {
			case <-runningStarted:
				return Output{}, invocationError("execution_failed", "shell failed")
			case <-ctx.Done():
				return Output{}, ctx.Err()
			}
		},
	}
	var calls sync.Map
	peer := Descriptor{
		Name: "Peer", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: safeClassification,
		Call: func(ctx context.Context, call CallContext, _ any) (Output, error) {
			calls.Store(call.ToolUseID, true)
			if call.ToolUseID == "running_cached" {
				close(runningStarted)
			}
			<-ctx.Done()
			return Output{}, ctx.Err()
		},
	}
	registry, err := NewRegistry(bash, peer)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Registry: registry, Authorizer: allowAuthorizer{},
		CredentialSanitizer: redact.New(secret),
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(executor, registry, 2)
	requests := []Request{
		{ID: "bash_cached", Name: "Bash", Input: json.RawMessage(`{}`)},
		{ID: "running_cached", Name: "Peer", Input: json.RawMessage(`{}`)},
		{ID: "queued_cached", Name: "Peer", Input: json.RawMessage(`{}`)},
	}
	results := scheduler.Execute(t.Context(), requests)
	var queued Result
	for _, result := range results {
		if result.ToolUseID == "queued_cached" {
			queued = result
		}
	}
	if !queued.IsError || queued.Code != "" || queued.Content != "" || !queued.ContentSuppressed ||
		strings.Contains(queued.Content, secret) {
		t.Fatalf("queued sibling result = %+v", queued)
	}
	if _, executed := calls.Load("queued_cached"); executed {
		t.Fatal("undispatched sibling implementation ran")
	}
	duplicate := executor.Execute(t.Context(), requests[2])
	if duplicate.Code != queued.Code || duplicate.Content != queued.Content || !duplicate.IsError {
		t.Fatalf("duplicate did not reuse terminal sibling result: first=%+v duplicate=%+v", queued, duplicate)
	}
	if _, executed := calls.Load("queued_cached"); executed {
		t.Fatal("duplicate sibling request executed after synthetic settlement")
	}
}

func TestSchedulerParentCancellationRemainsGeneric(t *testing.T) {
	started := make(chan struct{})
	descriptor := Descriptor{
		Name: "Safe", Source: SourceBuiltin,
		Validate: func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(ctx context.Context, _ CallContext, _ any) (Output, error) {
			close(started)
			<-ctx.Done()
			return Output{}, ctx.Err()
		},
	}
	registry, err := NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(executor, registry, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan []Result, 1)
	go func() {
		done <- scheduler.Execute(ctx, []Request{
			{ID: "running", Name: "Safe", Input: json.RawMessage(`{}`)},
			{ID: "queued", Name: "Safe", Input: json.RawMessage(`{}`)},
		})
	}()
	<-started
	cancel()
	select {
	case results := <-done:
		if len(results) != 2 {
			t.Fatalf("parent cancellation left requests unsettled: %+v", results)
		}
		for _, result := range results {
			if result.Code != "cancelled" {
				t.Fatalf("parent cancellation was misclassified: %+v", results)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not settle parent cancellation")
	}
}

func TestResultStorePersistsOnceAndReusesReplacement(t *testing.T) {
	t.Parallel()
	store, err := NewResultStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", 200)
	first := store.apply("id", content, 100)
	second := store.apply("id", strings.Repeat("y", 200), 100)
	if first != second || !strings.Contains(first, "persisted-output") || strings.Contains(first, strings.Repeat("y", 5)) {
		t.Fatalf("replacement was not stable: first=%q second=%q", first, second)
	}
	b, err := os.ReadFile(store.pathFor("id"))
	if err != nil || string(b) != content {
		t.Fatalf("persisted content mismatch: %v %d", err, len(b))
	}
	descriptor := resultReadDescriptor(store)
	value, err := descriptor.Validate(json.RawMessage(`{"tool_use_id":"id","offset":100,"limit":100}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := descriptor.Call(context.Background(), CallContext{}, value)
	if err != nil || output.Content != strings.Repeat("x", 100) || output.Metadata["truncated"] != false {
		t.Fatalf("bounded result retrieval output=%#v err=%v", output, err)
	}
}

func TestResultStoreValidatorInspectsExactNewlineFramedIndex(t *testing.T) {
	root := t.TempDir()
	set := redact.New("}\n")
	store, err := NewResultStoreWithValidator(root, func(raw []byte) error {
		matched, inspectErr := set.JSONContains(raw)
		if inspectErr != nil {
			return inspectErr
		}
		if matched {
			return errors.New("credential reflected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := store.apply("newline_guard", strings.Repeat("safe", 1000), 100)
	if !strings.Contains(replacement, "persistence is unavailable") {
		t.Fatalf("unsafe index did not fail persistence: %q", replacement)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed index validation left artifacts: %#v", entries)
	}
}

func TestResultStoreValidatorInspectsExistingPhysicalIndexFrames(t *testing.T) {
	root := t.TempDir()
	entry := storedResult{
		ID: "existing", File: resultFilename("existing"), Size: 0,
		Digest: strings.Repeat("0", sha256.Size*2),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	if err := os.WriteFile(filepath.Join(root, resultIndexFilename), line, 0o600); err != nil {
		t.Fatal(err)
	}
	set := redact.New("}\n")
	_, err = NewResultStoreWithValidator(root, func(raw []byte) error {
		matched, inspectErr := set.JSONContains(raw)
		if inspectErr != nil {
			return inspectErr
		}
		if matched {
			return errors.New("credential reflected")
		}
		return nil
	})
	if err == nil {
		t.Fatal("unsafe existing result index was accepted")
	}
}

func TestResultStoreValidatorRejectsCredentialBearingReopenedContent(t *testing.T) {
	const secret = "reopened-persisted-credential"
	root := t.TempDir()
	legacy, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	content := "prefix-" + secret + strings.Repeat("-tail", 1000)
	if replacement := legacy.apply("legacy_content", content, 64); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("legacy content was not persisted: %q", replacement)
	}

	set := redact.New(secret)
	resumed, err := NewResultStoreWithValidator(root, func(raw []byte) error {
		matched, inspectErr := set.JSONContains(raw)
		if inspectErr != nil {
			return inspectErr
		}
		if matched {
			return errors.New("credential reflected")
		}
		return nil
	})
	if err == nil || resumed != nil || !strings.Contains(err.Error(), "validate existing persisted tool result") {
		t.Fatalf("credential-bearing persisted content reopened: store=%#v err=%v", resumed, err)
	}
}

func TestResultStoreContainsPanickingValidator(t *testing.T) {
	store, err := NewResultStoreWithValidator(t.TempDir(), func([]byte) error {
		panic("validator failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := store.apply("validator_panic", strings.Repeat("safe", 1000), 64)
	if !strings.Contains(replacement, "persistence is unavailable") {
		t.Fatalf("panicking validator did not fail closed: %q", replacement)
	}
}

func TestResultStoreResumeIntegrityAndAliasRejection(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := NewResultStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("resume", 100)
	if replacement := store.apply("CaseID", content, 100); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("initial persistence failed: %q", replacement)
	}
	resumed, err := NewResultStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := resumed.read(context.Background(), "CaseID", 0, len(content))
	if err != nil || got != content {
		t.Fatalf("resumed verified result = %q, %v", got, err)
	}
	if resumed.pathFor("CaseID") == resumed.pathFor("caseid") {
		t.Fatal("case-distinct tool IDs collided")
	}

	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("do-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	plantedID := "planted"
	if err := os.Symlink(secret, resumed.pathFor(plantedID)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	replacement := resumed.apply(plantedID, strings.Repeat("safe", 100), 100)
	if !strings.Contains(replacement, "remaining output unavailable") || strings.Contains(replacement, "do-not-read") {
		t.Fatalf("planted alias was trusted: %q", replacement)
	}
	if _, _, _, err := resumed.read(context.Background(), plantedID, 0, 100); err == nil {
		t.Fatal("unindexed planted alias was readable")
	}
	if err := os.Remove(resumed.pathFor("CaseID")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, resumed.pathFor("CaseID")); err != nil {
		t.Skipf("hard links unavailable across test roots: %v", err)
	}
	if _, _, _, err := resumed.read(context.Background(), "CaseID", 0, 100); err == nil {
		t.Fatal("hard-linked replacement passed persisted-result integrity")
	}
}

func TestResultStoreFailureReturnsOnlyBoundedPreview(t *testing.T) {
	root := t.TempDir()
	store, err := NewResultStore(filepath.Join(root, "results"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store.directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.directory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("secret-sized-output", 100_000)
	replacement := store.apply("failure", content, 100)
	if len(replacement) > resultPreview+500 || !strings.Contains(replacement, "remaining output unavailable") || replacement == content {
		t.Fatalf("persistence failure returned unbounded output: bytes=%d", len(replacement))
	}
}

func TestResultStoreRejectsDirectSymlinkAndNonDirectoryRootsBeforeChmod(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "results")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		before, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewResultStore(link); err == nil {
			t.Fatal("direct symlink result root was accepted")
		}
		after, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})

	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results")
		if err := os.WriteFile(path, []byte("sentinel"), 0o640); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewResultStore(path); err == nil {
			t.Fatal("regular-file result root was accepted")
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("non-directory mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})
}

func TestResultStoreRejectsPostConstructionDirectoryReplacement(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "results")
	store, err := NewResultStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	existingContent := strings.Repeat("owned", 100)
	if replacement := store.apply("existing", existingContent, 100); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("initial persistence failed: %q", replacement)
	}

	original := filepath.Join(parent, "original-results")
	if err := os.Rename(directory, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.read(context.Background(), "existing", 0, len(existingContent)); err == nil {
		t.Fatal("read followed a replacement result directory")
	}

	newContent := strings.Repeat("must-not-be-written", 100)
	replacement := store.apply("new", newContent, 100)
	if !strings.Contains(replacement, "remaining output unavailable") {
		t.Fatalf("write through replacement root did not fail closed: %q", replacement)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement directory received result-store writes: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(original, resultFilename("new"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed write escaped into renamed original root: %v", err)
	}
}

func TestResultStorePinnedRootDetectsMidOperationDirectorySwap(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "results")
	store, err := NewResultStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.openVerifiedRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	original := filepath.Join(parent, "original-results")
	if err := os.Rename(directory, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("pinned-write", []byte("owned-root-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.verifyRoot(root); err == nil {
		t.Fatal("post-operation verification missed a directory swap")
	}
	if _, err := os.Stat(filepath.Join(original, "pinned-write")); err != nil {
		t.Fatalf("pinned root did not retain the operation: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("mid-operation swap redirected a write: %+v", entries)
	}
}

func TestCoreReadEditRejectsStaleAndThenSucceeds(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: workspace, Mode: permission.ModeAcceptEdits})
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: evaluator})
	readRaw, _ := json.Marshal(readInput{FilePath: path})
	read := executor.Execute(context.Background(), Request{ID: "read-1", Name: "Read", Input: readRaw})
	if read.IsError {
		t.Fatalf("read failed: %+v", read)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editRaw, _ := json.Marshal(editInput{FilePath: path, OldString: "old", NewString: "new"})
	stale := executor.Execute(context.Background(), Request{ID: "edit-stale", Name: "Edit", Input: editRaw})
	if !stale.IsError || stale.Code != "stale_file" {
		t.Fatalf("stale edit was not rejected: %+v", stale)
	}

	readRaw, _ = json.Marshal(readInput{FilePath: path})
	if result := executor.Execute(context.Background(), Request{ID: "read-2", Name: "Read", Input: readRaw}); result.IsError {
		t.Fatal(result.Content)
	}
	editRaw, _ = json.Marshal(editInput{FilePath: path, OldString: "external", NewString: "updated"})
	if result := executor.Execute(context.Background(), Request{ID: "edit-ok", Name: "Edit", Input: editRaw}); result.IsError {
		t.Fatalf("edit failed: %+v", result)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "updated\n" {
		t.Fatalf("file = %q", b)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %o", info.Mode().Perm())
	}
}

func TestAtomicWriteDoesNotClobberConcurrentSubstitution(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.txt")
	displaced := filepath.Join(workspace, "displaced.txt")
	if err := os.WriteFile(target, []byte("authorized-old"), 0o600); err != nil {
		t.Fatal(err)
	}
	rooted, err := openWorkspaceParent(workspace, target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rooted.Close()
	_, err = atomicWriteRoot(rooted, []byte("agent-replacement"), true, func(*os.File) error {
		if err := os.Rename(target, displaced); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("concurrent-owner"), 0o600)
	})
	if err == nil {
		t.Fatal("concurrent target substitution was silently overwritten")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != "concurrent-owner" {
		t.Fatalf("concurrent file was clobbered: %q, %v", got, readErr)
	}
	old, readErr := os.ReadFile(displaced)
	if readErr != nil || string(old) != "authorized-old" {
		t.Fatalf("authorized old inode was damaged: %q, %v", old, readErr)
	}
}

func TestCoreFileToolsAreConfinedByWorkspaceRoot(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	outsideRaw, _ := json.Marshal(readInput{FilePath: secret})
	outsideResult := executor.Execute(context.Background(), Request{ID: "outside-read", Name: "Read", Input: outsideRaw})
	if !outsideResult.IsError || outsideResult.Code != "structural_invalid" {
		t.Fatalf("outside read crossed workspace root: %+v", outsideResult)
	}
	link := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkRaw, _ := json.Marshal(readInput{FilePath: link})
	linkResult := executor.Execute(context.Background(), Request{ID: "symlink-read", Name: "Read", Input: linkRaw})
	if !linkResult.IsError || linkResult.Code != "execution_failed" {
		t.Fatalf("symlink escape crossed workspace root: %+v", linkResult)
	}
}

func TestSearchToolsSkipDynamicCredentialFileAndAgentControlDirectory(t *testing.T) {
	workspace := t.TempDir()
	credential := filepath.Join(workspace, "config", "prod.settings")
	control := filepath.Join(workspace, ".agentx", "mcp.json")
	for _, path := range []string{credential, control} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace, ProtectedPaths: []string{credential}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	grepInput, _ := json.Marshal(grepInput{Pattern: "needle-secret", Path: workspace, OutputMode: "content"})
	result := executor.Execute(t.Context(), Request{ID: "protected-search", Name: "Grep", Input: grepInput})
	if result.IsError {
		t.Fatal(result.Content)
	}
	if strings.Contains(result.Content, "prod.settings") || strings.Contains(result.Content, ".agentx") || strings.Contains(result.Content, "needle-secret") {
		t.Fatalf("recursive search exposed protected control data: %q", result.Content)
	}
}

func TestCoreReadRejectsHardLinkAlias(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, ".env.private")
	alias := filepath.Join(workspace, "ordinary.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, alias); err != nil {
		t.Skipf("hard links unavailable across test roots: %v", err)
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(readInput{FilePath: alias})
	result := executor.Execute(context.Background(), Request{ID: "hard-link-read", Name: "Read", Input: raw})
	if !result.IsError || result.Code != "execution_failed" || strings.Contains(result.Content, "secret") {
		t.Fatalf("hard-link alias exposed protected content: %+v", result)
	}
}

func TestLargeFileObservationIsBoundedAndCannotEnableWholeFileEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte("prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maximumEditBytes+1); err != nil {
		t.Fatal(err)
	}
	tracker := NewFileTracker()
	if err := tracker.Observe(path); err != nil {
		t.Fatalf("bounded observation failed: %v", err)
	}
	if err := tracker.RequireCurrent(path); err == nil || !strings.Contains(err.Error(), "safe edit limit") {
		t.Fatalf("large file unexpectedly became editable: %v", err)
	}
}

func TestGrepCollectsOnlyRequestedWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "many.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("match\n", 2_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	limit := 10
	output, err := grepCall(context.Background(), CallContext{}, grepInput{
		Pattern: "match", Path: root, OutputMode: "content", LineNumbers: true, HeadLimit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := output.Metadata
	if metadata["count"] != 10 || metadata["truncated"] != true {
		t.Fatalf("unexpected bounded grep metadata: %#v", metadata)
	}
	if strings.Count(output.Content, ":match\n") != limit {
		t.Fatalf("grep retained an unbounded result set: %q", output.Content)
	}
}

func TestGrepSkipsHardLinkedProtectedAlias(t *testing.T) {
	workspace := t.TempDir()
	secret := filepath.Join(t.TempDir(), ".env.private")
	alias := filepath.Join(workspace, "ordinary.txt")
	if err := os.WriteFile(secret, []byte("AZURE_OPENAI_API_KEY=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, alias); err != nil {
		t.Skipf("hard links unavailable across test roots: %v", err)
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	limit := 10
	raw, _ := json.Marshal(grepInput{Pattern: "secret-value", Path: workspace, OutputMode: "content", HeadLimit: &limit})
	result := executor.Execute(context.Background(), Request{ID: "grep-hardlink", Name: "Grep", Input: raw})
	if result.IsError || strings.Contains(result.Content, "secret-value") || strings.Contains(result.Content, alias) {
		t.Fatalf("grep exposed hard-linked protected data: %+v", result)
	}
}

func TestRecursiveSearchNeverExpandsDirectoryPermissionIntoProtectedFiles(t *testing.T) {
	workspace := t.TempDir()
	secretPath := filepath.Join(workspace, ".env.private")
	if err := os.WriteFile(secretPath, []byte("AZURE_OPENAI_SUBSCRIPTION_KEY=never-return-this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "ordinary.txt"), []byte("ordinary searchable text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: allowAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	limit := 10
	grepRaw, _ := json.Marshal(grepInput{Pattern: "never-return-this", Path: workspace, OutputMode: "content", HeadLimit: &limit})
	grepResult := executor.Execute(context.Background(), Request{ID: "grep-protected-child", Name: "Grep", Input: grepRaw})
	if grepResult.IsError || strings.Contains(grepResult.Content, "never-return-this") || strings.Contains(grepResult.Content, ".env.private") {
		t.Fatalf("recursive grep exposed protected child: %+v", grepResult)
	}
	globRaw, _ := json.Marshal(globInput{Pattern: "**/*", Path: workspace, Limit: 10})
	globResult := executor.Execute(context.Background(), Request{ID: "glob-protected-child", Name: "Glob", Input: globRaw})
	if globResult.IsError || strings.Contains(globResult.Content, ".env.private") {
		t.Fatalf("recursive glob exposed protected child: %+v", globResult)
	}
}

func TestTaskMutationsAreSchedulerBarriers(t *testing.T) {
	manager, err := task.Open(t.TempDir(), task.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	tests := []struct {
		descriptor Descriptor
		value      any
	}{
		{descriptor: taskStopDescriptor(manager), value: taskStopInput{TaskID: "b00000000"}},
		{descriptor: taskCreateDescriptor(manager), value: taskCreateInput{Subject: "s", Description: "d", ActiveForm: "a"}},
		{descriptor: taskUpdateDescriptor(manager), value: taskUpdateInput{TaskID: "t00000000"}},
	}
	for _, test := range tests {
		classification := test.descriptor.Classify(test.value)
		if classification.ConcurrencySafe || classification.ReadOnly {
			t.Fatalf("%s mutation classified concurrent: %#v", test.descriptor.Name, classification)
		}
	}
}

func TestDeniedBashNeverSpawns(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "spawned")
	registry, err := NewCoreRegistry(CoreOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: workspace, Mode: permission.ModeDontAsk})
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := NewExecutor(ExecutorOptions{Registry: registry, Authorizer: evaluator})
	raw, _ := json.Marshal(bashInput{Command: "touch " + marker})
	result := executor.Execute(context.Background(), Request{ID: "bash-denied", Name: "Bash", Input: raw})
	if !result.IsError || result.Code != "denied" {
		t.Fatalf("bash was not denied: %+v", result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied shell spawned, stat=%v", err)
	}
}
