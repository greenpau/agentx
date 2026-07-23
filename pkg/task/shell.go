package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/greenpau/agentx/pkg/childenv"
)

const maximumTaskErrorGraphNodes = 128

var (
	trustedTaskErrorStringType = reflect.TypeOf(errors.New(""))
	trustedTaskSingleWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedTaskMultiWrapType   = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedTaskJoinType        = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

type taskPublicError struct {
	message string
	classes []error
}

func (e *taskPublicError) Error() string {
	if e == nil {
		return "task operation failed"
	}
	return e.message
}

func (e *taskPublicError) Is(target error) bool {
	targetType := reflect.TypeOf(target)
	if e == nil || target == nil || targetType == nil || !targetType.Comparable() {
		return false
	}
	for _, class := range e.classes {
		if class == target {
			return true
		}
	}
	return false
}

func (e *taskPublicError) Format(state fmt.State, verb rune) {
	message := "task operation failed"
	if e != nil {
		message = e.message
	}
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", message)
	default:
		_, _ = fmt.Fprint(state, message)
	}
}

// CommandFactory constructs the authorized process placement selected by the
// caller. Implementations must bind the returned command to ctx; Manager still
// owns directory/environment assignment, process-group isolation, and Wait.
type CommandFactory func(ctx context.Context, program string, arguments ...string) *exec.Cmd

// ShellSpec defines one local background process.
type ShellSpec struct {
	Command        string
	Description    string
	ToolUseID      string
	Owner          string
	Dir            string
	Env            []string
	Shell          string
	Timeout        time.Duration
	CommandFactory CommandFactory
}

// ShellArguments returns a non-interactive invocation that suppresses shell
// startup files. Startup files and BASH_ENV/ENV are ambient code-execution
// channels and must not run inside an authorized tool invocation.
func ShellArguments(shell, command string) ([]string, error) {
	switch filepath.Base(shell) {
	case "bash":
		return []string{"--noprofile", "--norc", "-c", command}, nil
	case "zsh":
		return []string{"-f", "-c", command}, nil
	case "sh", "dash":
		return []string{"-c", command}, nil
	default:
		return nil, errors.New("unsupported shell executable")
	}
}

// LaunchShell starts a process whose lifetime belongs to the manager, not to
// the caller's model-turn context.
func (m *Manager) LaunchShell(ctx context.Context, spec ShellSpec) (Record, error) {
	if m.hostCallbackBusy() {
		return Record{}, ErrBusy
	}
	record, err := m.launchShell(ctx, spec)
	if err == nil {
		return record, err
	}
	return record, m.sanitizePublicError(err)
}

