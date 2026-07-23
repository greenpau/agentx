package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultStoreRecoversSyncedUnindexedCrashOrphanForRetry(t *testing.T) {
	root := t.TempDir()
	if _, err := NewResultStore(root); err != nil {
		t.Fatal(err)
	}
	const id = "crashed-before-index"
	orphanContent := []byte(strings.Repeat("orphan", 100))
	writeSyncedResultFixture(t, filepath.Join(root, resultFilename(id)), orphanContent)

	resumed, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, resultFilename(id))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recoverable orphan was not removed: %v", err)
	}
	retryContent := strings.Repeat("retried", 100)
	if replacement := resumed.apply(id, retryContent, 64); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("retry remained blocked by recovered orphan: %q", replacement)
	}
	got, _, _, err := resumed.read(context.Background(), id, 0, len(retryContent))
	if err != nil || got != retryContent {
		t.Fatalf("retried result = %q, %v", got, err)
	}
}

func TestResultStoreTruncatesOnlyInvalidFinalIndexTailAndPreservesRecords(t *testing.T) {
	root := t.TempDir()
	store, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	stableContent := strings.Repeat("stable", 100)
	if replacement := store.apply("stable", stableContent, 64); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("seed persistence failed: %q", replacement)
	}
	indexPath := filepath.Join(root, resultIndexFilename)
	stableIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	const crashedID = "partial-index-append"
	writeSyncedResultFixture(t, filepath.Join(root, resultFilename(crashedID)), []byte(strings.Repeat("partial", 100)))
	appendSyncedFixture(t, indexPath, []byte(`{"id":"partial-index-append","file":"result-`))

	resumed, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(repaired) != string(stableIndex) {
		t.Fatalf("tail repair changed authoritative records:\nwant %q\ngot  %q", stableIndex, repaired)
	}
	got, _, _, err := resumed.read(context.Background(), "stable", 0, len(stableContent))
	if err != nil || got != stableContent {
		t.Fatalf("preserved result = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(root, resultFilename(crashedID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan paired with torn tail was not removed: %v", err)
	}
	retryContent := strings.Repeat("recovered", 100)
	if replacement := resumed.apply(crashedID, retryContent, 64); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("retry after tail repair failed: %q", replacement)
	}
}

