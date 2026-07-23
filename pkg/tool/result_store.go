package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/greenpau/agentx/pkg/permission"
)

const (
	resultPreview               = 2_000
	maximumPersistedResultBytes = 32 << 20
	defaultResultReadBytes      = 30_000
	maximumResultReadBytes      = 100_000
	resultIndexFilename         = "index.jsonl"
	maximumResultIndexBytes     = 16 << 20
	maximumResultIndexEntries   = 100_000
	maximumResultIndexLineBytes = 4 << 10
	maximumResultOrphanEntries  = 128
)

type storedResult struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Size   int    `json:"size"`
	Digest string `json:"sha256"`
}

// ResultStore preserves exact oversized-output replacement bytes by tool ID.
type ResultStore struct {
	mu                sync.Mutex
	directory         string
	directoryIdentity os.FileInfo
	replacements      map[string]string
	known             map[string]storedResult
	validateIndex     func([]byte) error
	syncFile          func(*os.File) error
	syncDirectory     func(*os.File) error
}

// NewResultStore creates a restrictive session-specific tool-results directory.
func NewResultStore(directory string) (*ResultStore, error) {
	return NewResultStoreWithValidator(directory, nil)
}

// NewResultStoreWithValidator additionally validates each complete JSON index
// entry after structural framing and before any bytes are appended.
func NewResultStoreWithValidator(directory string, validate func([]byte) error) (*ResultStore, error) {
	if directory == "" {
		return nil, errors.New("tool result directory is required")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve tool result directory: %w", err)
	}
	identity, err := prepareResultDirectory(abs)
	if err != nil {
		return nil, err
	}
	store := &ResultStore{
		directory:         abs,
		directoryIdentity: identity,
		replacements:      make(map[string]string),
		known:             make(map[string]storedResult),
		validateIndex:     validate,
		syncFile:          func(file *os.File) error { return file.Sync() },
		syncDirectory:     syncResultDirectory,
	}
	if err := store.loadIndex(); err != nil {
		return nil, err
	}
	if err := store.reconcileResultFiles(); err != nil {
		return nil, err
	}
	if err := store.validateLoadedContent(); err != nil {
		return nil, err
	}
	return store, nil
}

// prepareResultDirectory never changes permissions through a pathname that
// names a direct symlink. Chmod is performed on a pinned directory descriptor,
// and the pathname is checked again before the identity is accepted.
func prepareResultDirectory(directory string) (_ os.FileInfo, err error) {
	if filepath.Dir(directory) == directory {
		return nil, errors.New("tool result directory must not be a filesystem root")
	}
	before, inspectErr := os.Lstat(directory)
	if errors.Is(inspectErr, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create tool result directory: %w", err)
		}
		before, inspectErr = os.Lstat(directory)
	}
	if inspectErr != nil {
		return nil, fmt.Errorf("inspect tool result directory: %w", inspectErr)
	}
	if err := validateResultDirectory(before, false); err != nil {
		return nil, err
	}

	pinned, err := os.Open(directory)
	if err != nil {
		return nil, fmt.Errorf("open tool result directory: %w", err)
	}
	defer func() {
		err = errors.Join(err, pinned.Close())
	}()
	opened, err := pinned.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened tool result directory: %w", err)
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		return nil, errors.New("tool result directory changed while opening")
	}
	confirmed, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("reinspect tool result directory before securing it: %w", err)
	}
	if err := validateResultDirectory(confirmed, false); err != nil || !os.SameFile(opened, confirmed) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("tool result directory changed before it could be secured")
	}
	if err := pinned.Chmod(0o700); err != nil {
		return nil, fmt.Errorf("secure tool result directory: %w", err)
	}
	if err := syncResultDirectory(pinned); err != nil {
		return nil, fmt.Errorf("sync tool result directory: %w", err)
	}
	secured, err := pinned.Stat()
	if err != nil {
		return nil, fmt.Errorf("verify secured tool result directory: %w", err)
	}
	if err := validateResultDirectory(secured, true); err != nil || !os.SameFile(opened, secured) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("tool result directory identity changed while securing it")
	}
	after, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("reinspect tool result directory: %w", err)
	}
	if err := validateResultDirectory(after, true); err != nil || !os.SameFile(secured, after) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("tool result directory changed while securing it")
	}
	if err := syncResultDirectoryParent(directory); err != nil {
		return nil, err
	}
	return secured, nil
}

