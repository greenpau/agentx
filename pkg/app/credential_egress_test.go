package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/mcp"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/task"
	"github.com/greenpau/agentx/pkg/transcript"
)

type cyclicOperationalError struct{}

func (*cyclicOperationalError) Error() string { panic("error panic payload") }
func (err *cyclicOperationalError) Unwrap() error {
	return err
}

type panickingOperationalUnwrapError struct {
	message string
}

func (err *panickingOperationalUnwrapError) Error() string { return err.message }
func (*panickingOperationalUnwrapError) Unwrap() error {
	panic("unwrap panic payload")
}

type statefulOperationalError struct {
	calls  int
	secret string
}

func (err *statefulOperationalError) Error() string {
	err.calls++
	if err.calls == 1 {
		return "safe first diagnostic"
	}
	return err.secret
}

type uncomparableOperationalError []string

func (uncomparableOperationalError) Error() string { return "uncomparable" }

type blockingOperationalUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingOperationalUnwrapError) Error() string { return "foreign operational failure" }
func (err *blockingOperationalUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.Canceled
}

func TestRedactedOperationalErrorPreservesClassificationWithoutExposingCause(t *testing.T) {
	const secret = "synthetic-provider-error-secret"
	sentinel := errors.New("classified failure")
	cause := fmt.Errorf("provider reflected %s: %w", secret, sentinel)
	wrapped := redactOperationalError(cause, func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") })
	if strings.Contains(wrapped.Error(), secret) || !errors.Is(wrapped, sentinel) {
		t.Fatalf("redacted error lost safety or classification: %v", wrapped)
	}
	if errors.Unwrap(wrapped) != nil {
		t.Fatal("redacted error exposed its secret-bearing cause through Unwrap")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formatted := fmt.Sprintf(format, wrapped)
		if strings.Contains(formatted, secret) || !strings.Contains(formatted, "[REDACTED]") {
			t.Fatalf("format %q exposed operational cause: %q", format, formatted)
		}
	}
}

func TestRedactedOperationalErrorReprojectionPreservesSealedClassification(t *testing.T) {
	const secret = "synthetic-reprojected-error-secret"
	sentinel := errors.New("classified failure")
	redactor := func(value string) string {
		return strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	first := redactOperationalError(fmt.Errorf("provider reflected %s: %w", secret, sentinel), redactor)
	second := redactOperationalError(errors.Join(first, errors.New("safe cleanup failure")), redactor)
	if !errors.Is(second, sentinel) {
		t.Fatal("reprojected operational error lost its sealed classification")
	}
	if strings.Contains(second.Error(), secret) || !strings.Contains(second.Error(), "[REDACTED]") {
		t.Fatalf("reprojected operational error exposed unsafe text: %q", second.Error())
	}
	if errors.Unwrap(second) != nil {
		t.Fatal("reprojected operational error exposed a cause through Unwrap")
	}

	usage := &cli.UsageError{Message: "invalid invocation " + secret}
	firstUsage := redactOperationalError(fmt.Errorf("outer: %w", usage), redactor)
	secondUsage := redactOperationalError(errors.Join(firstUsage, errors.New("safe cleanup failure")), redactor)
	var publicUsage *cli.UsageError
	if !cli.IsUsageError(secondUsage) || !errors.As(secondUsage, &publicUsage) {
		t.Fatal("reprojected operational error lost its sealed usage classification")
	}
	if publicUsage == usage || strings.Contains(publicUsage.Message, secret) {
		t.Fatalf("reprojected usage classification exposed its original cause: %#v", publicUsage)
	}

	repeated := first
	for index := 0; index < maximumOperationalErrorGraphNodes*4; index++ {
		repeated = redactOperationalError(errors.Join(repeated, errors.New("safe cleanup failure")), redactor)
	}
	projected, ok := repeated.(*redactedOperationalError)
	if !ok {
		t.Fatalf("reprojected classification has unexpected type %T", repeated)
	}
	if len(projected.classes) > maximumOperationalErrorGraphNodes {
		t.Fatalf("reprojected classification snapshot has %d classes", len(projected.classes))
	}
	if !errors.Is(repeated, sentinel) {
		t.Fatal("bounded repeated reprojection discarded the original policy sentinel")
	}
}

func TestRedactedOperationalErrorContainsHostileCauseAndSanitizerBehavior(t *testing.T) {
	const secret = "operational-error-secret"

	cyclic := redactOperationalError(&cyclicOperationalError{}, strings.ToUpper)
	if cyclic.Error() != "OPERATION FAILED" {
		t.Fatalf("cyclic error projection = %q", cyclic.Error())
	}

	panickingUnwrap := redactOperationalError(
		&panickingOperationalUnwrapError{message: "failure " + secret},
		func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	)
	if panickingUnwrap.Error() != "operation failed" {
		t.Fatalf("panicking unwrap projection = %q", panickingUnwrap.Error())
	}

	stateful := &statefulOperationalError{secret: secret}
	projected := redactOperationalError(stateful, func(value string) string { return value })
	if stateful.calls != 0 || projected.Error() != "operation failed" {
		t.Fatalf("stateful error was retained or formatted repeatedly: calls=%d projection=%q", stateful.calls, projected.Error())
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
		if rendered := fmt.Sprintf(format, projected); strings.Contains(rendered, secret) {
			t.Fatalf("format %s exposed stateful cause: %q", format, rendered)
		}
	}

	suppressed := redactOperationalError(errors.New(secret), func(string) string {
		panic("sanitizer panic payload")
	})
	if suppressed.Error() != "" {
		t.Fatalf("panicking sanitizer retained output: %q", suppressed.Error())
	}
	if projected.(*redactedOperationalError).Is(uncomparableOperationalError{"target"}) {
		t.Fatal("uncomparable target received a classification")
	}

	usage := &cli.UsageError{Message: "invalid invocation " + secret}
	classified := redactOperationalError(
		fmt.Errorf("outer: %w", usage),
		func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") },
	)
	if !cli.IsUsageError(classified) || ExitCode(classified) != 2 {
		t.Fatalf("usage classification was not preserved: %T %v", classified, classified)
	}
	var publicUsage *cli.UsageError
	if !errors.As(classified, &publicUsage) || publicUsage == usage || strings.Contains(publicUsage.Message, secret) {
		t.Fatalf("usage As projection exposed raw cause: original=%p public=%p message=%q", usage, publicUsage, publicUsage.Message)
	}
}

func TestExitCodeDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingOperationalUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	done := make(chan int, 1)
	go func() { done <- ExitCode(cause) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("ExitCode() = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("ExitCode blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("ExitCode invoked foreign Unwrap")
	default:
	}
}

func TestBuildSessionProjectsWorkspaceTrustWarningWithCompleteCredentialGuard(t *testing.T) {
	const secret = "them"
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("project instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	opts := buildTestCLIOptions(t, workspace, stateDir, "ses_guarded_trust_warning")
	writeTestAuthFile(t, stateDir, "https://example.test", "gpt-5.6-sol", "gpt-5.6-sol", secret, "2026-07-01-preview")
	opts.Bare = false
	opts.NoSessionPersistence = true
	userRoot := t.TempDir()
	t.Setenv("HOME", userRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(userRoot, "config"))
	t.Setenv("APPDATA", filepath.Join(userRoot, "appdata"))

	var warnings bytes.Buffer
	session, err := buildSession(t.Context(), buildOptions{
		CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: &warnings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if rendered := warnings.String(); rendered == "" || strings.Contains(rendered, secret) ||
		!strings.Contains(rendered, redact.Mask(secret)) {
		t.Fatalf("workspace trust warning = %q", rendered)
	}
}

func TestSessionSurfacesRedactPostStartupErrorsAndSealTerminalWriters(t *testing.T) {
	const secret = "test-key"
	sentinel := errors.New("classified writer failure")
	type surfaceCase struct {
		name                   string
		preservesSafeLeafClass bool
		run                    func(context.Context, cli.Options, string, io.Reader, io.Writer, io.Writer) error
	}
	tests := []surfaceCase{
		{
			name: "headless",
			run: func(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) error {
				opts.Prompt = "/cost"
				return runHeadless(ctx, opts, workspace, stdin, stdout, stderr)
			},
		},
		{
			name: "interactive",
			run:  runInteractive,
		},
		{
			name: "structured",
			// The structured encoder snapshots exact standard-library leaf
			// identities before sealing its writer failure. Terminal text
			// deliberately exposes only its own fixed classification.
			preservesSafeLeafClass: true,
			run: func(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) error {
				opts.OutputFormat = cli.OutputStreamJSON
				opts.InputFormat = cli.InputStreamJSON
				return runStructured(ctx, opts, workspace, stdin, stdout, stderr)
			},
		},
		{
			name:                   "structured one-shot",
			preservesSafeLeafClass: true,
			run: func(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) error {
				opts.OutputFormat = cli.OutputStreamJSON
				opts.InputFormat = cli.InputText
				return runStructuredOneShot(ctx, opts, workspace, stdin, stdout, stderr)
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			stateDir := filepath.Join(t.TempDir(), "state")
			opts := buildTestCLIOptions(t, workspace, stateDir, fmt.Sprintf("ses_surface_error_%d", index))
			writer := credentialErrorWriter{
				err: fmt.Errorf("writer reflected %s: %w", secret, sentinel),
			}
			err := test.run(t.Context(), opts, workspace, strings.NewReader(""), writer, io.Discard)
			safeDiagnostic := err != nil &&
				(strings.Contains(err.Error(), redact.Mask(secret)) ||
					strings.Contains(err.Error(), "writer failed") ||
					err.Error() == "operation failed")
			if err == nil || strings.Contains(err.Error(), secret) ||
				!safeDiagnostic {
				t.Fatalf("post-startup surface error = %v", err)
			}
			if test.preservesSafeLeafClass {
				if !errors.Is(err, sentinel) {
					t.Fatalf("structured writer lost safe leaf classification: %v", err)
				}
			} else {
				if errors.Is(err, sentinel) || !errors.Is(err, errTerminalWriterFailed) {
					t.Fatalf("terminal writer failure was not sealed: %v", err)
				}
			}
			if errors.Unwrap(err) != nil {
				t.Fatal("surface error exposed its secret-bearing cause through Unwrap")
			}
		})
	}
}

type credentialErrorWriter struct {
	err error
}

func (writer credentialErrorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func TestStructuredInputErrorIsRedactedAtSurfaceBoundary(t *testing.T) {
	const secret = "test-key"
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	opts := buildTestCLIOptions(t, workspace, stateDir, "ses_structured_input_error")
	opts.OutputFormat = cli.OutputStreamJSON
	opts.InputFormat = cli.InputStreamJSON
	input := strings.NewReader(`{"type":"user","message":"x","priority":"` + secret + `"}` + "\n")
	var stdout, stderr bytes.Buffer
	err := runStructured(t.Context(), opts, workspace, input, &stdout, &stderr)
	if err == nil || strings.Contains(err.Error()+stderr.String(), secret) ||
		!strings.Contains(err.Error(), redact.Mask(secret)) {
		t.Fatalf("structured input error = %v; stderr=%q", err, stderr.String())
	}
}

func TestRedactedSurfaceUsageErrorRetainsExitClassification(t *testing.T) {
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	opts := buildTestCLIOptions(t, workspace, stateDir, "ses_surface_usage")
	err := runHeadless(t.Context(), opts, workspace, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !cli.IsUsageError(err) || ExitCode(err) != 2 {
		t.Fatalf("redacted usage classification = %v, exit=%d", err, ExitCode(err))
	}
}

func TestBuildSessionFreezesHTTPHookResponseCredentialsIntoSharedUnion(t *testing.T) {
	const secret = "<hook_context>\nsafe"
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(userHome, "config"))
	t.Setenv("APPDATA", filepath.Join(userHome, "appdata"))
	pluginRoot := filepath.Join(workspace, ".agentx", "plugins", "credential-hook")
	writeRuntimeFixture(t, filepath.Join(pluginRoot, ".agentx-plugin", "plugin.json"), `{"name":"credential-hook","version":"1.0.0"}`)
	escapedSecret := url.PathEscape(secret)
	hookURL := "https://hooks.example.invalid/submit/" + escapedSecret
	writeRuntimeFixture(t, filepath.Join(pluginRoot, "hooks", "hooks.json"), fmt.Sprintf(
		`{"hooks":[{"id":"credential-context","event":"UserPromptSubmit","kind":"http","url":%q,"sensitive_path_segments":[1],"timeout":1000000000}]}`,
		hookURL,
	))
	opts := buildTestCLIOptions(t, workspace, stateDir, "ses_frozen_hook_credentials")
	opts.Bare = false
	opts.TrustWorkspace = true
	session, err := buildSession(t.Context(), buildOptions{
		CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if !session.credentials.Covers(redact.New(secret, escapedSecret)) {
		t.Fatal("frozen HTTP hook response scope was not promoted into the shared session union")
	}
	framed := "prompt\n\n<hook_context>\nsafe\n</hook_context>"
	projected := session.sanitize(framed)
	if strings.Contains(projected, secret) || projected == framed {
		t.Fatalf("shared session sanitizer did not protect downstream hook framing: %q", projected)
	}
}

func TestRuntimeSanitizesBackgroundTaskCredentialBeforeDurableWrite(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const (
		sessionID = "ses_task_output_redaction"
		secret    = "test-key"
		want      = "before[REDACTED]after"
	)
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	options := buildTestCLIOptions(t, workspace, stateDir, sessionID)
	session, err := buildSession(t.Context(), buildOptions{
		CLI: options, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	record, err := session.tasks.LaunchShell(t.Context(), task.ShellSpec{
		Command: `printf before; printf '%s' "$TERM"; /bin/sleep 0.05; printf '%s' "$COLORTERM"; printf after`,
		Dir:     workspace, Shell: "/bin/bash", Timeout: 5 * time.Second,
		Env: append(os.Environ(),
			"TERM=test-",
			"COLORTERM=key",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	var result task.PollResult
	var output strings.Builder
	for attempts := 0; attempts < 10; attempts++ {
		result = pollTaskAfterCallback(t, session.tasks, record.ID, result.NextOffset, true, 5*time.Second)
		output.WriteString(result.Output)
		if result.Task.Status.Terminal() {
			break
		}
	}
	if result.Task.Status != task.StatusCompleted || output.String() != want {
		t.Fatalf("terminal task = %+v output=%q", result.Task, output.String())
	}
	for _, path := range []string{
		record.OutputPath,
		filepath.Join(filepath.Dir(filepath.Dir(record.OutputPath)), "state.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("configured credential survived in %s", filepath.Base(path))
		}
	}
}

func TestRuntimeSanitizesBackgroundTaskCommandAndDescriptionBeforePersistence(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const (
		sessionID = "ses_task_record_redaction"
		secret    = "test-key"
	)
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	session, err := buildSession(t.Context(), buildOptions{
		CLI:       buildTestCLIOptions(t, workspace, stateDir, sessionID),
		Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	record, err := session.tasks.LaunchShell(t.Context(), task.ShellSpec{
		Command: "printf done # " + secret, Description: "background " + secret,
		Dir: workspace, Shell: "/bin/bash", Timeout: 5 * time.Second,
		Env: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(record.Command, secret) || strings.Contains(record.Description, secret) {
		t.Fatalf("returned task record retained configured credential: %+v", record)
	}
	result := pollTaskAfterCallback(t, session.tasks, record.ID, 0, true, 5*time.Second)
	for !result.Task.Status.Terminal() {
		result = pollTaskAfterCallback(t, session.tasks, record.ID, result.NextOffset, true, 5*time.Second)
	}
	if strings.Contains(result.Task.Command, secret) || strings.Contains(result.Task.Description, secret) {
		t.Fatalf("terminal task record retained configured credential: %+v", result.Task)
	}
	state, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(record.OutputPath)), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), secret) {
		t.Fatal("task state journal retained configured command or description credential")
	}
}

func pollTaskAfterCallback(
	t *testing.T,
	manager *task.Manager,
	taskID task.ID,
	offset int64,
	block bool,
	wait time.Duration,
) task.PollResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := manager.Poll(t.Context(), taskID, offset, block, wait)
		switch {
		case err == nil:
			return result
		case err == task.ErrBusy && time.Now().Before(deadline):
			time.Sleep(time.Millisecond)
		default:
			t.Fatal(err)
		}
	}
}

func TestMCPExpansionRejectsModelCredentialNamesAndRenamedValues(t *testing.T) {
	const secret = "synthetic-process-model-credential"
	environment := []string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret,
		"RENAMED_MODEL_VALUE=" + secret,
		"AWS_SHARED_CREDENTIALS_FILE=/private/model-credentials",
		"RENAMED_AWS_FILE=/private/model-credentials",
		"GITHUB_TOKEN=explicit-server-credential",
	}
	configs := []mcp.Config{
		{Name: "direct", Scope: mcp.ScopeUser, Transport: mcp.TransportStdio, Command: "$AZURE_OPENAI_SUBSCRIPTION_KEY"},
		{Name: "renamed", Scope: mcp.ScopeUser, Transport: mcp.TransportStdio, Command: "$RENAMED_MODEL_VALUE"},
		{Name: "aws-file", Scope: mcp.ScopeUser, Transport: mcp.TransportStdio, Command: "$AWS_SHARED_CREDENTIALS_FILE"},
		{Name: "renamed-aws-file", Scope: mcp.ScopeUser, Transport: mcp.TransportStdio, Command: "$RENAMED_AWS_FILE"},
		{Name: "server", Scope: mcp.ScopeUser, Transport: mcp.TransportStdio, Command: "server", Env: map[string]string{"GITHUB_TOKEN": "$GITHUB_TOKEN"}},
	}
	expanded := expandMCPConfigurations(configs, environment)
	for _, index := range []int{0, 1, 2, 3} {
		if expanded[index].ConfigurationError == "" || strings.Contains(expanded[index].ConfigurationError, secret) {
			t.Fatalf("model credential expansion %d = %#v", index, expanded[index])
		}
		if strings.Contains(expanded[index].Command, secret) || strings.Contains(expanded[index].Command, "/private/model-credentials") {
			t.Fatalf("model credential reached command %d", index)
		}
	}
	if expanded[4].ConfigurationError != "" || expanded[4].Env["GITHUB_TOKEN"] != "explicit-server-credential" {
		t.Fatalf("explicit server credential expansion = %#v", expanded[4])
	}
}

func TestHookSnapshotEnvironmentDropsCredentialValueAliases(t *testing.T) {
	const secret = "synthetic-hook-model-credential"
	got := nonSecretEnvironment([]string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret,
		"RENAMED=" + secret,
		"SAFE=value",
	})
	if got["SAFE"] != "value" || got["AZURE_OPENAI_SUBSCRIPTION_KEY"] != "" || got["RENAMED"] != "" {
		t.Fatalf("hook environment snapshot = %#v", got)
	}
}

func TestMCPResultSanitizerIsServerScoped(t *testing.T) {
	const envSecret = "synthetic-mcp-environment-secret"
	const headerSecret = "synthetic-mcp-header-secret"
	sanitize, err := mcpResultSanitizer([]mcp.Config{{Name: "one",
		Env:     map[string]string{"OPAQUE_TOKEN": envSecret},
		Headers: map[string]string{"Authorization": "Bearer " + headerSecret},
	}, {Name: "other", Env: map[string]string{"DEBUG": "1"}}}, "one")
	if err != nil {
		t.Fatal(err)
	}
	got := sanitize.Apply("env=" + envSecret + " header=Bearer " + headerSecret + " token=" + headerSecret)
	if strings.Contains(got, envSecret) || strings.Contains(got, headerSecret) || strings.Count(got, "[REDACTED]") < 2 {
		t.Fatalf("MCP credential reflection was not sanitized: %q", got)
	}
	if got := sanitize.Apply("ordinary 1 value"); got != "ordinary 1 value" {
		t.Fatalf("another server's short value corrupted this provider result: %q", got)
	}
}

func TestMCPDescriptorCredentialReflectionIsOmitted(t *testing.T) {
	const secret = "synthetic-descriptor-credential"
	descriptor := mcp.ToolDescriptor{
		Name: "mcp__one__echo", Description: "leak " + secret,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	configs := []mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": secret}}}
	if !mcpDescriptorExposesConfiguredCredential(descriptor, configs, "one") {
		t.Fatal("credential-bearing MCP descriptor was not rejected")
	}
	if mcpDescriptorExposesConfiguredCredential(descriptor, configs, "other") {
		t.Fatal("one server's credential suppressed an unrelated descriptor")
	}
}

func TestMCPDescriptorCredentialReflectionRejectsShortOpaqueValues(t *testing.T) {
	for length := 1; length <= 7; length++ {
		secret := strings.Repeat("x", length)
		descriptor := mcp.ToolDescriptor{
			Name:        "mcp__one__echo",
			Description: "reflected:" + secret,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}
		configs := []mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": secret}}}
		if !mcpDescriptorExposesConfiguredCredential(descriptor, configs, "one") {
			t.Fatalf("%d-byte configured value was not rejected", length)
		}
	}
}

func TestMCPDescriptorCredentialReflectionRejectsJSONEscapedValues(t *testing.T) {
	const secret = "opaque-\"<&>\\credential"
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string", "description": secret},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(schema), secret) {
		t.Fatal("test fixture did not exercise a JSON-escaped credential")
	}
	descriptor := mcp.ToolDescriptor{Name: "mcp__one__echo", InputSchema: schema}
	configs := []mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": secret}}}
	if !mcpDescriptorExposesConfiguredCredential(descriptor, configs, "one") {
		t.Fatal("JSON-escaped configured value was not rejected")
	}
}

func TestMCPDescriptorCredentialReflectionRejectsAlternateJSONEscapesAndScalars(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		schema      json.RawMessage
		annotations map[string]any
	}{
		{name: "solidus", secret: "a/b", schema: json.RawMessage(`{"type":"object","description":"a\/b"}`)},
		{name: "unicode", secret: "secret", schema: json.RawMessage(`{"type":"object","description":"\u0073ecret"}`)},
		{name: "number", secret: "1", schema: json.RawMessage(`{"type":"object","minimum":1}`)},
		{name: "boolean", secret: "true", schema: json.RawMessage(`{"type":"object"}`), annotations: map[string]any{"reflected": true}},
		{name: "null", secret: "null", schema: json.RawMessage(`{"type":"object"}`), annotations: map[string]any{"reflected": nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := mcp.ToolDescriptor{
				Name: "mcp__one__echo", InputSchema: test.schema, Annotations: test.annotations,
			}
			configs := []mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": test.secret}}}
			if !mcpDescriptorExposesConfiguredCredential(descriptor, configs, "one") {
				t.Fatalf("%s configured value was not rejected", test.name)
			}
		})
	}
}

func TestMCPStructuredResultSanitizesSemanticJSONAliases(t *testing.T) {
	const secret = "a/b"
	sanitize, err := mcpResultSanitizer(
		[]mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": secret}}},
		"one",
	)
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := normalizeMCPResult(mcp.ToolResult{
		StructuredContent: json.RawMessage(`{"value":"a\/b"}`),
	}, sanitize)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decoded["value"], secret) {
		t.Fatalf("structured result retained semantic credential: %q", text)
	}
}

func TestMCPResultRedactionAmplificationStaysInsideMessageBound(t *testing.T) {
	sanitize, err := mcpResultSanitizer(
		[]mcp.Config{{Name: "one", Env: map[string]string{"OPAQUE_TOKEN": "q"}}},
		"one",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = normalizeMCPResult(mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: strings.Repeat("q", mcp.DefaultMaxMessageBytes/9)}},
	}, sanitize)
	if err == nil {
		t.Fatal("amplified MCP result exceeded the message bound without failing closed")
	}
}

func TestMCPResultNormalizationUsesAggregateSemanticOutputBudget(t *testing.T) {
	sanitize := redact.New("q")
	result := mcp.ToolResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: strings.Repeat("q", 8)}},
	}
	if text, metadata, err := normalizeMCPResultBounded(result, sanitize, 64); err == nil || text != "" || metadata != nil {
		t.Fatalf("amplified tiny-budget result = %q, %#v, %v; want fail closed", text, metadata, err)
	}
	result = mcp.ToolResult{
		StructuredContent: json.RawMessage(`{"values":["q","q","q"]}`),
	}
	if text, metadata, err := normalizeMCPResultBounded(result, sanitize, 32); err == nil || text != "" || metadata != nil {
		t.Fatalf("amplified structured result = %q, %#v, %v; want fail closed", text, metadata, err)
	}
}

func TestMCPResultSanitizerBoundsCredentialUnionAcrossConfigurations(t *testing.T) {
	configs := make([]mcp.Config, 0, 2)
	for configIndex := 0; configIndex < 2; configIndex++ {
		values := make(map[string]string, mcp.MaxCredentialLiterals/2+1)
		for valueIndex := 0; valueIndex <= mcp.MaxCredentialLiterals/2; valueIndex++ {
			key := fmt.Sprintf("SECRET_%d_%03d", configIndex, valueIndex)
			values[key] = "credential-" + key
		}
		configs = append(configs, mcp.Config{Name: "one", Env: values})
	}
	if _, err := mcpResultSanitizer(configs, "one"); err == nil ||
		!strings.Contains(err.Error(), "redaction workload") {
		t.Fatalf("oversized cross-config credential union error = %v", err)
	}
}

func TestRuntimeCredentialUnionRejectsMissingSafeStreamMarker(t *testing.T) {
	const candidates = "*#~!@$%^&_-+=:;,.?0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz/|"
	values := make(map[string]string, len(candidates))
	for index := 0; index < len(candidates); index++ {
		values[fmt.Sprintf("TOKEN_%03d", index)] = candidates[index : index+1]
	}
	if _, err := runtimeCredentialSanitizer("synthetic-api-key", []mcp.Config{{
		Name: "one", Env: values,
	}}); err == nil || !strings.Contains(err.Error(), "safe streaming projection") {
		t.Fatalf("guard-exhausted runtime credential set = %v", err)
	}
}

func TestConnectedMCPConfigsSelectExactProviderScopedWinner(t *testing.T) {
	shadowed := mcp.Config{
		Name: "shared", Scope: mcp.ScopeUser, SourceID: "user:shared",
		Transport: mcp.TransportStdio, Command: "server",
		Env: map[string]string{"MCP_TOKEN": "1"},
	}
	winner := mcp.Config{
		Name: "shared", Scope: mcp.ScopeProject, SourceID: "project:shared",
		Transport: mcp.TransportStdio, Command: "server", Trusted: true, Approved: true,
		Env: map[string]string{"DEBUG": "1"},
	}
	descriptor, err := mcp.ValidateConfig(winner)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.State = mcp.StateConnected
	active := connectedMCPConfigs([]mcp.Config{shadowed, winner}, mcp.Snapshot{
		Servers: []mcp.Descriptor{descriptor},
	})
	if len(active) != 1 || active[0].SourceID != winner.SourceID {
		t.Fatalf("active MCP credential configs = %#v", active)
	}
	set, err := runtimeCredentialSanitizer("synthetic-api-key", active)
	if err != nil {
		t.Fatal(err)
	}
	if set.Contains("1") {
		t.Fatal("shadowed incompatible credential entered active session union")
	}
}

func TestRuntimeMCPUnionIncludesUnpublishedDefinitionsUsedBySiblingStatus(t *testing.T) {
	const unpublishedCredential = "connected"
	workspace := t.TempDir()
	userRoot := t.TempDir()
	writeRuntimeFixture(t, filepath.Join(userRoot, "mcp.json"), `{
		"mcpServers": {
			"unpublished-a": {
				"type": "stdio",
				"command": "unused",
				"env": {"MCP_TOKEN": "connected"},
				"disabled": true
			},
			"published-b": {
				"type": "stdio",
				"command": "unused",
				"disabled": true
			}
		}
	}`)
	loaded, _, err := discoverExtensionsFromUserRoot(
		t.Context(),
		workspace,
		cli.Options{},
		nil,
		userRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := loaded.mcp.Close(); err != nil {
			t.Error(err)
		}
	}()
	if len(loaded.mcpCredentialConfigs) != 2 {
		t.Fatalf("frozen MCP credential definitions = %d, want all 2", len(loaded.mcpCredentialConfigs))
	}
	credentials, err := runtimeCredentialSanitizer("synthetic-api-key", loaded.mcpCredentialConfigs)
	if err != nil {
		t.Fatal(err)
	}
	if !credentials.Contains(unpublishedCredential) {
		t.Fatal("unpublished provider credential was absent from the shared runtime union")
	}

	publishedConfig := mcp.Config{
		Name: "published-b", Scope: mcp.ScopeUser, SourceID: "user:mcp",
		Transport: mcp.TransportStdio, Command: "unused",
	}
	published, err := mcp.ValidateConfig(publishedConfig)
	if err != nil {
		t.Fatal(err)
	}
	published.State = mcp.StateConnected
	session := &runtimeSession{services: runtimeServices{extensions: runtimeExtensions{
		mcpState: mcp.Snapshot{Servers: []mcp.Descriptor{published}},
	}}}
	record, err := json.Marshal(map[string]any{"mcp_servers": sdkMCPServers(session)})
	if err != nil {
		t.Fatal(err)
	}
	record = append(record, '\n')
	if err := credentialJSONValidator(credentials)(record); err == nil {
		t.Fatalf("sibling status reconstructed unpublished credential: %s", record)
	}
}

func TestForkAndNonpersistentResumeRevalidateSourceWithCompleteCredentialUnion(t *testing.T) {
	const (
		sourceID = "ses_source_requires_full_union"
		secret   = "mcp-only-source-credential"
	)
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	opts := buildTestCLIOptions(t, workspace, stateDir, "ses_unused")

	sourceDir := testSessionDir(stateDir, workspace, sourceID)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".session.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceStore, err := transcript.Open(t.Context(), transcript.Config{
		Path: filepath.Join(sourceDir, "transcript.jsonl"), SessionID: sourceID, SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_source_secret", SessionID: sourceID,
		TurnID: "turn_source_secret", Sequence: 1, Timestamp: time.Now(),
		Kind: protocol.EventKindMessage, Visibility: protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginUser,
		Message: &protocol.Message{
			Role: protocol.RoleUser,
			Content: []protocol.ContentBlock{
				protocol.TextBlock(secret),
			},
		},
	}
	if err := sourceStore.Append(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.Close(); err != nil {
		t.Fatal(err)
	}

	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)
	userConfigRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, filepath.Join(userConfigRoot, "agentx", "mcp.json"), fmt.Sprintf(`{
		"mcpServers": {
			"source-guard": {
				"type": "stdio",
				"command": "unused",
				"env": {"MCP_TOKEN": %q},
				"disabled": true
			}
		}
	}`, secret))

	tests := []struct {
		name          string
		fork          bool
		noPersistence bool
		sessionID     string
	}{
		{name: "fork", fork: true, sessionID: "ses_validated_fork_destination"},
		{name: "nonpersistent resume", noPersistence: true},
		{name: "persistent resume"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := opts
			candidate.Bare = false
			candidate.Resume = sourceID
			candidate.ForkSession = test.fork
			candidate.NoSessionPersistence = test.noPersistence
			candidate.SessionID = test.sessionID
			session, buildErr := buildSession(t.Context(), buildOptions{
				CLI: candidate, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard,
			})
			if session != nil {
				_ = session.Close()
				t.Fatal("credential-bearing source transcript was restored")
			}
			if buildErr == nil || strings.Contains(buildErr.Error(), secret) {
				t.Fatalf("validated source error = %v", buildErr)
			}
		})
	}
}

