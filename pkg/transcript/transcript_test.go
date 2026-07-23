package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
)

var fixedTime = time.Date(2026, 7, 21, 14, 30, 0, 0, time.UTC)

func newTestStore(t *testing.T, path string, sessionID protocol.SessionID) *Store {
	t.Helper()
	store, err := Open(context.Background(), Config{
		Path:      path,
		SessionID: sessionID,
		SessionMetadata: protocol.SessionMetadata{
			WorkingDirectory: "/workspace/project",
			Entrypoint:       "cli",
			Surface:          "headless",
			ProductVersion:   "test",
		},
		Now: func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func TestStoreRejectsCredentialReconstructedByFinalRecordFraming(t *testing.T) {
	const secret = `foo"}]`
	set := redact.New(secret)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store, err := Open(t.Context(), Config{
		Path: path, SessionID: "ses_framing_guard",
		ValidateRecord: func(raw []byte) error {
			reflected, inspectErr := set.JSONContains(raw)
			if inspectErr != nil {
				return inspectErr
			}
			if reflected {
				return errors.New("credential reflected")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := messageEvent("evt_framing_guard", "ses_framing_guard", "foo")
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), secret) {
		t.Fatalf("fixture did not reconstruct credential: %s", encoded)
	}
	if err := store.Append(t.Context(), event); err == nil {
		t.Fatal("unsafe transcript record was appended")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe transcript materialized at %q: %v", path, err)
	}
}

func TestStoreValidatorInspectsExactNewlineFramedRecord(t *testing.T) {
	event := messageEvent("evt_newline_guard", "ses_newline_guard", "safe")
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(body[len(body)-3:]) + "\n"
	set := redact.New(secret)
	if matched, err := set.JSONContains(body); err != nil || matched {
		t.Fatalf("unframed fixture = matched %t, %v", matched, err)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store, err := Open(t.Context(), Config{
		Path: path, SessionID: event.SessionID,
		ValidateRecord: func(raw []byte) error {
			matched, inspectErr := set.JSONContains(raw)
			if inspectErr != nil {
				return inspectErr
			}
			if matched {
				return errors.New("credential reflected")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(t.Context(), event); err == nil {
		t.Fatal("newline-only framing credential was appended")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe transcript materialized at %q: %v", path, err)
	}
}

func TestOpenRejectsCredentialBearingLegacyPhysicalRecord(t *testing.T) {
	const secret = "legacy-transcript-resume-credential"
	event := messageEvent("evt_legacy_credential", "ses_legacy_credential", secret)
	event.Sequence = 1
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	set := redact.New(secret)
	validate := func(raw []byte) error {
		matched, inspectErr := set.JSONContains(raw)
		if inspectErr != nil {
			return inspectErr
		}
		if matched {
			return errors.New("credential reflected")
		}
		return nil
	}
	store, err := Open(t.Context(), Config{
		Path: path, SessionID: event.SessionID, ValidateRecord: validate,
	})
	if err == nil || store != nil || !strings.Contains(err.Error(), "validate existing transcript record") {
		t.Fatalf("credential-bearing legacy record reopened: store=%#v err=%v", store, err)
	}
	snapshot, err := ReadFile(t.Context(), path, ReadOptions{
		ExpectedSessionID: event.SessionID, ValidateRecord: validate,
	})
	if err == nil || len(snapshot.Events) != 0 {
		t.Fatalf("credential-bearing direct read succeeded: snapshot=%#v err=%v", snapshot, err)
	}
	for _, rendered := range []string{
		err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("legacy transcript validation error exposed credential: %q", rendered)
		}
	}
}

func TestOpenValidatesFutureNewlineForUnterminatedLegacyRecord(t *testing.T) {
	event := messageEvent("evt_legacy_tail", "ses_legacy_tail", "safe")
	event.Sequence = 1
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(raw[len(raw)-3:]) + "\n"
	set := redact.New(secret)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), Config{
		Path: path, SessionID: event.SessionID,
		ValidateRecord: func(raw []byte) error {
			matched, inspectErr := set.JSONContains(raw)
			if inspectErr != nil {
				return inspectErr
			}
			if matched {
				return errors.New("credential reflected")
			}
			return nil
		},
	})
	if err == nil || store != nil || !strings.Contains(err.Error(), "validate unterminated transcript record") {
		t.Fatalf("unsafe unterminated legacy record reopened: store=%#v err=%v", store, err)
	}
}

func TestReadFileValidatesCompleteRecoveredSnapshot(t *testing.T) {
	const secret = `ses_snapshot","events":[`
	event := messageEvent("evt_snapshot_projection", "ses_snapshot", "safe")
	event.Sequence = 1
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("fixture credential unexpectedly occurs in physical event: %s", raw)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	set := redact.New(secret)
	snapshot, err := ReadFile(t.Context(), path, ReadOptions{
		ExpectedSessionID: event.SessionID,
		ValidateRecord: func(raw []byte) error {
			matched, inspectErr := set.JSONContains(raw)
			if inspectErr != nil {
				return inspectErr
			}
			if matched {
				return errors.New("credential reflected")
			}
			return nil
		},
	})
	if err == nil || len(snapshot.Events) != 0 || !strings.Contains(err.Error(), "validate transcript recovery projection") {
		t.Fatalf("unsafe recovered Snapshot escaped: snapshot=%#v err=%v", snapshot, err)
	}
}

func TestTranscriptValidatorPanicIsContained(t *testing.T) {
	store, err := Open(t.Context(), Config{
		Path: filepath.Join(t.TempDir(), "session.jsonl"), SessionID: "ses_validator_panic",
		ValidateRecord: func([]byte) error {
			panic("validator unavailable")
		},
	})
	if err == nil || store != nil || !strings.Contains(err.Error(), "validate transcript recovery projection") {
		t.Fatalf("panicking transcript validator was accepted: store=%#v err=%v", store, err)
	}
}

func TestTranscriptClockPanicZeroAndReentryStayOutsideAppendGate(t *testing.T) {
	for _, test := range []struct {
		name  string
		clock func() time.Time
	}{
		{name: "panic", clock: func() time.Time { panic("clock unavailable") }},
		{name: "zero", clock: func() time.Time { return time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(t.Context(), Config{
				Path: filepath.Join(t.TempDir(), "session.jsonl"), SessionID: protocol.SessionID("ses_clock_" + test.name),
				Now: test.clock,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			event := messageEvent("evt_clock_"+protocol.EventID(test.name), store.SessionID(), "clock")
			event.Timestamp = time.Time{}
			normalized, written, err := store.AppendEvent(t.Context(), event)
			if err != nil || !written || normalized.Timestamp.IsZero() {
				t.Fatalf("AppendEvent = written %t timestamp %v err %v", written, normalized.Timestamp, err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "session.jsonl")
	var store *Store
	var reentered atomic.Bool
	clock := func() time.Time {
		if reentered.CompareAndSwap(false, true) {
			inner := messageEvent("evt_clock_inner", "ses_clock_reentry", "inner")
			inner.Timestamp = time.Time{}
			if err := store.Append(t.Context(), inner); err != nil {
				t.Errorf("reentrant append: %v", err)
			}
		}
		return fixedTime
	}
	var err error
	store, err = Open(t.Context(), Config{Path: path, SessionID: "ses_clock_reentry", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	outer := messageEvent("evt_clock_outer", store.SessionID(), "outer")
	outer.Timestamp = time.Time{}
	if err := store.Append(t.Context(), outer); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadFile(t.Context(), path, ReadOptions{ExpectedSessionID: store.SessionID()})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("reentrant clock persisted %d events, want 2", len(snapshot.Events))
	}
}

func messageEvent(id protocol.EventID, sessionID protocol.SessionID, text string) protocol.Event {
	return protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          id,
		SessionID:   sessionID,
		TurnID:      "turn_test",
		Timestamp:   fixedTime,
		Kind:        protocol.EventKindMessage,
		Visibility:  protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginUser,
		Message: &protocol.Message{
			Role:    protocol.RoleUser,
			Content: []protocol.ContentBlock{protocol.TextBlock(text)},
		},
	}
}

func toolCallEvent(eventID protocol.EventID, sessionID protocol.SessionID, toolID protocol.ToolUseID) protocol.Event {
	return protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          eventID,
		SessionID:   sessionID,
		TurnID:      "turn_test",
		Timestamp:   fixedTime.Add(time.Second),
		Kind:        protocol.EventKindToolCall,
		Visibility:  protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginModel,
		ToolCall: &protocol.ToolCall{
			ID:        toolID,
			Name:      "Read",
			Arguments: json.RawMessage(`{"path":"README.md"}`),
		},
	}
}

func toolResultEvent(eventID protocol.EventID, sessionID protocol.SessionID, toolID protocol.ToolUseID) protocol.Event {
	return protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          eventID,
		SessionID:   sessionID,
		TurnID:      "turn_test",
		Timestamp:   fixedTime.Add(2 * time.Second),
		Kind:        protocol.EventKindToolResult,
		Visibility:  protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginCapability,
		ToolResult: &protocol.ToolResult{
			ToolUseID: toolID,
			ToolName:  "Read",
			Status:    protocol.ToolResultSuccess,
			Content:   []protocol.ContentBlock{protocol.TextBlock("contents")},
		},
	}
}

func progressEvent(id protocol.EventID, sessionID protocol.SessionID) protocol.Event {
	return protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          id,
		SessionID:   sessionID,
		Timestamp:   fixedTime,
		Kind:        protocol.EventKindProgress,
		Visibility:  protocol.VisibilityUser,
		Persistence: protocol.PersistenceEphemeral,
		Origin:      protocol.OriginRuntime,
		Progress:    &protocol.ProgressEvent{Phase: "tool", Message: "working"},
	}
}

func TestStoreRoundTripPermissionsPairingAndLazyMaterialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open materialized a fresh transcript: %v", err)
	}
	if err := store.Append(context.Background(), progressEvent("evt_progress", "ses_test")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral progress materialized transcript: %v", err)
	}

	message := messageEvent("evt_message", "ses_test", "hello")
	call := toolCallEvent("evt_call", "ses_test", "tool_read")
	result := toolResultEvent("evt_result", "ses_test", "tool_read")
	if err := store.AppendBatch(context.Background(), []protocol.Event{message, call}); err != nil {
		t.Fatal(err)
	}
	normalizedResult, appended, err := store.AppendEvent(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if !appended || normalizedResult.ParentID == nil || *normalizedResult.ParentID != call.ID {
		t.Fatalf("tool result parent was not inferred from its accepted call: %#v", normalizedResult.ParentID)
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}

	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 3 || snapshot.MaxSequence != 3 {
		t.Fatalf("unexpected snapshot: events=%d max=%d", len(snapshot.Events), snapshot.MaxSequence)
	}
	for index, event := range snapshot.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence=%d", index, event.Sequence)
		}
		if event.Session.WorkingDirectory != "/workspace/project" || event.Session.Surface != "headless" {
			t.Fatalf("event was not destination-restamped: %#v", event.Session)
		}
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Call.ID != call.ID || pairs[0].Result.ID != result.ID {
		t.Fatalf("unexpected tool pairs: %#v", pairs)
	}
}

func TestDuplicateEventIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	event := messageEvent("evt_same", "ses_test", "hello")
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 {
		t.Fatalf("duplicate event was persisted: %d", len(snapshot.Events))
	}
}

func TestSequenceExhaustionFailsBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	event := messageEvent("evt_max", "ses_test", "last")
	event.Sequence = math.MaxUint64
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_overflow", "ses_test", "overflow")); !errors.Is(err, ErrSequenceExhausted) {
		t.Fatalf("append error = %v, want %v", err, ErrSequenceExhausted)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("sequence-exhausted append changed durable transcript")
	}
}

func TestTranscriptReadAndWriteResourceLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first := messageEvent("evt_one", "ses_test", "one")
	first.Sequence = 1
	second := messageEvent("evt_two", "ses_test", "two")
	second.Sequence = 2
	var data []byte
	for _, event := range []protocol.Event{first, second} {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test", MaxFileBytes: int64(len(data) - 1)}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("file limit error = %v", err)
	}
	if _, err := ReadFile(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test", MaxEvents: 1}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("event limit error = %v", err)
	}

	writePath := filepath.Join(t.TempDir(), "bounded.jsonl")
	store, err := Open(context.Background(), Config{Path: writePath, SessionID: "ses_test", MaxEvents: 1, MaxFileBytes: DefaultMaxFileBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), messageEvent("evt_first", "ses_test", "one")); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "two")); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("writer event limit error = %v", err)
	}
}

func TestToolCorrelationFailsClosedAndBatchIsPrevalidated(t *testing.T) {
	nameMismatch := toolResultEvent("evt_result", "ses_test", "tool_same")
	nameMismatch.ToolResult.ToolName = "Bash"
	parentMismatch := toolResultEvent("evt_result", "ses_test", "tool_same")
	wrongParent := protocol.EventID("evt_other")
	parentMismatch.ParentID = &wrongParent
	tests := []struct {
		name   string
		events []protocol.Event
		want   error
	}{
		{
			name:   "orphan result",
			events: []protocol.Event{toolResultEvent("evt_result", "ses_test", "tool_missing")},
			want:   ErrUnknownToolUse,
		},
		{
			name: "duplicate accepted id",
			events: []protocol.Event{
				toolCallEvent("evt_call_1", "ses_test", "tool_same"),
				toolCallEvent("evt_call_2", "ses_test", "tool_same"),
			},
			want: ErrDuplicateToolUse,
		},
		{
			name: "duplicate result",
			events: []protocol.Event{
				toolCallEvent("evt_call", "ses_test", "tool_same"),
				toolResultEvent("evt_result_1", "ses_test", "tool_same"),
				toolResultEvent("evt_result_2", "ses_test", "tool_same"),
			},
			want: ErrDuplicateToolResult,
		},
		{
			name: "tool name mismatch",
			events: []protocol.Event{
				toolCallEvent("evt_call", "ses_test", "tool_same"),
				nameMismatch,
			},
			want: ErrToolNameMismatch,
		},
		{
			name: "tool parent mismatch",
			events: []protocol.Event{
				toolCallEvent("evt_call", "ses_test", "tool_same"),
				parentMismatch,
			},
			want: ErrToolParentMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			store := newTestStore(t, path, "ses_test")
			err := store.AppendBatch(context.Background(), test.events)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid prevalidated batch wrote a file: %v", statErr)
			}
		})
	}
}

