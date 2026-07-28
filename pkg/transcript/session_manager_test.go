package transcript

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/sessionlock"
)

var sessionInventoryEpoch = time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)

func TestSessionManagerScopesIdenticalIDsToTheirWorkspace(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	workspaceA := filepath.Clean(t.TempDir())
	workspaceB := filepath.Clean(t.TempDir())
	managerA := newSessionManagementManager(t, sessionsRoot, workspaceA)
	managerB := newSessionManagementManager(t, sessionsRoot, workspaceB)

	const sessionID = "ses_same_id"
	createSessionManagementCandidate(t, managerA, sessionID, "workspace A\n", sessionInventoryEpoch)
	createSessionManagementCandidate(t, managerB, sessionID, "workspace B\n", sessionInventoryEpoch.Add(time.Minute))

	itemA := onlySessionManagementItem(t, managerA)
	itemB := onlySessionManagementItem(t, managerB)
	if itemA.Revision == itemB.Revision {
		t.Fatal("the same session ID in distinct workspace partitions received the same revision")
	}

	result, err := managerB.Delete(t.Context(), sessionID, itemA.Revision)
	if result.Status != SessionStale || err == nil {
		t.Fatalf("cross-workspace revision deletion = %#v, %v; want stale", result, err)
	}
	if got := onlySessionManagementItem(t, managerB); got.Revision != itemB.Revision {
		t.Fatalf("cross-workspace deletion changed workspace B: got %#v, want %#v", got, itemB)
	}

	result, err = managerA.Delete(t.Context(), sessionID, itemA.Revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("workspace A deletion = %#v, %v; want deleted", result, err)
	}
	assertSessionManagementIDs(t, listAllSessionManagementItems(t, managerA, 1), nil)
	if got := onlySessionManagementItem(t, managerB); got.Revision != itemB.Revision {
		t.Fatalf("workspace A deletion escaped its partition: got %#v, want %#v", got, itemB)
	}
}

func TestSessionManagerMissingPartitionListsEmptyWithoutCreatingIt(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	partitionPath := filepath.Join(sessionsRoot.Path(), manager.workspaceKey)
	if _, err := os.Lstat(partitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace partition existed before inventory: %v", err)
	}

	result, err := manager.List(t.Context(), 0, "")
	if err != nil {
		t.Fatalf("empty inventory: %v", err)
	}
	if result.Version != SessionManagementVersion || result.Status != SessionListOK ||
		result.Sessions == nil || len(result.Sessions) != 0 || result.NextPageToken != "" {
		t.Fatalf("empty inventory = %#v", result)
	}
	if _, err := os.Lstat(partitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only inventory created workspace partition: %v", err)
	}
}

