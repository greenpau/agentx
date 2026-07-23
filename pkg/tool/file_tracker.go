package tool

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type fileFingerprint struct {
	Size     int64
	Mode     os.FileMode
	ModTime  time.Time
	Hash     [sha256.Size]byte
	Complete bool
}

// FileTracker stores read observations needed for stale-write rejection.
type FileTracker struct {
	mu       sync.RWMutex
	observed map[string]fileFingerprint
}

func NewFileTracker() *FileTracker {
	return &FileTracker{observed: make(map[string]fileFingerprint)}
}

func (t *FileTracker) Observe(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	return t.ObserveFile(canonical, file)
}

// ObserveFile records the identity and contents of an already-opened file.
// Capability implementations use this form so the object observed is exactly
// the object obtained through their race-safe workspace root.
func (t *FileTracker) ObserveFile(path string, file *os.File) error {
	fingerprint, err := fingerprintOpenFile(file)
	if err != nil {
		return err
	}
	return t.recordObservation(path, fingerprint)
}

func (t *FileTracker) recordObservation(path string, fingerprint fileFingerprint) error {
	key, err := observationKey(path)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.observed[key] = fingerprint
	t.mu.Unlock()
	return nil
}

func sameFingerprint(left, right fileFingerprint) bool {
	return left.Size == right.Size && left.Mode.Perm() == right.Mode.Perm() && left.ModTime.Equal(right.ModTime) && left.Hash == right.Hash && left.Complete == right.Complete
}

func (t *FileTracker) RequireCurrent(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve previously read file: %w", err)
	}
	return t.RequireCurrentFile(canonical, file)
}

// RequireCurrentFile verifies an already-opened file against the session's
// observation ledger. The caller retains ownership of file.
func (t *FileTracker) RequireCurrentFile(path string, file *os.File) error {
	key, err := observationKey(path)
	if err != nil {
		return err
	}
	t.mu.RLock()
	observed, ok := t.observed[key]
	t.mu.RUnlock()
	if !ok {
		return errors.New("existing file must be read in this session before it is modified")
	}
	if !observed.Complete {
		return fmt.Errorf("file is larger than the %d-byte safe edit limit", maximumEditBytes)
	}
	current, err := fingerprintOpenFile(file)
	if err != nil {
		return err
	}
	if !sameFingerprint(observed, current) {
		return errors.New("file changed since it was read; read it again before modifying")
	}
	return nil
}

func observationKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func fingerprintOpenFile(file *os.File) (fileFingerprint, error) {
	info, err := file.Stat()
	if err != nil {
		return fileFingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return fileFingerprint{}, errors.New("target is not a regular file")
	}
	fingerprint := fileFingerprint{Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()}
	if info.Size() > maximumEditBytes {
		// Large files remain readable, but they are deliberately ineligible for
		// whole-file mutation. Do not hash unbounded content merely to remember
		// that fact.
		return fingerprint, nil
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fileFingerprint{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fileFingerprint{}, err
	}
	defer func() { _, _ = file.Seek(offset, io.SeekStart) }()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, info.Size()); err != nil {
		return fileFingerprint{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return fileFingerprint{}, err
	}
	if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return fileFingerprint{}, errors.New("file changed while it was being observed")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	fingerprint.Hash = digest
	fingerprint.Complete = true
	return fingerprint, nil
}
