package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/transcript"
)

func TestSessionManagementListMissingPartitionIsProviderFree(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("provider-free list with malformed auth.json: %v", err)
	}
	if stderr != "" {
		t.Fatalf("provider-free list wrote stderr: %q", stderr)
	}
	result := decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout)
	if result.Version != transcript.SessionManagementVersion ||
		result.Status != transcript.SessionListOK ||
		len(result.Sessions) != 0 ||
		result.NextPageToken != "" {
		t.Fatalf("empty inventory = %#v", result)
	}

	partition := sessionManagementPartitionPath(agentxHome, workspace)
	if _, statErr := os.Lstat(partition); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("empty inventory materialized workspace partition %q: %v", partition, statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(agentxHome, "projects")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("session management materialized project memory: %v", statErr)
	}
	sessionEntries, readErr := os.ReadDir(filepath.Join(agentxHome, "sessions"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(sessionEntries) != 0 {
		t.Fatalf("provider-free list created semantic session state: %#v", sessionEntries)
	}
	homeEntries, readErr := os.ReadDir(agentxHome)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(homeEntries) != 2 ||
		homeEntries[0].Name() != config.DefaultAuthFile ||
		homeEntries[1].Name() != "sessions" {
		t.Fatalf("provider-free list created unexpected application state: %#v", homeEntries)
	}
}

func TestSwallowedSessionManagementSelectorCannotStartConversation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "list after scalar",
			args: []string{
				"--system-prompt",
				"--list-sessions",
				"--cwd", "unused",
				"--output-format", "json",
			},
		},
		{
			name: "delete after scalar",
			args: []string{
				"--model",
				"--delete-session=ses_swallowed",
				"--session-revision", "r1_swallowed",
				"--cwd", "unused",
				"--output-format", "json",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, agentxHome := prepareSessionManagementHome(t)
			workspace := t.TempDir()
			args := append([]string{}, test.args...)
			for index, value := range args {
				if value == "unused" {
					args[index] = workspace
				}
			}
			stdout, stderr, err := runSessionManagementCLI(t, ctx, args)
			if !cli.IsUsageError(err) {
				t.Fatalf("Run(%q) error = %T %v, want usage error before runtime startup", args, err, err)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("rejected swallowed selector wrote stdout=%q stderr=%q", stdout, stderr)
			}
			if _, statErr := os.Lstat(sessionManagementPartitionPath(agentxHome, workspace)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected swallowed selector created a session partition: %v", statErr)
			}
			if _, statErr := os.Lstat(filepath.Join(agentxHome, "projects")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected swallowed selector created project memory: %v", statErr)
			}
		})
	}
}

func TestSessionManagementJSONIsOneMinimalObject(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const (
		sessionID = "ses_json_contract"
		marker    = "private-topic-prompt-tool-transcript-marker"
	)
	writeSessionManagementFixture(t, agentxHome, workspace, sessionID, marker)

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("JSON inventory wrote diagnostics to stderr: %q", stderr)
	}
	object := decodeSingleSessionManagementJSON[map[string]any](t, stdout)
	assertSessionManagementJSONKeys(t, object, "version", "status", "sessions")
	items, ok := object["sessions"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("JSON sessions = %#v", object["sessions"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("JSON inventory item = %#v", items[0])
	}
	assertSessionManagementJSONKeys(t, item, "session_id", "updated_at", "revision")
	if item["session_id"] != sessionID {
		t.Fatalf("JSON session_id = %#v", item["session_id"])
	}
	updatedAt, ok := item["updated_at"].(string)
	if !ok {
		t.Fatalf("JSON updated_at = %#v, want string", item["updated_at"])
	}
	if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		t.Fatalf("JSON updated_at is not canonical RFC3339Nano: %#v", item["updated_at"])
	}
	for name, secret := range map[string]string{
		"application home": agentxHome,
		"workspace path":   workspace,
		"transcript data":  marker,
	} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("JSON inventory exposed %s %q: %s", name, secret, stdout)
		}
	}
}

