package features

import "testing"

func allowedState(id ID) State {
	allowed := Axis{Decision: DecisionAllowed}
	return State{
		Feature: id, Inclusion: allowed, RuntimeGate: allowed, Eligibility: allowed,
		Platform: allowed, ManagedPolicy: allowed, Configuration: allowed,
		UserOptIn: allowed, Health: Health{State: HealthHealthy},
	}
}

func TestCatalogHasExplicitUniqueOptionalCapabilities(t *testing.T) {
	seen := make(map[ID]bool)
	for _, spec := range Catalog() {
		if seen[spec.ID] {
			t.Fatalf("duplicate feature %q", spec.ID)
		}
		seen[spec.ID] = true
	}
	for _, required := range []ID{Voice, LanguageServer, BrowserAutomation, ComputerUse, RemoteBridge, RemoteViewer, Teams, ModelContextProtocol} {
		if !seen[required] {
			t.Errorf("missing feature %q", required)
		}
	}
}

func TestEvaluateKeepsAxesIndependentAndFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*State)
		want   Availability
		axis   string
	}{
		{"included and healthy", func(*State) {}, Available, ""},
		{"gate off", func(s *State) { s.RuntimeGate = Axis{Decision: DecisionDenied, Reason: "disabled"} }, GateDisabled, "runtime_gate"},
		{"policy denial", func(s *State) { s.ManagedPolicy = Axis{Decision: DecisionDenied, Reason: "organization policy"} }, PolicyDenied, "managed_policy"},
		{"dependency unavailable", func(s *State) { s.Health = Health{State: HealthUnavailable, Reason: "binary absent"} }, Unhealthy, "health"},
		{"unknown eligibility", func(s *State) { s.Eligibility = Axis{Decision: DecisionUnknown} }, AvailabilityUnknown, "eligibility"},
		{"degraded remains usable", func(s *State) { s.Health = Health{State: HealthDegraded, Reason: "reconnecting"} }, AvailableDegraded, "health"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := allowedState(Voice)
			test.mutate(&state)
			got := Evaluate(state)
			if got.Availability != test.want || got.Axis != test.axis {
				t.Fatalf("evaluation = %+v, want availability=%q axis=%q", got, test.want, test.axis)
			}
			if (test.want == Available || test.want == AvailableDegraded) != got.Usable {
				t.Fatalf("usable mismatch: %+v", got)
			}
		})
	}
}

func TestUnsupportedIsExplicitAndNeutral(t *testing.T) {
	state, err := Unsupported(RemoteBridge, "transport implementation not included")
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(state)
	if result.Availability != Excluded || result.Usable {
		t.Fatalf("unsupported evaluation = %+v", result)
	}
	if state.RuntimeGate.Decision != DecisionNotApplicable || state.Health.State != HealthUnavailable {
		t.Fatalf("unsupported axes collapsed: %+v", state)
	}
	if _, err := Unsupported(ID("invented"), ""); err == nil {
		t.Fatal("unknown feature accepted")
	}
}

func TestEvaluateRejectsUnknownFeatureIdentity(t *testing.T) {
	allowed := Axis{Decision: DecisionAllowed}
	result := Evaluate(State{
		Feature: "future_unregistered_feature", Inclusion: allowed, RuntimeGate: allowed,
		Eligibility: allowed, Platform: allowed, ManagedPolicy: allowed,
		Configuration: allowed, UserOptIn: allowed, Health: Health{State: HealthHealthy},
	})
	if result.Usable || result.Availability != AvailabilityUnknown || result.Axis != "feature" {
		t.Fatalf("unknown feature evaluation=%+v", result)
	}
}
