package app

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/transcript"
)

func TestHeadlessTurnLoggingUsesStderrAndRespectsDebugLevel(t *testing.T) {
	lifecycleShapes := make(map[string][]string, 2)
	for _, test := range []struct {
		name  string
		debug bool
	}{
		{name: "info"},
		{name: "debug", debug: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeCommandRoutingCompletion(writer, "diagnostic-free-answer")
			}))
			defer server.Close()

			workspace := t.TempDir()
			agentxHome, _ := configureTestAgentXHome(
				t,
				server.URL,
				"gpt-5.6-sol",
				"gpt-5.6-sol",
				"synthetic-command-key",
				"2026-07-01-preview",
			)
			sessionID := "ses_logging_" + test.name
			args := []string{
				"--print", "--bare", "--session-id", sessionID,
				"--output-format", "text", "--cwd", workspace,
			}
			if test.debug {
				args = append(args, "--debug")
			}
			args = append(args, "exercise logging")

			var stdout, stderr bytes.Buffer
			if err := Run(
				testProviderContext(t, server),
				args,
				strings.NewReader(""),
				&stdout,
				&stderr,
			); err != nil {
				t.Fatalf("Run: %v; stderr=%q stdout=%q", err, stderr.String(), stdout.String())
			}
			if got, want := stdout.String(), "diagnostic-free-answer\n"; got != want {
				t.Fatalf("stdout = %q, want exact answer %q; stderr=%q", got, want, stderr.String())
			}
			for _, forbidden := range []string{
				"exercise logging",
				"diagnostic-free-answer",
				"synthetic-command-key",
			} {
				if strings.Contains(stderr.String(), forbidden) {
					t.Fatalf("stderr diagnostics exposed %q: %s", forbidden, stderr.String())
				}
			}

			snapshot, err := transcript.ReadFile(
				t.Context(),
				filepath.Join(testSessionDir(agentxHome, workspace, sessionID), "transcript.jsonl"),
				transcript.ReadOptions{ExpectedSessionID: protocol.SessionID(sessionID)},
			)
			if err != nil {
				t.Fatal(err)
			}
			lifecycleShapes[test.name] = assertDurableTurnLifecycle(t, snapshot, durableTurnExpectation{
				status:      protocol.TurnResultSuccess,
				stopReason:  "completed",
				turns:       1,
				usageEvents: 1,
				assistant:   true,
			})

			var records []map[string]any
			if strings.TrimSpace(stderr.String()) != "" {
				records = decodeNDJSONRecords(t, stderr.String())
			}
			messages := make(map[string]bool, len(records))
			sawDebugLevel := false
			lifecycleLevels := make(map[string]string, 2)
			for _, record := range records {
				message, _ := record["msg"].(string)
				messages[message] = true
				if message == "turn started" || message == "turn completed" {
					lifecycleLevels[message], _ = record["level"].(string)
				}
				if record["level"] == "debug" {
					sawDebugLevel = true
				}
			}
			if test.debug {
				if lifecycleLevels["turn started"] != "debug" ||
					lifecycleLevels["turn completed"] != "debug" ||
					!sawDebugLevel || !messages["session construction started"] ||
					!messages["model iteration started"] {
					t.Fatalf("debug diagnostics missing: messages=%v stderr=%q", messages, stderr.String())
				}
				return
			}
			if messages["turn started"] || messages["turn completed"] ||
				sawDebugLevel || messages["session construction started"] ||
				messages["model iteration started"] {
				t.Fatalf("default INFO logger emitted debug diagnostics: messages=%v stderr=%q", messages, stderr.String())
			}
		})
	}
	if got, want := strings.Join(lifecycleShapes["debug"], "\n"), strings.Join(lifecycleShapes["info"], "\n"); got != want {
		t.Fatalf("DEBUG changed durable lifecycle:\nDEBUG:\n%s\nINFO:\n%s", got, want)
	}
}

type durableTurnExpectation struct {
	status      protocol.TurnResultStatus
	stopReason  string
	turns       int
	usageEvents int
	assistant   bool
}

