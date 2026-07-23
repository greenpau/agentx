package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/childenv"
	"github.com/greenpau/agentx/pkg/redact"
)

const (
	defaultHookOutputLimit            = 256 << 10
	maximumHookCredentialLiterals     = 256
	maximumHookCredentialLiteralBytes = 64 << 10
)

var (
	errHTTPHookCredentialWorkload     = errors.New("HTTP hook credential material exceeds redaction workload limit")
	errHTTPHookAuthorizationMalformed = errors.New("HTTP hook authorization is malformed")
	errHTTPHookHeaderExpansionInvalid = errors.New("HTTP hook header expansion is invalid")
	errHTTPHookHeaderNameInvalid      = errors.New("HTTP hook header name is invalid")
	errHTTPHookHeaderValueInvalid     = errors.New("HTTP hook header value is invalid")
	errHTTPHookQueryInvalid           = errors.New("HTTP hook query configuration is invalid")
	errHTTPHookSensitivePathInvalid   = errors.New("HTTP hook sensitive path configuration is invalid")
)

type hookResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// HookRunner executes a frozen snapshot. Environment values are considered
// secret-bearing and are passed only through explicit, non-sensitive allowlists.
type HookRunner struct {
	Environment      map[string]string
	CommandEnvAllow  map[string]bool
	HTTPEnvAllow     map[string]bool
	AllowedHTTPURLs  *[]string // nil is open subject to SSRF; empty denies all.
	ProjectRoot      string
	OutputLimit      int
	ConditionMatcher func(rule string, input HookInput) bool
	// Sanitize removes host-owned credential material from all hook-controlled
	// output before it can affect authority, context, progress, or later hooks.
	// Deprecated: exact credentials should be installed with
	// SetCredentialLiterals so response-scoped literals can be merged into one
	// compositionally safe set.
	Sanitize      func(string) string
	resolver      hookResolver
	credentialSet *redact.Set
	credentialErr error
	// credentialFrozen means credentialSet already includes every response
	// scope from the active immutable hook snapshot. A later scope expansion
	// may reuse that set but cannot add a literal after shared sinks exist.
	credentialFrozen bool

	onceMu sync.Mutex
	once   map[[sha256.Size]byte]hookOnceExecution
}

type hookOnceExecution struct {
	running   bool
	succeeded bool
}

type hookExecution struct {
	result      HookResult
	credentials *redact.Set
}

func NewHookRunner() *HookRunner {
	return &HookRunner{OutputLimit: defaultHookOutputLimit, resolver: net.DefaultResolver}
}

// SetCredentialLiterals installs the complete host-owned credential set used
// at hook input and output boundaries. Configure it before Dispatch.
func (runner *HookRunner) SetCredentialLiterals(literals ...string) {
	runner.credentialSet, runner.credentialErr = boundedHookCredentialSet(literals)
	runner.credentialFrozen = false
}

// SetCredentialSanitizer installs a precomposed immutable set without exposing
// its literals. The same aggregate workload limits apply.
func (runner *HookRunner) SetCredentialSanitizer(set *redact.Set) {
	runner.credentialFrozen = false
	if set != nil &&
		(set.LiteralCount() > maximumHookCredentialLiterals ||
			set.TotalLiteralBytes() > maximumHookCredentialLiteralBytes) {
		runner.credentialSet = redact.New()
		runner.credentialErr = errors.New("hook credential material exceeds redaction workload limit")
		return
	}
	runner.credentialSet = set
	runner.credentialErr = nil
}

// FreezeCredentialSanitizer promotes every response-scoped credential from
// the immutable hook snapshot into the session-wide set before shared sinks
// are constructed. The runner rejects any later dispatch whose rederived
// scope would add a literal to this frozen set.
func (runner *HookRunner) FreezeCredentialSanitizer(snapshot HookSnapshot, base *redact.Set) (*redact.Set, error) {
	credentials := base
	if credentials == nil {
		credentials = redact.New()
	}
	for _, descriptor := range snapshot.Hooks {
		if descriptor.Kind != HookKindHTTP {
			continue
		}
		target, err := url.Parse(descriptor.URL)
		if err != nil {
			// Dispatch reports the static parse diagnostic and never executes
			// this hook, so it has no response scope to promote.
			continue
		}
		if runner.validateHTTPHookTarget(target) != nil {
			continue
		}
		headers, expansionShapeErr, expansionWorkloadErr := runner.expandHTTPHeaders(descriptor)
		if expansionShapeErr != nil {
			continue
		}
		if preflightHTTPHookRequest(target, headers) != nil {
			// Standard-library wire serialization is the final no-network
			// executability preflight for host and header syntax.
			continue
		}
		nextCredentials, compositionErr := httpHookResponseSanitizer(credentials, target, headers, descriptor.SensitivePathSegments)
		if httpHookScopeIsUnexecutable(compositionErr) {
			// These descriptors cannot pass the same pre-network dispatch
			// checks, so they contribute no response scope.
			continue
		}
		if expansionWorkloadErr != nil {
			return nil, errors.New("frozen HTTP hook credential material could not be composed safely")
		}
		credentials, err = nextCredentials, compositionErr
		if err != nil {
			return nil, errors.New("frozen HTTP hook credential material could not be composed safely")
		}
	}
	if !credentials.Empty() && credentials.TerminalMarker() == "" {
		return nil, errors.New("frozen HTTP hook credential material has no safe streaming projection")
	}
	runner.SetCredentialSanitizer(credentials)
	if runner.credentialErr != nil {
		return nil, errors.New("frozen HTTP hook credential material exceeds redaction workload limit")
	}
	runner.credentialFrozen = true
	return credentials, nil
}

