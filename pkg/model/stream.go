package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type sseRead struct {
	record sseRecord
	err    error
}

type callAccumulator struct {
	item      Item
	arguments strings.Builder
}

type responseLifecycle uint8

const (
	responseAwaitingCreated responseLifecycle = iota
	responseCreated
	responseInProgress
	responseTerminal
)

type streamedTextKey struct {
	itemID       string
	outputIndex  int
	contentIndex int
	contentType  ContentType
}

type streamedReasoningKey struct {
	itemID       string
	outputIndex  int
	contentIndex int
	kind         ReasoningDeltaKind
}

type azureStream struct {
	client  *AzureClient
	ctx     context.Context
	cancel  context.CancelFunc
	payload []byte
	// mediaRequest enables the closed media-specific status/code/parameter
	// classifier and seals provider-owned diagnostics before public projection.
	mediaRequest bool

	mu            sync.Mutex
	closed        bool
	body          io.ReadCloser
	attemptCancel context.CancelFunc
	attemptDone   <-chan struct{}
	records       <-chan sseRead
	requestID     string

	retriesUsed      int
	retryStarted     time.Time
	retryDelays      time.Duration
	providerSeen     bool
	terminal         bool
	deferredErr      error
	lifecycle        responseLifecycle
	pending          []Event
	responseID       string
	lastUsage        Usage
	usageSeen        bool
	responseBytes    int
	responseEvents   int
	callAccumulators int

	calls              map[string]*callAccumulator
	completedCalls     map[string]Item
	announcedCallIDs   map[string]struct{}
	streamedTextParts  map[streamedTextKey]*strings.Builder
	textRedactor       StreamRedactor
	textTemplate       Event
	argumentRedactors  map[string]StreamRedactor
	argumentTemplates  map[string]Event
	argumentEvents     map[string][]Event
	argumentOrder      []string
	reasoningRedactors map[streamedReasoningKey]StreamRedactor
	reasoningTemplates map[streamedReasoningKey]Event
	reasoningOrder     []streamedReasoningKey
}

func (s *azureStream) Next() (Event, error) {
	event, err := s.next()
	if err != nil {
		s.releasePayload()
		return Event{}, err
	}
	if err := s.client.validatePublicEventEnvelope(event); err != nil {
		s.terminal = true
		s.pending = nil
		s.deferredErr = nil
		s.closeAttempt()
		s.releasePayload()
		return Event{}, err
	}
	return event, nil
}

func (s *azureStream) next() (Event, error) {
	if err := s.contextError(); err != nil {
		return Event{}, err
	}
	if len(s.pending) > 0 {
		return s.popPending(), nil
	}
	if s.deferredErr != nil {
		err := s.deferredErr
		s.deferredErr = nil
		return Event{}, err
	}
	if s.terminal {
		return Event{}, io.EOF
	}
	for {
		if err := s.contextError(); err != nil {
			s.closeAttempt()
			return Event{}, err
		}
		records, attemptDone, ok := s.currentAttempt()
		if !ok {
			return Event{}, ErrClosed
		}

		timer := time.NewTimer(s.client.watchdog)
		select {
		case <-s.ctx.Done():
			stopTimer(timer)
			s.closeAttempt()
			return Event{}, s.contextError()
		case <-attemptDone:
			stopTimer(timer)
			s.closeAttempt()
			if err := s.contextError(); err != nil {
				return Event{}, err
			}
			failure := fmt.Errorf("%w after %s", ErrRequestTimeout, s.client.requestTimeout)
			if !s.providerSeen {
				if err := s.openWithRetry(failure); err == nil {
					continue
				} else {
					return Event{}, err
				}
			}
			return s.endWithError(failure)
		case <-timer.C:
			s.closeAttempt()
			if !s.providerSeen {
				if err := s.openWithRetry(ErrStreamWatchdog); err == nil {
					continue
				} else {
					return Event{}, err
				}
			}
			return s.endWithError(ErrStreamWatchdog)
		case result, open := <-records:
			stopTimer(timer)
			if !open && result.err == nil {
				result.err = io.EOF
			}
			if result.err != nil {
				select {
				case <-attemptDone:
					s.closeAttempt()
					if err := s.contextError(); err != nil {
						return Event{}, err
					}
					failure := fmt.Errorf("%w after %s", ErrRequestTimeout, s.client.requestTimeout)
					if !s.providerSeen {
						if err := s.openWithRetry(failure); err == nil {
							continue
						} else {
							return Event{}, err
						}
					}
					return s.endWithError(failure)
				default:
				}
			}
			result.err = s.client.sanitizeError(result.err)
			if result.err != nil {
				s.closeAttempt()
				if err := s.contextError(); err != nil {
					return Event{}, err
				}
				inspection := inspectModelError(result.err)
				if inspection.deadline {
					result.err = fmt.Errorf("%w after %s", ErrRequestTimeout, s.client.requestTimeout)
				}
				if !s.providerSeen {
					failure := result.err
					if inspection.eof {
						failure = ErrIncompleteStream
					}
					if err := s.openWithRetry(failure); err == nil {
						continue
					} else {
						return Event{}, err
					}
				}
				if inspection.eof {
					return s.endWithError(ErrIncompleteStream)
				}
				return s.endWithError(result.err)
			}
			if result.record.done {
				s.closeAttempt()
				if !s.providerSeen {
					if err := s.openWithRetry(ErrIncompleteStream); err == nil {
						continue
					} else {
						return Event{}, err
					}
				}
				return s.endWithError(ErrIncompleteStream)
			}
			if len(result.record.data) == 0 {
				// Comments and empty heartbeats count as activity for the next
				// watchdog interval but are not model-visible events.
				continue
			}
			s.responseEvents++
			if s.responseEvents > s.client.maximumResponseEvents {
				s.closeAttempt()
				return s.endWithError(fmt.Errorf("%w: Azure response exceeded %d SSE records", ErrProtocol, s.client.maximumResponseEvents))
			}
			if len(result.record.data) > s.client.maximumResponseBytes-s.responseBytes {
				s.closeAttempt()
				return s.endWithError(fmt.Errorf("%w: Azure response exceeded %d aggregate bytes", ErrProtocol, s.client.maximumResponseBytes))
			}
			s.responseBytes += len(result.record.data)
			s.providerSeen = true
			events, terminal, err := s.parseRecord(result.record)
			if err != nil {
				s.closeAttempt()
				return s.endWithError(s.client.sanitizeError(err))
			}
			if terminal {
				s.terminal = true
				s.closeAttempt()
				s.releasePayload()
			}
			if len(events) == 0 {
				if s.terminal {
					return Event{}, io.EOF
				}
				continue
			}
			s.pending = append(s.pending, events...)
			return s.popPending(), nil
		}
	}
}

