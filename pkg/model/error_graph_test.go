package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
)

type cyclicModelError struct{}

func (*cyclicModelError) Error() string { panic("error text must not be inspected") }
func (err *cyclicModelError) Unwrap() error {
	return err
}

type panickingModelUnwrapError struct{}

func (*panickingModelUnwrapError) Error() string { panic("error text must not be inspected") }
func (*panickingModelUnwrapError) Unwrap() error {
	panic("unwrap payload must remain private")
}

type changingModelError struct {
	calls  int
	secret string
}

type blockingModelUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingModelUnwrapError) Error() string { return "foreign transport failure" }
func (err *blockingModelUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.Canceled
}

type blockingModelTextError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (err *blockingModelTextError) Error() string {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return "foreign diagnostic"
}

func (err *changingModelError) Error() string {
	err.calls++
	if err.calls == 1 {
		return "safe first diagnostic"
	}
	return err.secret
}

type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) { panic("reader panic payload") }
func (panicReadCloser) Close() error             { panic("close panic payload") }

func TestExportedModelDiagnosticsDoNotInvokeBlockingForeignError(t *testing.T) {
	cause := &blockingModelTextError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)

	tests := map[string]func() string{
		"retry observation": func() string {
			return (RetryInfo{Attempt: 1, MaxAttempts: 2, Error: cause}).String()
		},
		"retry exhaustion": func() string {
			return (&RetryExhaustedError{Attempts: 2, Last: cause}).Error()
		},
	}
	for name, render := range tests {
		t.Run(name, func(t *testing.T) {
			done := make(chan string, 1)
			go func() { done <- render() }()
			select {
			case message := <-done:
				if !strings.Contains(message, "provider operation failed") {
					t.Fatalf("diagnostic = %q", message)
				}
			case <-time.After(time.Second):
				t.Fatal("exported model diagnostic blocked in foreign Error")
			}
			select {
			case <-cause.called:
				t.Fatal("exported model diagnostic invoked foreign Error")
			default:
			}
		})
	}
}

func TestModelErrorInspectionBoundsHostileGraphs(t *testing.T) {
	hostile := []error{&cyclicModelError{}, &panickingModelUnwrapError{}}
	for _, err := range hostile {
		inspection := inspectModelError(err)
		if inspection.cancelled || inspection.deadline || inspection.protocol || inspection.provider != nil {
			t.Fatal("hostile error received a trusted classification")
		}
		if got := safeModelErrorString(err); got != "provider operation failed" {
			t.Fatalf("safeModelErrorString() = %q", got)
		}
	}

	joined := errors.Join(fmt.Errorf("wrapped: %w", ErrProtocol), context.Canceled)
	inspection := inspectModelError(joined)
	if !inspection.protocol || !inspection.cancelled {
		t.Fatalf("standard joined classification = %#v", inspection)
	}
}

func TestAzureSanitizeErrorNeverRetainsCallerOwnedError(t *testing.T) {
	const secret = "stateful-transport-secret"
	client := &AzureClient{credentialSet: redact.New(secret)}
	source := &changingModelError{secret: secret}
	projected := client.sanitizeError(source)
	if projected == source || source.calls != 0 {
		t.Fatalf("caller error was retained or formatted repeatedly: source_calls=%d projected_type=%T", source.calls, projected)
	}
	if strings.Contains(projected.Error(), secret) || projected.Error() != "provider operation failed" {
		t.Fatalf("projected error = %q", projected.Error())
	}

	provider := &ProviderError{Code: secret, Message: "failed " + secret, RequestID: secret, Retryable: true}
	projected = client.sanitizeError(provider)
	var safeProvider *ProviderError
	if projected == provider || !errors.As(projected, &safeProvider) || !safeProvider.Retryable {
		t.Fatalf("provider classification was not preserved: %T", projected)
	}
	encoded := fmt.Sprintf("%v %+v %#v", projected, safeProvider, safeProvider)
	if strings.Contains(encoded, secret) {
		t.Fatalf("provider error projection exposed credential: %q", encoded)
	}
}

func TestAzureStreamDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingModelUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, cause
	})}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
	done := make(chan error, 1)
	go func() {
		_, err := client.Stream(context.Background(), basicRequest())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || strings.Contains(safeModelErrorString(err), "foreign transport failure") {
			t.Fatalf("blocking transport projection = %T %q", err, safeModelErrorString(err))
		}
	case <-time.After(time.Second):
		t.Fatal("AzureClient.Stream blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("AzureClient.Stream invoked foreign Unwrap")
	default:
	}
}

func TestAzureContainsPanickingTransportAndResponseBody(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		options := noWaitOptions()
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			panic("transport panic payload")
		})}
		client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
		_, err := client.Stream(t.Context(), basicRequest())
		if err == nil || strings.Contains(safeModelErrorString(err), "panic payload") {
			t.Fatalf("transport panic result = %T %q", err, safeModelErrorString(err))
		}
	})

	t.Run("body reader and close", func(t *testing.T) {
		var calls atomic.Int32
		options := noWaitOptions()
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       panicReadCloser{},
			}, nil
		})}
		client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
		stream, err := client.Stream(t.Context(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = stream.Next(); err == nil || strings.Contains(safeModelErrorString(err), "panic payload") {
			t.Fatalf("body panic result = %T %q", err, safeModelErrorString(err))
		}
		if calls.Load() != 2 {
			t.Fatalf("body panic attempts = %d, want 2", calls.Load())
		}
		_ = stream.Close()
	})
}

func TestAzureContainsPanickingRetryCallbacks(t *testing.T) {
	response := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)),
		}
	}

	t.Run("clock jitter and observer", func(t *testing.T) {
		var calls atomic.Int32
		options := AzureOptions{
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response(), nil
			})},
			Now:     func() time.Time { panic("clock panic payload") },
			Jitter:  func(time.Duration) time.Duration { panic("jitter panic payload") },
			Sleep:   func(context.Context, time.Duration) error { return nil },
			OnRetry: func(RetryInfo) { panic("observer panic payload") },
		}
		client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
		_, err := client.Stream(t.Context(), basicRequest())
		var exhausted *RetryExhaustedError
		if !errors.As(err, &exhausted) || calls.Load() != 2 {
			t.Fatalf("callback containment result = %T, calls=%d", err, calls.Load())
		}
	})

	t.Run("sleep", func(t *testing.T) {
		var calls atomic.Int32
		options := AzureOptions{
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response(), nil
			})},
			Jitter: func(time.Duration) time.Duration { return 0 },
			Sleep:  func(context.Context, time.Duration) error { panic("sleep panic payload") },
		}
		client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 1}, options)
		_, err := client.Stream(t.Context(), basicRequest())
		if err == nil || calls.Load() != 1 || strings.Contains(safeModelErrorString(err), "panic payload") {
			t.Fatalf("sleep panic result = %T %q, calls=%d", err, safeModelErrorString(err), calls.Load())
		}
	})
}
