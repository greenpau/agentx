package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// validateState is the single durable-state gate used for both recovery and
// commits. It returns a deep-cloned work graph whose derived dependents are
// rebuilt from blockers, so callers never persist caller-owned aliases or
// trust redundant relationship data from disk.
func (m *Manager) validateState(tasks map[ID]Record, work map[ID]WorkItem, todos []Todo) (map[ID]WorkItem, error) {
	if err := m.validateOutputDirectory(); err != nil {
		return nil, err
	}
	if len(tasks) > maximumTaskRecords {
		return nil, fmt.Errorf("shell task count exceeds %d", maximumTaskRecords)
	}
	if len(work) > maximumWorkRecords {
		return nil, fmt.Errorf("work item count exceeds %d", maximumWorkRecords)
	}
	if err := validateTodos(todos); err != nil {
		return nil, err
	}
	for id, record := range tasks {
		if err := m.validateRecord(id, record); err != nil {
			return nil, err
		}
	}

	normalized := make(map[ID]WorkItem, len(work))
	for id, item := range work {
		if err := validateWorkItemIdentity(id, item); err != nil {
			return nil, err
		}
		item = cloneWork(item)
		item.Dependents = nil
		normalized[id] = item
	}
	for id, item := range normalized {
		seen := make(map[ID]struct{}, len(item.Blockers))
		for _, blocker := range item.Blockers {
			if blocker == id {
				return nil, fmt.Errorf("work item %s cannot block itself", id)
			}
			if !validPersistedID(blocker, 't') {
				return nil, fmt.Errorf("work item %s has invalid blocker %q", id, blocker)
			}
			if _, exists := normalized[blocker]; !exists {
				return nil, fmt.Errorf("work item %s has unknown blocker %s", id, blocker)
			}
			if _, duplicate := seen[blocker]; duplicate {
				return nil, fmt.Errorf("work item %s has duplicate blocker %s", id, blocker)
			}
			seen[blocker] = struct{}{}
		}
	}
	if dependencyCycle(normalized) {
		return nil, ErrDependencyCycle
	}
	for id, item := range normalized {
		for _, blocker := range item.Blockers {
			parent := normalized[blocker]
			parent.Dependents = append(parent.Dependents, id)
			normalized[blocker] = parent
		}
	}
	for id, item := range normalized {
		sort.Slice(item.Dependents, func(i, j int) bool { return item.Dependents[i] < item.Dependents[j] })
		normalized[id] = item
	}
	return normalized, nil
}

func (m *Manager) validateOutputDirectory() error {
	return m.verifyOwnedDirectories()
}

func (m *Manager) verifyOwnedDirectories() error {
	if m.rootOwner == nil || m.outputOwner == nil {
		return errors.New("task directory identity is unavailable")
	}
	if err := m.rootOwner.Verify(); err != nil {
		return fmt.Errorf("verify task root: %w", err)
	}
	if err := m.outputOwner.Verify(); err != nil {
		return fmt.Errorf("verify task output directory: %w", err)
	}
	if filepath.Clean(m.root) != m.rootOwner.Path() || filepath.Clean(m.outputDir) != m.outputOwner.Path() || filepath.Dir(m.outputOwner.Path()) != m.rootOwner.Path() {
		return errors.New("task directory layout does not match its identities")
	}
	return nil
}

