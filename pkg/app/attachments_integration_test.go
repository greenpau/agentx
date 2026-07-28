package app

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
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/surface"
	"go.uber.org/zap"
)

const attachmentTestPromptUUID = "123e4567-e89b-42d3-a456-426614174000"

func TestStructuredAttachmentImportCommitAndAttachmentOnlyTurn(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 2, 2)
	manifest := appAttachmentManifest(
		"att_stream_image", attachment.KindImage, "screen.png",
		attachment.MIMEPNG, pngBytes,
	)

	var providerCalls atomic.Int32
	var providerBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		var err error
		providerBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		writeAttachmentCompletion(writer, "stream attachment accepted")
	}))
	defer server.Close()

	workspace := t.TempDir()
	agentxHome, _ := configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"stream-attachment-test-key", "v1",
	)
	rawDigest := sha256.Sum256(pngBytes)
	records := [][]byte{
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "begin",
			"prompt_uuid": attachmentTestPromptUUID,
			"upload_id":   "upl_stream_image", "attachment_id": manifest.AttachmentID,
			"name": manifest.Name, "size_bytes": len(pngBytes),
			"mime_type": manifest.MIMEType, "sha256": hex.EncodeToString(rawDigest[:]),
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "chunk",
			"upload_id": "upl_stream_image", "sequence": 0,
			"data": base64.StdEncoding.EncodeToString(pngBytes),
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "commit",
			"upload_id": "upl_stream_image",
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "commit",
			"upload_id": "upl_stream_image",
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "user", "uuid": attachmentTestPromptUUID,
			"message": surface.UserMessage{
				Role: "user", ContentVersion: surface.UserContentVersionAttachments,
				Content: []surface.UserContent{{
					Type: surface.UserContentAttachment, Attachment: &manifest,
				}},
			},
		}),
	}
	input := bytes.NewBuffer(bytes.Join(records, []byte{'\n'}))
	input.WriteByte('\n')

	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--session-id", "ses_stream_attachment",
		"--output-format", "stream-json", "--input-format", "stream-json",
		"--replay-user-messages", "--cwd", workspace, "--max-turns", "1",
	}, input, &output, &diagnostics)
	if err != nil {
		t.Fatalf("Run: %v; output=%s diagnostics=%s", err, output.String(), diagnostics.String())
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}

	var providerRequest map[string]any
	if err := json.Unmarshal(providerBody, &providerRequest); err != nil {
		t.Fatal(err)
	}
	inputItems, ok := providerRequest["input"].([]any)
	if !ok || len(inputItems) != 1 {
		t.Fatalf("provider input = %#v", providerRequest["input"])
	}
	content, ok := inputItems[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("attachment-only provider content = %#v", inputItems[0])
	}
	imageBlock, ok := content[0].(map[string]any)
	if !ok || imageBlock["type"] != "input_image" ||
		imageBlock["detail"] != "auto" ||
		imageBlock["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("attachment-only provider block = %#v", content[0])
	}

	outputRecords := decodeNDJSONRecords(t, output.String())
	var (
		sawCapability bool
		sawAccepted   bool
		sawCommitted  bool
		sawDuplicate  bool
		sawReplay     bool
		sawResult     bool
		terminalAcks  int
	)
	for _, record := range outputRecords {
		switch record["type"] {
		case "system":
			if record["subtype"] == "init" {
				assertAttachmentCapabilityRecord(t, record)
				sawCapability = true
			}
		case "attachment_import_result":
			switch record["status"] {
			case "accepted":
				sawAccepted = record["terminal"] == false
			case "committed":
				sawCommitted = record["terminal"] == true &&
					record["prompt_uuid"] == attachmentTestPromptUUID
			}
			if record["terminal"] == true {
				terminalAcks++
			}
		case "attachment_import_rejected":
			sawDuplicate = record["reason"] == "upload_already_terminal" &&
				record["terminal"] == false
		case "user":
			sawReplay = record["uuid"] == attachmentTestPromptUUID &&
				record["isReplay"] == true
			replayBytes, err := json.Marshal(record["message"])
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := surface.DecodeUserMessage(replayBytes)
			if err != nil {
				t.Fatalf("decode replay attachment message: %v; message=%s", err, replayBytes)
			}
			if !bytes.Contains(replayBytes, []byte(`"attachment_id":"att_stream_image"`)) ||
				!bytes.Contains(replayBytes, []byte(`"storage_id":"`+manifest.StorageID+`"`)) ||
				!bytes.Contains(replayBytes, []byte(`"content_version":1`)) ||
				bytes.Contains(replayBytes, []byte(`"data"`)) ||
				bytes.Contains(replayBytes, []byte(`"path"`)) ||
				len(roundTrip.Content) != 1 ||
				roundTrip.Content[0].Attachment == nil ||
				*roundTrip.Content[0].Attachment != manifest {
				t.Fatalf("unsafe replay attachment metadata: %s", replayBytes)
			}
		case "result":
			sawResult = record["prompt_uuid"] == attachmentTestPromptUUID &&
				record["subtype"] == "success"
		}
	}
	if !sawCapability || !sawAccepted || !sawCommitted || !sawDuplicate ||
		!sawReplay || !sawResult || terminalAcks != 1 {
		t.Fatalf(
			"stream lifecycle capability=%t accepted=%t committed=%t duplicate=%t replay=%t result=%t terminal=%d; output=%s",
			sawCapability, sawAccepted, sawCommitted, sawDuplicate,
			sawReplay, sawResult, terminalAcks, output.String(),
		)
	}
	encodedPayload := base64.StdEncoding.EncodeToString(pngBytes)
	if strings.Contains(output.String(), encodedPayload) ||
		strings.Contains(diagnostics.String(), encodedPayload) ||
		strings.Contains(output.String(), "data:image/") ||
		strings.Contains(diagnostics.String(), "data:image/") {
		t.Fatalf("stream output exposed attachment bytes: output=%s diagnostics=%s", output.String(), diagnostics.String())
	}
	transcriptBytes, err := os.ReadFile(filepath.Join(
		testSessionDir(agentxHome, workspace, "ses_stream_attachment"),
		"transcript.jsonl",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(
		transcriptBytes,
		[]byte(`"prompt_id":"`+attachmentTestPromptUUID+`"`),
	) {
		t.Fatalf("transcript lost stable prompt UUID: %s", transcriptBytes)
	}
	if bytes.Contains(transcriptBytes, []byte(encodedPayload)) ||
		bytes.Contains(transcriptBytes, []byte("data:image/")) {
		t.Fatalf("transcript exposed attachment bytes: %s", transcriptBytes)
	}
}

func TestRepeatableCLIAttachmentsPreserveImagePDFOrderAndDoNotLeakPaths(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 2, 1)
	pdfBytes := appAttachmentPDF(t, 2)
	sourceRoot := t.TempDir()
	imagePath := filepath.Join(sourceRoot, "private screenshot.png")
	pdfPath := filepath.Join(sourceRoot, "private report.pdf")
	if err := os.WriteFile(imagePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var providerCalls atomic.Int32
	var providerBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		var err error
		providerBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		writeAttachmentCompletion(writer, "ordered")
	}))
	defer server.Close()

	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"cli-attachment-test-key", "v1",
	)
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--debug", "--no-session-persistence",
		"--output-format", "stream-json", "--cwd", workspace, "--max-turns", "1",
		"--attachment", imagePath, "--attachment", pdfPath,
		"Compare these in their submitted order.",
	}, strings.NewReader(""), &output, &diagnostics)
	if err != nil {
		t.Fatalf("Run: %v; output=%s diagnostics=%s", err, output.String(), diagnostics.String())
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}

	var request map[string]any
	if err := json.Unmarshal(providerBody, &request); err != nil {
		t.Fatal(err)
	}
	items := request["input"].([]any)
	content := items[0].(map[string]any)["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("provider content = %#v", content)
	}
	text := content[0].(map[string]any)
	imageBlock := content[1].(map[string]any)
	fileBlock := content[2].(map[string]any)
	if text["type"] != "input_text" ||
		text["text"] != "Compare these in their submitted order." ||
		imageBlock["type"] != "input_image" ||
		fileBlock["type"] != "input_file" ||
		fileBlock["filename"] != filepath.Base(pdfPath) {
		t.Fatalf("ordered provider content = %#v", content)
	}
	if imageBlock["image_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes) ||
		fileBlock["file_data"] != "data:application/pdf;base64,"+base64.StdEncoding.EncodeToString(pdfBytes) {
		t.Fatalf("provider media payloads = %#v", content)
	}
	records := decodeNDJSONRecords(t, output.String())
	if len(records) == 0 {
		t.Fatal("stream output is empty")
	}
	assertAttachmentCapabilityRecord(t, records[0])
	var result map[string]any
	for _, record := range records {
		if record["type"] == "result" {
			result = record
		}
	}
	if result == nil || result["subtype"] != "success" || result["prompt_uuid"] == "" {
		t.Fatalf("CLI attachment result = %#v; output=%s", result, output.String())
	}
	for _, forbidden := range []string{
		imagePath, pdfPath,
		base64.StdEncoding.EncodeToString(pngBytes),
		base64.StdEncoding.EncodeToString(pdfBytes),
		"data:image/", "data:application/pdf",
		"cli-attachment-test-key",
	} {
		if strings.Contains(output.String(), forbidden) || strings.Contains(diagnostics.String(), forbidden) {
			t.Fatalf("public output exposed %q: output=%s diagnostics=%s", forbidden, output.String(), diagnostics.String())
		}
	}
}

