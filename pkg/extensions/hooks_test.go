package extensions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
)

type panickingHookResolver struct {
	payload any
}

func (resolver panickingHookResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	panic(resolver.payload)
}

type panickingHookPanicPayload struct {
	calls  *int
	secret string
}

func (payload *panickingHookPanicPayload) String() string {
	*payload.calls++
	panic("hook panic payload String must not run: " + payload.secret)
}

func (payload *panickingHookPanicPayload) Format(fmt.State, rune) {
	*payload.calls++
	panic("hook panic payload Format must not run: " + payload.secret)
}

type panickingHookError struct {
	calls  *int
	secret string
}

func (err panickingHookError) Error() string {
	*err.calls++
	panic("hook error formatter must be contained: " + err.secret)
}

func TestHookConcurrentDecisionPrecedence(t *testing.T) {
	descriptors := []HookDescriptor{
		{ID: "allow", Event: HookPreToolUse, Matcher: "Bash", Kind: HookKindCommand, Shell: "sh", Command: `sleep 0.03; printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"safe","updatedInput":{"command":"safe"}}}'`, Source: SourceUser, Timeout: time.Second},
		{ID: "deny", Event: HookPreToolUse, Matcher: "Bash", Kind: HookKindCommand, Shell: "sh", Command: `printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"policy"}}'`, Source: SourceUser, Timeout: time.Second},
		{ID: "ask", Event: HookPreToolUse, Matcher: "Bash", Kind: HookKindCommand, Shell: "sh", Command: `sleep 0.01; printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"confirm"}}'`, Source: SourceUser, Timeout: time.Second},
	}
	snapshot := NewHookManager().Reload(descriptors)
	runner := NewHookRunner()
	aggregate, err := runner.Dispatch(context.Background(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "true"}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Decision != HookDecisionDeny || aggregate.Reason != "policy" || len(aggregate.Results) != 3 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	if aggregate.UpdatedInput != nil {
		t.Fatalf("losing allow input escaped aggregation: %#v", aggregate.UpdatedInput)
	}
	for _, result := range aggregate.Results {
		if result.Err != nil {
			t.Fatalf("hook %s failed: %v", result.HookID, result.Err)
		}
	}
}

func TestHookCancellationOutputCapAndSecretSafeEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertions are Unix-specific")
	}
	manager := NewHookManager()
	snapshot := manager.Reload([]HookDescriptor{{
		ID: "bounded", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
		Command: `printf '%s|' "$SAFE_VALUE"; printf '%s' "$AZURE_OPENAI_SUBSCRIPTION_KEY"; yes x | head -c 4096`,
		Source:  SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.OutputLimit = 64
	runner.Environment = map[string]string{"SAFE_VALUE": "visible", "AZURE_OPENAI_SUBSCRIPTION_KEY": "never-leak"}
	runner.CommandEnvAllow = map[string]bool{"SAFE_VALUE": true, "AZURE_OPENAI_SUBSCRIPTION_KEY": true}
	aggregate, err := runner.Dispatch(context.Background(), snapshot, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookNotification, Fields: map[string]any{"message": "x", "notification_type": "idle"}})
	if err != nil {
		t.Fatal(err)
	}
	result := aggregate.Results[0]
	if !strings.HasPrefix(result.Stdout, "visible|") || strings.Contains(result.Stdout, "never-leak") || !result.Truncated || len(result.Stdout) != 64 {
		t.Fatalf("bounded secret-safe output = %#v", result)
	}

	timeoutSnapshot := manager.Reload([]HookDescriptor{{
		ID: "timeout", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
		Command: "exec sleep 5", Source: SourceUser, Timeout: 25 * time.Millisecond,
	}})
	started := time.Now()
	aggregate, err = runner.Dispatch(context.Background(), timeoutSnapshot, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookNotification, Fields: map[string]any{"notification_type": "idle"}})
	if err != nil {
		t.Fatal(err)
	}
	if !aggregate.Results[0].TimedOut || time.Since(started) > time.Second {
		t.Fatalf("hook timeout was not bounded: %#v after %s", aggregate.Results[0], time.Since(started))
	}
}

func TestHookCommandUsesFrozenSnapshotAndSuppressesBashProfiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash profile assertion is Unix-specific")
	}
	home := t.TempDir()
	marker := filepath.Join(home, "profile-ran")
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(`touch "$HOME/profile-ran"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVE_SECRET", "must-not-inherit")
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "snapshot", Event: HookNotification, Kind: HookKindCommand, Shell: "bash",
		Command: `printf '%s|%s' "$LIVE_SECRET" "$SAFE_VALUE"`, Source: SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.ProjectRoot = home
	runner.Environment = map[string]string{"HOME": home, "PATH": os.Getenv("PATH"), "SAFE_VALUE": "snapshot"}
	runner.CommandEnvAllow = map[string]bool{"SAFE_VALUE": true}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{SessionID: "s", CWD: home, Event: HookNotification, Fields: map[string]any{"notification_type": "idle"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := aggregate.Results[0].Stdout; got != "|snapshot" {
		t.Fatalf("frozen hook environment output = %q", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Bash login profile ran before hook: %v", err)
	}
}

func TestHookResultSanitizerRunsBeforeAggregation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	const secret = "production-subscription-secret"
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "sanitize", Event: HookUserPromptSubmit, Kind: HookKindCommand, Shell: "sh",
		Command: `printf '%s' '` + secret + `'`, Source: SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.SetCredentialLiterals(secret)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{SessionID: "s", CWD: runner.ProjectRoot, Event: HookUserPromptSubmit})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("hook sanitizer output = %s", encoded)
	}
}

func TestHookAggregateRejectsCredentialAcrossOneResultEnvelope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	const secret = `left","stderr":"right`
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "cross-field", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
		Command: `printf '%s' left; printf '%s' right >&2`,
		Source:  SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.SetCredentialLiterals(secret)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: runner.ProjectRoot, Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err == nil || !strings.Contains(err.Error(), "aggregate could not be safely projected") {
		t.Fatalf("cross-field hook aggregate error = %v", err)
	}
	if aggregate.Decision != HookDecisionNone || aggregate.Reason != "" ||
		aggregate.UpdatedInput != nil || len(aggregate.Contexts) != 0 ||
		len(aggregate.Results) != 0 || aggregate.Continue {
		t.Fatalf("unsafe hook aggregate retained authority or output: %#v", aggregate)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("hook aggregate failure exposed the credential")
	}
}

func TestHookAggregateSafetyEnvelopeIncludesPublicResultErrors(t *testing.T) {
	const secret = "credential-in-public-hook-error"
	runner := NewHookRunner()
	runner.SetCredentialLiterals(secret)
	aggregate := HookAggregate{
		Continue: true,
		Results: []HookResult{{
			HookID: "failed", Event: HookNotification, Continue: true,
			ExitCode: -1, Err: errors.New("failure: " + secret),
		}},
	}
	projected, err := runner.finalizeHookAggregate(aggregate, nil)
	if err == nil || !strings.Contains(err.Error(), "aggregate could not be safely projected") {
		t.Fatalf("public hook error safety result = %#v, %v", projected, err)
	}
	if len(projected.Results) != 0 || projected.Continue ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe public hook error escaped fail-closed projection: %#v, %v", projected, err)
	}
}

func TestHookAggregateUnionsResponseCredentialsAcrossSiblingResults(t *testing.T) {
	const secret = `0},{"hook_id":"second`
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	snapshot := NewHookManager().Reload([]HookDescriptor{
		{
			ID: "first", Event: HookNotification, Kind: HookKindHTTP,
			URL:    server.URL + "?token=" + url.QueryEscape(secret),
			Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "second", Event: HookNotification, Kind: HookKindHTTP,
			URL: server.URL, Source: SourceManaged, Timeout: time.Second,
		},
	})
	runner := NewHookRunner()
	freezeHookRunner(t, runner, snapshot)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err == nil || !strings.Contains(err.Error(), "aggregate could not be safely projected") {
		t.Fatalf("cross-result response credential error = %v; aggregate=%#v", err, aggregate)
	}
	if len(aggregate.Results) != 0 || aggregate.Continue || aggregate.Decision != HookDecisionNone {
		t.Fatalf("cross-result credential retained hook output or authority: %#v", aggregate)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("cross-result hook failure exposed the response credential")
	}
}

