package surface

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/greenpau/agentx/pkg/attachment"
)

const (
	// AttachmentImportProtocolVersion is the sole stream-json attachment import
	// version supported by this runtime.
	AttachmentImportProtocolVersion = attachment.ProtocolVersion
)

// AttachmentImportOperation is the closed upload state-machine discriminator.
type AttachmentImportOperation string

const (
	AttachmentImportBegin  AttachmentImportOperation = "begin"
	AttachmentImportChunk  AttachmentImportOperation = "chunk"
	AttachmentImportCommit AttachmentImportOperation = "commit"
	AttachmentImportAbort  AttachmentImportOperation = "abort"
)

// AttachmentImport is one strictly decoded stream-json import operation.
// Chunk Data remains encoded; only the owning attachment.Store may retain or
// commit decoded bytes.
type AttachmentImport struct {
	Version      int                       `json:"version"`
	Operation    AttachmentImportOperation `json:"operation"`
	PromptUUID   string                    `json:"prompt_uuid,omitempty"`
	UploadID     attachment.UploadID       `json:"upload_id"`
	AttachmentID attachment.ID             `json:"attachment_id,omitempty"`
	Name         string                    `json:"name,omitempty"`
	SizeBytes    int64                     `json:"size_bytes,omitempty"`
	MIMEType     string                    `json:"mime_type,omitempty"`
	SHA256       string                    `json:"sha256,omitempty"`
	Sequence     int                       `json:"sequence,omitempty"`
	Data         string                    `json:"data,omitempty"`
}

// BeginUpload projects the begin operation into the attachment-store request.
func (r AttachmentImport) BeginUpload() (attachment.BeginUpload, bool) {
	if r.Operation != AttachmentImportBegin {
		return attachment.BeginUpload{}, false
	}
	return attachment.BeginUpload{
		UploadID: r.UploadID, AttachmentID: r.AttachmentID,
		Name: r.Name, SizeBytes: r.SizeBytes, MIMEType: r.MIMEType,
		SHA256: r.SHA256,
	}, true
}

// ValidateUUID checks the canonical RFC 4122 textual UUID shape used by the
// versioned attachment protocol. Legacy text-only SDK prompt identifiers retain
// their existing compatibility validation elsewhere.
func ValidateUUID(value string) error {
	if len(value) != 36 ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return errors.New("UUID must use canonical RFC 4122 text form")
	}
	for index := 0; index < len(value); index++ {
		switch index {
		case 8, 13, 18, 23:
			continue
		}
		if !isHexCharacter(value[index]) {
			return errors.New("UUID must use canonical RFC 4122 text form")
		}
	}
	version := value[14]
	if version < '1' || version > '5' {
		return errors.New("UUID has an unsupported RFC 4122 version")
	}
	switch value[19] {
	case '8', '9', 'a', 'b', 'A', 'B':
	default:
		return errors.New("UUID has an invalid RFC 4122 variant")
	}
	return nil
}

func isHexCharacter(value byte) bool {
	return value >= '0' && value <= '9' ||
		value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}

