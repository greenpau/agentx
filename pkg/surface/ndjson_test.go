package surface

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

	"github.com/greenpau/agentx/pkg/identity"
	"github.com/greenpau/agentx/pkg/redact"
)

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(target []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	target[0], r.data = r.data[0], r.data[1:]
	return 1, nil
}

type shortWriter struct {
	writes int
}

func (w *shortWriter) Write(data []byte) (int, error) {
	w.writes++
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type hostileCallbackError struct {
	errorCalls  atomic.Int32
	isCalls     atomic.Int32
	unwrapCalls atomic.Int32
}

func (e *hostileCallbackError) Error() string {
	e.errorCalls.Add(1)
	panic("callback Error must not be called")
}

func (e *hostileCallbackError) Is(error) bool {
	e.isCalls.Add(1)
	panic("callback Is must not be called")
}

func (e *hostileCallbackError) Unwrap() error {
	e.unwrapCalls.Add(1)
	panic("callback Unwrap must not be called")
}

func (e *hostileCallbackError) assertUnused(t *testing.T) {
	t.Helper()
	if e.errorCalls.Load() != 0 || e.isCalls.Load() != 0 || e.unwrapCalls.Load() != 0 {
		t.Fatalf(
			"hostile callback error methods called: Error=%d Is=%d Unwrap=%d",
			e.errorCalls.Load(),
			e.isCalls.Load(),
			e.unwrapCalls.Load(),
		)
	}
}

type reentrantEncoderWriter struct {
	encoder   *Encoder
	output    bytes.Buffer
	setResult chan error
}

func (w *reentrantEncoderWriter) Write(data []byte) (int, error) {
	w.setResult <- w.encoder.SetValidator(nil)
	return w.output.Write(data)
}

type panickingEncoderWriter struct {
	calls int
}

func (w *panickingEncoderWriter) Write([]byte) (int, error) {
	w.calls++
	panic("writer callback panic")
}

type panickingStructuredMarshaler struct {
	calls *int
}

func (m panickingStructuredMarshaler) MarshalJSON() ([]byte, error) {
	*m.calls++
	panic("structured marshaler panic")
}

func TestDecoderChunkingUnknownBlankAndFinalRecord(t *testing.T) {
	input := "\n{\"type\":\"future\"}\n{\"type\":\"keep_alive\"}\n{\"type\":\"user\",\"message\":\"hi\"}"
	var warnings bytes.Buffer
	decoder := NewDecoder(&oneByteReader{data: []byte(input)}, &warnings)
	first, err := decoder.Next()
	if err != nil || first.Type != "keep_alive" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := decoder.Next()
	if err != nil || second.Type != "user" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF=%v", err)
	}
	if !strings.Contains(warnings.String(), "future") {
		t.Fatal("missing warning")
	}
}

func TestDecoderMalformedIsFatal(t *testing.T) {
	for _, input := range []string{"{bad}\n", "{}\n", `{"type":""}` + "\n"} {
		decoder := NewDecoder(strings.NewReader(input), io.Discard)
		if _, err := decoder.Next(); err == nil {
			t.Fatalf("expected fatal error for %q", input)
		}
	}
}