func TestCLIAttachmentOnlyTurnReachesProviderWithoutSyntheticText(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 1, 1)
	sourcePath := filepath.Join(t.TempDir(), "only.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		providerCalls atomic.Int32
		providerBody  []byte
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		var err error
		providerBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		writeAttachmentCompletion(writer, "attachment only")
	}))
	defer server.Close()
	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"attachment-only-test-key", "v1",
	)
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--bare", "--no-session-persistence", "--cwd", workspace,
		"--attachment", sourcePath,
	}, strings.NewReader(""), &output, &diagnostics)
	if err != nil {
		t.Fatalf("Run: %v; output=%s diagnostics=%s", err, output.String(), diagnostics.String())
	}
	if providerCalls.Load() != 1 || output.String() != "attachment only\n" {
		t.Fatalf("attachment-only result calls=%d output=%q diagnostics=%q", providerCalls.Load(), output.String(), diagnostics.String())
	}
	var request map[string]any
	if err := json.Unmarshal(providerBody, &request); err != nil {
		t.Fatal(err)
	}
	input := request["input"].([]any)
	content := input[0].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "input_image" {
		t.Fatalf("attachment-only CLI provider content = %#v", content)
	}
}

func TestAttachmentCapabilityPresenceAndTextOnlyFallback(t *testing.T) {
	textOnly := newSDKWireSession(t)
	for name, encode := range map[string]func(*runtimeSession) (map[string]any, error){
		"init": func(session *runtimeSession) (map[string]any, error) {
			var output bytes.Buffer
			err := encodeSDKInit(surface.NewEncoder(&output), session, cli.Options{})
			var result map[string]any
			if err == nil {
				err = json.Unmarshal(output.Bytes(), &result)
			}
			return result, err
		},
		"initialize": sdkInitializeResponse,
	} {
		t.Run(name, func(t *testing.T) {
			record, err := encode(textOnly)
			if err != nil {
				t.Fatal(err)
			}
			if _, present := record["input_capabilities"]; present {
				t.Fatalf("text-only capability must be absent: %#v", record["input_capabilities"])
			}

			capability, err := attachment.CapabilityFor(attachment.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			media := newSDKWireSession(t)
			media.inputMediaEnabled = true
			media.inputMedia = model.InputMediaCapability{
				Attachment: capability, MaxRequestItems: 100,
				MaxEncodedBytes: 56 << 20, MaxRequestBytes: 64 << 20,
			}
			record, err = encode(media)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			assertAttachmentCapabilityRecord(t, decoded)
		})
	}
}

func TestUnsupportedAttachmentConfigurationFailsBeforeProviderIO(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 1, 1)
	sourcePath := filepath.Join(t.TempDir(), "unsupported.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		writeAttachmentCompletion(writer, "must not run")
	}))
	defer server.Close()
	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"unsupported-attachment-test-key", "2026-07-01-preview",
	)
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--no-session-persistence",
		"--output-format", "stream-json", "--cwd", workspace,
		"--attachment", sourcePath, "inspect",
	}, strings.NewReader(""), &output, &diagnostics)
	if err == nil {
		t.Fatalf("unsupported attachment configuration succeeded: output=%s", output.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("unsupported attachment reached provider %d times", providerCalls.Load())
	}
	records := decodeNDJSONRecords(t, output.String())
	if len(records) < 2 || records[0]["type"] != "system" ||
		records[0]["subtype"] != "init" {
		t.Fatalf("unsupported attachment output = %#v", records)
	}
	if _, present := records[0]["input_capabilities"]; present {
		t.Fatalf("text-only init advertised attachments: %#v", records[0])
	}
	var sawRejected bool
	for _, record := range records {
		if record["type"] == "result" &&
			record["subtype"] == "error_during_execution" &&
			record["stop_reason"] == "input_error" &&
			record["prompt_uuid"] != "" {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatalf("unsupported attachment lacked correlated rejection: %s", output.String())
	}
	for _, forbidden := range []string{
		sourcePath, base64.StdEncoding.EncodeToString(pngBytes),
	} {
		if strings.Contains(output.String(), forbidden) ||
			strings.Contains(diagnostics.String(), forbidden) ||
			strings.Contains(err.Error(), forbidden) {
			t.Fatalf("unsupported attachment failure exposed %q", forbidden)
		}
	}
}

func TestCLIAttachmentCannotBecomeLocalSlashCommand(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 1, 1)
	sourcePath := filepath.Join(t.TempDir(), "slash.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		writeAttachmentCompletion(writer, "must not run")
	}))
	defer server.Close()
	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"slash-attachment-test-key", "v1",
	)
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--no-session-persistence",
		"--output-format", "stream-json", "--cwd", workspace,
		"--attachment", sourcePath, "/cost",
	}, strings.NewReader(""), &output, &diagnostics)
	if err == nil {
		t.Fatalf("slash command with attachment succeeded: output=%s", output.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("slash attachment reached provider %d times", providerCalls.Load())
	}
	if strings.Contains(output.String(), `"subtype":"local_command_output"`) {
		t.Fatalf("attachment-bearing input became a local command: %s", output.String())
	}
	var rejected bool
	for _, record := range decodeNDJSONRecords(t, output.String()) {
		if record["type"] == "result" &&
			record["stop_reason"] == "input_error" &&
			record["prompt_uuid"] != "" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("slash attachment lacked correlated input rejection: %s", output.String())
	}
}