func (m *Manager) launchShell(_ context.Context, spec ShellSpec) (Record, error) {
	if err := validateShellSpec(spec); err != nil {
		return Record{}, err
	}
	if spec.Timeout < 0 {
		return Record{}, errors.New("shell timeout cannot be negative")
	}
	shell := spec.Shell
	if shell == "" {
		shell = "/bin/bash"
	}
	if !filepath.IsAbs(shell) {
		return Record{}, errors.New("shell executable must be absolute")
	}
	shellArgs, err := ShellArguments(shell, spec.Command)
	if err != nil {
		return Record{}, err
	}
	description := spec.Description
	if strings.TrimSpace(description) == "" {
		description = spec.Command
	}
	recordCommand, recordDescription, err := m.sanitizeRecordFields(spec.Command, description)
	if err != nil {
		return Record{}, err
	}
	outputSanitizer, truncMarker, err := m.createOutputSanitizer()
	if err != nil {
		return Record{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Record{}, ErrClosed
	}
	if len(m.tasks) >= maximumTaskRecords {
		m.mu.Unlock()
		return Record{}, fmt.Errorf("shell task count cannot exceed %d", maximumTaskRecords)
	}
	if err := m.validateOutputDirectory(); err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	id, err := m.nextIDLocked('b')
	if err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	outputPath := filepath.Join(m.outputDir, string(id)+".log")
	file, err := secureOutputFile(outputPath)
	if err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	identity, err := file.Stat()
	if err != nil {
		_ = file.Close()
		m.mu.Unlock()
		return Record{}, fmt.Errorf("inspect created task output: %w", err)
	}
	m.outputIdentity[id] = identity
	now := m.currentTime().UTC()
	record := Record{
		Version: stateVersion, ID: id, Kind: KindShell, Status: StatusPending,
		Description: recordDescription, Command: recordCommand, ToolUseID: spec.ToolUseID,
		Owner: spec.Owner, OutputPath: outputPath, StartedAt: now,
	}
	m.tasks[id] = record
	if err := m.persistLocked(); err != nil {
		delete(m.tasks, id)
		delete(m.outputIdentity, id)
		m.mu.Unlock()
		_ = file.Close()
		removeErr := removeOutputIfSame(outputPath, identity)
		return Record{}, errors.Join(err, wrapOptional("remove uncommitted task output", removeErr))
	}

	var processCtx context.Context
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		processCtx, cancel = context.WithTimeout(context.Background(), spec.Timeout)
	} else {
		processCtx, cancel = context.WithCancel(context.Background())
	}
	cmd, buildErr := m.buildShellCommand(spec.CommandFactory, processCtx, shell, shellArgs...)
	if buildErr != nil || cmd == nil {
		cancel()
		closeErr := errors.Join(file.Sync(), file.Close())
		now := m.currentTime().UTC()
		record.Status = StatusFailed
		cause := buildErr
		if buildErr != nil {
			record.Error = m.sanitizeError(buildErr.Error())
		} else {
			record.Error = m.sanitizeError("shell command factory returned no process")
			cause = errors.New(record.Error)
		}
		record.EndedAt = &now
		m.markOutputIncomplete(&record, closeErr)
		projectionErr := m.publishTerminalRecordLocked(id, record)
		persistErr := m.persistTerminalLocked()
		m.mu.Unlock()
		return Record{}, errors.Join(cause, wrapOptional("close task output", closeErr), projectionErr, persistErr)
	}
	cmd.Dir = spec.Dir
	// Enforce the child boundary here as well as at the Bash tool adapter. A
	// future caller cannot turn a task into an ambient credential inheritance
	// channel by constructing ShellSpec directly.
	cmd.Env = childenv.Shell(spec.Env)
	prepareProcess(cmd)
	cmd.Cancel = func() error { return stopProcess(cmd) }
	live := &liveTask{
		cancel: cancel, cmd: cmd, done: make(chan struct{}), ctx: processCtx,
		output: file, outputCap: m.outputCap, sanitizer: outputSanitizer,
		truncMarker: truncMarker,
	}
	live.signal = func() error {
		// Kill through the owned group before cancelling the context. Cancelling
		// first starts exec.Cmd's watcher and races two group-signal sequences;
		// on Darwin the loser can observe EPERM even though termination succeeded.
		err := stopProcess(cmd)
		cancel()
		return err
	}
	writer := &taskWriter{manager: m, id: id, live: live}
	cmd.Stdout = writer
	cmd.Stderr = writer
	m.live[id] = live
	if err := cmd.Start(); err != nil {
		cancel()
		delete(m.live, id)
		closeErr := m.closeTaskOutput(live)
		now := m.currentTime().UTC()
		record.Status = StatusFailed
		record.Error = m.sanitizeError(safeTaskErrorText(err))
		record.EndedAt = &now
		m.markOutputIncomplete(&record, closeErr)
		projectionErr := m.publishTerminalRecordLocked(id, record)
		persistErr := m.persistTerminalLocked()
		published, publishedOK := m.tasks[id]
		m.mu.Unlock()
		close(live.done)
		if persistErr != nil || closeErr != nil {
			return Record{}, errors.Join(fmt.Errorf("start shell task: %w", err), wrapOptional("close task output", closeErr), projectionErr, persistErr)
		}
		if publishedOK {
			return cloneRecord(published), errors.Join(fmt.Errorf("start shell task: %w", err), projectionErr)
		}
		return Record{}, errors.Join(fmt.Errorf("start shell task: %w", err), projectionErr)
	}
	if verifyErr := verifyProcess(cmd); verifyErr != nil {
		// A started process without the requested containment boundary is not a
		// valid task. Own the sole Wait while Stop/Close observe cleanup in
		// progress, then durably record failure rather than exposing it as live.
		live.launchCleanup = true
		m.mu.Unlock()
		signalErr := live.signal()
		waitErr := cmd.Wait()
		closeErr := m.closeTaskOutput(live)

		m.mu.Lock()
		delete(m.live, id)
		now := m.currentTime().UTC()
		record.Status = StatusFailed
		record.Error = m.sanitizeError("process containment verification failed: " + verifyErr.Error())
		record.EndedAt = &now
		m.markOutputIncomplete(&record, closeErr)
		setExitCode(&record, waitErr)
		projectionErr := m.publishTerminalRecordLocked(id, record)
		persistErr := m.persistTerminalLocked()
		cleanupErr := errors.Join(
			fmt.Errorf("verify shell process containment: %w", verifyErr),
			wrapOptional("signal uncontained shell", signalErr),
			unexpectedWaitError(waitErr),
			wrapOptional("close task output", closeErr),
			projectionErr,
			wrapOptional("persist uncontained shell failure", persistErr),
		)
		live.terminalErr = cleanupErr
		m.mu.Unlock()
		close(live.done)
		return Record{}, cleanupErr
	}
	record.Status = StatusRunning
	m.tasks[id] = record
	if persistErr := m.persistLocked(); persistErr != nil {
		// The process exists, but its running state was not committed. Mark the
		// live entry as cleanup-owned before releasing the lock so Close/Stop can
		// wait without racing a second Wait call.
		live.launchCleanup = true
		m.mu.Unlock()

		signalErr := live.signal()
		waitErr := cmd.Wait() // always reap a successfully started child
		closeErr := m.closeTaskOutput(live)

		m.mu.Lock()
		delete(m.live, id)
		now := m.currentTime().UTC()
		record.Status = StatusFailed
		record.Error = m.sanitizeError("task launch state could not be persisted; process terminated")
		record.EndedAt = &now
		m.markOutputIncomplete(&record, closeErr)
		setExitCode(&record, waitErr)
		projectionErr := m.publishTerminalRecordLocked(id, record)
		finalPersistErr := m.persistTerminalLocked()
		cleanupErr := errors.Join(
			fmt.Errorf("persist running shell task: %w", persistErr),
			wrapOptional("signal shell after persistence failure", signalErr),
			unexpectedWaitError(waitErr),
			wrapOptional("close task output", closeErr),
			projectionErr,
			wrapOptional("persist failed shell cleanup", finalPersistErr),
		)
		live.terminalErr = cleanupErr
		m.mu.Unlock()
		close(live.done)
		return Record{}, cleanupErr
	}
	m.mu.Unlock()

	go m.waitShell(id, cmd, live)
	return cloneRecord(record), nil
}

