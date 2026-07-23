package mcp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

const maximumMCPErrorGraphNodes = 128

var (
	trustedMCPSingleWrapType = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedMCPMultiWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedMCPJoinType       = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

type mcpErrorClass uint8

const (
	mcpErrorUnknown mcpErrorClass = iota
	mcpErrorCancelled
	mcpErrorDeadline
	mcpErrorClosed
	mcpErrorUnavailable
	mcpErrorUnsupportedTransport
	mcpErrorProtocol
)

// classifyMCPError classifies exact roots and wrappers whose concrete
// implementation is owned by the Go standard library. It never invokes Error,
// Is, As, or Unwrap on a provider-owned implementation. A standard wrapper may
// reveal a foreign child, but traversal stops there without executing any of
// the child's methods.
func classifyMCPError(err error) mcpErrorClass {
	pending := []error{err}
	seen := make(map[error]struct{})
	var found [mcpErrorProtocol + 1]bool
	for visited := 0; len(pending) > 0 && visited < maximumMCPErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		typ := reflect.TypeOf(current)
		if typ != nil && typ.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
			switch current {
			case context.Canceled:
				found[mcpErrorCancelled] = true
			case context.DeadlineExceeded:
				found[mcpErrorDeadline] = true
			case ErrClosed:
				found[mcpErrorClosed] = true
			case ErrUnavailable:
				found[mcpErrorUnavailable] = true
			case ErrUnsupportedTransport:
				found[mcpErrorUnsupportedTransport] = true
			case ErrProtocol:
				found[mcpErrorProtocol] = true
			}
		}
		children := trustedMCPErrorChildren(current)
		remaining := maximumMCPErrorGraphNodes - visited - 1 - len(pending)
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
	for _, class := range []mcpErrorClass{
		mcpErrorCancelled,
		mcpErrorDeadline,
		mcpErrorClosed,
		mcpErrorUnavailable,
		mcpErrorUnsupportedTransport,
		mcpErrorProtocol,
	} {
		if found[class] {
			return class
		}
	}
	return mcpErrorUnknown
}

func trustedMCPErrorChildren(err error) []error {
	switch reflect.TypeOf(err) {
	case trustedMCPSingleWrapType:
		return []error{err.(interface{ Unwrap() error }).Unwrap()}
	case trustedMCPMultiWrapType, trustedMCPJoinType:
		return err.(interface{ Unwrap() []error }).Unwrap()
	}
	return nil
}