func (m *Manager) validateRecord(id ID, record Record) error {
	if !validPersistedID(id, 'b') || record.ID != id || record.Version != stateVersion || record.Kind != KindShell || !validTaskStatus(record.Status) {
		return fmt.Errorf("invalid persisted shell task %q", id)
	}
	if strings.TrimSpace(record.Command) == "" || strings.TrimSpace(record.Description) == "" || len(record.Description) > maximumStateString || len(record.Command) > maximumStateString || len(record.Error) > maximumStateString || len(record.OutputWarning) > maximumStateString || len(record.Owner) > maximumStateString || len(record.ToolUseID) > maximumToolUseID || record.StartedAt.IsZero() {
		return fmt.Errorf("persisted shell task %s has invalid or oversized fields", id)
	}
	expected := filepath.Join(m.outputDir, string(id)+".log")
	if filepath.Clean(record.OutputPath) != expected {
		return fmt.Errorf("persisted shell task %s has an unsafe output path", id)
	}
	if record.Status.Terminal() {
		if record.EndedAt == nil {
			return fmt.Errorf("terminal shell task %s has no end time", id)
		}
		if record.EndedAt.Before(record.StartedAt) {
			return fmt.Errorf("terminal shell task %s ends before it starts", id)
		}
	} else if record.EndedAt != nil || record.ExitCode != nil || record.OutputIncomplete || record.OutputWarning != "" {
		return fmt.Errorf("nonterminal shell task %s contains terminal fields", id)
	}
	if record.Status == StatusCompleted && (record.ExitCode == nil || *record.ExitCode != 0 || record.Error != "") {
		return fmt.Errorf("completed shell task %s has an invalid result", id)
	}
	if info, err := inspectOutputFile(expected, m.outputCap); err == nil {
		if identity, ok := m.outputIdentity[id]; ok && !os.SameFile(identity, info) {
			return fmt.Errorf("shell task %s output identity changed", id)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect shell task %s output: %w", id, err)
	}
	return nil
}

func inspectOutputFile(path string, outputCap int64) (os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > outputCap+int64(len(outputTruncMarker)) {
		return nil, errors.New("task output is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("task output changed while opening")
	}
	if after.Size() > outputCap+int64(len(outputTruncMarker)) {
		return nil, errors.New("task output exceeded its configured bound")
	}
	links, err := openedFileLinkCount(file, after)
	if err != nil {
		return nil, err
	}
	if links != 1 {
		return nil, errors.New("task output must have exactly one filesystem link")
	}
	return after, nil
}

func validateWorkItemIdentity(id ID, item WorkItem) error {
	if !validPersistedID(id, 't') || item.ID != id || item.Version != stateVersion || !validWorkStatus(item.Status) || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid persisted work item %q", id)
	}
	if item.UpdatedAt.Before(item.CreatedAt) {
		return fmt.Errorf("work item %s was updated before it was created", id)
	}
	if strings.TrimSpace(item.Subject) == "" || strings.TrimSpace(item.Description) == "" || strings.TrimSpace(item.ActiveForm) == "" || len(item.Subject) > maximumStateString || len(item.Description) > maximumStateString || len(item.ActiveForm) > maximumStateString || len(item.Owner) > maximumStateString || len(item.Metadata) > maximumMetadata || len(item.Blockers) > maximumWorkRecords || len(item.Dependents) > maximumWorkRecords {
		return fmt.Errorf("persisted work item %s has invalid or oversized fields", id)
	}
	for key, value := range item.Metadata {
		if len(key) > maximumMetadataKey || len(value) > maximumMetadataVal {
			return fmt.Errorf("persisted work item %s has oversized metadata", id)
		}
	}
	return nil
}

func validateTodos(todos []Todo) error {
	if len(todos) > maximumTodos {
		return fmt.Errorf("todo count exceeds %d", maximumTodos)
	}
	for index, todo := range todos {
		if strings.TrimSpace(todo.Content) == "" || strings.TrimSpace(todo.ActiveForm) == "" || !validWorkStatus(todo.Status) || len(todo.Content) > maximumStateString || len(todo.ActiveForm) > maximumStateString {
			return fmt.Errorf("invalid or oversized todo at index %d", index)
		}
	}
	return nil
}

func validateShellSpec(spec ShellSpec) error {
	if strings.TrimSpace(spec.Command) == "" {
		return errors.New("shell command is required")
	}
	if len(spec.Command) > maximumStateString {
		return fmt.Errorf("shell command exceeds %d bytes", maximumStateString)
	}
	if len(spec.Description) > maximumStateString {
		return fmt.Errorf("shell description exceeds %d bytes", maximumStateString)
	}
	if len(spec.ToolUseID) > maximumToolUseID {
		return fmt.Errorf("shell tool-use identifier exceeds %d bytes", maximumToolUseID)
	}
	if len(spec.Owner) > maximumStateString {
		return fmt.Errorf("shell owner exceeds %d bytes", maximumStateString)
	}
	return nil
}

func boundedStateString(value string) string {
	if len(value) <= maximumStateString {
		return value
	}
	return value[:maximumStateString]
}

func newWorkItem(id ID, subject, description, activeForm string, metadata map[string]string, now time.Time) WorkItem {
	return WorkItem{
		Version: stateVersion, ID: id, Subject: subject, Description: description,
		ActiveForm: activeForm, Status: WorkPending, Metadata: cloneStringMap(metadata),
		CreatedAt: now, UpdatedAt: now,
	}
}
