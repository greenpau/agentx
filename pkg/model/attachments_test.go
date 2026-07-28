package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/config"
)

func TestRequestValidateAttachmentContentUnion(t *testing.T) {
	imageManifest := modelTestManifest(
		"att_validate_image", "screen.png", attachment.KindImage,
		attachment.MIMEPNG, 128, strings.Repeat("a", 64),
	)
	fileManifest := modelTestManifest(
		"att_validate_file", "report.pdf", attachment.KindDocument,
		attachment.MIMEPDF, 256, strings.Repeat("b", 64),
	)
	valid := Request{Input: []Item{{
		Type: ItemMessage,
		Role: RoleUser,
		Content: []Content{
			{Type: ContentInputText, Text: "Inspect these."},
			modelTestContent(ContentInputImage, imageManifest),
			modelTestContent(ContentInputFile, fileManifest),
		},
	}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("text plus ordered media did not validate: %v", err)
	}
	attachmentOnly := Request{Input: []Item{{
		Type:    ItemMessage,
		Role:    RoleUser,
		Content: []Content{modelTestContent(ContentInputImage, imageManifest)},
	}}}
	if err := attachmentOnly.Validate(); err != nil {
		t.Fatalf("attachment-only request did not validate: %v", err)
	}
	attachmentOnly.AttachmentSource = &staticAttachmentSource{}
	encoded, err := json.Marshal(attachmentOnly)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("AttachmentSource")) ||
		bytes.Contains(encoded, []byte("staticAttachmentSource")) {
		t.Fatalf("ephemeral attachment source entered request JSON: %s", encoded)
	}

	tests := []struct {
		name    string
		request Request
	}{
		{
			name: "assistant image",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleAssistant,
				Content: []Content{modelTestContent(ContentInputImage, imageManifest)},
			}}},
		},
		{
			name: "system file",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleSystem,
				Content: []Content{modelTestContent(ContentInputFile, fileManifest)},
			}}},
		},
		{
			name: "missing manifest",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{{Type: ContentInputImage}},
			}}},
		},
		{
			name: "inline media text",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{{
					Type: ContentInputImage, Text: "not a path",
					Manifest: modelTestManifestPointer(imageManifest),
				}},
			}}},
		},
		{
			name: "text with manifest",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{{
					Type: ContentInputText, Text: "bad",
					Manifest: modelTestManifestPointer(imageManifest),
				}},
			}}},
		},
		{
			name: "image with PDF metadata",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{modelTestContent(ContentInputImage, fileManifest)},
			}}},
		},
		{
			name: "file with image metadata",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{modelTestContent(ContentInputFile, imageManifest)},
			}}},
		},
		{
			name: "duplicate attachment id",
			request: Request{Input: []Item{{
				Type: ItemMessage, Role: RoleUser,
				Content: []Content{
					modelTestContent(ContentInputImage, imageManifest),
					modelTestContent(ContentInputImage, imageManifest),
				},
			}}},
		},
		{
			name: "reasoning summary manifest",
			request: Request{Input: []Item{{
				Type: ItemReasoning, ID: "rs_media",
				Summary: []Content{{
					Type: ContentSummaryText, Text: "summary",
					Manifest: modelTestManifestPointer(imageManifest),
				}},
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("Validate() error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestRequestValidateAttachmentCountBoundary(t *testing.T) {
	limits := attachment.DefaultLimits()
	content := make([]Content, 0, limits.MaxAttachmentsPerMessage+1)
	for index := range limits.MaxAttachmentsPerMessage {
		manifest := modelTestManifest(
			attachment.ID(fmt.Sprintf("att_boundary_%d", index)),
			fmt.Sprintf("%d.png", index), attachment.KindImage,
			attachment.MIMEPNG, 1, fmt.Sprintf("%064x", index+1),
		)
		content = append(content, modelTestContent(ContentInputImage, manifest))
	}
	request := Request{Input: []Item{{Type: ItemMessage, Role: RoleUser, Content: content}}}
	if err := request.Validate(); err != nil {
		t.Fatalf("exact attachment count failed: %v", err)
	}
	excess := modelTestManifest(
		"att_boundary_excess", "excess.png", attachment.KindImage,
		attachment.MIMEPNG, 1, strings.Repeat("f", 64),
	)
	request.Input[0].Content = append(request.Input[0].Content, modelTestContent(ContentInputImage, excess))
	if err := request.Validate(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("over-limit attachment count error = %v", err)
	}
}

func TestAzureInputMediaCapabilityQualification(t *testing.T) {
	defaultLimits := attachment.DefaultLimits()
	wantEncoded, err := maximumEncodedMediaBytes(
		defaultLimits.MaxModelRequestMediaBytes, defaultMaximumMedia,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		model      string
		apiVersion string
		want       bool
	}{
		{name: "implicit v1", model: "gpt-5.6-sol", apiVersion: "", want: true},
		{name: "explicit v1", model: "gpt-5.6-sol", apiVersion: "v1", want: true},
		{name: "literal preview", model: "gpt-5.6-sol", apiVersion: "preview", want: true},
		{name: "dated API", model: "gpt-5.6-sol", apiVersion: "2026-07-01-preview"},
		{name: "different logical model", model: "gpt-5.6", apiVersion: ""},
		{name: "case variant", model: "GPT-5.6-SOL", apiVersion: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newModelMediaTestClient(t, test.model, test.apiVersion, AzureOptions{})
			capability, ok := client.InputMediaCapability()
			if ok != test.want {
				t.Fatalf("InputMediaCapability() available = %v, want %v", ok, test.want)
			}
			if !ok {
				if !reflect.DeepEqual(capability, InputMediaCapability{}) {
					t.Fatalf("text-only capability = %#v", capability)
				}
				return
			}
			if capability.Attachment.ProtocolVersion != attachment.ProtocolVersion ||
				capability.MaxRequestItems != defaultMaximumMedia ||
				capability.MaxEncodedBytes != wantEncoded ||
				capability.MaxRequestBytes != defaultMaximumRequest {
				t.Fatalf("InputMediaCapability() = %#v", capability)
			}
			if capability.MaxEncodedBytes >= 56<<20 {
				t.Fatalf("derived encoded bound = %d, want below 56 MiB", capability.MaxEncodedBytes)
			}
			if got := capability.Attachment.MediaTypes; len(got) != 3 ||
				got[0].MIMEType != attachment.MIMEPNG ||
				got[1].MIMEType != attachment.MIMEJPEG ||
				got[2].MIMEType != attachment.MIMEPDF {
				t.Fatalf("advertised MIME matrix = %#v", got)
			}
			// The returned capability is defensive.
			capability.Attachment.MediaTypes[0].MIMEType = "unsafe/mutated"
			again, ok := client.InputMediaCapability()
			if !ok || again.Attachment.MediaTypes[0].MIMEType != attachment.MIMEPNG {
				t.Fatalf("caller mutated client capability: %#v", again)
			}
		})
	}
}

func TestAzureProviderErrorsNeverReflectAttachmentData(t *testing.T) {
	client := &AzureClient{apiKey: "synthetic-provider-key"}
	base64Payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("private-media"), 32))
	fields, _ := client.sanitizeProviderErrorFields(azureError{
		Code:    "invalid_request",
		Param:   "input[0].content[1].image_url",
		Message: "rejected data:image/png;base64," + base64Payload,
	}, "")
	exported := fields.Code + fields.Type + fields.Param + fields.Message
	if strings.Contains(exported, base64Payload) ||
		strings.Contains(exported, "data:image/png;base64,") ||
		!strings.Contains(exported, "[attachment data redacted]") {
		t.Fatalf("provider error attachment reflection was not sealed: %q", exported)
	}

	standalone := client.sanitizeProviderDiagnostic("rejected " + base64Payload)
	if strings.Contains(standalone, base64Payload) ||
		!strings.Contains(standalone, "[attachment data redacted]") {
		t.Fatalf("standalone base64 reflection was not sealed: %q", standalone)
	}
}

func TestAzureProjectsOrderedImageAndPDFContentExactly(t *testing.T) {
	pngBytes := modelTestPNG(t, 2, 2)
	jpegBytes := modelTestJPEG(t, 2, 1)
	pdfBytes := modelTestPDF(t, 1)
	pngManifest := modelTestManifestForBytes(
		"att_wire_png", "screen.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	pdfManifest := modelTestManifestForBytes(
		"att_wire_pdf", "report.pdf", attachment.KindDocument, attachment.MIMEPDF, pdfBytes,
	)
	pdfSecond := modelTestManifestForBytes(
		"att_wire_pdf_second", "appendix.pdf", attachment.KindDocument, attachment.MIMEPDF, pdfBytes,
	)
	jpegManifest := modelTestManifestForBytes(
		"att_wire_jpeg", "photo.jpg", attachment.KindImage, attachment.MIMEJPEG, jpegBytes,
	)
	source := newStaticAttachmentSource(
		modelTestResolved{manifest: pngManifest, data: pngBytes},
		modelTestResolved{manifest: pdfManifest, data: pdfBytes},
		modelTestResolved{manifest: pdfSecond, data: pdfBytes},
		modelTestResolved{manifest: jpegManifest, data: jpegBytes},
	)

	var calls atomic.Int32
	var captured []byte
	options := AzureOptions{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		captured, _ = io.ReadAll(request.Body)
		return streamingResponse("resp_media_wire"), nil
	})}}
	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", options)
	request := Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{
				{Type: ContentInputText, Text: "Inspect in this order."},
				modelTestContent(ContentInputImage, pngManifest),
				modelTestContent(ContentInputFile, pdfManifest),
				modelTestContent(ContentInputFile, pdfSecond),
				modelTestContent(ContentInputImage, jpegManifest),
				{Type: ContentInputText, Text: "Then compare them."},
			},
		}},
		AttachmentSource: source,
	}
	stream, err := client.Stream(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
	if source.callCount() != 4 {
		t.Fatalf("attachment resolves = %d, want one per unique attachment", source.callCount())
	}

	var wire map[string]any
	if err := json.Unmarshal(captured, &wire); err != nil {
		t.Fatal(err)
	}
	input := wire["input"].([]any)
	message := input[0].(map[string]any)
	content := message["content"].([]any)
	want := []map[string]any{
		{"type": "input_text", "text": "Inspect in this order."},
		{
			"type": "input_image", "detail": "auto",
			"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes),
		},
		{
			"type": "input_file", "filename": "report.pdf",
			"file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdfBytes),
		},
		{
			"type": "input_file", "filename": "appendix.pdf",
			"file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdfBytes),
		},
		{
			"type": "input_image", "detail": "auto",
			"image_url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes),
		},
		{"type": "input_text", "text": "Then compare them."},
	}
	if len(content) != len(want) {
		t.Fatalf("wire content count = %d, want %d: %#v", len(content), len(want), content)
	}
	for index, expected := range want {
		if !reflect.DeepEqual(content[index], expected) {
			t.Fatalf("wire content[%d] = %#v, want %#v", index, content[index], expected)
		}
	}
	wireText := string(captured)
	for _, forbidden := range []string{
		string(pngManifest.AttachmentID), pngManifest.StorageID, pngManifest.SHA256,
		string(pdfManifest.AttachmentID), pdfManifest.StorageID, pdfManifest.SHA256,
	} {
		if strings.Contains(wireText, forbidden) {
			t.Fatalf("provider body exposed runtime attachment identity %q", forbidden)
		}
	}
}

