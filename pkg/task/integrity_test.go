package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsTamperedState(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	validWork := func(id ID) WorkItem {
		return WorkItem{
			Version: stateVersion, ID: id, Subject: "subject", Description: "description",
			ActiveForm: "working", Status: WorkPending, CreatedAt: now, UpdatedAt: now,
		}
	}
	validRecord := func(root string, id ID) Record {
		return Record{
			Version: stateVersion, ID: id, Kind: KindShell, Status: StatusRunning,
			Description: "running", Command: "sleep 1", OutputPath: filepath.Join(root, outputDirname, string(id)+".log"),
			StartedAt: now,
		}
	}
	largeMetadata := func() map[string]string {
		result := make(map[string]string, maximumMetadata+1)
		for index := 0; index <= maximumMetadata; index++ {
			result[fmt.Sprintf("key-%03d", index)] = "value"
		}
		return result
	}

	tests := []struct {
		name  string
		state func(string) persistedState
	}{
		{
			name: "oversized shell command",
			state: func(root string) persistedState {
				record := validRecord(root, "b00000000")
				record.Command = strings.Repeat("x", maximumStateString+1)
				return persistedState{Version: stateVersion, Tasks: map[ID]Record{record.ID: record}}
			},
		},
		{
			name: "oversized tool use identifier",
			state: func(root string) persistedState {
				record := validRecord(root, "b00000000")
				record.ToolUseID = strings.Repeat("x", maximumToolUseID+1)
				return persistedState{Version: stateVersion, Tasks: map[ID]Record{record.ID: record}}
			},
		},
		{
			name: "too many shell tasks",
			state: func(root string) persistedState {
				tasks := make(map[ID]Record, maximumTaskRecords+1)
				for index := 0; index <= maximumTaskRecords; index++ {
					id := ID(fmt.Sprintf("b%08x", index))
					tasks[id] = validRecord(root, id)
				}
				return persistedState{Version: stateVersion, Tasks: tasks}
			},
		},
		{
			name: "terminal task without end time",
			state: func(root string) persistedState {
				record := validRecord(root, "b00000000")
				record.Status = StatusKilled
				return persistedState{Version: stateVersion, Tasks: map[ID]Record{record.ID: record}}
			},
		},
		{
			name: "oversized work owner",
			state: func(_ string) persistedState {
				item := validWork("t00000000")
				item.Owner = strings.Repeat("x", maximumStateString+1)
				return persistedState{Version: stateVersion, Work: map[ID]WorkItem{item.ID: item}}
			},
		},
		{
			name: "too much metadata",
			state: func(_ string) persistedState {
				item := validWork("t00000000")
				item.Metadata = largeMetadata()
				return persistedState{Version: stateVersion, Work: map[ID]WorkItem{item.ID: item}}
			},
		},
		{
			name: "oversized metadata key",
			state: func(_ string) persistedState {
				item := validWork("t00000000")
				item.Metadata = map[string]string{strings.Repeat("k", maximumMetadataKey+1): "value"}
				return persistedState{Version: stateVersion, Work: map[ID]WorkItem{item.ID: item}}
			},
		},
		{
			name: "oversized metadata value",
			state: func(_ string) persistedState {
				item := validWork("t00000000")
				item.Metadata = map[string]string{"key": strings.Repeat("v", maximumMetadataVal+1)}
				return persistedState{Version: stateVersion, Work: map[ID]WorkItem{item.ID: item}}
			},
		},
		{
			name: "too many blockers",
			state: func(_ string) persistedState {
				item := validWork("t00000000")
				item.Blockers = make([]ID, maximumWorkRecords+1)
				return persistedState{Version: stateVersion, Work: map[ID]WorkItem{item.ID: item}}
			},
		},
		{
			name: "too many todos",
			state: func(_ string) persistedState {
				todos := make([]Todo, maximumTodos+1)
				for index := range todos {
					todos[index] = Todo{Content: "todo", ActiveForm: "doing", Status: WorkPending}
				}
				return persistedState{Version: stateVersion, Todos: todos}
			},
		},
		{
			name: "oversized todo",
			state: func(_ string) persistedState {
				return persistedState{Version: stateVersion, Todos: []Todo{{Content: strings.Repeat("x", maximumStateString+1), ActiveForm: "doing", Status: WorkPending}}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePersistedState(t, root, test.state(root))
			if _, err := Open(root, Options{}); err == nil {
				t.Fatal("tampered task state was accepted")
			}
		})
	}
}

func TestMutationsRejectOversizedDurableFieldsWithoutPoisoningState(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	for name, spec := range map[string]ShellSpec{
		"command":     {Command: strings.Repeat("x", maximumStateString+1)},
		"description": {Command: "true", Description: strings.Repeat("x", maximumStateString+1)},
		"tool use id": {Command: "true", ToolUseID: strings.Repeat("x", maximumToolUseID+1)},
		"owner":       {Command: "true", Owner: strings.Repeat("x", maximumStateString+1)},
	} {
		t.Run("shell "+name, func(t *testing.T) {
			if _, err := manager.LaunchShell(context.Background(), spec); err == nil {
				t.Fatal("oversized shell mutation succeeded")
			}
		})
	}
	if got := len(manager.List()); got != 0 {
		t.Fatalf("rejected shell mutations left %d records", got)
	}

	largeMetadata := make(map[string]string, maximumMetadata+1)
	for index := 0; index <= maximumMetadata; index++ {
		largeMetadata[fmt.Sprintf("key-%03d", index)] = "value"
	}
	for name, create := range map[string]func() (WorkItem, error){
		"subject": func() (WorkItem, error) {
			return manager.CreateWork(strings.Repeat("x", maximumStateString+1), "description", "active", nil)
		},
		"description": func() (WorkItem, error) {
			return manager.CreateWork("subject", strings.Repeat("x", maximumStateString+1), "active", nil)
		},
		"active form": func() (WorkItem, error) {
			return manager.CreateWork("subject", "description", strings.Repeat("x", maximumStateString+1), nil)
		},
		"metadata": func() (WorkItem, error) { return manager.CreateWork("subject", "description", "active", largeMetadata) },
	} {
		t.Run("create "+name, func(t *testing.T) {
			if _, err := create(); err == nil {
				t.Fatal("oversized work mutation succeeded")
			}
		})
	}
	if got := len(manager.ListWork()); got != 0 {
		t.Fatalf("rejected work mutations left %d records", got)
	}

	item, err := manager.CreateWork("subject", "description", "active", map[string]string{"existing": "value"})
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", maximumStateString+1)
	oversizedMetadataValue := strings.Repeat("v", maximumMetadataVal+1)
	blockers := make([]ID, maximumWorkRecords+1)
	patches := map[string]WorkPatch{
		"subject":        {Subject: &oversized},
		"description":    {Description: &oversized},
		"active form":    {ActiveForm: &oversized},
		"owner":          {Owner: &oversized},
		"blockers":       {Blockers: &blockers},
		"metadata key":   {Metadata: map[string]*string{strings.Repeat("k", maximumMetadataKey+1): stringPointer("value")}},
		"metadata value": {Metadata: map[string]*string{"key": &oversizedMetadataValue}},
	}
	tooMuchPatchMetadata := make(map[string]*string, maximumMetadata+1)
	for index := 0; index <= maximumMetadata; index++ {
		tooMuchPatchMetadata[fmt.Sprintf("patch-%03d", index)] = stringPointer("value")
	}
	patches["metadata count"] = WorkPatch{Metadata: tooMuchPatchMetadata}
	for name, patch := range patches {
		t.Run("update "+name, func(t *testing.T) {
			if _, err := manager.UpdateWork(item.ID, patch); err == nil {
				t.Fatal("oversized work patch succeeded")
			}
		})
	}
	unchanged, err := manager.GetWork(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Subject != "subject" || unchanged.Description != "description" || unchanged.ActiveForm != "active" || unchanged.Owner != "" || len(unchanged.Blockers) != 0 || len(unchanged.Metadata) != 1 {
		t.Fatalf("rejected patch changed work item: %#v", unchanged)
	}

	tooManyTodos := make([]Todo, maximumTodos+1)
	if err := manager.ReplaceTodos(tooManyTodos); err == nil {
		t.Fatal("oversized todo collection succeeded")
	}
	if err := manager.ReplaceTodos([]Todo{{Content: strings.Repeat("x", maximumStateString+1), ActiveForm: "active", Status: WorkPending}}); err == nil {
		t.Fatal("oversized todo field succeeded")
	}
	if got := manager.Todos(); len(got) != 0 {
		t.Fatalf("rejected todo replacement changed state: %#v", got)
	}

	reopened, err := Open(manager.root, Options{})
	if err != nil {
		t.Fatalf("state was poisoned by a rejected mutation: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.GetWork(item.ID); err != nil {
		t.Fatalf("valid state was not round-trippable: %v", err)
	}
}

func TestMutationCollectionLimits(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	t.Run("shell tasks", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		manager.mu.Lock()
		for index := 0; index < maximumTaskRecords; index++ {
			id := ID(fmt.Sprintf("b%08x", index))
			manager.tasks[id] = Record{
				Version: stateVersion, ID: id, Kind: KindShell, Status: StatusRunning,
				Description: "running", Command: "sleep 1", OutputPath: filepath.Join(manager.outputDir, string(id)+".log"), StartedAt: now,
			}
		}
		manager.mu.Unlock()
		if _, err := manager.LaunchShell(context.Background(), ShellSpec{Command: "true"}); err == nil {
			t.Fatal("shell task count limit was bypassed")
		}
		if got := len(manager.List()); got != maximumTaskRecords {
			t.Fatalf("rejected launch changed task count to %d", got)
		}
	})

	t.Run("work items", func(t *testing.T) {
		manager, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		manager.mu.Lock()
		for index := 0; index < maximumWorkRecords; index++ {
			id := ID(fmt.Sprintf("t%08x", index))
			manager.work[id] = newWorkItem(id, "subject", "description", "active", nil, now)
		}
		manager.mu.Unlock()
		if _, err := manager.CreateWork("extra", "description", "active", nil); err == nil {
			t.Fatal("work item count limit was bypassed")
		}
		if got := len(manager.ListWork()); got != maximumWorkRecords {
			t.Fatalf("rejected create changed work count to %d", got)
		}
	})
}

func TestWorkAndTodosRoundTripWithDerivedRelationships(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one, err := manager.CreateWork("one", "first", "doing one", map[string]string{"source": "test"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := manager.CreateWork("two", "second", "doing two", nil)
	if err != nil {
		t.Fatal(err)
	}
	blockers := []ID{one.ID}
	owner := "agent-a"
	status := WorkInProgress
	if _, err := manager.UpdateWork(two.ID, WorkPatch{Blockers: &blockers, Owner: &owner, Status: &status}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceTodos([]Todo{{Content: "ship it", ActiveForm: "shipping", Status: WorkInProgress}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredOne, err := restored.GetWork(one.ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredTwo, err := restored.GetWork(two.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredOne.Dependents) != 1 || restoredOne.Dependents[0] != two.ID || len(restoredTwo.Blockers) != 1 || restoredTwo.Blockers[0] != one.ID || restoredTwo.Owner != owner || restoredTwo.Status != status {
		t.Fatalf("work graph did not round-trip: one=%#v two=%#v", restoredOne, restoredTwo)
	}
	if todos := restored.Todos(); len(todos) != 1 || todos[0].Content != "ship it" {
		t.Fatalf("todos did not round-trip: %#v", todos)
	}
}

func TestClosedManagerRejectsEveryPublicMutation(t *testing.T) {
	manager, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := manager.CreateWork("subject", "description", "active", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	status := WorkCompleted
	checks := []error{}
	_, err = manager.LaunchShell(context.Background(), ShellSpec{Command: "true"})
	checks = append(checks, err)
	_, err = manager.CreateWork("new", "description", "active", nil)
	checks = append(checks, err)
	_, err = manager.UpdateWork(item.ID, WorkPatch{Status: &status})
	checks = append(checks, err)
	checks = append(checks, manager.ReplaceTodos([]Todo{{Content: "todo", ActiveForm: "doing", Status: WorkPending}}))
	checks = append(checks, manager.Stop("b00000000"))
	for index, err := range checks {
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("closed mutation %d returned %v, want ErrClosed", index, err)
		}
	}
}

func TestTaskOutputHardlinkSubstitutionIsRejected(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	record, err := manager.LaunchShell(context.Background(), ShellSpec{
		Command: "sleep 30", Dir: root, Env: os.Environ(), Shell: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	original := record.OutputPath + ".original"
	if err := os.Rename(record.OutputPath, original); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, record.OutputPath); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := manager.Poll(context.Background(), record.ID, 0, false, 0); err == nil {
		t.Fatal("hardlink-substituted task output was read")
	}
	if err := manager.Stop(record.ID); err == nil {
		t.Fatal("terminal journal accepted a substituted output identity")
	}
	content, err := os.ReadFile(victim)
	if err != nil || string(content) != "secret" {
		t.Fatalf("pinned writer modified substituted target: content=%q err=%v", content, err)
	}
}

func TestOpenRejectsHardlinkedTaskOutput(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, outputDirname)
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := ID("b00000000")
	output := filepath.Join(outputDir, string(id)+".log")
	if err := os.Link(victim, output); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	state := persistedState{Version: stateVersion, Tasks: map[ID]Record{id: {
		Version: stateVersion, ID: id, Kind: KindShell, Status: StatusRunning,
		Description: "running", Command: "sleep 1", OutputPath: output, StartedAt: now,
	}}}
	writePersistedState(t, root, state)
	if _, err := Open(root, Options{}); err == nil {
		t.Fatal("hardlinked task output was accepted during recovery")
	}
	content, err := os.ReadFile(victim)
	if err != nil || string(content) != "secret" {
		t.Fatalf("recovery modified hardlink target: content=%q err=%v", content, err)
	}
}

func TestOutputDirectorySymlinkAndReplacementAreRejected(t *testing.T) {
	t.Run("preexisting symlink", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(root, outputDirname)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Open(root, Options{}); err == nil {
			t.Fatal("symlinked task output directory was accepted")
		}
	})

	t.Run("post-open replacement", func(t *testing.T) {
		root := t.TempDir()
		manager, err := Open(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		original := filepath.Join(root, outputDirname+"-original")
		if err := os.Rename(manager.outputDir, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(manager.outputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.LaunchShell(context.Background(), ShellSpec{Command: "true"}); err == nil {
			t.Fatal("replaced task output directory was accepted")
		}
		entries, err := os.ReadDir(manager.outputDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("rejected launch wrote into replacement directory: %#v", entries)
		}
	})
}

func TestTaskRootSymlinkAndReplacementAreRejected(t *testing.T) {
	t.Run("preexisting direct symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "task-runtime")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		before, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(link, Options{}); err == nil {
			t.Fatal("symlinked task root was accepted")
		}
		after, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("task root symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
			t.Fatalf("symlink target was populated: entries=%v err=%v", entries, err)
		}
	})

	t.Run("post-open replacement", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "task-runtime")
		manager, err := Open(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		original := filepath.Join(parent, "task-runtime-original")
		if err := os.Rename(manager.root, original); err != nil {
			t.Skipf("directory replacement unavailable: %v", err)
		}
		if err := os.Mkdir(manager.root, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(manager.root, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateWork("blocked", "replacement must fail", "blocking", nil); err == nil {
			t.Fatal("task mutation accepted a replaced root")
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
			t.Fatalf("replacement root was modified: content=%q err=%v", content, err)
		}
		if _, err := os.Stat(filepath.Join(manager.root, stateFilename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state appeared beneath replacement root: %v", err)
		}
	})

	t.Run("replacement after validation", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "task-runtime")
		manager, err := Open(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		original := filepath.Join(parent, "task-runtime-original")
		manager.persistHook = func() error {
			if err := os.Rename(manager.root, original); err != nil {
				return err
			}
			if err := os.Mkdir(manager.root, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(manager.root, "sentinel"), []byte("keep"), 0o600)
		}
		if _, err := manager.CreateWork("blocked", "root swaps during commit", "blocking", nil); err == nil {
			t.Fatal("task commit accepted a root replaced after initial validation")
		}
		manager.persistHook = nil
		if content, err := os.ReadFile(filepath.Join(manager.root, "sentinel")); err != nil || string(content) != "keep" {
			t.Fatalf("replacement root was modified: content=%q err=%v", content, err)
		}
		if entries, err := os.ReadDir(manager.root); err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
			t.Fatalf("commit left files beneath replacement root: entries=%v err=%v", entries, err)
		}
	})
}

func writePersistedState(t *testing.T, root string, state persistedState) {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, stateFilename), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string { return &value }