func (s *azureStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	clear(s.payload)
	s.payload = nil
	s.mu.Unlock()
	s.cancel()
	s.closeAttempt()
	return nil
}

func (s *azureStream) openWithRetry(initialFailure error) error {
	failure := initialFailure
	for {
		if failure != nil {
			if err := s.contextError(); err != nil {
				return err
			}
			if !retryableTransport(failure) {
				return failure
			}
			if s.retriesUsed >= s.client.maxRetries {
				return s.client.retryExhaustedError(s.retriesUsed+1, failure, 0)
			}
			retryNumber := s.retriesUsed + 1
			delay := s.client.retryDelay(failure, retryNumber)
			if !s.retryWindowAllows(delay) {
				return s.client.retryExhaustedError(s.retriesUsed+1, failure, s.client.retryWindow)
			}
			s.client.notifyRetry(s.client.retryInfo(
				retryNumber+1, s.client.maxRetries+1, delay, failure,
			))
			if err := s.client.sleepBeforeRetry(s.ctx, delay); err != nil {
				return s.client.sanitizeError(err)
			}
			s.retryDelays += delay
			if !s.retryWindowAllows(0) {
				return s.client.retryExhaustedError(s.retriesUsed+1, failure, s.client.retryWindow)
			}
			s.retriesUsed++
		}

		payload, ok := s.payloadSnapshot()
		if !ok {
			return ErrClosed
		}
		response, cancel, err := s.client.execute(s.ctx, payload, s.mediaRequest)
		clear(payload)
		if err != nil {
			failure = err
			continue
		}
		if err := s.installAttempt(response, cancel); err != nil {
			closeProviderBody(response.Body)
			cancel()
			return err
		}
		return nil
	}
}

// retryWindowAllows accounts for both actual elapsed time and the nominal
// delays already accepted. The latter keeps custom/test sleep functions from
// accidentally turning the retry window into an unbounded tight loop.
func (s *azureStream) retryWindowAllows(nextDelay time.Duration) bool {
	elapsed := s.client.currentTime().Sub(s.retryStarted)
	if elapsed < 0 {
		elapsed = 0
	}
	if s.retryDelays > elapsed {
		elapsed = s.retryDelays
	}
	if elapsed > s.client.retryWindow {
		return false
	}
	return nextDelay <= s.client.retryWindow-elapsed
}

