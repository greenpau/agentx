package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testAuthJSON = `{
  "version": 1,
  "provider": "azure_openai",
  "azure_openai": {
    "endpoint": "https://example.openai.azure.com/",
    "model": "gpt-5.6-sol",
    "deployment": "file-deployment",
    "api_key": "synthetic-file-key",
    "api_version": "2024-12-01-preview"
  }
}`

func writeTestAuthFile(t *testing.T, content string) string {
	t.Helper()
	if !credentialFileAccessControlVerified {
		t.Skip("platform cannot verify owner-only credential-file access")
	}
	path := filepath.Join(t.TempDir(), DefaultAuthFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthFilePlaceholderIsTheSupportedSchema(t *testing.T) {
	document, err := parseAuthFile([]byte(AuthFilePlaceholder))
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || document.Provider != "azure_openai" {
		t.Fatalf("placeholder identity = %#v", document)
	}
	if document.AzureOpenAI.Endpoint != "https://your-resource.openai.azure.com" ||
		document.AzureOpenAI.Model != DefaultModel ||
		document.AzureOpenAI.Deployment != DefaultModel ||
		document.AzureOpenAI.APIKey != "replace-with-your-secret" ||
		document.AzureOpenAI.APIVersion != "preview" {
		t.Fatalf("placeholder Azure shape = %#v", document.AzureOpenAI)
	}
}

func TestParseAuthFileRejectsUnsupportedAndAmbiguousJSON(t *testing.T) {
	const fieldMarker = "credential-bearing-field-marker"
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "top-level duplicate",
			input:    `{"version":1,"version":1,"provider":"azure_openai","azure_openai":{"endpoint":"https://example.test","model":"gpt-5.6-sol","deployment":"gpt-5.6-sol","api_key":"key","api_version":"preview"}}`,
			contains: "duplicate",
		},
		{
			name:     "escaped duplicate",
			input:    `{"version":1,"provider":"azure_openai","azure_openai":{"endpoint":"https://example.test","model":"gpt-5.6-sol","deployment":"gpt-5.6-sol","api_key":"key","\u0061pi_key":"other","api_version":"preview"}}`,
			contains: "duplicate",
		},
		{
			name:     "unknown top-level member",
			input:    `{"version":1,"provider":"azure_openai","azure_openai":{"endpoint":"https://example.test","model":"gpt-5.6-sol","deployment":"gpt-5.6-sol","api_key":"key","api_version":"preview"},"` + fieldMarker + `":"secret"}`,
			contains: "unsupported object member",
		},
		{
			name:     "unknown nested member",
			input:    `{"version":1,"provider":"azure_openai","azure_openai":{"endpoint":"https://example.test","model":"gpt-5.6-sol","deployment":"gpt-5.6-sol","api_key":"key","api_version":"preview","` + fieldMarker + `":"secret"}}`,
			contains: "unsupported object member",
		},
		{
			name:     "trailing object",
			input:    testAuthJSON + `{}`,
			contains: "trailing",
		},
		{
			name:     "wrong version",
			input:    strings.Replace(testAuthJSON, `"version": 1`, `"version": 2`, 1),
			contains: "unsupported schema version",
		},
		{
			name:     "fractional version",
			input:    strings.Replace(testAuthJSON, `"version": 1`, `"version": 1.0`, 1),
			contains: "version must be an integer",
		},
		{
			name:     "wrong provider",
			input:    strings.Replace(testAuthJSON, `"provider": "azure_openai"`, `"provider": "openai"`, 1),
			contains: "unsupported provider",
		},
		{
			name:     "missing provider",
			input:    strings.Replace(testAuthJSON, `  "provider": "azure_openai",`+"\n", "", 1),
			contains: "missing a required object member",
		},
		{
			name:     "missing Azure field",
			input:    strings.Replace(testAuthJSON, `    "model": "gpt-5.6-sol",`+"\n", "", 1),
			contains: "missing a required Azure OpenAI field",
		},
		{
			name:     "wrong Azure object type",
			input:    `{"version":1,"provider":"azure_openai","azure_openai":[]}`,
			contains: "must be an object",
		},
		{
			name:     "wrong field type",
			input:    strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, `"api_key": 42`, 1),
			contains: "fields must be strings",
		},
		{
			name:     "nullable optional-value field",
			input:    strings.Replace(testAuthJSON, `"api_version": "2024-12-01-preview"`, `"api_version": null`, 1),
			contains: "fields must be strings",
		},
		{
			name:     "empty required field",
			input:    strings.Replace(testAuthJSON, `"deployment": "file-deployment"`, `"deployment": "  "`, 1),
			contains: "empty required",
		},
		{
			name:     "top-level array",
			input:    `[]`,
			contains: "one JSON object",
		},
		{
			name:     "malformed",
			input:    `{"version":`,
			contains: "malformed JSON",
		},
		{
			name:     "unpaired surrogate",
			input:    strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, `"api_key": "\ud800"`, 1),
			contains: "fields must be strings",
		},
		{
			name:     "unpaired low surrogate",
			input:    strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, `"api_key": "\udc00"`, 1),
			contains: "fields must be strings",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := parseAuthFile([]byte(test.input))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("document=%#v err=%v", document, err)
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error %q does not contain %q", err, test.contains)
			}
			if strings.Contains(err.Error(), fieldMarker) || strings.Contains(err.Error(), "synthetic-file-key") {
				t.Fatalf("auth diagnostic exposed file content: %q", err)
			}
		})
	}
}

