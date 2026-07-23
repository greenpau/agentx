package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/greenpau/agentx/pkg/childenv"
	"github.com/greenpau/agentx/pkg/redact"
)

type wireRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type wireNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type pendingResponse struct {
	result json.RawMessage
	err    error
}

type pendingCall struct {
	response   chan pendingResponse
	method     string
	generation uint64
}

// ToolCatalogVersion identifies one connection generation and one advertised
// tool-catalog epoch. Binding-aware Connection implementations must validate
// both values atomically with request registration before provider I/O.
type ToolCatalogVersion struct {
	ConnectionGeneration uint64
	CatalogEpoch         uint64
}

// Connection is the lifecycle and request surface consumed by Manager. Client
// is its stdio implementation; other transports remain explicitly unavailable.
// Plain implementations retain legacy static-catalog behavior. Implement
// BoundToolConnection as well when a live catalog must prove call-time
// freshness before provider I/O.
type Connection interface {
	Connect(context.Context) error
	Reconnect(context.Context) error
	Close() error
	State() ConnectionState
	LastError() string
	Generation() uint64
	InitializeResult() InitializeResult
	ListTools(context.Context) ([]ToolDescriptor, []Diagnostic, error)
	ListResources(context.Context) ([]ResourceDescriptor, []Diagnostic, error)
	ListResourceTemplates(context.Context) ([]ResourceTemplate, []Diagnostic, error)
	ListPrompts(context.Context) ([]PromptDescriptor, []Diagnostic, error)
	CallTool(context.Context, string, map[string]any) (ToolResult, error)
	ReadResource(context.Context, string) (ResourceResult, error)
	GetPrompt(context.Context, string, map[string]string) (PromptResult, error)
}

// BoundToolConnection closes the discovery-to-invocation authority gap for
// tool providers that can change their catalog without reconnecting.
type BoundToolConnection interface {
	Connection
	ListToolsBound(context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error)
	PrepareToolCall(context.Context, string, map[string]any) (ToolCallPreparation, error)
}

// ToolCallPreparation owns bounded validation and queue admission but no
// provider authority or I/O. Manager invokes Register under its short
// lifecycle lease after revalidating the manager generation.
type ToolCallPreparation interface {
	Register(ToolCatalogVersion) (RegisteredToolCall, error)
	Cancel()
}

// RegisteredToolCall owns one atomically registered provider request. Await
// performs provider I/O without holding Manager lifecycle locks.
type RegisteredToolCall interface {
	Await() (ToolResult, error)
	Cancel()
}

// Client owns one stdio child process and one JSON-RPC connection generation.
type Client struct {
	config      Config
	credentials *redact.Set

	mu             sync.RWMutex
	state          ConnectionState
	lastError      string
	generation     uint64
	initialize     InitializeResult
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	lifetimeCancel context.CancelFunc
	done           chan struct{}
	closing        bool
	pending        map[uint64]*pendingCall
	nextID         uint64

	writeMu sync.Mutex
	active  chan struct{}
	slots   chan struct{}

	cacheMu           sync.RWMutex
	tools             []ToolDescriptor
	resources         []ResourceDescriptor
	resourceTemplates []ResourceTemplate
	prompts           []PromptDescriptor
	toolsEpoch        uint64
	resourcesEpoch    uint64
	promptsEpoch      uint64
}

// String and GoString expose no retained process configuration, environment,
// headers, command environment, pending payloads, or provider state.
func (*Client) String() string   { return "" }
func (*Client) GoString() string { return "" }
func (client *Client) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, client.String())
}

func NewClient(raw Config) (*Client, error) {
	descriptor, err := ValidateConfig(raw)
	if err != nil {
		return nil, err
	}
	if descriptor.Transport != TransportStdio {
		return nil, ErrUnsupportedTransport
	}
	if !descriptor.Availability.Usable() {
		return nil, ErrUnavailable
	}
	client := newClientFromValidated(descriptor.config)
	if err := client.validateInitialCredentialCompatibility(); err != nil {
		return nil, err
	}
	return client, nil
}

func newClientFromValidated(config Config) *Client {
	literals, err := CredentialLiterals(config)
	if err != nil {
		literals = nil
	}
	return &Client{
		config: cloneConfig(config), credentials: redact.New(literals...), state: StatePending,
		pending: make(map[uint64]*pendingCall),
		active:  make(chan struct{}, DefaultConcurrency),
		slots:   make(chan struct{}, DefaultConcurrency+DefaultQueueDepth),
	}
}

