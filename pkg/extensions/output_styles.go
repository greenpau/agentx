package extensions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	maxOutputStyleBytes = 1 << 20
	maxOutputStyleFiles = 10_000
)

type OutputStyleRoot struct {
	Path       string
	Source     Source
	PluginName string
}

type OutputStyle struct {
	CanonicalName          string                `json:"canonical_name"`
	Description            string                `json:"description,omitempty"`
	Prompt                 string                `json:"prompt"`
	KeepCodingInstructions bool                  `json:"keep_coding_instructions"`
	ForceForPlugin         bool                  `json:"force_for_plugin"`
	Source                 Source                `json:"source"`
	PluginName             string                `json:"plugin_name,omitempty"`
	Path                   string                `json:"path,omitempty"`
	Availability           ExtensionAvailability `json:"availability"`
	Generation             uint64                `json:"generation"`
}

func cloneOutputStyle(style OutputStyle) OutputStyle {
	style.Availability = style.Availability.clone()
	return style
}

type OutputStyleSnapshot struct {
	Generation  uint64        `json:"generation"`
	Styles      []OutputStyle `json:"styles"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
	byName      map[string]int
}

func (s OutputStyleSnapshot) Lookup(name string) (OutputStyle, bool) {
	index, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return OutputStyle{}, false
	}
	style := cloneOutputStyle(s.Styles[index])
	return style, style.Availability.Usable()
}

func cloneOutputStyleSnapshot(source OutputStyleSnapshot) OutputStyleSnapshot {
	result := OutputStyleSnapshot{Generation: source.Generation}
	result.Styles = make([]OutputStyle, len(source.Styles))
	for index := range source.Styles {
		result.Styles[index] = cloneOutputStyle(source.Styles[index])
	}
	result.Diagnostics = append([]Diagnostic(nil), source.Diagnostics...)
	result.byName = make(map[string]int, len(source.byName))
	for key, value := range source.byName {
		result.byName[key] = value
	}
	return result
}

type OutputStyleManager struct {
	mu       sync.RWMutex
	snapshot OutputStyleSnapshot
}

func NewOutputStyleManager() *OutputStyleManager {
	return &OutputStyleManager{snapshot: OutputStyleSnapshot{byName: make(map[string]int)}}
}

func (m *OutputStyleManager) Snapshot() OutputStyleSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneOutputStyleSnapshot(m.snapshot)
}

func (m *OutputStyleManager) Reload(roots []OutputStyleRoot) OutputStyleSnapshot {
	ordered := append([]OutputStyleRoot(nil), roots...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := outputStyleSourceRank(ordered[i].Source), outputStyleSourceRank(ordered[j].Source)
		if ri != rj {
			return ri < rj
		}
		if ordered[i].PluginName != ordered[j].PluginName {
			return ordered[i].PluginName < ordered[j].PluginName
		}
		return ordered[i].Path < ordered[j].Path
	})

	styles := builtinOutputStyles()
	var diagnostics []Diagnostic
	for _, root := range ordered {
		discovered, rootDiagnostics := discoverOutputStyleRoot(root)
		styles = append(styles, discovered...)
		diagnostics = append(diagnostics, rootDiagnostics...)
	}

	merged := make([]OutputStyle, 0, len(styles))
	byName := make(map[string]int)
	for _, style := range styles {
		key := strings.ToLower(style.CanonicalName)
		if index, exists := byName[key]; exists {
			loser := merged[index]
			diagnostics = append(diagnostics, Diagnostic{Path: loser.Path, Message: fmt.Sprintf("output style %s shadowed by %s", loser.CanonicalName, displayStyleSource(style))})
			merged[index] = style
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, style)
	}

	m.mu.Lock()
	generation := m.snapshot.Generation + 1
	for i := range merged {
		merged[i].Generation = generation
	}
	next := OutputStyleSnapshot{Generation: generation, Styles: merged, Diagnostics: diagnostics, byName: byName}
	m.snapshot = next
	m.mu.Unlock()
	return cloneOutputStyleSnapshot(next)
}

func builtinOutputStyles() []OutputStyle {
	available := FullyAvailable()
	return []OutputStyle{
		{CanonicalName: "default", Description: "Default coding assistant response style", KeepCodingInstructions: true, Source: SourceBundled, Availability: available},
		{CanonicalName: "Explanatory", Description: "Explain implementation choices and important behavior", Prompt: "Explain important implementation choices and non-obvious behavior clearly while completing the task.", KeepCodingInstructions: true, Source: SourceBundled, Availability: available},
		{CanonicalName: "Learning", Description: "Teach through the implementation", Prompt: "Help the user learn from the work by connecting changes to the underlying concepts without losing focus on completion.", KeepCodingInstructions: true, Source: SourceBundled, Availability: available},
	}
}

func discoverOutputStyleRoot(root OutputStyleRoot) ([]OutputStyle, []Diagnostic) {
	abs, err := filepath.Abs(root.Path)
	if err != nil {
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}
	files, diagnostics := collectOutputStyleFiles(real)
	sort.Strings(files)
	styles := make([]OutputStyle, 0, len(files))
	seenCanonicalPaths := make(map[string]bool)
	for _, path := range files {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: err.Error()})
			continue
		}
		if seenCanonicalPaths[canonical] {
			continue
		}
		seenCanonicalPaths[canonical] = true
		style, styleDiagnostics, err := parseOutputStyle(canonical, root)
		diagnostics = append(diagnostics, styleDiagnostics...)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: err.Error()})
			continue
		}
		styles = append(styles, style)
	}
	return styles, diagnostics
}

func collectOutputStyleFiles(root string) ([]string, []Diagnostic) {
	var files []string
	var diagnostics []Diagnostic
	visitedDirectories := make(map[string]bool)
	var visit func(string)
	visit = func(path string) {
		if len(files) >= maxOutputStyleFiles {
			return
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: err.Error()})
			return
		}
		if !pathWithinRoot(root, canonical) {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: "output-style symlink escapes discovery root"})
			return
		}
		info, err := os.Stat(canonical)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Message: err.Error()})
			return
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(canonical), ".md") {
				files = append(files, canonical)
			}
			return
		}
		if visitedDirectories[canonical] {
			return
		}
		visitedDirectories[canonical] = true
		entries, err := os.ReadDir(canonical)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: canonical, Message: err.Error()})
			return
		}
		// os.ReadDir is lexical, but retain an explicit sort as part of the
		// registry contract rather than relying on that implementation detail.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			visit(filepath.Join(canonical, entry.Name()))
		}
	}
	visit(root)
	if len(files) >= maxOutputStyleFiles {
		diagnostics = append(diagnostics, Diagnostic{Path: root, Message: "output-style discovery reached 10000-file limit"})
	}
	sort.Strings(files)
	return files, diagnostics
}

func parseOutputStyle(path string, root OutputStyleRoot) (OutputStyle, []Diagnostic, error) {
	info, err := os.Stat(path)
	if err != nil {
		return OutputStyle{}, nil, err
	}
	if info.Size() > maxOutputStyleBytes {
		return OutputStyle{}, nil, errors.New("output style exceeds 1 MiB")
	}
	if !info.Mode().IsRegular() {
		return OutputStyle{}, nil, errors.New("output style is not a regular file")
	}
	data, err := ReadRegularFile(path, maxOutputStyleBytes)
	if err != nil {
		return OutputStyle{}, nil, err
	}
	meta, body, err := parseFrontmatter(string(data))
	if err != nil {
		return OutputStyle{}, nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return OutputStyle{}, nil, errors.New("output style prompt is empty")
	}
	name := scalar(meta, "name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") {
		return OutputStyle{}, nil, errors.New("invalid output style name")
	}
	keep, err := boolValue(meta, "keep-coding-instructions")
	if err != nil {
		return OutputStyle{}, nil, err
	}
	force, err := boolValue(meta, "force-for-plugin")
	if err != nil {
		return OutputStyle{}, nil, err
	}
	var diagnostics []Diagnostic
	pluginName := strings.ToLower(strings.TrimSpace(root.PluginName))
	if root.Source == SourcePlugin {
		if !pluginPartPattern.MatchString(pluginName) {
			return OutputStyle{}, nil, errors.New("plugin output style requires a valid plugin name")
		}
		name = pluginName + ":" + name
	} else if force {
		force = false
		diagnostics = append(diagnostics, Diagnostic{Path: path, Message: "force-for-plugin ignored outside a plugin descriptor"})
	}
	return OutputStyle{
		CanonicalName: name, Description: scalar(meta, "description"), Prompt: body,
		KeepCodingInstructions: keep, ForceForPlugin: force, Source: root.Source,
		PluginName: pluginName, Path: path, Availability: FullyAvailable(),
	}, diagnostics, nil
}

func outputStyleSourceRank(source Source) int {
	switch source {
	case SourceBundled:
		return 0
	case SourcePlugin:
		return 1
	case SourceUser:
		return 2
	case SourceProject, SourceExplicit:
		return 3
	case SourceManaged:
		return 4
	default:
		return -1
	}
}

func displayStyleSource(style OutputStyle) string {
	if style.Path != "" {
		return style.Path
	}
	return string(style.Source)
}

type OutputStyleSelection struct {
	Style                  *OutputStyle `json:"style,omitempty"`
	KeepCodingInstructions bool         `json:"keep_coding_instructions"`
	Diagnostics            []Diagnostic `json:"diagnostics,omitempty"`
	Generation             uint64       `json:"generation"`
}

// SelectOutputStyle freezes forced-plugin selection before configured style.
func SelectOutputStyle(snapshot OutputStyleSnapshot, configured string) OutputStyleSelection {
	selection := OutputStyleSelection{KeepCodingInstructions: true, Generation: snapshot.Generation}
	var forced []OutputStyle
	for _, style := range snapshot.Styles {
		if style.Source == SourcePlugin && style.ForceForPlugin && style.Availability.Usable() {
			forced = append(forced, cloneOutputStyle(style))
		}
	}
	if len(forced) > 0 {
		selection.Style = &forced[0]
		selection.KeepCodingInstructions = forced[0].KeepCodingInstructions
		if len(forced) > 1 {
			names := make([]string, len(forced))
			for index := range forced {
				names[index] = forced[index].CanonicalName
			}
			selection.Diagnostics = append(selection.Diagnostics, Diagnostic{Message: "multiple plugin output styles forced; selected " + names[0] + " from: " + strings.Join(names, ", ")})
		}
		return selection
	}
	configured = strings.TrimSpace(configured)
	if configured == "" || strings.EqualFold(configured, "default") {
		return selection
	}
	style, ok := snapshot.Lookup(configured)
	if !ok {
		selection.Diagnostics = append(selection.Diagnostics, Diagnostic{Message: fmt.Sprintf("configured output style %q is unavailable; using default", configured)})
		return selection
	}
	selection.Style = &style
	selection.KeepCodingInstructions = style.KeepCodingInstructions
	return selection
}

func (s OutputStyleSelection) PromptSection() string {
	if s.Style == nil || strings.TrimSpace(s.Style.Prompt) == "" {
		return ""
	}
	return "# Output Style: " + s.Style.CanonicalName + "\n" + strings.TrimSpace(s.Style.Prompt)
}