func TestUserPromptHookPreservesTypedAttachmentOrderAndReceivesMetadataOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell capture assertion is Unix-specific")
	}

	pngBytes := appAttachmentPNG(t, 2, 2)
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "private-hook-source.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := attachment.OpenStore(filepath.Join(t.TempDir(), "attachments"), attachment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	manifest, err := store.ImportFile(t.Context(), attachment.FileImport{
		Path: sourcePath, AttachmentID: "att_hook_image", Name: "hook-image.png",
		MIMEType: attachment.MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}

	hookRoot := t.TempDir()
	capturePath := filepath.Join(hookRoot, "captured.json")
	snapshot := extensions.NewHookManagerForEvents(
		extensions.HookUserPromptSubmit,
	).Reload([]extensions.HookDescriptor{{
		ID: "attachment-metadata", Event: extensions.HookUserPromptSubmit,
		Kind: extensions.HookKindCommand, Shell: "sh",
		Command: `IFS= read -r payload; printf '%s' "$payload" > "$AGENTX_PLUGIN_ROOT/captured.json"; printf '%s' '{"hookSpecificOutput":{"hook_event_name":"UserPromptSubmit","additionalContext":"hook-approved"}}'`,
		Source:  extensions.SourcePlugin, PluginRoot: hookRoot, Timeout: time.Second,
	}})
	if len(snapshot.Diagnostics) != 0 || len(snapshot.Hooks) != 1 {
		t.Fatalf("hook snapshot = %#v", snapshot)
	}
	runner := extensions.NewHookRunner()
	runner.ProjectRoot = hookRoot

	provider := &attachmentHookCaptureProvider{}
	query, err := engine.New(engine.Config{
		SessionID: "ses_attachment_hook", Model: "gpt-5.6-sol",
		Provider: provider, Capabilities: sdkWireCapabilities{},
		Attachments: store, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &runtimeSession{
		engine: query, attachments: store, workspace: hookRoot,
		permissionMode: "default", sanitize: func(value string) string { return value },
		logger: zap.NewNop(),
		services: runtimeServices{extensions: runtimeExtensions{
			hooks: snapshot, runner: runner,
		}},
	}
	message := protocol.Message{
		Role: protocol.RoleUser,
		Content: []protocol.ContentBlock{
			protocol.TextBlock("before"),
			protocol.AttachmentBlock(manifest),
			protocol.TextBlock("after"),
		},
	}
	outcome, err := session.submitMessage(t.Context(), message, attachmentTestPromptUUID)
	if err != nil {
		t.Fatalf("submit attachment message through hook: %v", err)
	}
	if outcome.Status != protocol.TurnResultSuccess {
		t.Fatalf("hooked attachment outcome = %#v", outcome)
	}

	requests := provider.Requests()
	if len(requests) != 1 || len(requests[0].Input) != 1 {
		t.Fatalf("provider requests = %#v", requests)
	}
	content := requests[0].Input[0].Content
	if len(content) != 4 ||
		content[0].Type != model.ContentInputText || content[0].Text != "before" ||
		content[1].Type != model.ContentInputImage || content[1].Manifest == nil ||
		*content[1].Manifest != manifest ||
		content[2].Type != model.ContentInputText || content[2].Text != "after" ||
		content[3].Type != model.ContentInputText ||
		!strings.Contains(content[3].Text, "<hook_context>\nhook-approved\n</hook_context>") {
		t.Fatalf("hook changed ordered typed content = %#v", content)
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(captured, &envelope); err != nil {
		t.Fatalf("decode hook envelope: %v; payload=%s", err, captured)
	}
	metadata, ok := envelope["attachments"].([]any)
	if !ok || len(metadata) != 1 {
		t.Fatalf("hook attachment metadata = %#v", envelope["attachments"])
	}
	item, ok := metadata[0].(map[string]any)
	if !ok ||
		item["attachment_id"] != string(manifest.AttachmentID) ||
		item["kind"] != string(manifest.Kind) ||
		item["name"] != manifest.Name ||
		item["mime_type"] != manifest.MIMEType ||
		item["size_bytes"] != float64(manifest.SizeBytes) ||
		item["sha256"] != manifest.SHA256 {
		t.Fatalf("hook attachment metadata item = %#v", metadata[0])
	}
	for _, forbidden := range []string{
		sourcePath,
		manifest.StorageID,
		base64.StdEncoding.EncodeToString(pngBytes),
		"data:image/",
		`"storage_id"`,
		`"path"`,
		`"data"`,
	} {
		if strings.Contains(string(captured), forbidden) {
			t.Fatalf("hook payload exposed forbidden attachment material %q: %s", forbidden, captured)
		}
	}
}

func TestInvalidPriorityNowAttachmentDoesNotInterruptActiveTurn(t *testing.T) {
	store, err := attachment.OpenStore(filepath.Join(t.TempDir(), "attachments"), attachment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	session := newSDKWireSession(t)
	initializeAttachmentTestSession(session, store)

	manifest := attachment.Manifest{
		AttachmentID: "att_not_committed", Kind: attachment.KindImage,
		Name: "missing.png", MIMEType: attachment.MIMEPNG, SizeBytes: 1,
		SHA256:    strings.Repeat("a", 64),
		StorageID: "blob_sha256_" + strings.Repeat("a", 64),
	}
	message, err := json.Marshal(surface.UserMessage{
		Role: "user", ContentVersion: surface.UserContentVersionAttachments,
		Content: []surface.UserContent{{
			Type: surface.UserContentAttachment, Attachment: &manifest,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(surface.InputEnvelope{
		Type: "user", UUID: attachmentTestPromptUUID, Priority: "now",
		Message: message,
	})
	if err != nil {
		t.Fatal(err)
	}

	activeContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	active := &activeTurn{}
	if !active.set(cancel) {
		t.Fatal("active turn was unexpectedly unavailable")
	}
	defer active.clear()
	queue := newInputQueue()
	var output bytes.Buffer
	readStructuredInput(
		t.Context(), bytes.NewReader(append(wire, '\n')), io.Discard,
		surface.NewEncoder(&output), surface.NewControlBroker(),
		queue, active, session, false,
	)
	select {
	case <-activeContext.Done():
		t.Fatal("invalid priority-now attachment interrupted active work")
	default:
	}
	if _, err := queue.next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("invalid attachment entered queue: %v", err)
	}
	if queue.rejection() == nil {
		t.Fatal("invalid attachment rejection was not retained")
	}
	records := decodeNDJSONRecords(t, output.String())
	if len(records) != 1 ||
		records[0]["type"] != "result" ||
		records[0]["stop_reason"] != "input_error" ||
		records[0]["prompt_uuid"] != attachmentTestPromptUUID {
		t.Fatalf("rejection output = %#v", records)
	}
}

func TestStructuredAttachmentUploadEOFSendsOneTerminalAbortWithoutProviderCall(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 1, 1)
	digest := sha256.Sum256(pngBytes)
	var providerCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		writeAttachmentCompletion(writer, "must not run")
	}))
	defer server.Close()
	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"eof-attachment-test-key", "v1",
	)
	begin := appAttachmentJSON(t, map[string]any{
		"type": "attachment_import", "version": 1, "operation": "begin",
		"prompt_uuid": attachmentTestPromptUUID,
		"upload_id":   "upl_eof_image", "attachment_id": "att_eof_image",
		"name": "eof.png", "size_bytes": len(pngBytes),
		"mime_type": attachment.MIMEPNG, "sha256": hex.EncodeToString(digest[:]),
	})
	var output, diagnostics bytes.Buffer
	err := Run(testProviderContext(t, server), []string{
		"--print", "--bare", "--no-session-persistence",
		"--output-format", "stream-json", "--input-format", "stream-json",
		"--cwd", workspace,
	}, bytes.NewReader(append(begin, '\n')), &output, &diagnostics)
	if err == nil {
		t.Fatalf("EOF without a user turn succeeded: output=%s", output.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("incomplete upload reached provider %d times", providerCalls.Load())
	}
	terminal := 0
	sawAccepted := false
	sawEOF := false
	for _, record := range decodeNDJSONRecords(t, output.String()) {
		if record["type"] != "attachment_import_result" {
			continue
		}
		if record["status"] == "accepted" && record["terminal"] == false {
			sawAccepted = true
		}
		if record["status"] == "aborted" &&
			record["terminal"] == true &&
			record["reason"] == "eof" {
			sawEOF = true
		}
		if record["terminal"] == true {
			terminal++
		}
	}
	if !sawAccepted || !sawEOF || terminal != 1 {
		t.Fatalf("EOF upload lifecycle accepted=%t eof=%t terminal=%d; output=%s", sawAccepted, sawEOF, terminal, output.String())
	}
}

func TestStreamPromptLedgerRejectsNinthSequentialUploadBeforeChunks(t *testing.T) {
	store, err := attachment.OpenStore(filepath.Join(t.TempDir(), "attachments"), attachment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	session := newSDKWireSession(t)
	initializeAttachmentTestSession(session, store)

	pngBytes := appAttachmentPNG(t, 1, 1)
	digest := sha256.Sum256(pngBytes)
	digestText := hex.EncodeToString(digest[:])
	encoded := base64.StdEncoding.EncodeToString(pngBytes)
	var output bytes.Buffer
	encoder := surface.NewEncoder(&output)
	for index := 0; index < attachment.DefaultMaxAttachmentsPerMessage; index++ {
		uploadID := attachment.UploadID(fmt.Sprintf("upl_ledger_%02d", index))
		attachmentID := attachment.ID(fmt.Sprintf("att_ledger_%02d", index))
		if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
			Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportBegin,
			PromptUUID: attachmentTestPromptUUID,
			UploadID:   uploadID, AttachmentID: attachmentID,
			Name: fmt.Sprintf("%02d.png", index), SizeBytes: int64(len(pngBytes)),
			MIMEType: attachment.MIMEPNG, SHA256: digestText,
		}); err != nil {
			t.Fatalf("begin %d: %v", index, err)
		}
		if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
			Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportChunk,
			UploadID: uploadID, Sequence: 0, Data: encoded,
		}); err != nil {
			t.Fatalf("chunk %d: %v", index, err)
		}
		if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
			Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportCommit,
			UploadID: uploadID,
		}); err != nil {
			t.Fatalf("commit %d: %v", index, err)
		}
	}

	ninthUploadID := attachment.UploadID("upl_ledger_08")
	if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
		Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportBegin,
		PromptUUID: attachmentTestPromptUUID,
		UploadID:   ninthUploadID, AttachmentID: "att_ledger_08",
		Name: "08.png", SizeBytes: int64(len(pngBytes)),
		MIMEType: attachment.MIMEPNG, SHA256: digestText,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Chunk(t.Context(), ninthUploadID, 0, encoded); !errors.Is(err, attachment.ErrUploadState) {
		t.Fatalf("ninth upload reserved chunk state: %v", err)
	}

	accepted := 0
	committed := 0
	ninthTerminal := 0
	for _, record := range decodeNDJSONRecords(t, output.String()) {
		if record["type"] != "attachment_import_result" {
			continue
		}
		switch record["status"] {
		case "accepted":
			accepted++
		case "committed":
			committed++
		case "failed":
			if record["upload_id"] == string(ninthUploadID) &&
				record["terminal"] == true &&
				record["reason"] == "resource_limit" &&
				record["prompt_uuid"] == attachmentTestPromptUUID {
				ninthTerminal++
			}
		}
	}
	if accepted != attachment.DefaultMaxAttachmentsPerMessage ||
		committed != attachment.DefaultMaxAttachmentsPerMessage ||
		ninthTerminal != 1 {
		t.Fatalf(
			"prompt ledger accepted=%d committed=%d ninth_terminal=%d; output=%s",
			accepted, committed, ninthTerminal, output.String(),
		)
	}
	if len(session.attachmentPrompts) != attachment.DefaultMaxAttachmentsPerMessage {
		t.Fatalf("prompt ledger retained %d attachments, want %d", len(session.attachmentPrompts), attachment.DefaultMaxAttachmentsPerMessage)
	}
}

