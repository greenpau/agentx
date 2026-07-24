package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/command"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/sessionlock"
	"github.com/greenpau/agentx/pkg/surface"
)

const appLockHelperPathEnv = "AGENTX_TEST_APP_SESSION_LOCK_PATH"
const buildIdentityHelperEnv = "AGENTX_TEST_BUILD_IDENTITY"

func TestConfiguredBuildIdentityIsImmutable(t *testing.T) {
	configureTestAuthFilePresence(t)
	if os.Getenv(buildIdentityHelperEnv) == "1" {
		const version = "9.8.7"
		const banner = "agentx 9.8.7, branch: test, commit: deadbeef"
		ConfigureBuildIdentity(version, banner)
		ConfigureBuildIdentity("1.0.0", "agentx 1.0.0")
		if got := ProductVersion(); got != version {
			t.Fatalf("product version = %q, want %q", got, version)
		}

		var versionOutput bytes.Buffer
		if err := Run(t.Context(), []string{"--version"}, strings.NewReader(""), &versionOutput, io.Discard); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(versionOutput.String()); got != banner {
			t.Fatalf("version banner = %q, want %q", got, banner)
		}

		var sdkOutput bytes.Buffer
		if err := encodeSDKInit(surface.NewEncoder(&sdkOutput), newSDKWireSession(t), cli.Options{}); err != nil {
			t.Fatal(err)
		}
		var sdkRecord map[string]any
		if err := json.Unmarshal(sdkOutput.Bytes(), &sdkRecord); err != nil {
			t.Fatal(err)
		}
		if got := sdkRecord["agentx_version"]; got != version {
			t.Fatalf("SDK version = %#v, want %q", got, version)
		}

		result, code, message := handleMCPRequest(t.Context(), nil, nil, mcpRPCRequest{Method: "initialize"})
		if code != 0 || message != "" {
			t.Fatalf("MCP initialize = code %d, message %q", code, message)
		}
		payload, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("MCP initialize result = %#v", result)
		}
		serverInfo, ok := payload["serverInfo"].(map[string]string)
		if !ok || serverInfo["version"] != version {
			t.Fatalf("MCP server info = %#v, want version %q", result, version)
		}
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestConfiguredBuildIdentityIsImmutable$")
	command.Env = append(os.Environ(), buildIdentityHelperEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build identity helper: %v\n%s", err, output)
	}
}

func TestAppSessionLockHelperProcess(t *testing.T) {
	path := os.Getenv(appLockHelperPathEnv)
	if path == "" {
		return
	}
	lock, err := sessionlock.Acquire(context.Background(), path)
	if err != nil {
		if errors.Is(err, sessionlock.ErrContended) {
			fmt.Fprintln(os.Stdout, "contended")
			return
		}
		fmt.Fprintf(os.Stdout, "error:%v\n", err)
		return
	}
	fmt.Fprintln(os.Stdout, "acquired")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = lock.Close()
}

type appLockHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	once    sync.Once
	err     error
}

type failAfterWriter struct {
	mu        sync.Mutex
	remaining int
	err       error
}

type testAuthFileDocument struct {
	Version     int                     `json:"version"`
	Provider    string                  `json:"provider"`
	AzureOpenAI testAzureOpenAIAuthFile `json:"azure_openai"`
}

type testAzureOpenAIAuthFile struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
	APIKey     string `json:"api_key"`
	APIVersion string `json:"api_version"`
}

func configureTestAgentXHome(t *testing.T, endpoint, model, deployment, apiKey, apiVersion string) (string, string) {
	t.Helper()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	authPath := writeTestAuthFile(t, agentxHome, endpoint, model, deployment, apiKey, apiVersion)
	t.Setenv("AGENTX_HOME", agentxHome)
	return agentxHome, authPath
}

func testProviderContext(t *testing.T, server *httptest.Server) context.Context {
	t.Helper()
	if server == nil || server.Client() == nil {
		t.Fatal("TLS provider test server is unavailable")
	}
	return context.WithValue(t.Context(), modelHTTPClientContextKey{}, server.Client())
}

func configureTestAuthFilePresence(t *testing.T) {
	t.Helper()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	if err := os.MkdirAll(agentxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentxHome, config.DefaultAuthFile), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTX_HOME", agentxHome)
}