func TestAzureAttachmentOnlyMessage(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_only", "only.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	source := newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
	var captured map[string]any
	client := newModelMediaTestClient(t, "gpt-5.6-sol", "preview", AzureOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
				return nil, err
			}
			return streamingResponse("resp_attachment_only"), nil
		})},
	})
	request := Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{modelTestContent(ContentInputImage, manifest)},
		}},
		AttachmentSource: source,
	}
	stream, err := client.Stream(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(stream); err != nil {
		t.Fatal(err)
	}
	input := captured["input"].([]any)
	content := input[0].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "input_image" {
		t.Fatalf("attachment-only wire content = %#v", content)
	}
}

func TestAzureRejectsUnsupportedMediaConfigurationsBeforeTransport(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_unsupported", "screen.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	source := newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
	tests := []struct {
		name       string
		model      string
		apiVersion string
		source     AttachmentSource
	}{
		{name: "different model", model: "gpt-5.6", apiVersion: "", source: source},
		{name: "dated API", model: "gpt-5.6-sol", apiVersion: "2026-07-01-preview", source: source},
		{name: "missing source", model: "gpt-5.6-sol", apiVersion: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := newModelMediaTestClient(t, test.model, test.apiVersion, AzureOptions{
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return streamingResponse("must_not_start"), nil
				})},
			})
			request := Request{
				Input: []Item{{
					Type: ItemMessage, Role: RoleUser,
					Content: []Content{modelTestContent(ContentInputImage, manifest)},
				}},
				AttachmentSource: test.source,
			}
			_, err := client.Stream(t.Context(), request)
			if !errors.Is(err, ErrInputMediaUnavailable) {
				t.Fatalf("Stream() error = %v, want ErrInputMediaUnavailable", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("unsupported media reached transport %d times", calls.Load())
			}
		})
	}
}