func syncResultDirectoryParent(directory string) (err error) {
	parentPath := filepath.Dir(directory)
	before, err := os.Lstat(parentPath)
	if err != nil {
		return fmt.Errorf("inspect tool result directory parent: %w", err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("tool result directory parent is unsafe")
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return fmt.Errorf("open tool result directory parent: %w", err)
	}
	defer func() {
		err = errors.Join(err, parent.Close())
	}()
	opened, err := parent.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		if err != nil {
			return fmt.Errorf("stat tool result directory parent: %w", err)
		}
		return errors.New("tool result directory parent changed while opening")
	}
	if err := syncResultDirectory(parent); err != nil {
		return fmt.Errorf("sync tool result directory parent: %w", err)
	}
	after, err := os.Lstat(parentPath)
	if err != nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		if err != nil {
			return fmt.Errorf("reinspect tool result directory parent: %w", err)
		}
		return errors.New("tool result directory parent changed while syncing")
	}
	return nil
}

func validateResultDirectory(info os.FileInfo, requirePrivate bool) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("tool result directory must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("tool result path is not a directory")
	}
	if requirePrivate && runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errors.New("tool result directory is not owner-only")
	}
	return nil
}

func validateResultFile(info os.FileInfo, expectedSize int64) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("tool result path is not a regular file")
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return errors.New("tool result file has an unexpected size")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return errors.New("tool result file is not owner-only")
	}
	return nil
}

// withRoot acquires the current pathname as a rooted capability, proves that
// it is the directory accepted at construction, and verifies both the pinned
// root and lexical pathname again after the operation. A pathname replacement
// therefore cannot redirect an operation to a new directory.
func (s *ResultStore) withRoot(operation func(*os.Root) error) (err error) {
	root, err := s.openVerifiedRoot()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, s.verifyRoot(root), root.Close())
	}()
	return operation(root)
}

func (s *ResultStore) openVerifiedRoot() (*os.Root, error) {
	before, err := os.Lstat(s.directory)
	if err != nil {
		return nil, fmt.Errorf("inspect tool result directory: %w", err)
	}
	if err := validateResultDirectory(before, true); err != nil || !os.SameFile(s.directoryIdentity, before) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("tool result directory identity no longer matches the session")
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, fmt.Errorf("open tool result root: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(s.directoryIdentity, opened) {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("stat opened tool result root: %w", err)
		}
		return nil, errors.New("tool result directory changed while opening")
	}
	if err := validateResultDirectory(opened, true); err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := s.verifyRoot(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func (s *ResultStore) verifyRoot(root *os.Root) error {
	pinned, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("verify pinned tool result root: %w", err)
	}
	if err := validateResultDirectory(pinned, true); err != nil || !os.SameFile(s.directoryIdentity, pinned) {
		if err != nil {
			return err
		}
		return errors.New("pinned tool result root identity changed")
	}
	lexical, err := os.Lstat(s.directory)
	if err != nil {
		return fmt.Errorf("reinspect tool result directory: %w", err)
	}
	if err := validateResultDirectory(lexical, true); err != nil || !os.SameFile(s.directoryIdentity, lexical) {
		if err != nil {
			return err
		}
		return errors.New("tool result directory was replaced during operation")
	}
	return nil
}