func (runner *HookRunner) Dispatch(ctx context.Context, snapshot HookSnapshot, input HookInput) (HookAggregate, error) {
	if runner.credentialErr != nil {
		return HookAggregate{}, errors.New("hook credential redaction workload exceeds its limit")
	}
	// Matching and conditional authorization use the authoritative event. The
	// external hook receives a cloned projection with configured model
	// credentials removed, so command stdin and HTTP request bodies cannot become
	// a second egress path when a secret appears inside tool input or output.
	var payload []byte
	var err error
	if runner.credentialSet != nil && !runner.credentialSet.Empty() {
		payload, err = json.Marshal(input)
		if err == nil {
			payload, err = runner.credentialSet.JSON(payload)
		}
		if err != nil {
			return HookAggregate{}, errors.New("hook input could not be safely sanitized")
		}
	} else {
		payload = nil
		payloadInput := runner.sanitizeInput(input)
		payload, err = json.Marshal(payloadInput)
	}
	if err != nil {
		return HookAggregate{}, err
	}
	runner.pruneOnce(snapshot)
	eligible := make([]HookDescriptor, 0)
	for _, descriptor := range snapshot.Hooks {
		if !descriptor.matches(input) {
			continue
		}
		if descriptor.If != "" {
			if !hookSupportsCondition(input.Event) || runner.ConditionMatcher == nil {
				continue
			}
			matches, matchErr := callHookConditionMatcher(runner.ConditionMatcher, descriptor.If, input)
			if matchErr != nil {
				return HookAggregate{}, matchErr
			}
			if !matches {
				continue
			}
		}
		eligible = append(eligible, descriptor)
	}
	matched := make([]HookDescriptor, 0, len(eligible))
	for _, descriptor := range eligible {
		if descriptor.Once && !runner.claimOnce(descriptor) {
			continue
		}
		matched = append(matched, cloneHookDescriptor(descriptor))
	}
	if len(matched) == 0 {
		return runner.finalizeHookAggregate(HookAggregate{Continue: true, Results: []HookResult{}}, nil)
	}

	results := make(chan hookExecution, len(matched))
	var wait sync.WaitGroup
	for index, descriptor := range matched {
		wait.Add(1)
		go func(order int, hook HookDescriptor) {
			defer wait.Done()
			execution := runner.executeContained(ctx, hook, input.Event, payload)
			result := execution.result
			if hook.Once {
				runner.completeOnce(hook, result.Err == nil && !result.TimedOut && !result.Cancelled)
			}
			result.order = order
			execution.result = result
			results <- execution
		}(index, descriptor)
	}
	wait.Wait()
	close(results)
	completed := make([]hookExecution, 0, len(matched))
	for execution := range results {
		completed = append(completed, execution)
	}
	// The authority result is independent of completion order. Stable result
	// ordering also makes structured surfaces and tests reproducible.
	sort.SliceStable(completed, func(i, j int) bool {
		return completed[i].result.order < completed[j].result.order
	})
	hookResults := make([]HookResult, len(completed))
	credentialSets := make([]*redact.Set, 0, len(completed))
	for index, execution := range completed {
		hookResults[index] = execution.result
		credentialSets = append(credentialSets, execution.credentials)
	}
	return runner.finalizeHookAggregate(aggregateHookResults(hookResults), credentialSets)
}

func callHookConditionMatcher(matcher func(string, HookInput) bool, rule string, input HookInput) (matched bool, err error) {
	defer func() {
		if recover() != nil {
			matched = false
			err = errors.New("hook condition matcher panicked")
		}
	}()
	return matcher(rule, input), nil
}

func (runner *HookRunner) finalizeHookAggregate(aggregate HookAggregate, credentialSets []*redact.Set) (HookAggregate, error) {
	credentialSets = append([]*redact.Set{runner.credentialSet}, credentialSets...)
	credentials := redact.Union(credentialSets...)
	if credentials.LiteralCount() > maximumHookCredentialLiterals ||
		credentials.TotalLiteralBytes() > maximumHookCredentialLiteralBytes {
		return HookAggregate{}, errors.New("hook result credential redaction workload exceeds its limit")
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		return HookAggregate{}, errors.New("encode hook result aggregate")
	}
	type safetyResult struct {
		HookResult
		Error string `json:"error,omitempty"`
	}
	safetyResults := make([]safetyResult, len(aggregate.Results))
	for index, result := range aggregate.Results {
		safetyResults[index].HookResult = result
		if result.Err != nil {
			safetyResults[index].Error = safeHookErrorText(result.Err)
		}
	}
	safetyEnvelope := struct {
		Decision     HookDecision   `json:"decision,omitempty"`
		Reason       string         `json:"reason,omitempty"`
		UpdatedInput map[string]any `json:"updated_input,omitempty"`
		Contexts     []string       `json:"contexts,omitempty"`
		Results      []safetyResult `json:"results"`
		Continue     bool           `json:"continue"`
	}{
		Decision: aggregate.Decision, Reason: aggregate.Reason,
		UpdatedInput: aggregate.UpdatedInput, Contexts: aggregate.Contexts,
		Results: safetyResults, Continue: aggregate.Continue,
	}
	safetyEncoded, err := json.Marshal(safetyEnvelope)
	if err != nil {
		return HookAggregate{}, errors.New("encode hook result safety envelope")
	}
	for _, candidate := range [][]byte{encoded, safetyEncoded} {
		if !credentials.Empty() {
			reflected, inspectionErr := credentials.JSONContains(candidate)
			if inspectionErr != nil || reflected {
				return HookAggregate{}, errors.New("hook result aggregate could not be safely projected")
			}
		}
		if runner.Sanitize != nil && (runner.credentialSet == nil || runner.credentialSet.Empty()) &&
			runner.sanitizeText(string(candidate)) != string(candidate) {
			return HookAggregate{}, errors.New("hook result aggregate could not be safely projected")
		}
	}
	return aggregate, nil
}

func hookOnceIdentity(descriptor HookDescriptor) [sha256.Size]byte {
	return sha256.Sum256([]byte(descriptor.ID + "\x00" + descriptor.SourceIdentity + "\x00" + string(descriptor.Source) + "\x00" + hookDedupKey(descriptor)))
}