func TestResultStoreRecognizesEveryPossibleIndexAppendCut(t *testing.T) {
	entry := storedResult{
		ID:     "unicode-\u2603",
		File:   resultFilename("unicode-\u2603"),
		Size:   12345,
		Digest: strings.Repeat("a", sha256.Size*2),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	for cut := 1; cut < len(line); cut++ {
		if !recoverableTornIndexTail(line[:cut]) {
			t.Fatalf("valid append prefix ending at byte %d was not recoverable: %q", cut, line[:cut])
		}
	}
}

func TestResultStoreRepairsCompleteUnterminatedFinalIndexRecord(t *testing.T) {
	root := t.TempDir()
	store, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if replacement := store.apply("stable", strings.Repeat("stable", 100), 64); !strings.Contains(replacement, "persisted-output") {
		t.Fatalf("seed persistence failed: %q", replacement)
	}
	indexPath := filepath.Join(root, resultIndexFilename)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	const recoveredID = "complete-without-newline"
	content := []byte(strings.Repeat("recovered", 100))
	writeSyncedResultFixture(t, filepath.Join(root, resultFilename(recoveredID)), content)
	digest := sha256.Sum256(content)
	entry := storedResult{
		ID:     recoveredID,
		File:   resultFilename(recoveredID),
		Size:   len(content),
		Digest: hex.EncodeToString(digest[:]),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	appendSyncedFixture(t, indexPath, line)

	resumed, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	wantIndex := append(append(append([]byte(nil), before...), line...), '\n')
	if string(repaired) != string(wantIndex) {
		t.Fatalf("complete final record repair mismatch:\nwant %q\ngot  %q", wantIndex, repaired)
	}
	got, _, _, err := resumed.read(context.Background(), recoveredID, 0, len(content))
	if err != nil || got != string(content) {
		t.Fatalf("recovered complete record = %q, %v", got, err)
	}
}

func TestResultStoreDoesNotTruncateCompleteInvalidFinalIndexRecord(t *testing.T) {
	for name, invalid := range map[string]string{
		"valid JSON with invalid ownership": `{}`,
		"closed malformed JSON":             `{"id":]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewResultStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if replacement := store.apply("stable", strings.Repeat("stable", 100), 64); !strings.Contains(replacement, "persisted-output") {
				t.Fatalf("seed persistence failed: %q", replacement)
			}
			indexPath := filepath.Join(root, resultIndexFilename)
			appendSyncedFixture(t, indexPath, []byte(invalid))
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewResultStore(root); err == nil {
				t.Fatal("complete invalid final record was silently truncated")
			}
			after, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("complete invalid final record was mutated")
			}
		})
	}
}

func TestResultStoreRecoversFromInjectedSyncFailures(t *testing.T) {
	t.Run("result file", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		store.syncFile = func(*os.File) error {
			return errors.New("injected result sync failure")
		}
		const id = "result-sync-failure"
		content := strings.Repeat("durable", 100)
		if replacement := store.apply(id, content, 64); !strings.Contains(replacement, "persistence is unavailable") {
			t.Fatalf("result sync failure falsely reported persistence: %q", replacement)
		}
		if _, err := os.Lstat(filepath.Join(root, resultFilename(id))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed result sync left a result artifact: %v", err)
		}
		resumed, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if replacement := resumed.apply(id, content, 64); !strings.Contains(replacement, "persisted-output") {
			t.Fatalf("result sync failure permanently blocked retry: %q", replacement)
		}
	})

	t.Run("index file", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		syncCalls := 0
		store.syncFile = func(file *os.File) error {
			syncCalls++
			if syncCalls == 2 {
				return errors.New("injected index sync failure")
			}
			return file.Sync()
		}
		const id = "uncertain-index-sync"
		content := strings.Repeat("durable", 100)
		if replacement := store.apply(id, content, 64); !strings.Contains(replacement, "persistence is unavailable") {
			t.Fatalf("sync failure falsely reported persistence: %q", replacement)
		}
		if _, err := os.Stat(filepath.Join(root, resultFilename(id))); err != nil {
			t.Fatalf("uncertain index append deleted synced content: %v", err)
		}
		resumed, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		got, _, _, err := resumed.read(context.Background(), id, 0, len(content))
		if err != nil || got != content {
			t.Fatalf("complete append after sync failure = %q, %v", got, err)
		}
	})

	t.Run("directory entry", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		store.syncDirectory = func(*os.File) error {
			if !failed {
				failed = true
				return errors.New("injected directory sync failure")
			}
			return nil
		}
		const id = "directory-sync-failure"
		content := strings.Repeat("durable", 100)
		if replacement := store.apply(id, content, 64); !strings.Contains(replacement, "persistence is unavailable") {
			t.Fatalf("directory sync failure falsely reported persistence: %q", replacement)
		}
		if _, err := os.Lstat(filepath.Join(root, resultFilename(id))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed pre-index creation left a result artifact: %v", err)
		}
		resumed, err := NewResultStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if replacement := resumed.apply(id, content, 64); !strings.Contains(replacement, "persisted-output") {
			t.Fatalf("directory sync failure permanently blocked retry: %q", replacement)
		}
	})
}

func TestResultStoreStartupReconciliationRejectsLinkedOrphans(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("do-not-read"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, resultFilename("linked-orphan"))
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewResultStore(root); err == nil {
			t.Fatal("symlink orphan was treated as a recoverable crash file")
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "do-not-read" {
			t.Fatalf("symlink target changed: %q, %v", content, err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "results")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "target")
		if err := os.WriteFile(target, []byte("do-not-remove"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, resultFilename("linked-orphan"))
		if err := os.Link(target, path); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		if _, err := NewResultStore(root); err == nil {
			t.Fatal("hard-linked orphan was treated as a recoverable crash file")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("hard-linked orphan was removed on rejection: %v", err)
		}
	})
}

func TestResultStoreOrdinaryPersistenceRefusesPlantedRegularFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewResultStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "planted-after-startup"
	path := filepath.Join(root, resultFilename(id))
	writeSyncedResultFixture(t, path, []byte("do-not-replace"))
	replacement := store.apply(id, strings.Repeat("new-output", 100), 64)
	if !strings.Contains(replacement, "unindexed pre-existing result file was refused") {
		t.Fatalf("ordinary persistence adopted a planted file: %q", replacement)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "do-not-replace" {
		t.Fatalf("planted file changed: %q, %v", content, err)
	}
}

func TestResultStoreStartupReconciliationIsBounded(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= maximumResultOrphanEntries; index++ {
		name := resultFilename("orphan-" + strings.Repeat("x", index))
		writeSyncedResultFixture(t, filepath.Join(root, name), []byte("bounded"))
	}
	if _, err := NewResultStore(root); err == nil {
		t.Fatal("excess startup orphan set was accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maximumResultOrphanEntries+1 {
		t.Fatalf("failed bounded reconciliation removed a partial orphan set: %d", len(entries))
	}
}

func writeSyncedResultFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeResultAll(file, content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendSyncedFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeResultAll(file, content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