func TestAzureRejectsAttachmentBytesReflectingAPIKeyBeforeTransport(t *testing.T) {
	pdfBytes := modelTestPDFWithComment(t, testAPIKey)
	if !bytes.Contains(pdfBytes, []byte(testAPIKey)) {
		t.Fatal("credential-bearing PDF fixture lost the configured API key")
	}
	manifest := modelTestManifestForBytes(
		"att_credential_bytes", "credential.pdf",
		attachment.KindDocument, attachment.MIMEPDF, pdfBytes,
	)
	source := newStaticAttachmentSource(modelTestResolved{
		manifest: manifest, data: pdfBytes,
	})
	var calls atomic.Int32
	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", AzureOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return streamingResponse("must_not_start"), nil
		})},
	})
	request := Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{modelTestContent(ContentInputFile, manifest)},
		}},
		AttachmentSource: source,
	}
	_, err := client.Stream(t.Context(), request)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("credential-bearing attachment error = %v, want ErrProtocol", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("credential-bearing attachment reached provider %d times", calls.Load())
	}
	if source.callCount() != 1 {
		t.Fatalf("attachment source resolves = %d, want 1", source.callCount())
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if rendered := fmt.Sprintf(format, err); strings.Contains(rendered, testAPIKey) {
			t.Fatalf("format %q exposed configured API key: %q", format, rendered)
		}
	}
}

