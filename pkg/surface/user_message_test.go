package surface

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/attachment"
)

func TestDecodeUserMessageRetainsLegacyTextCompatibility(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "bare string", raw: `"hello"`, want: "hello"},
		{name: "content string", raw: `{"role":"user","content":"hello"}`, want: "hello"},
		{
			name: "ordered aliases",
			raw:  `{"role":"user","content":[{"type":"text","text":"first"},{"type":"input_text","text":"second"}]}`,
			want: "first\nsecond",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := DecodeUserMessage(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if message.Role != "user" || message.ContentVersion != 0 ||
				message.HasAttachments() || message.Text() != test.want {
				t.Fatalf("message = %#v, text = %q", message, message.Text())
			}
			legacy, err := DecodeUserText(json.RawMessage(test.raw))
			if err != nil || legacy != test.want {
				t.Fatalf("DecodeUserText = %q, %v", legacy, err)
			}
		})
	}
}

func TestDecodeUserMessagePreservesOrderedTextAndAttachments(t *testing.T) {
	raw := fmt.Sprintf(
		`{"role":"user","content_version":1,"content":[`+
			`{"type":"input_text","text":"compare"},`+
			`%s,`+
			`{"type":"text","text":"with"},`+
			`%s,`+
			`%s]}`,
		attachmentBlockJSON("att_png", attachment.KindImage, "screen.png", attachment.MIMEPNG, 123, strings.Repeat("a", 64)),
		attachmentBlockJSON("att_jpeg", attachment.KindImage, "photo.jpg", attachment.MIMEJPEG, 456, strings.Repeat("b", 64)),
		attachmentBlockJSON("att_pdf", attachment.KindDocument, "report.pdf", attachment.MIMEPDF, 789, strings.Repeat("c", 64)),
	)
	message, err := DecodeUserMessage(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if message.ContentVersion != UserContentVersionAttachments || len(message.Content) != 5 {
		t.Fatalf("message = %#v", message)
	}
	wantTypes := []UserContentType{
		UserContentText, UserContentAttachment, UserContentText,
		UserContentAttachment, UserContentAttachment,
	}
	for index, want := range wantTypes {
		if message.Content[index].Type != want {
			t.Fatalf("content %d type = %q, want %q", index, message.Content[index].Type, want)
		}
	}
	wantIDs := []attachment.ID{"att_png", "att_jpeg", "att_pdf"}
	gotIDs := message.AttachmentIDs()
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("attachment IDs = %#v", gotIDs)
	}
	for index := range wantIDs {
		if gotIDs[index] != wantIDs[index] {
			t.Fatalf("attachment ID %d = %q, want %q", index, gotIDs[index], wantIDs[index])
		}
	}
	if message.Text() != "compare\nwith" {
		t.Fatalf("text projection = %q", message.Text())
	}
	if _, err := DecodeUserText(json.RawMessage(raw)); err == nil {
		t.Fatal("legacy text decoder silently discarded attachments")
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"content_version":1`,
		`"type":"attachment_ref"`,
		`"attachment_id":"att_png"`,
		`"storage_id":"blob_sha256_`,
	} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("canonical message missing %s: %s", expected, encoded)
		}
	}
	for _, forbidden := range []string{`"attachment":`, `"bytes":`, `"path":`, `"data":`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("canonical message exposed forbidden member %s: %s", forbidden, encoded)
		}
	}
}

