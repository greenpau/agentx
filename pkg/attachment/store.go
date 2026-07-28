package attachment

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
)

const (
	blobSuffix     = ".blob"
	manifestSuffix = ".json"
	tempPrefix     = "_tmp_"
)

type manifestEnvelope struct {
	Version  int      `json:"version"`
	Manifest Manifest `json:"manifest"`
}

type blobRecord struct {
	size int64
	refs int
}

// Store owns one session's immutable attachment manifests and blobs.
type Store struct {
	mu sync.Mutex

	root        *platform.OwnedDirectory
	blobDir     *platform.OwnedDirectory
	manifestDir *platform.OwnedDirectory
	uploadDir   *platform.OwnedDirectory

	limits  Limits
	random  io.Reader
	now     func() time.Time
	entries map[ID]Manifest
	blobs   map[string]blobRecord

	reservedIDs  map[ID]string
	reservedSize int64
	storageBytes int64
	closed       bool

	uploadStates   map[UploadID]*uploadState
	uploadTerminal map[UploadID]UploadAcknowledgement
	uploadNotices  chan UploadAcknowledgement
	autoTimeouts   bool

	// Deterministic source-race seams are package-private and nil in
	// production. Tests use them to prove identity and size churn detection.
	beforeSourceRead    func()
	afterSourceRead     func()
	beforeUploadPublish func()
}

