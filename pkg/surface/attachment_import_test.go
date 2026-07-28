package surface

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/attachment"
)

const validAttachmentPromptUUID = "123e4567-e89b-42d3-a456-426614174000"

func TestDecodeAttachmentImportAcceptsClosedLifecycle(t *testing.T) {
	begin := validAttachmentBeginJSON()
	chunk := `{"type":"attachment_import","version":1,"operation":"chunk","upload_id":"upl_valid","sequence":0,"data":"aGVsbG8="}`
	commit := `{"type":"attachment_import","version":1,"operation":"commit","upload_id":"upl_valid"}`
	abort := `{"type":"attachment_import","version":1,"operation":"abort","upload_id":"upl_valid"}`

	for _, test := range []struct {
		name      string
		raw       string
		operation AttachmentImportOperation
	}{
		{name: "begin", raw: begin, operation: AttachmentImportBegin},
		{name: "chunk", raw: chunk, operation: AttachmentImportChunk},
		{name: "commit", raw: commit, operation: AttachmentImportCommit},
		{name: "abort", raw: abort, operation: AttachmentImportAbort},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, err := DecodeAttachmentImport(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if record.Version != AttachmentImportProtocolVersion ||
				record.Operation != test.operation ||
				record.UploadID != "upl_valid" {
				t.Fatalf("record = %#v", record)
			}
			if test.operation == AttachmentImportBegin {
				request, ok := record.BeginUpload()
				if !ok || request.AttachmentID != "att_valid" ||
					request.MIMEType != attachment.MIMEPNG {
					t.Fatalf("begin request = %#v, %v", request, ok)
				}
			} else if _, ok := record.BeginUpload(); ok {
				t.Fatalf("%s projected as begin", test.operation)
			}

			envelope, err := NewDecoder(strings.NewReader(test.raw), io.Discard).Next()
			if err != nil {
				t.Fatal(err)
			}
			if envelope.Type != "attachment_import" ||
				envelope.AttachmentImport == nil ||
				envelope.AttachmentImport.Operation != test.operation ||
				envelope.OriginalByteSize() != len(test.raw) {
				t.Fatalf("envelope = %#v", envelope)
			}
		})
	}
}

