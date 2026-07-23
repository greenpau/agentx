package testing

import (
	"context"
	"encoding/json"
	stdtesting "testing"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/tool"
)

func TestEnvironmentEnabledFailsClosed(t *stdtesting.T) {
	tests := []struct {
		name string
		env  []string
		want bool
	}{
		{name: "absent"},
		{name: "production", env: []string{"NODE_ENV=production"}},
		{name: "test", env: []string{"NODE_ENV=test"}, want: true},
		{name: "case insensitive key", env: []string{"node_env=test"}, want: true},
		{name: "conflicting duplicate", env: []string{"NODE_ENV=test", "NODE_ENV=production"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *stdtesting.T) {
			if got := EnvironmentEnabled(test.env); got != test.want {
				t.Fatalf("EnvironmentEnabled(%q) = %v, want %v", test.env, got, test.want)
			}
		})
	}
}

func TestPermissionDescriptorHasExactTestContract(t *stdtesting.T) {
	descriptor := PermissionDescriptor([]string{"NODE_ENV=test"})
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := registry.Resolve(PermissionToolName)
	if !ok {
		t.Fatal("test descriptor was not enabled")
	}
	for _, raw := range []string{``, `null`, `[]`, `{"unexpected":true}`, `{} {}`} {
		if _, err := resolved.Validate(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid input %q was accepted", raw)
		}
	}
	value, err := resolved.Validate(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	classification := resolved.Classify(value)
	if !classification.ReadOnly || !classification.ConcurrencySafe || classification.Destructive || classification.OpenWorld {
		t.Fatalf("classification = %+v", classification)
	}
	request, err := resolved.ProjectPermission(value, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.MandatoryAsk != "Run test?" {
		t.Fatalf("mandatory prompt = %q", request.MandatoryAsk)
	}
	output, err := resolved.Call(context.Background(), tool.CallContext{}, value)
	if err != nil {
		t.Fatal(err)
	}
	if output.Content != "TestingPermission executed successfully" {
		t.Fatalf("output = %q", output.Content)
	}
}

func TestPermissionDescriptorHasNoProductionRegistryFootprint(t *stdtesting.T) {
	registry, err := tool.NewRegistry(PermissionDescriptor([]string{"NODE_ENV=production"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Resolve(PermissionToolName); ok || len(registry.Descriptors()) != 0 {
		t.Fatal("testing tool was exposed outside the test environment")
	}
}

func TestPermissionDescriptorAlwaysUsesOrdinaryApprovalBoundary(t *stdtesting.T) {
	registry, err := tool.NewRegistry(PermissionDescriptor([]string{"NODE_ENV=test"}))
	if err != nil {
		t.Fatal(err)
	}
	approvals := 0
	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace:       t.TempDir(),
		Mode:            permission.ModeBypass,
		BypassAvailable: true,
		Approver: func(_ context.Context, request permission.ApprovalRequest) (permission.ApprovalResponse, error) {
			approvals++
			if request.Tool != PermissionToolName || request.Reason != "Run test?" || !request.Mandatory {
				t.Fatalf("approval request = %+v", request)
			}
			return permission.ApprovalResponse{Kind: permission.DecisionAllow}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	result := executor.Execute(context.Background(), tool.Request{ID: "test-permission", Name: PermissionToolName, Input: json.RawMessage(`{}`)})
	if result.IsError || result.Content != "TestingPermission executed successfully" || approvals != 1 {
		t.Fatalf("result = %+v, approvals = %d", result, approvals)
	}
}
