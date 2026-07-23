package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if len(request.ID) == 0 {
			if request.Method == "notifications/initialized" && os.Getenv("GO_MCP_STOP_READING") == "1" {
				time.Sleep(5 * time.Second)
			}
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}, "resources": map[string]any{}, "prompts": map[string]any{}},
				"serverInfo":      map[string]string{"name": "test-server", "version": "1"},
				"instructions":    "test instructions",
			}
		case "tools/list":
			if os.Getenv("GO_MCP_LIST_EARLY_ERROR") == "1" {
				var params map[string]any
				_ = json.Unmarshal(request.Params, &params)
				if params["cursor"] != nil {
					response["error"] = map[string]any{"code": -32001, "message": "second page failed"}
				} else {
					response["result"] = map[string]any{
						"tools":      []any{map[string]any{"name": "bad name", "inputSchema": map[string]any{"type": "object"}}},
						"nextCursor": "second",
					}
				}
				break
			}
			response["result"] = map[string]any{"tools": []any{
				map[string]any{"name": "echo", "description": "echo input", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
				map[string]any{"name": "bad name", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Name == "exit" {
				_ = os.Stdout.Close()
				continue
			}
			if params.Name == "slow" {
				time.Sleep(time.Second)
			}
			if params.Name == "rpc_error" {
				response["error"] = map[string]any{
					"code": -32000, "message": os.Getenv("GO_MCP_REFLECT_SECRET"),
					"data": map[string]any{"detail": os.Getenv("GO_MCP_REFLECT_SECRET")},
				}
				break
			}
			text := fmt.Sprint(params.Arguments["text"])
			if params.Name == "reflect_credential" {
				text = os.Getenv("GO_MCP_REFLECT_SECRET")
			}
			if params.Name == "cwd" {
				text, _ = os.Getwd()
			}
			if params.Name == "invalid_type" {
				response["result"] = map[string]any{
					"content": []any{map[string]any{"type": "bad"}},
				}
				break
			}
			response["result"] = map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": text}},
				"structuredContent": map[string]any{"received": params.Name},
			}
		case "resources/list":
			response["result"] = map[string]any{"resources": []any{map[string]any{"uri": "test://resource/one", "name": "one", "mimeType": "text/plain"}}}
		case "resources/templates/list":
			response["result"] = map[string]any{"resourceTemplates": []any{map[string]any{"uriTemplate": "test://resource/{id}", "name": "by-id"}}}
		case "resources/read":
			response["result"] = map[string]any{"contents": []any{map[string]any{"uri": "test://resource/one", "mimeType": "text/plain", "text": "resource body"}}}
		case "prompts/list":
			response["result"] = map[string]any{"prompts": []any{map[string]any{"name": "review", "description": "review code", "arguments": []any{map[string]any{"name": "target", "required": true}}}}}
		case "prompts/get":
			if os.Getenv("GO_MCP_INVALID_PROMPT_TYPE") == "1" {
				response["result"] = map[string]any{
					"description": "review",
					"messages":    []any{map[string]any{"role": "user", "content": map[string]any{"type": "bad"}}},
				}
				break
			}
			response["result"] = map[string]any{"description": "review", "messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "review this"}}}}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "not found"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

type shortWriter struct {
	data []byte
}

func (writer *shortWriter) Write(data []byte) (int, error) {
	if len(data) > 2 {
		data = data[:2]
	}
	writer.data = append(writer.data, data...)
	return len(data), nil
}

func TestWriteFullHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{}
	if err := writeFull(writer, []byte("complete")); err != nil {
		t.Fatal(err)
	}
	if string(writer.data) != "complete" {
		t.Fatalf("short write lost bytes: %q", writer.data)
	}
}

func TestCleanProtocolEOFSettlesPendingRequest(t *testing.T) {
	client, err := NewClient(helperConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.CallTool(context.Background(), "exit", map[string]any{})
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("clean EOF did not settle request promptly: err=%v duration=%s", err, time.Since(started))
	}
	if state := client.State(); state != StateFailed && state != StateClosed {
		t.Fatalf("clean EOF left nonterminal state %s", state)
	}
}

func helperConfig() Config {
	return Config{
		Name: "test", Transport: TransportStdio, Command: os.Args[0],
		Args: []string{"-test.run=TestMCPHelperProcess"}, Env: map[string]string{"GO_WANT_MCP_HELPER": "1"},
		Scope: ScopeUser, Approved: true, ConnectTimeout: 10 * time.Second,
		RequestTimeout: time.Second, ToolTimeout: time.Second,
	}
}

func TestStdioClientUsesConfiguredWorkingDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	config := helperConfig()
	config.WorkingDirectory = workingDirectory
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(t.Context(), "cwd", map[string]any{})
	if err != nil || len(result.Content) != 1 {
		t.Fatalf("cwd call = %#v, %v", result, err)
	}
	got, err := filepath.EvalSymlinks(result.Content[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("MCP child cwd = %q, want %q", got, want)
	}
}

func TestStdioClientRejectsCredentialBearingProviderEnvelopeBeforePublicReturn(t *testing.T) {
	const secret = "synthetic-direct-mcp-response-credential"
	for _, method := range []string{"reflect_credential", "rpc_error"} {
		t.Run(method, func(t *testing.T) {
			config := helperConfig()
			config.Env["GO_MCP_REFLECT_SECRET"] = secret
			client, err := NewClient(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			result, err := client.CallTool(t.Context(), method, map[string]any{})
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("credential-bearing provider response = %#v, %v", result, err)
			}
			for _, rendered := range []string{
				err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
			} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("public MCP error exposed provider credential: %q", rendered)
				}
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("public MCP result exposed provider credential: %s", encoded)
			}
		})
	}
}

func TestStdioClientSuppressesAccumulatedDiagnosticsOnLaterListFailure(t *testing.T) {
	const secret = `tool "bad name" omitted: invalid descriptor`
	config := helperConfig()
	config.Env["GO_MCP_LIST_EARLY_ERROR"] = "1"
	config.Env["MCP_TOKEN"] = secret
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tools, diagnostics, err := client.ListTools(t.Context())
	if err == nil || tools != nil || diagnostics != nil {
		t.Fatalf("failed multipage list published partial state: tools=%#v diagnostics=%#v err=%v", tools, diagnostics, err)
	}
	for _, rendered := range []string{
		err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("early list failure exposed accumulated credential diagnostic: %q", rendered)
		}
	}
}

func TestStdioClientUsesFixedPublicContentValidationErrors(t *testing.T) {
	const secret = `unsupported content block type "bad"`
	for _, test := range []struct {
		name   string
		mutate func(*Config)
		call   func(*Client) error
	}{
		{
			name: "tool result",
			call: func(client *Client) error {
				_, err := client.CallTool(t.Context(), "invalid_type", map[string]any{})
				return err
			},
		},
		{
			name: "prompt result",
			mutate: func(config *Config) {
				config.Env["GO_MCP_INVALID_PROMPT_TYPE"] = "1"
			},
			call: func(client *Client) error {
				_, err := client.GetPrompt(t.Context(), "review", map[string]string{})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := helperConfig()
			config.Env["MCP_TOKEN"] = secret
			if test.mutate != nil {
				test.mutate(&config)
			}
			client, err := NewClient(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = test.call(client)
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("invalid provider content error = %v", err)
			}
			for _, rendered := range []string{
				err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
			} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("content validation exposed provider-controlled type: %q", rendered)
				}
			}
		})
	}
}

