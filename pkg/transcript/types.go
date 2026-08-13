// Package transcript owns append-only durable session history and defensive
// recovery. It never executes tools and never treats filesystem inspection as
// proof that an interrupted side effect succeeded.
package transcript

import (
	"errors"
	"os"
	"time"

	"github.com/greenpau/agentx/pkg/protocol"
)

const (
	// DefaultMaxRecordBytes bounds one untrusted JSONL record while allowing
	// large model/tool outputs. Oversized records are isolated and skipped.
	DefaultMaxRecordBytes = 32 * 1024 * 1024
	// DefaultMaxFileBytes bounds resume/fork memory and disk scanning. Writers
	// enforce the same ceiling so they never create an unreadable session.
	DefaultMaxFileBytes int64 = 256 * 1024 * 1024
	// DefaultMaxEvents bounds graph/index allocations during recovery.
	DefaultMaxEvents = 100_000
	// MaxAppendChunkBytes prevents one batch write from growing without bound.
	MaxAppendChunkBytes = 100 * 1024 * 1024
	// DefaultMaxDiagnostics bounds corrupt-file diagnostics in memory.
	DefaultMaxDiagnostics = 1024
	// DefaultCloseTimeout bounds shutdown waiting behind an in-flight append.
	// A timed-out Close leaves the store open so a later cleanup attempt can
	// retry after the current owner releases the gate.
	DefaultCloseTimeout = 3 * time.Second
)

var (
	ErrClosed              = errors.New("transcript is closed")
	ErrPoisoned            = errors.New("transcript writer is poisoned after an identity or durability failure")
	ErrUnsafePath          = errors.New("transcript path is not a regular file")
	ErrSessionMismatch     = errors.New("transcript session does not match writer session")
	ErrDuplicateToolUse    = errors.New("tool-use identifier was accepted more than once")
	ErrDuplicateToolResult = errors.New("tool-use identifier has more than one terminal result")
	ErrUnknownToolUse      = errors.New("tool result has no accepted tool call")
	ErrToolNameMismatch    = errors.New("tool result name does not match accepted call")
	ErrToolParentMismatch  = errors.New("tool result parent does not match accepted call")
	ErrSequenceRegression  = errors.New("transcript sequence is not monotonic")
	ErrSequenceExhausted   = errors.New("transcript sequence space is exhausted")
	ErrResourceLimit       = errors.New("transcript resource limit exceeded")
)

// Diagnostic describes bounded recovery evidence without copying corrupt input
// or arbitrary tool data into logs/metrics.
type Diagnostic struct {
	Code      string             `json:"code"`
	Message   string             `json:"message"`
	Line      int                `json:"line,omitempty"`
	EventID   protocol.EventID   `json:"event_id,omitempty"`
	ToolUseID protocol.ToolUseID `json:"tool_use_id,omitempty"`
}

// Snapshot is a defensively loaded physical event projection in canonical
// sequence order. ActiveConversation selects the semantic resume branch;
// ReconcileUnresolved then returns a derived model-safe copy.
type Snapshot struct {
	SessionID          protocol.SessionID `json:"session_id,omitempty"`
	Events             []protocol.Event   `json:"events"`
	Diagnostics        []Diagnostic       `json:"diagnostics,omitempty"`
	MaxSequence        uint64             `json:"max_sequence"`
	DroppedDiagnostics int                `json:"dropped_diagnostics,omitempty"`
	// ResumeCursor is the durable event that owns the next append parent after
	// semantic branch projection. Session-scoped metadata/accounting from other
	// branches may remain in Events without stealing this cursor.
	ResumeCursor protocol.EventID `json:"-"`
}

// ReadOptions controls defensive JSONL loading.
type ReadOptions struct {
	ExpectedSessionID protocol.SessionID
	MaxRecordBytes    int
	MaxDiagnostics    int
	MaxFileBytes      int64
	MaxEvents         int
	// ValidateRecord inspects every syntactically valid physical JSONL record
	// before typed Event decoding or recovery, and the final complete Snapshot
	// projection.
	ValidateRecord func([]byte) error
}

// Config constructs a single-process append owner. SessionMetadata is the
// destination stamp and replaces copied source metadata on every append.
type Config struct {
	Path            string
	SessionID       protocol.SessionID
	SessionMetadata protocol.SessionMetadata
	MaxRecordBytes  int
	MaxDiagnostics  int
	MaxFileBytes    int64
	MaxEvents       int
	SyncOnAppend    bool
	CloseTimeout    time.Duration
	Now             func() time.Time
	// ValidateRecord inspects one complete JSON event after all fields and
	// structural separators have been encoded. Returning an error prevents any
	// bytes from being appended.
	ValidateRecord func([]byte) error
	// syncDirectory is an internal fault-injection seam. Runtime callers use
	// the platform implementation selected by Open.
	syncDirectory func(*os.File) error
}

// ToolPair is an accepted call and its exactly one terminal result.
type ToolPair struct {
	Call   protocol.Event
	Result protocol.Event
}

type acceptedTool struct {
	eventID protocol.EventID
	name    string
}
