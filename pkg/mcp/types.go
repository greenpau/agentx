// Package mcp implements the untrusted adapter boundary for Model Context
// Protocol providers. It intentionally keeps provider configuration, transport
// state, protocol wire values, and model-facing names separate.
package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/childenv"
	"github.com/greenpau/agentx/pkg/redact"
)

const (
	ProtocolVersion        = "2025-06-18"
	DefaultConnectTimeout  = 30 * time.Second
	DefaultRequestTimeout  = 60 * time.Second
	DefaultToolTimeout     = 10 * time.Minute
	DefaultMaxMessageBytes = 4 << 20
	DefaultMaxListItems    = 2_000
	DefaultMaxPages        = 100
	DefaultConcurrency     = 3
	DefaultQueueDepth      = 64
	MaxDescriptionBytes    = 2_048
	MaxServerNameBytes     = 128
	MaxCapabilityNameBytes = 256
	// MaxCredentialLiterals and MaxCredentialLiteralBytes bound the exact-set
	// work applied to each provider result. They cover full environment/header
	// values and separately extracted bearer/basic credentials.
	MaxCredentialLiterals     = 256
	MaxCredentialLiteralBytes = 64 << 10
)

var (
	ErrUnavailable          = errors.New("MCP capability unavailable")
	ErrUnsupportedTransport = errors.New("MCP transport unsupported in this build")
	ErrClosed               = errors.New("MCP connection closed")
	ErrProtocol             = errors.New("MCP protocol error")
	serverNamePattern       = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	capabilityNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type Transport string

const (
	TransportStdio       Transport = "stdio"
	TransportSSE         Transport = "sse"
	TransportSSEIDE      Transport = "sse-ide"
	TransportHTTP        Transport = "http"
	TransportWS          Transport = "ws"
	TransportWSIDE       Transport = "ws-ide"
	TransportSDK         Transport = "sdk"
	TransportAgentXProxy Transport = "agentx-proxy"
)

type Scope string

const (
	ScopeLocal      Scope = "local"
	ScopeUser       Scope = "user"
	ScopeProject    Scope = "project"
	ScopeDynamic    Scope = "dynamic"
	ScopeEnterprise Scope = "enterprise"
	ScopeAgentX     Scope = "agentx"
	ScopeManaged    Scope = "managed"
	// ScopePlugin is an internal composition scope for plugin-contributed
	// definitions. It is never accepted as an unattributed ordinary setting.
	ScopePlugin Scope = "plugin"
)

type ConnectionState string

const (
	StatePending   ConnectionState = "pending"
	StateConnected ConnectionState = "connected"
	StateFailed    ConnectionState = "failed"
	StateNeedsAuth ConnectionState = "needs-auth"
	StateDisabled  ConnectionState = "disabled"
	StateClosed    ConnectionState = "closed"
)

type OAuthConfig struct {
	ClientID              string `json:"client_id,omitempty"`
	CallbackPort          int    `json:"callback_port,omitempty"`
	AuthServerMetadataURL string `json:"auth_server_metadata_url,omitempty"`
	ExtendedAuthorization bool   `json:"xaa,omitempty"`
}

// Config is source configuration. Credential-named Env and Headers, plus
// authorization-scheme header values, are secret-bearing. Scope, source
// identity, trust, policy, approval, and generation are assigned by the owning
// discovery/policy adapters. None may be asserted by an MCP document.
type Config struct {
	Name      string            `json:"name"`
	Transport Transport         `json:"type,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"-"`
	// WorkingDirectory is assigned by the owning discovery adapter. It is not
	// accepted from an untrusted MCP document.
	WorkingDirectory string            `json:"-"`
	URL              string            `json:"url,omitempty"`
	Headers          map[string]string `json:"-"`
	OAuth            *OAuthConfig      `json:"oauth,omitempty"`
	Scope            Scope             `json:"-"`
	SourceID         string            `json:"-"`
	// Trusted is asserted by the owning discovery adapter, never by MCP
	// configuration JSON. Dynamic, plugin, and product-integration definitions
	// fail closed without this capability-bearing assertion.
	Trusted      bool   `json:"-"`
	Disabled     bool   `json:"disabled,omitempty"`
	Untrusted    bool   `json:"-"`
	PolicyDenied bool   `json:"-"`
	Approved     bool   `json:"-"`
	Generation   uint64 `json:"-"`
	// ConfigurationError is assigned only by the owning loader after bounded
	// environment expansion fails. It keeps one broken server diagnostic inside
	// composition instead of aborting healthy siblings.
	ConfigurationError string        `json:"-"`
	ConnectTimeout     time.Duration `json:"connect_timeout,omitempty"`
	RequestTimeout     time.Duration `json:"request_timeout,omitempty"`
	ToolTimeout        time.Duration `json:"tool_timeout,omitempty"`
	MaxMessageBytes    int           `json:"max_message_bytes,omitempty"`
	// expandedCredentialLiterals records sensitive environment substitutions
	// embedded into command, argument, or environment strings. It is populated
	// only by the owning expansion adapter and never decoded from configuration.
	expandedCredentialLiterals []string
}

// String and GoString deliberately expose none of the retained configuration.
// Config owns credential-bearing maps and may be nested in public diagnostics.
func (Config) String() string   { return "" }
func (Config) GoString() string { return "" }
func (config Config) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, config.String())
}