func TestStreamUploadTerminalLedgerRejectsOverflowWithoutRetainingIt(t *testing.T) {
	limits := attachment.DefaultLimits()
	limits.MaxConcurrentUploads = 2
	limits.MaxUploadsPerSession = 2
	store, err := attachment.OpenStore(
		filepath.Join(t.TempDir(), "attachments"),
		attachment.Options{Limits: limits},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	session := newSDKWireSession(t)
	initializeAttachmentTestSession(session, store)

	pngBytes := appAttachmentPNG(t, 1, 1)
	digest := sha256.Sum256(pngBytes)
	digestText := hex.EncodeToString(digest[:])
	var output bytes.Buffer
	encoder := surface.NewEncoder(&output)
	for index := 0; index < limits.MaxUploadsPerSession; index++ {
		uploadID := attachment.UploadID(fmt.Sprintf("upl_terminal_%02d", index))
		if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
			Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportBegin,
			PromptUUID:   attachmentTestPromptUUID,
			UploadID:     uploadID,
			AttachmentID: attachment.ID(fmt.Sprintf("att_terminal_%02d", index)),
			Name:         fmt.Sprintf("%02d.png", index),
			SizeBytes:    int64(len(pngBytes)),
			MIMEType:     attachment.MIMEPNG,
			SHA256:       digestText,
		}); err != nil {
			t.Fatalf("begin %d: %v", index, err)
		}
		if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
			Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportAbort,
			UploadID: uploadID,
		}); err != nil {
			t.Fatalf("abort %d: %v", index, err)
		}
	}

	overflowID := attachment.UploadID("upl_terminal_overflow")
	if err := session.handleAttachmentImport(t.Context(), encoder, surface.AttachmentImport{
		Version: attachment.ProtocolVersion, Operation: surface.AttachmentImportBegin,
		PromptUUID: attachmentTestPromptUUID,
		UploadID:   overflowID, AttachmentID: "att_terminal_overflow",
		Name: "overflow.png", SizeBytes: int64(len(pngBytes)),
		MIMEType: attachment.MIMEPNG, SHA256: digestText,
	}); err != nil {
		t.Fatal(err)
	}

	accepted := 0
	terminal := 0
	rejected := 0
	for _, record := range decodeNDJSONRecords(t, output.String()) {
		switch record["type"] {
		case "attachment_import_result":
			if record["status"] == "accepted" {
				accepted++
			}
			if record["terminal"] == true {
				terminal++
			}
		case "attachment_import_rejected":
			if record["upload_id"] == string(overflowID) &&
				record["terminal"] == false &&
				record["reason"] == "resource_limit" {
				rejected++
			}
		}
	}
	if accepted != limits.MaxUploadsPerSession ||
		terminal != limits.MaxUploadsPerSession ||
		rejected != 1 {
		t.Fatalf(
			"terminal ledger accepted=%d terminal=%d rejected=%d; output=%s",
			accepted, terminal, rejected, output.String(),
		)
	}
	if got := len(session.uploadTerminalSent); got != limits.MaxUploadsPerSession {
		t.Fatalf("terminal ledger retained %d entries, want %d", got, limits.MaxUploadsPerSession)
	}
	if session.uploadTerminalSent[overflowID] {
		t.Fatalf("overflow upload entered terminal ledger")
	}
	if _, exists := session.uploadPrompts[overflowID]; exists {
		t.Fatalf("overflow upload entered active ledger")
	}
}

