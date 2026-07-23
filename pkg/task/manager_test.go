package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/redact"
)

type panickingOutputSanitizer struct {
	panicOnWrite bool
	panicOnFlush bool
	pending      string
}

type reentrantOutputSanitizer struct {
	probe func(string)
}

func (s *reentrantOutputSanitizer) Write(value string) string {
	s.probe("write")
	return value
}

func (s *reentrantOutputSanitizer) Flush() string {
	s.probe("flush")
	return ""
}

func (s *reentrantOutputSanitizer) TruncationMarker() string {
	s.probe("marker")
	return ""
}

type panickingRandomReader struct{}

func (panickingRandomReader) Read([]byte) (int, error) {
	panic("random-reader-panic-payload")
}

type taskReaderFunc func([]byte) (int, error)

func (read taskReaderFunc) Read(target []byte) (int, error) {
	return read(target)
}

type blockingTaskUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingTaskUnwrapError) Error() string { return "foreign task callback failure" }
func (err *blockingTaskUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.Canceled
}

func (s *panickingOutputSanitizer) Write(value string) string {
	if s.panicOnWrite {
		panic("unsafe write panic payload: " + value)
	}
	s.pending += value
	return ""
}

func (s *panickingOutputSanitizer) Flush() string {
	if s.panicOnFlush {
		panic("unsafe flush panic payload: " + s.pending)
	}
	value := s.pending
	s.pending = ""
	return value
}

func (s *panickingOutputSanitizer) TruncationMarker() string { return "" }