func sanitizeTaskRecord(sanitize func(string) (string, bool), command, description string) (safeCommand, safeDescription string, err error) {
	if sanitize == nil {
		return command, description, nil
	}
	defer func() {
		if recover() != nil {
			safeCommand = ""
			safeDescription = ""
			err = errors.New("sanitize task record: sanitizer panicked")
		}
	}()
	safeCommand, commandSuppressed := sanitize(command)
	safeDescription, descriptionSuppressed := sanitize(description)
	if commandSuppressed || descriptionSuppressed || strings.TrimSpace(safeCommand) == "" || strings.TrimSpace(safeDescription) == "" {
		return "", "", errors.New("sanitize task record: durable projection is unavailable")
	}
	return safeCommand, safeDescription, nil
}

func buildShellCommand(factory CommandFactory, ctx context.Context, program string, arguments ...string) (command *exec.Cmd, err error) {
	if factory == nil {
		return exec.CommandContext(ctx, program, arguments...), nil
	}
	defer func() {
		if recover() != nil {
			command = nil
			// A custom factory panic may include the authorized raw command or
			// environment. The panic payload is neither durable evidence nor a
			// safe caller diagnostic.
			err = errors.New("shell command factory panicked")
		}
	}()
	return factory(ctx, program, arguments...), nil
}

