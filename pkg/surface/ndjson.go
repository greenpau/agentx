// Package surface adapts the shared semantic runtime to user-visible input and
// output contracts. It does not own query, permission, or persistence policy.
package surface

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/greenpau/agentx/pkg/identity"
)

const MaxNDJSONRecordBytes = 8 << 20

const maximumStructuredCallbackErrorNodes = 128

var (
	ErrClosed                       = errors.New("structured control stream closed")
	ErrAborted                      = errors.New("structured control request aborted")
	errDuplicateJSONMember          = errors.New("JSON object contains a duplicate member")
	errStructuredOutputEncoding     = errors.New("encode structured output: record could not be encoded")
	errStructuredOutputValidation   = errors.New("validate structured output: record could not be safely encoded")
	errStructuredOutputTooLarge     = errors.New("write structured output: record exceeds size limit")
	errStructuredOutputWrite        = errors.New("write structured output: writer failed")
	errStructuredOutputShortWrite   = fmt.Errorf("write structured output: %w", io.ErrShortWrite)
	errStructuredControlEmitFailure = errors.New("structured control emission failed")
)

// NewUUID creates a version-4 RFC 4122 identifier for SDK fields whose public
// schema specifically requires UUID rather than an internal typed identifier.
func NewUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate SDK UUID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

// InputEnvelope is the documented aggregate stdin envelope. Message is kept
// raw until the semantic runner validates the API-message role and content.
// Request-ID camel-case aliases are accepted only by UnmarshalJSON.
type InputEnvelope struct {
	Type                 string               `json:"type"`
	UUID                 string               `json:"uuid,omitempty"`
	SessionID            string               `json:"session_id,omitempty"`
	Message              json.RawMessage      `json:"message,omitempty"`
	ParentToolUseID      *string              `json:"parent_tool_use_id,omitempty"`
	IsReplay             bool                 `json:"isReplay,omitempty"`
	IsSynthetic          bool                 `json:"isSynthetic,omitempty"`
	ToolUseResult        json.RawMessage      `json:"tool_use_result,omitempty"`
	Priority             string               `json:"priority,omitempty"`
	Timestamp            string               `json:"timestamp,omitempty"`
	RequestID            identity.RequestID   `json:"request_id,omitempty"`
	Request              *ControlRequest      `json:"request,omitempty"`
	Response             *ControlResponseBody `json:"response,omitempty"`
	EnvironmentVariables map[string]string    `json:"variables,omitempty"`

	originalByteSize int
}

// OriginalByteSize reports the complete decoded input record's byte size
// before compatibility aliases and fields were projected into InputEnvelope.
// Queue owners use it for bounded admission without retaining another copy of
// the raw record.
func (e InputEnvelope) OriginalByteSize() int { return e.originalByteSize }

func (e *InputEnvelope) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return err
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	if rawFields == nil {
		return errors.New("structured input record must be a JSON object")
	}
	type wireEnvelope struct {
		Type                 string               `json:"type"`
		UUID                 string               `json:"uuid,omitempty"`
		SessionID            string               `json:"session_id,omitempty"`
		Message              json.RawMessage      `json:"message,omitempty"`
		ParentToolUseID      *string              `json:"parent_tool_use_id,omitempty"`
		IsReplay             bool                 `json:"isReplay,omitempty"`
		IsSynthetic          bool                 `json:"isSynthetic,omitempty"`
		ToolUseResult        json.RawMessage      `json:"tool_use_result,omitempty"`
		Priority             string               `json:"priority,omitempty"`
		Timestamp            string               `json:"timestamp,omitempty"`
		RequestID            identity.RequestID   `json:"request_id,omitempty"`
		RequestIDAlias       identity.RequestID   `json:"requestId,omitempty"`
		Request              *ControlRequest      `json:"request,omitempty"`
		Response             *ControlResponseBody `json:"response,omitempty"`
		Variables            map[string]string    `json:"variables,omitempty"`
		EnvironmentVariables map[string]string    `json:"environment_variables,omitempty"`
	}
	var wire wireEnvelope
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if strings.TrimSpace(wire.Type) == "" {
		return errors.New("structured input type is required")
	}
	if err := validateInputEnvelopeFields(wire.Type, rawFields); err != nil {
		return err
	}
	var requestID identity.RequestID
	switch wire.Type {
	case "control_request", "control_cancel_request":
		requestID = wire.RequestID
		if _, canonicalPresent := rawFields["request_id"]; !canonicalPresent {
			requestID = wire.RequestIDAlias
		}
	case "control_response":
		if wire.Response != nil {
			requestID = wire.Response.RequestID
		}
	}
	var variables map[string]string
	if wire.Type == "update_environment_variables" {
		variables = wire.Variables
		if variables == nil {
			variables = wire.EnvironmentVariables
		}
	}
	*e = InputEnvelope{
		Type: wire.Type, UUID: wire.UUID, SessionID: wire.SessionID,
		Message: wire.Message, ParentToolUseID: wire.ParentToolUseID,
		IsReplay: wire.IsReplay, IsSynthetic: wire.IsSynthetic,
		ToolUseResult: wire.ToolUseResult, Priority: wire.Priority,
		Timestamp: wire.Timestamp, RequestID: requestID, Request: wire.Request,
		Response: wire.Response, EnvironmentVariables: variables,
		originalByteSize: len(data),
	}
	return nil
}

