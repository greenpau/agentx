package config

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/greenpau/agentx/pkg/redact"
)

// ProviderRegistry is a validated, provider-neutral view of auth.json. It
// owns no selected provider, model client, environment-derived effort, or
// session state. Its public projection contains only credential-free provider
// descriptors.
type ProviderRegistry struct {
	providers   []ProviderDescriptor
	credentials *redact.Set
}

// LoadProviderRegistry validates and normalizes every provider in auth.json
// without selecting one. A multi-provider registry therefore does not require
// a default for discovery, while multiple declared defaults remain invalid.
func LoadProviderRegistry(pathname string) (ProviderRegistry, error) {
	if pathname == "" {
		pathname = DefaultAuthFile
	}
	return loadProviderRegistry(authFileLocation{path: pathname})
}

// LoadProviderRegistryAtRoot is the descriptor-pinned counterpart to
// LoadProviderRegistry. pathname is retained only for diagnostics; the file
// read through root is always the literal DefaultAuthFile child.
func LoadProviderRegistryAtRoot(root *os.Root, pathname string) (ProviderRegistry, error) {
	if root == nil {
		return ProviderRegistry{}, errors.New("auth file root is unavailable")
	}
	if pathname == "" {
		pathname = DefaultAuthFile
	}
	return loadProviderRegistry(authFileLocation{root: root, path: pathname})
}

func loadProviderRegistry(location authFileLocation) (ProviderRegistry, error) {
	document, err := loadAuthFile(location)
	if err != nil {
		return ProviderRegistry{}, err
	}
	profiles, credentials, err := normalizeAuthProviders(document)
	if err != nil {
		return ProviderRegistry{}, err
	}
	descriptors := make([]ProviderDescriptor, len(profiles))
	for index := range profiles {
		descriptors[index] = cloneProviderDescriptor(profiles[index].descriptor)
		descriptors[index].Selected = false
	}
	return ProviderRegistry{providers: descriptors, credentials: credentials}, nil
}

// Providers returns the credential-free descriptors in auth.json source
// order. The returned slice and every reasoning-effort slice are defensive
// copies; mutations cannot change later registry projections.
func (r ProviderRegistry) Providers() []ProviderDescriptor {
	descriptors := make([]ProviderDescriptor, len(r.providers))
	for index := range r.providers {
		descriptors[index] = cloneProviderDescriptor(r.providers[index])
		descriptors[index].Selected = false
	}
	return descriptors
}

// CredentialSanitizer returns an immutable copy of the complete provider-key
// union. A discovery surface must compose this set with its other credential
// sources before constructing output or diagnostic sinks.
func (r ProviderRegistry) CredentialSanitizer() *redact.Set {
	return redact.Union(r.credentials)
}

// MarshalJSON emits only the credential-free provider catalog. Apply the
// complete credential union after outer JSON framing as a defensive guard
// against a future descriptor field accidentally overlapping a key.
func (r ProviderRegistry) MarshalJSON() ([]byte, error) {
	projection := struct {
		Providers []ProviderDescriptor `json:"providers"`
	}{Providers: r.Providers()}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil, errors.New("provider registry projection failed")
	}
	return credentialSafeJSON(encoded, r.CredentialSanitizer()), nil
}
