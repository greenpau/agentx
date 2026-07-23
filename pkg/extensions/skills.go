// Package extensions discovers and snapshots session-scoped extension
// descriptors. A turn consumes one immutable generation so a concurrent reload
// cannot mix old prompts with new invocation behavior.
package extensions

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxSkillBytes = 1 << 20

type Source string

const (
	SourceBundled  Source = "bundled"
	SourcePlugin   Source = "plugin"
	SourceUser     Source = "user"
	SourceProject  Source = "project"
	SourceManaged  Source = "managed"
	SourceExplicit Source = "explicit"
)

type Root struct {
	Path   string
	Source Source
	Owner  string
}

type Availability struct {
	Included          bool   `json:"included"`
	FeatureEnabled    bool   `json:"feature_enabled"`
	Eligible          bool   `json:"eligible"`
	PlatformSupported bool   `json:"platform_supported"`
	PolicyAllowed     bool   `json:"policy_allowed"`
	Healthy           bool   `json:"healthy"`
	Reason            string `json:"reason,omitempty"`
}

func Available() Availability {
	return Availability{Included: true, FeatureEnabled: true, Eligible: true, PlatformSupported: true, PolicyAllowed: true, Healthy: true}
}

func (a Availability) Usable() bool {
	return a.Included && a.FeatureEnabled && a.Eligible && a.PlatformSupported && a.PolicyAllowed && a.Healthy
}

type Skill struct {
	CanonicalName          string
	DisplayName            string
	Description            string
	WhenToUse              string
	Path                   string
	DirectoryIdentity      string
	Source                 Source
	Owner                  string
	AllowedTools           []string
	ArgumentHint           string
	Model                  string
	Effort                 string
	Context                string
	Agent                  string
	Version                string
	DisableModelInvocation bool
	UserInvocable          bool
	Body                   string
	Availability           Availability
}

func (s Skill) Summary() string {
	availability := "available"
	if !s.Availability.Usable() {
		availability = "unavailable: " + s.Availability.Reason
	}
	return fmt.Sprintf("- %s: %s [%s; source=%s]", s.CanonicalName, s.Description, availability, s.Source)
}

type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Snapshot struct {
	Generation  uint64
	Skills      []Skill
	Diagnostics []Diagnostic
	byName      map[string]int
}

func (s Snapshot) Lookup(name string, forModel bool) (Skill, bool) {
	index, ok := s.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Skill{}, false
	}
	skill := s.Skills[index]
	if !skill.Availability.Usable() || forModel && skill.DisableModelInvocation {
		return Skill{}, false
	}
	return skill, true
}

type Manager struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func NewManager() *Manager {
	return &Manager{snapshot: Snapshot{byName: make(map[string]int)}}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSnapshot(m.snapshot)
}

func (m *Manager) Reload(roots []Root) Snapshot {
	ordered := append([]Root(nil), roots...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if sourceRank(ordered[i].Source) != sourceRank(ordered[j].Source) {
			return sourceRank(ordered[i].Source) < sourceRank(ordered[j].Source)
		}
		return ordered[i].Path < ordered[j].Path
	})
	var all []Skill
	var diagnostics []Diagnostic
	for _, root := range ordered {
		discovered, diag := discoverRoot(root)
		all = append(all, discovered...)
		diagnostics = append(diagnostics, diag...)
	}
	// Later/higher-precedence descriptors replace earlier entries without
	// changing the deterministic position of the winning name.
	byName := make(map[string]int)
	merged := make([]Skill, 0, len(all))
	for _, skill := range all {
		key := strings.ToLower(skill.CanonicalName)
		if index, exists := byName[key]; exists {
			merged[index] = skill
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, skill)
	}
	m.mu.Lock()
	next := Snapshot{Generation: m.snapshot.Generation + 1, Skills: merged, Diagnostics: diagnostics, byName: byName}
	m.snapshot = next
	m.mu.Unlock()
	return cloneSnapshot(next)
}

func cloneSnapshot(source Snapshot) Snapshot {
	result := Snapshot{Generation: source.Generation}
	result.Skills = make([]Skill, len(source.Skills))
	for index, skill := range source.Skills {
		skill.AllowedTools = append([]string(nil), skill.AllowedTools...)
		result.Skills[index] = skill
	}
	result.Diagnostics = append([]Diagnostic(nil), source.Diagnostics...)
	result.byName = make(map[string]int, len(source.byName))
	for key, value := range source.byName {
		result.byName[key] = value
	}
	return result
}