func TestSessionManagementPaginationProjectsContinuationTokens(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	for _, sessionID := range []string{"ses_page_a", "ses_page_b", "ses_page_c"} {
		writeSessionManagementFixture(t, agentxHome, workspace, sessionID, sessionID)
	}

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--session-page-size", "1",
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil || stderr != "" {
		t.Fatalf("first JSON page = stderr %q, err %v", stderr, err)
	}
	first := decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout)
	if len(first.Sessions) != 1 || first.NextPageToken == "" {
		t.Fatalf("first JSON page omitted its bounded continuation: %#v", first)
	}
	if strings.Contains(stdout, agentxHome) || strings.Contains(stdout, workspace) {
		t.Fatalf("first JSON page exposed a store path: %q", stdout)
	}

	stdout, stderr, err = runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--session-page-size", "1",
		"--session-page-token", first.NextPageToken,
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil || stderr != "" {
		t.Fatalf("second JSON page = stderr %q, err %v", stderr, err)
	}
	second := decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout)
	if len(second.Sessions) != 1 ||
		second.Sessions[0].SessionID == first.Sessions[0].SessionID ||
		second.NextPageToken == "" {
		t.Fatalf("second JSON page did not advance: first=%#v second=%#v", first, second)
	}

	stdout, stderr, err = runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--session-page-size", "1",
		"--cwd", workspace,
		"--output-format", "text",
	})
	if err != nil || stderr != "" {
		t.Fatalf("first text page = stderr %q, err %v", stderr, err)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[1], "next_page_token\t") ||
		strings.TrimPrefix(lines[1], "next_page_token\t") == "" {
		t.Fatalf("text page omitted its continuation token: %q", stdout)
	}
}

func TestSessionManagementScopesSameIDToSelectedWorkspace(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	const sessionID = "ses_shared_id"
	firstDirectory := writeSessionManagementFixture(t, agentxHome, firstWorkspace, sessionID, "first-workspace-secret")
	secondDirectory := writeSessionManagementFixture(t, agentxHome, secondWorkspace, sessionID, "second-workspace-secret")

	first := listSessionManagementJSON(t, ctx, firstWorkspace)
	second := listSessionManagementJSON(t, ctx, secondWorkspace)
	if len(first.Sessions) != 1 || first.Sessions[0].SessionID != sessionID {
		t.Fatalf("first workspace inventory = %#v", first)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].SessionID != sessionID {
		t.Fatalf("second workspace inventory = %#v", second)
	}
	if first.Sessions[0].Revision == second.Sessions[0].Revision {
		t.Fatal("same session ID in distinct workspaces received the same deletion revision")
	}

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", first.Sessions[0].Revision,
		"--cwd", firstWorkspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("delete first workspace session: %v", err)
	}
	if stderr != "" {
		t.Fatalf("delete wrote stderr: %q", stderr)
	}
	deletion := decodeSingleSessionManagementJSON[transcript.SessionDeleteResult](t, stdout)
	if deletion.Status != transcript.SessionDeleted || deletion.SessionID != sessionID {
		t.Fatalf("deletion result = %#v", deletion)
	}
	if _, statErr := os.Lstat(firstDirectory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("first workspace session remains after deletion: %v", statErr)
	}
	if info, statErr := os.Lstat(secondDirectory); statErr != nil || !info.IsDir() {
		t.Fatalf("second workspace session was affected by deletion: %v", statErr)
	}
	if remaining := listSessionManagementJSON(t, ctx, secondWorkspace); len(remaining.Sessions) != 1 ||
		remaining.Sessions[0].SessionID != sessionID {
		t.Fatalf("second workspace inventory after first deletion = %#v", remaining)
	}
}