func cloneConfig(config Config) Config {
	config.Args = append([]string(nil), config.Args...)
	config.Env = cloneMap(config.Env)
	config.Headers = cloneMap(config.Headers)
	config.expandedCredentialLiterals = append([]string(nil), config.expandedCredentialLiterals...)
	if config.OAuth != nil {
		oauth := *config.OAuth
		config.OAuth = &oauth
	}
	return config
}

type Availability struct {
	BuildIncluded      bool     `json:"build_included"`
	FeatureEnabled     bool     `json:"feature_enabled"`
	PlatformSupported  bool     `json:"platform_supported"`
	SourceTrusted      bool     `json:"source_trusted"`
	PolicyAllowed      bool     `json:"policy_allowed"`
	Approved           bool     `json:"approved"`
	Configured         bool     `json:"configured"`
	TransportSupported bool     `json:"transport_supported"`
	Reasons            []string `json:"reasons,omitempty"`
}

func (a Availability) Usable() bool {
	return a.BuildIncluded && a.FeatureEnabled && a.PlatformSupported &&
		a.SourceTrusted && a.PolicyAllowed && a.Approved && a.Configured &&
		a.TransportSupported
}

func (a Availability) clone() Availability {
	a.Reasons = append([]string(nil), a.Reasons...)
	return a
}