func (runner *HookRunner) pruneOnce(snapshot HookSnapshot) {
	active := make(map[[sha256.Size]byte]struct{})
	for _, descriptor := range snapshot.Hooks {
		if descriptor.Once {
			active[hookOnceIdentity(descriptor)] = struct{}{}
		}
	}
	runner.onceMu.Lock()
	for identity, state := range runner.once {
		if _, present := active[identity]; !present && !state.running {
			delete(runner.once, identity)
		}
	}
	runner.onceMu.Unlock()
}

func (runner *HookRunner) claimOnce(descriptor HookDescriptor) bool {
	identity := hookOnceIdentity(descriptor)
	runner.onceMu.Lock()
	defer runner.onceMu.Unlock()
	if runner.once == nil {
		runner.once = make(map[[sha256.Size]byte]hookOnceExecution)
	}
	state := runner.once[identity]
	if state.running || state.succeeded {
		return false
	}
	runner.once[identity] = hookOnceExecution{running: true}
	return true
}

func (runner *HookRunner) completeOnce(descriptor HookDescriptor, succeeded bool) {
	identity := hookOnceIdentity(descriptor)
	runner.onceMu.Lock()
	defer runner.onceMu.Unlock()
	if succeeded {
		runner.once[identity] = hookOnceExecution{succeeded: true}
	} else {
		delete(runner.once, identity)
	}
}

func (runner *HookRunner) sanitizeInput(input HookInput) HookInput {
	if !runner.hasSanitizer() {
		return input
	}
	input.SessionID = runner.sanitizeText(input.SessionID)
	input.TranscriptPath = runner.sanitizeText(input.TranscriptPath)
	input.CWD = runner.sanitizeText(input.CWD)
	input.PermissionMode = runner.sanitizeText(input.PermissionMode)
	input.AgentID = runner.sanitizeText(input.AgentID)
	input.AgentType = runner.sanitizeText(input.AgentType)
	if input.Fields != nil {
		if fields, ok := runner.sanitizeValue(input.Fields).(map[string]any); ok {
			input.Fields = fields
		} else {
			input.Fields = map[string]any{}
		}
	}
	return input
}

func hookSupportsCondition(event HookEventName) bool {
	switch event {
	case HookPreToolUse, HookPostToolUse, HookPostToolUseFailure, HookPermissionRequest:
		return true
	default:
		return false
	}
}

func (runner *HookRunner) execute(parent context.Context, descriptor HookDescriptor, event HookEventName, payload []byte) hookExecution {
	timeout := descriptor.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout(descriptor.Kind)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var result HookResult
	var responseSet *redact.Set
	switch descriptor.Kind {
	case HookKindCommand:
		result = runner.executeCommand(ctx, descriptor, payload)
	case HookKindHTTP:
		result, responseSet = runner.executeHTTP(ctx, descriptor, payload)
	default:
		result = HookResult{Err: fmt.Errorf("unsupported hook kind %q", descriptor.Kind), Continue: true, ExitCode: -1}
	}
	result.HookID = descriptor.ID
	result.Event = event
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.Err = context.DeadlineExceeded
	}
	if errors.Is(parent.Err(), context.Canceled) {
		result.Cancelled = true
		result.Err = context.Canceled
	}
	applyHookAuthority(&result, event)
	if responseSet != nil {
		return hookExecution{result: runner.sanitizeResultWith(result, responseSet.Apply), credentials: responseSet}
	}
	return hookExecution{result: runner.sanitizeResult(result), credentials: runner.credentialSet}
}

func (runner *HookRunner) executeContained(parent context.Context, descriptor HookDescriptor, event HookEventName, payload []byte) (execution hookExecution) {
	defer func() {
		if recover() != nil {
			execution = hookExecution{
				result: HookResult{
					HookID: descriptor.ID, Event: event, Continue: true, ExitCode: -1,
					Err: errors.New("hook execution panicked"),
				},
				credentials: runner.credentialSet,
			}
		}
	}()
	return runner.execute(parent, descriptor, event, payload)
}

func (runner *HookRunner) sanitizeResult(result HookResult) HookResult {
	return runner.sanitizeResultWith(result, runner.sanitizeText)
}

func (runner *HookRunner) sanitizeResultWith(result HookResult, sanitize func(string) string) HookResult {
	result.Reason = sanitize(result.Reason)
	result.Context = sanitize(result.Context)
	result.SystemMessage = sanitize(result.SystemMessage)
	result.Stdout = sanitize(result.Stdout)
	result.Stderr = sanitize(result.Stderr)
	if result.Err != nil {
		result.Err = errors.New(sanitize(safeHookErrorText(result.Err)))
	}
	if result.UpdatedInput != nil {
		value, ok := sanitizeHookValue(result.UpdatedInput, sanitize).(map[string]any)
		if !ok {
			result.UpdatedInput = nil
			result.Decision = HookDecisionDeny
			result.Reason = "hook-updated input could not be safely sanitized"
		} else {
			result.UpdatedInput = value
		}
	}
	return result
}

func safeHookErrorText(err error) (text string) {
	text = "hook operation failed"
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			text = "hook operation failed"
		}
	}()
	return err.Error()
}

func (runner *HookRunner) sanitizeValue(value any) any {
	return sanitizeHookValue(value, runner.sanitizeText)
}

func sanitizeHookValue(value any, sanitize func(string) string) any {
	switch typed := value.(type) {
	case string:
		return sanitize(typed)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[sanitize(key)] = sanitizeHookValue(child, sanitize)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = sanitizeHookValue(child, sanitize)
		}
		return result
	case []string:
		result := make([]string, len(typed))
		for index, child := range typed {
			result[index] = sanitize(child)
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, child := range typed {
			result[sanitize(key)] = sanitize(child)
		}
		return result
	case json.RawMessage:
		return json.RawMessage(sanitize(string(typed)))
	case []byte:
		return []byte(sanitize(string(typed)))
	default:
		return value
	}
}

