// Package memory provides bounded, provenance-bearing persistent memory. It is
// selected context rather than a copy of the transcript.
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxMemoryFileBytes       = 256 << 10
	maxMemoryDirectoryItems  = 512
	maxMemoryRecallScanBytes = 8 << 20
	defaultBudgetBytes       = 25_000
)

var (
	ErrSecret      = errors.New("memory appears to contain a secret")
	ErrRecallLimit = errors.New("memory recall exceeds its resource boundary")
)

type Store struct {
	root          string
	rootIdentity  os.FileInfo
	syncDirectory func(*os.File) error
	secretGuards  []func(string) bool
}

type Entry struct {
	Name       string    `json:"name"`
	Content    string    `json:"content"`
	ModifiedAt time.Time `json:"modified_at"`
	Stale      bool      `json:"stale"`
	Score      int       `json:"score"`
}

func Open(root string, secretGuards ...func(string) bool) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create memory root: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect memory root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("memory root must be a non-symlink directory")
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("protect memory root: %w", err)
	}
	info, err = os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !memoryModePermitsPrivateUse(info.Mode()) {
		return nil, errors.New("memory root could not be secured")
	}
	guards := make([]func(string) bool, 0, len(secretGuards))
	for _, guard := range secretGuards {
		if guard != nil {
			guards = append(guards, panicSafeSecretGuard(guard))
		}
	}
	return &Store{root: abs, rootIdentity: info, syncDirectory: syncMemoryDirectory, secretGuards: guards}, nil
}