func TestHookStructuredParserErrorsDoNotEchoResponseControlledValues(t *testing.T) {
	const responseValue = "response-controlled-parser-value"
	tests := []struct {
		name string
		wire string
	}{
		{name: "decision", wire: `{"decision":"` + responseValue + `"}`},
		{name: "permission decision", wire: `{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"` + responseValue + `"}}`},
		{name: "event", wire: `{"hookSpecificOutput":{"hook_event_name":"` + responseValue + `"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var result HookResult
			err := parseHookStructuredResult([]byte(test.wire), HookPreToolUse, &result)
			if err == nil || strings.Contains(err.Error(), responseValue) {
				t.Fatalf("parser diagnostic = %v", err)
			}
		})
	}
}

func TestHookAggregateBoundsFinalSiblingCredentialUnion(t *testing.T) {
	requested := make(chan struct{}, 2)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	makeQuery := func(prefix string) string {
		values := make(url.Values)
		for index := 0; index < maximumHookCredentialLiterals/2+1; index++ {
			values.Add("token", fmt.Sprintf("%s-%03d", prefix, index))
		}
		return values.Encode()
	}
	snapshot := NewHookManager().Reload([]HookDescriptor{
		{
			ID: "first", Event: HookNotification, Kind: HookKindHTTP,
			URL:    server.URL + "?" + makeQuery("first"),
			Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "second", Event: HookNotification, Kind: HookKindHTTP,
			URL:    server.URL + "?" + makeQuery("second"),
			Source: SourceManaged, Timeout: time.Second,
		},
	})
	runner := NewHookRunner()
	if _, err := runner.FreezeCredentialSanitizer(snapshot, redact.New()); err == nil ||
		!strings.Contains(err.Error(), "could not be composed safely") {
		t.Fatalf("oversized frozen sibling scope error = %v", err)
	}
	if len(requested) != 0 {
		t.Fatalf("frozen sibling scope executed %d requests during startup", len(requested))
	}
}

func TestFreezeHookCredentialSanitizerPromotesResponseScopesForDownstreamFrames(t *testing.T) {
	const (
		contextSecret = "<hook_context>\nsafe"
		pathSecret    = "path/token value"
		username      = "hook-user"
		password      = "hook-password"
	)
	basicToken := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	escapedPathSecret := url.PathEscape(pathSecret)
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "frozen", Event: HookUserPromptSubmit, Kind: HookKindHTTP,
		URL: "https://hooks.example.invalid/submit/" + escapedPathSecret + "?token=" + url.QueryEscape(contextSecret),
		Headers: map[string]string{
			"Authorization": "Basic " + basicToken,
		},
		SensitivePathSegments: []int{1}, Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials, err := runner.FreezeCredentialSanitizer(snapshot, redact.New("session-secret"))
	if err != nil {
		t.Fatal(err)
	}
	for label, literal := range map[string]string{
		"session":         "session-secret",
		"decoded query":   contextSecret,
		"encoded query":   url.QueryEscape(contextSecret),
		"decoded path":    "/submit/" + pathSecret,
		"escaped path":    "/submit/" + escapedPathSecret,
		"decoded segment": pathSecret,
		"escaped segment": escapedPathSecret,
		"authorization":   "Basic " + basicToken,
		"basic token":     basicToken,
		"decoded basic":   username + ":" + password,
		"basic username":  username,
		"basic password":  password,
	} {
		if !credentials.Covers(redact.New(literal)) {
			t.Fatalf("frozen credential set omitted %s scope", label)
		}
	}
	framed := "prompt\n\n<hook_context>\nsafe\n</hook_context>"
	projected := credentials.Apply(framed)
	if strings.Contains(projected, contextSecret) || projected == framed {
		t.Fatalf("downstream hook-context framing was not protected: %q", projected)
	}
}

func TestSensitivePathSegmentReflectionIsSanitizedWithoutPromotingRouteSegments(t *testing.T) {
	const (
		escapedSecret = "path%2Ftoken"
		decodedSecret = "path/token"
	)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		segments := nonemptyEscapedPathSegments(request.URL.EscapedPath())
		if len(segments) != 4 {
			http.Error(writer, "unexpected route", http.StatusBadRequest)
			return
		}
		reflected, err := url.PathUnescape(segments[3])
		if err != nil {
			http.Error(writer, "invalid route", http.StatusBadRequest)
			return
		}
		components := strings.Split(reflected, "/")
		tail := components[len(components)-1]
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hook_event_name":          "PreToolUse",
				"permissionDecision":       "ask",
				"permissionDecisionReason": tail,
				"additionalContext":        "review " + reflected,
			},
		})
	}))
	defer server.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "selected-segment", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL:                   server.URL + "/api/v1/hooks/" + escapedSecret,
		SensitivePathSegments: []int{3},
		Source:                SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	for label, literal := range map[string]string{
		"full escaped path": "/api/v1/hooks/" + escapedSecret,
		"full decoded path": "/api/v1/hooks/" + decodedSecret,
		"escaped segment":   escapedSecret,
		"decoded segment":   decodedSecret,
		"decoded head":      "path",
		"decoded tail":      "token",
	} {
		if !credentials.Covers(redact.New(literal)) {
			t.Fatalf("frozen credential set omitted %s", label)
		}
	}
	for _, routeSegment := range []string{"api", "v1", "hooks"} {
		if credentials.Covers(redact.New(routeSegment)) {
			t.Fatalf("unselected route segment %q was promoted as a standalone credential", routeSegment)
		}
	}

	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{escapedSecret, decodedSecret, "path", "token"} {
		if strings.Contains(aggregate.Reason, literal) ||
			len(aggregate.Contexts) > 0 && strings.Contains(aggregate.Contexts[0], literal) {
			t.Fatalf("selected path alias %q escaped HTTP response sanitization: %s", literal, projected)
		}
	}
	if aggregate.Decision != HookDecisionAsk || !strings.Contains(aggregate.Reason, "[REDACTED]") ||
		len(aggregate.Contexts) != 1 || !strings.Contains(aggregate.Contexts[0], "[REDACTED]") {
		t.Fatalf("sanitized path response lost authority or context: %#v", aggregate)
	}
}

func TestFreezeHookCredentialSanitizerSkipsUnexecutableOversizedTarget(t *testing.T) {
	oversized := strings.Repeat("x", maximumHookCredentialLiteralBytes+1)
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "unexecutable", Event: HookNotification, Kind: HookKindHTTP,
		URL:    "ftp://hooks.example.invalid/endpoint?token=" + oversized,
		Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials, err := runner.FreezeCredentialSanitizer(snapshot, redact.New("session-secret"))
	if err != nil {
		t.Fatalf("unexecutable hook scope aborted freeze: %v", err)
	}
	if !credentials.Covers(redact.New("session-secret")) || credentials.Covers(redact.New(oversized)) {
		t.Fatal("unexecutable HTTP target contaminated the frozen response scope")
	}
}

func TestUnfrozenHTTPResponseCredentialScopeFailsBeforeNetwork(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "freeze-required", Event: HookNotification, Kind: HookKindHTTP,
		URL:    server.URL + "?token=response-scope",
		Source: SourceManaged, Timeout: time.Second,
	}})
	aggregate, err := NewHookRunner().Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
		!strings.Contains(aggregate.Results[0].Err.Error(), "require a frozen session scope") {
		t.Fatalf("unfrozen response scope result = %#v", aggregate)
	}
	select {
	case <-requested:
		t.Fatal("unfrozen response credential scope reached the network")
	default:
	}
}

func TestFrozenHookCredentialSanitizerRejectsChangedScopeBeforeNetwork(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "frozen", Event: HookNotification, Kind: HookKindHTTP,
		URL:    server.URL + "?token=initial-scope",
		Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	if _, err := runner.FreezeCredentialSanitizer(snapshot, redact.New("session-secret")); err != nil {
		t.Fatal(err)
	}
	snapshot.Hooks[0].URL = server.URL + "?token=changed-scope"
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
		!strings.Contains(aggregate.Results[0].Err.Error(), "differs from the frozen session scope") {
		t.Fatalf("changed frozen hook scope result = %#v", aggregate)
	}
	select {
	case <-requested:
		t.Fatal("changed response credential scope reached the network")
	default:
	}
}

func TestHookInputPayloadIsSanitizedBeforeCommandEgress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	const secret = "synthetic-input-model-credential"
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "input-sanitize", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
		Command: `if grep -q '` + secret + `' ; then printf leaked; else printf safe; fi`,
		Source:  SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.SetCredentialLiterals(secret)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: runner.ProjectRoot, Event: HookNotification,
		Fields: map[string]any{"message": map[string]any{"nested": []any{"prefix-" + secret}}, "notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := aggregate.Results[0].Stdout; got != "safe" {
		t.Fatalf("hook observed configured credential in input payload: %q", got)
	}
}

func TestHookCommandValidatesExactNewlineFramedInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	marker := filepath.Join(t.TempDir(), "executed")
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "newline-frame", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
		Command: `touch ` + marker, Source: SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.ProjectRoot = t.TempDir()
	runner.SetCredentialLiterals("}\n")
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: runner.ProjectRoot, Event: HookNotification,
		Fields: map[string]any{"message": "safe", "notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil {
		t.Fatalf("newline-framed hook input was accepted: %#v", aggregate.Results)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe hook command executed: %v", err)
	}
}

func TestHookInputSemanticProjectionCoversEscapesEnvelopeAndScalars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	t.Run("escaped raw message and agent envelope", func(t *testing.T) {
		const secret = "a/b"
		snapshot := NewHookManager().Reload([]HookDescriptor{{
			ID: "semantic-input", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
			Command: `if grep -Fq 'a/b'; then printf leaked; else printf safe; fi`,
			Source:  SourceUser, Timeout: time.Second,
		}})
		runner := NewHookRunner()
		runner.ProjectRoot = t.TempDir()
		runner.SetCredentialLiterals(secret)
		aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
			SessionID: "s", CWD: runner.ProjectRoot, AgentID: secret, AgentType: "agent-" + secret,
			Event: HookNotification,
			Fields: map[string]any{
				"message":           json.RawMessage(`{"value":"a\/b"}`),
				"notification_type": "idle",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := aggregate.Results[0].Stdout; got != "safe" {
			t.Fatalf("semantic hook payload exposed credential: %q", got)
		}
	})

	for _, test := range []struct {
		name   string
		secret string
		raw    json.RawMessage
	}{
		{name: "boolean", secret: "true", raw: json.RawMessage(`{"value":true}`)},
		{name: "number", secret: "1", raw: json.RawMessage(`{"value":1}`)},
		{name: "null", secret: "null", raw: json.RawMessage(`{"value":null}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "executed")
			snapshot := NewHookManager().Reload([]HookDescriptor{{
				ID: "scalar-input", Event: HookNotification, Kind: HookKindCommand, Shell: "sh",
				Command: "touch '" + marker + "'", Source: SourceUser, Timeout: time.Second,
			}})
			runner := NewHookRunner()
			runner.SetCredentialLiterals(test.secret)
			_, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
				Fields: map[string]any{"message": test.raw, "notification_type": "idle"},
			})
			if err == nil {
				t.Fatal("nonprojectable scalar hook input did not fail closed")
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("hook executed after scalar sanitization failure: %v", statErr)
			}
		})
	}
}