func TestSessionManagementDeletionPreservesOutOfScopeState(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const (
		targetID = "ses_scope_target"
		peerID   = "ses_scope_peer"
		childID  = "ses_scope_fork_child"
	)
	targetDirectory := writeSessionManagementFixture(t, agentxHome, workspace, targetID, "delete only this session")
	peerDirectory := writeSessionManagementFixture(t, agentxHome, workspace, peerID, "preserve peer session")
	childDirectory := writeSessionManagementForkFixture(
		t,
		agentxHome,
		workspace,
		childID,
		targetID,
		"preserve fork child",
	)

	workspaceKey := filepath.Base(sessionManagementPartitionPath(agentxHome, workspace))
	projectMemory := filepath.Join(agentxHome, "projects", workspaceKey, "memory", "scope-sentinel.txt")
	externalWorktree := filepath.Join(t.TempDir(), "external-worktree", "scope-sentinel.txt")
	settings := filepath.Join(agentxHome, "settings.json")
	cache := filepath.Join(agentxHome, "cache", "vscode-presentation-sentinel.json")
	sentinels := map[string]string{
		projectMemory:    "project-memory-must-survive",
		externalWorktree: "external-worktree-must-survive",
		settings:         "configuration-must-survive",
		cache:            "presentation-cache-must-survive",
	}
	for path, content := range sentinels {
		writeRuntimeFixture(t, path, content)
	}
	authPath := filepath.Join(agentxHome, config.DefaultAuthFile)
	authBefore, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	inventory := listSessionManagementJSON(t, ctx, workspace)
	var revision string
	for _, item := range inventory.Sessions {
		if item.SessionID == targetID {
			revision = item.Revision
			break
		}
	}
	if revision == "" {
		t.Fatalf("target session is absent from inventory: %#v", inventory)
	}
	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--delete-session", targetID,
		"--session-revision", revision,
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("delete scoped target: %v", err)
	}
	if stderr != "" {
		t.Fatalf("scoped deletion wrote stderr: %q", stderr)
	}
	deletion := decodeSingleSessionManagementJSON[transcript.SessionDeleteResult](t, stdout)
	if deletion.Status != transcript.SessionDeleted || deletion.SessionID != targetID {
		t.Fatalf("scoped deletion result = %#v", deletion)
	}
	if _, err := os.Lstat(targetDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target session remains after deletion: %v", err)
	}
	for name, directory := range map[string]string{
		"same-workspace peer": peerDirectory,
		"fork child":          childDirectory,
	} {
		if info, err := os.Lstat(directory); err != nil || !info.IsDir() {
			t.Fatalf("%s was affected by target deletion: %v", name, err)
		}
	}
	remaining := listSessionManagementJSON(t, ctx, workspace)
	remainingIDs := make(map[string]bool, len(remaining.Sessions))
	for _, item := range remaining.Sessions {
		remainingIDs[item.SessionID] = true
	}
	if remainingIDs[targetID] || !remainingIDs[peerID] || !remainingIDs[childID] ||
		len(remainingIDs) != 2 {
		t.Fatalf("inventory after scoped deletion = %#v", remaining)
	}
	for path, want := range sentinels {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preserved sentinel %q: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("preserved sentinel %q = %q, want %q", path, data, want)
		}
	}
	authAfter, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(authAfter, authBefore) {
		t.Fatalf("authentication file changed during deletion: before=%q after=%q", authBefore, authAfter)
	}
}