func (runner *HookRunner) hasSanitizer() bool {
	return runner.credentialSet != nil && !runner.credentialSet.Empty() || runner.Sanitize != nil
}

func (runner *HookRunner) sanitizeText(value string) string {
	if runner.credentialSet != nil && !runner.credentialSet.Empty() {
		return runner.credentialSet.Apply(value)
	}
	if runner.Sanitize == nil {
		return value
	}
	result := ""
	func() {
		defer func() { _ = recover() }()
		result = runner.Sanitize(value)
	}()
	return result
}

func (runner *HookRunner) executeCommand(ctx context.Context, descriptor HookDescriptor, payload []byte) HookResult {
	command, err := hookCommand(ctx, descriptor)
	if err != nil {
		return HookResult{Err: err, Continue: true, ExitCode: -1}
	}
	configureHookCommand(command)
	command.Dir = runner.ProjectRoot
	if command.Dir == "" {
		command.Dir = "."
	}
	environment, err := runner.commandEnvironment(descriptor)
	if err != nil {
		return HookResult{Err: err, Continue: true, ExitCode: -1}
	}
	command.Env = environment
	framedPayload := append(append([]byte(nil), payload...), '\n')
	if runner.credentialErr != nil {
		return HookResult{Err: runner.credentialErr, Continue: true, ExitCode: -1}
	}
	if runner.credentialSet != nil && !runner.credentialSet.Empty() {
		reflected, inspectionErr := runner.credentialSet.JSONContains(framedPayload)
		if inspectionErr != nil || reflected {
			return HookResult{Err: errors.New("hook command input could not be safely encoded"), Continue: true, ExitCode: -1}
		}
	}
	command.Stdin = bytes.NewReader(framedPayload)
	limit := runner.outputLimit()
	captureLimit := limit + runner.redactionLookahead()
	if captureLimit < limit {
		captureLimit = limit
	}
	stdout := newCappedBuffer(captureLimit)
	stderr := newCappedBuffer(captureLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	safeStdout, stdoutTruncated := runner.sanitizeCapturedOutput(stdout.String(), stdout.Truncated(), limit)
	safeStderr, stderrTruncated := runner.sanitizeCapturedOutput(stderr.String(), stderr.Truncated(), limit)
	result := HookResult{
		Stdout: safeStdout, Stderr: safeStderr, ExitCode: exitCode,
		Truncated: stdoutTruncated || stderrTruncated, Continue: true,
	}
	if err != nil {
		result.Err = err
	}
	if exitCode == 2 {
		// Exit 2 is a modeled hook authority result, not an operational
		// subprocess failure.
		result.Err = nil
	}
	if strings.HasPrefix(strings.TrimSpace(result.Stdout), "{") {
		structured := []byte(result.Stdout)
		if runner.credentialSet != nil && !runner.credentialSet.Empty() {
			structured, err = runner.credentialSet.JSON(structured)
			if err != nil {
				result.Err = errors.New("invalid structured hook output: output could not be safely sanitized")
				result.Stdout = ""
				result.Decision = HookDecisionNone
				result.UpdatedInput = nil
				return result
			}
			result.Stdout = string(structured)
		}
		if parseErr := parseHookStructuredResult(structured, descriptor.Event, &result); parseErr != nil {
			result.Err = fmt.Errorf("invalid structured hook output: %w", parseErr)
			result.Decision = HookDecisionNone
			result.UpdatedInput = nil
		}
	} else if exitCode == 0 && hookPlainOutputIsContext(descriptor.Event) {
		result.Context = strings.TrimSpace(result.Stdout)
	}
	return result
}

func hookCommand(ctx context.Context, descriptor HookDescriptor) (*exec.Cmd, error) {
	shell := descriptor.Shell
	if shell == "" {
		shell = "bash"
	}
	switch shell {
	case "powershell":
		name := "pwsh"
		if runtime.GOOS == "windows" {
			name = "powershell.exe"
		}
		return exec.CommandContext(ctx, name, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", descriptor.Command), nil
	case "bash":
		name, err := exec.LookPath("bash")
		if err != nil {
			return nil, errors.New("bash hook requested but bash is unavailable")
		}
		// A hook is already an explicit command capability. Login/profile files
		// are unrelated ambient code and must not run before that command.
		return exec.CommandContext(ctx, name, "--noprofile", "--norc", "-c", descriptor.Command), nil
	case "sh":
		return exec.CommandContext(ctx, "sh", "-c", descriptor.Command), nil
	default:
		return nil, fmt.Errorf("unsupported hook shell %q", shell)
	}
}

func (runner *HookRunner) commandEnvironment(descriptor HookDescriptor) ([]string, error) {
	fixed := make(map[string]string, 3)
	if runner.ProjectRoot != "" {
		fixed["AGENTX_PROJECT_DIR"] = runner.ProjectRoot
	}
	if descriptor.PluginRoot != "" {
		fixed["AGENTX_PLUGIN_ROOT"] = descriptor.PluginRoot
	}
	if descriptor.PluginDataDir != "" {
		fixed["AGENTX_PLUGIN_DATA"] = descriptor.PluginDataDir
	}
	return childenv.Hook(runner.Environment, runner.CommandEnvAllow, fixed)
}

func sensitiveEnvironmentName(name string) bool {
	return childenv.SensitiveName(name)
}

func (runner *HookRunner) executeHTTP(ctx context.Context, descriptor HookDescriptor, payload []byte) (HookResult, *redact.Set) {
	target, err := url.Parse(descriptor.URL)
	if err != nil {
		// URL parse errors can quote the complete URL, including query
		// credentials. Configuration diagnostics already identify the hook.
		return HookResult{Err: errors.New("parse HTTP hook URL"), Continue: true, ExitCode: -1}, nil
	}
	if err := runner.validateHTTPHookTarget(target); err != nil {
		return HookResult{Err: err, Continue: true, ExitCode: -1}, nil
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         runner.guardedDialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("HTTP hook redirects are disabled")
		},
	}
	expandedHeaders, expansionShapeErr, expansionWorkloadErr := runner.expandHTTPHeaders(descriptor)
	if expansionShapeErr != nil {
		return HookResult{
			Err:      errors.New(httpHookShapeErrorMessage(expansionShapeErr)),
			Continue: true, ExitCode: -1,
		}, nil
	}
	responseSet, sanitizerErr := httpHookResponseSanitizer(runner.credentialSet, target, expandedHeaders, descriptor.SensitivePathSegments)
	if httpHookScopeIsUnexecutable(sanitizerErr) {
		return HookResult{
			Err:      errors.New(httpHookShapeErrorMessage(sanitizerErr)),
			Continue: true, ExitCode: -1,
		}, runner.credentialSet
	}
	if preflightHTTPHookRequest(target, expandedHeaders) != nil {
		return HookResult{
			Err:      errors.New("serialize HTTP hook request"),
			Continue: true, ExitCode: -1,
		}, nil
	}
	if expansionWorkloadErr != nil || sanitizerErr != nil {
		return HookResult{
			Err:      errors.New("HTTP hook credential redaction workload exceeds its limit"),
			Continue: true, ExitCode: -1,
		}, runner.credentialSet
	}
	if !runner.credentialSet.Covers(responseSet) {
		message := "HTTP hook response credentials require a frozen session scope"
		if runner.credentialFrozen {
			message = "HTTP hook response credential scope differs from the frozen session scope"
		}
		return HookResult{
			Err:      errors.New(message),
			Continue: true, ExitCode: -1,
		}, runner.credentialSet
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return HookResult{Err: errors.New("construct HTTP hook request"), Continue: true, ExitCode: -1}, nil
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range expandedHeaders {
		request.Header.Set(name, value)
	}
	resultSet := responseSet
	if responseSet.Empty() && (runner.credentialSet == nil || runner.credentialSet.Empty()) {
		// Preserve the legacy sanitizer for callers that have not installed an
		// exact set and have no response-scoped literals to union with it.
		resultSet = nil
	}
	response, err := client.Do(request)
	if err != nil {
		return HookResult{Err: safeHTTPHookError(err), Continue: true, ExitCode: -1}, resultSet
	}
	defer response.Body.Close()
	limit := runner.outputLimit()
	captureLimit := limit + responseSet.MaxLiteralBytes()
	if captureLimit < limit {
		captureLimit = limit
	}
	body, rawTruncated, readErr := readBounded(response.Body, captureLimit)
	result := HookResult{ExitCode: 0, Truncated: rawTruncated, Continue: true}
	if readErr != nil {
		switch {
		case errors.Is(readErr, context.Canceled):
			result.Err = context.Canceled
		case errors.Is(readErr, context.DeadlineExceeded):
			result.Err = context.DeadlineExceeded
		default:
			result.Err = errors.New("read HTTP hook response")
		}
		return result, resultSet
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if resultSet != nil {
			var suppressed bool
			result.Stdout, result.Truncated, suppressed = projectHookOutput(resultSet, string(body), rawTruncated, limit)
			if suppressed {
				result.Stdout = ""
			}
		} else {
			if rawTruncated {
				result.Stdout = ""
			} else {
				result.Stdout = string(body)
			}
		}
		result.ExitCode = response.StatusCode
		result.Err = fmt.Errorf("HTTP hook returned status %d", response.StatusCode)
		return result, resultSet
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if rawTruncated {
		result.Err = errors.New("invalid HTTP hook response: response exceeds output limit")
		return result, resultSet
	}
	if runner.Sanitize != nil && (runner.credentialSet == nil || runner.credentialSet.Empty()) && !responseSet.Empty() {
		// An opaque legacy callback cannot be unioned with response-scoped
		// literals. Drop the complete response rather than chain independent
		// marker strategies that can recreate one another's credentials.
		result.Err = errors.New("HTTP hook response could not be safely sanitized")
		return result, resultSet
	}
	if resultSet != nil {
		body, err = resultSet.JSON(body)
		if err != nil {
			result.Err = errors.New("invalid HTTP hook response: response could not be safely sanitized")
			return result, resultSet
		}
		if len(body) > runner.outputLimit() {
			result.Truncated = true
			result.Err = errors.New("invalid HTTP hook response: sanitized response exceeds output limit")
			return result, resultSet
		}
	}
	result.Stdout = string(body)
	if err := parseHookStructuredResult(body, descriptor.Event, &result); err != nil {
		result.Err = fmt.Errorf("invalid HTTP hook response: %w", err)
	}
	return result, resultSet
}

func boundedHookCredentialSet(values []string) (*redact.Set, error) {
	seen := make(map[string]struct{})
	bounded := make([]string, 0, len(values))
	totalBytes := 0
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		if len(bounded) >= maximumHookCredentialLiterals ||
			len(value) > maximumHookCredentialLiteralBytes-totalBytes {
			return redact.New(), errors.New("hook credential material exceeds redaction workload limit")
		}
		seen[value] = struct{}{}
		bounded = append(bounded, value)
		totalBytes += len(value)
	}
	return redact.New(bounded...), nil
}

func httpHookResponseSanitizer(base *redact.Set, target *url.URL, headers map[string]string, sensitivePathSegments []int) (*redact.Set, error) {
	// Validate every shape before counting a single literal. Configuration maps
	// are intentionally unordered; malformed material must always be isolated
	// rather than racing an unrelated workload limit.
	pathAliases := make([]string, 0)
	queryAliases := make([]string, 0)
	if target != nil {
		if len(sensitivePathSegments) > 0 {
			canonicalPath := (&url.URL{Path: target.Path}).EscapedPath()
			pathAliases = append(pathAliases, target.RawPath, target.EscapedPath(), target.Path, canonicalPath)
			rawSegments := nonemptyEscapedPathSegments(target.RawPath)
			escapedSegments := nonemptyEscapedPathSegments(target.EscapedPath())
			for _, index := range sensitivePathSegments {
				if index < 0 || index >= len(escapedSegments) {
					return nil, errHTTPHookSensitivePathInvalid
				}
				aliases := []string{escapedSegments[index]}
				if len(rawSegments) == len(escapedSegments) {
					aliases = append(aliases, rawSegments[index])
				}
				decodedSegment, err := url.PathUnescape(escapedSegments[index])
				if err != nil {
					return nil, errHTTPHookSensitivePathInvalid
				}
				pathAliases = append(pathAliases, append(aliases, decodedSegment, url.PathEscape(decodedSegment))...)
				for _, component := range strings.Split(decodedSegment, "/") {
					if component != "" {
						pathAliases = append(pathAliases, component, url.PathEscape(component))
					}
				}
			}
		}
		for _, field := range strings.Split(target.RawQuery, "&") {
			if field == "" {
				continue
			}
			_, value, hasValue := strings.Cut(field, "=")
			if hasValue {
				queryAliases = append(queryAliases, value)
			}
		}
		query, err := url.ParseQuery(target.RawQuery)
		if err != nil {
			return nil, errHTTPHookQueryInvalid
		}
		queryNames := make([]string, 0, len(query))
		for name := range query {
			queryNames = append(queryNames, name)
		}
		sort.Strings(queryNames)
		for _, name := range queryNames {
			for _, value := range query[name] {
				queryAliases = append(queryAliases, value, url.QueryEscape(value))
			}
		}
	}

	type headerScope struct {
		value   string
		aliases []string
	}
	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	headerScopes := make([]headerScope, 0, len(headerNames))
	for _, name := range headerNames {
		if !validHTTPHeaderName(name) {
			return nil, errHTTPHookHeaderNameInvalid
		}
		value := headers[name]
		if !validHTTPHeaderValue(value) {
			return nil, errHTTPHookHeaderValueInvalid
		}
		wireValue := textproto.TrimString(value)
		scope := headerScope{}
		if wireValue != "" {
			scope.value = value
			scope.aliases = append(scope.aliases, wireValue)
		}
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
			fields := strings.Fields(wireValue)
			if len(fields) != 2 || fields[1] == "" {
				return nil, errHTTPHookAuthorizationMalformed
			}
			switch {
			case strings.EqualFold(fields[0], "basic"):
				decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(fields[1])
				if decodeErr != nil {
					decoded, decodeErr = base64.RawStdEncoding.Strict().DecodeString(fields[1])
				}
				if decodeErr != nil {
					return nil, errHTTPHookAuthorizationMalformed
				}
				padded := base64.StdEncoding.EncodeToString(decoded)
				raw := base64.RawStdEncoding.EncodeToString(decoded)
				if fields[1] != padded && fields[1] != raw {
					return nil, errHTTPHookAuthorizationMalformed
				}
				username, password, ok := strings.Cut(string(decoded), ":")
				if !ok {
					return nil, errHTTPHookAuthorizationMalformed
				}
				scope.aliases = append(scope.aliases, fields[1], padded, raw, string(decoded), username, password)
			case strings.EqualFold(fields[0], "bearer"):
				if !validBearerToken(fields[1]) {
					return nil, errHTTPHookAuthorizationMalformed
				}
				scope.aliases = append(scope.aliases, fields[1])
			default:
				return nil, errHTTPHookAuthorizationMalformed
			}
		}
		headerScopes = append(headerScopes, scope)
	}

	seen := make(map[string]struct{})
	secrets := make([]string, 0)
	totalBytes := 0
	addExact := func(value string) error {
		if value == "" {
			return nil
		}
		if _, exists := seen[value]; exists {
			return nil
		}
		if len(secrets) >= maximumHookCredentialLiterals ||
			len(value) > maximumHookCredentialLiteralBytes-totalBytes {
			return errHTTPHookCredentialWorkload
		}
		seen[value] = struct{}{}
		secrets = append(secrets, value)
		totalBytes += len(value)
		return nil
	}
	for _, value := range pathAliases {
		if value != "/" {
			if err := addExact(value); err != nil {
				return nil, err
			}
		}
	}
	for _, value := range queryAliases {
		if err := addExact(value); err != nil {
			return nil, err
		}
	}
	for _, scope := range headerScopes {
		if err := addExact(scope.value); err != nil {
			return nil, err
		}
		for _, alias := range scope.aliases {
			if err := addExact(alias); err != nil {
				return nil, err
			}
		}
	}
	result := base.With(secrets...)
	if result.LiteralCount() > maximumHookCredentialLiterals ||
		result.TotalLiteralBytes() > maximumHookCredentialLiteralBytes {
		return nil, errHTTPHookCredentialWorkload
	}
	return result, nil
}

func httpHookScopeIsUnexecutable(err error) bool {
	return errors.Is(err, errHTTPHookAuthorizationMalformed) ||
		errors.Is(err, errHTTPHookHeaderExpansionInvalid) ||
		errors.Is(err, errHTTPHookHeaderNameInvalid) ||
		errors.Is(err, errHTTPHookHeaderValueInvalid) ||
		errors.Is(err, errHTTPHookQueryInvalid) ||
		errors.Is(err, errHTTPHookSensitivePathInvalid)
}

func httpHookShapeErrorMessage(err error) string {
	switch {
	case errors.Is(err, errHTTPHookAuthorizationMalformed):
		return "HTTP hook authorization is malformed"
	case errors.Is(err, errHTTPHookHeaderExpansionInvalid):
		return "HTTP hook header expansion is invalid"
	case errors.Is(err, errHTTPHookHeaderNameInvalid):
		return "HTTP hook header name is invalid"
	case errors.Is(err, errHTTPHookHeaderValueInvalid):
		return "HTTP hook header value is invalid"
	case errors.Is(err, errHTTPHookQueryInvalid):
		return "HTTP hook query configuration is invalid"
	case errors.Is(err, errHTTPHookSensitivePathInvalid):
		return "HTTP hook sensitive path configuration is invalid"
	default:
		return "HTTP hook configuration is invalid"
	}
}

func safeHTTPHookError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if strings.Contains(err.Error(), "blocked address") {
		return errors.New("HTTP hook target blocked by SSRF policy")
	}
	return errors.New("HTTP hook request failed")
}