func (m *Manager) buildShellCommand(factory CommandFactory, ctx context.Context, program string, arguments ...string) (*exec.Cmd, error) {
	if factory == nil {
		return buildShellCommand(nil, ctx, program, arguments...)
	}
	if !m.beginHostCallback() {
		return nil, ErrBusy
	}
	defer m.endHostCallback()
	return buildShellCommand(factory, ctx, program, arguments...)
}

func (m *Manager) sanitizeRecordFields(command, description string) (string, string, error) {
	if m.sanitizeRecord == nil {
		return sanitizeTaskRecord(nil, command, description)
	}
	if !m.beginHostCallback() {
		return "", "", ErrBusy
	}
	defer m.endHostCallback()
	return sanitizeTaskRecord(m.sanitizeRecord, command, description)
}

func (m *Manager) sanitizeError(value string) string {
	if m.sanitizeRecord == nil {
		return sanitizeTaskError(nil, value)
	}
	if !m.beginHostCallback() {
		return ""
	}
	defer m.endHostCallback()
	return sanitizeTaskError(m.sanitizeRecord, value)
}

func (m *Manager) markOutputIncomplete(record *Record, cause error) {
	if m.sanitizeRecord == nil {
		markOutputIncomplete(record, cause, nil)
		return
	}
	if !m.beginHostCallback() {
		if record != nil && cause != nil {
			record.OutputIncomplete = true
		}
		return
	}
	defer m.endHostCallback()
	markOutputIncomplete(record, cause, m.sanitizeRecord)
}

func secureOutputFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("task output target is a symlink")
		}
		return nil, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect task output: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create task output: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect task output: %w", err)
	}
	links, err := openedFileLinkCount(file, info)
	if err != nil || links != 1 {
		_ = file.Close()
		_ = removeOutputIfSame(path, info)
		if err != nil {
			return nil, fmt.Errorf("verify task output link count: %w", err)
		}
		return nil, errors.New("task output must have exactly one filesystem link")
	}
	return file, nil
}

func removeOutputIfSame(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return errors.New("task output identity changed before cleanup")
	}
	return os.Remove(path)
}

type taskWriter struct {
	manager *Manager
	id      ID
	live    *liveTask
}

func (w *taskWriter) Write(p []byte) (int, error) {
	w.live.fileMu.Lock()
	defer w.live.fileMu.Unlock()

	w.manager.mu.RLock()
	_, ok := w.manager.tasks[w.id]
	cap := w.manager.outputCap
	w.manager.mu.RUnlock()
	if !ok {
		return len(p), nil
	}
	if w.live.outputFailed {
		return len(p), nil
	}
	safe, err := w.manager.sanitizeOutput(w.live.sanitizer, string(p), false)
	if err != nil {
		w.live.outputFailed = true
		w.live.outputErr = errors.Join(w.live.outputErr, err)
		return len(p), nil
	}
	if err := writeTaskOutputLocked(w.live, []byte(safe), cap); err != nil {
		w.live.outputFailed = true
		w.live.outputErr = errors.Join(w.live.outputErr, err)
		return len(p), nil
	}
	// Report the submitted length rather than the transformed length. The
	// sanitizer may hold a suffix or replace a credential with a marker, but it
	// consumed the complete child-process chunk.
	return len(p), nil
}