// validateInputEnvelopeFields applies the documented closed schema after the
// type discriminator is known. Compatibility aliases are admitted only for
// the record types that own them. Unknown record types remain forward
// compatible and are ignored by Decoder after their framing is validated.
func validateInputEnvelopeFields(inputType string, fields map[string]json.RawMessage) error {
	for field := range fields {
		if inputEnvelopeFieldAllowed(inputType, field) {
			continue
		}
		return fmt.Errorf("structured input type %q does not allow field %q", inputType, field)
	}
	return nil
}

func inputEnvelopeFieldAllowed(inputType, field string) bool {
	if field == "type" {
		return true
	}
	switch inputType {
	case "user", "assistant", "system":
		switch field {
		case "uuid", "session_id", "message", "parent_tool_use_id", "isReplay",
			"isSynthetic", "tool_use_result", "priority", "timestamp":
			return true
		}
	case "control_request":
		return field == "request_id" || field == "requestId" || field == "request"
	case "control_response":
		return field == "response"
	case "control_cancel_request":
		return field == "request_id" || field == "requestId"
	case "keep_alive":
		return false
	case "update_environment_variables":
		return field == "variables" || field == "environment_variables"
	default:
		return true
	}
	return false
}

// ControlRequest serializes operation fields beside subtype, never under an
// internal data wrapper. Data must be either absent or a JSON object.
type ControlRequest struct {
	Subtype string          `json:"-"`
	Data    json.RawMessage `json:"-"`
}

func NewControlRequest(subtype string, fields any) (ControlRequest, error) {
	request := ControlRequest{Subtype: subtype}
	if fields == nil {
		request.Data = json.RawMessage(`{}`)
		return request, nil
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return ControlRequest{}, err
	}
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return ControlRequest{}, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return ControlRequest{}, errors.New("control request fields must encode a JSON object")
	}
	delete(object, "subtype")
	data, err = json.Marshal(object)
	if err != nil {
		return ControlRequest{}, err
	}
	request.Data = data
	return request, nil
}

func (r ControlRequest) MarshalJSON() ([]byte, error) {
	if strings.TrimSpace(r.Subtype) == "" {
		return nil, errors.New("control request subtype is required")
	}
	fields := make(map[string]json.RawMessage)
	if len(r.Data) != 0 {
		if err := rejectDuplicateJSONMembers(r.Data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(r.Data, &fields); err != nil {
			return nil, errors.New("control request fields must be a JSON object")
		}
	}
	subtype, _ := json.Marshal(r.Subtype)
	fields["subtype"] = subtype
	return json.Marshal(fields)
}

func (r *ControlRequest) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("control request must be a JSON object")
	}
	var subtype string
	if raw, ok := fields["subtype"]; !ok || json.Unmarshal(raw, &subtype) != nil || strings.TrimSpace(subtype) == "" {
		return errors.New("control request subtype is required")
	}
	delete(fields, "subtype")
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	r.Subtype, r.Data = subtype, payload
	return nil
}

// ControlResponseBody is the closed published success/error response union.
// Response is operation-specific and is validated by the selected waiter.
type ControlResponseBody struct {
	Subtype                   string             `json:"subtype"`
	RequestID                 identity.RequestID `json:"request_id"`
	Response                  json.RawMessage    `json:"response,omitempty"`
	Error                     string             `json:"error,omitempty"`
	PendingPermissionRequests []OutputEnvelope   `json:"pending_permission_requests,omitempty"`
}

