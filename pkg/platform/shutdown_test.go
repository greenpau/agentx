package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type cyclicShutdownError struct{}

func (*cyclicShutdownError) Error() string { panic("callback Error must not be called") }
func (err *cyclicShutdownError) Unwrap() error {
	return err
}

type panickingShutdownUnwrapError struct{}

func (*panickingShutdownUnwrapError) Error() string { panic("callback Error must not be called") }
func (*panickingShutdownUnwrapError) Unwrap() error {
	panic("private unwrap panic payload")
}

type panickingShutdownIsError struct{}

func (*panickingShutdownIsError) Error() string { panic("callback Error must not be called") }
func (*panickingShutdownIsError) Is(error) bool {
	panic("custom Is must not be called")
}

type panickingShutdownAsError struct{}

func (*panickingShutdownAsError) Error() string { panic("callback Error must not be called") }
func (*panickingShutdownAsError) As(any) bool {
	panic("custom As must not be called")
}

type wideShutdownError struct {
	children []error
}

func (*wideShutdownError) Error() string       { panic("callback Error must not be called") }
func (err *wideShutdownError) Unwrap() []error { return err.children }

type wrappedShutdownError struct {
	child error
}

func (*wrappedShutdownError) Error() string     { panic("callback Error must not be called") }
func (err *wrappedShutdownError) Unwrap() error { return err.child }

type blockingShutdownUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingShutdownUnwrapError) Error() string { return "foreign shutdown failure" }
func (err *blockingShutdownUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.DeadlineExceeded
}