// OpenStore creates or reacquires a private store rooted at directory.
// Existing manifests are strictly decoded and every referenced blob is
// verified before the store is returned.
func OpenStore(directory string, options Options) (*Store, error) {
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(directory) == "" {
		return nil, ErrStoreUnsafe
	}
	root, err := platform.AcquirePrivateDirectory(directory)
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	blobDir, err := root.EnsurePrivateChild("blobs")
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	manifestDir, err := root.EnsurePrivateChild("manifests")
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	uploadDir, err := root.EnsurePrivateChild("uploads")
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	random := options.Random
	if random == nil {
		random = cryptorand.Reader
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	store := &Store{
		root:           root,
		blobDir:        blobDir,
		manifestDir:    manifestDir,
		uploadDir:      uploadDir,
		limits:         limits,
		random:         random,
		now:            now,
		entries:        make(map[ID]Manifest),
		blobs:          make(map[string]blobRecord),
		reservedIDs:    make(map[ID]string),
		uploadStates:   make(map[UploadID]*uploadState),
		uploadTerminal: make(map[UploadID]UploadAcknowledgement),
		// The terminal upload-attempt ledger independently uses the same
		// numeric cap as durable manifests. Sizing the notice queue to that
		// bound prevents a delayed stream consumer from losing timeout
		// acknowledgements across sequential waves of bounded uploads.
		uploadNotices: make(chan UploadAcknowledgement, limits.MaxUploadsPerSession),
		autoTimeouts:  options.Now == nil,
	}
	if err := store.loadAndReconcile(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// Capability returns a defensive copy of this store's advertised contract.
func (store *Store) Capability() Capability {
	store.mu.Lock()
	defer store.mu.Unlock()
	capability, _ := CapabilityFor(store.limits)
	return capability
}

// Limits returns this store's immutable normalized bounds.
func (store *Store) Limits() Limits {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.limits
}

// ImportFile snapshots one explicit caller-selected regular file. The source
// path is discarded before the immutable store commit.
func (store *Store) ImportFile(ctx context.Context, request FileImport) (Manifest, error) {
	if ctx == nil {
		return Manifest{}, errors.New("attachment import context is required")
	}
	if request.AttachmentID == "" {
		store.mu.Lock()
		generated, err := newID("att", store.random)
		store.mu.Unlock()
		if err != nil {
			return Manifest{}, err
		}
		request.AttachmentID = ID(generated)
	}
	if err := ValidateAttachmentID(request.AttachmentID); err != nil {
		return Manifest{}, err
	}
	if request.Name == "" {
		request.Name = filepath.Base(filepath.Clean(request.Path))
	}
	if err := validateDisplayName(request.Name, store.limits); err != nil {
		return Manifest{}, err
	}
	if request.MIMEType != "" {
		if _, err := kindForMIME(request.MIMEType, store.limits); err != nil {
			return Manifest{}, err
		}
	}
	if err := store.reserve(request.AttachmentID, "file"); err != nil {
		return Manifest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			store.releaseReservation(request.AttachmentID, 0)
		}
	}()

	data, err := store.snapshotSelectedFile(ctx, request.Path)
	if err != nil {
		return Manifest{}, err
	}
	defer clear(data)
	normalized, err := normalizeMedia(data, request.MIMEType, store.limits)
	if err != nil {
		return Manifest{}, err
	}
	defer clear(normalized.bytes)
	manifest, err := store.commitNormalized(ctx, request.AttachmentID, request.Name, normalized, 0)
	if err != nil {
		return Manifest{}, err
	}
	committed = true
	return manifest, nil
}

// Resolve returns a verified immutable copy of one attachment's bytes.
func (store *Store) Resolve(ctx context.Context, id ID) (Manifest, []byte, error) {
	resolved, err := store.ResolveMany(ctx, []ID{id})
	if err != nil {
		return Manifest{}, nil, err
	}
	return resolved[0].Manifest, resolved[0].Bytes, nil
}

// ResolveMany atomically validates the complete ordered set before returning
// any provider-bound data. Duplicate references are rejected.
func (store *Store) ResolveMany(ctx context.Context, ids []ID) ([]Resolved, error) {
	if ctx == nil {
		return nil, errors.New("attachment resolve context is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil, ErrClosed
	}
	if len(ids) == 0 || len(ids) > store.limits.MaxAttachmentsPerMessage {
		return nil, ErrResourceLimit
	}
	seen := make(map[ID]struct{}, len(ids))
	var aggregate int64
	manifests := make([]Manifest, len(ids))
	for index, id := range ids {
		if err := ValidateAttachmentID(id); err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, ErrDuplicate
		}
		seen[id] = struct{}{}
		manifest, exists := store.entries[id]
		if !exists {
			return nil, ErrNotCommitted
		}
		if manifest.SizeBytes > store.limits.MaxAggregateBytes-aggregate ||
			manifest.SizeBytes > store.limits.MaxModelRequestMediaBytes-aggregate {
			return nil, ErrResourceLimit
		}
		aggregate += manifest.SizeBytes
		manifests[index] = manifest
	}
	output := make([]Resolved, len(manifests))
	for index, manifest := range manifests {
		data, err := store.readBlobLocked(ctx, manifest)
		if err != nil {
			return nil, err
		}
		output[index] = Resolved{Manifest: manifest, Bytes: data}
	}
	return output, nil
}

// Verify proves that a transcript manifest still matches the store mapping and
// immutable bytes.
func (store *Store) Verify(ctx context.Context, manifest Manifest) error {
	if ctx == nil {
		return errors.New("attachment verify context is required")
	}
	if err := manifest.Validate(store.limits); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	stored, exists := store.entries[manifest.AttachmentID]
	if !exists {
		return ErrNotCommitted
	}
	if stored != manifest {
		return ErrTampered
	}
	_, err := store.readBlobLocked(ctx, manifest)
	return err
}

// CopyTo verifies and safely copies a complete ordered set into another
// session store. Storage identities remain content-addressed and source paths
// are never involved.
func (store *Store) CopyTo(ctx context.Context, destination *Store, ids []ID) error {
	if destination == nil || destination == store {
		return ErrStoreUnsafe
	}
	seen := make(map[ID]struct{}, len(ids))
	manifests := make([]Manifest, len(ids))
	// Verify the complete history-sized set before mutating the destination,
	// while keeping only one bounded attachment in memory at a time. ResolveMany
	// intentionally enforces per-message limits and therefore is not suitable
	// for a fork containing many valid turns.
	for index, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return ErrDuplicate
		}
		seen[id] = struct{}{}
		manifest, data, err := store.Resolve(ctx, id)
		clear(data)
		if err != nil {
			return err
		}
		manifests[index] = manifest
	}
	for index, expected := range manifests {
		manifest, data, err := store.Resolve(ctx, expected.AttachmentID)
		if err != nil || manifest != expected {
			clear(data)
			if err != nil {
				return err
			}
			return ErrTampered
		}
		normalized := normalizedMedia{
			kind: manifest.Kind, mimeType: manifest.MIMEType,
			bytes: data, digest: manifest.SHA256,
		}
		if err := destination.reserve(manifest.AttachmentID, "copy"); err != nil {
			clear(data)
			return err
		}
		if _, err := destination.commitNormalized(
			ctx, manifest.AttachmentID, manifest.Name, normalized, 0,
		); err != nil {
			destination.releaseReservation(manifest.AttachmentID, 0)
			clear(data)
			return err
		}
		clear(data)
		manifests[index] = Manifest{}
	}
	return nil
}

