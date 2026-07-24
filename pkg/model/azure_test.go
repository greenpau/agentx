package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/redact"
)

const (
	testAPIKey           = "test-api-key-DO-NOT-LEAK"
	testSourceCredential = "test-source-credential-DO-NOT-LEAK"
)

type capturedRequest struct {
	path      string
	query     url.Values
	header    http.Header
	body      map[string]any
	decodeErr error
}

func TestAzureRequestProjectionAndCanonicalStream(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		err := json.NewDecoder(request.Body).Decode(&body)
		captured <- capturedRequest{
			path: request.URL.Path, query: request.URL.Query(), header: request.Header.Clone(), body: body, decodeErr: err,
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("apim-request-id", "azure-request-1")
		writeSSE(t, writer,
			map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{
				"id": "resp_1", "model": "gpt-5.6-sol-2026-07-01", "status": "in_progress", "output": []any{},
			}},
			map[string]any{"type": "response.in_progress", "sequence_number": 1, "response": map[string]any{
				"id": "resp_1", "model": "gpt-5.6-sol-2026-07-01", "status": "in_progress", "output": []any{},
			}},
			map[string]any{"type": "response.output_text.delta", "sequence_number": 2, "item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": "Hello"},
			map[string]any{"type": "response.reasoning_summary_text.delta", "sequence_number": 3, "item_id": "rs_1", "output_index": 1, "delta": "Checking"},
			map[string]any{"type": "response.output_item.added", "sequence_number": 4, "output_index": 2, "item": map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": "", "status": "in_progress",
			}},
			map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 5, "item_id": "fc_1", "output_index": 2, "delta": "{\"q\":"},
			map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 6, "item_id": "fc_1", "output_index": 2, "delta": "\"go\"}"},
			map[string]any{"type": "response.function_call_arguments.done", "sequence_number": 7, "item_id": "fc_1", "output_index": 2, "arguments": "{\"q\":\"go\"}"},
			map[string]any{"type": "response.output_item.done", "sequence_number": 8, "output_index": 2, "item": map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": "{\"q\":\"go\"}", "status": "completed",
			}},
			map[string]any{"type": "response.completed", "sequence_number": 9, "response": map[string]any{
				"id": "resp_1", "model": "gpt-5.6-sol-2026-07-01", "status": "completed", "previous_response_id": "resp_0",
				"output": []any{
					map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Hello"}}},
					map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "encrypted-reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Checked"}}},
					map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lookup", "arguments": "{\"q\":\"go\"}", "status": "completed"},
				},
				"usage": map[string]any{
					"input_tokens": 100, "output_tokens": 30, "total_tokens": 130,
					"input_tokens_details":  map[string]any{"cached_tokens": 40},
					"output_tokens_details": map[string]any{"reasoning_tokens": 12},
				},
			}},
		)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, configOverrides{}, AzureOptions{})
	parallel := false
	request := Request{
		Model:        "logical-model-does-not-replace-deployment",
		Instructions: "stable system instructions",
		Input: []Item{
			TextMessage(RoleSystem, "system history"),
			TextMessage(RoleUser, "question"),
			TextMessage(RoleAssistant, "prior answer"),
			FunctionCall("fc_old", "call_old", "lookup", `{"q":"old"}`),
			FunctionCallOutput("call_old", "old result"),
			{Type: ItemReasoning, ID: "rs_old", EncryptedContent: "ciphertext", Summary: []Content{{Type: ContentSummaryText, Text: "Prior reasoning"}}},
		},
		Tools:              []Tool{{Name: "lookup", Description: "Looks up data.", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`), Strict: true}},
		Reasoning:          Reasoning{Effort: "xhigh"},
		MaxOutputTokens:    64000,
		PreviousResponseID: "resp_0",
		ParallelToolCalls:  &parallel,
		Metadata:           map[string]string{"session": "session_1"},
	}
	request.Input[2].APIResponseID = "local-response-provenance"
	stream, err := client.Stream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	events, err := Drain(stream)
	if err != nil {
		t.Fatal(err)
	}

	wire := <-captured
	if wire.decodeErr != nil {
		t.Fatal(wire.decodeErr)
	}
	if wire.path != "/openai/v1/responses" || wire.query.Get("api-version") != "2026-07-01-preview" {
		t.Fatalf("request URL = %s?%s", wire.path, wire.query.Encode())
	}
	if wire.header.Get("api-key") != testAPIKey || wire.header.Get("Authorization") != "" {
		t.Fatalf("authentication headers = api-key %q, Authorization %q", wire.header.Get("api-key"), wire.header.Get("Authorization"))
	}
	if wire.header.Get("Accept") != "text/event-stream" || wire.header.Get("Content-Type") != "application/json" {
		t.Fatalf("content negotiation headers = %#v", wire.header)
	}
	if got := wire.body["model"]; got != "configured-deployment" {
		t.Fatalf("wire model = %v, want configured deployment", got)
	}
	if wire.body["stream"] != true || wire.body["store"] != false {
		t.Fatalf("stream/store = %v/%v", wire.body["stream"], wire.body["store"])
	}
	assertNestedValue(t, wire.body, []string{"reasoning", "effort"}, "xhigh")
	include, ok := wire.body["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", wire.body["include"])
	}
	if wire.body["previous_response_id"] != "resp_0" || wire.body["parallel_tool_calls"] != false {
		t.Fatalf("continuation fields = %#v", wire.body)
	}
	inputs, ok := wire.body["input"].([]any)
	if !ok || len(inputs) != 6 {
		t.Fatalf("input = %#v", wire.body["input"])
	}
	wantTypes := []string{"message", "message", "message", "function_call", "function_call_output", "reasoning"}
	for i, want := range wantTypes {
		if inputs[i].(map[string]any)["type"] != want {
			t.Fatalf("input[%d] type = %#v, want %q", i, inputs[i], want)
		}
	}
	assistantContent := inputs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if assistantContent["type"] != "output_text" {
		t.Fatalf("assistant content = %#v", assistantContent)
	}
	if _, leaked := inputs[2].(map[string]any)["api_response_id"]; leaked {
		t.Fatalf("local response provenance leaked onto Azure wire: %#v", inputs[2])
	}
	reasoningInput := inputs[5].(map[string]any)
	if reasoningInput["encrypted_content"] != "ciphertext" {
		t.Fatalf("reasoning replay = %#v", reasoningInput)
	}
	tools, ok := wire.body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", wire.body["tools"])
	}
	wireTool := tools[0].(map[string]any)
	if wireTool["type"] != "function" || wireTool["name"] != "lookup" || wireTool["strict"] != true {
		t.Fatalf("wire tool = %#v", wireTool)
	}

	wantEventTypes := []EventType{
		EventResponseCreated, EventResponseInProgress, EventTextDelta, EventReasoningDelta,
		EventFunctionCallArgumentsDelta, EventFunctionCallArgumentsDelta,
		EventFunctionCallCompleted, EventUsage, EventResponseCompleted,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, wantEventTypes) {
		t.Fatalf("event types = %v, want %v", got, wantEventTypes)
	}
	for _, event := range events {
		if event.RequestID != "azure-request-1" {
			t.Fatalf("request ID on %s = %q", event.Type, event.RequestID)
		}
	}
	call := events[6].Call
	if call == nil || call.CallID != "call_1" || call.Name != "lookup" || call.Arguments != `{"q":"go"}` {
		t.Fatalf("completed call = %#v", call)
	}
	usage := events[7].Usage
	if usage == nil || *usage != (Usage{InputTokens: 100, OutputTokens: 30, TotalTokens: 130, CachedInputTokens: 40, ReasoningOutputTokens: 12}) {
		t.Fatalf("usage = %#v", usage)
	}
	terminal := events[8].Response
	if terminal == nil || terminal.ID != "resp_1" || terminal.PreviousResponseID != "resp_0" || len(terminal.Output) != 3 {
		t.Fatalf("terminal response = %#v", terminal)
	}
	if terminal.Output[1].Type != ItemReasoning || terminal.Output[1].EncryptedContent != "encrypted-reasoning" {
		t.Fatalf("terminal reasoning item = %#v", terminal.Output[1])
	}
	if terminal.Output[0].Phase != "final_answer" {
		t.Fatalf("terminal assistant phase = %q", terminal.Output[0].Phase)
	}
}

func TestAzureDirectConstructorRejectsAPIKeyThatWouldChangeOnWire(t *testing.T) {
	const paddedKey = "  direct-constructor-key  "
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		// If validation regresses, reflect the value as net/http serialized it;
		// the caller must never receive an uncovered canonical credential.
		writeAPIError(writer, http.StatusBadRequest, "reflected", request.Header.Get("api-key"))
	}))
	defer server.Close()

	endpoint, err := url.Parse(server.URL + "/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.Azure{
		Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "configured-deployment",
		APIKey: paddedKey, APIVersion: "2026-07-01-preview", ReasoningEffort: "high",
		RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second, MaxRetries: 0,
		UnsafeAllowInsecureLoopbackForTesting: true,
	}
	client, err := NewAzureClient(configuration, AzureOptions{HTTPClient: server.Client()})
	if !errors.Is(err, config.ErrInvalid) || client != nil {
		t.Fatalf("direct constructor accepted a wire-changing API key: client=%v err=%v", client, err)
	}
	if strings.Contains(err.Error(), paddedKey) {
		t.Fatal("constructor diagnostic exposed the rejected API key")
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected API key reached loopback provider %d times", calls.Load())
	}
}

func TestAzureRejectsCredentialAliasesInRequestMetadataBeforeTransport(t *testing.T) {
	tests := []struct {
		name       string
		secret     string
		endpoint   string
		apiKey     string
		apiVersion string
		userAgent  string
	}{
		{
			name: "endpoint host", secret: "host-egress-secret",
			endpoint: "https://host-egress-secret.example.test/openai/v1/responses",
		},
		{
			name: "endpoint path", secret: "path-egress-secret",
			endpoint: "https://example.test/openai/path-egress-secret/responses",
		},
		{
			name: "exact final URL boundary", secret: "example.test/openai",
			endpoint: "https://example.test/openai/v1/responses",
		},
		{
			name: "decoded api-version query", secret: "query/egress-secret",
			endpoint: "https://example.test/openai/v1/responses", apiVersion: "query/egress-secret",
		},
		{
			name: "User-Agent value", secret: "ua-egress-secret",
			endpoint: "https://example.test/openai/v1/responses", userAgent: "ua-egress-secret",
		},
		{
			name: "physical non-auth header", secret: "User-Agent: ua-egress-value\r\n",
			endpoint: "https://example.test/openai/v1/responses", userAgent: "ua-egress-value",
		},
		{
			name: "contributed credential embedded in api-key", secret: "embedded-egress-secret",
			endpoint: "https://example.test/openai/v1/responses",
			apiKey:   "azure-prefix-embedded-egress-secret-suffix",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			endpoint, err := url.Parse(test.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			apiKey := test.apiKey
			if apiKey == "" {
				apiKey = testAPIKey
			}
			apiVersion := test.apiVersion
			if apiVersion == "" {
				apiVersion = "2026-07-01-preview"
			}
			options := AzureOptions{
				CredentialSanitizer: redact.New(test.secret),
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("unsafe request reached transport")
				})},
				UserAgent: test.userAgent,
			}
			client, err := NewAzureClient(config.Azure{
				Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "configured-deployment",
				APIKey: apiKey, APIVersion: apiVersion, ReasoningEffort: "high",
				RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second,
			}, options)
			if client != nil || !errors.Is(err, ErrProtocol) {
				t.Fatalf("constructor accepted unsafe request metadata: client=%v err=%v", client, err)
			}
			rendered := fmt.Sprintf("%s %v %+v %#v", err, err, err, err)
			if strings.Contains(rendered, test.secret) || strings.Contains(rendered, apiKey) {
				t.Fatalf("constructor diagnostic exposed request credential material: %q", rendered)
			}
			if calls.Load() != 0 {
				t.Fatalf("unsafe request metadata reached transport %d times", calls.Load())
			}
		})
	}
}

func TestAzureRevalidatesExactRequestMetadataBeforeHTTPClientDo(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		credentials []string
		mutate      func(*testing.T, *AzureClient)
	}{
		{
			name: "endpoint host", secret: "mutated-host-secret",
			mutate: func(t *testing.T, client *AzureClient) {
				endpoint, err := url.Parse("https://mutated-host-secret.example.test/openai/v1/responses")
				if err != nil {
					t.Fatal(err)
				}
				client.endpoint = *endpoint
			},
		},
		{
			name: "endpoint path", secret: "mutated-path-secret",
			mutate: func(t *testing.T, client *AzureClient) {
				endpoint, err := url.Parse("https://example.test/openai/mutated-path-secret/responses")
				if err != nil {
					t.Fatal(err)
				}
				client.endpoint = *endpoint
			},
		},
		{
			name: "decoded api-version query", secret: "mutated/query-secret",
			mutate: func(_ *testing.T, client *AzureClient) {
				client.apiVersion = "mutated/query-secret"
			},
		},
		{
			name: "User-Agent value", secret: "mutated-ua-secret",
			mutate: func(_ *testing.T, client *AzureClient) {
				client.userAgent = "mutated-ua-secret"
			},
		},
		{
			name: "physical non-auth header", secret: "User-Agent: mutated-ua-value\r\n",
			mutate: func(_ *testing.T, client *AzureClient) {
				client.userAgent = "mutated-ua-value"
			},
		},
		{
			name:   "contributed credential embedded in api-key",
			secret: "mutated-embedded-secret",
			credentials: []string{
				"prefix-mutated-embedded-secret-suffix",
			},
			mutate: func(_ *testing.T, client *AzureClient) {
				client.apiKey = "prefix-mutated-embedded-secret-suffix"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			endpoint, err := url.Parse("https://safe.example.test/openai/v1/responses")
			if err != nil {
				t.Fatal(err)
			}
			literals := append([]string{test.secret}, test.credentials...)
			client, err := NewAzureClient(config.Azure{
				Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "configured-deployment",
				APIKey: testAPIKey, APIVersion: "2026-07-01-preview", ReasoningEffort: "high",
				RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second,
			}, AzureOptions{
				CredentialSanitizer: redact.New(literals...),
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("unsafe request reached transport")
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, client)
			_, err = client.Stream(t.Context(), basicRequest())
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("final request metadata guard = %v", err)
			}
			rendered := fmt.Sprintf("%s %v %+v %#v", err, err, err, err)
			if strings.Contains(rendered, test.secret) {
				t.Fatalf("final request diagnostic exposed credential material: %q", rendered)
			}
			if calls.Load() != 0 {
				t.Fatalf("unsafe final request reached transport %d times", calls.Load())
			}
		})
	}
}

func TestAzureAllowsExactAPIKeyDuplicateInCompleteCredentialUnion(t *testing.T) {
	const unrelatedCredential = "unrelated-contributed-secret"
	var calls atomic.Int32
	endpoint, err := url.Parse("https://example.test/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAzureClient(config.Azure{
		Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "configured-deployment",
		APIKey: testAPIKey, APIVersion: "2026-07-01-preview", ReasoningEffort: "high",
		RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second,
	}, AzureOptions{
		CredentialSanitizer: redact.New(testAPIKey, unrelatedCredential),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			if request.Header.Get(azureAPIKeyHeader) != testAPIKey {
				return nil, errors.New("Azure key was not preserved")
			}
			return streamingResponse("resp_exact_key_union"), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(t.Context(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("legitimate exact-key union requests = %d, want 1", calls.Load())
	}
}

func TestAzureRejectsCredentialReconstructedAcrossCompleteToolEnvelope(t *testing.T) {
	const secret = `Foo","description":"bar`
	var calls atomic.Int32
	client := newTestClient(t, "http://localhost", configOverrides{}, AzureOptions{
		CredentialSanitizer: redact.New(secret),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("request must not be sent")
		})},
	})
	request := basicRequest()
	request.Tools = []Tool{{
		Name: "Foo", Description: "bar",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	_, err := client.Stream(t.Context(), request)
	if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), secret) {
		t.Fatalf("cross-field request guard = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe request reached HTTP transport %d times", calls.Load())
	}
}

func TestAzureRejectsSemanticCredentialAliasesInReplayArgumentsBeforeRequest(t *testing.T) {
	const secret = "secret/path"
	for name, arguments := range map[string]string{
		"unicode": `{"value":"\u0073ecret/path"}`,
		"solidus": `{"value":"secret\/path"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			client := newTestClient(t, "http://localhost", configOverrides{}, AzureOptions{
				CredentialSanitizer: redact.New(secret),
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("unsafe request reached transport")
				})},
			})
			request := basicRequest()
			request.Input = append(request.Input, FunctionCall("fc_alias", "call_alias", "Read", arguments))
			_, err := client.Stream(t.Context(), request)
			if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), arguments) {
				t.Fatalf("semantic request argument error = %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("semantic request alias reached transport %d times", calls.Load())
			}
		})
	}
}

