package transcript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"

	"github.com/greenpau/agentx/pkg/protocol"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// ReadFile streams and validates a transcript. Missing files load as an empty
// snapshot; permission and other I/O failures remain explicit.
func ReadFile(ctx context.Context, path string, options ReadOptions) (Snapshot, error) {
	snapshot, _, err := readFileSnapshot(ctx, path, options, nil)
	return snapshot, err
}

// readFileSnapshot returns identity evidence captured from the same descriptor
// that supplied the decoded bytes. Store.Open retains that evidence so a
// pathname cannot be inserted or replaced between recovery and first append.
// afterOpen is a package-private adversarial-test seam and is nil in runtime.
func readFileSnapshot(ctx context.Context, path string, options ReadOptions, afterOpen func()) (Snapshot, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, nil, err
	}
	if path == "" {
		return Snapshot{}, nil, errors.New("transcript path is required")
	}
	options = normalizeReadOptions(options)

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		snapshot := Snapshot{SessionID: options.ExpectedSessionID}
		if err := validateTranscriptProjection(options.ValidateRecord, snapshot); err != nil {
			return Snapshot{}, nil, err
		}
		return snapshot, nil, nil
	}
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("inspect transcript: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, nil, ErrUnsafePath
	}
	if info.Size() < 0 || info.Size() > options.MaxFileBytes {
		return Snapshot{}, nil, fmt.Errorf("%w: transcript exceeds %d bytes", ErrResourceLimit, options.MaxFileBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("open transcript: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("inspect opened transcript: %w", err)
	}
	if !sameTranscriptSnapshot(info, openedInfo) {
		return Snapshot{}, nil, ErrUnsafePath
	}
	links, err := openedTranscriptLinkCount(file, openedInfo)
	if err != nil || links != 1 {
		return Snapshot{}, nil, ErrUnsafePath
	}
	if afterOpen != nil {
		afterOpen()
	}

	collector := newDiagnosticCollector(options.MaxDiagnostics)
	var decoded []protocol.Event
	readLimit := options.MaxFileBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	limited := &io.LimitedReader{R: file, N: readLimit}
	err = scanJSONLines(ctx, limited, options.MaxRecordBytes, func(line int, terminated bool, raw []byte, oversized bool) error {
		if oversized {
			collector.add(Diagnostic{
				Code:    "record_too_large",
				Message: "transcript record exceeded the configured byte limit and was skipped",
				Line:    line,
			})
			return nil
		}
		trimmed := bytes.TrimSpace(raw)
		trimmed = bytes.TrimPrefix(trimmed, utf8BOM)
		trimmed = bytes.TrimSuffix(trimmed, utf8BOM)
		trimmed = bytes.TrimSpace(trimmed)
		if len(trimmed) == 0 {
			return nil
		}
		if !json.Valid(trimmed) {
			code := "malformed_record"
			message := "malformed transcript record was isolated and skipped"
			if !terminated {
				code = "truncated_tail"
				message = "crash-truncated transcript tail was ignored"
			}
			collector.add(Diagnostic{Code: code, Message: message, Line: line})
			return nil
		}
		// A syntactically valid JSON object can still fail typed Event decoding,
		// for example when a provider-binding member has the wrong JSON type.
		// Run the physical validator first so security metadata cannot evade its
		// fail-closed policy by being isolated as an ordinary recovery diagnostic.
		if options.ValidateRecord != nil {
			physical := append([]byte(nil), raw...)
			if terminated {
				physical = append(physical, '\n')
			}
			if err := callTranscriptValidator(options.ValidateRecord, physical); err != nil {
				return errors.New("validate existing transcript record")
			}
			if !terminated {
				futureFrame := append(append([]byte(nil), raw...), '\n')
				if err := callTranscriptValidator(options.ValidateRecord, futureFrame); err != nil {
					return errors.New("validate unterminated transcript record")
				}
			}
		}
		var event protocol.Event
		if err := json.Unmarshal(trimmed, &event); err != nil {
			collector.add(Diagnostic{
				Code:    "malformed_record",
				Message: "malformed transcript record was isolated and skipped",
				Line:    line,
			})
			return nil
		}
		if err := event.ValidateStored(); err != nil {
			collector.add(Diagnostic{
				Code:    "invalid_event",
				Message: "schema-invalid transcript event was isolated and skipped",
				Line:    line,
				EventID: event.ID,
			})
			return nil
		}
		if event.Persistence != protocol.PersistenceDurable {
			collector.add(Diagnostic{
				Code:    "ephemeral_record",
				Message: "ephemeral event found on disk was ignored",
				Line:    line,
				EventID: event.ID,
			})
			return nil
		}
		if len(decoded) >= options.MaxEvents {
			return fmt.Errorf("%w: transcript exceeds %d durable events", ErrResourceLimit, options.MaxEvents)
		}
		decoded = append(decoded, event)
		return nil
	})
	if err != nil {
		return Snapshot{}, nil, fmt.Errorf("read transcript: %w", err)
	}
	readBytes := readLimit - limited.N
	if readBytes > options.MaxFileBytes {
		return Snapshot{}, nil, fmt.Errorf("%w: transcript exceeds %d bytes", ErrResourceLimit, options.MaxFileBytes)
	}
	finalInfo, statErr := file.Stat()
	finalLinks := uint64(0)
	linkErr := error(nil)
	if statErr == nil {
		finalLinks, linkErr = openedTranscriptLinkCount(file, finalInfo)
	}
	if statErr != nil || linkErr != nil || finalLinks != 1 ||
		!sameTranscriptSnapshot(info, finalInfo) || finalInfo.Size() != readBytes {
		return Snapshot{}, nil, ErrUnsafePath
	}
	if err := file.Close(); err != nil {
		return Snapshot{}, nil, fmt.Errorf("close transcript snapshot: %w", err)
	}
	closed = true

	snapshot, err := normalizeLoadedEvents(decoded, options.ExpectedSessionID, collector)
	if err != nil {
		return Snapshot{}, nil, err
	}
	snapshot.Diagnostics = append(snapshot.Diagnostics, collector.items...)
	snapshot.DroppedDiagnostics = collector.dropped
	if err := validateTranscriptProjection(options.ValidateRecord, snapshot); err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, finalInfo, nil
}

func callTranscriptValidator(validate func([]byte) error, raw []byte) (err error) {
	if validate == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("transcript record validator panicked")
		}
	}()
	return validate(append([]byte(nil), raw...))
}