// Collect removes committed manifests not named by the authoritative durable
// reference set, then removes only blobs whose logical reference count reaches
// zero. All retained references are verified before the first mutation.
func (store *Store) Collect(ctx context.Context, referenced []ID) (CleanupResult, error) {
	if ctx == nil {
		return CleanupResult{}, errors.New("attachment cleanup context is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return CleanupResult{}, ErrClosed
	}
	retain := make(map[ID]struct{}, len(referenced))
	for _, id := range referenced {
		if err := ValidateAttachmentID(id); err != nil {
			return CleanupResult{}, err
		}
		if _, duplicate := retain[id]; duplicate {
			return CleanupResult{}, ErrDuplicate
		}
		manifest, exists := store.entries[id]
		if !exists {
			return CleanupResult{}, ErrNotCommitted
		}
		if _, err := store.readBlobLocked(ctx, manifest); err != nil {
			return CleanupResult{}, err
		}
		retain[id] = struct{}{}
	}

	var result CleanupResult
	ids := make([]string, 0, len(store.entries))
	for id := range store.entries {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, rawID := range ids {
		id := ID(rawID)
		if _, keep := retain[id]; keep {
			continue
		}
		manifest := store.entries[id]
		if err := removeOwnedFile(store.manifestDir, manifestFilename(id)); err != nil {
			return result, ErrStoreUnsafe
		}
		delete(store.entries, id)
		record := store.blobs[manifest.SHA256]
		record.refs--
		store.blobs[manifest.SHA256] = record
		result.ManifestsRemoved++
	}
	for digest, record := range store.blobs {
		if record.refs > 0 {
			continue
		}
		if err := removeOwnedFile(store.blobDir, blobFilename(digest)); err != nil {
			return result, ErrStoreUnsafe
		}
		delete(store.blobs, digest)
		store.storageBytes -= record.size
		result.BlobsRemoved++
		result.BytesRemoved += record.size
	}
	return result, nil
}

// DiscardUnreferenced removes one committed import that has not crossed
// durable user-message admission. Callers must never use it for an attachment
// referenced by transcript history; retained history is reconciled through
// Collect instead.
func (store *Store) DiscardUnreferenced(ctx context.Context, id ID) error {
	if ctx == nil {
		return errors.New("attachment discard context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateAttachmentID(id); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	if _, reserved := store.reservedIDs[id]; reserved {
		return ErrUploadState
	}
	manifest, exists := store.entries[id]
	if !exists {
		return ErrNotCommitted
	}
	if _, err := store.readBlobLocked(ctx, manifest); err != nil {
		return err
	}
	if err := removeOwnedFile(store.manifestDir, manifestFilename(id)); err != nil {
		return ErrStoreUnsafe
	}
	delete(store.entries, id)
	record := store.blobs[manifest.SHA256]
	record.refs--
	if record.refs > 0 {
		store.blobs[manifest.SHA256] = record
		return nil
	}
	if err := removeOwnedFile(store.blobDir, blobFilename(manifest.SHA256)); err != nil {
		store.blobs[manifest.SHA256] = record
		return ErrStoreUnsafe
	}
	delete(store.blobs, manifest.SHA256)
	store.storageBytes -= record.size
	if store.storageBytes < 0 {
		store.storageBytes = 0
	}
	return nil
}

// Close aborts every in-flight upload, removes its temporary file, and makes
// future operations fail. Committed manifests and blobs remain durable.
func (store *Store) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	var result error
	for id, upload := range store.uploadStates {
		if err := store.removeUploadLocked(upload); err != nil {
			result = errors.Join(result, ErrStoreUnsafe)
		}
		store.releaseUploadReservationLocked(upload)
		store.uploadTerminal[id] = UploadAcknowledgement{
			UploadID: id, AttachmentID: upload.request.AttachmentID,
			Status: UploadAborted, Terminal: true, Reason: "store_closed",
		}
		delete(store.uploadStates, id)
	}
	store.closed = true
	close(store.uploadNotices)
	return result
}

func (store *Store) reserve(id ID, owner string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	if _, exists := store.entries[id]; exists {
		return ErrDuplicate
	}
	if _, exists := store.reservedIDs[id]; exists {
		return ErrDuplicate
	}
	if len(store.entries)+len(store.reservedIDs) >= store.limits.MaxUploadsPerSession {
		return ErrResourceLimit
	}
	store.reservedIDs[id] = owner
	return nil
}

func (store *Store) releaseReservation(id ID, size int64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.reservedIDs, id)
	if size > 0 {
		store.reservedSize -= size
		if store.reservedSize < 0 {
			store.reservedSize = 0
		}
	}
}

func (store *Store) commitNormalized(
	ctx context.Context,
	id ID,
	name string,
	normalized normalizedMedia,
	reservedSize int64,
) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	manifest, err := store.normalizedManifest(id, name, normalized)
	if err != nil {
		return Manifest{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	return store.commitNormalizedLocked(ctx, manifest, normalized.bytes, reservedSize)
}

func (store *Store) normalizedManifest(
	id ID,
	name string,
	normalized normalizedMedia,
) (Manifest, error) {
	manifest := Manifest{
		AttachmentID: id,
		Kind:         normalized.kind,
		Name:         name,
		MIMEType:     normalized.mimeType,
		SizeBytes:    int64(len(normalized.bytes)),
		SHA256:       normalized.digest,
		StorageID:    storageID(normalized.digest),
	}
	if err := manifest.Validate(store.limits); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (store *Store) commitNormalizedLocked(
	ctx context.Context,
	manifest Manifest,
	data []byte,
	reservedSize int64,
) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if store.closed {
		return Manifest{}, ErrClosed
	}
	if _, exists := store.entries[manifest.AttachmentID]; exists {
		return Manifest{}, ErrDuplicate
	}
	if _, reserved := store.reservedIDs[manifest.AttachmentID]; !reserved {
		return Manifest{}, ErrUploadState
	}
	if len(store.entries) >= store.limits.MaxUploadsPerSession {
		return Manifest{}, ErrResourceLimit
	}
	record, blobExists := store.blobs[manifest.SHA256]
	if !blobExists {
		if manifest.SizeBytes > store.limits.MaxStorageBytes-store.storageBytes {
			return Manifest{}, ErrResourceLimit
		}
		if err := writeImmutableFile(
			store.blobDir, blobFilename(manifest.SHA256), data, store.random,
		); err != nil {
			return Manifest{}, ErrStoreUnsafe
		}
		record = blobRecord{size: manifest.SizeBytes}
		store.storageBytes += manifest.SizeBytes
	} else if record.size != manifest.SizeBytes {
		return Manifest{}, ErrTampered
	} else if _, err := store.readBlobLocked(ctx, manifest); err != nil {
		return Manifest{}, err
	}

	encoded, err := json.Marshal(manifestEnvelope{Version: ProtocolVersion, Manifest: manifest})
	if err != nil || len(encoded) > maximumManifestBytes {
		if !blobExists {
			_ = removeOwnedFile(store.blobDir, blobFilename(manifest.SHA256))
			store.storageBytes -= manifest.SizeBytes
		}
		return Manifest{}, ErrInvalidManifest
	}
	if err := writeImmutableFile(
		store.manifestDir, manifestFilename(manifest.AttachmentID), encoded, store.random,
	); err != nil {
		if !blobExists {
			_ = removeOwnedFile(store.blobDir, blobFilename(manifest.SHA256))
			store.storageBytes -= manifest.SizeBytes
		}
		return Manifest{}, ErrStoreUnsafe
	}
	record.refs++
	store.blobs[manifest.SHA256] = record
	store.entries[manifest.AttachmentID] = manifest
	delete(store.reservedIDs, manifest.AttachmentID)
	if reservedSize > 0 {
		store.reservedSize -= reservedSize
		if store.reservedSize < 0 {
			store.reservedSize = 0
		}
	}
	return manifest, nil
}

func (store *Store) loadAndReconcile(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := cleanupTemporaryFiles(store.uploadDir, "upload_"); err != nil {
		return ErrStoreUnsafe
	}
	if err := cleanupTemporaryFiles(store.manifestDir, tempPrefix); err != nil {
		return ErrStoreUnsafe
	}
	if err := cleanupTemporaryFiles(store.blobDir, tempPrefix); err != nil {
		return ErrStoreUnsafe
	}

	names, err := listOwnedDirectory(store.manifestDir)
	if err != nil || len(names) > maximumStoreEntries {
		return ErrStoreUnsafe
	}
	if len(names) > store.limits.MaxUploadsPerSession {
		return ErrResourceLimit
	}
	for _, name := range names {
		if !strings.HasSuffix(name, manifestSuffix) {
			return ErrStoreUnsafe
		}
		raw, err := readOwnedFile(ctx, store.manifestDir, name, maximumManifestBytes)
		if err != nil {
			return ErrStoreUnsafe
		}
		var envelope manifestEnvelope
		if err := strictDecode(raw, &envelope); err != nil ||
			envelope.Version != ProtocolVersion ||
			envelope.Manifest.Validate(store.limits) != nil ||
			name != manifestFilename(envelope.Manifest.AttachmentID) {
			return ErrInvalidManifest
		}
		if _, duplicate := store.entries[envelope.Manifest.AttachmentID]; duplicate {
			return ErrDuplicate
		}
		store.entries[envelope.Manifest.AttachmentID] = envelope.Manifest
		record := store.blobs[envelope.Manifest.SHA256]
		if record.refs > 0 && record.size != envelope.Manifest.SizeBytes {
			return ErrTampered
		}
		record.size = envelope.Manifest.SizeBytes
		record.refs++
		store.blobs[envelope.Manifest.SHA256] = record
	}

	blobNames, err := listOwnedDirectory(store.blobDir)
	if err != nil || len(blobNames) > maximumStoreEntries {
		return ErrStoreUnsafe
	}
	present := make(map[string]struct{}, len(blobNames))
	for _, name := range blobNames {
		if !strings.HasSuffix(name, blobSuffix) {
			return ErrStoreUnsafe
		}
		digest := strings.TrimSuffix(name, blobSuffix)
		if !digestPattern.MatchString(digest) || name != blobFilename(digest) {
			return ErrStoreUnsafe
		}
		record, referenced := store.blobs[digest]
		if !referenced {
			if err := removeOwnedFile(store.blobDir, name); err != nil {
				return ErrStoreUnsafe
			}
			continue
		}
		manifest := Manifest{
			AttachmentID: firstIDForDigest(store.entries, digest),
			Kind:         firstKindForDigest(store.entries, digest),
			Name:         firstNameForDigest(store.entries, digest),
			MIMEType:     firstMIMEForDigest(store.entries, digest),
			SizeBytes:    record.size,
			SHA256:       digest,
			StorageID:    storageID(digest),
		}
		data, err := readOwnedFile(ctx, store.blobDir, name, store.limits.MaxItemBytes)
		if err != nil || int64(len(data)) != record.size {
			return ErrTampered
		}
		if err := VerifyResolved(manifest, data, store.limits); err != nil {
			return ErrTampered
		}
		store.storageBytes += record.size
		if store.storageBytes > store.limits.MaxStorageBytes {
			return ErrResourceLimit
		}
		present[digest] = struct{}{}
	}
	for digest := range store.blobs {
		if _, exists := present[digest]; !exists {
			return ErrTampered
		}
	}
	return nil
}

func firstIDForDigest(entries map[ID]Manifest, digest string) ID {
	for id, manifest := range entries {
		if manifest.SHA256 == digest {
			return id
		}
	}
	return ""
}

func firstKindForDigest(entries map[ID]Manifest, digest string) Kind {
	for _, manifest := range entries {
		if manifest.SHA256 == digest {
			return manifest.Kind
		}
	}
	return ""
}

func firstNameForDigest(entries map[ID]Manifest, digest string) string {
	for _, manifest := range entries {
		if manifest.SHA256 == digest {
			return manifest.Name
		}
	}
	return ""
}

func firstMIMEForDigest(entries map[ID]Manifest, digest string) string {
	for _, manifest := range entries {
		if manifest.SHA256 == digest {
			return manifest.MIMEType
		}
	}
	return ""
}

func (store *Store) readBlobLocked(ctx context.Context, manifest Manifest) ([]byte, error) {
	data, err := readOwnedFile(
		ctx, store.blobDir, blobFilename(manifest.SHA256), store.limits.MaxItemBytes,
	)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || int64(len(data)) != manifest.SizeBytes {
		return nil, ErrTampered
	}
	if err := VerifyResolved(manifest, data, store.limits); err != nil {
		return nil, ErrTampered
	}
	return data, nil
}

func (store *Store) snapshotSelectedFile(ctx context.Context, source string) ([]byte, error) {
	if strings.TrimSpace(source) == "" {
		return nil, ErrUnsafeSource
	}
	path, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return nil, ErrUnsafeSource
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() <= 0 || before.Size() > store.limits.MaxItemBytes {
		if before != nil && before.Size() > store.limits.MaxItemBytes {
			return nil, ErrResourceLimit
		}
		return nil, ErrUnsafeSource
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrUnsafeSource
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, ErrUnsafeSource
	}
	if count, known := regularFileLinkCount(file, opened); !known || count != 1 {
		return nil, ErrUnsafeSource
	}
	confirmed, err := os.Lstat(path)
	if err != nil || confirmed.Mode()&os.ModeSymlink != 0 ||
		!confirmed.Mode().IsRegular() || !os.SameFile(opened, confirmed) {
		return nil, ErrUnsafeSource
	}
	if store.beforeSourceRead != nil {
		store.beforeSourceRead()
	}
	data, err := readStableSourcePass(ctx, file, path, opened, store.limits.MaxItemBytes)
	if err != nil {
		return nil, err
	}
	if store.afterSourceRead != nil {
		store.afterSourceRead()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrUnsafeSource
	}
	second, err := hashStableSourcePass(ctx, file, path, opened, store.limits.MaxItemBytes)
	if err != nil {
		return nil, err
	}
	first := sha256.Sum256(data)
	if first != second {
		return nil, ErrUnsafeSource
	}
	return data, nil
}

func readStableSourcePass(
	ctx context.Context,
	file *os.File,
	path string,
	expected os.FileInfo,
	maximum int64,
) ([]byte, error) {
	output := make([]byte, 0, expected.Size())
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if int64(count) > maximum-total {
				return nil, ErrResourceLimit
			}
			total += int64(count)
			output = append(output, buffer[:count]...)
		}
		if err := validateOpenSource(file, path, expected, total, readErr == io.EOF); err != nil {
			return nil, err
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, ErrUnsafeSource
		}
	}
	if total != expected.Size() {
		return nil, ErrSizeMismatch
	}
	return output, nil
}

