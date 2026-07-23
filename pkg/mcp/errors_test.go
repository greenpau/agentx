package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type cyclicMCPError struct{}

func (*cyclicMCPError) Error() string { panic("error text must not be inspected") }
func (err *cyclicMCPError) Unwrap() error {
	return err
}

type panickingMCPUnwrapError struct{}

func (*panickingMCPUnwrapError) Error() string { panic("error text must not be inspected") }
func (*panickingMCPUnwrapError) Unwrap() error {
	panic("unwrap payload must remain private")
}

type panickingMCPIsError struct{}

func (*panickingMCPIsError) Error() string { panic("error text must not be inspected") }
func (*panickingMCPIsError) Is(error) bool {
	panic("custom Is must not execute")
}

type wideMCPError struct {
	children []error
}

func (*wideMCPError) Error() string       { panic("error text must not be inspected") }
func (err *wideMCPError) Unwrap() []error { return err.children }

type blockingMCPUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingMCPUnwrapError) Error() string { return "foreign MCP failure" }
func (err *blockingMCPUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.Canceled
}

func TestClassifyMCPErrorHandlesStandardWrappedAndJoinedSentinels(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want mcpErrorClass
	}{
		"cancelled":   {err: fmt.Errorf("wrapped: %w", context.Canceled), want: mcpErrorCancelled},
		"deadline":    {err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: mcpErrorDeadline},
		"closed":      {err: fmt.Errorf("wrapped: %w", ErrClosed), want: mcpErrorClosed},
		"unavailable": {err: fmt.Errorf("wrapped: %w", ErrUnavailable), want: mcpErrorUnavailable},
		"unsupported": {err: fmt.Errorf("wrapped: %w", ErrUnsupportedTransport), want: mcpErrorUnsupportedTransport},
		"protocol":    {err: fmt.Errorf("wrapped: %w", ErrProtocol), want: mcpErrorProtocol},
		"priority":    {err: errors.Join(ErrProtocol, context.Canceled), want: mcpErrorCancelled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyMCPError(test.err); got != test.want {
				t.Fatalf("classifyMCPError() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestMCPErrorProjectionContainsHostileGraphs(t *testing.T) {
	wide := &wideMCPError{children: make([]error, 10_000)}
	for index := range wide.children {
		wide.children[index] = errors.New("untrusted")
	}
	hostile := []error{
		&cyclicMCPError{},
		&panickingMCPUnwrapError{},
		&panickingMCPIsError{},
		wide,
	}
	for _, err := range hostile {
		if got := classifyMCPError(err); got != mcpErrorUnknown {
			t.Fatalf("hostile graph classified as %d", got)
		}
		if got := safeError(err); got != "connection failed" {
			t.Fatalf("safeError() = %q", got)
		}
		if got := safeManagerError(err); got != "operational failure" {
			t.Fatalf("safeManagerError() = %q", got)
		}
		public := publicMCPClientError("provider request failed", err)
		if public == nil || public.Error() != "provider request failed" {
			t.Fatalf("publicMCPClientError() = %v", public)
		}
		if _, projected := projectManagerToolResult(nil, ToolResult{}, err); projected == nil || projected.Error() != "operational failure" {
			t.Fatalf("projectManagerToolResult() = %v", projected)
		}
	}
}

func TestManagerReconcileDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingMCPUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	manager := NewManager(func(Config) (Connection, error) {
		return nil, cause
	})
	done := make(chan Snapshot, 1)
	go func() {
		done <- manager.Reconcile(context.Background(), []Config{{
			Name: "blocking", Transport: TransportStdio, Command: "blocking", Scope: ScopeUser,
		}})
	}()
	select {
	case snapshot := <-done:
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateFailed ||
			len(snapshot.Servers[0].Diagnostics) == 0 ||
			snapshot.Servers[0].Diagnostics[0].Message != "construct connection: operational failure" {
			t.Fatalf("blocking factory snapshot = %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Reconcile blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Manager.Reconcile invoked foreign Unwrap")
	default:
	}
}
