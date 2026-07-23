package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	pluginManifestRelativePath = ".agentx-plugin/plugin.json"
	maxPluginManifestBytes     = 1 << 20
	maxPluginManifestsPerRoot  = 10_000
)

var pluginPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ExtensionAvailability keeps independent availability gates explainable. A
// descriptor is callable only when every gate succeeds.
type ExtensionAvailability struct {
	BuildIncluded     bool     `json:"build_included"`
	FeatureEnabled    bool     `json:"feature_enabled"`
	AccountEligible   bool     `json:"account_eligible"`
	PlatformSupported bool     `json:"platform_supported"`
	SourceTrusted     bool     `json:"source_trusted"`
	PolicyAllowed     bool     `json:"policy_allowed"`
	ProviderAvailable bool     `json:"provider_available"`
	DescriptorValid   bool     `json:"descriptor_valid"`
	SessionEnabled    bool     `json:"session_enabled"`
	Reasons           []string `json:"reasons,omitempty"`
}

// FullyAvailable returns the conservative default for a validated local
// descriptor. Callers must deliberately lower gates that do not hold.
func FullyAvailable() ExtensionAvailability {
	return ExtensionAvailability{
		BuildIncluded: true, FeatureEnabled: true, AccountEligible: true,
		PlatformSupported: true, SourceTrusted: true, PolicyAllowed: true,
		ProviderAvailable: true, DescriptorValid: true, SessionEnabled: true,
	}
}

func (a ExtensionAvailability) Usable() bool {
	return a.BuildIncluded && a.FeatureEnabled && a.AccountEligible &&
		a.PlatformSupported && a.SourceTrusted && a.PolicyAllowed &&
		a.ProviderAvailable && a.DescriptorValid && a.SessionEnabled
}

func (a ExtensionAvailability) clone() ExtensionAvailability {
	a.Reasons = append([]string(nil), a.Reasons...)
	return a
}

// PluginRoot is an attributed discovery root. Path may name a plugin root or
// a directory containing multiple plugin roots.
type PluginRoot struct {
	Path        string
	Source      Source
	Marketplace string
	Scope       string
	Enabled     bool
	Trusted     bool
	Strict      bool
}

// PluginManifest is the validated, authority-bearing subset of plugin.json.
// Unknown fields are retained only as diagnostics and never gain behavior.
type PluginManifest struct {
	Name         string              `json:"name"`
	Version      string              `json:"version,omitempty"`
	Description  string              `json:"description,omitempty"`
	Homepage     string              `json:"homepage,omitempty"`
	Repository   string              `json:"repository,omitempty"`
	License      string              `json:"license,omitempty"`
	Keywords     []string            `json:"keywords,omitempty"`
	Dependencies []string            `json:"dependencies,omitempty"`
	Components   map[string][]string `json:"components,omitempty"`
}

// PluginDescriptor is immutable once published in a PluginSnapshot.
type PluginDescriptor struct {
	CanonicalID  string                `json:"canonical_id"`
	Name         string                `json:"name"`
	Marketplace  string                `json:"marketplace"`
	Version      string                `json:"version,omitempty"`
	Description  string                `json:"description,omitempty"`
	Root         string                `json:"root"`
	Manifest     string                `json:"manifest,omitempty"`
	Source       Source                `json:"source"`
	Scope        string                `json:"scope,omitempty"`
	Dependencies []string              `json:"dependencies,omitempty"`
	Components   map[string][]string   `json:"components,omitempty"`
	Availability ExtensionAvailability `json:"availability"`
	Generation   uint64                `json:"generation"`
}

// PluginPolicy applies after identity and provenance are known. A nil AllowIDs
// is open; a non-nil empty map denies all nonmanaged plugins.
type PluginPolicy struct {
	AllowIDs             map[string]bool
	DenyIDs              map[string]bool
	ManagedReservedNames map[string]bool
}

type PluginSnapshot struct {
	Generation  uint64             `json:"generation"`
	Plugins     []PluginDescriptor `json:"plugins"`
	Diagnostics []Diagnostic       `json:"diagnostics,omitempty"`
	byID        map[string]int
}

func (s PluginSnapshot) Lookup(id string) (PluginDescriptor, bool) {
	index, ok := s.byID[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return PluginDescriptor{}, false
	}
	descriptor := clonePlugin(s.Plugins[index])
	return descriptor, descriptor.Availability.Usable()
}

