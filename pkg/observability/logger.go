package observability

import (
	"io"
	"sync"

	"github.com/greenpau/agentx/pkg/redact"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	maximumDiagnosticLogRecordBytes = 64 << 10
	maximumDiagnosticMessageRunes   = 2_000
)

// LoggerConfig supplies the process-local diagnostic destination and the
// complete immutable credential set that is eligible to reach the session.
// A nil Writer discards records while retaining ordinary level behavior.
type LoggerConfig struct {
	Writer      io.Writer
	Debug       bool
	Credentials *redact.Set
}

// NewLogger constructs a structured, stderr-style diagnostic logger. The
// caller owns destination selection; headless callers must supply stderr so
// protocol stdout remains clean.
func NewLogger(config LoggerConfig) *zap.Logger {
	level := zapcore.InfoLevel
	if config.Debug {
		level = zapcore.DebugLevel
	}
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeDuration = zapcore.StringDurationEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		&diagnosticWriteSyncer{
			writer:      config.Writer,
			credentials: config.Credentials,
		},
		level,
	)
	// Core writes are deliberately best effort, and zap's own error channel
	// must not create a second unredacted diagnostic path.
	return zap.New(
		core,
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		zap.ErrorOutput(zapcore.AddSync(io.Discard)),
	)
}

// Log writes bounded operational metadata at level. Callers must not supply
// prompts, model or tool payloads, file contents, headers, or configuration
// objects as fields. Panic containment keeps diagnostics outside semantic
// success, persistence, permission, and exit-status decisions.
func Log(logger *zap.Logger, level zapcore.Level, message string, fields ...zap.Field) {
	defer func() {
		_ = recover()
	}()
	if logger == nil {
		return
	}
	// A diagnostic helper must never turn logging into process control, even if
	// a caller accidentally supplies one of zap's terminal levels.
	if level >= zapcore.DPanicLevel {
		level = zapcore.ErrorLevel
	}
	message = truncateRunes(RedactText(message), maximumDiagnosticMessageRunes)
	if entry := logger.Check(level, message); entry != nil {
		entry.Write(fields...)
	}
}

// diagnosticWriteSyncer treats every Write as one encoded zap record. Zap's
// JSON core performs one write per entry; malformed, oversized, unsafe, or
// unwritable records are dropped without becoming errors in the observed
// operation.
type diagnosticWriteSyncer struct {
	mu          sync.Mutex
	writer      io.Writer
	credentials *redact.Set
}

func (s *diagnosticWriteSyncer) Write(data []byte) (written int, err error) {
	consumed := len(data)
	defer func() {
		if recover() != nil {
			written, err = consumed, nil
		}
	}()
	if s == nil {
		return consumed, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer == nil || len(data) == 0 || len(data) > maximumDiagnosticLogRecordBytes {
		return consumed, nil
	}

	// Reserve one byte for the single physical JSONL delimiter. JSONBounded
	// both validates the input and applies semantic redaction to decoded string
	// values and keys before producing canonical JSON.
	safe, sanitizeErr := s.credentials.JSONBounded(data, maximumDiagnosticLogRecordBytes-1)
	if sanitizeErr != nil || len(safe) == 0 {
		return consumed, nil
	}
	framed := make([]byte, len(safe)+1)
	copy(framed, safe)
	framed[len(safe)] = '\n'

	// JSONBounded verifies its canonical body. Inspect the physical line too,
	// so appending the delimiter cannot reconstruct a credential that ends at
	// the record boundary.
	if s.credentials != nil && !s.credentials.Empty() {
		reflected, inspectErr := s.credentials.JSONContains(framed)
		if inspectErr != nil || reflected {
			return consumed, nil
		}
	}
	count, writeErr := s.writer.Write(framed)
	if writeErr != nil || count != len(framed) {
		return consumed, nil
	}
	return consumed, nil
}

// Sync is intentionally a no-op. The sink writes complete records directly
// and must never turn an unsupported stderr fsync into a semantic failure.
func (*diagnosticWriteSyncer) Sync() error { return nil }
