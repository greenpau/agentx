package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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
	"reflect"
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
	Version   int                    `json:"version"`
	Providers []testAuthFileProvider `json:"providers"`
}

type testAuthFileProvider struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	Default      bool                     `json:"default,omitempty"`
	Capabilities testAuthFileCapabilities `json:"capabilities"`
	AzureOpenAI  testAzureOpenAIAuthFile  `json:"azure_openai"`
}

type testAuthFileCapabilities struct {
	Reasoning testAuthFileReasoning `json:"reasoning"`
}

type testAuthFileReasoning struct {
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
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

func testProviderContextForServers(t *testing.T, servers ...*httptest.Server) context.Context {
	t.Helper()
	if len(servers) == 0 {
		t.Fatal("at least one TLS provider test server is required")
	}
	roots := x509.NewCertPool()
	for index, server := range servers {
		if server == nil || server.Certificate() == nil {
			t.Fatalf("TLS provider test server %d is unavailable", index)
		}
		roots.AddCert(server.Certificate())
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return context.WithValue(t.Context(), modelHTTPClientContextKey{}, &http.Client{Transport: transport})
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
	return writeTestAuthRegistry(t, agentxHome, []testAuthFileProvider{{
		ID: "test-provider", Type: "azure_openai", Default: true,
		Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{
			Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}, DefaultEffort: "high",
		}},
		AzureOpenAI: testAzureOpenAIAuthFile{
			Endpoint: endpoint, Model: model, Deployment: deployment,
			APIKey: apiKey, APIVersion: apiVersion,
		},
	}})
}

func writeTestAuthRegistry(t *testing.T, agentxHome string, providers []testAuthFileProvider) string {
	t.Helper()
	if err := os.MkdirAll(agentxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	document := testAuthFileDocument{Version: 2, Providers: providers}
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

func TestHeadlessSelectsExactConfiguredProviderEndpointAndCapabilities(t *testing.T) {
	const (
		solKey     = "opaque-sol-credential"
		terraKey   = "opaque-terra-credential"
		solModel   = "gpt-5.6-sol"
		terraModel = "gpt-5.6-terra"
	)
	type observedRequest struct {
		path, key, deployment, effort string
	}
	var (
		observedMu sync.Mutex
		observed   []observedRequest
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		observedMu.Lock()
		observed = append(observed, observedRequest{
			path: request.URL.Path, key: request.Header.Get("api-key"),
			deployment: body.Model, effort: body.Reasoning.Effort,
		})
		observedMu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_provider\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_provider\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_provider\",\"output_index\":0,\"content_index\":0,\"delta\":\"selected\"}\n\n")
		fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_provider\",\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_provider\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"selected\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", body.Model)
	}))
	defer server.Close()

	providers := []testAuthFileProvider{
		{
			ID: "sol-east", Type: "azure_openai", Default: true,
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"high", "xhigh"}, DefaultEffort: "high"}},
			AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: server.URL + "/sol", Model: solModel, Deployment: "sol-wire", APIKey: solKey, APIVersion: "preview"},
		},
		{
			ID: "terra-west", Type: "azure_openai",
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"medium", "high"}, DefaultEffort: "medium"}},
			AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: server.URL + "/terra", Model: terraModel, Deployment: "terra-wire", APIKey: terraKey, APIVersion: "preview"},
		},
	}
	home := filepath.Join(t.TempDir(), "agentx-home")
	writeTestAuthRegistry(t, home, providers)
	t.Setenv("AGENTX_HOME", home)
	workspace := t.TempDir()
	base := []string{"--print", "--bare", "--no-session-persistence", "--cwd", workspace}
	providerContext := testProviderContext(t, server)
	for _, invocation := range []struct {
		args []string
	}{
		{args: append(append([]string(nil), base...), "default provider")},
		{args: append(append([]string(nil), base...), "--provider", "terra-west", "--model", terraModel, "--effort", "high", "explicit provider")},
	} {
		var output, diagnostics bytes.Buffer
		if err := Run(providerContext, invocation.args, strings.NewReader(""), &output, &diagnostics); err != nil {
			t.Fatalf("Run(%v): %v diagnostics=%q", invocation.args, err, diagnostics.String())
		}
		if output.String() != "selected\n" {
			t.Fatalf("Run(%v) output = %q", invocation.args, output.String())
		}
	}
	providers[0].Default = false
	writeTestAuthRegistry(t, home, providers)
	var missingDefaultOutput, missingDefaultDiagnostics bytes.Buffer
	missingDefaultErr := Run(providerContext, append(append([]string(nil), base...), "must not call a provider"), strings.NewReader(""), &missingDefaultOutput, &missingDefaultDiagnostics)
	if missingDefaultErr == nil || !strings.Contains(missingDefaultErr.Error(), `"default": true`) || !strings.Contains(missingDefaultErr.Error(), "--provider <id>") {
		t.Fatalf("missing-default error = %v", missingDefaultErr)
	}
	if missingDefaultOutput.Len() != 0 || strings.Contains(missingDefaultDiagnostics.String(), solKey) || strings.Contains(missingDefaultDiagnostics.String(), terraKey) {
		t.Fatalf("missing-default output was unsafe: stdout=%q stderr=%q", missingDefaultOutput.String(), missingDefaultDiagnostics.String())
	}

	observedMu.Lock()
	defer observedMu.Unlock()
	want := []observedRequest{
		{path: "/sol/openai/v1/responses", key: solKey, deployment: "sol-wire", effort: "high"},
		{path: "/terra/openai/v1/responses", key: terraKey, deployment: "terra-wire", effort: "high"},
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("provider requests = %#v, want %#v", observed, want)
	}
}

