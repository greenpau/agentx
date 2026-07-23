package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/permission"
)

type HookEventName string

const (
	HookPreToolUse         HookEventName = "PreToolUse"
	HookPostToolUse        HookEventName = "PostToolUse"
	HookPostToolUseFailure HookEventName = "PostToolUseFailure"
	HookPermissionRequest  HookEventName = "PermissionRequest"
	HookPermissionDenied   HookEventName = "PermissionDenied"
	HookNotification       HookEventName = "Notification"
	HookUserPromptSubmit   HookEventName = "UserPromptSubmit"
	HookSessionStart       HookEventName = "SessionStart"
	HookSessionEnd         HookEventName = "SessionEnd"
	HookSetup              HookEventName = "Setup"
	HookStop               HookEventName = "Stop"
	HookStopFailure        HookEventName = "StopFailure"
	HookSubagentStart      HookEventName = "SubagentStart"
	HookSubagentStop       HookEventName = "SubagentStop"
	HookPreCompact         HookEventName = "PreCompact"
	HookPostCompact        HookEventName = "PostCompact"
	HookTeammateIdle       HookEventName = "TeammateIdle"
	HookTaskCreated        HookEventName = "TaskCreated"
	HookTaskCompleted      HookEventName = "TaskCompleted"
	HookElicitation        HookEventName = "Elicitation"
	HookElicitationResult  HookEventName = "ElicitationResult"
	HookConfigChange       HookEventName = "ConfigChange"
	HookInstructionsLoaded HookEventName = "InstructionsLoaded"
	HookWorktreeCreate     HookEventName = "WorktreeCreate"
	HookWorktreeRemove     HookEventName = "WorktreeRemove"
	HookCwdChanged         HookEventName = "CwdChanged"
	HookFileChanged        HookEventName = "FileChanged"
)

var supportedHookEvents = map[HookEventName]bool{
	HookPreToolUse: true, HookPostToolUse: true, HookPostToolUseFailure: true,
	HookPermissionRequest: true, HookPermissionDenied: true, HookNotification: true,
	HookUserPromptSubmit: true, HookSessionStart: true, HookSessionEnd: true,
	HookSetup: true, HookStop: true, HookStopFailure: true, HookSubagentStart: true,
	HookSubagentStop: true, HookPreCompact: true, HookPostCompact: true,
	HookTeammateIdle: true, HookTaskCreated: true, HookTaskCompleted: true,
	HookElicitation: true, HookElicitationResult: true, HookConfigChange: true,
	HookInstructionsLoaded: true, HookWorktreeCreate: true, HookWorktreeRemove: true,
	HookCwdChanged: true, HookFileChanged: true,
}

type HookKind string

const (
	HookKindCommand HookKind = "command"
	HookKindHTTP    HookKind = "http"
)