func (r ControlResponseBody) MarshalJSON() ([]byte, error) {
	if r.RequestID == "" {
		return nil, errors.New("control response request_id is required")
	}
	type responseOutput struct {
		Subtype                   string             `json:"subtype"`
		RequestID                 identity.RequestID `json:"request_id"`
		Response                  json.RawMessage    `json:"response,omitempty"`
		Error                     string             `json:"error,omitempty"`
		PendingPermissionRequests []OutputEnvelope   `json:"pending_permission_requests,omitempty"`
	}
	wire := responseOutput{Subtype: r.Subtype, RequestID: r.RequestID}
	switch r.Subtype {
	case "success":
		if r.Error != "" || len(r.PendingPermissionRequests) != 0 {
			return nil, errors.New("success control response contains error-only members")
		}
		if len(r.Response) != 0 && bytes.Equal(bytes.TrimSpace(r.Response), []byte("null")) {
			return nil, errors.New("control response response must not be null")
		}
		if len(r.Response) != 0 {
			if err := rejectDuplicateJSONMembers(r.Response); err != nil {
				return nil, err
			}
		}
		wire.Response = r.Response
	case "error":
		if strings.TrimSpace(r.Error) == "" || len(r.Response) != 0 {
			return nil, errors.New("error control response requires error and forbids response")
		}
		if err := validatePendingRequests(r.PendingPermissionRequests); err != nil {
			return nil, err
		}
		wire.Error = r.Error
		wire.PendingPermissionRequests = r.PendingPermissionRequests
	default:
		return nil, fmt.Errorf("control response subtype %q is unsupported", r.Subtype)
	}
	return json.Marshal(wire)
}

func (r *ControlResponseBody) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("control response must be a JSON object")
	}
	if err := rejectUnknownJSONFields(fields, "subtype", "request_id", "requestId", "response", "error", "pending_permission_requests"); err != nil {
		return fmt.Errorf("control response: %w", err)
	}
	var subtype string
	if json.Unmarshal(fields["subtype"], &subtype) != nil {
		return errors.New("control response subtype is required")
	}
	var requestID identity.RequestID
	if canonical, present := fields["request_id"]; present {
		if json.Unmarshal(canonical, &requestID) != nil {
			return errors.New("control response request_id must be a string")
		}
	} else {
		if json.Unmarshal(fields["requestId"], &requestID) != nil {
			return errors.New("control response request_id must be a string")
		}
	}
	if requestID == "" {
		return errors.New("control response request_id is required")
	}
	if subtype != "success" && subtype != "error" {
		return fmt.Errorf("control response subtype %q is unsupported", subtype)
	}
	response := fields["response"]
	if len(response) != 0 && bytes.Equal(bytes.TrimSpace(response), []byte("null")) {
		return errors.New("control response response must not be null")
	}
	errorRaw, hasError := fields["error"]
	errorText := ""
	if subtype == "error" {
		if !hasError || bytes.Equal(bytes.TrimSpace(errorRaw), []byte("null")) || json.Unmarshal(errorRaw, &errorText) != nil || strings.TrimSpace(errorText) == "" {
			return errors.New("error control response requires a nonempty error")
		}
	} else if hasError {
		return errors.New("success control response must not contain error")
	}
	var pending []OutputEnvelope
	pendingRaw, hasPending := fields["pending_permission_requests"]
	if hasPending {
		if bytes.Equal(bytes.TrimSpace(pendingRaw), []byte("null")) || json.Unmarshal(pendingRaw, &pending) != nil {
			return errors.New("pending_permission_requests must be an array")
		}
		if err := validatePendingRequests(pending); err != nil {
			return err
		}
	}
	if subtype == "success" {
		if hasPending {
			return errors.New("success control response must not contain pending_permission_requests")
		}
	} else {
		if len(response) != 0 {
			return errors.New("error control response must not contain response")
		}
	}
	*r = ControlResponseBody{Subtype: subtype, RequestID: requestID, Response: response, Error: errorText, PendingPermissionRequests: pending}
	return nil
}

func rejectUnknownJSONFields(fields map[string]json.RawMessage, allowed ...string) error {
	accepted := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		accepted[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := accepted[field]; !ok {
			return fmt.Errorf("field %q is not allowed", field)
		}
	}
	return nil
}