func TestOutputSanitizerFactoryFailureLeavesNoTaskArtifact(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const panicPayload = "factory-panic-payload-must-not-escape"
	tests := []struct {
		name    string
		factory func() OutputSanitizer
	}{
		{
			name: "panic",
			factory: func() OutputSanitizer {
				panic(panicPayload)
			},
		},
		{name: "nil", factory: func() OutputSanitizer { return nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manager, err := Open(root, Options{NewOutputSanitizer: test.factory})
			if err != nil {
				t.Fatal(err)
			}
			_, err = manager.LaunchShell(t.Context(), ShellSpec{
				Command: "true", Dir: t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
			})
			if err == nil || strings.Contains(err.Error(), panicPayload) {
				_ = manager.Close()
				t.Fatalf("factory failure = %v", err)
			}
			if records := manager.List(); len(records) != 0 {
				_ = manager.Close()
				t.Fatalf("factory failure created task state: %+v", records)
			}
			entries, readErr := os.ReadDir(manager.outputDir)
			if readErr != nil {
				_ = manager.Close()
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				_ = manager.Close()
				t.Fatalf("factory failure created output artifacts: %+v", entries)
			}
			if _, statErr := os.Stat(manager.statePath); !errors.Is(statErr, os.ErrNotExist) {
				_ = manager.Close()
				t.Fatalf("factory failure created state journal: %v", statErr)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOutputSanitizerPanicsFailClosedWithoutChangingProcessResult(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const secret = "processing-panic-credential-must-not-persist"
	for _, test := range []struct {
		name         string
		panicOnWrite bool
		panicOnFlush bool
	}{
		{name: "write", panicOnWrite: true},
		{name: "flush", panicOnFlush: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			manager, err := Open(root, Options{NewOutputSanitizer: func() OutputSanitizer {
				return &panickingOutputSanitizer{
					panicOnWrite: test.panicOnWrite,
					panicOnFlush: test.panicOnFlush,
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			record, err := manager.LaunchShell(t.Context(), ShellSpec{
				Command: `printf '%s' "$TERM"; printf '%s' "$COLORTERM"; /bin/sleep 0.1`,
				Dir:     t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
				Env: []string{
					"PATH=/usr/bin:/bin",
					"TERM=processing-panic-credential-",
					"COLORTERM=must-not-persist",
				},
			})
			if err != nil {
				_ = manager.Close()
				t.Fatal(err)
			}
			manager.mu.RLock()
			live := manager.live[record.ID]
			manager.mu.RUnlock()
			if live == nil {
				_ = manager.Close()
				t.Fatal("live task disappeared before failure observation")
			}
			select {
			case <-live.done:
			case <-time.After(5 * time.Second):
				_ = manager.Close()
				t.Fatal("task did not terminate")
			}
			terminal, err := manager.Get(record.ID)
			if err != nil {
				_ = manager.Close()
				t.Fatal(err)
			}
			if terminal.Status != StatusCompleted || terminal.ExitCode == nil || *terminal.ExitCode != 0 || terminal.Error != "" || !terminal.OutputIncomplete || terminal.OutputWarning == "" {
				_ = manager.Close()
				t.Fatalf("inspectability failure changed process result: %+v", terminal)
			}
			if live.terminalErr == nil {
				_ = manager.Close()
				t.Fatal("sanitizer failure lacked a terminal inspectability diagnostic")
			}
			diagnostic := live.terminalErr.Error()
			if strings.Contains(diagnostic, secret) || strings.Contains(diagnostic, "unsafe") || strings.Contains(diagnostic, "payload") {
				_ = manager.Close()
				t.Fatalf("sanitizer diagnostic exposed panic content: %q", diagnostic)
			}
			data, err := os.ReadFile(record.OutputPath)
			if err != nil {
				_ = manager.Close()
				t.Fatal(err)
			}
			if strings.Contains(string(data), secret) {
				_ = manager.Close()
				t.Fatalf("sanitizer panic persisted raw credential: %q", data)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(root, Options{})
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := reopened.Get(record.ID)
			if err != nil {
				_ = reopened.Close()
				t.Fatal(err)
			}
			if !recovered.OutputIncomplete || recovered.OutputWarning == "" || recovered.Status != StatusCompleted || recovered.ExitCode == nil || *recovered.ExitCode != 0 || recovered.Error != "" {
				_ = reopened.Close()
				t.Fatalf("recovered inspectability state = %+v", recovered)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskWriterSanitizesCredentialAcrossEveryWriteBoundary(t *testing.T) {
	const (
		secret = "credential-boundary-must-not-persist"
		input  = "before " + secret + " after"
		want   = "before [REDACTED] after"
	)
	for split := 0; split <= len(input); split++ {
		t.Run(fmt.Sprintf("split_%d", split), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "task-output-")
			if err != nil {
				t.Fatal(err)
			}
			id := ID("b00000000")
			manager := &Manager{
				outputCap: defaultOutputCap,
				tasks:     map[ID]Record{id: {ID: id}},
			}
			live := &liveTask{
				output: file, outputCap: defaultOutputCap,
				sanitizer: redact.NewLiteralStream(secret),
			}
			writer := &taskWriter{manager: manager, id: id, live: live}
			if n, err := writer.Write([]byte(input[:split])); err != nil || n != split {
				t.Fatalf("first write = %d, %v", n, err)
			}
			if n, err := writer.Write([]byte(input[split:])); err != nil || n != len(input)-split {
				t.Fatalf("second write = %d, %v", n, err)
			}
			if err := manager.closeTaskOutput(live); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(file.Name())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want || strings.Contains(string(got), secret) {
				t.Fatalf("durable output = %q, want %q", got, want)
			}
		})
	}
}

func TestTaskWriterTruncationCannotReconstructCredentialAtAnyCap(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		input  string
	}{
		{name: "conventional mask", secret: "ordinary-production-credential", input: "before ordinary-production-credential after"},
		{name: "fallback mask", secret: "R", input: "before R after"},
		{name: "historical marker", secret: "[REDACTED]", input: "before [REDACTED] after"},
		{name: "reviewer counterexample", secret: "a*\n", input: "aa*\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			safe := redact.Literal(test.input, test.secret)
			for cap := 0; cap < len(safe); cap++ {
				t.Run(fmt.Sprintf("cap_%d", cap), func(t *testing.T) {
					file, err := os.CreateTemp(t.TempDir(), "task-output-")
					if err != nil {
						t.Fatal(err)
					}
					id := ID("b00000000")
					stream := redact.NewLiteralStream(test.secret)
					manager := &Manager{
						outputCap: int64(cap),
						tasks:     map[ID]Record{id: {ID: id}},
					}
					live := &liveTask{
						output: file, outputCap: int64(cap), sanitizer: stream,
						truncMarker: stream.TruncationMarker(),
					}
					writer := &taskWriter{manager: manager, id: id, live: live}
					if n, err := writer.Write([]byte(test.input)); err != nil || n != len(test.input) {
						t.Fatalf("write = %d, %v", n, err)
					}
					if err := manager.closeTaskOutput(live); err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(file.Name())
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(got), test.secret) {
						t.Fatalf("cap %d reconstructed %q in %q", cap, test.secret, got)
					}
					if len(got) > cap+len(outputTruncMarker) {
						t.Fatalf("cap %d wrote %d bytes", cap, len(got))
					}
				})
			}
		})
	}
}

func TestShellTaskSanitizesCredentialBeforePersistenceAndRecovery(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const (
		secret = "background-credential-must-not-persist"
		want   = "before[REDACTED]after"
	)
	root := t.TempDir()
	options := Options{NewOutputSanitizer: func() OutputSanitizer {
		return redact.NewLiteralStream(secret)
	}}
	manager, err := Open(root, options)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: `printf before; printf '%s' "$TERM"; /bin/sleep 0.05; printf '%s' "$COLORTERM"; printf after`,
		Dir:     t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
		Env: []string{
			"PATH=/usr/bin:/bin",
			"TERM=background-credential-",
			"COLORTERM=must-not-persist",
		},
	})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	var result PollResult
	var output strings.Builder
	pollCtx, cancelPoll := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelPoll()
	for successfulPolls := 0; successfulPolls < 10; {
		result, err = manager.Poll(pollCtx, record.ID, result.NextOffset, true, 5*time.Second)
		if err == ErrBusy {
			select {
			case <-pollCtx.Done():
				_ = manager.Close()
				t.Fatal(pollCtx.Err())
			case <-time.After(time.Millisecond):
				continue
			}
		}
		if err != nil {
			_ = manager.Close()
			t.Fatal(err)
		}
		successfulPolls++
		output.WriteString(result.Output)
		if result.Task.Status.Terminal() {
			break
		}
	}
	if result.Task.Status != StatusCompleted || output.String() != want {
		_ = manager.Close()
		t.Fatalf("terminal task = %+v output=%q", result.Task, output.String())
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{record.OutputPath, filepath.Join(root, stateFilename)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("credential survived in durable task artifact %s", filepath.Base(path))
		}
	}

	restored, err := Open(root, options)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered, err := restored.Poll(t.Context(), record.ID, 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Task.Status != StatusCompleted || recovered.Output != want || strings.Contains(recovered.Output, secret) ||
		recovered.Task.OutputIncomplete || recovered.Task.OutputWarning != "" {
		t.Fatalf("recovered task = %+v", recovered)
	}
}

func TestShellLifecycleAndOutputDelta(t *testing.T) {
	t.Parallel()
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "printf first; printf second", Description: "test shell", Dir: t.TempDir(),
		Env: os.Environ(), Shell: "/bin/bash", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Poll(context.Background(), record.ID, 0, true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	output := result.Output
	for !result.Task.Status.Terminal() {
		result, err = manager.Poll(context.Background(), record.ID, result.NextOffset, true, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		output += result.Output
	}
	if result.Task.Status != StatusCompleted || output != "firstsecond" || result.NextOffset != int64(len(output)) {
		t.Fatalf("unexpected poll: %+v output=%q", result, output)
	}
	next, err := manager.Poll(context.Background(), record.ID, result.NextOffset, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Output != "" || next.NextOffset != result.NextOffset {
		t.Fatalf("unexpected second delta: %+v", next)
	}
}

func TestDurableOutputBoundsMatchTaskProtocol(t *testing.T) {
	if defaultOutputCap != MaximumOutputBytes || maximumOutputCap != MaximumOutputBytes || MaximumOutputBytes != int64(5<<30) {
		t.Fatalf("task output caps = default %d maximum %d, want 5 GiB", defaultOutputCap, maximumOutputCap)
	}
	if defaultReadLimit != DefaultOutputReadBytes || DefaultOutputReadBytes != 8<<20 {
		t.Fatalf("task output read limit = %d, want 8 MiB", defaultReadLimit)
	}
	if MaximumOutputFileBytes != MaximumOutputBytes+int64(len(outputTruncMarker)) {
		t.Fatalf("maximum task output file bytes = %d", MaximumOutputFileBytes)
	}
	manager, err := Open(t.TempDir(), Options{OutputCap: int64(5 << 30)})
	if err != nil {
		t.Fatalf("5 GiB task output cap was rejected: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), Options{OutputCap: int64(5<<30) + 1}); err == nil {
		t.Fatal("task output cap above 5 GiB was accepted")
	}
}

func TestRecordSnapshotsDoNotAliasTerminalState(t *testing.T) {
	exitCode := 0
	endedAt := time.Now().UTC()
	original := Record{ExitCode: &exitCode, EndedAt: &endedAt}
	snapshot := cloneRecord(original)
	*snapshot.ExitCode = 99
	*snapshot.EndedAt = snapshot.EndedAt.Add(time.Hour)
	if *original.ExitCode != 0 || !original.EndedAt.Equal(endedAt) {
		t.Fatalf("record snapshot mutated authoritative state: original=%+v snapshot=%+v", original, snapshot)
	}
}

func TestLaunchShellCommandFactoryPreservesTaskProcessContract(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	workingDirectory := t.TempDir()
	called := 0
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: "printf '%s|%s' \"$PWD\" \"$NO_COLOR\"", Description: "factory shell",
		Dir: workingDirectory, Env: []string{"PATH=/bin:/usr/bin", "NO_COLOR=factory-env"},
		Shell: "/bin/bash", Timeout: 5 * time.Second,
		CommandFactory: func(ctx context.Context, program string, arguments ...string) *exec.Cmd {
			called++
			if program != "/bin/bash" {
				t.Fatalf("factory program = %q", program)
			}
			if _, deadline := ctx.Deadline(); !deadline {
				t.Fatal("factory context did not retain task timeout")
			}
			want, _ := ShellArguments(program, "printf '%s|%s' \"$PWD\" \"$NO_COLOR\"")
			if strings.Join(arguments, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("factory arguments = %q, want %q", arguments, want)
			}
			return exec.CommandContext(ctx, program, arguments...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result PollResult
	var output strings.Builder
	for attempts := 0; attempts < 3; attempts++ {
		result, err = manager.Poll(t.Context(), record.ID, result.NextOffset, true, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		output.WriteString(result.Output)
		if result.Task.Status.Terminal() {
			break
		}
	}
	actualDirectory, actualEnvironment, separated := strings.Cut(output.String(), "|")
	wantInfo, wantErr := os.Stat(workingDirectory)
	actualInfo, actualErr := os.Stat(actualDirectory)
	if called != 1 || result.Task.Status != StatusCompleted || !separated || actualEnvironment != "factory-env" || wantErr != nil || actualErr != nil || !os.SameFile(wantInfo, actualInfo) {
		t.Fatalf("factory task = called %d output %q result %+v", called, output.String(), result)
	}
}

func TestLaunchShellContainsCommandFactoryPanicAndKeepsManagerUsable(t *testing.T) {
	const secret = "factory-panic-credential"
	root := t.TempDir()
	manager, err := Open(root, Options{SanitizeRecord: redact.New(secret).Redact})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	_, err = manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Description: "panic factory", Dir: t.TempDir(), Shell: "/bin/bash",
		CommandFactory: func(context.Context, string, ...string) *exec.Cmd {
			panic("factory failure " + secret)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "factory panicked") || strings.Contains(err.Error(), secret) {
		t.Fatalf("factory panic result = %v", err)
	}
	records := manager.List()
	if len(records) != 1 || records[0].Status != StatusFailed || records[0].EndedAt == nil ||
		strings.Contains(records[0].Error, secret) || records[0].Error == "" {
		t.Fatalf("factory panic lacked terminal evidence: %+v", records)
	}
	state, readErr := os.ReadFile(filepath.Join(root, stateFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(state), secret) {
		t.Fatal("factory panic credential persisted in task state")
	}
	launched, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Description: "after panic", Dir: t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("manager remained poisoned after factory panic: %v", err)
	}
	result, err := manager.Poll(t.Context(), launched.ID, 0, true, 5*time.Second)
	if err != nil || result.Task.Status != StatusCompleted {
		t.Fatalf("post-panic task = %+v, %v", result, err)
	}
}

func TestLaunchShellSanitizesCredentialBearingProcessStartErrorsForDirectCallers(t *testing.T) {
	const secret = "synthetic-task-start-path-credential"
	set := redact.New(secret)
	root := t.TempDir()
	manager, err := Open(root, Options{SanitizeRecord: set.Redact})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	missing := filepath.Join(t.TempDir(), secret)
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Description: "safe start failure", Dir: t.TempDir(), Shell: "/bin/bash",
		CommandFactory: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, missing)
		},
	})
	if err == nil {
		t.Fatal("missing credential-bearing command unexpectedly started")
	}
	for _, value := range []string{
		err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
		record.Error, fmt.Sprintf("%+v", record),
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("task start failure exposed credential-bearing process path: %q", value)
		}
	}
	state, readErr := os.ReadFile(filepath.Join(root, stateFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(state), secret) {
		t.Fatal("task start failure persisted credential-bearing process path")
	}
}

func TestTerminalValidationFailureSuppressesRecordBeforePublicState(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, Options{
		ValidateState: func(raw []byte) error {
			if bytes.Contains(raw, []byte(`"status": "failed"`)) &&
				bytes.Contains(raw, []byte(`"error":`)) {
				return errors.New("terminal payload rejected")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	_, err = manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Description: "terminal preflight", ToolUseID: "tool-call-123",
		Owner: "agent-123", Dir: t.TempDir(), Shell: "/bin/bash",
		CommandFactory: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing"))
		},
	})
	if err == nil {
		t.Fatal("missing command unexpectedly started")
	}
	records := manager.List()
	if len(records) != 1 || records[0].Status != StatusFailed || records[0].Error != "" ||
		records[0].OutputWarning != "" || records[0].ExitCode != nil ||
		records[0].ToolUseID != "tool-call-123" || records[0].Owner != "agent-123" {
		t.Fatalf("rejected terminal payload remained public: %#v", records)
	}
	state, readErr := os.ReadFile(filepath.Join(root, stateFilename))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if bytes.Contains(state, []byte(`"error":`)) {
		t.Fatalf("rejected terminal payload reached journal: %s", state)
	}
}

func TestOpenRejectsExistingStateThatFailsCurrentSafetyValidator(t *testing.T) {
	const secret = "legacy-task-state-validator-credential"
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWork("safe", "safe", "safe", nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, Options{ValidateState: func([]byte) error {
		return errors.New("validator rejected " + secret)
	}})
	if err == nil || reopened != nil {
		t.Fatalf("unsafe legacy task state was published: manager=%#v err=%v", reopened, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%#v", err), secret) {
		t.Fatalf("legacy state validation error exposed validator payload: %v", err)
	}
}

func TestValidateStatePanicFailsClosedAtEveryStateBoundary(t *testing.T) {
	const panicPayload = "task-state-validator-panic-payload"
	t.Run("new mutation", func(t *testing.T) {
		root := t.TempDir()
		manager, err := Open(root, Options{ValidateState: func([]byte) error {
			panic(panicPayload)
		}})
		if err != nil {
			t.Fatal(err)
		}
		var factoryCalls int
		_, err = manager.LaunchShell(t.Context(), ShellSpec{
			Command: "true", Description: "validator panic", Dir: t.TempDir(), Shell: "/bin/bash",
			CommandFactory: func(context.Context, string, ...string) *exec.Cmd {
				factoryCalls++
				return exec.Command("true")
			},
		})
		if err == nil || strings.Contains(err.Error(), panicPayload) {
			_ = manager.Close()
			t.Fatalf("validator panic = %v", err)
		}
		if factoryCalls != 0 {
			_ = manager.Close()
			t.Fatalf("validator panic reached process construction %d times", factoryCalls)
		}
		if records := manager.List(); len(records) != 0 {
			_ = manager.Close()
			t.Fatalf("validator panic published state: %#v", records)
		}
		if entries, readErr := os.ReadDir(manager.outputDir); readErr != nil || len(entries) != 0 {
			_ = manager.Close()
			t.Fatalf("validator panic left output artifacts: entries=%#v err=%v", entries, readErr)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("existing journal", func(t *testing.T) {
		root := t.TempDir()
		manager, err := Open(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateWork("safe", "safe", "safe", nil); err != nil {
			t.Fatal(err)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(root, Options{ValidateState: func([]byte) error {
			panic(panicPayload)
		}})
		if err == nil || reopened != nil || strings.Contains(err.Error(), panicPayload) {
			t.Fatalf("existing journal validator panic: manager=%#v err=%v", reopened, err)
		}
	})
}

func TestValidateStateCannotMutatePersistedEncoding(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, Options{ValidateState: func(encoded []byte) error {
		for index := range encoded {
			encoded[index] = 'x'
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWork("safe", "safe", "safe", nil); err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, Options{})
	if err != nil {
		t.Fatalf("validator mutated persisted state: %v", err)
	}
	defer reopened.Close()
	if len(reopened.ListWork()) != 1 {
		t.Fatalf("validator mutation changed persisted work: %#v", reopened.ListWork())
	}
}

func TestOpenRejectsLegacyOutputChangedByCurrentSanitizer(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	const secret = "legacy-task-output-credential"
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: `printf '%s' "$LANG"`, Description: "legacy output",
		Dir: t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
		Env: []string{"PATH=/usr/bin:/bin", "LANG=" + secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Poll(t.Context(), record.ID, 0, true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for !result.Task.Status.Terminal() {
		result, err = manager.Poll(t.Context(), record.ID, result.NextOffset, true, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(record.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatalf("legacy fixture did not contain credential: %q", raw)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	set := redact.New(secret)
	reopened, err := Open(root, Options{
		SanitizeRecord: set.Redact,
		ValidateState: func(raw []byte) error {
			reflected, inspectErr := set.JSONContains(raw)
			if inspectErr != nil || reflected {
				return errors.New("state reflected credential")
			}
			return nil
		},
		NewOutputSanitizer: func() OutputSanitizer { return redact.NewSetStream(set) },
	})
	if err == nil || reopened != nil {
		t.Fatalf("unsafe legacy output was published: manager=%#v err=%v", reopened, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("legacy output validation error exposed credential: %v", err)
	}
}

func TestTaskStateRejectsCredentialReconstructedByFinalJSONFraming(t *testing.T) {
	const secret = `foo"`
	set := redact.New(secret)
	var factoryCalls int
	manager, err := Open(t.TempDir(), Options{
		SanitizeRecord: set.Redact,
		ValidateState: func(raw []byte) error {
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
	defer manager.Close()
	_, err = manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Description: "foo", Dir: t.TempDir(), Shell: "/bin/bash",
		CommandFactory: func(context.Context, string, ...string) *exec.Cmd {
			factoryCalls++
			return exec.Command("true")
		},
	})
	if err == nil {
		t.Fatal("task with unsafe final state encoding was launched")
	}
	if factoryCalls != 0 {
		t.Fatalf("unsafe task reached process construction %d times", factoryCalls)
	}
	state, readErr := os.ReadFile(filepath.Join(manager.root, stateFilename))
	if readErr == nil && strings.Contains(string(state), secret) {
		t.Fatal("framing-reconstructed credential reached task state")
	}
}

func TestDirectShellTaskCannotInheritModelCredentialOrRenamedAlias(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	const secret = "synthetic-model-credential"
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: `printf '%s|%s' "$AZURE_OPENAI_SUBSCRIPTION_KEY" "$LANG"`,
		Dir:     t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
		Env: []string{
			"PATH=/usr/bin:/bin",
			"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret,
			"LANG=" + secret,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Poll(t.Context(), record.ID, 0, true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	output := result.Output
	for !result.Task.Status.Terminal() {
		result, err = manager.Poll(t.Context(), record.ID, result.NextOffset, true, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		output += result.Output
	}
	if result.Task.Status != StatusCompleted || output != "|" || strings.Contains(output, secret) {
		t.Fatalf("credential-safe direct task = %#v output=%q", result, output)
	}
}

func TestShellArgumentsSuppressStartupFiles(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{shell: "/bin/bash", want: "--noprofile --norc -c echo ok"},
		{shell: "/bin/zsh", want: "-f -c echo ok"},
		{shell: "/bin/sh", want: "-c echo ok"},
	}
	for _, test := range tests {
		args, err := ShellArguments(test.shell, "echo ok")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(args, " ") != test.want {
			t.Fatalf("ShellArguments(%q) = %q", test.shell, args)
		}
	}
	if _, err := ShellArguments("/tmp/custom-shell", "echo ok"); err == nil {
		t.Fatal("custom shell unexpectedly accepted")
	}
}

func TestStopWinsCompletionRace(t *testing.T) {
	t.Parallel()
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "sleep 30", Dir: t.TempDir(), Env: os.Environ(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(record.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	current, err := manager.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusKilled {
		t.Fatalf("status resurrected after kill: %+v", current)
	}
	if err := manager.Stop(record.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second stop = %v, want ErrNotRunning", err)
	}
}

func TestOutputCreationRejectsPreexistingSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := Open(root, Options{Random: bytes.NewReader(make([]byte, 8))})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, outputDirname, "b00000000.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = manager.LaunchShell(context.Background(), ShellSpec{Command: "echo bad", Dir: root, Env: os.Environ(), Shell: "/bin/bash"})
	if err == nil {
		t.Fatal("expected symlink output rejection")
	}
	b, readErr := os.ReadFile(target)
	if readErr != nil || string(b) != "safe" {
		t.Fatalf("symlink target changed: %q, %v", b, readErr)
	}
}

func TestWorkCRUDRelationshipsAndCycle(t *testing.T) {
	t.Parallel()
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	one, err := manager.CreateWork("one", "first", "doing one", map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := manager.CreateWork("two", "second", "doing two", nil)
	if err != nil {
		t.Fatal(err)
	}
	blockers := []ID{one.ID}
	status := WorkInProgress
	updated, err := manager.UpdateWork(two.ID, WorkPatch{Status: &status, Blockers: &blockers})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != WorkInProgress || len(updated.Blockers) != 1 {
		t.Fatalf("unexpected update: %+v", updated)
	}
	oneCurrent, _ := manager.GetWork(one.ID)
	if len(oneCurrent.Dependents) != 1 || oneCurrent.Dependents[0] != two.ID {
		t.Fatalf("dependent edge missing: %+v", oneCurrent)
	}
	reverse := []ID{two.ID}
	if _, err := manager.UpdateWork(one.ID, WorkPatch{Blockers: &reverse}); !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("cycle update = %v, want ErrDependencyCycle", err)
	}
	items := manager.ListWork()
	if len(items) != 2 || items[0].ID != one.ID || items[1].ID != two.ID {
		t.Fatalf("nondeterministic list: %+v", items)
	}
	if err := manager.ReplaceTodos([]Todo{{Content: "done", ActiveForm: "doing", Status: WorkCompleted}}); err != nil {
		t.Fatal(err)
	}
	if todos := manager.Todos(); len(todos) != 0 {
		t.Fatalf("completed todos were retained: %+v", todos)
	}
}

func TestCheckedSnapshotsHonorContextWithoutPartialResults(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	item, err := manager.CreateWork("one", "first", "doing one", map[string]string{"source": "test"})
	if err != nil {
		t.Fatal(err)
	}

	type snapshotResult struct {
		count int
		err   error
	}
	tests := []struct {
		name string
		call func(context.Context) (int, error)
	}{
		{
			name: "tasks",
			call: func(ctx context.Context) (int, error) {
				items, err := manager.ListContext(ctx)
				return len(items), err
			},
		},
		{
			name: "work",
			call: func(ctx context.Context) (int, error) {
				items, err := manager.ListWorkContext(ctx)
				return len(items), err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager.mu.Lock()
			ctx, cancel := context.WithCancel(t.Context())
			started := make(chan struct{})
			done := make(chan snapshotResult, 1)
			go func() {
				close(started)
				count, callErr := test.call(ctx)
				done <- snapshotResult{count: count, err: callErr}
			}()
			<-started
			time.Sleep(5 * time.Millisecond)
			cancel()

			var result snapshotResult
			var timedOut bool
			select {
			case result = <-done:
			case <-time.After(time.Second):
				timedOut = true
			}
			manager.mu.Unlock()
			if timedOut {
				t.Fatal("cancelled snapshot waited for task-state lock release")
			}
			if result.count != 0 || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("cancelled snapshot = count %d, error %v", result.count, result.err)
			}
		})
	}

	if items, err := manager.ListContext(nil); err == nil || items != nil {
		t.Fatalf("nil-context task snapshot = %#v, %v", items, err)
	}
	if items, err := manager.ListWorkContext(nil); err == nil || items != nil {
		t.Fatalf("nil-context work snapshot = %#v, %v", items, err)
	}
	tasks, err := manager.ListContext(t.Context())
	if err != nil || len(tasks) != len(manager.List()) {
		t.Fatalf("checked task snapshot = %#v, %v", tasks, err)
	}
	work, err := manager.ListWorkContext(t.Context())
	if err != nil || len(work) != 1 || work[0].ID != item.ID || len(manager.ListWork()) != 1 {
		t.Fatalf("checked work snapshot = %#v, %v", work, err)
	}
	work[0].Metadata["source"] = "mutated"
	current, err := manager.GetWork(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Metadata["source"] != "test" {
		t.Fatalf("checked snapshot aliased live metadata: %#v", current.Metadata)
	}
}

func TestCheckedSnapshotsRecoverDeterministicClones(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	var clockMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	manager, err := Open(root, Options{Clock: func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(time.Second)
		return now
	}})
	if err != nil {
		t.Fatal(err)
	}
	one, err := manager.CreateWork("one", "first", "doing one", map[string]string{"source": "test"})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	two, err := manager.CreateWork("two", "second", "doing two", nil)
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: "true", Dir: t.TempDir(), Shell: "/bin/bash", Timeout: 5 * time.Second,
	})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	result, err := manager.Poll(t.Context(), record.ID, 0, true, 5*time.Second)
	if err != nil || result.Task.Status != StatusCompleted {
		_ = manager.Close()
		t.Fatalf("terminal shell = %#v, %v", result.Task, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	tasks, err := restored.ListContext(t.Context())
	if err != nil || len(tasks) != 1 || tasks[0].ID != record.ID || tasks[0].ExitCode == nil {
		t.Fatalf("recovered tasks = %#v, %v", tasks, err)
	}
	work, err := restored.ListWorkContext(t.Context())
	if err != nil || len(work) != 2 || work[0].ID != one.ID || work[1].ID != two.ID {
		t.Fatalf("recovered work = %#v, %v", work, err)
	}

	*tasks[0].ExitCode = 42
	work[0].Metadata["source"] = "mutated"
	currentTask, err := restored.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentWork, err := restored.GetWork(one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentTask.ExitCode == nil || *currentTask.ExitCode != 0 || currentWork.Metadata["source"] != "test" {
		t.Fatalf("recovered checked snapshots aliased live state: task=%#v work=%#v", currentTask, currentWork)
	}
}

func TestRestoreDoesNotReplayRunningLocalTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.LaunchShell(context.Background(), ShellSpec{Command: "sleep 30", Dir: root, Env: os.Environ(), Shell: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restored.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusFailed || !strings.Contains(recovered.Error, "not replayed") {
		t.Fatalf("uncertain mutation was replayed or hidden: %+v", recovered)
	}
	_ = manager.Stop(record.ID)
	_ = manager.Close()
	_ = restored.Close()
}

func TestPublicErrorSanitizerCannotReintroduceUnsafeFallback(t *testing.T) {
	blocked := map[string]bool{
		ErrClosed.Error(): true,
		"task failed; external diagnostic was omitted":           true,
		"task operation failed; external diagnostic was omitted": true,
	}
	manager, err := Open(t.TempDir(), Options{
		SanitizeRecord: func(value string) (string, bool) {
			if blocked[value] {
				return "", true
			}
			return value, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = manager.CreateWork("subject", "description", "active", nil)
	if err == nil {
		t.Fatal("closed manager mutation returned nil")
	}
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		for unsafe := range blocked {
			if strings.Contains(rendered, unsafe) {
				t.Fatalf("public error fallback reintroduced %q in %q", unsafe, rendered)
			}
		}
	}
}

func TestTaskCompositionSeamPanicsAreContained(t *testing.T) {
	t.Run("clock", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{Clock: func() time.Time {
			panic("clock-panic-payload")
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		item, err := manager.CreateWork("subject", "description", "active", nil)
		if err != nil {
			t.Fatal(err)
		}
		if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			t.Fatalf("degraded clock produced zero timestamps: %#v", item)
		}
	})

	t.Run("random", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{Random: panickingRandomReader{}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		if _, err := manager.CreateWork("subject", "description", "active", nil); err == nil ||
			strings.Contains(err.Error(), "random-reader-panic-payload") {
			t.Fatalf("random reader panic = %v", err)
		}
		if len(manager.ListWork()) != 0 {
			t.Fatalf("random panic published work: %#v", manager.ListWork())
		}
	})
}

func probeReentrantTaskBoundary(manager *Manager) error {
	if _, err := manager.Get("bmissing0"); err != ErrBusy {
		return fmt.Errorf("Get error = %v", err)
	}
	if _, err := manager.ListContext(context.Background()); err != ErrBusy {
		return fmt.Errorf("ListContext error = %v", err)
	}
	if _, err := manager.GetWork("tmissing0"); err != ErrBusy {
		return fmt.Errorf("GetWork error = %v", err)
	}
	if _, err := manager.ListWorkContext(context.Background()); err != ErrBusy {
		return fmt.Errorf("ListWorkContext error = %v", err)
	}
	if _, err := manager.CreateWork("nested", "nested", "nested", nil); err != ErrBusy {
		return fmt.Errorf("CreateWork error = %v", err)
	}
	if _, err := manager.UpdateWork("tmissing0", WorkPatch{}); err != ErrBusy {
		return fmt.Errorf("UpdateWork error = %v", err)
	}
	if err := manager.ReplaceTodos(nil); err != ErrBusy {
		return fmt.Errorf("ReplaceTodos error = %v", err)
	}
	if _, err := manager.LaunchShell(context.Background(), ShellSpec{}); err != ErrBusy {
		return fmt.Errorf("LaunchShell error = %v", err)
	}
	if err := manager.Stop("bmissing0"); err != ErrBusy {
		return fmt.Errorf("Stop error = %v", err)
	}
	if _, err := manager.Poll(context.Background(), "bmissing0", 0, false, 0); err != ErrBusy {
		return fmt.Errorf("Poll error = %v", err)
	}
	if err := manager.CloseContext(context.Background()); err != ErrBusy {
		return fmt.Errorf("CloseContext error = %v", err)
	}
	if err := manager.Close(); err != ErrBusy {
		return fmt.Errorf("Close error = %v", err)
	}
	if records := manager.List(); records != nil {
		return fmt.Errorf("List returned %#v", records)
	}
	if work := manager.ListWork(); work != nil {
		return fmt.Errorf("ListWork returned %#v", work)
	}
	if todos := manager.Todos(); todos != nil {
		return fmt.Errorf("Todos returned %#v", todos)
	}
	return nil
}

func TestTaskHostCallbacksCannotReenterManagerLocks(t *testing.T) {
	t.Run("clock", func(t *testing.T) {
		var manager *Manager
		var probeErr error
		manager, probeErr = Open(t.TempDir(), Options{Clock: func() time.Time {
			if manager != nil {
				probeErr = probeReentrantTaskBoundary(manager)
			}
			return time.Unix(1_700_000_000, 0)
		}})
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		defer manager.Close()
		if _, err := manager.CreateWork("subject", "description", "active", nil); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	})

	t.Run("random", func(t *testing.T) {
		var manager *Manager
		var probeErr error
		random := taskReaderFunc(func(target []byte) (int, error) {
			probeErr = probeReentrantTaskBoundary(manager)
			clear(target)
			return len(target), nil
		})
		var err error
		manager, err = Open(t.TempDir(), Options{Random: random})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		if _, err := manager.CreateWork("subject", "description", "active", nil); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	})

	t.Run("state validator", func(t *testing.T) {
		var manager *Manager
		var probeErr error
		var err error
		manager, err = Open(t.TempDir(), Options{ValidateState: func([]byte) error {
			probeErr = probeReentrantTaskBoundary(manager)
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		if _, err := manager.CreateWork("subject", "description", "active", nil); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	})

	t.Run("persistence hook", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		var probeErr error
		manager.persistHook = func() error {
			probeErr = probeReentrantTaskBoundary(manager)
			return nil
		}
		if _, err := manager.CreateWork("subject", "description", "active", nil); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		manager.persistHook = nil
	})

	t.Run("record sanitizer", func(t *testing.T) {
		var manager *Manager
		var probeErr error
		var err error
		manager, err = Open(t.TempDir(), Options{SanitizeRecord: func(value string) (string, bool) {
			probeErr = probeReentrantTaskBoundary(manager)
			return value, false
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		record, err := manager.LaunchShell(t.Context(), ShellSpec{
			Command: "true", Description: "safe", Dir: t.TempDir(), Shell: "/bin/bash",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Poll(t.Context(), record.ID, 0, true, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	})

	t.Run("command factory", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		var probeErr error
		record, err := manager.LaunchShell(t.Context(), ShellSpec{
			Command: "true", Description: "safe", Dir: t.TempDir(), Shell: "/bin/bash",
			CommandFactory: func(ctx context.Context, program string, arguments ...string) *exec.Cmd {
				probeErr = probeReentrantTaskBoundary(manager)
				return exec.CommandContext(ctx, program, arguments...)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Poll(t.Context(), record.ID, 0, true, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if probeErr != nil {
			t.Fatal(probeErr)
		}
	})
}

func TestOutputSanitizerCallbacksCannotReenterManager(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}

	var manager *Manager
	var probeMu sync.Mutex
	calls := make(map[string]int)
	var firstProbeErr error
	probe := func(stage string) {
		err := probeReentrantTaskBoundary(manager)
		probeMu.Lock()
		defer probeMu.Unlock()
		calls[stage]++
		if firstProbeErr == nil {
			firstProbeErr = err
		}
	}

	var err error
	manager, err = Open(t.TempDir(), Options{
		NewOutputSanitizer: func() OutputSanitizer {
			probe("factory")
			return &reentrantOutputSanitizer{probe: probe}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := manager.LaunchShell(t.Context(), ShellSpec{
		Command: "printf output-sanitizer-reentry",
		Dir:     t.TempDir(),
		Shell:   "/bin/bash",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	var terminal Record
	for {
		terminal, err = manager.Get(record.ID)
		if err == ErrBusy {
			select {
			case <-waitCtx.Done():
				_ = manager.Close()
				t.Fatal(waitCtx.Err())
			case <-time.After(time.Millisecond):
				continue
			}
		}
		if err != nil {
			_ = manager.Close()
			t.Fatal(err)
		}
		if terminal.Status.Terminal() {
			break
		}
		select {
		case <-waitCtx.Done():
			_ = manager.Close()
			t.Fatal(waitCtx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if terminal.Status != StatusCompleted {
		_ = manager.Close()
		t.Fatalf("terminal status = %s, error = %q", terminal.Status, terminal.Error)
	}

	probeMu.Lock()
	probeErr := firstProbeErr
	observed := make(map[string]int, len(calls))
	for stage, count := range calls {
		observed[stage] = count
	}
	probeMu.Unlock()
	if probeErr != nil {
		_ = manager.Close()
		t.Fatal(probeErr)
	}
	for _, stage := range []string{"factory", "marker", "write", "flush"} {
		if observed[stage] == 0 {
			_ = manager.Close()
			t.Fatalf("%s callback was not exercised: %#v", stage, observed)
		}
	}

	if _, err := manager.Poll(t.Context(), record.ID, 0, false, 0); err != nil {
		_ = manager.Close()
		t.Fatalf("poll after callback release: %v", err)
	}
	if _, err := manager.CreateWork("later", "later", "later", nil); err != nil {
		_ = manager.Close()
		t.Fatalf("callback claim was not released: %v", err)
	}
	if _, err := manager.ListContext(t.Context()); err != nil {
		_ = manager.Close()
		t.Fatalf("checked task snapshot after callback release: %v", err)
	}
	if work, err := manager.ListWorkContext(t.Context()); err != nil || len(work) != 1 {
		_ = manager.Close()
		t.Fatalf("checked work snapshot after callback release = %#v, %v", work, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStopSanitizesSignalCallbackFailureAndStillReaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell lifecycle fixture requires /bin/bash")
	}
	const secret = "signal-callback-secret-must-not-escape"
	cause := errors.New(secret)
	manager, err := Open(t.TempDir(), Options{
		SanitizeRecord: func(value string) (string, bool) {
			return strings.ReplaceAll(value, secret, "[REDACTED]"), false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "while :; do sleep 0.1; done", Dir: t.TempDir(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	live := manager.live[record.ID]
	originalSignal := live.signal
	live.signal = func() error {
		_ = originalSignal()
		return cause
	}
	manager.mu.Unlock()
	err = manager.Stop(record.ID)
	if err == nil {
		t.Fatal("signal callback failure returned nil")
	}
	if errors.Is(err, cause) {
		t.Fatal("signal callback cause remained reachable through errors.Is")
	}
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("signal callback payload escaped in %q", rendered)
		}
	}
	if live.cmd.ProcessState == nil {
		t.Fatal("signal callback failure prevented child reap")
	}
	terminal, err := manager.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != StatusKilled {
		t.Fatalf("signal callback failure stranded terminal state: %#v", terminal)
	}
}

func TestStopDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell lifecycle fixture requires /bin/bash")
	}
	cause := &blockingTaskUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "while :; do sleep 0.1; done", Dir: t.TempDir(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	live := manager.live[record.ID]
	originalSignal := live.signal
	live.signal = func() error {
		_ = originalSignal()
		return cause
	}
	manager.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- manager.Stop(record.ID) }()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, errTaskSignalCallbackFailed) || errors.Is(err, cause) {
			t.Fatalf("Stop blocking callback projection = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Stop blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Manager.Stop invoked foreign Unwrap")
	default:
	}
	if live.cmd.ProcessState == nil {
		t.Fatal("blocking callback failure prevented child reap")
	}
}

func TestStopFallsBackToContextCancellationWhenSignalCallbackPanics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell lifecycle fixture requires /bin/bash")
	}
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "while :; do sleep 0.1; done", Dir: t.TempDir(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	live := manager.live[record.ID]
	live.signal = func() error {
		panic("signal-panic-before-process-stop")
	}
	manager.mu.Unlock()
	if err := manager.Stop(record.ID); err == nil {
		t.Fatal("signal callback panic returned nil")
	}
	if live.cmd.ProcessState == nil {
		t.Fatal("fallback cancellation did not reap child")
	}
	terminal, err := manager.Get(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != StatusKilled {
		t.Fatalf("signal callback panic stranded terminal state: %#v", terminal)
	}
}

func TestTaskDebugFormattingOmitsSecretBearingValues(t *testing.T) {
	const secret = "task-format-secret-must-not-escape"
	status := WorkStatus(secret)
	owner := secret
	values := []any{
		Record{ID: ID(secret), Kind: Kind(secret), Status: Status(secret), Description: secret, Command: secret, ToolUseID: secret, Owner: secret, OutputPath: secret, Error: secret, OutputWarning: secret},
		WorkItem{ID: ID(secret), Subject: secret, Description: secret, ActiveForm: secret, Status: WorkStatus(secret), Owner: secret, Blockers: []ID{ID(secret)}, Metadata: map[string]string{secret: secret}},
		Todo{Content: secret, Status: WorkStatus(secret), ActiveForm: secret},
		PollResult{Task: Record{Description: secret}, Output: secret},
		WorkPatch{Status: &status, Owner: &owner, Metadata: map[string]*string{secret: &owner}},
		Options{SanitizeRecord: func(value string) (string, bool) { return value, false }},
		ShellSpec{Command: secret, Description: secret, ToolUseID: secret, Owner: secret, Dir: secret, Env: []string{secret}, Shell: secret},
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if rendered := fmt.Sprintf(format, value); strings.Contains(rendered, secret) {
				t.Fatalf("%T %s exposed secret in %q", value, format, rendered)
			}
		}
	}
}