func writeTestAuthFile(t *testing.T, agentxHome, endpoint, model, deployment, apiKey, apiVersion string) string {
	t.Helper()
	if err := os.MkdirAll(agentxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	document := testAuthFileDocument{
		Version:  1,
		Provider: "azure_openai",
		AzureOpenAI: testAzureOpenAIAuthFile{
			Endpoint: endpoint, Model: model, Deployment: deployment,
			APIKey: apiKey, APIVersion: apiVersion,
		},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(agentxHome, config.DefaultAuthFile)
	if err := os.WriteFile(authPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return authPath
}

func (w *failAfterWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return len(data), nil
}

func startAppLockHelper(t *testing.T, path string) (*appLockHelper, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestAppSessionLockHelperProcess$")
	command.Env = append(os.Environ(), appLockHelperPathEnv+"="+path)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helper := &appLockHelper{command: command, stdin: stdin}
	lineReady := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lineReady <- scanner.Text()
			return
		}
		lineReady <- ""
	}()
	select {
	case line := <-lineReady:
		return helper, line
	case <-time.After(3 * time.Second):
		helper.release()
		t.Fatal("session lock helper did not report promptly")
		return nil, ""
	}
}

func (helper *appLockHelper) release() error {
	helper.once.Do(func() {
		_ = helper.stdin.Close()
		helper.err = helper.command.Wait()
	})
	return helper.err
}

func TestVersionUsesAuthFilePresenceGate(t *testing.T) {
	configureTestAuthFilePresence(t)
	var output bytes.Buffer
	if err := Run(t.Context(), []string{"--version"}, strings.NewReader(""), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != productVersionBanner()+"\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestStartupOutputContainsHostWriterFailures(t *testing.T) {
	configureTestAuthFilePresence(t)
	if err := Run(t.Context(), []string{"--help"}, strings.NewReader(""), panicWriter{}, io.Discard); err != errTerminalWriterPanicked {
		t.Fatalf("help writer panic = %v", err)
	}
	if err := Run(t.Context(), []string{"--help"}, strings.NewReader(""), terminalShortWriter{}, io.Discard); err != io.ErrShortWrite {
		t.Fatalf("help short writer = %v", err)
	}
	hostile := hostileTerminalWriterError{}
	if err := Run(
		t.Context(),
		[]string{"--version"},
		strings.NewReader(""),
		terminalErrorWriter{err: hostile},
		io.Discard,
	); err != errTerminalWriterFailed {
		t.Fatalf("version writer failure = %v", err)
	}
}

func TestHeadlessInterruptOwnershipUsesFinalSurfaceInference(t *testing.T) {
	for _, test := range []struct {
		args           []string
		stdoutTerminal bool
		want           bool
	}{
		{args: []string{"--print", "hello"}, stdoutTerminal: true, want: true},
		{args: []string{"-p"}, stdoutTerminal: true, want: true},
		{args: []string{"--print=true"}, stdoutTerminal: false, want: false},
		{args: []string{"--output-format", "json", "hello"}, stdoutTerminal: true, want: true},
		{args: []string{"hello"}, stdoutTerminal: false, want: true},
	} {
		if got := cli.HeadlessRequested(test.args, test.stdoutTerminal); got != test.want {
			t.Fatalf("HeadlessRequested(%q, %v) = %v", test.args, test.stdoutTerminal, got)
		}
	}
}

func TestStructuredStartupFailureKeepsStdoutCleanAndRedactsCredentials(t *testing.T) {
	workspace := t.TempDir()
	const secret = "fake-production-secret-for-startup-test"
	configureTestAgentXHome(
		t,
		"https://example.test/openai?credential="+secret,
		"gpt-5.6-sol",
		"gpt-5.6-sol",
		secret,
		"2026-07-01-preview",
	)
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{
		"--print", "--output-format", "stream-json", "--no-session-persistence",
		"--cwd", workspace, "hello",
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("invalid endpoint unexpectedly initialized a structured session")
	}
	if stdout.Len() != 0 {
		t.Fatalf("structured stdout was contaminated before init: %q", stdout.String())
	}
	if strings.Contains(err.Error()+stderr.String(), secret) {
		t.Fatal("startup diagnostic exposed the configured credential")
	}
}

func TestPostConfigStartupErrorsRedactCredentialAndPreserveCause(t *testing.T) {
	workspace := t.TempDir()
	const secret = "opaque-post-config-credential"
	agentxHome := filepath.Join(t.TempDir(), secret)
	writeTestAuthFile(t, agentxHome, "https://example.test", "gpt-5.6-sol", "gpt-5.6-sol", secret, "2026-07-01-preview")
	sessionsRoot := filepath.Join(agentxHome, "sessions")
	if err := os.Mkdir(sessionsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	projectHash := sha256.Sum256([]byte(workspace))
	projectKey := hex.EncodeToString(projectHash[:12])
	if err := os.WriteFile(filepath.Join(sessionsRoot, projectKey), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTX_HOME", agentxHome)
	_, err := buildSession(t.Context(), buildOptions{CLI: cli.Options{
		Print: true, Bare: true, OutputFormat: cli.OutputText, InputFormat: cli.InputText,
		SessionID: "ses_redacted_path", MaxTurns: 1,
	}, Workspace: workspace})
	if err == nil {
		t.Fatal("invalid credential-bearing AgentX home unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("post-config startup error was not redacted: %v", err)
	}

	cause := errors.New("sentinel " + secret)
	wrapped := redactOperationalError(cause, func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") })
	if !errors.Is(wrapped, cause) || strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("redacted wrapper lost cause or text safety: %v", wrapped)
	}
}

func TestBuildSessionRetainsCrossProcessLockUntilClose(t *testing.T) {
	workspace := t.TempDir()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	sessionID := "ses_lock_contention"
	opts := buildTestCLIOptions(t, workspace, agentxHome, sessionID)
	sessionDir := testSessionDir(agentxHome, workspace, sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(sessionDir, ".session.lock")
	holder, line := startAppLockHelper(t, lockPath)
	if line != "acquired" {
		_ = holder.release()
		t.Fatalf("initial lock helper = %q", line)
	}
	t.Cleanup(func() { _ = holder.release() })

	session, err := buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard})
	if session != nil {
		_ = session.Close()
	}
	if !errors.Is(err, sessionlock.ErrContended) {
		t.Fatalf("build with live owner = %v, want session contention", err)
	}
	for _, path := range []string{filepath.Join(sessionDir, "transcript.jsonl"), filepath.Join(sessionDir, "task-runtime")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("contention opened session state %s: %v", path, statErr)
		}
	}
	if err := holder.release(); err != nil {
		t.Fatal(err)
	}

	session, err = buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	probe, line := startAppLockHelper(t, lockPath)
	if line != "contended" {
		_ = probe.release()
		_ = session.Close()
		t.Fatalf("runtime did not retain session lock: helper=%q", line)
	}
	if err := probe.release(); err != nil {
		t.Fatal(err)
	}
	closeErrors := make(chan error, 8)
	var closeGroup sync.WaitGroup
	for range 8 {
		closeGroup.Add(1)
		go func() {
			defer closeGroup.Done()
			closeErrors <- session.Close()
		}()
	}
	closeGroup.Wait()
	close(closeErrors)
	for err := range closeErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	afterClose, line := startAppLockHelper(t, lockPath)
	if line != "acquired" {
		_ = afterClose.release()
		t.Fatalf("runtime Close did not release session lock: helper=%q", line)
	}
	if err := afterClose.release(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFailureReleasesSessionLock(t *testing.T) {
	workspace := t.TempDir()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	sessionID := "ses_lock_build_failure"
	opts := buildTestCLIOptions(t, workspace, agentxHome, sessionID)
	sessionDir := testSessionDir(agentxHome, workspace, sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// task.Open requires a directory here, forcing failure after the session
	// lock and transcript store have both been acquired.
	if err := os.WriteFile(filepath.Join(sessionDir, "task-runtime"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard})
	if session != nil {
		_ = session.Close()
	}
	if err == nil {
		t.Fatal("build unexpectedly succeeded with invalid task-runtime path")
	}
	probe, line := startAppLockHelper(t, filepath.Join(sessionDir, ".session.lock"))
	if line != "acquired" {
		_ = probe.release()
		t.Fatalf("build failure leaked session lock: helper=%q error=%v", line, err)
	}
	if releaseErr := probe.release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func buildTestCLIOptions(t *testing.T, workspace, agentxHome, sessionID string) cli.Options {
	t.Helper()
	writeTestAuthFile(t, agentxHome, "https://example.test", "gpt-5.6-sol", "gpt-5.6-sol", "test-key", "2026-07-01-preview")
	t.Setenv("AGENTX_HOME", agentxHome)
	return cli.Options{
		Print: true, Bare: true, OutputFormat: cli.OutputText, InputFormat: cli.InputText,
		SessionID: sessionID, MaxTurns: 1,
	}
}

func testSessionDir(agentxHome, workspace, sessionID string) string {
	digest := sha256.Sum256([]byte(workspace))
	return filepath.Join(agentxHome, "sessions", hex.EncodeToString(digest[:12]), sessionID)
}

func TestHeadlessAzureVerticalSlice(t *testing.T) {
	const (
		authModel      = "gpt-5.6-sol"
		authDeployment = "auth-file-wire-deployment"
		authAPIKey     = "auth-file-only-test-key"
		authAPIVersion = "auth-file-test-version"
	)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("method=%s", request.Method)
		}
		if request.URL.Path != "/openai/responses" {
			t.Errorf("path=%s", request.URL.Path)
		}
		if request.URL.Query().Get("api-version") != authAPIVersion {
			t.Errorf("api-version=%q", request.URL.Query().Get("api-version"))
		}
		if request.Header.Get("api-key") != authAPIKey || request.Header.Get("Authorization") != "" {
			t.Errorf("authentication headers did not come exclusively from auth.json")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != authDeployment || body["stream"] != true || body["store"] != false {
			t.Errorf("body=%#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n")
		fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n", authDeployment)
	}))
	defer server.Close()
	workspace := t.TempDir()
	agentxHome, authFile := configureTestAgentXHome(
		t,
		server.URL,
		authModel,
		authDeployment,
		authAPIKey,
		authAPIVersion,
	)
	info, err := os.Stat(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json protection = %s, want regular 0600 file", info.Mode())
	}
	credentialKeys := []string{
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_MODEL_NAME", "AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "AZURE_OPENAI_API_VERSION",
	}
	type environmentValue struct {
		value   string
		present bool
	}
	previous := make(map[string]environmentValue, len(credentialKeys))
	for _, key := range credentialKeys {
		value, present := os.LookupEnv(key)
		previous[key] = environmentValue{value: value, present: present}
	}
	t.Cleanup(func() {
		for _, key := range credentialKeys {
			saved := previous[key]
			var err error
			if saved.present {
				err = os.Setenv(key, saved.value)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
		}
	})
	for _, key := range credentialKeys {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	for _, key := range credentialKeys {
		if value, present := os.LookupEnv(key); present {
			t.Fatalf("process credential %s remained present with %d bytes", key, len(value))
		}
	}
	loaded, err := config.Load(authFile, os.Environ(), config.Overrides{})
	if err != nil {
		t.Fatalf("load auth-file-only configuration: %v", err)
	}
	for _, key := range credentialKeys {
		if loaded.Provenance[key] != config.SourceFile {
			t.Fatalf("%s provenance=%q, want %q", key, loaded.Provenance[key], config.SourceFile)
		}
	}
	if loaded.Provenance["model"] != config.SourceFile {
		t.Fatalf("logical model provenance=%q, want %q", loaded.Provenance["model"], config.SourceFile)
	}
	t.Setenv("AGENTX_HOME", agentxHome)
	var output, diagnostics bytes.Buffer
	err = Run(testProviderContext(t, server), []string{"--print", "--no-session-persistence", "--cwd", workspace, "--model", authModel, "say hi"}, strings.NewReader(""), &output, &diagnostics)
	if err != nil {
		t.Fatalf("Run: %v diagnostics=%q", err, diagnostics.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests=%d, want exactly one auth-file-backed request", requests.Load())
	}
	if output.String() != "hello\n" {
		t.Fatalf("output=%q", output.String())
	}
	if combined := output.String() + diagnostics.String(); strings.Contains(combined, authAPIKey) {
		t.Fatal("auth-file credential leaked")
	}
}

func TestHeadlessSlashRoutingPrecedesModelInvocationOnEveryOneShotFormat(t *testing.T) {
	for _, test := range []struct {
		name       string
		format     string
		prompt     string
		wantErr    error
		wantCalls  int32
		wantOutput string
	}{
		{name: "text supported local", format: "text", prompt: "/cost", wantOutput: "cost is unknown"},
		{name: "json supported local", format: "json", prompt: "/cost", wantOutput: `"num_turns":0`},
		{name: "stream supported local", format: "stream-json", prompt: "/cost", wantOutput: `"subtype":"local_command_output"`},
		{name: "known unsupported", format: "text", prompt: "/mcp", wantErr: command.ErrNonInteractive},
		{name: "unknown valid", format: "text", prompt: "/missing-command", wantErr: command.ErrUnknown},
		{name: "invalid slash grammar is prompt", format: "text", prompt: "/bad.name", wantCalls: 1, wantOutput: "model-called"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writeCommandRoutingCompletion(writer, "model-called")
			}))
			defer server.Close()
			workspace := configureCommandRoutingRuntime(t, server.URL)
			args := []string{
				"--print", "--bare", "--no-session-persistence", "--output-format", test.format,
				"--cwd", workspace, test.prompt,
			}
			var output, diagnostics bytes.Buffer
			err := Run(testProviderContext(t, server), args, strings.NewReader(""), &output, &diagnostics)
			if test.wantErr == nil && err != nil {
				t.Fatalf("Run: %v diagnostics=%q output=%q", err, diagnostics.String(), output.String())
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, test.wantErr)
			}
			if requests.Load() != test.wantCalls {
				t.Fatalf("provider requests = %d, want %d; output=%s diagnostics=%s", requests.Load(), test.wantCalls, output.String(), diagnostics.String())
			}
			if test.wantOutput != "" && !strings.Contains(output.String(), test.wantOutput) {
				t.Fatalf("output = %q, want substring %q", output.String(), test.wantOutput)
			}
			if test.wantErr != nil && output.Len() != 0 {
				t.Fatalf("text command error contaminated stdout: %q", output.String())
			}
		})
	}
}

func TestDuplexStructuredSlashRoutingReportsLocallyAndContinues(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeCommandRoutingCompletion(writer, "after-local-error")
	}))
	defer server.Close()
	workspace := configureCommandRoutingRuntime(t, server.URL)
	input := strings.NewReader(strings.Join([]string{
		`{"type":"user","uuid":"command-unsupported","message":"/mcp"}`,
		`{"type":"user","uuid":"command-local","message":"/cost"}`,
		`{"type":"user","uuid":"command-model","message":"/bad.name"}`,
	}, "\n") + "\n")
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--no-session-persistence",
		"--output-format", "stream-json", "--input-format", "stream-json",
		"--cwd", workspace,
	}, input, &output, &diagnostics)
	if err != nil {
		t.Fatalf("Run: %v diagnostics=%q output=%q", err, diagnostics.String(), output.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want only the invalid-grammar prompt", requests.Load())
	}
	records := decodeNDJSONRecords(t, output.String())
	localOutputs := 0
	zeroTurnResults := 0
	modelResults := 0
	initCommands := map[string]bool{}
	for _, record := range records {
		if record["type"] == "system" && record["subtype"] == "init" {
			for _, name := range record["slash_commands"].([]any) {
				initCommands[name.(string)] = true
			}
		}
		if record["type"] == "system" && record["subtype"] == "local_command_output" {
			localOutputs++
		}
		if record["type"] == "result" {
			if record["num_turns"] == float64(0) {
				zeroTurnResults++
			} else {
				modelResults++
			}
		}
	}
	if localOutputs != 2 || zeroTurnResults != 2 || modelResults != 1 {
		t.Fatalf("structured command/model projections = local outputs %d, zero-turn results %d, model results %d; output=%s", localOutputs, zeroTurnResults, modelResults, output.String())
	}
	if !initCommands["compact"] || !initCommands["cost"] || initCommands["mcp"] || initCommands["model"] {
		t.Fatalf("noninteractive init command inventory = %#v", initCommands)
	}
	if !strings.Contains(output.String(), "command is unavailable in noninteractive mode") || !strings.Contains(output.String(), "after-local-error") {
		t.Fatalf("structured local error or later model result missing: %s", output.String())
	}
}

