package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplaceAppendAndBoundedReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")
	if err := AtomicReplace(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendFile(path, []byte("-second"), 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("existing mode changed: got %o", got)
	}
	rangeResult, err := ReadRange(path, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(rangeResult.Data); got != "rst-" {
		t.Fatalf("range = %q", got)
	}
	if rangeResult.SnapshotSize != 12 || rangeResult.OmittedBytes != 6 {
		t.Fatalf("unexpected range evidence: %+v", rangeResult)
	}
	tail, err := ReadTail(path, 6)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(tail.Data); got != "second" || tail.OmittedBytes != 6 {
		t.Fatalf("tail = %+v", tail)
	}
}

func TestInspectPathPreservesLexicalAndPhysicalEvidence(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	existing, err := InspectPath(link, "")
	if err != nil {
		t.Fatal(err)
	}
	physicalRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if !existing.Exists || !existing.Symlink || existing.Physical != physicalRealDir {
		t.Fatalf("existing evidence = %+v", existing)
	}
	absent, err := InspectPath(filepath.Join(link, "new", "file"), "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalRealDir, "new", "file")
	if absent.Exists || absent.DeepestExistingPhysical != want {
		t.Fatalf("absent evidence = %+v, want physical %q", absent, want)
	}
}
