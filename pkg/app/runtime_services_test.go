package app

import (
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/features"
	"github.com/greenpau/agentx/pkg/mcp"
	"github.com/greenpau/agentx/pkg/observability"
)

func TestMCPFeatureAndDoctorRequireConnectedEligibleServer(t *testing.T) {
	connected := testMCPDescriptor(t, mcp.Config{Name: "connected", Transport: mcp.TransportStdio, Command: "unused", Scope: mcp.ScopeUser}, mcp.StateConnected)
	failed := testMCPDescriptor(t, mcp.Config{Name: "failed", Transport: mcp.TransportStdio, Command: "unused", Scope: mcp.ScopeUser}, mcp.StateFailed)
	disabled := testMCPDescriptor(t, mcp.Config{Name: "disabled", Transport: mcp.TransportStdio, Command: "unused", Scope: mcp.ScopeUser, Disabled: true}, mcp.StateDisabled)
	unsupported := testMCPDescriptor(t, mcp.Config{Name: "remote", Transport: mcp.TransportHTTP, URL: "https://example.test/mcp", Scope: mcp.ScopeUser}, mcp.StateDisabled)

	tests := []struct {
		name               string
		snapshot           mcp.Snapshot
		wantAvailability   features.Availability
		wantUsable         bool
		wantFeatureHealth  features.HealthState
		wantDoctor         observability.HealthStatus
		wantSummaryContain string
	}{
		{
			name: "not configured", wantAvailability: features.Unconfigured,
			wantFeatureHealth: features.HealthUnavailable, wantDoctor: observability.HealthUnavailable,
			wantSummaryContain: "no MCP servers configured",
		},
		{
			name: "all disabled", snapshot: mcp.Snapshot{Servers: []mcp.Descriptor{disabled}},
			wantAvailability: features.Unhealthy, wantFeatureHealth: features.HealthUnavailable,
			wantDoctor: observability.HealthUnavailable, wantSummaryContain: "1 disabled",
		},
		{
			name: "unsupported transport", snapshot: mcp.Snapshot{Servers: []mcp.Descriptor{unsupported}},
			wantAvailability: features.Unhealthy, wantFeatureHealth: features.HealthUnavailable,
			wantDoctor: observability.HealthUnavailable, wantSummaryContain: "1 unsupported",
		},
		{
			name: "failed connection", snapshot: mcp.Snapshot{Servers: []mcp.Descriptor{failed}},
			wantAvailability: features.Unhealthy, wantFeatureHealth: features.HealthUnavailable,
			wantDoctor: observability.HealthUnavailable, wantSummaryContain: "1 failed",
		},
		{
			name: "connected", snapshot: mcp.Snapshot{Servers: []mcp.Descriptor{connected}},
			wantAvailability: features.Available, wantUsable: true, wantFeatureHealth: features.HealthHealthy,
			wantDoctor: observability.HealthOK, wantSummaryContain: "1 connected",
		},
		{
			name: "partially failed", snapshot: mcp.Snapshot{Servers: []mcp.Descriptor{connected, failed}},
			wantAvailability: features.AvailableDegraded, wantUsable: true, wantFeatureHealth: features.HealthDegraded,
			wantDoctor: observability.HealthDegraded, wantSummaryContain: "1 failed",
		},
		{
			name:             "connected with discovery diagnostic",
			snapshot:         mcp.Snapshot{Servers: []mcp.Descriptor{connected}, Diagnostics: []mcp.Diagnostic{{Message: "synthetic diagnostic"}}},
			wantAvailability: features.AvailableDegraded, wantUsable: true, wantFeatureHealth: features.HealthDegraded,
			wantDoctor: observability.HealthDegraded, wantSummaryContain: "1 diagnostics available",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			states := evaluateFeatureProfile(false, runtimeExtensions{mcpState: test.snapshot})
			state := states[features.ModelContextProtocol]
			if state.Health.State != test.wantFeatureHealth {
				t.Fatalf("feature health = %q (%q), want %q", state.Health.State, state.Health.Reason, test.wantFeatureHealth)
			}
			evaluation := features.Evaluate(state)
			if evaluation.Availability != test.wantAvailability {
				t.Fatalf("feature availability = %q (%q), want %q", evaluation.Availability, evaluation.Reason, test.wantAvailability)
			}
			if evaluation.Usable != test.wantUsable {
				t.Fatalf("feature usability = %v (%q), want %v", evaluation.Usable, evaluation.Reason, test.wantUsable)
			}

			check := mcpDoctorCheck(test.snapshot)
			if check.Status != test.wantDoctor {
				t.Fatalf("doctor status = %q (%q), want %q", check.Status, check.Summary, test.wantDoctor)
			}
			if !strings.Contains(check.Summary, test.wantSummaryContain) {
				t.Fatalf("doctor summary %q does not contain %q", check.Summary, test.wantSummaryContain)
			}
		})
	}
}

func testMCPDescriptor(t *testing.T, config mcp.Config, state mcp.ConnectionState) mcp.Descriptor {
	t.Helper()
	descriptor, err := mcp.ValidateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.State = state
	return descriptor
}