type Diagnostic struct {
	Server  string `json:"server,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

type Descriptor struct {
	Name         string          `json:"name"`
	ScopedID     string          `json:"scoped_identity"`
	SourceID     string          `json:"source_identity,omitempty"`
	Scope        Scope           `json:"scope"`
	Transport    Transport       `json:"transport"`
	SemanticKey  string          `json:"-"`
	Fingerprint  string          `json:"-"`
	Availability Availability    `json:"availability"`
	State        ConnectionState `json:"state"`
	Generation   uint64          `json:"generation"`
	Diagnostics  []Diagnostic    `json:"diagnostics,omitempty"`
	config       Config
}

// String and GoString prevent diagnostic traversal into the retained Config.
func (Descriptor) String() string   { return "" }
func (Descriptor) GoString() string { return "" }
func (descriptor Descriptor) Format(state fmt.State, verb rune) {
	_, _ = fmt.Fprint(state, descriptor.String())
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Availability = descriptor.Availability.clone()
	descriptor.Diagnostics = append([]Diagnostic(nil), descriptor.Diagnostics...)
	descriptor.config = cloneConfig(descriptor.config)
	return descriptor
}

func ValidateConfig(raw Config) (_ Descriptor, resultErr error) {
	config := cloneConfig(raw)
	literals, err := CredentialLiterals(config)
	if err != nil {
		return Descriptor{}, errors.New("MCP credential configuration is invalid")
	}
	credentials := redact.New(literals...)
	defer func() {
		if resultErr == nil || credentials == nil || credentials.Empty() {
			return
		}
		message, suppressed := credentials.Redact(resultErr.Error())
		if suppressed {
			resultErr = errors.New("")
			return
		}
		resultErr = errors.New(message)
	}()
	config.Name = strings.TrimSpace(config.Name)
	if !validServerName(config.Name) {
		return Descriptor{}, errors.New("MCP server name is too long, contains invalid characters, or uses reserved double underscores")
	}
	if !validScope(config.Scope) {
		return Descriptor{}, errors.New("MCP scope is invalid")
	}
	if config.ConfigurationError != "" {
		return Descriptor{}, errors.New("MCP configuration expansion failed")
	}
	config.SourceID = strings.TrimSpace(config.SourceID)
	if config.SourceID == "" {
		switch config.Scope {
		case ScopeDynamic, ScopeAgentX, ScopePlugin:
			return Descriptor{}, errors.New("MCP definition requires an attributed source identity")
		default:
			config.SourceID = string(config.Scope) + ":" + config.Name
		}
	}
	if len(config.SourceID) > 1_024 || strings.ContainsAny(config.SourceID, "\r\n\x00") {
		return Descriptor{}, errors.New("MCP source identity contains invalid control characters or exceeds 1 KiB")
	}
	if config.Transport == "" {
		if strings.TrimSpace(config.Command) != "" {
			config.Transport = TransportStdio
		} else {
			return Descriptor{}, errors.New("MCP transport is required")
		}
	}
	sourceTrusted := !config.Untrusted
	if config.Scope == ScopeDynamic || config.Scope == ScopeAgentX || config.Scope == ScopePlugin {
		sourceTrusted = config.Trusted && !config.Untrusted
	}
	availability := Availability{
		BuildIncluded: true, FeatureEnabled: !config.Disabled, PlatformSupported: true,
		SourceTrusted: sourceTrusted, PolicyAllowed: !config.PolicyDenied,
		Approved: config.Scope != ScopeProject || config.Approved, Configured: true,
		TransportSupported: config.Transport == TransportStdio,
	}
	if config.Disabled {
		availability.Reasons = append(availability.Reasons, "server is disabled")
	}
	if !sourceTrusted {
		availability.Reasons = append(availability.Reasons, "source is not trusted")
	}
	if config.PolicyDenied {
		availability.Reasons = append(availability.Reasons, "blocked by MCP policy")
	}
	if !availability.Approved {
		availability.Reasons = append(availability.Reasons, "project server definition is not approved")
	}

	switch config.Transport {
	case TransportStdio:
		config.Command = strings.TrimSpace(config.Command)
		if config.Command == "" {
			return Descriptor{}, errors.New("stdio MCP server requires a command")
		}
		if strings.ContainsRune(config.Command, '\x00') {
			return Descriptor{}, errors.New("MCP command contains NUL")
		}
		for _, argument := range config.Args {
			if strings.ContainsRune(argument, '\x00') {
				return Descriptor{}, errors.New("MCP argument contains NUL")
			}
		}
		for name, value := range config.Env {
			if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, '\x00') {
				return Descriptor{}, errors.New("MCP environment contains an invalid name or NUL")
			}
		}
		if config.WorkingDirectory != "" {
			if strings.ContainsRune(config.WorkingDirectory, '\x00') || !filepath.IsAbs(config.WorkingDirectory) {
				return Descriptor{}, errors.New("MCP working directory must be an absolute path without NUL")
			}
			config.WorkingDirectory = filepath.Clean(config.WorkingDirectory)
			info, err := os.Lstat(config.WorkingDirectory)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return Descriptor{}, errors.New("MCP working directory must be a direct existing directory")
			}
		}
		if config.URL != "" || config.OAuth != nil {
			return Descriptor{}, errors.New("stdio MCP configuration cannot include URL or OAuth")
		}
	case TransportSSE, TransportSSEIDE, TransportHTTP, TransportWS, TransportWSIDE:
		target, err := url.Parse(config.URL)
		if err != nil || !target.IsAbs() || target.Hostname() == "" || target.User != nil {
			return Descriptor{}, errors.New("remote MCP server requires an absolute URL without user information")
		}
		allowedScheme := (config.Transport == TransportHTTP || config.Transport == TransportSSE || config.Transport == TransportSSEIDE) && (target.Scheme == "http" || target.Scheme == "https") ||
			(config.Transport == TransportWS || config.Transport == TransportWSIDE) && (target.Scheme == "ws" || target.Scheme == "wss")
		if !allowedScheme {
			return Descriptor{}, errors.New("MCP URL scheme is invalid for the selected transport")
		}
		for name, value := range config.Headers {
			if strings.TrimSpace(name) == "" || strings.ContainsAny(value, "\r\n\x00") {
				return Descriptor{}, errors.New("MCP headers contain invalid control characters")
			}
		}
		if config.OAuth != nil && config.OAuth.AuthServerMetadataURL != "" {
			metadata, err := url.Parse(config.OAuth.AuthServerMetadataURL)
			if err != nil || metadata.Scheme != "https" || metadata.Hostname() == "" {
				return Descriptor{}, errors.New("OAuth metadata URL must use HTTPS")
			}
		}
		availability.TransportSupported = false
		availability.Reasons = append(availability.Reasons, "selected transport is not operational in this build")
	case TransportSDK, TransportAgentXProxy:
		return Descriptor{}, ErrUnsupportedTransport
	default:
		return Descriptor{}, errors.New("MCP transport is invalid")
	}

	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = DefaultConnectTimeout
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = DefaultRequestTimeout
	}
	if config.ToolTimeout <= 0 {
		config.ToolTimeout = DefaultToolTimeout
	}
	if config.MaxMessageBytes <= 0 {
		config.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if config.ConnectTimeout > 10*time.Minute || config.RequestTimeout > 10*time.Minute || config.ToolTimeout > 28*24*time.Hour {
		return Descriptor{}, errors.New("MCP timeout exceeds configured safety bound")
	}
	if config.MaxMessageBytes < 1_024 || config.MaxMessageBytes > 64<<20 {
		return Descriptor{}, errors.New("MCP maximum message size must be between 1 KiB and 64 MiB")
	}

	semanticKey := configSemanticKey(config)
	fingerprint, err := configFingerprint(config)
	if err != nil {
		return Descriptor{}, err
	}
	scopedID := string(config.Scope) + ":" + config.Name
	state := StatePending
	if !availability.Usable() {
		state = StateDisabled
	}
	descriptor := Descriptor{
		Name: config.Name, ScopedID: scopedID, SourceID: config.SourceID,
		Scope: config.Scope, Transport: config.Transport, SemanticKey: semanticKey,
		Fingerprint: fingerprint, Availability: availability, State: state,
		Generation: config.Generation, config: config,
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return Descriptor{}, errors.New("MCP descriptor could not be safely projected")
	}
	reflected, err := credentials.JSONContains(encoded)
	if err != nil || reflected {
		return Descriptor{}, errors.New("MCP configuration is incompatible with public descriptor framing")
	}
	return descriptor, nil
}

// CredentialLiterals returns the bounded exact values which a provider could
// reflect from its explicit secret-bearing configuration.
func CredentialLiterals(config Config) ([]string, error) {
	seen := make(map[string]struct{})
	values := make([]string, 0, len(config.Env)+len(config.Headers))
	totalBytes := 0
	add := func(value string) error {
		if value == "" {
			return nil
		}
		if !utf8.ValidString(value) || strings.IndexFunc(value, func(character rune) bool {
			return unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
		}) >= 0 {
			return errors.New("MCP credential material must be valid UTF-8 without control or format characters")
		}
		if _, exists := seen[value]; exists {
			return nil
		}
		if len(values) >= MaxCredentialLiterals || len(value) > MaxCredentialLiteralBytes-totalBytes {
			return errors.New("MCP credential material exceeds redaction workload limit")
		}
		seen[value] = struct{}{}
		values = append(values, value)
		totalBytes += len(value)
		return nil
	}
	for _, value := range config.expandedCredentialLiterals {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	for name, value := range config.Env {
		if childenv.SensitiveName(name) {
			if err := add(value); err != nil {
				return nil, err
			}
		}
	}
	for name, value := range config.Headers {
		fields := strings.Fields(value)
		authorizationScheme := len(fields) == 2 &&
			(strings.EqualFold(fields[0], "bearer") || strings.EqualFold(fields[0], "basic"))
		sensitiveName := childenv.SensitiveName(strings.ReplaceAll(name, "-", "_"))
		if sensitiveName || authorizationScheme {
			if err := add(value); err != nil {
				return nil, err
			}
		}
		if authorizationScheme {
			if err := add(fields[1]); err != nil {
				return nil, err
			}
		}
	}
	return values, nil
}

func validScope(scope Scope) bool {
	switch scope {
	case ScopeLocal, ScopeUser, ScopeProject, ScopeDynamic, ScopeEnterprise, ScopeAgentX, ScopeManaged, ScopePlugin:
		return true
	default:
		return false
	}
}

func configSemanticKey(config Config) string {
	if config.Transport == TransportStdio {
		return "stdio\x00" + config.WorkingDirectory + "\x00" + config.Command + "\x00" + strings.Join(config.Args, "\x00")
	}
	target, _ := url.Parse(config.URL)
	if target != nil {
		target.Fragment = ""
		target.Host = strings.ToLower(target.Host)
		return "remote\x00" + target.String()
	}
	return string(config.Transport) + "\x00" + config.URL
}

func configFingerprint(config Config) (string, error) {
	type fingerprintConfig struct {
		Name             string            `json:"name"`
		Scope            Scope             `json:"scope"`
		SourceID         string            `json:"source_identity"`
		Transport        Transport         `json:"transport"`
		Command          string            `json:"command,omitempty"`
		Args             []string          `json:"args,omitempty"`
		Env              map[string]string `json:"env,omitempty"`
		WorkingDirectory string            `json:"working_directory,omitempty"`
		URL              string            `json:"url,omitempty"`
		Headers          map[string]string `json:"headers,omitempty"`
		OAuth            *OAuthConfig      `json:"oauth,omitempty"`
	}
	encoded, err := json.Marshal(fingerprintConfig{
		Name: config.Name, Scope: config.Scope, SourceID: config.SourceID,
		Transport: config.Transport, Command: config.Command,
		Args: config.Args, Env: config.Env, WorkingDirectory: config.WorkingDirectory, URL: config.URL, Headers: config.Headers, OAuth: config.OAuth,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
	binding     ToolBinding
}

// Binding returns the opaque catalog authority captured with this descriptor
// by Manager.Tools. Directly decoded or manually constructed descriptors are
// deliberately unbound and cannot be adapted into executable capabilities.
func (descriptor ToolDescriptor) Binding() (ToolBinding, bool) {
	return descriptor.binding, descriptor.binding.bound
}

type ResourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type PromptDescriptor struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type ContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MIMEType string          `json:"mimeType,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
}