func clonePlugin(p PluginDescriptor) PluginDescriptor {
	p.Dependencies = append([]string(nil), p.Dependencies...)
	if p.Components != nil {
		components := make(map[string][]string, len(p.Components))
		for kind, paths := range p.Components {
			components[kind] = append([]string(nil), paths...)
		}
		p.Components = components
	}
	p.Availability = p.Availability.clone()
	return p
}

func clonePluginSnapshot(s PluginSnapshot) PluginSnapshot {
	result := PluginSnapshot{Generation: s.Generation}
	result.Plugins = make([]PluginDescriptor, len(s.Plugins))
	for i := range s.Plugins {
		result.Plugins[i] = clonePlugin(s.Plugins[i])
	}
	result.Diagnostics = append([]Diagnostic(nil), s.Diagnostics...)
	result.byID = make(map[string]int, len(s.byID))
	for key, value := range s.byID {
		result.byID[key] = value
	}
	return result
}

type PluginManager struct {
	mu       sync.RWMutex
	snapshot PluginSnapshot
}

func NewPluginManager() *PluginManager {
	return &PluginManager{snapshot: PluginSnapshot{byID: make(map[string]int)}}
}

func (m *PluginManager) Snapshot() PluginSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clonePluginSnapshot(m.snapshot)
}

// Reload discovers a complete coherent generation. The previous generation is
// left untouched until all roots have been scanned.
func (m *PluginManager) Reload(roots []PluginRoot, policy PluginPolicy) PluginSnapshot {
	ordered := append([]PluginRoot(nil), roots...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := pluginSourceRank(ordered[i].Source), pluginSourceRank(ordered[j].Source)
		if ri != rj {
			return ri < rj
		}
		return ordered[i].Path < ordered[j].Path
	})

	var discovered []PluginDescriptor
	var diagnostics []Diagnostic
	for _, root := range ordered {
		plugins, rootDiagnostics := discoverPluginRoot(root, policy)
		discovered = append(discovered, plugins...)
		diagnostics = append(diagnostics, rootDiagnostics...)
	}

	byID := make(map[string]int)
	merged := make([]PluginDescriptor, 0, len(discovered))
	for _, plugin := range discovered {
		key := strings.ToLower(plugin.CanonicalID)
		if index, exists := byID[key]; exists {
			loser := merged[index]
			diagnostics = append(diagnostics, Diagnostic{Path: loser.Root, Message: fmt.Sprintf("plugin %s shadowed by %s", loser.CanonicalID, plugin.Root)})
			merged[index] = plugin
			continue
		}
		byID[key] = len(merged)
		merged = append(merged, plugin)
	}

	m.mu.Lock()
	nextGeneration := m.snapshot.Generation + 1
	for i := range merged {
		merged[i].Generation = nextGeneration
	}
	next := PluginSnapshot{Generation: nextGeneration, Plugins: merged, Diagnostics: diagnostics, byID: byID}
	m.snapshot = next
	m.mu.Unlock()
	return clonePluginSnapshot(next)
}

func discoverPluginRoot(root PluginRoot, policy PluginPolicy) ([]PluginDescriptor, []Diagnostic) {
	abs, err := filepath.Abs(root.Path)
	if err != nil {
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("plugin root is not a directory")
		}
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}

	directManifest := filepath.Join(real, pluginManifestRelativePath)
	if _, err := os.Stat(directManifest); err == nil {
		plugin, diagnostics := loadPluginManifest(real, directManifest, root, policy)
		if plugin.CanonicalID == "" {
			return nil, diagnostics
		}
		return []PluginDescriptor{plugin}, diagnostics
	}

	var manifests []string
	var diagnostics []Diagnostic
	err = filepath.WalkDir(real, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: walkErr.Error()})
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !entry.IsDir() && entry.Name() == "plugin.json" && filepath.Base(filepath.Dir(path)) == ".agentx-plugin" {
			if len(manifests) >= maxPluginManifestsPerRoot {
				return errors.New("plugin discovery exceeds 10000 manifests")
			}
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{Path: root.Path, Message: err.Error()})
	}
	sort.Strings(manifests)
	plugins := make([]PluginDescriptor, 0, len(manifests))
	for _, manifest := range manifests {
		pluginRoot := filepath.Dir(filepath.Dir(manifest))
		plugin, manifestDiagnostics := loadPluginManifest(pluginRoot, manifest, root, policy)
		diagnostics = append(diagnostics, manifestDiagnostics...)
		if plugin.CanonicalID != "" {
			plugins = append(plugins, plugin)
		}
	}
	if len(manifests) == 0 {
		if root.Strict {
			diagnostics = append(diagnostics, Diagnostic{Path: real, Message: "missing .agentx-plugin/plugin.json"})
		} else {
			name := filepath.Base(real)
			plugin, syntheticDiagnostics := syntheticPlugin(real, name, root, policy)
			diagnostics = append(diagnostics, syntheticDiagnostics...)
			if plugin.CanonicalID != "" {
				plugins = append(plugins, plugin)
			}
		}
	}
	return plugins, diagnostics
}