func (s *azureStream) installAttempt(response *http.Response, cancel context.CancelFunc) error {
	requestID := response.Header.Get("apim-request-id")
	if requestID == "" {
		requestID = response.Header.Get("x-request-id")
	}
	if err := s.client.validateOpaqueProviderValue("request id", requestID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	attemptContext := s.ctx
	if response.Request != nil {
		attemptContext = response.Request.Context()
	}
	records := pumpSSE(attemptContext, response.Body, s.client.maximumEventBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.body = response.Body
	s.attemptCancel = cancel
	s.attemptDone = attemptContext.Done()
	s.records = records
	s.requestID = requestID
	return nil
}

func pumpSSE(ctx context.Context, body io.Reader, maximumBytes int) <-chan sseRead {
	output := make(chan sseRead, 1)
	go func() {
		defer close(output)
		defer func() {
			if recover() != nil {
				select {
				case output <- sseRead{err: errors.New("provider response reader panicked")}:
				case <-ctx.Done():
				}
			}
		}()
		decoder := newSSEDecoder(body, maximumBytes)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			record, err := decoder.Next()
			result := sseRead{record: record, err: err}
			select {
			case output <- result:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return output
}

func (s *azureStream) currentAttempt() (<-chan sseRead, <-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records, s.attemptDone, !s.closed && s.records != nil
}

func (s *azureStream) payloadSnapshot() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.payload) == 0 {
		return nil, false
	}
	return append([]byte(nil), s.payload...), true
}

func (s *azureStream) releasePayload() {
	s.mu.Lock()
	clear(s.payload)
	s.payload = nil
	s.mu.Unlock()
}

func (s *azureStream) closeAttempt() {
	s.mu.Lock()
	body := s.body
	cancel := s.attemptCancel
	s.body = nil
	s.attemptCancel = nil
	s.attemptDone = nil
	s.records = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if body != nil {
		closeProviderBody(body)
	}
}

func (s *azureStream) contextError() error {
	select {
	case <-s.ctx.Done():
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return ErrClosed
		}
		return s.ctx.Err()
	default:
		return nil
	}
}

func (s *azureStream) popPending() Event {
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event
}

// endWithError preserves any ordinary text suffix held solely for cross-delta
// credential matching, then reports the terminal stream failure on the next
// read. The suffix cannot contain a complete credential; complete matches are
// replaced by literalStreamRedactor before entering pending output.
func (s *azureStream) endWithError(err error) (Event, error) {
	s.terminal = true
	s.releasePayload()
	flushed := s.flushBufferedDeltas()
	if len(flushed) == 0 {
		return Event{}, err
	}
	s.pending = append(s.pending, flushed...)
	s.deferredErr = err
	return s.popPending(), nil
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func requestIDFromError(err error) string {
	providerError := inspectModelError(err).provider
	if providerError != nil {
		return providerError.RequestID
	}
	return ""
}

type azureEventEnvelope struct {
	Type           string            `json:"type"`
	SequenceNumber int64             `json:"sequence_number"`
	Delta          string            `json:"delta"`
	Arguments      string            `json:"arguments"`
	Name           string            `json:"name"`
	ItemID         string            `json:"item_id"`
	OutputIndex    int               `json:"output_index"`
	ContentIndex   int               `json:"content_index"`
	Item           azureResponseItem `json:"item"`
	Response       azureResponse     `json:"response"`
	Error          *azureError       `json:"error"`
	Code           string            `json:"code"`
	Message        string            `json:"message"`
	Param          string            `json:"param"`
}

type azureResponse struct {
	ID                 string              `json:"id"`
	Model              string              `json:"model"`
	Status             string              `json:"status"`
	PreviousResponseID string              `json:"previous_response_id"`
	Output             []azureResponseItem `json:"output"`
	Usage              azureUsage          `json:"usage"`
	Error              *azureError         `json:"error"`
	IncompleteDetails  struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type azureResponseItem struct {
	Type             string                 `json:"type"`
	ID               string                 `json:"id"`
	Role             string                 `json:"role"`
	Status           string                 `json:"status"`
	Phase            string                 `json:"phase"`
	CallID           string                 `json:"call_id"`
	Name             string                 `json:"name"`
	Arguments        string                 `json:"arguments"`
	Output           json.RawMessage        `json:"output"`
	EncryptedContent string                 `json:"encrypted_content"`
	Content          []azureResponseContent `json:"content"`
	Summary          []azureResponseContent `json:"summary"`
}

type azureResponseContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type azureUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	InputDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type azureError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
	Type    string `json:"type"`
}

func (s *azureStream) parseRecord(record sseRecord) ([]Event, bool, error) {
	var envelope azureEventEnvelope
	if err := json.Unmarshal(record.data, &envelope); err != nil {
		return nil, false, fmt.Errorf("%w: decode Azure SSE event: %v", ErrProtocol, err)
	}
	rawType := envelope.Type
	if rawType == "" {
		rawType = record.event
	}
	if err := s.client.validateAzureEnvelopeMetadata(rawType, envelope); err != nil {
		return nil, false, err
	}
	base := Event{
		RawType: rawType, SequenceNumber: envelope.SequenceNumber,
		RequestID: s.requestID, ResponseID: s.responseID,
		ItemID: envelope.ItemID, OutputIndex: envelope.OutputIndex, ContentIndex: envelope.ContentIndex,
	}
	if envelope.Response.ID != "" && s.responseID != "" && envelope.Response.ID != s.responseID {
		return nil, false, fmt.Errorf("%w: response id changed during the stream", ErrProtocol)
	}
	switch rawType {
	case "response.created":
		if s.lifecycle != responseAwaitingCreated {
			return nil, false, fmt.Errorf("%w: response.created arrived after the response lifecycle started", ErrProtocol)
		}
		if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
			return nil, false, err
		}
		if envelope.Response.Status != "in_progress" {
			return nil, false, fmt.Errorf("%w: response.created carried an invalid status", ErrProtocol)
		}
		if err := s.validateWireResponse(envelope.Response); err != nil {
			return nil, false, err
		}
		response, err := convertResponse(envelope.Response)
		if err != nil {
			return nil, false, err
		}
		response.Output = s.client.sanitizeItemTexts(response.Output)
		s.lifecycle = responseCreated
		return []Event{{
			Type: EventResponseCreated, RawType: rawType, SequenceNumber: envelope.SequenceNumber,
			RequestID: s.requestID, ResponseID: response.ID, Response: &response,
		}}, false, nil
	case "response.in_progress":
		if s.lifecycle != responseCreated {
			return nil, false, fmt.Errorf("%w: response.in_progress arrived before response.created or more than once", ErrProtocol)
		}
		if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
			return nil, false, err
		}
		if envelope.Response.Status != "in_progress" {
			return nil, false, fmt.Errorf("%w: response.in_progress carried an invalid status", ErrProtocol)
		}
		if err := s.validateWireResponse(envelope.Response); err != nil {
			return nil, false, err
		}
		response, err := convertResponse(envelope.Response)
		if err != nil {
			return nil, false, err
		}
		response.Output = s.client.sanitizeItemTexts(response.Output)
		s.lifecycle = responseInProgress
		return []Event{{
			Type: EventResponseInProgress, RawType: rawType, SequenceNumber: envelope.SequenceNumber,
			RequestID: s.requestID, ResponseID: response.ID, Response: &response,
		}}, false, nil
	case "response.output_text.delta", "response.refusal.delta":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		base.Type = EventTextDelta
		contentType := ContentOutputText
		if rawType == "response.refusal.delta" {
			contentType = ContentRefusal
		}
		s.rememberTextDelta(base, contentType, envelope.Delta)
		if delta := s.redactVisibleText(base, envelope.Delta); delta != nil {
			return []Event{*delta}, false, nil
		}
		return nil, false, nil
	case "response.reasoning_text.delta", "response.reasoning.delta":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		base.Type = EventReasoningDelta
		base.ReasoningKind = ReasoningContent
		return []Event{s.redactReasoningDelta(base, envelope.Delta)}, false, nil
	case "response.reasoning_summary_text.delta":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		base.Type = EventReasoningDelta
		base.ReasoningKind = ReasoningSummary
		return []Event{s.redactReasoningDelta(base, envelope.Delta)}, false, nil
	case "response.output_item.added":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		if envelope.Item.Type == string(ItemFunctionCall) {
			if err := s.rememberCall(envelope.ItemID, envelope.Item); err != nil {
				return nil, false, err
			}
			if envelope.Item.CallID != "" {
				s.ensureStreamState()
				s.announcedCallIDs[envelope.Item.CallID] = struct{}{}
			}
		}
		return nil, false, nil
	case "response.function_call_arguments.delta":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		call, err := s.callFor(envelope.ItemID)
		if err != nil {
			return nil, false, err
		}
		if len(envelope.Delta) > s.client.maximumCallArgumentBytes-call.arguments.Len() {
			return nil, false, fmt.Errorf("%w: function-call arguments exceeded %d bytes", ErrProtocol, s.client.maximumCallArgumentBytes)
		}
		call.arguments.WriteString(envelope.Delta)
		base.Type = EventFunctionCallArgumentsDelta
		base.Delta = s.redactArgumentDelta(envelope.ItemID, base, envelope.Delta)
		// The cumulative raw arguments deliberately stay inside the adapter
		// until the complete nested JSON document passes semantic inspection.
		// A wire alias may be invisible to literal streaming redaction even
		// though its decoded value is a configured credential.
		base.Call = nil
		if s.client.credentialSanitizer().Empty() {
			return []Event{base}, false, nil
		}
		s.ensureStreamState()
		if base.Delta != "" {
			s.argumentEvents[envelope.ItemID] = append(s.argumentEvents[envelope.ItemID], base)
		}
		return nil, false, nil
	case "response.function_call_arguments.done":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		call, err := s.callFor(envelope.ItemID)
		if err != nil {
			return nil, false, err
		}
		if envelope.Name != "" {
			call.item.Name = envelope.Name
		}
		arguments := envelope.Arguments
		if arguments == "" {
			arguments = call.arguments.String()
		}
		call.item.Arguments = arguments
		event, err := s.completeCall(call.item, base)
		if err != nil || event == nil {
			return nil, false, err
		}
		events := s.flushArgumentDelta(envelope.ItemID)
		return append(events, *event), false, nil
	case "response.output_item.done":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		if envelope.Item.Type != string(ItemFunctionCall) {
			return nil, false, nil
		}
		item, err := convertItem(envelope.Item)
		if err != nil {
			return nil, false, err
		}
		event, err := s.completeCall(item, base)
		if err != nil || event == nil {
			return nil, false, err
		}
		events := s.flushArgumentDelta(item.ID)
		return append(events, *event), false, nil
	case "response.usage":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		usage := convertUsage(envelope.Response.Usage)
		return s.usageEvents(usage, base), false, nil
	case "response.completed":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
			return nil, false, err
		}
		if envelope.Response.Status != "completed" {
			return nil, false, fmt.Errorf("%w: response.completed carried an invalid status", ErrProtocol)
		}
		if err := s.validateWireResponse(envelope.Response); err != nil {
			return nil, false, err
		}
		response, err := convertResponse(envelope.Response)
		if err != nil {
			return nil, false, err
		}
		if err := s.reconcileTerminalOutput(response.Output); err != nil {
			return nil, false, err
		}
		events := make([]Event, 0, len(response.Output)+2)
		events = append(events, s.flushReasoningDeltas()...)
		callEvents := make([]Event, 0, len(response.Output))
		for _, item := range response.Output {
			if item.Type != ItemFunctionCall {
				continue
			}
			callEvent, callErr := s.completeCall(item, base)
			if callErr != nil {
				return nil, false, callErr
			}
			if callEvent != nil {
				callEvents = append(callEvents, *callEvent)
			}
		}
		if err := s.reconcileTerminalCalls(response.Output); err != nil {
			return nil, false, err
		}
		for index := range callEvents {
			safe := s.client.sanitizeItemText(*callEvents[index].Call)
			callEvents[index].Call = &safe
		}
		events = append(events, s.flushArgumentDeltas()...)
		events = append(events, callEvents...)
		response.Output = s.client.sanitizeItemTexts(response.Output)
		events = append(s.flushVisibleText(), events...)
		events = append(events, s.usageEvents(response.Usage, base)...)
		events = append(events, Event{
			Type: EventResponseCompleted, RawType: rawType, SequenceNumber: envelope.SequenceNumber,
			RequestID: s.requestID, ResponseID: response.ID, Response: &response,
		})
		s.lifecycle = responseTerminal
		return events, true, nil
	case "response.failed":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
			return nil, false, err
		}
		if envelope.Response.Status != "failed" {
			return nil, false, fmt.Errorf("%w: response.failed carried an invalid status", ErrProtocol)
		}
		s.lifecycle = responseTerminal
		providerError := s.eventError(envelope.Response.Error, envelope, "response_failed")
		return append(s.flushBufferedDeltas(), s.errorEvent(base, providerError)), true, nil
	case "response.incomplete":
		if err := s.requireInProgress(rawType); err != nil {
			return nil, false, err
		}
		if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
			return nil, false, err
		}
		if envelope.Response.Status != "incomplete" {
			return nil, false, fmt.Errorf("%w: response.incomplete carried an invalid status", ErrProtocol)
		}
		s.lifecycle = responseTerminal
		reason := envelope.Response.IncompleteDetails.Reason
		if reason == "" {
			reason = "response ended incomplete"
		}
		fields, requestID := s.client.sanitizeProviderErrorFields(azureError{Code: "incomplete", Type: "response_incomplete", Message: reason}, s.requestID)
		providerError := s.client.finalizeProviderError(&ProviderError{Code: fields.Code, Type: fields.Type, Param: fields.Param, Message: fields.Message, RequestID: requestID})
		return append(s.flushBufferedDeltas(), s.errorEvent(base, providerError)), true, nil
	case "error", "response.error":
		if envelope.Response.ID != "" {
			if err := s.bindResponseID(envelope.Response.ID, rawType); err != nil {
				return nil, false, err
			}
		}
		s.lifecycle = responseTerminal
		providerError := s.eventError(envelope.Error, envelope, "stream_error")
		return append(s.flushBufferedDeltas(), s.errorEvent(base, providerError)), true, nil
	default:
		// Future control events are intentionally ignored. They still set
		// providerSeen, preventing an unsafe replay of a started response.
		return nil, false, nil
	}
}