func assertDurableTurnLifecycle(t *testing.T, snapshot transcript.Snapshot, expectation durableTurnExpectation) []string {
	t.Helper()
	var (
		turnID        protocol.TurnID
		startedAt     = -1
		assistantAt   = -1
		usageAt       = -1
		terminalAt    = -1
		userEvents    int
		usageEvents   int
		assistantSeen bool
		terminal      *protocol.TurnResult
		normalized    = make([]string, 0, len(snapshot.Events))
	)
	for index := range snapshot.Events {
		event := &snapshot.Events[index]
		if event.Persistence != protocol.PersistenceDurable {
			t.Fatalf("transcript event %d persistence = %q, want durable", index, event.Persistence)
		}
		if event.Kind == protocol.EventKindMessage && event.Message != nil &&
			event.Message.Role == protocol.RoleUser {
			userEvents++
			turnID = event.TurnID
			startedAt = index
		}
	}
	if userEvents != 1 || turnID == "" {
		t.Fatalf("durable turn starts = %d with turn ID %q, want one", userEvents, turnID)
	}
	for index := range snapshot.Events {
		event := &snapshot.Events[index]
		if event.TurnID != turnID {
			t.Fatalf("transcript event %d turn ID = %q, want %q", index, event.TurnID, turnID)
		}
		detail := ""
		switch event.Kind {
		case protocol.EventKindUsage:
			usageEvents++
			usageAt = index
			if event.Usage != nil {
				detail = fmt.Sprintf(
					"usage=%s/%d/%d/%d/%d/%d",
					event.Usage.Model,
					event.Usage.InputTokens,
					event.Usage.CachedInputTokens,
					event.Usage.OutputTokens,
					event.Usage.ReasoningTokens,
					event.Usage.TotalTokens,
				)
			}
		case protocol.EventKindMessage:
			if event.Message != nil {
				detail = "role=" + string(event.Message.Role)
				if event.Message.Role == protocol.RoleAssistant {
					assistantSeen = true
					assistantAt = index
				}
			}
		case protocol.EventKindTurnResult:
			if terminal != nil {
				t.Fatalf("turn %q has more than one terminal result", turnID)
			}
			terminal = event.TurnResult
			terminalAt = index
			if terminal != nil {
				detail = fmt.Sprintf(
					"result=%s/%t/%s/%d",
					terminal.Status,
					terminal.IsError,
					terminal.StopReason,
					terminal.Turns,
				)
			}
		}
		normalized = append(normalized, fmt.Sprintf(
			"%s|%s|%s|%s|%s",
			event.Kind,
			event.Persistence,
			event.Visibility,
			event.Origin,
			detail,
		))
	}
	if usageEvents != expectation.usageEvents || assistantSeen != expectation.assistant {
		t.Fatalf(
			"turn %q durable usage=%d assistant=%t, want %d/%t",
			turnID,
			usageEvents,
			assistantSeen,
			expectation.usageEvents,
			expectation.assistant,
		)
	}
	if terminal == nil || terminalAt <= startedAt || terminalAt <= assistantAt || terminalAt <= usageAt ||
		terminalAt != len(snapshot.Events)-1 {
		t.Fatalf(
			"turn %q terminal index=%d start=%d assistant=%d usage=%d events=%d terminal=%#v",
			turnID,
			terminalAt,
			startedAt,
			assistantAt,
			usageAt,
			len(snapshot.Events),
			terminal,
		)
	}
	if terminal.Status != expectation.status ||
		terminal.IsError != (expectation.status != protocol.TurnResultSuccess) ||
		terminal.StopReason != expectation.stopReason || terminal.Turns != expectation.turns ||
		terminal.DurationMillis < 0 {
		t.Fatalf("turn %q terminal result = %#v", turnID, terminal)
	}
	return normalized
}