func (s *ResultStore) syncRoot(root *os.Root) (err error) {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open tool result root for sync: %w", err)
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(s.directoryIdentity, opened) {
		if err != nil {
			return fmt.Errorf("stat tool result root for sync: %w", err)
		}
		return errors.New("tool result root identity changed before sync")
	}
	if err := s.syncDirectory(directory); err != nil {
		return fmt.Errorf("sync tool result root: %w", err)
	}
	after, err := directory.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(opened, after) {
		if err != nil {
			return fmt.Errorf("restat tool result root after sync: %w", err)
		}
		return errors.New("tool result root identity changed during sync")
	}
	return s.verifyRoot(root)
}

func (s *ResultStore) apply(id, content string, limit int) string {
	if limit < 0 || len(content) <= limit {
		return content
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if replacement, ok := s.replacements[id]; ok {
		return replacement
	}
	if err := validateResultID(id); err != nil {
		return s.fallback(id, content, "tool-use identity is invalid")
	}
	if len(content) > maximumPersistedResultBytes {
		return s.fallback(id, content, fmt.Sprintf("output exceeded the %d-byte persistence limit", maximumPersistedResultBytes))
	}
	stored := content
	if _, exists := s.known[id]; exists {
		b, err := s.readVerified(id)
		if err != nil {
			return s.fallback(id, content, "indexed result failed integrity verification")
		}
		stored = string(b)
	} else if err := s.persist(id, []byte(content)); err != nil {
		return s.fallback(id, content, "result persistence is unavailable: "+err.Error())
	}
	preview := resultPreviewText(stored)
	replacement := fmt.Sprintf("<persisted-output tool_use_id=%q bytes=%d>\n%s\n[remaining output omitted; retrieve with ToolResultRead using this tool_use_id]\n</persisted-output>", id, len(stored), preview)
	s.replacements[id] = replacement
	return replacement
}

func (s *ResultStore) pathFor(id string) string {
	return filepath.Join(s.directory, resultFilename(id))
}

func resultFilename(id string) string {
	hash := sha256.Sum256([]byte(id))
	return "result-" + hex.EncodeToString(hash[:]) + ".bin"
}

func validateResultID(id string) error {
	if id == "" || len(id) > 256 {
		return errors.New("tool-use id is empty or exceeds 256 bytes")
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("tool-use id contains whitespace or control characters")
		}
	}
	return nil
}

