package model

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
)

const maximumModelErrorGraphNodes = 128

var (
	trustedModelErrorStringType = reflect.TypeOf(errors.New(""))
	trustedModelSingleWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedModelMultiWrapType   = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedModelJoinType        = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

type modelErrorInspection struct {
	cancelled  bool
	deadline   bool
	closed     bool
	protocol   bool
	eof        bool
	bufferFull bool
	provider   *ProviderError
}

// inspectModelError classifies exact roots, package-owned error state, and
// wrappers whose concrete implementation is owned by the Go standard library.
// It never invokes Error, Is, As, or Unwrap on a transport- or body-owned
// implementation. A standard wrapper may reveal a foreign child, but
// traversal stops at that child without executing any of its methods.
func inspectModelError(err error) modelErrorInspection {
	pending := []error{err}
	seen := make(map[error]struct{})
	var result modelErrorInspection
	for visited := 0; len(pending) > 0 && visited < maximumModelErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		if provider, ok := current.(*ProviderError); ok && result.provider == nil {
			result.provider = provider
		}
		typ := reflect.TypeOf(current)
		if typ != nil && typ.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
			switch current {
			case context.Canceled:
				result.cancelled = true
			case context.DeadlineExceeded:
				result.deadline = true
			case ErrClosed:
				result.closed = true
			case ErrProtocol:
				result.protocol = true
			case io.EOF:
				result.eof = true
			case bufio.ErrBufferFull:
				result.bufferFull = true
			}
		}
		children := trustedModelErrorChildren(current)
		remaining := maximumModelErrorGraphNodes - visited - 1 - len(pending)
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

func trustedModelErrorChildren(err error) []error {
	switch typed := err.(type) {
	case *RetryExhaustedError:
		if typed == nil {
			return nil
		}
		return []error{typed.Last}
	}
	switch reflect.TypeOf(err) {
	case trustedModelSingleWrapType:
		return []error{err.(interface{ Unwrap() error }).Unwrap()}
	case trustedModelMultiWrapType, trustedModelJoinType:
		return err.(interface{ Unwrap() []error }).Unwrap()
	}
	return nil
}

func safeModelErrorString(err error) (message string) {
	if err == nil {
		return ""
	}
	message = "provider operation failed"
	defer func() {
		if recover() != nil {
			message = "provider operation failed"
		}
	}()
	switch typed := err.(type) {
	case *ProviderError:
		return typed.Error()
	case *RetryExhaustedError:
		if typed != nil && typed.displaySet {
			return typed.display
		}
		return message
	case *azureRequestCompositionError:
		return typed.Error()
	}
	switch reflect.TypeOf(err) {
	case trustedModelErrorStringType, trustedModelSingleWrapType, trustedModelMultiWrapType:
		return err.Error()
	default:
		return message
	}
}

func doAzureRequest(client *http.Client, request *http.Request) (response *http.Response, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = errors.New("Azure response transport panicked")
		}
	}()
	return client.Do(request)
}

func closeProviderBody(body io.Closer) {
	if body == nil {
		return
	}
	// A caller-supplied RoundTripper also owns the response Body
	// implementation. Close is therefore no more trustworthy than Read: it
	// may panic or ignore cancellation forever. Cleanup must never hold the
	// stream coordinator hostage. Callers transfer each body here at most
	// once, so an uncooperative cleanup can strand at most one goroutine per
	// bounded request attempt.
	go func() {
		defer func() {
			_ = recover()
		}()
		_ = body.Close()
	}()
}