func createOutputSanitizer(factory func() OutputSanitizer) (sanitizer OutputSanitizer, truncMarker string, err error) {
	if factory == nil {
		return nil, outputTruncMarker, nil
	}
	defer func() {
		if recover() != nil {
			sanitizer = nil
			truncMarker = ""
			err = errors.New("create task output sanitizer: sanitizer panicked")
		}
	}()
	sanitizer = factory()
	if sanitizer == nil {
		return nil, "", errors.New("create task output sanitizer: factory returned nil")
	}
	truncMarker = sanitizer.TruncationMarker()
	if len(truncMarker) > len(outputTruncMarker) {
		return nil, "", fmt.Errorf("create task output sanitizer: truncation marker exceeds %d bytes", len(outputTruncMarker))
	}
	return sanitizer, truncMarker, nil
}

func (m *Manager) createOutputSanitizer() (OutputSanitizer, string, error) {
	if m.newOutputSanitizer == nil {
		return createOutputSanitizer(nil)
	}
	if !m.beginHostCallback() {
		return nil, "", ErrBusy
	}
	defer m.endHostCallback()
	return createOutputSanitizer(m.newOutputSanitizer)
}

func sanitizeTaskOutput(sanitizer OutputSanitizer, value string, flush bool) (safe string, err error) {
	if sanitizer == nil {
		return value, nil
	}
	defer func() {
		if recover() != nil {
			safe = ""
			err = errors.New("sanitize task output: sanitizer panicked")
		}
	}()
	if flush {
		return sanitizer.Flush(), nil
	}
	return sanitizer.Write(value), nil
}

func (m *Manager) sanitizeOutput(sanitizer OutputSanitizer, value string, flush bool) (string, error) {
	if sanitizer == nil {
		return sanitizeTaskOutput(nil, value, flush)
	}
	if !m.beginHostCallback() {
		return "", ErrBusy
	}
	defer m.endHostCallback()
	return sanitizeTaskOutput(sanitizer, value, flush)
}

// writeTaskOutputLocked accepts only already-sanitized bytes. The caller owns
// live.fileMu, so raw child output can never race the filter and reach disk.
func writeTaskOutputLocked(live *liveTask, safe []byte, cap int64) error {
	file := live.output
	if file == nil {
		return errors.New("task output is closed")
	}
	remaining := cap - live.size
	if remaining <= 0 {
		return appendOutputTruncationMarkerLocked(live)
	}
	chunk := safe
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
	}
	n, writeErr := file.Write(chunk)
	live.size += int64(n)
	if n != len(chunk) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	var markerErr error
	if len(chunk) < len(safe) {
		markerErr = appendOutputTruncationMarkerLocked(live)
	}
	return errors.Join(writeErr, markerErr)
}

func appendOutputTruncationMarkerLocked(live *liveTask) error {
	if live.capped {
		return nil
	}
	live.capped = true
	_, err := io.WriteString(live.output, live.truncMarker)
	return err
}

func (m *Manager) waitShell(id ID, cmd *exec.Cmd, live *liveTask) {
	err := cmd.Wait()
	closeErr := m.closeTaskOutput(live)

	m.mu.Lock()
	defer func() {
		if recover() != nil {
			// Completion callbacks run outside the initiating turn. A faulty
			// host seam must not strand the manager lock or the process
			// completion latch, and its panic payload is not safe evidence.
			m.dirty = true
			live.terminalErr = errors.Join(
				live.terminalErr,
				errors.New("finalize shell task: internal callback panicked"),
			)
		}
		m.mu.Unlock()
		close(live.done)
	}()
	delete(m.live, id)
	record, ok := m.tasks[id]
	if !ok {
		return
	}
	now := m.currentTime().UTC()
	record.EndedAt = &now
	m.markOutputIncomplete(&record, closeErr)
	if live.stopRequested {
		record.Status = StatusKilled
		record.Error = m.sanitizeError("stopped by request")
		setExitCode(&record, err)
	} else if err == nil {
		code := 0
		record.ExitCode = &code
		record.Status = StatusCompleted
	} else {
		record.Status = StatusFailed
		if live.ctx.Err() == context.DeadlineExceeded {
			record.Error = m.sanitizeError("task timed out")
		} else {
			record.Error = m.sanitizeError(safeTaskErrorText(err))
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			record.ExitCode = &code
		}
	}
	projectionErr := m.publishTerminalRecordLocked(id, record)
	live.terminalErr = errors.Join(
		projectionErr,
		wrapOptional("close task output", closeErr),
		m.persistTerminalLocked(),
	)
}

