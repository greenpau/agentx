package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

func (client *Client) ListTools(ctx context.Context) ([]ToolDescriptor, []Diagnostic, error) {
	client.cacheMu.RLock()
	epoch := client.toolsEpoch
	if client.tools != nil {
		tools := cloneTools(client.tools)
		client.cacheMu.RUnlock()
		return tools, nil, nil
	}
	client.cacheMu.RUnlock()
	if client.InitializeResult().Capabilities.Tools == nil {
		return nil, nil, fmt.Errorf("%w: server does not advertise tools", ErrUnavailable)
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var all []ToolDescriptor
	var diagnostics []Diagnostic
	seen := make(map[string]bool)
	cursor := ""
	for page := 0; page < DefaultMaxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Tools      []ToolDescriptor `json:"tools"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
		if err := client.request(requestContext, "tools/list", params, &response); err != nil {
			return nil, nil, err
		}
		for _, tool := range response.Tools {
			if err := validateToolDescriptor(tool); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "tool omitted: invalid descriptor"})
				continue
			}
			if seen[tool.Name] {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "duplicate tool omitted"})
				continue
			}
			seen[tool.Name] = true
			tool.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
			all = append(all, tool)
			if len(all) > DefaultMaxListItems {
				return nil, nil, fmt.Errorf("%w: tools list exceeds item limit", ErrProtocol)
			}
		}
		if response.NextCursor == "" {
			if err := client.validatePublicProjection(struct {
				Tools       []ToolDescriptor `json:"tools"`
				Diagnostics []Diagnostic     `json:"diagnostics"`
			}{Tools: all, Diagnostics: diagnostics}); err != nil {
				return nil, nil, err
			}
			client.cacheToolsIfCurrent(epoch, all)
			return cloneTools(all), diagnostics, nil
		}
		if response.NextCursor == cursor || len(response.NextCursor) > 2_048 {
			return nil, nil, fmt.Errorf("%w: invalid tools pagination cursor", ErrProtocol)
		}
		cursor = response.NextCursor
	}
	return nil, nil, fmt.Errorf("%w: tools pagination exceeded page limit", ErrProtocol)
}

// NamespacedTools returns a model-facing projection while preserving the
// server's original names for protocol invocation.
func (client *Client) NamespacedTools(ctx context.Context) ([]ToolDescriptor, []Diagnostic, error) {
	tools, diagnostics, err := client.ListTools(ctx)
	if err != nil {
		return nil, nil, err
	}
	result := make([]ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		name, err := NamespacedToolName(client.config.Name, tool.Name)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "tool omitted: namespacing failed"})
			continue
		}
		tool.Name = name
		result = append(result, tool)
	}
	if err := client.validatePublicProjection(struct {
		Tools       []ToolDescriptor `json:"tools"`
		Diagnostics []Diagnostic     `json:"diagnostics"`
	}{Tools: result, Diagnostics: diagnostics}); err != nil {
		return nil, nil, err
	}
	return result, diagnostics, nil
}

// listToolsBound returns a namespaced catalog together with the connection
// generation and list-changed epoch that supplied it. A notification racing
// discovery invalidates the candidate and causes one bounded rediscovery.
func (client *Client) ListToolsBound(ctx context.Context) ([]ToolDescriptor, []Diagnostic, ToolCatalogVersion, error) {
	var diagnostics []Diagnostic
	for attempt := 0; attempt < 2; attempt++ {
		generation, epoch, cached, connected := client.toolCatalogSnapshot()
		if !connected {
			return nil, nil, ToolCatalogVersion{}, ErrStaleToolBinding
		}
		if cached == nil {
			_, listedDiagnostics, err := client.ListTools(ctx)
			diagnostics = append(diagnostics, listedDiagnostics...)
			if err != nil {
				return nil, nil, ToolCatalogVersion{}, err
			}
			currentGeneration, currentEpoch, current, currentConnected := client.toolCatalogSnapshot()
			if !currentConnected || currentGeneration != generation || currentEpoch != epoch || current == nil {
				continue
			}
			cached = current
		}
		result := make([]ToolDescriptor, 0, len(cached))
		for _, tool := range cached {
			name, err := NamespacedToolName(client.config.Name, tool.Name)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "tool omitted: namespacing failed"})
				continue
			}
			tool.Name = name
			result = append(result, tool)
		}
		version := ToolCatalogVersion{
			ConnectionGeneration: generation,
			CatalogEpoch:         epoch,
		}
		if err := client.validatePublicProjection(struct {
			Tools       []ToolDescriptor   `json:"tools"`
			Diagnostics []Diagnostic       `json:"diagnostics"`
			Version     ToolCatalogVersion `json:"version"`
		}{Tools: result, Diagnostics: diagnostics, Version: version}); err != nil {
			return nil, nil, ToolCatalogVersion{}, err
		}
		return result, diagnostics, version, nil
	}
	return nil, nil, ToolCatalogVersion{}, ErrStaleToolBinding
}

func (client *Client) toolCatalogSnapshot() (uint64, uint64, []ToolDescriptor, bool) {
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.cmd == nil || client.closing || client.state != StateConnected {
		return 0, 0, nil, false
	}
	generation := client.generation
	client.cacheMu.RLock()
	defer client.cacheMu.RUnlock()
	var tools []ToolDescriptor
	if client.tools != nil {
		tools = cloneTools(client.tools)
	}
	return generation, client.toolsEpoch, tools, true
}

func (client *Client) ListResources(ctx context.Context) ([]ResourceDescriptor, []Diagnostic, error) {
	client.cacheMu.RLock()
	epoch := client.resourcesEpoch
	if client.resources != nil {
		result := append([]ResourceDescriptor(nil), client.resources...)
		client.cacheMu.RUnlock()
		return result, nil, nil
	}
	client.cacheMu.RUnlock()
	if client.InitializeResult().Capabilities.Resources == nil {
		return nil, nil, fmt.Errorf("%w: server does not advertise resources", ErrUnavailable)
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var all []ResourceDescriptor
	var diagnostics []Diagnostic
	cursor := ""
	seen := make(map[string]bool)
	for page := 0; page < DefaultMaxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Resources  []ResourceDescriptor `json:"resources"`
			NextCursor string               `json:"nextCursor,omitempty"`
		}
		if err := client.request(requestContext, "resources/list", params, &response); err != nil {
			return nil, nil, err
		}
		for _, resource := range response.Resources {
			if err := validateResource(resource); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "resource omitted: invalid descriptor"})
				continue
			}
			if seen[resource.URI] {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "duplicate resource omitted"})
				continue
			}
			seen[resource.URI] = true
			all = append(all, resource)
			if len(all) > DefaultMaxListItems {
				return nil, nil, fmt.Errorf("%w: resources list exceeds item limit", ErrProtocol)
			}
		}
		if response.NextCursor == "" {
			if err := client.validatePublicProjection(struct {
				Resources   []ResourceDescriptor `json:"resources"`
				Diagnostics []Diagnostic         `json:"diagnostics"`
			}{Resources: all, Diagnostics: diagnostics}); err != nil {
				return nil, nil, err
			}
			client.cacheResourcesIfCurrent(epoch, all)
			return append([]ResourceDescriptor(nil), all...), diagnostics, nil
		}
		if response.NextCursor == cursor || len(response.NextCursor) > 2_048 {
			return nil, nil, fmt.Errorf("%w: invalid resources pagination cursor", ErrProtocol)
		}
		cursor = response.NextCursor
	}
	return nil, nil, fmt.Errorf("%w: resources pagination exceeded page limit", ErrProtocol)
}

func (client *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, []Diagnostic, error) {
	client.cacheMu.RLock()
	epoch := client.resourcesEpoch
	if client.resourceTemplates != nil {
		result := append([]ResourceTemplate(nil), client.resourceTemplates...)
		client.cacheMu.RUnlock()
		return result, nil, nil
	}
	client.cacheMu.RUnlock()
	if client.InitializeResult().Capabilities.Resources == nil {
		return nil, nil, fmt.Errorf("%w: server does not advertise resources", ErrUnavailable)
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var all []ResourceTemplate
	var diagnostics []Diagnostic
	cursor := ""
	seen := make(map[string]bool)
	for page := 0; page < DefaultMaxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Templates  []ResourceTemplate `json:"resourceTemplates"`
			NextCursor string             `json:"nextCursor,omitempty"`
		}
		if err := client.request(requestContext, "resources/templates/list", params, &response); err != nil {
			return nil, nil, err
		}
		for _, template := range response.Templates {
			if strings.TrimSpace(template.URITemplate) == "" || strings.TrimSpace(template.Name) == "" || len(template.Description) > MaxDescriptionBytes {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "resource template omitted: invalid descriptor"})
				continue
			}
			if seen[template.URITemplate] {
				continue
			}
			seen[template.URITemplate] = true
			all = append(all, template)
			if len(all) > DefaultMaxListItems {
				return nil, nil, fmt.Errorf("%w: resource-template list exceeds item limit", ErrProtocol)
			}
		}
		if response.NextCursor == "" {
			if err := client.validatePublicProjection(struct {
				Templates   []ResourceTemplate `json:"templates"`
				Diagnostics []Diagnostic       `json:"diagnostics"`
			}{Templates: all, Diagnostics: diagnostics}); err != nil {
				return nil, nil, err
			}
			client.cacheResourceTemplatesIfCurrent(epoch, all)
			return append([]ResourceTemplate(nil), all...), diagnostics, nil
		}
		if response.NextCursor == cursor || len(response.NextCursor) > 2_048 {
			return nil, nil, fmt.Errorf("%w: invalid resource-template pagination cursor", ErrProtocol)
		}
		cursor = response.NextCursor
	}
	return nil, nil, fmt.Errorf("%w: resource-template pagination exceeded page limit", ErrProtocol)
}

func (client *Client) ListPrompts(ctx context.Context) ([]PromptDescriptor, []Diagnostic, error) {
	client.cacheMu.RLock()
	epoch := client.promptsEpoch
	if client.prompts != nil {
		result := clonePrompts(client.prompts)
		client.cacheMu.RUnlock()
		return result, nil, nil
	}
	client.cacheMu.RUnlock()
	if client.InitializeResult().Capabilities.Prompts == nil {
		return nil, nil, fmt.Errorf("%w: server does not advertise prompts", ErrUnavailable)
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var all []PromptDescriptor
	var diagnostics []Diagnostic
	cursor := ""
	seen := make(map[string]bool)
	for page := 0; page < DefaultMaxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Prompts    []PromptDescriptor `json:"prompts"`
			NextCursor string             `json:"nextCursor,omitempty"`
		}
		if err := client.request(requestContext, "prompts/list", params, &response); err != nil {
			return nil, nil, err
		}
		for _, prompt := range response.Prompts {
			if err := validatePrompt(prompt); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Server: client.config.Name, Message: "prompt omitted: invalid descriptor"})
				continue
			}
			if seen[prompt.Name] {
				continue
			}
			seen[prompt.Name] = true
			prompt.Arguments = append([]PromptArgument(nil), prompt.Arguments...)
			all = append(all, prompt)
			if len(all) > DefaultMaxListItems {
				return nil, nil, fmt.Errorf("%w: prompts list exceeds item limit", ErrProtocol)
			}
		}
		if response.NextCursor == "" {
			if err := client.validatePublicProjection(struct {
				Prompts     []PromptDescriptor `json:"prompts"`
				Diagnostics []Diagnostic       `json:"diagnostics"`
			}{Prompts: all, Diagnostics: diagnostics}); err != nil {
				return nil, nil, err
			}
			client.cachePromptsIfCurrent(epoch, all)
			return clonePrompts(all), diagnostics, nil
		}
		if response.NextCursor == cursor || len(response.NextCursor) > 2_048 {
			return nil, nil, fmt.Errorf("%w: invalid prompts pagination cursor", ErrProtocol)
		}
		cursor = response.NextCursor
	}
	return nil, nil, fmt.Errorf("%w: prompts pagination exceeded page limit", ErrProtocol)
}

func (client *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (ToolResult, error) {
	return client.CallToolBound(ctx, ToolCatalogVersion{}, name, arguments)
}

func (client *Client) CallToolBound(ctx context.Context, version ToolCatalogVersion, name string, arguments map[string]any) (ToolResult, error) {
	prepared, err := client.PrepareToolCall(ctx, name, arguments)
	if err != nil {
		return ToolResult{}, err
	}
	registered, err := prepared.Register(version)
	if err != nil {
		prepared.Cancel()
		return ToolResult{}, err
	}
	return registered.Await()
}

type clientToolCallPreparation struct {
	mu        sync.Mutex
	release   sync.Once
	client    *Client
	ctx       context.Context
	cancel    context.CancelFunc
	name      string
	arguments map[string]any
	state     uint8
}

type clientRegisteredToolCall struct {
	mu          sync.Mutex
	preparation *clientToolCallPreparation
	id          uint64
	generation  uint64
	pending     *pendingCall
	awaited     bool
}

func (client *Client) PrepareToolCall(ctx context.Context, name string, arguments map[string]any) (ToolCallPreparation, error) {
	if ctx == nil {
		return nil, errors.New("MCP tool call context is nil")
	}
	if !capabilityNamePattern.MatchString(name) {
		return nil, errors.New("invalid MCP tool name")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, errors.New("encode MCP tool arguments")
	}
	if len(encoded) > client.config.MaxMessageBytes/2 {
		return nil, errors.New("MCP tool arguments exceed size limit")
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := validateJSONDepth(value, 0); err != nil {
		return nil, err
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.ToolTimeout)
	if err := client.acquire(requestContext); err != nil {
		cancel()
		return nil, err
	}
	return &clientToolCallPreparation{
		client: client, ctx: requestContext, cancel: cancel,
		name: name, arguments: value,
	}, nil
}

func (preparation *clientToolCallPreparation) Register(version ToolCatalogVersion) (RegisteredToolCall, error) {
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if preparation.state != 0 {
		return nil, errors.New("MCP tool call preparation is no longer available")
	}
	client := preparation.client
	client.mu.Lock()
	if client.cmd == nil || client.closing || client.state == StateFailed || client.state == StateClosed || client.state == StateDisabled {
		client.mu.Unlock()
		return nil, ErrClosed
	}
	if version.ConnectionGeneration != 0 && client.generation != version.ConnectionGeneration {
		client.mu.Unlock()
		return nil, ErrStaleToolBinding
	}
	if version.ConnectionGeneration != 0 {
		client.cacheMu.RLock()
		if client.toolsEpoch != version.CatalogEpoch {
			client.cacheMu.RUnlock()
			client.mu.Unlock()
			return nil, ErrStaleToolBinding
		}
	}
	client.nextID++
	id := client.nextID
	generation := client.generation
	pending := &pendingCall{response: make(chan pendingResponse, 1), method: "tools/call", generation: generation}
	client.pending[id] = pending
	if version.ConnectionGeneration != 0 {
		client.cacheMu.RUnlock()
	}
	client.mu.Unlock()
	preparation.state = 1
	return &clientRegisteredToolCall{
		preparation: preparation, id: id, generation: generation, pending: pending,
	}, nil
}

func (preparation *clientToolCallPreparation) Cancel() {
	preparation.mu.Lock()
	if preparation.state == 0 {
		preparation.state = 2
	}
	preparation.mu.Unlock()
	preparation.finish()
}

func (preparation *clientToolCallPreparation) finish() {
	preparation.release.Do(func() {
		preparation.cancel()
		preparation.client.release()
	})
}

func (call *clientRegisteredToolCall) Await() (ToolResult, error) {
	call.mu.Lock()
	if call.awaited {
		call.mu.Unlock()
		return ToolResult{}, errors.New("registered MCP tool call was already awaited")
	}
	call.awaited = true
	call.mu.Unlock()
	defer call.preparation.finish()

	client := call.preparation.client
	request := wireRequest{
		JSONRPC: "2.0", ID: call.id, Method: "tools/call",
		Params: map[string]any{"name": call.preparation.name, "arguments": call.preparation.arguments},
	}
	if err := client.writeJSONGeneration(call.preparation.ctx, call.generation, request); err != nil {
		client.removePending(call.id, call.pending)
		return ToolResult{}, err
	}
	var response pendingResponse
	select {
	case response = <-call.pending.response:
	case <-call.preparation.ctx.Done():
		if client.removePending(call.id, call.pending) {
			notifyCtx, cancel := context.WithTimeout(context.Background(), min(client.config.RequestTimeout, 2*time.Second))
			_ = client.notifyGeneration(notifyCtx, call.generation, "notifications/cancelled", map[string]any{"requestId": call.id, "reason": "request context ended"})
			cancel()
			return ToolResult{}, call.preparation.ctx.Err()
		}
		response = <-call.pending.response
	}
	var result ToolResult
	if err := client.decodePendingResponse(response, &result); err != nil {
		return ToolResult{}, err
	}
	if err := validateToolResult(result); err != nil {
		return ToolResult{}, fmt.Errorf("%w: invalid tool result", ErrProtocol)
	}
	if err := client.validatePublicProjection(result); err != nil {
		return ToolResult{}, err
	}
	return cloneToolResult(result), nil
}

func (call *clientRegisteredToolCall) Cancel() {
	call.mu.Lock()
	if call.awaited {
		call.mu.Unlock()
		return
	}
	call.awaited = true
	call.mu.Unlock()
	call.preparation.client.removePending(call.id, call.pending)
	call.preparation.finish()
}

func (client *Client) ReadResource(ctx context.Context, uri string) (ResourceResult, error) {
	if err := validateURI(uri); err != nil {
		return ResourceResult{}, err
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var result ResourceResult
	if err := client.request(requestContext, "resources/read", map[string]string{"uri": uri}, &result); err != nil {
		return ResourceResult{}, err
	}
	if len(result.Contents) > DefaultMaxListItems {
		return ResourceResult{}, fmt.Errorf("%w: resource result exceeds item limit", ErrProtocol)
	}
	for _, content := range result.Contents {
		if content.URI == "" || content.Text == "" && content.Blob == "" || content.Text != "" && content.Blob != "" {
			return ResourceResult{}, fmt.Errorf("%w: malformed resource content", ErrProtocol)
		}
		if content.Blob != "" {
			if _, err := base64.StdEncoding.DecodeString(content.Blob); err != nil {
				return ResourceResult{}, fmt.Errorf("%w: invalid resource blob", ErrProtocol)
			}
		}
	}
	if err := client.validatePublicProjection(result); err != nil {
		return ResourceResult{}, err
	}
	return result, nil
}

func (client *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (PromptResult, error) {
	if !capabilityNamePattern.MatchString(name) {
		return PromptResult{}, errors.New("invalid MCP prompt name")
	}
	requestContext, cancel := client.withRequestTimeout(ctx, client.config.RequestTimeout)
	defer cancel()
	var result PromptResult
	if err := client.request(requestContext, "prompts/get", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return PromptResult{}, err
	}
	if len(result.Description) > MaxDescriptionBytes || len(result.Messages) > DefaultMaxListItems {
		return PromptResult{}, fmt.Errorf("%w: prompt result exceeds limits", ErrProtocol)
	}
	for _, message := range result.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return PromptResult{}, fmt.Errorf("%w: invalid prompt message role", ErrProtocol)
		}
		if err := validateContentBlock(message.Content); err != nil {
			return PromptResult{}, fmt.Errorf("%w: invalid prompt content", ErrProtocol)
		}
	}
	if err := client.validatePublicProjection(result); err != nil {
		return PromptResult{}, err
	}
	return result, nil
}

func (client *Client) withRequestTimeout(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		duration = DefaultRequestTimeout
	}
	return context.WithTimeout(parent, duration)
}

func validateResource(resource ResourceDescriptor) error {
	if strings.TrimSpace(resource.Name) == "" {
		return errors.New("name is required")
	}
	if len(resource.Description) > MaxDescriptionBytes {
		return errors.New("description exceeds size limit")
	}
	return validateURI(resource.URI)
}

func validateURI(value string) error {
	if len(value) == 0 || len(value) > 8_192 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("invalid resource URI")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return errors.New("resource URI must be absolute")
	}
	return nil
}

func validatePrompt(prompt PromptDescriptor) error {
	if !capabilityNamePattern.MatchString(prompt.Name) {
		return errors.New("invalid prompt name")
	}
	if len(prompt.Description) > MaxDescriptionBytes || len(prompt.Arguments) > 128 {
		return errors.New("prompt descriptor exceeds limits")
	}
	seen := make(map[string]bool)
	for _, argument := range prompt.Arguments {
		if !capabilityNamePattern.MatchString(argument.Name) || seen[argument.Name] || len(argument.Description) > MaxDescriptionBytes {
			return errors.New("invalid prompt argument")
		}
		seen[argument.Name] = true
	}
	return nil
}

func validateToolResult(result ToolResult) error {
	if len(result.Content) > DefaultMaxListItems {
		return errors.New("too many tool content blocks")
	}
	for _, block := range result.Content {
		if err := validateContentBlock(block); err != nil {
			return err
		}
	}
	if len(result.StructuredContent) > 0 {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(result.StructuredContent))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if err := validateJSONDepth(value, 0); err != nil {
			return err
		}
	}
	return nil
}

func validateContentBlock(block ContentBlock) error {
	switch block.Type {
	case "text":
		if block.Text == "" {
			return errors.New("text content is empty")
		}
	case "image", "audio":
		if block.Data == "" || block.MIMEType == "" {
			return errors.New("binary content requires data and MIME type")
		}
		if _, err := base64.StdEncoding.DecodeString(block.Data); err != nil {
			return errors.New("binary content is not valid base64")
		}
	case "resource_link":
		if err := validateURI(block.URI); err != nil {
			return err
		}
	case "resource":
		if len(block.Resource) == 0 {
			return errors.New("embedded resource is missing")
		}
		var value any
		if err := json.Unmarshal(block.Resource, &value); err != nil {
			return errors.New("embedded resource is malformed")
		}
		if err := validateJSONDepth(value, 0); err != nil {
			return err
		}
	default:
		return errors.New("unsupported content block type")
	}
	return nil
}

func cloneTools(source []ToolDescriptor) []ToolDescriptor {
	result := make([]ToolDescriptor, len(source))
	for index, tool := range source {
		tool.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		if tool.Annotations != nil {
			tool.Annotations = cloneAnyMap(tool.Annotations)
		}
		result[index] = tool
	}
	return result
}

func clonePrompts(source []PromptDescriptor) []PromptDescriptor {
	result := make([]PromptDescriptor, len(source))
	for index, prompt := range source {
		prompt.Arguments = append([]PromptArgument(nil), prompt.Arguments...)
		result[index] = prompt
	}
	return result
}

func cloneToolResult(source ToolResult) ToolResult {
	result := source
	result.Content = make([]ContentBlock, len(source.Content))
	for index, block := range source.Content {
		block.Resource = append(json.RawMessage(nil), block.Resource...)
		result.Content[index] = block
	}
	result.StructuredContent = append(json.RawMessage(nil), source.StructuredContent...)
	return result
}

// Discovery responses are cached only if no list-changed notification (or
// connection teardown) occurred after the request began. The caller may still
// use a response that raced invalidation, but a later caller must re-discover.
func (client *Client) cacheToolsIfCurrent(epoch uint64, tools []ToolDescriptor) bool {
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if client.toolsEpoch != epoch {
		return false
	}
	client.tools = cloneTools(tools)
	return true
}

func (client *Client) cacheResourcesIfCurrent(epoch uint64, resources []ResourceDescriptor) bool {
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if client.resourcesEpoch != epoch {
		return false
	}
	client.resources = append([]ResourceDescriptor(nil), resources...)
	return true
}

func (client *Client) cacheResourceTemplatesIfCurrent(epoch uint64, templates []ResourceTemplate) bool {
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if client.resourcesEpoch != epoch {
		return false
	}
	client.resourceTemplates = append([]ResourceTemplate(nil), templates...)
	return true
}

func (client *Client) cachePromptsIfCurrent(epoch uint64, prompts []PromptDescriptor) bool {
	client.cacheMu.Lock()
	defer client.cacheMu.Unlock()
	if client.promptsEpoch != epoch {
		return false
	}
	client.prompts = clonePrompts(prompts)
	return true
}