func (s *ResultStore) persist(id string, content []byte) error {
	name := resultFilename(id)
	digest := sha256.Sum256(content)
	entry := storedResult{ID: id, File: name, Size: len(content), Digest: hex.EncodeToString(digest[:])}
	if err := s.validateContentRecord(entry, content); err != nil {
		return err
	}
	line, err := s.encodeIndexEntry(entry)
	if err != nil {
		return err
	}
	err = s.withRoot(func(root *os.Root) (operationErr error) {
		created, err := s.createDurableResult(root, name, content)
		if created != nil {
			cleanup := true
			defer func() {
				if cleanup {
					operationErr = errors.Join(operationErr, s.removeOwnedResult(root, name, created))
				}
			}()
			if err != nil {
				return err
			}
			appendStarted, err := s.appendIndex(root, line)
			if appendStarted {
				// Once an index append begins, its durable state is uncertain.
				// Retain the synced content so startup can either validate the
				// complete record or remove the resulting orphan.
				cleanup = false
			}
			if err != nil {
				return err
			}
			cleanup = false
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	s.known[id] = entry
	return nil
}

func (s *ResultStore) createDurableResult(root *os.Root, name string, content []byte) (_ os.FileInfo, err error) {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("unindexed pre-existing result file was refused")
		}
		return nil, err
	}
	var created os.FileInfo
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	created, err = file.Stat()
	if err != nil {
		return created, err
	}
	if err := validateResultFile(created, 0); err != nil {
		return created, err
	}
	links, err := openedFileLinkCount(file, created)
	if err != nil || links != 1 {
		if err != nil {
			return created, err
		}
		return created, errors.New("new result file has ambiguous link identity")
	}
	if err := writeResultAll(file, content); err != nil {
		return created, err
	}
	if err := s.syncFile(file); err != nil {
		return created, err
	}
	after, err := file.Stat()
	if err != nil || validateResultFile(after, int64(len(content))) != nil || !os.SameFile(created, after) {
		if err != nil {
			return created, err
		}
		return created, errors.New("new result file changed while writing")
	}
	links, err = openedFileLinkCount(file, after)
	if err != nil || links != 1 {
		if err != nil {
			return created, err
		}
		return created, errors.New("new result file link identity changed while writing")
	}
	if err := s.syncRoot(root); err != nil {
		return created, err
	}
	return created, nil
}

func (s *ResultStore) removeOwnedResult(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateResultFile(current, -1); err != nil || !os.SameFile(expected, current) {
		if err != nil {
			return err
		}
		return errors.New("result file identity changed before cleanup")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	var links uint64
	var linkErr error
	if statErr == nil {
		links, linkErr = openedFileLinkCount(file, opened)
	}
	closeErr := file.Close()
	if statErr != nil || linkErr != nil || closeErr != nil {
		return errors.Join(statErr, linkErr, closeErr)
	}
	if err := validateResultFile(opened, -1); err != nil || !os.SameFile(current, opened) || links != 1 {
		if err != nil {
			return err
		}
		return errors.New("result file has ambiguous cleanup identity")
	}
	confirmed, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, confirmed) {
		if err != nil {
			return err
		}
		return errors.New("result file changed before cleanup")
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	return s.syncRoot(root)
}

func writeResultAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (s *ResultStore) encodeIndexEntry(entry storedResult) ([]byte, error) {
	line, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')
	if len(line) > maximumResultIndexLineBytes {
		return nil, errors.New("tool result index entry exceeds its framing limit")
	}
	if s.validateIndex != nil {
		if err := callResultValidator(s.validateIndex, line); err != nil {
			return nil, errors.New("validate tool result index entry")
		}
	}
	return line, nil
}

func (s *ResultStore) appendIndex(root *os.Root, line []byte) (_ bool, err error) {
	if len(s.known) >= maximumResultIndexEntries {
		return false, errors.New("result index exceeds entry limit")
	}
	flags := os.O_RDWR | os.O_APPEND
	startingSize := int64(0)
	created := false
	before, inspectErr := root.Lstat(resultIndexFilename)
	if errors.Is(inspectErr, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
		created = true
	} else if inspectErr != nil {
		return false, inspectErr
	} else {
		if err := validateResultFile(before, -1); err != nil {
			return false, errors.New("result index path is unsafe: " + err.Error())
		}
		startingSize = before.Size()
	}
	if startingSize > maximumResultIndexBytes-int64(len(line)) {
		return false, errors.New("result index is full")
	}
	index, err := root.OpenFile(resultIndexFilename, flags, 0o600)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, index.Close())
	}()
	info, err := index.Stat()
	if err != nil || validateResultFile(info, startingSize) != nil || inspectErr == nil && !os.SameFile(before, info) {
		if err != nil {
			return false, err
		}
		return false, errors.New("result index is unsafe")
	}
	links, err := openedFileLinkCount(index, info)
	if err != nil || links != 1 {
		if err != nil {
			return false, err
		}
		return false, errors.New("result index has ambiguous link identity")
	}
	if startingSize > 0 {
		var tail [1]byte
		if n, readErr := index.ReadAt(tail[:], startingSize-1); readErr != nil || n != 1 || tail[0] != '\n' {
			if readErr != nil {
				return false, readErr
			}
			return false, errors.New("result index has an unreconciled tail")
		}
	}
	started := true
	if err := writeResultAll(index, line); err != nil {
		return started, err
	}
	if err := s.syncFile(index); err != nil {
		return started, err
	}
	after, err := index.Stat()
	if err != nil || validateResultFile(after, startingSize+int64(len(line))) != nil || !os.SameFile(info, after) {
		if err != nil {
			return started, err
		}
		return started, errors.New("result index changed while appending")
	}
	links, err = openedFileLinkCount(index, after)
	if err != nil || links != 1 {
		if err != nil {
			return started, err
		}
		return started, errors.New("result index link identity changed while appending")
	}
	if created {
		if err := s.syncRoot(root); err != nil {
			return started, err
		}
	}
	return started, nil
}