func (client *Client) validateInitialCredentialCompatibility() error {
	if client.credentials == nil || client.credentials.Empty() {
		return nil
	}
	values := []any{
		wireRequest{
			JSONRPC: "2.0", ID: 1, Method: "initialize",
			Params: map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]string{"name": "agentx", "version": "1"},
			},
		},
		wireNotification{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return errors.New("inspect MCP bootstrap request")
		}
		encoded = append(encoded, '\n')
		reflected, inspectionErr := client.credentials.JSONContains(encoded)
		if inspectionErr != nil || reflected {
			return errors.New("MCP credential is incompatible with required protocol framing")
		}
	}
	return nil
}

func (client *Client) State() ConnectionState {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.state
}

func (client *Client) LastError() string {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.lastError
}

func (client *Client) Generation() uint64 {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.generation
}

func (client *Client) InitializeResult() InitializeResult {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return cloneInitializeResult(client.initialize)
}

func cloneInitializeResult(source InitializeResult) InitializeResult {
	result := source
	result.Capabilities.Tools = cloneAnyMap(source.Capabilities.Tools)
	result.Capabilities.Resources = cloneAnyMap(source.Capabilities.Resources)
	result.Capabilities.Prompts = cloneAnyMap(source.Capabilities.Prompts)
	result.Capabilities.Logging = cloneAnyMap(source.Capabilities.Logging)
	return result
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneJSONValue(value)
	}
	return result
}

func (client *Client) Connect(ctx context.Context) error {
	client.mu.Lock()
	if client.state == StateConnected {
		client.mu.Unlock()
		return nil
	}
	if client.cmd != nil {
		client.mu.Unlock()
		return errors.New("MCP connection attempt already active")
	}
	client.closing = false
	client.lastError = ""
	client.state = StatePending
	lifetimeContext, lifetimeCancel := context.WithCancel(context.Background())
	environment, environmentErr := childenv.MCP(os.Environ(), client.config.Env)
	if environmentErr != nil {
		lifetimeCancel()
		client.state = StateFailed
		client.lastError = "invalid child environment"
		client.mu.Unlock()
		return errors.New("construct MCP child environment")
	}
	command := exec.CommandContext(lifetimeContext, client.config.Command, client.config.Args...)
	configureMCPCommand(command)
	command.Env = environment
	command.Dir = client.config.WorkingDirectory
	stdin, err := command.StdinPipe()
	if err != nil {
		lifetimeCancel()
		client.state = StateFailed
		client.lastError = "create stdin pipe"
		client.mu.Unlock()
		return errors.New("create MCP stdin pipe")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		client.state = StateFailed
		client.lastError = "create stdout pipe"
		client.mu.Unlock()
		return errors.New("create MCP stdout pipe")
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		client.state = StateFailed
		client.lastError = "create stderr pipe"
		client.mu.Unlock()
		return errors.New("create MCP stderr pipe")
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		client.state = StateFailed
		client.lastError = "start process"
		client.mu.Unlock()
		return errors.New("start MCP server process")
	}
	client.generation++
	generation := client.generation
	client.cmd = command
	client.stdin = stdin
	client.lifetimeCancel = lifetimeCancel
	client.done = make(chan struct{})
	client.pending = make(map[uint64]*pendingCall)
	client.initialize = InitializeResult{}
	client.mu.Unlock()

	go client.readLoop(stdout, generation)
	go client.drainStderr(stderr)
	go client.waitProcess(command, generation)

	connectTimeout := client.config.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	connectContext, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	var initialized InitializeResult
	err = client.request(connectContext, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "agentx", "version": "1"},
	}, &initialized)
	if err != nil {
		client.failGeneration(generation, fmt.Errorf("initialize: %w", err))
		_ = client.closeGeneration(generation)
		return publicMCPClientError("initialize MCP server", err)
	}
	if err := validateInitializeResult(initialized); err != nil {
		client.failGeneration(generation, err)
		_ = client.closeGeneration(generation)
		return fmt.Errorf("%w: invalid initialize response", ErrProtocol)
	}
	if len(initialized.Instructions) > MaxDescriptionBytes {
		initialized.Instructions = initialized.Instructions[:MaxDescriptionBytes]
	}
	if err := client.validatePublicProjection(initialized); err != nil {
		client.failGeneration(generation, err)
		_ = client.closeGeneration(generation)
		return err
	}
	if err := client.notifyGeneration(connectContext, generation, "notifications/initialized", map[string]any{}); err != nil {
		client.failGeneration(generation, fmt.Errorf("send initialized notification: %w", err))
		_ = client.closeGeneration(generation)
		return publicMCPClientError("send MCP initialized notification", err)
	}
	return client.completeInitialization(generation, initialized)
}