type HookDescriptor struct {
	ID                    string            `json:"id"`
	Event                 HookEventName     `json:"event"`
	Matcher               string            `json:"matcher,omitempty"`
	If                    string            `json:"if,omitempty"`
	Kind                  HookKind          `json:"kind"`
	Command               string            `json:"command,omitempty"`
	Shell                 string            `json:"shell,omitempty"`
	URL                   string            `json:"url,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	AllowedEnvVars        []string          `json:"allowed_env_vars,omitempty"`
	SensitivePathSegments []int             `json:"sensitive_path_segments,omitempty"`
	Timeout               time.Duration     `json:"timeout"`
	Once                  bool              `json:"once,omitempty"`
	Source                Source            `json:"source"`
	SourceIdentity        string            `json:"source_identity,omitempty"`
	PluginRoot            string            `json:"plugin_root,omitempty"`
	PluginDataDir         string            `json:"plugin_data_dir,omitempty"`
	Generation            uint64            `json:"generation"`
	position              int
	matcherRegex          *regexp.Regexp
}

// MarshalJSON projects hook metadata without serializing executable text,
// header values, or URLs that may contain credentials.
func (descriptor HookDescriptor) MarshalJSON() ([]byte, error) {
	type safeDescriptor struct {
		ID                    string        `json:"id"`
		Event                 HookEventName `json:"event"`
		Matcher               string        `json:"matcher,omitempty"`
		If                    string        `json:"if,omitempty"`
		Kind                  HookKind      `json:"kind"`
		CommandConfigured     bool          `json:"command_configured,omitempty"`
		URLConfigured         bool          `json:"url_configured,omitempty"`
		HeaderNames           []string      `json:"header_names,omitempty"`
		AllowedEnvVars        []string      `json:"allowed_env_vars,omitempty"`
		SensitivePathSegments []int         `json:"sensitive_path_segments,omitempty"`
		Timeout               time.Duration `json:"timeout"`
		Once                  bool          `json:"once,omitempty"`
		Source                Source        `json:"source"`
		SourceIdentity        string        `json:"source_identity,omitempty"`
		Generation            uint64        `json:"generation"`
	}
	headerNames := make([]string, 0, len(descriptor.Headers))
	for name := range descriptor.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	return json.Marshal(safeDescriptor{
		ID: descriptor.ID, Event: descriptor.Event, Matcher: descriptor.Matcher,
		If: descriptor.If, Kind: descriptor.Kind,
		CommandConfigured: descriptor.Command != "", URLConfigured: descriptor.URL != "",
		HeaderNames: headerNames, AllowedEnvVars: append([]string(nil), descriptor.AllowedEnvVars...),
		SensitivePathSegments: append([]int(nil), descriptor.SensitivePathSegments...),
		Timeout:               descriptor.Timeout, Once: descriptor.Once, Source: descriptor.Source,
		SourceIdentity: descriptor.SourceIdentity, Generation: descriptor.Generation,
	})
}

func cloneHookDescriptor(hook HookDescriptor) HookDescriptor {
	hook.Headers = cloneStringMap(hook.Headers)
	hook.AllowedEnvVars = append([]string(nil), hook.AllowedEnvVars...)
	hook.SensitivePathSegments = append([]int(nil), hook.SensitivePathSegments...)
	return hook
}

type HookSnapshot struct {
	Generation      uint64           `json:"generation"`
	ReachableEvents []HookEventName  `json:"reachable_events"`
	Hooks           []HookDescriptor `json:"hooks"`
	Diagnostics     []Diagnostic     `json:"diagnostics,omitempty"`
}

func cloneHookSnapshot(source HookSnapshot) HookSnapshot {
	result := HookSnapshot{
		Generation:      source.Generation,
		ReachableEvents: append([]HookEventName(nil), source.ReachableEvents...),
		Diagnostics:     append([]Diagnostic(nil), source.Diagnostics...),
	}
	result.Hooks = make([]HookDescriptor, len(source.Hooks))
	for index := range source.Hooks {
		result.Hooks[index] = cloneHookDescriptor(source.Hooks[index])
	}
	return result
}

type HookManager struct {
	mu        sync.RWMutex
	snapshot  HookSnapshot
	reachable map[HookEventName]bool
}

// NewHookManager exposes every protocol event. Runtime surfaces that implement
// only a subset must use NewHookManagerForEvents so configured-but-unreachable
// hooks are diagnosed instead of being silently advertised.
func NewHookManager() *HookManager {
	return newHookManager(nil)
}

func NewHookManagerForEvents(events ...HookEventName) *HookManager {
	reachable := make(map[HookEventName]bool, len(events))
	for _, event := range events {
		if supportedHookEvents[event] {
			reachable[event] = true
		}
	}
	return newHookManager(reachable)
}

func newHookManager(reachable map[HookEventName]bool) *HookManager {
	manager := &HookManager{reachable: reachable}
	manager.snapshot.ReachableEvents = manager.reachableEvents()
	return manager
}

func (m *HookManager) Snapshot() HookSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneHookSnapshot(m.snapshot)
}

// Reload validates and freezes a new hook generation. Duplicate persisted
// definitions use last-wins semantics before the final deterministic ordering.
func (m *HookManager) Reload(descriptors []HookDescriptor) HookSnapshot {
	input := make([]HookDescriptor, len(descriptors))
	for index := range descriptors {
		input[index] = cloneHookDescriptor(descriptors[index])
		input[index].position = index
	}
	var diagnostics []Diagnostic
	byDedup := make(map[string]int)
	var valid []HookDescriptor
	for _, descriptor := range input {
		normalized, err := validateHookDescriptor(descriptor)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: descriptor.SourceIdentity, Message: err.Error()})
			continue
		}
		if !m.eventIsReachable(normalized.Event) {
			diagnostics = append(diagnostics, Diagnostic{Path: descriptor.SourceIdentity, Message: fmt.Sprintf("hook event %s is unavailable in the active runtime profile", normalized.Event)})
			continue
		}
		key := hookDedupKey(normalized)
		if index, exists := byDedup[key]; exists {
			valid[index] = normalized
			continue
		}
		byDedup[key] = len(valid)
		valid = append(valid, normalized)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if hookSourceRank(valid[i].Source) != hookSourceRank(valid[j].Source) {
			return hookSourceRank(valid[i].Source) < hookSourceRank(valid[j].Source)
		}
		if valid[i].position != valid[j].position {
			return valid[i].position < valid[j].position
		}
		return valid[i].ID < valid[j].ID
	})
	m.mu.Lock()
	generation := m.snapshot.Generation + 1
	for index := range valid {
		valid[index].Generation = generation
		valid[index].position = index
	}
	next := HookSnapshot{Generation: generation, ReachableEvents: m.reachableEvents(), Hooks: valid, Diagnostics: diagnostics}
	m.snapshot = next
	m.mu.Unlock()
	return cloneHookSnapshot(next)
}

func (m *HookManager) eventIsReachable(event HookEventName) bool {
	return m.reachable == nil || m.reachable[event]
}

func (m *HookManager) reachableEvents() []HookEventName {
	events := make([]HookEventName, 0, len(supportedHookEvents))
	for event := range supportedHookEvents {
		if m.eventIsReachable(event) {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })
	return events
}

func (snapshot HookSnapshot) SupportsEvent(event HookEventName) bool {
	for _, reachable := range snapshot.ReachableEvents {
		if reachable == event {
			return true
		}
	}
	return false
}

func (snapshot HookSnapshot) HasHook(event HookEventName) bool {
	for _, hook := range snapshot.Hooks {
		if hook.Event == event {
			return true
		}
	}
	return false
}

func validateHookDescriptor(descriptor HookDescriptor) (HookDescriptor, error) {
	descriptor.ID = strings.TrimSpace(descriptor.ID)
	if descriptor.ID == "" {
		return HookDescriptor{}, errors.New("hook id is required")
	}
	if !supportedHookEvents[descriptor.Event] {
		return HookDescriptor{}, fmt.Errorf("unsupported hook event %q", descriptor.Event)
	}
	if descriptor.Timeout <= 0 {
		descriptor.Timeout = defaultHookTimeout(descriptor.Kind)
	}
	if descriptor.Timeout > 10*time.Minute {
		return HookDescriptor{}, errors.New("hook timeout exceeds 10 minutes")
	}
	if descriptor.If != "" {
		if !hookSupportsCondition(descriptor.Event) {
			return HookDescriptor{}, fmt.Errorf("hook conditions are unavailable for event %s", descriptor.Event)
		}
		if _, err := permission.ParseRule(descriptor.If, permission.EffectAllow, "hook_condition", false); err != nil {
			return HookDescriptor{}, fmt.Errorf("invalid hook condition: %w", err)
		}
	}
	if descriptor.Matcher != "" && descriptor.Matcher != "*" && !isExactHookMatcher(descriptor.Matcher) {
		compiled, err := regexp.Compile(descriptor.Matcher)
		if err != nil {
			return HookDescriptor{}, fmt.Errorf("invalid hook matcher: %w", err)
		}
		descriptor.matcherRegex = compiled
	}
	switch descriptor.Kind {
	case HookKindCommand:
		if strings.TrimSpace(descriptor.Command) == "" {
			return HookDescriptor{}, errors.New("command hook requires a command")
		}
		if descriptor.Shell != "" && descriptor.Shell != "bash" && descriptor.Shell != "powershell" && descriptor.Shell != "sh" {
			return HookDescriptor{}, errors.New("hook shell must be bash, sh, or powershell")
		}
	case HookKindHTTP:
		if descriptor.Event == HookSessionStart || descriptor.Event == HookSetup {
			return HookDescriptor{}, fmt.Errorf("HTTP hooks are unavailable for %s", descriptor.Event)
		}
		if strings.TrimSpace(descriptor.URL) == "" {
			return HookDescriptor{}, errors.New("HTTP hook requires a URL")
		}
		headerNames := make([]string, 0, len(descriptor.Headers))
		for name := range descriptor.Headers {
			headerNames = append(headerNames, name)
		}
		sort.Strings(headerNames)
		for _, name := range headerNames {
			if !validHTTPHeaderName(name) {
				return HookDescriptor{}, errors.New("HTTP hook header name is invalid")
			}
			if !validHTTPHeaderValue(stripHeaderControls(descriptor.Headers[name])) {
				return HookDescriptor{}, errors.New("HTTP hook header value is invalid")
			}
		}
	default:
		return HookDescriptor{}, fmt.Errorf("unsupported hook kind %q", descriptor.Kind)
	}
	if len(descriptor.SensitivePathSegments) > 0 {
		target, err := url.Parse(descriptor.URL)
		if descriptor.Kind != HookKindHTTP || err != nil ||
			(target.Scheme != "http" && target.Scheme != "https") ||
			target.Hostname() == "" || target.Path == "" || target.Path == "/" {
			return HookDescriptor{}, errors.New("sensitive_path_segments requires an HTTP hook URL with a nonroot path")
		}
		segments := nonemptyEscapedPathSegments(target.EscapedPath())
		sort.Ints(descriptor.SensitivePathSegments)
		canonical := descriptor.SensitivePathSegments[:0]
		for _, index := range descriptor.SensitivePathSegments {
			if index < 0 || index >= len(segments) {
				return HookDescriptor{}, errors.New("sensitive_path_segments contains an out-of-range path segment index")
			}
			if len(canonical) == 0 || canonical[len(canonical)-1] != index {
				canonical = append(canonical, index)
			}
		}
		descriptor.SensitivePathSegments = canonical
	}
	return descriptor, nil
}

func nonemptyEscapedPathSegments(escapedPath string) []string {
	parts := strings.Split(escapedPath, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

// validHTTPHeaderName matches the RFC token grammar used by net/http before
// serializing a request. Validate at reload so an unexecutable descriptor
// cannot contribute header values to the frozen response-credential scope.
func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		switch character := name[index]; {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			continue
		default:
			switch character {
			case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
				continue
			default:
				return false
			}
		}
	}
	return true
}

// validHTTPHeaderValue matches net/http's field-value preflight: horizontal
// tab is allowed, while every other ASCII control byte and DEL is rejected.
func validHTTPHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\t' {
			continue
		}
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func defaultHookTimeout(kind HookKind) time.Duration {
	if kind == HookKindHTTP {
		return 10 * time.Minute
	}
	return 10 * time.Minute
}

var exactHookMatcherPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:\|[A-Za-z0-9_]+)*$`)

