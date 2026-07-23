package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/mcp"
	agenttesting "github.com/greenpau/agentx/pkg/testing"
	"github.com/greenpau/agentx/pkg/tool"
)

func TestApplicationTestingCapabilityCompositionIsEnvironmentGated(t *testing.T) {
	core, err := tool.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	production, err := registryWithTestingCapability(core, []string{"NODE_ENV=production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := production.Resolve(agenttesting.PermissionToolName); ok {
		t.Fatal("production application registry exposed the testing capability")
	}
	testProfile, err := registryWithTestingCapability(core, []string{"NODE_ENV=test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := testProfile.Resolve(agenttesting.PermissionToolName); !ok {
		t.Fatal("test application registry omitted the testing capability")
	}
}

func TestStandaloneMCPHostReusesCorePermissionBoundary(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("hello from MCP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Read","arguments":{"file_path":` + mustJSON(t, path) + `}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"Write","arguments":{"file_path":` + mustJSON(t, filepath.Join(workspace, "created.txt")) + `,"content":"no"}}}`,
	}, "\n") + "\n"
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runMCPServer(context.Background(), cli.Options{MCPServer: true}, workspace, reader, &output, &bytes.Buffer{})
	}()
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if strings.Count(strings.TrimSpace(output.String()), "\n")+1 >= 4 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("MCP host exited before all responses: %v; output=%s", err, output.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for MCP responses: %s", output.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("responses = %d, want 4: %s", len(lines), output.String())
	}
	responses := make(map[int]string, len(lines))
	for _, line := range lines {
		var envelope struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("decode MCP response %q: %v", line, err)
		}
		responses[envelope.ID] = line
	}
	if !strings.Contains(responses[1], `"protocolVersion"`) || !strings.Contains(responses[2], `"tools"`) || !strings.Contains(responses[3], "hello from MCP") {
		t.Fatalf("MCP responses missing initialization/list/read: %s", output.String())
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(responses[2]), &listed); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if len(listed.Result.Tools) == 0 {
		t.Fatal("tools/list returned no core descriptors")
	}
	for _, descriptor := range listed.Result.Tools {
		if err := mcp.ValidateToolSchema(descriptor.InputSchema); err != nil {
			t.Errorf("tools/list schema for %s is invalid: %v", descriptor.Name, err)
		}
	}
	if !strings.Contains(responses[4], `"isError":true`) || !strings.Contains(responses[4], "permission prompt required") {
		t.Fatalf("unapproved mutation did not fail closed: %s", responses[4])
	}
	if _, err := os.Stat(filepath.Join(workspace, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied MCP mutation changed disk: %v", err)
	}
}

func TestStandaloneMCPHostProtectsSelectedCredentialFileEvenInBypassMode(t *testing.T) {
	workspace := t.TempDir()
	credentialName := "private-provider.settings"
	credentialPath := filepath.Join(workspace, credentialName)
	const secret = "synthetic-standalone-provider-secret"
	if err := os.WriteFile(credentialPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	done := make(chan error, 1)
	go func() {
		done <- runMCPServer(context.Background(), cli.Options{
			MCPServer: true, DangerouslyBypass: true, EnvFile: credentialName,
		}, workspace, reader, &output, &bytes.Buffer{})
	}()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"Read","arguments":{"file_path":` + mustJSON(t, credentialPath) + `}}}` + "\n"
	if _, err := io.WriteString(writer, request); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for strings.TrimSpace(output.String()) == "" {
		select {
		case err := <-done:
			t.Fatalf("MCP host exited before protected-path response: %v", err)
		case <-deadline.C:
			t.Fatal("timed out waiting for protected-path response")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	response := output.String()
	if strings.Contains(response, secret) || !strings.Contains(response, `"isError":true`) || !strings.Contains(response, "permission prompt required") {
		t.Fatalf("selected credential path escaped MCP host protection: %s", response)
	}
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.String()
}

func TestStandaloneMCPHostProcessesCancellationWhileCallRuns(t *testing.T) {
	reader, writer := io.Pipe()
	var output bytes.Buffer
	started := make(chan struct{})
	handler := func(ctx context.Context, request mcpRPCRequest) (any, int, string) {
		if request.Method != "tools/call" {
			return map[string]any{}, 0, ""
		}
		close(started)
		<-ctx.Done()
		return nil, -32603, "handler observed cancellation"
	}
	done := make(chan error, 1)
	go func() { done <- serveMCPProtocol(t.Context(), reader, &output, handler) }()
	if _, err := io.WriteString(writer, `{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("MCP tool handler did not start")
	}
	startedAt := time.Now()
	if _, err := io.WriteString(writer, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow","reason":"test"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP cancellation did not settle the in-flight call")
	}
	if time.Since(startedAt) > time.Second || !strings.Contains(output.String(), `"code":-32800`) || !strings.Contains(output.String(), `"id":"slow"`) {
		t.Fatalf("cancellation response = %q after %s", output.String(), time.Since(startedAt))
	}
}

func TestStandaloneMCPHostContainsInputReaderFailure(t *testing.T) {
	var errorCalls, isCalls, unwrapCalls atomic.Int32
	var handlerCalled atomic.Bool
	hostile := hostileInputError{
		values:      []string{"uncomparable"},
		errorCalls:  &errorCalls,
		isCalls:     &isCalls,
		unwrapCalls: &unwrapCalls,
	}
	err := serveMCPProtocol(
		t.Context(),
		failingInputReader{err: hostile},
		io.Discard,
		func(context.Context, mcpRPCRequest) (any, int, string) {
			handlerCalled.Store(true)
			return nil, 0, ""
		},
	)
	if !errors.Is(err, errInputReaderFailed) {
		t.Fatalf("MCP input error = %v, want fixed reader failure", err)
	}
	if errorCalls.Load() != 0 || isCalls.Load() != 0 || unwrapCalls.Load() != 0 {
		t.Fatalf(
			"hostile MCP input callbacks = Error:%d Is:%d Unwrap:%d",
			errorCalls.Load(), isCalls.Load(), unwrapCalls.Load(),
		)
	}
	if handlerCalled.Load() {
		t.Fatal("hostile input reached the MCP handler")
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