func validateTranscriptProjection(validate func([]byte) error, snapshot Snapshot) error {
	if validate == nil {
		return nil
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return errors.New("encode transcript recovery projection")
	}
	raw = append(raw, '\n')
	if err := callTranscriptValidator(validate, raw); err != nil {
		return errors.New("validate transcript recovery projection")
	}
	return nil
}

func sameTranscriptSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) && left.Mode() == right.Mode()
}

func normalizeReadOptions(options ReadOptions) ReadOptions {
	if options.MaxRecordBytes <= 0 {
		options.MaxRecordBytes = DefaultMaxRecordBytes
	}
	if options.MaxDiagnostics <= 0 {
		options.MaxDiagnostics = DefaultMaxDiagnostics
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultMaxFileBytes
	}
	if options.MaxEvents <= 0 {
		options.MaxEvents = DefaultMaxEvents
	}
	return options
}

func normalizeLoadedEvents(events []protocol.Event, expected protocol.SessionID, collector *diagnosticCollector) (Snapshot, error) {
	snapshot := Snapshot{SessionID: expected}
	seenEvents := make(map[protocol.EventID]struct{}, len(events))
	seenSequences := make(map[uint64]protocol.EventID, len(events))
	ordered := make([]protocol.Event, 0, len(events))
	var previousSequence uint64

	for _, event := range events {
		if snapshot.SessionID == "" {
			snapshot.SessionID = event.SessionID
		}
		if event.SessionID != snapshot.SessionID {
			if expected != "" {
				return Snapshot{}, ErrSessionMismatch
			}
			collector.add(Diagnostic{
				Code:    "foreign_session_event",
				Message: "event owned by another session was skipped",
				EventID: event.ID,
			})
			continue
		}
		if _, exists := seenEvents[event.ID]; exists {
			collector.add(Diagnostic{
				Code:    "duplicate_event_id",
				Message: "duplicate event identifier was suppressed",
				EventID: event.ID,
			})
			continue
		}
		seenEvents[event.ID] = struct{}{}
		if owner, exists := seenSequences[event.Sequence]; exists {
			collector.add(Diagnostic{
				Code:    "duplicate_sequence",
				Message: "duplicate event sequence was suppressed; its first physical owner was retained",
				EventID: event.ID,
			})
			_ = owner
			continue
		}
		seenSequences[event.Sequence] = event.ID
		if previousSequence != 0 && event.Sequence <= previousSequence {
			collector.add(Diagnostic{
				Code:    "non_monotonic_sequence",
				Message: "physical record order was non-monotonic; canonical sequence order was reconstructed",
				EventID: event.ID,
			})
		}
		if event.Sequence > snapshot.MaxSequence {
			snapshot.MaxSequence = event.Sequence
		}
		previousSequence = event.Sequence
		ordered = append(ordered, event)
	}

	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	ordered = coherentParentProjection(ordered, collector)
	accepted := make(map[protocol.ToolUseID]protocol.Event)
	resolved := make(map[protocol.ToolUseID]protocol.EventID)
	for _, event := range ordered {

		switch event.Kind {
		case protocol.EventKindToolCall:
			toolID := event.ToolCall.ID
			if _, exists := accepted[toolID]; exists {
				collector.add(Diagnostic{
					Code:      "duplicate_tool_use",
					Message:   "duplicate accepted tool-use identifier was suppressed",
					EventID:   event.ID,
					ToolUseID: toolID,
				})
				continue
			}
			accepted[toolID] = event
		case protocol.EventKindToolResult:
			toolID := event.ToolResult.ToolUseID
			call, exists := accepted[toolID]
			if !exists {
				collector.add(Diagnostic{
					Code:      "orphan_tool_result",
					Message:   "tool result without a preceding accepted call was suppressed",
					EventID:   event.ID,
					ToolUseID: toolID,
				})
				continue
			}
			if event.ToolResult.ToolName != call.ToolCall.Name {
				collector.add(Diagnostic{
					Code:      "tool_name_mismatch",
					Message:   "tool result name did not match its accepted call and was suppressed",
					EventID:   event.ID,
					ToolUseID: toolID,
				})
				continue
			}
			if event.ParentID != nil && *event.ParentID != call.ID {
				collector.add(Diagnostic{
					Code:      "tool_parent_mismatch",
					Message:   "tool result parent did not match its accepted call and was suppressed",
					EventID:   event.ID,
					ToolUseID: toolID,
				})
				continue
			}
			if _, exists := resolved[toolID]; exists {
				collector.add(Diagnostic{
					Code:      "duplicate_tool_result",
					Message:   "duplicate terminal tool result was suppressed",
					EventID:   event.ID,
					ToolUseID: toolID,
				})
				continue
			}
			resolved[toolID] = event.ID
		}

		snapshot.Events = append(snapshot.Events, event)
	}
	return snapshot, nil
}

