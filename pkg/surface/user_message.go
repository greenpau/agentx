package surface

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/greenpau/agentx/pkg/attachment"
)

const (
	// UserContentVersionAttachments identifies the first provider-neutral
	// attachment-bearing user-content union.
	UserContentVersionAttachments = attachment.ProtocolVersion

	// MaxUserMessageAttachments is the exact first-version per-message count
	// bound. Byte bounds are additionally enforced by attachment.Manifest.
	MaxUserMessageAttachments = attachment.DefaultMaxAttachmentsPerMessage
)

// UserContentType is the closed, provider-neutral input-content discriminator.
type UserContentType string

const (
	UserContentText       UserContentType = "text"
	UserContentAttachment UserContentType = "attachment_ref"
)

// AttachmentReference is safe immutable metadata. It never contains source or
// runtime filesystem paths and cannot resolve its StorageID without the owning
// attachment.Store.
type AttachmentReference = attachment.Manifest

// UserContent contains exactly one text value or immutable attachment
// reference. Attachment metadata is flattened by MarshalJSON to match the
// versioned external union.
type UserContent struct {
	Type       UserContentType
	Text       string
	Attachment *AttachmentReference
}

// UserMessage is the typed external user message admitted by structured input.
// Role is always user after decoding. ContentVersion is zero only for legacy
// text-only forms.
type UserMessage struct {
	Role           string
	ContentVersion int
	Content        []UserContent
}

// HasAttachments reports whether the message contains any attachment
// references.
func (m UserMessage) HasAttachments() bool {
	for _, block := range m.Content {
		if block.Type == UserContentAttachment {
			return true
		}
	}
	return false
}

// Text joins ordered text blocks using the legacy DecodeUserText separator.
// Attachment positions remain represented by Content and are not converted to
// prompt placeholders.
func (m UserMessage) Text() string {
	parts := make([]string, 0, len(m.Content))
	for _, block := range m.Content {
		if block.Type == UserContentText {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// AttachmentIDs returns a new ordered slice of stable attachment identities.
func (m UserMessage) AttachmentIDs() []attachment.ID {
	ids := make([]attachment.ID, 0, len(m.Content))
	for _, block := range m.Content {
		if block.Type == UserContentAttachment && block.Attachment != nil {
			ids = append(ids, block.Attachment.AttachmentID)
		}
	}
	return ids
}

// Validate checks the complete typed union, including immutable manifest,
// duplicate identity, count, and aggregate decoded-byte bounds.
func (m UserMessage) Validate() error {
	if m.Role != "user" {
		return errors.New("user message role must be user")
	}
	if m.ContentVersion != 0 && m.ContentVersion != UserContentVersionAttachments {
		return fmt.Errorf("unsupported user content_version %d", m.ContentVersion)
	}
	if len(m.Content) == 0 {
		return errors.New("user message content is empty")
	}

	limits := attachment.DefaultLimits()
	seen := make(map[attachment.ID]struct{})
	attachmentCount := 0
	var aggregateBytes int64
	for index, block := range m.Content {
		switch block.Type {
		case UserContentText:
			if block.Attachment != nil {
				return fmt.Errorf("user content block %d text contains attachment metadata", index)
			}
			if strings.TrimSpace(block.Text) == "" {
				return fmt.Errorf("user content block %d text is empty", index)
			}
		case UserContentAttachment:
			if block.Text != "" {
				return fmt.Errorf("user content block %d attachment contains inline text", index)
			}
			if block.Attachment == nil {
				return fmt.Errorf("user content block %d attachment metadata is missing", index)
			}
			if err := block.Attachment.Validate(limits); err != nil {
				return fmt.Errorf("user content block %d: %w", index, err)
			}
			if _, duplicate := seen[block.Attachment.AttachmentID]; duplicate {
				return fmt.Errorf("user content block %d duplicates attachment_id %q", index, block.Attachment.AttachmentID)
			}
			seen[block.Attachment.AttachmentID] = struct{}{}
			attachmentCount++
			if attachmentCount > limits.MaxAttachmentsPerMessage {
				return fmt.Errorf("user message exceeds %d attachments", limits.MaxAttachmentsPerMessage)
			}
			if block.Attachment.SizeBytes > limits.MaxAggregateBytes-aggregateBytes ||
				block.Attachment.SizeBytes > limits.MaxModelRequestMediaBytes-aggregateBytes {
				return errors.New("user message attachment bytes exceed aggregate limit")
			}
			aggregateBytes += block.Attachment.SizeBytes
		default:
			return errors.New("unsupported user content block type")
		}
	}
	if attachmentCount > 0 && m.ContentVersion != UserContentVersionAttachments {
		return fmt.Errorf("attachment_ref requires content_version %d", UserContentVersionAttachments)
	}
	return nil
}

// MarshalJSON emits only the closed external user-message union.
func (m UserMessage) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type wireMessage struct {
		Role           string        `json:"role"`
		ContentVersion int           `json:"content_version,omitempty"`
		Content        []UserContent `json:"content"`
	}
	return json.Marshal(wireMessage{
		Role: m.Role, ContentVersion: m.ContentVersion, Content: m.Content,
	})
}

// MarshalJSON emits one closed content-block variant. Attachment manifest
// fields deliberately remain flat and contain no bytes or paths.
func (c UserContent) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case UserContentText:
		if c.Attachment != nil || strings.TrimSpace(c.Text) == "" {
			return nil, errors.New("invalid text content block")
		}
		return json.Marshal(struct {
			Type UserContentType `json:"type"`
			Text string          `json:"text"`
		}{Type: UserContentText, Text: c.Text})
	case UserContentAttachment:
		if c.Attachment == nil || c.Text != "" {
			return nil, errors.New("invalid attachment content block")
		}
		if err := c.Attachment.Validate(attachment.DefaultLimits()); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type UserContentType `json:"type"`
			attachment.Manifest
		}{Type: UserContentAttachment, Manifest: *c.Attachment})
	default:
		return nil, errors.New("unsupported user content block type")
	}
}

