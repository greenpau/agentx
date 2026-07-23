package platform

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"
)

var errShutdownCallbackPanicked = errors.New("shutdown callback panicked")

const (
	DefaultTerminalRestoreTimeout = 500 * time.Millisecond
	DefaultCriticalCleanupTimeout = 2 * time.Second
	DefaultHookTimeout            = 1500 * time.Millisecond
	DefaultObserverTimeout        = 500 * time.Millisecond
)

// ShutdownStage defines the irreversible process-exit ordering.
type ShutdownStage string

const (
	StageTerminal ShutdownStage = "terminal_restore"
	StageCritical ShutdownStage = "critical_cleanup"
	StageHook     ShutdownStage = "session_end_hooks"
	StageObserver ShutdownStage = "final_observers"
)

// ShutdownRequest is latched from the first call. Later requests never replace
// its reason or code.
type ShutdownRequest struct {
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason"`
}

// ShutdownConfig bounds every stage independently. Callbacks that ignore
// cancellation may continue in the background, but cannot block later phases.
type ShutdownConfig struct {
	TerminalTimeout time.Duration
	CriticalTimeout time.Duration
	HookTimeout     time.Duration
	ObserverTimeout time.Duration
}

func (c ShutdownConfig) normalized() ShutdownConfig {
	if c.TerminalTimeout <= 0 {
		c.TerminalTimeout = DefaultTerminalRestoreTimeout
	}
	if c.CriticalTimeout <= 0 {
		c.CriticalTimeout = DefaultCriticalCleanupTimeout
	}
	if c.HookTimeout <= 0 {
		c.HookTimeout = DefaultHookTimeout
	}
	if c.ObserverTimeout <= 0 {
		c.ObserverTimeout = DefaultObserverTimeout
	}
	return c
}

// ShutdownFunc is independently idempotent and cancellation-aware.
type ShutdownFunc func(context.Context, ShutdownRequest) error

// PhaseError is a deterministic, secret-safe cleanup diagnostic.
type PhaseError struct {
	Stage    ShutdownStage `json:"stage"`
	Name     string        `json:"name"`
	Message  string        `json:"message"`
	TimedOut bool          `json:"timed_out,omitempty"`
}

