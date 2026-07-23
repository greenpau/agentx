package tool

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	trustedToolErrorStringType = reflect.TypeOf(errors.New(""))
	trustedToolSingleWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedToolMultiWrapType   = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedToolJoinType        = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

// InvocationError is a stable behavioral error category.
type InvocationError struct {
	Code string
	Err  error

	message  string
	semantic bool
}

func (e *InvocationError) Error() string {
	if e == nil || e.message == "" {
		return "tool invocation failed"
	}
	return e.message
}

func (e *InvocationError) Unwrap() error { return e.Err }

func invocationError(code, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return &InvocationError{Code: code, Err: errors.New(message), message: message}
}

// semanticInvocationError is package-owned evidence that postauthorization
// semantic validation selected a more specific closed error code. The
// unexported marker prevents extension descriptors from choosing a code merely
// by returning an externally constructed InvocationError.
func semanticInvocationError(code, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return &InvocationError{Code: code, Err: errors.New(message), message: message, semantic: true}
}

func errorCode(err error, fallback string) string {
	const maximumErrorGraphNodes = 128
	pending := []error{err}
	seen := make(map[error]struct{})
	for visited := 0; len(pending) > 0 && visited < maximumErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		if reflect.TypeOf(current).Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
		}
		if typed, ok := current.(*InvocationError); ok {
			if typed == nil {
				continue
			}
			return canonicalErrorCode(typed.Code, fallback)
		}
		switch reflect.TypeOf(current) {
		case trustedToolMultiWrapType, trustedToolJoinType:
			children := current.(interface{ Unwrap() []error }).Unwrap()
			remaining := maximumErrorGraphNodes - visited - 1 - len(pending)
			if remaining < 0 {
				remaining = 0
			}
			if len(children) > remaining {
				children = children[:remaining]
			}
			for index := len(children) - 1; index >= 0; index-- {
				pending = append(pending, children[index])
			}
		case trustedToolSingleWrapType:
			pending = append(pending, current.(interface{ Unwrap() error }).Unwrap())
		}
	}
	return canonicalErrorCode(fallback, "execution_failed")
}

func safeToolErrorText(err error) (message string) {
	if err == nil {
		return ""
	}
	message = "tool operation failed"
	defer func() {
		if recover() != nil {
			message = "tool operation failed"
		}
	}()
	if typed, ok := err.(*InvocationError); ok {
		return typed.Error()
	}
	switch reflect.TypeOf(err) {
	case trustedToolErrorStringType, trustedToolSingleWrapType, trustedToolMultiWrapType:
		return err.Error()
	default:
		return message
	}
}

func exactToolError(err, target error) bool {
	typ := reflect.TypeOf(err)
	return err != nil && target != nil && typ != nil && typ.Comparable() && err == target
}

func canonicalErrorCode(code, fallback string) string {
	switch code {
	case "cancelled", "denied", "execution_failed", "hook_failed",
		"malformed_result", "permission_failed", "semantic_invalid",
		"sibling_error", "stale_file", "structural_invalid", "timeout",
		"unavailable", "unknown_tool":
		return code
	}
	if fallback != "" && fallback != code {
		return canonicalErrorCode(fallback, "execution_failed")
	}
	return "execution_failed"
}