func (s *azureStream) bindResponseID(candidate, _ string) error {
	if candidate == "" {
		return fmt.Errorf("%w: provider event is missing its response id", ErrProtocol)
	}
	if err := s.client.validateOpaqueProviderValue("response id", candidate, maximumProviderIDBytes, false); err != nil {
		return err
	}
	if s.responseID == "" {
		s.responseID = candidate
		return nil
	}
	if s.responseID != candidate {
		return fmt.Errorf("%w: response id changed during the stream", ErrProtocol)
	}
	return nil
}

func (s *azureStream) requireInProgress(_ string) error {
	if s.lifecycle != responseInProgress {
		return fmt.Errorf("%w: provider event arrived outside the in-progress response phase", ErrProtocol)
	}
	return nil
}

func (s *azureStream) ensureStreamState() {
	if s.completedCalls == nil {
		s.completedCalls = make(map[string]Item)
	}
	if s.announcedCallIDs == nil {
		s.announcedCallIDs = make(map[string]struct{})
	}
	if s.streamedTextParts == nil {
		s.streamedTextParts = make(map[streamedTextKey]*strings.Builder)
	}
	if s.calls == nil {
		s.calls = make(map[string]*callAccumulator)
	}
	if s.argumentRedactors == nil {
		s.argumentRedactors = make(map[string]StreamRedactor)
	}
	if s.argumentTemplates == nil {
		s.argumentTemplates = make(map[string]Event)
	}
	if s.argumentEvents == nil {
		s.argumentEvents = make(map[string][]Event)
	}
	if s.reasoningRedactors == nil {
		s.reasoningRedactors = make(map[streamedReasoningKey]StreamRedactor)
	}
	if s.reasoningTemplates == nil {
		s.reasoningTemplates = make(map[streamedReasoningKey]Event)
	}
}