func (s *ResultStore) loadIndex() error {
	return s.withRoot(func(root *os.Root) (operationErr error) {
		before, err := root.Lstat(resultIndexFilename)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect result index: %w", err)
		}
		if err := validateResultFile(before, -1); err != nil || before.Size() > maximumResultIndexBytes {
			return errors.New("result index is not a bounded private regular file")
		}
		file, err := root.OpenFile(resultIndexFilename, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open result index: %w", err)
		}
		defer func() {
			operationErr = errors.Join(operationErr, file.Close())
		}()
		opened, err := file.Stat()
		if err != nil || validateResultFile(opened, before.Size()) != nil || !os.SameFile(before, opened) {
			return errors.New("result index changed while opening")
		}
		links, err := openedFileLinkCount(file, opened)
		if err != nil || links != 1 {
			return errors.New("result index has ambiguous link identity")
		}
		raw, err := io.ReadAll(io.LimitReader(file, maximumResultIndexBytes+1))
		if err != nil || int64(len(raw)) != opened.Size() {
			return errors.New("result index size verification failed")
		}
		loaded, truncateAt, appendNewline, err := s.parseResultIndex(raw)
		if err != nil {
			return err
		}
		after, err := file.Stat()
		if err != nil || validateResultFile(after, opened.Size()) != nil || !os.SameFile(opened, after) {
			return errors.New("result index changed while reading")
		}
		links, err = openedFileLinkCount(file, after)
		if err != nil || links != 1 {
			return errors.New("result index link identity changed while reading")
		}
		expectedSize := opened.Size()
		switch {
		case appendNewline:
			if opened.Size() >= maximumResultIndexBytes {
				return errors.New("result index is too full to repair its final frame")
			}
			if _, err := file.Seek(0, io.SeekEnd); err != nil {
				return fmt.Errorf("seek result index for repair: %w", err)
			}
			if err := writeResultAll(file, []byte{'\n'}); err != nil {
				return fmt.Errorf("repair result index final frame: %w", err)
			}
			expectedSize++
		case truncateAt >= 0:
			if err := file.Truncate(truncateAt); err != nil {
				return fmt.Errorf("truncate torn result index tail: %w", err)
			}
			expectedSize = truncateAt
		}
		if appendNewline || truncateAt >= 0 {
			if err := s.syncFile(file); err != nil {
				return fmt.Errorf("sync repaired result index: %w", err)
			}
			repaired, err := file.Stat()
			if err != nil || validateResultFile(repaired, expectedSize) != nil || !os.SameFile(opened, repaired) {
				return errors.New("result index changed while repairing its final frame")
			}
			links, err = openedFileLinkCount(file, repaired)
			if err != nil || links != 1 {
				return errors.New("result index link identity changed while repairing")
			}
		}
		for id, entry := range loaded {
			s.known[id] = entry
		}
		return nil
	})
}