func TestAzureClientStringFormatsDoNotRenderCredentialBearingIdentities(t *testing.T) {
	const secret = "identity-secret"
	endpoint, err := url.Parse("https://example.test/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAzureClient(config.Azure{
		Endpoint: endpoint, ModelName: "prefix-" + secret + "-suffix", Deployment: secret,
		APIKey: secret, APIVersion: "2026-07-01-preview", ReasoningEffort: "high",
		RequestTimeout: time.Second, StreamWatchdog: time.Second,
	}, AzureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for label, rendered := range map[string]string{
		"String": client.String(),
		"%s":     fmt.Sprintf("%s", client),
		"%v":     fmt.Sprintf("%v", client),
		"%+v":    fmt.Sprintf("%+v", client),
		"%#v":    fmt.Sprintf("%#v", client),
	} {
		if strings.Contains(rendered, secret) || strings.Contains(rendered, client.ModelName()) {
			t.Fatalf("%s rendered credential-bearing identity: %q", label, rendered)
		}
	}
}

func TestAzureRejectsSemanticCredentialAliasesAcrossResponseArgumentSurfaces(t *testing.T) {
	const secret = "secret/path"
	aliases := map[string]string{
		"unicode": `{"value":"\u0073ecret/path"}`,
		"solidus": `{"value":"secret\/path"}`,
	}
	for aliasName, arguments := range aliases {
		for _, surface := range []string{"response", "delta", "terminal"} {
			t.Run(aliasName+"_"+surface, func(t *testing.T) {
				server := newLoopbackTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					createdOutput := []any{}
					if surface == "response" {
						createdOutput = []any{map[string]any{
							"type": "function_call", "id": "fc_alias", "call_id": "call_alias",
							"name": "Read", "arguments": arguments, "status": "in_progress",
						}}
					}
					events := []map[string]any{{
						"type": "response.created", "response": map[string]any{
							"id": "resp_alias", "status": "in_progress", "output": createdOutput,
						},
					}}
					if surface != "response" {
						events = append(events, map[string]any{
							"type": "response.in_progress", "response": map[string]any{
								"id": "resp_alias", "status": "in_progress", "output": []any{},
							},
						})
					}
					switch surface {
					case "delta":
						events = append(events,
							map[string]any{"type": "response.output_item.added", "item_id": "fc_alias", "item": map[string]any{
								"type": "function_call", "id": "fc_alias", "call_id": "call_alias",
								"name": "Read", "status": "in_progress",
							}},
							map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_alias", "delta": arguments},
							map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_alias", "name": "Read", "arguments": arguments},
						)
					case "terminal":
						events = append(events, map[string]any{
							"type": "response.completed", "response": map[string]any{
								"id": "resp_alias", "status": "completed",
								"output": []any{map[string]any{
									"type": "function_call", "id": "fc_alias", "call_id": "call_alias",
									"name": "Read", "arguments": arguments, "status": "completed",
								}},
							},
						})
					}
					writeSSE(t, writer, events...)
				}))
				defer server.Close()

				client := newTestClient(t, server.URL, configOverrides{}, AzureOptions{
					CredentialSanitizer: redact.New(secret),
				})
				stream, err := client.Stream(t.Context(), basicRequest())
				if err != nil {
					t.Fatal(err)
				}
				events, err := Drain(stream)
				if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), arguments) {
					t.Fatalf("semantic %s argument error = %v", surface, err)
				}
				for _, event := range events {
					if event.Type == EventFunctionCallArgumentsDelta || event.Call != nil || event.Type == EventResponseCompleted {
						t.Fatalf("unsafe %s argument reached public event: %#v", surface, event)
					}
				}
				encoded, marshalErr := json.Marshal(events)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), arguments) {
					t.Fatalf("unsafe %s argument reached public envelope: %s", surface, encoded)
				}
			})
		}
	}
}