func TestCommandHookSemanticProjectionRunsBeforeAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "escaped-authority", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindCommand, Shell: "sh",
		Command: `printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"\u0061llow","permissionDecisionReason":"unsafe"}}'`,
		Source:  SourceUser, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	runner.SetCredentialLiterals("allow")
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Decision != HookDecisionNone || aggregate.Results[0].Err == nil ||
		aggregate.Results[0].UpdatedInput != nil {
		t.Fatalf("escaped credential gained hook authority: %#v", aggregate)
	}
}

func TestHookTruncationTerminatesWithSetSafeMarker(t *testing.T) {
	runner := NewHookRunner()
	runner.SetCredentialLiterals("abc")
	first, truncated := runner.sanitizeCapturedOutput("a", true, 1)
	second, _ := runner.sanitizeCapturedOutput("bc", false, 2)
	if !truncated || strings.Contains(first+second, "abc") ||
		!strings.HasSuffix(first, runner.credentialSet.TerminalMarker()) {
		t.Fatalf("cross-result truncation reconstructed a credential: first=%q second=%q", first, second)
	}

	longSecret := strings.Repeat("s", 128)
	runner.SetCredentialLiterals(longSecret)
	safe, truncated := runner.sanitizeCapturedOutput(longSecret, true, 32)
	if !truncated || !strings.HasSuffix(safe, runner.credentialSet.TerminalMarker()) ||
		strings.Contains(safe, longSecret) {
		t.Fatalf("shrinking redaction lacked terminal framing: %q", safe)
	}
}

func TestHTTPHookLoopbackAndSSRFProtection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in sandbox: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input["hook_event_name"] != "PreToolUse" {
			http.Error(writer, "invalid event", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"hookSpecificOutput":{"hook_event_name":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"review"}}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	manager := NewHookManager()
	snapshot := manager.Reload([]HookDescriptor{{
		ID: "local", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL + "/api/v1/hook", Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	for _, routeLiteral := range []string{"api", "v1", "hook", "/api/v1/hook"} {
		if credentials.Covers(redact.New(routeLiteral)) {
			t.Fatalf("ordinary HTTP routing literal %q was promoted into the frozen credential set", routeLiteral)
		}
	}
	aggregate, err := runner.Dispatch(context.Background(), snapshot, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse, Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"}})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Decision != HookDecisionAsk || aggregate.Reason != "review" || aggregate.Results[0].Err != nil {
		t.Fatalf("loopback HTTP hook failed: %#v", aggregate)
	}

	blocked := manager.Reload([]HookDescriptor{{
		ID: "private", Event: HookPreToolUse, Kind: HookKindHTTP,
		URL: "http://10.0.0.1/hook", Source: SourceUser, Timeout: 100 * time.Millisecond,
	}})
	aggregate, err = runner.Dispatch(context.Background(), blocked, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse, Fields: map[string]any{"tool_name": "Read"}})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Results[0].Err == nil || !strings.Contains(aggregate.Results[0].Err.Error(), "SSRF") {
		t.Fatalf("private HTTP target was not blocked: %#v", aggregate.Results[0])
	}

	none := []string{}
	runner.AllowedHTTPURLs = &none
	aggregate, err = runner.Dispatch(context.Background(), snapshot, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse, Fields: map[string]any{"tool_name": "Read"}})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Results[0].Err == nil || !strings.Contains(aggregate.Results[0].Err.Error(), "not allowed") {
		t.Fatalf("empty URL allowlist must deny all: %#v", aggregate.Results[0])
	}
}

func TestHTTPHookCredentialReflectionIsSanitizedBeforeParsing(t *testing.T) {
	const (
		querySecret  = "synthetic-query-\"<&>\\secret"
		headerSecret = "synthetic-header._~+/secret=="
	)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in sandbox: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		queryValue := request.URL.Query().Get("token")
		headerValue := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"reason": queryValue,
			"hookSpecificOutput": map[string]any{
				"hook_event_name":          "PreToolUse",
				"permissionDecision":       "ask",
				"additionalContext":        "reflected " + headerValue,
				"permissionDecisionReason": "review " + queryValue,
			},
		})
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "credential-reflection", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL:     server.URL + "/hook?token=" + url.QueryEscape(querySecret),
		Headers: map[string]string{"Authorization": "Bearer " + headerSecret},
		Source:  SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	freezeHookRunner(t, runner, snapshot)
	aggregate, err := runner.Dispatch(context.Background(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), querySecret) || strings.Contains(string(encoded), headerSecret) {
		t.Fatalf("HTTP hook response reflected configured credentials: %s", encoded)
	}
	if aggregate.Decision != HookDecisionAsk || !strings.Contains(aggregate.Reason, "[REDACTED]") ||
		!strings.Contains(aggregate.Contexts[0], "[REDACTED]") {
		t.Fatalf("sanitized hook response lost authority or context: %#v", aggregate)
	}
}

func TestHTTPHookDecodedBasicCredentialReflectionIsSanitized(t *testing.T) {
	const (
		username = "user"
		password = "password"
		decoded  = username + ":" + password
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(decoded))
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotUsername, gotPassword, ok := request.BasicAuth()
		if !ok {
			http.Error(writer, "missing Basic authentication", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"reason": gotPassword,
			"hookSpecificOutput": map[string]any{
				"hook_event_name":    "PreToolUse",
				"permissionDecision": "ask",
				"additionalContext":  gotUsername,
			},
		})
	}))
	defer server.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "basic-reflection", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Headers: map[string]string{"Authorization": "Basic " + encoded},
		Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	freezeHookRunner(t, runner, snapshot)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projected), decoded) || strings.Contains(string(projected), encoded) ||
		strings.Contains(string(projected), username) || strings.Contains(string(projected), password) {
		t.Fatalf("decoded Basic credential escaped hook response sanitization: %s", projected)
	}
	if aggregate.Decision != HookDecisionAsk || !strings.Contains(aggregate.Reason, "[REDACTED]") ||
		len(aggregate.Contexts) != 1 || !strings.Contains(aggregate.Contexts[0], "[REDACTED]") {
		t.Fatalf("sanitized Basic response lost semantic fields: %#v", aggregate)
	}
}

