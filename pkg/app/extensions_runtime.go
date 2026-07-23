package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/childenv"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/mcp"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/tool"
)

const maximumExtensionConfigBytes = 4 << 20

const (
	maximumOutputStyleDiagnostics   = 8
	maximumExtensionDiagnosticBytes = 1024
)

type runtimeExtensions struct {
	skills               extensions.Snapshot
	plugins              extensions.PluginSnapshot
	styles               extensions.OutputStyleSnapshot
	selection            extensions.OutputStyleSelection
	hooks                extensions.HookSnapshot
	runner               *extensions.HookRunner
	mcp                  *mcp.Manager
	mcpState             mcp.Snapshot
	mcpConfigs           []mcp.Config
	mcpCredentialConfigs []mcp.Config
	mcpDiagnostics       []mcp.Diagnostic
	protectedPaths       []string
}

func discoverExtensions(ctx context.Context, workspace string, opts cli.Options, environment []string) (runtimeExtensions, []tool.Descriptor, error) {
	userRoot := ""
	if directory, err := os.UserConfigDir(); err == nil {
		userRoot = filepath.Join(directory, "agentx")
	}
	return discoverExtensionsFromUserRoot(ctx, workspace, opts, environment, userRoot)
}

// discoverExtensionsFromUserRoot keeps host-directory discovery outside the
// compositional registry logic. Tests use an isolated root to prove bare mode
// cannot observe or execute ordinary user filesystem extensions. Skills are
// deliberately excluded from the user root: AgentX only discovers skills from
// the active repository's .codex/skills directory.
func discoverExtensionsFromUserRoot(ctx context.Context, workspace string, opts cli.Options, environment []string, userRoot string) (runtimeExtensions, []tool.Descriptor, error) {
	result := runtimeExtensions{runner: extensions.NewHookRunner(), mcp: mcp.NewManager(nil)}
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			_ = result.mcp.Close()
		}
	}()

	pluginRoots := make([]extensions.PluginRoot, 0, 2)
	if !opts.Bare && userRoot != "" && directoryExists(filepath.Join(userRoot, "plugins")) {
		pluginRoots = append(pluginRoots, extensions.PluginRoot{Path: filepath.Join(userRoot, "plugins"), Source: extensions.SourceUser, Marketplace: "user", Scope: "user", Enabled: true, Trusted: true})
	}
	if opts.TrustWorkspace && !opts.Bare && directoryExists(filepath.Join(workspace, ".agentx", "plugins")) {
		pluginRoots = append(pluginRoots, extensions.PluginRoot{Path: filepath.Join(workspace, ".agentx", "plugins"), Source: extensions.SourceProject, Marketplace: "project", Scope: "project", Enabled: true, Trusted: true})
	}
	result.plugins = extensions.NewPluginManager().Reload(pluginRoots, extensions.PluginPolicy{})
	resolvedPlugins := usablePlugins(&result.plugins)

	skillRoots := make([]extensions.Root, 0, 1)
	if opts.TrustWorkspace && !opts.Bare && directoryExists(filepath.Join(workspace, ".codex", "skills")) {
		skillRoots = append(skillRoots, extensions.Root{Path: filepath.Join(workspace, ".codex", "skills"), Source: extensions.SourceProject, Owner: "project"})
	}
	styleRoots := make([]extensions.OutputStyleRoot, 0, 4)
	if !opts.Bare && userRoot != "" && directoryExists(filepath.Join(userRoot, "output-styles")) {
		styleRoots = append(styleRoots, extensions.OutputStyleRoot{Path: filepath.Join(userRoot, "output-styles"), Source: extensions.SourceUser})
	}
	if opts.TrustWorkspace && !opts.Bare && directoryExists(filepath.Join(workspace, ".agentx", "output-styles")) {
		styleRoots = append(styleRoots, extensions.OutputStyleRoot{Path: filepath.Join(workspace, ".agentx", "output-styles"), Source: extensions.SourceProject})
	}
	var hookFiles, mcpFiles []componentFile
	for _, plugin := range resolvedPlugins {
		for _, path := range plugin.Components["output-styles"] {
			styleRoots = append(styleRoots, extensions.OutputStyleRoot{Path: path, Source: extensions.SourcePlugin, PluginName: plugin.CanonicalID})
		}
		for _, path := range plugin.Components["hooks"] {
			hookFiles = append(hookFiles, componentFile{path: path, source: extensions.SourcePlugin, identity: plugin.CanonicalID, root: plugin.Root})
		}
		for _, path := range plugin.Components["mcp"] {
			mcpFiles = append(mcpFiles, componentFile{path: path, source: extensions.SourcePlugin, identity: plugin.CanonicalID, root: plugin.Root})
		}
	}
	result.skills = extensions.NewManager().Reload(skillRoots)
	result.styles = extensions.NewOutputStyleManager().Reload(styleRoots)
	result.selection = extensions.SelectOutputStyle(result.styles, opts.OutputStyle)

	hookDescriptors, hookDiagnostics := loadHookComponents(hookFiles)
	result.hooks = extensions.NewHookManagerForEvents(runtimeHookEvents()...).Reload(hookDescriptors)
	result.hooks.Diagnostics = append(result.hooks.Diagnostics, hookDiagnostics...)
	result.runner.ProjectRoot = workspace
	result.runner.Environment = nonSecretEnvironment(environment)

	mcpConfigs := make([]mcp.Config, 0)
	if !opts.Bare && userRoot != "" {
		path := filepath.Join(userRoot, "mcp.json")
		if regularFileExists(path) {
			result.protectedPaths = append(result.protectedPaths, path)
			configs, err := loadMCPDocument(path, mcp.ScopeUser, true, "user:mcp", workspace)
			if err != nil {
				return runtimeExtensions{}, nil, err
			}
			mcpConfigs = append(mcpConfigs, configs...)
		}
	}
	if opts.TrustWorkspace && !opts.Bare {
		path := opts.MCPConfig
		if path == "" {
			path = filepath.Join(workspace, ".agentx", "mcp.json")
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		if regularFileExists(path) {
			result.protectedPaths = append(result.protectedPaths, path)
			configs, err := loadMCPDocument(path, mcp.ScopeProject, true, "project:mcp", workspace)
			if err != nil {
				return runtimeExtensions{}, nil, err
			}
			mcpConfigs = append(mcpConfigs, configs...)
		} else if opts.MCPConfig != "" {
			return runtimeExtensions{}, nil, fmt.Errorf("MCP configuration %q is unavailable", path)
		}
	}
	for _, file := range mcpFiles {
		result.protectedPaths = append(result.protectedPaths, file.path)
		configs, err := loadMCPDocument(file.path, mcp.ScopePlugin, true, "plugin:"+file.identity, file.root)
		if err != nil {
			return runtimeExtensions{}, nil, err
		}
		mcpConfigs = append(mcpConfigs, configs...)
	}
	mcpConfigs = expandMCPConfigurations(mcpConfigs, environment)
	result.mcpState = result.mcp.Reconcile(ctx, mcpConfigs)
	result.mcpConfigs = append([]mcp.Config(nil), mcpConfigs...)
	// Provider-scoped descriptor sanitizers use the exact connected winner.
	// The session-wide union below remains broader because status, diagnostics,
	// initialization, and cleanup project definitions even when they publish no
	// model-callable tools.
	credentialCandidates := connectedMCPConfigs(mcpConfigs, result.mcpState)
	mcpTools, toolDiagnostics, err := result.mcp.Tools(ctx)
	result.mcpDiagnostics = append(result.mcpDiagnostics, toolDiagnostics...)
	if err != nil && !errors.Is(err, context.Canceled) {
		// A failed provider is isolated in the manager snapshot; healthy sibling
		// descriptors remain usable.
		err = nil
	}
	if err != nil {
		return runtimeExtensions{}, nil, err
	}
	descriptors := make([]tool.Descriptor, 0, len(mcpTools))
	for _, descriptor := range mcpTools {
		server := strings.SplitN(strings.TrimPrefix(descriptor.Name, "mcp__"), "__", 2)[0]
		sanitize, sanitizeErr := mcpResultSanitizer(credentialCandidates, server)
		if sanitizeErr != nil {
			result.mcpDiagnostics = append(result.mcpDiagnostics, mcp.Diagnostic{
				Server: server, Source: "tools/list",
				Message: "tool descriptor omitted because provider credential redaction exceeds its workload limit",
			})
			continue
		}
		if mcpDescriptorExposesCredential(descriptor, sanitize) {
			result.mcpDiagnostics = append(result.mcpDiagnostics, mcp.Diagnostic{
				Server: server, Source: "tools/list",
				Message: "tool descriptor omitted because it reflected configured provider credentials",
			})
			continue
		}
		adapted, adaptErr := adaptMCPTool(result.mcp, descriptor, sanitize)
		if adaptErr != nil {
			result.mcpDiagnostics = append(result.mcpDiagnostics, mcp.Diagnostic{
				Server: server, Source: "tools/list", Message: "tool descriptor omitted because it could not be adapted safely",
			})
			continue
		}
		descriptors = append(descriptors, adapted)
	}
	// Freeze every expanded configured definition into the shared runtime
	// credential union. Disabled, failed, rejected, and zero-tool providers can
	// still affect SDK/status/command/cleanup projections or be reflected by a
	// healthy sibling, so tool publication is not a safe narrowing boundary.
	result.mcpCredentialConfigs = append([]mcp.Config(nil), mcpConfigs...)
	result.mcpState = result.mcp.Snapshot()
	result.mcpState.Diagnostics = append(result.mcpState.Diagnostics, result.mcpDiagnostics...)
	closeOnFailure = false
	return result, descriptors, nil
}

