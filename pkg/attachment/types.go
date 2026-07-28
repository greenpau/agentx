// Package attachment owns bounded, immutable user attachment imports.
//
// Attachments are untrusted model input. A Manifest is safe metadata for
// transcripts and presentation, but it is not filesystem authority. Only a
// Store can resolve its opaque StorageID to bytes.
package attachment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the first provider-neutral attachment import
	// protocol understood by the native runtime.
	ProtocolVersion = 1

	MIMEPNG  = "image/png"
	MIMEJPEG = "image/jpeg"
	MIMEPDF  = "application/pdf"

	DefaultMaxAttachmentsPerMessage       = 8
	DefaultMaxConcurrentUploads           = 8
	DefaultMaxUploadsPerSession           = 100_000
	DefaultMaxItemBytes             int64 = 20 << 20
	DefaultMaxAggregateBytes        int64 = 40 << 20
	DefaultMaxStorageBytes          int64 = 512 << 20
	DefaultMaxModelMediaBytes       int64 = 40 << 20
	DefaultMaxChunkBytes                  = 256 << 10
	DefaultMaxDisplayNameBytes            = 255
	DefaultMaxMIMETypeBytes               = 64
	DefaultMaxImageDimension              = 8192
	DefaultMaxImagePixels           int64 = 20_000_000
	DefaultMaxPDFPages                    = 100
	DefaultUploadTimeout                  = 2 * time.Minute

	maximumManifestBytes = 4 << 10
	maximumStoreEntries  = DefaultMaxUploadsPerSession
)

var (
	ErrInvalidID         = errors.New("invalid attachment identity")
	ErrInvalidName       = errors.New("invalid attachment display name")
	ErrUnsupportedMedia  = errors.New("unsupported attachment media")
	ErrMediaMismatch     = errors.New("attachment MIME claim does not match content")
	ErrMalformedMedia    = errors.New("malformed attachment media")
	ErrResourceLimit     = errors.New("attachment resource limit exceeded")
	ErrUnsafeSource      = errors.New("selected attachment source is unsafe")
	ErrDuplicate         = errors.New("duplicate attachment identity")
	ErrNotCommitted      = errors.New("attachment is not committed")
	ErrTampered          = errors.New("attachment content is missing or tampered")
	ErrClosed            = errors.New("attachment store is closed")
	ErrUploadState       = errors.New("invalid attachment upload state")
	ErrUploadTerminal    = errors.New("attachment upload is already terminal")
	ErrUploadExpired     = errors.New("attachment upload expired")
	ErrSequence          = errors.New("attachment chunk sequence is invalid")
	ErrBase64            = errors.New("attachment chunk is not strict base64")
	ErrDigestMismatch    = errors.New("attachment digest does not match")
	ErrSizeMismatch      = errors.New("attachment size does not match")
	ErrStoreUnsafe       = errors.New("attachment store is unavailable or unsafe")
	ErrStorageIdentity   = errors.New("invalid attachment storage identity")
	ErrInvalidCapability = errors.New("invalid attachment capability limits")
	ErrInvalidManifest   = errors.New("invalid attachment manifest")
)