func TestStdioClientProcessStartFailureDoesNotExposeCredentialBearingPaths(t *testing.T) {
	const secret = "synthetic-mcp-start-path-credential"
	for name, mutate := range map[string]func(*Config){
		"command": func(config *Config) {
			config.Command = filepath.Join(t.TempDir(), secret)
			config.Args = nil
		},
		"working directory": func(config *Config) {
			config.Command = "./missing-command"
			config.Args = nil
			config.WorkingDirectory = filepath.Join(t.TempDir(), secret)
			if err := os.MkdirAll(config.WorkingDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := helperConfig()
			config.Env["MCP_TOKEN"] = secret
			mutate(&config)
			client, err := NewClient(config)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Connect(t.Context())
			if err == nil {
				t.Fatal("invalid MCP process unexpectedly started")
			}
			for _, rendered := range []string{
				err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
			} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("MCP process-start error exposed credential-bearing path: %q", rendered)
				}
			}
		})
	}
}

func TestRPCErrorDebugFormattingOmitsProviderMessageAndData(t *testing.T) {
	const secret = "synthetic-rpc-error-credential"
	err := &RPCError{
		Code: -32000, Message: secret,
		Data: json.RawMessage(`{"credential":"` + secret + `"}`),
	}
	for _, rendered := range []string{
		err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("RPC error formatting exposed provider payload: %q", rendered)
		}
	}
}

func TestStdioBlockedWriteHonorsCancellation(t *testing.T) {
	config := helperConfig()
	config.Env["GO_MCP_STOP_READING"] = "1"
	config.MaxMessageBytes = DefaultMaxMessageBytes
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.CallTool(ctx, "blocked", map[string]any{"text": strings.Repeat("x", 512<<10)})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("blocked MCP write ignored cancellation: err=%v duration=%s", err, time.Since(started))
	}
}

func TestStdioClientRejectsRenamedAmbientModelCredentialBeforeSpawn(t *testing.T) {
	const secret = "synthetic-host-model-credential"
	t.Setenv("AZURE_OPENAI_SUBSCRIPTION_KEY", secret)
	config := helperConfig()
	config.Env["RENAMED_CREDENTIAL"] = "prefix-" + secret + "-suffix"
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(t.Context())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-alias rejection = %v", err)
	}
	if client.cmd != nil || client.State() != StateFailed || client.LastError() != "invalid child environment" {
		t.Fatalf("MCP child started despite rejected environment: state=%s cmd=%#v diagnostic=%q", client.State(), client.cmd, client.LastError())
	}
}

func TestStdioClientDiscoveryCallsCancellationAndReconnect(t *testing.T) {
	client, err := NewClient(helperConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.State() != StateConnected || client.InitializeResult().ServerInfo.Name != "test-server" {
		t.Fatalf("client did not initialize: state=%s result=%#v", client.State(), client.InitializeResult())
	}
	tools, diagnostics, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" || len(diagnostics) != 1 {
		t.Fatalf("tool discovery isolation failed: tools=%#v diagnostics=%#v", tools, diagnostics)
	}
	namespaced, _, err := client.NamespacedTools(context.Background())
	if err != nil || len(namespaced) != 1 || namespaced[0].Name != "mcp__test__echo" {
		t.Fatalf("tool namespacing failed: %#v, %v", namespaced, err)
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "hello" || result.IsError {
		t.Fatalf("tool call failed: %#v, %v", result, err)
	}
	resources, _, err := client.ListResources(context.Background())
	if err != nil || len(resources) != 1 || resources[0].URI != "test://resource/one" {
		t.Fatalf("resources = %#v, %v", resources, err)
	}
	templates, _, err := client.ListResourceTemplates(context.Background())
	if err != nil || len(templates) != 1 {
		t.Fatalf("templates = %#v, %v", templates, err)
	}
	resource, err := client.ReadResource(context.Background(), resources[0].URI)
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != "resource body" {
		t.Fatalf("resource read = %#v, %v", resource, err)
	}
	prompts, _, err := client.ListPrompts(context.Background())
	if err != nil || len(prompts) != 1 || prompts[0].Name != "review" {
		t.Fatalf("prompts = %#v, %v", prompts, err)
	}
	prompt, err := client.GetPrompt(context.Background(), "review", map[string]string{"target": "x"})
	if err != nil || len(prompt.Messages) != 1 || prompt.Messages[0].Content.Text != "review this" {
		t.Fatalf("prompt = %#v, %v", prompt, err)
	}

	_, _, advertisedVersion, err := client.ListToolsBound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reconnect(context.Background()); err != nil || client.Generation() != 2 {
		t.Fatalf("reconnect failed: generation=%d state=%s err=%v", client.Generation(), client.State(), err)
	}
	if _, err := client.CallToolBound(context.Background(), advertisedVersion, "echo", map[string]any{"text": "stale"}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("stale advertised generation call error = %v", err)
	}
	_, _, currentVersion, err := client.ListToolsBound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := client.CallToolBound(context.Background(), currentVersion, "echo", map[string]any{"text": "current"})
	if err != nil || len(current.Content) != 1 || current.Content[0].Text != "current" {
		t.Fatalf("current advertised generation call = %#v, %v", current, err)
	}
	cancelContext, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.CallTool(cancelContext, "slow", map[string]any{"text": "late"})
	if err == nil || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("cancelled request was not bounded: err=%v duration=%s", err, time.Since(started))
	}
}

func TestStaleGenerationCannotSettleCurrentRequestOrInvalidateCaches(t *testing.T) {
	client := newClientFromValidated(helperConfig())
	pending := &pendingCall{response: make(chan pendingResponse, 1), method: "tools/list", generation: 2}
	client.mu.Lock()
	client.generation = 2
	client.cmd = &exec.Cmd{}
	client.pending[1] = pending
	client.mu.Unlock()
	client.cacheMu.Lock()
	client.tools = []ToolDescriptor{{Name: "current"}}
	client.cacheMu.Unlock()

	if err := client.handleMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), 1); err != nil {
		t.Fatal(err)
	}
	if err := client.handleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-pending.response:
		t.Fatalf("stale generation settled current request: %#v", response)
	default:
	}
	client.mu.RLock()
	_, retainedPending := client.pending[1]
	client.mu.RUnlock()
	client.cacheMu.RLock()
	retainedTools := len(client.tools)
	client.cacheMu.RUnlock()
	if !retainedPending || retainedTools != 1 {
		t.Fatalf("stale generation mutated current state: pending=%v tools=%d", retainedPending, retainedTools)
	}
}

func TestInitializationCannotOverwriteConcurrentProcessFailure(t *testing.T) {
	client := newClientFromValidated(Config{})
	client.mu.Lock()
	client.generation = 7
	client.cmd = &exec.Cmd{}
	client.state = StateFailed
	client.lastError = "MCP server process exited"
	client.mu.Unlock()
	err := client.completeInitialization(7, InitializeResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo:      ServerInfo{Name: "server", Version: "1"},
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("concurrent initialization completion = %v", err)
	}
	if state := client.State(); state != StateFailed {
		t.Fatalf("process failure was overwritten with %s", state)
	}
}

