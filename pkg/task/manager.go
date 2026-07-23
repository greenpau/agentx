package task

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
)

const (
	stateVersion  = 1
	stateFilename = "state.json"
	outputDirname = "tasks"
	// DefaultOutputReadBytes is the maximum byte delta returned by one poll.
	DefaultOutputReadBytes = int64(8 << 20)
	// MaximumOutputBytes is the durable payload cap before the truncation marker.
	MaximumOutputBytes = int64(5 << 30)
	outputTruncMarker  = "\n[task output truncated at configured disk limit]\n"
	// MaximumOutputFileBytes includes the sole marker appended at the payload cap.
	MaximumOutputFileBytes = MaximumOutputBytes + int64(len(outputTruncMarker))
	defaultReadLimit       = DefaultOutputReadBytes
	defaultOutputCap       = MaximumOutputBytes
	maximumOutputCap       = MaximumOutputBytes
	maximumStateBytes      = 16 << 20
	maximumTaskRecords     = 1_024
	maximumWorkRecords     = 4_096
	maximumTodos           = 1_024
	maximumStateString     = 1 << 20
	maximumToolUseID       = 256
	maximumMetadata        = 256
	maximumMetadataKey     = 256
	maximumMetadataVal     = 16 << 10
	shutdownWait           = 3 * time.Second
	processWaitDelay       = time.Second
	stateReadLockRetry     = time.Millisecond
)

var (
	errTaskPersistenceCallbackFailed = errors.New("task persistence callback failed")
	errTaskSignalCallbackFailed      = errors.New("task signal callback failed")
)

// OutputSanitizer filters one ordered task-output stream. Write may retain a
// bounded suffix; Flush returns the final safe suffix and permanently closes
// the filter. TruncationMarker returns terminal framing that can follow any
// prefix of previously returned output without reconstructing a configured
// credential; it may return empty to omit visible framing. Implementations
// must never return configured credential material.
type OutputSanitizer interface {
	Write(string) string
	Flush() string
	TruncationMarker() string
}

// Options controls the task store. Clock, Random, and NewOutputSanitizer are
// test or composition seams. NewOutputSanitizer must return fresh state for
// each task because stdout and stderr share one ordered stream per process.
type Options struct {
	Clock              func() time.Time
	Random             io.Reader
	OutputCap          int64
	NewOutputSanitizer func() OutputSanitizer
	// SanitizeRecord projects command and description into durable/display
	// state without changing the raw authorized process command.
	SanitizeRecord func(string) (string, bool)
	// ValidateState inspects one complete encoded state document after JSON
	// framing. Returning an error prevents the atomic replacement.
	ValidateState func([]byte) error
}

type persistedState struct {
	Version int             `json:"version"`
	Tasks   map[ID]Record   `json:"tasks"`
	Work    map[ID]WorkItem `json:"work"`
	Todos   []Todo          `json:"todos,omitempty"`
}

type liveTask struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	ctx    context.Context
	done   chan struct{}
	signal func() error
	output *os.File

	// stopRequested and launchCleanup are protected by Manager.mu. The process
	// waiter publishes terminalErr before closing done.
	stopRequested bool
	launchCleanup bool
	terminalErr   error

	fileMu       sync.Mutex
	size         int64
	capped       bool
	outputCap    int64
	sanitizer    OutputSanitizer
	truncMarker  string
	outputErr    error
	outputFailed bool
}

// Manager coordinates task state, process-local resources, and restrictive
// output files. Its atomic state journal is a deliberate durability-strengthening
// divergence: the reference runtime keeps most local task state in memory.
type Manager struct {
	mu                 sync.RWMutex
	callbackActive     atomic.Bool
	root               string
	outputDir          string
	statePath          string
	clock              func() time.Time
	random             io.Reader
	outputCap          int64
	newOutputSanitizer func() OutputSanitizer
	sanitizeRecord     func(string) (string, bool)
	validateEncoding   func([]byte) error
	rootOwner          *platform.OwnedDirectory
	outputOwner        *platform.OwnedDirectory
	tasks              map[ID]Record
	work               map[ID]WorkItem
	todos              []Todo
	live               map[ID]*liveTask
	// outputIdentity pins files created in this process. Durable recovery can
	// still verify regular-file and single-link properties, while live writes
	// use the retained handle and never reopen an attacker-swappable pathname.
	outputIdentity map[ID]os.FileInfo
	closed         bool
	closeDone      chan struct{}
	closeErr       error
	dirty          bool

	// persistHook is an unexported fault-injection seam used by lifecycle tests.
	// It executes under mu before a state-file transaction begins.
	persistHook func() error
}