func TestDecodeAttachmentImportRejectsUnknownMissingAndDuplicateFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unknown operation",
			raw:  `{"type":"attachment_import","version":1,"operation":"future","upload_id":"upl_valid"}`,
		},
		{
			name: "wrong version",
			raw:  strings.Replace(validAttachmentBeginJSON(), `"version":1`, `"version":2`, 1),
		},
		{
			name: "missing version",
			raw:  strings.Replace(validAttachmentBeginJSON(), `"version":1,`, "", 1),
		},
		{
			name: "missing begin field",
			raw:  strings.Replace(validAttachmentBeginJSON(), `"prompt_uuid":"`+validAttachmentPromptUUID+`",`, "", 1),
		},
		{
			name: "begin with chunk field",
			raw:  strings.Replace(validAttachmentBeginJSON(), `"sha256":`, `"sequence":0,"sha256":`, 1),
		},
		{
			name: "chunk with begin field",
			raw:  `{"type":"attachment_import","version":1,"operation":"chunk","upload_id":"upl_valid","sequence":0,"data":"YQ==","name":"bad.png"}`,
		},
		{
			name: "commit with data",
			raw:  `{"type":"attachment_import","version":1,"operation":"commit","upload_id":"upl_valid","data":"YQ=="}`,
		},
		{
			name: "abort missing upload",
			raw:  `{"type":"attachment_import","version":1,"operation":"abort"}`,
		},
		{
			name: "duplicate outer member",
			raw:  `{"type":"attachment_import","version":1,"operation":"commit","upload_id":"upl_first","upload_id":"upl_second"}`,
		},
		{
			name: "invalid upload ID",
			raw:  `{"type":"attachment_import","version":1,"operation":"commit","upload_id":"invalid"}`,
		},
		{
			name: "negative chunk sequence",
			raw:  `{"type":"attachment_import","version":1,"operation":"chunk","upload_id":"upl_valid","sequence":-1,"data":"YQ=="}`,
		},
		{
			name: "null chunk sequence",
			raw:  `{"type":"attachment_import","version":1,"operation":"chunk","upload_id":"upl_valid","sequence":null,"data":"YQ=="}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeAttachmentImport(json.RawMessage(test.raw)); err == nil {
				t.Fatalf("invalid attachment import was accepted: %s", test.raw)
			}
			if _, err := NewDecoder(strings.NewReader(test.raw), io.Discard).Next(); err == nil {
				t.Fatalf("decoder accepted invalid attachment import: %s", test.raw)
			}
		})
	}
}

func TestDecodeAttachmentImportEnforcesPhysicalRecordCeiling(t *testing.T) {
	raw := append([]byte(validAttachmentBeginJSON()), bytes.Repeat([]byte(" "), MaxNDJSONRecordBytes)...)
	if _, err := DecodeAttachmentImport(raw); err == nil {
		t.Fatal("oversized attachment import record was accepted")
	}
}

func TestAttachmentImportBeginValidatesIdentifiersAndClaims(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "prompt UUID shape", from: validAttachmentPromptUUID, to: "not-a-uuid"},
		{name: "prompt UUID version", from: validAttachmentPromptUUID, to: "123e4567-e89b-02d3-a456-426614174000"},
		{name: "prompt UUID variant", from: validAttachmentPromptUUID, to: "123e4567-e89b-42d3-7456-426614174000"},
		{name: "attachment ID", from: "att_valid", to: "bad"},
		{name: "display name", from: "screen.png", to: "../screen.png"},
		{name: "MIME", from: attachment.MIMEPNG, to: "image/svg+xml"},
		{name: "size", from: `"size_bytes":123`, to: `"size_bytes":0`},
		{name: "digest", from: strings.Repeat("a", 64), to: strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(validAttachmentBeginJSON(), test.from, test.to, 1)
			if _, err := DecodeAttachmentImport(json.RawMessage(raw)); err == nil {
				t.Fatalf("invalid begin claim was accepted: %s", raw)
			}
		})
	}
}

func TestAttachmentImportChunkUsesStrictBoundedBase64(t *testing.T) {
	for _, value := range []string{
		"",
		"not*base64",
		"YQ=",
		"YR==",
		"YQ==\n",
		"YQ==\r\n",
	} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			raw := attachmentChunkJSON(0, value)
			if _, err := DecodeAttachmentImport(json.RawMessage(raw)); err == nil {
				t.Fatalf("invalid strict base64 was accepted: %q", value)
			}
		})
	}

	limits := attachment.DefaultLimits()
	exactData := strings.Repeat("x", limits.MaxChunkDecodedBytes)
	exact := base64.StdEncoding.EncodeToString([]byte(exactData))
	record, err := DecodeAttachmentImport(json.RawMessage(attachmentChunkJSON(7, exact)))
	if err != nil {
		t.Fatalf("exact decoded chunk boundary: %v", err)
	}
	if record.Sequence != 7 || record.Data != exact {
		t.Fatalf("exact chunk record = %#v", record)
	}

	oversizedData := exactData + "x"
	oversized := base64.StdEncoding.EncodeToString([]byte(oversizedData))
	if _, err := DecodeAttachmentImport(json.RawMessage(attachmentChunkJSON(8, oversized))); err == nil {
		t.Fatal("one-byte decoded chunk overflow was accepted")
	}
}

func TestValidateUUIDMatchesGeneratedSDKUUID(t *testing.T) {
	for index := 0; index < 32; index++ {
		value, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateUUID(value); err != nil {
			t.Fatalf("generated UUID %q: %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"123e4567e89b42d3a456426614174000",
		"123e4567-e89b-42d3-a456-42661417400g",
		"123e4567-e89b-92d3-a456-426614174000",
		"123e4567-e89b-42d3-c456-426614174000",
	} {
		if err := ValidateUUID(value); err == nil {
			t.Fatalf("invalid UUID was accepted: %q", value)
		}
	}
}

func validAttachmentBeginJSON() string {
	return fmt.Sprintf(
		`{"type":"attachment_import","version":1,"operation":"begin",`+
			`"prompt_uuid":%q,"upload_id":"upl_valid","attachment_id":"att_valid",`+
			`"name":"screen.png","size_bytes":123,"mime_type":"image/png","sha256":%q}`,
		validAttachmentPromptUUID,
		strings.Repeat("a", 64),
	)
}

func attachmentChunkJSON(sequence int, data string) string {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(
		`{"type":"attachment_import","version":1,"operation":"chunk",`+
			`"upload_id":"upl_valid","sequence":%d,"data":%s}`,
		sequence,
		encoded,
	)
}
