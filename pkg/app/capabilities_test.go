package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/tool"
)

func TestSkillPermissionScopeNarrowsShellContent(t *testing.T) {
	scope := &skillPermissionScope{}
	if err := scope.Install([]string{"Bash(git status:*)", "Read"}); err != nil {
		t.Fatal(err)
	}
	status, err := permission.AnalyzeShell("git status --short", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Allows(permission.Request{Tool: "Bash", Content: status.Command, Shell: &status}) {
		t.Fatal("expected matching command family to remain in scope")
	}
	push, err := permission.AnalyzeShell("git push", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if scope.Allows(permission.Request{Tool: "Bash", Content: push.Command, Shell: &push}) {
		t.Fatal("content pattern must not widen to the complete Bash tool")
	}
	if !scope.Allows(permission.Request{Tool: "Read", Content: "README.md"}) {
		t.Fatal("whole-tool rule should match")
	}
	if scope.Allows(permission.Request{Tool: "Write", Content: "README.md"}) {
		t.Fatal("unlisted tool should be denied")
	}
}

func TestSkillPermissionScopeRequiresEveryShellSegment(t *testing.T) {
	scope := &skillPermissionScope{}
	if err := scope.Install([]string{"Bash(git status:*)"}); err != nil {
		t.Fatal(err)
	}
	analysis, err := permission.AnalyzeShell("git status && git push", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if scope.Allows(permission.Request{Tool: "Bash", Content: analysis.Command, Shell: &analysis}) {
		t.Fatal("one matching segment must not authorize an unmatched sibling")
	}
}

func TestSkillPermissionScopeFailsClosedOnMalformedRules(t *testing.T) {
	scope := &skillPermissionScope{}
	if err := scope.Install([]string{"Bash(git status:*"}); err == nil {
		t.Fatal("expected malformed rule error")
	}
	if scope.Allows(permission.Request{Tool: "Read", Content: "README.md"}) {
		t.Fatal("malformed active scope must fail closed")
	}
	if !scope.Allows(permission.Request{Tool: "Skill"}) {
		t.Fatal("Skill must remain callable so the model can select a replacement scope")
	}
}

func TestSkillDescriptorReplacesRestrictedScopeWithUnrestrictedSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill := func(name, body string) {
		t.Helper()
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill("restricted", "---\nallowed-tools: [Read]\n---\nrestricted instructions\n")
	writeSkill("unrestricted", "unrestricted instructions\n")
	snapshot := extensions.NewManager().Reload([]extensions.Root{{Path: root, Source: extensions.SourceProject}})
	scope := &skillPermissionScope{}
	descriptor := skillDescriptor(snapshot, scope, "", "")
	call := func(name string) {
		t.Helper()
		value, err := descriptor.Validate(json.RawMessage(fmt.Sprintf(`{"skill":%q}`, name)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := descriptor.Call(t.Context(), tool.CallContext{}, value); err != nil {
			t.Fatal(err)
		}
	}

	call("restricted")
	if !scope.Allows(permission.Request{Tool: "Read", Content: "README.md"}) {
		t.Fatal("restricted skill did not allow its declared tool")
	}
	if scope.Allows(permission.Request{Tool: "Write", Content: "README.md"}) {
		t.Fatal("restricted skill unexpectedly allowed an undeclared tool")
	}

	call("unrestricted")
	if !scope.Allows(permission.Request{Tool: "Write", Content: "README.md"}) {
		t.Fatal("unrestricted replacement skill inherited the prior restricted scope")
	}
}

func TestCapabilityAdapterCarriesPermissionDenialEvidence(t *testing.T) {
	descriptor := tool.Descriptor{
		Name: "Echo", Source: tool.SourceBuiltin,
		InputSchema: map[string]any{"type": "object"},
		Validate: func(raw json.RawMessage) (any, error) {
			var value map[string]any
			return value, json.Unmarshal(raw, &value)
		},
		Call: func(context.Context, tool.CallContext, any) (tool.Output, error) {
			t.Fatal("denied capability executed")
			return tool.Output{}, nil
		},
	}
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := permission.ParseRule("Echo", permission.EffectDeny, "managed", true)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: t.TempDir(), Rules: []permission.Rule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &capabilityAdapter{registry: registry, scheduler: tool.NewScheduler(executor, registry, 1)}
	results := adapter.Execute(t.Context(), []engine.CapabilityCall{{ID: "call_denied", Name: "Echo", Arguments: json.RawMessage(`{"value":"blocked"}`)}})
	if len(results) != 1 || results[0].PermissionDenial == nil {
		t.Fatalf("permission denial evidence = %#v", results)
	}
	denial := results[0].PermissionDenial
	if denial.ToolName != "Echo" || denial.ToolUseID != "call_denied" || string(denial.ToolInput) != `{"value":"blocked"}` {
		t.Fatalf("permission denial = %#v", denial)
	}
}

func TestCapabilityAdapterPreservesExplicitOutputSuppression(t *testing.T) {
	descriptor := tool.Descriptor{
		Name: "Suppressed", Source: tool.SourceBuiltin,
		InputSchema: map[string]any{"type": "object"},
		Validate: func(raw json.RawMessage) (any, error) {
			var value map[string]any
			return value, json.Unmarshal(raw, &value)
		},
		Call: func(context.Context, tool.CallContext, any) (tool.Output, error) {
			return tool.Output{ContentSuppressed: true, Metadata: map[string]any{"reason": "guard exhausted"}}, nil
		},
	}
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := permission.ParseRule(descriptor.Name, permission.EffectAllow, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{Workspace: t.TempDir(), Rules: []permission.Rule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &capabilityAdapter{registry: registry, scheduler: tool.NewScheduler(executor, registry, 1)}
	results := adapter.Execute(t.Context(), []engine.CapabilityCall{{
		ID: "call_suppressed", Name: descriptor.Name, Arguments: json.RawMessage(`{}`),
	}})
	if len(results) != 1 || results[0].Content != "" || !results[0].ContentSuppressed || results[0].IsError {
		t.Fatalf("suppressed capability projection = %#v", results)
	}
}

func TestCapabilityAdapterOmitsSemanticCredentialReflections(t *testing.T) {
	const (
		schemaSecret = "abc<def"
		nameSecret   = "credentialname"
	)
	descriptors := []tool.Descriptor{
		{
			Name: "SafeName", Description: "safe description", Source: tool.SourceBuiltin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string", "description": schemaSecret},
				},
			},
		},
		{
			Name: "SafeSchema", Description: "reflects " + schemaSecret, Source: tool.SourceBuiltin,
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name: "Tool_" + nameSecret, Description: "safe", Source: tool.SourceBuiltin,
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name: "Retained", Description: "safe", Source: tool.SourceBuiltin,
			InputSchema: map[string]any{"type": "object"},
		},
	}
	for index := range descriptors {
		descriptors[index].Validate = func(json.RawMessage) (any, error) { return struct{}{}, nil }
		descriptors[index].Call = func(context.Context, tool.CallContext, any) (tool.Output, error) {
			return tool.Output{}, nil
		}
	}
	registry, err := tool.NewRegistry(descriptors...)
	if err != nil {
		t.Fatal(err)
	}
	credentials := redact.New(schemaSecret, nameSecret)
	adapter := &capabilityAdapter{registry: registry, credentials: credentials}
	schemas := adapter.Schemas()
	if len(schemas) != 1 || schemas[0].Name != "Retained" {
		t.Fatalf("credential-bearing descriptors were projected: %#v", schemas)
	}
	encoded, err := json.Marshal(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var semantic any
	if err := json.Unmarshal(encoded, &semantic); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(semantic), schemaSecret) || strings.Contains(fmt.Sprint(semantic), nameSecret) {
		t.Fatalf("provider schema projection retained credential: %s", encoded)
	}
}

func TestCapabilityAdapterOmitsCredentialReconstructedByToolJSONFraming(t *testing.T) {
	const secret = `Foo","description":"bar`
	descriptor := tool.Descriptor{
		Name: "Foo", Description: "bar", Source: tool.SourceBuiltin,
		InputSchema: map[string]any{"type": "object"},
		Validate:    func(json.RawMessage) (any, error) { return struct{}{}, nil },
		Call: func(context.Context, tool.CallContext, any) (tool.Output, error) {
			return tool.Output{}, nil
		},
	}
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &capabilityAdapter{registry: registry, credentials: redact.New(secret)}
	if schemas := adapter.Schemas(); len(schemas) != 0 {
		t.Fatalf("framing-reconstructed credential descriptor escaped: %#v", schemas)
	}
}

func TestNormalizeErrorCodeUsesClosedVocabulary(t *testing.T) {
	admitted := []string{
		"call_batch_interrupted",
		"cancelled",
		"denied",
		"execution_failed",
		"hook_failed",
		"interrupted",
		"malformed_input",
		"malformed_result",
		"missing_terminal_result",
		"permission_denied",
		"permission_failed",
		"semantic_invalid",
		"sibling_error",
		"stale_file",
		"structural_invalid",
		"timeout",
		"unavailable",
		"unknown_tool",
		"user_interrupted",
	}
	for _, code := range admitted {
		t.Run("admitted_"+code, func(t *testing.T) {
			if got := normalizeErrorCode(code); got != code {
				t.Fatalf("normalizeErrorCode(%q) = %q, want exact admitted code", code, got)
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
			if got := normalizeErrorCode(code); got != "tool_error" {
				t.Fatalf("normalizeErrorCode(%q) = %q, want tool_error", code, got)
			}
		})
	}
}
