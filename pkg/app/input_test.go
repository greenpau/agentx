package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
)

type hostileInputError struct {
	values      []string
	errorCalls  *atomic.Int32
	isCalls     *atomic.Int32
	unwrapCalls *atomic.Int32
}

func (err hostileInputError) Error() string {
	err.errorCalls.Add(1)
	panic("input error callback must not run")
}

func (err hostileInputError) Is(error) bool {
	err.isCalls.Add(1)
	panic("input Is callback must not run")
}

func (err hostileInputError) Unwrap() error {
	err.unwrapCalls.Add(1)
	panic("input Unwrap callback must not run")
}

type failingInputReader struct{ err error }

func (reader failingInputReader) Read([]byte) (int, error) {
	return 0, reader.err
}

type panickingInputReader struct{}

func (panickingInputReader) Read([]byte) (int, error) {
	panic("input Read callback panic")
}

type invalidCountInputReader struct{}

func (invalidCountInputReader) Read(buffer []byte) (int, error) {
	return len(buffer) + 1, nil
}

type blockingInputReadCloser struct {
	readStarted  chan struct{}
	closeStarted chan struct{}
	readRelease  chan struct{}
	closeRelease chan struct{}
	readOnce     sync.Once
	closeOnce    sync.Once
}

func (reader *blockingInputReadCloser) Read([]byte) (int, error) {
	reader.readOnce.Do(func() { close(reader.readStarted) })
	<-reader.readRelease
	return 0, io.EOF
}

func (reader *blockingInputReadCloser) Close() error {
	reader.closeOnce.Do(func() { close(reader.closeStarted) })
	<-reader.closeRelease
	return nil
}

type panickingCloseInputReader struct {
	readStarted  chan struct{}
	closeStarted chan struct{}
	readRelease  chan struct{}
	readOnce     sync.Once
	closeOnce    sync.Once
}

func (reader *panickingCloseInputReader) Read([]byte) (int, error) {
	reader.readOnce.Do(func() { close(reader.readStarted) })
	<-reader.readRelease
	return 0, io.EOF
}

func (reader *panickingCloseInputReader) Close() error {
	reader.closeOnce.Do(func() { close(reader.closeStarted) })
	panic("input Close callback panic")
}

func TestHeadlessPromptPreservesUTF8ContentAndJoinsWithOneNewline(t *testing.T) {
	got, err := headlessPrompt("question\n", strings.NewReader("\r\n  indented\r\ntrail  \r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "question\n  indented\ntrail  \n"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if _, err := headlessPrompt("", strings.NewReader(string([]byte{0xff}))); err == nil {
		t.Fatal("invalid UTF-8 stdin was accepted")
	}
}

func TestHeadlessPromptContainsHostileInputReader(t *testing.T) {
	var errorCalls, isCalls, unwrapCalls atomic.Int32
	hostile := hostileInputError{
		values:      []string{"uncomparable"},
		errorCalls:  &errorCalls,
		isCalls:     &isCalls,
		unwrapCalls: &unwrapCalls,
	}
	for name, reader := range map[string]io.Reader{
		"hostile error": failingInputReader{err: hostile},
		"panic":         panickingInputReader{},
		"invalid count": invalidCountInputReader{},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := headlessPromptContextWithWarnings(
				t.Context(), "", reader, io.Discard, time.Second,
			)
			if !errors.Is(err, errInputReaderFailed) {
				t.Fatalf("headless input error = %v, want fixed reader failure", err)
			}
		})
	}
	if got := errorCalls.Load(); got != 0 {
		t.Fatalf("hostile input Error calls = %d", got)
	}
	if got := isCalls.Load(); got != 0 {
		t.Fatalf("hostile input Is calls = %d", got)
	}
	if got := unwrapCalls.Load(); got != 0 {
		t.Fatalf("hostile input Unwrap calls = %d", got)
	}
}

