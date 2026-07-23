package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"time"
)

var errInputReaderFailed = errors.New("input reader failed")

type lineResult struct {
	line string
	err  error
}

type contextReadResult struct {
	data []byte
	err  error
}

type firstReadObserver struct {
	reader   io.Reader
	observed chan struct{}
	once     sync.Once
}

func (r *firstReadObserver) Read(buffer []byte) (int, error) {
	count, err := readInputCallback(r.reader, buffer)
	if count > 0 || err != nil {
		r.once.Do(func() { close(r.observed) })
	}
	return count, err
}

type containedInputReader struct{ reader io.Reader }

func (r containedInputReader) Read(buffer []byte) (int, error) {
	return readInputCallback(r.reader, buffer)
}

func readInputCallback(reader io.Reader, buffer []byte) (count int, resultErr error) {
	defer func() {
		if recover() != nil {
			count = 0
			resultErr = errInputReaderFailed
		}
	}()
	count, resultErr = reader.Read(buffer)
	if count < 0 || count > len(buffer) {
		return 0, errInputReaderFailed
	}
	if resultErr == nil {
		return count, nil
	}
	typ := reflect.TypeOf(resultErr)
	if typ != nil && typ.Comparable() && resultErr == io.EOF {
		return count, io.EOF
	}
	return count, errInputReaderFailed
}

func startContainedInputClose(closer io.Closer) {
	if closer == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		_ = closer.Close()
	}()
}

// linePump owns all reads from one interactive stream. Cancellation never
// leaves a goroutine with references to session state. Close asks a closable
// input to unblock and joins the sole reader only within a fixed bound.
type linePump struct {
	lines  chan lineResult
	stop   chan struct{}
	done   chan struct{}
	closer io.Closer
	once   sync.Once
}

func newLinePump(reader io.Reader) *linePump {
	pump := &linePump{lines: make(chan lineResult, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if closer, ok := reader.(io.Closer); ok {
		pump.closer = closer
	}
	go func() {
		defer func() {
			if recover() != nil {
				select {
				case pump.lines <- lineResult{err: errInputReaderFailed}:
				case <-pump.stop:
				}
			}
			close(pump.lines)
			close(pump.done)
		}()
		buffered := bufio.NewReader(containedInputReader{reader: reader})
		for {
			line, err := buffered.ReadString('\n')
			result := lineResult{line: line, err: err}
			select {
			case pump.lines <- result:
			case <-pump.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return pump
}

func (p *linePump) Next(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result, ok := <-p.lines:
		if !ok {
			return "", io.EOF
		}
		return result.line, result.err
	}
}

func (p *linePump) Close() {
	p.once.Do(func() {
		close(p.stop)
		startContainedInputClose(p.closer)
	})
	select {
	case <-p.done:
	case <-time.After(100 * time.Millisecond):
		// A non-closable reader may not be interruptible. Its isolated pump owns
		// no runtime/session object and cannot publish after the caller returns.
	}
}

// readAllContextFirstByte bounds the otherwise ambiguous "open pipe with no
// writer activity" state. Once any byte arrives, the complete bounded prompt
// is collected through EOF. Actual process stdin is closable, so timeout and
// cancellation also give the owned reader goroutine a termination path.
func readAllContextFirstByte(ctx context.Context, reader io.Reader, maximum int64, firstByteTimeout time.Duration) ([]byte, bool, error) {
	observed := make(chan struct{})
	wrapped := &firstReadObserver{reader: reader, observed: observed}
	done := make(chan contextReadResult, 1)
	go func() {
		result := contextReadResult{err: errInputReaderFailed}
		defer func() {
			if recover() != nil {
				result = contextReadResult{err: errInputReaderFailed}
			}
			done <- result
		}()
		result.data, result.err = io.ReadAll(io.LimitReader(wrapped, maximum+1))
	}()
	if firstByteTimeout <= 0 {
		firstByteTimeout = stdinFirstByteTimeout
	}
	timer := time.NewTimer(firstByteTimeout)
	defer timer.Stop()
	select {
	case value := <-done:
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return value.data, false, value.err
	case <-observed:
		select {
		case value := <-done:
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			return value.data, false, value.err
		case <-ctx.Done():
			closeReaderAndWait(reader, done)
			return nil, false, ctx.Err()
		}
	case <-timer.C:
		closeReaderAndWait(reader, done)
		return nil, true, nil
	case <-ctx.Done():
		closeReaderAndWait(reader, done)
		return nil, false, ctx.Err()
	}
}

func closeReaderAndWait(reader io.Reader, done <-chan contextReadResult) {
	if closer, ok := reader.(io.Closer); ok {
		startContainedInputClose(closer)
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func scanLinesContext(ctx context.Context, reader io.Reader, maximum int) (<-chan lineResult, func()) {
	output := make(chan lineResult, 1)
	stop := make(chan struct{})
	var once sync.Once
	scanner := bufio.NewScanner(containedInputReader{reader: reader})
	scanner.Buffer(make([]byte, 4096), maximum)
	go func() {
		defer func() {
			if recover() != nil {
				select {
				case output <- lineResult{err: errInputReaderFailed}:
				case <-ctx.Done():
				case <-stop:
				}
			}
			close(output)
		}()
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case output <- lineResult{line: line}:
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case output <- lineResult{err: err}:
		case <-ctx.Done():
		case <-stop:
		}
	}()
	closeFn := func() {
		once.Do(func() {
			close(stop)
			if closer, ok := reader.(io.Closer); ok {
				startContainedInputClose(closer)
			}
		})
	}
	return output, closeFn
}
