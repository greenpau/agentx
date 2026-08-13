package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProviderRegistryIsProviderNeutral(t *testing.T) {
	first := providerFixture("sol")
	first.Efforts = []string{"none", "high", "max"}
	first.DefaultEffort = "high"
	second := providerFixture("terra")
	second.Efforts = []string{"low", "medium", "xhigh"}
	second.DefaultEffort = "medium"
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))

	// Discovery does not consult process effort configuration and does not need
	// to select a provider from an otherwise ambiguous registry.
	t.Setenv("AGENTX_REASONING_EFFORT", "unsupported-process-value")
	registry, err := LoadProviderRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Providers()
	if len(descriptors) != 2 || descriptors[0].ID != first.ID || descriptors[1].ID != second.ID {
		t.Fatalf("provider discovery order = %#v", descriptors)
	}
	for index, descriptor := range descriptors {
		if descriptor.Selected {
			t.Fatalf("provider %d was selected during discovery: %#v", index, descriptor)
		}
		if descriptor.Default {
			t.Fatalf("provider %d acquired a default in a multi-provider registry: %#v", index, descriptor)
		}
	}
	if descriptors[0].Model != first.Model || descriptors[0].Reasoning.DefaultEffort != first.DefaultEffort ||
		descriptors[1].Model != second.Model || descriptors[1].Reasoning.DefaultEffort != second.DefaultEffort {
		t.Fatalf("provider discovery capabilities = %#v", descriptors)
	}

	// The ordinary runtime loader still owns provider selection and rejects the
	// same registry when neither an explicit selector nor a default is present.
	if _, err := Load(path, nil, Overrides{}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), `"default": true`) {
		t.Fatalf("runtime selection error = %v", err)
	}
}

func TestProviderRegistryReturnsDefensiveCredentialSafeProjections(t *testing.T) {
	first := providerFixture("sol")
	first.Default = boolFixture(true)
	second := providerFixture("terra")
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
	registry, err := LoadProviderRegistry(path)
	if err != nil {
		t.Fatal(err)
	}

	descriptors := registry.Providers()
	descriptors[0].ID = "mutated"
	descriptors[0].Selected = true
	descriptors[0].Reasoning.Efforts[0] = "max"
	again := registry.Providers()
	if again[0].ID != first.ID || again[0].Selected || again[0].Reasoning.Efforts[0] != first.Efforts[0] {
		t.Fatalf("caller mutation changed registry state: %#v", again)
	}

	sanitizer := registry.CredentialSanitizer()
	if sanitizer.LiteralCount() != 2 {
		t.Fatalf("credential union count = %d, want 2", sanitizer.LiteralCount())
	}
	for _, credential := range []string{first.APIKey, second.APIKey} {
		if !sanitizer.Contains(credential) {
			t.Fatalf("credential union omitted a provider key")
		}
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || !strings.Contains(string(encoded), `"providers"`) {
		t.Fatalf("provider registry JSON = %q", encoded)
	}
	for _, credential := range []string{first.APIKey, second.APIKey} {
		if strings.Contains(string(encoded), credential) {
			t.Fatalf("provider registry JSON exposed a credential")
		}
		for format, rendered := range map[string]string{
			"%v":  fmt.Sprintf("%v", registry),
			"%#v": fmt.Sprintf("%#v", registry),
		} {
			if strings.Contains(rendered, credential) {
				t.Fatalf("provider registry %s rendering exposed a credential", format)
			}
		}
	}
}

func TestLoadProviderRegistrySingletonAndInvalidDefaults(t *testing.T) {
	t.Run("singleton is effective default but remains unselected", func(t *testing.T) {
		provider := providerFixture("singleton")
		path := writeTestAuthFile(t, authRegistryJSON(t, provider))
		registry, err := LoadProviderRegistry(path)
		if err != nil {
			t.Fatal(err)
		}
		descriptors := registry.Providers()
		if len(descriptors) != 1 || !descriptors[0].Default || descriptors[0].Selected {
			t.Fatalf("singleton discovery = %#v", descriptors)
		}
	})

	t.Run("multiple declared defaults remain invalid", func(t *testing.T) {
		first := providerFixture("sol")
		first.Default = boolFixture(true)
		first.APIKey = "multiple"
		second := providerFixture("terra")
		second.Default = boolFixture(true)
		second.APIKey = "providers"
		path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
		_, err := LoadProviderRegistry(path)
		assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	})
}

func TestLoadProviderRegistryNormalizesEveryProvider(t *testing.T) {
	first := providerFixture("sol")
	first.Default = boolFixture(true)
	second := providerFixture("terra")
	second.Endpoint = "http://terra.example.test"
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))

	_, err := LoadProviderRegistry(path)
	assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("unselected provider normalization error = %v", err)
	}
}

func TestLoadProviderRegistryAtRootUsesPinnedDescriptor(t *testing.T) {
	if !credentialFileAccessControlVerified {
		t.Skip("platform cannot verify owner-only credential-file access")
	}
	dir := t.TempDir()
	provider := providerFixture("descriptor-rooted")
	if err := os.WriteFile(
		filepath.Join(dir, DefaultAuthFile),
		[]byte(authRegistryJSON(t, provider)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	registry, err := LoadProviderRegistryAtRoot(root, filepath.Join(dir, DefaultAuthFile))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Providers()
	if len(descriptors) != 1 || descriptors[0].ID != provider.ID || !descriptors[0].Default || descriptors[0].Selected {
		t.Fatalf("descriptor-rooted discovery = %#v", descriptors)
	}
	if _, err := LoadProviderRegistryAtRoot(nil, filepath.Join(dir, DefaultAuthFile)); err == nil {
		t.Fatal("nil descriptor root was accepted")
	}
}

func TestLoadProviderRegistryRejectsCredentialInUnselectedCatalogProjection(t *testing.T) {
	first := providerFixture("sol")
	second := providerFixture("terra")
	catalog := []ProviderDescriptor{
		{
			ID: first.ID, Type: authFileProviderAzureOpenAI, Model: first.Model,
			Reasoning: ReasoningCapabilities{Efforts: first.Efforts, DefaultEffort: first.DefaultEffort},
		},
		{
			ID: second.ID, Type: authFileProviderAzureOpenAI, Model: second.Model,
			Reasoning: ReasoningCapabilities{Efforts: second.Efforts, DefaultEffort: second.DefaultEffort},
		},
	}
	credential, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	second.APIKey = string(credential)
	path := writeTestAuthFile(t, authRegistryJSON(t, first, second))
	_, err = LoadProviderRegistry(path)
	assertCredentialSafeError(t, err, first.APIKey, second.APIKey)
	if !strings.Contains(err.Error(), "public provider metadata overlaps") {
		t.Fatalf("unselected catalog collision error = %v", err)
	}
}