func (s *ResultStore) parseResultIndex(raw []byte) (map[string]storedResult, int64, bool, error) {
	loaded := make(map[string]storedResult)
	files := make(map[string]string)
	prefixEnd := len(raw)
	var tail []byte
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(raw, '\n')
		prefixEnd = lastNewline + 1
		tail = raw[prefixEnd:]
	}
	for offset := 0; offset < prefixEnd; {
		relativeEnd := bytes.IndexByte(raw[offset:prefixEnd], '\n')
		if relativeEnd < 0 {
			return nil, -1, false, errors.New("result index framing is inconsistent")
		}
		end := offset + relativeEnd
		if err := s.decodeIndexRecord(raw[offset:end], loaded, files); err != nil {
			return nil, -1, false, err
		}
		offset = end + 1
	}
	if len(tail) == 0 {
		return loaded, -1, false, nil
	}
	if !json.Valid(tail) {
		if recoverableTornIndexTail(tail) {
			return loaded, int64(prefixEnd), false, nil
		}
		return nil, -1, false, errors.New("result index contains an invalid final record")
	}
	if err := s.decodeIndexRecord(tail, loaded, files); err != nil {
		return nil, -1, false, err
	}
	return loaded, -1, true, nil
}

func recoverableTornIndexTail(tail []byte) bool {
	const recordPrefix = `{"id":`
	if len(tail) == 0 || len(tail)+1 > maximumResultIndexLineBytes {
		return false
	}
	prefix := []byte(recordPrefix)
	if len(tail) <= len(prefix) {
		return bytes.Equal(tail, prefix[:len(tail)])
	}
	if !bytes.HasPrefix(tail, prefix) {
		return false
	}
	var projection any
	err := json.Unmarshal(tail, &projection)
	var syntax *json.SyntaxError
	return errors.As(err, &syntax) && syntax.Error() == "unexpected end of JSON input"
}

func (s *ResultStore) decodeIndexRecord(line []byte, loaded map[string]storedResult, files map[string]string) error {
	if len(line) == 0 || len(line)+1 > maximumResultIndexLineBytes {
		return errors.New("result index contains an invalid record")
	}
	frame := append(append([]byte(nil), line...), '\n')
	if s.validateIndex != nil {
		if err := callResultValidator(s.validateIndex, frame); err != nil {
			return errors.New("validate existing tool result index entry")
		}
	}
	var entry storedResult
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("result index contains an invalid record")
	}
	if err := validateStoredResult(entry); err != nil {
		return err
	}
	if _, duplicate := loaded[entry.ID]; duplicate {
		return errors.New("result index contains a duplicate tool-use id")
	}
	if owner, duplicate := files[entry.File]; duplicate && owner != entry.ID {
		return errors.New("result index contains a duplicate result filename")
	}
	if len(loaded) >= maximumResultIndexEntries {
		return errors.New("result index exceeds entry limit")
	}
	loaded[entry.ID] = entry
	files[entry.File] = entry.ID
	return nil
}

func (s *ResultStore) reconcileResultFiles() error {
	expected := make(map[string]storedResult, len(s.known))
	for _, entry := range s.known {
		if _, duplicate := expected[entry.File]; duplicate {
			return errors.New("result index contains a duplicate result filename")
		}
		expected[entry.File] = entry
	}
	return s.withRoot(func(root *os.Root) error {
		directory, err := root.Open(".")
		if err != nil {
			return fmt.Errorf("open result directory for reconciliation: %w", err)
		}
		orphans := make([]resultOrphan, 0)
		entryCount := 0
		for {
			entries, readErr := directory.ReadDir(256)
			for _, entry := range entries {
				entryCount++
				if entryCount > maximumResultIndexEntries+maximumResultOrphanEntries+1 {
					_ = directory.Close()
					return errors.New("tool result directory exceeds reconciliation entry limit")
				}
				name := entry.Name()
				if name == resultIndexFilename {
					continue
				}
				if indexed, ok := expected[name]; ok {
					if _, err := inspectResultFile(root, name, int64(indexed.Size)); err != nil {
						_ = directory.Close()
						return errors.New("indexed result file is unsafe")
					}
					delete(expected, name)
					continue
				}
				if !validResultFilename(name) {
					_ = directory.Close()
					return errors.New("tool result directory contains an unexpected entry")
				}
				if len(orphans) >= maximumResultOrphanEntries {
					_ = directory.Close()
					return errors.New("tool result directory exceeds orphan recovery limit")
				}
				info, err := inspectResultFile(root, name, -1)
				if err != nil || info.Size() > maximumPersistedResultBytes {
					_ = directory.Close()
					return errors.New("unindexed result file is unsafe")
				}
				orphans = append(orphans, resultOrphan{name: name, identity: info})
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = directory.Close()
				return fmt.Errorf("enumerate result directory: %w", readErr)
			}
		}
		if err := directory.Close(); err != nil {
			return err
		}
		if len(expected) != 0 {
			return errors.New("result index references a missing persisted result")
		}
		for _, orphan := range orphans {
			if err := s.removeOwnedResult(root, orphan.name, orphan.identity); err != nil {
				return errors.New("remove recoverable unindexed result file")
			}
		}
		return nil
	})
}

