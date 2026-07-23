package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
)

const (
	maximumProviderEventTypeBytes  = 128
	maximumProviderIDBytes         = 256
	maximumProviderNameBytes       = 128
	maximumProviderEnumBytes       = 64
	maximumProviderDiagnosticBytes = 64 << 10
	maximumEngineErrorGraphNodes   = 128
)

var (
	trustedEngineErrorStringType = reflect.TypeOf(errors.New(""))
	trustedEngineSingleWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedEngineMultiWrapType   = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedEngineJoinType        = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

// publicEngineError retains errors.Is classification without exposing an
// untrusted provider error through Unwrap or As. The original cause may contain
// credential material even when its presentation string has been sanitized.
type publicEngineError struct {
	message string
	matches []error
}

func (e *publicEngineError) Error() string {
	if e == nil {
		return "engine operation failed"
	}
	return e.message
}
func (e *publicEngineError) Is(target error) bool {
	if e == nil {
		return false
	}
	return engineErrorMatches(e.matches, target)
}

func engineErrorMatches(matches []error, target error) bool {
	if target == nil {
		return false
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || !targetType.Comparable() {
		return false
	}
	for _, candidate := range matches {
		if candidate == target {
			return true
		}
	}
	return false
}

func engineErrorIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	return engineErrorMatches(inspectEngineError(err).matches, target)
}
func (e *publicEngineError) Format(state fmt.State, verb rune) {
	message := "engine operation failed"
	if e != nil {
		message = e.message
	}
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", message)
	default:
		_, _ = fmt.Fprint(state, message)
	}
}

func (e *Engine) publicError(cause error) error {
	if cause == nil {
		return nil
	}
	return e.publicErrorWithMessage(cause, safeEngineErrorString(cause))
}

func (e *Engine) publicErrorWithMessage(cause error, message string) error {
	if cause == nil {
		return nil
	}
	inspection := inspectEngineError(cause)
	return e.publicErrorFromInspection(message, inspection)
}

func (e *Engine) publicErrorFromInspection(message string, inspection engineErrorInspection) error {
	message = e.normalizedPublicErrorMessage(message)
	return &publicEngineError{message: message, matches: append([]error(nil), inspection.matches...)}
}

func publicConfigError(config Config, cause error, message string) error {
	return (&Engine{config: config}).publicErrorWithMessage(cause, message)
}

type engineErrorInspection struct {
	matches     []error
	cancelled   bool
	deadline    bool
	eof         bool
	delivery    bool
	persistence bool
	provider    *model.ProviderError
}

// inspectEngineError classifies exact roots, package-owned sealed snapshots,
// and wrappers whose concrete implementation is owned by the Go standard
// library. It never invokes Error, Is, As, or Unwrap on a provider- or
// callback-owned implementation. Standard wrappers may reveal a foreign child,
// but traversal stops at that child without executing any of its methods.
func inspectEngineError(err error) engineErrorInspection {
	pending := []error{err}
	seen := make(map[error]struct{})
	var result engineErrorInspection
	for visited := 0; len(pending) > 0 && visited < maximumEngineErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		currentType := reflect.TypeOf(current)
		if currentType != nil && currentType.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
			if len(result.matches) < maximumEngineErrorGraphNodes &&
				!engineErrorMatches(result.matches, current) {
				result.matches = append(result.matches, current)
			}
			switch current {
			case context.Canceled:
				result.cancelled = true
			case context.DeadlineExceeded:
				result.deadline = true
			case io.EOF:
				result.eof = true
			}
		}
		switch typed := current.(type) {
		case *publicEngineError:
			if typed != nil {
				mergeEngineInspection(&result, engineErrorInspection{matches: typed.matches})
			}
		case *callbackOperationError:
			if typed != nil {
				mergeEngineInspection(&result, typed.inspection)
			}
		case *eventDeliveryError:
			result.delivery = true
			if typed != nil {
				mergeEngineInspection(&result, typed.inspection)
			}
		case *eventPersistenceError:
			result.persistence = true
			if typed != nil {
				mergeEngineInspection(&result, typed.inspection)
			}
		default:
			classifyEngineErrorNode(current, &result)
		}
		children := trustedEngineErrorChildren(current)
		remaining := maximumEngineErrorGraphNodes - visited - 1 - len(pending)
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
	return result
}