type mcpConfigIdentity struct {
	scope       mcp.Scope
	name        string
	sourceID    string
	fingerprint string
}

func connectedMCPConfigs(configs []mcp.Config, snapshot mcp.Snapshot) []mcp.Config {
	connected := make(map[mcpConfigIdentity]struct{})
	for _, server := range snapshot.Servers {
		if server.State == mcp.StateConnected {
			connected[mcpConfigIdentity{
				scope: server.Scope, name: server.Name, sourceID: server.SourceID,
				fingerprint: server.Fingerprint,
			}] = struct{}{}
		}
	}
	result := make([]mcp.Config, 0, len(connected))
	for _, config := range configs {
		descriptor, err := mcp.ValidateConfig(config)
		if err != nil {
			continue
		}
		identity := mcpConfigIdentity{
			scope: descriptor.Scope, name: descriptor.Name, sourceID: descriptor.SourceID,
			fingerprint: descriptor.Fingerprint,
		}
		if _, ok := connected[identity]; ok {
			result = append(result, config)
		}
	}
	return result
}

func expandMCPConfigurations(configs []mcp.Config, environment []string) []mcp.Config {
	lookup := childenv.MCPExpansionLookup(environment)
	result := make([]mcp.Config, len(configs))
	for index, config := range configs {
		expanded, err := mcp.ExpandConfigEnvironment(config, lookup)
		if err != nil {
			config.ConfigurationError = "MCP environment expansion: " + err.Error()
			result[index] = config
			continue
		}
		result[index] = expanded
	}
	return result
}