func TestHeadlessSelectedProviderFailureNeverFallsBackAcrossRegistry(t *testing.T) {
	const (
		defaultID        = "sol-default"
		nondefaultID     = "terra-explicit"
		defaultModel     = "gpt-5.6-sol"
		nondefaultModel  = "gpt-5.6-terra"
		defaultKey       = "opaque-default-provider-credential"
		nondefaultKey    = "opaque-nondefault-provider-credential"
		defaultWireModel = "sol-default-wire-deployment"
		otherWireModel   = "terra-explicit-wire-deployment"
		apiVersion       = "2026-07-01-preview"
		failureCode      = "terminal_selected_failure"
		fallbackText     = "fallback-was-used"
	)
	tests := []struct {
		name             string
		failureDefault   bool
		explicitProvider string
	}{
		{name: "declared default fails", failureDefault: true},
		{name: "explicit nondefault fails", explicitProvider: nondefaultID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var defaultRequests, nondefaultRequests atomic.Int32
			assertRequest := func(
				t *testing.T,
				request *http.Request,
				wantKey, wantDeployment, wantEffort string,
			) {
				t.Helper()
				if request.Method != http.MethodPost || request.URL.Path != "/openai/responses" ||
					request.URL.Query().Get("api-version") != apiVersion {
					t.Errorf("selected provider route = %s %s with version-present=%t", request.Method, request.URL.Path, request.URL.Query().Has("api-version"))
				}
				if request.Header.Get("api-key") != wantKey || request.Header.Get("Authorization") != "" {
					t.Error("selected provider request did not use its isolated registry credential")
				}
				var body struct {
					Model     string `json:"model"`
					Store     bool   `json:"store"`
					Stream    bool   `json:"stream"`
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode selected provider request: %v", err)
					return
				}
				if body.Model != wantDeployment || body.Store || !body.Stream || body.Reasoning.Effort != wantEffort {
					t.Errorf("selected provider request body used the wrong deployment, stream policy, or effort")
				}
			}
			writeFailure := func(writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("x-request-id", "req-selected-terminal")
				writer.Header().Set("x-should-retry", "false")
				writer.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(writer, `{"error":{"code":%q,"message":"selected provider terminated","type":"server_error"}}`, failureCode)
			}
			writeSuccess := func(writer http.ResponseWriter, deployment string) {
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fallback\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
				fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_fallback\",\"output_index\":0,\"content_index\":0,\"delta\":\"fallback-was-used\"}\n\n")
				fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fallback\",\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_fallback\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", deployment, fallbackText)
			}

			defaultServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				defaultRequests.Add(1)
				assertRequest(t, request, defaultKey, defaultWireModel, "high")
				if test.failureDefault {
					writeFailure(writer)
					return
				}
				writeSuccess(writer, defaultWireModel)
			}))
			defer defaultServer.Close()
			nondefaultServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				nondefaultRequests.Add(1)
				assertRequest(t, request, nondefaultKey, otherWireModel, "medium")
				if !test.failureDefault {
					writeFailure(writer)
					return
				}
				writeSuccess(writer, otherWireModel)
			}))
			defer nondefaultServer.Close()

			home := filepath.Join(t.TempDir(), "agentx-home")
			writeTestAuthRegistry(t, home, []testAuthFileProvider{
				{
					ID: defaultID, Type: "azure_openai", Default: true,
					Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"high"}, DefaultEffort: "high"}},
					AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: defaultServer.URL, Model: defaultModel, Deployment: defaultWireModel, APIKey: defaultKey, APIVersion: apiVersion},
				},
				{
					ID: nondefaultID, Type: "azure_openai",
					Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"medium"}, DefaultEffort: "medium"}},
					AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: nondefaultServer.URL, Model: nondefaultModel, Deployment: otherWireModel, APIKey: nondefaultKey, APIVersion: apiVersion},
				},
			})
			t.Setenv("AGENTX_HOME", home)
			args := []string{
				"--print", "--bare", "--no-session-persistence", "--output-format", "json",
				"--cwd", t.TempDir(),
			}
			if test.explicitProvider != "" {
				args = append(args, "--provider", test.explicitProvider)
			}
			args = append(args, "selected route must terminate")

			var output, diagnostics bytes.Buffer
			err := Run(
				testProviderContextForServers(t, defaultServer, nondefaultServer),
				args,
				strings.NewReader(""),
				&output,
				&diagnostics,
			)
			if err == nil {
				t.Fatalf("selected provider failure = %v, want terminal provider error", err)
			}
			var result map[string]any
			if decodeErr := json.Unmarshal(output.Bytes(), &result); decodeErr != nil {
				t.Fatalf("decode aggregate terminal result: %v; output=%q", decodeErr, output.String())
			}
			if result["type"] != "result" || result["subtype"] != "error_during_execution" ||
				result["is_error"] != true || result["stop_reason"] != "provider_error" ||
				result["num_turns"] != float64(1) {
				t.Fatalf("aggregate terminal result = %#v", result)
			}
			if _, exists := result["result"]; exists {
				t.Fatalf("failed selected provider produced success result: %#v", result)
			}
			if projected, ok := result["errors"].([]any); !ok || len(projected) != 1 {
				t.Fatalf("failed selected provider omitted its sealed terminal error: %#v", result)
			}
			combined := err.Error() + output.String() + diagnostics.String()
			if strings.Contains(combined, fallbackText) {
				t.Fatalf("unselected provider fallback became observable: %s", combined)
			}
			for _, privateValue := range []string{
				defaultKey, nondefaultKey, defaultServer.URL, nondefaultServer.URL,
				defaultWireModel, otherWireModel, apiVersion,
			} {
				if strings.Contains(combined, privateValue) {
					t.Fatal("provider credential or private route metadata leaked through terminal failure")
				}
			}
			if test.failureDefault {
				if defaultRequests.Load() != 1 || nondefaultRequests.Load() != 0 {
					t.Fatalf("default failure requests = default:%d nondefault:%d, want 1:0", defaultRequests.Load(), nondefaultRequests.Load())
				}
			} else if defaultRequests.Load() != 0 || nondefaultRequests.Load() != 1 {
				t.Fatalf("explicit failure requests = default:%d nondefault:%d, want 0:1", defaultRequests.Load(), nondefaultRequests.Load())
			}
		})
	}
}

