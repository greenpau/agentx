package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/task"
	"github.com/greenpau/agentx/pkg/tool"
)

type authorizationEvent struct {
	Tool           string
	ToolUseID      string
	Classification permission.Classification
	Paths          []permission.PathAccess
	Decision       permission.Decision
}

type recordingEvaluator struct {
	evaluator *permission.Evaluator

	mu     sync.Mutex
	events []authorizationEvent
}

type hostileSemanticError struct {
	calls *atomic.Int32
}

func (e *hostileSemanticError) invoked() {
	e.calls.Add(1)
	panic("hostile semantic error callback must not run")
}

func (e *hostileSemanticError) Error() string {
	e.invoked()
	return ""
}

func (e *hostileSemanticError) Is(error) bool {
	e.invoked()
	return false
}

func (e *hostileSemanticError) As(any) bool {
	e.invoked()
	return false
}

func (e *hostileSemanticError) Unwrap() error {
	e.invoked()
	return nil
}

func (r *recordingEvaluator) Authorize(ctx context.Context, request permission.Request, rebuild permission.Rebuild) (permission.Decision, error) {
	decision, err := r.evaluator.Authorize(ctx, request, rebuild)
	r.mu.Lock()
	r.events = append(r.events, authorizationEvent{
		Tool:           request.Tool,
		ToolUseID:      request.ToolUseID,
		Classification: request.Classification,
		Paths:          append([]permission.PathAccess(nil), request.Paths...),
		Decision:       decision,
	})
	r.mu.Unlock()
	return decision, err
}

func (r *recordingEvaluator) event(t *testing.T, toolUseID string) authorizationEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.ToolUseID == toolUseID {
			return event
		}
	}
	t.Fatalf("no authorization event for %q; events=%+v", toolUseID, r.events)
	return authorizationEvent{}
}

func parseRule(t *testing.T, raw string) permission.Rule {
	t.Helper()
	rule, err := permission.ParseRule(raw, permission.EffectAllow, "integration", false)
	if err != nil {
		t.Fatalf("parse permission rule %q: %v", raw, err)
	}
	return rule
}

func executeCore(t *testing.T, executor *tool.Executor, id, name, canonicalName string, input any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("encode %s input: %v", name, err)
	}
	result := executor.Execute(t.Context(), tool.Request{
		ID: id, Name: name, Input: raw, AssistantID: "assistant-integration",
	})
	if result.ToolUseID != id {
		t.Fatalf("%s correlation ID = %q, want %q: %+v", name, result.ToolUseID, id, result)
	}
	if result.Name != canonicalName {
		t.Fatalf("%s canonical result name = %q, want %q: %+v", name, result.Name, canonicalName, result)
	}
	return result
}

func requireSuccess(t *testing.T, result tool.Result) {
	t.Helper()
	if result.IsError {
		t.Fatalf("%s/%s failed with %s: %s", result.ToolUseID, result.Name, result.Code, result.Content)
	}
}