func TestAzureRejectsCredentialReconstructedAcrossPublicEventAndResponseEnvelopes(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		requestID string
		response  map[string]any
		fixture   any
	}{
		{
			name: "event", secret: `req-safe","response_id":"resp-safe`,
			requestID: "req-safe",
			response:  map[string]any{"id": "resp-safe", "status": "in_progress", "output": []any{}},
			fixture: Event{
				Type: EventResponseCreated, RawType: "response.created",
				RequestID: "req-safe", ResponseID: "resp-safe",
				Response: &Response{ID: "resp-safe", Status: "in_progress"},
			},
		},
		{
			name: "response", secret: `resp-safe","model":"model-safe`,
			requestID: "request-safe",
			response:  map[string]any{"id": "resp-safe", "model": "model-safe", "status": "in_progress", "output": []any{}},
			fixture:   &Response{ID: "resp-safe", Model: "model-safe", Status: "in_progress"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, err := json.Marshal(test.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(fixture), test.secret) {
				t.Fatalf("%s fixture did not reconstruct credential: %s", test.name, fixture)
			}
			server := newLoopbackTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("apim-request-id", test.requestID)
				writeSSE(t, writer, map[string]any{"type": "response.created", "response": test.response})
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, configOverrides{}, AzureOptions{
				CredentialSanitizer: redact.New(test.secret),
			})
			stream, err := client.Stream(t.Context(), basicRequest())
			if err != nil {
				t.Fatal(err)
			}
			event, err := stream.Next()
			if !errors.Is(err, ErrProtocol) || event != (Event{}) || strings.Contains(err.Error(), test.secret) {
				t.Fatalf("%s public envelope result: event=%#v err=%v", test.name, event, err)
			}
			_ = stream.Close()
		})
	}
}

func TestAzureRetryDiagnosticsDiscardRawRetryAfterAndCloseCompositionSeams(t *testing.T) {
	const (
		errorComposition = "code=alpha: omega"
		retryAfterSecret = "raw-retry-after-DO-NOT-LEAK"
	)
	var calls atomic.Int32
	var retryFormats []string
	options := noWaitOptions()
	options.CredentialSanitizer = redact.New(errorComposition, retryAfterSecret)
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header: http.Header{
				"Content-Type":    []string{"application/json"},
				"Retry-After":     []string{retryAfterSecret},
				"apim-request-id": []string{"request-safe"},
			},
			Body: io.NopCloser(strings.NewReader(`{"error":{"code":"alpha","message":"omega"}}`)),
		}, nil
	})}
	options.OnRetry = func(info RetryInfo) {
		retryFormats = append(retryFormats,
			fmt.Sprintf("%v | %+v | %#v | error=%#v", info, info, info, info.Error),
		)
		if observed, ok := info.Error.(*ProviderError); ok {
			// The observer receives a detached copy and cannot change the
			// classification used by the active retry loop or terminal error.
			observed.Retryable = false
		}
	}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
	_, err := client.Stream(t.Context(), basicRequest())
	var exhausted *RetryExhaustedError
	var providerError *ProviderError
	if !errors.As(err, &exhausted) || !errors.As(err, &providerError) || !providerError.Retryable {
		t.Fatalf("retry classification was not preserved: %T %v", err, err)
	}
	if calls.Load() != 2 || len(retryFormats) != 1 {
		t.Fatalf("requests=%d retry callbacks=%d", calls.Load(), len(retryFormats))
	}
	formats := append([]string(nil), retryFormats...)
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formats = append(formats,
			fmt.Sprintf(format, providerError),
			fmt.Sprintf(format, exhausted),
			fmt.Sprintf(format, err),
		)
	}
	for _, rendered := range formats {
		if strings.Contains(rendered, errorComposition) || strings.Contains(rendered, retryAfterSecret) {
			t.Fatalf("retry diagnostic exposed provider composition: %q", rendered)
		}
	}
}