func isExactHookMatcher(matcher string) bool { return exactHookMatcherPattern.MatchString(matcher) }

func hookDedupKey(descriptor HookDescriptor) string {
	headers, _ := json.Marshal(descriptor.Headers)
	root := ""
	if descriptor.Source == SourcePlugin {
		root = descriptor.PluginRoot
	}
	return strings.Join([]string{
		string(descriptor.Event), descriptor.Matcher, descriptor.If, string(descriptor.Kind),
		descriptor.Command, descriptor.Shell, descriptor.URL, string(headers), root,
	}, "\x00")
}

func hookSourceRank(source Source) int {
	switch source {
	case SourceManaged:
		return 0
	case SourceUser:
		return 1
	case SourceProject:
		return 2
	case SourcePlugin:
		return 3
	case SourceExplicit:
		return 4
	case SourceBundled:
		return 5
	default:
		return 6
	}
}

// HookInput is marshalled as the common envelope plus event-specific fields.
// Event fields cannot overwrite common identity fields.
type HookInput struct {
	SessionID      string
	TranscriptPath string
	CWD            string
	PermissionMode string
	AgentID        string
	AgentType      string
	Event          HookEventName
	Fields         map[string]any
}

func (input HookInput) MarshalJSON() ([]byte, error) {
	if !supportedHookEvents[input.Event] {
		return nil, fmt.Errorf("unsupported hook event %q", input.Event)
	}
	object := make(map[string]any, len(input.Fields)+7)
	for key, value := range input.Fields {
		object[key] = value
	}
	for _, reserved := range []string{"session_id", "transcript_path", "cwd", "permission_mode", "agent_id", "agent_type", "hook_event_name"} {
		if _, exists := object[reserved]; exists {
			return nil, fmt.Errorf("event field %q conflicts with hook envelope", reserved)
		}
	}
	object["session_id"] = input.SessionID
	object["transcript_path"] = input.TranscriptPath
	object["cwd"] = input.CWD
	object["hook_event_name"] = input.Event
	if input.PermissionMode != "" {
		object["permission_mode"] = input.PermissionMode
	}
	if input.AgentID != "" {
		object["agent_id"] = input.AgentID
	}
	if input.AgentType != "" {
		object["agent_type"] = input.AgentType
	}
	return json.Marshal(object)
}

