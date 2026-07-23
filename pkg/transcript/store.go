package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/greenpau/agentx/pkg/protocol"
)

// Store is the synchronized, single-process append owner for one session file.
// The channel gate makes waiting for ownership cancellation-aware.
type Store struct {
	config Config
	gate   chan struct{}
	clock  atomic.Bool

	file     *os.File
	closed   bool
	poisoned error
	// expectedFile is identity evidence captured by the same bounded descriptor
	// read that built the initial indexes. Nil means the path was absent then.
	expectedFile os.FileInfo

	seenEvents  map[protocol.EventID]struct{}
	accepted    map[protocol.ToolUseID]acceptedTool
	resolved    map[protocol.ToolUseID]protocol.EventID
	maxSequence uint64
}

// Open validates existing history without materializing a new empty file.
func Open(ctx context.Context, config Config) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Path == "" {
		return nil, errors.New("transcript path is required")
	}
	if config.SessionID == "" {
		return nil, errors.New("transcript session id is required")
	}
	if config.MaxRecordBytes <= 0 {
		config.MaxRecordBytes = DefaultMaxRecordBytes
	}
	if config.MaxDiagnostics <= 0 {
		config.MaxDiagnostics = DefaultMaxDiagnostics
	}
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = DefaultMaxFileBytes
	}
	if config.MaxEvents <= 0 {
		config.MaxEvents = DefaultMaxEvents
	}
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = DefaultCloseTimeout
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.syncDirectory == nil {
		config.syncDirectory = syncTranscriptDirectory
	}
	config.Path = filepath.Clean(config.Path)

	snapshot, expectedFile, err := readFileSnapshot(ctx, config.Path, ReadOptions{
		ExpectedSessionID: config.SessionID,
		MaxRecordBytes:    config.MaxRecordBytes,
		MaxDiagnostics:    config.MaxDiagnostics,
		MaxFileBytes:      config.MaxFileBytes,
		MaxEvents:         config.MaxEvents,
		ValidateRecord:    config.ValidateRecord,
	}, nil)
	if err != nil {
		return nil, err
	}
	store := &Store{
		config:       config,
		gate:         make(chan struct{}, 1),
		seenEvents:   make(map[protocol.EventID]struct{}, len(snapshot.Events)),
		accepted:     make(map[protocol.ToolUseID]acceptedTool),
		resolved:     make(map[protocol.ToolUseID]protocol.EventID),
		maxSequence:  snapshot.MaxSequence,
		expectedFile: expectedFile,
	}
	for _, event := range snapshot.Events {
		store.index(event)
	}
	return store, nil
}

// Path returns the immutable destination path.
func (s *Store) Path() string { return s.config.Path }

// SessionID returns the immutable owning session identifier.
func (s *Store) SessionID() protocol.SessionID { return s.config.SessionID }

// Append validates and queues one durable event. Duplicate event IDs are an
// idempotent no-op; a different event that reuses a tool-use/result ID fails.
func (s *Store) Append(ctx context.Context, event protocol.Event) error {
	_, _, err := s.AppendEvent(ctx, event)
	return err
}

// AppendEvent returns the normalized event and whether it was written. When it
// is written, the value includes destination session stamp, timestamp, ID,
// sequence, and the accepted-call parent inferred for a tool result.
func (s *Store) AppendEvent(ctx context.Context, event protocol.Event) (protocol.Event, bool, error) {
	event = s.stampEvent(event)
	if err := s.lock(ctx); err != nil {
		return protocol.Event{}, false, err
	}
	defer s.unlock()

	prepared, err := s.prepare([]protocol.Event{event})
	if err != nil {
		return protocol.Event{}, false, err
	}
	if len(prepared) == 0 {
		// Ephemeral and duplicate events deliberately do not materialize a file.
		return event, false, nil
	}
	written, err := s.writePrepared(prepared)
	if err != nil {
		// Preparation assigns the stable correlation identity before I/O. Return
		// that identity even when the physical commit acknowledgement is
		// uncertain so the query engine can settle the call without executing it.
		return prepared[0].event, written, err
	}
	return prepared[0].event, true, nil
}