func TestAttachmentSessionResumeForkAndTamperIsolation(t *testing.T) {
	pngBytes := appAttachmentPNG(t, 2, 2)
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "selected once.png")
	if err := os.WriteFile(sourcePath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pngBytes)
	digestText := hex.EncodeToString(digest[:])

	var (
		providerCalls  atomic.Int32
		bodyMu         sync.Mutex
		providerBodies [][]byte
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		bodyMu.Lock()
		providerBodies = append(providerBodies, body)
		bodyMu.Unlock()
		providerCalls.Add(1)
		writeAttachmentCompletion(writer, "continued")
	}))
	defer server.Close()

	workspace := t.TempDir()
	agentxHome, _ := configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"continuity-attachment-test-key", "v1",
	)
	providerContext := testProviderContext(t, server)
	base := []string{
		"--print", "--bare", "--output-format", "stream-json",
		"--cwd", workspace, "--max-turns", "1",
	}
	run := func(args []string) (string, string, error) {
		var output, diagnostics bytes.Buffer
		err := Run(providerContext, args, strings.NewReader(""), &output, &diagnostics)
		return output.String(), diagnostics.String(), err
	}

	sourceID := "ses_attachment_continuity"
	firstOutput, firstDiagnostics, err := run(append(
		append([]string(nil), base...),
		"--session-id", sourceID, "--attachment", sourcePath, "remember this image",
	))
	if err != nil {
		t.Fatalf("initial run: %v; output=%s diagnostics=%s", err, firstOutput, firstDiagnostics)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	resumeOutput, resumeDiagnostics, err := run(append(
		append([]string(nil), base...),
		"--resume", sourceID, "continue without the source path",
	))
	if err != nil {
		t.Fatalf("resume: %v; output=%s diagnostics=%s", err, resumeOutput, resumeDiagnostics)
	}
	forkOutput, forkDiagnostics, err := run(append(
		append([]string(nil), base...),
		"--resume", sourceID, "--fork-session", "continue in a fork",
	))
	if err != nil {
		t.Fatalf("fork: %v; output=%s diagnostics=%s", err, forkOutput, forkDiagnostics)
	}
	forkRecords := decodeNDJSONRecords(t, forkOutput)
	if len(forkRecords) == 0 {
		t.Fatal("fork emitted no initialization record")
	}
	childID, ok := forkRecords[0]["session_id"].(string)
	if !ok || childID == "" || childID == sourceID {
		t.Fatalf("fork session identity = %#v", forkRecords[0]["session_id"])
	}
	for _, sessionID := range []string{sourceID, childID} {
		sessionDirectory := testSessionDir(agentxHome, workspace, sessionID)
		transcriptBytes, err := os.ReadFile(filepath.Join(sessionDirectory, "transcript.jsonl"))
		if err != nil {
			t.Fatalf("read %s transcript: %v", sessionID, err)
		}
		for _, forbidden := range []string{
			sourcePath,
			base64.StdEncoding.EncodeToString(pngBytes),
			"data:image/png;base64,",
		} {
			if bytes.Contains(transcriptBytes, []byte(forbidden)) {
				t.Fatalf("%s transcript exposed %q", sessionID, forbidden)
			}
		}
		childBlob := filepath.Join(
			sessionDirectory, "attachments", "blobs", digestText+".blob",
		)
		info, err := os.Stat(childBlob)
		if err != nil || !info.Mode().IsRegular() ||
			info.Size() != int64(len(pngBytes)) {
			t.Fatalf("%s immutable blob = %v, %v", sessionID, info, err)
		}
	}

	sourceBlob := filepath.Join(
		testSessionDir(agentxHome, workspace, sourceID),
		"attachments", "blobs", digestText+".blob",
	)
	if err := os.WriteFile(sourceBlob, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	childOutput, childDiagnostics, err := run(append(
		append([]string(nil), base...),
		"--resume", childID, "the fork remains independent",
	))
	if err != nil {
		t.Fatalf("fork resume after source tamper: %v; output=%s diagnostics=%s", err, childOutput, childDiagnostics)
	}
	callsBeforeTamperedResume := providerCalls.Load()
	tamperedOutput, tamperedDiagnostics, err := run(append(
		append([]string(nil), base...),
		"--resume", sourceID, "must fail before provider I/O",
	))
	if err == nil {
		t.Fatalf("tampered source session resumed; output=%s diagnostics=%s", tamperedOutput, tamperedDiagnostics)
	}
	if providerCalls.Load() != callsBeforeTamperedResume {
		t.Fatalf("tampered attachment reached provider: before=%d after=%d", callsBeforeTamperedResume, providerCalls.Load())
	}
	if strings.Contains(err.Error(), sourceBlob) ||
		strings.Contains(tamperedOutput, sourceBlob) ||
		strings.Contains(tamperedDiagnostics, sourceBlob) {
		t.Fatalf("tamper failure exposed runtime path: err=%v output=%s diagnostics=%s", err, tamperedOutput, tamperedDiagnostics)
	}
	if providerCalls.Load() != 4 {
		t.Fatalf("provider calls = %d, want initial+resume+fork+child resume", providerCalls.Load())
	}

	bodyMu.Lock()
	defer bodyMu.Unlock()
	if len(providerBodies) != 4 {
		t.Fatalf("captured provider bodies = %d", len(providerBodies))
	}
	for index, body := range providerBodies {
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("body %d: %v", index, err)
		}
		if got := countProviderMedia(request); got != 1 {
			t.Fatalf("body %d retained %d media items, want 1: %s", index, got, body)
		}
		if strings.Contains(string(body), sourcePath) {
			t.Fatalf("body %d exposed original path: %s", index, body)
		}
	}
	for _, output := range []string{
		firstOutput, firstDiagnostics, resumeOutput, resumeDiagnostics,
		forkOutput, forkDiagnostics, childOutput, childDiagnostics,
	} {
		if strings.Contains(output, sourcePath) ||
			strings.Contains(output, base64.StdEncoding.EncodeToString(pngBytes)) {
			t.Fatalf("continuity output exposed source material: %s", output)
		}
	}
}

