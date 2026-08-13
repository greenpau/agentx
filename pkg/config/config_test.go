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
  "version": 2,
  "providers": [
    {
      "id": "sol-5.6",
      "type": "azure_openai",
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://example.openai.azure.com/",
        "model": "gpt-5.6-sol",
        "deployment": "file-deployment",
        "api_key": "synthetic-file-key",
        "api_version": "2024-12-01-preview"
      }
    }
  ]
}`

type authProviderFixture struct {
	ID            string
	Default       *bool
	Endpoint      string
	Model         string
	Deployment    string
	APIKey        string
	APIVersion    string
	Efforts       []string
	DefaultEffort string
}

func providerFixture(id string) authProviderFixture {
	return authProviderFixture{
		ID: id, Endpoint: "https://" + id + ".example.test",
		Model: id + "-model", Deployment: id + "-deployment",
		APIKey: id + "-synthetic-key", APIVersion: "2026-07-01-preview",
		Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high",
	}
}

func boolFixture(value bool) *bool { return &value }

func authRegistryJSON(t *testing.T, providers ...authProviderFixture) string {
	t.Helper()
	entries := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		entry := map[string]any{
			"id":   provider.ID,
			"type": "azure_openai",
			"capabilities": map[string]any{
				"reasoning": map[string]any{
					"efforts": provider.Efforts, "default_effort": provider.DefaultEffort,
				},
			},
			"azure_openai": map[string]any{
				"endpoint": provider.Endpoint, "model": provider.Model,
				"deployment": provider.Deployment, "api_key": provider.APIKey,
				"api_version": provider.APIVersion,
			},
		}
		if provider.Default != nil {
			entry["default"] = *provider.Default
		}
		entries = append(entries, entry)
	}
	encoded, err := json.Marshal(map[string]any{"version": 2, "providers": entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

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

func assertCredentialSafeError(t *testing.T, err error, credentials ...string) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error category = %v, want ErrInvalid", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("credential-safe error retained an unwrap-visible cause: %T", errors.Unwrap(err))
	}
	renderings := map[string]string{
		"Error": err.Error(),
		"%s":    fmt.Sprintf("%s", err),
		"%v":    fmt.Sprintf("%v", err),
		"%+v":   fmt.Sprintf("%+v", err),
		"%#v":   fmt.Sprintf("%#v", err),
		"%q":    fmt.Sprintf("%q", err),
	}
	for format, rendered := range renderings {
		for _, credential := range credentials {
			if credential != "" && strings.Contains(rendered, credential) {
				t.Fatalf("%s rendering exposed credential %q: %q", format, credential, rendered)
			}
		}
	}
}

func TestAuthFilePlaceholderIsTheSupportedSchema(t *testing.T) {
	document, err := parseAuthFile([]byte(AuthFilePlaceholder))
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != authFileSchemaVersion || len(document.Providers) != 1 {
		t.Fatalf("placeholder identity = %#v", document)
	}
	provider := document.Providers[0]
	if provider.ID != "sol-5.6" || provider.Type != "azure_openai" || !provider.Default ||
		provider.Capabilities.Reasoning.DefaultEffort != "high" ||
		len(provider.Capabilities.Reasoning.Efforts) != 6 {
		t.Fatalf("placeholder provider shape = %#v", provider)
	}
	if provider.AzureOpenAI.Endpoint != "https://your-resource.openai.azure.com" ||
		provider.AzureOpenAI.Model != DefaultModel ||
		provider.AzureOpenAI.Deployment != DefaultModel ||
		provider.AzureOpenAI.APIKey != "replace-with-your-secret" ||
		provider.AzureOpenAI.APIVersion != "preview" {
		t.Fatalf("placeholder Azure shape = %#v", provider.AzureOpenAI)
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
			input:    strings.Replace(testAuthJSON, `"version": 2`, `"version": 2, "version": 2`, 1),
			contains: "duplicate",
		},
		{
			name:     "escaped duplicate",
			input:    strings.Replace(testAuthJSON, `"api_key": "synthetic-file-key"`, `"api_key": "synthetic-file-key", "\u0061pi_key": "other"`, 1),
			contains: "duplicate",
		},
		{
			name:     "unknown top-level member",
			input:    strings.Replace(testAuthJSON, `"version": 2,`, `"version": 2, "`+fieldMarker+`": "secret",`, 1),
			contains: "unsupported object member",
		},
		{
			name:     "unknown nested member",
			input:    strings.Replace(testAuthJSON, `"api_version": "2024-12-01-preview"`, `"api_version": "2024-12-01-preview", "`+fieldMarker+`": "secret"`, 1),
			contains: "unsupported object member",
		},
		{
			name:     "trailing object",
			input:    testAuthJSON + `{}`,
			contains: "trailing",
		},
		{
			name:     "wrong version",
			input:    strings.Replace(testAuthJSON, `"version": 2`, `"version": 1`, 1),
			contains: "unsupported schema version",
		},
		{
			name:     "fractional version",
			input:    strings.Replace(testAuthJSON, `"version": 2`, `"version": 2.0`, 1),
			contains: "version must be an integer",
		},
		{
			name:     "wrong provider type",
			input:    strings.Replace(testAuthJSON, `"type": "azure_openai"`, `"type": "openai"`, 1),
			contains: "unsupported provider type",
		},
		{
			name:     "missing providers",
			input:    `{"version":2}`,
			contains: "missing a required object member",
		},
		{
			name:     "wrong providers type",
			input:    `{"version":2,"providers":{}}`,
			contains: "providers must be an array",
		},
		{
			name:     "empty providers",
			input:    `{"version":2,"providers":[]}`,
			contains: "at least one provider",
		},
		{
			name:     "provider entry is not object",
			input:    `{"version":2,"providers":["sol"]}`,
			contains: "provider entries must be objects",
		},
		{
			name:     "duplicate provider entry member",
			input:    strings.Replace(testAuthJSON, `"id": "sol-5.6"`, `"id": "sol-5.6", "\u0069d": "other"`, 1),
			contains: "duplicate",
		},
		{
			name:     "provider default is not boolean",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","default":"true"}]}`,
			contains: "default must be a boolean",
		},
		{
			name:     "missing provider entry field",
			input:    strings.Replace(testAuthJSON, `      "id": "sol-5.6",`+"\n", "", 1),
			contains: "provider entry is missing",
		},
		{
			name:     "wrong capabilities type",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":[]}]}`,
			contains: "capabilities must be an object",
		},
		{
			name:     "unknown capabilities member",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"unknown":{}}}]}`,
			contains: "unsupported object member",
		},
		{
			name:     "missing reasoning capabilities",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{}}]}`,
			contains: "missing required reasoning capabilities",
		},
		{
			name:     "wrong reasoning type",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":[]}}]}`,
			contains: "reasoning capabilities must be an object",
		},
		{
			name:     "reasoning efforts are not array",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":{"efforts":"high","default_effort":"high"}}}]}`,
			contains: "reasoning efforts must be an array",
		},
		{
			name:     "reasoning effort is not string",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":{"efforts":[42],"default_effort":"high"}}}]}`,
			contains: "must contain only strings",
		},
		{
			name:     "empty reasoning efforts",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":{"efforts":[],"default_effort":"high"}}}]}`,
			contains: "at least one effort",
		},
		{
			name:     "reasoning default is not string",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":{"efforts":["high"],"default_effort":42}}}]}`,
			contains: "default_effort must be a string",
		},
		{
			name:     "missing Azure field",
			input:    strings.Replace(testAuthJSON, `    "model": "gpt-5.6-sol",`+"\n", "", 1),
			contains: "missing a required Azure OpenAI field",
		},
		{
			name:     "wrong Azure object type",
			input:    `{"version":2,"providers":[{"id":"sol","type":"azure_openai","capabilities":{"reasoning":{"efforts":["high"],"default_effort":"high"}},"azure_openai":[]}]}`,
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

func TestParseAuthFileRejectsLegacyV1Schema(t *testing.T) {
	legacy := `{
  "version": 1,
  "provider": "azure_openai",
  "azure_openai": {
    "endpoint": "https://example.openai.azure.com/",
    "model": "gpt-5.6-sol",
    "deployment": "legacy-deployment",
    "api_key": "legacy-synthetic-key",
    "api_version": "2024-12-01-preview"
  }
}`
	if document, err := parseAuthFile([]byte(legacy)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("legacy v1 document was accepted: %#v, %v", document, err)
	}
}

func TestParseAuthFileErrorsProtectDecodedCredentialCollisions(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential string
		input      func(string) string
	}{
		{
			name:       "unsupported type after key",
			credential: "unsupported provider type",
			input: func(credential string) string {
				input := strings.Replace(testAuthJSON, "synthetic-file-key", credential, 1)
				return strings.Replace(input, `"type": "azure_openai"`, `"type": "unsupported"`, 1)
			},
		},
		{
			name:       "duplicate member after key",
			credential: "duplicate object member",
			input: func(credential string) string {
				input := strings.Replace(testAuthJSON, "synthetic-file-key", credential, 1)
				return strings.Replace(input, `"api_version": "2024-12-01-preview"`, `"api_version": "2024-12-01-preview", "api_version": "v1"`, 1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseAuthFile([]byte(test.input(test.credential)))
			assertCredentialSafeError(t, err, test.credential)
		})
	}
}

func TestParseAuthFileRejectsProviderCountAboveLimit(t *testing.T) {
	providers := make([]authProviderFixture, 0, maxAuthFileProviders+1)
	for index := 0; index <= maxAuthFileProviders; index++ {
		providers = append(providers, providerFixture(fmt.Sprintf("provider-%02d", index)))
	}
	input := authRegistryJSON(t, providers...)
	if document, err := parseAuthFile([]byte(input)); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "providers exceed the limit") {
		t.Fatalf("oversized provider registry = %#v, %v", document, err)
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
		if len(document.Providers) != 1 || document.Providers[0].AzureOpenAI.APIKey == "" {
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
	if configuration.SelectedProvider.ID != "sol-5.6" ||
		!configuration.SelectedProvider.Default || !configuration.SelectedProvider.Selected ||
		len(configuration.Providers) != 1 || !configuration.Providers[0].Selected {
		t.Fatalf("selected singleton provider = %#v from %#v", configuration.SelectedProvider, configuration.Providers)
	}
	if configuration.SelectedProvider.Reasoning.DefaultEffort != "high" ||
		len(configuration.Azure.SupportedReasoningEfforts) != 6 ||
		len(configuration.ProviderBinding()) != 64 {
		t.Fatalf("selected provider capabilities or binding = %#v / %q", configuration.SelectedProvider, configuration.ProviderBinding())
	}
	for _, key := range []string{
		"AZURE_OPENAI_ENDPOINT",
		"AZURE_OPENAI_MODEL_NAME",
		"AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY",
		"AZURE_OPENAI_API_VERSION",
		"provider",
		"model",
		"reasoning_effort",
	} {
		if configuration.Provenance[key] != SourceFile {
			t.Fatalf("%s provenance = %q", key, configuration.Provenance[key])
		}
	}
	if strings.Contains(configuration.Azure.String(), "synthetic-file-key") ||
		strings.Contains(fmt.Sprintf("%#v", configuration.Azure), "synthetic-file-key") ||
		strings.Contains(fmt.Sprintf("%#v", configuration), "synthetic-file-key") ||
		strings.Contains(configuration.Redact("x synthetic-file-key y"), "synthetic-file-key") {
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

func TestLoadProviderSelectionMatrix(t *testing.T) {
	t.Run("singleton is implicit default", func(t *testing.T) {
		single := providerFixture("singleton")
		path := writeTestAuthFile(t, authRegistryJSON(t, single))
		configuration, err := Load(path, nil, Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if configuration.SelectedProvider.ID != single.ID ||
			!configuration.SelectedProvider.Default || !configuration.SelectedProvider.Selected {
			t.Fatalf("singleton selection = %#v", configuration.SelectedProvider)
		}
	})

	t.Run("unique declared default", func(t *testing.T) {
		sol := providerFixture("sol")
		sol.Default = boolFixture(false)
		terra := providerFixture("terra")
		terra.Default = boolFixture(true)
		path := writeTestAuthFile(t, authRegistryJSON(t, sol, terra))
		configuration, err := Load(path, nil, Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if configuration.SelectedProvider.ID != terra.ID || configuration.Azure.APIKey != terra.APIKey ||
			configuration.Providers[0].Selected || !configuration.Providers[1].Selected {
			t.Fatalf("default selection = %#v from %#v", configuration.SelectedProvider, configuration.Providers)
		}
		if configuration.Provenance["provider"] != SourceFile {
			t.Fatalf("default provider provenance = %q", configuration.Provenance["provider"])
		}
	})

	t.Run("multiple without default require instruction or explicit selector", func(t *testing.T) {
		sol := providerFixture("sol")
		terra := providerFixture("terra")
		path := writeTestAuthFile(t, authRegistryJSON(t, sol, terra))
		_, err := Load(path, nil, Overrides{})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"default": true`) ||
			!strings.Contains(err.Error(), "--provider <id>") {
			t.Fatalf("missing-default error = %v", err)
		}
		for _, secret := range []string{sol.APIKey, terra.APIKey} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("missing-default error exposed credential %q: %v", secret, err)
			}
		}

		configuration, err := Load(path, nil, Overrides{Provider: terra.ID})
		if err != nil {
			t.Fatal(err)
		}
		if configuration.SelectedProvider.ID != terra.ID || configuration.Azure.APIKey != terra.APIKey ||
			configuration.Provenance["provider"] != SourceFlag {
			t.Fatalf("explicit provider selection = %#v / %q", configuration.SelectedProvider, configuration.Provenance["provider"])
		}
	})

	t.Run("unknown explicit selector", func(t *testing.T) {
		profile := providerFixture("known")
		path := writeTestAuthFile(t, authRegistryJSON(t, profile))
		_, err := Load(path, nil, Overrides{Provider: "unknown"})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `--provider does not identify`) ||
			strings.Contains(err.Error(), `"unknown"`) {
			t.Fatalf("unknown-provider error = %v", err)
		}
	})
}

func TestLoadErrorsProtectCompleteProviderCredentialUnion(t *testing.T) {
	t.Run("unknown selector is value opaque", func(t *testing.T) {
		profile := providerFixture("known")
		profile.APIKey = "selector-secret-marker"
		path := writeTestAuthFile(t, authRegistryJSON(t, profile))
		_, err := Load(path, nil, Overrides{Provider: profile.APIKey})
		assertCredentialSafeError(t, err, profile.APIKey)
	})

	t.Run("missing default instructions collide with an unselected key", func(t *testing.T) {
		first := providerFixture("sol")
		first.APIKey = "selected-secret-marker"
		second := providerFixture("terra")
		second.APIKey = "--provider"
		path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
		_, err := Load(path, nil, Overrides{})
		assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	})

	t.Run("normalization freezes keys before semantic validation", func(t *testing.T) {
		first := providerFixture("-invalid")
		first.APIKey = "first-secret-marker"
		second := providerFixture("terra")
		second.APIKey = "invalid configuration"
		path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
		_, err := Load(path, nil, Overrides{})
		assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	})

	t.Run("unselected key protects selected reasoning failure", func(t *testing.T) {
		first := providerFixture("sol")
		first.Default = boolFixture(true)
		first.Efforts = []string{"low", "medium"}
		first.DefaultEffort = "low"
		second := providerFixture("terra")
		second.APIKey = "high"
		second.Efforts = []string{"low", "medium"}
		second.DefaultEffort = "low"
		path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
		_, err := Load(path, nil, Overrides{ReasoningEffort: "high"})
		assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	})

	t.Run("model mismatch diagnostic collides with selected key", func(t *testing.T) {
		profile := providerFixture("sol")
		profile.APIKey = "--model"
		path := writeTestAuthFile(t, authRegistryJSON(t, profile))
		_, err := Load(path, nil, Overrides{Model: "different-model"})
		assertCredentialSafeError(t, err, profile.APIKey)
	})

	t.Run("environment validation diagnostic collides with selected key", func(t *testing.T) {
		profile := providerFixture("sol")
		profile.APIKey = "process"
		path := writeTestAuthFile(t, authRegistryJSON(t, profile))
		environ := make([]string, maxEnvironmentEntryCount+1)
		for index := range environ {
			environ[index] = fmt.Sprintf("SAFE_%04d=value", index)
		}
		_, err := Load(path, environ, Overrides{})
		assertCredentialSafeError(t, err, profile.APIKey)
	})
}

func TestLoadRejectsAmbiguousProviderRegistry(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*authProviderFixture, *authProviderFixture)
		contains string
	}{
		{
			name: "duplicate defaults",
			mutate: func(first, second *authProviderFixture) {
				first.Default = boolFixture(true)
				second.Default = boolFixture(true)
			},
			contains: "multiple providers with default true",
		},
		{
			name: "duplicate IDs",
			mutate: func(first, second *authProviderFixture) {
				second.ID = first.ID
			},
			contains: "duplicate provider id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := providerFixture("sol")
			second := providerFixture("terra")
			test.mutate(&first, &second)
			path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
			if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ambiguous provider registry error = %v", err)
			}
		})
	}
}

func TestLoadRejectsInvalidProviderIDs(t *testing.T) {
	for _, id := range []string{
		"",
		"-leading-hyphen",
		"provider with spaces",
		"provider/with/slashes",
		"prøvider",
		strings.Repeat("p", 65),
	} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			profile := providerFixture(id)
			path := writeTestAuthFile(t, authRegistryJSON(t, profile))
			if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
				!strings.Contains(err.Error(), "provider id must be 1-64 ASCII") {
				t.Fatalf("invalid provider ID %q error = %v", id, err)
			}
		})
	}
}

func TestLoadRejectsInvalidReasoningCapabilities(t *testing.T) {
	for _, test := range []struct {
		name          string
		efforts       []string
		defaultEffort string
		contains      string
	}{
		{name: "duplicate effort", efforts: []string{"low", "high", "low"}, defaultEffort: "high", contains: "duplicate effort"},
		{name: "unsupported effort", efforts: []string{"low", "turbo"}, defaultEffort: "low", contains: "unsupported effort"},
		{name: "default not member", efforts: []string{"low", "medium"}, defaultEffort: "high", contains: "present in efforts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := providerFixture("sol")
			profile.Efforts = test.efforts
			profile.DefaultEffort = test.defaultEffort
			path := writeTestAuthFile(t, authRegistryJSON(t, profile))
			if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("reasoning capability error = %v", err)
			}
		})
	}
}

func TestReasoningEffortUsesSelectedProviderSubsetAndPrecedence(t *testing.T) {
	profile := providerFixture("sol")
	profile.Efforts = []string{"low", "medium"}
	profile.DefaultEffort = "low"
	path := writeTestAuthFile(t, authRegistryJSON(t, profile))

	for _, test := range []struct {
		name       string
		environ    []string
		overrides  Overrides
		wantEffort string
		wantSource Source
	}{
		{name: "file default", wantEffort: "low", wantSource: SourceFile},
		{name: "process", environ: []string{"AGENTX_REASONING_EFFORT=medium"}, wantEffort: "medium", wantSource: SourceProcess},
		{name: "flag over process", environ: []string{"AGENTX_REASONING_EFFORT=low"}, overrides: Overrides{ReasoningEffort: "medium"}, wantEffort: "medium", wantSource: SourceFlag},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := Load(path, test.environ, test.overrides)
			if err != nil {
				t.Fatal(err)
			}
			if configuration.Azure.ReasoningEffort != test.wantEffort ||
				configuration.Provenance["reasoning_effort"] != test.wantSource {
				t.Fatalf("reasoning effort = %q/%q, want %q/%q", configuration.Azure.ReasoningEffort, configuration.Provenance["reasoning_effort"], test.wantEffort, test.wantSource)
			}
		})
	}

	for _, test := range []struct {
		name      string
		environ   []string
		overrides Overrides
	}{
		{name: "unsupported process effort", environ: []string{"AGENTX_REASONING_EFFORT=high"}},
		{name: "unsupported flag effort", overrides: Overrides{ReasoningEffort: "high"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(path, test.environ, test.overrides); !errors.Is(err, ErrInvalid) ||
				!strings.Contains(err.Error(), "not supported by the selected provider") {
				t.Fatalf("unsupported selected-provider effort error = %v", err)
			}
		})
	}
}

func TestRuntimeCredentialSanitizerIncludesEveryProvider(t *testing.T) {
	sol := providerFixture("sol")
	sol.Default = boolFixture(true)
	terra := providerFixture("terra")
	path := writeTestAuthFile(t, authRegistryJSON(t, sol, terra))
	configuration, err := Load(path, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	sanitizer := configuration.CredentialSanitizer()
	if sanitizer.LiteralCount() != 2 {
		t.Fatalf("credential literal count = %d, want 2", sanitizer.LiteralCount())
	}
	message := "selected=" + sol.APIKey + " unselected=" + terra.APIKey
	redacted := configuration.Redact(message)
	for _, credential := range []string{sol.APIKey, terra.APIKey} {
		if !sanitizer.Contains(credential) || strings.Contains(redacted, credential) {
			t.Fatalf("complete provider union did not protect %q: %q", credential, redacted)
		}
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{sol.APIKey, terra.APIKey} {
		if strings.Contains(string(encoded), credential) {
			t.Fatalf("runtime JSON exposed provider credential %q: %s", credential, encoded)
		}
	}
}

func TestProviderBindingAllowsKeyRotationButDetectsRouteChanges(t *testing.T) {
	profile := providerFixture("sol")
	path := writeTestAuthFile(t, authRegistryJSON(t, profile))
	first, err := Load(path, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	profile.APIKey = "rotated-opaque-credential"
	rotatedPath := writeTestAuthFile(t, authRegistryJSON(t, profile))
	rotated, err := Load(rotatedPath, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderBinding() != rotated.ProviderBinding() {
		t.Fatal("API-key rotation changed the noncredential provider binding")
	}
	profile.Deployment = "replacement-deployment"
	changedPath := writeTestAuthFile(t, authRegistryJSON(t, profile))
	changed, err := Load(changedPath, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderBinding() == changed.ProviderBinding() {
		t.Fatal("deployment change did not change the provider binding")
	}
}

func TestLoadRejectsProviderCatalogCredentialCollision(t *testing.T) {
	const credential = "catalog-secret-marker"
	first := providerFixture(credential)
	first.Default = boolFixture(true)
	first.Endpoint = "https://first.example.test"
	first.Model = "first-model"
	first.Deployment = "first-deployment"
	second := providerFixture("terra")
	second.APIKey = credential
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
	_, err := Load(path, nil, Overrides{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "public provider metadata overlaps") {
		t.Fatalf("provider-catalog credential collision error = %v", err)
	}
	if strings.Contains(err.Error(), credential) {
		t.Fatalf("provider-catalog collision error exposed credential: %v", err)
	}
}

func TestLoadRejectsCompleteRegistryCredentialCollisionsWithNormalizedRoutes(t *testing.T) {
	first := providerFixture("sol")
	first.Default = boolFixture(true)
	second := providerFixture("terra")
	normalizedEndpoint, err := normalizeEndpoint(first.Endpoint, first.APIVersion)
	if err != nil {
		t.Fatal(err)
	}
	binding := providerRouteBinding("azure_openai", normalizedEndpoint, first.Model, first.Deployment, first.APIVersion)

	tests := []struct {
		name       string
		credential string
		mutate     func(*authProviderFixture)
	}{
		{name: "model", credential: first.Model},
		{name: "deployment", credential: first.Deployment},
		{name: "API version", credential: first.APIVersion},
		{name: "binding", credential: binding},
		{
			name: "semantic endpoint path", credential: "秘密",
			mutate: func(provider *authProviderFixture) {
				provider.Endpoint = "https://sol.example.test/秘密"
			},
		},
		{
			name: "wire endpoint path", credential: "%E7%A7%98%E5%AF%86",
			mutate: func(provider *authProviderFixture) {
				provider.Endpoint = "https://sol.example.test/秘密"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := first
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			unselected := second
			unselected.APIKey = test.credential
			path := writeTestAuthFile(t, authRegistryJSON(t, candidate, unselected))
			_, loadErr := Load(path, nil, Overrides{})
			assertCredentialSafeError(t, loadErr, candidate.APIKey, unselected.APIKey)
			if !strings.Contains(loadErr.Error(), "normalized provider routing") {
				t.Fatalf("route-collision error = %v", loadErr)
			}
		})
	}
}

func TestLoadRejectsCredentialInEveryReachableProviderCatalogState(t *testing.T) {
	first := providerFixture("sol")
	second := providerFixture("terra")
	// This physical fragment exists only when the second descriptor is selected,
	// proving normalization checks more than the first/default projection.
	second.APIKey = `"id":"terra","type":"azure_openai","model":"terra-model","default":false,"selected":true`
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
	_, err := Load(path, nil, Overrides{Provider: second.ID})
	assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	if !strings.Contains(err.Error(), "public provider metadata overlaps") {
		t.Fatalf("provider-catalog state collision error = %v", err)
	}
}

func TestExplicitProviderCannotEvadeSelectedScalarCredentialCollision(t *testing.T) {
	first := providerFixture("sol")
	second := providerFixture("terra")
	second.APIKey = "true"
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
	_, err := Load(path, nil, Overrides{Provider: second.ID})
	assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	if !strings.Contains(err.Error(), "public provider metadata overlaps") {
		t.Fatalf("selected-scalar collision error = %v", err)
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
	for _, model := range []string{"different-model", " " + DefaultModel, DefaultModel + " ", "\t" + DefaultModel + "\n"} {
		if _, err := Load(path, nil, Overrides{Model: model}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("model override %q error = %v", model, err)
		}
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

func TestLoadRejectsProviderRoutingSurroundingWhitespace(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "endpoint", old: `"https://example.openai.azure.com/"`, new: `" https://example.openai.azure.com/"`},
		{name: "model", old: `"gpt-5.6-sol"`, new: `"gpt-5.6-sol "`},
		{name: "deployment", old: `"file-deployment"`, new: `"\tfile-deployment"`},
		{name: "API version", old: `"2024-12-01-preview"`, new: `" 2024-12-01-preview "`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := strings.Replace(testAuthJSON, test.old, test.new, 1)
			path := writeTestAuthFile(t, content)
			if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "surrounding whitespace") {
				t.Fatalf("provider routing whitespace was normalized before validation: %v", err)
			}
		})
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
		{"model surrounding whitespace", func(value *Azure) { value.ModelName = " " + DefaultModel }},
		{"deployment surrounding whitespace", func(value *Azure) { value.Deployment = DefaultModel + " " }},
		{"API version surrounding whitespace", func(value *Azure) { value.APIVersion = " preview" }},
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

func TestAzureValidateProtectsCredentialCollisionsWithoutExposingCause(t *testing.T) {
	endpoint, err := normalizeEndpoint("https://example.test", "v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"model", `"`} {
		configuration := Azure{
			Endpoint: endpoint, ModelName: "", Deployment: DefaultModel, APIKey: credential,
			APIVersion: "v1", ReasoningEffort: "high", RequestTimeout: time.Second,
			StreamWatchdog: time.Second, MaxRetries: 1,
		}
		assertCredentialSafeError(t, configuration.Validate(), configuration.APIKey)
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
