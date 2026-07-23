package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/tool"
)

func TestExtensionToolHookProjectsCompleteEnvelopes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell capture assertion is Unix-specific")
	}
	events := []extensions.HookEventName{
		extensions.HookPreToolUse,
		extensions.HookPostToolUse,
		extensions.HookPostToolUseFailure,
		extensions.HookPermissionDenied,
	}
	descriptors := make([]extensions.HookDescriptor, 0, len(events))
	for _, event := range events {
		descriptors = append(descriptors, extensions.HookDescriptor{
			ID: string(event), Event: event, Kind: extensions.HookKindCommand, Shell: "sh",
			Command: `IFS= read -r payload; printf '%s' "$payload"`, Source: extensions.SourceUser, Timeout: time.Second,
		})
	}
	snapshot := extensions.NewHookManagerForEvents(events...).Reload(descriptors)
	runner := extensions.NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	hook := extensionToolHook{runner: runner, snapshot: snapshot, sessionID: "session", transcriptPath: "/tmp/transcript", workspace: runner.ProjectRoot, permissionMode: "default"}
	request := tool.Request{ID: "use-1", Name: "Echo", Input: json.RawMessage(`{"value":"original"}`)}
	success := tool.Result{ToolUseID: request.ID, Name: request.Name, Content: "ok", ExecutedInput: json.RawMessage(`{"value":"executed"}`), Metadata: map[string]any{"count": 1}}
	failure := tool.Result{ToolUseID: request.ID, Name: request.Name, Content: "cancelled by user", IsError: true, Code: "cancelled", ExecutedInput: success.ExecutedInput}
	denied := tool.Result{ToolUseID: request.ID, Name: request.Name, Content: "managed policy", IsError: true, Code: "denied"}

	tests := []struct {
		event  extensions.HookEventName
		result *tool.Result
		check  func(*testing.T, map[string]any)
	}{
		{event: extensions.HookPreToolUse, check: func(t *testing.T, envelope map[string]any) {
			assertHookInputValue(t, envelope, "original")
			if _, exists := envelope["tool_response"]; exists {
				t.Fatal("pre-tool envelope included a terminal response")
			}
		}},
		{event: extensions.HookPostToolUse, result: &success, check: func(t *testing.T, envelope map[string]any) {
			assertHookInputValue(t, envelope, "executed")
			response, ok := envelope["tool_response"].(map[string]any)
			if !ok || response["content"] != "ok" || response["is_error"] != false {
				t.Fatalf("post-tool response = %#v", envelope["tool_response"])
			}
		}},
		{event: extensions.HookPostToolUseFailure, result: &failure, check: func(t *testing.T, envelope map[string]any) {
			assertHookInputValue(t, envelope, "executed")
			if envelope["error"] != "cancelled by user" || envelope["is_interrupt"] != true {
				t.Fatalf("failure envelope = %#v", envelope)
			}
		}},
		{event: extensions.HookPermissionDenied, result: &denied, check: func(t *testing.T, envelope map[string]any) {
			assertHookInputValue(t, envelope, "original")
			if envelope["reason"] != "managed policy" {
				t.Fatalf("denial envelope = %#v", envelope)
			}
		}},
	}
	for _, test := range tests {
		t.Run(string(test.event), func(t *testing.T) {
			aggregate, err := hook.dispatch(t.Context(), test.event, request, test.result)
			if err != nil {
				t.Fatal(err)
			}
			if len(aggregate.Results) != 1 || aggregate.Results[0].Err != nil {
				t.Fatalf("hook execution = %#v", aggregate)
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(aggregate.Results[0].Stdout), &envelope); err != nil {
				t.Fatalf("decode captured envelope: %v\n%s", err, aggregate.Results[0].Stdout)
			}
			if envelope["hook_event_name"] != string(test.event) || envelope["tool_name"] != "Echo" || envelope["tool_use_id"] != "use-1" {
				t.Fatalf("common tool envelope = %#v", envelope)
			}
			test.check(t, envelope)
		})
	}
}