func (s *azureStream) rememberTextDelta(base Event, contentType ContentType, delta string) {
	if delta == "" {
		return
	}
	s.ensureStreamState()
	key := streamedTextKey{
		itemID: base.ItemID, outputIndex: base.OutputIndex,
		contentIndex: base.ContentIndex, contentType: contentType,
	}
	part := s.streamedTextParts[key]
	if part == nil {
		part = &strings.Builder{}
		s.streamedTextParts[key] = part
	}
	part.WriteString(delta)
}

func (s *azureStream) redactVisibleText(base Event, delta string) *Event {
	if delta == "" {
		return nil
	}
	if s.client.credentialSanitizer().Empty() {
		base.Delta = delta
		return &base
	}
	if s.textRedactor == nil {
		s.textRedactor = newSetStreamRedactor(s.client.credentialSanitizer())
	}
	s.textTemplate = base
	safe := s.textRedactor.Write(delta)
	if safe == "" {
		return nil
	}
	base.Delta = safe
	return &base
}

func (s *azureStream) flushVisibleText() []Event {
	if s.textRedactor == nil {
		return nil
	}
	safe := s.textRedactor.Flush()
	s.textRedactor = nil
	if safe == "" {
		return nil
	}
	event := s.textTemplate
	event.Type = EventTextDelta
	event.Delta = safe
	return []Event{event}
}

