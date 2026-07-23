package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/greenpau/agentx/pkg/redact"
)

var (
	errTerminalWriterFailed   = errors.New("terminal writer failed")
	errTerminalWriterPanicked = errors.New("terminal writer panicked")
)

// TerminalSafeText preserves ordinary Unicode, line feeds, and tabs while
// making every C0/C1 terminal control visible and inert. In particular ESC,
// OSC, CSI, BEL, CR, and clipboard/title sequences can never reach a terminal
// as executable bytes through model, tool, hook, MCP, or error text.
func TerminalSafeText(value string) string {
	var output strings.Builder
	for _, char := range value {
		switch {
		case char == '\n' || char == '\t':
			output.WriteRune(char)
		case char == '\r':
			output.WriteString(`\r`)
		case char < 0x20 || char == 0x7f:
			fmt.Fprintf(&output, `\x%02x`, char)
		case char >= 0x80 && char <= 0x9f:
			fmt.Fprintf(&output, `\u%04x`, char)
		default:
			output.WriteRune(char)
		}
	}
	return output.String()
}

// terminalRecord projects one complete presentation record only after all
// labels, field separators, and terminal framing have been assembled.
func terminalRecord(value string, credentials *redact.Set) (string, error) {
	value = TerminalSafeText(value)
	if credentials == nil || credentials.Empty() {
		return value, nil
	}
	safe, suppressed := credentials.Redact(value)
	if suppressed {
		return "", errors.New("terminal record could not be safely projected")
	}
	return safe, nil
}

func writeTerminalRecord(writer io.Writer, credentials *redact.Set, value string) error {
	safe, err := terminalRecord(value, credentials)
	if err != nil {
		return err
	}
	return writeStringExact(writer, safe)
}

func writeStringExact(writer io.Writer, value string) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = errTerminalWriterPanicked
		}
	}()
	written, err := io.WriteString(writer, value)
	if err != nil {
		// The writer is a host boundary. Do not retain a callback-owned error
		// whose formatting or unwrap graph can execute more foreign code.
		return errTerminalWriterFailed
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

// terminalLineWriter adapts APIs that own warning formatting but accept only
// an io.Writer. It buffers through the physical newline so a credential formed
// across a fixed warning prefix, an untrusted field, and record framing is
// inspected as one terminal record.
type terminalLineWriter struct {
	writer      io.Writer
	credentials *redact.Set
	pending     []byte
}

func newTerminalLineWriter(writer io.Writer, credentials *redact.Set) *terminalLineWriter {
	return &terminalLineWriter{writer: writer, credentials: credentials}
}

func (writer *terminalLineWriter) Write(data []byte) (int, error) {
	if writer == nil || writer.writer == nil {
		return len(data), nil
	}
	previous := len(writer.pending)
	writer.pending = append(writer.pending, data...)
	consumed := 0
	for {
		offset := bytes.IndexByte(writer.pending[consumed:], '\n')
		if offset < 0 {
			break
		}
		end := consumed + offset + 1
		if err := writeTerminalRecord(writer.writer, writer.credentials, string(writer.pending[consumed:end])); err != nil {
			// Earlier complete records are already committed. Report only bytes
			// from this call that belonged to those records and discard the
			// failed suffix so a conventional retry cannot duplicate them.
			current := consumed - previous
			if current < 0 {
				current = 0
			}
			if current > len(data) {
				current = len(data)
			}
			writer.pending = writer.pending[:0]
			return current, err
		}
		consumed = end
	}
	if consumed > 0 {
		writer.pending = append(writer.pending[:0], writer.pending[consumed:]...)
	}
	return len(data), nil
}

func (writer *terminalLineWriter) Flush() error {
	if writer == nil || len(writer.pending) == 0 {
		return nil
	}
	pending := string(writer.pending)
	writer.pending = writer.pending[:0]
	return writeTerminalRecord(writer.writer, writer.credentials, pending)
}