func TestEncoderEscapesSeparatorsAndOneLine(t *testing.T) {
	var output bytes.Buffer
	if err := NewEncoder(&output).Encode(map[string]string{"x": "a\u2028b\u2029c"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 || strings.Contains(output.String(), "\u2028") {
		t.Fatalf("unsafe output %q", output.String())
	}
	if !strings.Contains(output.String(), `\u2028`) || !strings.Contains(output.String(), `\u2029`) {
		t.Fatalf("not escaped %q", output.String())
	}
}

func TestEncoderTreatsShortWriteAsFatalAndPoisonsStream(t *testing.T) {
	writer := &shortWriter{}
	encoder := NewEncoder(writer)
	if err := encoder.Encode(map[string]string{"first": "record"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
	if err := encoder.Encode(map[string]string{"second": "record"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("poisoned encoder error = %v", err)
	}
	if writer.writes != 1 {
		t.Fatalf("poisoned encoder wrote %d times", writer.writes)
	}
}

func TestEncoderCallbacksReenterStateWithoutHoldingEncoderMutex(t *testing.T) {
	writer := &reentrantEncoderWriter{setResult: make(chan error, 1)}
	encoder := NewEncoder(writer)
	writer.encoder = encoder
	validatorSet := make(chan error, 1)
	if err := encoder.SetValidator(func([]byte) error {
		validatorSet <- encoder.SetValidator(nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- encoder.Encode(map[string]string{"record": "safe"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reentrant callbacks failed outer encode: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("encoder callback reentrancy deadlocked")
	}
	for name, result := range map[string]<-chan error{
		"validator": validatorSet,
		"writer":    writer.setResult,
	} {
		select {
		case err := <-result:
			if err == nil || !strings.Contains(err.Error(), "cannot change") {
				t.Fatalf("%s reentrant SetValidator = %v", name, err)
			}
		default:
			t.Fatalf("%s callback did not reenter encoder state", name)
		}
	}
	if writer.output.Len() == 0 {
		t.Fatal("reentrant writer did not receive the record")
	}
}

func TestEncoderContainsCallbackPanicsAndPermanentlyPoisonsOutput(t *testing.T) {
	t.Run("validator", func(t *testing.T) {
		var output bytes.Buffer
		encoder := NewEncoder(&output)
		calls := 0
		if err := encoder.SetValidator(func([]byte) error {
			calls++
			panic("validator callback panic")
		}); err != nil {
			t.Fatal(err)
		}
		first := encoder.Encode(map[string]string{"record": "first"})
		second := encoder.Encode(map[string]string{"record": "second"})
		if first != errStructuredOutputValidation || second != first {
			t.Fatalf("validator failures = %v then %v", first, second)
		}
		if calls != 1 || output.Len() != 0 {
			t.Fatalf("validator calls=%d output=%q", calls, output.String())
		}
	})

	t.Run("writer", func(t *testing.T) {
		writer := &panickingEncoderWriter{}
		encoder := NewEncoder(writer)
		first := encoder.Encode(map[string]string{"record": "first"})
		marshalCalls := 0
		second := encoder.Encode(panickingStructuredMarshaler{calls: &marshalCalls})
		if first != errStructuredOutputWrite || second != first {
			t.Fatalf("writer failures = %v then %v", first, second)
		}
		if writer.calls != 1 || marshalCalls != 0 {
			t.Fatalf("post-failure writer calls=%d marshal calls=%d", writer.calls, marshalCalls)
		}
	})
}

func TestEncoderContainsRecordMarshalerPanicWithoutPoisoningStream(t *testing.T) {
	var output bytes.Buffer
	encoder := NewEncoder(&output)
	calls := 0
	if err := encoder.Encode(panickingStructuredMarshaler{calls: &calls}); err != errStructuredOutputEncoding {
		t.Fatalf("marshaler panic = %v", err)
	}
	if calls != 1 || output.Len() != 0 {
		t.Fatalf("marshaler calls=%d output=%q", calls, output.String())
	}
	if err := encoder.Encode(map[string]string{"record": "safe"}); err != nil {
		t.Fatalf("record-specific marshaler panic poisoned stream: %v", err)
	}
}

func TestEncoderDoesNotInspectCallbackErrorsOrAllowValidatorMutation(t *testing.T) {
	t.Run("validator error", func(t *testing.T) {
		callbackErr := &hostileCallbackError{}
		var output bytes.Buffer
		encoder := NewEncoder(&output)
		if err := encoder.SetValidator(func([]byte) error { return callbackErr }); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(map[string]string{"record": "safe"}); err != errStructuredOutputValidation {
			t.Fatalf("validator failure = %v", err)
		}
		callbackErr.assertUnused(t)
		if output.Len() != 0 {
			t.Fatalf("rejected output = %q", output.String())
		}
	})

	t.Run("writer error", func(t *testing.T) {
		callbackErr := &hostileCallbackError{}
		encoder := NewEncoder(failingWriter{err: callbackErr})
		err := encoder.Encode(map[string]string{"record": "safe"})
		if !errors.Is(err, errStructuredOutputWrite) || errors.Is(err, callbackErr) || errors.Unwrap(err) != nil {
			t.Fatalf("writer failure = %v", err)
		}
		callbackErr.assertUnused(t)
	})

	t.Run("sealed standard classification", func(t *testing.T) {
		sentinel := errors.New("classified writer failure")
		hostile := &hostileCallbackError{}
		callbackErr := errors.Join(hostile, sentinel)
		encoder := NewEncoder(failingWriter{err: callbackErr})
		err := encoder.Encode(map[string]string{"record": "safe"})
		if err == nil || err.Error() != errStructuredOutputWrite.Error() ||
			!errors.Is(err, sentinel) || errors.Is(err, callbackErr) ||
			errors.Is(err, hostile) || errors.Unwrap(err) != nil {
			t.Fatalf("sealed writer classification = %v", err)
		}
		hostile.assertUnused(t)
	})

	t.Run("validator mutation", func(t *testing.T) {
		value := map[string]string{"record": "safe"}
		expected, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, '\n')
		var output bytes.Buffer
		encoder := NewEncoder(&output)
		if err := encoder.SetValidator(func(data []byte) error {
			for index := range data {
				data[index] = 'x'
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), expected) {
			t.Fatalf("validator mutated committed bytes: %q", output.Bytes())
		}
	})
}

func TestDecoderPropagatesWarningOutputFailure(t *testing.T) {
	want := errors.New("diagnostic output unavailable")
	decoder := NewDecoder(strings.NewReader("{\"type\":\"future\"}\n"), failingWriter{err: want})
	if _, err := decoder.Next(); !errors.Is(err, want) {
		t.Fatalf("warning failure = %v", err)
	}
}

func TestControlBrokerFastResponseAndCancel(t *testing.T) {
	broker := NewControlBroker()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resolved := make(chan bool, 1)
	response, err := broker.Request(ctx, ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
		if event.Request == nil || event.Request.Subtype != "can_use_tool" {
			t.Fatalf("request envelope = %#v", event)
		}
		go func() {
			resolved <- broker.Resolve(event.RequestID, ControlResponseBody{Subtype: "success", RequestID: event.RequestID, Response: json.RawMessage(`{}`)})
		}()
		return nil
	})
	if err != nil || response.Subtype != "success" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if !<-resolved {
		t.Fatal("waiter was not registered before request emission")
	}

	broker.Close()
	_, err = broker.Request(context.Background(), ControlRequest{}, func(OutputEnvelope) error { return nil })
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("closed err=%v", err)
	}
}

func TestControlBrokerDoesNotHoldLockAcrossSynchronousEmitResolution(t *testing.T) {
	broker := NewControlBroker()
	done := make(chan struct {
		response ControlResponseBody
		err      error
	}, 1)
	go func() {
		response, err := broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
			if !broker.Resolve(event.RequestID, ControlResponseBody{
				Subtype: "success", RequestID: event.RequestID, Response: json.RawMessage(`{}`),
			}) {
				return errors.New("synchronous response was not correlated")
			}
			return nil
		})
		done <- struct {
			response ControlResponseBody
			err      error
		}{response: response, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.response.Subtype != "success" {
			t.Fatalf("synchronous response = %#v, %v", result.response, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous emit resolution deadlocked on the broker mutex")
	}
}

func TestControlBrokerEmitFailureConditionallyRollsBack(t *testing.T) {
	broker := NewControlBroker()
	emitFailure := errors.New("emit failed after response")
	response, err := broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
		if !broker.Resolve(event.RequestID, ControlResponseBody{
			Subtype: "success", RequestID: event.RequestID, Response: json.RawMessage(`{"winner":true}`),
		}) {
			t.Fatal("synchronous response was not correlated")
		}
		return emitFailure
	})
	if err != nil || response.Subtype != "success" {
		t.Fatalf("resolved waiter lost to emit failure: response=%#v err=%v", response, err)
	}
	if broker.Pending() != 0 {
		t.Fatalf("resolved request remained pending: %d", broker.Pending())
	}

	_, err = broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, func(OutputEnvelope) error {
		return emitFailure
	})
	if !errors.Is(err, errStructuredControlEmitFailure) || errors.Is(err, emitFailure) || broker.Pending() != 0 {
		t.Fatalf("unresolved emit failure = %v pending=%d", err, broker.Pending())
	}
}

func TestControlBrokerContainsEmitterPanicsAndRemainsReusable(t *testing.T) {
	broker := NewControlBroker()
	_, err := broker.Request(
		context.Background(),
		ControlRequest{Subtype: "can_use_tool"},
		func(OutputEnvelope) error { panic("emitter callback panic") },
	)
	if err != errStructuredControlEmitFailure || broker.Pending() != 0 {
		t.Fatalf("panicking emitter = %v pending=%d", err, broker.Pending())
	}

	response, err := broker.Request(
		context.Background(),
		ControlRequest{Subtype: "can_use_tool"},
		func(event OutputEnvelope) error {
			if !broker.Resolve(event.RequestID, ControlResponseBody{
				Subtype: "success", RequestID: event.RequestID, Response: json.RawMessage(`{}`),
			}) {
				t.Fatal("post-panic request was not pending")
			}
			return nil
		},
	)
	if err != nil || response.Subtype != "success" || broker.Pending() != 0 {
		t.Fatalf("post-panic response=%#v err=%v pending=%d", response, err, broker.Pending())
	}
}

func TestControlBrokerDoesNotInspectEmitterErrorsAndResolvedResultWinsPanic(t *testing.T) {
	t.Run("unresolved", func(t *testing.T) {
		broker := NewControlBroker()
		callbackErr := &hostileCallbackError{}
		_, err := broker.Request(
			context.Background(),
			ControlRequest{Subtype: "can_use_tool"},
			func(OutputEnvelope) error { return callbackErr },
		)
		if err != errStructuredControlEmitFailure || broker.Pending() != 0 {
			t.Fatalf("hostile emitter = %v pending=%d", err, broker.Pending())
		}
		callbackErr.assertUnused(t)
	})

	t.Run("resolved then panic", func(t *testing.T) {
		broker := NewControlBroker()
		response, err := broker.Request(
			context.Background(),
			ControlRequest{Subtype: "can_use_tool"},
			func(event OutputEnvelope) error {
				if !broker.Resolve(event.RequestID, ControlResponseBody{
					Subtype: "success", RequestID: event.RequestID, Response: json.RawMessage(`{"winner":true}`),
				}) {
					t.Fatal("synchronous response was not correlated")
				}
				panic("emitter panic after response")
			},
		)
		if err != nil || response.Subtype != "success" || broker.Pending() != 0 {
			t.Fatalf("resolved waiter lost to panic: response=%#v err=%v pending=%d", response, err, broker.Pending())
		}
	})
}

func TestControlBrokerCancellationAndAbortContainEmitterPanics(t *testing.T) {
	t.Run("context cancellation", func(t *testing.T) {
		broker := NewControlBroker()
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := broker.Request(ctx, ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
				if event.Type == "control_request" {
					close(started)
					return nil
				}
				panic("cancellation emitter panic")
			})
			done <- err
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || !errors.Is(err, errStructuredControlEmitFailure) {
				t.Fatalf("cancellation panic result = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation emitter panic stranded waiter")
		}
		if broker.Pending() != 0 {
			t.Fatalf("cancellation panic left %d pending requests", broker.Pending())
		}
	})

	t.Run("abort pending and after", func(t *testing.T) {
		broker := NewControlBroker()
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
				if event.Type == "control_request" {
					close(started)
					return nil
				}
				panic("abort emitter panic")
			})
			done <- err
		}()
		<-started
		abortErr := broker.AbortPendingThen(func() error {
			panic("after callback panic")
		})
		if !errors.Is(abortErr, errStructuredControlEmitFailure) {
			t.Fatalf("abort callback failures = %v", abortErr)
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrAborted) {
				t.Fatalf("aborted waiter = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("abort callback panic stranded waiter")
		}
		if broker.Pending() != 0 {
			t.Fatalf("abort callback panic left %d pending requests", broker.Pending())
		}
	})
}

func TestControlEnvelopesUsePublishedFlatShapeAndAliasesAreInboundOnly(t *testing.T) {
	request, err := NewControlRequest("can_use_tool", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "go test ./..."}, "tool_use_id": "tool_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(OutputEnvelope{Type: "control_request", RequestID: "req_1", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"request_id":"req_1"`, `"request":{"input":`, `"subtype":"can_use_tool"`, `"tool_name":"Bash"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %s in %s", expected, text)
		}
	}
	if strings.Contains(text, `"data"`) || strings.Contains(text, `"success"`) {
		t.Fatalf("internal wrapper leaked: %s", text)
	}

	decoder := NewDecoder(strings.NewReader(`{"type":"control_response","response":{"subtype":"success","requestId":"req_alias","response":{"behavior":"allow","updatedInput":{}}}}`), io.Discard)
	envelope, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != "req_alias" || envelope.Response == nil || envelope.Response.RequestID != "req_alias" {
		t.Fatalf("requestId alias was not normalized: %#v", envelope)
	}
	reencoded, err := json.Marshal(OutputEnvelope{Type: "control_response", Response: envelope.Response})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reencoded), "requestId") || !strings.Contains(string(reencoded), `"request_id":"req_alias"`) {
		t.Fatalf("alias escaped outbound: %s", reencoded)
	}
}

