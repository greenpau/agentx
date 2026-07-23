package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type managerLifecycleProbe struct {
	connectFn    func(context.Context) error
	reconnectFn  func(context.Context) error
	closeFn      func() error
	stateFn      func() ConnectionState
	lastErrorFn  func() string
	generationFn func() uint64
	listToolsFn  func(context.Context) ([]ToolDescriptor, []Diagnostic, error)
	callToolFn   func(context.Context, string, map[string]any) (ToolResult, error)
	closeCalls   atomic.Int32
}

func (probe *managerLifecycleProbe) Connect(ctx context.Context) error {
	if probe.connectFn != nil {
		return probe.connectFn(ctx)
	}
	return nil
}

func (probe *managerLifecycleProbe) Reconnect(ctx context.Context) error {
	if probe.reconnectFn != nil {
		return probe.reconnectFn(ctx)
	}
	return nil
}

func (probe *managerLifecycleProbe) Close() error {
	probe.closeCalls.Add(1)
	if probe.closeFn != nil {
		return probe.closeFn()
	}
	return nil
}

func (probe *managerLifecycleProbe) State() ConnectionState {
	if probe.stateFn != nil {
		return probe.stateFn()
	}
	return StateConnected
}

func (probe *managerLifecycleProbe) LastError() string {
	if probe.lastErrorFn != nil {
		return probe.lastErrorFn()
	}
	return ""
}

func (probe *managerLifecycleProbe) Generation() uint64 {
	if probe.generationFn != nil {
		return probe.generationFn()
	}
	return 1
}

func (*managerLifecycleProbe) InitializeResult() InitializeResult {
	return InitializeResult{}
}

func (probe *managerLifecycleProbe) ListTools(ctx context.Context) ([]ToolDescriptor, []Diagnostic, error) {
	if probe.listToolsFn != nil {
		return probe.listToolsFn(ctx)
	}
	return []ToolDescriptor{{
		Name:        "mcp__probe__echo",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}, nil, nil
}

func (*managerLifecycleProbe) ListResources(context.Context) ([]ResourceDescriptor, []Diagnostic, error) {
	return nil, nil, nil
}

func (*managerLifecycleProbe) ListResourceTemplates(context.Context) ([]ResourceTemplate, []Diagnostic, error) {
	return nil, nil, nil
}

func (*managerLifecycleProbe) ListPrompts(context.Context) ([]PromptDescriptor, []Diagnostic, error) {
	return nil, nil, nil
}

func (probe *managerLifecycleProbe) CallTool(
	ctx context.Context, name string, arguments map[string]any,
) (ToolResult, error) {
	if probe.callToolFn != nil {
		return probe.callToolFn(ctx, name, arguments)
	}
	return ToolResult{}, nil
}

func (*managerLifecycleProbe) ReadResource(context.Context, string) (ResourceResult, error) {
	return ResourceResult{}, nil
}

func (*managerLifecycleProbe) GetPrompt(context.Context, string, map[string]string) (PromptResult, error) {
	return PromptResult{}, nil
}

func managerProbeConfig() Config {
	return Config{Name: "probe", Transport: TransportStdio, Command: "probe", Scope: ScopeUser}
}

func requireManagerCloseCompletes(t *testing.T, manager *Manager) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- manager.Close()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Manager.Close did not complete within its lifecycle bound")
		return nil
	}
}

func requireSnapshotResult(t *testing.T, done <-chan Snapshot) Snapshot {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not return after its provider callback was released")
		return Snapshot{}
	}
}