// Open creates or restores a task manager below root.
func Open(root string, options Options) (manager *Manager, resultErr error) {
	defer func() {
		resultErr = sanitizeTaskPublicError(options.SanitizeRecord, resultErr)
	}()
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("task root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve task root: %w", err)
	}
	rootOwner, err := platform.AcquirePrivateDirectory(abs)
	if err != nil {
		return nil, fmt.Errorf("acquire task root: %w", err)
	}
	abs = rootOwner.Path()
	outputOwner, err := rootOwner.EnsurePrivateChild(outputDirname)
	if err != nil {
		return nil, fmt.Errorf("create task output directory: %w", err)
	}
	out := outputOwner.Path()
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	clock = containedClock(clock)
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	cap := options.OutputCap
	if cap <= 0 {
		cap = defaultOutputCap
	}
	if cap > maximumOutputCap {
		return nil, fmt.Errorf("task output cap exceeds %d bytes", maximumOutputCap)
	}
	m := &Manager{
		root:               abs,
		outputDir:          out,
		statePath:          filepath.Join(abs, stateFilename),
		clock:              clock,
		random:             random,
		outputCap:          cap,
		newOutputSanitizer: options.NewOutputSanitizer,
		sanitizeRecord:     options.SanitizeRecord,
		validateEncoding:   options.ValidateState,
		rootOwner:          rootOwner,
		outputOwner:        outputOwner,
		tasks:              make(map[ID]Record),
		work:               make(map[ID]WorkItem),
		live:               make(map[ID]*liveTask),
		outputIdentity:     make(map[ID]os.FileInfo),
		closeDone:          make(chan struct{}),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func containedClock(clock func() time.Time) func() time.Time {
	return func() (now time.Time) {
		defer func() {
			if recover() != nil || now.IsZero() {
				// Clock is a composition/test seam, not permission to strand a
				// task or crash a completion goroutine. Wall time is the safe
				// degraded source when that seam fails.
				now = time.Now()
			}
		}()
		return clock()
	}
}

// beginHostCallback is the task-runtime counterpart to the query engine's
// callback recursion guard. Public entrypoints check callbackActive before
// taking Manager.mu, so a host seam can inspect or mutate through the manager
// only by receiving ErrBusy (or an empty snapshot for legacy slice-only
// accessors) rather than deadlocking on the mutex already held by its caller.
func (m *Manager) beginHostCallback() bool {
	return m != nil && m.callbackActive.CompareAndSwap(false, true)
}

func (m *Manager) endHostCallback() {
	m.callbackActive.Store(false)
}

func (m *Manager) hostCallbackBusy() bool {
	return m != nil && m.callbackActive.Load()
}

func (m *Manager) acquireReadLock(ctx context.Context) error {
	for {
		if m.hostCallbackBusy() {
			return ErrBusy
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.mu.TryRLock() {
			if m.hostCallbackBusy() {
				m.mu.RUnlock()
				return ErrBusy
			}
			return nil
		}
		timer := time.NewTimer(stateReadLockRetry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) currentTime() time.Time {
	if !m.beginHostCallback() {
		return time.Now()
	}
	defer m.endHostCallback()
	return m.clock()
}

func (m *Manager) sanitizePublicError(err error) error {
	if err == nil {
		return nil
	}
	if m.sanitizeRecord == nil {
		return sanitizeTaskPublicError(nil, err)
	}
	if !m.beginHostCallback() {
		return ErrBusy
	}
	defer m.endHostCallback()
	return sanitizeTaskPublicError(m.sanitizeRecord, err)
}

func (m *Manager) load() error {
	if err := m.verifyOwnedDirectories(); err != nil {
		return err
	}
	b, err := readBoundedState(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return m.verifyOwnedDirectories()
	}
	if err != nil {
		return fmt.Errorf("read task state: %w", err)
	}
	if err := m.validateEncodedState(b); err != nil {
		// The existing journal predates this manager instance. Do not decode
		// and publish a document that the current complete-state safety
		// boundary rejects.
		return errors.New("existing task state failed safety validation")
	}
	if err := m.verifyOwnedDirectories(); err != nil {
		return err
	}
	var state persistedState
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode task state: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("decode task state: trailing JSON value")
	}
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported task state version %d", state.Version)
	}
	if state.Tasks != nil {
		m.tasks = state.Tasks
	}
	if state.Work != nil {
		m.work = state.Work
	}
	m.todos = append([]Todo(nil), state.Todos...)
	if err := m.validateLoadedState(); err != nil {
		return err
	}
	if err := m.validateRecoveredOutputs(); err != nil {
		return err
	}

	// A local process handle cannot be reconstructed safely. Preserve evidence,
	// but never claim that an old process is still running or replay it.
	now := m.currentTime().UTC()
	changed := false
	for id, record := range m.tasks {
		if record.Status == StatusPending || record.Status == StatusRunning {
			record.Status = StatusFailed
			record.Error = m.sanitizeError("interrupted by runtime restart; side effects were not replayed")
			record.EndedAt = &now
			_ = m.publishTerminalRecordLocked(id, record)
			changed = true
		}
	}
	if changed {
		return m.persistLocked()
	}
	return nil
}

func (m *Manager) persistLocked() error {
	normalizedWork, err := m.validateState(m.tasks, m.work, m.todos)
	if err != nil {
		return fmt.Errorf("validate task state: %w", err)
	}
	m.work = normalizedWork
	if m.persistHook != nil {
		if err := m.callPersistHook(); err != nil {
			return fmt.Errorf("persist task state: %w", err)
		}
	}
	if err := m.verifyOwnedDirectories(); err != nil {
		return err
	}
	state := persistedState{Version: stateVersion, Tasks: m.tasks, Work: m.work, Todos: m.todos}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task state: %w", err)
	}
	if err := m.validateEncodedState(b); err != nil {
		return fmt.Errorf("validate encoded task state: %w", err)
	}
	if len(b) > maximumStateBytes {
		return fmt.Errorf("encoded task state exceeds %d bytes", maximumStateBytes)
	}
	tmp, err := os.CreateTemp(m.root, ".state-*")
	if err != nil {
		return fmt.Errorf("create task state temporary file: %w", err)
	}
	tmpName := tmp.Name()
	tmpIdentity, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("inspect created task state temporary file: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			_ = removeOutputIfSame(tmpName, tmpIdentity)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure task state temporary file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write task state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync task state: %w", err)
	}
	tmpInfo, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("inspect task state temporary file: %w", err)
	}
	if !os.SameFile(tmpIdentity, tmpInfo) {
		_ = tmp.Close()
		return errors.New("task state temporary file identity changed while writing")
	}
	if err := m.verifyOwnedDirectories(); err != nil {
		_ = tmp.Close()
		return err
	}
	tmpPathInfo, err := os.Lstat(tmpName)
	if err != nil || !tmpPathInfo.Mode().IsRegular() || tmpPathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(tmpInfo, tmpPathInfo) {
		_ = tmp.Close()
		return errors.New("task state temporary file identity changed")
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close task state: %w", err)
	}
	if existing, statErr := os.Lstat(m.statePath); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 {
			return errors.New("task state destination is not a direct regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect task state destination: %w", statErr)
	}
	if err := m.verifyOwnedDirectories(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.statePath); err != nil {
		return fmt.Errorf("replace task state: %w", err)
	}
	activated, err := os.Lstat(m.statePath)
	if err != nil || !activated.Mode().IsRegular() || !os.SameFile(tmpInfo, activated) {
		return errors.New("task state identity changed during activation")
	}
	remove = false
	if err := m.verifyOwnedDirectories(); err != nil {
		m.dirty = true
		return err
	}
	// Sync the containing directory after rename where the platform exposes
	// that durability primitive. Without this boundary, a power loss can retain
	// the file contents but forget the directory entry replacement.
	if err := syncTaskDirectory(m.root); err != nil {
		m.dirty = true
		return fmt.Errorf("sync task state directory: %w", err)
	}
	if err := m.verifyOwnedDirectories(); err != nil {
		m.dirty = true
		return err
	}
	m.dirty = false
	return nil
}

