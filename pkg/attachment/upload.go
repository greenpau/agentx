package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"sort"
	"time"
)

type uploadState struct {
	request    BeginUpload
	tempName   string
	file       *os.File
	hash       hash.Hash
	next       uint64
	received   int64
	deadline   time.Time
	committing bool
	timer      *time.Timer
}

// Begin reserves count, aggregate bytes, storage, and attachment identity
// before accepting a single chunk.
func (store *Store) Begin(ctx context.Context, request BeginUpload) (UploadAcknowledgement, error) {
	if ctx == nil {
		return UploadAcknowledgement{}, errors.New("attachment upload context is required")
	}
	if err := ctx.Err(); err != nil {
		return UploadAcknowledgement{}, err
	}
	if err := ValidateUploadID(request.UploadID); err != nil {
		return UploadAcknowledgement{}, err
	}
	if err := ValidateAttachmentID(request.AttachmentID); err != nil {
		return UploadAcknowledgement{}, err
	}
	if err := validateDisplayName(request.Name, store.limits); err != nil {
		return UploadAcknowledgement{}, err
	}
	if _, err := kindForMIME(request.MIMEType, store.limits); err != nil {
		return UploadAcknowledgement{}, err
	}
	if request.SizeBytes <= 0 || request.SizeBytes > store.limits.MaxItemBytes {
		return UploadAcknowledgement{}, ErrResourceLimit
	}
	if !digestPattern.MatchString(request.SHA256) {
		return UploadAcknowledgement{}, ErrInvalidManifest
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return UploadAcknowledgement{}, ErrClosed
	}
	store.expireUploadsLocked(store.now())
	if _, exists := store.uploadStates[request.UploadID]; exists {
		return UploadAcknowledgement{}, ErrDuplicate
	}
	if _, exists := store.uploadTerminal[request.UploadID]; exists {
		return UploadAcknowledgement{}, ErrUploadTerminal
	}
	if len(store.uploadStates) >= store.limits.MaxConcurrentUploads {
		return UploadAcknowledgement{}, ErrResourceLimit
	}
	// Terminal upload attempts and durable manifests have independent ledgers.
	// Both use MaxUploadsPerSession as their numeric ceiling. Every active
	// upload reserves the terminal-ledger entry it must eventually produce.
	if len(store.uploadTerminal)+len(store.uploadStates) >=
		store.limits.MaxUploadsPerSession {
		return UploadAcknowledgement{}, ErrResourceLimit
	}
	if _, exists := store.entries[request.AttachmentID]; exists {
		return UploadAcknowledgement{}, ErrDuplicate
	}
	if _, exists := store.reservedIDs[request.AttachmentID]; exists {
		return UploadAcknowledgement{}, ErrDuplicate
	}
	if len(store.entries)+len(store.reservedIDs) >= store.limits.MaxUploadsPerSession {
		return UploadAcknowledgement{}, ErrResourceLimit
	}
	if request.SizeBytes > store.limits.MaxAggregateBytes-store.reservedSize ||
		request.SizeBytes > store.limits.MaxStorageBytes-store.storageBytes-store.reservedSize {
		return UploadAcknowledgement{}, ErrResourceLimit
	}

	tempName := uploadFilename(request.UploadID)
	file, err := createUploadFile(store.uploadDir, tempName)
	if err != nil {
		return UploadAcknowledgement{}, ErrStoreUnsafe
	}
	upload := &uploadState{
		request: request, tempName: tempName, file: file, hash: sha256.New(),
		deadline: store.now().Add(store.limits.UploadTimeout),
	}
	store.uploadStates[request.UploadID] = upload
	store.reservedIDs[request.AttachmentID] = "upload"
	store.reservedSize += request.SizeBytes
	if store.autoTimeouts {
		upload.timer = time.AfterFunc(store.limits.UploadTimeout, func() {
			store.expireAndNotify(request.UploadID)
		})
	}
	return UploadAcknowledgement{
		UploadID: request.UploadID, AttachmentID: request.AttachmentID,
		Status: UploadAccepted, Terminal: false,
	}, nil
}

