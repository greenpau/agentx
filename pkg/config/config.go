// Package config loads, validates, and normalizes process configuration.
package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/redact"
)

const (
	DefaultAuthFile        = "auth.json"
	DefaultModel           = "gpt-5.6-sol"
	DefaultAzureAPIVersion = ""
	// DefaultReasoningEffort remains the conventional direct-constructor
	// default. auth.json-backed starts use each provider's default_effort.
	DefaultReasoningEffort = "high"
	DefaultRequestTimeout  = 10 * time.Minute
	DefaultStreamWatchdog  = 90 * time.Second
	DefaultMaxRetries      = 10

	// UserGuideURL is the stable user-facing setup guide for authentication
	// failures that must be resolved outside the running process.
	UserGuideURL = "https://github.com/greenpau/agentx/blob/main/USER_GUIDE.md"

	// AuthFilePlaceholder is safe to include in diagnostics and documentation.
	// It is the complete supported auth-file schema, with no live credential.
	AuthFilePlaceholder = `{
  "version": 2,
  "providers": [
    {
      "id": "sol-5.6",
      "type": "azure_openai",
      "default": true,
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://your-resource.openai.azure.com",
        "model": "gpt-5.6-sol",
        "deployment": "gpt-5.6-sol",
        "api_key": "replace-with-your-secret",
        "api_version": "preview"
      }
    }
  ]
}`
)

var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	ErrInvalid         = errors.New("invalid configuration")
	ErrAuthFileMissing = errors.New("auth file is missing")
)

// credentialSafeConfigError preserves the public configuration category
// without retaining an unsafe cause. In particular, Unwrap must not expose a
// fixed diagnostic whose text happens to equal one of the configured keys.
type credentialSafeConfigError struct {
	message string
}

func (e *credentialSafeConfigError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *credentialSafeConfigError) Is(target error) bool { return target == ErrInvalid }

func (e *credentialSafeConfigError) GoString() string { return e.Error() }

func (e *credentialSafeConfigError) Format(state fmt.State, verb rune) {
	// Ignore quoting, width, and precision. Adding even conventional format
	// framing after projection could recreate a one-byte credential that was
	// absent from the protected message.
	_, _ = fmt.Fprint(state, e.Error())
}

// protectConfigurationError copies only a complete credential-safe
// presentation of cause. The cause itself is deliberately discarded so
// errors.Unwrap/errors.As cannot recover an unsafe diagnostic. When no safe
// replacement exists, the presentation is suppressed while errors.Is still
// reports ErrInvalid.
func protectConfigurationError(credentials *redact.Set, cause error) error {
	if cause == nil {
		return nil
	}
	message := cause.Error()
	if credentials != nil {
		var suppressed bool
		message, suppressed = credentials.Redact(message)
		if suppressed {
			message = ""
		}
	}
	return &credentialSafeConfigError{message: message}
}

// MissingAuthFileDiagnostic returns the credential-independent setup guidance
// presented when ErrAuthFileMissing stops normal startup. The effective path
// is quoted so control characters cannot alter terminal framing.
func MissingAuthFileDiagnostic(path string) string {
	if path == "" {
		path = "~/.agentx/" + DefaultAuthFile
	}
	return fmt.Sprintf(
		"AgentX credentials are not configured. Create the authentication file at %q with this shape:\n%s\nSee %s",
		path,
		AuthFilePlaceholder,
		UserGuideURL,
	)
}

type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "auth_file"
	SourceProcess Source = "process"
	SourceFlag    Source = "flag"
)

// Azure contains the normalized credential and deployment settings. APIKey is
// intentionally excluded from String and every presentation projection.
type Azure struct {
	Endpoint        *url.URL
	ModelName       string
	Deployment      string
	APIKey          string `json:"-"`
	APIVersion      string
	ReasoningEffort string
	// SupportedReasoningEfforts is the exact operator-declared capability set
	// for the selected endpoint. It is copied before crossing package bounds.
	SupportedReasoningEfforts []string
	RequestTimeout            time.Duration
	StreamWatchdog            time.Duration
	MaxRetries                int

	// UnsafeAllowInsecureLoopbackForTesting is an explicit transport test
	// seam. Application configuration loading never sets it.
	UnsafeAllowInsecureLoopbackForTesting bool `json:"-"`
}