func TestHTTPHookCanonicalBasicAliasesAndOpaqueCustomBasicValue(t *testing.T) {
	const (
		username = "raw-user"
		password = "password"
		decoded  = username + ":" + password
	)
	padded := base64.StdEncoding.EncodeToString([]byte(decoded))
	raw := base64.RawStdEncoding.EncodeToString([]byte(decoded))
	if padded == raw {
		t.Fatal("test Basic credential unexpectedly has no padding")
	}
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Opaque") != "Basic label" {
			http.Error(writer, "opaque custom header changed", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"reason": padded,
			"hookSpecificOutput": map[string]any{
				"hook_event_name":          "PreToolUse",
				"permissionDecision":       "ask",
				"permissionDecisionReason": raw,
				"additionalContext":        "Basic label",
			},
		})
	}))
	defer server.Close()
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "canonical-basic", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Headers: map[string]string{
			"Authorization": "Basic " + raw,
			"X-Opaque":      "Basic label",
		},
		Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	for _, literal := range []string{padded, raw, decoded, username, password, "Basic label"} {
		if !credentials.Covers(redact.New(literal)) {
			t.Fatalf("canonical Basic credential set omitted %q", literal)
		}
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{padded, raw, decoded, username, password, "Basic label"} {
		if strings.Contains(string(projected), literal) {
			t.Fatalf("canonical Basic/custom opaque alias %q escaped: %s", literal, projected)
		}
	}
	if aggregate.Decision != HookDecisionAsk || !strings.Contains(aggregate.Reason, "[REDACTED]") ||
		len(aggregate.Contexts) != 1 || !strings.Contains(aggregate.Contexts[0], "[REDACTED]") {
		t.Fatalf("canonical Basic/custom opaque response lost semantics: %#v", aggregate)
	}
}

func TestHTTPHookMalformedBasicAuthorizationFailsBeforeNetwork(t *testing.T) {
	headers := []string{
		"Basic",
		"Basic %%%",
		"Basic abc def",
		"Basic " + base64.StdEncoding.EncodeToString([]byte("missing-colon")),
		"Bearer",
		"Bearer abc def",
		`Bearer "quoted"`,
		"Token opaque",
		"Basic YTr=",
	}
	for _, header := range headers {
		t.Run(strings.ReplaceAll(header, " ", "_"), func(t *testing.T) {
			requested := make(chan struct{}, 1)
			server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requested <- struct{}{}
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer server.Close()
			snapshot := NewHookManager().Reload([]HookDescriptor{{
				ID: "malformed-basic", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
				URL: server.URL, Headers: map[string]string{"Authorization": header},
				Source: SourceManaged, Timeout: time.Second,
			}})
			runner := NewHookRunner()
			credentials := freezeHookRunner(t, runner, snapshot)
			if credentials.Covers(redact.New(header)) {
				t.Fatal("unexecutable authorization contaminated the frozen credential scope")
			}
			aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
				Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
				!strings.Contains(aggregate.Results[0].Err.Error(), "authorization is malformed") {
				t.Fatalf("malformed Basic authorization result = %#v", aggregate)
			}
			select {
			case <-requested:
				t.Fatal("HTTP hook executed before malformed Basic authorization was rejected")
			default:
			}
		})
	}
}