func (m *Manager) callPersistHook() (err error) {
	if !m.beginHostCallback() {
		return ErrBusy
	}
	defer m.endHostCallback()
	defer func() {
		if recover() != nil {
			err = errors.New("task persistence callback panicked")
		}
	}()
	if err := m.persistHook(); err != nil {
		return errTaskPersistenceCallbackFailed
	}
	return nil
}

// persistTerminalLocked retries once because a terminal in-memory state has no
// live process left to recreate it. If both commits fail, dirty records that
// Close or a later successful mutation must attempt the journal again.
func (m *Manager) persistTerminalLocked() error {
	first := m.persistLocked()
	if first == nil {
		return nil
	}
	second := m.persistLocked()
	if second == nil {
		return nil
	}
	m.dirty = true
	return errors.Join(first, second)
}

// publishTerminalRecordLocked validates a candidate before it becomes visible
// through Get, List, or Poll. If the full encoded document rejects newly added
// terminal payload, the manager first suppresses optional terminal evidence
// without changing stable task or tool correlation. If even the minimal record
// is unsafe, the previously validated record remains authoritative.
func (m *Manager) publishTerminalRecordLocked(id ID, record Record) error {
	candidate := cloneTaskRecords(m.tasks)
	candidate[id] = cloneRecord(record)
	if err := m.preflightStateEncoding(candidate); err == nil {
		m.tasks[id] = cloneRecord(record)
		return nil
	}

	suppressed := cloneRecord(record)
	suppressed.Error = ""
	suppressed.OutputIncomplete = false
	suppressed.OutputWarning = ""
	if suppressed.Status != StatusCompleted {
		suppressed.ExitCode = nil
	}
	candidate[id] = suppressed
	if err := m.preflightStateEncoding(candidate); err == nil {
		m.tasks[id] = suppressed
		return errors.New("terminal task payload was suppressed by safety validation")
	}

	return errors.New("terminal task record was rejected by safety validation")
}