func coherentParentProjection(events []protocol.Event, collector *diagnosticCollector) []protocol.Event {
	byID := make(map[protocol.EventID]protocol.Event, len(events))
	for _, event := range events {
		byID[event.ID] = event
	}
	processed := make(map[protocol.EventID]bool, len(events))
	valid := make(map[protocol.EventID]bool, len(events))
	for _, start := range events {
		if processed[start.ID] {
			continue
		}
		path := make([]protocol.EventID, 0, 16)
		positions := make(map[protocol.EventID]int)
		cursor := start.ID
		terminalValid := true
		for {
			if processed[cursor] {
				terminalValid = valid[cursor]
				break
			}
			if position, exists := positions[cursor]; exists {
				collector.add(Diagnostic{
					Code:    "parent_cycle",
					Message: "parent cycle was suppressed from the coherent recovery projection",
					EventID: path[position],
				})
				terminalValid = false
				break
			}
			positions[cursor] = len(path)
			path = append(path, cursor)
			event, exists := byID[cursor]
			if !exists || event.ParentID == nil {
				break
			}
			parent, exists := byID[*event.ParentID]
			if !exists {
				collector.add(Diagnostic{
					Code:    "missing_parent",
					Message: "parent event was unavailable; this event begins a retained coherent suffix",
					EventID: event.ID,
				})
				break
			}
			if parent.Sequence >= event.Sequence {
				collector.add(Diagnostic{
					Code:    "noncausal_parent",
					Message: "event parent did not precede its child and the dependent history was suppressed",
					EventID: event.ID,
				})
				terminalValid = false
				break
			}
			cursor = *event.ParentID
		}
		for index := len(path) - 1; index >= 0; index-- {
			id := path[index]
			processed[id] = true
			valid[id] = terminalValid
		}
	}
	result := make([]protocol.Event, 0, len(events))
	for _, event := range events {
		if valid[event.ID] {
			result = append(result, event)
			continue
		}
		collector.add(Diagnostic{
			Code:    "unreachable_event",
			Message: "event was excluded from the coherent recovery projection",
			EventID: event.ID,
		})
	}
	return result
}

// scanJSONLines isolates physical lines without allocating an unbounded split
// result. It retains only one bounded record and discards the remainder of an
// oversized line until the next LF.
func scanJSONLines(
	ctx context.Context,
	reader io.Reader,
	maxRecordBytes int,
	visit func(line int, terminated bool, raw []byte, oversized bool) error,
) error {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	lineNumber := 1
	line := make([]byte, 0, min(maxRecordBytes, 64*1024))
	oversized := false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fragment, err := buffered.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > maxRecordBytes {
				line = line[:0]
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}

		switch {
		case err == nil:
			if !oversized && len(line) > 0 {
				line = line[:len(line)-1]
			}
			if err := visit(lineNumber, true, line, oversized); err != nil {
				return err
			}
			lineNumber++
			line = make([]byte, 0, min(maxRecordBytes, 64*1024))
			oversized = false
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(fragment) > 0 || len(line) > 0 || oversized {
				if err := visit(lineNumber, false, line, oversized); err != nil {
					return err
				}
			}
			return nil
		default:
			return err
		}
	}
}

type diagnosticCollector struct {
	limit   int
	items   []Diagnostic
	dropped int
}

func newDiagnosticCollector(limit int) *diagnosticCollector {
	return &diagnosticCollector{limit: limit}
}

func (c *diagnosticCollector) add(diagnostic Diagnostic) {
	if len(c.items) < c.limit {
		c.items = append(c.items, diagnostic)
		return
	}
	c.dropped++
}