func TestInvalidHTTPHeaderNameIsIsolatedBeforeFrozenScopeAndNetwork(t *testing.T) {
	const invalidName = "Bad Header"
	oversized := strings.Repeat("x", maximumHookCredentialLiteralBytes+1)
	invalidRequested := make(chan struct{}, 1)
	invalidServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invalidRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer invalidServer.Close()
	validRequested := make(chan struct{}, 1)
	validServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		validRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer validServer.Close()

	manager := NewHookManager()
	snapshot := manager.Reload([]HookDescriptor{
		{
			ID: "invalid-header", Event: HookNotification, Kind: HookKindHTTP,
			URL: invalidServer.URL, Headers: map[string]string{invalidName: oversized},
			Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
			URL: validServer.URL, Headers: map[string]string{"X-Valid": "safe-value"},
			Source: SourceManaged, Timeout: time.Second,
		},
	})
	if len(snapshot.Hooks) != 1 || snapshot.Hooks[0].ID != "valid-sibling" ||
		len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Message != "HTTP hook header name is invalid" ||
		strings.Contains(snapshot.Diagnostics[0].Message, invalidName) ||
		strings.Contains(snapshot.Diagnostics[0].Message, oversized) {
		t.Fatalf("invalid header isolation = %#v", snapshot)
	}
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	if credentials.Covers(redact.New(oversized)) {
		t.Fatal("invalid header value contaminated the frozen credential scope")
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 1 || aggregate.Results[0].Err != nil {
		t.Fatalf("valid sibling dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-validRequested:
	default:
		t.Fatal("valid sibling did not execute")
	}
	select {
	case <-invalidRequested:
		t.Fatal("invalid header descriptor reached the network")
	default:
	}

	// Defend the exported runner boundary even if a caller mutates a validated
	// snapshot after reload.
	mutated := manager.Reload([]HookDescriptor{{
		ID: "mutated-header", Event: HookNotification, Kind: HookKindHTTP,
		URL: invalidServer.URL, Source: SourceManaged, Timeout: time.Second,
	}})
	mutated.Hooks[0].Headers = map[string]string{invalidName: oversized}
	mutatedRunner := NewHookRunner()
	mutatedCredentials := freezeHookRunner(t, mutatedRunner, mutated)
	if mutatedCredentials.Covers(redact.New(oversized)) {
		t.Fatal("mutated invalid header value contaminated the frozen credential scope")
	}
	aggregate, err = mutatedRunner.Dispatch(t.Context(), mutated, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
		aggregate.Results[0].Err.Error() != "HTTP hook header name is invalid" {
		t.Fatalf("mutated invalid header dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-invalidRequested:
		t.Fatal("mutated invalid header descriptor reached the network")
	default:
	}
}

func TestHTTPHeaderNameValidationMatchesTokenGrammar(t *testing.T) {
	for _, name := range []string{"X-Test", "0", "!#$%&'*+-.^_`|~"} {
		if !validHTTPHeaderName(name) {
			t.Fatalf("valid HTTP token name %q was rejected", name)
		}
	}
	for _, name := range []string{"", "Bad Header", "Bad:Header", "Bad\nHeader", "Bäd"} {
		if validHTTPHeaderName(name) {
			t.Fatalf("invalid HTTP token name %q was accepted", name)
		}
	}
}

func TestInvalidHTTPHeaderValueIsIsolatedBeforeFrozenScopeAndNetwork(t *testing.T) {
	const invalidValue = "before\x01after"
	invalidRequested := make(chan struct{}, 1)
	invalidServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invalidRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer invalidServer.Close()
	validRequested := make(chan struct{}, 2)
	validServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		validRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer validServer.Close()

	manager := NewHookManager()
	snapshot := manager.Reload([]HookDescriptor{
		{
			ID: "invalid-value", Event: HookNotification, Kind: HookKindHTTP,
			URL: invalidServer.URL, Headers: map[string]string{"X-Invalid": invalidValue},
			Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
			URL: validServer.URL, Source: SourceManaged, Timeout: time.Second,
		},
	})
	if len(snapshot.Hooks) != 1 || snapshot.Hooks[0].ID != "valid-sibling" ||
		len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Message != "HTTP hook header value is invalid" ||
		strings.Contains(snapshot.Diagnostics[0].Message, invalidValue) {
		t.Fatalf("invalid header value isolation = %#v", snapshot)
	}
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	if credentials.Covers(redact.New(invalidValue)) {
		t.Fatal("invalid header value contaminated the frozen credential scope")
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 1 || aggregate.Results[0].Err != nil {
		t.Fatalf("valid sibling dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-validRequested:
	default:
		t.Fatal("valid sibling did not execute")
	}
	select {
	case <-invalidRequested:
		t.Fatal("invalid header value descriptor reached the network")
	default:
	}

	mutated := manager.Reload([]HookDescriptor{
		{
			ID: "mutated-value", Event: HookNotification, Kind: HookKindHTTP,
			URL: invalidServer.URL, Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
			URL: validServer.URL, Source: SourceManaged, Timeout: time.Second,
		},
	})
	mutated.Hooks[0].Headers = map[string]string{"X-Invalid": invalidValue}
	mutatedRunner := NewHookRunner()
	mutatedCredentials := freezeHookRunner(t, mutatedRunner, mutated)
	if mutatedCredentials.Covers(redact.New(invalidValue)) {
		t.Fatal("mutated invalid header value contaminated the frozen credential scope")
	}
	aggregate, err = mutatedRunner.Dispatch(t.Context(), mutated, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 2 ||
		aggregate.Results[0].Err == nil ||
		aggregate.Results[0].Err.Error() != "HTTP hook header value is invalid" ||
		aggregate.Results[1].Err != nil {
		t.Fatalf("mutated invalid header value dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-validRequested:
	default:
		t.Fatal("valid sibling did not execute beside a mutated invalid header value")
	}
	select {
	case <-invalidRequested:
		t.Fatal("mutated invalid header value reached the network")
	default:
	}
}

func TestMutatedSensitivePathIndexIsIsolatedBeforeFrozenScopeAndNetwork(t *testing.T) {
	invalidRequested := make(chan struct{}, 1)
	invalidServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invalidRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer invalidServer.Close()
	validRequested := make(chan struct{}, 1)
	validServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		validRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer validServer.Close()

	snapshot := NewHookManager().Reload([]HookDescriptor{
		{
			ID: "mutated-path", Event: HookNotification, Kind: HookKindHTTP,
			URL:                   invalidServer.URL + "/api/v1/hooks/path%2Ftoken",
			SensitivePathSegments: []int{3},
			Source:                SourceManaged, Timeout: time.Second,
		},
		{
			ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
			URL: validServer.URL, Source: SourceManaged, Timeout: time.Second,
		},
	})
	snapshot.Hooks[0].SensitivePathSegments = []int{4}
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	for _, literal := range []string{"path%2Ftoken", "path/token"} {
		if credentials.Covers(redact.New(literal)) {
			t.Fatalf("invalid sensitive path selection promoted %q", literal)
		}
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 2 ||
		aggregate.Results[0].Err == nil ||
		aggregate.Results[0].Err.Error() != "HTTP hook sensitive path configuration is invalid" ||
		aggregate.Results[1].Err != nil {
		t.Fatalf("mutated sensitive path dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-validRequested:
	default:
		t.Fatal("valid sibling did not execute beside an invalid sensitive path selection")
	}
	select {
	case <-invalidRequested:
		t.Fatal("invalid sensitive path selection reached the network")
	default:
	}
}

func TestHTTPHookShapeValidationPrecedesWorkloadDeterministically(t *testing.T) {
	oversized := strings.Repeat("x", maximumHookCredentialLiteralBytes+1)
	for iteration := 0; iteration < 100; iteration++ {
		headers := make(map[string]string, 2)
		if iteration%2 == 0 {
			headers["A-Oversized"] = oversized
			headers["Authorization"] = "Basic %%%"
		} else {
			headers["Authorization"] = "Basic %%%"
			headers["A-Oversized"] = oversized
		}
		credentials, err := httpHookResponseSanitizer(redact.New(), nil, headers, nil)
		if credentials != nil || !errors.Is(err, errHTTPHookAuthorizationMalformed) {
			t.Fatalf("iteration %d mixed shape/workload result = %v, %v", iteration, credentials, err)
		}
		snapshot := NewHookManager().Reload([]HookDescriptor{{
			ID: "mixed-shape-workload", Event: HookNotification, Kind: HookKindHTTP,
			URL: "https://hooks.example.invalid/notify", Headers: headers,
			Source: SourceManaged, Timeout: time.Second,
		}})
		if _, err := NewHookRunner().FreezeCredentialSanitizer(snapshot, redact.New()); err != nil {
			t.Fatalf("iteration %d malformed shape lost isolation at freeze: %v", iteration, err)
		}
	}
}

func TestHTTPHeaderValueValidationPreservesRequiredStripping(t *testing.T) {
	runner := NewHookRunner()
	runner.Environment = map[string]string{"VALUE": "a\r\n\x00b"}
	runner.HTTPEnvAllow = map[string]bool{"VALUE": true}
	value, err := runner.expandHTTPHeader("prefix-${VALUE}-suffix\r\n\x00", []string{"VALUE"})
	if err != nil || value != "prefix-ab-suffix" {
		t.Fatalf("stripped HTTP header value = %q, %v", value, err)
	}
	for _, value := range []string{"before\x01after", "before\x7fafter"} {
		if validHTTPHeaderValue(value) {
			t.Fatalf("invalid HTTP field value %q was accepted", value)
		}
	}
	if !validHTTPHeaderValue("before\tafter") {
		t.Fatal("horizontal tab was rejected from an HTTP field value")
	}
}

func TestHTTPHeaderExpansionIsSinglePassBoundedAndNonblocking(t *testing.T) {
	runner := NewHookRunner()
	runner.Environment = map[string]string{
		"SELF":  "${SELF}",
		"LEFT":  "${RIGHT}",
		"RIGHT": "${LEFT}",
		"CHUNK": strings.Repeat("x", 1024),
	}
	runner.HTTPEnvAllow = map[string]bool{
		"SELF": true, "LEFT": true, "RIGHT": true, "CHUNK": true,
	}
	for _, test := range []struct {
		name     string
		template string
		allow    []string
	}{
		{name: "self cycle", template: "${SELF}", allow: []string{"SELF"}},
		{name: "mutual cycle", template: "${LEFT}", allow: []string{"LEFT", "RIGHT"}},
		{name: "unterminated", template: "prefix-${SELF", allow: []string{"SELF"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runner.expandHTTPHeader(test.template, test.allow); !errors.Is(err, errHTTPHookHeaderExpansionInvalid) {
				t.Fatalf("invalid expansion error = %v", err)
			}
		})
	}
	amplified := strings.Repeat("${CHUNK}", maximumHookCredentialLiteralBytes/1024+1)
	if _, err := runner.expandHTTPHeader(amplified, []string{"CHUNK"}); !errors.Is(err, errHTTPHookCredentialWorkload) {
		t.Fatalf("amplified expansion error = %v", err)
	}

	invalidRequested := make(chan struct{}, 1)
	invalidServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		invalidRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer invalidServer.Close()
	validRequested := make(chan struct{}, 1)
	validServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		validRequested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer validServer.Close()
	snapshot := NewHookManager().Reload([]HookDescriptor{
		{
			ID: "cyclic-expansion", Event: HookNotification, Kind: HookKindHTTP,
			URL: invalidServer.URL, Headers: map[string]string{"X-Value": "${SELF}"},
			AllowedEnvVars: []string{"SELF"}, Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
			URL: validServer.URL, Source: SourceManaged, Timeout: time.Second,
		},
	})
	credentials := freezeHookRunner(t, runner, snapshot)
	if credentials.Covers(redact.New("${SELF}")) {
		t.Fatal("cyclic expansion contaminated the frozen credential scope")
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil || len(aggregate.Results) != 2 ||
		aggregate.Results[0].Err == nil ||
		aggregate.Results[0].Err.Error() != "HTTP hook header expansion is invalid" ||
		aggregate.Results[1].Err != nil {
		t.Fatalf("cyclic expansion dispatch = %#v, %v", aggregate, err)
	}
	select {
	case <-validRequested:
	default:
		t.Fatal("valid sibling did not execute beside cyclic expansion")
	}
	select {
	case <-invalidRequested:
		t.Fatal("cyclic header expansion reached the network")
	default:
	}

	overflowSnapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "amplified-expansion", Event: HookNotification, Kind: HookKindHTTP,
		URL: invalidServer.URL, Headers: map[string]string{"X-Value": amplified},
		AllowedEnvVars: []string{"CHUNK"}, Source: SourceManaged, Timeout: time.Second,
	}})
	if _, err := runner.FreezeCredentialSanitizer(overflowSnapshot, redact.New()); err == nil ||
		!strings.Contains(err.Error(), "could not be composed safely") {
		t.Fatalf("amplified frozen expansion error = %v", err)
	}
	select {
	case <-invalidRequested:
		t.Fatal("amplified header expansion reached the network")
	default:
	}
}

func TestInvalidHTTPQueryScopeIsIsolatedBeforeNetwork(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "semicolon", query: "token=credential;next=x"},
		{name: "malformed percent", query: "token=%zz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidRequested := make(chan struct{}, 1)
			invalidServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				invalidRequested <- struct{}{}
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer invalidServer.Close()
			validRequested := make(chan struct{}, 1)
			validServer := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				validRequested <- struct{}{}
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer validServer.Close()
			snapshot := NewHookManager().Reload([]HookDescriptor{
				{
					ID: "invalid-query", Event: HookNotification, Kind: HookKindHTTP,
					URL:    invalidServer.URL + "?" + test.query,
					Source: SourceManaged, Timeout: time.Second,
				},
				{
					ID: "valid-sibling", Event: HookNotification, Kind: HookKindHTTP,
					URL: validServer.URL, Source: SourceManaged, Timeout: time.Second,
				},
			})
			runner := NewHookRunner()
			freezeHookRunner(t, runner, snapshot)
			aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
				Fields: map[string]any{"notification_type": "idle"},
			})
			if err != nil || len(aggregate.Results) != 2 ||
				aggregate.Results[0].Err == nil ||
				aggregate.Results[0].Err.Error() != "HTTP hook query configuration is invalid" ||
				aggregate.Results[1].Err != nil {
				t.Fatalf("invalid query dispatch = %#v, %v", aggregate, err)
			}
			select {
			case <-validRequested:
			default:
				t.Fatal("valid sibling did not execute beside an invalid query")
			}
			select {
			case <-invalidRequested:
				t.Fatal("invalid query reached the network")
			default:
			}
		})
	}
}

func TestHTTPHeaderWireTrimAliasIsSanitized(t *testing.T) {
	const (
		rawValue  = " \tpadded-opaque-secret\t "
		wireValue = "padded-opaque-secret"
	)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hook_event_name":          "PreToolUse",
				"permissionDecision":       "ask",
				"permissionDecisionReason": request.Header.Get("X-Opaque"),
			},
		})
	}))
	defer server.Close()
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "trim-alias", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Headers: map[string]string{"X-Opaque": rawValue},
		Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	credentials := freezeHookRunner(t, runner, snapshot)
	for _, literal := range []string{rawValue, wireValue} {
		if !credentials.Covers(redact.New(literal)) {
			t.Fatalf("wire-trim credential set omitted %q", literal)
		}
	}
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil || aggregate.Decision != HookDecisionAsk ||
		!strings.Contains(aggregate.Reason, "[REDACTED]") ||
		strings.Contains(aggregate.Reason, wireValue) {
		t.Fatalf("wire-trim reflection = %#v, %v", aggregate, err)
	}
	whitespace, err := httpHookResponseSanitizer(redact.New(), nil, map[string]string{"X-Opaque": " \t "}, nil)
	if err != nil || !whitespace.Empty() {
		t.Fatalf("all-whitespace header poisoned credential scope: %v, %v", whitespace, err)
	}
}