func (runner *HookRunner) validateHTTPHookTarget(target *url.URL) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("HTTP hook URL must use http or https")
	}
	if target.Hostname() == "" || target.User != nil {
		return errors.New("HTTP hook URL must have a host and no user information")
	}
	if target.Fragment != "" {
		return errors.New("HTTP hook URL fragments are not allowed")
	}
	if runner.AllowedHTTPURLs != nil {
		allowed := false
		for _, pattern := range *runner.AllowedHTTPURLs {
			if wildcardMatch(pattern, target.String()) {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.New("HTTP hook URL is not allowed by policy")
		}
	}
	return nil
}

func wildcardMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		relative := strings.Index(value[position:], part)
		if relative < 0 || index == 0 && !strings.HasPrefix(value, part) {
			return false
		}
		position += relative + len(part)
	}
	return strings.HasSuffix(pattern, "*") || strings.HasSuffix(value, parts[len(parts)-1])
}

func (runner *HookRunner) guardedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	resolver := runner.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := callHookResolver(resolver, ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("HTTP hook host has no addresses")
	}
	for _, address := range addresses {
		if disallowedHookIP(address.IP) {
			return nil, fmt.Errorf("HTTP hook host resolves to blocked address %s", address.IP)
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}
	// Pin the connection to the already validated address. A later DNS answer
	// cannot redirect the request to a private target.
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

func callHookResolver(resolver hookResolver, ctx context.Context, host string) (addresses []net.IPAddr, err error) {
	defer func() {
		if recover() != nil {
			addresses = nil
			err = errors.New("resolve HTTP hook host")
		}
	}()
	addresses, err = resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("resolve HTTP hook host")
	}
	cloned := make([]net.IPAddr, len(addresses))
	for index, address := range addresses {
		cloned[index] = address
		cloned[index].IP = append(net.IP(nil), address.IP...)
		cloned[index].Zone = strings.Clone(address.Zone)
	}
	return cloned, nil
}

func disallowedHookIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() {
		return false
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		first, second := ipv4[0], ipv4[1]
		return first == 0 || first == 10 || first == 127 ||
			(first == 100 && second >= 64 && second <= 127) ||
			(first == 169 && second == 254) ||
			(first == 172 && second >= 16 && second <= 31) ||
			(first == 192 && second == 168)
	}
	return ip.IsPrivate()
}

func (runner *HookRunner) expandHTTPHeader(template string, hookAllow []string) (string, error) {
	hookSet := make(map[string]bool, len(hookAllow))
	for _, name := range hookAllow {
		hookSet[name] = true
	}
	var output strings.Builder
	capacity := len(template)
	if capacity > maximumHookCredentialLiteralBytes {
		capacity = maximumHookCredentialLiteralBytes
	}
	output.Grow(capacity)
	appendBounded := func(value string) error {
		value = stripHeaderControls(value)
		if len(value) > maximumHookCredentialLiteralBytes-output.Len() {
			return errHTTPHookCredentialWorkload
		}
		output.WriteString(value)
		return nil
	}
	cursor := 0
	for cursor < len(template) {
		relativeStart := strings.Index(template[cursor:], "${")
		if relativeStart < 0 {
			if err := appendBounded(template[cursor:]); err != nil {
				return "", err
			}
			cursor = len(template)
			break
		}
		start := cursor + relativeStart
		if err := appendBounded(template[cursor:start]); err != nil {
			return "", err
		}
		relativeEnd := strings.IndexByte(template[start+2:], '}')
		if relativeEnd < 0 {
			return "", errHTTPHookHeaderExpansionInvalid
		}
		end := start + 2 + relativeEnd
		name := template[start+2 : end]
		if name == "" || strings.Contains(name, "${") {
			return "", errHTTPHookHeaderExpansionInvalid
		}
		value := ""
		if hookSet[name] && runner.HTTPEnvAllow[name] && !sensitiveEnvironmentName(name) {
			value = stripHeaderControls(runner.Environment[name])
		}
		if strings.Contains(value, "${") {
			return "", errHTTPHookHeaderExpansionInvalid
		}
		if err := appendBounded(value); err != nil {
			return "", err
		}
		cursor = end + 1
	}
	result := output.String()
	if strings.Contains(result, "${") {
		return "", errHTTPHookHeaderExpansionInvalid
	}
	if !validHTTPHeaderValue(result) {
		return "", errHTTPHookHeaderValueInvalid
	}
	return result, nil
}