func TestInputEnvelopeUsesClosedPerTypeSchemas(t *testing.T) {
	valid := []string{
		`{"type":"user","message":"hello","priority":"next"}`,
		`{"type":"control_request","requestId":"req","request":{"subtype":"interrupt"}}`,
		`{"type":"control_request","requestId":"req","request":{"subtype":"future","limit":1e1000}}`,
		`{"type":"control_response","response":{"subtype":"success","requestId":"req","response":{}}}`,
		`{"type":"control_cancel_request","request_id":"req"}`,
		`{"type":"keep_alive"}`,
		`{"type":"update_environment_variables","environment_variables":{"DEBUG":"1"}}`,
	}
	for _, input := range valid {
		if _, err := NewDecoder(strings.NewReader(input), io.Discard).Next(); err != nil {
			t.Fatalf("valid closed input %s: %v", input, err)
		}
	}

	invalid := []string{
		`{"type":"control_cancel_request","response":{"subtype":"success","request_id":"victim","response":{}}}`,
		`{"type":"control_request","request_id":"outer","request":{"subtype":"interrupt"},"response":{"subtype":"success","request_id":"victim","response":{}}}`,
		`{"type":"control_response","request_id":"outer","response":{"subtype":"success","request_id":"inner","response":{}}}`,
		`{"type":"user","message":"hello","request_id":"victim"}`,
		`{"type":"keep_alive","request_id":"victim"}`,
		`{"type":"update_environment_variables","variables":{},"response":{"subtype":"success","request_id":"victim","response":{}}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","response":{},"unexpected":"value"}}`,
	}
	for _, input := range invalid {
		if _, err := NewDecoder(strings.NewReader(input), io.Discard).Next(); err == nil {
			t.Fatalf("cross-type or unknown field was accepted: %s", input)
		}
	}
}

