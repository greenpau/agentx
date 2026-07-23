package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockingResponseBody struct {
	releaseRead  <-chan struct{}
	releaseClose <-chan struct{}
	reads        *atomic.Int32
	closes       *atomic.Int32
	activeReads  *atomic.Int32
	activeCloses *atomic.Int32
}

func (body *blockingResponseBody) Read([]byte) (int, error) {
	body.reads.Add(1)
	body.activeReads.Add(1)
	defer body.activeReads.Add(-1)
	<-body.releaseRead
	return 0, io.EOF
}

func (body *blockingResponseBody) Close() error {
	body.closes.Add(1)
	body.activeCloses.Add(1)
	defer body.activeCloses.Add(-1)
	<-body.releaseClose
	return nil
}

type prefixThenBlockingBody struct {
	reader       *strings.Reader
	releaseRead  <-chan struct{}
	releaseClose <-chan struct{}
	reads        *atomic.Int32
	closes       *atomic.Int32
	activeReads  *atomic.Int32
	activeCloses *atomic.Int32
}

func (body *prefixThenBlockingBody) Read(buffer []byte) (int, error) {
	if body.reader.Len() > 0 {
		return body.reader.Read(buffer)
	}
	body.reads.Add(1)
	body.activeReads.Add(1)
	defer body.activeReads.Add(-1)
	<-body.releaseRead
	return 0, io.EOF
}

func (body *prefixThenBlockingBody) Close() error {
	body.closes.Add(1)
	body.activeCloses.Add(1)
	defer body.activeCloses.Add(-1)
	<-body.releaseClose
	return nil
}

func TestAzureHostileStreamingBodiesCannotBlockCoordinator(t *testing.T) {
	t.Run("blocked reads and closes remain attempt bounded", func(t *testing.T) {
		releaseRead := make(chan struct{})
		releaseClose := make(chan struct{})
		var calls atomic.Int32
		var reads atomic.Int32
		var closes atomic.Int32
		var activeReads atomic.Int32
		var activeCloses atomic.Int32
		options := noWaitOptions()
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &blockingResponseBody{
					releaseRead: releaseRead, releaseClose: releaseClose,
					reads: &reads, closes: &closes,
					activeReads: &activeReads, activeCloses: &activeCloses,
				},
			}, nil
		})}
		client := newTestClient(t, "http://localhost", configOverrides{
			watchdog: 10 * time.Millisecond, maxRetries: 2,
		}, options)

		started := time.Now()
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.Next()
		var exhausted *RetryExhaustedError
		if !errors.As(err, &exhausted) || !errors.Is(err, ErrStreamWatchdog) {
			t.Fatalf("blocked stream result = %T %v", err, err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("watchdog coordinator took %s", elapsed)
		}
		waitForAtomicValue(t, &reads, 3)
		waitForAtomicValue(t, &closes, 3)
		if calls.Load() != 3 || activeReads.Load() != 3 || activeCloses.Load() != 3 {
			t.Fatalf(
				"attempt resources calls=%d reads=%d active_reads=%d closes=%d active_closes=%d",
				calls.Load(), reads.Load(), activeReads.Load(), closes.Load(), activeCloses.Load(),
			)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if reads.Load() != 3 || closes.Load() != 3 {
			t.Fatalf("close started duplicate work: reads=%d closes=%d", reads.Load(), closes.Load())
		}

		close(releaseRead)
		close(releaseClose)
		waitForAtomicValue(t, &activeReads, 0)
		waitForAtomicValue(t, &activeCloses, 0)
	})

	t.Run("blocked post-terminal read and close do not delay terminal events", func(t *testing.T) {
		releaseRead := make(chan struct{})
		releaseClose := make(chan struct{})
		var reads atomic.Int32
		var closes atomic.Int32
		var activeReads atomic.Int32
		var activeCloses atomic.Int32
		options := noWaitOptions()
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			response := streamingResponse("resp_blocked_close")
			payload, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			response.Body = &prefixThenBlockingBody{
				reader:      strings.NewReader(string(payload)),
				releaseRead: releaseRead, releaseClose: releaseClose,
				reads: &reads, closes: &closes,
				activeReads: &activeReads, activeCloses: &activeCloses,
			}
			return response, nil
		})}
		client := newTestClient(t, "http://localhost", configOverrides{
			watchdog: time.Second, maxRetries: 1,
		}, options)

		started := time.Now()
		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		events, err := Drain(stream)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("terminal response waited for Close for %s", elapsed)
		}
		if len(events) != 3 || events[len(events)-1].Type != EventResponseCompleted {
			t.Fatalf("terminal events = %v", eventTypes(events))
		}
		waitForAtomicValue(t, &reads, 1)
		waitForAtomicValue(t, &closes, 1)
		if activeReads.Load() != 1 || activeCloses.Load() != 1 {
			t.Fatalf("active reads=%d closes=%d, want 1 each", activeReads.Load(), activeCloses.Load())
		}

		close(releaseRead)
		close(releaseClose)
		waitForAtomicValue(t, &activeReads, 0)
		waitForAtomicValue(t, &activeCloses, 0)
	})

	t.Run("attempt deadline after provider event is exact and never retries", func(t *testing.T) {
		releaseRead := make(chan struct{})
		releaseClose := make(chan struct{})
		var calls atomic.Int32
		var reads atomic.Int32
		var closes atomic.Int32
		var activeReads atomic.Int32
		var activeCloses atomic.Int32
		const prefix = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_deadline\",\"model\":\"gpt-5.6-sol\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
		options := noWaitOptions()
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Request:    (&http.Request{}).WithContext(context.Background()),
				Body: &prefixThenBlockingBody{
					reader:      strings.NewReader(prefix),
					releaseRead: releaseRead, releaseClose: releaseClose,
					reads: &reads, closes: &closes,
					activeReads: &activeReads, activeCloses: &activeCloses,
				},
			}, nil
		})}
		client := newTestClient(t, "http://localhost", configOverrides{
			watchdog: time.Second, maxRetries: 3,
		}, options)
		client.requestTimeout = 20 * time.Millisecond

		stream, err := client.Stream(context.Background(), basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		event, err := stream.Next()
		if err != nil || event.Type != EventResponseCreated {
			t.Fatalf("created event = %#v, err=%v", event, err)
		}
		_, err = stream.Next()
		if !errors.Is(err, ErrRequestTimeout) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, context.Canceled) {
			t.Fatalf("attempt deadline result = %T %v", err, err)
		}
		waitForAtomicValue(t, &reads, 1)
		waitForAtomicValue(t, &closes, 1)
		if calls.Load() != 1 || activeReads.Load() != 1 || activeCloses.Load() != 1 {
			t.Fatalf(
				"attempt resources calls=%d reads=%d active_reads=%d closes=%d active_closes=%d",
				calls.Load(), reads.Load(), activeReads.Load(), closes.Load(), activeCloses.Load(),
			)
		}

		close(releaseRead)
		close(releaseClose)
		waitForAtomicValue(t, &activeReads, 0)
		waitForAtomicValue(t, &activeCloses, 0)
	})
}