// DecodeAttachmentImport strictly decodes one complete versioned import
// envelope. Base64 is decoded only into bounded scratch space to validate its
// strict syntax; the returned record retains only the original encoded chunk.
func DecodeAttachmentImport(raw json.RawMessage) (AttachmentImport, error) {
	if len(raw) > MaxNDJSONRecordBytes {
		return AttachmentImport{}, errors.New("attachment import exceeds the NDJSON record size limit")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return AttachmentImport{}, errors.New("attachment import must be a JSON object")
	}
	if err := rejectDuplicateJSONMembers(trimmed); err != nil {
		return AttachmentImport{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return AttachmentImport{}, errors.New("attachment import must be a JSON object")
	}
	var typeName string
	if rawType, present := fields["type"]; !present ||
		json.Unmarshal(rawType, &typeName) != nil ||
		typeName != "attachment_import" {
		return AttachmentImport{}, errors.New("attachment import type must be attachment_import")
	}
	var operation AttachmentImportOperation
	if rawOperation, present := fields["operation"]; !present ||
		json.Unmarshal(rawOperation, &operation) != nil {
		return AttachmentImport{}, errors.New("attachment import operation is required")
	}
	allowed, required, err := attachmentImportFields(operation)
	if err != nil {
		return AttachmentImport{}, err
	}
	if err := rejectUnknownFieldsFixed(fields, allowed...); err != nil {
		return AttachmentImport{}, fmt.Errorf("attachment import %s: %w", operation, err)
	}
	for _, name := range required {
		if _, present := fields[name]; !present {
			return AttachmentImport{}, fmt.Errorf("attachment import %s requires field %q", operation, name)
		}
	}

	var record AttachmentImport
	if err := json.Unmarshal(trimmed, &record); err != nil {
		return AttachmentImport{}, errors.New("attachment import fields are malformed")
	}
	if record.Version != AttachmentImportProtocolVersion {
		return AttachmentImport{}, fmt.Errorf("unsupported attachment import version %d", record.Version)
	}
	record.Operation = operation
	if err := attachment.ValidateUploadID(record.UploadID); err != nil {
		return AttachmentImport{}, err
	}

	limits := attachment.DefaultLimits()
	switch operation {
	case AttachmentImportBegin:
		if err := ValidateUUID(record.PromptUUID); err != nil {
			return AttachmentImport{}, fmt.Errorf("attachment import prompt_uuid: %w", err)
		}
		if err := attachment.ValidateAttachmentID(record.AttachmentID); err != nil {
			return AttachmentImport{}, err
		}
		kind, err := attachmentKindForMIME(record.MIMEType)
		if err != nil {
			return AttachmentImport{}, err
		}
		// Manifest validation applies the same name, MIME, decoded-size, digest,
		// and storage-identity bounds as the eventual committed metadata.
		claim := attachment.Manifest{
			AttachmentID: record.AttachmentID,
			Kind:         kind,
			Name:         record.Name,
			MIMEType:     record.MIMEType,
			SizeBytes:    record.SizeBytes,
			SHA256:       record.SHA256,
			StorageID:    "blob_sha256_" + record.SHA256,
		}
		if err := claim.Validate(limits); err != nil {
			return AttachmentImport{}, err
		}
	case AttachmentImportChunk:
		if bytes.Equal(bytes.TrimSpace(fields["sequence"]), []byte("null")) {
			return AttachmentImport{}, errors.New("attachment chunk sequence must be an integer")
		}
		if record.Sequence < 0 {
			return AttachmentImport{}, errors.New("attachment chunk sequence must be nonnegative")
		}
		if err := validateStrictBase64Chunk(record.Data, limits); err != nil {
			return AttachmentImport{}, err
		}
	case AttachmentImportCommit, AttachmentImportAbort:
		// Exact field selection and the upload-ID check above fully validate
		// terminal operations; lifecycle ownership belongs to the store.
	}
	return record, nil
}

func attachmentImportFields(operation AttachmentImportOperation) (allowed, required []string, err error) {
	common := []string{"type", "version", "operation", "upload_id"}
	switch operation {
	case AttachmentImportBegin:
		allowed = append(append([]string(nil), common...),
			"prompt_uuid", "attachment_id", "name", "size_bytes", "mime_type", "sha256",
		)
		required = append([]string(nil), allowed...)
	case AttachmentImportChunk:
		allowed = append(append([]string(nil), common...), "sequence", "data")
		required = append([]string(nil), allowed...)
	case AttachmentImportCommit, AttachmentImportAbort:
		allowed = common
		required = append([]string(nil), common...)
	default:
		return nil, nil, errors.New("unsupported attachment import operation")
	}
	return allowed, required, nil
}

func attachmentKindForMIME(mimeType string) (attachment.Kind, error) {
	switch mimeType {
	case attachment.MIMEPNG, attachment.MIMEJPEG:
		return attachment.KindImage, nil
	case attachment.MIMEPDF:
		return attachment.KindDocument, nil
	default:
		return "", attachment.ErrUnsupportedMedia
	}
}

func validateStrictBase64Chunk(value string, limits attachment.Limits) error {
	decoded, err := attachment.DecodeStrictBase64Chunk(value, limits)
	clear(decoded)
	return err
}
