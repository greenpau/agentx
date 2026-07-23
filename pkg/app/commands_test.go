package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/mcp"
	"github.com/greenpau/agentx/pkg/task"
)

func TestLocalCommandSupportedArgumentSurface(t *testing.T) {
	manager := mcp.NewManager(nil)
	t.Cleanup(func() { _ = manager.Close() })
	style := extensions.OutputStyle{CanonicalName: "Concise"}
	runtime := &runtimeSession{services: runtimeServices{extensions: runtimeExtensions{
		mcp:       manager,
		selection: extensions.OutputStyleSelection{Style: &style},
	}}}

	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "plugin", raw: "", want: "\"generation\""},
		{name: "plugin", raw: " LIST ", want: "\"generation\""},
		{name: "output-style", raw: "", want: "Concise"},
		{name: "mcp", raw: "", want: "\"servers\""},
		{name: "mcp", raw: "status", want: "\"servers\""},
		{name: "mcp", raw: "reload", want: "Tool registry changes take effect in a new session."},
	} {
		t.Run(test.name+"_"+strings.TrimSpace(test.raw), func(t *testing.T) {
			result, err := runtime.RunLocalCommand(t.Context(), test.name, nil, test.raw)
			if err != nil {
				t.Fatalf("RunLocalCommand(%q, %q): %v", test.name, test.raw, err)
			}
			if !strings.Contains(result.Output, test.want) {
				t.Fatalf("output = %q, want substring %q", result.Output, test.want)
			}
		})
	}
}

func TestHelpCommandRendersTheDispatchCatalog(t *testing.T) {
	runtime := &runtimeSession{}
	result, err := runtime.RunLocalCommand(t.Context(), "help", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Available commands:",
		"/memory [list|remember|recall]",
		"Show token usage and known cost (aliases: /usage)",
	} {
		if !strings.Contains(result.Output, expected) {
			t.Errorf("help omitted %q:\n%s", expected, result.Output)
		}
	}
	if strings.Contains(result.Output, "--print") {
		t.Fatalf("interactive command help rendered process-startup flags:\n%s", result.Output)
	}
}

func TestListOnlyCommandsRejectUnadvertisedArguments(t *testing.T) {
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks"), task.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime := &runtimeSession{tasks: manager}

	for _, name := range []string{"skills", "tasks"} {
		t.Run(name, func(t *testing.T) {
			_, err := runtime.RunLocalCommand(t.Context(), name, []string{"unexpected"}, "unexpected")
			if err == nil || err.Error() != "usage: /"+name {
				t.Fatalf("error = %v, want usage: /%s", err, name)
			}
		})
	}
}

func TestTasksCommandRetriesConcurrentTaskCallback(t *testing.T) {
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	var callbackOnce sync.Once
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks"), task.Options{
		Clock: func() time.Time {
			callbackOnce.Do(func() {
				close(callbackStarted)
				<-callbackRelease
			})
			return time.Unix(1_700_000_000, 0)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	createDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateWork("subject", "description", "working", nil)
		createDone <- err
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("task clock callback did not start")
	}
	time.AfterFunc(10*time.Millisecond, func() { close(callbackRelease) })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	runtime := &runtimeSession{tasks: manager}
	result, err := runtime.RunLocalCommand(ctx, "tasks", nil, "")
	if err != nil {
		t.Fatalf("/tasks did not retry transient callback contention: %v", err)
	}
	if result.Output != "[]" {
		t.Fatalf("/tasks output = %q, want empty runtime-task snapshot", result.Output)
	}
	if err := <-createDone; err != nil {
		t.Fatalf("concurrent work creation failed: %v", err)
	}
}

func TestTasksCommandReentrantTaskCallbackFailsBoundedly(t *testing.T) {
	var runtime *runtimeSession
	var nestedErr error
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks"), task.Options{
		Clock: func() time.Time {
			_, nestedErr = runtime.RunLocalCommand(context.Background(), "tasks", nil, "")
			return time.Unix(1_700_000_000, 0)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime = &runtimeSession{tasks: manager}
	started := time.Now()
	if _, err := manager.CreateWork("subject", "description", "working", nil); err != nil {
		t.Fatal(err)
	}
	if nestedErr != task.ErrBusy {
		t.Fatalf("reentrant /tasks error = %v, want task.ErrBusy", nestedErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("reentrant /tasks did not fail boundedly: %s", elapsed)
	}
}

func TestSkillsCommandListsOnlyAvailableSkills(t *testing.T) {
	runtime := &runtimeSession{skills: extensions.Snapshot{Skills: []extensions.Skill{
		{CanonicalName: "available", Description: "ready", Source: extensions.SourceUser, Availability: extensions.Available()},
		{CanonicalName: "unavailable", Description: "not ready", Source: extensions.SourceProject, Availability: extensions.Availability{}},
	}}}

	result, err := runtime.RunLocalCommand(t.Context(), "skills", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "available: ready") || strings.Contains(result.Output, "unavailable") {
		t.Fatalf("output = %q, want only the available skill", result.Output)
	}
}

func TestLocalCommandUnsupportedArgumentsAreNotAdvertised(t *testing.T) {
	manager := mcp.NewManager(nil)
	t.Cleanup(func() { _ = manager.Close() })
	runtime := &runtimeSession{services: runtimeServices{extensions: runtimeExtensions{mcp: manager}}}

	for _, test := range []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "compact", raw: "retain API decisions", wantErr: "manual compaction instructions are not supported"},
		{name: "plugin", raw: "reload", wantErr: "usage: /plugin [list]"},
		{name: "output-style", raw: "Concise", wantErr: "restart with --output-style"},
		{name: "mcp", raw: "reconnect", wantErr: "usage: /mcp [status|reload|reconnect NAME]"},
		{name: "mcp", raw: "unknown", wantErr: "usage: /mcp [status|reload|reconnect NAME]"},
	} {
		t.Run(test.name+"_"+test.raw, func(t *testing.T) {
			_, err := runtime.RunLocalCommand(t.Context(), test.name, nil, test.raw)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestLocalCommandActionPreservesReconnectTarget(t *testing.T) {
	for _, test := range []struct {
		raw           string
		wantAction    string
		wantRemainder string
	}{
		{raw: "  ReConNeCt   Server-One  ", wantAction: "reconnect", wantRemainder: "Server-One"},
		{raw: "status", wantAction: "status", wantRemainder: ""},
		{raw: "", wantAction: "", wantRemainder: ""},
	} {
		action, remainder := localCommandAction(test.raw)
		if action != test.wantAction || remainder != test.wantRemainder {
			t.Errorf("localCommandAction(%q) = (%q, %q), want (%q, %q)", test.raw, action, remainder, test.wantAction, test.wantRemainder)
		}
	}
}