type componentFile struct {
	path, identity, root string
	source               extensions.Source
}

func usablePlugins(snapshot *extensions.PluginSnapshot) []extensions.PluginDescriptor {
	const (
		_ uint8 = iota
		pluginVisiting
		pluginResolved
		pluginFailed
	)
	indices := make(map[string]int, len(snapshot.Plugins))
	for index, plugin := range snapshot.Plugins {
		indices[plugin.CanonicalID] = index
	}
	states := make(map[string]uint8, len(snapshot.Plugins))
	failures := make(map[string]error)
	stack := make([]string, 0, len(snapshot.Plugins))
	resolvedPlugins := make([]extensions.PluginDescriptor, 0, len(snapshot.Plugins))
	var visit func(string) error
	visit = func(id string) error {
		switch states[id] {
		case pluginResolved:
			return nil
		case pluginFailed:
			return failures[id]
		case pluginVisiting:
			start := 0
			for index := range stack {
				if stack[index] == id {
					start = index
					break
				}
			}
			cycle := append(append([]string(nil), stack[start:]...), id)
			return fmt.Errorf("plugin dependency cycle: %s", strings.Join(cycle, " -> "))
		}
		index, ok := indices[id]
		if !ok {
			err := fmt.Errorf("plugin dependency %s not found", id)
			states[id], failures[id] = pluginFailed, err
			return err
		}
		plugin := snapshot.Plugins[index]
		if !plugin.Availability.Usable() {
			err := fmt.Errorf("plugin dependency %s is unavailable: %s", id, strings.Join(plugin.Availability.Reasons, "; "))
			states[id], failures[id] = pluginFailed, err
			return err
		}
		states[id] = pluginVisiting
		stack = append(stack, id)
		for _, dependency := range plugin.Dependencies {
			if err := visit(dependency); err != nil {
				stack = stack[:len(stack)-1]
				states[id], failures[id] = pluginFailed, err
				return err
			}
		}
		stack = stack[:len(stack)-1]
		states[id] = pluginResolved
		resolvedPlugins = append(resolvedPlugins, plugin)
		return nil
	}
	for _, plugin := range snapshot.Plugins {
		if plugin.Availability.Usable() {
			_ = visit(plugin.CanonicalID)
		}
	}
	for index := range snapshot.Plugins {
		plugin := snapshot.Plugins[index]
		if !plugin.Availability.Usable() || states[plugin.CanonicalID] != pluginFailed {
			continue
		}
		snapshot.Plugins[index].Availability.ProviderAvailable = false
		snapshot.Plugins[index].Availability.Reasons = append(
			snapshot.Plugins[index].Availability.Reasons,
			"dependency resolution failed",
		)
		snapshot.Diagnostics = append(snapshot.Diagnostics, extensions.Diagnostic{
			Path:    plugin.Root,
			Message: boundedExtensionDiagnostic(fmt.Sprintf("plugin %s is unavailable: %v", plugin.CanonicalID, failures[plugin.CanonicalID]), maximumExtensionDiagnosticBytes),
		})
	}
	return resolvedPlugins
}

func directoryExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func readBoundedRegular(path string) ([]byte, error) {
	return extensions.ReadRegularFile(path, maximumExtensionConfigBytes)
}