func (input HookInput) matchQuery() string {
	field := func(name string) string {
		value, _ := input.Fields[name].(string)
		return value
	}
	switch input.Event {
	case HookPreToolUse, HookPostToolUse, HookPostToolUseFailure, HookPermissionRequest, HookPermissionDenied:
		return field("tool_name")
	case HookSessionStart:
		return field("source")
	case HookSessionEnd:
		return field("reason")
	case HookSetup:
		return field("trigger")
	case HookPreCompact, HookPostCompact:
		return field("trigger")
	case HookNotification:
		return field("notification_type")
	case HookStopFailure:
		return field("error")
	case HookSubagentStart, HookSubagentStop:
		return field("agent_type")
	case HookElicitation, HookElicitationResult:
		return field("server")
	case HookConfigChange:
		return field("source")
	case HookInstructionsLoaded:
		return field("load_reason")
	case HookFileChanged:
		value := field("file_path")
		if index := strings.LastIndexAny(value, `/\\`); index >= 0 {
			return value[index+1:]
		}
		return value
	default:
		return ""
	}
}

func (descriptor HookDescriptor) matches(input HookInput) bool {
	if descriptor.Event != input.Event {
		return false
	}
	matcher := strings.TrimSpace(descriptor.Matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	query := input.matchQuery()
	if query == "" {
		return false
	}
	if isExactHookMatcher(matcher) {
		for _, candidate := range strings.Split(matcher, "|") {
			if candidate == query {
				return true
			}
		}
		return false
	}
	return descriptor.matcherRegex != nil && descriptor.matcherRegex.MatchString(query)
}

type HookDecision string

const (
	HookDecisionNone  HookDecision = ""
	HookDecisionAllow HookDecision = "allow"
	HookDecisionAsk   HookDecision = "ask"
	HookDecisionDeny  HookDecision = "deny"
)

type HookResult struct {
	HookID         string         `json:"hook_id"`
	Event          HookEventName  `json:"event"`
	Decision       HookDecision   `json:"decision,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Context        string         `json:"context,omitempty"`
	SystemMessage  string         `json:"system_message,omitempty"`
	UpdatedInput   map[string]any `json:"updated_input,omitempty"`
	Continue       bool           `json:"continue"`
	SuppressOutput bool           `json:"suppress_output,omitempty"`
	Stdout         string         `json:"stdout,omitempty"`
	Stderr         string         `json:"stderr,omitempty"`
	ExitCode       int            `json:"exit_code"`
	TimedOut       bool           `json:"timed_out,omitempty"`
	Cancelled      bool           `json:"cancelled,omitempty"`
	Truncated      bool           `json:"truncated,omitempty"`
	Err            error          `json:"-"`
	order          int
}

type HookAggregate struct {
	Decision     HookDecision   `json:"decision,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	UpdatedInput map[string]any `json:"updated_input,omitempty"`
	Contexts     []string       `json:"contexts,omitempty"`
	Results      []HookResult   `json:"results"`
	Continue     bool           `json:"continue"`
}

func aggregateHookResults(results []HookResult) HookAggregate {
	aggregate := HookAggregate{Continue: true, Results: append([]HookResult(nil), results...)}
	winningRank := 0
	for _, result := range results {
		if !result.Continue {
			aggregate.Continue = false
		}
		if result.Context != "" {
			aggregate.Contexts = append(aggregate.Contexts, result.Context)
		}
		rank := hookDecisionRank(result.Decision)
		if rank > winningRank {
			winningRank = rank
			aggregate.Decision = result.Decision
			aggregate.Reason = result.Reason
			aggregate.UpdatedInput = result.UpdatedInput
		}
	}
	return aggregate
}

func hookDecisionRank(decision HookDecision) int {
	switch decision {
	case HookDecisionDeny:
		return 3
	case HookDecisionAsk:
		return 2
	case HookDecisionAllow:
		return 1
	default:
		return 0
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