func TestHeadlessTerminalFailureLoggingRetainsErrorAndDurableResult(t *testing.T) {
	lifecycleShapes := make(map[string][]string, 2)
	for _, test := range []struct {
		name  string
		debug bool
	}{
		{name: "info"},
		{name: "debug", debug: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("apim-request-id", "req_terminal_log")
				writer.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(writer, `{"error":{"code":"BadRequest","message":"unsupported api version"}}`)
			}))
			defer server.Close()

			workspace := t.TempDir()
			agentxHome, _ := configureTestAgentXHome(
				t,
				server.URL,
				"gpt-5.6-sol",
				"gpt-5.6-sol",
				"synthetic-command-key",
				"2026-07-01-preview",
			)
			sessionID := "ses_failure_logging_" + test.name
			args := []string{
				"--print", "--bare", "--session-id", sessionID,
				"--output-format", "text", "--cwd", workspace,
			}
			if test.debug {
				args = append(args, "--debug")
			}
			args = append(args, "terminal failure prompt")

			var stdout, stderr bytes.Buffer
			err := Run(
				testProviderContext(t, server),
				args,
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			if err == nil || err.Error() != "operation failed" {
				t.Fatalf("Run error = %v, want operation failed; stderr=%q stdout=%q", err, stderr.String(), stdout.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			for _, forbidden := range []string{
				"terminal failure prompt",
				"synthetic-command-key",
			} {
				if strings.Contains(stderr.String(), forbidden) {
					t.Fatalf("stderr diagnostics exposed %q: %s", forbidden, stderr.String())
				}
			}

			snapshot, readErr := transcript.ReadFile(
				t.Context(),
				filepath.Join(testSessionDir(agentxHome, workspace, sessionID), "transcript.jsonl"),
				transcript.ReadOptions{ExpectedSessionID: protocol.SessionID(sessionID)},
			)
			if readErr != nil {
				t.Fatal(readErr)
			}
			lifecycleShapes[test.name] = assertDurableTurnLifecycle(t, snapshot, durableTurnExpectation{
				status:     protocol.TurnResultError,
				stopReason: "provider_error",
				turns:      1,
			})

			records := decodeNDJSONRecords(t, stderr.String())
			var (
				startedCount   int
				completedCount int
				failedRecords  []map[string]any
			)
			for _, record := range records {
				switch record["msg"] {
				case "turn started":
					startedCount++
					if record["level"] != "debug" {
						t.Fatalf("turn start level = %v, want debug", record["level"])
					}
				case "turn completed":
					completedCount++
				case "turn failed":
					failedRecords = append(failedRecords, record)
				}
			}
			if completedCount != 0 || len(failedRecords) != 1 {
				t.Fatalf(
					"terminal logs: started=%d completed=%d failed=%d; stderr=%q",
					startedCount,
					completedCount,
					len(failedRecords),
					stderr.String(),
				)
			}
			failed := failedRecords[0]
			failureMessage, _ := failed["error_message"].(string)
			if failed["level"] != "error" ||
				failed["session_id"] != sessionID ||
				failed["model"] != "gpt-5.6-sol" ||
				!strings.Contains(failureMessage, "unsupported api version") {
				t.Fatalf("terminal failure diagnostic = %#v", failed)
			}
			if test.debug && startedCount != 1 {
				t.Fatalf("DEBUG turn starts = %d, want 1; stderr=%q", startedCount, stderr.String())
			}
			if !test.debug && startedCount != 0 {
				t.Fatalf("INFO turn starts = %d, want 0; stderr=%q", startedCount, stderr.String())
			}
		})
	}
	if got, want := strings.Join(lifecycleShapes["debug"], "\n"), strings.Join(lifecycleShapes["info"], "\n"); got != want {
		t.Fatalf("DEBUG changed failed durable lifecycle:\nDEBUG:\n%s\nINFO:\n%s", got, want)
	}
}

func TestHeadlessRetryLoggingRetainsSafeFailureDetail(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Retry-After", "0")
			writer.Header().Set("apim-request-id", "req_retry_log")
			writer.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(writer, `{"error":{"code":"server_error","message":"temporary capacity"}}`)
			return
		}
		writeCommandRoutingCompletion(writer, "recovered-answer")
	}))
	defer server.Close()

	workspace := configureCommandRoutingRuntime(t, server.URL)
	var stdout, stderr bytes.Buffer
	err := Run(
		testProviderContext(t, server),
		[]string{
			"--print", "--bare", "--no-session-persistence",
			"--output-format", "text", "--cwd", workspace,
			"exercise retry logging",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("Run: %v; stderr=%q stdout=%q", err, stderr.String(), stdout.String())
	}
	if requests.Load() != 2 || stdout.String() != "recovered-answer\n" {
		t.Fatalf("requests=%d stdout=%q stderr=%q", requests.Load(), stdout.String(), stderr.String())
	}

	records := decodeNDJSONRecords(t, stderr.String())
	var retry map[string]any
	for _, record := range records {
		if record["msg"] == "model request retry" {
			retry = record
			break
		}
	}
	if retry == nil {
		t.Fatalf("retry diagnostic missing: %s", stderr.String())
	}
	if retry["level"] != "warn" || retry["attempt"] != float64(2) ||
		retry["model"] != "gpt-5.6-sol" || retry["session_id"] == "" ||
		!strings.Contains(retry["reason"].(string), "temporary capacity") {
		t.Fatalf("retry diagnostic = %#v", retry)
	}
	for _, forbidden := range []string{"exercise retry logging", "recovered-answer", "synthetic-command-key"} {
		if strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("retry diagnostics exposed %q: %s", forbidden, stderr.String())
		}
	}
}