// rejectDuplicateJSONMembers walks every object in one JSON value before a
// map or struct decoder can silently apply last-member-wins semantics. JSON
// string escapes are decoded by Token, so syntactically different spellings
// of the same member name are duplicates too.
func rejectDuplicateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValueForDuplicateMembers(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("JSON value contains trailing data")
	}
	return nil
}

func scanJSONValueForDuplicateMembers(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object member name must be a string")
			}
			if _, duplicate := members[key]; duplicate {
				return errDuplicateJSONMember
			}
			members[key] = struct{}{}
			if err := scanJSONValueForDuplicateMembers(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValueForDuplicateMembers(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	}
	return nil
}

func validatePendingRequests(requests []OutputEnvelope) error {
	for index, request := range requests {
		if request.Type != "control_request" || request.RequestID == "" || request.Request == nil || strings.TrimSpace(request.Request.Subtype) == "" || request.Response != nil {
			return fmt.Errorf("pending_permission_requests[%d] is not a complete control_request", index)
		}
	}
	return nil
}

// OutputEnvelope is used only for the three control envelopes. Ordinary SDK
// messages have dedicated projections in pkg/app and do not use Data.
type OutputEnvelope struct {
	Type      string               `json:"type"`
	RequestID identity.RequestID   `json:"request_id,omitempty"`
	Request   *ControlRequest      `json:"request,omitempty"`
	Response  *ControlResponseBody `json:"response,omitempty"`
}

// Decoder accepts arbitrary reader chunking, skips blank records, processes a
// final record without a newline, and fails the stream on malformed JSON.
type Decoder struct {
	scanner *bufio.Scanner
	warn    io.Writer
}

func NewDecoder(reader io.Reader, warnings io.Writer) *Decoder {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxNDJSONRecordBytes)
	return &Decoder{scanner: scanner, warn: warnings}
}

func (d *Decoder) Next() (InputEnvelope, error) {
	for d.scanner.Scan() {
		line := bytes.TrimSpace(d.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope InputEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return InputEnvelope{}, fmt.Errorf("malformed NDJSON input: %w", err)
		}
		switch envelope.Type {
		case "user", "assistant", "system", "control_request", "control_response", "control_cancel_request", "keep_alive", "update_environment_variables":
			return envelope, nil
		default:
			if d.warn != nil {
				if _, err := fmt.Fprintf(d.warn, "warning: ignored unknown structured input type %q\n", envelope.Type); err != nil {
					return InputEnvelope{}, fmt.Errorf("write structured input warning: %w", err)
				}
			}
		}
	}
	if err := d.scanner.Err(); err != nil {
		return InputEnvelope{}, fmt.Errorf("read NDJSON input: %w", err)
	}
	return InputEnvelope{}, io.EOF
}

// Encoder serializes protocol records atomically. Diagnostics must use a
// separate writer; stdout remains protocol-only.
type Encoder struct {
	encodeMu  sync.Mutex
	mu        sync.Mutex
	writer    io.Writer
	validator func([]byte) error
	started   bool
	failed    error
}

func NewEncoder(writer io.Writer) *Encoder { return &Encoder{writer: writer} }

// SetValidator installs a complete-record validator before the first write.
// It is intended for session-scoped egress policy that must inspect JSON
// framing as well as individual semantic fields.
func (e *Encoder) SetValidator(validator func([]byte) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("structured output validator cannot change after encoding starts")
	}
	if e.failed != nil {
		return e.failed
	}
	e.validator = validator
	return nil
}

func (e *Encoder) Encode(value any) error {
	if failure := e.currentFailure(); failure != nil {
		return failure
	}
	data, err := marshalStructuredOutput(value)
	if err != nil {
		return err
	}
	// Serialize complete records, but keep callback-owned validator and writer
	// code outside the state mutex so they may inspect encoder state without
	// deadlocking SetValidator.
	e.encodeMu.Lock()
	defer e.encodeMu.Unlock()
	e.mu.Lock()
	if e.failed != nil {
		err := e.failed
		e.mu.Unlock()
		return err
	}
	e.started = true
	validator, writer := e.validator, e.writer
	e.mu.Unlock()
	// encoding/json currently escapes these JavaScript separators. Make the
	// wire guarantee explicit in case a custom Marshaler returns them raw.
	data = bytes.ReplaceAll(data, []byte("\u2028"), []byte(`\u2028`))
	data = bytes.ReplaceAll(data, []byte("\u2029"), []byte(`\u2029`))
	data = append(data, '\n')
	if len(data) > MaxNDJSONRecordBytes {
		return e.fail(errStructuredOutputTooLarge)
	}
	if validator != nil {
		// The validator receives an exact clone: it inspects the physical
		// record but cannot mutate the bytes later committed to the writer.
		if !structuredOutputValid(validator, append([]byte(nil), data...)) {
			return e.fail(errStructuredOutputValidation)
		}
	}
	if err := writeStructuredOutput(writer, data); err != nil {
		return e.fail(err)
	}
	return nil
}