type duplicateControlFields struct{}

func (duplicateControlFields) MarshalJSON() ([]byte, error) {
	return []byte(`{"tool_name":"Read","tool_name":"Bash"}`), nil
}

func TestStructuredControlJSONRejectsDuplicateMembersAtEveryLayer(t *testing.T) {
	inputs := []string{
		`{"type":"user","t\u0079pe":"keep_alive","message":"hello"}`,
		`{"type":"control_request","request_id":"req","request_id":"victim","request":{"subtype":"interrupt"}}`,
		`{"type":"control_request","request_id":"req","request":{"subtype":"interrupt","subtype":"can_use_tool"}}`,
		`{"type":"control_request","request_id":"req","request":{"subtype":"can_use_tool","input":{"command":"safe","command":"unsafe"}}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","request_id":"victim","response":{}}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","response":{"behavior":"allow","behavior":"deny"}}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"req","error":"failed","pending_permission_requests":[{"type":"control_request","type":"keep_alive","request_id":"nested","request":{"subtype":"can_use_tool"}}]}}`,
	}
	for _, input := range inputs {
		if _, err := NewDecoder(strings.NewReader(input), io.Discard).Next(); !errors.Is(err, errDuplicateJSONMember) {
			t.Fatalf("duplicate member was not rejected with fixed diagnostic: %s: %v", input, err)
		}
	}

	var request ControlRequest
	if err := json.Unmarshal([]byte(`{"subtype":"interrupt","subtype":"can_use_tool"}`), &request); !errors.Is(err, errDuplicateJSONMember) {
		t.Fatalf("standalone duplicate control request = %v", err)
	}
	var response ControlResponseBody
	if err := json.Unmarshal(
		[]byte(`{"subtype":"success","request_id":"req","response":{"behavior":"allow","behavior":"deny"}}`),
		&response,
	); !errors.Is(err, errDuplicateJSONMember) {
		t.Fatalf("standalone duplicate control response = %v", err)
	}
	if _, err := NewControlRequest("can_use_tool", duplicateControlFields{}); !errors.Is(err, errDuplicateJSONMember) {
		t.Fatalf("constructor duplicate control fields = %v", err)
	}

	rawRequest := ControlRequest{
		Subtype: "can_use_tool",
		Data:    json.RawMessage(`{"tool_name":"Read","tool_name":"Bash"}`),
	}
	if _, err := json.Marshal(rawRequest); !errors.Is(err, errDuplicateJSONMember) {
		t.Fatalf("outbound duplicate control request = %v", err)
	}
	rawResponse := ControlResponseBody{
		Subtype:   "success",
		RequestID: "req",
		Response:  json.RawMessage(`{"behavior":"allow","behavior":"deny"}`),
	}
	if _, err := json.Marshal(rawResponse); !errors.Is(err, errDuplicateJSONMember) {
		t.Fatalf("outbound duplicate control response = %v", err)
	}
}