func TestListChangedEpochPreventsInflightResponseFromRepopulatingCache(t *testing.T) {
	client := newClientFromValidated(helperConfig())
	client.mu.Lock()
	client.generation = 1
	client.cmd = &exec.Cmd{}
	client.mu.Unlock()

	client.cacheMu.RLock()
	toolsEpoch := client.toolsEpoch
	resourcesEpoch := client.resourcesEpoch
	promptsEpoch := client.promptsEpoch
	client.cacheMu.RUnlock()

	client.handleNotification(1, "notifications/tools/list_changed")
	client.handleNotification(1, "notifications/resources/list_changed")
	client.handleNotification(1, "notifications/prompts/list_changed")

	if client.cacheToolsIfCurrent(toolsEpoch, []ToolDescriptor{{Name: "stale"}}) {
		t.Fatal("stale tools response repopulated invalidated cache")
	}
	if client.cacheResourcesIfCurrent(resourcesEpoch, []ResourceDescriptor{{Name: "stale"}}) {
		t.Fatal("stale resources response repopulated invalidated cache")
	}
	if client.cacheResourceTemplatesIfCurrent(resourcesEpoch, []ResourceTemplate{{Name: "stale"}}) {
		t.Fatal("stale resource-template response repopulated invalidated cache")
	}
	if client.cachePromptsIfCurrent(promptsEpoch, []PromptDescriptor{{Name: "stale"}}) {
		t.Fatal("stale prompts response repopulated invalidated cache")
	}

	client.cacheMu.RLock()
	defer client.cacheMu.RUnlock()
	if client.tools != nil || client.resources != nil || client.resourceTemplates != nil || client.prompts != nil {
		t.Fatalf("invalidated caches were repopulated: tools=%#v resources=%#v templates=%#v prompts=%#v", client.tools, client.resources, client.resourceTemplates, client.prompts)
	}
}

func TestValidateInitializeResultRejectsUnsupportedOrUnsafeIdentity(t *testing.T) {
	valid := InitializeResult{ProtocolVersion: ProtocolVersion, ServerInfo: ServerInfo{Name: "server", Version: "1"}}
	if err := validateInitializeResult(valid); err != nil {
		t.Fatalf("valid initialization rejected: %v", err)
	}
	unsupported := valid
	unsupported.ProtocolVersion = "1999-01-01"
	if err := validateInitializeResult(unsupported); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unsupported protocol version accepted: %v", err)
	}
	unsafe := valid
	unsafe.ServerInfo.Name = "server\x1b]52;c;payload\a"
	if err := validateInitializeResult(unsafe); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unsafe server identity accepted: %v", err)
	}
}

func TestMCPPublishedJSONSnapshotsAreDeeplyImmutable(t *testing.T) {
	client := newClientFromValidated(Config{})
	client.initialize = InitializeResult{Capabilities: ServerCapabilities{
		Tools: map[string]any{"nested": map[string]any{"enabled": true}},
	}}
	first := client.InitializeResult()
	first.Capabilities.Tools["nested"].(map[string]any)["enabled"] = false
	second := client.InitializeResult()
	if second.Capabilities.Tools["nested"].(map[string]any)["enabled"] != true {
		t.Fatal("initialize capability snapshot mutated cached state")
	}

	tools := []ToolDescriptor{{Annotations: map[string]any{"nested": map[string]any{"value": "original"}}}}
	cloned := cloneTools(tools)
	cloned[0].Annotations["nested"].(map[string]any)["value"] = "changed"
	if tools[0].Annotations["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("tool annotation snapshot mutated source state")
	}
}

func TestInitializeCapabilitiesEnforceRemoteJSONBounds(t *testing.T) {
	tooDeep := map[string]any{"leaf": true}
	for range 34 {
		tooDeep = map[string]any{"nested": tooDeep}
	}
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo:      ServerInfo{Name: "bounded"},
		Capabilities:    ServerCapabilities{Tools: tooDeep},
	}
	if err := validateInitializeResult(result); !errors.Is(err, ErrProtocol) {
		t.Fatalf("deep initialize capability err=%v", err)
	}
}

func TestConfigValidationUnavailableRemoteAndExpansion(t *testing.T) {
	descriptor, err := ValidateConfig(Config{Name: "remote", Transport: TransportHTTP, URL: "https://example.test/mcp", Scope: ScopeUser, OAuth: &OAuthConfig{AuthServerMetadataURL: "https://auth.example.test/.well-known"}})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Availability.Usable() || descriptor.State != StateDisabled || descriptor.Availability.TransportSupported {
		t.Fatalf("remote transport presented as operational: %#v", descriptor)
	}
	if _, err := NewClient(descriptor.config); err == nil {
		t.Fatal("remote transport constructed a stdio client")
	}
	expanded, err := ExpandEnvironment("${ROOT}/$NAME/$$literal", func(name string) (string, bool) {
		values := map[string]string{"ROOT": "/tmp", "NAME": "server"}
		value, ok := values[name]
		return value, ok
	})
	if err != nil || expanded != "/tmp/server/$literal" {
		t.Fatalf("environment expansion = %q, %v", expanded, err)
	}
	if _, err := ExpandEnvironment("$MISSING", func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("missing environment variable must fail")
	}
	if _, err := ValidateConfig(Config{Name: "bad name", Transport: TransportStdio, Command: "x", Scope: ScopeUser}); err == nil {
		t.Fatal("invalid server name accepted")
	}
	if _, err := ValidateConfig(Config{Name: "ambiguous__server", Transport: TransportStdio, Command: "x", Scope: ScopeUser}); err == nil {
		t.Fatal("namespace-ambiguous server name accepted")
	}
	if _, err := NamespacedToolName("safe", "ambiguous__tool"); err == nil {
		t.Fatal("namespace-ambiguous tool name accepted")
	}
	for _, scope := range []Scope{"", "invented"} {
		if _, err := ValidateConfig(Config{Name: "unscoped", Transport: TransportStdio, Command: "x", Scope: scope}); err == nil {
			t.Fatalf("invalid scope %q was accepted", scope)
		}
	}
	var asserted Config
	if err := json.Unmarshal([]byte(`{"name":"asserted","type":"stdio","command":"x","scope":"user","source_identity":"forged","trusted_source":true,"approved":true}`), &asserted); err != nil {
		t.Fatal(err)
	}
	if asserted.Scope != "" || asserted.SourceID != "" || asserted.Trusted || asserted.Approved {
		t.Fatalf("MCP document asserted discovery-owned provenance: %#v", asserted)
	}
	if _, err := ValidateConfig(asserted); err == nil {
		t.Fatal("MCP document without discovery-owned scope was accepted")
	}
	user, err := ValidateConfig(Config{Name: "derived", Transport: TransportStdio, Command: "x", Scope: ScopeUser})
	if err != nil || user.SourceID != "user:derived" || !user.Availability.Usable() {
		t.Fatalf("ordinary scope attribution = %#v, %v", user, err)
	}
	project, err := ValidateConfig(Config{Name: "project", Transport: TransportStdio, Command: "x", Scope: ScopeProject})
	if err != nil || project.Availability.Usable() || project.Availability.Approved {
		t.Fatalf("unapproved project definition did not fail closed: %#v, %v", project, err)
	}
	if _, err := ValidateConfig(Config{Name: "plugin", Transport: TransportStdio, Command: "x", Scope: ScopePlugin}); err == nil {
		t.Fatal("unattributed plugin definition was accepted")
	}
	plugin, err := ValidateConfig(Config{Name: "plugin", Transport: TransportStdio, Command: "x", Scope: ScopePlugin, SourceID: "plugin:example"})
	if err != nil || plugin.Availability.Usable() || plugin.Availability.SourceTrusted {
		t.Fatalf("plugin definition without owning-adapter trust did not fail closed: %#v, %v", plugin, err)
	}
	plugin, err = ValidateConfig(Config{Name: "plugin", Transport: TransportStdio, Command: "x", Scope: ScopePlugin, SourceID: "plugin:example", Trusted: true})
	if err != nil || !plugin.Availability.Usable() {
		t.Fatalf("attributed trusted plugin definition unavailable: %#v, %v", plugin, err)
	}
}