func (e *Encoder) currentFailure() error {
	e.mu.Lock()
	failure := e.failed
	e.mu.Unlock()
	return failure
}

func marshalStructuredOutput(value any) (data []byte, resultErr error) {
	resultErr = errStructuredOutputEncoding
	defer func() {
		if recover() != nil {
			data = nil
			resultErr = errStructuredOutputEncoding
		}
	}()
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errStructuredOutputEncoding
	}
	return data, nil
}

func structuredOutputValid(validator func([]byte) error, data []byte) (valid bool) {
	defer func() {
		if recover() != nil {
			valid = false
		}
	}()
	return validator(data) == nil
}

func writeStructuredOutput(writer io.Writer, data []byte) (resultErr error) {
	resultErr = errStructuredOutputWrite
	defer func() {
		if recover() != nil {
			resultErr = errStructuredOutputWrite
		}
	}()
	written, err := writer.Write(data)
	if err != nil {
		return newStructuredOutputWriteFailure(err)
	}
	if written != len(data) {
		return errStructuredOutputShortWrite
	}
	return nil
}

type structuredOutputWriteFailure struct {
	classes map[error]struct{}
}

func (e *structuredOutputWriteFailure) Error() string { return errStructuredOutputWrite.Error() }
func (e *structuredOutputWriteFailure) Is(target error) bool {
	if target == nil {
		return false
	}
	typ := reflect.TypeOf(target)
	if typ == nil || !typ.Comparable() {
		return false
	}
	_, exists := e.classes[target]
	return exists
}

func newStructuredOutputWriteFailure(cause error) error {
	classes := inspectStructuredCallbackError(cause)
	classes[errStructuredOutputWrite] = struct{}{}
	return &structuredOutputWriteFailure{classes: classes}
}

func inspectStructuredCallbackError(cause error) map[error]struct{} {
	pending := []error{cause}
	seen := make(map[error]struct{})
	classes := make(map[error]struct{})
	for visited := 0; len(pending) > 0 && visited < maximumStructuredCallbackErrorNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		typ := reflect.TypeOf(current)
		if typ != nil && typ.Comparable() {
			if _, duplicate := seen[current]; duplicate {
				continue
			}
			seen[current] = struct{}{}
		}
		if !trustedStandardErrorType(typ) {
			// Callback-defined wrappers are opaque. Do not call their Unwrap,
			// Is, As, or Error methods and do not retain them as classes.
			continue
		}
		children, inspected := unwrapStructuredCallbackError(current)
		// Retain only exact terminal classification identities, never an outer
		// callback wrapper that may itself carry credential-bearing state.
		if inspected && len(children) == 0 && typ != nil && typ.Comparable() {
			classes[current] = struct{}{}
		}
		remaining := maximumStructuredCallbackErrorNodes - visited - 1 - len(pending)
		if remaining < 0 {
			remaining = 0
		}
		if len(children) > remaining {
			children = children[:remaining]
		}
		for index := len(children) - 1; index >= 0; index-- {
			pending = append(pending, children[index])
		}
	}
	return classes
}