// loadHookComponents accepts the compact plugin hook document used by this Go
// port: {"hooks":[HookDescriptor,...]}. Executable fields stay attributed to
// the trusted plugin and are never serialized by HookDescriptor.MarshalJSON.
// A malformed component is diagnosed and omitted without suppressing healthy
// components from the same or another plugin.
func loadHookComponents(files []componentFile) ([]extensions.HookDescriptor, []extensions.Diagnostic) {
	var result []extensions.HookDescriptor
	var diagnostics []extensions.Diagnostic
	for _, file := range files {
		data, err := readBoundedRegular(file.path)
		if err != nil {
			diagnostics = append(diagnostics, extensions.Diagnostic{
				Path:    file.path,
				Message: boundedExtensionDiagnostic("read plugin hooks: "+err.Error(), maximumExtensionDiagnosticBytes),
			})
			continue
		}
		var document struct {
			Hooks []extensions.HookDescriptor `json:"hooks"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			diagnostics = append(diagnostics, extensions.Diagnostic{Path: file.path, Message: "invalid plugin hook document"})
			continue
		}
		for index := range document.Hooks {
			document.Hooks[index].Source = file.source
			document.Hooks[index].SourceIdentity = file.identity
			document.Hooks[index].PluginRoot = file.root
		}
		result = append(result, document.Hooks...)
	}
	return result, diagnostics
}

func writeOutputStyleSelectionDiagnostics(writer io.Writer, selection extensions.OutputStyleSelection, credentials *redact.Set) error {
	if writer == nil || len(selection.Diagnostics) == 0 {
		return nil
	}
	limit := len(selection.Diagnostics)
	if limit > maximumOutputStyleDiagnostics {
		limit = maximumOutputStyleDiagnostics
	}
	for _, diagnostic := range selection.Diagnostics[:limit] {
		message := boundedExtensionDiagnostic(diagnostic.Message, maximumExtensionDiagnosticBytes)
		if err := writeTerminalRecord(writer, credentials, fmt.Sprintf("warning: output style: %s\n", message)); err != nil {
			return fmt.Errorf("write output-style diagnostic: %w", err)
		}
	}
	if len(selection.Diagnostics) > limit {
		record := fmt.Sprintf("warning: output style: %d additional diagnostics omitted\n", len(selection.Diagnostics)-limit)
		if err := writeTerminalRecord(writer, credentials, record); err != nil {
			return fmt.Errorf("write output-style diagnostic summary: %w", err)
		}
	}
	return nil
}

func boundedExtensionDiagnostic(message string, maximum int) string {
	message = TerminalSafeText(strings.TrimSpace(message))
	message = strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(message)
	if maximum <= 0 || len(message) <= maximum {
		return message
	}
	const suffix = "..."
	remaining := maximum - len(suffix)
	if remaining <= 0 {
		return suffix[:maximum]
	}
	prefixBytes := remaining * 2 / 3
	for prefixBytes > 0 && !utf8.ValidString(message[:prefixBytes]) {
		prefixBytes--
	}
	suffixStart := len(message) - (remaining - prefixBytes)
	for suffixStart < len(message) && !utf8.ValidString(message[suffixStart:]) {
		suffixStart++
	}
	return message[:prefixBytes] + suffix + message[suffixStart:]
}

type mcpServerJSON struct {
	Type            mcp.Transport     `json:"type,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	URL             string            `json:"url,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	OAuth           *mcp.OAuthConfig  `json:"oauth,omitempty"`
	Disabled        bool              `json:"disabled,omitempty"`
	ConnectTimeout  time.Duration     `json:"connect_timeout,omitempty"`
	RequestTimeout  time.Duration     `json:"request_timeout,omitempty"`
	ToolTimeout     time.Duration     `json:"tool_timeout,omitempty"`
	MaxMessageBytes int               `json:"max_message_bytes,omitempty"`
}

func loadMCPDocument(path string, scope mcp.Scope, trusted bool, identity, workingDirectory string) ([]mcp.Config, error) {
	data, err := readBoundedRegular(path)
	if err != nil {
		return nil, fmt.Errorf("read MCP config %q: %w", path, err)
	}
	var document struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("decode MCP config %q: invalid document", path)
	}
	names := make([]string, 0, len(document.Servers))
	for name := range document.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	configs := make([]mcp.Config, 0, len(names))
	for _, name := range names {
		var value mcpServerJSON
		serverDecoder := json.NewDecoder(bytes.NewReader(document.Servers[name]))
		serverDecoder.DisallowUnknownFields()
		if err := serverDecoder.Decode(&value); err != nil || serverDecoder.Decode(&struct{}{}) != io.EOF {
			configs = append(configs, mcp.Config{
				Name: name, Scope: scope, SourceID: identity + ":" + filepath.Clean(path),
				WorkingDirectory: workingDirectory, Trusted: trusted, Approved: trusted,
				ConfigurationError: "MCP server descriptor is invalid",
			})
			continue
		}
		config := mcp.Config{
			Name: name, Transport: value.Type, Command: value.Command, Args: value.Args, Env: value.Env, WorkingDirectory: workingDirectory,
			URL: value.URL, Headers: value.Headers, OAuth: value.OAuth, Scope: scope,
			SourceID: identity + ":" + filepath.Clean(path), Trusted: trusted, Approved: trusted,
			Disabled: value.Disabled, ConnectTimeout: value.ConnectTimeout, RequestTimeout: value.RequestTimeout,
			ToolTimeout: value.ToolTimeout, MaxMessageBytes: value.MaxMessageBytes,
		}
		configs = append(configs, config)
	}
	return configs, nil
}

func adaptMCPTool(manager *mcp.Manager, descriptor mcp.ToolDescriptor, sanitize *redact.Set) (tool.Descriptor, error) {
	if !strings.HasPrefix(descriptor.Name, "mcp__") {
		return tool.Descriptor{}, errors.New("MCP tool is not namespaced")
	}
	parts := strings.SplitN(strings.TrimPrefix(descriptor.Name, "mcp__"), "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return tool.Descriptor{}, errors.New("invalid namespaced MCP tool")
	}
	server, remoteName := parts[0], parts[1]
	binding, bound := descriptor.Binding()
	if !bound {
		return tool.Descriptor{}, errors.New("MCP tool descriptor has no catalog binding")
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(descriptor.InputSchema))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil || schema == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return tool.Descriptor{}, errors.New("invalid MCP input schema")
	}
	return tool.Descriptor{
		Name: descriptor.Name, Description: descriptor.Description, InputSchema: schema, Source: tool.SourceMCP,
		Validate: func(raw json.RawMessage) (any, error) {
			if len(raw) > mcp.DefaultMaxMessageBytes/2 {
				return nil, errors.New("MCP input exceeds size limit")
			}
			var object map[string]any
			inputDecoder := json.NewDecoder(bytes.NewReader(raw))
			inputDecoder.UseNumber()
			if err := inputDecoder.Decode(&object); err != nil || object == nil || inputDecoder.Decode(&struct{}{}) != io.EOF {
				return nil, errors.New("MCP input must be one JSON object")
			}
			if err := mcp.ValidateToolInput(schema, object); err != nil {
				return nil, fmt.Errorf("MCP input does not satisfy the advertised schema: %w", err)
			}
			return object, nil
		},
		Classify: func(any) permission.Classification { return permission.Classification{OpenWorld: true} },
		ProjectPermission: func(_ any, raw json.RawMessage) (permission.Request, error) {
			return permission.Request{Input: raw}, nil
		},
		Call: func(ctx context.Context, _ tool.CallContext, value any) (tool.Output, error) {
			result, err := manager.CallBoundTool(ctx, binding, remoteName, value.(map[string]any))
			if err != nil {
				if errors.Is(err, mcp.ErrStaleToolBinding) {
					return tool.Output{}, errors.New("MCP tool descriptor is stale; start a new session")
				}
				return tool.Output{}, errors.New("MCP tool call failed")
			}
			text, blockMetadata, normalizeErr := normalizeMCPResult(result, sanitize)
			if normalizeErr != nil {
				return tool.Output{}, errors.New("MCP tool result could not be safely normalized")
			}
			if result.IsError {
				return tool.Output{}, errors.New(text)
			}
			return tool.Output{Content: text, Metadata: map[string]any{"mcp_server": server, "mcp_content_blocks": blockMetadata}}, nil
		},
		CredentialSanitizer: sanitize,
		MaxResultChars:      50_000,
	}, nil
}