func TestHTTPHookSanitizesNoncanonicalRawQueryReflection(t *testing.T) {
	const rawSecret = "a%2fb"
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"reason": strings.TrimPrefix(request.URL.RawQuery, "token=")})
	}))
	defer server.Close()
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "raw-query", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL + "/hook?token=" + rawSecret, Source: SourceManaged, Timeout: time.Second,
	}})
	runner := NewHookRunner()
	freezeHookRunner(t, runner, snapshot)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reason := aggregate.Results[0].Reason; reason == "" || strings.Contains(reason, rawSecret) {
		t.Fatalf("raw encoded query reflection was not sanitized: %#v", aggregate.Results[0])
	}
}

func TestHTTPHookPayloadUsesConfiguredCredentialSet(t *testing.T) {
	const secret = "semantic-http-payload-credential"
	observed := make(chan bool, 1)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		observed <- strings.Contains(string(encoded), secret)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	runner := NewHookRunner()
	runner.SetCredentialLiterals(secret)
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "payload-set", Event: HookNotification, Kind: HookKindHTTP,
		URL: server.URL, Source: SourceManaged, Timeout: time.Second,
	}})
	_, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"message": "before " + secret + " after", "notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if <-observed {
		t.Fatal("HTTP hook request body received configured credential")
	}
}

func TestHTTPHookLegacySanitizerStillAppliesWithoutResponseScopedValues(t *testing.T) {
	const secret = "legacy-host-credential"
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"reason": "before " + secret + " after"})
	}))
	defer server.Close()

	runner := NewHookRunner()
	runner.Sanitize = func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") }
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "legacy-response", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Source: SourceManaged, Timeout: time.Second,
	}})
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(aggregate.Results[0].Reason, secret) || !strings.Contains(aggregate.Results[0].Reason, "[REDACTED]") {
		t.Fatalf("legacy response sanitizer = %#v", aggregate.Results[0])
	}
}

func TestHTTPHookEveryShortCustomHeaderReflectionIsSanitized(t *testing.T) {
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"reason": request.Header.Get("X-Opaque")})
	}))
	defer server.Close()

	for length := 1; length <= 7; length++ {
		t.Run(fmt.Sprintf("%d_bytes", length), func(t *testing.T) {
			secret := strings.Repeat("q", length)
			snapshot := NewHookManager().Reload([]HookDescriptor{{
				ID: "short-header", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
				URL: server.URL, Headers: map[string]string{"X-Opaque": secret},
				Source: SourceManaged, Timeout: time.Second,
			}})
			runner := NewHookRunner()
			freezeHookRunner(t, runner, snapshot)
			aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
				Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
			})
			if err != nil {
				t.Fatal(err)
			}
			reason := aggregate.Results[0].Reason
			if reason == "" || strings.Contains(reason, secret) {
				t.Fatalf("%d-byte header reflection = %#v", length, aggregate.Results[0])
			}
		})
	}
}

func TestHTTPHookSemanticJSONAliasesAndSetUnion(t *testing.T) {
	tests := []struct {
		name       string
		hostSecret string
		header     string
		wire       string
	}{
		{name: "solidus escape", header: "a/b", wire: `{"reason":"a\/b"}`},
		{name: "unicode escape", header: "secret", wire: `{"reason":"\u0073ecret"}`},
		{name: "marker cycle union", hostSecret: "R", header: "*", wire: `{"reason":"R*"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.wire))
			}))
			defer server.Close()
			runner := NewHookRunner()
			if test.hostSecret != "" {
				runner.SetCredentialLiterals(test.hostSecret)
			}
			snapshot := NewHookManager().Reload([]HookDescriptor{{
				ID: "semantic-alias", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
				URL: server.URL, Headers: map[string]string{"X-Opaque": test.header},
				Source: SourceManaged, Timeout: time.Second,
			}})
			freezeHookRunner(t, runner, snapshot)
			aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
				Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
			})
			if err != nil {
				t.Fatal(err)
			}
			reason := aggregate.Results[0].Reason
			for _, secret := range []string{test.hostSecret, test.header} {
				if secret != "" && strings.Contains(reason, secret) {
					t.Fatalf("semantic alias retained %q in %#v", secret, aggregate.Results[0])
				}
			}
		})
	}
}

func TestHTTPHookReflectedJSONScalarsFailClosed(t *testing.T) {
	for _, test := range []struct {
		secret             string
		wire               string
		wantAggregateError bool
	}{
		{secret: "1", wire: `{"reason":1}`},
		{secret: "true", wire: `{"continue":true}`, wantAggregateError: true},
		{secret: "false", wire: `{"continue":false}`},
		{secret: "null", wire: `{"reason":null}`},
	} {
		t.Run(test.secret, func(t *testing.T) {
			server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.wire))
			}))
			defer server.Close()
			snapshot := NewHookManager().Reload([]HookDescriptor{{
				ID: "scalar", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
				URL: server.URL, Headers: map[string]string{"X-Opaque": test.secret},
				Source: SourceManaged, Timeout: time.Second,
			}})
			runner := NewHookRunner()
			freezeHookRunner(t, runner, snapshot)
			aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
				SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
				Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
			})
			if test.wantAggregateError {
				if err == nil || strings.Contains(err.Error(), test.secret) ||
					len(aggregate.Results) != 0 || aggregate.Continue ||
					aggregate.Decision != HookDecisionNone {
					t.Fatalf("fixed aggregate scalar reflection = %#v, %v", aggregate, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result := aggregate.Results[0]
			if result.Err == nil || result.Decision != HookDecisionNone || result.Context != "" || result.UpdatedInput != nil {
				t.Fatalf("scalar reflection gained authority: %#v", result)
			}
		})
	}
}

func TestHookCapturedOutputSanitizesBeforeEveryCap(t *testing.T) {
	const secret = "cap-boundary-credential"
	raw := "before " + secret + " after"
	runner := NewHookRunner()
	runner.SetCredentialLiterals(secret)
	for limit := 0; limit <= len(raw); limit++ {
		captureLimit := limit + runner.redactionLookahead()
		if captureLimit > len(raw) {
			captureLimit = len(raw)
		}
		captured := raw[:captureLimit]
		safe, _ := runner.sanitizeCapturedOutput(captured, captureLimit < len(raw), limit)
		if strings.Contains(safe, secret) {
			t.Fatalf("limit %d exposed credential in %q", limit, safe)
		}
	}
}

func TestHTTPHookSemanticRedactionCannotAmplifyPastOutputLimit(t *testing.T) {
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"reason":"qqqq"}`))
	}))
	defer server.Close()
	runner := NewHookRunner()
	runner.OutputLimit = 20
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "amplification", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Headers: map[string]string{"X-Opaque": "q"},
		Source: SourceManaged, Timeout: time.Second,
	}})
	freezeHookRunner(t, runner, snapshot)
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := aggregate.Results[0]
	if result.Err == nil || !result.Truncated || result.Decision != HookDecisionNone || result.Context != "" {
		t.Fatalf("amplified response was not dropped: %#v", result)
	}
}