type ToolResult struct {
	Content           []ContentBlock  `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type ResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

type PromptMessage struct {
	Role    string       `json:"role"`
	Content ContentBlock `json:"content"`
}

type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ServerCapabilities struct {
	Tools     map[string]any `json:"tools,omitempty"`
	Resources map[string]any `json:"resources,omitempty"`
	Prompts   map[string]any `json:"prompts,omitempty"`
	Logging   map[string]any `json:"logging,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("MCP JSON-RPC error %d", e.Code)
}

func (e *RPCError) GoString() string { return e.Error() }

func NamespacedToolName(server, tool string) (string, error) {
	if !validServerName(server) {
		return "", errors.New("invalid MCP server name")
	}
	if !validCapabilityName(tool) {
		return "", errors.New("invalid MCP tool name")
	}
	return "mcp__" + server + "__" + tool, nil
}

func validServerName(name string) bool {
	return len(name) > 0 && len(name) <= MaxServerNameBytes && serverNamePattern.MatchString(name) && !strings.Contains(name, "__")
}

func validCapabilityName(name string) bool {
	return len(name) > 0 && len(name) <= MaxCapabilityNameBytes && capabilityNamePattern.MatchString(name) && !strings.Contains(name, "__")
}

func validateToolDescriptor(tool ToolDescriptor) error {
	if !validCapabilityName(tool.Name) {
		return errors.New("tool name is invalid")
	}
	if len(tool.Description) > MaxDescriptionBytes {
		return errors.New("tool description exceeds size limit")
	}
	if len(tool.InputSchema) == 0 {
		return errors.New("tool input schema is required")
	}
	if tool.Annotations != nil {
		if err := validateJSONDepth(tool.Annotations, 0); err != nil {
			return fmt.Errorf("invalid tool annotations: %w", err)
		}
	}
	return validateJSONSchema(tool.InputSchema)
}

func validateJSONSchema(raw json.RawMessage) error {
	if len(raw) > 1<<20 {
		return errors.New("JSON schema exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON schema: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("JSON schema must be an object")
	}
	if err := validateJSONDepth(object, 0); err != nil {
		return err
	}
	if schemaType, ok := object["type"].(string); ok && schemaType != "object" {
		return errors.New("tool input schema type must be object")
	}
	if err := ValidateToolSchema(object); err != nil {
		return fmt.Errorf("unsupported tool input schema: %w", err)
	}
	return nil
}

func validateJSONDepth(value any, depth int) error {
	if depth > 32 {
		return errors.New("remote JSON exceeds maximum nesting depth")
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 2_000 {
			return errors.New("remote JSON object has too many fields")
		}
		for key, child := range typed {
			if len(key) > 512 {
				return errors.New("remote JSON key exceeds size limit")
			}
			if err := validateJSONDepth(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > DefaultMaxListItems {
			return errors.New("remote JSON array exceeds item limit")
		}
		for _, child := range typed {
			if err := validateJSONDepth(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = cloneJSONValue(child)
		}
		return result
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