func TestAzureMediaPreflightLimitsAndVerificationBeforeTransport(t *testing.T) {
	pngBytes := modelTestPNG(t, 2, 2)
	pdfBytes := modelTestPDF(t, 2)
	manifest := modelTestManifestForBytes(
		"att_preflight", "screen.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	second := modelTestManifestForBytes(
		"att_preflight_second", "second.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	pdfManifest := modelTestManifestForBytes(
		"att_preflight_pdf", "pages.pdf", attachment.KindDocument, attachment.MIMEPDF, pdfBytes,
	)
	tests := []struct {
		name    string
		options AzureOptions
		source  func() AttachmentSource
		content []Content
	}{
		{
			name:    "request count",
			options: AzureOptions{MaximumRequestMediaItems: 1},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(
					modelTestResolved{manifest: manifest, data: pngBytes},
					modelTestResolved{manifest: second, data: pngBytes},
				)
			},
			content: []Content{
				modelTestContent(ContentInputImage, manifest),
				modelTestContent(ContentInputImage, second),
			},
		},
		{
			name: "decoded bytes",
			options: AzureOptions{AttachmentLimits: func() attachment.Limits {
				limits := attachment.DefaultLimits()
				limits.MaxModelRequestMediaBytes = int64(len(pngBytes) - 1)
				return limits
			}()},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "encoded bytes",
			options: AzureOptions{
				MaximumEncodedMediaBytes: int64(
					len("data:image/png;base64,") +
						base64.StdEncoding.EncodedLen(len(pngBytes)) - 1,
				),
			},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name:    "request bytes",
			options: AzureOptions{MaximumRequestBytes: 128},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "returned manifest mismatch",
			source: func() AttachmentSource {
				changed := manifest
				changed.Name = "changed.png"
				return newStaticAttachmentSource(modelTestResolved{manifest: changed, data: pngBytes})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "tampered bytes",
			source: func() AttachmentSource {
				tampered := append([]byte(nil), pngBytes...)
				tampered[len(tampered)/2] ^= 1
				return newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: tampered})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "dimension revalidation",
			options: AzureOptions{AttachmentLimits: func() attachment.Limits {
				limits := attachment.DefaultLimits()
				limits.MaxImageDimension = 1
				limits.MaxImagePixels = 1
				return limits
			}()},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "PDF page revalidation",
			options: AzureOptions{AttachmentLimits: func() attachment.Limits {
				limits := attachment.DefaultLimits()
				limits.MaxPDFPages = 1
				return limits
			}()},
			source: func() AttachmentSource {
				return newStaticAttachmentSource(modelTestResolved{manifest: pdfManifest, data: pdfBytes})
			},
			content: []Content{modelTestContent(ContentInputFile, pdfManifest)},
		},
		{
			name: "resolver error privacy",
			source: func() AttachmentSource {
				return &staticAttachmentSource{err: errors.New("read /private/source/screenshot.png: denied")}
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
		{
			name: "resolver panic",
			source: func() AttachmentSource {
				return &staticAttachmentSource{panicValue: "panic /private/source/screenshot.png"}
			},
			content: []Content{modelTestContent(ContentInputImage, manifest)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			test.options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return streamingResponse("must_not_start"), nil
			})}
			client := newModelMediaTestClient(t, "gpt-5.6-sol", "", test.options)
			request := Request{
				Input: []Item{{
					Type: ItemMessage, Role: RoleUser, Content: test.content,
				}},
				AttachmentSource: test.source(),
			}
			_, err := client.Stream(t.Context(), request)
			if !errors.Is(err, ErrInputMediaUnavailable) {
				t.Fatalf("Stream() error = %v, want ErrInputMediaUnavailable", err)
			}
			if strings.Contains(err.Error(), "/private/source") {
				t.Fatalf("provider preflight error exposed source path: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("invalid media reached transport %d times", calls.Load())
			}
		})
	}
}