func TestHookCredentialRedactionWorkloadFailsClosedBeforeExecution(t *testing.T) {
	literals := make([]string, maximumHookCredentialLiterals+1)
	for index := range literals {
		literals[index] = fmt.Sprintf("credential-%03d", index)
	}
	runner := NewHookRunner()
	runner.SetCredentialLiterals(literals...)
	_, err := runner.Dispatch(t.Context(), HookSnapshot{}, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"message": "safe", "notification_type": "idle"},
	})
	if err == nil || !strings.Contains(err.Error(), "redaction workload") {
		t.Fatalf("oversized host credential set error = %v", err)
	}
	for _, literal := range literals {
		if strings.Contains(err.Error(), literal) {
			t.Fatal("credential workload diagnostic exposed a configured literal")
		}
	}
}

func TestHTTPHookResponseCredentialWorkloadFailsBeforeNetwork(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	oversized := strings.Repeat("x", maximumHookCredentialLiteralBytes+1)
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "oversized-response-scope", Event: HookPreToolUse, Matcher: "Read", Kind: HookKindHTTP,
		URL: server.URL, Headers: map[string]string{"X-Opaque": oversized},
		Source: SourceManaged, Timeout: time.Second,
	}})
	aggregate, err := NewHookRunner().Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{}, "tool_use_id": "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
		!strings.Contains(aggregate.Results[0].Err.Error(), "redaction workload") ||
		strings.Contains(aggregate.Results[0].Err.Error(), oversized) {
		t.Fatalf("oversized response credential scope = %#v", aggregate)
	}
	select {
	case <-requested:
		t.Fatal("HTTP hook executed before its response credential scope was bounded")
	default:
	}
}

func TestHTTPHookHostAndResponseCredentialUnionIsBoundedBeforeNetwork(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := newIPv4HookServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	host := make([]string, maximumHookCredentialLiterals)
	for index := range host {
		host[index] = fmt.Sprintf("host-credential-%03d", index)
	}
	runner := NewHookRunner()
	runner.SetCredentialLiterals(host...)
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "union-workload", Event: HookNotification, Kind: HookKindHTTP,
		URL:    server.URL + "?token=response-credential",
		Source: SourceManaged, Timeout: time.Second,
	}})
	aggregate, err := runner.Dispatch(t.Context(), snapshot, HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Results) != 1 || aggregate.Results[0].Err == nil ||
		!strings.Contains(aggregate.Results[0].Err.Error(), "redaction workload") {
		t.Fatalf("host/response workload result = %#v", aggregate)
	}
	select {
	case <-requested:
		t.Fatal("oversized host/response credential union reached the network")
	default:
	}
}

func newIPv4HookServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in sandbox: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func freezeHookRunner(t *testing.T, runner *HookRunner, snapshot HookSnapshot) *redact.Set {
	t.Helper()
	credentials, err := runner.FreezeCredentialSanitizer(snapshot, runner.credentialSet)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func TestHookMalformedDescriptorAndEventMismatchFailClosed(t *testing.T) {
	manager := NewHookManager()
	snapshot := manager.Reload([]HookDescriptor{
		{ID: "bad-regex", Event: HookPreToolUse, Matcher: "[", Kind: HookKindCommand, Command: "true", Source: SourceUser},
		{ID: "wrong-event", Event: HookPreToolUse, Kind: HookKindCommand, Shell: "sh", Command: `printf '%s' '{"hookSpecificOutput":{"hook_event_name":"PostToolUse","permissionDecision":"allow"}}'`, Source: SourceUser, Timeout: time.Second},
	})
	if len(snapshot.Hooks) != 1 || len(snapshot.Diagnostics) != 1 {
		t.Fatalf("malformed descriptor isolation failed: %#v", snapshot)
	}
	aggregate, err := NewHookRunner().Dispatch(context.Background(), snapshot, HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse, Fields: map[string]any{"tool_name": "Read"}})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Results[0].Err == nil || aggregate.Decision != HookDecisionNone {
		t.Fatalf("event mismatch gained authority: %#v", aggregate)
	}
}

func TestHookSnapshotSerializationRedactsExecutableAndSecrets(t *testing.T) {
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "secret", Event: HookPreToolUse, Kind: HookKindHTTP,
		URL:                   "https://example.test/api/v1/hooks/path%2Fnever-serialize?token=never-serialize",
		Headers:               map[string]string{"Authorization": "Bearer never-serialize"},
		SensitivePathSegments: []int{3},
		Source:                SourceManaged, Timeout: time.Second,
	}})
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-serialize") ||
		!strings.Contains(string(encoded), "Authorization") ||
		!strings.Contains(string(encoded), `"sensitive_path_segments":[3]`) {
		t.Fatalf("hook snapshot secret projection = %s", encoded)
	}
}

func TestSensitivePathSegmentsValidateCanonicalizeAndRemainOutsideDedupIdentity(t *testing.T) {
	const target = "https://example.test/api/v1/hooks/path%2Ftoken"
	snapshot := NewHookManager().Reload([]HookDescriptor{
		{
			ID: "first", Event: HookPreToolUse, Kind: HookKindHTTP, URL: target,
			Source: SourceManaged, Timeout: time.Second,
		},
		{
			ID: "last", Event: HookPreToolUse, Kind: HookKindHTTP, URL: target,
			Headers:               map[string]string{"X-Safe": "same"},
			SensitivePathSegments: []int{3, 3},
			Source:                SourceManaged, Timeout: time.Second,
		},
	})
	if len(snapshot.Hooks) != 2 {
		t.Fatalf("descriptors with different headers should remain distinct: %#v", snapshot.Hooks)
	}

	// Segment sensitivity changes safety projection, not the configured hook's
	// execution identity. The later otherwise-identical descriptor wins.
	snapshot = NewHookManager().Reload([]HookDescriptor{
		{
			ID: "first", Event: HookPreToolUse, Kind: HookKindHTTP, URL: target,
			SensitivePathSegments: []int{1},
			Source:                SourceManaged, Timeout: time.Second,
		},
		{
			ID: "last", Event: HookPreToolUse, Kind: HookKindHTTP, URL: target,
			SensitivePathSegments: []int{3, 1, 3},
			Source:                SourceManaged, Timeout: time.Second,
		},
	})
	if len(snapshot.Diagnostics) != 0 || len(snapshot.Hooks) != 1 ||
		snapshot.Hooks[0].ID != "last" ||
		len(snapshot.Hooks[0].SensitivePathSegments) != 2 ||
		snapshot.Hooks[0].SensitivePathSegments[0] != 1 ||
		snapshot.Hooks[0].SensitivePathSegments[1] != 3 {
		t.Fatalf("sensitive path canonicalization/last-wins = %#v", snapshot)
	}
}

