package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
)

func TestTerminalSafeTextMakesControlSequencesInert(t *testing.T) {
	input := "before\x1b]52;c;Y2xpcGJvYXJk\a\x1b[2J\rnext\u0085\tunicode ✓\n"
	got := TerminalSafeText(input)
	for _, forbidden := range []rune{'\x1b', '\a', '\r', '\u0085'} {
		if strings.ContainsRune(got, forbidden) {
			t.Fatalf("terminal control %U survived: %q", forbidden, got)
		}
	}
	for _, visible := range []string{`\x1b]52`, `\x07`, `\x1b[2J`, `\r`, `\u0085`, "\t", "unicode ✓", "\n"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("safe projection lost %q: %q", visible, got)
		}
	}
}

func TestTerminalRecordGuardsCompleteCrossFieldFraming(t *testing.T) {
	const secret = "Read: because\nInput"
	record := "\nPermission required for Read: because\nInput: {}\nAllow? "
	safe, err := terminalRecord(record, redact.New(secret))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(safe, secret) || !strings.Contains(safe, redact.Mask(secret)) {
		t.Fatalf("terminal record projection = %q", safe)
	}
}

func TestWorkspaceTrustWarningGuardsCompleteFramingAndShortCredentials(t *testing.T) {
	for name, secret := range map[string]string{
		"line framing": "them\n",
		"short key":    "in",
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeWorkspaceTrustWarning(&output, redact.New(secret)); err != nil {
				t.Fatal(err)
			}
			if rendered := output.String(); strings.Contains(rendered, secret) ||
				!strings.Contains(rendered, redact.Mask(secret)) {
				t.Fatalf("workspace warning projection = %q", rendered)
			}
		})
	}
}

func TestTerminalLineWriterGuardsFragmentedWarningFraming(t *testing.T) {
	const secret = `type "future`
	var output bytes.Buffer
	writer := newTerminalLineWriter(&output, redact.New(secret))
	for _, fragment := range []string{
		"warning: ignored unknown structured input ty",
		"pe \"future\"\n",
	} {
		if _, err := writer.Write([]byte(fragment)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, secret) || !strings.Contains(got, redact.Mask(secret)) {
		t.Fatalf("fragmented warning projection = %q", got)
	}
}

func TestTerminalLineWriterReportsCommittedPrefixOnLaterFailure(t *testing.T) {
	target := &failSecondTerminalWrite{}
	writer := newTerminalLineWriter(target, nil)
	data := []byte("first\nsecond\n")
	written, err := writer.Write(data)
	if !errors.Is(err, errTerminalWriterFailed) || written != len("first\n") {
		t.Fatalf("fragmented terminal write = (%d, %v)", written, err)
	}
	if got := target.output.String(); got != "first\n" {
		t.Fatalf("committed terminal prefix = %q", got)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("failed suffix was retained for duplicate retry: %v", err)
	}
}

func TestTerminalWriterFailuresAreSealedAndPanicsContained(t *testing.T) {
	if err := writeStringExact(panicWriter{}, "unsafe"); err != errTerminalWriterPanicked {
		t.Fatalf("panic writer error = %v", err)
	}
	if err := writeStringExact(terminalShortWriter{}, "unsafe"); err != io.ErrShortWrite {
		t.Fatalf("short writer error = %v", err)
	}
	hostile := hostileTerminalWriterError{}
	if err := writeStringExact(terminalErrorWriter{err: hostile}, "unsafe"); err != errTerminalWriterFailed {
		t.Fatalf("foreign writer error = %v", err)
	}
}

type hostileTerminalWriterError struct{}

func (hostileTerminalWriterError) Error() string {
	panic("terminal writer Error callback must not run")
}

func (hostileTerminalWriterError) Unwrap() error {
	panic("terminal writer Unwrap callback must not run")
}

type terminalErrorWriter struct{ err error }

func (writer terminalErrorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type terminalShortWriter struct{}

func (terminalShortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

type failSecondTerminalWrite struct {
	writes int
	output bytes.Buffer
}

func (writer *failSecondTerminalWrite) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == 2 {
		return 0, io.ErrClosedPipe
	}
	return writer.output.Write(data)
}

func TestInteractiveSinkRedactsSplitCredentialAndTerminatesPartialError(t *testing.T) {
	t.Run("split credential", func(t *testing.T) {
		const secret = "interactive-source-credential"
		var output bytes.Buffer
		sink := newInteractiveSink(&output)
		sink.SetCredentialSanitizer(redact.New(secret))
		turnID := protocol.TurnID("turn_split")
		split := len(secret) / 2
		for _, delta := range []string{"before " + secret[:split], secret[split:] + " after"} {
			if err := sink.Publish(context.Background(), protocol.Event{
				TurnID: turnID, Kind: protocol.EventKindProgress,
				Progress: &protocol.ProgressEvent{Phase: "model_text", Message: delta},
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := sink.finish(engine.Outcome{TurnID: turnID, Text: "before safe after"}); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); strings.Contains(got, secret) || got != "before "+redact.Mask(secret)+" after\n" {
			t.Fatalf("interactive split projection = %q", got)
		}
	})

	t.Run("partial error", func(t *testing.T) {
		const secret = "partial-secret"
		var output bytes.Buffer
		sink := newInteractiveSink(&output)
		sink.SetCredentialSanitizer(redact.New(secret))
		turnID := protocol.TurnID("turn_error")
		if err := sink.Publish(context.Background(), protocol.Event{
			TurnID: turnID, Kind: protocol.EventKindProgress,
			Progress: &protocol.ProgressEvent{Phase: "model_text", Message: "visible partial"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := sink.finish(engine.Outcome{TurnID: turnID}); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "visible partial\n" {
			t.Fatalf("partial error output = %q", got)
		}
	})
}