func trustedStandardErrorType(typ reflect.Type) bool {
	if typ == nil {
		return false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	path := typ.PkgPath()
	if path == "" {
		return false
	}
	first := path
	if slash := strings.IndexByte(first, '/'); slash >= 0 {
		first = first[:slash]
	}
	return !strings.Contains(first, ".")
}

func unwrapStructuredCallbackError(err error) (children []error, inspected bool) {
	inspected = true
	defer func() {
		if recover() != nil {
			children, inspected = nil, false
		}
	}()
	switch typed := err.(type) {
	case interface{ Unwrap() []error }:
		return typed.Unwrap(), true
	case interface{ Unwrap() error }:
		return []error{typed.Unwrap()}, true
	default:
		return nil, true
	}
}

// SealedOutputErrorClassifications exposes exact, bounded policy identities
// only for this package's opaque output failure. It never returns the raw
// error graph or invokes callback-owned behavior.
func SealedOutputErrorClassifications(err error) []error {
	failure, ok := err.(*structuredOutputWriteFailure)
	if !ok {
		return nil
	}
	classes := make([]error, 0, len(failure.classes))
	for class := range failure.classes {
		classes = append(classes, class)
	}
	return classes
}

func (e *Encoder) fail(err error) error {
	e.mu.Lock()
	if e.failed == nil {
		e.failed = err
	}
	failure := e.failed
	e.mu.Unlock()
	return failure
}

type controlResult struct {
	response ControlResponseBody
	err      error
}

type pendingControl struct {
	result chan controlResult
	stop   context.CancelFunc
	emit   func(OutputEnvelope) error
}

// ControlBroker correlates concurrent permission and SDK requests. A waiter is
// registered before its request is emitted, closing the fast-response race.
type ControlBroker struct {
	mu      sync.Mutex
	pending map[identity.RequestID]pendingControl
	order   []identity.RequestID
	closed  bool
}

func NewControlBroker() *ControlBroker {
	return &ControlBroker{pending: make(map[identity.RequestID]pendingControl)}
}

func (b *ControlBroker) Request(ctx context.Context, request ControlRequest, emit func(OutputEnvelope) error) (ControlResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return ControlResponseBody{}, err
	}
	id, err := identity.NewRequest()
	if err != nil {
		return ControlResponseBody{}, err
	}
	waitCtx, stop := context.WithCancel(ctx)
	waiter := pendingControl{result: make(chan controlResult, 1), stop: stop, emit: emit}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		stop()
		return ControlResponseBody{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		stop()
		return ControlResponseBody{}, err
	}
	b.pending[id] = waiter
	b.order = append(b.order, id)
	b.mu.Unlock()
	if err := emitStructuredControl(emit, OutputEnvelope{Type: "control_request", RequestID: id, Request: &request}); err != nil {
		// Arbitrary emitters may synchronously resolve, cancel, abort, or close
		// this request. Roll back only while this exact waiter is still pending;
		// otherwise its already-selected terminal result wins the emit error.
		if b.rollbackPending(id, waiter.result) {
			stop()
			return ControlResponseBody{}, err
		}
		result := <-waiter.result
		waiter.stop()
		return result.response, result.err
	}
	select {
	case result := <-waiter.result:
		waiter.stop()
		return result.response, result.err
	case <-waitCtx.Done():
		// A response that already removed the waiter wins even if cancellation
		// became ready at the same instant.
		if !b.take(id) {
			result := <-waiter.result
			waiter.stop()
			return result.response, result.err
		}
		cancelErr := emitStructuredControl(emit, OutputEnvelope{Type: "control_cancel_request", RequestID: id})
		waiter.stop()
		return ControlResponseBody{}, errors.Join(waitCtx.Err(), cancelErr)
	}
}

func (b *ControlBroker) rollbackPending(id identity.RequestID, result chan controlResult) bool {
	b.mu.Lock()
	waiter, ok := b.pending[id]
	if ok && waiter.result == result {
		delete(b.pending, id)
		b.removeOrderLocked(id)
	} else {
		ok = false
	}
	b.mu.Unlock()
	return ok
}