func TestResumedAndForkedAttachmentRejectDifferentPromptUUIDBeforeProviderIO(t *testing.T) {
	const differentPromptUUID = "223e4567-e89b-42d3-a456-426614174000"
	pngBytes := appAttachmentPNG(t, 1, 1)
	manifest := appAttachmentManifest(
		"att_prompt_owner", attachment.KindImage, "owned.png",
		attachment.MIMEPNG, pngBytes,
	)
	rawDigest := sha256.Sum256(pngBytes)
	var providerCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		writeAttachmentCompletion(writer, "owned")
	}))
	defer server.Close()
	workspace := t.TempDir()
	configureTestAgentXHome(
		t, server.URL, "gpt-5.6-sol", "gpt-5.6-sol",
		"prompt-owner-test-key", "v1",
	)
	providerContext := testProviderContext(t, server)
	base := []string{
		"--print", "--bare", "--output-format", "stream-json",
		"--input-format", "stream-json", "--cwd", workspace, "--max-turns", "1",
	}
	importRecords := [][]byte{
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "begin",
			"prompt_uuid": attachmentTestPromptUUID,
			"upload_id":   "upl_prompt_owner", "attachment_id": manifest.AttachmentID,
			"name": manifest.Name, "size_bytes": len(pngBytes),
			"mime_type": manifest.MIMEType, "sha256": hex.EncodeToString(rawDigest[:]),
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "chunk",
			"upload_id": "upl_prompt_owner", "sequence": 0,
			"data": base64.StdEncoding.EncodeToString(pngBytes),
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "attachment_import", "version": 1, "operation": "commit",
			"upload_id": "upl_prompt_owner",
		}),
		appAttachmentJSON(t, map[string]any{
			"type": "user", "uuid": attachmentTestPromptUUID,
			"message": surface.UserMessage{
				Role: "user", ContentVersion: surface.UserContentVersionAttachments,
				Content: []surface.UserContent{{
					Type: surface.UserContentAttachment, Attachment: &manifest,
				}},
			},
		}),
	}
	var firstOutput, firstDiagnostics bytes.Buffer
	if err := Run(
		providerContext,
		append(append([]string(nil), base...), "--session-id", "ses_attachment_prompt_owner"),
		bytes.NewReader(append(bytes.Join(importRecords, []byte{'\n'}), '\n')),
		&firstOutput, &firstDiagnostics,
	); err != nil {
		t.Fatalf("create durable attachment owner: %v; output=%s diagnostics=%s", err, firstOutput.String(), firstDiagnostics.String())
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("initial provider calls = %d, want 1", providerCalls.Load())
	}

	referenceMessage, err := json.Marshal(surface.UserMessage{
		Role: "user", ContentVersion: surface.UserContentVersionAttachments,
		Content: []surface.UserContent{{
			Type: surface.UserContentAttachment, Attachment: &manifest,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicateRecord := appAttachmentJSON(t, surface.InputEnvelope{
		Type: "user", UUID: attachmentTestPromptUUID, Message: referenceMessage,
	})
	var duplicateOutput, duplicateDiagnostics bytes.Buffer
	if err := Run(
		providerContext,
		append(
			append([]string(nil), base...),
			"--resume", "ses_attachment_prompt_owner", "--replay-user-messages",
		),
		bytes.NewReader(append(append([]byte(nil), duplicateRecord...), '\n')),
		&duplicateOutput, &duplicateDiagnostics,
	); err != nil {
		t.Fatalf("exact attachment prompt replay: %v; output=%s diagnostics=%s", err, duplicateOutput.String(), duplicateDiagnostics.String())
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("exact attachment prompt replay invoked provider %d times", providerCalls.Load())
	}
	var sawRoundTripReplay bool
	for _, record := range decodeNDJSONRecords(t, duplicateOutput.String()) {
		if record["type"] != "user" ||
			record["uuid"] != attachmentTestPromptUUID ||
			record["isReplay"] != true {
			continue
		}
		raw, err := json.Marshal(record["message"])
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := surface.DecodeUserMessage(raw)
		if err == nil &&
			len(replayed.Content) == 1 &&
			replayed.Content[0].Attachment != nil &&
			*replayed.Content[0].Attachment == manifest {
			sawRoundTripReplay = true
		}
	}
	if !sawRoundTripReplay {
		t.Fatalf("exact attachment prompt lacked round-trippable replay: %s", duplicateOutput.String())
	}

	referenceRecord := appAttachmentJSON(t, surface.InputEnvelope{
		Type: "user", UUID: differentPromptUUID, Message: referenceMessage,
	})
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "resume",
			args: append(
				append([]string(nil), base...),
				"--resume", "ses_attachment_prompt_owner",
			),
		},
		{
			name: "fork",
			args: append(
				append([]string(nil), base...),
				"--resume", "ses_attachment_prompt_owner", "--fork-session",
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			callsBefore := providerCalls.Load()
			var output, diagnostics bytes.Buffer
			err := Run(
				providerContext, test.args,
				bytes.NewReader(append(append([]byte(nil), referenceRecord...), '\n')),
				&output, &diagnostics,
			)
			if err == nil {
				t.Fatalf("different prompt UUID reused durable attachment: output=%s", output.String())
			}
			if providerCalls.Load() != callsBefore {
				t.Fatalf("different prompt UUID reached provider: before=%d after=%d", callsBefore, providerCalls.Load())
			}
			var sawRejected bool
			for _, record := range decodeNDJSONRecords(t, output.String()) {
				if record["type"] == "result" &&
					record["stop_reason"] == "input_error" &&
					record["prompt_uuid"] == differentPromptUUID {
					sawRejected = true
				}
			}
			if !sawRejected {
				t.Fatalf("different prompt UUID lacked correlated rejection: output=%s diagnostics=%s err=%v", output.String(), diagnostics.String(), err)
			}
			if strings.Contains(output.String(), base64.StdEncoding.EncodeToString(pngBytes)) ||
				strings.Contains(diagnostics.String(), base64.StdEncoding.EncodeToString(pngBytes)) {
				t.Fatalf("prompt ownership rejection exposed media bytes")
			}
		})
	}
}

func assertAttachmentCapabilityRecord(t *testing.T, record map[string]any) {
	t.Helper()
	raw, present := record["input_capabilities"]
	if !present {
		t.Fatalf("attachment capability is absent: %#v", record)
	}
	input, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("input_capabilities = %#v", raw)
	}
	attachments, ok := input["attachments"].(map[string]any)
	if !ok || attachments["protocol_version"] != float64(1) {
		t.Fatalf("attachments capability = %#v", input["attachments"])
	}
	sources, ok := attachments["sources"].([]any)
	if !ok || len(sources) != 2 {
		t.Fatalf("attachment sources = %#v", attachments["sources"])
	}
	fileSource, fileOK := sources[0].(map[string]any)
	streamSource, streamOK := sources[1].(map[string]any)
	if !fileOK || !streamOK ||
		fileSource["source"] != string(attachment.SourceFilePath) ||
		fileSource["scope"] != attachment.SourceScopeInitialCLI ||
		streamSource["source"] != string(attachment.SourceStreamJSON) ||
		streamSource["scope"] != attachment.SourceScopePerTurn {
		t.Fatalf("attachment source scopes = %#v", attachments["sources"])
	}
	media, ok := attachments["media_types"].([]any)
	if !ok || len(media) != 3 {
		t.Fatalf("attachment media = %#v", attachments["media_types"])
	}
	gotMIME := make([]string, 0, len(media))
	for _, entry := range media {
		gotMIME = append(gotMIME, entry.(map[string]any)["mime_type"].(string))
	}
	if strings.Join(gotMIME, ",") != "image/png,image/jpeg,application/pdf" {
		t.Fatalf("attachment MIME matrix = %v", gotMIME)
	}
	limits, ok := attachments["limits"].(map[string]any)
	if !ok ||
		limits["max_attachments_per_message"] != float64(attachment.DefaultMaxAttachmentsPerMessage) ||
		limits["max_uploads_per_session"] != float64(attachment.DefaultMaxUploadsPerSession) ||
		limits["max_item_bytes"] != float64(attachment.DefaultMaxItemBytes) ||
		limits["max_aggregate_bytes"] != float64(attachment.DefaultMaxAggregateBytes) {
		t.Fatalf("attachment limits = %#v", attachments["limits"])
	}
	provider, ok := attachments["provider_limits"].(map[string]any)
	if !ok ||
		provider["max_request_items"] == nil ||
		provider["max_encoded_media_bytes"] == nil ||
		provider["max_request_bytes"] == nil ||
		provider["max_ndjson_record_bytes"] != float64(surface.MaxNDJSONRecordBytes) {
		t.Fatalf("provider attachment limits = %#v", attachments["provider_limits"])
	}
}