// ReasoningCapabilities describes the reasoning controls declared for one
// configured model endpoint. Efforts preserves auth.json presentation order.
type ReasoningCapabilities struct {
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}

// ProviderDescriptor is the credential-free public identity of one auth.json
// provider profile. It deliberately excludes Azure routing and API versions.
type ProviderDescriptor struct {
	ID        string                `json:"id"`
	Type      string                `json:"type"`
	Model     string                `json:"model"`
	Default   bool                  `json:"default"`
	Selected  bool                  `json:"selected"`
	Reasoning ReasoningCapabilities `json:"reasoning"`
}

type Runtime struct {
	Azure            Azure
	SelectedProvider ProviderDescriptor   `json:"selected_provider"`
	Providers        []ProviderDescriptor `json:"providers"`
	AuthFile         string               `json:"-"`
	Provenance       map[string]Source
	providerBinding  string
	credentials      *redact.Set
}

type Overrides struct {
	Provider        string
	Model           string
	ReasoningEffort string
	RequestTimeout  time.Duration
	StreamWatchdog  time.Duration
	MaxRetries      int
}

func Load(pathname string, environ []string, overrides Overrides) (Runtime, error) {
	if pathname == "" {
		pathname = DefaultAuthFile
	}
	return load(authFileLocation{path: pathname}, environ, overrides)
}

// LoadAtRoot loads the literal auth.json child through a caller-owned,
// descriptor-pinned application-home root. pathname is retained only for
// diagnostics and protected-path attribution.
func LoadAtRoot(root *os.Root, pathname string, environ []string, overrides Overrides) (Runtime, error) {
	if root == nil {
		return Runtime{}, errors.New("auth file root is unavailable")
	}
	if pathname == "" {
		pathname = DefaultAuthFile
	}
	return load(authFileLocation{root: root, path: pathname}, environ, overrides)
}

func load(location authFileLocation, environ []string, overrides Overrides) (Runtime, error) {
	auth, err := loadAuthFile(location)
	if err != nil {
		return Runtime{}, err
	}
	profiles, credentials, err := normalizeAuthProviders(auth)
	if err != nil {
		return Runtime{}, err
	}
	selectedIndex, err := selectAuthProvider(profiles, overrides.Provider)
	if err != nil {
		return Runtime{}, protectConfigurationError(credentials, err)
	}
	selected := profiles[selectedIndex]
	processEffort, processEffortSet, err := reasoningEffortFromEnvironment(environ)
	if err != nil {
		return Runtime{}, protectConfigurationError(credentials, err)
	}

	provenance := make(map[string]Source)
	for _, key := range []string{
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_MODEL_NAME", "AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "AZURE_OPENAI_API_VERSION",
	} {
		provenance[key] = SourceFile
	}
	provenance["provider"] = SourceFile
	if overrides.Provider != "" {
		provenance["provider"] = SourceFlag
	}

	configuredModel := selected.descriptor.Model
	provenance["model"] = SourceFile
	model := configuredModel
	if overrides.Model != "" {
		if overrides.Model != configuredModel {
			return Runtime{}, protectConfigurationError(credentials, fmt.Errorf("%w: --model is not the deployment-backed model configured by auth.json", ErrInvalid))
		}
		provenance["model"] = SourceFlag
	}
	effort := selected.descriptor.Reasoning.DefaultEffort
	provenance["reasoning_effort"] = SourceFile
	if v := strings.TrimSpace(processEffort); processEffortSet && v != "" {
		effort = v
		provenance["reasoning_effort"] = SourceProcess
	}
	if overrides.ReasoningEffort != "" {
		effort = overrides.ReasoningEffort
		provenance["reasoning_effort"] = SourceFlag
	}

	requestTimeout := DefaultRequestTimeout
	if overrides.RequestTimeout > 0 {
		requestTimeout = overrides.RequestTimeout
	}
	watchdog := DefaultStreamWatchdog
	if overrides.StreamWatchdog > 0 {
		watchdog = overrides.StreamWatchdog
	}
	maxRetries := DefaultMaxRetries
	if overrides.MaxRetries > 0 {
		maxRetries = overrides.MaxRetries
	}

	apiVersion := strings.TrimSpace(selected.auth.APIVersion)
	if apiVersion == "" {
		// Keep Azure's v1 selector implicit. The OpenAI-compatible endpoint
		// documents v1 as the default, and omitting the query is required by
		// some resource generations. Explicit versions remain source-attributed.
		apiVersion = DefaultAzureAPIVersion
		provenance["AZURE_OPENAI_API_VERSION"] = SourceDefault
	}
	azure := Azure{
		Endpoint:                  selected.endpoint,
		ModelName:                 strings.TrimSpace(model),
		Deployment:                strings.TrimSpace(selected.auth.Deployment),
		APIKey:                    selected.auth.APIKey,
		APIVersion:                apiVersion,
		ReasoningEffort:           strings.TrimSpace(effort),
		SupportedReasoningEfforts: append([]string(nil), selected.descriptor.Reasoning.Efforts...),
		RequestTimeout:            requestTimeout,
		StreamWatchdog:            watchdog,
		MaxRetries:                maxRetries,
	}
	if err := azure.Validate(); err != nil {
		return Runtime{}, protectConfigurationError(credentials, err)
	}
	descriptors := make([]ProviderDescriptor, len(profiles))
	for index, profile := range profiles {
		descriptors[index] = cloneProviderDescriptor(profile.descriptor)
		descriptors[index].Selected = index == selectedIndex
	}
	selectedDescriptor := cloneProviderDescriptor(descriptors[selectedIndex])
	return Runtime{
		Azure: azure, SelectedProvider: selectedDescriptor, Providers: descriptors,
		AuthFile: location.path, Provenance: provenance,
		providerBinding: selected.binding, credentials: credentials,
	}, nil
}