func (runner *HookRunner) expandHTTPHeaders(descriptor HookDescriptor) (map[string]string, error, error) {
	names := make([]string, 0, len(descriptor.Headers))
	for name := range descriptor.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !validHTTPHeaderName(name) {
			return map[string]string{}, errHTTPHookHeaderNameInvalid, nil
		}
	}
	expanded := make(map[string]string, len(names))
	var shapeErr error
	var workloadErr error
	for _, name := range names {
		value, err := runner.expandHTTPHeader(descriptor.Headers[name], descriptor.AllowedEnvVars)
		switch {
		case errors.Is(err, errHTTPHookCredentialWorkload):
			if workloadErr == nil {
				workloadErr = err
			}
		case err != nil:
			if shapeErr == nil {
				shapeErr = err
			}
		default:
			expanded[name] = value
		}
	}
	return expanded, shapeErr, workloadErr
}

func preflightHTTPHookRequest(target *url.URL, headers map[string]string) error {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target.String(), http.NoBody)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		request.Header.Set(name, headers[name])
	}
	return request.Write(io.Discard)
}

func validBearerToken(token string) bool {
	if token == "" {
		return false
	}
	padding := false
	for index := 0; index < len(token); index++ {
		character := token[index]
		if character == '=' {
			padding = true
			continue
		}
		if padding {
			return false
		}
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '-', '.', '_', '~', '+', '/':
		default:
			return false
		}
	}
	return true
}