func TestAzureHostileErrorBodiesRespectAttemptDeadline(t *testing.T) {
	releaseRead := make(chan struct{})
	releaseClose := make(chan struct{})
	var calls atomic.Int32
	var reads atomic.Int32
	var closes atomic.Int32
	var activeReads atomic.Int32
	var activeCloses atomic.Int32
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: &blockingResponseBody{
				releaseRead: releaseRead, releaseClose: releaseClose,
				reads: &reads, closes: &closes,
				activeReads: &activeReads, activeCloses: &activeCloses,
			},
		}, nil
	})}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 2}, options)
	client.requestTimeout = 10 * time.Millisecond

	started := time.Now()
	_, err := client.Stream(context.Background(), basicRequest())
	var exhausted *RetryExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("blocked error body result = %T %v", err, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("error-body deadline coordinator took %s", elapsed)
	}
	waitForAtomicValue(t, &reads, 3)
	waitForAtomicValue(t, &closes, 3)
	if calls.Load() != 3 || activeReads.Load() != 3 || activeCloses.Load() != 3 {
		t.Fatalf(
			"attempt resources calls=%d reads=%d active_reads=%d closes=%d active_closes=%d",
			calls.Load(), reads.Load(), activeReads.Load(), closes.Load(), activeCloses.Load(),
		)
	}

	close(releaseRead)
	close(releaseClose)
	waitForAtomicValue(t, &activeReads, 0)
	waitForAtomicValue(t, &activeCloses, 0)
}

func TestAzureCallerCancellationWinsOverHostileErrorBody(t *testing.T) {
	releaseRead := make(chan struct{})
	releaseClose := make(chan struct{})
	var calls atomic.Int32
	var reads atomic.Int32
	var closes atomic.Int32
	var activeReads atomic.Int32
	var activeCloses atomic.Int32
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: &blockingResponseBody{
				releaseRead: releaseRead, releaseClose: releaseClose,
				reads: &reads, closes: &closes,
				activeReads: &activeReads, activeCloses: &activeCloses,
			},
		}, nil
	})}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 3}, options)
	client.requestTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, basicRequest())
		results <- err
	}()

	waitForAtomicValue(t, &reads, 1)
	cancel()
	var err error
	select {
	case err = <-results:
	case <-time.After(time.Second):
		close(releaseRead)
		close(releaseClose)
		t.Fatal("caller cancellation waited for hostile response body")
	}
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("caller cancellation result = %T %v", err, err)
	}
	waitForAtomicValue(t, &closes, 1)
	if calls.Load() != 1 || activeReads.Load() != 1 || activeCloses.Load() != 1 {
		t.Fatalf(
			"attempt resources calls=%d reads=%d active_reads=%d closes=%d active_closes=%d",
			calls.Load(), reads.Load(), activeReads.Load(), closes.Load(), activeCloses.Load(),
		)
	}

	close(releaseRead)
	close(releaseClose)
	waitForAtomicValue(t, &activeReads, 0)
	waitForAtomicValue(t, &activeCloses, 0)
}

func TestAzureHostileRoundTripperRespectsAttemptDeadline(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		active.Add(1)
		defer active.Add(-1)
		<-release
		return nil, errors.New("released hostile transport")
	})}
	client := newTestClient(t, "http://localhost", configOverrides{maxRetries: 2}, options)
	client.requestTimeout = 10 * time.Millisecond

	started := time.Now()
	_, err := client.Stream(context.Background(), basicRequest())
	var exhausted *RetryExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("blocked transport result = %T %v", err, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("transport deadline coordinator took %s", elapsed)
	}
	waitForAtomicValue(t, &calls, 3)
	if active.Load() != 3 {
		t.Fatalf("active transports = %d, want 3", active.Load())
	}

	close(release)
	waitForAtomicValue(t, &active, 0)
}

func waitForAtomicValue(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for value.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("atomic value = %d, want %d", value.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}