func TestShutdownIsFirstCallWinsAndPhaseOrdered(t *testing.T) {
	manager := NewShutdownManager(ShutdownConfig{
		TerminalTimeout: 100 * time.Millisecond,
		CriticalTimeout: 100 * time.Millisecond,
		HookTimeout:     100 * time.Millisecond,
		ObserverTimeout: 100 * time.Millisecond,
	})
	var mu sync.Mutex
	order := make([]ShutdownStage, 0, 4)
	for _, stage := range []ShutdownStage{StageTerminal, StageCritical, StageHook, StageObserver} {
		stage := stage
		if _, err := manager.Register(stage, string(stage), func(_ context.Context, request ShutdownRequest) error {
			if request.ExitCode != 143 || request.Reason != "sigterm" {
				t.Errorf("callback saw nonlatched request: %+v", request)
			}
			mu.Lock()
			order = append(order, stage)
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	firstDone := manager.Begin(ShutdownRequest{ExitCode: 143, Reason: "sigterm"})
	secondDone := manager.Begin(ShutdownRequest{ExitCode: 0, Reason: "later"})
	if firstDone != secondDone {
		t.Fatal("later shutdown call did not share completion")
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	result := manager.Result()
	if result.Request.ExitCode != 143 || result.Request.Reason != "sigterm" {
		t.Fatalf("request was overwritten: %+v", result.Request)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []ShutdownStage{StageTerminal, StageCritical, StageHook, StageObserver}
	if len(order) != len(want) {
		t.Fatalf("phase count = %v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("phase order = %v", order)
		}
	}
}

func TestShutdownBoundsHangingCallbacksAndRunsSiblings(t *testing.T) {
	manager := NewShutdownManager(ShutdownConfig{CriticalTimeout: 30 * time.Millisecond})
	hanging := make(chan struct{})
	defer close(hanging)
	if _, err := manager.Register(StageCritical, "hang", func(context.Context, ShutdownRequest) error {
		<-hanging
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	siblingRan := make(chan struct{})
	if _, err := manager.Register(StageCritical, "failure", func(context.Context, ShutdownRequest) error {
		close(siblingRan)
		return errors.New("cleanup failed")
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := manager.Shutdown(ctx, ShutdownRequest{ExitCode: 1, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-siblingRan:
	default:
		t.Fatal("sibling cleanup did not run")
	}
	seenTimeout, seenFailure := false, false
	for _, item := range result.Errors {
		seenTimeout = seenTimeout || item.Name == "hang" && item.TimedOut
		seenFailure = seenFailure || item.Name == "failure" && !item.TimedOut
	}
	if !seenTimeout || !seenFailure {
		t.Fatalf("phase errors = %+v", result.Errors)
	}
}

func TestShutdownRegistrationIsSetLike(t *testing.T) {
	manager := NewShutdownManager(ShutdownConfig{})
	var calls int
	callback := func(context.Context, ShutdownRequest) error { calls++; return nil }
	unregister, err := manager.Register(StageCritical, "writer", callback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(StageCritical, "writer", callback); err != nil {
		t.Fatal(err)
	}
	unregister()
	result, err := manager.Shutdown(context.Background(), ShutdownRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(result.Errors) != 0 {
		t.Fatalf("unregistration failed: calls=%d result=%+v", calls, result)
	}
}

func TestShutdownContainsPanicAndRedactsCallbackError(t *testing.T) {
	manager := NewShutdownManager(ShutdownConfig{})
	if _, err := manager.Register(StageCritical, "panic", func(context.Context, ShutdownRequest) error {
		panic("token=do-not-expose")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(StageCritical, "error", func(context.Context, ShutdownRequest) error {
		return errors.New("authorization=do-not-expose")
	}); err != nil {
		t.Fatal(err)
	}
	laterRan := false
	if _, err := manager.Register(StageObserver, "later", func(context.Context, ShutdownRequest) error {
		laterRan = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Shutdown(context.Background(), ShutdownRequest{Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !laterRan {
		t.Fatal("later shutdown phase did not run after callback panic")
	}
	if len(result.Errors) != 2 {
		t.Fatalf("shutdown errors=%+v", result.Errors)
	}
	for _, item := range result.Errors {
		if strings.Contains(item.Message, "do-not-expose") {
			t.Fatalf("shutdown error leaked callback text: %+v", item)
		}
	}
	if result.Errors[0].Message != "shutdown callback panicked" || result.Errors[1].Message != "shutdown callback failed" {
		t.Fatalf("shutdown classifications=%+v", result.Errors)
	}
}

func TestShutdownErrorClassificationDoesNotExecuteForeignMethods(t *testing.T) {
	wide := &wideShutdownError{children: make([]error, 10_000)}
	for index := range wide.children {
		wide.children[index] = errors.New("untrusted")
	}
	wide.children[len(wide.children)-1] = context.DeadlineExceeded

	for _, hostile := range []error{
		&cyclicShutdownError{},
		&panickingShutdownUnwrapError{},
		&panickingShutdownIsError{},
		&panickingShutdownAsError{},
		wide,
	} {
		got := classifyShutdownError(StageCritical, "hostile", hostile)
		if got.Message != "shutdown callback failed" || got.TimedOut {
			t.Fatalf("classifyShutdownError(%T) = %+v", hostile, got)
		}
	}

	for name, test := range map[string]struct {
		err        error
		contextErr error
		message    string
		timedOut   bool
	}{
		"panic": {
			err:     errShutdownCallbackPanicked,
			message: "shutdown callback panicked",
		},
		"deadline": {
			err:      context.DeadlineExceeded,
			message:  "shutdown callback timed out",
			timedOut: true,
		},
		"cancelled": {
			err:     context.Canceled,
			message: "shutdown callback cancelled",
		},
		"wrapped opaque": {
			err:     &wrappedShutdownError{child: context.DeadlineExceeded},
			message: "shutdown callback failed",
		},
		"owned deadline": {
			err:        errors.New("private"),
			contextErr: context.DeadlineExceeded,
			message:    "shutdown callback timed out",
			timedOut:   true,
		},
		"owned cancellation": {
			err:        errors.New("private"),
			contextErr: context.Canceled,
			message:    "shutdown callback cancelled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyShutdownError(StageCritical, name, test.err, test.contextErr)
			if got.Message != test.message || got.TimedOut != test.timedOut {
				t.Fatalf("classification = %+v", got)
			}
		})
	}
}

func TestShutdownDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingShutdownUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	manager := NewShutdownManager(ShutdownConfig{CriticalTimeout: 100 * time.Millisecond})
	if _, err := manager.Register(StageCritical, "blocking", func(context.Context, ShutdownRequest) error {
		return cause
	}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result ShutdownResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := manager.Shutdown(context.Background(), ShutdownRequest{Reason: "test"})
		done <- outcome{result: result, err: err}
	}()
	select {
	case returned := <-done:
		if returned.err != nil || len(returned.result.Errors) != 1 ||
			returned.result.Errors[0].Message != "shutdown callback failed" {
			t.Fatalf("blocking shutdown result=%+v err=%v", returned.result, returned.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Shutdown invoked foreign Unwrap")
	default:
	}
}