func TestAzureMediaPayloadIsReusedForRetryAndReleasedAtTerminal(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_retry", "retry.png", attachment.KindImage, attachment.MIMEPNG, pngBytes,
	)
	source := newStaticAttachmentSource(modelTestResolved{manifest: manifest, data: pngBytes})
	var calls atomic.Int32
	var mu sync.Mutex
	var bodies [][]byte
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"server_error","message":"retry","type":"api_error"}}`,
				)),
			}, nil
		}
		return streamingResponse("resp_media_retry"), nil
	})}
	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", options)
	request := Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{modelTestContent(ContentInputImage, manifest)},
		}},
		AttachmentSource: source,
	}
	streamValue, err := client.Stream(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	stream := streamValue.(*azureStream)
	for {
		event, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == EventResponseCompleted {
			break
		}
	}
	if len(stream.payload) != 0 {
		t.Fatalf("terminal stream retained %d request payload bytes", len(stream.payload))
	}
	if source.callCount() != 1 {
		t.Fatalf("retry resolved attachment %d times, want once", source.callCount())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry payloads differ: attempts=%d", len(bodies))
	}
	if !bytes.Contains(bodies[0], []byte(base64.StdEncoding.EncodeToString(pngBytes))) {
		t.Fatal("media retry payload lacks expected snapshot")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderMediaRejectionClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   string
		param  string
		want   bool
	}{
		{name: "payload too large", status: http.StatusRequestEntityTooLarge, want: true},
		{name: "unsupported media", status: http.StatusUnsupportedMediaType, want: true},
		{name: "media rejection code", status: http.StatusBadRequest, code: "media_rejected", want: true},
		{name: "image param", status: http.StatusBadRequest, param: "input[0].content[1].image_url", want: true},
		{name: "file param", status: http.StatusBadRequest, param: "input[0].content[1].file_data", want: true},
		{name: "unrelated code", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "message text is not classified", status: http.StatusBadRequest, param: "input[0].content[0].text"},
		{name: "lookalike param", status: http.StatusBadRequest, param: "input[0].content[0].image_url_suffix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := providerRejectedMedia(test.status, test.code, test.param); got != test.want {
				t.Fatalf(
					"providerRejectedMedia(%d, %q, %q) = %v, want %v",
					test.status, test.code, test.param, got, test.want,
				)
			}
		})
	}

	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", AzureOptions{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnsupportedMediaType,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"unsupported","message":"media rejected","type":"api_error"}}`,
				)),
			}, nil
		})},
	})
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_classification", "classification.png", attachment.KindImage,
		attachment.MIMEPNG, pngBytes,
	)
	_, err := client.Stream(t.Context(), Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{modelTestContent(ContentInputImage, manifest)},
		}},
		AttachmentSource: newStaticAttachmentSource(modelTestResolved{
			manifest: manifest, data: pngBytes,
		}),
	})
	var providerError *ProviderError
	if !errors.As(err, &providerError) || !providerError.MediaRejected {
		t.Fatalf("HTTP media rejection classification = %#v, err=%v", providerError, err)
	}
	if !IsMediaRejection(err) || IsMediaRejection(errors.New("provider said unsupported image in prose")) {
		t.Fatalf("IsMediaRejection() did not preserve trusted-only classification")
	}
}

func TestProviderAttachmentDiagnosticRedactionHandlesCaseAndLineWrapping(t *testing.T) {
	value := "before DATA:IMAGE/PNG;BASE64,\nQUJDREVGR0hJSktM\nTU5PUFFSU1RVVldY after"
	redacted := redactProviderAttachmentData(value)
	if strings.Contains(redacted, "QUJDREVGR0hJSktM") ||
		strings.Contains(redacted, "TU5PUFFSU1RVVldY") ||
		!strings.Contains(redacted, "[attachment data redacted]") {
		t.Fatalf("wrapped attachment diagnostic was not redacted: %q", redacted)
	}
}