func normalizeMCPResult(result mcp.ToolResult, sanitize *redact.Set) (string, []map[string]any, error) {
	return normalizeMCPResultBounded(result, sanitize, mcp.DefaultMaxMessageBytes)
}

type mcpNormalizationBudget struct {
	remaining int
}

func (b *mcpNormalizationBudget) take(size int) error {
	if size < 0 || size > b.remaining {
		return errors.New("sanitized MCP result exceeds message limit")
	}
	b.remaining -= size
	return nil
}

func normalizeMCPResultBounded(result mcp.ToolResult, sanitize *redact.Set, limit int) (string, []map[string]any, error) {
	if sanitize == nil {
		sanitize = redact.New()
	}
	if limit <= 0 {
		return "", nil, errors.New("MCP result normalization requires a positive limit")
	}
	budget := &mcpNormalizationBudget{remaining: limit}
	safeText := func(value string) (string, error) {
		safe, truncated, suppressed := sanitize.RedactBounded(value, budget.remaining)
		if truncated || suppressed {
			return "", errors.New("MCP result string could not be safely sanitized within message limit")
		}
		return safe, nil
	}
	storeMetadataString := func(descriptor map[string]any, key, value string) error {
		if err := budget.take(len(value)); err != nil {
			return err
		}
		descriptor[key] = value
		return nil
	}
	var output strings.Builder
	partCount := 0
	appendPart := func(pieces ...string) error {
		size := 0
		if partCount > 0 {
			size++
		}
		for _, piece := range pieces {
			if len(piece) > int(^uint(0)>>1)-size {
				return errors.New("sanitized MCP result exceeds message limit")
			}
			size += len(piece)
		}
		if err := budget.take(size); err != nil {
			return err
		}
		if partCount > 0 {
			output.WriteByte('\n')
		}
		for _, piece := range pieces {
			output.WriteString(piece)
		}
		partCount++
		return nil
	}
	var metadata []map[string]any
	for _, block := range result.Content {
		safeType, err := safeText(block.Type)
		if err != nil {
			return "", nil, err
		}
		// Account for the map/container itself so a message packed with empty
		// blocks cannot allocate an unbounded metadata slice.
		if err := budget.take(32); err != nil {
			return "", nil, err
		}
		descriptor := make(map[string]any, 4)
		if err := storeMetadataString(descriptor, "type", safeType); err != nil {
			return "", nil, err
		}
		switch block.Type {
		case "text":
			text, err := safeText(block.Text)
			if err != nil {
				return "", nil, err
			}
			if err := appendPart(text); err != nil {
				return "", nil, err
			}
		case "image", "audio":
			mimeType, err := safeText(block.MIMEType)
			if err != nil {
				return "", nil, err
			}
			if err := storeMetadataString(descriptor, "mime_type", mimeType); err != nil {
				return "", nil, err
			}
			descriptor["encoded_bytes"] = len(block.Data)
			framing := fmt.Sprintf("[%s content: %s, %d base64 characters; binary attachment delivery is unavailable on this text tool-result surface]", safeType, mimeType, len(block.Data))
			if err := appendPart(framing); err != nil {
				return "", nil, err
			}
		case "resource_link":
			uri, err := safeText(block.URI)
			if err != nil {
				return "", nil, err
			}
			name, err := safeText(block.Name)
			if err != nil {
				return "", nil, err
			}
			mimeType, err := safeText(block.MIMEType)
			if err != nil {
				return "", nil, err
			}
			if err := storeMetadataString(descriptor, "uri", uri); err != nil {
				return "", nil, err
			}
			if err := storeMetadataString(descriptor, "name", name); err != nil {
				return "", nil, err
			}
			if err := storeMetadataString(descriptor, "mime_type", mimeType); err != nil {
				return "", nil, err
			}
			label := strings.TrimSpace(name)
			if label == "" {
				label = "resource"
			}
			if err := appendPart("[", label, " link: ", uri, "]"); err != nil {
				return "", nil, err
			}
		case "resource":
			resource, err := sanitize.JSONBounded(block.Resource, budget.remaining)
			if err != nil {
				return "", nil, err
			}
			descriptor["encoded_bytes"] = len(block.Resource)
			if err := appendPart("[embedded MCP resource]\n", string(resource)); err != nil {
				return "", nil, err
			}
		default:
			if err := appendPart("[unsupported MCP content block: ", safeType, "]"); err != nil {
				return "", nil, err
			}
		}
		metadata = append(metadata, descriptor)
	}
	if len(result.StructuredContent) > 0 {
		structured, err := sanitize.JSONBounded(result.StructuredContent, budget.remaining)
		if err != nil {
			return "", nil, err
		}
		if err := appendPart(string(structured)); err != nil {
			return "", nil, err
		}
	}
	if partCount == 0 {
		if result.IsError {
			if err := appendPart("MCP tool returned an error without content."); err != nil {
				return "", nil, err
			}
		} else {
			if err := appendPart("(MCP tool completed with no content)"); err != nil {
				return "", nil, err
			}
		}
	}
	joined, truncated, suppressed := sanitize.RedactBounded(output.String(), limit)
	if truncated || suppressed {
		return "", nil, errors.New("MCP result could not be safely sanitized")
	}
	return joined, metadata, nil
}