func stripHeaderControls(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

func parseHookStructuredResult(data []byte, expected HookEventName, result *HookResult) error {
	var wire struct {
		Continue           *bool           `json:"continue"`
		SuppressOutput     bool            `json:"suppressOutput"`
		StopReason         string          `json:"stopReason"`
		Decision           string          `json:"decision"`
		Reason             string          `json:"reason"`
		SystemMessage      string          `json:"systemMessage"`
		HookSpecificOutput json.RawMessage `json:"hookSpecificOutput"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&wire); err != nil {
		return errors.New("hook response is invalid JSON")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("hook response contains trailing JSON")
	}
	result.Continue = true
	if wire.Continue != nil {
		result.Continue = *wire.Continue
	}
	result.SuppressOutput = wire.SuppressOutput
	result.Reason = wire.Reason
	if result.Reason == "" {
		result.Reason = wire.StopReason
	}
	result.SystemMessage = wire.SystemMessage
	switch strings.ToLower(wire.Decision) {
	case "approve", "allow":
		result.Decision = HookDecisionAllow
	case "block", "deny":
		result.Decision = HookDecisionDeny
	case "ask":
		result.Decision = HookDecisionAsk
	case "":
	default:
		return errors.New("hook response has an unsupported decision")
	}
	if len(wire.HookSpecificOutput) > 0 && string(wire.HookSpecificOutput) != "null" {
		var specific struct {
			HookEventName            HookEventName  `json:"hook_event_name"`
			PermissionDecision       string         `json:"permissionDecision"`
			PermissionDecisionReason string         `json:"permissionDecisionReason"`
			AdditionalContext        string         `json:"additionalContext"`
			UpdatedInput             map[string]any `json:"updatedInput"`
		}
		if err := json.Unmarshal(wire.HookSpecificOutput, &specific); err != nil {
			return errors.New("hook-specific output is invalid")
		}
		if specific.HookEventName != "" && specific.HookEventName != expected {
			return errors.New("hook response event does not match the dispatched event")
		}
		switch strings.ToLower(specific.PermissionDecision) {
		case "allow":
			result.Decision = HookDecisionAllow
		case "ask":
			result.Decision = HookDecisionAsk
		case "deny":
			result.Decision = HookDecisionDeny
		case "":
		default:
			return errors.New("hook response has an unsupported permission decision")
		}
		if specific.PermissionDecisionReason != "" {
			result.Reason = specific.PermissionDecisionReason
		}
		result.Context = specific.AdditionalContext
		result.UpdatedInput = specific.UpdatedInput
	}
	return nil
}

func applyHookAuthority(result *HookResult, event HookEventName) {
	if result.ExitCode == 2 && hookEventCanBlock(event) {
		result.Decision = HookDecisionDeny
		if result.Reason == "" {
			result.Reason = strings.TrimSpace(result.Stderr)
		}
	}
	if result.ExitCode != 0 && result.ExitCode != 2 {
		// Operational hook failures are nonblocking unless a valid structured
		// result deliberately set continue=false.
		if result.Decision != HookDecisionDeny {
			result.Decision = HookDecisionNone
		}
	}
}

func hookEventCanBlock(event HookEventName) bool {
	switch event {
	case HookPreToolUse, HookPermissionRequest, HookUserPromptSubmit, HookStop,
		HookSubagentStop, HookPreCompact, HookTeammateIdle, HookTaskCreated,
		HookTaskCompleted, HookElicitation, HookElicitationResult, HookConfigChange:
		return true
	default:
		return false
	}
}

func hookPlainOutputIsContext(event HookEventName) bool {
	switch event {
	case HookPostToolUse, HookPostToolUseFailure, HookPermissionDenied,
		HookUserPromptSubmit, HookSessionStart, HookSetup, HookSubagentStart,
		HookPreCompact:
		return true
	default:
		return false
	}
}

func (runner *HookRunner) outputLimit() int {
	if runner.OutputLimit <= 0 {
		return defaultHookOutputLimit
	}
	return runner.OutputLimit
}

func (runner *HookRunner) redactionLookahead() int {
	if runner.credentialSet == nil {
		return 0
	}
	maximum := runner.credentialSet.MaxLiteralBytes()
	if maximum <= 1 {
		return 0
	}
	return maximum - 1
}

func (runner *HookRunner) sanitizeCapturedOutput(value string, rawTruncated bool, limit int) (string, bool) {
	if runner.credentialSet != nil && !runner.credentialSet.Empty() {
		safe, truncated, _ := projectHookOutput(runner.credentialSet, value, rawTruncated, limit)
		return safe, truncated
	}
	if runner.Sanitize != nil {
		if rawTruncated {
			return "", true
		}
		return runner.sanitizeText(value), false
	}
	return value, rawTruncated
}

func projectHookOutput(set *redact.Set, value string, rawTruncated bool, limit int) (string, bool, bool) {
	safe, projected, suppressed := set.RedactBounded(value, limit)
	if suppressed {
		return "", true, true
	}
	if rawTruncated && !projected {
		marker := set.TerminalMarker()
		if marker == "" {
			return "", true, true
		}
		safe += marker
	}
	return safe, rawTruncated || projected, false
}

type cappedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{remaining: limit} }

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(data)
	if len(data) > buffer.remaining {
		data = data[:buffer.remaining]
		buffer.truncated = true
	}
	if len(data) > 0 {
		_, _ = buffer.buffer.Write(data)
		buffer.remaining -= len(data)
	}
	if original > len(data) {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *cappedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *cappedBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

func readBounded(reader io.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		limit = defaultHookOutputLimit
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}