type resultOrphan struct {
	name     string
	identity os.FileInfo
}

func validResultFilename(name string) bool {
	const prefix = "result-"
	const suffix = ".bin"
	if len(name) != len(prefix)+sha256.Size*2+len(suffix) ||
		!strings.HasPrefix(name, prefix) ||
		!strings.HasSuffix(name, suffix) {
		return false
	}
	for _, character := range name[len(prefix) : len(name)-len(suffix)] {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func inspectResultFile(root *os.Root, name string, expectedSize int64) (_ os.FileInfo, err error) {
	before, err := root.Lstat(name)
	if err != nil || validateResultFile(before, expectedSize) != nil {
		return nil, errors.New("result file metadata is unsafe")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil || validateResultFile(opened, expectedSize) != nil || !os.SameFile(before, opened) {
		return nil, errors.New("result file changed while opening")
	}
	links, err := openedFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return nil, errors.New("result file has ambiguous link identity")
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) {
		return nil, errors.New("result file changed while inspecting")
	}
	return opened, nil
}

func validateStoredResult(entry storedResult) error {
	if err := validateResultID(entry.ID); err != nil || entry.File != resultFilename(entry.ID) || entry.Size < 0 || entry.Size > maximumPersistedResultBytes || len(entry.Digest) != sha256.Size*2 {
		return errors.New("result index contains an invalid ownership record")
	}
	if _, err := hex.DecodeString(entry.Digest); err != nil {
		return errors.New("result index contains an invalid digest")
	}
	return nil
}

func callResultValidator(validate func([]byte) error, raw []byte) (err error) {
	if validate == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("tool result validator panicked")
		}
	}()
	return validate(append([]byte(nil), raw...))
}

func (s *ResultStore) validateContentRecord(entry storedResult, content []byte) error {
	if s.validateIndex == nil {
		return nil
	}
	projection := struct {
		ToolUseID string `json:"tool_use_id"`
		File      string `json:"file"`
		Size      int    `json:"size"`
		Digest    string `json:"sha256"`
		Content   string `json:"content"`
	}{
		ToolUseID: entry.ID,
		File:      entry.File,
		Size:      entry.Size,
		Digest:    entry.Digest,
		Content:   string(content),
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return errors.New("encode tool result content validation record")
	}
	raw = append(raw, '\n')
	if err := callResultValidator(s.validateIndex, raw); err != nil {
		return errors.New("validate persisted tool result content")
	}
	return nil
}

func (s *ResultStore) validateLoadedContent() error {
	if s.validateIndex == nil || len(s.known) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.known))
	for id := range s.known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := s.readVerified(id); err != nil {
			return errors.New("validate existing persisted tool result")
		}
	}
	return nil
}

func resultPreviewText(content string) string {
	if len(content) <= resultPreview {
		return content
	}
	cut := resultPreview
	if newline := strings.LastIndex(content[resultPreview/2:resultPreview], "\n"); newline >= 0 {
		cut = resultPreview/2 + newline + 1
	}
	return validUTF8Prefix(content, cut)
}

