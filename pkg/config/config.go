// Package config loads, validates, and normalizes process configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"path"
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
  "version": 1,
  "provider": "azure_openai",
  "azure_openai": {
    "endpoint": "https://your-resource.openai.azure.com",
    "model": "gpt-5.6-sol",
    "deployment": "gpt-5.6-sol",
    "api_key": "replace-with-your-secret",
    "api_version": "preview"
  }
}`
)

var (
	ErrInvalid         = errors.New("invalid configuration")
	ErrAuthFileMissing = errors.New("auth file is missing")
)

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
	RequestTimeout  time.Duration
	StreamWatchdog  time.Duration
	MaxRetries      int

	// UnsafeAllowInsecureLoopbackForTesting is an explicit transport test
	// seam. Application configuration loading never sets it.
	UnsafeAllowInsecureLoopbackForTesting bool `json:"-"`
}

type Runtime struct {
	Azure      Azure
	AuthFile   string `json:"-"`
	Provenance map[string]Source
}

type Overrides struct {
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
	processEffort, processEffortSet, err := reasoningEffortFromEnvironment(environ)
	if err != nil {
		return Runtime{}, err
	}

	provenance := make(map[string]Source)
	for _, key := range []string{
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_MODEL_NAME", "AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "AZURE_OPENAI_API_VERSION",
	} {
		provenance[key] = SourceFile
	}

	configuredModel := strings.TrimSpace(auth.AzureOpenAI.Model)
	provenance["model"] = SourceFile
	model := configuredModel
	if overrides.Model != "" {
		if strings.TrimSpace(overrides.Model) != configuredModel {
			return Runtime{}, fmt.Errorf("%w: --model is not the deployment-backed model configured by auth.json", ErrInvalid)
		}
		model = overrides.Model
		provenance["model"] = SourceFlag
	}
	effort := DefaultReasoningEffort
	if v := strings.TrimSpace(processEffort); processEffortSet && v != "" {
		effort = v
		provenance["reasoning_effort"] = SourceProcess
	} else {
		provenance["reasoning_effort"] = SourceDefault
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

	apiVersion := strings.TrimSpace(auth.AzureOpenAI.APIVersion)
	if apiVersion == "" {
		// Keep Azure's v1 selector implicit. The OpenAI-compatible endpoint
		// documents v1 as the default, and omitting the query is required by
		// some resource generations. Explicit versions remain source-attributed.
		apiVersion = DefaultAzureAPIVersion
		provenance["AZURE_OPENAI_API_VERSION"] = SourceDefault
	}
	rawEndpoint := strings.TrimSpace(auth.AzureOpenAI.Endpoint)
	endpoint, err := normalizeEndpoint(rawEndpoint, apiVersion)
	if err != nil {
		return Runtime{}, fmt.Errorf("%w: Azure OpenAI endpoint: %v", ErrInvalid, err)
	}
	azure := Azure{
		Endpoint:        endpoint,
		ModelName:       strings.TrimSpace(model),
		Deployment:      strings.TrimSpace(auth.AzureOpenAI.Deployment),
		APIKey:          auth.AzureOpenAI.APIKey,
		APIVersion:      apiVersion,
		ReasoningEffort: strings.TrimSpace(effort),
		RequestTimeout:  requestTimeout,
		StreamWatchdog:  watchdog,
		MaxRetries:      maxRetries,
	}
	if err := azure.Validate(); err != nil {
		return Runtime{}, err
	}
	return Runtime{Azure: azure, AuthFile: location.path, Provenance: provenance}, nil
}

func (a Azure) Validate() error {
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
	switch a.ReasoningEffort {
	case "none", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("%w: unsupported reasoning effort", ErrInvalid)
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
	return r.Azure.Redact(fmt.Sprintf("Runtime{%s auth_file=%s}", r.Azure.String(), authFile))
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
	return credentialSafeJSON(encoded, redact.New(r.Azure.APIKey)), nil
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