func appAttachmentManifest(
	id attachment.ID,
	kind attachment.Kind,
	name string,
	mimeType string,
	data []byte,
) attachment.Manifest {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return attachment.Manifest{
		AttachmentID: id, Kind: kind, Name: name, MIMEType: mimeType,
		SizeBytes: int64(len(data)), SHA256: digest,
		StorageID: "blob_sha256_" + digest,
	}
}

func appAttachmentPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.Set(x, y, color.RGBA{
				R: uint8(20 + x), G: uint8(30 + y), B: 40, A: 255,
			})
		}
	}
	var output bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func appAttachmentPDF(t *testing.T, pages int) []byte {
	t.Helper()
	if pages < 1 {
		t.Fatal("PDF fixture requires a positive page count")
	}
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
	fmt.Fprintf(
		&output,
		"2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
		strings.Join(kids, " "), pages,
	)
	for page := range pages {
		offsets[3+page] = output.Len()
		fmt.Fprintf(
			&output,
			"%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>\nendobj\n",
			3+page,
		)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\n", len(offsets))
	fmt.Fprintf(&output, "startxref\n%d\n%%%%EOF\n", xref)
	return output.Bytes()
}

func appAttachmentJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeAttachmentCompletion(writer http.ResponseWriter, text string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_attachment\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	fmt.Fprint(writer, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_attachment\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_attachment\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", text)
	fmt.Fprintf(
		writer,
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_attachment\",\"model\":\"gpt-5.6-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_attachment\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
		text,
	)
}

func countProviderMedia(request map[string]any) int {
	count := 0
	items, _ := request["input"].([]any)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		content, _ := item["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "input_image" || block["type"] == "input_file" {
				count++
			}
		}
	}
	return count
}