func TestAzureReasoningReplayAlwaysIncludesSummaryArray(t *testing.T) {
	projected, err := projectItem(Item{Type: ItemReasoning, ID: "rs_empty", EncryptedContent: "ciphertext"})
	if err != nil {
		t.Fatal(err)
	}
	summary, exists := projected["summary"]
	if !exists {
		t.Fatalf("reasoning replay omitted required summary: %#v", projected)
	}
	items, ok := summary.([]map[string]string)
	if !ok || len(items) != 0 {
		t.Fatalf("reasoning replay summary = %#v, want empty array", summary)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"summary":[]`) {
		t.Fatalf("encoded reasoning replay omitted empty summary array: %s", encoded)
	}
}

func TestAzureReasoningNoneOmitsEncryptedInclude(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, exists := body["include"]; exists {
			t.Errorf("include present for reasoning none: %#v", body["include"])
		}
		writeCompleted(writer, "resp_none")
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{}, AzureOptions{})
	stream, err := client.Stream(context.Background(), Request{Input: []Item{TextMessage(RoleUser, "hi")}, Reasoning: Reasoning{Effort: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
}

func TestAzureMidstreamErrorIsCanonicalAndNeverRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("apim-request-id", "request-error")
		writeSSE(t, writer,
			map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_error", "status": "in_progress", "output": []any{}}},
			map[string]any{"type": "response.in_progress", "response": map[string]any{"id": "resp_error", "status": "in_progress", "output": []any{}}},
			map[string]any{"type": "error", "code": "server_error", "message": "failed with " + testAPIKey, "param": "input"},
		)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{maxRetries: 5}, noWaitOptions())
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	events, err := Drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("request count = %d, want 1", calls.Load())
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, []EventType{EventResponseCreated, EventResponseInProgress, EventError}) {
		t.Fatalf("event types = %v", got)
	}
	providerError := events[2].Error
	if providerError == nil || providerError.Code != "server_error" {
		t.Fatalf("provider error = %#v", providerError)
	}
	if strings.Contains(providerError.Error(), testAPIKey) || strings.Contains(events[2].RequestID, testAPIKey) {
		t.Fatalf("credential leaked in stream error: %#v", events[2])
	}
}

func TestAzureIncompleteBeforeEventRetriesButAfterEventDoesNot(t *testing.T) {
	t.Run("before event", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if calls.Add(1) == 1 {
				return
			}
			writeCompleted(writer, "resp_retry")
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{maxRetries: 1}, noWaitOptions())
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		events, err := Drain(stream)
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 || len(events) != 3 || events[2].Type != EventResponseCompleted {
			t.Fatalf("calls=%d events=%v", calls.Load(), eventTypes(events))
		}
	})

	t.Run("after event", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			writeSSE(t, writer, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_partial", "status": "in_progress", "output": []any{}}})
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{maxRetries: 3}, noWaitOptions())
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		event, err := stream.Next()
		if err != nil || event.Type != EventResponseCreated {
			t.Fatalf("first event = %#v, %v", event, err)
		}
		_, err = stream.Next()
		if !errors.Is(err, ErrIncompleteStream) {
			t.Fatalf("second Next error = %v, want incomplete", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("request count = %d, want 1", calls.Load())
		}
		_ = stream.Close()
	})
}

func TestAzureHTTPRetryPolicy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch calls.Add(1) {
		case 1:
			writer.Header().Set("Retry-After", "3")
			writer.Header().Set("apim-request-id", "req-rate")
			writeAPIError(writer, http.StatusTooManyRequests, "rate_limit", "slow down")
		case 2:
			writeAPIError(writer, http.StatusInternalServerError, "server_error", "temporary")
		default:
			writeCompleted(writer, "resp_ok")
		}
	}))
	defer server.Close()
	var sleeps []time.Duration
	var retries []RetryInfo
	options := noWaitOptions()
	options.Sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}
	options.OnRetry = func(info RetryInfo) { retries = append(retries, info) }
	client := newTestClient(t, server.URL, configOverrides{maxRetries: 2}, options)
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("request count = %d, want 3", calls.Load())
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{3 * time.Second, time.Second}) {
		t.Fatalf("retry delays = %v", sleeps)
	}
	if len(retries) != 2 || retries[0].Attempt != 2 || retries[0].RequestID != "req-rate" || retries[1].Attempt != 3 {
		t.Fatalf("retry observations = %#v", retries)
	}
}

func TestAzureRetryAfterOverridesBackoffCapWithinTotalRetryWindow(t *testing.T) {
	var calls atomic.Int32
	var sleeps []time.Duration
	var retries []RetryInfo
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	options := AzureOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header: http.Header{
						"Content-Type": []string{"application/json"},
						"Retry-After":  []string{"100"},
					},
					Body: io.NopCloser(strings.NewReader(`{"error":{"code":"busy","message":"retry later"}}`)),
				}, nil
			}
			return streamingResponse("resp_retry_after"), nil
		})},
		RetryBase:    time.Second,
		RetryMaximum: 4 * time.Second,
		RetryWindow:  2 * time.Minute,
		Now:          func() time.Time { return now },
		Jitter:       func(time.Duration) time.Duration { return 0 },
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		OnRetry: func(info RetryInfo) { retries = append(retries, info) },
	}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 10}, options)
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || !reflect.DeepEqual(sleeps, []time.Duration{100 * time.Second}) {
		t.Fatalf("requests=%d retry sleeps=%v", calls.Load(), sleeps)
	}
	if len(retries) != 1 || retries[0].Delay != 100*time.Second {
		t.Fatalf("retry observations = %#v", retries)
	}

	dateFailure := &ProviderError{Retryable: true, retryDelay: time.Hour, hasRetryDelay: true}
	if delay := client.retryDelay(dateFailure, 1); delay != time.Hour {
		t.Fatalf("HTTP-date Retry-After delay = %s, want one hour", delay)
	}
}

func TestAzureRetryAfterCannotExceedTotalRetryWindow(t *testing.T) {
	var calls atomic.Int32
	var sleeps []time.Duration
	options := AzureOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Retry-After":  []string{"3600"},
				},
				Body: io.NopCloser(strings.NewReader(`{"error":{"code":"busy","message":"retry later"}}`)),
			}, nil
		})},
		RetryBase:    time.Second,
		RetryMaximum: 4 * time.Second,
		RetryWindow:  5 * time.Second,
		Jitter:       func(time.Duration) time.Duration { return 0 },
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
	}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 10}, options)
	_, err := client.Stream(context.Background(), basicRequest())
	var exhausted *RetryExhaustedError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 1 || exhausted.RetryWindow != 5*time.Second {
		t.Fatalf("Stream error = %#v, want first-attempt retry-window exhaustion", err)
	}
	if calls.Load() != 1 || len(sleeps) != 0 {
		t.Fatalf("requests=%d retry sleeps=%v, want no out-of-window retry", calls.Load(), sleeps)
	}
}

func TestAzureRedactsCompleteCredentialUnionSplitAtEveryVisibleDeltaBoundary(t *testing.T) {
	const prefix = "before "
	const suffix = " after"
	want := prefix + redact.Mask(testAPIKey, testSourceCredential) + suffix
	for split := 0; split <= len(testSourceCredential); split++ {
		t.Run(fmt.Sprintf("split_%d", split), func(t *testing.T) {
			client := &AzureClient{
				apiKey: testAPIKey, credentialSet: redact.New(testAPIKey, testSourceCredential),
				maximumResponseItems: 10, maximumToolCalls: 10,
				maximumCallArgumentBytes: 1024,
			}
			stream := &azureStream{
				client: client, lifecycle: responseInProgress, responseID: "resp_redacted",
				calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
			}
			var events []Event
			parse := func(envelope map[string]any) {
				t.Helper()
				payload, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				parsed, _, err := stream.parseRecord(sseRecord{data: payload})
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, parsed...)
			}
			parse(map[string]any{
				"type": "response.output_text.delta", "item_id": "msg_redacted",
				"output_index": 0, "content_index": 0, "delta": prefix + testSourceCredential[:split],
			})
			parse(map[string]any{
				"type": "response.output_text.delta", "item_id": "msg_redacted",
				"output_index": 0, "content_index": 0, "delta": testSourceCredential[split:] + suffix,
			})
			parse(map[string]any{"type": "response.completed", "response": map[string]any{
				"id": "resp_redacted", "status": "completed", "output": []any{map[string]any{
					"type": "message", "id": "msg_redacted", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": prefix + testSourceCredential + suffix}},
				}},
			}})

			var streamed strings.Builder
			var terminal *Response
			for _, event := range events {
				if event.Type == EventTextDelta {
					streamed.WriteString(event.Delta)
				}
				if event.Type == EventResponseCompleted {
					terminal = event.Response
				}
			}
			if got := streamed.String(); got != want {
				t.Fatalf("streamed output = %q, want %q", got, want)
			}
			if terminal == nil || len(terminal.Output) != 1 || terminal.Output[0].Content[0].Text != want {
				t.Fatalf("terminal response was not sanitized: %#v", terminal)
			}
			serialized, err := json.Marshal(events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(serialized), testAPIKey) || strings.Contains(string(serialized), testSourceCredential) {
				t.Fatalf("credential leaked from normalized events: %s", serialized)
			}
		})
	}
}

func TestAzureFlushesHarmlessCredentialPrefixAtTerminal(t *testing.T) {
	const partial = "test-api-key"
	client := &AzureClient{apiKey: testAPIKey, maximumResponseItems: 10, maximumToolCalls: 10, maximumCallArgumentBytes: 1024}
	stream := &azureStream{
		client: client, lifecycle: responseInProgress, responseID: "resp_partial",
		calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
	}
	parse := func(envelope map[string]any) []Event {
		t.Helper()
		payload, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		events, _, err := stream.parseRecord(sseRecord{data: payload})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	events := parse(map[string]any{
		"type": "response.output_text.delta", "item_id": "msg_partial",
		"output_index": 0, "content_index": 0, "delta": "ends " + partial,
	})
	events = append(events, parse(map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_partial", "status": "completed", "output": []any{map[string]any{
			"type": "message", "id": "msg_partial", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "ends " + partial}},
		}},
	}})...)
	var got strings.Builder
	for _, event := range events {
		if event.Type == EventTextDelta {
			got.WriteString(event.Delta)
		}
	}
	if got.String() != "ends "+partial {
		t.Fatalf("terminal flush = %q", got.String())
	}
}

func TestAzureRetryExhaustionAndNoRetryHeader(t *testing.T) {
	t.Run("exhaustion", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			writeAPIError(writer, http.StatusServiceUnavailable, "unavailable", "temporary")
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{maxRetries: 2}, noWaitOptions())
		_, err := client.Stream(context.Background(), basicRequest())
		var exhausted *RetryExhaustedError
		if !errors.As(err, &exhausted) || exhausted.Attempts != 3 || calls.Load() != 3 {
			t.Fatalf("Stream error = %#v, requests=%d", err, calls.Load())
		}
	})

	t.Run("x should retry false", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			writer.Header().Set("x-should-retry", "false")
			writeAPIError(writer, http.StatusInternalServerError, "fatal", "do not retry")
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{maxRetries: 5}, noWaitOptions())
		_, err := client.Stream(context.Background(), basicRequest())
		var providerError *ProviderError
		if !errors.As(err, &providerError) || providerError.Retryable || calls.Load() != 1 {
			t.Fatalf("Stream error = %#v, requests=%d", err, calls.Load())
		}
	})

	t.Run("x should retry true", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if calls.Add(1) == 1 {
				writer.Header().Set("x-should-retry", "true")
				writeAPIError(writer, http.StatusBadRequest, "transient", "try again")
				return
			}
			writeCompleted(writer, "resp_retried")
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{maxRetries: 1}, noWaitOptions())
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Drain(stream); err != nil || calls.Load() != 2 {
			t.Fatalf("Drain error = %v, requests=%d", err, calls.Load())
		}
	})
}

func TestAzureCancellationDuringRetryIsNeverRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writeAPIError(writer, http.StatusServiceUnavailable, "unavailable", "temporary")
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	options := noWaitOptions()
	options.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	client := newTestClient(t, server.URL, configOverrides{maxRetries: 5}, options)
	_, err := client.Stream(ctx, basicRequest())
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("Stream error = %v, requests=%d", err, calls.Load())
	}
}

func TestAzureCallerCancellationStopsActiveStream(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := newTestClient(t, server.URL, configOverrides{watchdog: time.Second, maxRetries: 5}, noWaitOptions())
	stream, err := client.Stream(ctx, basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	_, err = stream.Next()
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("Next error = %v, requests=%d", err, calls.Load())
	}
	_ = stream.Close()
}

func TestAzureAttemptDeadlineIsProviderTimeoutNotCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSE(t, writer, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_timeout", "status": "in_progress", "output": []any{}}})
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{watchdog: time.Second}, noWaitOptions())
	client.requestTimeout = 30 * time.Millisecond
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if event, err := stream.Next(); err != nil || event.Type != EventResponseCreated {
		t.Fatalf("created event = %#v, err=%v", event, err)
	}
	_, err = stream.Next()
	if !errors.Is(err, ErrRequestTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("attempt deadline error = %#v", err)
	}
}

func TestAzureWatchdogRetriesOnlyBeforeProviderEvent(t *testing.T) {
	t.Run("before event", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if calls.Add(1) == 1 {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(http.StatusOK)
				writer.(http.Flusher).Flush()
				<-request.Context().Done()
				return
			}
			writeCompleted(writer, "resp_after_watchdog")
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{watchdog: 20 * time.Millisecond, maxRetries: 1}, noWaitOptions())
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		events, err := Drain(stream)
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 || len(events) != 3 || events[2].Type != EventResponseCompleted {
			t.Fatalf("requests=%d events=%v", calls.Load(), eventTypes(events))
		}
	})

	t.Run("after event", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			writeSSE(t, writer, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_hang", "status": "in_progress", "output": []any{}}})
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, configOverrides{watchdog: 20 * time.Millisecond, maxRetries: 3}, noWaitOptions())
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		if event, err := stream.Next(); err != nil || event.Type != EventResponseCreated {
			t.Fatalf("first event = %#v, %v", event, err)
		}
		if _, err := stream.Next(); !errors.Is(err, ErrStreamWatchdog) {
			t.Fatalf("second Next error = %v, want watchdog", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("request count = %d, want 1", calls.Load())
		}
		_ = stream.Close()
	})
}

func TestAzureSecretsAreRedactedFromFormattingAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("apim-request-id", "request-safe")
		writeAPIError(writer, http.StatusBadRequest, "bad_"+testAPIKey, "message "+testAPIKey)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{}, noWaitOptions())
	for _, formatted := range []string{fmt.Sprint(client), fmt.Sprintf("%+v", client), fmt.Sprintf("%#v", client)} {
		if strings.Contains(formatted, testAPIKey) {
			t.Fatalf("credential leaked from client formatting: %s", formatted)
		}
	}
	_, err := client.Stream(context.Background(), basicRequest())
	if err == nil || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("credential leaked from error: %v", err)
	}
	var providerError *ProviderError
	if !errors.As(err, &providerError) || strings.Contains(providerError.RequestID, testAPIKey) {
		t.Fatalf("provider error = %#v", providerError)
	}
}

func TestAzureRedirectDoesNotReplayAPIKeyOrBody(t *testing.T) {
	var attackerCalls atomic.Int32
	attacker := newLoopbackTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attackerCalls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if request.Header.Get("api-key") != "" || len(body) != 0 {
			t.Errorf("redirect leaked api-key=%q body=%q", request.Header.Get("api-key"), body)
		}
		writeCompleted(writer, "attacker-response")
	}))
	defer attacker.Close()

	var originCalls atomic.Int32
	origin := newLoopbackTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		originCalls.Add(1)
		if request.Header.Get("api-key") != testAPIKey {
			t.Errorf("origin api-key = %q", request.Header.Get("api-key"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("origin request body = %q", body)
		}
		writer.Header().Set("Location", attacker.URL+"/collect")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	var callerRedirects atomic.Int32
	provided := &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			callerRedirects.Add(1)
			return nil
		},
	}
	client := newTestClient(t, origin.URL, configOverrides{maxRetries: 1}, AzureOptions{HTTPClient: provided})
	if client.httpClient == provided || client.httpClient.Transport != provided.Transport || client.httpClient.Timeout != provided.Timeout {
		t.Fatal("Azure client did not preserve caller transport semantics in a private client copy")
	}
	_, err := client.Stream(context.Background(), basicRequest())
	if err == nil {
		t.Fatal("redirect response was accepted")
	}
	if originCalls.Load() != 1 || attackerCalls.Load() != 0 || callerRedirects.Load() != 0 {
		t.Fatalf("origin=%d attacker=%d caller_redirect_callback=%d", originCalls.Load(), attackerCalls.Load(), callerRedirects.Load())
	}
	if provided.CheckRedirect == nil {
		t.Fatal("caller HTTP client was mutated")
	}
}

func TestAzureRedirectPolicyClonesClientAndFailsClosed(t *testing.T) {
	var callerRedirects atomic.Int32
	provided := &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   17 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			callerRedirects.Add(1)
			return nil
		},
	}
	protected := azureRedirectSafeClient(provided)
	request, err := http.NewRequest(http.MethodPost, "https://attacker.invalid/collect", strings.NewReader("secret body"))
	if err != nil {
		t.Fatal(err)
	}
	if err := protected.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", err)
	}
	if protected == provided || protected.Transport != provided.Transport || protected.Timeout != provided.Timeout {
		t.Fatal("redirect-safe client did not clone while preserving transport semantics")
	}
	if callerRedirects.Load() != 0 || provided.CheckRedirect == nil {
		t.Fatal("caller redirect behavior was invoked or mutated")
	}
}

func newLoopbackTestServer(t *testing.T, handler http.Handler) *httptest.Server {
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

func TestAzureBodyErrorsAreRedactedBeforeRetryObservation(t *testing.T) {
	var calls atomic.Int32
	var observed []RetryInfo
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &errorBody{err: errors.New("read failed with " + testAPIKey)},
		}, nil
	})}
	options.OnRetry = func(info RetryInfo) { observed = append(observed, info) }
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Next()
	if err == nil || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("body error was not safely returned: %v", err)
	}
	if calls.Load() != 2 || len(observed) != 1 || strings.Contains(observed[0].Error.Error(), testAPIKey) {
		t.Fatalf("requests=%d retry observations=%#v", calls.Load(), observed)
	}
	_ = stream.Close()
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "seconds", value: "9", want: 9 * time.Second, ok: true},
		{name: "zero", value: "0", want: 0, ok: true},
		{name: "date", value: now.Add(11 * time.Second).Format(http.TimeFormat), want: 11 * time.Second, ok: true},
		{name: "past date", value: now.Add(-time.Minute).Format(http.TimeFormat), want: 0, ok: true},
		{name: "negative", value: "-1"},
		{name: "invalid", value: "soon"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseRetryAfter(test.value, now)
			if got != test.want || ok != test.ok {
				t.Fatalf("parseRetryAfter() = %v, %v; want %v, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

type configOverrides struct {
	watchdog   time.Duration
	maxRetries int
}

func newTestClient(t *testing.T, serverURL string, overrides configOverrides, options AzureOptions) *AzureClient {
	t.Helper()
	endpoint, err := url.Parse(serverURL + "/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	watchdog := 250 * time.Millisecond
	if overrides.watchdog > 0 {
		watchdog = overrides.watchdog
	}
	maxRetries := overrides.maxRetries
	if maxRetries == 0 {
		maxRetries = 2
	}
	configuration := config.Azure{
		Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "configured-deployment",
		APIKey: testAPIKey, APIVersion: "2026-07-01-preview", ReasoningEffort: "high",
		RequestTimeout: 2 * time.Second, StreamWatchdog: watchdog, MaxRetries: maxRetries,
		UnsafeAllowInsecureLoopbackForTesting: true,
	}
	client, err := NewAzureClient(configuration, options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func noWaitOptions() AzureOptions {
	return AzureOptions{
		Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep: func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
	}
}

func basicRequest() Request {
	return Request{Input: []Item{TextMessage(RoleUser, "hello")}}
}

func writeSSE(t *testing.T, writer http.ResponseWriter, events ...map[string]any) {
	t.Helper()
	writer.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Errorf("marshal SSE event: %v", err)
			return
		}
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event["type"], payload); err != nil {
			return
		}
	}
}

func writeCompleted(writer http.ResponseWriter, responseID string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "in_progress", "output": []any{}}},
		{"type": "response.in_progress", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "in_progress", "output": []any{}}},
		{"type": "response.completed", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "completed", "output": []any{}, "usage": map[string]any{}}},
	} {
		payload, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", payload)
	}
}

func streamingResponse(responseID string) *http.Response {
	var body strings.Builder
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "in_progress", "output": []any{}}},
		{"type": "response.in_progress", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "in_progress", "output": []any{}}},
		{"type": "response.completed", "response": map[string]any{"id": responseID, "model": "gpt-5.6-sol", "status": "completed", "output": []any{}, "usage": map[string]any{}}},
	} {
		payload, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&body, "data: %s\n\n", payload)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body.String())),
	}
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "type": "api_error"}})
}

func eventTypes(events []Event) []EventType {
	types := make([]EventType, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}

func assertNestedValue(t *testing.T, object map[string]any, path []string, want any) {
	t.Helper()
	var current any = object
	for _, component := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%v is not an object while looking for %v", current, path)
		}
		current = mapping[component]
	}
	if !reflect.DeepEqual(current, want) {
		t.Fatalf("value at %v = %#v, want %#v", path, current, want)
	}
}

func TestStreamCloseIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{watchdog: time.Second}, noWaitOptions())
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Next(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Next() after Close = %v, want ErrClosed", err)
	}
}

func TestAzureStreamEnforcesAggregateAndFunctionArgumentLimits(t *testing.T) {
	client := &AzureClient{
		maximumResponseBytes: 64, maximumResponseEvents: 10, maximumResponseItems: 2,
		maximumToolCalls: 2, maximumCallArgumentBytes: 4, watchdog: time.Second,
	}
	stream := &azureStream{client: client, lifecycle: responseInProgress, responseID: "resp", calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item)}
	added, _ := json.Marshal(azureEventEnvelope{Type: "response.output_item.added", ItemID: "item", Item: azureResponseItem{Type: string(ItemFunctionCall), ID: "item", CallID: "call", Name: "Read"}})
	if _, _, err := stream.parseRecord(sseRecord{data: added}); err != nil {
		t.Fatal(err)
	}
	delta, _ := json.Marshal(azureEventEnvelope{Type: "response.function_call_arguments.delta", ItemID: "item", Delta: "12345"})
	if _, _, err := stream.parseRecord(sseRecord{data: delta}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized call arguments = %v", err)
	}

	client.maximumResponseItems = 1
	completed, _ := json.Marshal(azureEventEnvelope{Type: "response.completed", Response: azureResponse{ID: "resp", Status: "completed", Output: []azureResponseItem{{Type: string(ItemMessage)}, {Type: string(ItemMessage)}}}})
	if _, _, err := stream.parseRecord(sseRecord{data: completed}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized output item list = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	records := make(chan sseRead, 2)
	first := []byte(`{"type":"future.one"}`)
	second := []byte(`{"type":"future.two"}`)
	records <- sseRead{record: sseRecord{data: first}}
	records <- sseRead{record: sseRecord{data: second}}
	close(records)
	aggregate := &azureStream{
		client: &AzureClient{maximumResponseBytes: len(first) + len(second) - 1, maximumResponseEvents: 10, watchdog: time.Second},
		ctx:    ctx, cancel: cancel, records: records, calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
	}
	if _, err := aggregate.Next(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("aggregate response limit = %v", err)
	}
}

func TestAzureMalformedSSEIsProtocolErrorWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {not-json}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{maxRetries: 4}, noWaitOptions())
	stream, err := client.Stream(context.Background(), basicRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Next()
	if !errors.Is(err, ErrProtocol) || calls.Load() != 1 {
		t.Fatalf("Next error = %v, requests=%d", err, calls.Load())
	}
	_ = stream.Close()
}

func TestAzureLifecycleBindsOneResponseIDAndRequiresInProgress(t *testing.T) {
	newStream := func() *azureStream {
		return &azureStream{
			client: &AzureClient{
				maximumResponseItems: 10, maximumToolCalls: 10,
				maximumCallArgumentBytes: 1024,
			},
			calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
		}
	}
	parse := func(stream *azureStream, event map[string]any) error {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = stream.parseRecord(sseRecord{data: payload})
		return err
	}
	created := map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_one", "status": "in_progress", "output": []any{}}}
	inProgress := map[string]any{"type": "response.in_progress", "response": map[string]any{"id": "resp_one", "status": "in_progress", "output": []any{}}}
	completed := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_one", "status": "completed", "output": []any{}}}

	stream := newStream()
	if err := parse(stream, completed); !errors.Is(err, ErrProtocol) {
		t.Fatalf("terminal before created = %v", err)
	}
	stream = newStream()
	if err := parse(stream, created); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, completed); !errors.Is(err, ErrProtocol) {
		t.Fatalf("terminal before in-progress = %v", err)
	}
	stream = newStream()
	if err := parse(stream, created); err != nil {
		t.Fatal(err)
	}
	changed := map[string]any{"type": "response.in_progress", "response": map[string]any{"id": "resp_other", "status": "in_progress", "output": []any{}}}
	if err := parse(stream, changed); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "response id changed") {
		t.Fatalf("changed response id = %v", err)
	}
	stream = newStream()
	if err := parse(stream, created); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, inProgress); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, completed); err != nil {
		t.Fatalf("coherent lifecycle: %v", err)
	}
}

func TestAzureRejectsUnsafeOpaqueProviderMetadataBeforeEmission(t *testing.T) {
	validTerminal := func() map[string]any {
		return map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_safe", "model": "gpt-5.6-sol", "status": "completed",
				"output": []any{map[string]any{
					"type": "message", "id": "msg_safe", "role": "assistant", "status": "completed",
					"phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "safe"}},
				}},
			},
		}
	}
	tests := map[string]func(map[string]any){
		"event type":           func(event map[string]any) { event["type"] = testAPIKey },
		"event item id":        func(event map[string]any) { event["item_id"] = testAPIKey },
		"response id":          func(event map[string]any) { event["response"].(map[string]any)["id"] = testAPIKey },
		"response model":       func(event map[string]any) { event["response"].(map[string]any)["model"] = testAPIKey },
		"response status":      func(event map[string]any) { event["response"].(map[string]any)["status"] = testAPIKey },
		"previous response id": func(event map[string]any) { event["response"].(map[string]any)["previous_response_id"] = testAPIKey },
		"item id": func(event map[string]any) {
			event["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["id"] = testAPIKey
		},
		"item type": func(event map[string]any) {
			event["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["type"] = testAPIKey
		},
		"item status": func(event map[string]any) {
			event["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["status"] = testAPIKey
		},
		"item phase": func(event map[string]any) {
			event["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["phase"] = testAPIKey
		},
		"content type": func(event map[string]any) {
			event["response"].(map[string]any)["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] = testAPIKey
		},
		"encrypted content": func(event map[string]any) {
			event["response"].(map[string]any)["output"] = []any{map[string]any{
				"type": "reasoning", "id": "reason_safe", "encrypted_content": testAPIKey,
			}}
		},
		"call id": func(event map[string]any) {
			event["response"].(map[string]any)["output"] = []any{map[string]any{
				"type": "function_call", "id": "call_item_safe", "call_id": testAPIKey, "name": "Read", "arguments": "{}", "status": "completed",
			}}
		},
		"tool name": func(event map[string]any) {
			event["response"].(map[string]any)["output"] = []any{map[string]any{
				"type": "function_call", "id": "call_item_safe", "call_id": "call_safe", "name": testAPIKey, "arguments": "{}", "status": "completed",
			}}
		},
		"bidi event item id": func(event map[string]any) { event["item_id"] = "safe\u202eevil" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validTerminal()
			mutate(event)
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			stream := &azureStream{
				client:    &AzureClient{apiKey: testAPIKey, maximumResponseItems: 10, maximumToolCalls: 10, maximumCallArgumentBytes: 1024},
				lifecycle: responseInProgress, responseID: "resp_safe", calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
			}
			events, _, err := stream.parseRecord(sseRecord{data: payload})
			if !errors.Is(err, ErrProtocol) || len(events) != 0 {
				t.Fatalf("unsafe metadata result: events=%#v err=%v", events, err)
			}
			if strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), "safe\u202eevil") {
				t.Fatalf("protocol error exposed unsafe provider metadata: %q", err)
			}
		})
	}
}

func TestAzureRejectsUnsafeRequestIDBeforeInstallingStream(t *testing.T) {
	for _, requestID := range []string{testAPIKey, "request\u202eevil", "request\x1bevil"} {
		client := &AzureClient{apiKey: testAPIKey}
		stream := &azureStream{client: client, ctx: t.Context()}
		header := make(http.Header)
		header.Set("apim-request-id", requestID)
		response := &http.Response{
			Header: header,
			Body:   io.NopCloser(strings.NewReader("")),
		}
		err := stream.installAttempt(response, func() {})
		if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), requestID) || strings.Contains(err.Error(), testAPIKey) {
			t.Fatalf("request id %q error = %v", requestID, err)
		}
	}
}

func TestAzureProviderErrorsCannotReassembleCredentialAcrossFields(t *testing.T) {
	client := &AzureClient{apiKey: testAPIKey, maximumErrorBytes: 1 << 20}
	split := len(testAPIKey) / 2
	body, err := json.Marshal(map[string]any{"error": map[string]any{
		"code": testAPIKey[:split], "message": testAPIKey[split:], "type": "unsafe\u202emetadata", "param": "bad\x1bvalue",
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	decoded := client.decodeHTTPError(context.Background(), response)
	var providerError *ProviderError
	if !errors.As(decoded, &providerError) {
		t.Fatalf("decoded error = %T %v", decoded, decoded)
	}
	fields := providerError.Code + providerError.Type + providerError.Param + providerError.Message + providerError.RequestID
	if strings.Contains(fields, testAPIKey) || strings.Contains(fields, testAPIKey[:split]) || strings.Contains(fields, testAPIKey[split:]) {
		t.Fatalf("HTTP provider error retained credential fragments: %#v", providerError)
	}
	if strings.ContainsAny(fields, "\x1b\r\n") || strings.Contains(fields, "\u202e") {
		t.Fatalf("HTTP provider error retained unsafe controls: %#v", providerError)
	}

	stream := &azureStream{client: client, requestID: "request_safe"}
	streamError := stream.eventError(nil, azureEventEnvelope{
		Code: testAPIKey[:split], Message: testAPIKey[split:], Param: "bad\x1bvalue",
	}, "stream_error")
	streamFields := streamError.Code + streamError.Type + streamError.Param + streamError.Message + streamError.RequestID
	if strings.Contains(streamFields, testAPIKey) || strings.Contains(streamFields, testAPIKey[:split]) || strings.Contains(streamFields, testAPIKey[split:]) {
		t.Fatalf("stream provider error retained credential fragments: %#v", streamError)
	}
	if strings.ContainsAny(streamFields, "\x1b\r\n") || strings.Contains(streamFields, "\u202e") {
		t.Fatalf("stream provider error retained unsafe controls: %#v", streamError)
	}

	controls, _ := client.sanitizeProviderErrorFields(azureError{
		Code: "safe", Type: "type\u202eoverride", Param: "bad\x1bvalue", Message: "line\nfeed",
	}, "request_safe")
	controlFields := controls.Code + controls.Type + controls.Param + controls.Message
	if strings.ContainsAny(controlFields, "\x1b\r\n") || strings.Contains(controlFields, "\u202e") {
		t.Fatalf("provider error retained controls without credential reflection: %#v", controls)
	}
}

func TestAzureProviderDiagnosticRedactsBeforeBounding(t *testing.T) {
	client := &AzureClient{apiKey: testAPIKey}
	prefixBytes := 7
	message := strings.Repeat("x", maximumProviderDiagnosticBytes-prefixBytes) + testAPIKey + " trailing"
	fields, _ := client.sanitizeProviderErrorFields(azureError{Message: message}, "")
	if len(fields.Message) > maximumProviderDiagnosticBytes {
		t.Fatalf("bounded provider message length = %d", len(fields.Message))
	}
	if strings.Contains(fields.Message, testAPIKey) || strings.Contains(fields.Message, testAPIKey[:prefixBytes]) {
		t.Fatalf("truncate-before-redact retained credential prefix at boundary")
	}

	split := len(testAPIKey) / 2
	code := strings.Repeat("x", maximumProviderDiagnosticBytes-split) + testAPIKey[:split] + strings.Repeat("z", len(testAPIKey))
	fields, _ = client.sanitizeProviderErrorFields(azureError{Code: code, Message: testAPIKey[split:]}, "")
	exported := fields.Code + fields.Type + fields.Param + fields.Message
	if strings.Contains(exported, testAPIKey) || strings.Contains(exported, testAPIKey[:split]) || strings.Contains(exported, testAPIKey[split:]) {
		t.Fatalf("post-bound provider fields reconstructed credential")
	}
}

func TestAzureProviderDiagnosticRedactsBeforeUnicodeNormalization(t *testing.T) {
	for _, secret := range []string{
		"credential\u200bwith-format",
		string([]byte{'c', 'r', 'e', 'd', 0xff, 'e', 'n', 't', 'i', 'a', 'l'}),
	} {
		client := &AzureClient{apiKey: secret}
		safe := client.sanitizeProviderDiagnostic("prefix " + secret + " suffix")
		if strings.Contains(safe, secret) || strings.Contains(safe, "\uFFFD") {
			t.Fatalf("normalized provider diagnostic retained a credential alias: %q", safe)
		}
		fields, requestID := client.sanitizeProviderErrorFields(
			azureError{Code: "prefix " + secret[:len(secret)/2], Message: secret[len(secret)/2:] + " suffix"},
			"",
		)
		exported := fields.Code + fields.Type + fields.Param + fields.Message + requestID
		if strings.Contains(exported, secret) || strings.Contains(exported, "\uFFFD") {
			t.Fatalf("normalized structured provider error retained a credential alias: %#v", fields)
		}
	}
}

func TestAzureWithholdsUnvalidatedArgumentDeltasAndRedactsReasoningDeltas(t *testing.T) {
	client := &AzureClient{
		apiKey: testAPIKey, credentialSet: redact.New(testAPIKey, testSourceCredential),
		maximumResponseItems: 10, maximumToolCalls: 10, maximumCallArgumentBytes: 4096,
	}
	stream := &azureStream{
		client: client, lifecycle: responseInProgress, responseID: "resp_safe",
		calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
	}
	parse := func(event map[string]any) []Event {
		t.Helper()
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		events, _, err := stream.parseRecord(sseRecord{data: payload})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	parse(map[string]any{"type": "response.output_item.added", "item_id": "fc_safe", "item": map[string]any{
		"type": "function_call", "id": "fc_safe", "call_id": "call_safe", "name": "Read", "status": "in_progress",
	}})
	split := len(testSourceCredential) / 2
	var events []Event
	events = append(events, parse(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_safe", "delta": testSourceCredential[:split]})...)
	events = append(events, parse(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_safe", "delta": testSourceCredential[split:]})...)
	events = append(events, parse(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "reason_safe", "delta": testSourceCredential[:split]})...)
	events = append(events, parse(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "reason_safe", "delta": testSourceCredential[split:]})...)

	var arguments, reasoning strings.Builder
	for _, event := range events {
		if event.Call != nil {
			t.Fatalf("argument delta exposed cumulative raw call: %#v", event.Call)
		}
		switch event.Type {
		case EventFunctionCallArgumentsDelta:
			arguments.WriteString(event.Delta)
		case EventReasoningDelta:
			reasoning.WriteString(event.Delta)
		}
	}
	want := redact.Mask(testAPIKey, testSourceCredential)
	if arguments.String() != "" || reasoning.String() != want {
		t.Fatalf("projected deltas: arguments=%q reasoning=%q", arguments.String(), reasoning.String())
	}
	serialized, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), testAPIKey) || strings.Contains(string(serialized), testSourceCredential) {
		t.Fatalf("credential leaked from streamed deltas: %s", serialized)
	}
}

func TestAzureRedactsCredentialAcrossTerminalContentPartBoundaries(t *testing.T) {
	client := &AzureClient{apiKey: testAPIKey, credentialSet: redact.New(testAPIKey, testSourceCredential)}
	split := len(testSourceCredential) / 2
	item := Item{
		Type: ItemMessage, Role: RoleAssistant,
		Content: []Content{
			{Type: ContentOutputText, Text: "before " + testSourceCredential[:split]},
			{Type: ContentOutputText, Text: testSourceCredential[split:] + " after"},
		},
		Summary: []Content{
			{Type: ContentSummaryText, Text: testSourceCredential[:split]},
			{Type: ContentSummaryText, Text: testSourceCredential[split:]},
		},
	}
	safe := client.sanitizeItemText(item)
	var content, summary strings.Builder
	for _, part := range safe.Content {
		content.WriteString(part.Text)
	}
	for _, part := range safe.Summary {
		summary.WriteString(part.Text)
	}
	want := redact.Mask(testAPIKey, testSourceCredential)
	if content.String() != "before "+want+" after" || summary.String() != want {
		t.Fatalf("cross-part redaction: content=%q summary=%q", content.String(), summary.String())
	}
	serialized, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), testAPIKey) || strings.Contains(string(serialized), testSourceCredential) {
		t.Fatalf("terminal parts retained credential: %s", serialized)
	}
}

func TestAzureTerminalOutputMustMatchStreamedTextAndCalls(t *testing.T) {
	newStream := func() *azureStream {
		return &azureStream{
			client: &AzureClient{
				maximumResponseItems: 10, maximumToolCalls: 10,
				maximumCallArgumentBytes: 1024,
			},
			calls: make(map[string]*callAccumulator), completedCalls: make(map[string]Item),
		}
	}
	parse := func(stream *azureStream, event map[string]any) error {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = stream.parseRecord(sseRecord{data: payload})
		return err
	}
	start := func(stream *azureStream, id string) {
		if err := parse(stream, map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}); err != nil {
			t.Fatal(err)
		}
		if err := parse(stream, map[string]any{"type": "response.in_progress", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}); err != nil {
			t.Fatal(err)
		}
	}

	stream := newStream()
	start(stream, "resp_text")
	if err := parse(stream, map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": "streamed"}); err != nil {
		t.Fatal(err)
	}
	terminal := map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_text", "status": "completed", "output": []any{map[string]any{
			"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "different"}},
		}},
	}}
	if err := parse(stream, terminal); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "contradicts streamed") {
		t.Fatalf("contradictory terminal text = %v", err)
	}

	stream = newStream()
	start(stream, "resp_call")
	if err := parse(stream, map[string]any{"type": "response.output_item.added", "item_id": "fc_1", "item": map[string]any{
		"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "Read", "arguments": "",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_1", "name": "Read", "arguments": `{"path":"a"}`}); err != nil {
		t.Fatal(err)
	}
	terminal = map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_call", "status": "completed", "output": []any{map[string]any{
			"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "Read", "arguments": `{"path":"b"}`,
		}},
	}}
	if err := parse(stream, terminal); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "contradicts streamed call data") {
		t.Fatalf("contradictory terminal call = %v", err)
	}

	stream = newStream()
	start(stream, "resp_call_delta")
	if err := parse(stream, map[string]any{"type": "response.output_item.added", "item_id": "fc_delta", "item": map[string]any{
		"type": "function_call", "id": "fc_delta", "call_id": "call_delta", "name": "Read", "arguments": "",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_delta", "delta": `{"path":"a"}`}); err != nil {
		t.Fatal(err)
	}
	if err := parse(stream, map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_delta", "name": "Read", "arguments": `{"path":"b"}`}); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "contradict streamed deltas") {
		t.Fatalf("function-call done replaced streamed arguments: %v", err)
	}
}

func TestAzureRejectsNonEventStreamSuccessWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"not-a-stream"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, configOverrides{maxRetries: 4}, noWaitOptions())
	_, err := client.Stream(context.Background(), basicRequest())
	if !errors.Is(err, ErrProtocol) || calls.Load() != 1 {
		t.Fatalf("Stream error = %v, requests=%d", err, calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type errorBody struct {
	err error
}

func (body *errorBody) Read([]byte) (int, error) { return 0, body.err }
func (body *errorBody) Close() error             { return nil }