func loadPluginManifest(pluginRoot, manifestPath string, root PluginRoot, policy PluginPolicy) (PluginDescriptor, []Diagnostic) {
	info, err := os.Stat(manifestPath)
	if err != nil {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: err.Error()}}
	}
	if info.Size() > maxPluginManifestBytes {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: "plugin manifest exceeds 1 MiB"}}
	}
	if !info.Mode().IsRegular() {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: "plugin manifest is not a regular file"}}
	}
	data, err := ReadRegularFile(manifestPath, maxPluginManifestBytes)
	if err != nil {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: err.Error()}}
	}
	manifest, diagnostics, err := decodePluginManifest(data, manifestPath)
	if err != nil {
		return PluginDescriptor{}, append(diagnostics, Diagnostic{Path: manifestPath, Message: err.Error()})
	}
	descriptor, policyDiagnostics := buildPluginDescriptor(pluginRoot, manifestPath, manifest, root, policy)
	return descriptor, append(diagnostics, policyDiagnostics...)
}

func syntheticPlugin(pluginRoot, name string, root PluginRoot, policy PluginPolicy) (PluginDescriptor, []Diagnostic) {
	manifest := PluginManifest{Name: name}
	descriptor, diagnostics := buildPluginDescriptor(pluginRoot, "", manifest, root, policy)
	if descriptor.CanonicalID != "" {
		diagnostics = append(diagnostics, Diagnostic{Path: pluginRoot, Message: "plugin manifest missing; using non-strict synthetic descriptor"})
	}
	return descriptor, diagnostics
}

func decodePluginManifest(data []byte, path string) (PluginManifest, []Diagnostic, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return PluginManifest{}, nil, fmt.Errorf("invalid plugin manifest JSON: %w", err)
	}
	known := map[string]bool{
		"name": true, "version": true, "description": true, "author": true,
		"homepage": true, "repository": true, "license": true, "keywords": true,
		"dependencies": true, "commands": true, "agents": true, "skills": true,
		"outputStyles": true, "output-styles": true, "hooks": true, "mcpServers": true,
		"lspServers": true,
	}
	var diagnostics []Diagnostic
	for key := range raw {
		if !known[key] {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: fmt.Sprintf("unknown manifest field %q ignored", key)})
		}
	}
	decodeString := func(key string, target *string) error {
		value, ok := raw[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(value, target); err != nil {
			return fmt.Errorf("%s must be a string", key)
		}
		*target = strings.TrimSpace(*target)
		return nil
	}
	var manifest PluginManifest
	for key, target := range map[string]*string{
		"name": &manifest.Name, "version": &manifest.Version,
		"description": &manifest.Description, "homepage": &manifest.Homepage,
		"repository": &manifest.Repository, "license": &manifest.License,
	} {
		if err := decodeString(key, target); err != nil {
			return PluginManifest{}, diagnostics, err
		}
	}
	if value, ok := raw["keywords"]; ok {
		if err := json.Unmarshal(value, &manifest.Keywords); err != nil {
			return PluginManifest{}, diagnostics, errors.New("keywords must be an array of strings")
		}
	}
	if value, ok := raw["dependencies"]; ok {
		dependencies, err := decodeDependencies(value)
		if err != nil {
			return PluginManifest{}, diagnostics, err
		}
		manifest.Dependencies = dependencies
	}
	manifest.Components = make(map[string][]string)
	for _, key := range []string{"commands", "agents", "skills", "outputStyles", "output-styles", "hooks", "mcpServers", "lspServers"} {
		if value, ok := raw[key]; ok {
			var pathValue string
			if err := json.Unmarshal(value, &pathValue); err == nil {
				manifest.Components[normalizeComponentKind(key)] = []string{pathValue}
				continue
			}
			var pathValues []string
			if err := json.Unmarshal(value, &pathValues); err == nil {
				manifest.Components[normalizeComponentKind(key)] = append(manifest.Components[normalizeComponentKind(key)], pathValues...)
				continue
			}
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: fmt.Sprintf("inline or malformed %s component is not activated by filesystem discovery", key)})
		}
	}
	return manifest, diagnostics, nil
}