func TestSessionManagementUsesRelativeCWDAndFrozenApplicationHome(t *testing.T) {
	ctx, frozenHome := prepareSessionManagementHome(t)
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "ses_relative_frozen_home"
	frozenDirectory := writeSessionManagementFixture(
		t,
		frozenHome,
		workspace,
		sessionID,
		"frozen-home-session",
	)
	frozenInventory := listSessionManagementJSON(t, ctx, workspace)
	if len(frozenInventory.Sessions) != 1 ||
		frozenInventory.Sessions[0].SessionID != sessionID {
		t.Fatalf("frozen-home inventory = %#v", frozenInventory)
	}

	replacementHome := filepath.Join(t.TempDir(), "replacement-home")
	if err := os.MkdirAll(replacementHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(replacementHome, config.DefaultAuthFile),
		[]byte(`{different-malformed-auth-json`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	replacementDirectory := writeSessionManagementFixture(
		t,
		replacementHome,
		workspace,
		sessionID,
		"replacement-home-session",
	)
	t.Setenv("AGENTX_HOME", replacementHome)
	t.Chdir(base)

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--cwd", "workspace",
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("list with relative workspace and frozen home: %v", err)
	}
	if stderr != "" {
		t.Fatalf("relative frozen-home list wrote stderr: %q", stderr)
	}
	listed := decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout)
	if len(listed.Sessions) != 1 ||
		listed.Sessions[0].SessionID != sessionID ||
		listed.Sessions[0].Revision != frozenInventory.Sessions[0].Revision {
		t.Fatalf("relative frozen-home inventory = %#v, want %#v", listed, frozenInventory)
	}

	stdout, stderr, err = runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", listed.Sessions[0].Revision,
		"--cwd", "workspace",
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("delete with relative workspace and frozen home: %v", err)
	}
	if stderr != "" {
		t.Fatalf("relative frozen-home deletion wrote stderr: %q", stderr)
	}
	deletion := decodeSingleSessionManagementJSON[transcript.SessionDeleteResult](t, stdout)
	if deletion.Status != transcript.SessionDeleted || deletion.SessionID != sessionID {
		t.Fatalf("relative frozen-home deletion = %#v", deletion)
	}
	if _, err := os.Lstat(frozenDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("frozen-home target remains after deletion: %v", err)
	}
	if info, err := os.Lstat(replacementDirectory); err != nil || !info.IsDir() {
		t.Fatalf("replacement application home was affected: %v", err)
	}
}

func TestSessionManagementListThenDeleteUsesRevision(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const sessionID = "ses_list_delete"
	sessionDirectory := writeSessionManagementFixture(t, agentxHome, workspace, sessionID, "delete-me")

	inventory := listSessionManagementJSON(t, ctx, workspace)
	if len(inventory.Sessions) != 1 || inventory.Sessions[0].Revision == "" {
		t.Fatalf("inventory lacks deletion revision: %#v", inventory)
	}
	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", inventory.Sessions[0].Revision,
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("delete listed revision: %v", err)
	}
	if stderr != "" {
		t.Fatalf("successful deletion wrote stderr: %q", stderr)
	}
	object := decodeSingleSessionManagementJSON[map[string]any](t, stdout)
	assertSessionManagementJSONKeys(t, object, "version", "status", "session_id")
	if object["status"] != string(transcript.SessionDeleted) || object["session_id"] != sessionID {
		t.Fatalf("deletion JSON = %#v", object)
	}
	if _, statErr := os.Lstat(sessionDirectory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("deleted session directory remains: %v", statErr)
	}
	after := listSessionManagementJSON(t, ctx, workspace)
	if len(after.Sessions) != 0 {
		t.Fatalf("deleted session remains resumable in inventory: %#v", after)
	}
}