func (s *Store) Remember(name, content string) error {
	if s.looksSecret(name) {
		return ErrSecret
	}
	pathname, err := s.path(name)
	if err != nil {
		return err
	}
	if s.looksSecret(filepath.Base(pathname)) {
		return ErrSecret
	}
	payload := []byte(strings.TrimSpace(content) + "\n")
	if len(payload) > maxMemoryFileBytes {
		return errors.New("memory exceeds 256 KiB")
	}
	if s.looksSecret(string(payload)) {
		return ErrSecret
	}
	directory, err := s.openRoot()
	if err != nil {
		return err
	}
	directoryClosed := false
	closeDirectory := func() error {
		if directoryClosed {
			return nil
		}
		directoryClosed = true
		return directory.Close()
	}
	defer closeDirectory()
	temporary, err := os.CreateTemp(s.root, ".memory-*")
	if err != nil {
		return fmt.Errorf("create temporary memory: %w", err)
	}
	tempName := temporary.Name()
	tempIdentity, statErr := temporary.Stat()
	if statErr != nil {
		_ = temporary.Close()
		return fmt.Errorf("inspect temporary memory: %w", statErr)
	}
	cleanup := func() {
		_ = temporary.Close()
		_ = removeMemoryIfSame(tempName, tempIdentity)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := io.Copy(temporary, strings.NewReader(string(payload))); err != nil {
		cleanup()
		return fmt.Errorf("write memory: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync memory: %w", err)
	}
	finalTemp, statErr := temporary.Stat()
	var links uint64
	var linkErr error
	if statErr == nil {
		links, linkErr = openedMemoryFileLinkCount(temporary, finalTemp)
	}
	if statErr != nil || linkErr != nil || !os.SameFile(tempIdentity, finalTemp) || finalTemp.Size() != int64(len(payload)) || !finalTemp.Mode().IsRegular() || !memoryModePermitsPrivateUse(finalTemp.Mode()) || links != 1 {
		cleanup()
		if statErr != nil {
			return fmt.Errorf("inspect completed memory: %w", statErr)
		}
		if linkErr != nil {
			return fmt.Errorf("inspect completed memory link count: %w", linkErr)
		}
		return errors.New("temporary memory identity changed before activation")
	}
	if err := temporary.Close(); err != nil {
		_ = removeMemoryIfSame(tempName, tempIdentity)
		return err
	}
	if err := s.verifyRoot(directory); err != nil {
		_ = removeMemoryIfSame(tempName, tempIdentity)
		return err
	}
	if err := os.Rename(tempName, pathname); err != nil {
		_ = removeMemoryIfSame(tempName, tempIdentity)
		return fmt.Errorf("replace memory: %w", err)
	}
	if err := s.syncDirectory(directory); err != nil {
		return fmt.Errorf("sync memory directory: %w", err)
	}
	if err := s.verifyRoot(directory); err != nil {
		return err
	}
	if err := closeDirectory(); err != nil {
		return fmt.Errorf("close memory directory: %w", err)
	}
	return nil
}

func (s *Store) Recall(query string, budgetBytes int, now time.Time) ([]Entry, error) {
	if budgetBytes <= 0 {
		budgetBytes = defaultBudgetBytes
	}
	terms := strings.Fields(strings.ToLower(query))
	directory, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	// Read from the already pinned directory descriptor and cap enumeration
	// before the standard library can materialize an attacker-sized directory.
	entries, err := directory.ReadDir(maxMemoryDirectoryItems + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	if len(entries) > maxMemoryDirectoryItems {
		return nil, ErrRecallLimit
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	declaredBytes := int64(0)
	for _, item := range entries {
		if item.IsDir() || filepath.Ext(item.Name()) != ".md" || item.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := item.Info()
		if infoErr != nil || info.Size() < 0 || info.Size() > maxMemoryFileBytes {
			continue
		}
		if declaredBytes > maxMemoryRecallScanBytes-info.Size() {
			return nil, ErrRecallLimit
		}
		declaredBytes += info.Size()
	}
	var candidates []Entry
	scannedBytes := 0
	for _, item := range entries {
		if item.IsDir() || filepath.Ext(item.Name()) != ".md" || item.Type()&os.ModeSymlink != 0 {
			continue
		}
		if s.looksSecret(item.Name()) {
			continue
		}
		data, info, err := readMemorySnapshot(filepath.Join(s.root, item.Name()))
		if err != nil {
			continue
		}
		if len(data) > maxMemoryRecallScanBytes-scannedBytes {
			return nil, ErrRecallLimit
		}
		scannedBytes += len(data)
		content := string(data)
		// Remember applies the same guard before writing, but Recall is the
		// authoritative model-context boundary. Recheck files that may have
		// been edited or introduced outside this process.
		if s.looksSecret(content) {
			continue
		}
		lower := strings.ToLower(item.Name() + " " + content)
		score := 0
		for _, term := range terms {
			if strings.Contains(lower, term) {
				score++
			}
		}
		if len(terms) > 0 && score == 0 {
			continue
		}
		candidates = append(candidates, Entry{Name: strings.TrimSuffix(item.Name(), ".md"), Content: content, ModifiedAt: info.ModTime(), Stale: !now.IsZero() && now.Sub(info.ModTime()) > 90*24*time.Hour, Score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if !candidates[i].ModifiedAt.Equal(candidates[j].ModifiedAt) {
			return candidates[i].ModifiedAt.After(candidates[j].ModifiedAt)
		}
		return candidates[i].Name < candidates[j].Name
	})
	result := make([]Entry, 0)
	used := 0
	for _, entry := range candidates {
		if used+len(entry.Content) > budgetBytes {
			continue
		}
		if !s.safeJSONProjection(entry) {
			continue
		}
		used += len(entry.Content)
		result = append(result, entry)
	}
	if err := s.verifyRoot(directory); err != nil {
		return nil, err
	}
	if !s.safeJSONProjection(result) {
		return nil, ErrSecret
	}
	return result, nil
}

// ValidateProjection checks one complete memory-derived presentation after all
// labels, separators, and framing have been applied. It never rewrites the
// value because changing memory identity or content at this boundary would
// make the selected context differ from the stored entry.
func (s *Store) ValidateProjection(value string) error {
	if s.looksSecret(value) {
		return ErrSecret
	}
	return nil
}

func (s *Store) safeJSONProjection(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return s.ValidateProjection(string(encoded)) == nil
}

func (s *Store) looksSecret(value string) bool {
	if LooksSecret(value) {
		return true
	}
	for _, guard := range s.secretGuards {
		if guard(value) {
			return true
		}
	}
	return false
}

func panicSafeSecretGuard(guard func(string) bool) func(string) bool {
	return func(value string) (unsafe bool) {
		// A custom guard is a security boundary supplied by the caller. Treat a
		// panic as an unsafe projection and intentionally discard the panic
		// value, which may itself contain the credential being inspected.
		unsafe = true
		defer func() {
			_ = recover()
		}()
		unsafe = guard(value)
		return unsafe
	}
}

func (s *Store) openRoot() (*os.File, error) {
	before, err := os.Lstat(s.root)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !memoryModePermitsPrivateUse(before.Mode()) || !os.SameFile(s.rootIdentity, before) {
		return nil, errors.New("memory root identity or permissions changed")
	}
	directory, err := os.Open(s.root)
	if err != nil {
		return nil, fmt.Errorf("open memory root: %w", err)
	}
	if err := s.verifyRoot(directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func (s *Store) verifyRoot(directory *os.File) error {
	opened, openedErr := directory.Stat()
	current, pathErr := os.Lstat(s.root)
	if openedErr != nil || pathErr != nil || !opened.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !memoryModePermitsPrivateUse(current.Mode()) || !os.SameFile(s.rootIdentity, opened) || !os.SameFile(opened, current) {
		return errors.New("memory root changed during operation")
	}
	return nil
}

func removeMemoryIfSame(pathname string, expected os.FileInfo) error {
	current, err := os.Lstat(pathname)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return errors.New("memory temporary path changed before cleanup")
	}
	return os.Remove(pathname)
}

func (s *Store) path(name string) (string, error) {
	name = strings.TrimSpace(strings.TrimSuffix(name, ".md"))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return "", errors.New("memory name must be a single path-safe component")
	}
	for _, c := range name {
		if !(c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", errors.New("memory name contains unsupported characters")
		}
	}
	pathname := filepath.Join(s.root, name+".md")
	rel, err := filepath.Rel(s.root, pathname)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("memory path escapes root")
	}
	return pathname, nil
}

func LooksSecret(value string) bool {
	lower := strings.ToLower(value)
	markers := []string{
		"-----begin private key-----", "authorization: bearer ",
		"subscription-key=", "subscription_key=", "api-key=", "api_key=", "apikey=",
		"password=", "azure_openai_subscription_key=",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, word := range strings.Fields(value) {
		if strings.HasPrefix(word, "sk-") && len(word) >= 20 {
			return true
		}
	}
	return false
}