// Chunk accepts one strict padded-base64 chunk at the exact next sequence,
// beginning at sequence zero. A correlated chunk protocol violation is
// terminal; callers obtain its acknowledgement with UploadOutcome.
func (store *Store) Chunk(
	ctx context.Context,
	uploadID UploadID,
	sequence uint64,
	encoded string,
) error {
	if ctx == nil {
		return errors.New("attachment chunk context is required")
	}
	if err := ValidateUploadID(uploadID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		store.settleCancelledUpload(uploadID)
		return err
	}
	decoded, validationErr := DecodeStrictBase64Chunk(encoded, store.limits)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	if store.expireUploadLocked(uploadID, store.now()) {
		return ErrUploadExpired
	}
	upload, exists := store.uploadStates[uploadID]
	if !exists {
		if _, terminal := store.uploadTerminal[uploadID]; terminal {
			return ErrUploadTerminal
		}
		return ErrUploadState
	}
	if upload.committing {
		return ErrUploadState
	}
	if validationErr != nil {
		store.failUploadLocked(uploadID, upload, "chunk_rejected")
		return validationErr
	}
	if sequence != upload.next {
		store.failUploadLocked(uploadID, upload, "sequence_mismatch")
		return ErrSequence
	}
	if int64(len(decoded)) > upload.request.SizeBytes-upload.received {
		store.failUploadLocked(uploadID, upload, "size_mismatch")
		return ErrSizeMismatch
	}
	if err := writeFull(upload.file, decoded); err != nil {
		store.failUploadLocked(uploadID, upload, "storage_failure")
		return ErrStoreUnsafe
	}
	_, _ = upload.hash.Write(decoded)
	upload.received += int64(len(decoded))
	upload.next++
	info, err := upload.file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		!privateFileMode(info) || info.Size() != upload.received {
		store.failUploadLocked(uploadID, upload, "storage_failure")
		return ErrStoreUnsafe
	}
	return nil
}

// DecodeStrictBase64Chunk validates the physical chunk alphabet, padding, and
// decoded bounds. encoding/base64 intentionally ignores CR/LF even in Strict
// mode, so the explicit alphabet pass is required for a no-whitespace wire
// contract.
func DecodeStrictBase64Chunk(encoded string, limits Limits) ([]byte, error) {
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, ErrBase64
	}
	if len(encoded) > normalized.MaxChunkEncodedBytes {
		return nil, ErrResourceLimit
	}
	if len(encoded)%4 != 0 {
		return nil, ErrBase64
	}
	padding := 0
	for index := 0; index < len(encoded); index++ {
		value := encoded[index]
		switch {
		case value >= 'A' && value <= 'Z',
			value >= 'a' && value <= 'z',
			value >= '0' && value <= '9',
			value == '+', value == '/':
			if padding != 0 {
				return nil, ErrBase64
			}
		case value == '=':
			padding++
			if padding > 2 || index < len(encoded)-2 {
				return nil, ErrBase64
			}
		default:
			return nil, ErrBase64
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, ErrBase64
	}
	if len(decoded) == 0 || len(decoded) > normalized.MaxChunkDecodedBytes {
		clear(decoded)
		return nil, ErrResourceLimit
	}
	return decoded, nil
}