type normalizedAuthProvider struct {
	descriptor ProviderDescriptor
	auth       azureOpenAIAuth
	endpoint   *url.URL
	binding    string
}

func normalizeAuthProviders(document authFileDocument) ([]normalizedAuthProvider, *redact.Set, error) {
	apiKeys := make([]string, 0, len(document.Providers))
	for _, provider := range document.Providers {
		apiKeys = append(apiKeys, provider.AzureOpenAI.APIKey)
	}
	// Freeze the whole parsed credential scope before semantic validation. A
	// malformed selected or unselected profile must not make an earlier error
	// escape protection by delaying union construction until the success path.
	credentials := redact.New(apiKeys...)
	protect := func(cause error) error {
		return protectConfigurationError(credentials, cause)
	}
	if credentials.LiteralCount() > maxAuthFileProviders || credentials.TotalLiteralBytes() > int(maxCredentialFileBytes) {
		return nil, nil, protect(invalidAuthFile("auth file provider credentials exceed the redaction workload limit"))
	}
	if !credentials.Empty() && credentials.TerminalMarker() == "" {
		return nil, nil, protect(invalidAuthFile("auth file provider credentials have no safe presentation projection"))
	}

	profiles := make([]normalizedAuthProvider, 0, len(document.Providers))
	identifiers := make(map[string]struct{}, len(document.Providers))
	defaultCount := 0
	for _, raw := range document.Providers {
		if !providerIDPattern.MatchString(raw.ID) {
			return nil, nil, protect(invalidAuthFile("auth file provider id must be 1-64 ASCII letters, digits, dots, underscores, or hyphens and start with a letter or digit"))
		}
		if _, exists := identifiers[raw.ID]; exists {
			return nil, nil, protect(invalidAuthFile("auth file contains a duplicate provider id"))
		}
		identifiers[raw.ID] = struct{}{}
		if raw.Default {
			defaultCount++
		}
		reasoning, err := validateReasoningCapabilities(raw.Capabilities.Reasoning)
		if err != nil {
			return nil, nil, protect(err)
		}
		if strings.TrimSpace(raw.AzureOpenAI.Endpoint) != raw.AzureOpenAI.Endpoint ||
			strings.TrimSpace(raw.AzureOpenAI.Model) != raw.AzureOpenAI.Model ||
			strings.TrimSpace(raw.AzureOpenAI.Deployment) != raw.AzureOpenAI.Deployment ||
			strings.TrimSpace(raw.AzureOpenAI.APIVersion) != raw.AzureOpenAI.APIVersion {
			return nil, nil, protect(invalidAuthFile("auth file provider routing contains unsupported surrounding whitespace"))
		}
		apiVersion := raw.AzureOpenAI.APIVersion
		endpoint, err := normalizeEndpoint(raw.AzureOpenAI.Endpoint, apiVersion)
		if err != nil {
			return nil, nil, protect(fmt.Errorf("%w: Azure OpenAI endpoint: %v", ErrInvalid, err))
		}
		azure := Azure{
			Endpoint: endpoint, ModelName: raw.AzureOpenAI.Model,
			Deployment: raw.AzureOpenAI.Deployment, APIKey: raw.AzureOpenAI.APIKey,
			APIVersion: apiVersion, ReasoningEffort: reasoning.DefaultEffort,
			SupportedReasoningEfforts: append([]string(nil), reasoning.Efforts...),
			RequestTimeout:            DefaultRequestTimeout, StreamWatchdog: DefaultStreamWatchdog,
			MaxRetries: DefaultMaxRetries,
		}
		if err := azure.Validate(); err != nil {
			return nil, nil, protect(err)
		}
		descriptor := ProviderDescriptor{
			ID: raw.ID, Type: raw.Type, Model: azure.ModelName,
			Default: raw.Default, Reasoning: reasoning,
		}
		binding := providerRouteBinding(descriptor.Type, endpoint, azure.ModelName, azure.Deployment, azure.APIVersion)
		if providerRouteReflectsCredential(credentials, endpoint, azure.ModelName, azure.Deployment, azure.APIVersion, binding) {
			return nil, nil, protect(invalidAuthFile("auth file normalized provider routing overlaps configured credential material"))
		}
		profiles = append(profiles, normalizedAuthProvider{
			descriptor: descriptor, auth: raw.AzureOpenAI, endpoint: endpoint, binding: binding,
		})
	}
	if defaultCount > 1 {
		return nil, nil, protect(invalidAuthFile("auth.json defines multiple providers with default true; set default true on exactly one provider"))
	}
	if len(profiles) == 1 {
		profiles[0].descriptor.Default = true
	}
	reflected, inspectionErr := providerCatalogReflectsCredential(credentials, profiles)
	if inspectionErr != nil {
		return nil, nil, protect(invalidAuthFile("auth file provider catalog cannot be represented safely"))
	}
	if reflected {
		return nil, nil, protect(invalidAuthFile("auth file public provider metadata overlaps configured credential material"))
	}
	return profiles, credentials, nil
}

