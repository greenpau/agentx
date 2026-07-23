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

type panickingEnvReader struct{}

func (panickingEnvReader) Read([]byte) (int, error) {
	panic("credential-bearing reader panic")
}

func TestParseEnvQuotesAndComments(t *testing.T) {
	got, err := ParseEnv(strings.NewReader("\uFEFF # comment\r\n A = one # x\r\nB=\"two\\nlines\" # comment\r\nC=' literal ' \r\nexport D=ok\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "one", "B": "two\nlines", "C": " literal ", "D": "ok"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s: got %q want %q", key, got[key], value)
		}
	}
}

func TestParseEnvRejectsDuplicateAmbiguousAndMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "duplicate", input: []byte("A=one\nA=two\n")},
		{name: "case duplicate", input: []byte("A=one\na=two\n")},
		{name: "invalid UTF-8", input: []byte{'A', '=', 0xff, '\n'}},
		{name: "embedded BOM", input: []byte("A=one\n\uFEFFB=two\n")},
		{name: "source control", input: []byte("A=one\x1bhidden\n")},
		{name: "quoted trailing data", input: []byte("A=\"one\"trailing\n")},
		{name: "single quote ambiguity", input: []byte("A='one'trailing'\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if values, err := ParseEnv(strings.NewReader(string(test.input))); err == nil || values != nil {
				t.Fatalf("malformed dotenv accepted: %#v, %v", values, err)
			}
		})
	}
}

func TestParseEnvContainsReaderPanicsAndDiagnostics(t *testing.T) {
	values, err := ParseEnv(panickingEnvReader{})
	if err == nil || values != nil {
		t.Fatalf("panicking reader = %#v, %v", values, err)
	}
	if strings.Contains(err.Error(), "credential-bearing") {
		t.Fatal("reader panic payload reached the diagnostic")
	}
}

func TestDotenvAndProcessEnvironmentInputsAreBounded(t *testing.T) {
	var dotenv strings.Builder
	for index := 0; index <= maxEnvironmentEntryCount; index++ {
		fmt.Fprintf(&dotenv, "SAFE_%04d=value\n", index)
	}
	if values, err := ParseEnv(strings.NewReader(dotenv.String())); err == nil || values != nil {
		t.Fatalf("oversized dotenv = %#v, %v", values, err)
	}

	process := make([]string, maxEnvironmentEntryCount+1)
	for index := range process {
		process[index] = fmt.Sprintf("SAFE_%04d=value", index)
	}
	if values, err := LoadEnvFile("", process); !errors.Is(err, ErrInvalid) || values != nil {
		t.Fatalf("oversized process environment = %#v, %v", values, err)
	}
}

func TestLoadCoherentSourceNormalizationAndRedaction(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env.production")
	data := "AZURE_OPENAI_ENDPOINT=https://example.openai.azure.com/\nAZURE_OPENAI_MODEL_NAME=gpt-5.6-sol\nAZURE_OPENAI_DEPLOYMENT=file-deploy\nAZURE_OPENAI_SUBSCRIPTION_KEY=supersecret\n AZURE_OPENAI_API_VERSION = \"2024-12-01-preview\"\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file, nil, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Azure.Endpoint.String(); got != "https://example.openai.azure.com/openai/responses" {
		t.Fatalf("endpoint = %q", got)
	}
	if cfg.Azure.Deployment != "file-deploy" || cfg.Provenance["model"] != SourceFile {
		t.Fatalf("precedence/provenance = %#v", cfg)
	}
	if strings.Contains(cfg.Azure.String(), "supersecret") || strings.Contains(fmt.Sprintf("%#v", cfg.Azure), "supersecret") || strings.Contains(fmt.Sprintf("%#v", cfg), "supersecret") || strings.Contains(cfg.Azure.Redact("x supersecret y"), "supersecret") {
		t.Fatal("secret was not redacted")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "supersecret") {
		t.Fatal("JSON serialization exposed the credential")
	}
}

func TestLoadRejectsMixedAzureCredentialSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env.production")
	data := "AZURE_OPENAI_ENDPOINT=https://attacker.example\nAZURE_OPENAI_MODEL_NAME=gpt-5.6-sol\nAZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol\nAZURE_OPENAI_API_VERSION=preview\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(file, []string{"AZURE_OPENAI_SUBSCRIPTION_KEY=exported-secret"}, Overrides{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "coherent source") {
		t.Fatalf("mixed credential bundle = %v", err)
	}
}

func TestLoadAcceptsCompleteProcessBundleWithoutDotenvFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	configuration, err := Load(missing, []string{
		"AZURE_OPENAI_ENDPOINT=https://example.test",
		"AZURE_OPENAI_MODEL_NAME=gpt-5.6-sol",
		"AZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=synthetic-process-key",
		"AZURE_OPENAI_API_VERSION=2026-07-01-preview",
	}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.ModelName != "gpt-5.6-sol" || configuration.Provenance["AZURE_OPENAI_SUBSCRIPTION_KEY"] != SourceProcess {
		t.Fatalf("process bundle provenance = %#v", configuration)
	}
}