func TestHeadlessPromptCancellationDoesNotWaitForBrokenClose(t *testing.T) {
	reader := &blockingInputReadCloser{
		readStarted:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		readRelease:  make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := headlessPromptContextWithWarnings(ctx, "", reader, io.Discard, time.Second)
		done <- err
	}()
	select {
	case <-reader.readStarted:
	case <-time.After(time.Second):
		t.Fatal("input Read callback did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("headless cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("headless cancellation waited for broken Close")
	}
	select {
	case <-reader.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("input Close callback did not start")
	}
	close(reader.readRelease)
	close(reader.closeRelease)
}

func TestHeadlessPromptCancellationContainsClosePanic(t *testing.T) {
	reader := &panickingCloseInputReader{
		readStarted:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		readRelease:  make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := headlessPromptContextWithWarnings(ctx, "", reader, io.Discard, time.Second)
		done <- err
	}()
	select {
	case <-reader.readStarted:
	case <-time.After(time.Second):
		t.Fatal("input Read callback did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("headless cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("headless cancellation did not contain Close panic")
	}
	select {
	case <-reader.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("panicking input Close callback did not start")
	}
	close(reader.readRelease)
}

func TestHeadlessPromptFirstByteTimeoutWarnsAndContinues(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var warnings strings.Builder
	started := time.Now()
	got, err := headlessPromptContextWithWarnings(context.Background(), "positional", reader, &warnings, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != "positional" {
		t.Fatalf("prompt after timeout = %q", got)
	}
	if !strings.Contains(warnings.String(), "no stdin data received") {
		t.Fatalf("warning = %q", warnings.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("first-byte timeout took %s", elapsed)
	}
}

func TestHeadlessPromptTimeoutContainsWarningWriterFailure(t *testing.T) {
	for name, test := range map[string]struct {
		warnings io.Writer
		want     error
	}{
		"panic": {
			warnings: panicWriter{},
			want:     errTerminalWriterPanicked,
		},
		"hostile error": {
			warnings: terminalErrorWriter{err: hostileTerminalWriterError{}},
			want:     errTerminalWriterFailed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			_, err = headlessPromptContextWithWarnings(
				t.Context(), "positional", reader, test.warnings, 10*time.Millisecond,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("timeout warning writer error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestHeadlessPromptTimeoutWarningGuardsCompleteTerminalRecord(t *testing.T) {
	const timeout = 10 * time.Millisecond
	for name, secret := range map[string]string{
		"dynamic framing": "10ms; continuing",
		"line framing":    "input\n",
		"short key":       "in",
	} {
		t.Run(name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			var warnings bytes.Buffer
			got, err := headlessPromptContextWithTerminalWarnings(
				t.Context(), "positional", reader, &warnings, redact.New(secret), timeout,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != "positional" {
				t.Fatalf("prompt after timeout = %q", got)
			}
			if rendered := warnings.String(); strings.Contains(rendered, secret) ||
				!strings.Contains(rendered, redact.Mask(secret)) {
				t.Fatalf("guarded timeout warning = %q", rendered)
			}
		})
	}
}

func TestHeadlessPipeReadIsCancellable(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := headlessPromptContext(ctx, "", reader)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("headless cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("headless stdin remained blocked after cancellation")
	}
}

func TestInteractivePipeReadIsCancellable(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	pump := newLinePump(reader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pump.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interactive cancellation = %v", err)
	}
	pump.Close()
	select {
	case <-pump.done:
	case <-time.After(time.Second):
		t.Fatal("interactive reader goroutine did not join")
	}
}

func TestInteractiveLinePumpContainsReaderFailure(t *testing.T) {
	var errorCalls, isCalls, unwrapCalls atomic.Int32
	hostile := hostileInputError{
		values:      []string{"uncomparable"},
		errorCalls:  &errorCalls,
		isCalls:     &isCalls,
		unwrapCalls: &unwrapCalls,
	}
	for name, reader := range map[string]io.Reader{
		"hostile error": failingInputReader{err: hostile},
		"panic":         panickingInputReader{},
		"invalid count": invalidCountInputReader{},
	} {
		t.Run(name, func(t *testing.T) {
			pump := newLinePump(reader)
			defer pump.Close()
			if _, err := pump.Next(t.Context()); err != errInputReaderFailed {
				t.Fatalf("interactive input error = %v, want fixed reader failure", err)
			}
		})
	}
	if errorCalls.Load() != 0 || isCalls.Load() != 0 || unwrapCalls.Load() != 0 {
		t.Fatalf(
			"hostile input callbacks = Error:%d Is:%d Unwrap:%d",
			errorCalls.Load(), isCalls.Load(), unwrapCalls.Load(),
		)
	}
}

func TestInteractiveLinePumpCloseDoesNotWaitForBrokenCallback(t *testing.T) {
	reader := &blockingInputReadCloser{
		readStarted:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		readRelease:  make(chan struct{}),
		closeRelease: make(chan struct{}),
	}
	pump := newLinePump(reader)
	select {
	case <-reader.readStarted:
	case <-time.After(time.Second):
		t.Fatal("interactive Read callback did not start")
	}
	started := time.Now()
	pump.Close()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("interactive Close waited for broken callback: %s", elapsed)
	}
	select {
	case <-reader.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("interactive Close callback did not start")
	}
	close(reader.readRelease)
	close(reader.closeRelease)
}