func TestMalformedProviderArgumentsRemainPairable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	call := toolCallEvent("evt_call", "ses_test", "tool_malformed")
	raw := `{"path":`
	call.ToolCall.Arguments = nil
	call.ToolCall.RawArguments = &raw
	if err := call.Validate(); err != nil {
		t.Fatalf("provider call must be persistable before argument parsing: %v", err)
	}
	if _, err := call.ToolCall.ParseArguments(); err == nil {
		t.Fatal("malformed provider arguments unexpectedly parsed")
	}
	result := toolResultEvent("evt_result", "ses_test", "tool_malformed")
	result.ToolResult.Status = protocol.ToolResultMalformed
	result.ToolResult.IsError = true
	result.ToolResult.Content = []protocol.ContentBlock{protocol.TextBlock("tool arguments were malformed")}
	result.ToolResult.Error = &protocol.ErrorInfo{Code: "malformed_arguments", Message: "tool arguments must be a JSON object"}
	if err := store.AppendBatch(context.Background(), []protocol.Event{call, result}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := snapshot.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Result.ToolResult.Status != protocol.ToolResultMalformed {
		t.Fatalf("malformed accepted call did not receive one terminal result: %#v", pairs)
	}
}

func TestReadFileIsolatesMalformedMiddleAndTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first := messageEvent("evt_one", "ses_test", "one")
	first.Sequence = 1
	second := messageEvent("evt_two", "ses_test", "two")
	second.Sequence = 2
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	contents := append(append(append(append([]byte{}, firstJSON...), '\n'), []byte("{malformed}\n")...), secondJSON...)
	contents = append(contents, '\n')
	contents = append(contents, []byte(`{"version":1,"id":`)...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := ReadFile(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[0].ID != first.ID || snapshot.Events[1].ID != second.ID {
		t.Fatalf("valid records were not preserved: %#v", snapshot.Events)
	}
	if !hasDiagnostic(snapshot, "malformed_record") || !hasDiagnostic(snapshot, "truncated_tail") {
		t.Fatalf("missing corruption diagnostics: %#v", snapshot.Diagnostics)
	}
}

func TestAppendIsolatesCrashPartialTailBeforeNewRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first := messageEvent("evt_first", "ses_test", "first")
	first.Sequence = 1
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"version":1,"id":"evt_crash_partial"`)
	contents := append(append(append([]byte{}, firstJSON...), '\n'), partial...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test", SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, append(append([]byte{}, partial...), '\n')) {
		t.Fatalf("partial tail was not isolated before append: %q", raw)
	}
	reopened, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[0].ID != "evt_first" || snapshot.Events[1].ID != "evt_second" {
		t.Fatalf("append after crash tail lost valid records: %#v", snapshot.Events)
	}
	if !hasDiagnostic(snapshot, "malformed_record") || hasDiagnostic(snapshot, "truncated_tail") {
		t.Fatalf("isolated crash tail diagnostics = %#v", snapshot.Diagnostics)
	}
}

func TestAppendRepairsValidRecordWithoutFinalNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first := messageEvent("evt_first", "ses_test", "first")
	first.Sequence = 1
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, firstJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test", SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, append(append([]byte{}, firstJSON...), '\n')) {
		t.Fatalf("valid unterminated record was not repaired: %q", raw)
	}
	reopened, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[0].ID != "evt_first" || snapshot.Events[1].ID != "evt_second" || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("valid no-newline append round trip = %#v", snapshot)
	}
}

func TestCloseWaitForAppendOwnershipIsBounded(t *testing.T) {
	store, err := Open(context.Background(), Config{
		Path: filepath.Join(t.TempDir(), "session.jsonl"), SessionID: "ses_test", CloseTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = store.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while append gate held = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close lock wait was unbounded: %s", elapsed)
	}
	store.unlock()
	if err := store.Close(); err != nil {
		t.Fatalf("Close did not remain retryable after bounded wait: %v", err)
	}
}

func TestRecoverySynthesizesInterruptedResultWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	resolvedCall := toolCallEvent("evt_call_done", "ses_test", "tool_done")
	resolvedResult := toolResultEvent("evt_result_done", "ses_test", "tool_done")
	unresolvedCall := toolCallEvent("evt_call_lost", "ses_test", "tool_lost")
	if err := store.AppendBatch(context.Background(), []protocol.Event{resolvedCall, resolvedResult, unresolvedCall}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := store.LoadAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := recovered.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2", len(pairs))
	}
	var synthetic protocol.Event
	for _, pair := range pairs {
		if pair.Call.ToolCall.ID == "tool_lost" {
			synthetic = pair.Result
		}
	}
	if synthetic.ToolResult == nil || synthetic.ToolResult.Status != protocol.ToolResultInterrupted ||
		!synthetic.ToolResult.Synthetic || synthetic.Persistence != protocol.PersistenceEphemeral ||
		synthetic.Origin != protocol.OriginRecovery || synthetic.ParentID == nil || *synthetic.ParentID != unresolvedCall.ID {
		t.Fatalf("unexpected recovery result: %#v", synthetic)
	}
	if synthetic.Sequence != 0 {
		t.Fatalf("derived result invented a durable sequence: %d", synthetic.Sequence)
	}
	if err := synthetic.ValidateStored(); err != nil {
		t.Fatalf("synthetic result is not protocol-valid: %v", err)
	}

	again, err := store.LoadAndReconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var againID protocol.EventID
	for _, event := range again.Events {
		if event.Kind == protocol.EventKindToolResult && event.ToolResult.ToolUseID == "tool_lost" {
			againID = event.ID
		}
	}
	if againID != synthetic.ID {
		t.Fatalf("recovery ID is not deterministic: %q != %q", againID, synthetic.ID)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("recovery rewrote transcript: before=%d after=%d", before.Size(), after.Size())
	}
	physical, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := physical.ToolPairs(); err == nil {
		t.Fatal("physical transcript unexpectedly claimed the unresolved call was settled")
	}
}

func TestModelEventsExcludePresentationAndAccountingRecords(t *testing.T) {
	message := messageEvent("evt_message", "ses_test", "hello")
	turnResult := protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          "evt_turn_result",
		SessionID:   "ses_test",
		Sequence:    2,
		Timestamp:   fixedTime,
		Kind:        protocol.EventKindTurnResult,
		Visibility:  protocol.VisibilityUser,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginRuntime,
		TurnResult:  &protocol.TurnResult{Status: protocol.TurnResultSuccess},
	}
	usage := protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          "evt_usage",
		SessionID:   "ses_test",
		Sequence:    3,
		Timestamp:   fixedTime,
		Kind:        protocol.EventKindUsage,
		Visibility:  protocol.VisibilityInternal,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginRuntime,
		Usage:       &protocol.Usage{Model: "gpt-5.6-sol"},
	}
	events := (Snapshot{Events: []protocol.Event{message, turnResult, usage}}).ModelEvents()
	if len(events) != 1 || events[0].ID != message.ID {
		t.Fatalf("unexpected model projection: %#v", events)
	}
}

func TestConcurrentAppendPreservesUniqueMonotonicSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	const count = 100
	var group sync.WaitGroup
	errorsByIndex := make([]error, count)
	for index := 0; index < count; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			event := messageEvent(protocol.EventID(fmt.Sprintf("evt_%03d", index)), "ses_test", fmt.Sprintf("message %d", index))
			errorsByIndex[index] = store.Append(context.Background(), event)
		}()
	}
	group.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != count {
		t.Fatalf("got %d events, want %d", len(snapshot.Events), count)
	}
	for index, event := range snapshot.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("physical event %d has sequence %d", index, event.Sequence)
		}
	}
}

func TestReadDedupeSessionMismatchAndBoundedDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	event := messageEvent("evt_same", "ses_test", "hello")
	event.Sequence = 1
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte{}, encoded...), '\n')
	data = append(data, encoded...)
	data = append(data, '\n')
	for range 5 {
		data = append(data, []byte("bad\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadFile(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test", MaxDiagnostics: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || len(snapshot.Diagnostics) != 2 || snapshot.DroppedDiagnostics == 0 {
		t.Fatalf("unexpected bounded dedupe result: %#v", snapshot)
	}
	if _, err := ReadFile(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_other"}); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("got %v, want session mismatch", err)
	}
}

func TestCancelledContextCloseAndSymlinkSafety(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	store := newTestStore(t, path, "ses_test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Append(ctx, messageEvent("evt_cancelled", "ses_test", "no")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close was not idempotent: %v", err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_closed", "ses_test", "no")); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want closed", err)
	}

	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Open(context.Background(), Config{Path: link, SessionID: "ses_test"}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v, want unsafe path", err)
	}
}

func TestReadFilePinsBoundedSingleLinkSnapshot(t *testing.T) {
	event := messageEvent("evt_snapshot", "ses_test", "snapshot")
	event.Sequence = 1
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	t.Run("hardlink", func(t *testing.T) {
		directory := t.TempDir()
		source := filepath.Join(directory, "source.jsonl")
		linked := filepath.Join(directory, "linked.jsonl")
		if err := os.WriteFile(source, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(source, linked); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if _, err := ReadFile(context.Background(), linked, ReadOptions{ExpectedSessionID: "ses_test"}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("hardlinked transcript error = %v, want %v", err, ErrUnsafePath)
		}
	})

	t.Run("mutation after open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		var mutationErr error
		_, _, err := readFileSnapshot(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test"}, func() {
			file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if openErr != nil {
				mutationErr = openErr
				return
			}
			_, writeErr := file.WriteString("concurrent-tail")
			mutationErr = errors.Join(writeErr, file.Close())
		})
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("mutated snapshot error = %v, want %v", err, ErrUnsafePath)
		}
	})

	t.Run("growth beyond configured bound", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		maximum := int64(len(encoded) + 8)
		var mutationErr error
		_, _, err := readFileSnapshot(context.Background(), path, ReadOptions{ExpectedSessionID: "ses_test", MaxFileBytes: maximum}, func() {
			file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if openErr != nil {
				mutationErr = openErr
				return
			}
			_, writeErr := file.Write(bytes.Repeat([]byte{'x'}, 64))
			mutationErr = errors.Join(writeErr, file.Close())
		})
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("growing snapshot error = %v, want %v", err, ErrResourceLimit)
		}
	})
}

func TestStoreRetainsRecoveredPathIdentityUntilFirstAppend(t *testing.T) {
	t.Run("absent path insertion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(context.Background(), messageEvent("evt_inserted", "ses_test", "no")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("append after insertion = %v, want %v", err, ErrUnsafePath)
		}
	})

	t.Run("existing path replacement", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "session.jsonl")
		first := messageEvent("evt_first", "ses_test", "first")
		first.Sequence = 1
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		old := filepath.Join(directory, "old.jsonl")
		if err := os.Rename(path, old); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("append after replacement = %v, want %v", err, ErrUnsafePath)
		}
	})
}

func TestStoreRejectsHardlinkAddedAfterMaterialization(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test", SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), messageEvent("evt_first", "ses_test", "first")); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "linked.jsonl")
	if err := os.Link(path, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("append after hardlink = %v, want %v", err, ErrUnsafePath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("evt_second")) {
		t.Fatal("append wrote semantic bytes after link-count ambiguity")
	}
}

func TestStoreRejectsPathReplacementAfterMaterialization(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test", SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), messageEvent("evt_first", "ses_test", "first")); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(directory, "moved.jsonl")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("append after path replacement = %v, want %v", err, ErrUnsafePath)
	}
	movedRaw, err := os.ReadFile(moved)
	if err != nil {
		t.Fatal(err)
	}
	replacementRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(movedRaw, []byte("evt_second")) || len(replacementRaw) != 0 {
		t.Fatal("append wrote semantic bytes after transcript path replacement")
	}
}

func TestStoreCloseRejectsPathReplacementBeforeDurabilityBarrier(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	store, err := Open(context.Background(), Config{Path: path, SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), messageEvent("evt_first", "ses_test", "first")); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(directory, "moved.jsonl")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("close after path replacement = %v, want %v", err, ErrUnsafePath)
	}
}

func TestStoreSyncsParentDirectoryOnFirstMaterialization(t *testing.T) {
	t.Run("once", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private", "session.jsonl")
		calls := 0
		store, err := Open(context.Background(), Config{
			Path: path, SessionID: "ses_test", SyncOnAppend: true,
			syncDirectory: func(directory *os.File) error {
				calls++
				info, err := directory.Stat()
				if err != nil || !info.IsDir() {
					return errors.New("directory sync did not receive a pinned directory")
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.Append(context.Background(), messageEvent("evt_first", "ses_test", "first")); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(context.Background(), messageEvent("evt_second", "ses_test", "second")); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("parent directory sync calls = %d, want 1", calls)
		}
	})

	t.Run("failure precedes semantic append", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		failure := errors.New("injected directory sync failure")
		store, err := Open(context.Background(), Config{
			Path: path, SessionID: "ses_test", SyncOnAppend: true,
			syncDirectory: func(*os.File) error { return failure },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.Append(context.Background(), messageEvent("evt_not_written", "ses_test", "no")); !errors.Is(err, failure) {
			t.Fatalf("directory sync failure = %v, want %v", err, failure)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			t.Fatalf("semantic append preceded directory durability: size=%d", info.Size())
		}
	})
}

func TestOversizedRecordIsSkippedWithoutLosingNextLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	valid := messageEvent("evt_valid", "ses_test", "ok")
	valid.Sequence = 1
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	maxRecordBytes := len(encoded) + 16
	data := append([]byte(`{"oversized":"`), make([]byte, maxRecordBytes+128)...)
	data = append(data, []byte(`"}`)...)
	data = append(data, '\n')
	data = append(data, encoded...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadFile(context.Background(), path, ReadOptions{
		ExpectedSessionID: "ses_test",
		MaxRecordBytes:    maxRecordBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].ID != valid.ID || !hasDiagnostic(snapshot, "record_too_large") {
		t.Fatalf("unexpected oversized-line recovery: %#v", snapshot)
	}
}

func TestRecoveryReconstructsSequenceOrderAndSuppressesIncoherentBranches(t *testing.T) {
	first := messageEvent("evt_order_first", "ses_test", "first")
	first.Sequence = 1
	second := messageEvent("evt_order_second", "ses_test", "second")
	second.Sequence = 2
	second.ParentID = &first.ID
	duplicateSequence := messageEvent("evt_duplicate_sequence", "ses_test", "duplicate")
	duplicateSequence.Sequence = 2
	cycleA := messageEvent("evt_cycle_a", "ses_test", "cycle-a")
	cycleA.Sequence = 3
	cycleB := messageEvent("evt_cycle_b", "ses_test", "cycle-b")
	cycleB.Sequence = 4
	cycleA.ParentID = &cycleB.ID
	cycleB.ParentID = &cycleA.ID

	collector := newDiagnosticCollector(DefaultMaxDiagnostics)
	snapshot, err := normalizeLoadedEvents([]protocol.Event{second, cycleB, first, duplicateSequence, cycleA}, "ses_test", collector)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || snapshot.Events[0].ID != first.ID || snapshot.Events[1].ID != second.ID {
		t.Fatalf("coherent sequence projection = %#v", snapshot.Events)
	}
	codes := make(map[string]bool)
	for _, diagnostic := range collector.items {
		codes[diagnostic.Code] = true
	}
	for _, code := range []string{"non_monotonic_sequence", "duplicate_sequence", "noncausal_parent", "unreachable_event"} {
		if !codes[code] {
			t.Fatalf("missing %s diagnostic: %#v", code, collector.items)
		}
	}
}

func TestRecoveryTreatsMissingParentAsCoherentSuffixRoot(t *testing.T) {
	event := messageEvent("evt_suffix", "ses_test", "suffix")
	event.Sequence = 8
	missing := protocol.EventID("evt_compacted_prefix")
	event.ParentID = &missing
	collector := newDiagnosticCollector(DefaultMaxDiagnostics)
	snapshot, err := normalizeLoadedEvents([]protocol.Event{event}, "ses_test", collector)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || !hasDiagnostic(Snapshot{Diagnostics: collector.items}, "missing_parent") {
		t.Fatalf("missing-parent suffix was not retained with evidence: %#v %#v", snapshot, collector.items)
	}
}

func TestActiveConversationSelectsLatestLeafAndRestoresResponseSiblings(t *testing.T) {
	user := messageEvent("evt_user_root", "ses_test", "prompt")
	user.Sequence = 1
	assistant := messageEvent("evt_assistant_main", "ses_test", "working")
	assistant.Sequence = 2
	assistant.Message.Role = protocol.RoleAssistant
	assistant.Message.APIResponseID = "resp_group"
	assistant.ParentID = &user.ID
	call := toolCallEvent("evt_parallel_call", "ses_test", "tool_parallel")
	call.Sequence = 3
	call.ToolCall.APIResponseID = "resp_group"
	call.ParentID = &user.ID
	result := toolResultEvent("evt_parallel_result", "ses_test", "tool_parallel")
	result.Sequence = 4
	result.ParentID = &call.ID
	final := messageEvent("evt_final_main", "ses_test", "done")
	final.Sequence = 5
	final.Timestamp = fixedTime.Add(3 * time.Second)
	final.Message.Role = protocol.RoleAssistant
	final.ParentID = &assistant.ID

	physical := Snapshot{SessionID: "ses_test", Events: []protocol.Event{user, assistant, call, result, final}, MaxSequence: 5}
	active := physical.ActiveConversation()
	want := []protocol.EventID{user.ID, assistant.ID, call.ID, result.ID, final.ID}
	if len(active.Events) != len(want) {
		t.Fatalf("active response group = %#v", active.Events)
	}
	for index, id := range want {
		if active.Events[index].ID != id {
			t.Fatalf("active event %d = %s, want %s", index, active.Events[index].ID, id)
		}
	}
}

func TestActiveConversationKeepsDerivedInterruptedResultsAfterCalls(t *testing.T) {
	user := messageEvent("evt_user", "ses_test", "prompt")
	user.Sequence = 1
	call := toolCallEvent("evt_unresolved", "ses_test", "tool_unresolved")
	call.Sequence = 2
	call.ParentID = &user.ID
	recovered := (Snapshot{SessionID: "ses_test", Events: []protocol.Event{user, call}, MaxSequence: 2}).ReconcileUnresolved().ActiveConversation()
	if len(recovered.Events) != 3 || recovered.Events[0].ID != user.ID || recovered.Events[1].ID != call.ID ||
		recovered.Events[2].ToolResult == nil || recovered.Events[2].ToolResult.ToolUseID != call.ToolCall.ID {
		t.Fatalf("derived settlement order = %#v", recovered.Events)
	}
}

func TestRecoveryOmitsFullyUnresolvedModernResponseButKeepsRawEvidence(t *testing.T) {
	user := messageEvent("evt_user_unresolved_group", "ses_test", "prompt")
	user.Sequence = 1
	assistant := messageEvent("evt_assistant_unresolved_group", "ses_test", "working")
	assistant.Sequence = 2
	assistant.Message.Role = protocol.RoleAssistant
	assistant.Message.APIResponseID = "resp_unresolved"
	assistant.ParentID = &user.ID
	call := toolCallEvent("evt_call_unresolved_group", "ses_test", "tool_unresolved_group")
	call.Sequence = 3
	call.ToolCall.APIResponseID = "resp_unresolved"
	call.ParentID = &user.ID
	provider := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_provider_unresolved_group", SessionID: "ses_test", TurnID: "turn_test",
		Sequence: 4, Timestamp: fixedTime.Add(2 * time.Second), Kind: protocol.EventKindSessionMetadata,
		Visibility: protocol.VisibilityInternal, Persistence: protocol.PersistenceDurable, Origin: protocol.OriginModel,
		Metadata: &protocol.MetadataEvent{Key: "provider_response_output", Value: json.RawMessage(`[{"type":"message","role":"assistant","api_response_id":"resp_unresolved"},{"type":"function_call","call_id":"tool_unresolved_group","api_response_id":"resp_unresolved"}]`)},
	}
	physical := Snapshot{SessionID: "ses_test", Events: []protocol.Event{user, assistant, call, provider}, MaxSequence: 4}
	recovered := physical.ActiveConversation().ReconcileUnresolved()
	if len(recovered.Events) != 1 || recovered.Events[0].ID != user.ID || !hasDiagnostic(recovered, "omitted_fully_unresolved_group") {
		t.Fatalf("fully unresolved response remained live: %#v", recovered)
	}
	if len(physical.Events) != 4 || physical.Events[2].ToolCall == nil {
		t.Fatalf("physical audit evidence was mutated: %#v", physical.Events)
	}
}

func TestRecoverySynthesizesOnlyMissingMemberOfMixedResponse(t *testing.T) {
	user := messageEvent("evt_user_mixed", "ses_test", "prompt")
	user.Sequence = 1
	first := toolCallEvent("evt_call_resolved", "ses_test", "tool_resolved")
	first.Sequence = 2
	first.ParentID = &user.ID
	first.ToolCall.APIResponseID = "resp_mixed"
	second := toolCallEvent("evt_call_missing", "ses_test", "tool_missing")
	second.Sequence = 3
	second.ParentID = &user.ID
	second.ToolCall.APIResponseID = "resp_mixed"
	result := toolResultEvent("evt_result_resolved", "ses_test", "tool_resolved")
	result.Sequence = 4
	result.ParentID = &first.ID
	recovered := (Snapshot{SessionID: "ses_test", Events: []protocol.Event{user, first, second, result}, MaxSequence: 4}).ActiveConversation().ReconcileUnresolved()
	pairs, err := recovered.ToolPairs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Result.ToolResult.Synthetic || !pairs[1].Result.ToolResult.Synthetic || pairs[1].Result.ToolResult.Status != protocol.ToolResultInterrupted {
		t.Fatalf("mixed response reconciliation = %#v", pairs)
	}
}

func TestActiveConversationChoosesLatestTerminalLeafBeforeAnchor(t *testing.T) {
	root := messageEvent("evt_leaf_root", "ses_test", "root")
	root.Sequence = 1
	root.Timestamp = fixedTime
	olderAnchor := messageEvent("evt_older_anchor", "ses_test", "older anchor")
	olderAnchor.Sequence = 2
	olderAnchor.Timestamp = fixedTime.Add(time.Second)
	olderAnchor.ParentID = &root.ID
	lateTerminal := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_late_terminal", SessionID: "ses_test", TurnID: "turn_a", Sequence: 3,
		Timestamp: fixedTime.Add(10 * time.Second), Kind: protocol.EventKindTurnResult, Visibility: protocol.VisibilityUser,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginRuntime, ParentID: &olderAnchor.ID,
		TurnResult: &protocol.TurnResult{Status: protocol.TurnResultSuccess},
	}
	newerAnchor := messageEvent("evt_newer_anchor", "ses_test", "newer anchor")
	newerAnchor.Sequence = 4
	newerAnchor.Timestamp = fixedTime.Add(5 * time.Second)
	newerAnchor.ParentID = &root.ID
	active := (Snapshot{SessionID: "ses_test", Events: []protocol.Event{root, olderAnchor, lateTerminal, newerAnchor}, MaxSequence: 4}).ActiveConversation()
	if len(active.Events) != 3 || active.Events[1].ID != olderAnchor.ID || active.Events[2].ID != lateTerminal.ID {
		t.Fatalf("terminal-leaf recency lost to anchor recency: %#v", active.Events)
	}
}

func TestActiveConversationRetainsSessionMetadataAndUsageAcrossBranches(t *testing.T) {
	root := messageEvent("evt_metadata_root", "ses_test", "root")
	root.Sequence = 1
	activeMessage := messageEvent("evt_metadata_active", "ses_test", "active")
	activeMessage.Sequence = 2
	activeMessage.Timestamp = fixedTime.Add(10 * time.Second)
	activeMessage.ParentID = &root.ID
	metadata := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_global_metadata", SessionID: "ses_test", Sequence: 3,
		Timestamp: fixedTime.Add(time.Second), Kind: protocol.EventKindSessionMetadata, Visibility: protocol.VisibilityInternal,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginRuntime, ParentID: &root.ID,
		Metadata: &protocol.MetadataEvent{Key: "title", Value: json.RawMessage(`"retained"`)},
	}
	usage := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_global_usage", SessionID: "ses_test", Sequence: 4,
		Timestamp: fixedTime.Add(2 * time.Second), Kind: protocol.EventKindUsage, Visibility: protocol.VisibilityUser,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginModel, ParentID: &metadata.ID,
		Usage: &protocol.Usage{Model: "gpt-5.6-sol", TotalTokens: 7},
	}
	active := (Snapshot{SessionID: "ses_test", Events: []protocol.Event{root, activeMessage, metadata, usage}, MaxSequence: 4}).ActiveConversation()
	want := []protocol.EventID{root.ID, activeMessage.ID, metadata.ID, usage.ID}
	if len(active.Events) != len(want) {
		t.Fatalf("session-scoped records were dropped: %#v", active.Events)
	}
	for index, id := range want {
		if active.Events[index].ID != id {
			t.Fatalf("session-scoped event %d = %s, want %s", index, active.Events[index].ID, id)
		}
	}
}

func TestActiveConversationSuppressesOlderConversationLeaf(t *testing.T) {
	root := messageEvent("evt_root", "ses_test", "root")
	root.Sequence = 1
	older := messageEvent("evt_old_branch", "ses_test", "old")
	older.Sequence = 2
	older.ParentID = &root.ID
	newer := messageEvent("evt_new_branch", "ses_test", "new")
	newer.Sequence = 3
	newer.ParentID = &root.ID
	active := (Snapshot{SessionID: "ses_test", Events: []protocol.Event{root, older, newer}, MaxSequence: 3}).ActiveConversation()
	if len(active.Events) != 2 || active.Events[0].ID != root.ID || active.Events[1].ID != newer.ID || !hasDiagnostic(active, "inactive_branch") {
		t.Fatalf("latest active leaf projection = %#v", active)
	}
}

func hasDiagnostic(snapshot Snapshot, code string) bool {
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