// Commit verifies the exact declared byte count and raw digest, normalizes the
// media, publishes the immutable blob and manifest, and returns the upload's
// sole terminal acknowledgement.
func (store *Store) Commit(
	ctx context.Context,
	uploadID UploadID,
) (UploadAcknowledgement, error) {
	if ctx == nil {
		return UploadAcknowledgement{}, errors.New("attachment commit context is required")
	}
	if err := ValidateUploadID(uploadID); err != nil {
		return UploadAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		ack, settled := store.settleCancelledUpload(uploadID)
		if settled {
			return ack, err
		}
		return UploadAcknowledgement{}, err
	}

	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		return UploadAcknowledgement{}, ErrClosed
	}
	if store.expireUploadLocked(uploadID, store.now()) {
		ack := store.uploadTerminal[uploadID]
		store.mu.Unlock()
		return ack, ErrUploadExpired
	}
	upload, exists := store.uploadStates[uploadID]
	if !exists {
		_, terminal := store.uploadTerminal[uploadID]
		store.mu.Unlock()
		if terminal {
			return UploadAcknowledgement{}, ErrUploadTerminal
		}
		return UploadAcknowledgement{}, ErrUploadState
	}
	if upload.committing {
		store.mu.Unlock()
		return UploadAcknowledgement{}, ErrUploadState
	}
	if upload.received != upload.request.SizeBytes {
		ack := store.failUploadLocked(uploadID, upload, "size_mismatch")
		store.mu.Unlock()
		return ack, ErrSizeMismatch
	}
	if hex.EncodeToString(upload.hash.Sum(nil)) != upload.request.SHA256 {
		ack := store.failUploadLocked(uploadID, upload, "digest_mismatch")
		store.mu.Unlock()
		return ack, ErrDigestMismatch
	}
	if err := upload.file.Sync(); err != nil {
		ack := store.failUploadLocked(uploadID, upload, "storage_failure")
		store.mu.Unlock()
		return ack, ErrStoreUnsafe
	}
	if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
		ack := store.failUploadLocked(uploadID, upload, "storage_failure")
		store.mu.Unlock()
		return ack, ErrStoreUnsafe
	}
	raw, err := readUploadFile(ctx, upload.file, upload.request.SizeBytes)
	if err != nil {
		reason := "storage_failure"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = string(AbortCancellation)
		}
		ack := store.failUploadLocked(uploadID, upload, reason)
		store.mu.Unlock()
		return ack, err
	}
	defer clear(raw)
	rawDigest := sha256.Sum256(raw)
	if hex.EncodeToString(rawDigest[:]) != upload.request.SHA256 {
		ack := store.failUploadLocked(uploadID, upload, "digest_mismatch")
		store.mu.Unlock()
		clear(raw)
		return ack, ErrDigestMismatch
	}
	upload.committing = true
	store.mu.Unlock()

	normalized, normalizeErr := normalizeMedia(raw, upload.request.MIMEType, store.limits)
	if normalizeErr != nil {
		store.mu.Lock()
		if ack, terminal := store.uploadTerminal[uploadID]; terminal {
			store.mu.Unlock()
			return ack, ErrClosed
		}
		ack := store.failUploadLocked(uploadID, upload, "media_rejected")
		store.mu.Unlock()
		return ack, normalizeErr
	}
	defer clear(normalized.bytes)
	if store.beforeUploadPublish != nil {
		store.beforeUploadPublish()
	}
	if err := store.finishUploadTemporary(uploadID, upload); err != nil {
		store.mu.Lock()
		if ack, terminal := store.uploadTerminal[uploadID]; terminal {
			store.mu.Unlock()
			return ack, err
		}
		ack := store.failUploadLocked(uploadID, upload, "storage_failure")
		store.mu.Unlock()
		return ack, err
	}
	manifest, manifestErr := store.normalizedManifest(
		upload.request.AttachmentID, upload.request.Name, normalized,
	)
	store.mu.Lock()
	defer store.mu.Unlock()
	if ack, terminal := store.uploadTerminal[uploadID]; terminal {
		return ack, ErrClosed
	}
	current, active := store.uploadStates[uploadID]
	if !active || current != upload || store.closed {
		return UploadAcknowledgement{}, ErrClosed
	}
	if !store.now().Before(upload.deadline) {
		ack := store.settleExpiredUploadLocked(uploadID, upload)
		return ack, ErrUploadExpired
	}
	var commitErr error
	if manifestErr != nil {
		commitErr = manifestErr
	} else {
		manifest, commitErr = store.commitNormalizedLocked(
			ctx, manifest, normalized.bytes, upload.request.SizeBytes,
		)
	}
	delete(store.uploadStates, uploadID)
	if upload.timer != nil {
		upload.timer.Stop()
	}
	if commitErr != nil {
		delete(store.reservedIDs, upload.request.AttachmentID)
		store.reservedSize -= upload.request.SizeBytes
		if store.reservedSize < 0 {
			store.reservedSize = 0
		}
		ack := UploadAcknowledgement{
			UploadID: uploadID, AttachmentID: upload.request.AttachmentID,
			Status: UploadFailed, Terminal: true, Reason: "commit_failed",
		}
		store.uploadTerminal[uploadID] = ack
		return ack, commitErr
	}
	ack := UploadAcknowledgement{
		UploadID: uploadID, AttachmentID: upload.request.AttachmentID,
		Status: UploadCommitted, Terminal: true, Manifest: &manifest,
	}
	store.uploadTerminal[uploadID] = ack
	return ack, nil
}