// ShutdownResult is the inspectable terminal evidence for one shutdown run.
type ShutdownResult struct {
	Request    ShutdownRequest `json:"request"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Errors     []PhaseError    `json:"errors,omitempty"`
}

type registeredShutdown struct {
	sequence uint64
	name     string
	fn       ShutdownFunc
}

// ShutdownManager owns one first-call-wins sequence. Registration is set-like
// by stage and name, and registration changes after a stage snapshot do not
// affect that stage's current run.
type ShutdownManager struct {
	config ShutdownConfig

	mu      sync.Mutex
	next    uint64
	entries map[ShutdownStage]map[string]registeredShutdown
	started bool
	request ShutdownRequest
	done    chan struct{}
	result  ShutdownResult
}

// NewShutdownManager creates a dormant registry and starts no goroutines.
func NewShutdownManager(config ShutdownConfig) *ShutdownManager {
	return &ShutdownManager{
		config:  config.normalized(),
		entries: make(map[ShutdownStage]map[string]registeredShutdown),
		done:    make(chan struct{}),
	}
}

// Register adds a named callback. A duplicate stage/name replaces nothing and
// returns an unregistration function for the existing set entry.
func (m *ShutdownManager) Register(stage ShutdownStage, name string, fn ShutdownFunc) (func(), error) {
	if !validShutdownStage(stage) {
		return nil, fmt.Errorf("invalid shutdown stage %q", stage)
	}
	if name == "" || fn == nil {
		return nil, errors.New("shutdown registration requires name and callback")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil, errors.New("shutdown already started")
	}
	if m.entries[stage] == nil {
		m.entries[stage] = make(map[string]registeredShutdown)
	}
	if _, exists := m.entries[stage][name]; !exists {
		m.next++
		m.entries[stage][name] = registeredShutdown{sequence: m.next, name: name, fn: fn}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.started {
				delete(m.entries[stage], name)
			}
		})
	}, nil
}

// Begin latches request on its first invocation and starts exactly one bounded
// sequence. The returned channel is shared by every caller and closes once the
// result is available.
func (m *ShutdownManager) Begin(request ShutdownRequest) <-chan struct{} {
	m.mu.Lock()
	if m.started {
		done := m.done
		m.mu.Unlock()
		return done
	}
	m.started = true
	m.request = request
	m.mu.Unlock()
	go m.run()
	return m.done
}

// Shutdown begins shutdown if necessary and waits without transferring
// cleanup ownership to the caller's context. Cancelling the wait does not stop
// the already-owned process cleanup.
func (m *ShutdownManager) Shutdown(ctx context.Context, request ShutdownRequest) (ShutdownResult, error) {
	done := m.Begin(request)
	select {
	case <-done:
		return m.Result(), nil
	case <-ctx.Done():
		return ShutdownResult{}, fmt.Errorf("wait for shutdown: %w", ctx.Err())
	}
}

// Result returns the completed result, or the latched request while work is in
// flight. Callers use Done to distinguish the two states.
func (m *ShutdownManager) Result() ShutdownResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.result.StartedAt.IsZero() {
		return ShutdownResult{Request: m.request}
	}
	result := m.result
	result.Errors = append([]PhaseError(nil), result.Errors...)
	return result
}

func (m *ShutdownManager) run() {
	started := time.Now()
	request := m.latchedRequest()
	errorsFound := make([]PhaseError, 0)
	for _, phase := range []struct {
		stage   ShutdownStage
		timeout time.Duration
	}{
		{StageTerminal, m.config.TerminalTimeout},
		{StageCritical, m.config.CriticalTimeout},
		{StageHook, m.config.HookTimeout},
		{StageObserver, m.config.ObserverTimeout},
	} {
		errorsFound = append(errorsFound, m.runPhase(phase.stage, phase.timeout, request)...)
	}
	m.mu.Lock()
	m.result = ShutdownResult{
		Request: request, StartedAt: started, FinishedAt: time.Now(), Errors: errorsFound,
	}
	close(m.done)
	m.mu.Unlock()
}

func (m *ShutdownManager) runPhase(stage ShutdownStage, timeout time.Duration, request ShutdownRequest) []PhaseError {
	entries := m.snapshot(stage)
	if len(entries) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	type outcome struct {
		entry registeredShutdown
		err   error
	}
	results := make(chan outcome, len(entries))
	for _, entry := range entries {
		entry := entry
		go func() {
			results <- outcome{entry: entry, err: invokeShutdownCallback(entry.fn, ctx, request)}
		}()
	}
	remaining := len(entries)
	byName := make(map[string]PhaseError)
	completed := make(map[string]bool, len(entries))
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			completed[result.entry.name] = true
			if result.err != nil {
				byName[result.entry.name] = classifyShutdownError(stage, result.entry.name, result.err, ctx.Err())
			}
		case <-ctx.Done():
			for _, entry := range entries {
				if !completed[entry.name] {
					byName[entry.name] = PhaseError{Stage: stage, Name: entry.name, Message: "shutdown phase timed out", TimedOut: true}
				}
			}
			remaining = 0
		}
	}
	result := make([]PhaseError, 0, len(byName))
	for _, entry := range entries {
		if item, ok := byName[entry.name]; ok {
			result = append(result, item)
		}
	}
	return result
}

func invokeShutdownCallback(fn ShutdownFunc, ctx context.Context, request ShutdownRequest) (err error) {
	defer func() {
		if recover() != nil {
			err = errShutdownCallbackPanicked
		}
	}()
	return fn(ctx, request)
}

func classifyShutdownError(stage ShutdownStage, name string, err error, contextErrors ...error) PhaseError {
	result := PhaseError{Stage: stage, Name: name, Message: "shutdown callback failed"}
	var contextErr error
	if len(contextErrors) > 0 {
		contextErr = contextErrors[0]
	}
	classification := inspectShutdownError(err, contextErr)
	switch {
	case classification.panicked:
		result.Message = "shutdown callback panicked"
	case classification.deadline:
		result.Message = "shutdown callback timed out"
		result.TimedOut = true
	case classification.cancelled:
		result.Message = "shutdown callback cancelled"
	}
	return result
}

type shutdownErrorInspection struct {
	panicked  bool
	deadline  bool
	cancelled bool
}

// inspectShutdownError retains only exact host sentinels and the terminal
// state of the shutdown phase's owned context. It never invokes Error, Is, As,
// or Unwrap on a callback-owned error.
func inspectShutdownError(err, contextErr error) shutdownErrorInspection {
	var result shutdownErrorInspection
	if exactShutdownError(err, errShutdownCallbackPanicked) {
		result.panicked = true
	}
	if exactShutdownError(err, context.DeadlineExceeded) ||
		exactShutdownError(contextErr, context.DeadlineExceeded) {
		result.deadline = true
	}
	if exactShutdownError(err, context.Canceled) ||
		exactShutdownError(contextErr, context.Canceled) {
		result.cancelled = true
	}
	return result
}

func exactShutdownError(err, target error) bool {
	typ := reflect.TypeOf(err)
	return err != nil && target != nil && typ != nil && typ.Comparable() && err == target
}

func (m *ShutdownManager) snapshot(stage ShutdownStage) []registeredShutdown {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]registeredShutdown, 0, len(m.entries[stage]))
	for _, entry := range m.entries[stage] {
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].sequence < items[j].sequence })
	return items
}

func (m *ShutdownManager) latchedRequest() ShutdownRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.request
}

func validShutdownStage(stage ShutdownStage) bool {
	return stage == StageTerminal || stage == StageCritical || stage == StageHook || stage == StageObserver
}