func TestSessionManagementStaleAndLockedStatusesAreJSON(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const sessionID = "ses_statuses"
	sessionDirectory := writeSessionManagementFixture(t, agentxHome, workspace, sessionID, "status-secret")
	inventory := listSessionManagementJSON(t, ctx, workspace)
	if len(inventory.Sessions) != 1 {
		t.Fatalf("inventory = %#v", inventory)
	}
	revision := inventory.Sessions[0].Revision
	staleRevision := "r1_" + base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
	if staleRevision == revision {
		staleRevision = "r1_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, sha256.Size))
	}

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", staleRevision,
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err == nil {
		t.Fatal("stale deletion returned success")
	}
	if stderr != "" {
		t.Fatalf("stale status contaminated stderr: %q", stderr)
	}
	stale := decodeSingleSessionManagementJSON[transcript.SessionDeleteResult](t, stdout)
	if stale.Status != transcript.SessionStale || stale.SessionID != sessionID {
		t.Fatalf("stale deletion result = %#v", stale)
	}

	holder, line := startAppLockHelper(t, filepath.Join(sessionDirectory, ".session.lock"))
	if line != "acquired" {
		_ = holder.release()
		t.Fatalf("lock helper = %q, want acquired", line)
	}
	t.Cleanup(func() { _ = holder.release() })
	stdout, stderr, err = runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", revision,
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err == nil {
		t.Fatal("locked deletion returned success")
	}
	if stderr != "" {
		t.Fatalf("locked status contaminated stderr: %q", stderr)
	}
	locked := decodeSingleSessionManagementJSON[transcript.SessionDeleteResult](t, stdout)
	if locked.Status != transcript.SessionLocked || locked.SessionID != sessionID {
		t.Fatalf("locked deletion result = %#v", locked)
	}
	if info, statErr := os.Lstat(sessionDirectory); statErr != nil || !info.IsDir() {
		t.Fatalf("stale or locked deletion removed the session: %v", statErr)
	}
	if releaseErr := holder.release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func TestSessionManagementTextOutput(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const (
		sessionID = "ses_text_output"
		marker    = "private-text-output-transcript-marker"
	)
	writeSessionManagementFixture(t, agentxHome, workspace, sessionID, marker)

	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--cwd", workspace,
		"--output-format", "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("text inventory wrote stderr: %q", stderr)
	}
	fields := strings.Split(strings.TrimSuffix(stdout, "\n"), "\t")
	if len(fields) != 3 || fields[0] != sessionID || fields[2] == "" {
		t.Fatalf("text inventory = %q", stdout)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields[1]); err != nil {
		t.Fatalf("text updated_at = %q: %v", fields[1], err)
	}
	for _, secret := range []string{agentxHome, workspace, marker} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("text inventory exposed private data %q: %q", secret, stdout)
		}
	}

	stdout, stderr, err = runSessionManagementCLI(t, ctx, []string{
		"--delete-session", sessionID,
		"--session-revision", fields[2],
		"--cwd", workspace,
		"--output-format", "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("text deletion wrote stderr: %q", stderr)
	}
	if stdout != "deleted\t"+sessionID+"\n" {
		t.Fatalf("text deletion = %q", stdout)
	}
}

func TestSessionManagementProjectsBootstrapFailureAndSanitizesInvalidTextID(t *testing.T) {
	t.Run("manager construction remains versioned JSON", func(t *testing.T) {
		var stdout bytes.Buffer
		err := runSessionManagement(
			t.Context(),
			nil,
			cli.Options{ListSessions: true, OutputFormat: cli.OutputJSON},
			filepath.Clean(t.TempDir()),
			&stdout,
		)
		if err == nil {
			t.Fatal("unsafe manager construction returned success")
		}
		result := decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout.String())
		if result.Version != transcript.SessionManagementVersion ||
			result.Status != transcript.SessionListStoreUnsafe ||
			len(result.Sessions) != 0 {
			t.Fatalf("manager-construction projection = %#v", result)
		}
	})

	t.Run("invalid ID is not reflected into text", func(t *testing.T) {
		ctx, _ := prepareSessionManagementHome(t)
		_, home, err := applicationHomeForContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		const invalid = "bad\ninjected\tidentifier"
		var stdout bytes.Buffer
		err = runSessionManagement(
			ctx,
			home.sessions,
			cli.Options{
				DeleteSession:   invalid,
				SessionRevision: "r1_invalid",
				OutputFormat:    cli.OutputText,
			},
			filepath.Clean(t.TempDir()),
			&stdout,
		)
		if err == nil {
			t.Fatal("invalid deletion identifier returned success")
		}
		if strings.Contains(stdout.String(), invalid) ||
			stdout.String() != string(transcript.SessionStoreUnsafe)+"\t\n" {
			t.Fatalf("invalid identifier text projection = %q", stdout.String())
		}
	})
}