func configureCommandRoutingRuntime(t *testing.T, endpoint string) string {
	t.Helper()
	workspace := t.TempDir()
	configureTestAgentXHome(t, endpoint, "gpt-5.6-sol", "gpt-5.6-sol", "synthetic-command-key", "2026-07-01-preview")
	return workspace
}

func writeCommandRoutingCompletion(writer http.ResponseWriter, text string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_command\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_command\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_command\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", text)
	fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_command\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_command\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", text)
}

func decodeNDJSONRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode NDJSON record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestStructuredPromptUUIDDeduplicatesAfterResume(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_dedup\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_dedup\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_dedup\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_dedup\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	workspace := t.TempDir()
	configureTestAgentXHome(t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol", "test-key", "2024-12-01-preview")

	baseArgs := []string{"--print", "--bare", "--output-format", "stream-json", "--input-format", "stream-json", "--cwd", workspace, "--max-turns", "1"}
	input := strings.NewReader(`{"type":"user","uuid":"host-prompt-durable","message":"perform once"}` + "\n")
	var firstOutput, firstDiagnostics bytes.Buffer
	providerContext := testProviderContext(t, server)
	if err := Run(providerContext, append(append([]string(nil), baseArgs...), "--session-id", "ses_structured_dedup"), input, &firstOutput, &firstDiagnostics); err != nil {
		t.Fatalf("first structured run: %v diagnostics=%q", err, firstDiagnostics.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("first run provider requests = %d", requests.Load())
	}

	input = strings.NewReader(`{"type":"user","uuid":"host-prompt-durable","message":"must not run"}` + "\n")
	var secondOutput, secondDiagnostics bytes.Buffer
	if err := Run(providerContext, append(append([]string(nil), baseArgs...), "--resume", "ses_structured_dedup"), input, &secondOutput, &secondDiagnostics); err != nil {
		t.Fatalf("resumed structured run: %v diagnostics=%q", err, secondDiagnostics.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("duplicate prompt invoked provider; requests = %d", requests.Load())
	}
	if strings.Contains(secondOutput.String(), `"duplicate_ignored"`) || strings.Contains(secondOutput.String(), `"uuid":"host-prompt-durable"`) {
		t.Fatalf("duplicate emitted a non-schema acknowledgement without replay enabled: %s", secondOutput.String())
	}
}

func TestStructuredEOFExitFollowsLastOrdinaryResult(t *testing.T) {
	for _, laterSuccess := range []bool{false, true} {
		name := "interrupted-last"
		if laterSuccess {
			name = "later-success"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if requests.Add(1) == 1 {
					close(firstStarted)
					select {
					case <-request.Context().Done():
					case <-releaseFirst:
					}
					return
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_after_interrupt\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
				fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_after_interrupt\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
				fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_after_interrupt\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_after_interrupt\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"recovered\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			}))
			defer server.Close()
			defer close(releaseFirst)

			workspace := t.TempDir()
			configureTestAgentXHome(t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol", "test-key", "2024-12-01-preview")

			inputReader, inputWriter := io.Pipe()
			var output, diagnostics bytes.Buffer
			providerContext := testProviderContext(t, server)
			done := make(chan error, 1)
			go func() {
				done <- Run(providerContext, []string{
					"--print", "--bare", "--no-session-persistence",
					"--output-format", "stream-json", "--input-format", "stream-json",
					"--cwd", workspace,
				}, inputReader, &output, &diagnostics)
			}()
			if _, err := fmt.Fprintln(inputWriter, `{"type":"user","uuid":"prompt-interrupted","message":"wait"}`); err != nil {
				t.Fatal(err)
			}
			select {
			case <-firstStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("first structured turn did not reach the local provider")
			}
			if _, err := fmt.Fprintln(inputWriter, `{"type":"control_request","request_id":"interrupt-exit","request":{"subtype":"interrupt"}}`); err != nil {
				t.Fatal(err)
			}
			if laterSuccess {
				if _, err := fmt.Fprintln(inputWriter, `{"type":"user","uuid":"prompt-after-interrupt","message":"continue"}`); err != nil {
					t.Fatal(err)
				}
			}
			if err := inputWriter.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if laterSuccess && err != nil {
					t.Fatalf("later success did not supersede interrupted result: %v; output=%s diagnostics=%s", err, output.String(), diagnostics.String())
				}
				if !laterSuccess && !errors.Is(err, errLastStructuredResultError) {
					t.Fatalf("interrupted result did not select failure at EOF: %v; output=%s diagnostics=%s", err, output.String(), diagnostics.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("structured runtime did not close after EOF")
			}
			if laterSuccess && requests.Load() != 2 {
				t.Fatalf("provider requests = %d, want 2", requests.Load())
			}
			if !strings.Contains(output.String(), `"request_id":"interrupt-exit"`) || !strings.Contains(output.String(), `"subtype":"error_during_execution"`) {
				t.Fatalf("interrupt wire lifecycle missing: %s", output.String())
			}
		})
	}
}

func TestStructuredOutputFailurePreventsTurnSideEffects(t *testing.T) {
	workspace := t.TempDir()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	opts := buildTestCLIOptions(t, workspace, agentxHome, "ses_output_failure")
	opts.OutputFormat = cli.OutputStreamJSON
	opts.InputFormat = cli.InputStreamJSON
	want := errors.New("structured stdout disconnected")
	stdout := &failAfterWriter{remaining: 1, err: want}
	input := strings.NewReader(`{"type":"user","uuid":"host-output-failure","message":"must not run"}` + "\n")
	if err := runStructured(t.Context(), opts, workspace, input, stdout, io.Discard); !errors.Is(err, want) {
		t.Fatalf("structured output failure = %v", err)
	}
	transcriptPath := filepath.Join(testSessionDir(agentxHome, workspace, opts.SessionID), "transcript.jsonl")
	if info, err := os.Stat(transcriptPath); err == nil && info.Size() != 0 {
		t.Fatalf("turn persisted after running-state output failure: size=%d", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestStreamSinkDoesNotExposeInternalProviderState(t *testing.T) {
	var output bytes.Buffer
	sink := &streamSink{encoder: surface.NewEncoder(&output), includePartial: true}
	event := protocol.Event{Version: protocol.CurrentVersion, ID: "evt_internal", SessionID: "ses_test", Sequence: 1, Timestamp: time.Now(), Kind: protocol.EventKindSessionMetadata, Visibility: protocol.VisibilityInternal, Persistence: protocol.PersistenceDurable, Origin: protocol.OriginModel, Metadata: &protocol.MetadataEvent{Key: "provider_response_output", Value: json.RawMessage(`{"encrypted_content":"ciphertext"}`)}}
	if err := sink.Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("internal provider state leaked: %s", output.String())
	}
}

func TestFatalStructuredInputDiscardsPreviouslyQueuedWork(t *testing.T) {
	queue := newInputQueue()
	valid := surface.InputEnvelope{Type: "user", UUID: "prompt-before-malformed", Message: json.RawMessage(`"must not run"`)}
	if err := queue.push(valid); err != nil {
		t.Fatal(err)
	}
	want := errors.New("malformed record after queued prompt")
	queue.close(want)
	if _, err := queue.next(t.Context()); !errors.Is(err, want) {
		t.Fatalf("next after fatal input = %v", err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.items) != 0 || queue.bytes != 0 {
		t.Fatalf("fatal queue retained work: items=%d bytes=%d", len(queue.items), queue.bytes)
	}
}

func TestFatalStructuredInputClosesDequeuedToActiveRace(t *testing.T) {
	active := &activeTurn{}
	active.abort()
	turnCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if active.set(cancel) {
		t.Fatal("turn became active after fatal protocol failure")
	}
	if !errors.Is(turnCtx.Err(), context.Canceled) {
		t.Fatalf("future turn context was not cancelled: %v", turnCtx.Err())
	}
}

func TestStructuredReaderDiscardsValidRecordQueuedBeforeMalformedRecord(t *testing.T) {
	workspace := t.TempDir()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	opts := buildTestCLIOptions(t, workspace, agentxHome, "ses_malformed_after_valid")
	opts.OutputFormat = cli.OutputStreamJSON
	opts.InputFormat = cli.InputStreamJSON
	session, err := buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()

	queue := newInputQueue()
	broker := surface.NewControlBroker()
	input := strings.NewReader("{\"type\":\"user\",\"uuid\":\"queued-before-bad\",\"message\":\"must not run\"}\n{bad}\n")
	readStructuredInput(t.Context(), input, io.Discard, surface.NewEncoder(io.Discard), broker, queue, &activeTurn{}, session, false)
	if _, err := queue.next(t.Context()); err == nil || !strings.Contains(err.Error(), "malformed NDJSON input") {
		t.Fatalf("queued valid record survived later malformed record: %v", err)
	}
	if session.engine.HasPromptID("queued-before-bad") {
		t.Fatal("discarded queued prompt was accepted by semantic engine")
	}
	transcriptPath := filepath.Join(testSessionDir(agentxHome, workspace, opts.SessionID), "transcript.jsonl")
	if _, err := os.Stat(transcriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded queued prompt materialized transcript: %v", err)
	}
}