func discoverRoot(root Root) ([]Skill, []Diagnostic) {
	abs, err := filepath.Abs(root.Path)
	if err != nil {
		return nil, []Diagnostic{{Path: root.Path, Message: err.Error()}}
	}
	var skills []Skill
	var diagnostics []Diagnostic
	err = filepath.WalkDir(abs, func(pathname string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: pathname, Message: walkErr.Error()})
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: pathname, Message: err.Error()})
			return nil
		}
		if info.Size() > maxSkillBytes {
			diagnostics = append(diagnostics, Diagnostic{Path: pathname, Message: "skill exceeds 1 MiB"})
			return nil
		}
		skill, err := parseSkill(pathname, abs, root)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: pathname, Message: err.Error()})
			return nil
		}
		skills = append(skills, skill)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		diagnostics = append(diagnostics, Diagnostic{Path: root.Path, Message: err.Error()})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].DirectoryIdentity < skills[j].DirectoryIdentity })
	return skills, diagnostics
}

func parseSkill(pathname, rootPath string, root Root) (Skill, error) {
	data, err := ReadRegularFile(pathname, maxSkillBytes)
	if err != nil {
		return Skill{}, err
	}
	meta, body, err := parseFrontmatter(string(data))
	if err != nil {
		return Skill{}, err
	}
	dir := filepath.Dir(pathname)
	rel, err := filepath.Rel(rootPath, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return Skill{}, errors.New("skill directory identity is outside discovery root")
	}
	canonical := filepath.Base(dir)
	display := scalar(meta, "name")
	if display == "" {
		display = canonical
	}
	userInvocable := true
	if raw := scalar(meta, "user-invocable"); raw != "" {
		userInvocable, err = strconv.ParseBool(raw)
		if err != nil {
			return Skill{}, errors.New("user-invocable must be true or false")
		}
	}
	disableModel, err := boolValue(meta, "disable-model-invocation")
	if err != nil {
		return Skill{}, err
	}
	return Skill{
		CanonicalName: canonical, DisplayName: display, Description: scalar(meta, "description"),
		WhenToUse: scalar(meta, "when_to_use"), Path: pathname, DirectoryIdentity: filepath.ToSlash(rel),
		Source: root.Source, Owner: root.Owner, AllowedTools: listValue(meta, "allowed-tools"),
		ArgumentHint: scalar(meta, "argument-hint"), Model: scalar(meta, "model"), Effort: scalar(meta, "effort"),
		Context: scalar(meta, "context"), Agent: scalar(meta, "agent"), Version: scalar(meta, "version"),
		DisableModelInvocation: disableModel, UserInvocable: userInvocable, Body: strings.TrimSpace(body), Availability: Available(),
	}, nil
}

func parseFrontmatter(text string) (map[string]string, string, error) {
	meta := make(map[string]string)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return meta, text, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), maxSkillBytes)
	scanner.Scan() // opening marker
	var consumed int
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		consumed += len(line) + 1
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, "", fmt.Errorf("invalid frontmatter line %q", line)
		}
		meta[strings.ToLower(strings.TrimSpace(key))] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	if !closed {
		return nil, "", errors.New("unterminated frontmatter")
	}
	// Include the opening marker and the closing line in the byte offset. CRLF
	// is normalized only for locating the body, not for body content semantics.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	index := strings.Index(normalized[4:], "\n---\n")
	if index < 0 {
		if strings.HasSuffix(normalized, "\n---") {
			return meta, "", nil
		}
		return nil, "", errors.New("unterminated frontmatter")
	}
	_ = consumed
	return meta, normalized[4+index+5:], nil
}

func scalar(meta map[string]string, key string) string { return strings.TrimSpace(meta[key]) }

func boolValue(meta map[string]string, key string) (bool, error) {
	raw := scalar(meta, key)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value, nil
}

func listValue(meta map[string]string, key string) []string {
	raw := strings.TrimSpace(meta[key])
	if raw == "" {
		return nil
	}
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := unquote(strings.TrimSpace(part)); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}

func sourceRank(source Source) int {
	switch source {
	case SourceBundled:
		return 0
	case SourcePlugin:
		return 1
	case SourceUser:
		return 2
	case SourceProject:
		return 3
	case SourceExplicit:
		return 4
	case SourceManaged:
		return 5
	default:
		return -1
	}
}

// Expand substitutes literal skill arguments. It never interprets the result
// as a shell program; execution remains the caller's explicit responsibility.
func Expand(skill Skill, arguments []string) string {
	result := strings.ReplaceAll(skill.Body, "$ARGUMENTS", strings.Join(arguments, " "))
	// Replace longest indices first so $10 is not partially consumed as $1.
	for index := len(arguments); index > 0; index-- {
		result = strings.ReplaceAll(result, "$"+strconv.Itoa(index), arguments[index-1])
	}
	return result
}
