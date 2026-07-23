package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/tool"
)

type callbackTerminalWriter struct {
	once     sync.Once
	callback func()
	output   bytes.Buffer
}

func (writer *callbackTerminalWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() {
		if writer.callback != nil {
			writer.callback()
		}
	})
	return writer.output.Write(data)
}

func TestTerminalInteractionsApprovalQuestionAndInputFlow(t *testing.T) {
	var output bytes.Buffer
	interactions := newTerminalInteractions(strings.NewReader("yes\n2\nhello world\n"), &output)
	defer interactions.Close()

	approval, err := interactions.Approve(t.Context(), permission.ApprovalRequest{
		Tool: "Read", ToolUseID: "tool-1", Reason: "inspect fixture",
		Input: json.RawMessage(`{"file_path":"/tmp/fixture"}`),
	})
	if err != nil || approval.Kind != permission.DecisionAllow {
		t.Fatalf("approval = %#v, %v", approval, err)
	}
	question := tool.Question{
		Header: "Choice", Question: "Which route?",
		Options: []tool.QuestionOption{
			{Label: "First", Description: "choose the first route"},
			{Label: "Second", Description: "choose the second route"},
		},
	}
	answers, err := interactions.Ask(t.Context(), []tool.Question{question})
	if err != nil || len(answers[question.Question]) != 1 || answers[question.Question][0] != "Second" {
		t.Fatalf("answers = %#v, %v", answers, err)
	}
	line, err := interactions.ReadLine(t.Context(), "> ")
	if err != nil || line != "hello world" {
		t.Fatalf("line = %q, %v", line, err)
	}
	rendered := output.String()
	for _, fragment := range []string{"Permission required for Read", "Which route?", "2) Second", "> "} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("interactive output omitted %q: %q", fragment, rendered)
		}
	}
}

func TestTerminalInteractionWriterReentryFailsFast(t *testing.T) {
	writer := &callbackTerminalWriter{}
	interactions := newTerminalInteractions(strings.NewReader("yes\n"), writer)
	defer interactions.Close()
	var nestedErr error
	writer.callback = func() {
		interactions.SetCredentialSanitizer(redact.New("credential-not-in-prompt"))
		_, nestedErr = interactions.ReadLine(context.Background(), "nested: ")
	}
	response, err := interactions.Approve(t.Context(), permission.ApprovalRequest{
		Tool: "Read", Reason: "safe", Input: json.RawMessage(`{}`),
	})
	if err != nil || response.Kind != permission.DecisionAllow {
		t.Fatalf("outer approval = %#v, %v", response, err)
	}
	if nestedErr != errTerminalInteractionWriterActive {
		t.Fatalf("nested interaction error = %v", nestedErr)
	}
}

func TestTerminalInteractionContainsWriterPanic(t *testing.T) {
	interactions := newTerminalInteractions(strings.NewReader("yes\n"), panicWriter{})
	defer interactions.Close()
	_, err := interactions.Approve(t.Context(), permission.ApprovalRequest{
		Tool: "Read", Reason: "safe", Input: json.RawMessage(`{}`),
	})
	if err != errTerminalWriterPanicked {
		t.Fatalf("approval writer panic = %v", err)
	}
}

func TestInteractiveSinkWriterReentryFailsFast(t *testing.T) {
	writer := &callbackTerminalWriter{}
	sink := newInteractiveSink(writer)
	turnID := protocol.TurnID("turn-interactive-reentry")
	outer := protocol.Event{
		TurnID: turnID, Kind: protocol.EventKindProgress,
		Progress: &protocol.ProgressEvent{Phase: "model_text", Message: "outer"},
	}
	var nestedErr error
	writer.callback = func() {
		sink.SetCredentialSanitizer(redact.New("credential-not-in-output"))
		nestedErr = sink.Publish(context.Background(), protocol.Event{
			TurnID: protocol.TurnID("turn-nested"), Kind: protocol.EventKindProgress,
			Progress: &protocol.ProgressEvent{Phase: "model_text", Message: "nested"},
		})
	}
	if err := sink.Publish(context.Background(), outer); err != nil {
		t.Fatal(err)
	}
	if nestedErr != errInteractiveSinkWriterActive {
		t.Fatalf("nested publish error = %v", nestedErr)
	}
	if err := sink.finish(engine.Outcome{TurnID: turnID, Text: "outer"}); err != nil {
		t.Fatal(err)
	}
	if got := writer.output.String(); got != "outer\n" {
		t.Fatalf("interactive sink output = %q", got)
	}
}

func TestInteractiveSinkWriterFailurePoisonsTurn(t *testing.T) {
	sink := newInteractiveSink(terminalErrorWriter{err: errors.New("private writer failure")})
	turnID := protocol.TurnID("turn-writer-failure")
	err := sink.Publish(context.Background(), protocol.Event{
		TurnID: turnID, Kind: protocol.EventKindProgress,
		Progress: &protocol.ProgressEvent{Phase: "model_text", Message: "partial"},
	})
	if err != errTerminalWriterFailed {
		t.Fatalf("publish error = %v", err)
	}
	if err := sink.finish(engine.Outcome{TurnID: turnID}); err != errTerminalWriterFailed {
		t.Fatalf("finish did not retain terminal writer failure: %v", err)
	}
}