func inspectEngineErrorWithContext(err, contextErr error) engineErrorInspection {
	result := inspectEngineError(err)
	switch contextErr {
	case context.Canceled:
		mergeEngineInspection(&result, engineErrorInspection{
			matches:   []error{context.Canceled},
			cancelled: true,
		})
	case context.DeadlineExceeded:
		mergeEngineInspection(&result, engineErrorInspection{
			matches:  []error{context.DeadlineExceeded},
			deadline: true,
		})
	}
	return result
}

func mergeEngineInspection(target *engineErrorInspection, source engineErrorInspection) {
	if target == nil {
		return
	}
	target.cancelled = target.cancelled || source.cancelled
	target.deadline = target.deadline || source.deadline
	target.eof = target.eof || source.eof
	target.delivery = target.delivery || source.delivery
	target.persistence = target.persistence || source.persistence
	if target.provider == nil {
		target.provider = source.provider
	}
	for _, match := range source.matches {
		if len(target.matches) >= maximumEngineErrorGraphNodes || match == nil {
			break
		}
		matchType := reflect.TypeOf(match)
		if matchType == nil || !matchType.Comparable() || engineErrorMatches(target.matches, match) {
			continue
		}
		target.matches = append(target.matches, match)
		switch match {
		case context.Canceled:
			target.cancelled = true
		case context.DeadlineExceeded:
			target.deadline = true
		case io.EOF:
			target.eof = true
		}
		classifyEngineErrorNode(match, target)
	}
}

func classifyEngineErrorNode(err error, result *engineErrorInspection) {
	switch typed := err.(type) {
	case *model.ProviderError:
		if typed != nil && result.provider == nil {
			result.provider = typed
		}
	}
}

func trustedEngineErrorChildren(err error) []error {
	switch reflect.TypeOf(err) {
	case trustedEngineSingleWrapType:
		return []error{err.(interface{ Unwrap() error }).Unwrap()}
	case trustedEngineMultiWrapType, trustedEngineJoinType:
		return err.(interface{ Unwrap() []error }).Unwrap()
	}
	return nil
}

// safeEngineErrorString formats only package-owned errors and standard-library
// errors whose Error implementation stores an already-rendered string.
// Foreign errors receive a fixed diagnostic without executing callback code.
func safeEngineErrorString(err error) (message string) {
	if err == nil {
		return ""
	}
	message = "engine operation failed"
	defer func() {
		if recover() != nil {
			message = "engine operation failed"
		}
	}()
	switch typed := err.(type) {
	case *publicEngineError:
		return typed.Error()
	case *callbackOperationError:
		return typed.Error()
	case *eventDeliveryError:
		return typed.Error()
	case *eventPersistenceError:
		return typed.Error()
	case *model.ProviderError:
		return typed.Error()
	}
	switch reflect.TypeOf(err) {
	case trustedEngineErrorStringType, trustedEngineSingleWrapType, trustedEngineMultiWrapType:
		return err.Error()
	default:
		return message
	}
}

// validateEngineIdentityProjection rejects configuration values that would
// send configured credential material to the provider or expose it through
// the public status/session surfaces. The permutation check closes split-field
// reconstruction; the canonical JSON pass closes escaped and structural
// aliases before the engine becomes observable.
func validateEngineIdentityProjection(config Config) error {
	credentials := config.CredentialSanitizer
	if credentials == nil || credentials.Empty() {
		return nil
	}
	values := []string{
		string(config.SessionID),
		config.Model,
		config.ReasoningEffort,
		config.Model,
	}
	if credentials.ContainsAcrossPermutations(values) {
		return errors.New("engine identity reflects configured credential material")
	}
	projection := struct {
		SessionID       protocol.SessionID `json:"session_id"`
		Model           string             `json:"model"`
		ReasoningEffort string             `json:"reasoning_effort"`
		UsageModel      string             `json:"usage_model"`
	}{
		SessionID:       config.SessionID,
		Model:           config.Model,
		ReasoningEffort: config.ReasoningEffort,
		UsageModel:      config.Model,
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return errors.New("engine identity could not be safely encoded")
	}
	reflected, err := credentials.JSONContains(encoded)
	if err != nil {
		return errors.New("engine identity could not be safely inspected")
	}
	if reflected {
		return errors.New("engine identity reflects configured credential material")
	}
	return nil
}

// normalizedPublicErrorMessage reapplies the complete sanitizer after control
// normalization. Replacing an unsafe control with U+FFFD can itself construct
// a configured credential that was not present in the provider's raw value.
func (e *Engine) normalizedPublicErrorMessage(value string) string {
	value = e.sanitizeText(value)
	value = string(valueWithoutUnsafeControls(value))
	return e.sanitizeText(value)
}