func TestLoadAcceptsCaseInsensitiveProcessNamesAndRejectsDuplicates(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	configuration, err := Load(missing, []string{
		"azure_openai_endpoint=https://example.test",
		"Azure_OpenAI_Model_Name=gpt-5.6-sol",
		"azure_openai_deployment=gpt-5.6-sol",
		"azure_openai_subscription_key=synthetic-process-key",
	}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Provenance["AZURE_OPENAI_SUBSCRIPTION_KEY"] != SourceProcess {
		t.Fatalf("process provenance = %#v", configuration.Provenance)
	}

	_, err = Load(missing, []string{
		"AZURE_OPENAI_ENDPOINT=https://example.test",
		"azure_openai_endpoint=https://other.test",
		"AZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=synthetic-process-key",
	}, Overrides{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate process environment = %v", err)
	}
}

func TestLoadAttributesFallbackModelToDefaultNotEmptyProcessValue(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	configuration, err := Load(missing, []string{
		"AZURE_OPENAI_ENDPOINT=https://example.test",
		"AZURE_OPENAI_MODEL_NAME=   ",
		"AZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=synthetic-process-key",
	}, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Azure.ModelName != DefaultModel || configuration.Provenance["model"] != SourceDefault {
		t.Fatalf("fallback model/provenance = %q/%q", configuration.Azure.ModelName, configuration.Provenance["model"])
	}
}

func TestLoadRejectsMissingSecretWithoutEcho(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env.production")
	if err := os.WriteFile(file, []byte("AZURE_OPENAI_ENDPOINT=https://example.test\nAZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(file, nil, Overrides{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "AZURE_OPENAI_SUBSCRIPTION_KEY") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestEndpointRejectsInsecureRemote(t *testing.T) {
	_, err := normalizeEndpoint("http://example.test", "v1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEndpointRejectsUserInfoQueryAndFragment(t *testing.T) {
	for _, endpoint := range []string{
		"https://user:password@example.test", "https://example.test?key=value", "https://example.test/#fragment",
	} {
		if _, err := normalizeEndpoint(endpoint, "v1"); err == nil {
			t.Errorf("normalizeEndpoint(%q) succeeded", endpoint)
		}
	}
}

func TestLoadEnvFileRejectsSymlinkAndLooseMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("A=B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err == nil {
		if _, err := LoadEnvFile(link, nil); err == nil {
			t.Fatal("dotenv symlink was accepted")
		}
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("A=B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEnvFile(loose, nil)
	if err == nil {
		t.Fatal("loosely permissioned or unverifiable credential file was accepted")
	}
}

func TestLoadEnvFileRejectsHardlinkedCredentialFile(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, ".env.production")
	if err := os.WriteFile(original, []byte("A=B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "credential-alias")
	if err := os.Link(original, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := LoadEnvFile(original, nil); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("hardlinked dotenv error = %v", err)
	}
}

func TestLoadEnvFileDiagnosticsDoNotEchoSelectedPath(t *testing.T) {
	const marker = "credential-bearing-path-marker"
	path := filepath.Join(t.TempDir(), marker+"\n")
	_, err := LoadEnvFile(path, nil)
	if err == nil {
		t.Fatal("missing dotenv file was accepted")
	}
	if strings.Contains(err.Error(), marker) || strings.ContainsAny(err.Error(), "\r\n") {
		t.Fatalf("dotenv diagnostic exposed the selected path: %q", err)
	}
}

func TestLoadEnvFileRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.production")
	if err := os.WriteFile(path, []byte("A=one\na=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(path, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate dotenv keys = %v", err)
	}
}

func TestLoadPreservesCredentialWhitespaceForValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.production")
	data := "AZURE_OPENAI_ENDPOINT=https://example.test\n" +
		"AZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol\n" +
		"AZURE_OPENAI_SUBSCRIPTION_KEY=' synthetic-key '\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("credential whitespace was normalized before validation: %v", err)
	}
}

func TestLoadRejectsUnconfiguredModelOverride(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env.production")
	data := "AZURE_OPENAI_ENDPOINT=https://example.test\nAZURE_OPENAI_MODEL_NAME=gpt-5.6-sol\nAZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol\nAZURE_OPENAI_SUBSCRIPTION_KEY=secret\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(file, nil, Overrides{Model: "different-model"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadLeavesAzureV1VersionImplicit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".env.production")
	data := "AZURE_OPENAI_ENDPOINT=https://example.test\nAZURE_OPENAI_MODEL_NAME=gpt-5.6-sol\nAZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol\nAZURE_OPENAI_SUBSCRIPTION_KEY=secret\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(file, nil, Overrides{})
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
		Endpoint: endpoint, ModelName: "gpt-5.6-sol", Deployment: "gpt-5.6-sol", APIKey: "synthetic-key",
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
		Endpoint: valid, ModelName: "gpt-5.6-sol", Deployment: "gpt-5.6-sol", APIKey: "synthetic-key",
		ReasoningEffort: "high", RequestTimeout: time.Second, StreamWatchdog: time.Second, MaxRetries: 1,
	}
	for _, raw := range []string{
		"http://example.test/openai/v1/responses",
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
		envPath    = "opaque-dotenv-path"
	)
	runtime := Runtime{
		Azure: Azure{
			Endpoint: endpoint, ModelName: identity, Deployment: identity, APIKey: credential,
			APIVersion: "preview", ReasoningEffort: "high", RequestTimeout: time.Second,
			StreamWatchdog: time.Second, MaxRetries: 1,
		},
		EnvFile: envPath,
	}
	for _, rendered := range []string{runtime.String(), runtime.GoString(), fmt.Sprintf("%#v", runtime)} {
		if strings.Contains(rendered, credential) || strings.Contains(rendered, identity) ||
			strings.Contains(rendered, envPath) {
			t.Fatalf("configuration diagnostic exposed private identity: %q", rendered)
		}
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), credential) || strings.Contains(string(encoded), envPath) {
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
				EnvFile: credential,
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
