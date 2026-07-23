// Package features models optional capability availability without collapsing
// build inclusion, gates, eligibility, policy, platform, configuration, user
// choice, and runtime health into one misleading Boolean.
package features

import (
	"fmt"
	"sort"
)

// ID is a stable optional-capability identity.
type ID string

const (
	Assistant            ID = "assistant"
	AssistantViewer      ID = "assistant_viewer"
	Voice                ID = "voice"
	TerminalCompanion    ID = "terminal_companion"
	BrowserAutomation    ID = "browser_automation"
	ComputerUse          ID = "computer_use"
	LanguageServer       ID = "language_server"
	ModelContextProtocol ID = "mcp"
	MCPToolHost          ID = "mcp_tool_host"
	Plugins              ID = "plugins"
	RemoteBridge         ID = "remote_bridge"
	RemoteViewer         ID = "remote_viewer"
	RemoteAgents         ID = "remote_agents"
	DirectConnect        ID = "direct_connect"
	SSHPlacement         ID = "ssh_placement"
	Teleport             ID = "teleport"
	Subagents            ID = "subagents"
	Teams                ID = "teams"
	MaintainedDocuments  ID = "maintained_documents"
	AwaySummary          ID = "away_summary"
	Advisor              ID = "advisor"
	DesktopMCPImport     ID = "desktop_mcp_import"
	Notebook             ID = "notebook"
	IDEIntegration       ID = "ide_integration"
	Notifications        ID = "notifications"
	SleepPrevention      ID = "sleep_prevention"
)

// Category groups features for diagnostics only; it grants no authority.
type Category string

const (
	CategoryExperience  Category = "experience"
	CategoryExtension   Category = "extension"
	CategoryDistributed Category = "distributed"
	CategoryDeveloper   Category = "developer"
	CategoryPlatform    Category = "platform"
)

// Spec declares stable product shape, not live availability.
type Spec struct {
	ID                    ID       `json:"id"`
	Category              Category `json:"category"`
	Description           string   `json:"description"`
	RequiresConfiguration bool     `json:"requires_configuration"`
	RequiresOptIn         bool     `json:"requires_opt_in"`
	Lazy                  bool     `json:"lazy"`
}

var catalog = []Spec{
	{Assistant, CategoryExperience, "persistent assistant mode", false, true, true},
	{AssistantViewer, CategoryExperience, "viewer for a remotely owned assistant session", true, true, true},
	{Voice, CategoryExperience, "push-to-talk capture and transcription", true, true, true},
	{TerminalCompanion, CategoryExperience, "presentation-only terminal companion", false, true, true},
	{BrowserAutomation, CategoryDeveloper, "browser extension automation", true, true, true},
	{ComputerUse, CategoryDeveloper, "exclusive native desktop control", true, true, true},
	{LanguageServer, CategoryDeveloper, "plugin-provided language server integration", true, false, true},
	{ModelContextProtocol, CategoryExtension, "external MCP client connections", true, false, true},
	{MCPToolHost, CategoryExtension, "standalone MCP tool-host entrypoint", false, false, true},
	{Plugins, CategoryExtension, "plugin manifests and contributed components", false, false, true},
	{RemoteBridge, CategoryDistributed, "remote-control bridge", true, true, true},
	{RemoteViewer, CategoryDistributed, "remote session viewer and controller", true, true, true},
	{RemoteAgents, CategoryDistributed, "cloud-placed background agents", true, true, true},
	{DirectConnect, CategoryDistributed, "direct-connect remote session server", true, true, true},
	{SSHPlacement, CategoryDistributed, "SSH-hosted session placement", true, true, true},
	{Teleport, CategoryDistributed, "cross-machine session resume", true, true, true},
	{Subagents, CategoryDistributed, "delegated local agents", false, false, true},
	{Teams, CategoryDistributed, "coordinated agents, mailboxes, and shared tasks", true, true, true},
	{MaintainedDocuments, CategoryExperience, "derived maintained-document updates", true, true, true},
	{AwaySummary, CategoryExperience, "focus-derived away summaries", false, true, true},
	{Advisor, CategoryExperience, "server-owned advisor protocol", true, true, true},
	{DesktopMCPImport, CategoryExtension, "read-only discovery of desktop MCP settings", true, true, true},
	{Notebook, CategoryDeveloper, "notebook cell inspection and execution", true, false, true},
	{IDEIntegration, CategoryDeveloper, "IDE context and navigation integration", true, true, true},
	{Notifications, CategoryPlatform, "terminal or desktop completion notifications", true, true, true},
	{SleepPrevention, CategoryPlatform, "owned operating-system sleep inhibition", false, true, true},
}