func TestHookConditionMatcherUsesValidatedPermissionProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	registry := newHookEchoRegistry(t, nil)
	snapshot := extensions.NewHookManagerForEvents(extensions.HookPreToolUse).Reload([]extensions.HookDescriptor{{
		ID: "conditional", Event: extensions.HookPreToolUse, Matcher: "Echo", If: "Echo(safe *)",
		Kind: extensions.HookKindCommand, Shell: "sh",
		Command: `printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"condition matched"}}'`,
		Source:  extensions.SourceUser, Timeout: time.Second,
	}})
	runner := extensions.NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.ConditionMatcher = hookConditionMatcher(registry)
	hook := extensionToolHook{runner: runner, snapshot: snapshot, sessionID: "session", workspace: runner.ProjectRoot}

	matched, err := hook.Pre(t.Context(), tool.Request{ID: "safe", Name: "Echo", Input: json.RawMessage(`{"value":"safe command"}`)}, "Echo")
	if err != nil {
		t.Fatal(err)
	}
	if matched.DenyReason != "condition matched" {
		t.Fatalf("matching condition result = %#v", matched)
	}
	unmatched, err := hook.Pre(t.Context(), tool.Request{ID: "other", Name: "Echo", Input: json.RawMessage(`{"value":"different"}`)}, "Echo")
	if err != nil {
		t.Fatal(err)
	}
	if unmatched.DenyReason != "" {
		t.Fatalf("nonmatching condition ran: %#v", unmatched)
	}
}