func cloneTaskRecords(source map[ID]Record) map[ID]Record {
	result := make(map[ID]Record, len(source))
	for id, record := range source {
		result[id] = cloneRecord(record)
	}
	return result
}

func (m *Manager) preflightStateEncoding(tasks map[ID]Record) error {
	normalizedWork, err := m.validateState(tasks, m.work, m.todos)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(persistedState{
		Version: stateVersion, Tasks: tasks, Work: normalizedWork, Todos: m.todos,
	}, "", "  ")
	if err != nil {
		return errors.New("encode task state preflight")
	}
	if len(encoded) > maximumStateBytes {
		return errors.New("encoded task state exceeds its byte limit")
	}
	if err := m.validateEncodedState(encoded); err != nil {
		return errors.New("encoded task state failed safety validation")
	}
	return nil
}

// validateEncodedState contains host validator failures at the task boundary.
// Validator errors and panic payloads can be credential-bearing, so callers
// receive only a fixed classification.
func (m *Manager) validateEncodedState(encoded []byte) (err error) {
	if m.validateEncoding == nil {
		return nil
	}
	if !m.beginHostCallback() {
		return ErrBusy
	}
	defer m.endHostCallback()
	defer func() {
		if recover() != nil {
			err = errors.New("task state safety validator panicked")
		}
	}()
	if m.validateEncoding(append([]byte(nil), encoded...)) != nil {
		return errors.New("task state failed safety validation")
	}
	return nil
}