func providerRouteBinding(providerType string, endpoint *url.URL, model, deployment, apiVersion string) string {
	endpointValue := ""
	if endpoint != nil {
		endpointValue = endpoint.String()
	}
	bindingInput := strings.Join([]string{providerType, endpointValue, model, deployment, apiVersion}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(bindingInput)))
}

// providerRouteReflectsCredential rejects a key that could be disclosed by a
// normalized route, request URL, wire model, or durable binding. Inspect both
// URL semantics and their escaped wire forms because url.URL intentionally
// retains both representations.
func providerRouteReflectsCredential(credentials *redact.Set, endpoint *url.URL, model, deployment, apiVersion, binding string) bool {
	for _, value := range []string{model, deployment, apiVersion, binding} {
		if credentials.Contains(value) {
			return true
		}
	}
	if endpoint == nil {
		return false
	}
	requestEndpoint := *endpoint
	if apiVersion != "" {
		query := requestEndpoint.Query()
		query.Set("api-version", apiVersion)
		requestEndpoint.RawQuery = query.Encode()
	}
	return urlReflectsCredential(credentials, endpoint) || urlReflectsCredential(credentials, &requestEndpoint)
}

func urlReflectsCredential(credentials *redact.Set, endpoint *url.URL) bool {
	if endpoint == nil {
		return false
	}
	for _, value := range []string{
		endpoint.String(), endpoint.RequestURI(), endpoint.Scheme, endpoint.Opaque,
		endpoint.Host, endpoint.Hostname(), endpoint.Port(), endpoint.Path,
		endpoint.RawPath, endpoint.EscapedPath(), endpoint.RawQuery,
		endpoint.Fragment, endpoint.RawFragment,
	} {
		if credentials.Contains(value) {
			return true
		}
	}
	if endpoint.User != nil && credentials.Contains(endpoint.User.String()) {
		return true
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil {
		return true
	}
	for name, values := range query {
		if credentials.Contains(name) {
			return true
		}
		for _, value := range values {
			if credentials.Contains(value) {
				return true
			}
		}
	}
	return false
}

// providerCatalogReflectsCredential evaluates both the provider-neutral
// discovery state and every state reachable through exact runtime selection.
// Thus credentials cannot become public only before or after a descriptor's
// selected flag changes.
func providerCatalogReflectsCredential(credentials *redact.Set, profiles []normalizedAuthProvider) (bool, error) {
	for selectedIndex := -1; selectedIndex < len(profiles); selectedIndex++ {
		catalog := make([]ProviderDescriptor, len(profiles))
		for index := range profiles {
			catalog[index] = cloneProviderDescriptor(profiles[index].descriptor)
			catalog[index].Selected = index == selectedIndex
		}
		encoded, err := json.Marshal(catalog)
		if err != nil {
			return false, err
		}
		reflected, err := credentials.JSONContains(encoded)
		if err != nil || reflected {
			return reflected, err
		}
	}
	return false, nil
}

func validateReasoningCapabilities(raw authFileReasoning) (ReasoningCapabilities, error) {
	seen := make(map[string]struct{}, len(raw.Efforts))
	efforts := make([]string, 0, len(raw.Efforts))
	for _, effort := range raw.Efforts {
		if !validReasoningEffort(effort) {
			return ReasoningCapabilities{}, invalidAuthFile("auth file reasoning capabilities contain an unsupported effort")
		}
		if _, duplicate := seen[effort]; duplicate {
			return ReasoningCapabilities{}, invalidAuthFile("auth file reasoning capabilities contain a duplicate effort")
		}
		seen[effort] = struct{}{}
		efforts = append(efforts, effort)
	}
	if _, ok := seen[raw.DefaultEffort]; !ok {
		return ReasoningCapabilities{}, invalidAuthFile("auth file reasoning default_effort must be present in efforts")
	}
	return ReasoningCapabilities{Efforts: efforts, DefaultEffort: raw.DefaultEffort}, nil
}

func selectAuthProvider(profiles []normalizedAuthProvider, explicit string) (int, error) {
	if explicit != "" {
		for index := range profiles {
			if profiles[index].descriptor.ID == explicit {
				return index, nil
			}
		}
		return -1, fmt.Errorf("%w: --provider does not identify a provider in auth.json", ErrInvalid)
	}
	if len(profiles) == 1 {
		return 0, nil
	}
	for index := range profiles {
		if profiles[index].descriptor.Default {
			return index, nil
		}
	}
	return -1, fmt.Errorf("%w: auth.json defines multiple providers but none is default; add \"default\": true to exactly one object in \"providers\", or invoke AgentX with --provider <id>", ErrInvalid)
}

func cloneProviderDescriptor(source ProviderDescriptor) ProviderDescriptor {
	result := source
	result.Reasoning.Efforts = append([]string(nil), source.Reasoning.Efforts...)
	return result
}

func validReasoningEffort(effort string) bool {
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (a Azure) Validate() error {
	return protectConfigurationError(redact.New(a.APIKey), a.validate())
}

func (a Azure) validate() error {
	missing := make([]string, 0, 3)
	if a.Endpoint == nil {
		missing = append(missing, "AZURE_OPENAI_ENDPOINT")
	}
	if a.APIKey == "" {
		missing = append(missing, "AZURE_OPENAI_SUBSCRIPTION_KEY")
	}
	if a.Deployment == "" {
		missing = append(missing, "AZURE_OPENAI_DEPLOYMENT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrInvalid, strings.Join(missing, ", "))
	}
	if err := validateEndpoint(a.Endpoint, a.UnsafeAllowInsecureLoopbackForTesting); err != nil {
		return fmt.Errorf("%w: Azure OpenAI endpoint: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(a.ModelName) == "" {
		return fmt.Errorf("%w: model name is empty", ErrInvalid)
	}
	if strings.TrimSpace(a.Deployment) == "" {
		return fmt.Errorf("%w: deployment is empty", ErrInvalid)
	}
	if strings.TrimSpace(a.APIKey) == "" {
		return fmt.Errorf("%w: subscription key is empty", ErrInvalid)
	}
	if strings.TrimSpace(a.ModelName) != a.ModelName || strings.TrimSpace(a.Deployment) != a.Deployment || strings.TrimSpace(a.APIVersion) != a.APIVersion {
		return fmt.Errorf("%w: provider routing contains unsupported surrounding whitespace", ErrInvalid)
	}
	if !safeConfiguredText(a.ModelName, 256) {
		return fmt.Errorf("%w: model name contains unsupported characters or exceeds its limit", ErrInvalid)
	}
	if !safeConfiguredText(a.Deployment, 256) {
		return fmt.Errorf("%w: deployment contains unsupported characters or exceeds its limit", ErrInvalid)
	}
	if !safeConfiguredText(a.APIKey, 16<<10) {
		return fmt.Errorf("%w: subscription key contains unsupported characters or exceeds its limit", ErrInvalid)
	}
	// net/http trims leading and trailing SP/HTAB before serializing header
	// values. Reject a key that would change on the wire so the exact
	// credential frozen into every sanitizer is also the value a provider can
	// observe and reflect.
	if textproto.TrimString(a.APIKey) != a.APIKey {
		return fmt.Errorf("%w: subscription key contains unsupported surrounding whitespace", ErrInvalid)
	}
	if strings.IndexFunc(a.APIKey, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: subscription key contains unsupported whitespace", ErrInvalid)
	}
	if a.APIVersion != "" && !safeConfiguredText(a.APIVersion, 128) {
		return fmt.Errorf("%w: API version contains unsupported characters or exceeds its limit", ErrInvalid)
	}
	if !validReasoningEffort(a.ReasoningEffort) {
		return fmt.Errorf("%w: unsupported reasoning effort", ErrInvalid)
	}
	if len(a.SupportedReasoningEfforts) > 0 {
		seen := make(map[string]struct{}, len(a.SupportedReasoningEfforts))
		for _, effort := range a.SupportedReasoningEfforts {
			if !validReasoningEffort(effort) {
				return fmt.Errorf("%w: unsupported reasoning capability", ErrInvalid)
			}
			if _, duplicate := seen[effort]; duplicate {
				return fmt.Errorf("%w: duplicate reasoning capability", ErrInvalid)
			}
			seen[effort] = struct{}{}
		}
		if _, supported := seen[a.ReasoningEffort]; !supported {
			return fmt.Errorf("%w: reasoning effort %q is not supported by the selected provider", ErrInvalid, a.ReasoningEffort)
		}
	}
	if a.RequestTimeout <= 0 || a.StreamWatchdog <= 0 || a.MaxRetries < 0 {
		return fmt.Errorf("%w: timeouts must be positive and retries non-negative", ErrInvalid)
	}
	return nil
}

func safeConfiguredText(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
	}) < 0
}

func normalizeEndpoint(raw, apiVersion string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("must be an absolute HTTPS URL")
	}
	if err := validateEndpoint(u, false); err != nil {
		return nil, err
	}
	u.RawQuery = ""
	u.Fragment = ""
	cleaned := strings.TrimSuffix(u.Path, "/")
	for _, suffix := range []string{"/openai/v1/responses", "/openai/responses", "/openai/v1", "/openai"} {
		if strings.HasSuffix(cleaned, suffix) {
			cleaned = strings.TrimSuffix(cleaned, suffix)
			break
		}
	}
	route := "openai/responses"
	if apiVersion == "" || apiVersion == "v1" || apiVersion == "preview" {
		route = "openai/v1/responses"
	}
	cleaned = path.Join(cleaned, route)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	u.Path = cleaned
	return u, nil
}

// validateEndpoint is shared by auth-file normalization and the exported Azure
// configuration constructor boundary. Callers constructing config.Azure
// directly must not be able to bypass the same credential-egress constraints
// enforced by Load.
func validateEndpoint(u *url.URL, allowInsecureLoopbackForTesting bool) error {
	if u == nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return errors.New("must be an absolute HTTPS URL")
	}
	host := u.Hostname()
	loopback := host == "localhost"
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		loopback = true
	}
	if u.Scheme != "https" && !(allowInsecureLoopbackForTesting && u.Scheme == "http" && loopback) {
		return errors.New("must use HTTPS")
	}
	if u.User != nil {
		return errors.New("must not contain URL user information")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not contain a query string or fragment")
	}
	return nil
}