func TestPermissionRequestHooksComposeWithBaseAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertions are Unix-specific")
	}

	t.Run("base deny never reaches approval hook", func(t *testing.T) {
		root := t.TempDir()
		permissionMarker := filepath.Join(root, "permission-requested")
		denialEnvelope := filepath.Join(root, "permission-denied.json")
		descriptors := []extensions.HookDescriptor{
			preToolDecisionHook("allow"),
			permissionRequestHook(root, `printf x > "$AGENTX_PLUGIN_ROOT/permission-requested"; printf '%s' '{"decision":"approve","reason":"hook approved"}'`),
			{ID: "denied", Event: extensions.HookPermissionDenied, Kind: extensions.HookKindCommand, Shell: "sh", Command: `IFS= read -r payload; printf '%s' "$payload" > "$AGENTX_PLUGIN_ROOT/permission-denied.json"`, Source: extensions.SourcePlugin, PluginRoot: root, Timeout: time.Second},
		}
		rule, err := permission.ParseRule("Echo", permission.EffectDeny, "managed", true)
		if err != nil {
			t.Fatal(err)
		}
		calls := &atomic.Int32{}
		result := executeHookPermissionCase(t, descriptors, []permission.Rule{rule}, json.RawMessage(`{"value":"safe"}`), calls)
		if !result.IsError || result.Code != "denied" || calls.Load() != 0 {
			t.Fatalf("base denial was bypassed: %#v calls=%d", result, calls.Load())
		}
		if _, err := os.Stat(permissionMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("PermissionRequest ran after terminal base deny: %v", err)
		}
		data, err := os.ReadFile(denialEnvelope)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		assertHookInputValue(t, envelope, "safe")
		if envelope["reason"] != "request matches a deny rule" {
			t.Fatalf("permission-denied envelope = %#v", envelope)
		}
	})

	t.Run("pre-tool ask remains stronger than base allow", func(t *testing.T) {
		root := t.TempDir()
		descriptors := []extensions.HookDescriptor{
			preToolDecisionHook("ask"),
			permissionRequestHook(root, `printf x > "$AGENTX_PLUGIN_ROOT/permission-requested"; printf '%s' '{"decision":"approve","reason":"reviewed"}'`),
		}
		rule, err := permission.ParseRule("Echo", permission.EffectAllow, "user", false)
		if err != nil {
			t.Fatal(err)
		}
		calls := &atomic.Int32{}
		result := executeHookPermissionCase(t, descriptors, []permission.Rule{rule}, json.RawMessage(`{"value":"safe"}`), calls)
		if result.IsError || result.Content != "safe" || calls.Load() != 1 {
			t.Fatalf("pre-tool ask was not resolved compositionally: %#v calls=%d", result, calls.Load())
		}
		if _, err := os.Stat(filepath.Join(root, "permission-requested")); err != nil {
			t.Fatalf("base allow bypassed hook ask: %v", err)
		}
	})

	t.Run("base ask can be approved", func(t *testing.T) {
		root := t.TempDir()
		descriptor := permissionRequestHook(root, `printf x > "$AGENTX_PLUGIN_ROOT/permission-requested"; printf '%s' '{"decision":"approve","reason":"hook approved"}'`)
		calls := &atomic.Int32{}
		result := executeHookPermissionCase(t, []extensions.HookDescriptor{descriptor}, nil, json.RawMessage(`{"value":"safe"}`), calls)
		if result.IsError || result.Content != "safe" || calls.Load() != 1 {
			t.Fatalf("hook-approved ask = %#v calls=%d", result, calls.Load())
		}
		if _, err := os.Stat(filepath.Join(root, "permission-requested")); err != nil {
			t.Fatalf("permission hook was not reached: %v", err)
		}
	})

	t.Run("edited approval is revalidated and reauthorized", func(t *testing.T) {
		root := t.TempDir()
		descriptor := permissionRequestHook(root, `printf '%s' '{"decision":"approve","reason":"replace","hookSpecificOutput":{"hook_event_name":"PermissionRequest","updatedInput":{"value":"blocked"}}}'`)
		deniedDescriptor := extensions.HookDescriptor{ID: "denied", Event: extensions.HookPermissionDenied, Kind: extensions.HookKindCommand, Shell: "sh", Command: `IFS= read -r payload; printf '%s' "$payload" > "$AGENTX_PLUGIN_ROOT/permission-denied.json"`, Source: extensions.SourcePlugin, PluginRoot: root, Timeout: time.Second}
		rule, err := permission.ParseRule("Echo(blocked)", permission.EffectDeny, "managed", true)
		if err != nil {
			t.Fatal(err)
		}
		calls := &atomic.Int32{}
		result := executeHookPermissionCase(t, []extensions.HookDescriptor{descriptor, deniedDescriptor}, []permission.Rule{rule}, json.RawMessage(`{"value":"safe"}`), calls)
		if !result.IsError || result.Code != "denied" || calls.Load() != 0 {
			t.Fatalf("edited hook approval bypassed reauthorization: %#v calls=%d", result, calls.Load())
		}
		if !strings.Contains(result.Content, "deny rule") {
			t.Fatalf("unexpected denial reason: %q", result.Content)
		}
		data, err := os.ReadFile(filepath.Join(root, "permission-denied.json"))
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		assertHookInputValue(t, envelope, "blocked")
	})
}

func TestPermissionApproverUsesFirstDecisiveResponder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertions are Unix-specific")
	}
	newHook := func(t *testing.T, command string) extensionToolHook {
		t.Helper()
		root := t.TempDir()
		descriptor := permissionRequestHook(root, command)
		runner := extensions.NewHookRunner()
		runner.ProjectRoot = root
		registry := newHookEchoRegistry(t, &atomic.Int32{})
		runner.ConditionMatcher = hookConditionMatcher(registry)
		return extensionToolHook{
			runner: runner, snapshot: extensions.NewHookManagerForEvents(runtimeHookEvents()...).Reload([]extensions.HookDescriptor{descriptor}),
			sessionID: "session", workspace: root, permissionMode: "default",
		}
	}
	request := permission.ApprovalRequest{Tool: "Echo", ToolUseID: "use", Input: json.RawMessage(`{"value":"safe"}`)}

	t.Run("host approval wins a slow hook", func(t *testing.T) {
		hook := newHook(t, `sleep 1; printf '%s' '{"decision":"deny","reason":"late deny"}'`)
		approver := hook.permissionApprover(func(context.Context, permission.ApprovalRequest) (permission.ApprovalResponse, error) {
			return permission.ApprovalResponse{Kind: permission.DecisionAllow, Reason: "host approved"}, nil
		})
		started := time.Now()
		response, err := approver(t.Context(), request)
		if err != nil || response.Kind != permission.DecisionAllow || time.Since(started) > 500*time.Millisecond {
			t.Fatalf("host did not win first-decisive race: response=%+v err=%v duration=%s", response, err, time.Since(started))
		}
	})

	t.Run("hook denial cancels a waiting host", func(t *testing.T) {
		hook := newHook(t, `printf '%s' '{"decision":"deny","reason":"hook denied"}'`)
		cancelled := make(chan struct{})
		approver := hook.permissionApprover(func(ctx context.Context, _ permission.ApprovalRequest) (permission.ApprovalResponse, error) {
			<-ctx.Done()
			close(cancelled)
			return permission.ApprovalResponse{}, ctx.Err()
		})
		response, err := approver(t.Context(), request)
		if err != nil || response.Kind != permission.DecisionDeny {
			t.Fatalf("hook did not win first-decisive race: response=%+v err=%v", response, err)
		}
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("losing host responder was not cancelled")
		}
	})
}