// AppendBatch validates the complete batch against a shadow correlation index
// before writing. Batches larger than 100 MiB are split only at record edges.
func (s *Store) AppendBatch(ctx context.Context, events []protocol.Event) error {
	if len(events) == 0 {
		return nil
	}
	events = s.stampEvents(events)
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	prepared, err := s.prepare(events)
	if err != nil {
		return err
	}
	_, err = s.writePrepared(prepared)
	return err
}

type preparedEvent struct {
	event protocol.Event
	line  []byte
}

func (s *Store) prepare(events []protocol.Event) ([]preparedEvent, error) {
	if s.closed {
		return nil, ErrClosed
	}
	if s.poisoned != nil {
		return nil, fmt.Errorf("%w: %v", ErrPoisoned, s.poisoned)
	}

	seen := cloneSet(s.seenEvents)
	accepted := cloneIndex(s.accepted)
	resolved := cloneResultIndex(s.resolved)
	maxSequence := s.maxSequence
	prepared := make([]preparedEvent, 0, len(events))

	for _, original := range events {
		event := original
		if event.Version == 0 {
			event.Version = protocol.CurrentVersion
		}
		if event.ID == "" {
			id, err := protocol.NewEventID()
			if err != nil {
				return nil, fmt.Errorf("create transcript event id: %w", err)
			}
			event.ID = id
		}
		if event.SessionID != "" && event.SessionID != s.config.SessionID {
			return nil, ErrSessionMismatch
		}
		event.SessionID = s.config.SessionID
		event.Session = s.config.SessionMetadata
		if event.Timestamp.IsZero() {
			// Public entrypoints stamp outside the ownership gate. Keep a
			// callback-free fallback here for internal defensive completeness.
			event.Timestamp = time.Now().UTC()
		} else {
			event.Timestamp = event.Timestamp.UTC()
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("validate event %s: %w", event.ID, err)
		}
		if event.Persistence == protocol.PersistenceEphemeral {
			continue
		}
		if _, duplicate := seen[event.ID]; duplicate {
			continue
		}
		if len(seen) >= s.config.MaxEvents {
			return nil, fmt.Errorf("%w: transcript exceeds %d durable events", ErrResourceLimit, s.config.MaxEvents)
		}

		if event.Sequence == 0 {
			if maxSequence == math.MaxUint64 {
				return nil, ErrSequenceExhausted
			}
			maxSequence++
			event.Sequence = maxSequence
		} else {
			if event.Sequence <= maxSequence {
				return nil, fmt.Errorf("%w: event %s", ErrSequenceRegression, event.ID)
			}
			maxSequence = event.Sequence
		}

		switch event.Kind {
		case protocol.EventKindToolCall:
			toolID := event.ToolCall.ID
			if _, exists := accepted[toolID]; exists {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateToolUse, toolID)
			}
			accepted[toolID] = acceptedTool{eventID: event.ID, name: event.ToolCall.Name}
		case protocol.EventKindToolResult:
			toolID := event.ToolResult.ToolUseID
			call, exists := accepted[toolID]
			if !exists {
				return nil, fmt.Errorf("%w: %s", ErrUnknownToolUse, toolID)
			}
			if event.ToolResult.ToolName != call.name {
				return nil, fmt.Errorf("%w: %s", ErrToolNameMismatch, toolID)
			}
			if _, exists := resolved[toolID]; exists {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateToolResult, toolID)
			}
			if event.ParentID == nil {
				parent := call.eventID
				event.ParentID = &parent
			} else if *event.ParentID != call.eventID {
				return nil, fmt.Errorf("%w: %s", ErrToolParentMismatch, toolID)
			}
			resolved[toolID] = event.ID
		}
		if err := event.ValidateStored(); err != nil {
			return nil, fmt.Errorf("validate normalized event %s: %w", event.ID, err)
		}

		line, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode event %s: %w", event.ID, err)
		}
		line = append(line, '\n')
		if s.config.ValidateRecord != nil {
			if err := callTranscriptValidator(s.config.ValidateRecord, line); err != nil {
				return nil, errors.New("validate encoded transcript event")
			}
		}
		if len(line) > s.config.MaxRecordBytes {
			return nil, fmt.Errorf("event %s exceeds transcript record limit", event.ID)
		}
		prepared = append(prepared, preparedEvent{event: event, line: line})
		seen[event.ID] = struct{}{}
	}
	return prepared, nil
}

