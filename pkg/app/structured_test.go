package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/surface"
	"github.com/greenpau/agentx/pkg/tool"
)

type sdkWireProvider struct{}

func (sdkWireProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, errors.New("SDK wire test provider must not be called")
}

type sdkWireCapabilities struct{}

func (sdkWireCapabilities) Schemas() []model.Tool { return nil }
func (sdkWireCapabilities) Execute(context.Context, []engine.CapabilityCall) []engine.CapabilityResult {
	return nil
}

func newSDKWireSession(t *testing.T) *runtimeSession {
	t.Helper()
	query, err := engine.New(engine.Config{
		SessionID: "ses_sdk_wire", Model: "gpt-5.6-sol", Provider: sdkWireProvider{}, Capabilities: sdkWireCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeSession{
		engine: query, registry: registry, workspace: "/work/project",
		config: config.Runtime{
			Azure:      config.Azure{ModelName: "gpt-5.6-sol"},
			Provenance: map[string]config.Source{"AZURE_OPENAI_SUBSCRIPTION_KEY": config.SourceFile},
		},
	}
}

func TestInputQueueIsCountAndByteBounded(t *testing.T) {
	queue := newInputQueue()
	message, _ := json.Marshal("small")
	for index := 0; index < maximumQueuedInputs; index++ {
		if err := queue.push(surface.InputEnvelope{Type: "user", Message: message}); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	if err := queue.push(surface.InputEnvelope{Type: "user", Message: message}); !errors.Is(err, errStructuredQueueFull) {
		t.Fatalf("count overflow = %v", err)
	}

	oversized := newInputQueue()
	if err := oversized.push(surface.InputEnvelope{Type: "user", Message: make([]byte, maximumQueuedInputBytes+1)}); !errors.Is(err, errStructuredQueueFull) {
		t.Fatalf("byte overflow = %v", err)
	}
}

func TestInputQueueAccountsCompleteEnvelopeAtByteBoundary(t *testing.T) {
	parent := strings.Repeat("parent-", 64)
	item := surface.InputEnvelope{
		Type: "user", UUID: "queue-boundary", SessionID: "ses_queue",
		Message: json.RawMessage(`"safe"`), ParentToolUseID: &parent,
		Priority: "next", Timestamp: strings.Repeat("timestamp-", 64),
	}
	base := inputEnvelopeSize(item)
	if base >= maximumQueuedInputBytes {
		t.Fatalf("boundary fixture base = %d", base)
	}
	payloadLength := maximumQueuedInputBytes - base
	payload := bytes.Repeat([]byte{'x'}, payloadLength)
	if payloadLength >= 2 {
		payload[0], payload[payloadLength-1] = '"', '"'
	}
	item.ToolUseResult = json.RawMessage(payload)
	if got := inputEnvelopeSize(item); got != maximumQueuedInputBytes {
		t.Fatalf("complete envelope size = %d, want %d", got, maximumQueuedInputBytes)
	}

	queue := newInputQueue()
	if err := queue.push(item); err != nil {
		t.Fatalf("exact-boundary envelope = %v", err)
	}
	if queue.bytes != maximumQueuedInputBytes {
		t.Fatalf("charged queue bytes = %d", queue.bytes)
	}
	oversized := item
	oversized.Timestamp += "x"
	if err := newInputQueue().push(oversized); !errors.Is(err, errStructuredQueueFull) {
		t.Fatalf("one-byte oversized envelope = %v", err)
	}
	dequeued, err := queue.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if dequeued.ParentToolUseID == nil || *dequeued.ParentToolUseID != parent ||
		dequeued.Timestamp != item.Timestamp || len(dequeued.ToolUseResult) != payloadLength {
		t.Fatalf("dequeued complete envelope = %#v", dequeued)
	}
	if queue.bytes != 0 {
		t.Fatalf("dequeue left %d charged bytes", queue.bytes)
	}
}

func TestInputQueueRejectsAdversarialHugeToolResultFanout(t *testing.T) {
	payloadLength := surface.MaxNDJSONRecordBytes - (4 << 10)
	payload := bytes.Repeat([]byte{'x'}, payloadLength)
	payload[0], payload[payloadLength-1] = '"', '"'
	parent := strings.Repeat("p", 512)
	item := surface.InputEnvelope{
		Type: "user", UUID: "huge-result", Message: json.RawMessage(`"safe"`),
		ParentToolUseID: &parent, ToolUseResult: json.RawMessage(payload),
		Timestamp: strings.Repeat("t", 512),
	}
	queue := newInputQueue()
	accepted := 0
	for index := 0; index < maximumQueuedInputs; index++ {
		item.UUID = fmt.Sprintf("huge-result-%d", index)
		err := queue.push(item)
		if errors.Is(err, errStructuredQueueFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted != 2 || len(queue.items) != accepted {
		t.Fatalf("huge envelopes accepted=%d retained=%d, want 2", accepted, len(queue.items))
	}
	if queue.bytes > maximumQueuedInputBytes {
		t.Fatalf("huge envelope queue charged %d bytes", queue.bytes)
	}
}

func TestInputQueueChargesOriginalWireEnvelope(t *testing.T) {
	wire := []byte(fmt.Sprintf(
		`{"type":"user","message":"safe","parent_tool_use_id":"parent","tool_use_result":"%s","timestamp":"origin"}`,
		strings.Repeat("x", 1<<20),
	))
	var envelope surface.InputEnvelope
	if err := json.Unmarshal(wire, &envelope); err != nil {
		t.Fatal(err)
	}
	if got := envelope.OriginalByteSize(); got != len(wire) {
		t.Fatalf("original byte size = %d, want %d", got, len(wire))
	}
	if got := inputEnvelopeSize(envelope); got < len(wire)+256 {
		t.Fatalf("queue charge = %d, want at least %d", got, len(wire)+256)
	}
}

func TestInputQueueRejectsWritesAfterClose(t *testing.T) {
	queue := newInputQueue()
	queue.close(nil)
	if err := queue.push(surface.InputEnvelope{Type: "user", Message: json.RawMessage(`"late"`)}); !errors.Is(err, surface.ErrClosed) {
		t.Fatalf("push after close = %v", err)
	}
}

func TestInputQueuePriorityOrderingIsStable(t *testing.T) {
	queue := newInputQueue()
	for _, item := range []struct {
		id       string
		priority string
	}{
		{id: "later-1", priority: "later"},
		{id: "next-1", priority: "next"},
		{id: "now-1", priority: "now"},
		{id: "next-2"}, // omitted priority defaults to next
		{id: "now-2", priority: "now"},
		{id: "later-2", priority: "later"},
	} {
		message, _ := json.Marshal(item.id)
		if err := queue.push(surface.InputEnvelope{Type: "user", UUID: item.id, Priority: item.priority, Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range []string{"now-1", "now-2", "next-1", "next-2", "later-1", "later-2"} {
		got, err := queue.next(context.Background())
		if err != nil {
			t.Fatalf("next %d: %v", index, err)
		}
		if got.UUID != want {
			t.Fatalf("next %d = %q, want %q", index, got.UUID, want)
		}
	}
}

func TestPriorityNowInterruptsOnlyAfterQueueAdmission(t *testing.T) {
	queue := newInputQueue()
	active := &activeTurn{}
	turnContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !active.set(cancel) {
		t.Fatal("active turn unexpectedly rejected")
	}
	message, _ := json.Marshal("urgent")
	if err := enqueueStructuredUser(queue, active, surface.InputEnvelope{Type: "user", UUID: "urgent", Priority: "now", Message: message}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-turnContext.Done():
	default:
		t.Fatal("priority-now input did not interrupt the active turn")
	}
	queued, err := queue.next(context.Background())
	if err != nil || queued.UUID != "urgent" {
		t.Fatalf("admitted urgent input = %#v, %v", queued, err)
	}

	closed := newInputQueue()
	closed.close(nil)
	secondContext, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	second := &activeTurn{}
	if !second.set(secondCancel) {
		t.Fatal("second active turn unexpectedly rejected")
	}
	if err := enqueueStructuredUser(closed, second, surface.InputEnvelope{Type: "user", UUID: "rejected", Priority: "now", Message: message}); !errors.Is(err, surface.ErrClosed) {
		t.Fatalf("closed-queue admission = %v", err)
	}
	select {
	case <-secondContext.Done():
		t.Fatal("rejected priority-now input cancelled active work")
	default:
	}
}

func TestSDKInitCarriesRequiredConfiguredModelMetadata(t *testing.T) {
	var output bytes.Buffer
	if err := encodeSDKInit(surface.NewEncoder(&output), newSDKWireSession(t), cli.Options{}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "system" || record["subtype"] != "init" || record["model"] != "gpt-5.6-sol" || record["permissionMode"] != "default" {
		t.Fatalf("init identity = %#v", record)
	}
	if record["apiKeySource"] != "project" || record["agentx_version"] != Version || record["cwd"] != "/work/project" || record["session_id"] != "ses_sdk_wire" {
		t.Fatalf("init metadata = %#v", record)
	}
	for _, key := range []string{"tools", "mcp_servers", "slash_commands", "output_style", "skills", "plugins", "uuid"} {
		if _, exists := record[key]; !exists {
			t.Fatalf("init missing %s: %#v", key, record)
		}
	}
	if _, leaked := record["protocol_version"]; leaked {
		t.Fatalf("non-schema init member leaked: %#v", record)
	}
}

func TestInitializeControlUsesCorrelatedPublishedResponseShape(t *testing.T) {
	request, err := surface.NewControlRequest("initialize", nil)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := handleControl(surface.InputEnvelope{Type: "control_request", RequestID: "req_init", Request: &request}, surface.NewEncoder(&output), surface.NewControlBroker(), &activeTurn{}, newSDKWireSession(t)); err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if len(record["request_id"]) != 0 || string(record["type"]) != `"control_response"` {
		t.Fatalf("outer response = %s", output.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(record["response"], &response); err != nil {
		t.Fatal(err)
	}
	if string(response["subtype"]) != `"success"` || string(response["request_id"]) != `"req_init"` {
		t.Fatalf("response body = %s", record["response"])
	}
	var payload map[string]any
	if err := json.Unmarshal(response["response"], &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"commands", "agents", "output_style", "available_output_styles", "models", "account", "pid"} {
		if _, exists := payload[key]; !exists {
			t.Fatalf("initialize payload missing %s: %#v", key, payload)
		}
	}
}

func TestSDKResultAndSessionStateUsePublishedDiscriminators(t *testing.T) {
	var output bytes.Buffer
	encoder := surface.NewEncoder(&output)
	outcome := engine.Outcome{
		SessionID: "ses_wire", TurnID: "turn_wire", Status: protocol.TurnResultCancelled,
		StopReason: "cancelled", ModelTurns: 2, Duration: 1500 * time.Millisecond, APIDuration: 725 * time.Millisecond,
		Usage: protocol.Usage{Model: "gpt-5.6-sol", InputTokens: 7, CachedInputTokens: 2, OutputTokens: 3, ReasoningTokens: 1, TotalTokens: 10},
	}
	if err := encodeSDKResult(encoder, outcome, context.Canceled); err != nil {
		t.Fatal(err)
	}
	if err := encodeSessionState(encoder, outcome.SessionID, "idle"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%q", lines)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &result); err != nil {
		t.Fatal(err)
	}
	if result["type"] != "result" || result["subtype"] != "error_during_execution" || result["is_error"] != true {
		t.Fatalf("result envelope = %#v", result)
	}
	if _, exists := result["result"]; exists {
		t.Fatalf("error result included success text: %#v", result)
	}
	for _, key := range []string{"duration_ms", "duration_api_ms", "num_turns", "stop_reason", "total_cost_usd", "usage", "modelUsage", "permission_denials", "errors", "uuid", "session_id"} {
		if _, exists := result[key]; !exists {
			t.Fatalf("result missing %s: %#v", key, result)
		}
	}
	if result["duration_api_ms"] != float64(725) {
		t.Fatalf("API duration = %#v, want 725ms", result["duration_api_ms"])
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &state); err != nil {
		t.Fatal(err)
	}
	if state["subtype"] != "session_state_changed" || state["state"] != "idle" || state["uuid"] == "" {
		t.Fatalf("state envelope = %#v", state)
	}
}

type immediateReadFailure struct{ err error }

func (r immediateReadFailure) Read([]byte) (int, error) { return 0, r.err }
func (immediateReadFailure) Close() error               { return nil }

type hostileStructuredReaderError struct {
	calls   *atomic.Int32
	payload []byte
}

func (e hostileStructuredReaderError) Error() string {
	e.calls.Add(1)
	panic("reader error Error must not be called")
}

func (e hostileStructuredReaderError) Is(error) bool {
	e.calls.Add(1)
	panic("reader error Is must not be called")
}

func (e hostileStructuredReaderError) Unwrap() error {
	e.calls.Add(1)
	panic("reader error Unwrap must not be called")
}

type hostileStructuredErrorReader struct {
	errorCalls *atomic.Int32
}

func (r *hostileStructuredErrorReader) Read([]byte) (int, error) {
	return 0, hostileStructuredReaderError{calls: r.errorCalls, payload: []byte("uncomparable")}
}

func (*hostileStructuredErrorReader) Close() error {
	panic("reader Close callback panic")
}

type dataThenHostileStructuredErrorReader struct {
	errorCalls *atomic.Int32
}

func (r *dataThenHostileStructuredErrorReader) Read(buffer []byte) (int, error) {
	count := copy(buffer, []byte("{\"type\":\"user\",\"uuid\":\"must-not-run\",\"message\":\"unsafe\"}\n"))
	return count, hostileStructuredReaderError{calls: r.errorCalls, payload: []byte("uncomparable")}
}

func (*dataThenHostileStructuredErrorReader) Close() error { return nil }

type panickingStructuredReader struct {
	closeCalled chan struct{}
}

func (*panickingStructuredReader) Read([]byte) (int, error) {
	panic("reader Read callback panic")
}

func (r *panickingStructuredReader) Close() error {
	close(r.closeCalled)
	panic("reader Close callback panic")
}

func TestInitializedStructuredStreamsEmitTerminalResultForInputFailure(t *testing.T) {
	inputFailure := errors.New("synthetic structured input failure")
	hostileErrorCalls := &atomic.Int32{}
	panicReader := &panickingStructuredReader{closeCalled: make(chan struct{})}
	opaqueReadFailure := "read NDJSON input: " + errStructuredInputRead.Error()
	tests := []struct {
		name        string
		inputFormat cli.InputFormat
		input       io.Reader
		run         func(context.Context, cli.Options, string, io.Reader, io.Writer, io.Writer) error
		wantError   string
	}{
		{
			name: "duplex empty", inputFormat: cli.InputStreamJSON, input: strings.NewReader(""),
			run: runStructured,
		},
		{
			name: "duplex read failure", inputFormat: cli.InputStreamJSON, input: immediateReadFailure{err: inputFailure},
			run: runStructured,
		},
		{
			name: "duplex hostile error", inputFormat: cli.InputStreamJSON,
			input: &hostileStructuredErrorReader{errorCalls: hostileErrorCalls},
			run:   runStructured, wantError: opaqueReadFailure,
		},
		{
			name: "duplex data discarded on hostile error", inputFormat: cli.InputStreamJSON,
			input: &dataThenHostileStructuredErrorReader{errorCalls: hostileErrorCalls},
			run:   runStructured, wantError: opaqueReadFailure,
		},
		{
			name: "duplex read and close panic", inputFormat: cli.InputStreamJSON,
			input: panicReader,
			run:   runStructured, wantError: opaqueReadFailure,
		},
		{
			name: "one-shot empty", inputFormat: cli.InputText, input: strings.NewReader(""),
			run: runStructuredOneShot,
		},
		{
			name: "one-shot read failure", inputFormat: cli.InputText, input: immediateReadFailure{err: inputFailure},
			run: runStructuredOneShot,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			opts := buildTestCLIOptions(t, workspace, t.TempDir(), fmt.Sprintf("ses_input_terminal_%d", index))
			opts.OutputFormat = cli.OutputStreamJSON
			opts.InputFormat = test.inputFormat
			var output bytes.Buffer
			err := test.run(t.Context(), opts, workspace, test.input, &output, io.Discard)
			if err == nil {
				t.Fatalf("input failure returned nil; output=%s", output.String())
			}
			records := decodeNDJSONRecords(t, output.String())
			if len(records) != 2 || records[0]["type"] != "system" || records[0]["subtype"] != "init" {
				t.Fatalf("initialized failure records = %#v", records)
			}
			result := records[1]
			if result["type"] != "result" || result["subtype"] != "error_during_execution" ||
				result["is_error"] != true || result["stop_reason"] != "input_error" {
				t.Fatalf("terminal input result = %#v", result)
			}
			if errorsValue, ok := result["errors"].([]any); !ok || len(errorsValue) != 1 {
				t.Fatalf("terminal input errors = %#v", result["errors"])
			} else if test.wantError != "" && errorsValue[0] != test.wantError {
				t.Fatalf("terminal input error = %#v, want %q", errorsValue[0], test.wantError)
			}
		})
	}
	if got := hostileErrorCalls.Load(); got != 0 {
		t.Fatalf("hostile reader error methods called %d times", got)
	}
	select {
	case <-panicReader.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("panicking reader Close callback was not contained")
	}
}

type panickingSDKResultError struct {
	errorCalls  atomic.Int32
	isCalls     atomic.Int32
	unwrapCalls atomic.Int32
}

func (e *panickingSDKResultError) Error() string {
	e.errorCalls.Add(1)
	panic("SDK result Error callback panic")
}

func (e *panickingSDKResultError) Is(error) bool {
	e.isCalls.Add(1)
	panic("SDK result Is callback panic")
}

func (e *panickingSDKResultError) Unwrap() error {
	e.unwrapCalls.Add(1)
	panic("SDK result Unwrap callback panic")
}

func TestSDKResultContainsPanickingOperationalErrorProjection(t *testing.T) {
	callbackErr := &panickingSDKResultError{}
	record, err := sdkResultRecord(
		engine.Outcome{
			SessionID: "ses_panicking_result",
			Status:    protocol.TurnResultError,
			Usage:     protocol.Usage{Model: "gpt-5.6-sol"},
		},
		callbackErr,
	)
	if err != nil {
		t.Fatal(err)
	}
	errorsValue, ok := record["errors"].([]string)
	if !ok || len(errorsValue) != 1 || errorsValue[0] != "operation failed" {
		t.Fatalf("projected callback error = %#v", record["errors"])
	}
	if got := callbackErr.errorCalls.Load(); got != 0 {
		t.Fatalf("Error calls = %d, want zero", got)
	}
	if got := callbackErr.unwrapCalls.Load(); got != 0 {
		t.Fatalf("Unwrap calls = %d, want zero", got)
	}
	if got := callbackErr.isCalls.Load(); got != 0 {
		t.Fatalf("Is calls = %d, want zero", got)
	}
}

func TestAggregateJSONUsesThePublishedSDKResultUnion(t *testing.T) {
	outcome := engine.Outcome{
		SessionID: "ses_json", TurnID: "turn_internal", Status: protocol.TurnResultMaxTurns,
		StopReason: "max_turns", ModelTurns: 4, Duration: 25 * time.Millisecond,
		Usage: protocol.Usage{Model: "gpt-5.6-sol", InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
	}
	var output bytes.Buffer
	if err := writeJSONResult(&output, outcome, errors.New("turn limit reached")); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "result" || record["subtype"] != "error_max_turns" || record["is_error"] != true {
		t.Fatalf("aggregate result = %#v", record)
	}
	if record["uuid"] == "turn_internal" || record["uuid"] == "" {
		t.Fatalf("aggregate result exposed internal turn identity: %#v", record["uuid"])
	}
	if _, present := record["error"]; present {
		t.Fatalf("aggregate result used non-schema singular error: %#v", record)
	}
	errorsValue, ok := record["errors"].([]any)
	if !ok || len(errorsValue) != 1 || errorsValue[0] != "turn limit reached" {
		t.Fatalf("aggregate errors = %#v", record["errors"])
	}
	for _, key := range []string{"duration_api_ms", "modelUsage", "permission_denials", "stop_reason", "total_cost_usd", "usage", "session_id"} {
		if _, present := record[key]; !present {
			t.Fatalf("aggregate result missing %s: %#v", key, record)
		}
	}
}

func TestSDKResultDistinguishesUnknownFromKnownZeroCost(t *testing.T) {
	knownZero := 0.0
	for _, test := range []struct {
		name string
		cost *float64
		want any
	}{
		{name: "unknown", cost: nil, want: nil},
		{name: "known zero", cost: &knownZero, want: float64(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome := engine.Outcome{
				SessionID: "ses_cost", Status: protocol.TurnResultSuccess,
				Usage: protocol.Usage{Model: "gpt-5.6-sol", CostUSD: test.cost},
			}
			record, err := sdkResultRecord(outcome, nil)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if got := decoded["total_cost_usd"]; got != test.want {
				t.Fatalf("total_cost_usd = %#v, want %#v", got, test.want)
			}
			models, ok := decoded["modelUsage"].(map[string]any)
			if !ok {
				t.Fatalf("modelUsage = %#v", decoded["modelUsage"])
			}
			model, ok := models["gpt-5.6-sol"].(map[string]any)
			if !ok {
				t.Fatalf("model usage = %#v", models["gpt-5.6-sol"])
			}
			if got := model["costUSD"]; got != test.want {
				t.Fatalf("model costUSD = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSDKResultProjectsPermissionDenials(t *testing.T) {
	outcome := engine.Outcome{
		SessionID: "ses_denials", Status: protocol.TurnResultSuccess,
		PermissionDenials: []engine.PermissionDenial{{
			ToolName: "Bash", ToolUseID: "call_denied", ToolInput: json.RawMessage(`{"command":"blocked"}`),
		}},
	}
	record, err := sdkResultRecord(outcome, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		PermissionDenials []struct {
			ToolName  string         `json:"tool_name"`
			ToolUseID string         `json:"tool_use_id"`
			ToolInput map[string]any `json:"tool_input"`
		} `json:"permission_denials"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.PermissionDenials) != 1 || decoded.PermissionDenials[0].ToolName != "Bash" || decoded.PermissionDenials[0].ToolUseID != "call_denied" || decoded.PermissionDenials[0].ToolInput["command"] != "blocked" {
		t.Fatalf("permission denials = %#v", decoded.PermissionDenials)
	}

	outcome.PermissionDenials[0].ToolInput = json.RawMessage(`[]`)
	if _, err := sdkResultRecord(outcome, nil); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("invalid denial input = %v", err)
	}
}

func TestStreamSinkProjectsAPIMessagesWithoutInternalEventWrapper(t *testing.T) {
	var output bytes.Buffer
	sink := &streamSink{encoder: surface.NewEncoder(&output), model: "gpt-5.6-sol", replayUserMessages: true}
	event, err := protocol.NewMessageEvent("ses_wire", "turn_wire", protocol.RoleAssistant, protocol.TextBlock("answer"))
	if err != nil {
		t.Fatal(err)
	}
	event.Message.APIMessageID = "msg_provider"
	event.Message.Phase = "final_answer"
	if err := sink.Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if _, exists := record["event"]; exists {
		t.Fatalf("internal event wrapper leaked: %s", output.String())
	}
	if string(record["type"]) != `"assistant"` || len(record["message"]) == 0 || string(record["parent_tool_use_id"]) != "null" {
		t.Fatalf("assistant projection = %s", output.String())
	}
	var message map[string]any
	if err := json.Unmarshal(record["message"], &message); err != nil {
		t.Fatal(err)
	}
	if message["role"] != "assistant" || message["model"] != "gpt-5.6-sol" || message["phase"] != "final_answer" {
		t.Fatalf("API assistant message = %#v", message)
	}
}

func TestStreamSinkReplayFlagEchoesAPIUserWithReplayIdentity(t *testing.T) {
	event, err := protocol.NewMessageEvent("ses_wire", "turn_wire", protocol.RoleUser, protocol.TextBlock("again"))
	if err != nil {
		t.Fatal(err)
	}
	event.Message.PromptID = "host-user-uuid"
	var disabled bytes.Buffer
	if err := (&streamSink{encoder: surface.NewEncoder(&disabled)}).Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if disabled.Len() != 0 {
		t.Fatalf("user was replayed without flag: %s", disabled.String())
	}
	var enabled bytes.Buffer
	if err := (&streamSink{encoder: surface.NewEncoder(&enabled), replayUserMessages: true}).Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(enabled.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "user" || record["uuid"] != "host-user-uuid" || record["session_id"] != "ses_wire" || record["isReplay"] != true {
		t.Fatalf("replay record = %#v", record)
	}
	message, ok := record["message"].(map[string]any)
	if !ok || message["role"] != "user" {
		t.Fatalf("replay API message = %#v", record["message"])
	}
}

func TestStreamSinkProjectsCompletedCompactionBoundary(t *testing.T) {
	event, err := protocol.NewBaseEvent("ses_wire", "", protocol.EventKindCompaction)
	if err != nil {
		t.Fatal(err)
	}
	event.Visibility = protocol.VisibilityUser
	event.Persistence = protocol.PersistenceEphemeral
	event.Compaction = &protocol.CompactionEvent{Trigger: "manual", State: "completed", PreTokens: 42_000}
	var output bytes.Buffer
	if err := (&streamSink{encoder: surface.NewEncoder(&output)}).Publish(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		CompactMetadata struct {
			Trigger   string `json:"trigger"`
			PreTokens int    `json:"pre_tokens"`
		} `json:"compact_metadata"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Type != "system" || record.Subtype != "compact_boundary" || record.CompactMetadata.Trigger != "manual" || record.CompactMetadata.PreTokens != 42_000 {
		t.Fatalf("compact boundary = %#v", record)
	}
}

func TestDuplicatePromptAcknowledgementStaysInsideClosedSDKUnion(t *testing.T) {
	envelope := surface.InputEnvelope{
		Type: "user", UUID: "host-user-uuid",
		Message: json.RawMessage(`{"role":"user","content":"do this once"}`),
	}
	var suppressed bytes.Buffer
	if err := encodeDuplicate(surface.NewEncoder(&suppressed), newSDKWireSession(t), envelope, false); err != nil {
		t.Fatal(err)
	}
	if suppressed.Len() != 0 {
		t.Fatalf("duplicate emitted without replay: %s", suppressed.String())
	}
	var replayed bytes.Buffer
	if err := encodeDuplicate(surface.NewEncoder(&replayed), newSDKWireSession(t), envelope, true); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(replayed.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "user" || record["uuid"] != "host-user-uuid" || record["isReplay"] != true {
		t.Fatalf("duplicate replay acknowledgement = %#v", record)
	}
	if _, nonstandard := record["subtype"]; nonstandard {
		t.Fatalf("duplicate replay used nonstandard system subtype: %#v", record)
	}
}

func TestPermissionWireFailsClosedAndHonorsEmptyInputSentinel(t *testing.T) {
	allowed, err := decodePermissionDecision(json.RawMessage(`{"behavior":"allow","updatedInput":{}}`))
	if err != nil || allowed.Kind != permission.DecisionAllow || len(allowed.UpdatedInput) != 0 {
		t.Fatalf("empty sentinel = %#v err=%v", allowed, err)
	}
	missing, err := decodePermissionDecision(json.RawMessage(`{"behavior":"allow"}`))
	if err != nil || missing.Kind != permission.DecisionDeny || !strings.Contains(missing.Reason, "requires updatedInput") {
		t.Fatalf("missing input = %#v err=%v", missing, err)
	}
	interrupted, err := decodePermissionDecision(json.RawMessage(`{"behavior":"deny","message":"stop","interrupt":true}`))
	if err != nil || interrupted.Kind != permission.DecisionCancel {
		t.Fatalf("interrupt deny = %#v err=%v", interrupted, err)
	}
	invalid, err := decodePermissionDecision(json.RawMessage(`{"behavior":"maybe","updatedInput":{}}`))
	if err != nil || invalid.Kind != permission.DecisionDeny {
		t.Fatalf("invalid behavior = %#v err=%v", invalid, err)
	}
	tolerated, err := decodePermissionDecision(json.RawMessage(`{"behavior":"allow","updatedInput":{},"updatedPermissions":[{"type":"unknown"}],"decisionClassification":"future_value"}`))
	if err != nil || tolerated.Kind != permission.DecisionAllow {
		t.Fatalf("malformed auxiliary fields were not tolerated: %#v err=%v", tolerated, err)
	}
	unsupported, err := decodePermissionDecision(json.RawMessage(`{"behavior":"allow","updatedInput":{},"updatedPermissions":[{"type":"setMode","mode":"plan","destination":"session"}]}`))
	if err != nil || unsupported.Kind != permission.DecisionDeny || !strings.Contains(unsupported.Reason, "updates are unavailable") {
		t.Fatalf("valid unsupported update = %#v err=%v", unsupported, err)
	}
}

func TestStructuredPermissionWaiterPreservesLocalCancellation(t *testing.T) {
	var output bytes.Buffer
	broker := surface.NewControlBroker()
	interactions := &structuredInteractions{
		broker: broker, encoder: surface.NewEncoder(&output), sessionID: "ses_permission_cancel",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		response, err := interactions.Approve(ctx, permission.ApprovalRequest{
			Tool: "Bash", ToolUseID: "tool_cancel", Input: json.RawMessage(`{"command":"sleep 1"}`), Reason: "review required",
		})
		if response.Kind != "" {
			done <- fmt.Errorf("cancelled approval returned decision %q", response.Kind)
			return
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for broker.Pending() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if broker.Pending() != 1 {
		cancel()
		t.Fatalf("permission waiter did not register: output=%s", output.String())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("permission cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("permission waiter did not settle after cancellation")
	}
	if !strings.Contains(output.String(), `"type":"control_cancel_request"`) {
		t.Fatalf("permission cancellation was not emitted: %s", output.String())
	}
}

func TestInterruptCancelsPermissionsBeforePayloadlessAcknowledgement(t *testing.T) {
	var output bytes.Buffer
	encoder := surface.NewEncoder(&output)
	broker := surface.NewControlBroker()
	request, err := surface.NewControlRequest("can_use_tool", map[string]any{"tool_name": "Bash", "input": map[string]any{}, "tool_use_id": "tool_1"})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	waiterDone := make(chan error, 1)
	go func() {
		_, err := broker.Request(context.Background(), request, func(event surface.OutputEnvelope) error {
			encodeErr := encoder.Encode(event)
			if event.Type == "control_request" {
				close(started)
			}
			return encodeErr
		})
		waiterDone <- err
	}()
	<-started
	turnCtx, cancel := context.WithCancel(context.Background())
	active := &activeTurn{}
	if !active.set(cancel) {
		t.Fatal("active turn was rejected")
	}
	interrupt, err := surface.NewControlRequest("interrupt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := handleControl(surface.InputEnvelope{Type: "control_request", RequestID: "interrupt_1", Request: &interrupt}, encoder, broker, active, &runtimeSession{}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(turnCtx.Err(), context.Canceled) {
		t.Fatalf("turn was not cancelled: %v", turnCtx.Err())
	}
	if err := <-waiterDone; !errors.Is(err, surface.ErrAborted) {
		t.Fatalf("permission waiter = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("records=%q", lines)
	}
	var cancelRecord, ack map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &cancelRecord); err != nil {
		t.Fatal(err)
	}
	if string(cancelRecord["type"]) != `"control_cancel_request"` {
		t.Fatalf("second record was not cancellation: %s", lines[1])
	}
	if err := json.Unmarshal([]byte(lines[2]), &ack); err != nil {
		t.Fatal(err)
	}
	if string(ack["type"]) != `"control_response"` || len(ack["request_id"]) != 0 {
		t.Fatalf("ack outer envelope = %s", lines[2])
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(ack["response"], &response); err != nil {
		t.Fatal(err)
	}
	if string(response["subtype"]) != `"success"` || string(response["request_id"]) != `"interrupt_1"` || len(response["response"]) != 0 {
		t.Fatalf("ack body = %s", ack["response"])
	}
}

type releaseReader struct {
	started chan struct{}
	release <-chan struct{}
}

func (r releaseReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}

type closeInterruptReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *closeInterruptReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}

func (r *closeInterruptReader) Close() error {
	select {
	case <-r.release:
	default:
		close(r.release)
	}
	return nil
}

type hostileBlockingReadCloser struct {
	readStarted  chan struct{}
	closeStarted chan struct{}
	readRelease  chan struct{}
	closeRelease chan struct{}
	readOnce     sync.Once
	closeOnce    sync.Once
}

func (r *hostileBlockingReadCloser) Read([]byte) (int, error) {
	r.readOnce.Do(func() { close(r.readStarted) })
	<-r.readRelease
	return 0, io.EOF
}

func (r *hostileBlockingReadCloser) Close() error {
	r.closeOnce.Do(func() { close(r.closeStarted) })
	<-r.closeRelease
	return nil
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) {
	panic("writer callback panic")
}

func TestStructuredInputRejectsBlockedNonclosableReaderBeforeStartingPump(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	source := releaseReader{started: started, release: release}
	if _, err := structuredInputSource(source); !errors.Is(err, errStructuredInputOwnership) {
		t.Fatalf("nonclosable structured source = %v", err)
	}
	select {
	case <-started:
		t.Fatal("rejected nonclosable source started a reader goroutine")
	default:
	}
	close(release)
}

func TestStructuredInputStopDoesNotWaitForBrokenReadOrCloseCallbacks(t *testing.T) {
	source := &hostileBlockingReadCloser{
		readStarted:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		readRelease:  make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
	input := newStructuredInputReader(context.Background(), source)
	decoderDone := make(chan error, 1)
	go func() {
		_, err := surface.NewDecoder(input.reader, io.Discard).Next()
		decoderDone <- err
	}()
	select {
	case <-source.readStarted:
	case <-time.After(time.Second):
		t.Fatal("broken source Read did not start")
	}
	stopDone := make(chan struct{})
	go func() {
		input.stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("structured input stop waited for broken callbacks")
	}
	select {
	case err := <-decoderDone:
		if err == nil {
			t.Fatal("decoder returned nil after broken-source cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("decoder remained blocked behind broken source")
	}
	select {
	case <-source.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("source Close callback did not start")
	}
	close(source.readRelease)
	close(source.closeRelease)
}

func TestStructuredDecoderContainsWarningWriterPanicAndClosesQueue(t *testing.T) {
	session := newSDKWireSession(t)
	queue := newInputQueue()
	broker := surface.NewControlBroker()
	readStructuredInput(
		t.Context(),
		strings.NewReader("{\"type\":\"future_sdk_record\"}\n"),
		panicWriter{},
		surface.NewEncoder(io.Discard),
		broker,
		queue,
		&activeTurn{},
		session,
		false,
	)
	_, err := queue.next(t.Context())
	if !errors.Is(err, errTerminalWriterPanicked) {
		t.Fatalf("decoder warning writer panic = %v, want fixed terminal failure", err)
	}
}

func TestStructuredInputPumpIsOwnedAndJoinedAfterCancellation(t *testing.T) {
	source := &closeInterruptReader{started: make(chan struct{}), release: make(chan struct{})}
	owned, err := structuredInputSource(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	input := newStructuredInputReader(ctx, owned)
	decoderDone := make(chan error, 1)
	go func() {
		_, err := surface.NewDecoder(input.reader, io.Discard).Next()
		decoderDone <- err
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("structured input pump did not start")
	}
	cancel()
	stopDone := make(chan struct{})
	go func() {
		input.stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("structured input pump was not joined")
	}
	select {
	case err := <-decoderDone:
		if err == nil {
			t.Fatal("decoder returned nil after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("decoder remained blocked after cancellation")
	}
}