func nonSecretEnvironment(environment []string) map[string]string {
	return childenv.NonSecretMap(environment)
}

// mcpResultSanitizer protects credential-classified environment/header values
// owned by one MCP server. Provider scoping bounds collateral rewriting to that
// provider's descriptors and results.
func mcpResultSanitizer(configs []mcp.Config, server string) (*redact.Set, error) {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	totalBytes := 0
	add := func(value string) {
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
		totalBytes += len(value)
	}
	for _, config := range configs {
		if !strings.EqualFold(strings.TrimSpace(config.Name), server) {
			continue
		}
		configured, err := mcp.CredentialLiterals(config)
		if err != nil {
			return nil, err
		}
		for _, value := range configured {
			if _, exists := seen[value]; !exists &&
				(len(values) >= mcp.MaxCredentialLiterals || len(value) > mcp.MaxCredentialLiteralBytes-totalBytes) {
				return nil, errors.New("MCP credential material exceeds redaction workload limit")
			}
			add(value)
		}
	}
	result := redact.New(values...)
	if !result.Empty() && result.TerminalMarker() == "" {
		return nil, errors.New("MCP credential material has no safe streaming projection")
	}
	return result, nil
}

// runtimeCredentialSanitizer builds the bounded complete union for sinks that
// combine contributions from multiple providers, such as the model request,
// transcript, task journal, and SDK stream. Provider-scoped sets remain useful
// for attribution, but they cannot protect a shared serialization boundary.
func runtimeCredentialSanitizer(apiKey string, configs []mcp.Config) (*redact.Set, error) {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	totalBytes := 0
	add := func(value string) error {
		if value == "" {
			return nil
		}
		if _, exists := seen[value]; exists {
			return nil
		}
		if len(values) >= mcp.MaxCredentialLiterals ||
			len(value) > mcp.MaxCredentialLiteralBytes-totalBytes {
			return errors.New("session credential material exceeds redaction workload limit")
		}
		seen[value] = struct{}{}
		values = append(values, value)
		totalBytes += len(value)
		return nil
	}
	if err := add(apiKey); err != nil {
		return nil, err
	}
	for _, config := range configs {
		configured, err := mcp.CredentialLiterals(config)
		if err != nil {
			return nil, err
		}
		for _, value := range configured {
			if err := add(value); err != nil {
				return nil, err
			}
		}
	}
	result := redact.New(values...)
	if !result.Empty() && result.TerminalMarker() == "" {
		return nil, errors.New("session credential material has no safe streaming projection")
	}
	return result, nil
}

func mcpDescriptorExposesConfiguredCredential(descriptor mcp.ToolDescriptor, configs []mcp.Config, server string) bool {
	sanitize, err := mcpResultSanitizer(configs, server)
	if err != nil {
		return true
	}
	return mcpDescriptorExposesCredential(descriptor, sanitize)
}