// Abort settles one active upload and removes its temporary bytes.
func (store *Store) Abort(
	ctx context.Context,
	uploadID UploadID,
) (UploadAcknowledgement, error) {
	if ctx == nil {
		return UploadAcknowledgement{}, errors.New("attachment abort context is required")
	}
	if err := ctx.Err(); err != nil {
		return UploadAcknowledgement{}, err
	}
	if err := ValidateUploadID(uploadID); err != nil {
		return UploadAcknowledgement{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return UploadAcknowledgement{}, ErrClosed
	}
	if store.expireUploadLocked(uploadID, store.now()) {
		return store.uploadTerminal[uploadID], ErrUploadExpired
	}
	upload, exists := store.uploadStates[uploadID]
	if !exists {
		if _, terminal := store.uploadTerminal[uploadID]; terminal {
			return UploadAcknowledgement{}, ErrUploadTerminal
		}
		return UploadAcknowledgement{}, ErrUploadState
	}
	if upload.committing {
		return UploadAcknowledgement{}, ErrUploadState
	}
	ack := store.abortUploadLocked(uploadID, upload, AbortCaller)
	return ack, nil
}

// AbortAll settles cancellation, EOF, process failure, or shutdown for every
// active non-committing upload in stable upload-ID order.
func (store *Store) AbortAll(reason AbortReason) []UploadAcknowledgement {
	if !validAbortReason(reason) {
		reason = AbortProcessFailure
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ids := make([]string, 0, len(store.uploadStates))
	for id, upload := range store.uploadStates {
		if !upload.committing {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	output := make([]UploadAcknowledgement, 0, len(ids))
	for _, rawID := range ids {
		id := UploadID(rawID)
		output = append(output, store.abortUploadLocked(id, store.uploadStates[id], reason))
	}
	return output
}

// Expire settles all deadlines at or before now and returns their sole
// terminal acknowledgements in stable upload-ID order.
func (store *Store) Expire(now time.Time) []UploadAcknowledgement {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.expireUploadsLocked(now)
}

// UploadOutcome returns a defensive terminal acknowledgement, if one exists.
func (store *Store) UploadOutcome(uploadID UploadID) (UploadAcknowledgement, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ack, exists := store.uploadTerminal[uploadID]
	if ack.Manifest != nil {
		copy := *ack.Manifest
		ack.Manifest = &copy
	}
	return ack, exists
}

// UploadNotifications carries automatic timeout acknowledgements. Explicit
// commit, abort, AbortAll, and Expire outcomes are returned directly and are
// not duplicated on this channel. The channel closes with Store.Close.
func (store *Store) UploadNotifications() <-chan UploadAcknowledgement {
	return store.uploadNotices
}

func (store *Store) expireUploadsLocked(now time.Time) []UploadAcknowledgement {
	ids := make([]string, 0, len(store.uploadStates))
	for id, upload := range store.uploadStates {
		if !upload.committing && !now.Before(upload.deadline) {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	output := make([]UploadAcknowledgement, 0, len(ids))
	for _, rawID := range ids {
		id := UploadID(rawID)
		upload := store.uploadStates[id]
		ack := UploadAcknowledgement{
			UploadID: id, AttachmentID: upload.request.AttachmentID,
			Status: UploadExpired, Terminal: true, Reason: "timeout",
		}
		if upload.timer != nil {
			upload.timer.Stop()
		}
		_ = store.removeUploadLocked(upload)
		store.releaseUploadReservationLocked(upload)
		delete(store.uploadStates, id)
		store.uploadTerminal[id] = ack
		output = append(output, ack)
	}
	return output
}

func (store *Store) expireUploadLocked(id UploadID, now time.Time) bool {
	upload, exists := store.uploadStates[id]
	if !exists || upload.committing || now.Before(upload.deadline) {
		return false
	}
	store.expireUploadsLocked(now)
	_, terminal := store.uploadTerminal[id]
	return terminal
}

func (store *Store) abortUploadLocked(
	id UploadID,
	upload *uploadState,
	reason AbortReason,
) UploadAcknowledgement {
	_ = store.removeUploadLocked(upload)
	store.releaseUploadReservationLocked(upload)
	delete(store.uploadStates, id)
	ack := UploadAcknowledgement{
		UploadID: id, AttachmentID: upload.request.AttachmentID,
		Status: UploadAborted, Terminal: true, Reason: string(reason),
	}
	store.uploadTerminal[id] = ack
	return ack
}

func (store *Store) failUploadLocked(
	id UploadID,
	upload *uploadState,
	reason string,
) UploadAcknowledgement {
	_ = store.removeUploadLocked(upload)
	store.releaseUploadReservationLocked(upload)
	delete(store.uploadStates, id)
	ack := UploadAcknowledgement{
		UploadID: id, AttachmentID: upload.request.AttachmentID,
		Status: UploadFailed, Terminal: true, Reason: reason,
	}
	store.uploadTerminal[id] = ack
	return ack
}

func (store *Store) settleExpiredUploadLocked(
	id UploadID,
	upload *uploadState,
) UploadAcknowledgement {
	_ = store.removeUploadLocked(upload)
	store.releaseUploadReservationLocked(upload)
	delete(store.uploadStates, id)
	ack := UploadAcknowledgement{
		UploadID: id, AttachmentID: upload.request.AttachmentID,
		Status: UploadExpired, Terminal: true, Reason: "timeout",
	}
	store.uploadTerminal[id] = ack
	return ack
}

func (store *Store) releaseUploadReservationLocked(upload *uploadState) {
	delete(store.reservedIDs, upload.request.AttachmentID)
	store.reservedSize -= upload.request.SizeBytes
	if store.reservedSize < 0 {
		store.reservedSize = 0
	}
}

func (store *Store) removeUploadLocked(upload *uploadState) error {
	var result error
	if upload.timer != nil {
		upload.timer.Stop()
		upload.timer = nil
	}
	if upload.file != nil {
		result = errors.Join(result, upload.file.Close())
		upload.file = nil
	}
	result = errors.Join(result, removeOwnedFile(store.uploadDir, upload.tempName))
	return result
}

func (store *Store) expireAndNotify(id UploadID) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return
	}
	upload, exists := store.uploadStates[id]
	if !exists || upload.committing {
		return
	}
	now := store.now()
	if now.Before(upload.deadline) {
		upload.timer = time.AfterFunc(upload.deadline.Sub(now), func() {
			store.expireAndNotify(id)
		})
		return
	}
	ack := UploadAcknowledgement{
		UploadID: id, AttachmentID: upload.request.AttachmentID,
		Status: UploadExpired, Terminal: true, Reason: "timeout",
	}
	_ = store.removeUploadLocked(upload)
	store.releaseUploadReservationLocked(upload)
	delete(store.uploadStates, id)
	store.uploadTerminal[id] = ack
	select {
	case store.uploadNotices <- ack:
	default:
		// The channel is sized to the maximum terminal upload ledger, so this
		// branch is unreachable unless a caller replaces package invariants.
	}
}

func (store *Store) settleCancelledUpload(id UploadID) (UploadAcknowledgement, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	upload, exists := store.uploadStates[id]
	if !exists || upload.committing {
		return UploadAcknowledgement{}, false
	}
	return store.abortUploadLocked(id, upload, AbortCancellation), true
}

func (store *Store) finishUploadTemporary(id UploadID, upload *uploadState) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.uploadStates[id]
	if store.closed || !exists || current != upload {
		if _, terminal := store.uploadTerminal[id]; terminal {
			return ErrUploadTerminal
		}
		return ErrUploadState
	}
	if upload.file == nil {
		return ErrUploadState
	}
	if err := upload.file.Close(); err != nil {
		upload.file = nil
		return ErrStoreUnsafe
	}
	upload.file = nil
	if err := removeOwnedFile(store.uploadDir, upload.tempName); err != nil {
		return ErrStoreUnsafe
	}
	return nil
}

func createUploadFile(
	directory interface {
		Verify() error
		OpenRoot() (*os.Root, error)
	},
	name string,
) (*os.File, error) {
	if directory == nil || !simpleStoreFilename(name) || directory.Verify() != nil {
		return nil, ErrStoreUnsafe
	}
	root, err := directory.OpenRoot()
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	defer root.Close()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrStoreUnsafe
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateFileMode(info) {
		file.Close()
		_ = root.Remove(name)
		return nil, ErrStoreUnsafe
	}
	return file, nil
}

func readUploadFile(ctx context.Context, file *os.File, expected int64) ([]byte, error) {
	data := make([]byte, 0, expected)
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := file.Read(buffer)
		if count > 0 {
			if int64(count) > expected-total {
				return nil, ErrSizeMismatch
			}
			total += int64(count)
			data = append(data, buffer[:count]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrStoreUnsafe
		}
	}
	if total != expected {
		return nil, ErrSizeMismatch
	}
	info, err := file.Stat()
	if err != nil || info.Size() != expected || !privateFileMode(info) {
		return nil, ErrStoreUnsafe
	}
	if count, known := regularFileLinkCount(file, info); !known || count != 1 {
		return nil, ErrStoreUnsafe
	}
	return data, nil
}

func uploadFilename(id UploadID) string {
	return "upload_" + string(id)
}

func validAbortReason(reason AbortReason) bool {
	switch reason {
	case AbortCaller, AbortCancellation, AbortEOF, AbortProcessFailure, AbortShutdown:
		return true
	default:
		return false
	}
}