func TestManagerCloseBypassesNeverReturningLifecycleCallbacks(t *testing.T) {
	t.Run("factory", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerLifecycleProbe{}
		manager := NewManager(func(Config) (Connection, error) {
			close(entered)
			<-release
			return probe, nil
		})
		manager.closeTimeout = 20 * time.Millisecond
		reconcileDone := make(chan Snapshot, 1)
		go func() {
			reconcileDone <- manager.Reconcile(context.Background(), []Config{managerProbeConfig()})
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked factory: %v", err)
		}
		close(release)
		if snapshot := requireSnapshotResult(t, reconcileDone); len(snapshot.Servers) != 0 {
			t.Fatalf("blocked factory published after Close: %#v", snapshot)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("stale factory candidate close calls = %d, want 1", calls)
		}
	})

	t.Run("connect", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerLifecycleProbe{connectFn: func(context.Context) error {
			close(entered)
			<-release
			return nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		manager.closeTimeout = 20 * time.Millisecond
		reconcileDone := make(chan Snapshot, 1)
		go func() {
			reconcileDone <- manager.Reconcile(context.Background(), []Config{managerProbeConfig()})
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked Connect: %v", err)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("in-flight connection close calls = %d, want 1", calls)
		}
		close(release)
		if snapshot := requireSnapshotResult(t, reconcileDone); len(snapshot.Servers) != 0 {
			t.Fatalf("blocked Connect published after Close: %#v", snapshot)
		}
	})

	t.Run("reconnect", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerLifecycleProbe{reconnectFn: func(context.Context) error {
			close(entered)
			<-release
			return nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		manager.closeTimeout = 20 * time.Millisecond
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		reconnectDone := make(chan error, 1)
		go func() {
			reconnectDone <- manager.Reconnect(context.Background(), "probe")
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked Reconnect: %v", err)
		}
		close(release)
		select {
		case err := <-reconnectDone:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("stale Reconnect error = %v, want ErrClosed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Reconnect did not return after release")
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("reconnecting connection close calls = %d, want 1", calls)
		}
	})

	t.Run("state", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var block atomic.Bool
		var once sync.Once
		probe := &managerLifecycleProbe{stateFn: func() ConnectionState {
			if block.Load() {
				once.Do(func() { close(entered) })
				<-release
			}
			return StateConnected
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		manager.closeTimeout = 20 * time.Millisecond
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		block.Store(true)
		snapshotDone := make(chan Snapshot, 1)
		go func() {
			snapshotDone <- manager.Snapshot()
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked State: %v", err)
		}
		close(release)
		snapshot := requireSnapshotResult(t, snapshotDone)
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateClosed {
			t.Fatalf("Snapshot did not discard stale State result: %#v", snapshot)
		}
	})

	t.Run("generation", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		probe := &managerLifecycleProbe{generationFn: func() uint64 {
			once.Do(func() { close(entered) })
			<-release
			return 1
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		manager.closeTimeout = 20 * time.Millisecond
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		toolsDone := make(chan error, 1)
		go func() {
			_, _, err := manager.Tools(context.Background())
			toolsDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked Generation: %v", err)
		}
		close(release)
		select {
		case err := <-toolsDone:
			if !errors.Is(err, ErrStaleToolBinding) {
				t.Fatalf("stale tool discovery error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("tool discovery did not return after Generation release")
		}
	})
}

func TestManagerLifecycleCallbacksMayReenterClose(t *testing.T) {
	t.Run("factory", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		manager = NewManager(func(Config) (Connection, error) {
			if err := manager.Close(); err != nil {
				return nil, err
			}
			return probe, nil
		})
		done := make(chan Snapshot, 1)
		go func() {
			done <- manager.Reconcile(context.Background(), []Config{managerProbeConfig()})
		}()
		if snapshot := requireSnapshotResult(t, done); len(snapshot.Servers) != 0 {
			t.Fatalf("reentrant factory published after Close: %#v", snapshot)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("reentrant factory candidate close calls = %d, want 1", calls)
		}
	})

	t.Run("connect", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		probe.connectFn = func(context.Context) error {
			return manager.Close()
		}
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		done := make(chan Snapshot, 1)
		go func() {
			done <- manager.Reconcile(context.Background(), []Config{managerProbeConfig()})
		}()
		if snapshot := requireSnapshotResult(t, done); len(snapshot.Servers) != 0 {
			t.Fatalf("reentrant Connect published after Close: %#v", snapshot)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("reentrant Connect close calls = %d, want 1", calls)
		}
	})

	t.Run("reconnect", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		probe.reconnectFn = func(context.Context) error {
			return manager.Close()
		}
		if err := manager.Reconnect(t.Context(), "probe"); !errors.Is(err, ErrClosed) {
			t.Fatalf("reentrant Reconnect error = %v, want ErrClosed", err)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("reentrant Reconnect close calls = %d, want 1", calls)
		}
	})

	t.Run("state", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		var once sync.Once
		probe.stateFn = func() ConnectionState {
			once.Do(func() { _ = manager.Close() })
			return StateConnected
		}
		snapshot := manager.Snapshot()
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateClosed {
			t.Fatalf("reentrant State snapshot = %#v", snapshot)
		}
	})

	t.Run("generation", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		var once sync.Once
		probe.generationFn = func() uint64 {
			once.Do(func() { _ = manager.Close() })
			return 1
		}
		if _, _, err := manager.Tools(t.Context()); !errors.Is(err, ErrStaleToolBinding) {
			t.Fatalf("reentrant Generation discovery error = %v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		var manager *Manager
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		manager.closeTimeout = 20 * time.Millisecond
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("initial snapshot = %#v", snapshot)
		}
		probe.closeFn = func() error {
			return manager.Close()
		}
		err := requireManagerCloseCompletes(t, manager)
		if err == nil || err.Error() != "one or more MCP provider connections failed to close" {
			t.Fatalf("reentrant provider Close error = %v", err)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("reentrant provider Close calls = %d, want 1", calls)
		}
	})
}

func TestManagerContainsPanickingLifecycleCallbacks(t *testing.T) {
	t.Run("factory", func(t *testing.T) {
		manager := NewManager(func(Config) (Connection, error) {
			panic("private factory panic")
		})
		t.Cleanup(func() { _ = manager.Close() })
		snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()})
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateFailed ||
			len(snapshot.Servers[0].Diagnostics) != 1 {
			t.Fatalf("panicking factory snapshot = %#v", snapshot)
		}
	})

	t.Run("connect", func(t *testing.T) {
		probe := &managerLifecycleProbe{connectFn: func(context.Context) error {
			panic("private connect panic")
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()})
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateFailed ||
			len(snapshot.Servers[0].Diagnostics) != 1 {
			t.Fatalf("panicking Connect snapshot = %#v", snapshot)
		}
		if calls := probe.closeCalls.Load(); calls != 1 {
			t.Fatalf("panicking Connect candidate close calls = %d, want 1", calls)
		}
	})

	t.Run("reconnect", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		probe.reconnectFn = func(context.Context) error {
			panic("private reconnect panic")
		}
		if err := manager.Reconnect(t.Context(), "probe"); err == nil || err.Error() != "operational failure" {
			t.Fatalf("panicking Reconnect error = %v", err)
		}
	})

	t.Run("state-and-last-error", func(t *testing.T) {
		probe := &managerLifecycleProbe{}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		probe.stateFn = func() ConnectionState { panic("private state panic") }
		probe.lastErrorFn = func() string { panic("private last-error panic") }
		snapshot := manager.Snapshot()
		if len(snapshot.Servers) != 1 || snapshot.Servers[0].State != StateFailed ||
			len(snapshot.Servers[0].Diagnostics) != 0 {
			t.Fatalf("panicking state projection = %#v", snapshot)
		}
	})

	t.Run("generation", func(t *testing.T) {
		probe := &managerLifecycleProbe{generationFn: func() uint64 {
			panic("private generation panic")
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, diagnostics, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 0 || len(diagnostics) != 1 ||
			diagnostics[0].Message != "tool catalog changed during discovery" {
			t.Fatalf("panicking Generation projection = %#v, %#v, %v", tools, diagnostics, err)
		}
	})
}

func TestManagerCanceledQueuedLifecyclePreservesOperationOrder(t *testing.T) {
	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	var calls atomic.Int32
	manager := NewManager(func(Config) (Connection, error) {
		call := calls.Add(1)
		probe := &managerLifecycleProbe{}
		if call == 1 {
			probe.connectFn = func(context.Context) error {
				close(firstEntered)
				<-firstRelease
				return nil
			}
		}
		return probe, nil
	})
	t.Cleanup(func() { _ = manager.Close() })

	firstDone := make(chan Snapshot, 1)
	go func() {
		firstDone <- manager.Reconcile(context.Background(), []Config{managerProbeConfig()})
	}()
	<-firstEntered

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancelledDone := make(chan Snapshot, 1)
	go func() {
		cancelledDone <- manager.Reconcile(cancelledContext, []Config{{
			Name: "cancelled", Transport: TransportStdio, Command: "cancelled", Scope: ScopeUser,
		}})
	}()
	cancel()
	_ = requireSnapshotResult(t, cancelledDone)

	thirdDone := make(chan Snapshot, 1)
	go func() {
		thirdDone <- manager.Reconcile(context.Background(), []Config{{
			Name: "third", Transport: TransportStdio, Command: "third", Scope: ScopeUser,
		}})
	}()
	select {
	case snapshot := <-thirdDone:
		t.Fatalf("later operation bypassed the still-active predecessor: %#v", snapshot)
	case <-time.After(30 * time.Millisecond):
	}
	close(firstRelease)
	if snapshot := requireSnapshotResult(t, firstDone); len(snapshot.Servers) != 1 ||
		snapshot.Servers[0].Name != "probe" {
		t.Fatalf("first reconciliation = %#v", snapshot)
	}
	if snapshot := requireSnapshotResult(t, thirdDone); len(snapshot.Servers) != 1 ||
		snapshot.Servers[0].Name != "third" {
		t.Fatalf("third reconciliation = %#v", snapshot)
	}
}

type managerBoundLifecycleProbe struct {
	*managerLifecycleProbe
	listBoundFn func(context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error)
	prepareFn   func(context.Context, string, map[string]any) (ToolCallPreparation, error)
}

func (probe *managerBoundLifecycleProbe) ListToolsBound(ctx context.Context) (
	[]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error,
) {
	if probe.listBoundFn != nil {
		return probe.listBoundFn(ctx)
	}
	tools, diagnostics, err := probe.ListTools(ctx)
	return tools, diagnostics, ToolCatalogVersion{ConnectionGeneration: probe.Generation()}, err
}

func (probe *managerBoundLifecycleProbe) PrepareToolCall(
	ctx context.Context, name string, arguments map[string]any,
) (ToolCallPreparation, error) {
	if probe.prepareFn != nil {
		return probe.prepareFn(ctx, name, arguments)
	}
	return &managerToolPreparationProbe{}, nil
}

type managerToolPreparationProbe struct {
	registerFn func(ToolCatalogVersion) (RegisteredToolCall, error)
	cancelFn   func()
}

func (probe *managerToolPreparationProbe) Register(version ToolCatalogVersion) (RegisteredToolCall, error) {
	if probe.registerFn != nil {
		return probe.registerFn(version)
	}
	return &managerRegisteredToolCallProbe{}, nil
}

func (probe *managerToolPreparationProbe) Cancel() {
	if probe.cancelFn != nil {
		probe.cancelFn()
	}
}

type managerRegisteredToolCallProbe struct {
	awaitFn  func() (ToolResult, error)
	cancelFn func()
}

func (probe *managerRegisteredToolCallProbe) Await() (ToolResult, error) {
	if probe.awaitFn != nil {
		return probe.awaitFn()
	}
	return ToolResult{}, nil
}

func (probe *managerRegisteredToolCallProbe) Cancel() {
	if probe.cancelFn != nil {
		probe.cancelFn()
	}
}

func requireManagerBinding(t *testing.T, manager *Manager, config Config) ToolBinding {
	t.Helper()
	if snapshot := manager.Reconcile(t.Context(), []Config{config}); len(snapshot.Servers) != 1 {
		t.Fatalf("manager snapshot = %#v", snapshot)
	}
	tools, diagnostics, err := manager.Tools(t.Context())
	if err != nil || len(tools) != 1 || len(diagnostics) != 0 {
		t.Fatalf("manager tools = %#v, diagnostics %#v, error %v", tools, diagnostics, err)
	}
	binding, ok := tools[0].Binding()
	if !ok {
		t.Fatal("manager tool has no binding")
	}
	return binding
}

func TestManagerContainsPanickingToolProviderCallbacks(t *testing.T) {
	t.Run("list-tools", func(t *testing.T) {
		probe := &managerLifecycleProbe{listToolsFn: func(context.Context) ([]ToolDescriptor, []Diagnostic, error) {
			panic("private list panic")
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, diagnostics, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 0 || len(diagnostics) != 1 ||
			diagnostics[0].Message != "protocol error" {
			t.Fatalf("panicking ListTools projection = %#v, %#v, %v", tools, diagnostics, err)
		}
	})

	t.Run("list-tools-bound", func(t *testing.T) {
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.listBoundFn = func(context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error) {
			panic("private bound list panic")
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, diagnostics, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 0 || len(diagnostics) != 1 ||
			diagnostics[0].Message != "protocol error" {
			t.Fatalf("panicking ListToolsBound projection = %#v, %#v, %v", tools, diagnostics, err)
		}
	})

	t.Run("legacy-call", func(t *testing.T) {
		probe := &managerLifecycleProbe{callToolFn: func(context.Context, string, map[string]any) (ToolResult, error) {
			panic("private call panic")
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("panicking legacy CallTool error = %v", err)
		}
	})

	t.Run("prepare", func(t *testing.T) {
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			panic("private prepare panic")
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("panicking PrepareToolCall error = %v", err)
		}
	})

	t.Run("register-and-preparation-cancel", func(t *testing.T) {
		preparation := &managerToolPreparationProbe{
			registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
				panic("private register panic")
			},
			cancelFn: func() { panic("private preparation cancel panic") },
		}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return preparation, nil
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("panicking Register/Cancel error = %v", err)
		}
	})

	t.Run("await", func(t *testing.T) {
		registered := &managerRegisteredToolCallProbe{awaitFn: func() (ToolResult, error) {
			panic("private await panic")
		}}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return &managerToolPreparationProbe{registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
				return registered, nil
			}}, nil
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("panicking Await error = %v", err)
		}
	})

	t.Run("registered-cancel", func(t *testing.T) {
		var manager *Manager
		registered := &managerRegisteredToolCallProbe{cancelFn: func() {
			panic("private registered cancel panic")
		}}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return &managerToolPreparationProbe{registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
				if err := manager.Close(); err != nil {
					return nil, err
				}
				return registered, nil
			}}, nil
		}
		manager = NewManager(func(Config) (Connection, error) { return probe, nil })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrStaleToolBinding) {
			t.Fatalf("panicking registered Cancel stale error = %v", err)
		}
	})
}