func TestSessionManagerSortsAndPaginatesWithoutTruncation(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	fixtures := []struct {
		id      string
		updated time.Time
	}{
		{id: "z_old", updated: sessionInventoryEpoch},
		{id: "a_new", updated: sessionInventoryEpoch.Add(3 * time.Hour)},
		{id: "mid", updated: sessionInventoryEpoch.Add(2 * time.Hour)},
		{id: "b_new", updated: sessionInventoryEpoch.Add(3 * time.Hour)},
		{id: "e_old", updated: sessionInventoryEpoch},
	}
	for _, fixture := range fixtures {
		createSessionManagementCandidate(t, manager, fixture.id, fixture.id+"\n", fixture.updated)
	}

	first, err := manager.List(t.Context(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.NextPageToken == "" {
		t.Fatal("first bounded page silently omitted its continuation token")
	}
	assertSessionManagementIDs(t, first.Sessions, []string{"a_new", "b_new"})

	second, err := manager.List(t.Context(), 2, first.NextPageToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.NextPageToken == "" {
		t.Fatal("second bounded page silently omitted its continuation token")
	}
	assertSessionManagementIDs(t, second.Sessions, []string{"mid", "e_old"})

	third, err := manager.List(t.Context(), 2, second.NextPageToken)
	if err != nil {
		t.Fatal(err)
	}
	if third.NextPageToken != "" {
		t.Fatalf("terminal page has continuation token %q", third.NextPageToken)
	}
	assertSessionManagementIDs(t, third.Sessions, []string{"z_old"})

	all := append(append(append([]SessionInventoryItem{}, first.Sessions...), second.Sessions...), third.Sessions...)
	assertSessionManagementIDs(t, all, []string{"a_new", "b_new", "mid", "e_old", "z_old"})
	for _, item := range all {
		parsed, parseErr := time.Parse(time.RFC3339Nano, item.UpdatedAt)
		if parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != item.UpdatedAt {
			t.Fatalf("updated_at %q is not canonical RFC3339Nano: %v", item.UpdatedAt, parseErr)
		}
	}
	for _, token := range []string{
		first.NextPageToken + "A",
		encodePageToken(0, inventoryGeneration(all)),
		encodePageToken(len(all), inventoryGeneration(all)),
	} {
		malformed, malformedErr := manager.List(t.Context(), 2, token)
		if !errors.Is(malformedErr, ErrSessionPageStale) ||
			malformed.Status != SessionListStale ||
			len(malformed.Sessions) != 0 {
			t.Fatalf("malformed continuation token = %#v, %v; want empty stale result", malformed, malformedErr)
		}
	}

	createSessionManagementCandidate(
		t,
		manager,
		"new_generation",
		"new generation\n",
		sessionInventoryEpoch.Add(4*time.Hour),
	)
	stale, err := manager.List(t.Context(), 2, first.NextPageToken)
	if !errors.Is(err, ErrSessionPageStale) || stale.Status != SessionListStale ||
		stale.Version != SessionManagementVersion {
		t.Fatalf("old continuation token = %#v, %v; want versioned stale result", stale, err)
	}
}

func TestSessionManagerIgnoresNamesOutsideNativeSessionGrammar(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	partition, err := manager.EnsureWorkspacePartition()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"séance",
		strings.Repeat("a", 129),
		"..session",
		"%2e%2e",
		"session..",
	} {
		if ValidSessionID(name) {
			t.Fatalf("invalid-name fixture %q unexpectedly satisfies native grammar", name)
		}
		path := filepath.Join(partition.Path(), name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create invalid-name fixture %q: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(path, transcriptName), []byte("must not be inventoried\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	createSessionManagementCandidate(t, manager, "ses_valid", "valid\n", sessionInventoryEpoch)

	assertSessionManagementIDs(
		t,
		listAllSessionManagementItems(t, manager, MaxSessionPageSize),
		[]string{"ses_valid"},
	)

	const sentinel = "must not be reached by session ID traversal"
	escapeTarget := filepath.Join(sessionsRoot.Path(), "escape-target")
	if err := os.WriteFile(escapeTarget, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Delete(t.Context(), "../escape-target", staleSessionManagementRevision())
	if !errors.Is(err, ErrSessionStoreUnsafe) || result.Status != SessionStoreUnsafe {
		t.Fatalf("traversal-like Delete() = %#v, %v; want store_unsafe", result, err)
	}
	data, err := os.ReadFile(escapeTarget)
	if err != nil || string(data) != sentinel {
		t.Fatalf("invalid session ID reached outside its partition: %q, %v", data, err)
	}
}

func TestSessionManagerBoundsWorkspaceAndStagingEnumeration(t *testing.T) {
	t.Run("workspace entries", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		for index := 0; index <= MaxWorkspaceEntries; index++ {
			name := fmt.Sprintf(".inventory-noise-%04d", index)
			if err := os.WriteFile(filepath.Join(partition.Path(), name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})

	t.Run("deletion stages", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		revision := staleSessionManagementRevision()
		for index := 0; index <= MaxDeletionStages; index++ {
			sessionID := fmt.Sprintf("ses_stage_%03d", index)
			if _, err := partition.CreatePrivateChild(deletionStageName(sessionID, revision)); err != nil {
				t.Fatal(err)
			}
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})
}

func TestSessionManagerRefusesIntentWithoutWorkspaceMetadataHeadroom(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	const sessionID = "ses_no_headroom"
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"no metadata headroom\n",
		sessionInventoryEpoch,
	)
	for index := 1; index < MaxWorkspaceEntries; index++ {
		if err := os.WriteFile(
			filepath.Join(partition.Path(), fmt.Sprintf(".headroom-%04d", index)),
			nil,
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	revision := onlySessionManagementItem(t, manager).Revision
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionStoreUnsafe || !errors.Is(err, ErrSessionStoreUnsafe) {
		t.Fatalf("Delete() without metadata headroom = %#v, %v; want store_unsafe", result, err)
	}
	for _, path := range []string{
		filepath.Join(sessionPath, deleteIntentName),
		filepath.Join(partition.Path(), deleteReceiptRoot),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("headroom refusal created hidden deletion state at %s: %v", path, err)
		}
	}
}

func TestSessionManagerRefusesIntentWithoutDeletionStageHeadroom(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	const sessionID = "ses_stage_headroom_target"
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"stage headroom target\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	partitionIdentity, _, err := boundedPartitionSnapshot(partition)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxDeletionStages; index++ {
		id := fmt.Sprintf("ses_existing_stage_%03d", index)
		path := createSessionManagementCandidate(
			t,
			manager,
			id,
			"existing stage\n",
			sessionInventoryEpoch,
		)
		candidate, err := manager.inspectSessionCandidate(
			t.Context(),
			partition,
			partitionIdentity,
			id,
			"",
			newDigestBudget(),
		)
		if err != nil || candidate == nil || candidate.item.Revision == "" {
			t.Fatalf("inspect stage fixture %d: %#v, %v", index, candidate, err)
		}
		intent := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   id,
			Revision:    candidate.item.Revision,
			StagingName: deletionStageName(id, candidate.item.Revision),
		}
		data, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(path, deleteIntentName),
			append(data, '\n'),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, filepath.Join(partition.Path(), intent.StagingName)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionStoreUnsafe || !errors.Is(err, ErrSessionStoreUnsafe) {
		t.Fatalf("Delete() without stage headroom = %#v, %v; want store_unsafe", result, err)
	}
	if _, err := os.Lstat(filepath.Join(sessionPath, deleteIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage-headroom refusal persisted deletion intent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(partition.Path(), deleteReceiptRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage-headroom refusal created receipt root: %v", err)
	}
}

func TestSessionManagerRefusesIntentWithoutReceiptHeadroom(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	manager.receiptEntryLimitForTest = 1
	const targetID = "ses_receipt_headroom_target"
	targetPath := createSessionManagementCandidate(
		t,
		manager,
		targetID,
		"receipt headroom target\n",
		sessionInventoryEpoch,
	)
	targetRevision := onlySessionManagementItem(t, manager).Revision
	_, registryLock, err := manager.acquireReceiptRegistry(t.Context(), partition, nil, true)
	if errors.Is(err, sessionlock.ErrUnsupported) {
		t.Skip("session locks are unsupported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	partitionIdentity, _, err := boundedPartitionSnapshot(partition)
	if err != nil {
		_ = registryLock.Close()
		t.Fatal(err)
	}
	for index := 0; index < manager.receiptEntryLimit(); index++ {
		sessionID := fmt.Sprintf("ses_pending_receipt_%03d", index)
		createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"pending receipt\n",
			sessionInventoryEpoch,
		)
		candidate, err := manager.inspectSessionCandidate(
			t.Context(),
			partition,
			partitionIdentity,
			sessionID,
			"",
			newDigestBudget(),
		)
		if err != nil || candidate == nil || candidate.lockEvidence == nil {
			_ = registryLock.Close()
			t.Fatalf("inspect pending receipt fixture %d: %#v, %v", index, candidate, err)
		}
		intent := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   sessionID,
			Revision:    candidate.item.Revision,
			StagingName: deletionStageName(sessionID, candidate.item.Revision),
		}
		if err := ensureDeletionIntent(candidate.owner, intent, nil); err != nil {
			_ = registryLock.Close()
			t.Fatalf("persist pending intent fixture %d: %v", index, err)
		}
		receipt := manager.receiptForCandidate(partitionIdentity, candidate, intent)
		if _, err := manager.ensureDeletionReceipt(partition, receipt, registryLock); err != nil {
			_ = registryLock.Close()
			t.Fatalf("persist pending receipt fixture %d: %v", index, err)
		}
	}
	if err := registryLock.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Delete(t.Context(), targetID, targetRevision)
	if result.Status != SessionStoreUnsafe || !errors.Is(err, ErrSessionStoreUnsafe) {
		t.Fatalf("Delete() without receipt headroom = %#v, %v; want store_unsafe", result, err)
	}
	if _, err := os.Lstat(filepath.Join(targetPath, deleteIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt-headroom refusal persisted target deletion intent: %v", err)
	}
	state, err := manager.State(t.Context(), targetID)
	if err != nil || !state.Resumable || state.DeletionPending {
		t.Fatalf("target state after receipt-headroom refusal = %#v, %v; want resumable", state, err)
	}
}

func TestSessionManagerReservesCompletedReceiptRetentionSlot(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	manager.receiptRetentionForTest = 2
	revisions := make(map[string]string)
	for index := 0; index < 4; index++ {
		sessionID := fmt.Sprintf("ses_receipt_retention_%d", index)
		createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"completed receipt retention\n",
			sessionInventoryEpoch.Add(time.Duration(index)*time.Second),
		)
	}
	for _, item := range listAllSessionManagementItems(t, manager, MaxSessionPageSize) {
		revisions[item.SessionID] = item.Revision
	}
	for index := 0; index < 4; index++ {
		sessionID := fmt.Sprintf("ses_receipt_retention_%d", index)
		result, err := manager.Delete(t.Context(), sessionID, revisions[sessionID])
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("Delete(%q) = %#v, %v", sessionID, result, err)
		}
		scan, err := manager.scanWorkspace(t.Context(), "", newDigestBudget())
		if err != nil {
			t.Fatal(err)
		}
		complete := 0
		for _, receipt := range scan.receipts {
			if receipt.complete {
				complete++
			}
		}
		if complete > manager.completedReceiptRetention() {
			t.Fatalf("completed receipt count after deletion %d = %d, want <= %d",
				index, complete, manager.completedReceiptRetention())
		}
	}
}

func TestSessionManagerSerializesReceiptRegistryMutation(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	const sessionID = "ses_registry_busy"
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"registry serialization\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	_, registryLock, err := manager.acquireReceiptRegistry(t.Context(), partition, nil, true)
	if errors.Is(err, sessionlock.ErrUnsupported) {
		t.Skip("session locks are unsupported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	result, deleteErr := manager.Delete(t.Context(), sessionID, revision)
	if deleteErr != nil || result.Status != SessionLocked {
		_ = registryLock.Close()
		t.Fatalf("Delete() under registry contention = %#v, %v; want session_locked", result, deleteErr)
	}
	if _, err := os.Lstat(filepath.Join(sessionPath, deleteIntentName)); !errors.Is(err, os.ErrNotExist) {
		_ = registryLock.Close()
		t.Fatalf("registry contention persisted deletion intent: %v", err)
	}
	if err := registryLock.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("Delete() after registry release = %#v, %v", result, err)
	}
}

func TestSessionManagerRejectsRegistryLockReplacementAfterInventory(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	receiptRoot, originalLock, err := manager.acquireReceiptRegistry(
		t.Context(),
		partition,
		nil,
		true,
	)
	if errors.Is(err, sessionlock.ErrUnsupported) {
		t.Skip("session locks are unsupported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	scan, err := manager.scanWorkspace(t.Context(), "", newDigestBudget())
	if err != nil {
		_ = originalLock.Close()
		t.Fatal(err)
	}
	if scan.receiptRegistryLockEvidence == nil {
		_ = originalLock.Close()
		t.Fatal("inventory omitted the persistent registry-lock identity")
	}
	lockPath := filepath.Join(receiptRoot.Path(), deleteRegistryLockName)
	if err := os.Remove(lockPath); err != nil {
		_ = originalLock.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		_ = originalLock.Close()
		t.Fatal(err)
	}
	replacementEvidence, err := inspectReceiptRegistryLock(receiptRoot)
	if err != nil {
		_ = originalLock.Close()
		t.Fatal(err)
	}
	if sameFileEvidence(replacementEvidence, *scan.receiptRegistryLockEvidence) {
		_ = originalLock.Close()
		t.Fatal("registry-lock replacement unexpectedly retained the old identity")
	}
	_, replacementLock, err := manager.acquireReceiptRegistry(
		t.Context(),
		partition,
		scan.receiptRegistryLockEvidence,
		false,
	)
	if err == nil {
		_ = replacementLock.Close()
		_ = originalLock.Close()
		t.Fatal("registry acquisition accepted a replacement lock inode")
	}
	if closeErr := originalLock.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestSessionManagerRequiresPersistentRegistryLockForDeletionMetadata(t *testing.T) {
	for _, test := range []struct {
		name       string
		sessionID  string
		injectHook func(*SessionManager, error)
	}{
		{
			name:      "live intent only",
			sessionID: "ses_missing_registry_intent",
			injectHook: func(manager *SessionManager, injected error) {
				manager.afterIntent = func() error { return injected }
			},
		},
		{
			name:      "detached stage and receipt",
			sessionID: "ses_missing_registry_stage",
			injectHook: func(manager *SessionManager, injected error) {
				manager.afterDetach = func() error { return injected }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, partition := newSessionManagementPartition(t)
			createSessionManagementCandidate(
				t,
				manager,
				test.sessionID,
				"persistent registry lock\n",
				sessionInventoryEpoch,
			)
			revision := onlySessionManagementItem(t, manager).Revision
			injected := errors.New("retain deletion metadata")
			test.injectHook(manager, injected)
			result, err := manager.Delete(t.Context(), test.sessionID, revision)
			if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
				t.Fatalf("deletion fixture = %#v, %v", result, err)
			}
			manager.afterIntent = nil
			manager.afterDetach = nil

			lockPath := filepath.Join(partition.Path(), deleteReceiptRoot, deleteRegistryLockName)
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
			listResult, listErr := manager.List(t.Context(), 1, "")
			if listResult.Status != SessionListStoreUnsafe ||
				!errors.Is(listErr, ErrSessionStoreUnsafe) {
				t.Fatalf("List() without persistent registry lock = %#v, %v; want store_unsafe", listResult, listErr)
			}
			deleteResult, deleteErr := manager.Delete(t.Context(), test.sessionID, revision)
			if deleteResult.Status != SessionStoreUnsafe ||
				!errors.Is(deleteErr, ErrSessionStoreUnsafe) {
				t.Fatalf("Delete() without persistent registry lock = %#v, %v; want store_unsafe", deleteResult, deleteErr)
			}
		})
	}
}

func TestSessionManagerFailsClosedOnUnsafeCandidateIdentity(t *testing.T) {
	t.Run("valid ID directory symlink", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(partition.Path(), "ses_symlink")); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})

	t.Run("valid ID non-directory", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		if err := os.WriteFile(filepath.Join(partition.Path(), "ses_file"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})

	t.Run("transcript symlink", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		owner, err := partition.CreatePrivateChild("ses_transcript_symlink")
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside-transcript")
		if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(owner.Path(), transcriptName)); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})

	t.Run("transcript hard link", func(t *testing.T) {
		manager, _ := newSessionManagementPartition(t)
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_transcript_hardlink",
			"linked transcript\n",
			sessionInventoryEpoch,
		)
		if err := os.Link(
			filepath.Join(sessionPath, transcriptName),
			filepath.Join(sessionPath, "transcript-alias"),
		); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})

	t.Run("lock hard link", func(t *testing.T) {
		manager, _ := newSessionManagementPartition(t)
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_lock_hardlink",
			"linked lock\n",
			sessionInventoryEpoch,
		)
		if err := os.Link(
			filepath.Join(sessionPath, sessionLockName),
			filepath.Join(sessionPath, "lock-alias"),
		); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})
}

func TestSessionManagerExcludesIncompleteForks(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	createSessionManagementCandidate(t, manager, "ses_complete", "complete\n", sessionInventoryEpoch)
	incompletePath := createSessionManagementCandidate(
		t,
		manager,
		"ses_incomplete",
		"incomplete\n",
		sessionInventoryEpoch.Add(time.Hour),
	)
	if err := os.WriteFile(
		filepath.Join(incompletePath, forkIncompleteMarker),
		[]byte("incomplete\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	assertSessionManagementIDs(t, listAllSessionManagementItems(t, manager, 1), []string{"ses_complete"})
	latest, err := manager.Latest(t.Context())
	if err != nil || latest != "ses_complete" {
		t.Fatalf("Latest() = %q, %v; want ses_complete", latest, err)
	}
	latestItem, err := manager.LatestItem(t.Context())
	if err != nil || latestItem.SessionID != "ses_complete" || latestItem.Revision == "" {
		t.Fatalf("LatestItem() = %#v, %v; want generation-bound ses_complete", latestItem, err)
	}
	completeState, err := manager.State(t.Context(), "ses_complete")
	if err != nil || !completeState.Resumable ||
		completeState.Revision != latestItem.Revision {
		t.Fatalf("complete session state = %#v, %v; want latest revision", completeState, err)
	}
	state, err := manager.State(t.Context(), "ses_incomplete")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.IncompleteFork || state.Resumable || state.DeletionPending {
		t.Fatalf("incomplete fork state = %#v", state)
	}
}

func TestSessionManagerStillValidatesIncompleteForkIdentities(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		"ses_incomplete_unsafe",
		"incomplete\n",
		sessionInventoryEpoch,
	)
	if err := os.WriteFile(
		filepath.Join(sessionPath, forkIncompleteMarker),
		[]byte("incomplete\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(sessionPath, transcriptName)
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-transcript")
	if err := os.WriteFile(external, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, transcriptPath); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	requireSessionManagementStoreUnsafe(t, manager)
}

func TestSessionManagerDeletionOutcomes(t *testing.T) {
	t.Run("deleted then not found", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_delete",
			"delete me\n",
			sessionInventoryEpoch,
		)
		if err := os.Mkdir(filepath.Join(sessionPath, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionPath, "nested", "state"), []byte("owned\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		revision := onlySessionManagementItem(t, manager).Revision

		result, err := manager.Delete(t.Context(), "ses_delete", revision)
		if err != nil || result.Status != SessionDeleted ||
			result.Version != SessionManagementVersion || result.SessionID != "ses_delete" {
			t.Fatalf("Delete() = %#v, %v; want deleted", result, err)
		}
		if _, err := os.Lstat(sessionPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted session remains at its live path: %v", err)
		}
		assertSessionManagementIDs(t, listAllSessionManagementItems(t, manager, 1), nil)

		result, err = manager.Delete(t.Context(), "ses_delete", revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("second Delete() = %#v, %v; want idempotent deleted", result, err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_stale",
			"before\n",
			sessionInventoryEpoch,
		)
		oldRevision := onlySessionManagementItem(t, manager).Revision
		if err := os.WriteFile(
			filepath.Join(sessionPath, transcriptName),
			[]byte("after revision changed\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}

		result, err := manager.Delete(t.Context(), "ses_stale", oldRevision)
		if result.Status != SessionStale || err == nil {
			t.Fatalf("Delete() with old revision = %#v, %v; want stale", result, err)
		}
		if _, err := os.Stat(sessionPath); err != nil {
			t.Fatalf("stale deletion removed the live session: %v", err)
		}
	})

	t.Run("replaced lock identity is stale", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_replaced_lock",
			"active old lock\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		lockPath := filepath.Join(sessionPath, sessionLockName)
		oldLock, err := sessionlock.Acquire(t.Context(), lockPath)
		if errors.Is(err, sessionlock.ErrUnsupported) {
			t.Skip("session locks are unsupported on this platform")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(lockPath, filepath.Join(sessionPath, "detached-old-lock")); err != nil {
			_ = oldLock.Close()
			t.Fatal(err)
		}
		replacement, err := sessionlock.Acquire(t.Context(), lockPath)
		if err != nil {
			_ = oldLock.Close()
			t.Fatal(err)
		}
		if err := replacement.Close(); err != nil {
			_ = oldLock.Close()
			t.Fatal(err)
		}

		result, deleteErr := manager.Delete(t.Context(), "ses_replaced_lock", revision)
		if result.Status != SessionStale || deleteErr == nil {
			_ = oldLock.Close()
			t.Fatalf("Delete() after lock replacement = %#v, %v; want stale", result, deleteErr)
		}
		if _, err := os.Stat(sessionPath); err != nil {
			_ = oldLock.Close()
			t.Fatalf("lock replacement allowed live session deletion: %v", err)
		}
		if err := oldLock.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("replaced transcript identity is stale", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_replaced_transcript",
			"old-body\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		transcriptPath := filepath.Join(sessionPath, transcriptName)
		info, err := os.Stat(transcriptPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(transcriptPath, filepath.Join(sessionPath, "detached-old-transcript")); err != nil {
			t.Fatal(err)
		}
		const replacementBody = "new-body\n"
		if err := os.WriteFile(transcriptPath, []byte(replacementBody), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(transcriptPath, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}

		result, deleteErr := manager.Delete(t.Context(), "ses_replaced_transcript", revision)
		if result.Status != SessionStale || deleteErr == nil {
			t.Fatalf("Delete() after transcript replacement = %#v, %v; want stale", result, deleteErr)
		}
		data, err := os.ReadFile(transcriptPath)
		if err != nil || string(data) != replacementBody {
			t.Fatalf("stale deletion changed replacement transcript: %q, %v", data, err)
		}
	})

	t.Run("replaced session directory identity is stale", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		const sessionID = "ses_replaced_directory"
		const replacementBody = "same-size-body\n"
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			replacementBody,
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		partition, exists, err := manager.OpenWorkspacePartition()
		if err != nil || !exists {
			t.Fatalf("open partition before replacement = %t, %v", exists, err)
		}
		displacedPath := filepath.Join(partition.Path(), ".displaced-old-session")
		if err := os.Rename(sessionPath, displacedPath); err != nil {
			t.Fatal(err)
		}
		replacementPath := createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			replacementBody,
			sessionInventoryEpoch,
		)

		result, deleteErr := manager.Delete(t.Context(), sessionID, revision)
		if result.Status != SessionStale || deleteErr == nil {
			t.Fatalf("Delete() after directory replacement = %#v, %v; want stale", result, deleteErr)
		}
		data, err := os.ReadFile(filepath.Join(replacementPath, transcriptName))
		if err != nil || string(data) != replacementBody {
			t.Fatalf("stale deletion changed replacement session: %q, %v", data, err)
		}
		if _, err := os.Stat(displacedPath); err != nil {
			t.Fatalf("stale deletion changed displaced original session: %v", err)
		}
	})

	t.Run("session locked", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_locked",
			"active\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		lock, err := sessionlock.Acquire(t.Context(), filepath.Join(sessionPath, sessionLockName))
		if errors.Is(err, sessionlock.ErrUnsupported) {
			t.Skip("session locks are unsupported on this platform")
		}
		if err != nil {
			t.Fatal(err)
		}
		result, deleteErr := manager.Delete(t.Context(), "ses_locked", revision)
		if result.Status != SessionLocked || deleteErr != nil {
			_ = lock.Close()
			t.Fatalf("Delete() under contention = %#v, %v; want session_locked", result, deleteErr)
		}
		if _, err := os.Stat(sessionPath); err != nil {
			_ = lock.Close()
			t.Fatalf("contended deletion removed the live session: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}

		result, err = manager.Delete(t.Context(), "ses_locked", revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("Delete() after lock release = %#v, %v; want deleted", result, err)
		}
	})
}

func TestSessionManagerRevisionBindsTranscriptContent(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		"ses_content_bound",
		"before!\n",
		sessionInventoryEpoch,
	)
	transcriptPath := filepath.Join(sessionPath, transcriptName)
	before, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := onlySessionManagementItem(t, manager).Revision
	if err := os.WriteFile(transcriptPath, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(transcriptPath, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		t.Fatal("content-binding fixture did not preserve inode, size, and modification time")
	}
	newRevision := onlySessionManagementItem(t, manager).Revision
	if newRevision == oldRevision {
		t.Fatal("same-inode, same-size transcript content replacement preserved the revision")
	}
	result, deleteErr := manager.Delete(t.Context(), "ses_content_bound", oldRevision)
	if result.Status != SessionStale || deleteErr == nil {
		t.Fatalf("Delete() with content-stale revision = %#v, %v; want stale", result, deleteErr)
	}
}

func TestSessionManagerOmitsUnrepresentableUpdatedAtButKeepsInternalOrdering(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	createSessionManagementCandidate(
		t,
		manager,
		"ses_canonical_time",
		"canonical\n",
		sessionInventoryEpoch,
	)
	extremePath := createSessionManagementCandidate(
		t,
		manager,
		"ses_extreme_time",
		"extreme\n",
		time.Time{},
	)
	extreme := time.Date(12000, time.January, 2, 3, 4, 5, 6, time.UTC)
	transcriptPath := filepath.Join(extremePath, transcriptName)
	if err := os.Chtimes(transcriptPath, extreme, extreme); err != nil {
		t.Skipf("filesystem does not support an out-of-RFC3339 timestamp: %v", err)
	}
	info, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Year() <= 9999 {
		t.Skipf("filesystem normalized the extreme timestamp to %v", info.ModTime())
	}
	items := listAllSessionManagementItems(t, manager, MaxSessionPageSize)
	assertSessionManagementIDs(t, items, []string{"ses_extreme_time", "ses_canonical_time"})
	if items[0].UpdatedAt != "" {
		t.Fatalf("extreme updated_at = %q; want omitted", items[0].UpdatedAt)
	}
	if _, err := time.Parse(time.RFC3339Nano, items[1].UpdatedAt); err != nil {
		t.Fatalf("ordinary updated_at = %q: %v", items[1].UpdatedAt, err)
	}
}

func TestSessionManagerRetainsIntentAfterUnsafeBoundaryChange(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	sessionPath := createSessionManagementCandidate(
		t,
		manager,
		"ses_boundary_change",
		"before!\n",
		sessionInventoryEpoch,
	)
	transcriptPath := filepath.Join(sessionPath, transcriptName)
	info, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := onlySessionManagementItem(t, manager).Revision
	manager.afterIntent = func() error {
		if err := os.WriteFile(transcriptPath, []byte("changed\n"), 0o600); err != nil {
			return err
		}
		return os.Chtimes(transcriptPath, info.ModTime(), info.ModTime())
	}
	result, deleteErr := manager.Delete(t.Context(), "ses_boundary_change", oldRevision)
	if result.Status != SessionDeleteIncomplete || deleteErr == nil {
		t.Fatalf("Delete() after boundary change = %#v, %v; want delete_incomplete", result, deleteErr)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, deleteIntentName)); err != nil {
		t.Fatalf("durable deletion intent was not retained: %v", err)
	}
	state, err := manager.State(t.Context(), "ses_boundary_change")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.DeletionPending || state.Resumable || state.Revision == oldRevision {
		t.Fatalf("boundary-changed session state = %#v; want hidden newer generation with pending deletion", state)
	}
	listed, listErr := manager.List(t.Context(), 1, "")
	if listed.Status != SessionListStoreUnsafe || !errors.Is(listErr, ErrSessionStoreUnsafe) {
		t.Fatalf("List() after unsafe boundary mutation = %#v, %v; want store_unsafe", listed, listErr)
	}
}

func TestEnsureDeletionIntentRecoversOnlyExactPrefixes(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		tempData  func([]byte) []byte
		wantError bool
	}{
		{name: "empty", tempData: func([]byte) []byte { return nil }},
		{name: "partial prefix", tempData: func(data []byte) []byte { return data[:len(data)/2] }},
		{name: "non prefix", tempData: func([]byte) []byte { return []byte("not-this-transaction\n") }, wantError: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			manager, partition := newSessionManagementPartition(t)
			_, registryLock, err := manager.acquireReceiptRegistry(t.Context(), partition, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := registryLock.Close(); err != nil {
				t.Fatal(err)
			}
			const sessionID = "ses_intent_prefix"
			createSessionManagementCandidate(t, manager, sessionID, "intent prefix\n", sessionInventoryEpoch)
			revision := onlySessionManagementItem(t, manager).Revision
			owner, err := partition.InspectPrivateChild(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			intent := deletionIntent{
				Version:     SessionManagementVersion,
				SessionID:   sessionID,
				Revision:    revision,
				StagingName: deletionStageName(sessionID, revision),
			}
			encoded, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			tempData := fixture.tempData(encoded)
			if err := os.WriteFile(
				filepath.Join(owner.Path(), deleteIntentTempName),
				tempData,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			listResult, listErr := manager.List(t.Context(), 1, "")
			if fixture.wantError {
				if !errors.Is(listErr, ErrSessionStoreUnsafe) ||
					listResult.Status != SessionListStoreUnsafe {
					t.Fatalf("inventory with non-prefix intent = %#v, %v; want store_unsafe", listResult, listErr)
				}
			} else if listErr != nil || listResult.Status != SessionListOK ||
				len(listResult.Sessions) != 0 {
				t.Fatalf("inventory with recoverable intent = %#v, %v", listResult, listErr)
			}
			err = ensureDeletionIntent(owner, intent, nil)
			if fixture.wantError {
				if err == nil {
					t.Fatal("non-prefix temporary intent was silently repaired")
				}
				got, readErr := os.ReadFile(filepath.Join(owner.Path(), deleteIntentTempName))
				if readErr != nil || !reflect.DeepEqual(got, tempData) {
					t.Fatalf("rejected temp changed: %q, %v", got, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("recover intent prefix: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(owner.Path(), deleteIntentName))
			if err != nil || !reflect.DeepEqual(got, encoded) {
				t.Fatalf("committed intent = %q, %v; want %q", got, err, encoded)
			}
			if _, err := os.Lstat(filepath.Join(owner.Path(), deleteIntentTempName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary intent remains after commit: %v", err)
			}
		})
	}

	t.Run("final and temp exact", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		_, registryLock, err := manager.acquireReceiptRegistry(t.Context(), partition, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := registryLock.Close(); err != nil {
			t.Fatal(err)
		}
		const sessionID = "ses_intent_both"
		createSessionManagementCandidate(t, manager, sessionID, "intent both\n", sessionInventoryEpoch)
		revision := onlySessionManagementItem(t, manager).Revision
		owner, err := partition.InspectPrivateChild(sessionID)
		if err != nil {
			t.Fatal(err)
		}
		intent := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   sessionID,
			Revision:    revision,
			StagingName: deletionStageName(sessionID, revision),
		}
		encoded, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		for _, name := range []string{deleteIntentName, deleteIntentTempName} {
			if err := os.WriteFile(filepath.Join(owner.Path(), name), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := manager.List(t.Context(), 1, "")
		if err != nil || result.Status != SessionListOK || len(result.Sessions) != 0 {
			t.Fatalf("inventory with exact final+temp = %#v, %v", result, err)
		}
		if err := ensureDeletionIntent(owner, intent, nil); err != nil {
			t.Fatalf("recover exact final+temp: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(owner.Path(), deleteIntentTempName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exact temp remains after recovery: %v", err)
		}
	})
}

func TestSessionManagerCleanupReceiptSurvivesPostRemovalFailure(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_cleanup_receipt"
	createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"cleanup receipt\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	injected := errors.New("injected after stage removal")
	manager.afterCleanup = func() error { return injected }

	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
		t.Fatalf("Delete() after stage removal = %#v, %v; want delete_incomplete", result, err)
	}
	assertSessionManagementPending(t, manager, sessionID)
	manager.afterCleanup = nil
	result, err = manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("receipt retry = %#v, %v; want deleted", result, err)
	}
}

func TestSessionManagerRecoversInterruptedReceiptPublication(t *testing.T) {
	for _, fixture := range []struct {
		name         string
		prefixLength func(int) int
	}{
		{name: "empty", prefixLength: func(int) int { return 0 }},
		{name: "partial record", prefixLength: func(size int) int { return size / 2 }},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			manager, _ := newSessionManagementPartition(t)
			const sessionID = "ses_receipt_publish"
			createSessionManagementCandidate(
				t,
				manager,
				sessionID,
				"receipt publication\n",
				sessionInventoryEpoch,
			)
			revision := onlySessionManagementItem(t, manager).Revision
			injected := errors.New("injected receipt publication crash")
			manager.afterIntent = func() error {
				scan, err := manager.scanWorkspace(t.Context(), sessionID, newDigestBudget())
				if err != nil {
					return err
				}
				candidate := scan.candidates[sessionID]
				if candidate == nil || candidate.intent == nil {
					return errors.New("deletion candidate is unavailable at receipt hook")
				}
				receipt := manager.receiptForCandidate(
					scan.partitionIdentity,
					candidate,
					*candidate.intent,
				)
				encoded, err := deletionReceiptData(receipt)
				if err != nil {
					return err
				}
				receiptRoot, err := scan.partition.CreatePrivateChild(deleteReceiptRoot)
				if errors.Is(err, os.ErrExist) {
					receiptRoot, err = scan.partition.InspectPrivateChild(deleteReceiptRoot)
				}
				if err != nil {
					return err
				}
				temp, err := receiptRoot.CreatePrivateChild(
					deletionReceiptTempName(sessionID, revision),
				)
				if err != nil {
					return err
				}
				prefixLength := fixture.prefixLength(len(encoded))
				if prefixLength > 0 {
					if err := os.WriteFile(
						filepath.Join(temp.Path(), deleteReceiptName),
						encoded[:prefixLength],
						0o600,
					); err != nil {
						return err
					}
				}
				if err := errors.Join(temp.Sync(), receiptRoot.Sync(), scan.partition.Sync()); err != nil {
					return err
				}
				return injected
			}
			result, err := manager.Delete(t.Context(), sessionID, revision)
			if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
				t.Fatalf("Delete() at receipt publication = %#v, %v", result, err)
			}
			assertSessionManagementPending(t, manager, sessionID)
			manager.afterIntent = nil
			result, err = manager.Delete(t.Context(), sessionID, revision)
			if err != nil || result.Status != SessionDeleted {
				t.Fatalf("receipt publication retry = %#v, %v; want deleted", result, err)
			}
		})
	}
}

func TestSessionManagerSweepsPartialReceiptGCStage(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	const oldID = "ses_gc_old"
	createSessionManagementCandidate(t, manager, oldID, "old receipt\n", sessionInventoryEpoch)
	oldRevision := onlySessionManagementItem(t, manager).Revision
	result, err := manager.Delete(t.Context(), oldID, oldRevision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("delete old session = %#v, %v", result, err)
	}
	scan, err := manager.scanWorkspace(t.Context(), oldID, newDigestBudget())
	if err != nil {
		t.Fatal(err)
	}
	record := scan.receipts[deletionReceiptKey(oldID, oldRevision)]
	if record == nil || !record.complete {
		t.Fatal("completed receipt is unavailable for GC fixture")
	}
	receiptRoot, registryLock, err := manager.acquireReceiptRegistry(t.Context(), partition, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	gcName := deletionReceiptGCName(record.name)
	detached, detachErr := receiptRoot.DetachPrivateChild(record.owner, gcName)
	if !detached.Committed {
		_ = registryLock.Close()
		t.Fatalf("detach GC fixture: %v", detachErr)
	}
	if err := os.Remove(filepath.Join(detached.Owner.Path(), deleteReceiptName)); err != nil {
		_ = registryLock.Close()
		t.Fatal(err)
	}
	if err := errors.Join(detached.Owner.Sync(), receiptRoot.Sync(), registryLock.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.List(t.Context(), 1, ""); err != nil {
		t.Fatalf("validated partial GC stage broke read-only inventory: %v", err)
	}

	const newID = "ses_gc_new"
	createSessionManagementCandidate(
		t,
		manager,
		newID,
		"new receipt\n",
		sessionInventoryEpoch.Add(time.Hour),
	)
	newRevision := onlySessionManagementItem(t, manager).Revision
	result, err = manager.Delete(t.Context(), newID, newRevision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("delete with partial GC sweep = %#v, %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(receiptRoot.Path(), gcName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial GC stage remains after serialized sweep: %v", err)
	}
}

func TestSessionManagerRejectsOverlappingFinalAndGCReceipts(t *testing.T) {
	manager, partition := newSessionManagementPartition(t)
	const sessionID = "ses_receipt_overlap"
	createSessionManagementCandidate(t, manager, sessionID, "receipt overlap\n", sessionInventoryEpoch)
	revision := onlySessionManagementItem(t, manager).Revision
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("delete receipt fixture = %#v, %v", result, err)
	}
	scan, err := manager.scanWorkspace(t.Context(), sessionID, newDigestBudget())
	if err != nil {
		t.Fatal(err)
	}
	record := scan.receipts[deletionReceiptKey(sessionID, revision)]
	if record == nil {
		t.Fatal("completed receipt fixture is missing")
	}
	receiptRoot, err := partition.InspectPrivateChild(deleteReceiptRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiptRoot.CreatePrivateChild(deletionReceiptGCName(record.name)); err != nil {
		t.Fatal(err)
	}
	if err := receiptRoot.Sync(); err != nil {
		t.Fatal(err)
	}
	requireSessionManagementStoreUnsafe(t, manager)
}

func TestSessionManagerCompletedReceiptDoesNotTargetRecreatedGeneration(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_recreated"
	createSessionManagementCandidate(t, manager, sessionID, "old generation\n", sessionInventoryEpoch)
	oldRevision := onlySessionManagementItem(t, manager).Revision
	result, err := manager.Delete(t.Context(), sessionID, oldRevision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("delete old generation = %#v, %v", result, err)
	}
	newPath := createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"new generation\n",
		sessionInventoryEpoch.Add(time.Hour),
	)
	newRevision := onlySessionManagementItem(t, manager).Revision
	if newRevision == oldRevision {
		t.Fatal("recreated session reused the old revision")
	}
	result, err = manager.Delete(t.Context(), sessionID, oldRevision)
	if result.Status != SessionStale || err == nil {
		t.Fatalf("old receipt against recreated generation = %#v, %v; want stale", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(newPath, transcriptName)); err != nil ||
		string(data) != "new generation\n" {
		t.Fatalf("old receipt changed recreated generation: %q, %v", data, err)
	}
	injected := errors.New("leave new generation staged")
	manager.afterDetach = func() error { return injected }
	result, err = manager.Delete(t.Context(), sessionID, newRevision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
		t.Fatalf("stage recreated generation = %#v, %v", result, err)
	}
	result, err = manager.Delete(t.Context(), sessionID, oldRevision)
	if result.Status != SessionStale || err == nil {
		t.Fatalf("old receipt against recreated stage = %#v, %v; want stale", result, err)
	}
	manager.afterDetach = nil
	result, err = manager.Delete(t.Context(), sessionID, newRevision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("cleanup recreated stage = %#v, %v", result, err)
	}
}

func TestSessionManagerReceiptRecoversPartialDetachedStage(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_partial_stage"
	createSessionManagementCandidate(t, manager, sessionID, "partial stage\n", sessionInventoryEpoch)
	revision := onlySessionManagementItem(t, manager).Revision
	injected := errors.New("stop after detach")
	manager.afterDetach = func() error { return injected }
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
		t.Fatalf("Delete() after detach = %#v, %v", result, err)
	}
	partition, exists, err := manager.OpenWorkspacePartition()
	if err != nil || !exists {
		t.Fatalf("open partition = %t, %v", exists, err)
	}
	stagePath := filepath.Join(partition.Path(), deletionStageName(sessionID, revision))
	if err := os.Remove(filepath.Join(stagePath, transcriptName)); err != nil {
		t.Fatal(err)
	}
	manager.afterDetach = nil
	result, err = manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("partial-stage receipt retry = %#v, %v; want deleted", result, err)
	}
}

func TestSessionManagerRecoversValidatedUnreceiptedCompatibilityStage(t *testing.T) {
	for _, fixture := range []struct {
		name              string
		sessionID         string
		createReceiptRoot bool
	}{
		{
			name:      "missing receipt root",
			sessionID: "ses_compatibility_stage_missing",
		},
		{
			name:              "empty receipt root",
			sessionID:         "ses_compatibility_stage_empty",
			createReceiptRoot: true,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			manager, partition := newSessionManagementPartition(t)
			createSessionManagementCandidate(
				t,
				manager,
				fixture.sessionID,
				"compatibility stage\n",
				sessionInventoryEpoch,
			)
			revision := onlySessionManagementItem(t, manager).Revision
			partitionIdentity, _, err := boundedPartitionSnapshot(partition)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := manager.inspectSessionCandidate(
				t.Context(),
				partition,
				partitionIdentity,
				fixture.sessionID,
				fixture.sessionID,
				newDigestBudget(),
			)
			if err != nil {
				t.Fatal(err)
			}
			intent := deletionIntent{
				Version:     SessionManagementVersion,
				SessionID:   fixture.sessionID,
				Revision:    revision,
				StagingName: deletionStageName(fixture.sessionID, revision),
			}
			if err := ensureDeletionIntent(candidate.owner, intent, nil); err != nil {
				t.Fatal(err)
			}
			detached, err := partition.DetachPrivateChild(candidate.owner, intent.StagingName)
			if errors.Is(err, platform.ErrAtomicRenameNoReplaceUnsupported) {
				t.Skip("atomic no-replace detach is unsupported on this platform")
			}
			if err != nil || !detached.Committed {
				t.Fatalf("prepare compatibility stage = %#v, %v", detached, err)
			}
			if fixture.createReceiptRoot {
				if _, err := partition.CreatePrivateChild(deleteReceiptRoot); err != nil {
					t.Fatal(err)
				}
				if err := partition.Sync(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Lstat(
				filepath.Join(partition.Path(), deleteReceiptRoot),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("compatibility fixture unexpectedly has a receipt root: %v", err)
			}

			result, err := manager.Delete(t.Context(), fixture.sessionID, revision)
			if err != nil || result.Status != SessionDeleted {
				t.Fatalf("Delete() compatibility stage = %#v, %v; want deleted", result, err)
			}
			assertSessionManagementStageAbsent(t, manager, fixture.sessionID, revision)
		})
	}
}

func TestSessionManagerDeletionRetriesAtEachCommittedBoundary(t *testing.T) {
	injected := errors.New("injected deletion failure")

	t.Run("after intent", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_after_intent",
			"intent recovery\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		manager.afterIntent = func() error { return injected }

		result, err := manager.Delete(t.Context(), "ses_after_intent", revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("Delete() after intent failure = %#v, %v", result, err)
		}
		if _, err := os.Stat(sessionPath); err != nil {
			t.Fatalf("intent-only recovery lost live session identity: %v", err)
		}
		assertSessionManagementPending(t, manager, "ses_after_intent")
		assertSessionManagementIDs(t, listAllSessionManagementItems(t, manager, 1), nil)

		manager.afterIntent = nil
		result, err = manager.Delete(t.Context(), "ses_after_intent", revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("intent retry by original ID/revision = %#v, %v; want deleted", result, err)
		}
	})

	t.Run("after receipt publication", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		manager.receiptEntryLimitForTest = 1
		const sessionID = "ses_after_receipt"
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"receipt commit recovery\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		manager.afterReceipt = func() error { return injected }

		result, err := manager.Delete(t.Context(), sessionID, revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("Delete() after receipt publication failure = %#v, %v", result, err)
		}
		if _, err := os.Stat(sessionPath); err != nil {
			t.Fatalf("receipt-only recovery lost the live session identity: %v", err)
		}
		assertSessionManagementPending(t, manager, sessionID)
		assertSessionManagementIDs(t, listAllSessionManagementItems(t, manager, 1), nil)

		manager.afterReceipt = nil
		result, err = manager.Delete(t.Context(), sessionID, revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("receipt-publication retry = %#v, %v; want deleted", result, err)
		}
	})

	t.Run("after detach", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_after_detach",
			"detach recovery\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		manager.afterDetach = func() error { return injected }

		result, err := manager.Delete(t.Context(), "ses_after_detach", revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("Delete() after detach failure = %#v, %v", result, err)
		}
		assertSessionManagementDetached(t, manager, "ses_after_detach", revision, sessionPath)
		manager.afterDetach = nil

		result, err = manager.Delete(t.Context(), "ses_after_detach", staleSessionManagementRevision())
		if result.Status != SessionStale || err == nil {
			t.Fatalf("staged Delete() with stale revision = %#v, %v; want stale", result, err)
		}
		assertSessionManagementPending(t, manager, "ses_after_detach")

		result, err = manager.Delete(t.Context(), "ses_after_detach", revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("detached retry by original ID/revision = %#v, %v; want deleted", result, err)
		}
		assertSessionManagementStageAbsent(t, manager, "ses_after_detach", revision)
	})

	t.Run("before cleanup", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			"ses_before_cleanup",
			"cleanup recovery\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		manager.beforeCleanup = func() error { return injected }

		result, err := manager.Delete(t.Context(), "ses_before_cleanup", revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("Delete() before cleanup failure = %#v, %v", result, err)
		}
		assertSessionManagementDetached(t, manager, "ses_before_cleanup", revision, sessionPath)
		manager.beforeCleanup = nil

		result, err = manager.Delete(t.Context(), "ses_before_cleanup", revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("cleanup retry by original ID/revision = %#v, %v; want deleted", result, err)
		}
		assertSessionManagementStageAbsent(t, manager, "ses_before_cleanup", revision)
	})

	t.Run("staging identity moved before cleanup", func(t *testing.T) {
		sessionsRoot := newSessionManagementRoot(t)
		manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
		const sessionID = "ses_moved_cleanup"
		sessionPath := createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"moved cleanup identity\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		partition, exists, err := manager.OpenWorkspacePartition()
		if err != nil || !exists {
			t.Fatalf("open partition = %t, %v", exists, err)
		}
		stagePath := filepath.Join(partition.Path(), deletionStageName(sessionID, revision))
		movedPath := filepath.Join(partition.Path(), ".moved-away")
		manager.beforeCleanup = func() error {
			return os.Rename(stagePath, movedPath)
		}

		result, err := manager.Delete(t.Context(), sessionID, revision)
		if result.Status != SessionDeleteIncomplete || err == nil {
			t.Fatalf("Delete() after staging move = %#v, %v; want delete_incomplete", result, err)
		}
		if _, err := os.Lstat(sessionPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("live session name remains after committed detach: %v", err)
		}
		if _, err := os.Stat(filepath.Join(movedPath, transcriptName)); err != nil {
			t.Fatalf("moved detached identity was removed or lost: %v", err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})
}

func TestSessionManagerConcurrentDeleteListAndSelection(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	createSessionManagementCandidate(
		t,
		manager,
		"ses_concurrent_delete",
		"concurrent deletion\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	intentReady := make(chan struct{})
	releaseDelete := make(chan struct{})
	manager.afterIntent = func() error {
		close(intentReady)
		<-releaseDelete
		return nil
	}
	type deleteOutcome struct {
		result SessionDeleteResult
		err    error
	}
	done := make(chan deleteOutcome, 1)
	go func() {
		result, err := manager.Delete(t.Context(), "ses_concurrent_delete", revision)
		done <- deleteOutcome{result: result, err: err}
	}()
	select {
	case <-intentReady:
	case <-time.After(5 * time.Second):
		t.Fatal("deletion did not commit its intent")
	}

	inventory, err := manager.List(t.Context(), 1, "")
	if err != nil || inventory.Status != SessionListOK || len(inventory.Sessions) != 0 {
		close(releaseDelete)
		t.Fatalf("List() during deletion = %#v, %v; want complete empty inventory", inventory, err)
	}
	state, err := manager.State(t.Context(), "ses_concurrent_delete")
	if err != nil || !state.DeletionPending || state.Resumable {
		close(releaseDelete)
		t.Fatalf("State() during deletion = %#v, %v; want pending", state, err)
	}
	if _, err := manager.Latest(t.Context()); !errors.Is(err, ErrNoPreviousSession) {
		close(releaseDelete)
		t.Fatalf("Latest() during deletion = %v, want no previous session", err)
	}
	contended, err := manager.Delete(t.Context(), "ses_concurrent_delete", revision)
	if err != nil || contended.Status != SessionLocked {
		close(releaseDelete)
		t.Fatalf("concurrent Delete() = %#v, %v; want session_locked", contended, err)
	}
	close(releaseDelete)
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Status != SessionDeleted {
			t.Fatalf("primary Delete() = %#v, %v; want deleted", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary deletion did not finish")
	}
}

func TestSessionManagerLiveLockRaceRestoresStageBeforeRegistryOrder(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_live_lock_race_order"
	createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"live lock race ordering\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	injected := errors.New("retain detached stage")
	manager.afterDetach = func() error { return injected }
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
		t.Fatalf("prepare detached stage = %#v, %v", result, err)
	}
	manager.afterDetach = nil

	scan, err := manager.scanWorkspace(t.Context(), sessionID, newDigestBudget())
	if err != nil {
		t.Fatal(err)
	}
	stage := scan.stages[sessionID]
	if stage == nil || stage.lockEvidence == nil {
		t.Fatalf("detached recovery stage = %#v; want retained lock evidence", stage)
	}
	var heldStage *sessionlock.Lock
	manager.beforeLockRaceStageCleanup = func() error {
		var acquireErr error
		heldStage, acquireErr = sessionlock.AcquireExisting(
			t.Context(),
			filepath.Join(stage.owner.Path(), sessionLockName),
		)
		if acquireErr != nil {
			return fmt.Errorf("acquire staged lock in required first position: %w", acquireErr)
		}
		probe, acquireErr := sessionlock.AcquireExisting(
			t.Context(),
			filepath.Join(scan.partition.Path(), deleteReceiptRoot, deleteRegistryLockName),
		)
		if acquireErr != nil {
			_ = heldStage.Close()
			heldStage = nil
			return fmt.Errorf("registry remained held before ordinary staged cleanup: %w", acquireErr)
		}
		return probe.Close()
	}
	result, err = manager.reconcileLiveLockRace(
		t.Context(),
		SessionDeleteResult{
			Version:   SessionManagementVersion,
			Status:    SessionStoreUnsafe,
			SessionID: sessionID,
		},
		scan,
		sessionID,
		revision,
		newDigestBudget(),
	)
	manager.beforeLockRaceStageCleanup = nil
	if heldStage == nil {
		t.Fatal("lock-order probe did not acquire the staged session lock")
	}
	if result.Status != SessionLocked || err != nil {
		_ = heldStage.Close()
		t.Fatalf("reconciled staged cleanup = %#v, %v; want session_locked", result, err)
	}
	if err := heldStage.Close(); err != nil {
		t.Fatal(err)
	}

	result, err = manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("cleanup after ordered reconciliation = %#v, %v; want deleted", result, err)
	}
}

func TestSessionManagerCarriesRegistryLeaseAcrossLiveCleanupHandoff(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_live_cleanup_handoff"
	createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"live cleanup handoff\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	cleanupReady := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.beforeCleanup = func() error {
		close(cleanupReady)
		<-releaseCleanup
		return nil
	}
	type outcome struct {
		result SessionDeleteResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := manager.Delete(t.Context(), sessionID, revision)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-cleanupReady:
	case <-time.After(5 * time.Second):
		t.Fatal("live deletion did not reach cleanup with its registry lease")
	}
	contended, err := manager.Delete(t.Context(), sessionID, revision)
	if err != nil || contended.Status != SessionLocked {
		close(releaseCleanup)
		t.Fatalf("Delete() during live cleanup handoff = %#v, %v; want session_locked", contended, err)
	}
	close(releaseCleanup)
	select {
	case primary := <-done:
		if primary.err != nil || primary.result.Status != SessionDeleted {
			t.Fatalf("primary live deletion = %#v, %v", primary.result, primary.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary live deletion did not finish")
	}
	manager.beforeCleanup = nil
}

func TestSessionManagerReleasesHandedOffRegistryLeaseOnCancellation(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_cancelled_cleanup_handoff"
	createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"cancelled cleanup handoff\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	ctx, cancel := context.WithCancel(t.Context())
	manager.afterDetach = func() error {
		cancel()
		return nil
	}
	result, err := manager.Delete(ctx, sessionID, revision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup handoff = %#v, %v; want delete_incomplete", result, err)
	}
	manager.afterDetach = nil
	result, err = manager.Delete(t.Context(), sessionID, revision)
	if err != nil || result.Status != SessionDeleted {
		t.Fatalf("retry after cancelled cleanup handoff = %#v, %v; want deleted", result, err)
	}
}

func TestSessionManagerSerializesConcurrentStagedCleanup(t *testing.T) {
	manager, _ := newSessionManagementPartition(t)
	const sessionID = "ses_concurrent_cleanup"
	createSessionManagementCandidate(
		t,
		manager,
		sessionID,
		"concurrent staged cleanup\n",
		sessionInventoryEpoch,
	)
	revision := onlySessionManagementItem(t, manager).Revision
	injected := errors.New("leave detached stage")
	manager.afterDetach = func() error { return injected }
	result, err := manager.Delete(t.Context(), sessionID, revision)
	if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
		t.Fatalf("stage fixture deletion = %#v, %v", result, err)
	}
	manager.afterDetach = nil

	cleanupReady := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.beforeCleanup = func() error {
		close(cleanupReady)
		<-releaseCleanup
		return nil
	}
	type cleanupOutcome struct {
		result SessionDeleteResult
		err    error
	}
	done := make(chan cleanupOutcome, 1)
	go func() {
		result, err := manager.Delete(t.Context(), sessionID, revision)
		done <- cleanupOutcome{result: result, err: err}
	}()
	select {
	case <-cleanupReady:
	case <-time.After(5 * time.Second):
		t.Fatal("primary staged cleanup did not acquire its registry lease")
	}
	contended, err := manager.Delete(t.Context(), sessionID, revision)
	if err != nil || contended.Status != SessionLocked {
		close(releaseCleanup)
		t.Fatalf("concurrent staged cleanup = %#v, %v; want session_locked", contended, err)
	}
	close(releaseCleanup)
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Status != SessionDeleted {
			t.Fatalf("primary staged cleanup = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary staged cleanup did not finish")
	}
	manager.beforeCleanup = nil
}

func TestSessionManagerReconcilesStaleCleanupSnapshots(t *testing.T) {
	createStage := func(t *testing.T, sessionID string) (*SessionManager, *workspaceScan, *deletionStage, string) {
		t.Helper()
		manager, _ := newSessionManagementPartition(t)
		createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"stale cleanup snapshot\n",
			sessionInventoryEpoch,
		)
		revision := onlySessionManagementItem(t, manager).Revision
		injected := errors.New("retain staged deletion")
		manager.afterDetach = func() error { return injected }
		result, err := manager.Delete(t.Context(), sessionID, revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("prepare staged deletion = %#v, %v", result, err)
		}
		manager.afterDetach = nil
		scan, err := manager.scanWorkspace(t.Context(), sessionID, newDigestBudget())
		if err != nil {
			t.Fatal(err)
		}
		stage := scan.stages[sessionID]
		if stage == nil || stage.receipt == nil {
			t.Fatalf("staged deletion snapshot = %#v; want receipted stage", stage)
		}
		return manager, scan, stage, revision
	}

	t.Run("winner completed", func(t *testing.T) {
		const sessionID = "ses_stale_cleanup_complete"
		manager, staleScan, staleStage, revision := createStage(t, sessionID)
		result, err := manager.Delete(t.Context(), sessionID, revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("winning cleanup = %#v, %v", result, err)
		}
		result, err = manager.cleanupStage(
			t.Context(),
			SessionDeleteResult{
				Version:   SessionManagementVersion,
				Status:    SessionStoreUnsafe,
				SessionID: sessionID,
			},
			staleScan,
			staleStage,
			newDigestBudget(),
		)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("stale completed cleanup = %#v, %v; want deleted", result, err)
		}
	})

	t.Run("winner removed stage before receipt completion", func(t *testing.T) {
		const sessionID = "ses_stale_cleanup_pending"
		manager, staleScan, staleStage, revision := createStage(t, sessionID)
		injected := errors.New("stop after recursive cleanup")
		manager.afterCleanup = func() error { return injected }
		result, err := manager.Delete(t.Context(), sessionID, revision)
		if result.Status != SessionDeleteIncomplete || !errors.Is(err, injected) {
			t.Fatalf("partial winning cleanup = %#v, %v", result, err)
		}
		manager.afterCleanup = nil
		result, err = manager.cleanupStage(
			t.Context(),
			SessionDeleteResult{
				Version:   SessionManagementVersion,
				Status:    SessionStoreUnsafe,
				SessionID: sessionID,
			},
			staleScan,
			staleStage,
			newDigestBudget(),
		)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("stale pending cleanup = %#v, %v; want deleted", result, err)
		}
	})

	t.Run("same ID was recreated", func(t *testing.T) {
		const sessionID = "ses_stale_cleanup_recreated"
		manager, staleScan, staleStage, revision := createStage(t, sessionID)
		result, err := manager.Delete(t.Context(), sessionID, revision)
		if err != nil || result.Status != SessionDeleted {
			t.Fatalf("winning cleanup = %#v, %v", result, err)
		}
		newPath := createSessionManagementCandidate(
			t,
			manager,
			sessionID,
			"new generation after stale cleanup\n",
			sessionInventoryEpoch.Add(time.Hour),
		)
		result, err = manager.cleanupStage(
			t.Context(),
			SessionDeleteResult{
				Version:   SessionManagementVersion,
				Status:    SessionStoreUnsafe,
				SessionID: sessionID,
			},
			staleScan,
			staleStage,
			newDigestBudget(),
		)
		if result.Status != SessionStale || !errors.Is(err, errDeletionGenerationStale) {
			t.Fatalf("stale cleanup against recreated ID = %#v, %v; want stale", result, err)
		}
		data, readErr := os.ReadFile(filepath.Join(newPath, transcriptName))
		if readErr != nil || string(data) != "new generation after stale cleanup\n" {
			t.Fatalf("stale cleanup changed new generation: %q, %v", data, readErr)
		}
	})

	t.Run("contended stage is not reinspected first", func(t *testing.T) {
		const sessionID = "ses_stale_cleanup_contended"
		manager, staleScan, staleStage, _ := createStage(t, sessionID)
		lock, err := sessionlock.AcquireExisting(
			t.Context(),
			filepath.Join(staleStage.owner.Path(), sessionLockName),
		)
		if errors.Is(err, sessionlock.ErrUnsupported) {
			t.Skip("session locks are unsupported on this platform")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(staleStage.owner.Path(), transcriptName),
			[]byte("changed while externally locked\n"),
			0o600,
		); err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		result, cleanupErr := manager.cleanupStage(
			t.Context(),
			SessionDeleteResult{
				Version:   SessionManagementVersion,
				Status:    SessionStoreUnsafe,
				SessionID: sessionID,
			},
			staleScan,
			staleStage,
			newDigestBudget(),
		)
		if result.Status != SessionLocked || cleanupErr != nil {
			_ = lock.Close()
			t.Fatalf("contended stale cleanup = %#v, %v; want session_locked", result, cleanupErr)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessionManagerRejectsForgedOrMismatchedDeletionStages(t *testing.T) {
	t.Run("empty cleanup remnant without receipt is unsafe", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		revision := staleSessionManagementRevision()
		if _, err := partition.CreatePrivateChild(deletionStageName("ses_forged", revision)); err != nil {
			t.Fatal(err)
		}
		result, err := manager.Delete(t.Context(), "ses_forged", revision)
		if !errors.Is(err, ErrSessionStoreUnsafe) || result.Status != SessionStoreUnsafe {
			t.Fatalf("cleanup of empty unreceipted stage = %#v, %v; want store_unsafe", result, err)
		}
	})

	t.Run("mismatched committed intent", func(t *testing.T) {
		manager, partition := newSessionManagementPartition(t)
		revision := staleSessionManagementRevision()
		name := deletionStageName("ses_forged", revision)
		owner, err := partition.CreatePrivateChild(name)
		if err != nil {
			t.Fatal(err)
		}
		mismatched := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   "ses_other",
			Revision:    revision,
			StagingName: deletionStageName("ses_other", revision),
		}
		data, err := json.Marshal(mismatched)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(owner.Path(), deleteIntentName), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		requireSessionManagementStoreUnsafe(t, manager)
	})
}

func TestSessionManagerJSONProjectionContainsNoStoreOrTranscriptData(t *testing.T) {
	sessionsRoot := newSessionManagementRoot(t)
	workspace := filepath.Clean(t.TempDir())
	manager := newSessionManagementManager(t, sessionsRoot, workspace)
	const transcriptSecret = "PROMPT TITLE TOOL DATA MUST REMAIN PRIVATE"
	createSessionManagementCandidate(
		t,
		manager,
		"ses_projection",
		transcriptSecret+"\n",
		sessionInventoryEpoch,
	)
	listResult, err := manager.List(t.Context(), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	listJSON, err := json.Marshal(listResult)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionManagementJSONKeys(t, listJSON, []string{"sessions", "status", "version"})
	var decoded struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(listJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Sessions) != 1 {
		t.Fatalf("JSON sessions = %d, want 1", len(decoded.Sessions))
	}
	assertSessionManagementJSONKeys(
		t,
		decoded.Sessions[0],
		[]string{"revision", "session_id", "updated_at"},
	)
	assertSessionManagementJSONOmits(
		t,
		listJSON,
		transcriptSecret,
		transcriptName,
		sessionsRoot.Path(),
		workspace,
		manager.workspaceKey,
	)

	revision := listResult.Sessions[0].Revision
	deleteResult, err := manager.Delete(t.Context(), "ses_projection", revision)
	if err != nil || deleteResult.Status != SessionDeleted {
		t.Fatalf("Delete() = %#v, %v", deleteResult, err)
	}
	deleteJSON, err := json.Marshal(deleteResult)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionManagementJSONKeys(t, deleteJSON, []string{"session_id", "status", "version"})
	assertSessionManagementJSONOmits(
		t,
		deleteJSON,
		transcriptSecret,
		transcriptName,
		sessionsRoot.Path(),
		workspace,
		manager.workspaceKey,
	)
}

func newSessionManagementRoot(t *testing.T) *platform.OwnedDirectory {
	t.Helper()
	root, err := platform.AcquirePrivateDirectory(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func newSessionManagementManager(
	t *testing.T,
	sessionsRoot *platform.OwnedDirectory,
	workspace string,
) *SessionManager {
	t.Helper()
	manager, err := NewSessionManager(sessionsRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func newSessionManagementPartition(t *testing.T) (*SessionManager, *platform.OwnedDirectory) {
	t.Helper()
	sessionsRoot := newSessionManagementRoot(t)
	manager := newSessionManagementManager(t, sessionsRoot, filepath.Clean(t.TempDir()))
	partition, err := manager.EnsureWorkspacePartition()
	if err != nil {
		t.Fatal(err)
	}
	return manager, partition
}

func createSessionManagementCandidate(
	t *testing.T,
	manager *SessionManager,
	sessionID string,
	transcriptBody string,
	updated time.Time,
) string {
	t.Helper()
	partition, err := manager.EnsureWorkspacePartition()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := partition.CreatePrivateChild(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(owner.Path(), transcriptName)
	if err := os.WriteFile(transcriptPath, []byte(transcriptBody), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := sessionlock.Acquire(t.Context(), filepath.Join(owner.Path(), sessionLockName))
	if errors.Is(err, sessionlock.ErrUnsupported) {
		t.Skip("session locks are unsupported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !updated.IsZero() {
		if err := os.Chtimes(transcriptPath, updated, updated); err != nil {
			t.Fatal(err)
		}
	}
	return owner.Path()
}

func onlySessionManagementItem(t *testing.T, manager *SessionManager) SessionInventoryItem {
	t.Helper()
	items := listAllSessionManagementItems(t, manager, MaxSessionPageSize)
	if len(items) != 1 {
		t.Fatalf("inventory has %d sessions, want 1: %#v", len(items), items)
	}
	return items[0]
}

func listAllSessionManagementItems(
	t *testing.T,
	manager *SessionManager,
	pageSize int,
) []SessionInventoryItem {
	t.Helper()
	var items []SessionInventoryItem
	token := ""
	seen := make(map[string]struct{})
	for {
		result, err := manager.List(t.Context(), pageSize, token)
		if err != nil {
			t.Fatalf("List(%q): %v", token, err)
		}
		if result.Status != SessionListOK {
			t.Fatalf("List(%q) status = %q", token, result.Status)
		}
		items = append(items, result.Sessions...)
		if result.NextPageToken == "" {
			return items
		}
		if _, duplicate := seen[result.NextPageToken]; duplicate {
			t.Fatalf("pagination token repeated: %q", result.NextPageToken)
		}
		seen[result.NextPageToken] = struct{}{}
		token = result.NextPageToken
	}
}

func assertSessionManagementIDs(
	t *testing.T,
	items []SessionInventoryItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.SessionID)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session IDs = %#v, want %#v", got, want)
	}
}

func requireSessionManagementStoreUnsafe(t *testing.T, manager *SessionManager) {
	t.Helper()
	result, err := manager.List(t.Context(), MaxSessionPageSize, "")
	if !errors.Is(err, ErrSessionStoreUnsafe) || result.Status != SessionListStoreUnsafe ||
		result.Version != SessionManagementVersion {
		t.Fatalf("unsafe inventory = %#v, %v; want store_unsafe", result, err)
	}
}

func staleSessionManagementRevision() string {
	return "r1_" + base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
}

func assertSessionManagementPending(t *testing.T, manager *SessionManager, sessionID string) {
	t.Helper()
	pending, err := manager.DeletionPending(t.Context(), sessionID)
	if err != nil || !pending {
		t.Fatalf("DeletionPending(%q) = %t, %v; want true", sessionID, pending, err)
	}
}

func assertSessionManagementDetached(
	t *testing.T,
	manager *SessionManager,
	sessionID string,
	revision string,
	livePath string,
) {
	t.Helper()
	if _, err := os.Lstat(livePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached live session remains reachable: %v", err)
	}
	assertSessionManagementPending(t, manager, sessionID)
	assertSessionManagementIDs(t, listAllSessionManagementItems(t, manager, 1), nil)
	partition, exists, err := manager.OpenWorkspacePartition()
	if err != nil || !exists {
		t.Fatalf("open partition after detach = %t, %v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(partition.Path(), deletionStageName(sessionID, revision))); err != nil {
		t.Fatalf("deletion stage is not retryable: %v", err)
	}
}

func assertSessionManagementStageAbsent(
	t *testing.T,
	manager *SessionManager,
	sessionID string,
	revision string,
) {
	t.Helper()
	partition, exists, err := manager.OpenWorkspacePartition()
	if err != nil || !exists {
		t.Fatalf("open partition after cleanup = %t, %v", exists, err)
	}
	if _, err := os.Lstat(filepath.Join(partition.Path(), deletionStageName(sessionID, revision))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deletion stage remains after reported deletion: %v", err)
	}
	pending, err := manager.DeletionPending(t.Context(), sessionID)
	if err != nil || pending {
		t.Fatalf("DeletionPending(%q) after cleanup = %t, %v", sessionID, pending, err)
	}
}

func assertSessionManagementJSONKeys(t *testing.T, data []byte, want []string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(object))
	for _, key := range want {
		if _, exists := object[key]; !exists {
			t.Fatalf("JSON object %s is missing key %q", data, key)
		}
		got = append(got, key)
	}
	if len(object) != len(want) {
		t.Fatalf("JSON object keys = %#v, want exactly %#v; object=%s", object, want, data)
	}
}

func assertSessionManagementJSONOmits(t *testing.T, data []byte, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(data), value) {
			t.Fatalf("JSON output exposes forbidden value %q: %s", value, data)
		}
	}
}