func TestParseAuthFileAcceptsReplacementCharacterAndValidSurrogatePair(t *testing.T) {
	for _, replacement := range []string{
		`"api_key": "synthetic-\ufffd-key"`,
		`"api_key": "synthetic-�-key"`,
		`"api_key": "synthetic-\ud83d\ude00-key"`,
	} {
		input := strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, replacement, 1)
		document, err := parseAuthFile([]byte(input))
		if err != nil {
			t.Fatalf("valid JSON string %s was rejected: %v", replacement, err)
		}
		if document.AzureOpenAI.APIKey == "" {
			t.Fatalf("valid JSON string %s decoded empty", replacement)
		}
	}
}

func TestParseAuthFileRejectsInvalidUTF8AndOversizedInput(t *testing.T) {
	invalidUTF8 := append([]byte(testAuthJSON), 0xff)
	if _, err := parseAuthFile(invalidUTF8); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	oversized := make([]byte, maxCredentialFileBytes+1)
	for index := range oversized {
		oversized[index] = ' '
	}
	if _, err := parseAuthFile(oversized); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized input error = %v", err)
	}
}

func TestRequireAuthFileChecksPresenceWithoutReading(t *testing.T) {
	const pathMarker = "credential-bearing-path-marker"
	missing := filepath.Join(t.TempDir(), pathMarker, DefaultAuthFile)
	err := RequireAuthFile(missing)
	if !errors.Is(err, ErrAuthFileMissing) {
		t.Fatalf("missing auth file error = %v", err)
	}
	for _, required := range []string{DefaultAuthFile, UserGuideURL, AuthFilePlaceholder} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("missing auth diagnostic does not contain %q: %v", required, err)
		}
	}
	if !strings.Contains(err.Error(), pathMarker) {
		t.Fatalf("missing auth diagnostic omitted the effective path: %v", err)
	}

	dir := t.TempDir()
	invalid := filepath.Join(dir, DefaultAuthFile)
	if err := os.WriteFile(invalid, []byte(`not JSON and not read by this gate`), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := RequireAuthFile(invalid); err != nil {
		t.Fatalf("regular-file presence gate parsed or read the file: %v", err)
	}

	link := filepath.Join(dir, "auth-link")
	if err := os.Symlink(invalid, link); err == nil {
		if err := RequireAuthFile(link); err == nil || !strings.Contains(err.Error(), "non-symlink") ||
			!strings.Contains(err.Error(), UserGuideURL) || !strings.Contains(err.Error(), AuthFilePlaceholder) {
			t.Fatalf("symlink presence gate error = %v", err)
		}
	}
	if err := RequireAuthFile(dir); err == nil || !strings.Contains(err.Error(), UserGuideURL) ||
		!strings.Contains(err.Error(), AuthFilePlaceholder) {
		t.Fatalf("directory presence gate error = %v", err)
	}
}