func TestHeadlessConcurrentExplicitProvidersRemainRouteIsolated(t *testing.T) {
	const apiVersion = "2026-07-01-preview"
	type providerCase struct {
		id, model, deployment, key, effort, output string
		requests                                   atomic.Int32
		server                                     *httptest.Server
		workspace                                  string
	}
	providers := []*providerCase{
		{
			id: "sol-concurrent", model: "gpt-5.6-sol", deployment: "sol-concurrent-wire",
			key: "opaque-sol-concurrent-credential", effort: "xhigh", output: "sol-isolated",
		},
		{
			id: "terra-concurrent", model: "gpt-5.6-terra", deployment: "terra-concurrent-wire",
			key: "opaque-terra-concurrent-credential", effort: "low", output: "terra-isolated",
		},
	}
	arrived := make(chan string, len(providers))
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	writeCompletion := func(writer http.ResponseWriter, provider *providerCase) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"status\":\"in_progress\",\"output\":[]}}\n\n", "resp_"+provider.id)
		fmt.Fprintf(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":%q,\"status\":\"in_progress\",\"output\":[]}}\n\n", "resp_"+provider.id)
		fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":%q,\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", "msg_"+provider.id, provider.output)
		fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":%q,\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", "resp_"+provider.id, provider.deployment, "msg_"+provider.id, provider.output)
	}
	for _, provider := range providers {
		provider := provider
		provider.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			provider.requests.Add(1)
			if request.Method != http.MethodPost || request.URL.Path != "/openai/responses" ||
				request.URL.Query().Get("api-version") != apiVersion {
				t.Errorf("provider %s used an unexpected method or route", provider.id)
			}
			if request.Header.Get("api-key") != provider.key || request.Header.Get("Authorization") != "" {
				t.Errorf("provider %s used another profile's credential", provider.id)
			}
			var body struct {
				Model     string `json:"model"`
				Reasoning struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode provider %s request: %v", provider.id, err)
				return
			}
			if body.Model != provider.deployment || body.Reasoning.Effort != provider.effort {
				t.Errorf("provider %s used another profile's deployment or effort", provider.id)
			}
			select {
			case arrived <- provider.id:
			case <-request.Context().Done():
				return
			}
			select {
			case <-release:
			case <-request.Context().Done():
				return
			}
			writeCompletion(writer, provider)
		}))
		defer provider.server.Close()
		provider.workspace = t.TempDir()
	}

	home := filepath.Join(t.TempDir(), "agentx-home")
	writeTestAuthRegistry(t, home, []testAuthFileProvider{
		{
			ID: providers[0].id, Type: "azure_openai", Default: true,
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"high", providers[0].effort}, DefaultEffort: "high"}},
			AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: providers[0].server.URL, Model: providers[0].model, Deployment: providers[0].deployment, APIKey: providers[0].key, APIVersion: apiVersion},
		},
		{
			ID: providers[1].id, Type: "azure_openai",
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{Efforts: []string{"none", providers[1].effort}, DefaultEffort: "none"}},
			AzureOpenAI:  testAzureOpenAIAuthFile{Endpoint: providers[1].server.URL, Model: providers[1].model, Deployment: providers[1].deployment, APIKey: providers[1].key, APIVersion: apiVersion},
		},
	})
	t.Setenv("AGENTX_HOME", home)
	providerContext := testProviderContextForServers(t, providers[0].server, providers[1].server)
	type runResult struct {
		provider    *providerCase
		output      string
		diagnostics string
		err         error
	}
	results := make(chan runResult, len(providers))
	for _, provider := range providers {
		provider := provider
		go func() {
			var output, diagnostics bytes.Buffer
			err := Run(providerContext, []string{
				"--print", "--bare", "--no-session-persistence",
				"--provider", provider.id, "--model", provider.model, "--effort", provider.effort,
				"--cwd", provider.workspace, "concurrent isolated route",
			}, strings.NewReader(""), &output, &diagnostics)
			results <- runResult{provider: provider, output: output.String(), diagnostics: diagnostics.String(), err: err}
		}()
	}

	seen := make(map[string]bool, len(providers))
	for range providers {
		select {
		case id := <-arrived:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent provider requests did not both reach their selected routes")
		}
	}
	for _, provider := range providers {
		if !seen[provider.id] {
			t.Fatalf("provider %s did not reach its selected route", provider.id)
		}
	}
	releaseAll()
	for range providers {
		select {
		case result := <-results:
			if result.err != nil || result.output != result.provider.output+"\n" || result.diagnostics != "" {
				t.Fatalf("provider %s result = output:%q diagnostics:%q error:%v", result.provider.id, result.output, result.diagnostics, result.err)
			}
			combined := result.output + result.diagnostics
			for _, provider := range providers {
				if strings.Contains(combined, provider.key) {
					t.Fatal("concurrent provider credential leaked")
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent provider invocation did not settle")
		}
	}
	for _, provider := range providers {
		if provider.requests.Load() != 1 {
			t.Fatalf("provider %s requests = %d, want exactly one", provider.id, provider.requests.Load())
		}
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