func TestExpandConfigEnvironmentAndIsolatedFailureDiagnostic(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{"BIN": "/opt/mcp", "ROOT": "/workspace", "MCP_TOKEN": "synthetic-token"}
		value, ok := values[name]
		return value, ok
	}
	expanded, err := ExpandConfigEnvironment(Config{
		Name: "expanded", Transport: TransportStdio, Scope: ScopeUser,
		Command: "$BIN/server", Args: []string{"--root=${ROOT}", "$$literal", "--token=$MCP_TOKEN"},
		Env: map[string]string{"AUTH": "$MCP_TOKEN"},
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if expanded.Command != "/opt/mcp/server" || len(expanded.Args) != 3 || expanded.Args[0] != "--root=/workspace" || expanded.Args[1] != "$literal" || expanded.Args[2] != "--token=synthetic-token" || expanded.Env["AUTH"] != "synthetic-token" {
		t.Fatalf("expanded config = %#v", expanded)
	}
	credentials, err := CredentialLiterals(expanded)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(credentials, "synthetic-token") {
		t.Fatalf("argument-expanded credential was not tracked: %#v", credentials)
	}

	broken := Config{Name: "broken", Transport: TransportStdio, Scope: ScopeUser, Command: "server", ConfigurationError: "expand argument 0: missing MCP environment variable MISSING"}
	valid := Config{Name: "valid", Transport: TransportStdio, Scope: ScopeUser, Command: "server"}
	descriptors, diagnostics := composeDescriptors([]Config{broken, valid})
	if len(descriptors) != 1 || descriptors[0].Name != "valid" {
		t.Fatalf("broken expansion removed healthy sibling: %#v", descriptors)
	}
	if len(diagnostics) != 1 || diagnostics[0].Server != "" || diagnostics[0].Source != "" ||
		diagnostics[0].Message != "MCP configuration expansion failed" {
		t.Fatalf("expansion diagnostic = %#v", diagnostics)
	}
}

func TestExpandEnvironmentRejectsInvalidNamesAndOversizedValues(t *testing.T) {
	lookup := func(string) (string, bool) { return strings.Repeat("x", maximumExpandedConfigValueBytes+1), true }
	if _, err := ExpandEnvironment("${BAD-NAME}", lookup); err == nil {
		t.Fatal("invalid environment name was accepted")
	}
	if _, err := ExpandEnvironment("$VALUE", lookup); err == nil {
		t.Fatal("oversized environment expansion was accepted")
	}
}

func TestValidateConfigBoundsCredentialRedactionWorkload(t *testing.T) {
	tooMany := make(map[string]string, MaxCredentialLiterals+1)
	for index := 0; index <= MaxCredentialLiterals; index++ {
		tooMany[fmt.Sprintf("SECRET_%03d", index)] = fmt.Sprintf("credential-%03d", index)
	}
	if _, err := ValidateConfig(Config{
		Name: "too-many-secrets", Transport: TransportStdio, Command: "server",
		Scope: ScopeUser, Env: tooMany,
	}); err == nil || err.Error() != "MCP credential configuration is invalid" {
		t.Fatalf("oversized credential set validation error = %v", err)
	}
	if _, err := ValidateConfig(Config{
		Name: "too-many-secret-bytes", Transport: TransportStdio, Command: "server",
		Scope: ScopeUser, Env: map[string]string{"MCP_TOKEN": strings.Repeat("x", MaxCredentialLiteralBytes+1)},
	}); err == nil || err.Error() != "MCP credential configuration is invalid" {
		t.Fatalf("oversized credential bytes validation error = %v", err)
	}
}

func TestCredentialLiteralsDistinguishOperationalSettingsFromSecrets(t *testing.T) {
	operational, err := CredentialLiterals(Config{
		Env:     map[string]string{"DEBUG": "1"},
		Headers: map[string]string{"X-Debug": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(operational) != 0 {
		t.Fatalf("operational settings entered credential set: %#v", operational)
	}

	values, err := CredentialLiterals(Config{
		Env: map[string]string{"MCP_TOKEN": "token-sensitive"},
		Headers: map[string]string{
			"Authorization":             "Bearer bearer-sensitive",
			"X-API-Key":                 "api-sensitive",
			"Ocp-Apim-Subscription-Key": "subscription-sensitive",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"token-sensitive",
		"Bearer bearer-sensitive",
		"bearer-sensitive",
		"api-sensitive",
		"subscription-sensitive",
	} {
		if !slices.Contains(values, secret) {
			t.Fatalf("credential %q was not protected: %#v", secret, values)
		}
	}
	short, err := CredentialLiterals(Config{Env: map[string]string{"TOKEN": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(short, "1") {
		t.Fatalf("credential-named short value was not protected: %#v", short)
	}
}

func TestMCPClientRejectsBootstrapIncompatibleCredentialBeforeProcessStart(t *testing.T) {
	config := helperConfig()
	config.Env["MCP_TOKEN"] = "1"
	if _, err := NewClient(config); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("bootstrap-incompatible credential result = %v", err)
	}
}

func TestMCPWriteValidatesExactNewlineFramedRequest(t *testing.T) {
	client := newClientFromValidated(Config{})
	client.credentials = redact.New("}\n")
	err := client.writeJSONGeneration(t.Context(), 0, map[string]any{"safe": true})
	if err == nil || strings.Contains(err.Error(), "}\n") {
		t.Fatalf("newline-framed credential guard = %v", err)
	}
}

func TestCredentialLiteralsRejectControlBearingSecrets(t *testing.T) {
	for _, value := range []string{"line\nbreak", "escape\x1bvalue", "format\u202evalue"} {
		if _, err := CredentialLiterals(Config{Env: map[string]string{"MCP_TOKEN": value}}); err == nil {
			t.Fatalf("control-bearing credential %q was accepted", value)
		}
	}
}

func TestDescriptorSerializationDoesNotExposeCredentialsOrCommandArguments(t *testing.T) {
	descriptor, err := ValidateConfig(Config{
		Name: "secret", Transport: TransportStdio, Command: "server",
		Args: []string{"--token", "never-serialize"}, Env: map[string]string{"API_TOKEN": "never-serialize"},
		Scope: ScopeUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == "null" || bytesContains(encoded, []byte("never-serialize")) || bytesContains(encoded, []byte("semantic_key")) {
		t.Fatalf("descriptor leaked sensitive configuration: %s", encoded)
	}
}

func TestMCPConfigurationOwnersHaveSafeDiagnosticFormatting(t *testing.T) {
	const secret = "mcp-debug-formatting-credential"
	config := helperConfig()
	config.Env["MCP_TOKEN"] = secret
	config.Headers = map[string]string{"Authorization": "Bearer " + secret}
	descriptor, err := ValidateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{Servers: []Descriptor{descriptor}}
	for label, value := range map[string]any{
		"config value":   config,
		"config pointer": &config,
		"descriptor":     descriptor,
		"client":         client,
		"snapshot":       snapshot,
	} {
		for _, rendered := range []string{
			fmt.Sprintf("%v", value),
			fmt.Sprintf("%+v", value),
			fmt.Sprintf("%#v", value),
		} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s formatting exposed retained credential: %q", label, rendered)
			}
		}
	}
}

func TestValidateConfigDoesNotEchoCredentialBearingInvalidFields(t *testing.T) {
	const secret = "credential-bearing-configuration-error"
	for name, config := range map[string]Config{
		"loader error": {
			Name: "broken", Transport: TransportStdio, Command: "server", Scope: ScopeUser,
			Env: map[string]string{"MCP_TOKEN": secret}, ConfigurationError: secret,
		},
		"scope": {
			Name: "broken", Transport: TransportStdio, Command: "server", Scope: Scope(secret),
			Env: map[string]string{"MCP_TOKEN": secret},
		},
		"transport": {
			Name: "broken", Transport: Transport(secret), Command: "server", Scope: ScopeUser,
			Env: map[string]string{"MCP_TOKEN": secret},
		},
	} {
		t.Run(name, func(t *testing.T) {
			descriptor, err := ValidateConfig(config)
			if err == nil {
				t.Fatalf("invalid config accepted: %#v", descriptor)
			}
			for _, rendered := range []string{
				err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
			} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("config validation exposed credential-bearing field: %q", rendered)
				}
			}
		})
	}
}

func TestManagerGuardsSnapshotWithCredentialUnionAcrossAllConfigs(t *testing.T) {
	const secret = "serverb"
	manager := NewManager(func(Config) (Connection, error) {
		return &fakeConnection{}, nil
	})
	t.Cleanup(func() { _ = manager.Close() })
	snapshot := manager.Reconcile(t.Context(), []Config{
		{
			Name: "servera", Transport: TransportStdio, Command: "server", Scope: ScopeUser,
			Env: map[string]string{"MCP_TOKEN": secret},
		},
		{Name: secret, Transport: TransportStdio, Command: "server", Scope: ScopeUser},
	})
	if len(snapshot.Servers) != 0 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("cross-config credential-bearing snapshot was published: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("manager snapshot exposed cross-config credential: %s", encoded)
	}
}

func TestManagerInvalidConfigDiagnosticOmitsCredentialIdentity(t *testing.T) {
	const secret = "invalid-config-credential-name"
	manager := NewManager(nil)
	t.Cleanup(func() { _ = manager.Close() })
	snapshot := manager.Reconcile(t.Context(), []Config{{
		Name: secret, SourceID: secret, Transport: Transport("invalid"), Command: "server", Scope: ScopeUser,
		Env: map[string]string{"MCP_TOKEN": secret},
	}})
	if len(snapshot.Servers) != 0 || len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Server != "" || snapshot.Diagnostics[0].Source != "" {
		t.Fatalf("invalid config diagnostic retained credential identity: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("invalid config snapshot exposed credential identity: %s", encoded)
	}
}

func bytesContains(data, pattern []byte) bool {
	for index := 0; index+len(pattern) <= len(data); index++ {
		match := true
		for offset := range pattern {
			if data[index+offset] != pattern[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

type fakeConnection struct {
	mu              sync.Mutex
	state           ConnectionState
	generation      uint64
	started         chan struct{}
	release         chan struct{}
	closeStart      chan struct{}
	closeWait       chan struct{}
	closeErr        error
	closePanic      string
	connectErr      error
	closed          bool
	toolCalls       int
	catalogEpoch    uint64
	tools           []ToolDescriptor
	listStart       chan struct{}
	listRelease     chan struct{}
	toolCallStart   chan struct{}
	toolCallRelease chan struct{}
}

type fakeToolCallPreparation struct {
	connection *fakeConnection
}

type fakeRegisteredToolCall struct {
	connection *fakeConnection
}

func (connection *fakeConnection) Connect(ctx context.Context) error {
	connection.mu.Lock()
	connection.generation++
	connection.state = StatePending
	started, release := connection.started, connection.release
	connection.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	connection.mu.Lock()
	connectErr := connection.connectErr
	if connectErr != nil {
		connection.state = StateFailed
	} else {
		connection.state = StateConnected
	}
	connection.mu.Unlock()
	return connectErr
}
func (connection *fakeConnection) Reconnect(ctx context.Context) error {
	return connection.Connect(ctx)
}
func (connection *fakeConnection) Close() error {
	connection.mu.Lock()
	connection.closed = true
	connection.state = StateClosed
	started, wait, result, panicPayload := connection.closeStart, connection.closeWait, connection.closeErr, connection.closePanic
	connection.mu.Unlock()
	if started != nil {
		close(started)
	}
	if wait != nil {
		<-wait
	}
	if panicPayload != "" {
		panic(panicPayload)
	}
	return result
}
func (connection *fakeConnection) State() ConnectionState {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.state
}
func (connection *fakeConnection) LastError() string { return "" }
func (connection *fakeConnection) Generation() uint64 {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.generation
}
func (connection *fakeConnection) InitializeResult() InitializeResult { return InitializeResult{} }
func (connection *fakeConnection) ListTools(context.Context) ([]ToolDescriptor, []Diagnostic, error) {
	connection.mu.Lock()
	tools := cloneTools(connection.tools)
	started, release := connection.listStart, connection.listRelease
	connection.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return tools, nil, nil
}
func (connection *fakeConnection) ListResources(context.Context) ([]ResourceDescriptor, []Diagnostic, error) {
	return []ResourceDescriptor{}, nil, nil
}
func (connection *fakeConnection) ListResourceTemplates(context.Context) ([]ResourceTemplate, []Diagnostic, error) {
	return []ResourceTemplate{}, nil, nil
}
func (connection *fakeConnection) ListPrompts(context.Context) ([]PromptDescriptor, []Diagnostic, error) {
	return []PromptDescriptor{}, nil, nil
}
func (connection *fakeConnection) CallTool(context.Context, string, map[string]any) (ToolResult, error) {
	connection.mu.Lock()
	connection.toolCalls++
	connection.mu.Unlock()
	return ToolResult{}, nil
}
func (connection *fakeConnection) ListToolsBound(ctx context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error) {
	tools, diagnostics, err := connection.ListTools(ctx)
	connection.mu.Lock()
	generation, epoch := connection.generation, connection.catalogEpoch
	connection.mu.Unlock()
	return tools, diagnostics, ToolCatalogVersion{ConnectionGeneration: generation, CatalogEpoch: epoch}, err
}
func (connection *fakeConnection) PrepareToolCall(context.Context, string, map[string]any) (ToolCallPreparation, error) {
	return &fakeToolCallPreparation{connection: connection}, nil
}
func (preparation *fakeToolCallPreparation) Register(version ToolCatalogVersion) (RegisteredToolCall, error) {
	connection := preparation.connection
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.state != StateConnected || connection.generation != version.ConnectionGeneration || connection.catalogEpoch != version.CatalogEpoch {
		return nil, ErrStaleToolBinding
	}
	return &fakeRegisteredToolCall{connection: connection}, nil
}
func (preparation *fakeToolCallPreparation) Cancel() {}
func (call *fakeRegisteredToolCall) Await() (ToolResult, error) {
	connection := call.connection
	connection.mu.Lock()
	start, release := connection.toolCallStart, connection.toolCallRelease
	connection.mu.Unlock()
	if start != nil {
		close(start)
	}
	if release != nil {
		<-release
	}
	connection.mu.Lock()
	connection.toolCalls++
	connection.mu.Unlock()
	return ToolResult{}, nil
}
func (*fakeRegisteredToolCall) Cancel() {}
func (connection *fakeConnection) ReadResource(context.Context, string) (ResourceResult, error) {
	return ResourceResult{}, nil
}
func (connection *fakeConnection) GetPrompt(context.Context, string, map[string]string) (PromptResult, error) {
	return PromptResult{}, nil
}

func TestManagerPrecedenceEnterpriseExclusivityAndSerializedGenerations(t *testing.T) {
	var mu sync.Mutex
	created := make(map[string]*fakeConnection)
	factory := func(config Config) (Connection, error) {
		connection := &fakeConnection{}
		if config.Command == "slow" {
			connection.started = make(chan struct{})
			connection.release = make(chan struct{})
		}
		mu.Lock()
		created[config.Command] = connection
		mu.Unlock()
		return connection, nil
	}
	manager := NewManager(factory)
	defer manager.Close()
	snapshot := manager.Reconcile(context.Background(), []Config{
		{Name: "plugin", Transport: TransportStdio, Command: "same", Scope: ScopePlugin, SourceID: "plugin:example", Trusted: true},
		{Name: "manual", Transport: TransportStdio, Command: "same", Scope: ScopeUser},
	})
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].Name != "manual" || len(snapshot.Diagnostics) == 0 {
		t.Fatalf("semantic precedence failed: %#v", snapshot)
	}
	snapshot = manager.Reconcile(context.Background(), []Config{
		{Name: "user", Transport: TransportStdio, Command: "user", Scope: ScopeUser},
		{Name: "enterprise", Transport: TransportStdio, Command: "enterprise", Scope: ScopeEnterprise},
	})
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].Name != "enterprise" {
		t.Fatalf("enterprise exclusivity failed: %#v", snapshot)
	}

	firstDone := make(chan Snapshot, 1)
	go func() {
		firstDone <- manager.Reconcile(context.Background(), []Config{{Name: "slow", Transport: TransportStdio, Command: "slow", Scope: ScopeUser}})
	}()
	for {
		mu.Lock()
		slow := created["slow"]
		mu.Unlock()
		if slow != nil {
			<-slow.started
			break
		}
		time.Sleep(time.Millisecond)
	}
	latestDone := make(chan Snapshot, 1)
	go func() {
		latestDone <- manager.Reconcile(context.Background(), []Config{{Name: "fast", Transport: TransportStdio, Command: "fast", Scope: ScopeUser}})
	}()
	select {
	case latest := <-latestDone:
		t.Fatalf("later reconciliation overtook a live generation: %#v", latest)
	case <-time.After(30 * time.Millisecond):
	}
	mu.Lock()
	slow := created["slow"]
	mu.Unlock()
	close(slow.release)
	first := <-firstDone
	latest := <-latestDone
	current := manager.Snapshot()
	if len(first.Servers) != 1 || first.Servers[0].Name != "slow" || len(latest.Servers) != 1 || latest.Servers[0].Name != "fast" || len(current.Servers) != 1 || current.Servers[0].Name != "fast" {
		t.Fatalf("serialized generations published out of order: first=%#v latest=%#v current=%#v", first, latest, current)
	}
	slow.mu.Lock()
	closed := slow.closed
	slow.mu.Unlock()
	if !closed {
		t.Fatal("replaced generation connection was not cleaned up")
	}
}

func TestManagerCloseInvalidatesInflightReconcileAndPreventsRestart(t *testing.T) {
	var mu sync.Mutex
	var calls int
	connection := &fakeConnection{started: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(func(config Config) (Connection, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return connection, nil
	})
	reconcileDone := make(chan Snapshot, 1)
	go func() {
		reconcileDone <- manager.Reconcile(context.Background(), []Config{{Name: "slow", Transport: TransportStdio, Command: "slow", Scope: ScopeUser}})
	}()
	<-connection.started

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed while invalidating an in-flight reconciliation: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close waited for an in-flight provider Connect callback")
	}
	close(connection.release)
	result := <-reconcileDone
	if len(result.Servers) != 0 {
		t.Fatalf("invalidated reconciliation published after Close: %#v", result)
	}
	connection.mu.Lock()
	closed := connection.closed
	connection.mu.Unlock()
	if !closed {
		t.Fatal("Close orphaned the connection started by reconciliation")
	}
	current := manager.Snapshot()
	if len(current.Servers) != 0 {
		t.Fatalf("closed manager exposed a live generation: %#v", current)
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	postClose := manager.Reconcile(context.Background(), []Config{{Name: "restart", Transport: TransportStdio, Command: "restart", Scope: ScopeUser}})
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before || len(postClose.Servers) != 0 {
		t.Fatalf("closed manager restarted work: calls=%d->%d snapshot=%#v", before, after, postClose)
	}
}

func TestManagerStartsAllConnectionClosesConcurrently(t *testing.T) {
	release := make(chan struct{})
	created := make(map[string]*fakeConnection)
	manager := NewManager(func(config Config) (Connection, error) {
		connection := &fakeConnection{closeStart: make(chan struct{}), closeWait: release}
		created[config.Name] = connection
		return connection, nil
	})
	snapshot := manager.Reconcile(context.Background(), []Config{
		{Name: "alpha", Transport: TransportStdio, Command: "alpha", Scope: ScopeUser},
		{Name: "beta", Transport: TransportStdio, Command: "beta", Scope: ScopeUser},
	})
	if len(snapshot.Servers) != 2 {
		t.Fatalf("server snapshot = %#v", snapshot)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Close() }()
	for _, name := range []string{"alpha", "beta"} {
		select {
		case <-created[name].closeStart:
		case <-time.After(time.Second):
			close(release)
			t.Fatalf("connection %s close was serialized behind a sibling", name)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestManagerCloseBoundsUncooperativeConnection(t *testing.T) {
	release := make(chan struct{})
	manager := NewManager(func(config Config) (Connection, error) {
		return &fakeConnection{closeStart: make(chan struct{}), closeWait: release}, nil
	})
	manager.closeTimeout = 20 * time.Millisecond
	snapshot := manager.Reconcile(context.Background(), []Config{{
		Name: "stubborn", Transport: TransportStdio, Command: "stubborn", Scope: ScopeUser,
	}})
	if len(snapshot.Servers) != 1 {
		close(release)
		t.Fatalf("server snapshot = %#v", snapshot)
	}
	start := time.Now()
	err := manager.Close()
	elapsed := time.Since(start)
	close(release)
	if err == nil || err.Error() != "one or more MCP provider connections failed to close" {
		t.Fatalf("uncooperative close error = %v", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("manager close exceeded aggregate bound: %s", elapsed)
	}
	if repeated := manager.Close(); repeated == nil || repeated.Error() != err.Error() {
		t.Fatalf("repeated close = %v, want cached %v", repeated, err)
	}
}

func TestManagerCloseErrorsAreDeterministicAndContainPanics(t *testing.T) {
	connections := map[string]*fakeConnection{
		"alpha": {closeErr: errors.New("alpha close failure")},
		"zeta":  {closePanic: "zeta panic payload must remain private"},
	}
	manager := NewManager(func(config Config) (Connection, error) {
		return connections[config.Name], nil
	})
	snapshot := manager.Reconcile(context.Background(), []Config{
		{Name: "zeta", Transport: TransportStdio, Command: "zeta", Scope: ScopeUser},
		{Name: "alpha", Transport: TransportStdio, Command: "alpha", Scope: ScopeUser},
	})
	if len(snapshot.Servers) != 2 {
		t.Fatalf("server snapshot = %#v", snapshot)
	}
	err := manager.Close()
	if err == nil {
		t.Fatal("provider close failures were omitted")
	}
	message := err.Error()
	if message != "one or more MCP provider connections failed to close" {
		t.Fatalf("provider close errors are not fixed and deterministic: %q", message)
	}
	if repeated := manager.Close(); repeated == nil || repeated.Error() != message {
		t.Fatalf("repeated close = %v, want cached %q", repeated, message)
	}
}

func TestManagerReconcileCleanupCannotHoldShutdownUnbounded(t *testing.T) {
	release := make(chan struct{})
	old := &fakeConnection{closeStart: make(chan struct{}), closeWait: release}
	failed := &fakeConnection{
		closeStart: make(chan struct{}), closeWait: release,
		connectErr: errors.New("synthetic connect failure"),
	}
	manager := NewManager(func(config Config) (Connection, error) {
		switch config.Command {
		case "old":
			return old, nil
		case "failed":
			return failed, nil
		default:
			t.Fatalf("unexpected command %q", config.Command)
			return nil, errors.New("unexpected command")
		}
	})
	manager.closeTimeout = 20 * time.Millisecond
	initial := manager.Reconcile(context.Background(), []Config{{
		Name: "provider", Transport: TransportStdio, Command: "old", Scope: ScopeUser,
	}})
	if len(initial.Servers) != 1 || initial.Servers[0].State != StateConnected {
		close(release)
		t.Fatalf("initial generation = %#v", initial)
	}

	reconcileDone := make(chan Snapshot, 1)
	go func() {
		reconcileDone <- manager.Reconcile(context.Background(), []Config{{
			Name: "provider", Transport: TransportStdio, Command: "failed", Scope: ScopeUser,
		}})
	}()
	for label, started := range map[string]<-chan struct{}{
		"replaced generation": old.closeStart,
		"failed candidate":    failed.closeStart,
	} {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatalf("%s close did not start concurrently", label)
		}
	}

	closeDone := make(chan error, 1)
	started := time.Now()
	go func() { closeDone <- manager.Close() }()
	var reconciled Snapshot
	select {
	case reconciled = <-reconcileDone:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("reconciliation cleanup exceeded its aggregate bound")
	}
	select {
	case err := <-closeDone:
		if err == nil || err.Error() != "one or more MCP provider connections failed to close" {
			close(release)
			t.Fatalf("shutdown did not report the in-flight stubborn candidate: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("reconciliation held the lifecycle lock past the cleanup deadline")
	}
	elapsed := time.Since(started)
	close(release)
	if elapsed > 250*time.Millisecond {
		t.Fatalf("reconcile-versus-close took %s, want a bounded handoff", elapsed)
	}
	if reconciled.Generation != initial.Generation+1 || len(reconciled.Servers) != 1 ||
		reconciled.Servers[0].Name != "provider" || reconciled.Servers[0].State != StateFailed {
		t.Fatalf("bounded cleanup changed generation publication: initial=%#v reconciled=%#v", initial, reconciled)
	}
	current := manager.Snapshot()
	if current.Generation != reconciled.Generation || len(current.Servers) != 1 || current.Servers[0].State != StateFailed {
		t.Fatalf("closed manager changed failed-generation semantics: %#v", current)
	}
}

func TestManagerConcurrentReconcileReconnectsReusedConnectionOnce(t *testing.T) {
	connection := &fakeConnection{}
	manager := NewManager(func(Config) (Connection, error) { return connection, nil })
	defer manager.Close()
	config := Config{Name: "reused", Transport: TransportStdio, Command: "reused", Scope: ScopeUser}
	if snapshot := manager.Reconcile(context.Background(), []Config{config}); len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateConnected {
		t.Fatalf("initial generation = %#v", snapshot)
	}
	connection.mu.Lock()
	connection.state = StateFailed
	connection.started = make(chan struct{})
	connection.release = make(chan struct{})
	started := connection.started
	release := connection.release
	connection.mu.Unlock()

	firstDone := make(chan Snapshot, 1)
	secondDone := make(chan Snapshot, 1)
	go func() { firstDone <- manager.Reconcile(context.Background(), []Config{config}) }()
	<-started
	go func() { secondDone <- manager.Reconcile(context.Background(), []Config{config}) }()
	select {
	case result := <-secondDone:
		t.Fatalf("second reconciliation raced a reused connection restart: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	first, second := <-firstDone, <-secondDone
	if len(first.Servers) != 1 || first.Servers[0].State != StateConnected || len(second.Servers) != 1 || second.Servers[0].State != StateConnected {
		t.Fatalf("reconnect generations = first %#v, second %#v", first, second)
	}
	if generation := connection.Generation(); generation != 2 {
		t.Fatalf("reused connection restarted %d times; want one initial connect and one reconnect", generation)
	}
}

func TestManagerToolBindingRejectsReconnectAndReconcileGenerations(t *testing.T) {
	connection := &fakeConnection{tools: []ToolDescriptor{{
		Name: "mcp__bound__echo", InputSchema: json.RawMessage(`{"type":"object"}`),
	}}}
	manager := NewManager(func(Config) (Connection, error) { return connection, nil })
	defer manager.Close()
	config := Config{Name: "bound", Transport: TransportStdio, Command: "bound", Scope: ScopeUser}
	if snapshot := manager.Reconcile(t.Context(), []Config{config}); len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateConnected {
		t.Fatalf("initial generation = %#v", snapshot)
	}
	tools, _, err := manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("bound tools = %#v, %v", tools, err)
	}
	binding, ok := tools[0].Binding()
	if !ok {
		t.Fatal("listed descriptor did not carry a tool binding")
	}
	if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); err != nil {
		t.Fatalf("bound call failed: %v", err)
	}

	if err := manager.Reconnect(t.Context(), "bound"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("pre-reconnect binding error = %v", err)
	}
	tools, _, err = manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("reconnected tools = %#v, %v", tools, err)
	}
	reconnected, ok := tools[0].Binding()
	if !ok {
		t.Fatal("reconnected descriptor did not carry a tool binding")
	}
	if _, err := manager.CallBoundTool(t.Context(), reconnected, "echo", map[string]any{}); err != nil {
		t.Fatalf("reconnected binding failed: %v", err)
	}

	connection.mu.Lock()
	connection.catalogEpoch++
	connection.mu.Unlock()
	if _, err := manager.CallBoundTool(t.Context(), reconnected, "echo", map[string]any{}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("pre-list-change binding error = %v", err)
	}
	tools, _, err = manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("changed-catalog tools = %#v, %v", tools, err)
	}
	changedCatalog, ok := tools[0].Binding()
	if !ok {
		t.Fatal("changed catalog descriptor did not carry a tool binding")
	}
	if _, err := manager.CallBoundTool(t.Context(), changedCatalog, "echo", map[string]any{}); err != nil {
		t.Fatalf("changed catalog binding failed: %v", err)
	}

	manager.Reconcile(t.Context(), []Config{config})
	if _, err := manager.CallBoundTool(t.Context(), changedCatalog, "echo", map[string]any{}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("pre-reconcile binding error = %v", err)
	}
	tools, _, err = manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("reconciled tools = %#v, %v", tools, err)
	}
	current, ok := tools[0].Binding()
	if !ok {
		t.Fatal("reconciled descriptor did not carry a tool binding")
	}
	if _, err := manager.CallBoundTool(t.Context(), current, "echo", map[string]any{}); err != nil {
		t.Fatalf("current binding failed: %v", err)
	}
	connection.mu.Lock()
	calls := connection.toolCalls
	connection.mu.Unlock()
	if calls != 4 {
		t.Fatalf("provider received %d calls, want only the four current catalog/generation calls", calls)
	}
}

func TestManagerToolPublicationRejectsProviderThatFailsAfterListing(t *testing.T) {
	connection := &fakeConnection{
		tools: []ToolDescriptor{{
			Name: "mcp__unstable__echo", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		listStart:   make(chan struct{}),
		listRelease: make(chan struct{}),
	}
	manager := NewManager(func(Config) (Connection, error) { return connection, nil })
	t.Cleanup(func() { _ = manager.Close() })
	config := Config{Name: "unstable", Transport: TransportStdio, Command: "unstable", Scope: ScopeUser}
	if snapshot := manager.Reconcile(t.Context(), []Config{config}); len(snapshot.Servers) != 1 ||
		snapshot.Servers[0].State != StateConnected {
		t.Fatalf("initial generation = %#v", snapshot)
	}
	type result struct {
		tools       []ToolDescriptor
		diagnostics []Diagnostic
		err         error
	}
	done := make(chan result, 1)
	go func() {
		tools, diagnostics, err := manager.Tools(t.Context())
		done <- result{tools: tools, diagnostics: diagnostics, err: err}
	}()
	<-connection.listStart
	connection.mu.Lock()
	connection.state = StateFailed
	connection.mu.Unlock()
	close(connection.listRelease)
	publication := <-done
	if publication.err != nil {
		t.Fatal(publication.err)
	}
	if len(publication.tools) != 0 || len(publication.diagnostics) == 0 {
		t.Fatalf("failed provider publication = tools %#v, diagnostics %#v", publication.tools, publication.diagnostics)
	}
}

func TestManagerToolBindingRejectsLiveCatalogEpochChange(t *testing.T) {
	var client *Client
	manager := NewManager(func(config Config) (Connection, error) {
		var err error
		client, err = NewClient(config)
		return client, err
	})
	t.Cleanup(func() { _ = manager.Close() })
	config := helperConfig()
	config.Name = "epoch"
	if snapshot := manager.Reconcile(t.Context(), []Config{config}); len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateConnected {
		t.Fatalf("initial generation = %#v", snapshot)
	}
	tools, _, err := manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("initial tools = %#v, %v", tools, err)
	}
	binding, ok := tools[0].Binding()
	if !ok {
		t.Fatal("initial tool binding is absent")
	}
	client.handleNotification(client.Generation(), "notifications/tools/list_changed")
	if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{"text": "stale"}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("pre-notification catalog binding error = %v", err)
	}

	tools, _, err = manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("refreshed tools = %#v, %v", tools, err)
	}
	current, ok := tools[0].Binding()
	if !ok {
		t.Fatal("refreshed tool binding is absent")
	}
	result, err := manager.CallBoundTool(t.Context(), current, "echo", map[string]any{"text": "current"})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "current" {
		t.Fatalf("refreshed bound call = %#v, %v", result, err)
	}
}

func TestManagerToolBindingRegistrationDoesNotBlockReplacementLifecycle(t *testing.T) {
	oldConnection := &fakeConnection{
		tools:         []ToolDescriptor{{Name: "mcp__linear__echo", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		toolCallStart: make(chan struct{}), toolCallRelease: make(chan struct{}),
	}
	newConnection := &fakeConnection{
		tools: []ToolDescriptor{{Name: "mcp__linear__echo", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	manager := NewManager(func(config Config) (Connection, error) {
		if config.Command == "old" {
			return oldConnection, nil
		}
		return newConnection, nil
	})
	t.Cleanup(func() { _ = manager.Close() })
	oldConfig := Config{Name: "linear", Transport: TransportStdio, Command: "old", Scope: ScopeUser}
	if snapshot := manager.Reconcile(t.Context(), []Config{oldConfig}); len(snapshot.Servers) != 1 {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	tools, _, err := manager.Tools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("initial tools = %#v, %v", tools, err)
	}
	binding, ok := tools[0].Binding()
	if !ok {
		t.Fatal("initial binding is absent")
	}
	callDone := make(chan error, 1)
	go func() {
		_, callErr := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{})
		callDone <- callErr
	}()
	<-oldConnection.toolCallStart

	reconcileDone := make(chan Snapshot, 1)
	go func() {
		reconcileDone <- manager.Reconcile(t.Context(), []Config{{
			Name: "linear", Transport: TransportStdio, Command: "new", Scope: ScopeUser,
		}})
	}()
	select {
	case snapshot := <-reconcileDone:
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateConnected {
			t.Fatalf("replacement snapshot = %#v", snapshot)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("registered tool call blocked replacement lifecycle")
	}
	close(oldConnection.toolCallRelease)
	if err := <-callDone; err != nil {
		t.Fatalf("linearized bound call failed: %v", err)
	}
	if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrStaleToolBinding) {
		t.Fatalf("published replacement accepted old binding: %v", err)
	}
}