func valueWithoutUnsafeControls(value string) []rune {
	result := make([]rune, 0, len(value))
	for _, character := range value {
		if unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) || unicode.IsControl(character) && character != '\n' && character != '\t' {
			result = append(result, '\uFFFD')
			continue
		}
		result = append(result, character)
	}
	return result
}

// validateProviderRequestEnvelope is the final shared boundary before a
// concrete or custom provider receives the effective request. Function-call
// arguments need a second semantic JSON pass because the outer request stores
// them as strings, while the canonical outer encoding closes cross-field and
// structural framing seams.
func (e *Engine) validateProviderRequestEnvelope(request model.Request) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: effective model request is invalid", model.ErrProtocol)
	}
	credentials := e.config.CredentialSanitizer
	if credentials != nil && !credentials.Empty() {
		for _, item := range request.Input {
			if item.Type != model.ItemFunctionCall {
				continue
			}
			reflected, err := credentials.JSONContains([]byte(item.Arguments))
			if err != nil {
				return fmt.Errorf("%w: effective model request arguments could not be safely inspected", model.ErrProtocol)
			}
			if reflected {
				return fmt.Errorf("%w: effective model request reflected configured credential material", model.ErrProtocol)
			}
		}
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("%w: effective model request could not be safely encoded", model.ErrProtocol)
	}
	if e.sanitizeText(string(encoded)) != string(encoded) {
		return fmt.Errorf("%w: effective model request reflected configured credential material", model.ErrProtocol)
	}
	if credentials == nil || credentials.Empty() {
		return nil
	}
	reflected, err := credentials.JSONContains(encoded)
	if err != nil {
		return fmt.Errorf("%w: effective model request could not be safely inspected", model.ErrProtocol)
	}
	if reflected {
		return fmt.Errorf("%w: effective model request reflected configured credential material", model.ErrProtocol)
	}
	return nil
}

// validateOpaqueProviderValue rejects provider-controlled metadata which must
// be retained exactly for correlation or replay. Redacting these values would
// silently break identity, so credential reflection is a protocol failure.
func (e *Engine) validateOpaqueProviderValue(field, value string, maximum int, allowSpace bool) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || maximum > 0 && len(value) > maximum {
		return fmt.Errorf("%w: provider %s is unsafe", model.ErrProtocol, field)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) || !allowSpace && unicode.IsSpace(character) {
			return fmt.Errorf("%w: provider %s is unsafe", model.ErrProtocol, field)
		}
	}
	if e.sanitizeText(value) != value {
		return fmt.Errorf("%w: provider %s reflected configured credential", model.ErrProtocol, field)
	}
	return nil
}