func TestSessionDeletionStateBlocksResumeForkContinueAndRecreation(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const sessionID = "ses_pending_selection"
	livePath := writeSessionManagementFixture(t, agentxHome, workspace, sessionID, "pending-selection")
	inventory := listSessionManagementJSON(t, ctx, workspace)
	if len(inventory.Sessions) != 1 {
		t.Fatalf("initial inventory = %#v", inventory)
	}
	revision := inventory.Sessions[0].Revision
	stageName := fmt.Sprintf(".agentx-delete-v1-%03d-%s-%s", len(sessionID), sessionID, revision)
	intent := map[string]any{
		"version":      transcript.SessionManagementVersion,
		"session_id":   sessionID,
		"revision":     revision,
		"staging_name": stageName,
	}
	intentData, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	intentData = append(intentData, '\n')
	receiptRoot := filepath.Join(filepath.Dir(livePath), ".agentx-delete-receipts-v1")
	if err := os.Mkdir(receiptRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptRoot, ".registry.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(livePath, ".agentx-delete-intent-v1.json")
	if err := os.WriteFile(intentPath, intentData, 0o600); err != nil {
		t.Fatal(err)
	}

	assertSessionSelectionBlockedByDeletion(t, ctx, workspace, sessionID)
	if listed := listSessionManagementJSON(t, ctx, workspace); len(listed.Sessions) != 0 {
		t.Fatalf("live deletion intent remained in resumable inventory: %#v", listed)
	}

	_, home, err := applicationHomeForContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := transcript.NewSessionManager(home.sessions, workspace)
	if err != nil {
		t.Fatal(err)
	}
	partition, exists, err := manager.OpenWorkspacePartition()
	if err != nil || !exists {
		t.Fatalf("open workspace partition = %t, %v", exists, err)
	}
	owner, err := partition.OpenPrivateChild(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := partition.DetachPrivateChild(owner, stageName)
	if err != nil || !detached.Committed {
		t.Fatalf("prepare detached deletion state = %#v, %v", detached, err)
	}

	assertSessionSelectionBlockedByDeletion(t, ctx, workspace, sessionID)
	if _, err := os.Lstat(livePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selection race recreated detached live ID: %v", err)
	}
	if listed := listSessionManagementJSON(t, ctx, workspace); len(listed.Sessions) != 0 {
		t.Fatalf("detached deletion stage remained in resumable inventory: %#v", listed)
	}
}

func TestSessionDeleteRacesActualResumeForkAndList(t *testing.T) {
	ctx, agentxHome := prepareSessionManagementHome(t)
	workspace := t.TempDir()
	const sessionID = "ses_active_race"
	writeSessionManagementFixture(t, agentxHome, workspace, sessionID, "active race")
	inventory := listSessionManagementJSON(t, ctx, workspace)
	if len(inventory.Sessions) != 1 {
		t.Fatalf("initial inventory = %#v", inventory)
	}
	revision := inventory.Sessions[0].Revision

	active, _, err := resolveSessionLayout(
		ctx,
		workspace,
		cli.Options{Resume: sessionID, Bare: true},
		nil,
	)
	if err != nil {
		t.Fatalf("acquire actual resumed session: %v", err)
	}
	if active.lock == nil {
		t.Fatal("resumed session returned without an active lock")
	}
	t.Cleanup(func() { _ = active.lock.Close() })

	_, home, err := applicationHomeForContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := transcript.NewSessionManager(home.sessions, workspace)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(3)
	var deleteResult transcript.SessionDeleteResult
	var deleteErr, listErr, forkErr error
	var concurrentList transcript.SessionListResult
	go func() {
		defer wait.Done()
		<-start
		deleteResult, deleteErr = manager.Delete(ctx, sessionID, revision)
	}()
	go func() {
		defer wait.Done()
		<-start
		concurrentList, listErr = manager.List(ctx, 1, "")
	}()
	go func() {
		defer wait.Done()
		<-start
		forkLayout, _, err := resolveSessionLayout(
			ctx,
			workspace,
			cli.Options{
				Resume:      sessionID,
				ForkSession: true,
				SessionID:   "ses_active_race_child",
				Bare:        true,
			},
			nil,
		)
		forkErr = err
		if forkLayout.lock != nil {
			_ = forkLayout.lock.Close()
		}
	}()
	close(start)
	wait.Wait()

	if deleteErr != nil || deleteResult.Status != transcript.SessionLocked {
		t.Fatalf("Delete() against active resume = %#v, %v; want session_locked", deleteResult, deleteErr)
	}
	if listErr != nil || concurrentList.Status != transcript.SessionListOK ||
		len(concurrentList.Sessions) != 1 ||
		concurrentList.Sessions[0].SessionID != sessionID {
		t.Fatalf("List() against active resume/fork = %#v, %v", concurrentList, listErr)
	}
	if forkErr == nil {
		t.Fatal("fork acquired an actively resumed source")
	}
	if err := active.lock.Close(); err != nil {
		t.Fatal(err)
	}

	deleteResult, deleteErr = manager.Delete(ctx, sessionID, revision)
	if deleteErr != nil || deleteResult.Status != transcript.SessionDeleted {
		t.Fatalf("Delete() after active resume released = %#v, %v", deleteResult, deleteErr)
	}
}

func TestLockedSessionStateRejectsGenerationSubstitution(t *testing.T) {
	t.Run("new destination", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			state transcript.NativeSessionState
			ok    bool
		}{
			{
				name:  "same empty destination",
				state: transcript.NativeSessionState{Exists: true},
				ok:    true,
			},
			{
				name:  "became resumable",
				state: transcript.NativeSessionState{Exists: true, Resumable: true, Revision: "new-revision"},
			},
			{
				name:  "became incomplete fork",
				state: transcript.NativeSessionState{Exists: true, IncompleteFork: true},
			},
			{
				name:  "became deletion pending",
				state: transcript.NativeSessionState{Exists: true, DeletionPending: true},
			},
			{
				name:  "directory disappeared",
				state: transcript.NativeSessionState{},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := validateNewSessionDestinationState(test.state, "ses_destination_race")
				if (err == nil) != test.ok {
					t.Fatalf("validation error = %v, want ok=%t", err, test.ok)
				}
			})
		}
	})

	t.Run("source generation", func(t *testing.T) {
		const revision = "r1_selected_generation"
		for _, test := range []struct {
			name  string
			state transcript.NativeSessionState
			ok    bool
		}{
			{
				name: "same locked generation",
				state: transcript.NativeSessionState{
					Exists: true, Resumable: true, Revision: revision,
				},
				ok: true,
			},
			{
				name: "same ID replacement",
				state: transcript.NativeSessionState{
					Exists: true, Resumable: true, Revision: "r1_recreated_generation",
				},
			},
			{
				name: "deletion pending",
				state: transcript.NativeSessionState{
					Exists: true, DeletionPending: true, Revision: revision,
				},
			},
			{
				name: "incomplete fork",
				state: transcript.NativeSessionState{
					Exists: true, IncompleteFork: true, Revision: revision,
				},
			},
			{
				name:  "no durable history",
				state: transcript.NativeSessionState{Exists: true},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := validateSourceSessionGeneration(test.state, "ses_source_race", revision)
				if (err == nil) != test.ok {
					t.Fatalf("validation error = %v, want ok=%t", err, test.ok)
				}
			})
		}
	})
}