func (m *Manager) closeTaskOutput(live *liveTask) error {
	live.fileMu.Lock()
	defer live.fileMu.Unlock()
	if live.output == nil {
		return nil
	}
	file := live.output
	var sanitizeErr error
	var writeErr error
	if !live.outputFailed {
		var safeTail string
		safeTail, sanitizeErr = m.sanitizeOutput(live.sanitizer, "", true)
		if sanitizeErr == nil && safeTail != "" {
			writeErr = writeTaskOutputLocked(live, []byte(safeTail), live.outputCap)
		}
	}
	live.output = nil
	return errors.Join(live.outputErr, sanitizeErr, writeErr, file.Sync(), file.Close())
}

// Stop first claims the completion race, then signals and waits. Killed is
// published only by the sole process waiter after termination is confirmed.
func (m *Manager) Stop(id ID) (resultErr error) {
	if m.hostCallbackBusy() {
		return ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	live, claimed, err := m.beginStop(id, false)
	if err != nil && live == nil {
		return err
	}
	if !claimed {
		if live != nil {
			if waitErr := waitForProcess(live.done, shutdownWait); waitErr != nil {
				return errors.Join(ErrNotRunning, waitErr)
			}
		}
		return ErrNotRunning
	}
	signalErr := callTaskSignal(live.signal)
	var fallbackSignalErr error
	if signalErr != nil {
		// A host-provided signal seam may fail or panic before invoking the
		// manager-owned cancellation path. Stop the owned process group
		// directly as well as cancelling its context; a custom CommandFactory
		// is required to bind the context but cannot strand cleanup if it fails
		// to honor that contract.
		fallbackSignalErr = stopProcess(live.cmd)
		live.cancel()
	}
	waitErr := waitForProcess(live.done, shutdownWait)
	var terminalErr error
	if waitErr == nil {
		terminalErr = live.terminalErr
	}
	return errors.Join(
		wrapOptional("signal shell task", signalErr),
		wrapOptional("fallback signal shell task", fallbackSignalErr),
		waitErr,
		wrapOptional("persist killed shell task", terminalErr),
	)
}

// beginStop atomically claims stop intent without publishing a terminal state.
// Callers that win must invoke live.signal outside Manager.mu.
func (m *Manager) beginStop(id ID, duringClose bool) (*liveTask, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed && !duringClose {
		return nil, false, ErrClosed
	}
	record, ok := m.tasks[id]
	if !ok {
		return nil, false, ErrNotFound
	}
	if record.Status != StatusRunning {
		return nil, false, ErrNotRunning
	}
	live := m.live[id]
	if live == nil {
		return nil, false, fmt.Errorf("%w: running task %s has no live process", ErrInvalidState, id)
	}
	if live.stopRequested || live.launchCleanup {
		return live, false, ErrNotRunning
	}
	live.stopRequested = true
	return live, true, nil
}

func waitForProcess(done <-chan struct{}, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return ErrStopTimeout
	}
}

func setExitCode(record *Record, waitErr error) {
	if waitErr == nil {
		code := 0
		record.ExitCode = &code
		return
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		record.ExitCode = &code
	}
}

func unexpectedWaitError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return nil
	}
	return fmt.Errorf("reap shell after persistence failure: %w", err)
}

