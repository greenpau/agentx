package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/transcript"
)

type attachmentRuntimeProvider struct {
	mu       sync.Mutex
	failures []error
	requests []model.Request
}

func (provider *attachmentRuntimeProvider) Stream(
	_ context.Context,
	request model.Request,
) (model.Stream, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	if len(provider.failures) > 0 {
		err := provider.failures[0]
		provider.failures = provider.failures[1:]
		if err != nil {
			return nil, err
		}
	}
	return &fakeStream{events: completed(
		[]model.Item{model.TextMessage(model.RoleAssistant, "done")},
		model.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	)}, nil
}

func (provider *attachmentRuntimeProvider) capturedRequests() []model.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]model.Request(nil), provider.requests...)
}

func TestMediaRejectionQuarantineIsDurableAndPreventsResend(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "attachments")
	attachmentStore, err := attachment.OpenStore(storeRoot, attachment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := attachmentStore.Close(); err != nil {
			t.Error(err)
		}
	})
	pngBytes := engineAttachmentPNG(t)
	sourcePath := filepath.Join(root, "private selected screenshot.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := attachmentStore.ImportFile(t.Context(), attachment.FileImport{
		AttachmentID: "att_quarantine", Path: sourcePath,
		Name: "screen.png", MIMEType: attachment.MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}

	transcriptStore, err := transcript.Open(t.Context(), transcript.Config{
		Path:      filepath.Join(root, "transcript.jsonl"),
		SessionID: "ses_attachment_quarantine", SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := transcriptStore.Close(); err != nil {
			t.Error(err)
		}
	})
	mediaRejection := &model.ProviderError{
		StatusCode: 415, Code: "unsupported_media",
		Message: "media rejected", MediaRejected: true,
	}
	provider := &attachmentRuntimeProvider{failures: []error{mediaRejection, nil}}
	query, err := New(Config{
		SessionID: "ses_attachment_quarantine", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{},
		Transcript: transcriptStore, Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	message := protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("inspect this"),
			protocol.AttachmentBlock(manifest),
		},
	}
	first, err := query.SubmitMessage(
		t.Context(), message, "123e4567-e89b-42d3-a456-426614174001",
	)
	if err == nil || first.Status != protocol.TurnResultError {
		t.Fatalf("media rejection outcome = %#v, err=%v", first, err)
	}
	second, err := query.SubmitPrompt(
		t.Context(), "continue without resending rejected media",
		"123e4567-e89b-42d3-a456-426614174002",
	)
	if err != nil || second.Status != protocol.TurnResultSuccess {
		t.Fatalf("post-rejection turn = %#v, err=%v", second, err)
	}
	requests := provider.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(requests))
	}
	if got := engineRequestMediaCount(requests[0]); got != 1 {
		t.Fatalf("initial request media = %d, want 1", got)
	}
	if got := engineRequestMediaCount(requests[1]); got != 0 {
		t.Fatalf("quarantined request resent %d media items", got)
	}
	if !engineRequestContainsText(requests[1], "[image]") {
		t.Fatalf("quarantined request lacks explicit image placeholder: %#v", requests[1].Input)
	}

	snapshot, err := transcriptStore.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var (
		sawManifest   bool
		sawQuarantine bool
	)
	for _, event := range snapshot.Events {
		if event.Kind == protocol.EventKindMessage && event.Message != nil {
			for _, block := range event.Message.Content {
				if block.Type == protocol.ContentAttachment &&
					block.AttachmentManifest() == manifest {
					sawManifest = true
				}
			}
		}
		if event.Kind == protocol.EventKindSessionMetadata &&
			event.Metadata != nil &&
			event.Metadata.Key == attachmentQuarantineKey {
			sawQuarantine = true
		}
	}
	if !sawManifest || !sawQuarantine {
		t.Fatalf("durable attachment evidence manifest=%t quarantine=%t", sawManifest, sawQuarantine)
	}
	encoded, err := json.Marshal(snapshot.Events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		sourcePath,
		base64.StdEncoding.EncodeToString(pngBytes),
		"data:image/png;base64,",
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("transcript exposed attachment source material %q", forbidden)
		}
	}

	restoredProvider := &attachmentRuntimeProvider{}
	restored, err := New(Config{
		SessionID: "ses_attachment_quarantine", Model: "gpt-5.6-sol",
		Provider: restoredProvider, Capabilities: &fakeCapabilities{},
		Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("restore quarantined history: %v", err)
	}
	accepted, exists := restored.AcceptedPrompt("123e4567-e89b-42d3-a456-426614174001")
	if !exists || len(accepted.Content) != 2 ||
		accepted.Content[1].AttachmentManifest() != manifest {
		t.Fatalf("restored prompt metadata = %#v, exists=%t", accepted, exists)
	}
	if _, err := restored.SubmitPrompt(
		t.Context(), "resumed turn", "123e4567-e89b-42d3-a456-426614174003",
	); err != nil {
		t.Fatal(err)
	}
	restoredRequests := restoredProvider.capturedRequests()
	if len(restoredRequests) != 1 ||
		engineRequestMediaCount(restoredRequests[0]) != 0 {
		t.Fatalf("restored quarantine resent media: %#v", restoredRequests)
	}

	blobPath := filepath.Join(storeRoot, "blobs", manifest.SHA256+".blob")
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedProvider := &attachmentRuntimeProvider{}
	tampered, err := New(Config{
		SessionID: "ses_attachment_quarantine", Model: "gpt-5.6-sol",
		Provider: tamperedProvider, Capabilities: &fakeCapabilities{},
		Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tampered.Restore(snapshot); err == nil ||
		!strings.Contains(err.Error(), "missing or tampered") {
		t.Fatalf("tampered attachment restore = %v", err)
	}
	if len(tamperedProvider.capturedRequests()) != 0 {
		t.Fatal("tampered attachment restore reached provider")
	}
}

func TestUnrelatedProviderFailureDoesNotQuarantineValidMedia(t *testing.T) {
	root := t.TempDir()
	attachmentStore, err := attachment.OpenStore(
		filepath.Join(root, "attachments"), attachment.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := attachmentStore.Close(); err != nil {
			t.Error(err)
		}
	})
	sourcePath := filepath.Join(root, "valid-after-unrelated-error.png")
	if err := os.WriteFile(sourcePath, engineAttachmentPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := attachmentStore.ImportFile(t.Context(), attachment.FileImport{
		AttachmentID: "att_unrelated_failure", Path: sourcePath,
		Name: "valid.png", MIMEType: attachment.MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &attachmentRuntimeProvider{failures: []error{
		&model.ProviderError{
			StatusCode: 400, Code: "invalid_request",
			Param: "tools[0].parameters", Message: "unrelated request failure",
		},
		nil,
	}}
	query, err := New(Config{
		SessionID: "ses_unrelated_media_failure", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{},
		Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := query.SubmitMessage(t.Context(), protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("inspect"),
			protocol.AttachmentBlock(manifest),
		},
	}, "123e4567-e89b-42d3-a456-426614174020")
	if firstErr == nil || first.Status != protocol.TurnResultError {
		t.Fatalf("unrelated provider failure = %#v, err=%v", first, firstErr)
	}
	if len(query.quarantined) != 0 {
		t.Fatalf("unrelated provider failure quarantined valid media: %#v", query.quarantined)
	}
	second, secondErr := query.SubmitPrompt(
		t.Context(), "retry context normally",
		"123e4567-e89b-42d3-a456-426614174021",
	)
	if secondErr != nil || second.Status != protocol.TurnResultSuccess {
		t.Fatalf("turn after unrelated failure = %#v, err=%v", second, secondErr)
	}
	requests := provider.capturedRequests()
	if len(requests) != 2 ||
		engineRequestMediaCount(requests[0]) != 1 ||
		engineRequestMediaCount(requests[1]) != 1 {
		t.Fatalf("unrelated failure request media was not retained: %#v", requests)
	}
}

func TestQuarantineRemainsLiveWhenDurableEventSinkDeliveryFails(t *testing.T) {
	root := t.TempDir()
	attachmentStore, err := attachment.OpenStore(
		filepath.Join(root, "attachments"), attachment.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := attachmentStore.Close(); err != nil {
			t.Error(err)
		}
	})
	sourcePath := filepath.Join(root, "sink-failure-quarantine.png")
	if err := os.WriteFile(sourcePath, engineAttachmentPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := attachmentStore.ImportFile(t.Context(), attachment.FileImport{
		AttachmentID: "att_sink_failure", Path: sourcePath,
		Name: "sink.png", MIMEType: attachment.MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &attachmentRuntimeProvider{failures: []error{
		&model.ProviderError{
			StatusCode: 415, Code: "unsupported_media",
			Message: "media rejected", MediaRejected: true,
		},
		nil,
	}}
	transcriptStore := &faultStore{}
	var failedQuarantineDelivery bool
	query, err := New(Config{
		SessionID: "ses_quarantine_sink_failure", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{},
		Transcript: transcriptStore, Attachments: attachmentStore, MaxTurns: 1,
		Sink: EventSinkFunc(func(_ context.Context, event protocol.Event) error {
			if !failedQuarantineDelivery &&
				event.Kind == protocol.EventKindSessionMetadata &&
				event.Metadata != nil &&
				event.Metadata.Key == attachmentQuarantineKey {
				failedQuarantineDelivery = true
				return errors.New("injected quarantine delivery failure")
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := query.SubmitMessage(t.Context(), protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("inspect"),
			protocol.AttachmentBlock(manifest),
		},
	}, "123e4567-e89b-42d3-a456-426614174022")
	if firstErr == nil || first.Status != protocol.TurnResultError ||
		!failedQuarantineDelivery {
		t.Fatalf("quarantine delivery failure = %#v, err=%v", first, firstErr)
	}
	if query.quarantined[manifest.AttachmentID] != manifest.SHA256 {
		t.Fatalf("delivery failure reopened media egress: %#v", query.quarantined)
	}
	second, secondErr := query.SubmitPrompt(
		t.Context(), "continue after presentation failure",
		"123e4567-e89b-42d3-a456-426614174023",
	)
	if secondErr != nil || second.Status != protocol.TurnResultSuccess {
		t.Fatalf("turn after quarantine delivery failure = %#v, err=%v", second, secondErr)
	}
	requests := provider.capturedRequests()
	if len(requests) != 2 ||
		engineRequestMediaCount(requests[0]) != 1 ||
		engineRequestMediaCount(requests[1]) != 0 {
		t.Fatalf("quarantine delivery failure resent media: %#v", requests)
	}
	transcriptStore.mu.Lock()
	defer transcriptStore.mu.Unlock()
	sawQuarantine := false
	for _, event := range transcriptStore.events {
		if event.Kind == protocol.EventKindSessionMetadata &&
			event.Metadata != nil &&
			event.Metadata.Key == attachmentQuarantineKey {
			sawQuarantine = true
		}
	}
	if !sawQuarantine {
		t.Fatal("quarantine delivery failure lacked durable evidence")
	}
}

func TestCompactionPreservesNewestAttachmentAndVerifiesItOnResume(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "attachments")
	attachmentStore, err := attachment.OpenStore(storeRoot, attachment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := attachmentStore.Close(); err != nil {
			t.Error(err)
		}
	})
	pngBytes := engineAttachmentPNG(t)
	sourcePath := filepath.Join(root, "selected for compaction.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := attachmentStore.ImportFile(t.Context(), attachment.FileImport{
		AttachmentID: "att_compaction", Path: sourcePath,
		Name: "recent.png", MIMEType: attachment.MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcriptStore, err := transcript.Open(t.Context(), transcript.Config{
		Path:      filepath.Join(root, "transcript.jsonl"),
		SessionID: "ses_attachment_compaction", SyncOnAppend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := transcriptStore.Close(); err != nil {
			t.Error(err)
		}
	})
	provider := &attachmentRuntimeProvider{}
	query, err := New(Config{
		SessionID: "ses_attachment_compaction", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: &fakeCapabilities{},
		Transcript: transcriptStore, Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 12 {
		_, err := query.SubmitPrompt(
			t.Context(),
			fmt.Sprintf("context-%02d %s", index, strings.Repeat("history ", 250)),
			fmt.Sprintf("compaction-prompt-%02d", index),
		)
		if err != nil {
			t.Fatalf("seed turn %d: %v", index, err)
		}
	}
	if _, err := query.SubmitMessage(t.Context(), protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("newest relevant screenshot"),
			protocol.AttachmentBlock(manifest),
		},
	}, "123e4567-e89b-42d3-a456-426614174010"); err != nil {
		t.Fatal(err)
	}
	if got := engineHistoryMediaCount(query.history); got != 1 {
		t.Fatalf("pre-compaction history media = %d, want 1", got)
	}
	if err := query.CompactContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := engineHistoryMediaCount(query.history); got != 1 {
		t.Fatalf("compacted live history media = %d, want newest attachment", got)
	}

	snapshot, err := transcriptStore.LoadAndReconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var projectionBytes []byte
	for _, event := range snapshot.Events {
		if event.Kind == protocol.EventKindSessionMetadata &&
			event.Metadata != nil &&
			event.Metadata.Key == contextProjectionKey {
			projectionBytes = append([]byte(nil), event.Metadata.Value...)
		}
	}
	if len(projectionBytes) == 0 ||
		!bytes.Contains(projectionBytes, []byte(`"attachment_id":"att_compaction"`)) {
		t.Fatalf("durable compaction projection lost attachment metadata: %s", projectionBytes)
	}
	for _, forbidden := range []string{
		sourcePath,
		base64.StdEncoding.EncodeToString(pngBytes),
		"data:image/png;base64,",
	} {
		if bytes.Contains(projectionBytes, []byte(forbidden)) {
			t.Fatalf("compaction projection exposed %q", forbidden)
		}
	}

	restoredProvider := &attachmentRuntimeProvider{}
	restored, err := New(Config{
		SessionID: "ses_attachment_compaction", Model: "gpt-5.6-sol",
		Provider: restoredProvider, Capabilities: &fakeCapabilities{},
		Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("restore compacted attachment history: %v", err)
	}
	if got := engineHistoryMediaCount(restored.history); got != 1 {
		t.Fatalf("restored compacted history media = %d, want 1", got)
	}

	blobPath := filepath.Join(storeRoot, "blobs", manifest.SHA256+".blob")
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered, err := New(Config{
		SessionID: "ses_attachment_compaction", Model: "gpt-5.6-sol",
		Provider: &attachmentRuntimeProvider{}, Capabilities: &fakeCapabilities{},
		Attachments: attachmentStore, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tampered.Restore(snapshot); err == nil ||
		!strings.Contains(err.Error(), "missing or tampered") {
		t.Fatalf("tampered compacted attachment restore = %v", err)
	}
}

func engineRequestMediaCount(request model.Request) int {
	count := 0
	for _, item := range request.Input {
		for _, content := range item.Content {
			if content.Type == model.ContentInputImage ||
				content.Type == model.ContentInputFile {
				count++
			}
		}
	}
	return count
}

func engineHistoryMediaCount(history []model.Item) int {
	count := 0
	for _, item := range history {
		for _, content := range item.Content {
			if content.Type == model.ContentInputImage ||
				content.Type == model.ContentInputFile {
				count++
			}
		}
	}
	return count
}

func engineRequestContainsText(request model.Request, want string) bool {
	for _, item := range request.Input {
		for _, content := range item.Content {
			if content.Type == model.ContentInputText && content.Text == want {
				return true
			}
		}
	}
	return false
}

func engineAttachmentPNG(t *testing.T) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			value.Set(x, y, color.RGBA{
				R: uint8(70 + x), G: uint8(80 + y), B: 90, A: 255,
			})
		}
	}
	var output bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