func assertSessionSelectionBlockedByDeletion(
	t *testing.T,
	ctx context.Context,
	workspace string,
	sessionID string,
) {
	t.Helper()
	cases := []struct {
		name    string
		options cli.Options
	}{
		{name: "resume", options: cli.Options{Resume: sessionID, Bare: true}},
		{
			name: "fork source",
			options: cli.Options{
				Resume:      sessionID,
				ForkSession: true,
				SessionID:   "ses_pending_child",
				Bare:        true,
			},
		},
		{name: "explicit recreation", options: cli.Options{SessionID: sessionID, Bare: true}},
		{name: "continue latest", options: cli.Options{Continue: true, Bare: true}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			layout, _, err := resolveSessionLayout(ctx, workspace, test.options, nil)
			if err == nil {
				if layout.lock != nil {
					_ = layout.lock.Close()
				}
				t.Fatal("deletion-pending session selection succeeded")
			}
			if test.name == "continue latest" {
				if !errors.Is(err, transcript.ErrNoPreviousSession) {
					t.Fatalf("continue deletion state = %v, want no previous session", err)
				}
				return
			}
			if !errors.Is(err, transcript.ErrSessionDeletionStaged) {
				t.Fatalf("deletion state = %v, want deletion pending", err)
			}
		})
	}
}