// DecodeUserMessage accepts legacy strings, legacy API user-message content
// strings and text/input_text arrays, plus the version-1 attachment union.
// Message and block objects are closed and duplicate members are always
// rejected before map/struct decoding.
func DecodeUserMessage(raw json.RawMessage) (UserMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return UserMessage{}, errors.New("user record is missing message")
	}
	if err := rejectDuplicateJSONMembers(trimmed); err != nil {
		return UserMessage{}, err
	}

	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return UserMessage{}, errors.New("user message must be a valid string")
		}
		message := UserMessage{
			Role: "user",
			Content: []UserContent{{
				Type: UserContentText,
				Text: text,
			}},
		}
		if err := message.Validate(); err != nil {
			return UserMessage{}, err
		}
		return message, nil
	}
	if trimmed[0] != '{' {
		return UserMessage{}, errors.New("user message must be a string or API message object")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return UserMessage{}, errors.New("user message must be an API message object")
	}
	if err := rejectUnknownFieldsFixed(fields, "role", "content", "content_version"); err != nil {
		return UserMessage{}, fmt.Errorf("user message: %w", err)
	}

	role := "user"
	if rawRole, present := fields["role"]; present {
		var decodedRole string
		if err := json.Unmarshal(rawRole, &decodedRole); err != nil || decodedRole == "" {
			return UserMessage{}, errors.New("user message role must be a string")
		}
		if decodedRole != "user" {
			return UserMessage{}, errors.New("user message role must be user")
		}
		role = decodedRole
	}
	rawContent, present := fields["content"]
	if !present {
		return UserMessage{}, errors.New("user API message is missing content")
	}

	contentVersion := 0
	if rawVersion, versionPresent := fields["content_version"]; versionPresent {
		if err := json.Unmarshal(rawVersion, &contentVersion); err != nil {
			return UserMessage{}, errors.New("user content_version must be an integer")
		}
		if contentVersion != UserContentVersionAttachments {
			return UserMessage{}, fmt.Errorf("unsupported user content_version %d", contentVersion)
		}
	}

	var legacyContent string
	if err := json.Unmarshal(rawContent, &legacyContent); err == nil {
		if contentVersion != 0 {
			return UserMessage{}, errors.New("versioned user content must be a block array")
		}
		message := UserMessage{
			Role: "user",
			Content: []UserContent{{
				Type: UserContentText,
				Text: legacyContent,
			}},
		}
		if err := message.Validate(); err != nil {
			return UserMessage{}, err
		}
		return message, nil
	}

	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(rawContent, &rawBlocks); err != nil || len(rawBlocks) == 0 {
		return UserMessage{}, errors.New("user API message content must be text or a nonempty content-block array")
	}
	blocks := make([]UserContent, 0, len(rawBlocks))
	for index, rawBlock := range rawBlocks {
		block, err := decodeUserContent(rawBlock)
		if err != nil {
			return UserMessage{}, fmt.Errorf("user content block %d: %w", index, err)
		}
		blocks = append(blocks, block)
	}
	message := UserMessage{
		Role: role, ContentVersion: contentVersion, Content: blocks,
	}
	if err := message.Validate(); err != nil {
		return UserMessage{}, err
	}
	return message, nil
}