func wrapOptional(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func markOutputIncomplete(record *Record, cause error, sanitize func(string) (string, bool)) {
	if record == nil || cause == nil {
		return
	}
	record.OutputIncomplete = true
	const warning = "task output is incomplete because sanitization or persistence failed"
	if sanitize == nil {
		record.OutputWarning = warning
		return
	}
	defer func() { _ = recover() }()
	safe, suppressed := sanitize(warning)
	if !suppressed {
		record.OutputWarning = boundedStateString(safe)
	}
}

func sanitizeTaskError(sanitize func(string) (string, bool), value string) string {
	if sanitize == nil {
		return boundedStateString(value)
	}
	project := func(candidate string) (safe string, usable bool) {
		defer func() {
			if recover() != nil {
				safe = ""
				usable = false
			}
		}()
		safe, suppressed := sanitize(candidate)
		return safe, !suppressed && strings.TrimSpace(safe) != ""
	}
	if safe, usable := project(value); usable {
		return boundedStateString(safe)
	}
	const fallback = "task failed; external diagnostic was omitted"
	if safe, usable := project(fallback); usable {
		return boundedStateString(safe)
	}
	return ""
}

func sanitizeTaskPublicError(sanitize func(string) (string, bool), err error) error {
	if err == nil {
		return nil
	}
	classes := inspectTaskError(err)
	text, ok := taskErrorText(err)
	if !ok {
		for _, class := range classes {
			if text, ok = taskErrorText(class); ok {
				break
			}
		}
		if !ok {
			text = "task failed; external diagnostic was omitted"
		}
	}
	safe := sanitizeTaskError(sanitize, text)
	if safe == "" {
		// Even a fixed fallback can itself be a configured credential. An empty,
		// opaque error is preferable to reintroducing a literal the sanitizer
		// explicitly could not project.
		return errors.New("")
	}
	for _, sentinel := range []error{
		ErrNotFound, ErrNotRunning, ErrInvalidState, ErrDependencyCycle,
		ErrClosed, ErrStopTimeout, ErrBusy,
	} {
		if err == sentinel && safe == sentinel.Error() {
			return sentinel
		}
	}
	return &taskPublicError{message: safe, classes: classes}
}

func taskErrorText(err error) (text string, ok bool) {
	if err == nil {
		return "", true
	}
	defer func() {
		if recover() != nil {
			text = ""
			ok = false
		}
	}()
	if projected, projectedOK := err.(*taskPublicError); projectedOK {
		return projected.Error(), true
	}
	switch reflect.TypeOf(err) {
	case trustedTaskErrorStringType, trustedTaskSingleWrapType, trustedTaskMultiWrapType:
		return err.Error(), true
	}
	if exitErr, exitOK := err.(*exec.ExitError); exitOK {
		return exitErr.Error(), true
	}
	return "", false
}

func safeTaskErrorText(err error) string {
	if text, ok := taskErrorText(err); ok {
		return text
	}
	return "task failed; external diagnostic was omitted"
}

func inspectTaskError(err error) []error {
	pending := []error{err}
	seen := make(map[error]struct{})
	classes := make([]error, 0, maximumTaskErrorGraphNodes)
	for visited := 0; len(pending) > 0 && visited < maximumTaskErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		currentType := reflect.TypeOf(current)
		if currentType != nil && currentType.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
			classes = appendTaskErrorClass(classes, current)
		}
		if projected, ok := current.(*taskPublicError); ok && projected != nil {
			for _, class := range projected.classes {
				classes = appendTaskErrorClass(classes, class)
			}
		}
		children := trustedTaskErrorChildren(current)
		remaining := maximumTaskErrorGraphNodes - visited - 1 - len(pending)
		if remaining < 0 {
			remaining = 0
		}
		if len(children) > remaining {
			children = children[:remaining]
		}
		for index := len(children) - 1; index >= 0; index-- {
			pending = append(pending, children[index])
		}
	}
	return classes
}

func appendTaskErrorClass(classes []error, candidate error) []error {
	if candidate == nil || len(classes) >= maximumTaskErrorGraphNodes {
		return classes
	}
	candidateType := reflect.TypeOf(candidate)
	if candidateType == nil || !candidateType.Comparable() {
		return classes
	}
	for _, existing := range classes {
		if existing == candidate {
			return classes
		}
	}
	return append(classes, candidate)
}