func TestNestedResponseCannotRetargetControlCancellation(t *testing.T) {
	input := `{"type":"control_cancel_request","request_id":"intended","response":{"subtype":"success","request_id":"victim","response":{}}}`
	var envelope InputEnvelope
	if err := json.Unmarshal([]byte(input), &envelope); err == nil {
		t.Fatalf("nested response retargeted cancellation as %#v", envelope)
	}
}

func TestDecodeUserTextAcceptsAPIMessageAndRejectsAssistantRole(t *testing.T) {
	text, err := DecodeUserText(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"first"},{"type":"input_text","text":"second"}]}`))
	if err != nil || text != "first\nsecond" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if _, err := DecodeUserText(json.RawMessage(`{"role":"assistant","content":"not input"}`)); err == nil {
		t.Fatal("assistant role was accepted as user input")
	}
}

func TestControlBrokerContextAbortEmitsCancellation(t *testing.T) {
	broker := NewControlBroker()
	ctx, cancel := context.WithCancel(context.Background())
	var emitted []OutputEnvelope
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := broker.Request(ctx, ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
			emitted = append(emitted, event)
			if event.Type == "control_request" {
				close(started)
			}
			return nil
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("abort error = %v", err)
	}
	if len(emitted) != 2 || emitted[0].Type != "control_request" || emitted[1].Type != "control_cancel_request" || emitted[0].RequestID != emitted[1].RequestID {
		t.Fatalf("abort envelopes = %#v", emitted)
	}
}

func TestControlBrokerAbortPendingPreservesRequestOrder(t *testing.T) {
	broker := NewControlBroker()
	type observation struct {
		typeName string
		id       identity.RequestID
	}
	var mu sync.Mutex
	var observed []observation
	emit := func(event OutputEnvelope) error {
		mu.Lock()
		observed = append(observed, observation{event.Type, event.RequestID})
		mu.Unlock()
		return nil
	}
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, emit)
			results <- err
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			emittedCount := len(observed)
			mu.Unlock()
			if broker.Pending() == index+1 && emittedCount == index+1 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		emittedCount := len(observed)
		mu.Unlock()
		if broker.Pending() != index+1 || emittedCount != index+1 {
			t.Fatalf("after request %d: pending=%d emitted=%d", index, broker.Pending(), emittedCount)
		}
	}
	if err := broker.AbortPending(); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := <-results; !errors.Is(err, ErrAborted) {
			t.Fatalf("abort result = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 4 {
		t.Fatalf("observed = %#v", observed)
	}
	if observed[2].typeName != "control_cancel_request" || observed[3].typeName != "control_cancel_request" || observed[0].id != observed[2].id || observed[1].id != observed[3].id {
		t.Fatalf("cancellation order = %#v", observed)
	}
}

func TestFinalUnterminatedControlResponseWinsBeforeEOFCleanup(t *testing.T) {
	broker := NewControlBroker()
	type requestInfo struct {
		name string
		id   identity.RequestID
	}
	requests := make(chan requestInfo, 2)
	results := make(chan struct {
		name     string
		response ControlResponseBody
		err      error
	}, 2)
	for _, name := range []string{"matched", "pending"} {
		name := name
		go func() {
			response, err := broker.Request(context.Background(), ControlRequest{Subtype: "can_use_tool"}, func(event OutputEnvelope) error {
				requests <- requestInfo{name: name, id: event.RequestID}
				return nil
			})
			results <- struct {
				name     string
				response ControlResponseBody
				err      error
			}{name: name, response: response, err: err}
		}()
	}
	ids := make(map[string]identity.RequestID)
	for index := 0; index < 2; index++ {
		request := <-requests
		ids[request.name] = request.id
	}
	input := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"behavior":"allow","updatedInput":{}}}}`, ids["matched"])
	decoder := NewDecoder(strings.NewReader(input), io.Discard)
	envelope, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Response == nil || !broker.Resolve(envelope.Response.RequestID, *envelope.Response) {
		t.Fatal("final response did not resolve its waiter")
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("decoder EOF = %v", err)
	}
	broker.Close()
	seen := make(map[string]error)
	for index := 0; index < 2; index++ {
		result := <-results
		seen[result.name] = result.err
		if result.name == "matched" && (result.err != nil || result.response.Subtype != "success") {
			t.Fatalf("matched result = %#v err=%v", result.response, result.err)
		}
	}
	if !errors.Is(seen["pending"], ErrClosed) {
		t.Fatalf("unmatched waiter = %v", seen["pending"])
	}
}