func permissionRequestHook(root, command string) extensions.HookDescriptor {
	return extensions.HookDescriptor{
		ID: "permission", Event: extensions.HookPermissionRequest, Matcher: "Echo",
		Kind: extensions.HookKindCommand, Shell: "sh", Command: command,
		Source: extensions.SourcePlugin, PluginRoot: root, Timeout: time.Second,
	}
}

func preToolDecisionHook(decision string) extensions.HookDescriptor {
	return extensions.HookDescriptor{
		ID: "pre-" + decision, Event: extensions.HookPreToolUse, Matcher: "Echo",
		Kind: extensions.HookKindCommand, Shell: "sh",
		Command: `printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"` + decision + `","permissionDecisionReason":"pre ` + decision + `"}}'`,
		Source:  extensions.SourceUser, Timeout: time.Second,
	}
}

func executeHookPermissionCase(t *testing.T, descriptors []extensions.HookDescriptor, rules []permission.Rule, input json.RawMessage, calls *atomic.Int32) tool.Result {
	t.Helper()
	registry := newHookEchoRegistry(t, calls)
	snapshot := extensions.NewHookManagerForEvents(runtimeHookEvents()...).Reload(descriptors)
	runner := extensions.NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.ConditionMatcher = hookConditionMatcher(registry)
	hook := extensionToolHook{runner: runner, snapshot: snapshot, sessionID: "session", workspace: runner.ProjectRoot, permissionMode: "default"}
	approver := hook.permissionApprover(nil)
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: runner.ProjectRoot, Rules: rules, Approver: approver})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator, Hooks: []tool.Hook{hook}})
	if err != nil {
		t.Fatal(err)
	}
	return executor.Execute(t.Context(), tool.Request{ID: "use", Name: "Echo", Input: input})
}

type hookEchoInput struct {
	Value string `json:"value"`
}

func newHookEchoRegistry(t *testing.T, calls *atomic.Int32) *tool.Registry {
	t.Helper()
	descriptor := tool.Descriptor{
		Name: "Echo", Source: tool.SourceBuiltin,
		Validate: func(raw json.RawMessage) (any, error) {
			var input hookEchoInput
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				return nil, err
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF {
				return nil, errors.New("input contains trailing JSON")
			}
			if strings.TrimSpace(input.Value) == "" {
				return nil, errors.New("value is required")
			}
			return input, nil
		},
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			return permission.Request{Input: raw, Content: value.(hookEchoInput).Value}, nil
		},
		Call: func(_ context.Context, _ tool.CallContext, value any) (tool.Output, error) {
			if calls != nil {
				calls.Add(1)
			}
			return tool.Output{Content: value.(hookEchoInput).Value}, nil
		},
	}
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func assertHookInputValue(t *testing.T, envelope map[string]any, expected string) {
	t.Helper()
	input, ok := envelope["tool_input"].(map[string]any)
	if !ok || input["value"] != expected {
		t.Fatalf("tool input = %#v, expected value %q", envelope["tool_input"], expected)
	}
}