func decodeDependencies(raw json.RawMessage) ([]string, error) {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return normalizeStringList(list), nil
	}
	var versions map[string]string
	if err := json.Unmarshal(raw, &versions); err == nil {
		list = make([]string, 0, len(versions))
		for dependency := range versions {
			list = append(list, dependency)
		}
		sort.Strings(list)
		return normalizeStringList(list), nil
	}
	return nil, errors.New("dependencies must be an array of identifiers or an object")
}

func normalizeStringList(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func buildPluginDescriptor(pluginRoot, manifestPath string, manifest PluginManifest, root PluginRoot, policy PluginPolicy) (PluginDescriptor, []Diagnostic) {
	if !pluginPartPattern.MatchString(manifest.Name) {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: "plugin name must contain only ASCII letters, digits, dot, underscore, and hyphen"}}
	}
	marketplace := strings.ToLower(strings.TrimSpace(root.Marketplace))
	if marketplace == "" {
		marketplace = "inline"
	}
	if !pluginPartPattern.MatchString(marketplace) {
		return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: "invalid marketplace identity"}}
	}
	name := strings.ToLower(manifest.Name)
	id := name + "@" + marketplace
	availability := FullyAvailable()
	availability.SessionEnabled = root.Enabled
	availability.SourceTrusted = root.Trusted
	if !root.Enabled {
		availability.Reasons = append(availability.Reasons, "disabled for this session")
	}
	if !root.Trusted {
		availability.Reasons = append(availability.Reasons, "source is not trusted")
	}
	if policy.DenyIDs[strings.ToLower(id)] || policy.DenyIDs[name] {
		availability.PolicyAllowed = false
		availability.Reasons = append(availability.Reasons, "blocked by plugin policy")
	}
	if policy.AllowIDs != nil && !policy.AllowIDs[strings.ToLower(id)] && !policy.AllowIDs[name] && root.Source != SourceManaged {
		availability.PolicyAllowed = false
		availability.Reasons = append(availability.Reasons, "not present in plugin allowlist")
	}
	if root.Source == SourceExplicit && policy.ManagedReservedNames[name] {
		availability.PolicyAllowed = false
		availability.Reasons = append(availability.Reasons, "plugin name is reserved by managed policy")
	}

	dependencies := make([]string, 0, len(manifest.Dependencies))
	for _, dependency := range manifest.Dependencies {
		qualified, err := qualifyPluginDependency(dependency, marketplace)
		if err != nil {
			return PluginDescriptor{}, []Diagnostic{{Path: manifestPath, Message: err.Error()}}
		}
		dependencies = append(dependencies, qualified)
	}
	sort.Strings(dependencies)
	components, componentDiagnostics := resolvePluginComponents(pluginRoot, manifest.Components)
	return PluginDescriptor{
		CanonicalID: id, Name: name, Marketplace: marketplace, Version: manifest.Version,
		Description: manifest.Description, Root: pluginRoot, Manifest: manifestPath,
		Source: root.Source, Scope: root.Scope, Dependencies: dependencies, Components: components,
		Availability: availability,
	}, componentDiagnostics
}

func normalizeComponentKind(kind string) string {
	switch kind {
	case "outputStyles":
		return "output-styles"
	case "mcpServers":
		return "mcp"
	case "lspServers":
		return "lsp"
	default:
		return kind
	}
}

