package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/redact"
)

const defaultManagerCloseTimeout = 2 * time.Second

var ErrStaleToolBinding = errors.New("MCP tool binding is stale")

type ConnectionFactory func(Config) (Connection, error)

type managedConnection struct {
	fingerprint     string
	connection      Connection
	maxMessageBytes int
	closeOnce       sync.Once
	closeErr        error
	busyToken       uint64
}

func newManagedConnection(fingerprint string, connection Connection, maxMessageBytes int) *managedConnection {
	if maxMessageBytes <= 0 || maxMessageBytes > 64<<20 {
		maxMessageBytes = DefaultMaxMessageBytes
	}
	return &managedConnection{
		fingerprint: fingerprint, connection: connection, maxMessageBytes: maxMessageBytes,
	}
}

func (entry *managedConnection) close() error {
	if entry == nil {
		return errors.New("provider connection is unavailable")
	}
	entry.closeOnce.Do(func() {
		entry.closeErr = closeConnection(entry.connection)
	})
	return entry.closeErr
}

type connectionCloseTarget struct {
	id          string
	disposition string
	entry       *managedConnection
}

type lifecycleLease struct {
	token uint64
	done  chan struct{}
}

// ToolBinding pins one advertised tool catalog to the manager and connection
// generations from which it was discovered. Its fields are intentionally
// opaque; callers can only present it back to Manager for a bound invocation.
type ToolBinding struct {
	scopedID             string
	managerGeneration    uint64
	connectionGeneration uint64
	catalogEpoch         uint64
	bound                bool
	legacy               bool
}