func mcpDescriptorExposesCredential(descriptor mcp.ToolDescriptor, sanitize *redact.Set) bool {
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return true
	}
	reflected, inspectionErr := sanitize.JSONContains(encoded)
	if inspectionErr != nil || reflected {
		return true
	}
	fields := []string{descriptor.Name, descriptor.Description}
	for _, field := range fields {
		if sanitize.Contains(field) {
			return true
		}
	}
	if jsonDocumentContainsLiteral(descriptor.InputSchema, sanitize) {
		return true
	}
	return jsonValueContainsLiteral(descriptor.Annotations, sanitize)
}

func jsonDocumentContainsLiteral(document json.RawMessage, literals *redact.Set) bool {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return true
	}
	return jsonValueContainsLiteral(value, literals)
}

func jsonValueContainsLiteral(value any, literals *redact.Set) bool {
	switch typed := value.(type) {
	case string:
		return literals.Contains(typed)
	case map[string]any:
		for key, child := range typed {
			if literals.Contains(key) || jsonValueContainsLiteral(child, literals) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if jsonValueContainsLiteral(child, literals) {
				return true
			}
		}
	case []string:
		for _, child := range typed {
			if literals.Contains(child) {
				return true
			}
		}
	case map[string]string:
		for key, child := range typed {
			if literals.Contains(key) || literals.Contains(child) {
				return true
			}
		}
	case nil:
		return literals.Contains("null")
	case bool:
		if typed {
			return literals.Contains("true")
		}
		return literals.Contains("false")
	case json.Number:
		return literals.Contains(typed.String())
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		encoded, err := json.Marshal(typed)
		return err != nil || literals.Contains(string(encoded))
	}
	return false
}

type extensionToolHook struct {
	runner         *extensions.HookRunner
	snapshot       extensions.HookSnapshot
	sessionID      string
	transcriptPath string
	workspace      string
	permissionMode string
}

func (hook extensionToolHook) dispatch(ctx context.Context, event extensions.HookEventName, request tool.Request, result *tool.Result) (extensions.HookAggregate, error) {
	rawInput := request.Input
	if result != nil && len(result.ExecutedInput) > 0 {
		rawInput = result.ExecutedInput
	}
	input, err := hookToolInput(rawInput)
	if err != nil {
		return extensions.HookAggregate{}, fmt.Errorf("project %s hook input: %w", event, err)
	}
	fields := map[string]any{"tool_name": request.Name, "tool_input": input, "tool_use_id": request.ID}
	switch event {
	case extensions.HookPostToolUse:
		fields["tool_response"] = hookToolResponse(*result)
	case extensions.HookPostToolUseFailure:
		fields["error"] = result.Content
		if result.Code == "cancelled" {
			fields["is_interrupt"] = true
		}
	case extensions.HookPermissionDenied:
		fields["reason"] = result.Content
	}
	hookInput := extensions.HookInput{SessionID: hook.sessionID, TranscriptPath: hook.transcriptPath, CWD: hook.workspace, PermissionMode: hook.permissionMode, Event: event, Fields: fields}
	encoded, err := json.Marshal(hookInput)
	if err != nil {
		return extensions.HookAggregate{}, errors.New("encode scoped hook observer payload")
	}
	projected, scoped, err := request.ProjectObserverPayload(encoded)
	if err != nil {
		return extensions.HookAggregate{}, errors.New("hook observer payload could not be safely projected")
	}
	if scoped {
		decoder := json.NewDecoder(bytes.NewReader(projected))
		decoder.UseNumber()
		if err := decoder.Decode(&hookInput); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return extensions.HookAggregate{}, errors.New("decode scoped hook observer payload")
		}
	}
	return hook.runner.Dispatch(ctx, hook.snapshot, hookInput)
}

func (hook extensionToolHook) Pre(ctx context.Context, request tool.Request, canonical string) (tool.HookResult, error) {
	request.Name = canonical
	aggregate, err := hook.dispatch(ctx, extensions.HookPreToolUse, request, nil)
	if err != nil {
		return tool.HookResult{}, err
	}
	result := tool.HookResult{}
	if aggregate.Decision == extensions.HookDecisionDeny || !aggregate.Continue {
		result.DenyReason = fallback(aggregate.Reason, "pre-tool hook denied execution")
	}
	if aggregate.Decision == extensions.HookDecisionAsk {
		result.AskReason = fallback(aggregate.Reason, "pre-tool hook requires approval")
	}
	if aggregate.UpdatedInput != nil {
		updated, marshalErr := json.Marshal(aggregate.UpdatedInput)
		if marshalErr != nil {
			return tool.HookResult{}, marshalErr
		}
		result.UpdatedInput = updated
	}
	for _, message := range aggregate.Contexts {
		result.Progress = append(result.Progress, tool.Progress{ToolUseID: request.ID, Message: message})
	}
	return result, nil
}

func (hook extensionToolHook) Post(ctx context.Context, request tool.Request, result tool.Result) error {
	request.Name = result.Name
	_, err := hook.dispatch(ctx, extensions.HookPostToolUse, request, &result)
	return err
}