// String deliberately omits credentials.
func (a Azure) String() string {
	endpoint := "<unset>"
	if a.Endpoint != nil {
		endpoint = "<configured>"
	}
	return a.Redact(fmt.Sprintf(
		"AzureOpenAI{endpoint=%s model=<configured> deployment=<configured> api_version=<configured> reasoning_effort=<configured>}",
		endpoint,
	))
}

func (a Azure) GoString() string { return a.String() }

// MarshalJSON projects configuration without exposing the API key, including
// when another configured identity equals the key. Pathological credentials
// that collide with JSON framing produce a minimal safe projection.
func (a Azure) MarshalJSON() ([]byte, error) {
	credentials := redact.New(a.APIKey)
	projected := a
	projected.APIKey = ""
	projected.ModelName = credentials.Apply(projected.ModelName)
	projected.Deployment = credentials.Apply(projected.Deployment)
	projected.APIVersion = credentials.Apply(projected.APIVersion)
	projected.ReasoningEffort = credentials.Apply(projected.ReasoningEffort)
	if projected.Endpoint != nil {
		endpoint := *projected.Endpoint
		endpoint.Scheme = credentials.Apply(endpoint.Scheme)
		endpoint.Opaque = credentials.Apply(endpoint.Opaque)
		endpoint.Host = credentials.Apply(endpoint.Host)
		endpoint.Path = credentials.Apply(endpoint.Path)
		endpoint.RawPath = credentials.Apply(endpoint.RawPath)
		endpoint.RawQuery = credentials.Apply(endpoint.RawQuery)
		endpoint.Fragment = credentials.Apply(endpoint.Fragment)
		endpoint.RawFragment = credentials.Apply(endpoint.RawFragment)
		endpoint.User = nil
		projected.Endpoint = &endpoint
	}
	type wireAzure Azure
	encoded, err := json.Marshal(wireAzure(projected))
	if err != nil {
		return nil, errors.New("configuration projection failed")
	}
	return credentialSafeJSON(encoded, credentials), nil
}