func (s *Store) stampEvent(event protocol.Event) protocol.Event {
	if event.Timestamp.IsZero() {
		event.Timestamp = s.currentTime().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	return event
}

func (s *Store) stampEvents(events []protocol.Event) []protocol.Event {
	stamped := make([]protocol.Event, len(events))
	for index, event := range events {
		stamped[index] = s.stampEvent(event)
	}
	return stamped
}

func (s *Store) currentTime() (now time.Time) {
	if s == nil || !s.clock.CompareAndSwap(false, true) {
		return time.Now()
	}
	defer s.clock.Store(false)
	defer func() {
		if recover() != nil || now.IsZero() {
			now = time.Now()
		}
	}()
	return s.config.Now()
}

func (s *Store) writePrepared(prepared []preparedEvent) (bool, error) {
	if len(prepared) == 0 {
		return false, nil
	}
	if err := s.ensureFile(); err != nil {
		return false, err
	}
	info, err := verifyOpenTranscript(s.file, s.config.MaxFileBytes)
	if err != nil {
		s.poisoned = err
		return false, fmt.Errorf("inspect transcript before append: %w", err)
	}
	if err := verifyTranscriptPath(s.config.Path, info); err != nil {
		s.poisoned = err
		return false, fmt.Errorf("verify transcript path before append: %w", err)
	}
	preparedBytes := int64(0)
	for _, item := range prepared {
		preparedBytes += int64(len(item.line))
	}
	if preparedBytes > s.config.MaxFileBytes || info.Size() > s.config.MaxFileBytes-preparedBytes {
		return false, fmt.Errorf("%w: transcript append exceeds %d bytes", ErrResourceLimit, s.config.MaxFileBytes)
	}

	written := false
	for start := 0; start < len(prepared); {
		end := start
		size := 0
		for end < len(prepared) {
			next := len(prepared[end].line)
			if end > start && size+next > MaxAppendChunkBytes {
				break
			}
			size += next
			end++
		}
		var chunk bytes.Buffer
		chunk.Grow(size)
		for i := start; i < end; i++ {
			_, _ = chunk.Write(prepared[i].line)
		}
		if err := writeAll(s.file, chunk.Bytes()); err != nil {
			s.poisoned = err
			return written, fmt.Errorf("append transcript: %w", err)
		}
		written = true
		start = end
	}
	if s.config.SyncOnAppend {
		if err := s.file.Sync(); err != nil {
			s.poisoned = err
			return written, fmt.Errorf("sync transcript append: %w", err)
		}
	}
	info, err = verifyOpenTranscript(s.file, s.config.MaxFileBytes)
	if err != nil {
		s.poisoned = err
		return written, fmt.Errorf("verify transcript after append: %w", err)
	}
	if err := verifyTranscriptPath(s.config.Path, info); err != nil {
		s.poisoned = err
		return written, fmt.Errorf("verify transcript path after append: %w", err)
	}
	for _, item := range prepared {
		s.index(item.event)
	}
	return written, nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (s *Store) ensureFile() error {
	if s.file != nil {
		return nil
	}
	parent := filepath.Dir(s.config.Path)
	parentInfo, err := os.Lstat(parent)
	switch {
	case err == nil:
		if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("transcript parent is not a directory")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create transcript directory: %w", err)
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			return fmt.Errorf("secure transcript directory: %w", err)
		}
		parentInfo, err = os.Lstat(parent)
		if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("inspect created transcript directory: %w", err)
		}
	default:
		return fmt.Errorf("inspect transcript directory: %w", err)
	}
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open transcript directory: %w", err)
	}
	defer parentDirectory.Close()
	openedParent, err := parentDirectory.Stat()
	if err != nil || !openedParent.IsDir() || !os.SameFile(parentInfo, openedParent) {
		return ErrUnsafePath
	}

	var before os.FileInfo
	before, err = os.Lstat(s.config.Path)
	switch {
	case s.expectedFile == nil && err == nil:
		// The path was absent while initial recovery built empty indexes. Refuse
		// a file inserted afterward rather than appending to unindexed history.
		return ErrUnsafePath
	case s.expectedFile == nil && errors.Is(err, os.ErrNotExist):
		before = nil
	case s.expectedFile != nil && err == nil:
		if !sameTranscriptSnapshot(s.expectedFile, before) {
			return ErrUnsafePath
		}
	case s.expectedFile != nil && errors.Is(err, os.ErrNotExist):
		return ErrUnsafePath
	case err != nil:
		return fmt.Errorf("inspect transcript destination: %w", err)
	}

	flags := os.O_APPEND | os.O_RDWR
	if before == nil {
		// O_EXCL prevents a symlink or foreign file inserted between Lstat and
		// OpenFile from redirecting the first append.
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(s.config.Path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript for append: %w", err)
	}
	fail := func(cause error) error {
		_ = file.Close()
		return cause
	}
	after, statErr := file.Stat()
	if statErr != nil || !after.Mode().IsRegular() || before != nil && !sameTranscriptSnapshot(before, after) {
		return fail(ErrUnsafePath)
	}
	pathInfo, pathErr := os.Lstat(s.config.Path)
	if pathErr != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, after) {
		return fail(ErrUnsafePath)
	}
	if err := verifyTranscriptParentPath(parent, openedParent); err != nil {
		return fail(err)
	}
	if _, verifyErr := verifyOpenTranscript(file, s.config.MaxFileBytes); verifyErr != nil {
		return fail(verifyErr)
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure transcript file: %w", err))
	}
	if err := isolateUnterminatedTail(file); err != nil {
		return fail(err)
	}
	if _, err := verifyOpenTranscript(file, s.config.MaxFileBytes); err != nil {
		return fail(err)
	}
	if before == nil {
		// Persist inode metadata before the directory entry. The first semantic
		// append has its own file-data fsync boundary in writePrepared.
		if err := file.Sync(); err != nil {
			return fail(fmt.Errorf("sync new transcript file: %w", err))
		}
		if err := verifyTranscriptParentPath(parent, openedParent); err != nil {
			return fail(err)
		}
		if err := s.config.syncDirectory(parentDirectory); err != nil {
			return fail(fmt.Errorf("sync transcript directory: %w", err))
		}
		if err := verifyTranscriptParentPath(parent, openedParent); err != nil {
			return fail(err)
		}
	}
	s.file = file
	return nil
}