func TestCoreRegistryExecutorFilesystemMatrix(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("/bin/bash is unavailable")
	}

	workspace := t.TempDir()
	existing := filepath.Join(workspace, "existing.txt")
	unread := filepath.Join(workspace, "unread.txt")
	created := filepath.Join(workspace, "created.txt")
	if err := os.WriteFile(existing, []byte("alpha needle\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unread, []byte("unread\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := tool.NewCoreRegistry(tool.CoreOptions{
		Workspace: workspace,
		Shell:     "/bin/bash",
		Environment: []string{
			"PATH=/bin:/usr/bin",
			"LANG=C",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace: workspace,
		Mode:      permission.ModeAcceptEdits,
		Rules: []permission.Rule{
			parseRule(t, "Bash(printf matrix-shell)"),
		},
		PromptSuppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &recordingEvaluator{evaluator: evaluator}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	read := executeCore(t, executor, "fs-read-1", "Read", "Read", map[string]any{
		"file_path": existing,
	})
	requireSuccess(t, read)
	if !strings.Contains(read.Content, "1→alpha needle") {
		t.Fatalf("Read did not return numbered source: %q", read.Content)
	}

	unreadWrite := executeCore(t, executor, "fs-write-unread", "Write", "Write", map[string]any{
		"file_path": unread,
		"content":   "must-not-replace\n",
	})
	if !unreadWrite.IsError || unreadWrite.Code != "stale_file" {
		t.Fatalf("unread replacement = %+v, want stale_file", unreadWrite)
	}
	if got, err := os.ReadFile(unread); err != nil || string(got) != "unread\n" {
		t.Fatalf("unread target changed: content=%q err=%v", got, err)
	}

	create := executeCore(t, executor, "fs-write-create", "Write", "Write", map[string]any{
		"file_path": created,
		"content":   "first token\n",
	})
	requireSuccess(t, create)
	replaceCreated := executeCore(t, executor, "fs-write-replace-created", "Write", "Write", map[string]any{
		"file_path": created,
		"content":   "second token\n",
	})
	requireSuccess(t, replaceCreated)

	if err := os.WriteFile(existing, []byte("external needle\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	staleWrite := executeCore(t, executor, "fs-write-stale", "Write", "Write", map[string]any{
		"file_path": existing,
		"content":   "must-not-win\n",
	})
	if !staleWrite.IsError || staleWrite.Code != "stale_file" {
		t.Fatalf("stale replacement = %+v, want stale_file", staleWrite)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "external needle\n" {
		t.Fatalf("stale replacement changed target: content=%q err=%v", got, err)
	}

	requireSuccess(t, executeCore(t, executor, "fs-read-2", "Read", "Read", map[string]any{
		"file_path": existing,
	}))
	requireSuccess(t, executeCore(t, executor, "fs-write-replace", "Write", "Write", map[string]any{
		"file_path": existing,
		"content":   "replacement needle\n",
	}))
	edit := executeCore(t, executor, "fs-edit", "Edit", "Edit", map[string]any{
		"file_path":  existing,
		"old_string": "replacement",
		"new_string": "edited",
	})
	requireSuccess(t, edit)
	if got, err := os.ReadFile(existing); err != nil || string(got) != "edited needle\n" {
		t.Fatalf("Edit durable content=%q err=%v", got, err)
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatalf("stat edited file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("Edit mode=%v, want 0640", info.Mode().Perm())
	}
	if got, err := os.ReadFile(created); err != nil || string(got) != "second token\n" {
		t.Fatalf("Write durable content=%q err=%v", got, err)
	}

	glob := executeCore(t, executor, "fs-glob", "Glob", "Glob", map[string]any{
		"pattern": "**/*.txt",
		"path":    workspace,
		"limit":   10,
	})
	requireSuccess(t, glob)
	for _, path := range []string{created, existing, unread} {
		if !strings.Contains(glob.Content, path) {
			t.Fatalf("Glob omitted %q: %q", path, glob.Content)
		}
	}

	grep := executeCore(t, executor, "fs-grep", "Grep", "Grep", map[string]any{
		"pattern":      "edited needle|second token",
		"path":         workspace,
		"output_mode":  "content",
		"line_numbers": true,
		"head_limit":   10,
	})
	requireSuccess(t, grep)
	if !strings.Contains(grep.Content, "edited needle") || !strings.Contains(grep.Content, "second token") {
		t.Fatalf("Grep omitted durable mutations: %q", grep.Content)
	}

	shell := executeCore(t, executor, "fs-bash-allow", "Bash", "Bash", map[string]any{
		"command": "printf matrix-shell",
	})
	requireSuccess(t, shell)
	if shell.Content != "matrix-shell" {
		t.Fatalf("Bash output = %q, want matrix-shell", shell.Content)
	}

	deniedMarker := filepath.Join(workspace, "denied-marker")
	denied := executeCore(t, executor, "fs-bash-deny", "Bash", "Bash", map[string]any{
		"command": "touch denied-marker",
	})
	if !denied.IsError || denied.Code != "denied" || !denied.PermissionRejected {
		t.Fatalf("unruled Bash = %+v, want permission denial", denied)
	}
	if _, err := os.Stat(deniedMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied Bash created %s: %v", deniedMarker, err)
	}

	readEvent := authorizer.event(t, "fs-read-1")
	if readEvent.Tool != "Read" || !readEvent.Classification.ReadOnly || !readEvent.Classification.ConcurrencySafe ||
		len(readEvent.Paths) != 1 || readEvent.Paths[0] != (permission.PathAccess{Path: existing, Operation: permission.PathRead}) ||
		readEvent.Decision.Kind != permission.DecisionAllow {
		t.Fatalf("Read authorization projection = %+v", readEvent)
	}
	writeEvent := authorizer.event(t, "fs-write-create")
	if writeEvent.Tool != "Write" || writeEvent.Classification.ReadOnly ||
		len(writeEvent.Paths) != 1 || writeEvent.Paths[0] != (permission.PathAccess{Path: created, Operation: permission.PathWrite}) ||
		writeEvent.Decision.Kind != permission.DecisionAllow || writeEvent.Decision.Source != "mode" {
		t.Fatalf("Write authorization projection = %+v", writeEvent)
	}
	shellEvent := authorizer.event(t, "fs-bash-allow")
	if shellEvent.Tool != "Bash" || shellEvent.Decision.Kind != permission.DecisionAllow ||
		shellEvent.Decision.Source != "integration" || shellEvent.Decision.MatchedRule != "Bash(printf matrix-shell)" {
		t.Fatalf("allowed Bash authorization = %+v", shellEvent)
	}
	deniedEvent := authorizer.event(t, "fs-bash-deny")
	if deniedEvent.Decision.Kind != permission.DecisionDeny || deniedEvent.Decision.Source != "mode" {
		t.Fatalf("denied Bash authorization = %+v", deniedEvent)
	}
}

func TestCoreRegistryExecutorTaskV2Matrix(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("/bin/bash is unavailable")
	}

	workspace := t.TempDir()
	taskRoot := filepath.Join(t.TempDir(), "task-state")
	var ticks atomic.Int64
	var validationArmed atomic.Bool
	validationEntered := make(chan struct{}, 1)
	validationRelease := make(chan struct{})
	clock := func() time.Time {
		return time.Date(2032, 4, 5, 6, 7, 8, 0, time.UTC).Add(time.Duration(ticks.Add(1)) * time.Second)
	}
	validateState := func([]byte) error {
		if validationArmed.CompareAndSwap(true, false) {
			validationEntered <- struct{}{}
			<-validationRelease
		}
		return nil
	}
	manager, err := task.Open(taskRoot, task.Options{Clock: clock, ValidateState: validateState})
	if err != nil {
		t.Fatal(err)
	}
	managerOpen := true
	t.Cleanup(func() {
		if managerOpen {
			_ = manager.Close()
		}
	})

	const backgroundCommand = "printf task-output; sleep 30"
	registry, err := tool.NewCoreRegistry(tool.CoreOptions{
		Workspace: workspace,
		Tasks:     manager,
		Shell:     "/bin/bash",
		Environment: []string{
			"PATH=/bin:/usr/bin",
			"LANG=C",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskOutput", "TaskStop"} {
		if _, ok := registry.Resolve(name); !ok {
			t.Fatalf("task-v2 registry omitted %s", name)
		}
	}
	if _, ok := registry.Resolve("TodoWrite"); ok {
		t.Fatal("task-v2 registry exposed legacy TodoWrite")
	}

	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace: workspace,
		Rules: []permission.Rule{
			parseRule(t, "TaskCreate"),
			parseRule(t, "TaskUpdate"),
			parseRule(t, "TaskStop"),
			parseRule(t, "Bash("+backgroundCommand+")"),
		},
		PromptSuppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &recordingEvaluator{evaluator: evaluator}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	createA := executeCore(t, executor, "task-create-a", "TaskCreate", "TaskCreate", map[string]any{
		"subject":     "A",
		"description": "first durable task",
		"active_form": "Creating A",
		"metadata":    map[string]string{"phase": "seed"},
	})
	requireSuccess(t, createA)
	var itemA task.WorkItem
	if err := json.Unmarshal([]byte(createA.Content), &itemA); err != nil {
		t.Fatalf("decode TaskCreate A: %v; content=%q", err, createA.Content)
	}
	if !strings.HasPrefix(string(itemA.ID), "t") || itemA.Status != task.WorkPending || itemA.Metadata["phase"] != "seed" {
		t.Fatalf("TaskCreate A = %+v", itemA)
	}

	createB := executeCore(t, executor, "task-create-b", "TaskCreate", "TaskCreate", map[string]any{
		"subject":     "B",
		"description": "second durable task",
		"active_form": "Creating B",
		"metadata":    map[string]string{"drop": "yes"},
	})
	requireSuccess(t, createB)
	var itemB task.WorkItem
	if err := json.Unmarshal([]byte(createB.Content), &itemB); err != nil {
		t.Fatalf("decode TaskCreate B: %v", err)
	}

	updateB := executeCore(t, executor, "task-update-b", "TaskUpdate", "TaskUpdate", map[string]any{
		"task_id": string(itemB.ID),
		"status":  "in_progress",
		"owner":   "worker-1",
		"blockers": []string{
			string(itemA.ID),
		},
		"metadata": map[string]any{
			"drop":  nil,
			"phase": "build",
		},
	})
	requireSuccess(t, updateB)
	itemB = task.WorkItem{}
	if err := json.Unmarshal([]byte(updateB.Content), &itemB); err != nil {
		t.Fatalf("decode TaskUpdate B: %v", err)
	}
	if itemB.Status != task.WorkInProgress || itemB.Owner != "worker-1" ||
		len(itemB.Blockers) != 1 || itemB.Blockers[0] != itemA.ID ||
		itemB.Metadata["phase"] != "build" {
		t.Fatalf("TaskUpdate B = %+v", itemB)
	}
	if _, exists := itemB.Metadata["drop"]; exists {
		t.Fatalf("TaskUpdate did not remove null metadata: %+v", itemB.Metadata)
	}

	getA := executeCore(t, executor, "task-get-a", "TaskGet", "TaskGet", map[string]any{"task_id": string(itemA.ID)})
	requireSuccess(t, getA)
	if err := json.Unmarshal([]byte(getA.Content), &itemA); err != nil {
		t.Fatalf("decode TaskGet A: %v", err)
	}
	if len(itemA.Dependents) != 1 || itemA.Dependents[0] != itemB.ID {
		t.Fatalf("TaskGet A relationship state = %+v", itemA)
	}

	cycle := executeCore(t, executor, "task-update-cycle", "TaskUpdate", "TaskUpdate", map[string]any{
		"task_id": string(itemA.ID),
		"blockers": []string{
			string(itemB.ID),
		},
	})
	if !cycle.IsError || cycle.Code != "semantic_invalid" {
		t.Fatalf("cyclic TaskUpdate = %+v, want semantic_invalid", cycle)
	}
	getAAfterCycle := executeCore(t, executor, "task-get-a-after-cycle", "TaskGet", "TaskGet", map[string]any{
		"task_id": string(itemA.ID),
	})
	requireSuccess(t, getAAfterCycle)
	var itemAAfterCycle task.WorkItem
	if err := json.Unmarshal([]byte(getAAfterCycle.Content), &itemAAfterCycle); err != nil {
		t.Fatal(err)
	}
	if len(itemAAfterCycle.Blockers) != 0 || len(itemAAfterCycle.Dependents) != 1 ||
		itemAAfterCycle.Dependents[0] != itemB.ID {
		t.Fatalf("cyclic update changed relationship state: %+v", itemAAfterCycle)
	}

	list := executeCore(t, executor, "task-list", "TaskList", "TaskList", map[string]any{})
	requireSuccess(t, list)
	var listed []task.WorkItem
	if err := json.Unmarshal([]byte(list.Content), &listed); err != nil {
		t.Fatalf("decode TaskList: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != itemA.ID || listed[1].ID != itemB.ID {
		t.Fatalf("TaskList order/state = %+v", listed)
	}

	missing := executeCore(t, executor, "task-get-missing", "TaskGet", "TaskGet", map[string]any{"task_id": "t00000000"})
	if !missing.IsError || missing.Code != "semantic_invalid" {
		t.Fatalf("missing TaskGet = %+v, want semantic_invalid", missing)
	}

	createDelete := executeCore(t, executor, "task-create-delete", "TaskCreate", "TaskCreate", map[string]any{
		"subject":     "delete",
		"description": "delete through the public tool",
		"active_form": "Deleting",
	})
	requireSuccess(t, createDelete)
	var deleteItem task.WorkItem
	if err := json.Unmarshal([]byte(createDelete.Content), &deleteItem); err != nil {
		t.Fatal(err)
	}
	deleted := executeCore(t, executor, "task-update-delete", "TaskUpdate", "TaskUpdate", map[string]any{
		"task_id": string(deleteItem.ID),
		"delete":  true,
	})
	requireSuccess(t, deleted)
	if !strings.Contains(deleted.Content, string(deleteItem.ID)) {
		t.Fatalf("TaskUpdate delete omitted identity: %q", deleted.Content)
	}
	deletedGet := executeCore(t, executor, "task-get-deleted", "TaskGet", "TaskGet", map[string]any{"task_id": string(deleteItem.ID)})
	if !deletedGet.IsError || deletedGet.Code != "semantic_invalid" {
		t.Fatalf("deleted TaskGet = %+v, want semantic_invalid", deletedGet)
	}

	background := executeCore(t, executor, "task-bash-background", "Bash", "Bash", map[string]any{
		"command":           backgroundCommand,
		"description":       "task matrix background process",
		"run_in_background": true,
		"timeout":           60_000,
	})
	requireSuccess(t, background)
	var shellRecord task.Record
	if err := json.Unmarshal([]byte(background.Content), &shellRecord); err != nil {
		t.Fatalf("decode background Bash: %v; content=%q", err, background.Content)
	}
	if shellRecord.Status != task.StatusRunning || shellRecord.ToolUseID != "task-bash-background" ||
		shellRecord.Kind != task.KindShell || !strings.HasPrefix(string(shellRecord.ID), "b") {
		t.Fatalf("background Bash record = %+v", shellRecord)
	}

	contendingInput, err := json.Marshal(map[string]any{
		"task_id": string(itemA.ID),
		"owner":   "contention-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	validationArmed.Store(true)
	contendingResult := make(chan tool.Result, 1)
	go func() {
		contendingResult <- executor.Execute(context.Background(), tool.Request{
			ID: "task-update-contention", Name: "TaskUpdate", Input: contendingInput,
		})
	}()
	select {
	case <-validationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("TaskUpdate did not enter the controlled state validator")
	}
	go func() {
		timer := time.NewTimer(12 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		close(validationRelease)
	}()

	poll := executeCore(t, executor, "task-output-alias", "BashOutputTool", "TaskOutput", map[string]any{
		"task_id": string(shellRecord.ID),
		"offset":  0,
		"timeout": 5_000,
		"block":   true,
	})
	requireSuccess(t, poll)
	var pollResult task.PollResult
	if err := json.Unmarshal([]byte(poll.Content), &pollResult); err != nil {
		t.Fatalf("decode TaskOutput: %v; content=%q", err, poll.Content)
	}
	if !strings.Contains(pollResult.Output, "task-output") || pollResult.NextOffset <= 0 ||
		pollResult.Task.ID != shellRecord.ID {
		t.Fatalf("TaskOutput = %+v", pollResult)
	}
	select {
	case result := <-contendingResult:
		if result.IsError || result.ToolUseID != "task-update-contention" || result.Name != "TaskUpdate" {
			t.Fatalf("contending TaskUpdate = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contending TaskUpdate did not finish")
	}

	stop := executeCore(t, executor, "task-stop-alias", "KillShell", "TaskStop", map[string]any{
		"shell_id": string(shellRecord.ID),
	})
	requireSuccess(t, stop)
	var stopped task.Record
	if err := json.Unmarshal([]byte(stop.Content), &stopped); err != nil {
		t.Fatalf("decode TaskStop: %v; content=%q", err, stop.Content)
	}
	if stopped.ID != shellRecord.ID || stopped.Status != task.StatusKilled {
		t.Fatalf("TaskStop = %+v", stopped)
	}

	secondStop := executeCore(t, executor, "task-stop-terminal", "TaskStop", "TaskStop", map[string]any{
		"task_id": string(shellRecord.ID),
	})
	if !secondStop.IsError || secondStop.Code != "semantic_invalid" {
		t.Fatalf("second TaskStop = %+v, want semantic_invalid", secondStop)
	}

	if event := authorizer.event(t, "task-create-a"); event.Decision.Kind != permission.DecisionAllow ||
		event.Decision.Source != "integration" || event.Classification.ReadOnly {
		t.Fatalf("TaskCreate authorization = %+v", event)
	}
	if event := authorizer.event(t, "task-list"); event.Decision.Kind != permission.DecisionAllow ||
		event.Decision.Source != "classification" || !event.Classification.ReadOnly || !event.Classification.ConcurrencySafe {
		t.Fatalf("TaskList authorization = %+v", event)
	}
	if event := authorizer.event(t, "task-update-delete"); event.Decision.Kind != permission.DecisionAllow ||
		!event.Classification.Destructive {
		t.Fatalf("destructive TaskUpdate authorization = %+v", event)
	}
	if event := authorizer.event(t, "task-output-alias"); event.Tool != "TaskOutput" ||
		event.Decision.Kind != permission.DecisionAllow || !event.Classification.ReadOnly {
		t.Fatalf("TaskOutput alias authorization = %+v", event)
	}
	if event := authorizer.event(t, "task-stop-alias"); event.Tool != "TaskStop" ||
		event.Decision.Kind != permission.DecisionAllow {
		t.Fatalf("TaskStop alias authorization = %+v", event)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close task manager: %v", err)
	}
	managerOpen = false

	reopened, err := task.Open(taskRoot, task.Options{Clock: clock})
	if err != nil {
		t.Fatalf("reopen task manager: %v", err)
	}
	defer reopened.Close()
	if recoveredA, err := reopened.GetWork(itemA.ID); err != nil || len(recoveredA.Dependents) != 1 || recoveredA.Dependents[0] != itemB.ID {
		t.Fatalf("recovered A = %+v err=%v", recoveredA, err)
	}
	if recoveredB, err := reopened.GetWork(itemB.ID); err != nil || recoveredB.Status != task.WorkInProgress ||
		recoveredB.Metadata["phase"] != "build" || recoveredB.Owner != "worker-1" {
		t.Fatalf("recovered B = %+v err=%v", recoveredB, err)
	}
	if _, err := reopened.GetWork(deleteItem.ID); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("deleted task recovered: %v", err)
	}
	if recoveredShell, err := reopened.Get(shellRecord.ID); err != nil || recoveredShell.Status != task.StatusKilled {
		t.Fatalf("recovered shell = %+v err=%v", recoveredShell, err)
	}

	reopenedRegistry, err := tool.NewCoreRegistry(tool.CoreOptions{
		Workspace: workspace,
		Tasks:     reopened,
		Shell:     "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenedExecutor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: reopenedRegistry, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	recoveredOutput := executeCore(t, reopenedExecutor, "task-output-recovered", "TaskOutput", "TaskOutput", map[string]any{
		"task_id": string(shellRecord.ID),
		"offset":  0,
		"timeout": 0,
		"block":   false,
	})
	requireSuccess(t, recoveredOutput)
	if err := json.Unmarshal([]byte(recoveredOutput.Content), &pollResult); err != nil {
		t.Fatalf("decode recovered TaskOutput: %v", err)
	}
	if pollResult.Task.Status != task.StatusKilled || !strings.Contains(pollResult.Output, "task-output") {
		t.Fatalf("recovered TaskOutput = %+v", pollResult)
	}
}

func TestCoreRegistryExecutorLegacyTodoMatrix(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	taskRoot := filepath.Join(t.TempDir(), "todo-state")
	manager, err := task.Open(taskRoot, task.Options{})
	if err != nil {
		t.Fatal(err)
	}
	managerOpen := true
	t.Cleanup(func() {
		if managerOpen {
			_ = manager.Close()
		}
	})

	registry, err := tool.NewCoreRegistry(tool.CoreOptions{
		Workspace:   workspace,
		Tasks:       manager,
		LegacyTodos: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Resolve("TodoWrite"); !ok {
		t.Fatal("legacy registry omitted TodoWrite")
	}
	for _, name := range []string{"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"} {
		if _, ok := registry.Resolve(name); ok {
			t.Fatalf("legacy registry exposed %s", name)
		}
	}
	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace:        workspace,
		Rules:            []permission.Rule{parseRule(t, "TodoWrite")},
		PromptSuppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &recordingEvaluator{evaluator: evaluator}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	write := executeCore(t, executor, "todo-write-active", "TodoWrite", "TodoWrite", map[string]any{
		"todos": []map[string]any{
			{"content": "first", "status": "in_progress", "active_form": "Doing first"},
			{"content": "second", "status": "pending", "active_form": "Doing second"},
		},
	})
	requireSuccess(t, write)
	var todos []task.Todo
	if err := json.Unmarshal([]byte(write.Content), &todos); err != nil {
		t.Fatalf("decode TodoWrite: %v", err)
	}
	if len(todos) != 2 || todos[0].Status != task.WorkInProgress || todos[1].Status != task.WorkPending {
		t.Fatalf("TodoWrite state = %+v", todos)
	}

	complete := executeCore(t, executor, "todo-write-complete", "TodoWrite", "TodoWrite", map[string]any{
		"todos": []map[string]any{
			{"content": "first", "status": "completed", "active_form": "Doing first"},
			{"content": "second", "status": "completed", "active_form": "Doing second"},
		},
	})
	requireSuccess(t, complete)
	if err := json.Unmarshal([]byte(complete.Content), &todos); err != nil {
		t.Fatalf("decode completed TodoWrite: %v", err)
	}
	if len(todos) != 0 || len(manager.Todos()) != 0 {
		t.Fatalf("completed-only todos were retained: result=%+v manager=%+v", todos, manager.Todos())
	}
	if event := authorizer.event(t, "todo-write-active"); event.Tool != "TodoWrite" ||
		event.Decision.Kind != permission.DecisionAllow || event.Decision.Source != "integration" ||
		event.Classification.ReadOnly {
		t.Fatalf("TodoWrite authorization = %+v", event)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close todo manager: %v", err)
	}
	managerOpen = false
	reopened, err := task.Open(taskRoot, task.Options{})
	if err != nil {
		t.Fatalf("reopen todo manager: %v", err)
	}
	defer reopened.Close()
	if got := reopened.Todos(); len(got) != 0 {
		t.Fatalf("completed-only todos reappeared after reopen: %+v", got)
	}
}

func TestCoreTaskAdaptersBoundBusyRetries(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	var validationArmed atomic.Bool
	validationEntered := make(chan struct{}, 1)
	validationRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseValidator := func() {
		releaseOnce.Do(func() { close(validationRelease) })
	}
	t.Cleanup(releaseValidator)

	manager, err := task.Open(filepath.Join(t.TempDir(), "busy-state"), task.Options{
		ValidateState: func([]byte) error {
			if validationArmed.CompareAndSwap(true, false) {
				validationEntered <- struct{}{}
				<-validationRelease
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	registry, err := tool.NewCoreRegistry(tool.CoreOptions{Workspace: workspace, Tasks: manager})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace:        workspace,
		Rules:            []permission.Rule{parseRule(t, "TaskCreate")},
		PromptSuppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &recordingEvaluator{evaluator: evaluator}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	initial := executeCore(t, executor, "busy-initial-create", "TaskCreate", "TaskCreate", map[string]any{
		"subject": "initial", "description": "available before contention", "active_form": "Creating initial",
	})
	requireSuccess(t, initial)
	var initialItem task.WorkItem
	if err := json.Unmarshal([]byte(initial.Content), &initialItem); err != nil {
		t.Fatal(err)
	}

	validationArmed.Store(true)
	contender := make(chan error, 1)
	go func() {
		_, contenderErr := manager.CreateWork("contender", "holds validator", "Holding validator", nil)
		contender <- contenderErr
	}()
	select {
	case <-validationEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not enter the controlled validator")
	}

	cancelledContext, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	cancelledRaw, err := json.Marshal(map[string]any{
		"subject": "cancelled", "description": "must not be created", "active_form": "Cancelling",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := executor.Execute(cancelledContext, tool.Request{
		ID: "busy-create-cancelled", Name: "TaskCreate", Input: cancelledRaw,
	})
	if !cancelled.IsError || cancelled.Code != "cancelled" ||
		cancelled.ToolUseID != "busy-create-cancelled" || cancelled.Name != "TaskCreate" {
		t.Fatalf("context-cancelled task retry = %+v", cancelled)
	}

	unavailable := executeCore(t, executor, "busy-get-unavailable", "TaskGet", "TaskGet", map[string]any{
		"task_id": string(initialItem.ID),
	})
	if !unavailable.IsError || unavailable.Code != "unavailable" ||
		!strings.Contains(unavailable.Content, "task runtime remained busy") {
		t.Fatalf("exhausted task retry = %+v", unavailable)
	}
	if event := authorizer.event(t, "busy-get-unavailable"); event.Decision.Kind != permission.DecisionAllow ||
		event.Decision.Source != "classification" || !event.Classification.ReadOnly {
		t.Fatalf("busy TaskGet authorization = %+v", event)
	}

	releaseValidator()
	select {
	case err := <-contender:
		if err != nil {
			t.Fatalf("contending manager operation failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contending manager operation did not finish")
	}
	list := executeCore(t, executor, "busy-list-after-release", "TaskList", "TaskList", map[string]any{})
	requireSuccess(t, list)
	var items []task.WorkItem
	if err := json.Unmarshal([]byte(list.Content), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("cancelled retry created work or list lost state: %+v", items)
	}
}

func TestExecutorSemanticErrorClassificationIsPackageSealed(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	evaluator, err := permission.NewEvaluator(permission.Config{
		Workspace:        workspace,
		PromptSuppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var errorCalls, toolCalls atomic.Int32
	descriptor := tool.Descriptor{
		Name:   "HostileSemantic",
		Source: tool.SourcePlugin,
		Validate: func(json.RawMessage) (any, error) {
			return struct{}{}, nil
		},
		Semantic: func(any) error {
			return &hostileSemanticError{calls: &errorCalls}
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(context.Context, tool.CallContext, any) (tool.Output, error) {
			toolCalls.Add(1)
			return tool.Output{Content: "must not execute"}, nil
		},
	}
	registry, err := tool.NewRegistry(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: registry, Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	result := executeCore(t, executor, "hostile-semantic", descriptor.Name, descriptor.Name, map[string]any{})
	if !result.IsError || result.Code != "semantic_invalid" ||
		result.Content != "authorized input semantic validation failed: tool operation failed" {
		t.Fatalf("hostile semantic result = %+v", result)
	}
	if got := errorCalls.Load(); got != 0 {
		t.Fatalf("semantic error callbacks ran %d time(s)", got)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool executed %d time(s) after semantic failure", got)
	}

	spoofed := descriptor
	spoofed.Name = "SpoofedSemantic"
	spoofed.Semantic = func(any) error {
		return &tool.InvocationError{Code: "unavailable", Err: errors.New("extension-selected code")}
	}
	spoofedRegistry, err := tool.NewRegistry(spoofed)
	if err != nil {
		t.Fatal(err)
	}
	spoofedExecutor, err := tool.NewExecutor(tool.ExecutorOptions{Registry: spoofedRegistry, Authorizer: evaluator})
	if err != nil {
		t.Fatal(err)
	}
	spoofedResult := executeCore(t, spoofedExecutor, "spoofed-semantic", spoofed.Name, spoofed.Name, map[string]any{})
	if !spoofedResult.IsError || spoofedResult.Code != "semantic_invalid" {
		t.Fatalf("extension selected semantic error code: %+v", spoofedResult)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool executed %d time(s) after spoofed semantic failure", got)
	}
}