func (client *Client) completeInitialization(generation uint64, initialized InitializeResult) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.generation != generation || client.closing || client.cmd == nil || client.state != StatePending {
		return ErrClosed
	}
	client.initialize = cloneInitializeResult(initialized)
	client.state = StateConnected
	client.lastError = ""
	return nil
}

func (client *Client) Reconnect(ctx context.Context) error {
	if err := client.Close(); err != nil {
		return err
	}
	return client.Connect(ctx)
}

func (client *Client) Close() error {
	client.mu.RLock()
	generation := client.generation
	client.mu.RUnlock()
	return client.closeGeneration(generation)
}

func (client *Client) closeGeneration(generation uint64) error {
	client.mu.Lock()
	if client.generation != generation || client.cmd == nil {
		if client.cmd == nil {
			client.state = StateClosed
		}
		client.mu.Unlock()
		return nil
	}
	client.closing = true
	stdin := client.stdin
	cancel := client.lifetimeCancel
	done := client.done
	command := client.cmd
	client.state = StateClosed
	client.failPendingLocked(ErrClosed)
	client.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			if command != nil {
				_ = forceKillMCPCommand(command)
			}
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				return errors.New("MCP process did not exit after forced termination")
			}
		}
	}
	client.mu.Lock()
	if client.generation == generation {
		client.cmd = nil
		client.stdin = nil
		client.lifetimeCancel = nil
		client.done = nil
		client.closing = false
	}
	client.mu.Unlock()
	client.invalidateAllCaches()
	return nil
}

func (client *Client) readLoop(stdout io.Reader, generation uint64) {
	scanner := bufio.NewScanner(stdout)
	maxBytes := client.config.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	scanner.Buffer(make([]byte, 64<<10), maxBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := client.handleMessage(line, generation); err != nil {
			client.failGeneration(generation, err)
			go func() { _ = client.closeGeneration(generation) }()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		client.failGeneration(generation, fmt.Errorf("read protocol stream: %w", err))
		go func() { _ = client.closeGeneration(generation) }()
		return
	}
	client.failGeneration(generation, errors.New("MCP protocol stream closed"))
	go func() { _ = client.closeGeneration(generation) }()
}

func (client *Client) handleMessage(line []byte, generation uint64) error {
	client.mu.RLock()
	current := client.generation == generation && !client.closing && client.cmd != nil
	client.mu.RUnlock()
	if !current {
		// A retired read loop may drain bytes after reconnect. Its responses,
		// requests, and invalidation notifications have no authority over the
		// current generation.
		return nil
	}
	if err := client.validateInboundEnvelope(line); err != nil {
		return err
	}
	var message wireResponse
	if err := json.Unmarshal(line, &message); err != nil {
		return fmt.Errorf("%w: malformed JSON-RPC message", ErrProtocol)
	}
	if message.JSONRPC != "2.0" {
		return fmt.Errorf("%w: unsupported JSON-RPC version", ErrProtocol)
	}
	if len(message.ID) > 0 && string(message.ID) != "null" {
		id, err := parseResponseID(message.ID)
		if err != nil {
			return err
		}
		if message.Method != "" {
			// Server-initiated requests such as elicitation are not enabled by
			// this profile. Return an explicit protocol failure exactly once.
			return client.respondError(generation, id, -32601, "server-initiated requests are unavailable")
		}
		client.mu.Lock()
		pending, ok := client.pending[id]
		if ok && pending.generation == generation && client.generation == generation && !client.closing {
			delete(client.pending, id)
		} else {
			ok = false
		}
		client.mu.Unlock()
		if ok {
			response := pendingResponse{result: append(json.RawMessage(nil), message.Result...)}
			if message.Error != nil {
				response.err = fmt.Errorf("%w: server returned JSON-RPC error %d", ErrProtocol, message.Error.Code)
			}
			pending.response <- response
		}
		return nil
	}
	if message.Method != "" {
		client.handleNotification(generation, message.Method)
		return nil
	}
	return fmt.Errorf("%w: response has neither id nor method", ErrProtocol)
}

func parseResponseID(raw json.RawMessage) (uint64, error) {
	var numeric uint64
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := strconv.ParseUint(text, 10, 64)
		if err == nil {
			return value, nil
		}
	}
	return 0, fmt.Errorf("%w: invalid response id", ErrProtocol)
}