func decodeUserContent(raw json.RawMessage) (UserContent, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return UserContent{}, errors.New("content block must be an object")
	}
	var blockType string
	if rawType, present := fields["type"]; !present || json.Unmarshal(rawType, &blockType) != nil || blockType == "" {
		return UserContent{}, errors.New("content block type is required")
	}
	switch blockType {
	case "text", "input_text":
		if err := rejectUnknownFieldsFixed(fields, "type", "text"); err != nil {
			return UserContent{}, err
		}
		var text string
		rawText, present := fields["text"]
		if !present || json.Unmarshal(rawText, &text) != nil {
			return UserContent{}, errors.New("text content block requires string text")
		}
		return UserContent{Type: UserContentText, Text: text}, nil
	case string(UserContentAttachment):
		if err := rejectUnknownFieldsFixed(
			fields,
			"type", "attachment_id", "kind", "name", "mime_type",
			"size_bytes", "sha256", "storage_id",
		); err != nil {
			return UserContent{}, err
		}
		for _, required := range []string{
			"attachment_id", "kind", "name", "mime_type",
			"size_bytes", "sha256", "storage_id",
		} {
			if _, present := fields[required]; !present {
				return UserContent{}, fmt.Errorf("attachment_ref requires field %q", required)
			}
		}
		var wire struct {
			AttachmentID attachment.ID   `json:"attachment_id"`
			Kind         attachment.Kind `json:"kind"`
			Name         string          `json:"name"`
			MIMEType     string          `json:"mime_type"`
			SizeBytes    int64           `json:"size_bytes"`
			SHA256       string          `json:"sha256"`
			StorageID    string          `json:"storage_id"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return UserContent{}, errors.New("attachment_ref fields are malformed")
		}
		manifest := attachment.Manifest{
			AttachmentID: wire.AttachmentID,
			Kind:         wire.Kind,
			Name:         wire.Name,
			MIMEType:     wire.MIMEType,
			SizeBytes:    wire.SizeBytes,
			SHA256:       wire.SHA256,
			StorageID:    wire.StorageID,
		}
		if err := manifest.Validate(attachment.DefaultLimits()); err != nil {
			return UserContent{}, err
		}
		return UserContent{
			Type: UserContentAttachment, Attachment: &manifest,
		}, nil
	default:
		return UserContent{}, errors.New("unsupported user content block type")
	}
}

func rejectUnknownFieldsFixed(fields map[string]json.RawMessage, allowed ...string) error {
	accepted := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		accepted[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := accepted[field]; !ok {
			return errors.New("contains an unknown field")
		}
	}
	return nil
}