func resolvePluginComponents(pluginRoot string, declared map[string][]string) (map[string][]string, []Diagnostic) {
	components := make(map[string][]string)
	rootCanonical, err := filepath.EvalSymlinks(pluginRoot)
	if err != nil {
		return components, []Diagnostic{{Path: pluginRoot, Message: "canonicalize plugin root: " + err.Error()}}
	}
	var componentDiagnostics []Diagnostic
	seen := make(map[string]map[string]bool)
	add := func(kind, candidate, unavailableMessage, escapeMessage string) {
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			message := unavailableMessage
			if message == "" {
				message = err.Error()
			}
			componentDiagnostics = append(componentDiagnostics, Diagnostic{Path: candidate, Message: message})
			return
		}
		if !pathWithinRoot(rootCanonical, canonical) {
			componentDiagnostics = append(componentDiagnostics, Diagnostic{Path: candidate, Message: escapeMessage})
			return
		}
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		if !seen[kind][canonical] {
			seen[kind][canonical] = true
			components[kind] = append(components[kind], canonical)
		}
	}
	standard := []struct {
		kind, relative string
	}{
		{kind: "commands", relative: "commands"},
		{kind: "agents", relative: "agents"},
		{kind: "skills", relative: "skills"},
		{kind: "output-styles", relative: "output-styles"},
		{kind: "hooks", relative: filepath.Join("hooks", "hooks.json")},
	}
	for _, entry := range standard {
		candidate := filepath.Join(rootCanonical, entry.relative)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			componentDiagnostics = append(componentDiagnostics, Diagnostic{Path: candidate, Message: err.Error()})
			continue
		}
		add(entry.kind, candidate, fmt.Sprintf("standard plugin component %s is unavailable", entry.kind), fmt.Sprintf("standard plugin component %s symlink escapes plugin root", entry.kind))
	}
	declaredKinds := make([]string, 0, len(declared))
	for kind := range declared {
		declaredKinds = append(declaredKinds, kind)
	}
	sort.Strings(declaredKinds)
	for _, kind := range declaredKinds {
		relatives := declared[kind]
		for _, relative := range relatives {
			if !strings.HasPrefix(relative, "./") {
				componentDiagnostics = append(componentDiagnostics, Diagnostic{Path: pluginRoot, Message: fmt.Sprintf("plugin component %s path must begin with ./", kind)})
				continue
			}
			candidate := filepath.Clean(filepath.Join(rootCanonical, filepath.FromSlash(strings.TrimPrefix(relative, "./"))))
			rel, err := filepath.Rel(rootCanonical, candidate)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				componentDiagnostics = append(componentDiagnostics, Diagnostic{Path: pluginRoot, Message: fmt.Sprintf("plugin component %s path escapes plugin root", kind)})
				continue
			}
			add(kind, candidate, fmt.Sprintf("declared plugin component %s is unavailable", kind), fmt.Sprintf("plugin component %s symlink escapes plugin root", kind))
		}
	}
	for kind := range components {
		sort.Strings(components[kind])
	}
	return components, componentDiagnostics
}

func qualifyPluginDependency(dependency, marketplace string) (string, error) {
	dependency = strings.ToLower(strings.TrimSpace(dependency))
	if dependency == "" {
		return "", errors.New("plugin dependency cannot be empty")
	}
	if !strings.Contains(dependency, "@") {
		dependency += "@" + marketplace
	}
	parts := strings.Split(dependency, "@")
	if len(parts) != 2 || !pluginPartPattern.MatchString(parts[0]) || !pluginPartPattern.MatchString(parts[1]) {
		return "", fmt.Errorf("invalid plugin dependency %q", dependency)
	}
	return dependency, nil
}

func pluginSourceRank(source Source) int {
	switch source {
	case SourceBundled:
		return 0
	case SourcePlugin:
		return 1
	case SourceManaged:
		return 2
	case SourceUser:
		return 3
	case SourceProject:
		return 4
	case SourceExplicit:
		return 5
	default:
		return -1
	}
}

// Resolve returns dependencies before dependents. It rejects a cycle or
// missing/unavailable dependency without returning a partially usable order.
func (s PluginSnapshot) Resolve(rootIDs ...string) ([]PluginDescriptor, error) {
	state := make(map[string]uint8)
	var stack []string
	var order []PluginDescriptor
	var visit func(string) error
	visit = func(id string) error {
		id = strings.ToLower(strings.TrimSpace(id))
		switch state[id] {
		case 2:
			return nil
		case 1:
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
		index, ok := s.byID[id]
		if !ok {
			return fmt.Errorf("plugin dependency %s not found", id)
		}
		plugin := s.Plugins[index]
		if !plugin.Availability.Usable() {
			return fmt.Errorf("plugin dependency %s is unavailable: %s", id, strings.Join(plugin.Availability.Reasons, "; "))
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dependency := range plugin.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
		order = append(order, clonePlugin(plugin))
		return nil
	}
	for _, id := range rootIDs {
		if !strings.Contains(id, "@") {
			return nil, fmt.Errorf("root plugin identity %q must include marketplace", id)
		}
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}