// Catalog returns a stable copy of every named optional capability.
func Catalog() []Spec {
	result := append([]Spec(nil), catalog...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Lookup resolves a known feature without accepting arbitrary names.
func Lookup(id ID) (Spec, bool) {
	for _, spec := range catalog {
		if spec.ID == id {
			return spec, true
		}
	}
	return Spec{}, false
}

// Decision is the explicit state of one independent availability axis.
type Decision string

const (
	DecisionAllowed       Decision = "allowed"
	DecisionDenied        Decision = "denied"
	DecisionUnknown       Decision = "unknown"
	DecisionNotApplicable Decision = "not_applicable"
)

// Axis includes source attribution needed to repair a disabled feature.
type Axis struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	Source   string   `json:"source,omitempty"`
}

func (a Axis) passes() bool {
	return a.Decision == DecisionAllowed || a.Decision == DecisionNotApplicable
}

// HealthState is deliberately separate from enablement and configuration.
type HealthState string

const (
	HealthHealthy     HealthState = "healthy"
	HealthDegraded    HealthState = "degraded"
	HealthUnavailable HealthState = "unavailable"
	HealthUnknown     HealthState = "unknown"
)

// Health is a bounded live probe result.
type Health struct {
	State  HealthState `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// State retains every gate independently for diagnostics and live refresh.
type State struct {
	Feature       ID     `json:"feature"`
	Inclusion     Axis   `json:"inclusion"`
	RuntimeGate   Axis   `json:"runtime_gate"`
	Eligibility   Axis   `json:"eligibility"`
	Platform      Axis   `json:"platform"`
	ManagedPolicy Axis   `json:"managed_policy"`
	Configuration Axis   `json:"configuration"`
	UserOptIn     Axis   `json:"user_opt_in"`
	Health        Health `json:"health"`
}

// Availability is the effective projection; callers should retain State when
// displaying diagnostics so no independent reason is lost.
type Availability string

const (
	Available           Availability = "available"
	AvailableDegraded   Availability = "available_degraded"
	Excluded            Availability = "build_excluded"
	GateDisabled        Availability = "runtime_gate_disabled"
	Ineligible          Availability = "ineligible"
	PlatformUnsupported Availability = "platform_unsupported"
	PolicyDenied        Availability = "managed_policy_denied"
	Unconfigured        Availability = "unconfigured"
	UserDisabled        Availability = "user_disabled"
	Unhealthy           Availability = "unhealthy"
	AvailabilityUnknown Availability = "unknown"
)

// Evaluation is a user-safe effective result.
type Evaluation struct {
	Availability Availability `json:"availability"`
	Usable       bool         `json:"usable"`
	Reason       string       `json:"reason,omitempty"`
	Axis         string       `json:"axis,omitempty"`
}

// Evaluate applies stable fail-closed precedence without changing any axis.
func Evaluate(state State) Evaluation {
	if _, known := Lookup(state.Feature); !known {
		return Evaluation{Availability: AvailabilityUnknown, Axis: "feature", Reason: "feature identity is not registered"}
	}
	checks := []struct {
		name         string
		axis         Axis
		deniedStatus Availability
	}{
		{"inclusion", state.Inclusion, Excluded},
		{"runtime_gate", state.RuntimeGate, GateDisabled},
		{"eligibility", state.Eligibility, Ineligible},
		{"platform", state.Platform, PlatformUnsupported},
		{"managed_policy", state.ManagedPolicy, PolicyDenied},
		{"configuration", state.Configuration, Unconfigured},
		{"user_opt_in", state.UserOptIn, UserDisabled},
	}
	for _, check := range checks {
		switch check.axis.Decision {
		case DecisionUnknown:
			return Evaluation{Availability: AvailabilityUnknown, Axis: check.name, Reason: defaultReason(check.axis.Reason, "availability has not been established")}
		case DecisionDenied:
			return Evaluation{Availability: check.deniedStatus, Axis: check.name, Reason: check.axis.Reason}
		case DecisionAllowed, DecisionNotApplicable:
			continue
		default:
			return Evaluation{Availability: AvailabilityUnknown, Axis: check.name, Reason: "availability axis has an invalid decision"}
		}
	}
	switch state.Health.State {
	case HealthHealthy:
		return Evaluation{Availability: Available, Usable: true}
	case HealthDegraded:
		return Evaluation{Availability: AvailableDegraded, Usable: true, Axis: "health", Reason: state.Health.Reason}
	case HealthUnavailable:
		return Evaluation{Availability: Unhealthy, Axis: "health", Reason: state.Health.Reason}
	default:
		return Evaluation{Availability: AvailabilityUnknown, Axis: "health", Reason: defaultReason(state.Health.Reason, "runtime health has not been probed")}
	}
}

// Unsupported produces a complete supported-absence state for a build that
// does not include a feature. It registers no executable adapter by itself.
func Unsupported(id ID, reason string) (State, error) {
	if _, ok := Lookup(id); !ok {
		return State{}, fmt.Errorf("unknown feature %q", id)
	}
	notApplicable := Axis{Decision: DecisionNotApplicable}
	return State{
		Feature:       id,
		Inclusion:     Axis{Decision: DecisionDenied, Reason: reason, Source: "build"},
		RuntimeGate:   notApplicable,
		Eligibility:   notApplicable,
		Platform:      notApplicable,
		ManagedPolicy: notApplicable,
		Configuration: notApplicable,
		UserOptIn:     notApplicable,
		Health:        Health{State: HealthUnavailable, Reason: reason},
	}, nil
}

func defaultReason(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