func TestControlResponseRejectsNullOptionalMembers(t *testing.T) {
	for _, input := range []string{
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","response":null}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","error":null}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"req","error":null}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"req","error":"x","pending_permission_requests":null}}`,
	} {
		if _, err := NewDecoder(strings.NewReader(input), io.Discard).Next(); err == nil {
			t.Fatalf("null optional member accepted: %s", input)
		}
	}
}

func TestControlResponseRejectsMembersFromTheOtherUnionBranch(t *testing.T) {
	for _, input := range []string{
		`{"type":"control_response","response":{"subtype":"success","request_id":"req","response":{},"pending_permission_requests":[]}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"req","error":"failed","response":{}}}`,
	} {
		if _, err := NewDecoder(strings.NewReader(input), io.Discard).Next(); err == nil {
			t.Fatalf("cross-branch control member accepted: %s", input)
		}
	}
}

func TestCanonicalRequestIDWinsOverCompatibilityAlias(t *testing.T) {
	input := `{"type":"control_response","response":{"subtype":"success","request_id":"canonical","requestId":"alias","response":{}}}`
	envelope, err := NewDecoder(strings.NewReader(input), io.Discard).Next()
	if err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != "canonical" || envelope.Response == nil || envelope.Response.RequestID != "canonical" {
		t.Fatalf("canonical request id did not win: %#v", envelope)
	}
	invalidCanonical := `{"type":"control_response","response":{"subtype":"success","request_id":"","requestId":"alias","response":{}}}`
	if _, err := NewDecoder(strings.NewReader(invalidCanonical), io.Discard).Next(); err == nil {
		t.Fatal("invalid canonical request_id fell back to alias")
	}
}

func TestNewUUIDUsesRFC4122Version4Shape(t *testing.T) {
	uuid, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' || uuid[14] != '4' {
		t.Fatalf("UUID shape = %q", uuid)
	}
	if uuid[19] != '8' && uuid[19] != '9' && uuid[19] != 'a' && uuid[19] != 'b' {
		t.Fatalf("UUID variant = %q", uuid)
	}
}

func TestEncoderFinalValidatorRejectsCredentialReconstructedByJSONFraming(t *testing.T) {
	const secret = `foo"`
	set := redact.New(secret)
	var output bytes.Buffer
	encoder := NewEncoder(&output)
	if err := encoder.SetValidator(func(raw []byte) error {
		reflected, err := set.JSONContains(raw)
		if err != nil {
			return err
		}
		if reflected {
			return errors.New("credential reflected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"content": "foo"}); err == nil {
		t.Fatal("framing-reconstructed credential was written")
	}
	if output.Len() != 0 {
		t.Fatalf("unsafe structured output bytes = %q", output.String())
	}
	if err := encoder.Encode(map[string]any{"content": "safe"}); err == nil {
		t.Fatal("security failure did not latch the encoder")
	}
}

func TestEncoderValidatorInspectsExactNewlineFramedRecord(t *testing.T) {
	value := map[string]any{"content": "safe"}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(body[len(body)-3:]) + "\n"
	set := redact.New(secret)
	if matched, err := set.JSONContains(body); err != nil || matched {
		t.Fatalf("unframed fixture = matched %t, %v", matched, err)
	}
	var output bytes.Buffer
	encoder := NewEncoder(&output)
	if err := encoder.SetValidator(func(raw []byte) error {
		matched, inspectErr := set.JSONContains(raw)
		if inspectErr != nil {
			return inspectErr
		}
		if matched {
			return errors.New("credential reflected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(value); err == nil {
		t.Fatal("newline-only framing credential was written")
	}
	if output.Len() != 0 {
		t.Fatalf("unsafe framed record reached output: %q", output.String())
	}
}