func TestDecodeUserMessageAllowsAttachmentOnlyTurn(t *testing.T) {
	raw := fmt.Sprintf(
		`{"role":"user","content_version":1,"content":[%s]}`,
		attachmentBlockJSON("att_only", attachment.KindImage, "only.png", attachment.MIMEPNG, 64, strings.Repeat("d", 64)),
	)
	message, err := DecodeUserMessage(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !message.HasAttachments() || message.Text() != "" || len(message.Content) != 1 {
		t.Fatalf("attachment-only message = %#v", message)
	}
}

func TestDecodeUserMessageUsesClosedUnambiguousSchemas(t *testing.T) {
	validAttachment := attachmentBlockJSON(
		"att_valid", attachment.KindImage, "valid.png", attachment.MIMEPNG,
		1, strings.Repeat("e", 64),
	)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "assistant role", raw: `{"role":"assistant","content":"no"}`},
		{name: "empty bare string", raw: `""`},
		{name: "empty text block", raw: `{"role":"user","content":[{"type":"text","text":"  "}]}`},
		{name: "unknown message member", raw: `{"role":"user","content":"hi","future":true}`},
		{name: "unknown text member", raw: `{"role":"user","content":[{"type":"text","text":"hi","future":true}]}`},
		{name: "duplicate message member", raw: `{"role":"user","content":"first","content":"second"}`},
		{name: "duplicate block member", raw: `{"role":"user","content":[{"type":"text","text":"first","text":"second"}]}`},
		{name: "unknown block", raw: `{"role":"user","content":[{"type":"input_image","image_url":"data:"}]}`},
		{
			name: "attachment missing version",
			raw:  fmt.Sprintf(`{"role":"user","content":[%s]}`, validAttachment),
		},
		{
			name: "unsupported version",
			raw:  `{"role":"user","content_version":2,"content":[{"type":"text","text":"hi"}]}`,
		},
		{
			name: "versioned string content",
			raw:  `{"role":"user","content_version":1,"content":"hi"}`,
		},
		{
			name: "unknown attachment member",
			raw: strings.Replace(
				fmt.Sprintf(`{"role":"user","content_version":1,"content":[%s]}`, validAttachment),
				`"storage_id":`, `"future":true,"storage_id":`, 1,
			),
		},
		{
			name: "missing attachment member",
			raw: strings.Replace(
				fmt.Sprintf(`{"role":"user","content_version":1,"content":[%s]}`, validAttachment),
				`,"storage_id":"blob_sha256_`+strings.Repeat("e", 64)+`"`, "", 1,
			),
		},
		{
			name: "MIME kind mismatch",
			raw: fmt.Sprintf(
				`{"role":"user","content_version":1,"content":[%s]}`,
				attachmentBlockJSON("att_bad_kind", attachment.KindDocument, "bad.png", attachment.MIMEPNG, 1, strings.Repeat("f", 64)),
			),
		},
		{
			name: "storage digest mismatch",
			raw: strings.Replace(
				fmt.Sprintf(`{"role":"user","content_version":1,"content":[%s]}`, validAttachment),
				`blob_sha256_`+strings.Repeat("e", 64),
				`blob_sha256_`+strings.Repeat("f", 64),
				1,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeUserMessage(json.RawMessage(test.raw)); err == nil {
				t.Fatalf("invalid user message was accepted: %s", test.raw)
			}
		})
	}
}

func TestUserMessageAttachmentCountDuplicateAndAggregateBounds(t *testing.T) {
	limits := attachment.DefaultLimits()
	content := make([]UserContent, 0, limits.MaxAttachmentsPerMessage)
	for index := 0; index < limits.MaxAttachmentsPerMessage; index++ {
		digest := fmt.Sprintf("%064x", index+1)
		manifest := validAttachmentManifest(
			attachment.ID(fmt.Sprintf("att_%d", index)),
			attachment.KindImage,
			fmt.Sprintf("%d.png", index),
			attachment.MIMEPNG,
			1,
			digest,
		)
		content = append(content, UserContent{
			Type: UserContentAttachment, Attachment: &manifest,
		})
	}
	exact := UserMessage{
		Role: "user", ContentVersion: UserContentVersionAttachments,
		Content: content,
	}
	if err := exact.Validate(); err != nil {
		t.Fatalf("exact attachment count: %v", err)
	}

	overflow := exact
	extra := validAttachmentManifest(
		"att_extra", attachment.KindImage, "extra.png", attachment.MIMEPNG,
		1, strings.Repeat("f", 64),
	)
	overflow.Content = append(append([]UserContent(nil), exact.Content...), UserContent{
		Type: UserContentAttachment, Attachment: &extra,
	})
	if err := overflow.Validate(); err == nil {
		t.Fatal("attachment count overflow was accepted")
	}

	duplicate := exact
	duplicate.Content = append([]UserContent(nil), exact.Content...)
	duplicate.Content[1].Attachment = duplicate.Content[0].Attachment
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate attachment ID was accepted")
	}

	aggregate := UserMessage{
		Role: "user", ContentVersion: UserContentVersionAttachments,
	}
	for index := 0; index < 3; index++ {
		digest := fmt.Sprintf("%064x", index+100)
		manifest := validAttachmentManifest(
			attachment.ID(fmt.Sprintf("att_large_%d", index)),
			attachment.KindImage,
			fmt.Sprintf("large-%d.png", index),
			attachment.MIMEPNG,
			limits.MaxItemBytes,
			digest,
		)
		aggregate.Content = append(aggregate.Content, UserContent{
			Type: UserContentAttachment, Attachment: &manifest,
		})
	}
	if err := aggregate.Validate(); err == nil {
		t.Fatal("aggregate attachment byte overflow was accepted")
	}
}

func attachmentBlockJSON(
	id string,
	kind attachment.Kind,
	name, mimeType string,
	size int64,
	digest string,
) string {
	manifest := validAttachmentManifest(
		attachment.ID(id), kind, name, mimeType, size, digest,
	)
	data, err := json.Marshal(struct {
		Type string `json:"type"`
		attachment.Manifest
	}{Type: string(UserContentAttachment), Manifest: manifest})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func validAttachmentManifest(
	id attachment.ID,
	kind attachment.Kind,
	name, mimeType string,
	size int64,
	digest string,
) attachment.Manifest {
	return attachment.Manifest{
		AttachmentID: id,
		Kind:         kind,
		Name:         name,
		MIMEType:     mimeType,
		SizeBytes:    size,
		SHA256:       digest,
		StorageID:    "blob_sha256_" + digest,
	}
}