func initializeAttachmentTestSession(session *runtimeSession, store *attachment.Store) {
	session.attachments = store
	session.inputMediaEnabled = true
	session.uploadPrompts = make(map[attachment.UploadID]string)
	session.uploadReserved = make(map[attachment.UploadID]int64)
	session.attachmentPrompts = make(map[attachment.ID]string)
	session.attachmentReserved = make(map[attachment.ID]int64)
	session.promptCounts = make(map[string]int)
	session.promptBytes = make(map[string]int64)
	session.uploadTerminalSent = make(map[attachment.UploadID]bool)
}

type attachmentHookCaptureProvider struct {
	mu       sync.Mutex
	requests []model.Request
}

func (provider *attachmentHookCaptureProvider) Stream(
	_ context.Context,
	request model.Request,
) (model.Stream, error) {
	provider.mu.Lock()
	provider.requests = append(provider.requests, request)
	provider.mu.Unlock()
	return &attachmentHookCompletionStream{}, nil
}

func (provider *attachmentHookCaptureProvider) Requests() []model.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]model.Request(nil), provider.requests...)
}

type attachmentHookCompletionStream struct {
	done bool
}

func (stream *attachmentHookCompletionStream) Next() (model.Event, error) {
	if stream.done {
		return model.Event{}, io.EOF
	}
	stream.done = true
	return model.Event{
		Type: model.EventResponseCompleted,
		Response: &model.Response{
			ID: "resp_attachment_hook", Status: "completed",
			Output: []model.Item{model.TextMessage(model.RoleAssistant, "done")},
			Usage:  model.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		},
	}, nil
}

func (*attachmentHookCompletionStream) Close() error { return nil }