func (e *Engine) validateProviderItemMetadata(item model.Item) error {
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "item type", value: string(item.Type), maximum: maximumProviderEnumBytes},
		{name: "item id", value: item.ID, maximum: maximumProviderIDBytes},
		{name: "API response id", value: item.APIResponseID, maximum: maximumProviderIDBytes},
		{name: "item role", value: string(item.Role), maximum: maximumProviderEnumBytes},
		{name: "item status", value: item.Status, maximum: maximumProviderEnumBytes},
		{name: "item phase", value: item.Phase, maximum: maximumProviderEnumBytes},
		{name: "call id", value: item.CallID, maximum: maximumProviderIDBytes},
		{name: "tool name", value: item.Name, maximum: maximumProviderNameBytes, allowSpace: true},
		{name: "encrypted reasoning state", value: item.EncryptedContent, allowSpace: true},
	}
	for _, field := range fields {
		if err := e.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	for _, part := range item.Content {
		if err := e.validateOpaqueProviderValue("content type", string(part.Type), maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	for _, part := range item.Summary {
		if err := e.validateOpaqueProviderValue("summary type", string(part.Type), maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) validateRestoredMessageMetadata(message *protocol.Message) error {
	if message == nil {
		return nil
	}
	if err := e.validateOpaqueProviderValue("API message id", message.APIMessageID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	if err := e.validateOpaqueProviderValue("API response id", message.APIResponseID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	return e.validateOpaqueProviderValue("item phase", message.Phase, maximumProviderEnumBytes, false)
}

func (e *Engine) validateRestoredToolCallMetadata(call *protocol.ToolCall) error {
	if call == nil {
		return nil
	}
	if err := e.validateOpaqueProviderValue("call id", string(call.ID), maximumProviderIDBytes, false); err != nil {
		return err
	}
	if err := e.validateOpaqueProviderValue("tool name", call.Name, maximumProviderNameBytes, true); err != nil {
		return err
	}
	return e.validateOpaqueProviderValue("API response id", call.APIResponseID, maximumProviderIDBytes, false)
}

func (e *Engine) validateRestoredToolResultMetadata(result *protocol.ToolResult) error {
	if result == nil {
		return nil
	}
	if err := e.validateOpaqueProviderValue("call id", string(result.ToolUseID), maximumProviderIDBytes, false); err != nil {
		return err
	}
	return e.validateOpaqueProviderValue("tool name", result.ToolName, maximumProviderNameBytes, true)
}

func (e *Engine) validateProviderItemsMetadata(items []model.Item) error {
	for _, item := range items {
		if err := e.validateProviderItemMetadata(item); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) validateProviderResponseMetadata(response *model.Response) error {
	if response == nil {
		return nil
	}
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "response id", value: response.ID, maximum: maximumProviderIDBytes},
		{name: "response model", value: response.Model, maximum: maximumProviderIDBytes},
		{name: "response status", value: response.Status, maximum: maximumProviderEnumBytes},
		{name: "previous response id", value: response.PreviousResponseID, maximum: maximumProviderIDBytes},
	}
	for _, field := range fields {
		if err := e.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	return e.validateProviderItemsMetadata(response.Output)
}

func (e *Engine) validateProviderErrorMetadata(providerError *model.ProviderError) error {
	if providerError == nil {
		return nil
	}
	values := []string{providerError.Code, providerError.Type, providerError.Param, providerError.Message, providerError.RequestID}
	for _, value := range values {
		if err := e.validateOpaqueProviderValue("error metadata", value, maximumProviderDiagnosticBytes, true); err != nil {
			return err
		}
	}
	order := make([]int, 0, len(values))
	used := make([]bool, len(values))
	var search func() bool
	search = func() bool {
		if len(order) == len(values) {
			var joined strings.Builder
			for _, index := range order {
				joined.WriteString(values[index])
			}
			value := joined.String()
			return e.sanitizeText(value) != value
		}
		for index := range values {
			if used[index] {
				continue
			}
			used[index] = true
			order = append(order, index)
			if search() {
				return true
			}
			order = order[:len(order)-1]
			used[index] = false
		}
		return false
	}
	if search() {
		return fmt.Errorf("%w: provider error metadata reflected configured credential", model.ErrProtocol)
	}
	return nil
}

func (e *Engine) validateProviderErrorCause(cause error) error {
	if cause == nil {
		return nil
	}
	inspection := inspectEngineError(cause)
	if inspection.provider != nil {
		providerError := *inspection.provider
		if err := e.validateProviderErrorMetadata(&providerError); err != nil {
			return err
		}
	}
	// Freeze both presentation text and standard-unwrapped classification at
	// the provider boundary. Later turn classification and finalization must
	// not re-enter a stateful caller-owned Error, Is, As, or Unwrap method.
	return e.publicErrorFromInspection(safeEngineErrorString(cause), inspection)
}

func (e *Engine) validateProviderEventMetadata(event model.Event) error {
	if !validModelEventType(event.Type) {
		return fmt.Errorf("%w: provider emitted an unknown event type", model.ErrProtocol)
	}
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "event type", value: string(event.Type), maximum: maximumProviderEventTypeBytes},
		{name: "raw event type", value: event.RawType, maximum: maximumProviderEventTypeBytes},
		{name: "request id", value: event.RequestID, maximum: maximumProviderIDBytes},
		{name: "response id", value: event.ResponseID, maximum: maximumProviderIDBytes},
		{name: "event item id", value: event.ItemID, maximum: maximumProviderIDBytes},
		{name: "reasoning delta kind", value: string(event.ReasoningKind), maximum: maximumProviderEnumBytes},
	}
	for _, field := range fields {
		if err := e.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	if event.Call != nil {
		if err := e.validateProviderItemMetadata(*event.Call); err != nil {
			return err
		}
	}
	if err := e.validateProviderErrorMetadata(event.Error); err != nil {
		return err
	}
	return e.validateProviderResponseMetadata(event.Response)
}

func validModelEventType(eventType model.EventType) bool {
	switch eventType {
	case model.EventResponseCreated,
		model.EventResponseInProgress,
		model.EventTextDelta,
		model.EventReasoningDelta,
		model.EventFunctionCallArgumentsDelta,
		model.EventFunctionCallCompleted,
		model.EventUsage,
		model.EventResponseCompleted,
		model.EventError:
		return true
	default:
		return false
	}
}