func TestManagerBoundsAndClonesCustomCatalogsAndResults(t *testing.T) {
	t.Run("catalog-item-limit", func(t *testing.T) {
		oversized := make([]ToolDescriptor, DefaultMaxListItems+1)
		probe := &managerLifecycleProbe{listToolsFn: func(context.Context) ([]ToolDescriptor, []Diagnostic, error) {
			return oversized, nil, nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, diagnostics, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 0 || len(diagnostics) != 1 ||
			diagnostics[0].Message != "tool catalog rejected" {
			t.Fatalf("oversized catalog projection = %#v, %#v, %v", tools, diagnostics, err)
		}
	})

	t.Run("catalog-byte-limit", func(t *testing.T) {
		config := managerProbeConfig()
		config.MaxMessageBytes = 1_024
		probe := &managerLifecycleProbe{listToolsFn: func(context.Context) ([]ToolDescriptor, []Diagnostic, error) {
			return nil, []Diagnostic{{Message: string(make([]byte, 1_025))}}, nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{config}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, diagnostics, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 0 || len(diagnostics) != 1 ||
			diagnostics[0].Message != "tool catalog rejected" {
			t.Fatalf("oversized catalog diagnostic projection = %#v, %#v, %v", tools, diagnostics, err)
		}
	})

	t.Run("catalog-clone", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object"}`)
		annotations := map[string]any{"mode": "original"}
		probe := &managerLifecycleProbe{listToolsFn: func(context.Context) ([]ToolDescriptor, []Diagnostic, error) {
			return []ToolDescriptor{{
				Name: "mcp__probe__echo", InputSchema: schema, Annotations: annotations,
			}}, nil, nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		tools, _, err := manager.Tools(t.Context())
		if err != nil || len(tools) != 1 {
			t.Fatalf("manager tools = %#v, %v", tools, err)
		}
		schema[0] = '['
		annotations["mode"] = "mutated"
		if string(tools[0].InputSchema) != `{"type":"object"}` ||
			tools[0].Annotations["mode"] != "original" {
			t.Fatalf("published tool retained provider-owned aliases: %#v", tools[0])
		}
	})

	t.Run("legacy-result-byte-limit", func(t *testing.T) {
		config := managerProbeConfig()
		config.MaxMessageBytes = 1_024
		probe := &managerLifecycleProbe{callToolFn: func(context.Context, string, map[string]any) (ToolResult, error) {
			return ToolResult{Content: []ContentBlock{{
				Type: "text", Text: string(make([]byte, 1_025)),
			}}}, nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, config)
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("oversized legacy result error = %v", err)
		}
	})

	t.Run("bound-result-item-limit", func(t *testing.T) {
		content := make([]ContentBlock, DefaultMaxListItems+1)
		registered := &managerRegisteredToolCallProbe{awaitFn: func() (ToolResult, error) {
			return ToolResult{Content: content}, nil
		}}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return &managerToolPreparationProbe{registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
				return registered, nil
			}}, nil
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		t.Cleanup(func() { _ = manager.Close() })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		if _, err := manager.CallBoundTool(t.Context(), binding, "echo", map[string]any{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("oversized bound result error = %v", err)
		}
	})
}

func TestManagerCloseBypassesBlockingToolProviderCallbacks(t *testing.T) {
	t.Run("list-tools-bound", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.listBoundFn = func(context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error) {
			close(entered)
			<-release
			tools, diagnostics, err := probe.ListTools(context.Background())
			return tools, diagnostics, ToolCatalogVersion{ConnectionGeneration: 1}, err
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		if snapshot := manager.Reconcile(t.Context(), []Config{managerProbeConfig()}); len(snapshot.Servers) != 1 {
			t.Fatalf("manager snapshot = %#v", snapshot)
		}
		toolsDone := make(chan error, 1)
		go func() {
			_, _, err := manager.Tools(context.Background())
			toolsDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked ListToolsBound: %v", err)
		}
		close(release)
		select {
		case err := <-toolsDone:
			if !errors.Is(err, ErrStaleToolBinding) {
				t.Fatalf("stale ListToolsBound error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("ListToolsBound did not return after release")
		}
	})

	t.Run("legacy-call", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerLifecycleProbe{callToolFn: func(context.Context, string, map[string]any) (ToolResult, error) {
			close(entered)
			<-release
			return ToolResult{}, nil
		}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		callDone := make(chan error, 1)
		go func() {
			_, err := manager.CallBoundTool(context.Background(), binding, "echo", map[string]any{})
			callDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked legacy CallTool: %v", err)
		}
		close(release)
		select {
		case err := <-callDone:
			if err != nil {
				t.Fatalf("already-accepted legacy CallTool error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("legacy CallTool did not return after release")
		}
	})

	t.Run("prepare", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			close(entered)
			<-release
			return &managerToolPreparationProbe{}, nil
		}
		callDone := make(chan error, 1)
		go func() {
			_, err := manager.CallBoundTool(context.Background(), binding, "echo", map[string]any{})
			callDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked PrepareToolCall: %v", err)
		}
		close(release)
		select {
		case err := <-callDone:
			if !errors.Is(err, ErrStaleToolBinding) {
				t.Fatalf("stale prepared call error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("PrepareToolCall did not return after release")
		}
	})

	t.Run("register", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		preparation := &managerToolPreparationProbe{registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
			close(entered)
			<-release
			return &managerRegisteredToolCallProbe{}, nil
		}}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return preparation, nil
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		callDone := make(chan error, 1)
		go func() {
			_, err := manager.CallBoundTool(context.Background(), binding, "echo", map[string]any{})
			callDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked Register: %v", err)
		}
		close(release)
		select {
		case err := <-callDone:
			if !errors.Is(err, ErrStaleToolBinding) {
				t.Fatalf("stale registered call error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Register did not return after release")
		}
	})

	t.Run("await", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		registered := &managerRegisteredToolCallProbe{awaitFn: func() (ToolResult, error) {
			close(entered)
			<-release
			return ToolResult{}, nil
		}}
		probe := &managerBoundLifecycleProbe{managerLifecycleProbe: &managerLifecycleProbe{}}
		probe.prepareFn = func(context.Context, string, map[string]any) (ToolCallPreparation, error) {
			return &managerToolPreparationProbe{registerFn: func(ToolCatalogVersion) (RegisteredToolCall, error) {
				return registered, nil
			}}, nil
		}
		manager := NewManager(func(Config) (Connection, error) { return probe, nil })
		binding := requireManagerBinding(t, manager, managerProbeConfig())
		callDone := make(chan error, 1)
		go func() {
			_, err := manager.CallBoundTool(context.Background(), binding, "echo", map[string]any{})
			callDone <- err
		}()
		<-entered
		if err := requireManagerCloseCompletes(t, manager); err != nil {
			t.Fatalf("Close with blocked Await: %v", err)
		}
		close(release)
		select {
		case err := <-callDone:
			if err != nil {
				t.Fatalf("already-accepted Await error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Await did not return after release")
		}
	})
}
