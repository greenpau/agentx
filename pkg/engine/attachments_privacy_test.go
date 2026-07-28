package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestProviderCannotReflectExactRequestMediaInOpaqueFields(t *testing.T) {
	for _, test := range []struct {
		name  string
		event func(string) model.Event
	}{
		{
			name: "request_id",
			event: func(value string) model.Event {
				return model.Event{
					Type: model.EventResponseCreated, RequestID: value,
					ResponseID: "resp_safe",
				}
			},
		},
		{
			name: "response_id",
			event: func(value string) model.Event {
				return model.Event{
					Type: model.EventResponseCreated, RequestID: "request_safe",
					ResponseID: value,
				}
			},
		},
		{
			name: "encrypted_content",
			event: func(value string) model.Event {
				return model.Event{
					Type: model.EventResponseCompleted,
					Response: &model.Response{
						ID: "resp_safe", Status: "completed",
						Output: []model.Item{{
							Type: model.ItemReasoning, ID: "reason_safe",
							EncryptedContent: value,
						}},
						Usage: model.Usage{
							InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
						},
					},
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := attachment.OpenStore(
				filepath.Join(root, "attachments"), attachment.Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			sourcePath := filepath.Join(root, "opaque-source.png")
			if err := os.WriteFile(sourcePath, engineAttachmentPNG(t), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest, err := store.ImportFile(t.Context(), attachment.FileImport{
				Path: sourcePath, AttachmentID: "att_opaque_reflection",
				Name: "opaque.png", MIMEType: attachment.MIMEPNG,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := store.Resolve(t.Context(), manifest.AttachmentID)
			if err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(raw)
			if test.name != "encrypted_content" &&
				len(encoded) > maximumProviderIDBytes {
				t.Fatalf(
					"opaque-ID media fixture is %d bytes, exceeds provider ID bound %d",
					len(encoded), maximumProviderIDBytes,
				)
			}

			provider := &fakeProvider{
				responses: [][]model.Event{{test.event(encoded)}},
			}
			transcriptStore := &faultStore{}
			sink := &capturingSink{}
			capabilities := &fakeCapabilities{}
			core, logs := observer.New(zapcore.DebugLevel)
			query, err := New(Config{
				SessionID: "ses_opaque_media_reflection", Model: "gpt-5.6-sol",
				Provider: provider, Capabilities: capabilities,
				Transcript: transcriptStore, Sink: sink, Attachments: store,
				MaxTurns: 1, Logger: zap.New(core),
			})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := query.SubmitMessage(t.Context(), protocol.Message{
				Role: protocol.RoleUser,
				Content: []protocol.ContentBlock{
					protocol.TextBlock("inspect"),
					protocol.AttachmentBlock(manifest),
				},
			}, "123e4567-e89b-42d3-a456-426614174098")
			if !errors.Is(err, model.ErrProtocol) ||
				outcome.Status != protocol.TurnResultError {
				t.Fatalf("opaque media reflection outcome = %#v, err=%v", outcome, err)
			}
			if len(provider.requests) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(provider.requests))
			}
			if len(capabilities.calls) != 0 {
				t.Fatalf("opaque media reflection reached tool execution: %#v", capabilities.calls)
			}
			if len(query.history) != 1 ||
				query.history[0].Role != model.RoleUser ||
				engineHistoryMediaCount(query.history) != 1 {
				t.Fatalf("opaque media reflection entered history: %#v", query.history)
			}
			if logs.FilterMessage("model stream event").Len() != 0 {
				t.Fatalf("opaque media reflection was logged as a stream event: %#v", logs.All())
			}

			transcriptStore.mu.Lock()
			durable, marshalErr := json.Marshal(transcriptStore.events)
			transcriptStore.mu.Unlock()
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			presentation, marshalErr := json.Marshal(sink.events)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var logText strings.Builder
			for _, entry := range logs.All() {
				logText.WriteString(entry.Message)
				logText.WriteString(fmt.Sprint(entry.ContextMap()))
			}
			for boundary, value := range map[string]string{
				"error":      fmt.Sprintf("%+v", err),
				"transcript": string(durable),
				"sink":       string(presentation),
				"logs":       logText.String(),
			} {
				if strings.Contains(value, encoded) ||
					strings.Contains(value, sourcePath) {
					t.Fatalf("%s exposed opaque media reflection: %s", boundary, value)
				}
			}
		})
	}
}

func TestProviderCannotEchoRequestMediaThroughSSEDeltasOrContentParts(t *testing.T) {
	media := []struct {
		name     string
		kind     attachment.Kind
		mimeType string
		fileName string
		raw      []byte
	}{
		{
			name: "png", kind: attachment.KindImage,
			mimeType: attachment.MIMEPNG, fileName: "echo.png",
			raw: engineAttachmentPNG(t),
		},
		{
			name: "pdf", kind: attachment.KindDocument,
			mimeType: attachment.MIMEPDF, fileName: "echo.pdf",
			raw: engineAttachmentPDF(t),
		},
	}
	for _, medium := range media {
		for _, echoForm := range []string{"data_url", "raw_base64"} {
			for _, projection := range []string{"sse_deltas", "content_parts"} {
				name := strings.Join([]string{medium.name, echoForm, projection}, "/")
				t.Run(name, func(t *testing.T) {
					testProviderAttachmentEchoBoundary(
						t, medium.kind, medium.mimeType, medium.fileName,
						medium.raw, echoForm, projection,
					)
				})
			}
		}
	}
}

func TestAzureMediaRejectionRoutesQuarantineWithoutRetryOrResend(t *testing.T) {
	for _, route := range []string{"http_400_media_code", "terminal_sse_media_code"} {
		t.Run(route, func(t *testing.T) {
			root := t.TempDir()
			store, err := attachment.OpenStore(
				filepath.Join(root, "attachments"), attachment.Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			sourcePath := filepath.Join(root, "rejected-source.png")
			if err := os.WriteFile(sourcePath, engineAttachmentPNG(t), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest, err := store.ImportFile(t.Context(), attachment.FileImport{
				Path: sourcePath, AttachmentID: "att_route_rejection",
				Name: "rejected.png", MIMEType: attachment.MIMEPNG,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := store.Resolve(t.Context(), manifest.AttachmentID)
			if err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(raw)

			var (
				providerCalls atomic.Int32
				bodies        [][]byte
			)
			client := engineAttachmentEchoAzureClient(
				t, store.Limits(),
				func(request *http.Request) (*http.Response, error) {
					call := providerCalls.Add(1)
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					bodies = append(bodies, body)
					if call == 1 {
						if route == "http_400_media_code" {
							header := make(http.Header)
							header.Set("Content-Type", "application/json")
							header.Set("x-should-retry", "true")
							return &http.Response{
								StatusCode: http.StatusBadRequest,
								Header:     header,
								Body: io.NopCloser(strings.NewReader(
									`{"error":{"code":"media_rejected","message":"media rejected","type":"invalid_request_error"}}`,
								)),
							}, nil
						}
						return engineAttachmentFailedResponse(), nil
					}
					return engineAttachmentCompletedResponse(), nil
				},
			)
			transcriptStore := &faultStore{}
			sink := &capturingSink{}
			query, err := New(Config{
				SessionID: "ses_azure_media_rejection_route",
				Model:     "gpt-5.6-sol", Provider: client,
				Capabilities: &fakeCapabilities{},
				Transcript:   transcriptStore, Sink: sink,
				Attachments: store, MaxTurns: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			first, firstErr := query.SubmitMessage(t.Context(), protocol.Message{
				Role: protocol.RoleUser,
				Content: []protocol.ContentBlock{
					protocol.TextBlock("inspect rejected media"),
					protocol.AttachmentBlock(manifest),
				},
			}, "123e4567-e89b-42d3-a456-426614174096")
			if firstErr == nil || first.Status != protocol.TurnResultError ||
				!inspectEngineError(firstErr).mediaRejected {
				t.Fatalf("first rejection outcome = %#v, err=%v", first, firstErr)
			}
			if providerCalls.Load() != 1 {
				t.Fatalf("media rejection route made %d calls before quarantine, want 1", providerCalls.Load())
			}
			if query.quarantined[manifest.AttachmentID] != manifest.SHA256 {
				t.Fatalf("media rejection was not quarantined: %#v", query.quarantined)
			}
			second, secondErr := query.SubmitPrompt(
				t.Context(), "continue without media",
				"123e4567-e89b-42d3-a456-426614174097",
			)
			if secondErr != nil || second.Status != protocol.TurnResultSuccess {
				t.Fatalf("post-quarantine outcome = %#v, err=%v", second, secondErr)
			}
			if providerCalls.Load() != 2 || len(bodies) != 2 {
				t.Fatalf("provider calls=%d bodies=%d, want 2", providerCalls.Load(), len(bodies))
			}
			if !bytes.Contains(bodies[0], []byte(encoded)) {
				t.Fatal("initial rejected request lacked attachment bytes")
			}
			if bytes.Contains(bodies[1], []byte(encoded)) ||
				bytes.Contains(bodies[1], []byte("data:image/png;base64,")) {
				t.Fatalf("quarantined media was resent: %s", bodies[1])
			}

			transcriptStore.mu.Lock()
			durable, marshalErr := json.Marshal(transcriptStore.events)
			transcriptStore.mu.Unlock()
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !bytes.Contains(durable, []byte(`"key":"attachment_quarantine"`)) {
				t.Fatalf("durable transcript lacks quarantine evidence: %s", durable)
			}
			for _, forbidden := range []string{encoded, sourcePath, "data:image/png;base64,"} {
				if bytes.Contains(durable, []byte(forbidden)) {
					t.Fatalf("durable rejection evidence exposed %q: %s", forbidden, durable)
				}
			}
		})
	}
}

func testProviderAttachmentEchoBoundary(
	t *testing.T,
	kind attachment.Kind,
	mimeType, fileName string,
	raw []byte,
	echoForm, projection string,
) {
	t.Helper()
	root := t.TempDir()
	store, err := attachment.OpenStore(
		filepath.Join(root, "attachments"),
		attachment.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	sourcePath := filepath.Join(root, "caller-selected-"+fileName)
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ImportFile(t.Context(), attachment.FileImport{
		Path: sourcePath, AttachmentID: "att_provider_echo",
		Name: fileName, MIMEType: mimeType,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedManifest, resolved, err := store.Resolve(t.Context(), manifest.AttachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedManifest != manifest {
		t.Fatalf("resolved manifest = %#v, want %#v", resolvedManifest, manifest)
	}
	encoded := base64.StdEncoding.EncodeToString(resolved)
	echo := encoded
	if echoForm == "data_url" {
		echo = "data:" + mimeType + ";base64," + encoded
	}
	chunks := engineAttachmentEchoChunks(echo, echoForm == "data_url")

	var (
		providerCalls atomic.Int32
		providerBody  []byte
	)
	client := engineAttachmentEchoAzureClient(t, store.Limits(), func(request *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		providerBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return engineAttachmentEchoResponse(projection, chunks), nil
	})
	transcriptStore := &faultStore{}
	sink := &capturingSink{}
	capabilities := &fakeCapabilities{results: []CapabilityResult{{
		ID: "call_media_echo", Name: "Read",
		Status: protocol.ToolResultSuccess, Content: "must not execute",
	}}}
	core, logs := observer.New(zapcore.DebugLevel)
	query, err := New(Config{
		SessionID: "ses_provider_attachment_echo", Model: "gpt-5.6-sol",
		Provider: client, Capabilities: capabilities,
		Transcript: transcriptStore, Sink: sink, Attachments: store,
		MaxTurns: 1, Logger: zap.New(core),
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := query.SubmitMessage(t.Context(), protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("inspect the attached media"),
			protocol.AttachmentBlock(manifest),
		},
	}, "123e4567-e89b-42d3-a456-426614174099")
	if !errors.Is(err, model.ErrProtocol) ||
		outcome.Status != protocol.TurnResultError {
		t.Fatalf("media echo outcome = %#v, err=%v", outcome, err)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}
	if !bytes.Contains(providerBody, []byte(echo)) {
		t.Fatalf("provider request did not contain echoed fixture form %q", echoForm)
	}
	if len(capabilities.calls) != 0 {
		t.Fatalf("media echo reached tool execution: %#v", capabilities.calls)
	}
	if len(query.history) != 1 ||
		query.history[0].Role != model.RoleUser ||
		engineHistoryMediaCount(query.history) != 1 {
		t.Fatalf("media echo entered engine history: %#v", query.history)
	}

	transcriptStore.mu.Lock()
	durable, marshalErr := json.Marshal(transcriptStore.events)
	transcriptStore.mu.Unlock()
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	presentation, marshalErr := json.Marshal(sink.events)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	outcomeBytes, marshalErr := json.Marshal(outcome)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var logText strings.Builder
	for _, entry := range logs.All() {
		logText.WriteString(entry.Message)
		logText.WriteString(fmt.Sprint(entry.ContextMap()))
	}
	public := map[string]string{
		"returned error": fmt.Sprintf("%+v", err),
		"outcome":        string(outcomeBytes),
		"transcript":     string(durable),
		"sink":           string(presentation),
		"logs":           logText.String(),
	}
	for _, forbidden := range append([]string{echo, encoded}, chunks...) {
		if len(forbidden) < 12 {
			t.Fatalf("privacy fixture produced an undersized fragment %q", forbidden)
		}
		for boundary, value := range public {
			if strings.Contains(value, forbidden) {
				t.Fatalf("%s exposed provider-reflected media fragment %q: %s", boundary, forbidden, value)
			}
		}
	}
	for boundary, value := range public {
		if strings.Contains(value, "data:"+mimeType+";base64,") ||
			strings.Contains(value, sourcePath) {
			t.Fatalf("%s exposed provider media framing or caller path: %s", boundary, value)
		}
	}
}

func engineAttachmentEchoAzureClient(
	t *testing.T,
	limits attachment.Limits,
	roundTrip func(*http.Request) (*http.Response, error),
) *model.AzureClient {
	t.Helper()
	endpoint, err := url.Parse("http://localhost/openai/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	client, err := model.NewAzureClient(config.Azure{
		Endpoint: endpoint, ModelName: "gpt-5.6-sol",
		Deployment: "attachment-echo-deployment",
		APIKey:     "engine-attachment-echo-api-key",
		APIVersion: "v1", ReasoningEffort: "high",
		RequestTimeout: 2 * time.Second, StreamWatchdog: time.Second,
		MaxRetries: 1, UnsafeAllowInsecureLoopbackForTesting: true,
	}, model.AzureOptions{
		HTTPClient:       &http.Client{Transport: engineAttachmentEchoRoundTripper(roundTrip)},
		AttachmentLimits: limits,
		RetryBase:        time.Millisecond,
		RetryMaximum:     time.Millisecond,
		RetryWindow:      100 * time.Millisecond,
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type engineAttachmentEchoRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip engineAttachmentEchoRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return roundTrip(request)
}

func engineAttachmentEchoResponse(
	projection string,
	chunks []string,
) *http.Response {
	events := []map[string]any{
		{
			"type": "response.created",
			"response": map[string]any{
				"id": "resp_media_echo", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
		{
			"type": "response.in_progress",
			"response": map[string]any{
				"id": "resp_media_echo", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
	}
	messageContent := []any{
		map[string]any{"type": "output_text", "text": "safe terminal"},
	}
	if projection == "sse_deltas" {
		events = append(events, map[string]any{
			"type": "response.output_text.delta", "sequence_number": 1,
			"item_id": "msg_media_echo", "output_index": 0,
			"content_index": 0, "delta": "safe preface ",
		})
		for index, chunk := range chunks {
			events = append(events, map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": index + 2,
				"item_id":         "msg_media_echo",
				"output_index":    0, "content_index": 0,
				"delta": chunk,
			})
		}
	} else {
		messageContent = []any{
			map[string]any{"type": "output_text", "text": "safe preface "},
		}
		for _, chunk := range chunks {
			messageContent = append(messageContent, map[string]any{
				"type": "output_text", "text": chunk,
			})
		}
	}
	events = append(events, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_media_echo", "model": "gpt-5.6-sol",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type": "message", "id": "msg_media_echo",
					"role": "assistant", "status": "completed",
					"phase": "final_answer", "content": messageContent,
				},
				map[string]any{
					"type": "function_call", "id": "fc_media_echo",
					"call_id": "call_media_echo", "name": "Read",
					"arguments": "{}", "status": "completed",
				},
			},
			"usage": map[string]any{
				"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
			},
		},
	})
	var body strings.Builder
	for _, event := range events {
		payload, _ := json.Marshal(event)
		fmt.Fprintf(&body, "data: %s\n\n", payload)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(body.String())),
	}
}

func engineAttachmentFailedResponse() *http.Response {
	events := []map[string]any{
		{
			"type": "response.created",
			"response": map[string]any{
				"id": "resp_media_failed", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
		{
			"type": "response.in_progress",
			"response": map[string]any{
				"id": "resp_media_failed", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
		{
			"type": "response.failed",
			"response": map[string]any{
				"id": "resp_media_failed", "model": "gpt-5.6-sol",
				"status": "failed", "output": []any{},
				"error": map[string]any{
					"code": "media_rejected", "type": "invalid_request_error",
					"message": "media rejected",
				},
			},
		},
	}
	return engineAttachmentSSEResponse(events)
}

func engineAttachmentCompletedResponse() *http.Response {
	events := []map[string]any{
		{
			"type": "response.created",
			"response": map[string]any{
				"id": "resp_after_quarantine", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
		{
			"type": "response.in_progress",
			"response": map[string]any{
				"id": "resp_after_quarantine", "model": "gpt-5.6-sol",
				"status": "in_progress", "output": []any{},
			},
		},
		{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_after_quarantine", "model": "gpt-5.6-sol",
				"status": "completed",
				"output": []any{map[string]any{
					"type": "message", "id": "msg_after_quarantine",
					"role": "assistant", "status": "completed",
					"phase": "final_answer",
					"content": []any{map[string]any{
						"type": "output_text", "text": "continued",
					}},
				}},
				"usage": map[string]any{
					"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
				},
			},
		},
	}
	return engineAttachmentSSEResponse(events)
}

func engineAttachmentSSEResponse(events []map[string]any) *http.Response {
	var body strings.Builder
	for _, event := range events {
		payload, _ := json.Marshal(event)
		fmt.Fprintf(&body, "data: %s\n\n", payload)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(body.String())),
	}
}

func engineAttachmentEchoChunks(value string, dataURL bool) []string {
	if dataURL {
		comma := strings.IndexByte(value, ',')
		if comma < 13 || comma+1 >= len(value) {
			panic("invalid attachment data URL test fixture")
		}
		payload := value[comma+1:]
		payloadSplit := len(payload) / 2
		return []string{
			value[:12],
			value[12:comma+1] + payload[:payloadSplit],
			payload[payloadSplit:],
		}
	}
	first := len(value) / 3
	second := first * 2
	return []string{value[:first], value[first:second], value[second:]}
}

func engineAttachmentPDF(t *testing.T) []byte {
	t.Helper()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(
		&output,
		"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), xref,
	)
	return output.Bytes()
}