func trustedTaskErrorChildren(err error) []error {
	switch typed := err.(type) {
	case *os.PathError:
		if typed == nil {
			return nil
		}
		return []error{typed.Err}
	case *os.LinkError:
		if typed == nil {
			return nil
		}
		return []error{typed.Err}
	case *os.SyscallError:
		if typed == nil {
			return nil
		}
		return []error{typed.Err}
	}
	switch reflect.TypeOf(err) {
	case trustedTaskSingleWrapType:
		return []error{err.(interface{ Unwrap() error }).Unwrap()}
	case trustedTaskMultiWrapType, trustedTaskJoinType:
		return err.(interface{ Unwrap() []error }).Unwrap()
	}
	return nil
}

// Poll reads output by byte offset and can wait for output growth or terminal
// state. Timeout is not task failure.
func (m *Manager) Poll(ctx context.Context, id ID, offset int64, block bool, timeout time.Duration) (result PollResult, resultErr error) {
	if m.hostCallbackBusy() {
		return PollResult{}, ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	if ctx == nil {
		return PollResult{}, errors.New("task poll context is nil")
	}
	if offset < 0 {
		return PollResult{}, errors.New("offset must be non-negative")
	}
	if timeout < 0 || timeout > 10*time.Minute {
		return PollResult{}, errors.New("timeout must be between zero and ten minutes")
	}
	var deadline *time.Timer
	var deadlineC <-chan time.Time
	if block && timeout > 0 {
		deadline = time.NewTimer(timeout)
		deadlineC = deadline.C
		defer func() {
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
		}()
	}
	for {
		if err := m.validateOutputDirectory(); err != nil {
			return PollResult{}, err
		}
		record, err := m.Get(id)
		if err != nil {
			return PollResult{}, err
		}
		expected := filepath.Join(m.outputDir, string(id)+".log")
		if filepath.Clean(record.OutputPath) != expected {
			return PollResult{}, errors.New("task output path escaped its owning directory")
		}
		m.mu.RLock()
		identity := m.outputIdentity[id]
		m.mu.RUnlock()
		content, next, err := readDelta(record.OutputPath, offset, defaultReadLimit, m.outputCap, identity)
		if err != nil {
			return PollResult{}, err
		}
		if content != "" || record.Status.Terminal() || !block || timeout == 0 {
			return PollResult{Task: record, Output: content, NextOffset: next}, nil
		}
		m.mu.RLock()
		live := m.live[id]
		m.mu.RUnlock()
		var done <-chan struct{}
		if live != nil {
			done = live.done
		}
		tick := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			tick.Stop()
			return PollResult{}, ctx.Err()
		case <-deadlineC:
			tick.Stop()
			record, getErr := m.Get(id)
			if getErr != nil {
				return PollResult{}, getErr
			}
			return PollResult{Task: record, NextOffset: offset, TimedOut: true}, nil
		case <-done:
			tick.Stop()
		case <-tick.C:
		}
	}
}

func readDelta(path string, offset int64, limit int64, outputCap int64, expected os.FileInfo) (string, int64, error) {
	before, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return "", offset, nil
	}
	if statErr != nil {
		return "", offset, fmt.Errorf("inspect task output: %w", statErr)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > outputCap+int64(len(outputTruncMarker)) {
		return "", offset, errors.New("task output is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", offset, fmt.Errorf("open task output: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", offset, errors.New("task output changed while opening")
	}
	if expected != nil && !os.SameFile(expected, after) {
		return "", offset, errors.New("task output identity changed")
	}
	links, err := openedFileLinkCount(file, after)
	if err != nil {
		return "", offset, fmt.Errorf("verify task output link count: %w", err)
	}
	if links != 1 {
		return "", offset, errors.New("task output must have exactly one filesystem link")
	}
	if after.Size() > outputCap+int64(len(outputTruncMarker)) {
		return "", offset, errors.New("task output exceeded its configured bound")
	}
	if offset > after.Size() {
		return "", offset, errors.New("offset exceeds task output size")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", offset, fmt.Errorf("seek task output: %w", err)
	}
	b, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", offset, fmt.Errorf("read task output: %w", err)
	}
	return string(b), offset + int64(len(b)), nil
}