func TestConnectedMCPConfigsRejectSameSourceShadowFingerprint(t *testing.T) {
	oversizedShadowEnvironment := make(map[string]string, mcp.MaxCredentialLiterals)
	for index := 0; index < mcp.MaxCredentialLiterals; index++ {
		oversizedShadowEnvironment[fmt.Sprintf("TOKEN_%03d", index)] = fmt.Sprintf("shadow-credential-%03d", index)
	}
	shadowed := mcp.Config{
		Name: "shared", Scope: mcp.ScopeUser, SourceID: "user:mcp",
		Transport: mcp.TransportStdio, Command: "server",
		Env: oversizedShadowEnvironment,
	}
	disabled := mcp.Config{
		Name: "shared", Scope: mcp.ScopeUser, SourceID: "user:mcp",
		Transport: mcp.TransportStdio, Command: "server", Disabled: true,
		Env: map[string]string{"MCP_TOKEN": "disabled-credential"},
	}
	failed := mcp.Config{
		Name: "shared", Scope: mcp.ScopeUser, SourceID: "user:mcp",
		Transport: mcp.TransportStdio, Command: "server",
		Env: map[string]string{"MCP_TOKEN": "failed-credential"},
	}
	winner := mcp.Config{
		Name: "shared", Scope: mcp.ScopeUser, SourceID: "user:mcp",
		Transport: mcp.TransportStdio, Command: "server",
		Env: map[string]string{"DEBUG": "1"},
	}
	descriptor, err := mcp.ValidateConfig(winner)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.State = mcp.StateConnected
	disabledDescriptor, err := mcp.ValidateConfig(disabled)
	if err != nil {
		t.Fatal(err)
	}
	disabledDescriptor.State = mcp.StateDisabled
	failedDescriptor, err := mcp.ValidateConfig(failed)
	if err != nil {
		t.Fatal(err)
	}
	failedDescriptor.State = mcp.StateFailed
	active := connectedMCPConfigs([]mcp.Config{shadowed, disabled, failed, winner}, mcp.Snapshot{
		Servers: []mcp.Descriptor{descriptor, disabledDescriptor, failedDescriptor},
	})
	if len(active) != 1 || active[0].Env["DEBUG"] != "1" {
		t.Fatalf("same-source active MCP credential configs = %#v", active)
	}
	sanitize, err := mcpResultSanitizer(active, "shared")
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{"shadow-credential-255", "disabled-credential", "failed-credential"} {
		if sanitize.Contains(excluded) {
			t.Fatalf("same-source non-published credential %q entered provider sanitizer", excluded)
		}
	}
	if sanitize.Contains("1") {
		t.Fatal("same-source shadow credential entered published provider sanitizer")
	}
	candidate := mcp.ToolDescriptor{
		Name: "mcp__shared__echo", Description: "protocol version 1",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	if mcpDescriptorExposesCredential(candidate, sanitize) {
		t.Fatal("ordinary healthy descriptor was suppressed by same-source shadow credential")
	}
}

func TestMCPToolUsesOrdinaryPermissionComposition(t *testing.T) {
	raw := json.RawMessage(`{}`)
	request := permission.Request{
		Tool: "mcp__one__echo", Input: raw,
		Classification: permission.Classification{OpenWorld: true},
	}
	if request.MandatoryAsk != "" || !request.Classification.OpenWorld {
		t.Fatalf("MCP permission projection bypassed ordinary composition: %#v", request)
	}
	rule, err := permission.ParseRule(request.Tool, permission.EffectAllow, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		cfg  permission.Config
		want permission.DecisionKind
	}{
		{name: "default", cfg: permission.Config{Workspace: t.TempDir(), PromptSuppressed: true}, want: permission.DecisionDeny},
		{name: "exact allow", cfg: permission.Config{Workspace: t.TempDir(), Rules: []permission.Rule{rule}, PromptSuppressed: true}, want: permission.DecisionAllow},
		{name: "bypass", cfg: permission.Config{Workspace: t.TempDir(), Mode: permission.ModeBypass, BypassAvailable: true, PromptSuppressed: true}, want: permission.DecisionAllow},
		{name: "dont ask", cfg: permission.Config{Workspace: t.TempDir(), Mode: permission.ModeDontAsk, PromptSuppressed: true}, want: permission.DecisionDeny},
	} {
		t.Run(test.name, func(t *testing.T) {
			evaluator, err := permission.NewEvaluator(test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(context.Background(), request, nil)
			if err != nil || decision.Kind != test.want {
				t.Fatalf("decision = %#v, %v; want %s", decision, err, test.want)
			}
		})
	}
}

func TestMCPToolAdapterRejectsDescriptorWithoutCatalogBinding(t *testing.T) {
	descriptor := mcp.ToolDescriptor{Name: "mcp__one__echo", InputSchema: json.RawMessage(`{"type":"object"}`)}
	if _, err := adaptMCPTool(mcp.NewManager(nil), descriptor, nil); err == nil {
		t.Fatal("unbound MCP descriptor was adapted into an executable capability")
	}
}