// validateRecoveredOutputs replays each existing output through a fresh
// configured sanitizer before the manager is returned. Comparing the complete
// raw and projected streams lets already-safe logs resume without rewriting
// them while quarantining a legacy credential-bearing log from all public task
// APIs.
func (m *Manager) validateRecoveredOutputs() error {
	if m.newOutputSanitizer == nil {
		return nil
	}
	ids := make([]ID, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		record := m.tasks[id]
		info, err := inspectOutputFile(record.OutputPath, m.outputCap)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("existing task output failed safety validation")
		}
		file, err := os.Open(record.OutputPath)
		if err != nil {
			return errors.New("existing task output failed safety validation")
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = file.Close()
			return errors.New("existing task output failed safety validation")
		}
		sanitizer, _, createErr := m.createOutputSanitizer()
		if createErr != nil {
			_ = file.Close()
			return errors.New("existing task output failed safety validation")
		}
		rawHash := sha256.New()
		safeHash := sha256.New()
		var rawBytes, safeBytes int64
		buffer := make([]byte, 64<<10)
		for {
			count, readErr := file.Read(buffer)
			if count > 0 {
				chunk := buffer[:count]
				_, _ = rawHash.Write(chunk)
				rawBytes += int64(count)
				safe, sanitizeErr := m.sanitizeOutput(sanitizer, string(chunk), false)
				if sanitizeErr != nil {
					_ = file.Close()
					return errors.New("existing task output failed safety validation")
				}
				_, _ = safeHash.Write([]byte(safe))
				safeBytes += int64(len(safe))
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return errors.New("existing task output failed safety validation")
			}
		}
		tail, flushErr := m.sanitizeOutput(sanitizer, "", true)
		_, _ = safeHash.Write([]byte(tail))
		safeBytes += int64(len(tail))
		closeErr := file.Close()
		current, pathErr := os.Lstat(record.OutputPath)
		if flushErr != nil || closeErr != nil || pathErr != nil ||
			!current.Mode().IsRegular() || !os.SameFile(opened, current) ||
			current.Size() != opened.Size() || !current.ModTime().Equal(opened.ModTime()) ||
			rawBytes != safeBytes || !bytes.Equal(rawHash.Sum(nil), safeHash.Sum(nil)) {
			return errors.New("existing task output failed safety validation")
		}
	}
	return nil
}

func readBoundedState(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maximumStateBytes {
		return nil, errors.New("task state is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("task state changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maximumStateBytes {
		return nil, errors.New("task state exceeds byte limit")
	}
	post, err := os.Lstat(path)
	if err != nil || !post.Mode().IsRegular() || !os.SameFile(after, post) || post.Size() != after.Size() || !post.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("task state changed while reading")
	}
	return b, nil
}

func (m *Manager) validateLoadedState() error {
	normalizedWork, err := m.validateState(m.tasks, m.work, m.todos)
	if err != nil {
		return err
	}
	m.work = normalizedWork
	return nil
}

func validPersistedID(id ID, prefix byte) bool {
	value := string(id)
	if len(value) != 9 || value[0] != prefix {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !(value[index] >= '0' && value[index] <= '9' || value[index] >= 'a' && value[index] <= 'z') {
			return false
		}
	}
	return true
}

func validTaskStatus(status Status) bool {
	return status == StatusPending || status == StatusRunning || status == StatusCompleted || status == StatusFailed || status == StatusKilled
}

func (m *Manager) nextIDLocked(prefix byte) (ID, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 8)
	for attempts := 0; attempts < 32; attempts++ {
		if err := m.readTaskRandom(buf); err != nil {
			return "", err
		}
		for i := range buf {
			buf[i] = alphabet[int(buf[i])%len(alphabet)]
		}
		id := ID(string(prefix) + string(buf))
		if _, exists := m.tasks[id]; exists {
			continue
		}
		if _, exists := m.work[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("could not generate unique task identifier")
}

func (m *Manager) readTaskRandom(target []byte) error {
	if !m.beginHostCallback() {
		return ErrBusy
	}
	defer m.endHostCallback()
	return readTaskRandom(m.random, target)
}

func readTaskRandom(random io.Reader, target []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("generate task identifier")
		}
	}()
	if _, err := io.ReadFull(random, target); err != nil {
		return errors.New("generate task identifier")
	}
	return nil
}

func cloneRecord(record Record) Record {
	if record.ExitCode != nil {
		exitCode := *record.ExitCode
		record.ExitCode = &exitCode
	}
	if record.EndedAt != nil {
		endedAt := *record.EndedAt
		record.EndedAt = &endedAt
	}
	return record
}

func cloneWork(item WorkItem) WorkItem {
	item.Blockers = append([]ID(nil), item.Blockers...)
	item.Dependents = append([]ID(nil), item.Dependents...)
	if item.Metadata != nil {
		metadata := make(map[string]string, len(item.Metadata))
		for key, value := range item.Metadata {
			metadata[key] = value
		}
		item.Metadata = metadata
	}
	return item
}