func (b *ControlBroker) Resolve(id identity.RequestID, response ControlResponseBody) bool {
	if id == "" || response.RequestID != id {
		return false
	}
	b.mu.Lock()
	waiter, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
		b.removeOrderLocked(id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	waiter.result <- controlResult{response: response}
	return true
}

func (b *ControlBroker) Cancel(id identity.RequestID) bool {
	b.mu.Lock()
	waiter, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
		b.removeOrderLocked(id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	waiter.result <- controlResult{err: ErrAborted}
	waiter.stop()
	return true
}

// AbortPending synchronously emits cancellations in request order and then
// settles the detached waiters.
func (b *ControlBroker) AbortPending() error {
	return b.AbortPendingThen(nil)
}

// AbortPendingThen detaches pending waiters, emits every cancellation, runs
// after while those waiters are still parked, and only then releases them.
// The interrupt adapter uses after for its acknowledgement, preventing waiter
// cleanup events from racing between cancellation and acknowledgement.
func (b *ControlBroker) AbortPendingThen(after func() error) error {
	b.mu.Lock()
	ids := append([]identity.RequestID(nil), b.order...)
	waiters := make(map[identity.RequestID]pendingControl, len(ids))
	for _, id := range ids {
		if waiter, ok := b.pending[id]; ok {
			waiters[id] = waiter
			delete(b.pending, id)
		}
	}
	b.order = nil
	b.mu.Unlock()
	var errs []error
	for _, id := range ids {
		waiter, ok := waiters[id]
		if !ok {
			continue
		}
		if err := emitStructuredControl(waiter.emit, OutputEnvelope{Type: "control_cancel_request", RequestID: id}); err != nil {
			errs = append(errs, err)
		}
	}
	if after != nil {
		if err := invokeStructuredControlCallback(after); err != nil {
			errs = append(errs, err)
		}
	}
	for _, id := range ids {
		waiter, ok := waiters[id]
		if !ok {
			continue
		}
		waiter.result <- controlResult{err: ErrAborted}
		waiter.stop()
	}
	return errors.Join(errs...)
}

func emitStructuredControl(emit func(OutputEnvelope) error, envelope OutputEnvelope) (resultErr error) {
	if emit == nil {
		return errStructuredControlEmitFailure
	}
	resultErr = errStructuredControlEmitFailure
	defer func() {
		if recover() != nil {
			resultErr = errStructuredControlEmitFailure
		}
	}()
	if err := emit(envelope); err != nil {
		return errStructuredControlEmitFailure
	}
	return nil
}

func invokeStructuredControlCallback(callback func() error) (resultErr error) {
	if callback == nil {
		return nil
	}
	resultErr = errStructuredControlEmitFailure
	defer func() {
		if recover() != nil {
			resultErr = errStructuredControlEmitFailure
		}
	}()
	if err := callback(); err != nil {
		return errStructuredControlEmitFailure
	}
	return nil
}

func (b *ControlBroker) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func (b *ControlBroker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	pending := b.pending
	order := append([]identity.RequestID(nil), b.order...)
	b.pending = make(map[identity.RequestID]pendingControl)
	b.order = nil
	b.mu.Unlock()
	for _, id := range order {
		waiter, ok := pending[id]
		if !ok {
			continue
		}
		waiter.result <- controlResult{err: ErrClosed}
		waiter.stop()
	}
}

func (b *ControlBroker) take(id identity.RequestID) bool {
	b.mu.Lock()
	_, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
		b.removeOrderLocked(id)
	}
	b.mu.Unlock()
	return ok
}

func (b *ControlBroker) remove(id identity.RequestID) {
	b.mu.Lock()
	waiter, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
		b.removeOrderLocked(id)
	}
	b.mu.Unlock()
	if ok {
		waiter.stop()
	}
}

func (b *ControlBroker) removeOrderLocked(id identity.RequestID) {
	for index, candidate := range b.order {
		if candidate == id {
			copy(b.order[index:], b.order[index+1:])
			b.order = b.order[:len(b.order)-1]
			return
		}
	}
}

// DecodeUserText accepts the API user message shape and a legacy bare string.
// A role, when present, is authoritative and must be user.
func DecodeUserText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("user record is missing message")
	}
	var legacy string
	if err := json.Unmarshal(raw, &legacy); err == nil {
		if strings.TrimSpace(legacy) == "" {
			return "", errors.New("user message is empty")
		}
		return legacy, nil
	}
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", errors.New("user message must be an API message object")
	}
	if message.Role != "" && message.Role != "user" {
		return "", fmt.Errorf("expected message role %q, got %q", "user", message.Role)
	}
	if len(message.Content) == 0 {
		return "", errors.New("user API message is missing content")
	}
	var content string
	if err := json.Unmarshal(message.Content, &content); err == nil {
		if strings.TrimSpace(content) == "" {
			return "", errors.New("user message is empty")
		}
		return content, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(message.Content, &blocks); err != nil || len(blocks) == 0 {
		return "", errors.New("user API message content must be text or a nonempty text-block array")
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" && block.Type != "input_text" {
			return "", fmt.Errorf("unsupported user content block type %q", block.Type)
		}
		if strings.TrimSpace(block.Text) == "" {
			return "", errors.New("user content text is empty")
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n"), nil
}