func (r Runtime) String() string {
	authFile := "<unset>"
	if r.AuthFile != "" {
		authFile = "<configured>"
	}
	return r.Redact(fmt.Sprintf("Runtime{%s provider=<configured> auth_file=%s}", r.Azure.String(), authFile))
}
func (r Runtime) GoString() string { return r.String() }

// MarshalJSON applies the complete Azure credential guard after outer Runtime
// framing, where otherwise a key name or provenance value could recreate it.
func (r Runtime) MarshalJSON() ([]byte, error) {
	type wireRuntime Runtime
	encoded, err := json.Marshal(wireRuntime(r))
	if err != nil {
		return nil, errors.New("runtime configuration projection failed")
	}
	return credentialSafeJSON(encoded, r.CredentialSanitizer()), nil
}

func credentialSafeJSON(encoded []byte, credentials *redact.Set) []byte {
	if credentials == nil || !credentials.Contains(string(encoded)) {
		return encoded
	}
	for _, fallback := range [][]byte{[]byte("null"), []byte("{}"), []byte("[]"), []byte("0"), []byte(`""`)} {
		if !credentials.Contains(string(fallback)) {
			return fallback
		}
	}
	// The fixed candidates have no byte common to every member, so one must be
	// safe for any nonempty exact literal. Retain a defensive final fallback.
	return []byte{}
}

// Redact replaces the configured credential wherever an operational error may
// have echoed it. It is a last line of defense; callers should avoid placing a
// secret in an error in the first place.
func (a Azure) Redact(value string) string {
	return redact.Literal(value, a.APIKey)
}

// CredentialSanitizer returns the immutable union of every provider key in the
// parsed auth registry. Callers must compose this whole set with extension
// credentials before opening shared output or persistence sinks.
func (r Runtime) CredentialSanitizer() *redact.Set {
	return redact.Union(r.credentials, redact.New(r.Azure.APIKey))
}

// Redact applies the complete configured provider credential set.
func (r Runtime) Redact(value string) string { return r.CredentialSanitizer().Apply(value) }

// ProviderBinding identifies the selected endpoint routing tuple without
// exposing it. API keys are intentionally excluded so key rotation is safe.
func (r Runtime) ProviderBinding() string { return r.providerBinding }