func (hook extensionToolHook) Failure(ctx context.Context, request tool.Request, result tool.Result) error {
	request.Name = result.Name
	_, err := hook.dispatch(ctx, extensions.HookPostToolUseFailure, request, &result)
	return err
}

func (hook extensionToolHook) PermissionDenied(ctx context.Context, request tool.Request, result tool.Result) error {
	request.Name = result.Name
	_, err := hook.dispatch(ctx, extensions.HookPermissionDenied, request, &result)
	return err
}

func (hook extensionToolHook) permissionApprover(next permission.Approver) permission.Approver {
	return func(ctx context.Context, request permission.ApprovalRequest) (permission.ApprovalResponse, error) {
		input, err := hookToolInput(request.Input)
		if err != nil {
			return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "permission hook received invalid tool input"}, nil
		}
		type outcome struct {
			response permission.ApprovalResponse
			decisive bool
			err      error
		}
		raceCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		results := make(chan outcome, 2)
		go func() {
			aggregate, dispatchErr := hook.runner.Dispatch(raceCtx, hook.snapshot, extensions.HookInput{
				SessionID: hook.sessionID, TranscriptPath: hook.transcriptPath, CWD: hook.workspace,
				PermissionMode: hook.permissionMode, Event: extensions.HookPermissionRequest,
				Fields: map[string]any{"tool_name": request.Tool, "tool_input": input},
			})
			if dispatchErr != nil {
				results <- outcome{err: fmt.Errorf("permission-request hook: %w", dispatchErr)}
				return
			}
			if aggregate.Decision == extensions.HookDecisionDeny || !aggregate.Continue {
				results <- outcome{decisive: true, response: permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: fallback(aggregate.Reason, "permission-request hook denied execution")}}
				return
			}
			if aggregate.Decision == extensions.HookDecisionAllow {
				var updated json.RawMessage
				if aggregate.UpdatedInput != nil {
					updated, dispatchErr = json.Marshal(aggregate.UpdatedInput)
					if dispatchErr != nil {
						results <- outcome{err: fmt.Errorf("marshal permission-hook input: %w", dispatchErr)}
						return
					}
				}
				results <- outcome{decisive: true, response: permission.ApprovalResponse{Kind: permission.DecisionAllow, UpdatedInput: updated, Reason: fallback(aggregate.Reason, "permission-request hook approved once")}}
				return
			}
			results <- outcome{}
		}()
		responders := 1
		if next != nil {
			responders++
			go func() {
				response, approvalErr := next(raceCtx, request)
				if approvalErr != nil {
					results <- outcome{err: approvalErr}
					return
				}
				switch response.Kind {
				case permission.DecisionAllow, permission.DecisionDeny, permission.DecisionCancel:
					results <- outcome{response: response, decisive: true}
				default:
					results <- outcome{err: errors.New("approval surface returned a nonterminal decision")}
				}
			}()
		}
		for completed := 0; completed < responders; completed++ {
			select {
			case result := <-results:
				if result.err != nil {
					cancel()
					return permission.ApprovalResponse{}, result.err
				}
				if result.decisive {
					cancel()
					return result.response, nil
				}
			case <-ctx.Done():
				return permission.ApprovalResponse{}, ctx.Err()
			}
		}
		return permission.ApprovalResponse{Kind: permission.DecisionDeny, Reason: "permission request remained unresolved and no approval surface is available"}, nil
	}
}

func hookToolInput(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || input == nil {
		return nil, errors.New("tool input must be one JSON object")
	}
	return input, nil
}

func hookToolResponse(result tool.Result) map[string]any {
	response := map[string]any{"content": result.Content, "is_error": result.IsError}
	if result.Code != "" {
		response["code"] = result.Code
	}
	if result.Metadata != nil {
		response["metadata"] = result.Metadata
	}
	return response
}

func hookConditionMatcher(registry *tool.Registry) func(string, extensions.HookInput) bool {
	return func(rawRule string, input extensions.HookInput) bool {
		rule, err := permission.ParseRule(rawRule, permission.EffectAllow, "hook_condition", false)
		if err != nil {
			return false
		}
		toolName, _ := input.Fields["tool_name"].(string)
		descriptor, ok := registry.Resolve(toolName)
		if !ok {
			return false
		}
		rawInput, err := json.Marshal(input.Fields["tool_input"])
		if err != nil {
			return false
		}
		validated, err := descriptor.Validate(rawInput)
		if err != nil {
			return false
		}
		projected := permission.Request{Tool: descriptor.Name, Input: rawInput}
		if descriptor.ProjectPermission != nil {
			projected, err = descriptor.ProjectPermission(validated, rawInput)
			if err != nil {
				return false
			}
			projected.Tool = descriptor.Name
		}
		contents := append([]string{projected.Content}, projected.MatchContents...)
		return rule.Matches(projected.Tool, contents...)
	}
}

func runtimeHookEvents() []extensions.HookEventName {
	return []extensions.HookEventName{
		extensions.HookPreToolUse,
		extensions.HookPostToolUse,
		extensions.HookPostToolUseFailure,
		extensions.HookPermissionRequest,
		extensions.HookPermissionDenied,
		extensions.HookUserPromptSubmit,
		extensions.HookSessionStart,
		extensions.HookSessionEnd,
	}
}