func prepareSessionManagementHome(t *testing.T) (context.Context, string) {
	t.Helper()
	agentxHome := filepath.Join(t.TempDir(), "agentx-home")
	if err := os.MkdirAll(agentxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	// Management intentionally uses only the common presence gate. Any attempt
	// to parse provider configuration makes every integration test fail.
	if err := os.WriteFile(filepath.Join(agentxHome, config.DefaultAuthFile), []byte(`{malformed-auth-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTX_HOME", agentxHome)
	ctx, err := PrepareApplicationHome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, frozen, err := applicationHomeForContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, frozen.root.Path()
}

func writeSessionManagementFixture(t *testing.T, agentxHome, workspace, sessionID, marker string) string {
	t.Helper()
	return writeSessionManagementFixtureWithParent(
		t,
		agentxHome,
		workspace,
		sessionID,
		"",
		marker,
	)
}

func writeSessionManagementForkFixture(
	t *testing.T,
	agentxHome string,
	workspace string,
	sessionID string,
	parentSessionID string,
	marker string,
) string {
	t.Helper()
	return writeSessionManagementFixtureWithParent(
		t,
		agentxHome,
		workspace,
		sessionID,
		parentSessionID,
		marker,
	)
}

func writeSessionManagementFixtureWithParent(
	t *testing.T,
	agentxHome string,
	workspace string,
	sessionID string,
	parentSessionID string,
	marker string,
) string {
	t.Helper()
	sessionDirectory := testSessionDir(agentxHome, workspace, sessionID)
	if err := os.MkdirAll(sessionDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(
		filepath.Join(sessionDirectory, ".session.lock"),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := transcript.Open(t.Context(), transcript.Config{
		Path:         filepath.Join(sessionDirectory, "transcript.jsonl"),
		SessionID:    protocol.SessionID(sessionID),
		SyncOnAppend: true,
		CloseTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := protocol.Event{
		Version:     protocol.CurrentVersion,
		ID:          protocol.EventID("evt_" + sessionID),
		SessionID:   protocol.SessionID(sessionID),
		TurnID:      "turn_session_management_fixture",
		Timestamp:   time.Now().UTC(),
		Kind:        protocol.EventKindMessage,
		Visibility:  protocol.VisibilityBoth,
		Persistence: protocol.PersistenceDurable,
		Origin:      protocol.OriginUser,
		Session: protocol.SessionMetadata{
			ParentSessionID:  protocol.SessionID(parentSessionID),
			WorkingDirectory: workspace,
			Entrypoint:       "session-management-test",
		},
		Message: &protocol.Message{
			Role:    protocol.RoleUser,
			Content: []protocol.ContentBlock{protocol.TextBlock(marker)},
		},
	}
	if err := store.Append(t.Context(), event); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return sessionDirectory
}

func runSessionManagementCLI(t *testing.T, ctx context.Context, args []string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(ctx, args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func listSessionManagementJSON(t *testing.T, ctx context.Context, workspace string) transcript.SessionListResult {
	t.Helper()
	stdout, stderr, err := runSessionManagementCLI(t, ctx, []string{
		"--list-sessions",
		"--cwd", workspace,
		"--output-format", "json",
	})
	if err != nil {
		t.Fatalf("list workspace sessions: %v", err)
	}
	if stderr != "" {
		t.Fatalf("list workspace sessions wrote stderr: %q", stderr)
	}
	return decodeSingleSessionManagementJSON[transcript.SessionListResult](t, stdout)
}

func decodeSingleSessionManagementJSON[T any](t *testing.T, output string) T {
	t.Helper()
	var result T
	decoder := json.NewDecoder(strings.NewReader(output))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode session-management JSON %q: %v", output, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("session-management stdout contained more than one JSON object: %q (%v)", output, err)
	}
	return result
}

func assertSessionManagementJSONKeys(t *testing.T, object map[string]any, expected ...string) {
	t.Helper()
	if len(object) != len(expected) {
		t.Fatalf("JSON object keys = %#v, want exactly %#v", object, expected)
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			t.Fatalf("JSON object lacks %q: %#v", key, object)
		}
	}
}

func sessionManagementPartitionPath(agentxHome, workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return filepath.Join(agentxHome, "sessions", hex.EncodeToString(sum[:12]))
}
