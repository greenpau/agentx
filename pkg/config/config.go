// Package config loads, validates, and normalizes process configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/redact"
)

const (
	DefaultEnvFile         = ".env.production"
	DefaultModel           = "gpt-5.6-sol"
	DefaultAzureAPIVersion = ""
	DefaultReasoningEffort = "high"
	DefaultRequestTimeout  = 10 * time.Minute
	DefaultStreamWatchdog  = 90 * time.Second
	DefaultMaxRetries      = 10
)

var ErrInvalid = errors.New("invalid configuration")

type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "dotenv"
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
}

type Runtime struct {
	Azure      Azure
	EnvFile    string `json:"-"`
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
		pathname = DefaultEnvFile
	}
	processValues, processKeys, err := parseProcessEnvironment(environ)
	if err != nil {
		return Runtime{}, err
	}
	var values map[string]string
	if completeAzureProcessBundle(processValues) {
		// A complete process-owned bundle is an alternative credential source,
		// not an overlay. Do not require or inspect a dotenv file that will not
		// contribute to the runtime configuration.
		values = make(map[string]string, len(processValues))
		for key, value := range processValues {
			values[key] = value
		}
	} else {
		values, err = loadEnvFile(pathname, processValues)
		if err != nil {
			return Runtime{}, err
		}
	}
	provenance := make(map[string]Source)
	sourceFor := func(key string) Source {
		if processKeys[key] {
			return SourceProcess
		}
		if _, ok := values[key]; ok {
			return SourceFile
		}
		return SourceDefault
	}
	credentialKeys := []string{
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_MODEL_NAME", "AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "AZURE_OPENAI_API_VERSION",
	}
	credentialSource := Source("")
	for _, key := range credentialKeys {
		if strings.TrimSpace(values[key]) == "" {
			continue
		}
		source := sourceFor(key)
		provenance[key] = source
		if credentialSource == "" {
			credentialSource = source
			continue
		}
		if source != credentialSource {
			return Runtime{}, fmt.Errorf("%w: Azure endpoint, model, deployment, API version, and subscription key must come from one coherent source; process and dotenv values cannot be mixed", ErrInvalid)
		}
	}

	configuredModel := strings.TrimSpace(values["AZURE_OPENAI_MODEL_NAME"])
	if configuredModel == "" {
		configuredModel = DefaultModel
		provenance["model"] = SourceDefault
	} else {
		provenance["model"] = sourceFor("AZURE_OPENAI_MODEL_NAME")
	}
	model := configuredModel
	if overrides.Model != "" {
		if strings.TrimSpace(overrides.Model) != configuredModel {
			return Runtime{}, fmt.Errorf("%w: --model is not the deployment-backed model configured by AZURE_OPENAI_MODEL_NAME", ErrInvalid)
		}
		model = overrides.Model
		provenance["model"] = SourceFlag
	}
	effort := DefaultReasoningEffort
	if v := strings.TrimSpace(values["AGENTX_REASONING_EFFORT"]); v != "" {
		effort = v
		provenance["reasoning_effort"] = sourceFor("AGENTX_REASONING_EFFORT")
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

	apiVersion := strings.TrimSpace(values["AZURE_OPENAI_API_VERSION"])
	if apiVersion == "" {
		// Keep Azure's v1 selector implicit. The OpenAI-compatible endpoint
		// documents v1 as the default, and omitting the query is required by
		// some resource generations. Explicit versions remain source-attributed.
		apiVersion = DefaultAzureAPIVersion
		provenance["AZURE_OPENAI_API_VERSION"] = SourceDefault
	}
	rawEndpoint := strings.TrimSpace(values["AZURE_OPENAI_ENDPOINT"])
	endpoint, err := normalizeEndpoint(rawEndpoint, apiVersion)
	if err != nil {
		return Runtime{}, fmt.Errorf("%w: Azure OpenAI endpoint: %v", ErrInvalid, err)
	}
	azure := Azure{
		Endpoint:        endpoint,
		ModelName:       strings.TrimSpace(model),
		Deployment:      strings.TrimSpace(values["AZURE_OPENAI_DEPLOYMENT"]),
		APIKey:          values["AZURE_OPENAI_SUBSCRIPTION_KEY"],
		APIVersion:      apiVersion,
		ReasoningEffort: strings.TrimSpace(effort),
		RequestTimeout:  requestTimeout,
		StreamWatchdog:  watchdog,
		MaxRetries:      maxRetries,
	}
	if azure.Deployment == "" {
		azure.Deployment = azure.ModelName
	}
	if err := azure.Validate(); err != nil {
		return Runtime{}, err
	}
	return Runtime{Azure: azure, EnvFile: pathname, Provenance: provenance}, nil
}

func completeAzureProcessBundle(values map[string]string) bool {
	return strings.TrimSpace(values["AZURE_OPENAI_ENDPOINT"]) != "" &&
		strings.TrimSpace(values["AZURE_OPENAI_DEPLOYMENT"]) != "" &&
		strings.TrimSpace(values["AZURE_OPENAI_SUBSCRIPTION_KEY"]) != ""
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
	if err := validateEndpoint(a.Endpoint); err != nil {
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
	if err := validateEndpoint(u); err != nil {
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

// validateEndpoint is shared by dotenv normalization and the exported Azure
// configuration constructor boundary. Callers constructing config.Azure
// directly must not be able to bypass the same credential-egress constraints
// enforced by Load.
func validateEndpoint(u *url.URL) error {
	if u == nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return errors.New("must be an absolute HTTPS URL")
	}
	host := u.Hostname()
	loopback := host == "localhost"
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		loopback = true
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
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
	envFile := "<unset>"
	if r.EnvFile != "" {
		envFile = "<configured>"
	}
	return r.Azure.Redact(fmt.Sprintf("Runtime{%s env_file=%s}", r.Azure.String(), envFile))
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
