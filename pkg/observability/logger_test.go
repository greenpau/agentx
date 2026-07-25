package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLoggerLevelFilteringAndJSONEncoding(t *testing.T) {
	tests := []struct {
		name      string
		debug     bool
		wantLines int
		wantFirst string
	}{
		{name: "info default", wantLines: 1, wantFirst: "info"},
		{name: "debug enabled", debug: true, wantLines: 2, wantFirst: "debug"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := NewLogger(LoggerConfig{Writer: &output, Debug: test.debug})
			Log(logger, zapcore.DebugLevel, "debug record", zap.Int("iteration", 1))
			Log(logger, zapcore.InfoLevel, "info record",
				zap.String("status", "completed"),
				zap.Duration("duration", 125*time.Millisecond),
			)

			lines := nonemptyLogLines(output.String())
			if len(lines) != test.wantLines {
				t.Fatalf("log lines = %d (%q), want %d", len(lines), output.String(), test.wantLines)
			}
			var first map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
				t.Fatalf("decode first log record: %v\n%s", err, lines[0])
			}
			if first["level"] != test.wantFirst {
				t.Fatalf("first level = %#v, want %q", first["level"], test.wantFirst)
			}
			if timestamp, ok := first["ts"].(string); !ok || timestamp == "" {
				t.Fatalf("timestamp = %#v, want an ISO-8601 string", first["ts"])
			}
			if _, exists := first["stacktrace"]; exists {
				t.Fatalf("logger added an automatic stack trace: %#v", first)
			}
			if test.wantFirst == "info" && first["duration"] != "125ms" {
				t.Fatalf("duration = %#v, want a readable string", first["duration"])
			}
			caller, ok := first["caller"].(string)
			if !ok || caller == "" {
				t.Fatalf("caller = %#v, want a short call site", first["caller"])
			}
			if strings.HasPrefix(caller, "/") || strings.Contains(caller, `:\Users\`) ||
				strings.Contains(caller, "/Users/") || strings.Contains(caller, "/home/") {
				t.Fatalf("caller exposed an absolute or home path: %q", caller)
			}
		})
	}
}

func TestLoggerCredentialRedactionAndFraming(t *testing.T) {
	t.Run("ordinary field", func(t *testing.T) {
		const secret = "synthetic-logger-secret"
		var output bytes.Buffer
		logger := NewLogger(LoggerConfig{Writer: &output, Credentials: redact.New(secret)})
		Log(logger, zapcore.InfoLevel, "diagnostic record", zap.String("error", "provider returned "+secret))

		if strings.Contains(output.String(), secret) {
			t.Fatalf("credential reached log output: %q", output.String())
		}
		lines := nonemptyLogLines(output.String())
		if len(lines) != 1 {
			t.Fatalf("log lines = %d (%q), want 1", len(lines), output.String())
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
			t.Fatal(err)
		}
		if record["error"] == "provider returned "+secret {
			t.Fatalf("credential-bearing field was not redacted: %#v", record)
		}
	})

	t.Run("escaped spelling", func(t *testing.T) {
		var output bytes.Buffer
		sink := &diagnosticWriteSyncer{writer: &output, credentials: redact.New("secret")}
		input := []byte("{\"value\":\"\\u0073ecret\"}\n")
		if written, err := sink.Write(input); err != nil || written != len(input) {
			t.Fatalf("Write = %d, %v; want %d, nil", written, err, len(input))
		}
		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
			t.Fatalf("decode sanitized record: %v; output=%q", err, output.String())
		}
		if record["value"] == "secret" || strings.Contains(output.String(), "secret") {
			t.Fatalf("escaped credential reached output: %#v / %q", record, output.String())
		}
	})

	t.Run("cross-field structure", func(t *testing.T) {
		var output bytes.Buffer
		sink := &diagnosticWriteSyncer{writer: &output, credentials: redact.New(`left":"right`)}
		input := []byte(`{"left":"right"}` + "\n")
		if written, err := sink.Write(input); err != nil || written != len(input) {
			t.Fatalf("Write = %d, %v; want %d, nil", written, err, len(input))
		}
		if output.Len() != 0 {
			t.Fatalf("structurally reconstructed credential was written: %q", output.String())
		}
	})

	t.Run("physical delimiter", func(t *testing.T) {
		var output bytes.Buffer
		sink := &diagnosticWriteSyncer{writer: &output, credentials: redact.New("safe\"}\n")}
		input := []byte("{\"value\":\"safe\"}\n")
		if written, err := sink.Write(input); err != nil || written != len(input) {
			t.Fatalf("Write = %d, %v; want %d, nil", written, err, len(input))
		}
		if output.Len() != 0 {
			t.Fatalf("credential reconstructed by line framing was written: %q", output.String())
		}
	})
}

func TestLoggerNilAndHostileWritersAreContained(t *testing.T) {
	t.Run("nil writer", func(t *testing.T) {
		logger := NewLogger(LoggerConfig{})
		Log(logger, zapcore.InfoLevel, "discarded")
		if err := logger.Sync(); err != nil {
			t.Fatalf("Sync = %v, want nil", err)
		}
		Log(nil, zapcore.InfoLevel, "nil logger")
	})

	tests := []struct {
		name   string
		writer io.Writer
	}{
		{name: "error", writer: hostileLogWriter{mode: hostileWriterError}},
		{name: "short", writer: hostileLogWriter{mode: hostileWriterShort}},
		{name: "panic", writer: hostileLogWriter{mode: hostileWriterPanic}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := NewLogger(LoggerConfig{Writer: test.writer})
			Log(logger, zapcore.ErrorLevel, "contained writer failure", zap.String("class", test.name))
			if err := logger.Sync(); err != nil {
				t.Fatalf("Sync = %v, want nil", err)
			}
		})
	}

	t.Run("panicking logger core", func(t *testing.T) {
		logger := zap.New(panickingLogCore{Core: zapcore.NewNopCore()})
		Log(logger, zapcore.InfoLevel, "contained core panic")
	})
}

func TestLoggerDropsMalformedAndOversizedRecords(t *testing.T) {
	var output bytes.Buffer
	sink := &diagnosticWriteSyncer{writer: &output}
	for _, input := range [][]byte{
		[]byte("not-json\n"),
		bytes.Repeat([]byte("x"), maximumDiagnosticLogRecordBytes+1),
	} {
		if written, err := sink.Write(input); err != nil || written != len(input) {
			t.Fatalf("Write = %d, %v; want %d, nil", written, err, len(input))
		}
	}
	if output.Len() != 0 {
		t.Fatalf("invalid diagnostic record reached output: %q", output.String())
	}
}

func nonemptyLogLines(value string) []string {
	var result []string
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return result
}

type hostileWriterMode uint8

const (
	hostileWriterError hostileWriterMode = iota
	hostileWriterShort
	hostileWriterPanic
)

type hostileLogWriter struct{ mode hostileWriterMode }

func (w hostileLogWriter) Write(data []byte) (int, error) {
	switch w.mode {
	case hostileWriterError:
		return 0, errors.New("synthetic writer failure")
	case hostileWriterShort:
		if len(data) == 0 {
			return 0, nil
		}
		return len(data) - 1, nil
	case hostileWriterPanic:
		panic("synthetic writer panic")
	default:
		return len(data), nil
	}
}

type panickingLogCore struct{ zapcore.Core }

func (panickingLogCore) Enabled(zapcore.Level) bool {
	panic("synthetic core panic")
}