// Get returns an immutable copy of current task state.
func (m *Manager) Get(id ID) (record Record, resultErr error) {
	if m.hostCallbackBusy() {
		return Record{}, ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.tasks[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

// List returns tasks in deterministic start-time and ID order.
func (m *Manager) List() []Record {
	if m.hostCallbackBusy() {
		return nil
	}
	m.mu.RLock()
	items := cloneTaskItems(m.tasks)
	m.mu.RUnlock()
	sortTaskItems(items)
	return items
}

// ListContext returns deterministic task summaries through an error-bearing
// boundary suitable for tool adapters. It preserves List's snapshot contents
// while making callback contention and cancellation observable.
func (m *Manager) ListContext(ctx context.Context) (items []Record, resultErr error) {
	if m.hostCallbackBusy() {
		return nil, ErrBusy
	}
	defer func() {
		if resultErr != ErrBusy {
			resultErr = m.sanitizePublicError(resultErr)
		}
	}()
	if ctx == nil {
		return nil, errors.New("task list context is nil")
	}
	if err := m.acquireReadLock(ctx); err != nil {
		return nil, err
	}
	items = cloneTaskItems(m.tasks)
	m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortTaskItems(items)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func cloneTaskItems(source map[ID]Record) []Record {
	items := make([]Record, 0, len(source))
	for _, record := range source {
		items = append(items, cloneRecord(record))
	}
	return items
}

func sortTaskItems(items []Record) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].StartedAt.Before(items[j].StartedAt)
	})
}

// Close cancels every owned live process. It is idempotent.
func (m *Manager) Close() error {
	return m.CloseContext(context.Background())
}

// CloseContext cancels every owned live process while respecting an outer
// shutdown-stage deadline. All signals are issued before waiting, so returning
// on deadline cannot leave a task that was never asked to stop.
func (m *Manager) CloseContext(ctx context.Context) (resultErr error) {
	if m.hostCallbackBusy() {
		return ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	if ctx == nil {
		return errors.New("task close context is nil")
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		select {
		case <-done:
			m.mu.RLock()
			err := m.closeErr
			m.mu.RUnlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.closed = true
	ids := make([]ID, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	type stopTarget struct {
		id   ID
		live *liveTask
	}
	targets := make([]stopTarget, 0, len(ids))
	var failures []error
	for _, id := range ids {
		live, claimed, err := m.beginStop(id, true)
		if err != nil && !errors.Is(err, ErrNotRunning) {
			failures = append(failures, fmt.Errorf("stop task %s: %w", id, err))
		}
		if live == nil {
			continue
		}
		targets = append(targets, stopTarget{id: id, live: live})
		if claimed {
			if err := callTaskSignal(live.signal); err != nil {
				failures = append(failures, fmt.Errorf("signal task %s: %w", id, err))
				if fallbackErr := stopProcess(live.cmd); fallbackErr != nil {
					failures = append(failures, fmt.Errorf("fallback signal task %s: %w", id, fallbackErr))
				}
				// Also notify the exec.Cmd context watcher. Direct group
				// termination above remains authoritative if a custom command
				// factory failed to bind this context as required.
				live.cancel()
			}
		}
	}
	deadline := time.Now().Add(shutdownWait)
	for _, target := range targets {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			failures = append(failures, fmt.Errorf("task %s: timed out waiting for termination", target.id))
			continue
		}
		timer := time.NewTimer(remaining)
		select {
		case <-target.live.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if target.live.terminalErr != nil {
				failures = append(failures, fmt.Errorf("finalize task %s: %w", target.id, target.live.terminalErr))
			}
		case <-timer.C:
			failures = append(failures, fmt.Errorf("task %s: timed out waiting for termination", target.id))
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			failures = append(failures, fmt.Errorf("task %s: shutdown deadline reached: %w", target.id, ctx.Err()))
		}
	}
	m.mu.Lock()
	if m.dirty {
		if err := m.persistLocked(); err != nil {
			failures = append(failures, fmt.Errorf("flush terminal task state: %w", err))
		}
	}
	result := errors.Join(failures...)
	m.closeErr = result
	close(m.closeDone)
	m.mu.Unlock()
	return result
}

func callTaskSignal(signal func() error) (err error) {
	if signal == nil {
		return errors.New("task signal callback is unavailable")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("task signal callback panicked")
		}
	}()
	if err := signal(); err != nil {
		return errTaskSignalCallbackFailed
	}
	return nil
}