func (s *azureStream) redactArgumentDelta(key string, base Event, delta string) string {
	if delta == "" || s.client.credentialSanitizer().Empty() {
		return delta
	}
	s.ensureStreamState()
	redactor := s.argumentRedactors[key]
	if redactor == nil {
		redactor = newSetStreamRedactor(s.client.credentialSanitizer())
		s.argumentRedactors[key] = redactor
		s.argumentOrder = append(s.argumentOrder, key)
	}
	base.Type = EventFunctionCallArgumentsDelta
	base.Call = nil
	base.Delta = ""
	s.argumentTemplates[key] = base
	return redactor.Write(delta)
}

func (s *azureStream) flushArgumentDelta(key string) []Event {
	events := append([]Event(nil), s.argumentEvents[key]...)
	delete(s.argumentEvents, key)
	redactor := s.argumentRedactors[key]
	if redactor == nil {
		return events
	}
	delete(s.argumentRedactors, key)
	template := s.argumentTemplates[key]
	delete(s.argumentTemplates, key)
	safe := redactor.Flush()
	if safe == "" {
		return events
	}
	template.Delta = safe
	return append(events, template)
}

func (s *azureStream) flushArgumentDeltas() []Event {
	var events []Event
	for _, key := range s.argumentOrder {
		events = append(events, s.flushArgumentDelta(key)...)
	}
	s.argumentOrder = nil
	return events
}

func (s *azureStream) redactReasoningDelta(base Event, delta string) Event {
	if delta == "" || s.client.credentialSanitizer().Empty() {
		base.Delta = delta
		return base
	}
	s.ensureStreamState()
	key := streamedReasoningKey{
		itemID: base.ItemID, outputIndex: base.OutputIndex,
		contentIndex: base.ContentIndex, kind: base.ReasoningKind,
	}
	redactor := s.reasoningRedactors[key]
	if redactor == nil {
		redactor = newSetStreamRedactor(s.client.credentialSanitizer())
		s.reasoningRedactors[key] = redactor
		s.reasoningOrder = append(s.reasoningOrder, key)
	}
	base.Delta = ""
	s.reasoningTemplates[key] = base
	base.Delta = redactor.Write(delta)
	return base
}

func (s *azureStream) flushReasoningDeltas() []Event {
	var events []Event
	for _, key := range s.reasoningOrder {
		redactor := s.reasoningRedactors[key]
		if redactor == nil {
			continue
		}
		delete(s.reasoningRedactors, key)
		template := s.reasoningTemplates[key]
		delete(s.reasoningTemplates, key)
		safe := redactor.Flush()
		if safe != "" {
			template.Delta = safe
			events = append(events, template)
		}
	}
	s.reasoningOrder = nil
	return events
}

func (s *azureStream) flushBufferedDeltas() []Event {
	events := s.flushVisibleText()
	events = append(events, s.flushReasoningDeltas()...)
	s.discardArgumentDeltas()
	return events
}

func (s *azureStream) discardArgumentDeltas() {
	s.argumentRedactors = nil
	s.argumentTemplates = nil
	s.argumentEvents = nil
	s.argumentOrder = nil
}

func (s *azureStream) reconcileTerminalOutput(output []Item) error {
	for key, streamed := range s.streamedTextParts {
		index := key.outputIndex
		if key.itemID != "" {
			index = -1
			for candidateIndex := range output {
				if output[candidateIndex].ID == key.itemID {
					index = candidateIndex
					break
				}
			}
		}
		if index < 0 || index >= len(output) {
			return fmt.Errorf("%w: terminal output omitted streamed message content", ErrProtocol)
		}
		item := output[index]
		if item.Type != ItemMessage || key.contentIndex < 0 || key.contentIndex >= len(item.Content) {
			return fmt.Errorf("%w: terminal output omitted streamed message content", ErrProtocol)
		}
		part := item.Content[key.contentIndex]
		if part.Type != key.contentType || part.Text != streamed.String() {
			return fmt.Errorf("%w: terminal output contradicts streamed message content", ErrProtocol)
		}
	}
	return nil
}