type Snapshot struct {
	Generation  uint64       `json:"generation"`
	Servers     []Descriptor `json:"servers"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// String and GoString prevent recursive diagnostic formatting from traversing
// descriptors that retain source configuration.
func (Snapshot) String() string   { return "" }
func (Snapshot) GoString() string { return "" }
func (snapshot Snapshot) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, snapshot.String())
}

func cloneSnapshot(source Snapshot) Snapshot {
	result := Snapshot{Generation: source.Generation, Diagnostics: append([]Diagnostic(nil), source.Diagnostics...)}
	result.Servers = make([]Descriptor, len(source.Servers))
	for index := range source.Servers {
		result.Servers[index] = cloneDescriptor(source.Servers[index])
	}
	return result
}

// Manager owns one coherent provider generation. One failed provider cannot
// remove healthy siblings, and stale reconciliations cannot publish.
type Manager struct {
	mu            sync.RWMutex
	generation    uint64
	lifecycle     uint64
	active        uint64
	lifecycleTail chan struct{}
	shutdown      chan struct{}
	snapshot      Snapshot
	connections   map[string]*managedConnection
	inflight      map[uint64][]*managedConnection
	byName        map[string]string
	factory       ConnectionFactory
	credentials   *redact.Set
	closeTimeout  time.Duration
	closed        bool
	closeErr      error
	closeDone     chan struct{}
}

func NewManager(factory ConnectionFactory) *Manager {
	if factory == nil {
		factory = func(config Config) (Connection, error) { return NewClient(config) }
	}
	lifecycleTail := make(chan struct{})
	close(lifecycleTail)
	return &Manager{
		connections:   make(map[string]*managedConnection),
		inflight:      make(map[uint64][]*managedConnection),
		byName:        make(map[string]string),
		factory:       factory,
		closeTimeout:  defaultManagerCloseTimeout,
		lifecycleTail: lifecycleTail,
		shutdown:      make(chan struct{}),
		closeDone:     make(chan struct{}),
	}
}

func (manager *Manager) Snapshot() Snapshot {
	manager.mu.RLock()
	result := cloneSnapshot(manager.snapshot)
	generation := manager.generation
	credentials := manager.credentials
	connections := make(map[string]*managedConnection, len(manager.connections))
	for id, entry := range manager.connections {
		connections[id] = entry
	}
	manager.mu.RUnlock()
	for index := range result.Servers {
		if entry := connections[result.Servers[index].ScopedID]; entry != nil {
			result.Servers[index].State = safeConnectionState(entry.connection)
			if message := safeConnectionLastError(entry.connection); message != "" {
				result.Servers[index].Diagnostics = append(result.Servers[index].Diagnostics, Diagnostic{Message: safeManagerError(errors.New(message))})
			}
		}
	}
	manager.mu.RLock()
	current := manager.generation == generation
	if current {
		for id, entry := range connections {
			if manager.connections[id] != entry {
				current = false
				break
			}
		}
	}
	manager.mu.RUnlock()
	if !current {
		return manager.staticSnapshot()
	}
	if validateManagerProjection(credentials, result) != nil {
		return Snapshot{Generation: result.Generation}
	}
	return result
}

func (manager *Manager) Reconcile(ctx context.Context, configs []Config) Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	credentials := credentialSetForConfigs(configs)
	descriptors, diagnostics := composeDescriptors(configs)
	lease, ok := manager.beginLifecycle(ctx)
	if !ok {
		return manager.staticSnapshot()
	}
	defer manager.finishLifecycle(lease)

	type plannedConnection struct {
		index int
		entry *managedConnection
	}
	manager.mu.Lock()
	if manager.closed || manager.active != lease.token {
		manager.mu.Unlock()
		return manager.staticSnapshot()
	}
	oldConnections := make(map[string]*managedConnection, len(manager.connections))
	for id, entry := range manager.connections {
		oldConnections[id] = entry
	}
	plans := make([]plannedConnection, 0, len(descriptors))
	for index := range descriptors {
		descriptor := &descriptors[index]
		if !descriptor.Availability.Usable() || descriptor.Transport != TransportStdio {
			continue
		}
		if old := oldConnections[descriptor.ScopedID]; old != nil &&
			old.fingerprint == descriptor.Fingerprint && old.busyToken == 0 {
			old.busyToken = lease.token
			plans = append(plans, plannedConnection{index: index, entry: old})
			delete(oldConnections, descriptor.ScopedID)
		}
	}
	manager.mu.Unlock()

	planned := make(map[int]*managedConnection, len(plans))
	for _, plan := range plans {
		planned[plan.index] = plan.entry
	}
	connections := make(map[string]*managedConnection)
	byName := make(map[string]string)
	closeTargets := make([]connectionCloseTarget, 0, len(oldConnections))
	for index := range descriptors {
		descriptor := &descriptors[index]
		if !descriptor.Availability.Usable() || descriptor.Transport != TransportStdio {
			descriptor.State = StateDisabled
			continue
		}
		byName[strings.ToLower(descriptor.Name)] = descriptor.ScopedID
		if old := planned[index]; old != nil {
			connections[descriptor.ScopedID] = old
			state := safeConnectionState(old.connection)
			if (state == StateFailed || state == StateClosed) && manager.lifecycleCurrent(lease) {
				if err := safeConnectionReconnect(old.connection, ctx); err != nil {
					descriptor.Diagnostics = append(descriptor.Diagnostics, Diagnostic{Server: descriptor.Name, Message: "reconnect failed"})
				}
				state = safeConnectionState(old.connection)
			}
			descriptor.State = state
			continue
		}
		if !manager.lifecycleCurrent(lease) {
			break
		}
		connection, err := safeConnectionFactory(manager.factory, cloneConfig(descriptor.config))
		if err != nil {
			if connection != nil {
				entry := newManagedConnection(
					descriptor.Fingerprint, connection, descriptor.config.MaxMessageBytes,
				)
				_ = manager.registerInflight(lease, entry)
				closeTargets = append(closeTargets, connectionCloseTarget{
					id: descriptor.ScopedID, disposition: "failed factory candidate",
					entry: entry,
				})
			}
			descriptor.State = StateFailed
			descriptor.Diagnostics = append(descriptor.Diagnostics, Diagnostic{Server: descriptor.Name, Message: "construct connection: " + safeManagerError(err)})
			continue
		}
		if connection == nil {
			descriptor.State = StateFailed
			descriptor.Diagnostics = append(descriptor.Diagnostics, Diagnostic{
				Server: descriptor.Name, Message: "construct connection: unavailable",
			})
			continue
		}
		entry := newManagedConnection(descriptor.Fingerprint, connection, descriptor.config.MaxMessageBytes)
		if !manager.registerInflight(lease, entry) {
			descriptor.State = StateFailed
			closeTargets = append(closeTargets, connectionCloseTarget{
				id: descriptor.ScopedID, disposition: "stale candidate", entry: entry,
			})
			break
		}
		if err := safeConnectionConnect(connection, ctx); err != nil {
			descriptor.State = safeConnectionState(connection)
			if descriptor.State == StateConnected || descriptor.State == StatePending {
				descriptor.State = StateFailed
			}
			descriptor.Diagnostics = append(descriptor.Diagnostics, Diagnostic{Server: descriptor.Name, Message: "connect failed: " + safeManagerError(err)})
			closeTargets = append(closeTargets, connectionCloseTarget{
				id: descriptor.ScopedID, disposition: "failed candidate", entry: entry,
			})
			continue
		}
		connections[descriptor.ScopedID] = entry
		descriptor.State = safeConnectionState(connection)
	}

	manager.mu.Lock()
	if manager.closed || manager.active != lease.token {
		for _, entry := range manager.inflight[lease.token] {
			closeTargets = append(closeTargets, connectionCloseTarget{
				disposition: "stale candidate", entry: entry,
			})
		}
		manager.mu.Unlock()
		_ = closeConnections(closeTargets, manager.closeTimeout)
		return manager.staticSnapshot()
	}
	manager.generation++
	generation := manager.generation
	for index := range descriptors {
		descriptors[index].Generation = generation
	}
	candidate := Snapshot{Generation: generation, Servers: descriptors, Diagnostics: diagnostics}
	if validateManagerProjection(credentials, candidate) != nil {
		for id, entry := range connections {
			closeTargets = append(closeTargets, connectionCloseTarget{
				id: id, disposition: "credential-unsafe generation", entry: entry,
			})
		}
		connections = make(map[string]*managedConnection)
		byName = make(map[string]string)
		candidate = Snapshot{Generation: generation}
	}
	manager.snapshot = candidate
	manager.connections = connections
	manager.byName = byName
	manager.credentials = credentials
	manager.mu.Unlock()

	for id, entry := range oldConnections {
		closeTargets = append(closeTargets, connectionCloseTarget{
			id: id, disposition: "replaced generation", entry: entry,
		})
	}
	_ = closeConnections(closeTargets, manager.closeTimeout)
	return manager.staticSnapshot()
}

func (manager *Manager) beginLifecycle(ctx context.Context) (lifecycleLease, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return lifecycleLease{}, false
	}
	predecessor := manager.lifecycleTail
	done := make(chan struct{})
	manager.lifecycleTail = done
	shutdown := manager.shutdown
	manager.mu.Unlock()

	select {
	case <-predecessor:
	case <-shutdown:
		close(done)
		return lifecycleLease{}, false
	case <-ctx.Done():
		// Preserve the predecessor chain for later operations even though this
		// caller no longer waits for its own turn.
		go func() {
			<-predecessor
			close(done)
		}()
		return lifecycleLease{}, false
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		close(done)
		return lifecycleLease{}, false
	}
	manager.lifecycle++
	lease := lifecycleLease{token: manager.lifecycle, done: done}
	manager.active = lease.token
	manager.mu.Unlock()
	return lease, true
}

func (manager *Manager) finishLifecycle(lease lifecycleLease) {
	manager.mu.Lock()
	for _, entry := range manager.connections {
		if entry != nil && entry.busyToken == lease.token {
			entry.busyToken = 0
		}
	}
	for _, entry := range manager.inflight[lease.token] {
		if entry != nil && entry.busyToken == lease.token {
			entry.busyToken = 0
		}
	}
	delete(manager.inflight, lease.token)
	if manager.active == lease.token {
		manager.active = 0
	}
	manager.mu.Unlock()
	close(lease.done)
}

func (manager *Manager) lifecycleCurrent(lease lifecycleLease) bool {
	manager.mu.RLock()
	current := !manager.closed && manager.active == lease.token
	manager.mu.RUnlock()
	return current
}

func (manager *Manager) registerInflight(lease lifecycleLease, entry *managedConnection) bool {
	if entry == nil || entry.connection == nil {
		return false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.active != lease.token {
		return false
	}
	entry.busyToken = lease.token
	manager.inflight[lease.token] = append(manager.inflight[lease.token], entry)
	return true
}

func (manager *Manager) staticSnapshot() Snapshot {
	manager.mu.RLock()
	result := cloneSnapshot(manager.snapshot)
	credentials := manager.credentials
	manager.mu.RUnlock()
	if validateManagerProjection(credentials, result) != nil {
		return Snapshot{Generation: result.Generation}
	}
	return result
}

func safeConnectionFactory(factory ConnectionFactory, config Config) (connection Connection, err error) {
	if factory == nil {
		return nil, errors.New("MCP connection factory is unavailable")
	}
	defer func() {
		if recover() != nil {
			connection = nil
			err = errors.New("MCP connection factory panicked")
		}
	}()
	return factory(config)
}

func safeConnectionConnect(connection Connection, ctx context.Context) (err error) {
	if connection == nil {
		return ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			err = errors.New("MCP connection connect panicked")
		}
	}()
	return connection.Connect(ctx)
}

func safeConnectionReconnect(connection Connection, ctx context.Context) (err error) {
	if connection == nil {
		return ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			err = errors.New("MCP connection reconnect panicked")
		}
	}()
	return connection.Reconnect(ctx)
}

func safeConnectionState(connection Connection) (state ConnectionState) {
	if connection == nil {
		return StateFailed
	}
	defer func() {
		if recover() != nil {
			state = StateFailed
		}
	}()
	state = connection.State()
	switch state {
	case StatePending, StateConnected, StateFailed, StateNeedsAuth, StateDisabled, StateClosed:
		return state
	default:
		return StateFailed
	}
}

func safeConnectionLastError(connection Connection) (message string) {
	if connection == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			message = ""
		}
	}()
	return connection.LastError()
}

func safeConnectionGeneration(connection Connection) (generation uint64, ok bool) {
	if connection == nil {
		return 0, false
	}
	defer func() {
		if recover() != nil {
			generation = 0
			ok = false
		}
	}()
	return connection.Generation(), true
}

func safeConnectionListTools(connection Connection, ctx context.Context) (
	tools []ToolDescriptor, diagnostics []Diagnostic, err error,
) {
	if connection == nil {
		return nil, nil, ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			tools = nil
			diagnostics = nil
			err = fmt.Errorf("%w: provider tool discovery panicked", ErrProtocol)
		}
	}()
	return connection.ListTools(ctx)
}

func safeConnectionListToolsBound(connection BoundToolConnection, ctx context.Context) (
	tools []ToolDescriptor, diagnostics []Diagnostic, version ToolCatalogVersion, err error,
) {
	if connection == nil {
		return nil, nil, ToolCatalogVersion{}, ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			tools = nil
			diagnostics = nil
			version = ToolCatalogVersion{}
			err = fmt.Errorf("%w: provider bound tool discovery panicked", ErrProtocol)
		}
	}()
	return connection.ListToolsBound(ctx)
}

func safeConnectionCallTool(
	connection Connection, ctx context.Context, name string, arguments map[string]any,
) (result ToolResult, err error) {
	if connection == nil {
		return ToolResult{}, ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			result = ToolResult{}
			err = fmt.Errorf("%w: provider tool call panicked", ErrProtocol)
		}
	}()
	return connection.CallTool(ctx, name, arguments)
}

func safePrepareToolCall(
	connection BoundToolConnection, ctx context.Context, name string, arguments map[string]any,
) (preparation ToolCallPreparation, err error) {
	if connection == nil {
		return nil, ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			preparation = nil
			err = fmt.Errorf("%w: provider tool preparation panicked", ErrProtocol)
		}
	}()
	return connection.PrepareToolCall(ctx, name, arguments)
}

func safeRegisterToolCall(
	preparation ToolCallPreparation, version ToolCatalogVersion,
) (registered RegisteredToolCall, err error) {
	if preparation == nil {
		return nil, ErrProtocol
	}
	defer func() {
		if recover() != nil {
			registered = nil
			err = fmt.Errorf("%w: provider tool registration panicked", ErrProtocol)
		}
	}()
	return preparation.Register(version)
}

func safeAwaitToolCall(registered RegisteredToolCall) (result ToolResult, err error) {
	if registered == nil {
		return ToolResult{}, ErrProtocol
	}
	defer func() {
		if recover() != nil {
			result = ToolResult{}
			err = fmt.Errorf("%w: provider tool await panicked", ErrProtocol)
		}
	}()
	return registered.Await()
}

func safeCancelToolPreparation(preparation ToolCallPreparation) {
	if preparation == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	preparation.Cancel()
}

func safeCancelRegisteredToolCall(registered RegisteredToolCall) {
	if registered == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	registered.Cancel()
}

func normalizeManagerToolCatalog(
	server string,
	tools []ToolDescriptor,
	diagnostics []Diagnostic,
	maxBytes int,
) (_ []ToolDescriptor, _ []Diagnostic, resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = ErrProtocol
		}
	}()
	if maxBytes <= 0 || maxBytes > 64<<20 {
		maxBytes = DefaultMaxMessageBytes
	}
	if len(tools) > DefaultMaxListItems || len(diagnostics) > DefaultMaxListItems {
		return nil, nil, ErrProtocol
	}
	total := 0
	add := func(size int) bool {
		if size < 0 || total > maxBytes-size {
			return false
		}
		total += size
		return true
	}
	for _, diagnostic := range diagnostics {
		if !add(32) || !add(len(diagnostic.Server)) ||
			!add(len(diagnostic.Source)) || !add(len(diagnostic.Message)) {
			return nil, nil, ErrProtocol
		}
	}
	normalizedDiagnostics := make([]Diagnostic, 0, 2)
	if len(diagnostics) > 0 {
		normalizedDiagnostics = append(normalizedDiagnostics, Diagnostic{
			Server: server, Message: "provider reported tool catalog diagnostics",
		})
	}
	seen := make(map[string]struct{}, len(tools))
	normalizedTools := make([]ToolDescriptor, 0, len(tools))
	invalidSeen := false
	for _, tool := range tools {
		if !add(64) || !add(len(tool.Name)) ||
			!add(len(tool.Description)) || !add(len(tool.InputSchema)) {
			return nil, nil, ErrProtocol
		}
		annotationSize, err := measureManagerJSONValue(tool.Annotations, 0, maxBytes-total)
		if err != nil || !add(annotationSize) {
			return nil, nil, ErrProtocol
		}
		if !utf8.ValidString(tool.Name) || !utf8.ValidString(tool.Description) ||
			!utf8.Valid(tool.InputSchema) ||
			strings.IndexFunc(tool.Description, unicode.IsControl) >= 0 ||
			validateManagerToolDescriptor(server, tool) != nil {
			if !invalidSeen {
				normalizedDiagnostics = append(normalizedDiagnostics, Diagnostic{
					Server: server, Message: "tool omitted: invalid descriptor",
				})
				invalidSeen = true
			}
			continue
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			if !invalidSeen {
				normalizedDiagnostics = append(normalizedDiagnostics, Diagnostic{
					Server: server, Message: "tool omitted: duplicate descriptor",
				})
				invalidSeen = true
			}
			continue
		}
		seen[tool.Name] = struct{}{}
		tool.binding = ToolBinding{}
		normalizedTools = append(normalizedTools, cloneTools([]ToolDescriptor{tool})[0])
	}
	return normalizedTools, normalizedDiagnostics, nil
}

func validateManagerToolDescriptor(server string, tool ToolDescriptor) error {
	const prefix = "mcp__"
	if !strings.HasPrefix(tool.Name, prefix) {
		return errors.New("tool name is not namespaced")
	}
	parts := strings.Split(strings.TrimPrefix(tool.Name, prefix), "__")
	if len(parts) != 2 || !strings.EqualFold(parts[0], server) ||
		!validServerName(parts[0]) || !validCapabilityName(parts[1]) {
		return errors.New("tool namespace is invalid")
	}
	tool.Name = parts[1]
	return validateToolDescriptor(tool)
}

func measureManagerJSONValue(value any, depth int, remaining int) (int, error) {
	if depth > 32 || remaining < 0 {
		return 0, ErrProtocol
	}
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case bool:
		return 1, nil
	case string:
		if !utf8.ValidString(typed) || len(typed) > remaining {
			return 0, ErrProtocol
		}
		return len(typed), nil
	case json.Number:
		if !json.Valid([]byte(typed.String())) || len(typed.String()) > remaining {
			return 0, ErrProtocol
		}
		return len(typed.String()), nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return 0, ErrProtocol
		}
		return 4, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, ErrProtocol
		}
		return 8, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 16, nil
	case map[string]any:
		if len(typed) > DefaultMaxListItems {
			return 0, ErrProtocol
		}
		total := 16
		for key, child := range typed {
			if !utf8.ValidString(key) || len(key) > 512 ||
				strings.IndexFunc(key, unicode.IsControl) >= 0 ||
				len(key) > remaining-total-8 {
				return 0, ErrProtocol
			}
			total += len(key) + 8
			size, err := measureManagerJSONValue(child, depth+1, remaining-total)
			if err != nil || size > remaining-total {
				return 0, ErrProtocol
			}
			total += size
		}
		return total, nil
	case []any:
		if len(typed) > DefaultMaxListItems {
			return 0, ErrProtocol
		}
		total := 16
		for _, child := range typed {
			if total > remaining-4 {
				return 0, ErrProtocol
			}
			total += 4
			size, err := measureManagerJSONValue(child, depth+1, remaining-total)
			if err != nil || size > remaining-total {
				return 0, ErrProtocol
			}
			total += size
		}
		return total, nil
	default:
		return 0, ErrProtocol
	}
}

func normalizeManagerToolResult(result ToolResult, maxBytes int) (ToolResult, error) {
	if maxBytes <= 0 || maxBytes > 64<<20 {
		maxBytes = DefaultMaxMessageBytes
	}
	if len(result.Content) > DefaultMaxListItems {
		return ToolResult{}, ErrProtocol
	}
	if !utf8.Valid(result.StructuredContent) {
		return ToolResult{}, ErrProtocol
	}
	total := len(result.StructuredContent) + 64
	if total > maxBytes {
		return ToolResult{}, ErrProtocol
	}
	for _, block := range result.Content {
		if !utf8.ValidString(block.Type) || !utf8.ValidString(block.Text) ||
			!utf8.ValidString(block.Data) || !utf8.ValidString(block.MIMEType) ||
			!utf8.ValidString(block.URI) || !utf8.ValidString(block.Name) ||
			!utf8.Valid(block.Resource) {
			return ToolResult{}, ErrProtocol
		}
		if total > maxBytes-64 {
			return ToolResult{}, ErrProtocol
		}
		total += 64
		for _, size := range []int{
			len(block.Type), len(block.Text), len(block.Data), len(block.MIMEType),
			len(block.URI), len(block.Name), len(block.Resource),
		} {
			if size < 0 || total > maxBytes-size {
				return ToolResult{}, ErrProtocol
			}
			total += size
		}
	}
	if err := validateToolResult(result); err != nil {
		return ToolResult{}, ErrProtocol
	}
	return cloneToolResult(result), nil
}

func composeDescriptors(configs []Config) ([]Descriptor, []Diagnostic) {
	var valid []Descriptor
	var diagnostics []Diagnostic
	for _, config := range configs {
		descriptor, err := ValidateConfig(config)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Message: err.Error()})
			continue
		}
		valid = append(valid, descriptor)
	}
	hasEnterprise := false
	for _, descriptor := range valid {
		if descriptor.Scope == ScopeEnterprise {
			hasEnterprise = true
			break
		}
	}
	if hasEnterprise {
		filtered := valid[:0]
		for _, descriptor := range valid {
			if descriptor.Scope == ScopeEnterprise {
				filtered = append(filtered, descriptor)
			} else {
				diagnostics = append(diagnostics, Diagnostic{Server: descriptor.Name, Source: descriptor.SourceID, Message: "excluded by enterprise MCP exclusivity"})
			}
		}
		valid = filtered
	}
	sort.SliceStable(valid, func(i, j int) bool {
		ri, rj := scopeRank(valid[i].Scope), scopeRank(valid[j].Scope)
		if ri != rj {
			return ri < rj
		}
		if valid[i].Name != valid[j].Name {
			return valid[i].Name < valid[j].Name
		}
		return valid[i].SourceID < valid[j].SourceID
	})

	var nameWinners []Descriptor
	var unavailable []Descriptor
	byName := make(map[string]int)
	for _, descriptor := range valid {
		if !descriptor.Availability.Usable() {
			unavailable = append(unavailable, descriptor)
			continue
		}
		nameKey := strings.ToLower(descriptor.Name)
		if index, exists := byName[nameKey]; exists {
			loser := nameWinners[index]
			diagnostics = append(diagnostics, Diagnostic{Server: loser.Name, Source: loser.SourceID, Message: fmt.Sprintf("shadowed by %s", descriptor.ScopedID)})
			nameWinners[index] = descriptor
			continue
		}
		byName[nameKey] = len(nameWinners)
		nameWinners = append(nameWinners, descriptor)
	}
	// Semantic deduplication is a separate pass so a name replacement cannot
	// accidentally bypass a collision with a third descriptor.
	var winners []Descriptor
	bySemantic := make(map[string]int)
	for _, descriptor := range nameWinners {
		if index, exists := bySemantic[descriptor.SemanticKey]; exists {
			current := winners[index]
			if scopeRank(descriptor.Scope) > scopeRank(current.Scope) {
				diagnostics = append(diagnostics, Diagnostic{Server: current.Name, Source: current.SourceID, Message: fmt.Sprintf("semantic duplicate shadowed by %s", descriptor.ScopedID)})
				winners[index] = descriptor
			} else {
				diagnostics = append(diagnostics, Diagnostic{Server: descriptor.Name, Source: descriptor.SourceID, Message: fmt.Sprintf("semantic duplicate shadowed by %s", current.ScopedID)})
			}
			continue
		}
		bySemantic[descriptor.SemanticKey] = len(winners)
		winners = append(winners, descriptor)
	}
	// Unavailable definitions remain explainable but never suppress a callable
	// duplicate. Keep them after active winners in deterministic order.
	return append(winners, unavailable...), diagnostics
}

func credentialSetForConfigs(configs []Config) *redact.Set {
	var literals []string
	for _, config := range configs {
		values, err := CredentialLiterals(config)
		if err != nil {
			continue
		}
		literals = append(literals, values...)
	}
	return redact.New(literals...)
}

func validateManagerProjection(credentials *redact.Set, value any) error {
	if credentials == nil || credentials.Empty() {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("MCP manager projection could not be inspected")
	}
	reflected, err := credentials.JSONContains(encoded)
	if err != nil || reflected {
		return errors.New("MCP manager projection reflected configured credential material")
	}
	return nil
}

func scopeRank(scope Scope) int {
	switch scope {
	case ScopePlugin:
		return 0
	case ScopeAgentX:
		return 0
	case ScopeUser:
		return 1
	case ScopeProject:
		return 2
	case ScopeDynamic:
		return 3
	case ScopeLocal:
		return 4
	case ScopeManaged:
		return 5
	case ScopeEnterprise:
		return 6
	default:
		return -1
	}
}

func safeManagerError(err error) string {
	if err == nil {
		return ""
	}
	switch classifyMCPError(err) {
	case mcpErrorCancelled:
		return "cancelled"
	case mcpErrorDeadline:
		return "timed out"
	case mcpErrorUnavailable:
		return "unavailable"
	case mcpErrorUnsupportedTransport:
		return "unsupported transport"
	case mcpErrorProtocol:
		return "protocol error"
	default:
		return "operational failure"
	}
}

func (manager *Manager) Connection(name string) (Connection, bool) {
	manager.mu.RLock()
	if manager.closed || manager.active != 0 {
		manager.mu.RUnlock()
		return nil, false
	}
	generation := manager.generation
	id, ok := manager.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		manager.mu.RUnlock()
		return nil, false
	}
	entry, ok := manager.connections[id]
	manager.mu.RUnlock()
	if !ok || entry == nil || safeConnectionState(entry.connection) != StateConnected {
		return nil, false
	}
	manager.mu.RLock()
	current := !manager.closed && manager.active == 0 &&
		manager.generation == generation && manager.connections[id] == entry
	manager.mu.RUnlock()
	if !current {
		return nil, false
	}
	return entry.connection, true
}

// CallBoundTool invokes only the provider generation represented by binding.
// A reload that replaces the connection fails the manager-generation check; a
// reconnect of the same connection is checked atomically when Client registers
// its request, closing the check/use race.
func (manager *Manager) CallBoundTool(ctx context.Context, binding ToolBinding, name string, arguments map[string]any) (ToolResult, error) {
	if ctx == nil {
		return ToolResult{}, errors.New("MCP tool call context is nil")
	}
	// Queue admission and argument validation may block, so prepare without a
	// lifecycle lease. No provider authority or I/O exists at this stage.
	manager.mu.RLock()
	if manager.closed || manager.active != 0 || !binding.bound ||
		binding.scopedID == "" || manager.generation != binding.managerGeneration {
		manager.mu.RUnlock()
		return ToolResult{}, ErrStaleToolBinding
	}
	entry, ok := manager.connections[binding.scopedID]
	credentials := manager.credentials
	manager.mu.RUnlock()
	if !ok || entry == nil || entry.connection == nil {
		return ToolResult{}, ErrStaleToolBinding
	}
	caller, bindingAware := entry.connection.(BoundToolConnection)
	if binding.legacy {
		connectionGeneration, generationOK := safeConnectionGeneration(entry.connection)
		if bindingAware || !generationOK || connectionGeneration != binding.connectionGeneration {
			return ToolResult{}, ErrStaleToolBinding
		}
		// Plain custom Connection implementations have no catalog-invalidation
		// signal. Preserve their historical static-catalog behavior while
		// snapshotting the exact connection under a short manager lease.
		manager.mu.RLock()
		current, currentOK := manager.connections[binding.scopedID]
		stale := manager.closed || manager.active != 0 ||
			manager.generation != binding.managerGeneration ||
			!currentOK || current != entry || current.connection == nil
		manager.mu.RUnlock()
		if stale {
			return ToolResult{}, ErrStaleToolBinding
		}
		result, err := safeConnectionCallTool(entry.connection, ctx, name, arguments)
		return projectManagerToolResultBound(credentials, entry.maxMessageBytes, result, err)
	}
	if !bindingAware {
		return ToolResult{}, ErrStaleToolBinding
	}
	prepared, err := safePrepareToolCall(caller, ctx, name, arguments)
	if err != nil {
		return projectManagerToolResultBound(credentials, entry.maxMessageBytes, ToolResult{}, err)
	}
	if prepared == nil {
		return ToolResult{}, ErrProtocol
	}
	version := ToolCatalogVersion{
		ConnectionGeneration: binding.connectionGeneration,
		CatalogEpoch:         binding.catalogEpoch,
	}
	// Preparation grants no authority. Revalidate the manager before asking an
	// external implementation to register the captured provider version.
	manager.mu.RLock()
	current, currentOK := manager.connections[binding.scopedID]
	stale := manager.closed || manager.active != 0 ||
		manager.generation != binding.managerGeneration ||
		!currentOK || current != entry || current.connection == nil
	manager.mu.RUnlock()
	if stale {
		safeCancelToolPreparation(prepared)
		return ToolResult{}, ErrStaleToolBinding
	}
	// Register may be implemented by external in-process code, so it must never
	// run under a manager lifecycle lock. Registration owns no provider I/O.
	registered, err := safeRegisterToolCall(prepared, version)
	if err != nil {
		safeCancelToolPreparation(prepared)
		return projectManagerToolResultBound(credentials, entry.maxMessageBytes, ToolResult{}, err)
	}
	if registered == nil {
		safeCancelToolPreparation(prepared)
		return ToolResult{}, ErrProtocol
	}

	// The short lease linearizes manager acceptance after the connection has
	// atomically registered its generation/catalog authority but before Await
	// performs provider I/O.
	manager.mu.RLock()
	current, currentOK = manager.connections[binding.scopedID]
	stale = manager.closed || manager.active != 0 ||
		manager.generation != binding.managerGeneration ||
		!currentOK || current != entry || current.connection == nil
	manager.mu.RUnlock()
	if stale {
		safeCancelRegisteredToolCall(registered)
		return ToolResult{}, ErrStaleToolBinding
	}
	result, err := safeAwaitToolCall(registered)
	return projectManagerToolResultBound(credentials, entry.maxMessageBytes, result, err)
}

func projectManagerToolResult(credentials *redact.Set, result ToolResult, err error) (ToolResult, error) {
	return projectManagerToolResultBound(credentials, DefaultMaxMessageBytes, result, err)
}

func projectManagerToolResultBound(
	credentials *redact.Set, maxMessageBytes int, result ToolResult, err error,
) (ToolResult, error) {
	if err != nil {
		if err == ErrStaleToolBinding {
			return ToolResult{}, ErrStaleToolBinding
		}
		switch classifyMCPError(err) {
		case mcpErrorCancelled:
			return ToolResult{}, context.Canceled
		case mcpErrorDeadline:
			return ToolResult{}, context.DeadlineExceeded
		case mcpErrorClosed:
			return ToolResult{}, ErrClosed
		case mcpErrorProtocol:
			return ToolResult{}, ErrProtocol
		default:
			return ToolResult{}, errors.New(safeManagerError(err))
		}
	}
	normalized, err := normalizeManagerToolResult(result, maxMessageBytes)
	if err != nil {
		return ToolResult{}, ErrProtocol
	}
	if err := validateManagerProjection(credentials, normalized); err != nil {
		return ToolResult{}, ErrProtocol
	}
	return normalized, nil
}

func (manager *Manager) Reconnect(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lease, ok := manager.beginLifecycle(ctx)
	if !ok {
		manager.mu.RLock()
		closed := manager.closed
		manager.mu.RUnlock()
		if closed {
			return ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrUnavailable
	}
	defer manager.finishLifecycle(lease)

	manager.mu.Lock()
	if manager.closed || manager.active != lease.token {
		manager.mu.Unlock()
		return ErrClosed
	}
	id, ok := manager.byName[strings.ToLower(strings.TrimSpace(name))]
	entry := manager.connections[id]
	if !ok || entry == nil || entry.connection == nil || entry.busyToken != 0 {
		manager.mu.Unlock()
		return ErrUnavailable
	}
	entry.busyToken = lease.token
	manager.mu.Unlock()

	err := safeConnectionReconnect(entry.connection, ctx)
	state := safeConnectionState(entry.connection)
	manager.mu.Lock()
	if manager.closed || manager.active != lease.token || manager.connections[id] != entry {
		manager.mu.Unlock()
		return ErrClosed
	}
	manager.generation++
	manager.snapshot.Generation = manager.generation
	for index := range manager.snapshot.Servers {
		manager.snapshot.Servers[index].Generation = manager.generation
		if manager.snapshot.Servers[index].ScopedID == id {
			manager.snapshot.Servers[index].State = state
		}
	}
	manager.mu.Unlock()
	if err == nil {
		return nil
	}
	return errors.New(safeManagerError(err))
}

func (manager *Manager) Tools(ctx context.Context) ([]ToolDescriptor, []Diagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Discovery I/O runs without a lifecycle lock. A short publication lease
	// below revalidates its manager/connection generation before bindings become
	// visible.
	manager.mu.RLock()
	if manager.closed || manager.active != 0 {
		manager.mu.RUnlock()
		if manager.closed {
			return nil, nil, ErrClosed
		}
		return nil, nil, ErrStaleToolBinding
	}
	managerGeneration := manager.generation
	lifecycle := manager.lifecycle
	credentials := manager.credentials
	names := make([]string, 0, len(manager.byName))
	for name := range manager.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	type listedConnection struct {
		scopedID string
		name     string
		entry    *managedConnection
	}
	connections := make([]listedConnection, 0, len(names))
	for _, name := range names {
		id := manager.byName[name]
		if entry, ok := manager.connections[id]; ok {
			connections = append(connections, listedConnection{scopedID: id, name: name, entry: entry})
		}
	}
	manager.mu.RUnlock()
	var tools []ToolDescriptor
	var diagnostics []Diagnostic
	type listedCatalog struct {
		entry   listedConnection
		tools   []ToolDescriptor
		version ToolCatalogVersion
		legacy  bool
	}
	var catalogs []listedCatalog
	for _, entry := range connections {
		if entry.entry == nil || entry.entry.connection == nil {
			continue
		}
		connection := entry.entry.connection
		lister, ok := connection.(BoundToolConnection)
		if !ok {
			listed, listDiagnostics, err := safeConnectionListTools(connection, ctx)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: entry.name, Message: safeManagerError(err)})
				continue
			}
			listed, listDiagnostics, err = normalizeManagerToolCatalog(
				entry.name, listed, listDiagnostics, entry.entry.maxMessageBytes,
			)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: entry.name, Message: "tool catalog rejected"})
				continue
			}
			diagnostics = append(diagnostics, listDiagnostics...)
			generation, generationOK := safeConnectionGeneration(connection)
			if !generationOK {
				diagnostics = append(diagnostics, Diagnostic{Server: entry.name, Message: "tool catalog changed during discovery"})
				continue
			}
			catalogs = append(catalogs, listedCatalog{
				entry: entry, tools: listed, legacy: true,
				version: ToolCatalogVersion{ConnectionGeneration: generation},
			})
			continue
		}
		listed, listDiagnostics, version, err := safeConnectionListToolsBound(lister, ctx)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Server: entry.name, Message: safeManagerError(err)})
			continue
		}
		listed, listDiagnostics, err = normalizeManagerToolCatalog(
			entry.name, listed, listDiagnostics, entry.entry.maxMessageBytes,
		)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Server: entry.name, Message: "tool catalog rejected"})
			continue
		}
		diagnostics = append(diagnostics, listDiagnostics...)
		catalogs = append(catalogs, listedCatalog{entry: entry, tools: listed, version: version})
	}

	validCatalogs := catalogs[:0]
	for _, catalog := range catalogs {
		generation, ok := safeConnectionGeneration(catalog.entry.entry.connection)
		if !ok || generation != catalog.version.ConnectionGeneration {
			diagnostics = append(diagnostics, Diagnostic{Server: catalog.entry.name, Message: "tool catalog changed during discovery"})
			continue
		}
		validCatalogs = append(validCatalogs, catalog)
	}
	catalogs = validCatalogs

	manager.mu.RLock()
	if manager.closed || manager.active != 0 ||
		manager.generation != managerGeneration || manager.lifecycle != lifecycle {
		manager.mu.RUnlock()
		return nil, nil, ErrStaleToolBinding
	}
	currentConnections := make(map[string]*managedConnection, len(catalogs))
	for _, catalog := range catalogs {
		currentConnections[catalog.entry.scopedID] = manager.connections[catalog.entry.scopedID]
	}
	manager.mu.RUnlock()
	for _, catalog := range catalogs {
		current, ok := currentConnections[catalog.entry.scopedID]
		if !ok || current == nil || current != catalog.entry.entry ||
			current.connection == nil ||
			safeConnectionState(current.connection) != StateConnected {
			diagnostics = append(diagnostics, Diagnostic{Server: catalog.entry.name, Message: "tool catalog changed during discovery"})
			continue
		}
		generation, generationOK := safeConnectionGeneration(current.connection)
		if !generationOK || generation != catalog.version.ConnectionGeneration {
			diagnostics = append(diagnostics, Diagnostic{Server: catalog.entry.name, Message: "tool catalog changed during discovery"})
			continue
		}
		manager.mu.RLock()
		stillCurrent := !manager.closed && manager.active == 0 &&
			manager.generation == managerGeneration && manager.lifecycle == lifecycle &&
			manager.connections[catalog.entry.scopedID] == current
		manager.mu.RUnlock()
		if !stillCurrent {
			return nil, nil, ErrStaleToolBinding
		}
		for index := range catalog.tools {
			catalog.tools[index].binding = ToolBinding{
				scopedID: catalog.entry.scopedID, managerGeneration: managerGeneration,
				connectionGeneration: catalog.version.ConnectionGeneration,
				catalogEpoch:         catalog.version.CatalogEpoch, bound: true, legacy: catalog.legacy,
			}
		}
		tools = append(tools, catalog.tools...)
	}
	if err := validateManagerProjection(credentials, struct {
		Tools       []ToolDescriptor `json:"tools"`
		Diagnostics []Diagnostic     `json:"diagnostics"`
	}{Tools: tools, Diagnostics: diagnostics}); err != nil {
		return nil, nil, ErrProtocol
	}
	return tools, diagnostics, nil
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	if manager.closed {
		done := manager.closeDone
		timeout := manager.closeTimeout
		manager.mu.Unlock()
		select {
		case <-done:
			manager.mu.RLock()
			result := manager.closeErr
			manager.mu.RUnlock()
			return result
		case <-time.After(normalizeCloseTimeout(timeout)):
			return errors.New("one or more MCP provider connections failed to close")
		}
	}
	manager.closed = true
	manager.lifecycle++
	close(manager.shutdown)
	connections := manager.connections
	inflight := manager.inflight
	manager.connections = make(map[string]*managedConnection)
	manager.inflight = make(map[uint64][]*managedConnection)
	manager.byName = make(map[string]string)
	for index := range manager.snapshot.Servers {
		if _, ok := connections[manager.snapshot.Servers[index].ScopedID]; ok {
			manager.snapshot.Servers[index].State = StateClosed
		}
	}
	manager.mu.Unlock()
	// Initiate every independent provider close before waiting for any one of
	// them. A stubborn subprocess must not prevent later siblings from receiving
	// their own bounded Client.Close termination sequence.
	ids := make([]string, 0, len(connections))
	for id := range connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	targets := make([]connectionCloseTarget, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, connectionCloseTarget{id: id, entry: connections[id]})
	}
	for token, entries := range inflight {
		for index, entry := range entries {
			targets = append(targets, connectionCloseTarget{
				id: fmt.Sprintf("inflight-%020d-%08d", token, index), disposition: "shutdown", entry: entry,
			})
		}
	}
	result := closeConnections(targets, manager.closeTimeout)
	manager.mu.Lock()
	manager.closeErr = result
	close(manager.closeDone)
	manager.mu.Unlock()
	return result
}

func closeConnections(targets []connectionCloseTarget, timeout time.Duration) error {
	if len(targets) == 0 {
		return nil
	}
	timeout = normalizeCloseTimeout(timeout)
	targets = append([]connectionCloseTarget(nil), targets...)
	sort.SliceStable(targets, func(i, j int) bool {
		left, right := closeTargetKey(targets[i]), closeTargetKey(targets[j])
		return left < right
	})
	type closeResult struct {
		key string
		err error
	}
	results := make(chan closeResult, len(targets))
	pending := make(map[string]struct{}, len(targets))
	for index, target := range targets {
		key := fmt.Sprintf("%08d\x00%s", index, closeTargetKey(target))
		pending[key] = struct{}{}
		go func(key string, entry *managedConnection) {
			err := entry.close()
			results <- closeResult{key: key, err: err}
		}(key, target.entry)
	}
	byKey := make(map[string]error, len(targets))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case result := <-results:
			if _, exists := pending[result.key]; !exists {
				continue
			}
			delete(pending, result.key)
			byKey[result.key] = result.err
		case <-timer.C:
			for key := range pending {
				byKey[key] = errors.New("provider close timed out")
			}
			pending = nil
		}
	}
	failures := 0
	for index, target := range targets {
		key := fmt.Sprintf("%08d\x00%s", index, closeTargetKey(target))
		if err := byKey[key]; err != nil {
			failures++
		}
	}
	if failures > 0 {
		return errors.New("one or more MCP provider connections failed to close")
	}
	return nil
}

func normalizeCloseTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultManagerCloseTimeout
	}
	return timeout
}

func closeConnection(connection Connection) (err error) {
	if connection == nil {
		return errors.New("provider connection is unavailable")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("provider close panicked")
		}
	}()
	return connection.Close()
}

func closeTargetKey(target connectionCloseTarget) string {
	return target.id + "\x00" + target.disposition
}

func closeTargetLabel(target connectionCloseTarget) string {
	if target.disposition == "" {
		return target.id
	}
	return target.id + " (" + target.disposition + ")"
}
