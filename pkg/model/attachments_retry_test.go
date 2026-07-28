package model

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/greenpau/agentx/pkg/attachment"
)

func TestAzureMediaRequestErrorsSealDiagnosticsAndRequireMediaEvidence(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_error_seal", "screen.png", attachment.KindImage,
		attachment.MIMEPNG, pngBytes,
	)
	encoded := base64.StdEncoding.EncodeToString(pngBytes)
	fragments := make([]string, 0, (len(encoded)+15)/16)
	for offset := 0; offset < len(encoded); offset += 16 {
		end := offset + 16
		if end > len(encoded) {
			end = len(encoded)
		}
		fragments = append(fragments, encoded[offset:end])
	}
	reflected := "DATA:IMAGE/PNG;BASE64,\n" + strings.Join(fragments, "\n!")

	for _, test := range []struct {
		name         string
		code         string
		param        string
		wantRejected bool
		wantMessage  string
	}{
		{
			name: "unrelated invalid request", code: "invalid_request",
			param: "tools[0].parameters", wantMessage: "provider request failed",
		},
		{
			name: "explicit media rejection", code: "media_rejected",
			wantRejected: true, wantMessage: "provider rejected attachment input",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			body, err := json.Marshal(map[string]any{
				"error": map[string]any{
					"code": test.code, "type": "invalid_request_error",
					"param": test.param, "message": reflected,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			options := noWaitOptions()
			options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header: http.Header{
						"x-request-id": []string{fragments[0]},
					},
					Body: io.NopCloser(strings.NewReader(string(body))),
				}, nil
			})}
			client := newModelMediaTestClient(t, "gpt-5.6-sol", "", options)
			source := newStaticAttachmentSource(modelTestResolved{
				manifest: manifest, data: pngBytes,
			})
			_, err = client.Stream(t.Context(), Request{
				Input: []Item{{
					Type: ItemMessage, Role: RoleUser,
					Content: []Content{modelTestContent(ContentInputImage, manifest)},
				}},
				AttachmentSource: source,
			})
			var providerError *ProviderError
			if !errors.As(err, &providerError) {
				t.Fatalf("sealed media error = %v", err)
			}
			if providerError.MediaRejected != test.wantRejected ||
				providerError.Message != test.wantMessage ||
				providerError.Code != "" ||
				providerError.Type != "" ||
				providerError.Param != "" ||
				providerError.RequestID != "" {
				t.Fatalf("sealed media error = %#v", providerError)
			}
			if calls.Load() != 1 || source.callCount() != 1 {
				t.Fatalf(
					"sealed media error calls=%d resolves=%d",
					calls.Load(), source.callCount(),
				)
			}
			exported, marshalErr := json.Marshal(providerError)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			public := string(exported) + "\n" + err.Error()
			if strings.Contains(public, encoded) ||
				strings.Contains(strings.ToLower(public), "data:image/png;base64,") {
				t.Fatalf("sealed media error exposed request data: %s", public)
			}
			for _, fragment := range fragments {
				if len(fragment) >= 8 && strings.Contains(public, fragment) {
					t.Fatalf("sealed media error exposed wrapped fragment %q: %s", fragment, public)
				}
			}
		})
	}

	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", noWaitOptions())
	for _, test := range []struct {
		name         string
		code         string
		wantRejected bool
	}{
		{name: "unrelated SSE failure", code: "invalid_request"},
		{name: "media SSE failure", code: "media_rejected", wantRejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &azureStream{client: client, mediaRequest: true}
			providerError := stream.eventError(&azureError{
				Code: test.code, Type: "invalid_request_error",
				Param: "tools[0].parameters", Message: reflected,
			}, azureEventEnvelope{}, "response_failed")
			if providerError.MediaRejected != test.wantRejected ||
				providerError.Code != "" ||
				providerError.Param != "" ||
				strings.Contains(providerError.Error(), fragments[0]) {
				t.Fatalf("sealed SSE media error = %#v", providerError)
			}
		})
	}
}

func TestAzureMediaRejectionNeverRetriesEvenWhenHeaderRequestsRetry(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_no_retry", "screen.png", attachment.KindImage,
		attachment.MIMEPNG, pngBytes,
	)
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			options := noWaitOptions()
			options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				header := make(http.Header)
				header.Set("x-should-retry", "true")
				return &http.Response{
					StatusCode: status,
					Header:     header,
					Body: io.NopCloser(strings.NewReader(
						`{"error":{"code":"media_rejected","message":"unsupported media","type":"invalid_request_error"}}`,
					)),
				}, nil
			})}
			client := newModelMediaTestClient(t, "gpt-5.6-sol", "", options)
			source := newStaticAttachmentSource(modelTestResolved{
				manifest: manifest, data: pngBytes,
			})
			_, err := client.Stream(t.Context(), Request{
				Input: []Item{{
					Type: ItemMessage, Role: RoleUser,
					Content: []Content{modelTestContent(ContentInputImage, manifest)},
				}},
				AttachmentSource: source,
			})
			var providerError *ProviderError
			if !errors.As(err, &providerError) ||
				!providerError.MediaRejected ||
				providerError.Retryable {
				t.Fatalf("media rejection = %#v, err=%v", providerError, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("media rejection made %d provider calls, want exactly 1", calls.Load())
			}
			if source.callCount() != 1 {
				t.Fatalf("media rejection resolved attachment %d times, want 1", source.callCount())
			}
		})
	}
}

func TestAzureMediaBearingTerminalSSEErrorIsAuthoritativeAndNeverRetried(t *testing.T) {
	pngBytes := modelTestPNG(t, 1, 1)
	manifest := modelTestManifestForBytes(
		"att_sse_rejection", "screen.png", attachment.KindImage,
		attachment.MIMEPNG, pngBytes,
	)
	source := newStaticAttachmentSource(modelTestResolved{
		manifest: manifest, data: pngBytes,
	})
	var calls atomic.Int32
	options := noWaitOptions()
	options.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_media_failed\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
					"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_media_failed\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
					"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_media_failed\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"media_rejected\",\"message\":\"media rejected\",\"type\":\"invalid_request_error\"}}}\n\n",
			)),
		}, nil
	})}
	client := newModelMediaTestClient(t, "gpt-5.6-sol", "", options)
	stream, err := client.Stream(t.Context(), Request{
		Input: []Item{{
			Type: ItemMessage, Role: RoleUser,
			Content: []Content{modelTestContent(ContentInputImage, manifest)},
		}},
		AttachmentSource: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := Drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("terminal SSE media rejection made %d calls, want 1", calls.Load())
	}
	if source.callCount() != 1 {
		t.Fatalf("terminal SSE media rejection resolved attachment %d times, want 1", source.callCount())
	}
	if len(events) != 3 ||
		events[0].Type != EventResponseCreated ||
		events[1].Type != EventResponseInProgress ||
		events[2].Type != EventError ||
		events[2].Error == nil {
		t.Fatalf("terminal SSE media events = %#v", events)
	}
	providerError := events[2].Error
	if providerError.Param != "" ||
		!providerError.MediaRejected ||
		providerError.Retryable ||
		!IsMediaRejection(providerError) {
		t.Fatalf("terminal SSE media rejection = %#v", providerError)
	}
}
