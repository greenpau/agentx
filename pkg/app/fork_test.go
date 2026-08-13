package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/transcript"
)

func TestIncompleteForkIsNotContinuableUntilPublished(t *testing.T) {
	sessionsRoot, err := platform.AcquirePrivateDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	manager, err := transcript.NewSessionManager(sessionsRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.EnsureWorkspacePartition()
	if err != nil {
		t.Fatal(err)
	}
	complete, err := root.EnsurePrivateChild("ses_complete")
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := root.EnsurePrivateChild("ses_incomplete")
	if err != nil {
		t.Fatal(err)
	}
	completeTranscript := filepath.Join(complete.Path(), "transcript.jsonl")
	incompleteTranscript := filepath.Join(incomplete.Path(), "transcript.jsonl")
	for _, directory := range []*platform.OwnedDirectory{complete, incomplete} {
		if err := os.WriteFile(filepath.Join(directory.Path(), ".session.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(completeTranscript, []byte("complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incompleteTranscript, []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(completeTranscript, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(incompleteTranscript, now, now); err != nil {
		t.Fatal(err)
	}
	layout := sessionLayout{
		sessionID: "ses_incomplete", sessionDir: incomplete.Path(), transcriptPath: incompleteTranscript, sessionOwner: incomplete,
		sessionManager: manager,
	}
	if err := beginForkPublication(layout); err != nil {
		t.Fatal(err)
	}
	if latest, err := manager.Latest(t.Context()); err != nil || latest != "ses_complete" {
		t.Fatalf("incomplete fork was selected: latest=%q err=%v", latest, err)
	}
	if err := completeForkPublication(layout); err != nil {
		t.Fatal(err)
	}
	if latest, err := manager.Latest(t.Context()); err != nil || latest != "ses_incomplete" {
		t.Fatalf("published fork was not selected: latest=%q err=%v", latest, err)
	}
}

func TestForkRestoreFailureRemainsUnpublished(t *testing.T) {
	const (
		sourceID      = "ses_reasoning_source"
		destinationID = "ses_reasoning_destination"
	)
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	writeTestAuthRegistry(t, agentxHome, []testAuthFileProvider{{
		ID: "sol-limited", Type: "azure_openai", Default: true,
		Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{
			Efforts: []string{"low", "high"}, DefaultEffort: "high",
		}},
		AzureOpenAI: testAzureOpenAIAuthFile{
			Endpoint: "https://example.test", Model: "gpt-5.6-sol", Deployment: "deployment",
			APIKey: "fork-reasoning-test-key", APIVersion: "preview",
		},
	}})
	t.Setenv("AGENTX_HOME", agentxHome)
	workspace := t.TempDir()
	runtimeConfig, err := config.Load(filepath.Join(agentxHome, config.DefaultAuthFile), nil, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := testSessionDir(agentxHome, workspace, sourceID)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".session.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := protocol.SessionMetadata{
		WorkingDirectory: workspace, ProviderID: runtimeConfig.SelectedProvider.ID,
		ProviderType: runtimeConfig.SelectedProvider.Type, Model: runtimeConfig.SelectedProvider.Model,
		ProviderBinding: runtimeConfig.ProviderBinding(),
	}
	store, err := transcript.Open(t.Context(), transcript.Config{
		Path: filepath.Join(sourceDir, "transcript.jsonl"), SessionID: sourceID,
		SessionMetadata: metadata, SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := protocol.NewMessageEvent(sourceID, "turn_source", protocol.RoleUser, protocol.TextBlock("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	effort, _ := json.Marshal("xhigh")
	reasoningEvent, err := protocol.NewBaseEvent(sourceID, "", protocol.EventKindSessionMetadata)
	if err != nil {
		t.Fatal(err)
	}
	reasoningEvent.Visibility = protocol.VisibilityInternal
	reasoningEvent.Metadata = &protocol.MetadataEvent{Key: "reasoning_effort", Value: effort}
	if err := store.Append(t.Context(), reasoningEvent); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	session, buildErr := buildSession(t.Context(), buildOptions{
		CLI: cli.Options{
			Print: true, Bare: true, Resume: sourceID, ForkSession: true,
			SessionID: destinationID, MaxTurns: 1,
		},
		Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard,
	})
	if session != nil {
		_ = session.Close()
		t.Fatal("fork with unsupported durable effort started")
	}
	if buildErr == nil || !strings.Contains(buildErr.Error(), "session reasoning effort is unsupported by the selected provider") || strings.Contains(buildErr.Error(), "fork-reasoning-test-key") {
		t.Fatalf("fork restore error = %v", buildErr)
	}

	_, home, err := applicationHomeForContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := transcript.NewSessionManager(home.sessions, workspace)
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.State(t.Context(), destinationID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.IncompleteFork || state.Resumable {
		t.Fatalf("failed fork publication state = %#v", state)
	}
}

func TestCopyForkNeverPersistsSyntheticInterruptionEvidence(t *testing.T) {
	const sourceID protocol.SessionID = "ses_source"
	const destinationID protocol.SessionID = "ses_destination"
	user := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_source_user", SessionID: sourceID, TurnID: "turn_source", Sequence: 1,
		Timestamp: time.Now(), Kind: protocol.EventKindMessage, Visibility: protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginUser,
		Message: &protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{protocol.TextBlock("run it")}},
	}
	call := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_source_call", SessionID: sourceID, TurnID: "turn_source", Sequence: 2,
		Timestamp: time.Now().Add(time.Second), Kind: protocol.EventKindToolCall, Visibility: protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable, Origin: protocol.OriginModel, ParentID: &user.ID,
		ToolCall: &protocol.ToolCall{ID: "tool_source_call", Name: "Read", Arguments: json.RawMessage(`{"path":"README.md"}`), APIResponseID: "resp_source"},
	}
	syntheticParent := call.ID
	synthetic := protocol.Event{
		Version: protocol.CurrentVersion, ID: "evt_derived_interruption", SessionID: sourceID, TurnID: "turn_source",
		Timestamp: call.Timestamp, Kind: protocol.EventKindToolResult, Visibility: protocol.VisibilityBoth,
		Persistence: protocol.PersistenceEphemeral, Origin: protocol.OriginRecovery, ParentID: &syntheticParent,
		ToolResult: &protocol.ToolResult{
			ToolUseID: call.ToolCall.ID, ToolName: call.ToolCall.Name, Status: protocol.ToolResultInterrupted,
			Content: []protocol.ContentBlock{protocol.TextBlock("derived only")}, IsError: true, Synthetic: true,
		},
	}
	source := transcript.Snapshot{SessionID: sourceID, Events: []protocol.Event{user, call, synthetic}, MaxSequence: 2}
	store, err := transcript.Open(context.Background(), transcript.Config{
		Path: filepath.Join(t.TempDir(), "transcript.jsonl"), SessionID: destinationID, SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := copyFork(context.Background(), store, source, destinationID); err != nil {
		t.Fatal(err)
	}
	physical, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(physical.Events) != 2 || physical.Events[1].Kind != protocol.EventKindToolCall {
		t.Fatalf("fork did not preserve exactly the durable raw evidence: %#v", physical.Events)
	}
	for _, event := range physical.Events {
		if event.Kind == protocol.EventKindToolResult || event.Persistence == protocol.PersistenceEphemeral {
			t.Fatalf("fork persisted derived interruption evidence: %#v", event)
		}
	}
	semantic := physical.ActiveConversation().ReconcileUnresolved()
	if len(semantic.Events) != 1 || semantic.Events[0].Kind != protocol.EventKindMessage {
		t.Fatalf("forked unresolved group remained model-visible: %#v", semantic.Events)
	}
}