func (s *ResultStore) fallback(id, content, reason string) string {
	replacement := fmt.Sprintf("<truncated-output tool_use_id=%q bytes=%d>\n%s\n[remaining output unavailable: %s]\n</truncated-output>", id, len(content), resultPreviewText(content), reason)
	s.replacements[id] = replacement
	return replacement
}

type resultReadInput struct {
	ToolUseID string `json:"tool_use_id"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func resultReadDescriptor(store *ResultStore) Descriptor {
	return Descriptor{
		Name: "ToolResultRead", Source: SourceBuiltin,
		Description: "Read a bounded byte range from oversized output by its accepted tool-use identity.",
		InputSchema: objectSchema(map[string]any{
			"tool_use_id": stringSchema("Accepted tool-use ID"),
			"offset":      integerSchema("Previously delivered byte offset", 0, maximumPersistedResultBytes),
			"limit":       integerSchema("Maximum bytes to return", 1, maximumResultReadBytes),
		}, "tool_use_id"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input resultReadInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if validateResultID(input.ToolUseID) != nil || input.Offset < 0 {
				return nil, errors.New("tool_use_id is required and offset must be non-negative")
			}
			if input.Limit == 0 {
				input.Limit = defaultResultReadBytes
			}
			if input.Limit < 1 || input.Limit > maximumResultReadBytes {
				return nil, errors.New("limit outside supported bounds")
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(resultReadInput)
			content, next, truncated, err := store.read(ctx, input.ToolUseID, input.Offset, input.Limit)
			if err != nil {
				return Output{}, invocationError("execution_failed", "read persisted tool result: %v", err)
			}
			return Output{Content: content, Metadata: map[string]any{"tool_use_id": input.ToolUseID, "next_offset": next, "truncated": truncated}}, nil
		},
		MaxResultChars: maximumResultReadBytes,
	}
}

func (s *ResultStore) read(ctx context.Context, id string, offset int64, limit int) (string, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", offset, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.readVerified(id)
	if err != nil {
		return "", offset, false, err
	}
	if offset > int64(len(b)) {
		return "", offset, false, errors.New("offset exceeds persisted result size")
	}
	end := offset + int64(limit)
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	next := end
	return string(b[offset:end]), next, next < int64(len(b)), nil
}

func (s *ResultStore) readVerified(id string) ([]byte, error) {
	entry, ok := s.known[id]
	if !ok {
		return nil, errors.New("tool-use id has no owned persisted result")
	}
	var content []byte
	err := s.withRoot(func(root *os.Root) error {
		before, err := root.Lstat(entry.File)
		if err != nil || validateResultFile(before, int64(entry.Size)) != nil {
			return errors.New("persisted result metadata is unsafe")
		}
		file, err := root.Open(entry.File)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || validateResultFile(opened, int64(entry.Size)) != nil || !os.SameFile(before, opened) {
			return errors.New("persisted result changed while opening")
		}
		links, err := openedFileLinkCount(file, opened)
		if err != nil || links != 1 {
			return errors.New("persisted result has ambiguous link identity")
		}
		b, err := io.ReadAll(io.LimitReader(file, maximumPersistedResultBytes+1))
		if err != nil || len(b) != entry.Size {
			return errors.New("persisted result size verification failed")
		}
		after, err := file.Stat()
		if err != nil || validateResultFile(after, int64(entry.Size)) != nil || !os.SameFile(opened, after) {
			return errors.New("persisted result changed while reading")
		}
		links, err = openedFileLinkCount(file, after)
		if err != nil || links != 1 {
			return errors.New("persisted result link identity changed while reading")
		}
		digest := sha256.Sum256(b)
		if hex.EncodeToString(digest[:]) != entry.Digest {
			return errors.New("persisted result integrity verification failed")
		}
		content = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.validateContentRecord(entry, content); err != nil {
		return nil, err
	}
	return content, nil
}