func verifyOpenTranscript(file *os.File, maximum int64) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrUnsafePath
	}
	links, err := openedTranscriptLinkCount(file, info)
	if err != nil || links != 1 {
		return nil, ErrUnsafePath
	}
	return info, nil
}

func verifyTranscriptPath(path string, opened os.FileInfo) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrUnsafePath
	}
	if err != nil {
		return fmt.Errorf("inspect transcript path: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, opened) {
		return ErrUnsafePath
	}
	return nil
}

func verifyTranscriptParentPath(path string, opened os.FileInfo) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrUnsafePath
	}
	if err != nil {
		return fmt.Errorf("inspect transcript parent path: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, opened) {
		return ErrUnsafePath
	}
	return nil
}

// isolateUnterminatedTail makes the next append begin at a physical JSONL
// boundary. A valid final JSON object without LF is repaired; a malformed
// crash tail is retained as an isolated diagnostic record. The separator is
// synced before new semantic bytes can be appended, so another crash cannot
// turn the repair and new record into one ambiguous fragment.
func isolateUnterminatedTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect transcript tail: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	var final [1]byte
	if _, err := file.ReadAt(final[:], info.Size()-1); err != nil {
		return fmt.Errorf("read transcript tail: %w", err)
	}
	if final[0] == '\n' {
		return nil
	}
	if err := writeAll(file, []byte{'\n'}); err != nil {
		return fmt.Errorf("isolate unterminated transcript tail: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync transcript tail repair: %w", err)
	}
	return nil
}

// Flush establishes the explicit durability barrier for every accepted append.
func (s *Store) Flush(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	if s.closed {
		return ErrClosed
	}
	if s.poisoned != nil {
		return fmt.Errorf("%w: %v", ErrPoisoned, s.poisoned)
	}
	if s.file == nil {
		return nil
	}
	if err := syncVerifiedTranscript(s.file, s.config.Path, s.config.MaxFileBytes); err != nil {
		s.poisoned = err
		return err
	}
	return nil
}