func TestSensitivePathSegmentValidationRejectsInvalidIndices(t *testing.T) {
	tests := []struct {
		name       string
		descriptor HookDescriptor
	}{
		{
			name: "out of range",
			descriptor: HookDescriptor{
				ID: "out-of-range", Event: HookPreToolUse, Kind: HookKindHTTP,
				URL:                   "https://example.test/api/v1/hooks/path%2Ftoken",
				SensitivePathSegments: []int{4}, Source: SourceManaged,
			},
		},
		{
			name: "negative",
			descriptor: HookDescriptor{
				ID: "negative", Event: HookPreToolUse, Kind: HookKindHTTP,
				URL:                   "https://example.test/api/v1/hooks/path%2Ftoken",
				SensitivePathSegments: []int{-1}, Source: SourceManaged,
			},
		},
		{
			name: "root path",
			descriptor: HookDescriptor{
				ID: "root", Event: HookPreToolUse, Kind: HookKindHTTP,
				URL: "https://example.test/", SensitivePathSegments: []int{0},
				Source: SourceManaged,
			},
		},
		{
			name: "command hook",
			descriptor: HookDescriptor{
				ID: "command", Event: HookPreToolUse, Kind: HookKindCommand,
				Command: "true", SensitivePathSegments: []int{0}, Source: SourceManaged,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := NewHookManager().Reload([]HookDescriptor{test.descriptor})
			if len(snapshot.Hooks) != 0 || len(snapshot.Diagnostics) != 1 ||
				!strings.Contains(snapshot.Diagnostics[0].Message, "sensitive_path_segments") {
				t.Fatalf("invalid sensitive path descriptor = %#v", snapshot)
			}
		})
	}
}

func TestHookRuntimeProfileDiagnosesUnreachableEvents(t *testing.T) {
	manager := NewHookManagerForEvents(HookPreToolUse, HookPermissionRequest)
	snapshot := manager.Reload([]HookDescriptor{
		{ID: "reachable", Event: HookPreToolUse, Kind: HookKindCommand, Command: "true", Source: SourceUser},
		{ID: "unreachable", Event: HookStop, Kind: HookKindCommand, Command: "true", Source: SourceUser},
	})
	if len(snapshot.Hooks) != 1 || snapshot.Hooks[0].ID != "reachable" {
		t.Fatalf("profile published unreachable hooks: %#v", snapshot.Hooks)
	}
	if !snapshot.SupportsEvent(HookPreToolUse) || !snapshot.SupportsEvent(HookPermissionRequest) || snapshot.SupportsEvent(HookStop) {
		t.Fatalf("reachable event profile = %#v", snapshot.ReachableEvents)
	}
	if !diagnosticsContain(snapshot.Diagnostics, "hook event Stop is unavailable in the active runtime profile") {
		t.Fatalf("missing unreachable-event diagnostic: %#v", snapshot.Diagnostics)
	}
}

func TestHookInvalidConditionIsDiagnosed(t *testing.T) {
	snapshot := NewHookManager().Reload([]HookDescriptor{
		{ID: "invalid-condition", Event: HookPreToolUse, If: "Bash(git status", Kind: HookKindCommand, Command: "true", Source: SourceUser},
		{ID: "inapplicable-condition", Event: HookSessionStart, If: "Read", Kind: HookKindCommand, Command: "true", Source: SourceUser},
	})
	if len(snapshot.Hooks) != 0 || !diagnosticsContain(snapshot.Diagnostics, "invalid hook condition") || !diagnosticsContain(snapshot.Diagnostics, "hook conditions are unavailable for event SessionStart") {
		t.Fatalf("invalid condition was not isolated: %#v", snapshot)
	}
}

func TestHookRunnerContainsCallbackPanicsAndReleasesOnceClaims(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command-hook retry assertion is Unix-specific")
	}
	input := HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookPreToolUse,
		Fields: map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "safe.txt"}, "tool_use_id": "u"},
	}
	conditionSnapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "condition-once", Event: HookPreToolUse, If: "Read", Once: true,
		Kind: HookKindCommand, Shell: "sh", Command: "printf '{}'", Source: SourceUser,
	}})
	runner := NewHookRunner()
	runner.ConditionMatcher = func(string, HookInput) bool {
		panic("condition matcher panic payload")
	}
	if _, err := runner.Dispatch(t.Context(), conditionSnapshot, input); err == nil || err.Error() != "hook condition matcher panicked" {
		t.Fatalf("condition matcher panic = %v", err)
	}
	runner.ConditionMatcher = func(string, HookInput) bool { return true }
	retried, err := runner.Dispatch(t.Context(), conditionSnapshot, input)
	if err != nil || len(retried.Results) != 1 || retried.Results[0].Err != nil {
		t.Fatalf("condition matcher panic stranded once claim: aggregate=%#v err=%v", retried, err)
	}
	consumed, err := runner.Dispatch(t.Context(), conditionSnapshot, input)
	if err != nil || len(consumed.Results) != 0 {
		t.Fatalf("successful once retry was not consumed: aggregate=%#v err=%v", consumed, err)
	}

	workerSnapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "worker-once", Event: HookNotification, Once: true,
		Kind: HookKindHTTP, URL: "http://worker-panic.example.invalid/hook", Source: SourceUser,
	}})
	var formatterCalls int
	payload := &panickingHookPanicPayload{calls: &formatterCalls, secret: "worker-panic-secret"}
	worker := NewHookRunner()
	worker.resolver = panickingHookResolver{payload: payload}
	notification := HookInput{
		SessionID: "s", CWD: t.TempDir(), Event: HookNotification,
		Fields: map[string]any{"notification_type": "idle"},
	}
	first, err := worker.Dispatch(t.Context(), workerSnapshot, notification)
	if err != nil || len(first.Results) != 1 || first.Results[0].Err == nil {
		t.Fatalf("worker panic result = %#v err=%v", first, err)
	}
	if message := first.Results[0].Err.Error(); message != "HTTP hook request failed" {
		t.Fatalf("worker panic diagnostic = %q", message)
	}
	if formatterCalls != 0 {
		t.Fatalf("worker panic payload formatter calls = %d", formatterCalls)
	}
	second, err := worker.Dispatch(t.Context(), workerSnapshot, notification)
	if err != nil || len(second.Results) != 1 || second.Results[0].Err == nil {
		t.Fatalf("worker panic stranded once claim: aggregate=%#v err=%v", second, err)
	}

	var errorCalls int
	sanitized := worker.sanitizeResultWith(
		HookResult{Err: panickingHookError{calls: &errorCalls, secret: "hook-error-secret"}},
		func(value string) string { return value },
	)
	if sanitized.Err == nil || sanitized.Err.Error() != "hook operation failed" || errorCalls != 1 {
		t.Fatalf("panicking hook error projection = %#v calls=%d", sanitized.Err, errorCalls)
	}
	if _, err := worker.finalizeHookAggregate(
		HookAggregate{Continue: true, Results: []HookResult{sanitized}}, nil,
	); err != nil {
		t.Fatalf("safe hook error aggregate: %v", err)
	}
}

func TestOnceHookIsConsumedOnlyAfterSuccess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "once-marker")
	snapshot := NewHookManager().Reload([]HookDescriptor{{
		ID: "once", Event: HookNotification, Kind: HookKindCommand, Shell: "sh", Once: true,
		Command: "if [ -f '" + marker + "' ]; then printf '{}'; else touch '" + marker + "'; exit 1; fi",
		Source:  SourceProject,
	}})
	runner := NewHookRunner()
	input := HookInput{SessionID: "s", CWD: t.TempDir(), Event: HookNotification, Fields: map[string]any{"notification_type": "idle"}}
	first, err := runner.Dispatch(t.Context(), snapshot, input)
	if err != nil || len(first.Results) != 1 || first.Results[0].Err == nil {
		t.Fatalf("failed once attempt=%+v err=%v", first, err)
	}
	second, err := runner.Dispatch(t.Context(), snapshot, input)
	if err != nil || len(second.Results) != 1 || second.Results[0].Err != nil {
		t.Fatalf("successful once retry=%+v err=%v", second, err)
	}
	third, err := runner.Dispatch(t.Context(), snapshot, input)
	if err != nil || len(third.Results) != 0 || !third.Continue {
		t.Fatalf("consumed once hook=%+v err=%v", third, err)
	}
}