func hashStableSourcePass(
	ctx context.Context,
	file *os.File,
	path string,
	expected os.FileInfo,
	maximum int64,
) ([sha256.Size]byte, error) {
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return [sha256.Size]byte{}, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if int64(count) > maximum-total {
				return [sha256.Size]byte{}, ErrResourceLimit
			}
			total += int64(count)
			_, _ = hash.Write(buffer[:count])
		}
		if err := validateOpenSource(file, path, expected, total, readErr == io.EOF); err != nil {
			return [sha256.Size]byte{}, err
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return [sha256.Size]byte{}, ErrUnsafeSource
		}
	}
	if total != expected.Size() {
		return [sha256.Size]byte{}, ErrSizeMismatch
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func validateOpenSource(
	file *os.File,
	path string,
	expected os.FileInfo,
	readBytes int64,
	atEOF bool,
) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) ||
		opened.Size() != expected.Size() || !opened.ModTime().Equal(expected.ModTime()) {
		return ErrUnsafeSource
	}
	if count, known := regularFileLinkCount(file, opened); !known || count != 1 {
		return ErrUnsafeSource
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() || !os.SameFile(expected, current) ||
		current.Size() != expected.Size() || !current.ModTime().Equal(expected.ModTime()) {
		return ErrUnsafeSource
	}
	if readBytes > expected.Size() || (atEOF && readBytes != expected.Size()) {
		return ErrSizeMismatch
	}
	return nil
}