func (s *azureStream) rememberCall(itemID string, wire azureResponseItem) error {
	s.ensureStreamState()
	if err := s.client.validateOpaqueProviderValue("event item id", itemID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	if err := s.client.validateAzureItemMetadata(wire); err != nil {
		return err
	}
	incoming, err := convertItem(wire)
	if err != nil {
		return err
	}
	if incoming.Type == ItemFunctionCall {
		if err := s.client.validateFunctionCallArguments(incoming.Arguments); err != nil {
			return err
		}
	}
	if incoming.ID == "" {
		incoming.ID = itemID
	}
	for _, key := range []string{itemID, wire.ID, wire.CallID} {
		if key != "" && s.calls[key] != nil {
			call := s.calls[key]
			if call.item.ID != "" && incoming.ID != "" && call.item.ID != incoming.ID ||
				call.item.CallID != "" && incoming.CallID != "" && call.item.CallID != incoming.CallID ||
				call.item.Name != "" && incoming.Name != "" && call.item.Name != incoming.Name {
				return fmt.Errorf("%w: repeated function-call announcement changed identity", ErrProtocol)
			}
			if call.item.ID == "" {
				call.item.ID = incoming.ID
			}
			if call.item.CallID == "" {
				call.item.CallID = incoming.CallID
			}
			if call.item.Name == "" {
				call.item.Name = incoming.Name
			}
			if itemID != "" {
				s.calls[itemID] = call
			}
			if incoming.ID != "" {
				s.calls[incoming.ID] = call
			}
			if incoming.CallID != "" {
				s.calls[incoming.CallID] = call
			}
			return nil
		}
	}
	if s.callAccumulators >= s.client.maximumToolCalls {
		return fmt.Errorf("%w: response exceeded %d function calls", ErrProtocol, s.client.maximumToolCalls)
	}
	call := &callAccumulator{item: incoming}
	s.callAccumulators++
	if itemID != "" {
		s.calls[itemID] = call
	}
	if incoming.ID != "" {
		s.calls[incoming.ID] = call
	}
	if incoming.CallID != "" {
		s.calls[incoming.CallID] = call
	}
	return nil
}

func (s *azureStream) callFor(key string) (*callAccumulator, error) {
	s.ensureStreamState()
	if key == "" {
		return nil, fmt.Errorf("%w: function-call argument event is missing its item id", ErrProtocol)
	}
	if err := s.client.validateOpaqueProviderValue("event item id", key, maximumProviderIDBytes, false); err != nil {
		return nil, err
	}
	if call := s.calls[key]; call != nil {
		return call, nil
	}
	if s.callAccumulators >= s.client.maximumToolCalls {
		return nil, fmt.Errorf("%w: response exceeded %d function calls", ErrProtocol, s.client.maximumToolCalls)
	}
	call := &callAccumulator{item: Item{Type: ItemFunctionCall, ID: key}}
	s.calls[key] = call
	s.callAccumulators++
	return call, nil
}

func (s *azureStream) completeCall(item Item, base Event) (*Event, error) {
	s.ensureStreamState()
	if err := s.client.validateItemMetadata(item); err != nil {
		return nil, err
	}
	if item.Type != ItemFunctionCall {
		return nil, fmt.Errorf("%w: completed call has an invalid type", ErrProtocol)
	}
	key := item.CallID
	if key == "" {
		key = item.ID
	}
	if key == "" || item.CallID == "" || item.Name == "" {
		return nil, fmt.Errorf("%w: completed function call is missing id, call_id, or name", ErrProtocol)
	}
	var accumulated *callAccumulator
	for _, candidate := range []string{item.ID, item.CallID} {
		if candidate != "" && s.calls[candidate] != nil {
			accumulated = s.calls[candidate]
			break
		}
	}
	if accumulated != nil {
		if accumulated.item.ID != "" && item.ID != "" && accumulated.item.ID != item.ID ||
			accumulated.item.CallID != "" && accumulated.item.CallID != item.CallID ||
			accumulated.item.Name != "" && accumulated.item.Name != item.Name {
			return nil, fmt.Errorf("%w: completed function call changed streamed identity", ErrProtocol)
		}
		if accumulated.arguments.Len() > 0 {
			streamedArguments := accumulated.arguments.String()
			if item.Arguments == "" {
				item.Arguments = streamedArguments
			} else if item.Arguments != streamedArguments {
				return nil, fmt.Errorf("%w: completed function-call arguments contradict streamed deltas", ErrProtocol)
			}
		}
	}
	if completed, exists := s.completedCalls[key]; exists {
		if completed.ID != item.ID || completed.CallID != item.CallID || completed.Name != item.Name || completed.Arguments != item.Arguments {
			return nil, fmt.Errorf("%w: terminal function call contradicts streamed call data", ErrProtocol)
		}
		return nil, nil
	}
	if len(item.Arguments) > s.client.maximumCallArgumentBytes {
		return nil, fmt.Errorf("%w: function-call arguments exceeded %d bytes", ErrProtocol, s.client.maximumCallArgumentBytes)
	}
	if err := s.client.validateFunctionCallArguments(item.Arguments); err != nil {
		return nil, err
	}
	if len(s.completedCalls) >= s.client.maximumToolCalls {
		return nil, fmt.Errorf("%w: response exceeded %d completed function calls", ErrProtocol, s.client.maximumToolCalls)
	}
	s.completedCalls[key] = item
	copy := s.client.sanitizeItemText(item)
	base.Type = EventFunctionCallCompleted
	base.ItemID = item.ID
	base.Call = &copy
	return &base, nil
}

func (c *AzureClient) sanitizeItemText(item Item) Item {
	item.Content = c.redactContentParts(item.Content)
	item.Summary = c.redactContentParts(item.Summary)
	item.Arguments = c.redact(item.Arguments)
	item.Output = c.redact(item.Output)
	// EncryptedContent is authenticated opaque state. Metadata validation rejects
	// credential reflection before this point because rewriting it would corrupt
	// replay rather than sanitize it.
	return item
}

// redactContentParts carries literal-match state across adjacent provider
// parts. Engine projection concatenates those parts without a delimiter, so
// redacting each string independently would miss a credential split at a part
// boundary.
func (c *AzureClient) redactContentParts(parts []Content) []Content {
	result := append([]Content(nil), parts...)
	if c.credentialSanitizer().Empty() || len(result) == 0 {
		return result
	}
	redactor := newSetStreamRedactor(c.credentialSanitizer())
	for index := range result {
		result[index].Text = redactor.Write(result[index].Text)
	}
	result[len(result)-1].Text += redactor.Flush()
	return result
}

func (c *AzureClient) sanitizeItemTexts(items []Item) []Item {
	result := make([]Item, len(items))
	for index := range items {
		result[index] = c.sanitizeItemText(items[index])
	}
	return result
}

func (s *azureStream) reconcileTerminalCalls(output []Item) error {
	terminal := make(map[string]Item)
	for _, item := range output {
		if item.Type != ItemFunctionCall {
			continue
		}
		terminal[item.CallID] = item
	}
	for callID := range s.announcedCallIDs {
		if _, exists := terminal[callID]; !exists {
			return fmt.Errorf("%w: terminal output omitted an announced function call", ErrProtocol)
		}
	}
	for callID, streamed := range s.completedCalls {
		item, exists := terminal[callID]
		if !exists {
			return fmt.Errorf("%w: terminal output omitted a streamed function call", ErrProtocol)
		}
		if streamed.ID != item.ID || streamed.Name != item.Name || streamed.Arguments != item.Arguments {
			return fmt.Errorf("%w: terminal function call contradicts streamed call data", ErrProtocol)
		}
	}
	return nil
}

func (s *azureStream) validateWireResponse(response azureResponse) error {
	if len(response.Output) > s.client.maximumResponseItems {
		return fmt.Errorf("%w: response exceeded %d output items", ErrProtocol, s.client.maximumResponseItems)
	}
	toolCalls := 0
	for _, item := range response.Output {
		if item.Type != string(ItemFunctionCall) {
			continue
		}
		toolCalls++
		if toolCalls > s.client.maximumToolCalls {
			return fmt.Errorf("%w: response exceeded %d function calls", ErrProtocol, s.client.maximumToolCalls)
		}
		if len(item.Arguments) > s.client.maximumCallArgumentBytes {
			return fmt.Errorf("%w: function-call arguments exceeded %d bytes", ErrProtocol, s.client.maximumCallArgumentBytes)
		}
		if err := s.client.validateFunctionCallArguments(item.Arguments); err != nil {
			return err
		}
	}
	return nil
}

func (s *azureStream) usageEvents(usage Usage, base Event) []Event {
	if usage == (Usage{}) || s.usageSeen && usage == s.lastUsage {
		return nil
	}
	s.usageSeen = true
	s.lastUsage = usage
	copy := usage
	base.Type = EventUsage
	base.Usage = &copy
	return []Event{base}
}

func (s *azureStream) eventError(nested *azureError, envelope azureEventEnvelope, fallbackType string) *ProviderError {
	fields := azureError{Code: envelope.Code, Message: envelope.Message, Param: envelope.Param, Type: fallbackType}
	if nested != nil {
		fields = *nested
		if fields.Type == "" {
			fields.Type = fallbackType
		}
	}
	if fields.Message == "" {
		fields.Message = "Azure Responses API stream failed"
	}
	fields, requestID := s.client.sanitizeProviderErrorFields(fields, s.requestID)
	mediaRejected := s.mediaRequest &&
		providerRejectedMedia(0, fields.Code, fields.Param)
	if s.mediaRequest {
		fields, requestID = sealedMediaRequestProviderError(mediaRejected)
	}
	return s.client.finalizeProviderError(&ProviderError{
		Code: fields.Code, Type: fields.Type,
		Param: fields.Param, Message: fields.Message, RequestID: requestID,
		MediaRejected: mediaRejected,
	})
}

func (s *azureStream) errorEvent(base Event, providerError *ProviderError) Event {
	base.Type = EventError
	base.Error = providerError
	return base
}

func convertResponse(wire azureResponse) (Response, error) {
	response := Response{
		ID: wire.ID, Model: wire.Model, Status: wire.Status,
		PreviousResponseID: wire.PreviousResponseID, Usage: convertUsage(wire.Usage),
		Output: make([]Item, 0, len(wire.Output)),
	}
	for _, wireItem := range wire.Output {
		item, err := convertItem(wireItem)
		if err != nil {
			return Response{}, err
		}
		response.Output = append(response.Output, item)
	}
	return response, nil
}

func convertItem(wire azureResponseItem) (Item, error) {
	item := Item{
		Type: ItemType(wire.Type), ID: wire.ID, Role: Role(wire.Role), Status: wire.Status, Phase: wire.Phase,
		CallID: wire.CallID, Name: wire.Name, Arguments: wire.Arguments,
		EncryptedContent: wire.EncryptedContent,
	}
	switch item.Type {
	case ItemMessage:
		for _, part := range wire.Content {
			text := part.Text
			if part.Type == string(ContentRefusal) {
				text = part.Refusal
			}
			item.Content = append(item.Content, Content{Type: ContentType(part.Type), Text: text})
		}
	case ItemFunctionCall:
	case ItemFunctionCallOutput:
		if len(wire.Output) > 0 {
			if err := json.Unmarshal(wire.Output, &item.Output); err != nil {
				item.Output = string(wire.Output)
			}
		}
	case ItemReasoning:
		for _, part := range wire.Summary {
			item.Summary = append(item.Summary, Content{Type: ContentType(part.Type), Text: part.Text})
		}
	default:
		return Item{}, fmt.Errorf("%w: unsupported response item type", ErrProtocol)
	}
	return item, nil
}

func convertUsage(wire azureUsage) Usage {
	return Usage{
		InputTokens: wire.InputTokens, OutputTokens: wire.OutputTokens, TotalTokens: wire.TotalTokens,
		CachedInputTokens:     wire.InputDetails.CachedTokens,
		ReasoningOutputTokens: wire.OutputDetails.ReasoningTokens,
	}
}
