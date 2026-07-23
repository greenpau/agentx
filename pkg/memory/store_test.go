package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecallReturnsExplicitEmptyCollection(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Recall("", 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("empty recall = %#v, want a non-nil empty collection", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty recall JSON = %s, want []", encoded)
	}
}

func TestRememberRecallAndPermissions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("go-style", "Prefer table-driven Go tests."); err != nil {
		t.Fatal(err)
	}
	items, err := store.Recall("Go tests", 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "go-style" {
		t.Fatalf("items=%#v", items)
	}
	info, err := os.Stat(filepath.Join(store.root, "go-style.md"))
	if err != nil {
		t.Fatal(err)
	}
	if memoryPOSIXPermissionsEnforced && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestRejectsEscapeAndSecrets(t *testing.T) {
	store, _ := Open(t.TempDir())
	if err := store.Remember("../escape", "x"); err == nil {
		t.Fatal("expected path rejection")
	}
	if err := store.Remember("secret", "API_KEY=sk-abcdefghijklmnopqrstuvwxyz"); !errors.Is(err, ErrSecret) {
		t.Fatalf("err=%v", err)
	}
}

func TestConfiguredExactSecretGuardAppliesOnRememberAndRecall(t *testing.T) {
	const secret = "opaque-provider-credential-without-a-marker"
	root := t.TempDir()
	guard := func(value string) bool { return strings.Contains(value, secret) }
	store, err := Open(root, guard)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("direct", "prefix "+secret+" suffix"); !errors.Is(err, ErrSecret) {
		t.Fatalf("configured credential was persisted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "external.md"), []byte("prefix "+secret+" suffix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := store.Recall("", 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("externally introduced configured credential was recalled: %#v", items)
	}
}

func TestRecallGuardsCompleteEntryAndReturnedSlice(t *testing.T) {
	t.Run("complete entry", func(t *testing.T) {
		const framedCredential = `alpha","content":"omega`
		root := t.TempDir()
		store, err := Open(root, func(value string) bool {
			return strings.Contains(value, framedCredential)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remember("alpha", "omega"); err != nil {
			t.Fatal(err)
		}
		items, err := store.Recall("", 1000, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if items == nil || len(items) != 0 {
			t.Fatalf("entry whose framing reconstructed a credential was recalled: %#v", items)
		}
	})

	t.Run("complete returned slice", func(t *testing.T) {
		const framedCredential = `"score":0},{"name":"beta`
		root := t.TempDir()
		store, err := Open(root, func(value string) bool {
			return strings.Contains(value, framedCredential)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"alpha", "beta"} {
			if err := store.Remember(name, "ordinary content"); err != nil {
				t.Fatal(err)
			}
		}
		stamp := time.Unix(1_700_000_000, 0)
		for _, name := range []string{"alpha.md", "beta.md"} {
			if err := os.Chtimes(filepath.Join(root, name), stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		items, err := store.Recall("", 1000, stamp)
		if !errors.Is(err, ErrSecret) {
			t.Fatalf("Recall error = %v, want ErrSecret", err)
		}
		if items != nil {
			t.Fatalf("unsafe aggregate returned partial entries: %#v", items)
		}
	})
}

func TestPanickingConfiguredGuardFailsClosed(t *testing.T) {
	t.Run("remember", func(t *testing.T) {
		store, err := Open(t.TempDir(), func(value string) bool {
			if strings.Contains(value, "panic-on-write") {
				panic("credential-bearing panic payload")
			}
			return false
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remember("note", "panic-on-write"); !errors.Is(err, ErrSecret) {
			t.Fatalf("Remember error = %v, want ErrSecret", err)
		}
	})

	t.Run("complete recall projection", func(t *testing.T) {
		store, err := Open(t.TempDir(), func(value string) bool {
			if strings.Contains(value, `","content":"`) {
				panic("credential-bearing panic payload")
			}
			return false
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remember("note", "ordinary content"); err != nil {
			t.Fatal(err)
		}
		items, err := store.Recall("", 1000, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if items == nil || len(items) != 0 {
			t.Fatalf("panicking complete-entry guard did not fail closed: %#v", items)
		}
		if err := store.ValidateProjection(`safe","content":"value`); !errors.Is(err, ErrSecret) {
			t.Fatalf("ValidateProjection error = %v, want ErrSecret", err)
		}
	})
}

func TestConfiguredGuardCoversFinalPayloadDelimiterAndFilename(t *testing.T) {
	t.Run("final newline", func(t *testing.T) {
		const secret = "safe-body\n"
		root := t.TempDir()
		store, err := Open(root, func(value string) bool { return strings.Contains(value, secret) })
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remember("note", "safe-body"); !errors.Is(err, ErrSecret) {
			t.Fatalf("newline-framed credential persisted: %v", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("rejected memory left artifacts: %#v", entries)
		}
	})

	t.Run("filename", func(t *testing.T) {
		const secret = "credential-name.md"
		root := t.TempDir()
		store, err := Open(root, func(value string) bool { return strings.Contains(value, secret) })
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remember("credential-name", "safe"); !errors.Is(err, ErrSecret) {
			t.Fatalf("credential filename persisted: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, secret), []byte("externally safe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		items, err := store.Recall("", 1000, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("credential filename entered recalled context: %#v", items)
		}
	})
}

func TestRecallRejectsExternallyIntroducedSecretsAndUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.md"), []byte("AZURE_OPENAI_SUBSCRIPTION_KEY=do-not-project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "public.md"), []byte("not private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(source, []byte("hardlinked context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	items, err := store.Recall("", 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if memoryPOSIXPermissionsEnforced {
		if len(items) != 0 {
			t.Fatalf("unsafe memories entered model context: %#v", items)
		}
	} else if len(items) != 1 || items[0].Name != "public" {
		t.Fatalf("Windows ACL-limited profile recalled unexpected memories: %#v", items)
	}
}

func TestRecallUsesDeterministicNameTieBreak(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		if err := store.Remember(name, "same score"); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Unix(1_700_000_000, 0)
	for _, name := range []string{"zeta.md", "alpha.md"} {
		if err := os.Chtimes(filepath.Join(store.root, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Recall("same", 1000, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "alpha" || items[1].Name != "zeta" {
		t.Fatalf("nondeterministic memory order: %#v", items)
	}
}

func TestRecallBoundsDirectoryEnumerationAndAggregateInput(t *testing.T) {
	t.Run("directory items", func(t *testing.T) {
		root := t.TempDir()
		store, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= maxMemoryDirectoryItems; index++ {
			name := filepath.Join(root, fmt.Sprintf("%04d.tmp", index))
			if err := os.WriteFile(name, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Recall("", defaultBudgetBytes, time.Now()); !errors.Is(err, ErrRecallLimit) {
			t.Fatalf("Recall error = %v, want ErrRecallLimit", err)
		}
	})

	t.Run("aggregate bytes", func(t *testing.T) {
		root := t.TempDir()
		store, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte("x"), maxMemoryFileBytes)
		count := maxMemoryRecallScanBytes/maxMemoryFileBytes + 1
		for index := 0; index < count; index++ {
			name := filepath.Join(root, fmt.Sprintf("%04d.md", index))
			if err := os.WriteFile(name, payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Recall("", maxMemoryRecallScanBytes, time.Now()); !errors.Is(err, ErrRecallLimit) {
			t.Fatalf("Recall error = %v, want ErrRecallLimit", err)
		}
	})
}

func TestOpenRejectsSymlinkMemoryRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "memory")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("expected symlink memory root rejection")
	}
}

func TestRememberCountsCanonicalNewlineInLimit(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("too-large", strings.Repeat("x", maxMemoryFileBytes)); err == nil {
		t.Fatal("memory whose canonical payload exceeds the limit was accepted")
	}
	if err := store.Remember("fits", strings.Repeat("x", maxMemoryFileBytes-1)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.root, "fits.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxMemoryFileBytes {
		t.Fatalf("canonical memory size = %d", info.Size())
	}
}

func TestRememberSyncsDirectoryAfterActivation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("directory sync failed")
	called := false
	store.syncDirectory = func(*os.File) error {
		called = true
		if _, err := os.Stat(filepath.Join(store.root, "durable.md")); err != nil {
			t.Fatalf("directory sync ran before rename activation: %v", err)
		}
		return failure
	}
	if err := store.Remember("durable", "remember this"); !errors.Is(err, failure) {
		t.Fatalf("Remember error = %v", err)
	}
	if !called {
		t.Fatal("memory directory was not synced")
	}
}

func TestMemoryOperationsRejectReplacedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "memory")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("unsafe", "must not be redirected"); err == nil {
		t.Fatal("Remember accepted a replaced memory root")
	}
	if _, err := store.Recall("", 1000, time.Now()); err == nil {
		t.Fatal("Recall accepted a replaced memory root")
	}
	if _, err := os.Stat(filepath.Join(root, "unsafe.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root received memory content: %v", err)
	}
}