type modelTestResolved struct {
	manifest attachment.Manifest
	data     []byte
}

type staticAttachmentSource struct {
	mu         sync.Mutex
	values     map[attachment.ID]modelTestResolved
	calls      []attachment.ID
	err        error
	panicValue any
}

func newStaticAttachmentSource(values ...modelTestResolved) *staticAttachmentSource {
	source := &staticAttachmentSource{values: make(map[attachment.ID]modelTestResolved, len(values))}
	for _, value := range values {
		source.values[value.manifest.AttachmentID] = value
	}
	return source
}

func (source *staticAttachmentSource) Resolve(
	ctx context.Context,
	id attachment.ID,
) (attachment.Manifest, []byte, error) {
	if source.panicValue != nil {
		panic(source.panicValue)
	}
	if err := ctx.Err(); err != nil {
		return attachment.Manifest{}, nil, err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls = append(source.calls, id)
	if source.err != nil {
		return attachment.Manifest{}, nil, source.err
	}
	value, found := source.values[id]
	if !found {
		return attachment.Manifest{}, nil, attachment.ErrNotCommitted
	}
	return value.manifest, append([]byte(nil), value.data...), nil
}

func (source *staticAttachmentSource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return len(source.calls)
}

func newModelMediaTestClient(
	t *testing.T,
	logicalModel string,
	apiVersion string,
	options AzureOptions,
) *AzureClient {
	t.Helper()
	endpoint, err := url.Parse("http://localhost/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected provider call")
		})}
	}
	client, err := NewAzureClient(config.Azure{
		Endpoint: endpoint, ModelName: logicalModel, Deployment: "configured-deployment",
		APIKey: testAPIKey, APIVersion: apiVersion, ReasoningEffort: "high",
		RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second, MaxRetries: 1,
		UnsafeAllowInsecureLoopbackForTesting: true,
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func modelTestContent(contentType ContentType, manifest attachment.Manifest) Content {
	return Content{Type: contentType, Manifest: modelTestManifestPointer(manifest)}
}

func modelTestManifestPointer(manifest attachment.Manifest) *attachment.Manifest {
	copy := manifest
	return &copy
}

func modelTestManifest(
	id attachment.ID,
	name string,
	kind attachment.Kind,
	mimeType string,
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

func modelTestManifestForBytes(
	id attachment.ID,
	name string,
	kind attachment.Kind,
	mimeType string,
	data []byte,
) attachment.Manifest {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return modelTestManifest(id, name, kind, mimeType, int64(len(data)), digest)
}

func modelTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.Set(x, y, color.RGBA{R: uint8(20 + x), G: uint8(30 + y), B: 40, A: 255})
		}
	}
	var output bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func modelTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.Set(x, y, color.RGBA{R: uint8(50 + x), G: uint8(60 + y), B: 70, A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, value, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func modelTestPDF(t *testing.T, pages int) []byte {
	t.Helper()
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n")
	offsets := make([]int, pages+3)
	offsets[1] = output.Len()
	output.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	kids := make([]string, 0, pages)
	for page := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+page))
	}
	offsets[2] = output.Len()
	output.WriteString(fmt.Sprintf(
		"2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
		strings.Join(kids, " "), pages,
	))
	for page := range pages {
		offsets[3+page] = output.Len()
		output.WriteString(fmt.Sprintf(
			"%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>\nendobj\n",
			3+page,
		))
	}
	xref := output.Len()
	output.WriteString(fmt.Sprintf("xref\n0 %d\n", len(offsets)))
	output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		output.WriteString(fmt.Sprintf("%010d 00000 n \n", offset))
	}
	output.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\n", len(offsets)))
	output.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return output.Bytes()
}

func modelTestPDFWithComment(t *testing.T, comment string) []byte {
	t.Helper()
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n")
	output.WriteString("% ")
	output.WriteString(comment)
	output.WriteByte('\n')
	offsets := make([]int, 4)
	offsets[1] = output.Len()
	output.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offsets[2] = output.Len()
	output.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	offsets[3] = output.Len()
	output.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>\nendobj\n")
	xref := output.Len()
	output.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		output.WriteString(fmt.Sprintf("%010d 00000 n \n", offset))
	}
	output.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\n")
	output.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return output.Bytes()
}