func (client *Client) respondError(generation, id uint64, code int, message string) error {
	timeout := client.config.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return client.writeJSONGeneration(ctx, generation, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

func (client *Client) handleNotification(generation uint64, method string) {
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.generation != generation || client.closing || client.cmd == nil {
		return
	}
	client.cacheMu.Lock()
	switch method {
	case "notifications/tools/list_changed":
		client.toolsEpoch++
		client.tools = nil
	case "notifications/resources/list_changed":
		client.resourcesEpoch++
		client.resources = nil
		client.resourceTemplates = nil
	case "notifications/prompts/list_changed":
		client.promptsEpoch++
		client.prompts = nil
	}
	client.cacheMu.Unlock()
}

func (client *Client) waitProcess(command *exec.Cmd, generation uint64) {
	err := command.Wait()
	client.mu.Lock()
	if client.generation != generation {
		client.mu.Unlock()
		return
	}
	closing := client.closing
	if !closing {
		client.state = StateFailed
		client.lastError = "MCP server process exited"
		client.failPendingLocked(errors.New("MCP server process exited"))
	}
	done := client.done
	client.mu.Unlock()
	if done != nil {
		close(done)
	}
	_ = err // Exit diagnostics intentionally exclude potentially secret stderr.
}

func (client *Client) drainStderr(stderr io.Reader) {
	// Draining prevents child-process deadlock. The content is deliberately not
	// retained in ordinary state because servers commonly print credentials.
	_, _ = io.Copy(io.Discard, stderr)
}

func (client *Client) failGeneration(generation uint64, err error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.generation != generation || client.closing {
		return
	}
	client.state = StateFailed
	client.lastError = safeError(err)
	client.failPendingLocked(err)
}

func (client *Client) failPendingLocked(err error) {
	for id, pending := range client.pending {
		delete(client.pending, id)
		pending.response <- pendingResponse{err: err}
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	switch classifyMCPError(err) {
	case mcpErrorDeadline:
		return "operation timed out"
	case mcpErrorCancelled:
		return "operation cancelled"
	case mcpErrorProtocol:
		return "protocol error"
	default:
		return "connection failed"
	}
}

func (client *Client) request(ctx context.Context, method string, params any, output any) error {
	if err := client.acquire(ctx); err != nil {
		return err
	}
	defer client.release()
	client.mu.Lock()
	if client.cmd == nil || client.closing || client.state == StateFailed || client.state == StateClosed || client.state == StateDisabled {
		client.mu.Unlock()
		return ErrClosed
	}
	client.nextID++
	id := client.nextID
	generation := client.generation
	pending := &pendingCall{response: make(chan pendingResponse, 1), method: method, generation: generation}
	client.pending[id] = pending
	client.mu.Unlock()

	request := wireRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := client.writeJSONGeneration(ctx, generation, request); err != nil {
		client.removePending(id, pending)
		return err
	}
	select {
	case response := <-pending.response:
		return client.decodePendingResponse(response, output)
	case <-ctx.Done():
		if client.removePending(id, pending) {
			notifyCtx, cancel := context.WithTimeout(context.Background(), min(client.config.RequestTimeout, 2*time.Second))
			_ = client.notifyGeneration(notifyCtx, generation, "notifications/cancelled", map[string]any{"requestId": id, "reason": "request context ended"})
			cancel()
			return ctx.Err()
		}
		// A response won the removal race and is already buffered.
		response := <-pending.response
		return client.decodePendingResponse(response, output)
	}
}

func (client *Client) decodePendingResponse(response pendingResponse, output any) error {
	if response.err != nil {
		return publicMCPClientError("MCP request failed", response.err)
	}
	if output == nil {
		return nil
	}
	if len(response.result) == 0 {
		return fmt.Errorf("%w: response result is missing", ErrProtocol)
	}
	if err := json.Unmarshal(response.result, output); err != nil {
		return fmt.Errorf("%w: decode result", ErrProtocol)
	}
	return nil
}

func (client *Client) validateInboundEnvelope(encoded []byte) error {
	if client.credentials == nil || client.credentials.Empty() {
		return nil
	}
	reflected, err := client.credentials.JSONContains(encoded)
	if err != nil || reflected {
		return fmt.Errorf("%w: provider response reflected configured credential material", ErrProtocol)
	}
	return nil
}

func (client *Client) validatePublicProjection(value any) error {
	if client.credentials == nil || client.credentials.Empty() {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: provider result could not be safely projected", ErrProtocol)
	}
	reflected, inspectErr := client.credentials.JSONContains(encoded)
	if inspectErr != nil || reflected {
		return fmt.Errorf("%w: provider result reflected configured credential material", ErrProtocol)
	}
	return nil
}

func publicMCPClientError(category string, err error) error {
	switch classifyMCPError(err) {
	case mcpErrorCancelled:
		return context.Canceled
	case mcpErrorDeadline:
		return context.DeadlineExceeded
	case mcpErrorClosed:
		return ErrClosed
	case mcpErrorProtocol:
		return fmt.Errorf("%w: %s", ErrProtocol, category)
	default:
		return errors.New(category)
	}
}

func (client *Client) removePending(id uint64, expected *pendingCall) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	current, ok := client.pending[id]
	if !ok || current != expected {
		return false
	}
	delete(client.pending, id)
	return true
}

func (client *Client) notifyGeneration(ctx context.Context, generation uint64, method string, params any) error {
	return client.writeJSONGeneration(ctx, generation, wireNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (client *Client) writeJSON(ctx context.Context, value any) error {
	client.mu.RLock()
	generation := client.generation
	client.mu.RUnlock()
	return client.writeJSONGeneration(ctx, generation, value)
}

func (client *Client) writeJSONGeneration(ctx context.Context, generation uint64, value any) error {
	if ctx == nil {
		return errors.New("MCP write context is nil")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	maxBytes := client.config.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	if len(encoded)+1 > maxBytes {
		return errors.New("MCP request exceeds message size limit")
	}
	encoded = append(encoded, '\n')
	if client.credentials != nil && !client.credentials.Empty() {
		reflected, inspectionErr := client.credentials.JSONContains(encoded)
		if inspectionErr != nil || reflected {
			return errors.New("MCP request could not be safely encoded")
		}
	}
	completed := make(chan error, 1)
	go func() {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			completed <- err
			return
		}
		client.mu.RLock()
		stdin := client.stdin
		closed := client.cmd == nil || client.closing || client.generation != generation
		client.mu.RUnlock()
		if closed || stdin == nil {
			completed <- ErrClosed
			return
		}
		if err := writeFull(stdin, encoded); err != nil {
			completed <- fmt.Errorf("write MCP request: %w", err)
			return
		}
		completed <- nil
	}()
	select {
	case err := <-completed:
		return err
	case <-ctx.Done():
		// A pipe write has no context-aware API. Closing the generation's stdin
		// is the only portable way to unblock it; the connection is failed so no
		// later request can observe a half-written JSON-RPC frame.
		client.interruptGenerationWrite(generation, ctx.Err())
		return ctx.Err()
	}
}

func (client *Client) interruptGenerationWrite(generation uint64, cause error) {
	client.mu.Lock()
	if client.generation != generation || client.cmd == nil || client.closing {
		client.mu.Unlock()
		return
	}
	client.closing = true
	client.state = StateFailed
	client.lastError = safeError(cause)
	stdin := client.stdin
	cancel := client.lifetimeCancel
	client.failPendingLocked(cause)
	client.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	go func() { _ = client.closeGeneration(generation) }()
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrNoProgress
		}
		data = data[written:]
	}
	return nil
}

func (client *Client) acquire(ctx context.Context) error {
	select {
	case client.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case client.active <- struct{}{}:
		return nil
	case <-ctx.Done():
		<-client.slots
		return ctx.Err()
	}
}

func (client *Client) release() {
	<-client.active
	<-client.slots
}

func (client *Client) invalidateAllCaches() {
	client.cacheMu.Lock()
	client.toolsEpoch++
	client.resourcesEpoch++
	client.promptsEpoch++
	client.tools = nil
	client.resources = nil
	client.resourceTemplates = nil
	client.prompts = nil
	client.cacheMu.Unlock()
}

func validateInitializeResult(result InitializeResult) error {
	if result.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version", ErrProtocol)
	}
	name := strings.TrimSpace(result.ServerInfo.Name)
	if name == "" || len(name) > MaxServerNameBytes || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: invalid server identity", ErrProtocol)
	}
	if len(result.ServerInfo.Version) > MaxDescriptionBytes || strings.IndexFunc(result.ServerInfo.Version, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: invalid server version", ErrProtocol)
	}
	for _, capability := range []struct {
		name  string
		value map[string]any
	}{
		{name: "tools", value: result.Capabilities.Tools},
		{name: "resources", value: result.Capabilities.Resources},
		{name: "prompts", value: result.Capabilities.Prompts},
		{name: "logging", value: result.Capabilities.Logging},
	} {
		if capability.value != nil {
			if err := validateJSONDepth(capability.value, 0); err != nil {
				return fmt.Errorf("%w: invalid %s capability: %v", ErrProtocol, capability.name, err)
			}
		}
	}
	return nil
}