func syncVerifiedTranscript(file *os.File, path string, maximum int64) error {
	info, err := verifyOpenTranscript(file, maximum)
	if err != nil {
		return fmt.Errorf("inspect transcript before flush: %w", err)
	}
	if err := verifyTranscriptPath(path, info); err != nil {
		return fmt.Errorf("verify transcript path before flush: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush transcript: %w", err)
	}
	info, err = verifyOpenTranscript(file, maximum)
	if err != nil {
		return fmt.Errorf("inspect transcript after flush: %w", err)
	}
	if err := verifyTranscriptPath(path, info); err != nil {
		return fmt.Errorf("verify transcript path after flush: %w", err)
	}
	return nil
}

// Load synchronizes prior appends and returns the physical durable projection.
func (s *Store) Load(ctx context.Context) (Snapshot, error) {
	if err := s.lock(ctx); err != nil {
		return Snapshot{}, err
	}
	defer s.unlock()
	if err := s.flushLocked(); err != nil {
		return Snapshot{}, err
	}
	snapshot, _, err := readFileSnapshot(ctx, s.config.Path, ReadOptions{
		ExpectedSessionID: s.config.SessionID,
		MaxRecordBytes:    s.config.MaxRecordBytes,
		MaxDiagnostics:    s.config.MaxDiagnostics,
		MaxFileBytes:      s.config.MaxFileBytes,
		MaxEvents:         s.config.MaxEvents,
		ValidateRecord:    s.config.ValidateRecord,
	}, nil)
	return snapshot, err
}

// LoadAndReconcile returns a model-safe projection: fully unresolved modern
// response groups are omitted and missing members of retained groups receive
// synthetic interrupted results. Derived events are never written.
func (s *Store) LoadAndReconcile(ctx context.Context) (Snapshot, error) {
	snapshot, err := s.Load(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	reconciled := snapshot.ReconcileUnresolved()
	if err := validateTranscriptProjection(s.config.ValidateRecord, reconciled); err != nil {
		return Snapshot{}, err
	}
	return reconciled, nil
}

// Close flushes and releases the append handle. It is idempotent.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.CloseTimeout)
	defer cancel()
	if err := s.lock(ctx); err != nil {
		return fmt.Errorf("close transcript waiting for active operation: %w", err)
	}
	defer s.unlock()
	if s.closed {
		return nil
	}
	var result error
	if s.poisoned != nil {
		result = fmt.Errorf("%w: %v", ErrPoisoned, s.poisoned)
	} else if s.file != nil {
		if err := syncVerifiedTranscript(s.file, s.config.Path, s.config.MaxFileBytes); err != nil {
			result = fmt.Errorf("flush transcript during close: %w", err)
		}
	}
	if s.file != nil {
		if err := s.file.Close(); err != nil && result == nil {
			result = fmt.Errorf("close transcript: %w", err)
		}
	}
	s.closed = true
	return result
}

func (s *Store) index(event protocol.Event) {
	s.seenEvents[event.ID] = struct{}{}
	if event.Sequence > s.maxSequence {
		s.maxSequence = event.Sequence
	}
	switch event.Kind {
	case protocol.EventKindToolCall:
		s.accepted[event.ToolCall.ID] = acceptedTool{eventID: event.ID, name: event.ToolCall.Name}
	case protocol.EventKindToolResult:
		s.resolved[event.ToolResult.ToolUseID] = event.ID
	}
}

func (s *Store) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) unlock() { <-s.gate }

func cloneSet(source map[protocol.EventID]struct{}) map[protocol.EventID]struct{} {
	clone := make(map[protocol.EventID]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func cloneIndex(source map[protocol.ToolUseID]acceptedTool) map[protocol.ToolUseID]acceptedTool {
	clone := make(map[protocol.ToolUseID]acceptedTool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneResultIndex(source map[protocol.ToolUseID]protocol.EventID) map[protocol.ToolUseID]protocol.EventID {
	clone := make(map[protocol.ToolUseID]protocol.EventID, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