func manifestFilename(id ID) string {
	return string(id) + manifestSuffix
}

func blobFilename(digest string) string {
	return digest + blobSuffix
}

func listOwnedDirectory(directory *platform.OwnedDirectory) ([]string, error) {
	if directory == nil || directory.Verify() != nil {
		return nil, ErrStoreUnsafe
	}
	root, err := directory.OpenRoot()
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	defer root.Close()
	handle, err := root.Open(".")
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	defer handle.Close()
	entries, err := handle.ReadDir(maximumStoreEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ErrStoreUnsafe
	}
	if len(entries) > maximumStoreEntries {
		return nil, ErrResourceLimit
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, ErrStoreUnsafe
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func cleanupTemporaryFiles(directory *platform.OwnedDirectory, prefix string) error {
	names, err := listOwnedDirectory(directory)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			if prefix == "upload_" {
				return ErrStoreUnsafe
			}
			continue
		}
		if err := removeOwnedFile(directory, name); err != nil {
			return err
		}
	}
	return nil
}

func readOwnedFile(
	ctx context.Context,
	directory *platform.OwnedDirectory,
	name string,
	maximum int64,
) ([]byte, error) {
	if directory == nil || maximum < 1 || !simpleStoreFilename(name) ||
		directory.Verify() != nil {
		return nil, ErrStoreUnsafe
	}
	root, err := directory.OpenRoot()
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() <= 0 || before.Size() > maximum {
		return nil, ErrTampered
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, ErrTampered
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		opened.Size() != before.Size() || !privateFileMode(opened) {
		return nil, ErrTampered
	}
	if count, known := regularFileLinkCount(file, opened); !known || count != 1 {
		return nil, ErrTampered
	}
	data := make([]byte, 0, opened.Size())
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if int64(count) > maximum-total {
				return nil, ErrTampered
			}
			total += int64(count)
			data = append(data, buffer[:count]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, ErrTampered
		}
	}
	after, err := file.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil ||
		!os.SameFile(opened, after) || !os.SameFile(after, current) ||
		total != opened.Size() || after.Size() != opened.Size() ||
		!after.ModTime().Equal(opened.ModTime()) || !privateFileMode(after) {
		return nil, ErrTampered
	}
	if directory.Verify() != nil {
		return nil, ErrStoreUnsafe
	}
	return data, nil
}