var (
	attachmentIDPattern = regexp.MustCompile(`^att_[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	uploadIDPattern     = regexp.MustCompile(`^upl_[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ID is a stable attachment reference. It does not encode a path.
type ID string

// UploadID correlates one bounded begin/chunk/commit/abort state machine.
type UploadID string

// Kind is the closed provider-neutral media family.
type Kind string

const (
	KindImage    Kind = "image"
	KindDocument Kind = "document"
)

// Source names a caller-controlled import mechanism.
type Source string

const (
	SourceFilePath   Source = "file_path"
	SourceStreamJSON Source = "stream_json_v1"
)

// SourceCapability gives an import source its exact user-surface scope.
type SourceCapability struct {
	Source Source `json:"source"`
	Scope  string `json:"scope"`
}

const (
	SourceScopeInitialCLI = "initial_cli"
	SourceScopePerTurn    = "per_turn"
)

// Limits is both the enforcing configuration and the advertised contract.
// MaxUploadsPerSession caps live durable manifests and, independently, the
// terminal upload-attempt ledger. Zero values select the defaults.
type Limits struct {
	MaxAttachmentsPerMessage  int           `json:"max_attachments_per_message"`
	MaxConcurrentUploads      int           `json:"max_concurrent_uploads"`
	MaxUploadsPerSession      int           `json:"max_uploads_per_session"`
	MaxItemBytes              int64         `json:"max_item_bytes"`
	MaxAggregateBytes         int64         `json:"max_aggregate_bytes"`
	MaxStorageBytes           int64         `json:"max_storage_bytes"`
	MaxModelRequestMediaBytes int64         `json:"max_model_request_media_bytes"`
	MaxChunkDecodedBytes      int           `json:"max_chunk_decoded_bytes"`
	MaxChunkEncodedBytes      int           `json:"max_chunk_encoded_bytes"`
	MaxDisplayNameBytes       int           `json:"max_display_name_bytes"`
	MaxMIMETypeBytes          int           `json:"max_mime_type_bytes"`
	MaxImageDimension         int           `json:"max_image_dimension"`
	MaxImagePixels            int64         `json:"max_image_pixels"`
	MaxPDFPages               int           `json:"max_pdf_pages"`
	UploadTimeout             time.Duration `json:"-"`
	UploadTimeoutMillis       int64         `json:"upload_timeout_ms"`
}

// DefaultLimits returns the exact first-version runtime limits.
func DefaultLimits() Limits {
	decoded := DefaultMaxChunkBytes
	return Limits{
		MaxAttachmentsPerMessage:  DefaultMaxAttachmentsPerMessage,
		MaxConcurrentUploads:      DefaultMaxConcurrentUploads,
		MaxUploadsPerSession:      DefaultMaxUploadsPerSession,
		MaxItemBytes:              DefaultMaxItemBytes,
		MaxAggregateBytes:         DefaultMaxAggregateBytes,
		MaxStorageBytes:           DefaultMaxStorageBytes,
		MaxModelRequestMediaBytes: DefaultMaxModelMediaBytes,
		MaxChunkDecodedBytes:      decoded,
		MaxChunkEncodedBytes:      ((decoded + 2) / 3) * 4,
		MaxDisplayNameBytes:       DefaultMaxDisplayNameBytes,
		MaxMIMETypeBytes:          DefaultMaxMIMETypeBytes,
		MaxImageDimension:         DefaultMaxImageDimension,
		MaxImagePixels:            DefaultMaxImagePixels,
		MaxPDFPages:               DefaultMaxPDFPages,
		UploadTimeout:             DefaultUploadTimeout,
		UploadTimeoutMillis:       DefaultUploadTimeout.Milliseconds(),
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxAttachmentsPerMessage == 0 {
		value.MaxAttachmentsPerMessage = defaults.MaxAttachmentsPerMessage
	}
	if value.MaxConcurrentUploads == 0 {
		value.MaxConcurrentUploads = defaults.MaxConcurrentUploads
	}
	if value.MaxUploadsPerSession == 0 {
		value.MaxUploadsPerSession = defaults.MaxUploadsPerSession
	}
	if value.MaxItemBytes == 0 {
		value.MaxItemBytes = defaults.MaxItemBytes
	}
	if value.MaxAggregateBytes == 0 {
		value.MaxAggregateBytes = defaults.MaxAggregateBytes
	}
	if value.MaxStorageBytes == 0 {
		value.MaxStorageBytes = defaults.MaxStorageBytes
	}
	if value.MaxModelRequestMediaBytes == 0 {
		value.MaxModelRequestMediaBytes = defaults.MaxModelRequestMediaBytes
	}
	if value.MaxChunkDecodedBytes == 0 {
		value.MaxChunkDecodedBytes = defaults.MaxChunkDecodedBytes
		if value.MaxItemBytes < int64(value.MaxChunkDecodedBytes) {
			value.MaxChunkDecodedBytes = int(value.MaxItemBytes)
		}
	}
	if value.MaxChunkEncodedBytes == 0 {
		value.MaxChunkEncodedBytes = ((value.MaxChunkDecodedBytes + 2) / 3) * 4
	}
	if value.MaxDisplayNameBytes == 0 {
		value.MaxDisplayNameBytes = defaults.MaxDisplayNameBytes
	}
	if value.MaxMIMETypeBytes == 0 {
		value.MaxMIMETypeBytes = defaults.MaxMIMETypeBytes
	}
	if value.MaxImageDimension == 0 {
		value.MaxImageDimension = defaults.MaxImageDimension
	}
	if value.MaxImagePixels == 0 {
		value.MaxImagePixels = defaults.MaxImagePixels
	}
	if value.MaxPDFPages == 0 {
		value.MaxPDFPages = defaults.MaxPDFPages
	}
	if value.UploadTimeout == 0 {
		if value.UploadTimeoutMillis > 0 {
			value.UploadTimeout = time.Duration(value.UploadTimeoutMillis) * time.Millisecond
		} else {
			value.UploadTimeout = defaults.UploadTimeout
		}
	}
	value.UploadTimeoutMillis = value.UploadTimeout.Milliseconds()

	if value.MaxAttachmentsPerMessage < 1 ||
		value.MaxAttachmentsPerMessage > DefaultMaxAttachmentsPerMessage ||
		value.MaxConcurrentUploads < 1 ||
		value.MaxConcurrentUploads > DefaultMaxConcurrentUploads ||
		value.MaxUploadsPerSession < value.MaxConcurrentUploads ||
		value.MaxUploadsPerSession > DefaultMaxUploadsPerSession ||
		value.MaxItemBytes < 1 ||
		value.MaxItemBytes > DefaultMaxItemBytes ||
		value.MaxAggregateBytes < value.MaxItemBytes ||
		value.MaxAggregateBytes > DefaultMaxAggregateBytes ||
		value.MaxStorageBytes < value.MaxItemBytes ||
		value.MaxStorageBytes > DefaultMaxStorageBytes ||
		value.MaxModelRequestMediaBytes < 1 ||
		value.MaxModelRequestMediaBytes > value.MaxAggregateBytes ||
		value.MaxModelRequestMediaBytes > DefaultMaxModelMediaBytes ||
		value.MaxChunkDecodedBytes < 1 ||
		value.MaxChunkDecodedBytes > int(value.MaxItemBytes) ||
		value.MaxChunkDecodedBytes > DefaultMaxChunkBytes ||
		value.MaxChunkEncodedBytes < ((value.MaxChunkDecodedBytes+2)/3)*4 ||
		value.MaxChunkEncodedBytes > ((DefaultMaxChunkBytes+2)/3)*4 ||
		value.MaxDisplayNameBytes < 1 ||
		value.MaxDisplayNameBytes > DefaultMaxDisplayNameBytes ||
		value.MaxMIMETypeBytes < len(MIMEPDF) ||
		value.MaxMIMETypeBytes > DefaultMaxMIMETypeBytes ||
		value.MaxImageDimension < 1 ||
		value.MaxImageDimension > DefaultMaxImageDimension ||
		value.MaxImagePixels < 1 ||
		value.MaxImagePixels > DefaultMaxImagePixels ||
		value.MaxPDFPages < 1 ||
		value.MaxPDFPages > DefaultMaxPDFPages ||
		value.UploadTimeout < time.Millisecond ||
		value.UploadTimeout > DefaultUploadTimeout {
		return Limits{}, ErrInvalidCapability
	}
	return value, nil
}

// MediaCapability describes one exact accepted MIME and its transformations.
type MediaCapability struct {
	Kind            Kind   `json:"kind"`
	MIMEType        string `json:"mime_type"`
	MaxBytes        int64  `json:"max_bytes"`
	MaxDimension    int    `json:"max_dimension,omitempty"`
	MaxPixels       int64  `json:"max_pixels,omitempty"`
	MaxPages        int    `json:"max_pages,omitempty"`
	TransformPolicy string `json:"transform_policy"`
}

// Capability is safe to advertise during runtime initialization.
type Capability struct {
	ProtocolVersion int                `json:"protocol_version"`
	Sources         []SourceCapability `json:"sources"`
	MediaTypes      []MediaCapability  `json:"media_types"`
	Limits          Limits             `json:"limits"`
}

// CapabilityFor returns a defensive copy of the exact store contract.
func CapabilityFor(limits Limits) (Capability, error) {
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return Capability{}, err
	}
	return Capability{
		ProtocolVersion: ProtocolVersion,
		Sources: []SourceCapability{
			{Source: SourceFilePath, Scope: SourceScopeInitialCLI},
			{Source: SourceStreamJSON, Scope: SourceScopePerTurn},
		},
		MediaTypes: []MediaCapability{
			{
				Kind: KindImage, MIMEType: MIMEPNG, MaxBytes: normalized.MaxItemBytes,
				MaxDimension: normalized.MaxImageDimension, MaxPixels: normalized.MaxImagePixels,
				TransformPolicy: "decode_reencode_strip_metadata_reject_oversize_no_resize",
			},
			{
				Kind: KindImage, MIMEType: MIMEJPEG, MaxBytes: normalized.MaxItemBytes,
				MaxDimension: normalized.MaxImageDimension, MaxPixels: normalized.MaxImagePixels,
				TransformPolicy: "decode_reencode_strip_metadata_reject_oversize_no_resize",
			},
			{
				Kind: KindDocument, MIMEType: MIMEPDF, MaxBytes: normalized.MaxItemBytes,
				MaxPages:        normalized.MaxPDFPages,
				TransformPolicy: "validate_structure_no_execute_no_ocr_no_conversion",
			},
		},
		Limits: normalized,
	}, nil
}

// Manifest is the complete safe, provider-neutral attachment reference.
type Manifest struct {
	AttachmentID ID     `json:"attachment_id"`
	Kind         Kind   `json:"kind"`
	Name         string `json:"name"`
	MIMEType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	StorageID    string `json:"storage_id"`
}

// Validate confirms that metadata is complete and internally consistent.
func (manifest Manifest) Validate(limits Limits) error {
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if err := ValidateAttachmentID(manifest.AttachmentID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateDisplayName(manifest.Name, normalized); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	kind, err := kindForMIME(manifest.MIMEType, normalized)
	if err != nil || kind != manifest.Kind {
		return fmt.Errorf("%w: media metadata is inconsistent", ErrInvalidManifest)
	}
	if manifest.SizeBytes <= 0 || manifest.SizeBytes > normalized.MaxItemBytes {
		return fmt.Errorf("%w: invalid decoded size", ErrInvalidManifest)
	}
	if !digestPattern.MatchString(manifest.SHA256) {
		return fmt.Errorf("%w: invalid digest", ErrInvalidManifest)
	}
	if manifest.StorageID != storageID(manifest.SHA256) {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, ErrStorageIdentity)
	}
	return nil
}

// Resolved contains provider-bound bytes and their safe manifest.
type Resolved struct {
	Manifest Manifest
	Bytes    []byte
}

// FileImport describes one explicit caller-selected source path. Path is
// consumed only for the duration of ImportFile and is never retained.
type FileImport struct {
	AttachmentID ID
	Path         string
	Name         string
	MIMEType     string
}

// BeginUpload declares all bounded state before any chunk is admitted.
type BeginUpload struct {
	UploadID     UploadID
	AttachmentID ID
	Name         string
	SizeBytes    int64
	MIMEType     string
	SHA256       string
}

// UploadStatus is a closed import lifecycle.
type UploadStatus string

const (
	UploadAccepted  UploadStatus = "accepted"
	UploadCommitted UploadStatus = "committed"
	UploadAborted   UploadStatus = "aborted"
	UploadExpired   UploadStatus = "expired"
	UploadFailed    UploadStatus = "failed"
)

// AbortReason is closed so untrusted text cannot be reflected through
// acknowledgements or diagnostics.
type AbortReason string

const (
	AbortCaller         AbortReason = "caller_abort"
	AbortCancellation   AbortReason = "cancelled"
	AbortEOF            AbortReason = "eof"
	AbortProcessFailure AbortReason = "process_failure"
	AbortShutdown       AbortReason = "shutdown"
)

// UploadAcknowledgement correlates the one terminal upload outcome.
type UploadAcknowledgement struct {
	UploadID     UploadID     `json:"upload_id"`
	AttachmentID ID           `json:"attachment_id"`
	Status       UploadStatus `json:"status"`
	Terminal     bool         `json:"terminal"`
	Manifest     *Manifest    `json:"manifest,omitempty"`
	Reason       string       `json:"reason,omitempty"`
}

// CleanupResult reports only counts and bytes, never store paths.
type CleanupResult struct {
	ManifestsRemoved int   `json:"manifests_removed"`
	BlobsRemoved     int   `json:"blobs_removed"`
	BytesRemoved     int64 `json:"bytes_removed"`
}

// Options configures one session-associated store.
type Options struct {
	Limits Limits
	Random io.Reader
	Now    func() time.Time
}

// ValidateAttachmentID validates the closed stable reference syntax.
func ValidateAttachmentID(id ID) error {
	if !attachmentIDPattern.MatchString(string(id)) {
		return ErrInvalidID
	}
	return nil
}

// ValidateUploadID validates the closed upload correlation syntax.
func ValidateUploadID(id UploadID) error {
	if !uploadIDPattern.MatchString(string(id)) {
		return ErrInvalidID
	}
	return nil
}

// NewAttachmentID creates a cryptographically random stable reference.
func NewAttachmentID() (ID, error) {
	value, err := newID("att", rand.Reader)
	return ID(value), err
}

// NewUploadID creates a cryptographically random upload correlation.
func NewUploadID() (UploadID, error) {
	value, err := newID("upl", rand.Reader)
	return UploadID(value), err
}

func newID(prefix string, random io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", fmt.Errorf("generate attachment identity: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}

func validateDisplayName(name string, limits Limits) error {
	if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) ||
		len(name) > limits.MaxDisplayNameBytes || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) {
		return ErrInvalidName
	}
	for _, value := range name {
		if unicode.IsControl(value) || unicode.Is(unicode.Bidi_Control, value) {
			return ErrInvalidName
		}
	}
	return nil
}

func kindForMIME(mimeType string, limits Limits) (Kind, error) {
	if mimeType == "" || mimeType != strings.TrimSpace(mimeType) ||
		len(mimeType) > limits.MaxMIMETypeBytes {
		return "", ErrUnsupportedMedia
	}
	switch mimeType {
	case MIMEPNG, MIMEJPEG:
		return KindImage, nil
	case MIMEPDF:
		return KindDocument, nil
	default:
		return "", ErrUnsupportedMedia
	}
}

func storageID(digest string) string {
	return "blob_sha256_" + digest
}