func TestLoadAuthFileNormalizationProvenanceAndRedaction(t *testing.T) {
	path := writeTestAuthFile(t, testAuthJSON)
	configuration, err := Load(path, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := configuration.Azure.Endpoint.String(); got != "https://example.openai.azure.com/openai/responses" {
		t.Fatalf("endpoint = %q", got)
	}
	if configuration.Azure.ModelName != DefaultModel ||
		configuration.Azure.Deployment != "file-deployment" ||
		configuration.Azure.APIKey != "synthetic-file-key" ||
		configuration.AuthFile != path {
		t.Fatalf("configuration = %#v", configuration)
	}
	for _, key := range []string{
		"AZURE_OPENAI_ENDPOINT",
		"AZURE_OPENAI_MODEL_NAME",
		"AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY",
		"AZURE_OPENAI_API_VERSION",
		"model",
	} {
		if configuration.Provenance[key] != SourceFile {
			t.Fatalf("%s provenance = %q", key, configuration.Provenance[key])
		}
	}
	if strings.Contains(configuration.Azure.String(), "synthetic-file-key") ||
		strings.Contains(fmt.Sprintf("%#v", configuration.Azure), "synthetic-file-key") ||
		strings.Contains(fmt.Sprintf("%#v", configuration), "synthetic-file-key") ||
		strings.Contains(configuration.Azure.Redact("x synthetic-file-key y"), "synthetic-file-key") {
		t.Fatal("secret was not redacted")
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-file-key") || strings.Contains(string(encoded), path) {
		t.Fatal("JSON serialization exposed a credential or auth-file path")
	}
}

func TestLoadAlwaysRequiresAuthFileAndIgnoresAzureProcessCredentials(t *testing.T) {
	process := []string{
		"AZURE_OPENAI_ENDPOINT=https://process-attacker.test",
		"AZURE_OPENAI_MODEL_NAME=process-model",
		"AZURE_OPENAI_DEPLOYMENT=process-deployment",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=synthetic-process-key",
		"AZURE_OPENAI_API_VERSION=process-version",
		"AGENTX_REASONING_EFFORT=low",
	}
	missing := filepath.Join(t.TempDir(), "missing-auth.json")
	if configuration, err := Load(missing, process, Overrides{}); !errors.Is(err, ErrAuthFileMissing) {
		t.Fatalf("process credentials bypassed missing auth file: %#v, %v", configuration, err)
	}

	path := writeTestAuthFile(t, testAuthJSON)
	configuration, err := Load(path, process, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.Endpoint.Host != "example.openai.azure.com" ||
		configuration.Azure.ModelName != DefaultModel ||
		configuration.Azure.Deployment != "file-deployment" ||
		configuration.Azure.APIKey != "synthetic-file-key" ||
		configuration.Azure.APIVersion != "2024-12-01-preview" {
		t.Fatalf("process Azure values overrode auth file: %#v", configuration.Azure)
	}
	if configuration.Azure.ReasoningEffort != "low" ||
		configuration.Provenance["reasoning_effort"] != SourceProcess {
		t.Fatalf("reasoning effort = %q/%q", configuration.Azure.ReasoningEffort, configuration.Provenance["reasoning_effort"])
	}
	for _, key := range []string{
		"AZURE_OPENAI_ENDPOINT",
		"AZURE_OPENAI_MODEL_NAME",
		"AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY",
		"AZURE_OPENAI_API_VERSION",
	} {
		if configuration.Provenance[key] != SourceFile {
			t.Fatalf("%s process provenance was retained: %q", key, configuration.Provenance[key])
		}
	}
}

func TestLoadNeverFallsBackToValidLegacyDotenv(t *testing.T) {
	workspace := t.TempDir()
	legacy := filepath.Join(workspace, ".env.production")
	contents := strings.Join([]string{
		"AZURE_OPENAI_ENDPOINT=https://legacy.example.test",
		"AZURE_OPENAI_MODEL_NAME=gpt-5.6-sol",
		"AZURE_OPENAI_DEPLOYMENT=legacy-deployment",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=legacy-secret",
		"AZURE_OPENAI_API_VERSION=preview",
	}, "\n") + "\n"
	if err := os.WriteFile(legacy, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(workspace, DefaultAuthFile)
	if configuration, err := Load(authPath, nil, Overrides{}); !errors.Is(err, ErrAuthFileMissing) {
		t.Fatalf("legacy dotenv bypassed missing auth.json: %#v, %v", configuration, err)
	}
	if err := os.WriteFile(authPath, []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if configuration, err := Load(authPath, nil, Overrides{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("legacy dotenv bypassed invalid auth.json: %#v, %v", configuration, err)
	}
}

func TestLoadAtRootCannotBeRedirectedByApplicationHomeReplacement(t *testing.T) {
	if !credentialFileAccessControlVerified {
		t.Skip("platform cannot verify owner-only credential-file access")
	}
	parent := t.TempDir()
	selectedHome := filepath.Join(parent, "agentx-home")
	if err := os.Mkdir(selectedHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selectedHome, DefaultAuthFile), []byte(testAuthJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(selectedHome)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	displacedHome := filepath.Join(parent, "displaced-home")
	if err := os.Rename(selectedHome, displacedHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(selectedHome, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Replace(testAuthJSON, "synthetic-file-key", "replacement-file-key", 1)
	replacement = strings.Replace(replacement, "file-deployment", "replacement-deployment", 1)
	if err := os.WriteFile(filepath.Join(selectedHome, DefaultAuthFile), []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	configuration, err := LoadAtRoot(
		root,
		filepath.Join(selectedHome, DefaultAuthFile),
		nil,
		Overrides{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.APIKey != "synthetic-file-key" ||
		configuration.Azure.Deployment != "file-deployment" {
		t.Fatalf("descriptor-rooted load used replacement credentials: %#v", configuration.Azure)
	}
}

func TestReasoningEffortEnvironmentAndOverridePrecedence(t *testing.T) {
	path := writeTestAuthFile(t, testAuthJSON)
	configuration, err := Load(path, []string{"agentx_reasoning_effort=medium"}, Overrides{ReasoningEffort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.ReasoningEffort != "xhigh" ||
		configuration.Provenance["reasoning_effort"] != SourceFlag {
		t.Fatalf("override effort = %q/%q", configuration.Azure.ReasoningEffort, configuration.Provenance["reasoning_effort"])
	}

	_, err = Load(path, []string{
		"AGENTX_REASONING_EFFORT=low",
		"agentx_reasoning_effort=high",
	}, Overrides{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate reasoning effort error = %v", err)
	}
}

func TestProcessEnvironmentInputIsBounded(t *testing.T) {
	process := make([]string, maxEnvironmentEntryCount+1)
	for index := range process {
		process[index] = fmt.Sprintf("SAFE_%04d=value", index)
	}
	if value, present, err := reasoningEffortFromEnvironment(process); !errors.Is(err, ErrInvalid) || value != "" || present {
		t.Fatalf("oversized process environment = %q, %t, %v", value, present, err)
	}
}

func TestLoadRejectsUnconfiguredModelOverride(t *testing.T) {
	path := writeTestAuthFile(t, testAuthJSON)
	if _, err := Load(path, nil, Overrides{Model: "different-model"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("model override error = %v", err)
	}
	configuration, err := Load(path, nil, Overrides{Model: DefaultModel})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Provenance["model"] != SourceFlag {
		t.Fatalf("model provenance = %q", configuration.Provenance["model"])
	}
}

func TestLoadLeavesAzureV1VersionImplicit(t *testing.T) {
	content := strings.Replace(testAuthJSON, `"api_version": "2024-12-01-preview"`, `"api_version": ""`, 1)
	path := writeTestAuthFile(t, content)
	configuration, err := Load(path, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.APIVersion != DefaultAzureAPIVersion {
		t.Fatalf("API version = %q, want %q", configuration.Azure.APIVersion, DefaultAzureAPIVersion)
	}
	if configuration.Provenance["AZURE_OPENAI_API_VERSION"] != SourceDefault {
		t.Fatalf("API version provenance = %q", configuration.Provenance["AZURE_OPENAI_API_VERSION"])
	}
}

func TestLoadPreservesCredentialWhitespaceForValidation(t *testing.T) {
	content := strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, `"api_key": " synthetic-file-key "`, 1)
	path := writeTestAuthFile(t, content)
	if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("credential whitespace was normalized before validation: %v", err)
	}
}

func TestCredentialFileRejectsSymlinkLooseModeAndHardlink(t *testing.T) {
	if !credentialFileAccessControlVerified {
		t.Skip("platform cannot verify owner-only credential-file access")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, DefaultAuthFile)
	if err := os.WriteFile(target, []byte(testAuthJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "auth-link")
	if err := os.Symlink(target, link); err == nil {
		if _, err := Load(link, nil, Overrides{}); err == nil {
			t.Fatal("auth-file symlink was accepted")
		}
	}

	loose := filepath.Join(dir, "loose-auth.json")
	if err := os.WriteFile(loose, []byte(testAuthJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(loose, nil, Overrides{}); err == nil {
		t.Fatal("loosely permissioned credential file was accepted")
	}

	hardlink := filepath.Join(dir, "auth-hardlink")
	if err := os.Link(target, hardlink); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := Load(target, nil, Overrides{}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("hardlinked auth-file error = %v", err)
	}
}

func TestCredentialFileDiagnosticsNameAndSafelyQuoteSelectedPath(t *testing.T) {
	const marker = "credential-bearing-path-marker"
	path := filepath.Join(t.TempDir(), marker+"\nnext-line")
	_, err := Load(path, nil, Overrides{})
	if !errors.Is(err, ErrAuthFileMissing) {
		t.Fatalf("missing auth file error = %v", err)
	}
	if !strings.Contains(err.Error(), marker) {
		t.Fatalf("auth-file diagnostic omitted the selected path: %q", err)
	}
	if strings.Contains(err.Error(), marker+"\nnext-line") || !strings.Contains(err.Error(), `\nnext-line`) {
		t.Fatalf("auth-file diagnostic did not safely quote the selected path: %q", err)
	}
}

func TestCredentialFileRejectsOversizedContentBeforeParsing(t *testing.T) {
	if !credentialFileAccessControlVerified {
		t.Skip("platform cannot verify owner-only credential-file access")
	}
	path := filepath.Join(t.TempDir(), DefaultAuthFile)
	content := make([]byte, maxCredentialFileBytes+1)
	for index := range content {
		content[index] = ' '
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil, Overrides{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized auth-file error = %v", err)
	}
}

func TestEndpointRejectsInsecureRemote(t *testing.T) {
	if _, err := normalizeEndpoint("http://example.test", "v1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEndpointRejectsUserInfoQueryAndFragment(t *testing.T) {
	for _, endpoint := range []string{
		"https://user:password@example.test",
		"https://example.test?key=value",
		"https://example.test/#fragment",
	} {
		if _, err := normalizeEndpoint(endpoint, "v1"); err == nil {
			t.Errorf("normalizeEndpoint(%q) succeeded", endpoint)
		}
	}
}

func TestEndpointRouteTracksAzureAPIVersionFamily(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{version: "", want: "https://example.test/openai/v1/responses"},
		{version: "v1", want: "https://example.test/openai/v1/responses"},
		{version: "preview", want: "https://example.test/openai/v1/responses"},
		{version: "2025-03-01-preview", want: "https://example.test/openai/responses"},
	}
	for _, test := range tests {
		endpoint, err := normalizeEndpoint("https://example.test/openai/v1/responses", test.version)
		if err != nil {
			t.Fatal(err)
		}
		if endpoint.String() != test.want {
			t.Errorf("version %q endpoint = %q, want %q", test.version, endpoint, test.want)
		}
	}
}

func TestAzureRejectsControlCharactersAndOversizedIdentityFields(t *testing.T) {
	endpoint, err := normalizeEndpoint("https://example.test", "v1")
	if err != nil {
		t.Fatal(err)
	}
	base := Azure{
		Endpoint: endpoint, ModelName: DefaultModel, Deployment: DefaultModel, APIKey: "synthetic-key",
		APIVersion: "2026-07-01-preview", ReasoningEffort: "high", RequestTimeout: time.Second,
		StreamWatchdog: time.Second, MaxRetries: 1,
	}
	tests := []struct {
		name   string
		mutate func(*Azure)
	}{
		{"model control", func(value *Azure) { value.ModelName = "gpt\x1b]52;c;payload\a" }},
		{"deployment newline", func(value *Azure) { value.Deployment = "deployment\nheader" }},
		{"credential newline", func(value *Azure) { value.APIKey = "key\r\ninjected: value" }},
		{"credential leading space", func(value *Azure) { value.APIKey = " synthetic-key" }},
		{"credential trailing space", func(value *Azure) { value.APIKey = "synthetic-key " }},
		{"credential internal unicode whitespace", func(value *Azure) { value.APIKey = "synthetic\u00a0key" }},
		{"credential format character", func(value *Azure) { value.APIKey = "synthetic\u200bkey" }},
		{"credential invalid UTF-8", func(value *Azure) { value.APIKey = string([]byte{'k', 0xff, 'y'}) }},
		{"API version control", func(value *Azure) { value.APIVersion = "preview\u0085next" }},
		{"model oversized", func(value *Azure) { value.ModelName = strings.Repeat("m", 257) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unsafe Azure configuration accepted: %v", err)
			}
		})
	}
}

func TestAzureValidateCannotBypassEndpointCredentialEgressChecks(t *testing.T) {
	valid, err := normalizeEndpoint("https://example.test", "v1")
	if err != nil {
		t.Fatal(err)
	}
	base := Azure{
		Endpoint: valid, ModelName: DefaultModel, Deployment: DefaultModel, APIKey: "synthetic-key",
		ReasoningEffort: "high", RequestTimeout: time.Second, StreamWatchdog: time.Second, MaxRetries: 1,
	}
	for _, raw := range []string{
		"http://example.test/openai/v1/responses",
		"http://[::1]/openai/v1/responses",
		"https://user:password@example.test/openai/v1/responses",
		"https://example.test/openai/v1/responses?redirect=attacker",
		"https://example.test/openai/v1/responses#fragment",
	} {
		endpoint, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		candidate := base
		candidate.Endpoint = endpoint
		if err := candidate.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe direct endpoint %q was accepted: %v", raw, err)
		}
	}
	loopback, err := url.Parse("http://[::1]/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	base.Endpoint = loopback
	base.UnsafeAllowInsecureLoopbackForTesting = true
	if err := base.Validate(); err != nil {
		t.Fatalf("loopback test endpoint was rejected: %v", err)
	}
}

func TestConfigurationDiagnosticsOmitConfiguredIdentityAndPath(t *testing.T) {
	endpoint, err := normalizeEndpoint("https://example.test", "v1")
	if err != nil {
		t.Fatal(err)
	}
	const (
		credential = "opaque-configuration-credential"
		identity   = "opaque-deployment-identity"
		authPath   = "opaque-auth-path"
	)
	runtime := Runtime{
		Azure: Azure{
			Endpoint: endpoint, ModelName: identity, Deployment: identity, APIKey: credential,
			APIVersion: "preview", ReasoningEffort: "high", RequestTimeout: time.Second,
			StreamWatchdog: time.Second, MaxRetries: 1,
		},
		AuthFile: authPath,
	}
	for _, rendered := range []string{runtime.String(), runtime.GoString(), fmt.Sprintf("%#v", runtime)} {
		if strings.Contains(rendered, credential) || strings.Contains(rendered, identity) ||
			strings.Contains(rendered, authPath) {
			t.Fatalf("configuration diagnostic exposed private identity: %q", rendered)
		}
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), credential) || strings.Contains(string(encoded), authPath) {
		t.Fatalf("configuration JSON exposed a credential path: %s", encoded)
	}
}

func TestConfigurationJSONClosesIdentityAndFramingAliases(t *testing.T) {
	endpoint, err := normalizeEndpoint("https://example.test", "v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{
		"shared-identity",
		"Azure",
		"Endpoint",
		"configured",
		"Runtime",
		"auth_file",
		"{",
		"null",
		"0",
	} {
		t.Run(fmt.Sprintf("%x", credential), func(t *testing.T) {
			runtime := Runtime{
				Azure: Azure{
					Endpoint: endpoint, ModelName: credential, Deployment: credential, APIKey: credential,
					APIVersion: credential, ReasoningEffort: "high", RequestTimeout: time.Second,
					StreamWatchdog: time.Second, MaxRetries: 1,
				},
				AuthFile: credential,
			}
			for name, rendered := range map[string]string{
				"azure string": runtime.Azure.String(),
				"runtime":      runtime.String(),
			} {
				if strings.Contains(rendered, credential) {
					t.Fatalf("%s exposed credential %q in %q", name, credential, rendered)
				}
			}
			for name, value := range map[string]any{"azure": runtime.Azure, "runtime": runtime} {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatalf("%s projection: %v", name, err)
				}
				if strings.Contains(string(encoded), credential) {
					t.Fatalf("%s projection exposed credential %q in %q", name, credential, encoded)
				}
			}
		})
	}
}