func writeImmutableFile(
	directory *platform.OwnedDirectory,
	name string,
	data []byte,
	random io.Reader,
) (resultErr error) {
	if directory == nil || len(data) == 0 || !simpleStoreFilename(name) ||
		directory.Verify() != nil {
		return ErrStoreUnsafe
	}
	root, err := directory.OpenRoot()
	if err != nil {
		return ErrStoreUnsafe
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, ErrStoreUnsafe)
		}
	}()
	if _, err := root.Lstat(name); err == nil {
		return ErrDuplicate
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrStoreUnsafe
	}
	var token [16]byte
	if _, err := io.ReadFull(random, token[:]); err != nil {
		return fmt.Errorf("generate attachment temporary identity: %w", err)
	}
	temp := tempPrefix + hex.EncodeToString(token[:])
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return ErrStoreUnsafe
	}
	linked := false
	defer func() {
		closeErr := file.Close()
		if !linked {
			_ = root.Remove(temp)
		}
		if closeErr != nil {
			resultErr = errors.Join(resultErr, ErrStoreUnsafe)
		}
	}()
	if err := writeFull(file, data); err != nil {
		return ErrStoreUnsafe
	}
	if err := file.Sync(); err != nil {
		return ErrStoreUnsafe
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) ||
		!privateFileMode(info) {
		return ErrStoreUnsafe
	}
	if err := root.Link(temp, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrDuplicate
		}
		return ErrStoreUnsafe
	}
	linked = true
	if err := root.Remove(temp); err != nil {
		_ = root.Remove(name)
		return ErrStoreUnsafe
	}
	if err := directory.Sync(); err != nil {
		_ = root.Remove(name)
		return ErrStoreUnsafe
	}
	published, err := root.Lstat(name)
	if err != nil || !published.Mode().IsRegular() || published.Size() != int64(len(data)) ||
		!privateFileMode(published) {
		return ErrStoreUnsafe
	}
	current, err := file.Stat()
	if err != nil || !os.SameFile(published, current) {
		return ErrStoreUnsafe
	}
	if count, known := regularFileLinkCount(file, current); !known || count != 1 {
		return ErrStoreUnsafe
	}
	return directory.Verify()
}

func removeOwnedFile(directory *platform.OwnedDirectory, name string) error {
	if directory == nil || !simpleStoreFilename(name) || directory.Verify() != nil {
		return ErrStoreUnsafe
	}
	root, err := directory.OpenRoot()
	if err != nil {
		return ErrStoreUnsafe
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStoreUnsafe
	}
	if err := root.Remove(name); err != nil {
		return ErrStoreUnsafe
	}
	if err := directory.Sync(); err != nil {
		return ErrStoreUnsafe
	}
	return nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if count > 0 {
			data = data[count:]
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func strictDecode(data []byte, target any) error {
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func rejectDuplicateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object member name is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON member %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func simpleStoreFilename(name string) bool {
	return name != "" && name == filepath.Base(name) && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && len(name) <= 128
}

func privateFileMode(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm() == 0o600
}
