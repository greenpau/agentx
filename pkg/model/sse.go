package model

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

type sseRecord struct {
	event    string
	id       string
	data     []byte
	done     bool
	activity bool
}

// sseDecoder implements the line-oriented Server-Sent Events grammar without
// assuming that network reads align to UTF-8 code points, lines, or events.
// State is retained across comment activity records so heartbeats can reset the
// stream watchdog without discarding a partially assembled multiline event.
type sseDecoder struct {
	reader       *bufio.Reader
	maximumBytes int
	event        string
	id           string
	data         bytes.Buffer
	hasData      bool
	tooLarge     bool
	eof          bool
}

func newSSEDecoder(reader io.Reader, maximumBytes int) *sseDecoder {
	return &sseDecoder{
		reader:       bufio.NewReaderSize(reader, 32<<10),
		maximumBytes: maximumBytes,
	}
}

func (d *sseDecoder) Next() (sseRecord, error) {
	if d.eof {
		return sseRecord{}, io.EOF
	}
	for {
		line, err := d.readLine()
		inspection := inspectModelError(err)
		if err != nil && !inspection.eof {
			return sseRecord{}, err
		}
		if len(line) > 0 {
			if line[0] == ':' {
				if inspection.eof {
					d.eof = true
				}
				return sseRecord{activity: true}, nil
			}
			d.consumeField(line)
		} else if d.hasData {
			if d.tooLarge {
				d.resetEvent()
				return sseRecord{}, fmt.Errorf("%w: SSE event exceeds %d bytes", ErrProtocol, d.maximumBytes)
			}
			record := d.dispatch()
			if inspection.eof {
				d.eof = true
			}
			return record, nil
		} else {
			// A blank event with no data is not dispatched by the SSE grammar.
			d.event = ""
		}

		if inspection.eof {
			d.eof = true
			if d.hasData {
				if d.tooLarge {
					d.resetEvent()
					return sseRecord{}, fmt.Errorf("%w: SSE event exceeds %d bytes", ErrProtocol, d.maximumBytes)
				}
				return d.dispatch(), nil
			}
			return sseRecord{}, io.EOF
		}
	}
}

func (d *sseDecoder) readLine() ([]byte, error) {
	var line []byte
	for {
		fragment, err := d.reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(line)+len(fragment) > d.maximumBytes {
				return nil, fmt.Errorf("%w: SSE line exceeds %d bytes", ErrProtocol, d.maximumBytes)
			}
			line = append(line, fragment...)
		}
		if inspectModelError(err).bufferFull {
			continue
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		return line, err
	}
}

func (d *sseDecoder) consumeField(line []byte) {
	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		value = nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(field) {
	case "event":
		d.event = string(value)
	case "data":
		separator := 0
		if d.hasData {
			separator = 1
		}
		if d.data.Len()+separator+len(value) > d.maximumBytes {
			d.tooLarge = true
		} else if !d.tooLarge {
			if d.hasData {
				d.data.WriteByte('\n')
			}
			d.data.Write(value)
		}
		d.hasData = true
	case "id":
		if !bytes.ContainsRune(value, '\x00') {
			d.id = string(value)
		}
	case "retry":
		// The Responses retry contract is driven by HTTP Retry-After and the
		// bounded client policy, not an untrusted SSE reconnection directive.
	}
}

func (d *sseDecoder) dispatch() sseRecord {
	data := append([]byte(nil), d.data.Bytes()...)
	record := sseRecord{
		event:    d.event,
		id:       d.id,
		data:     data,
		done:     strings.TrimSpace(string(data)) == "[DONE]",
		activity: true,
	}
	d.resetEvent()
	return record
}

func (d *sseDecoder) resetEvent() {
	d.event = ""
	d.data.Reset()
	d.hasData = false
	d.tooLarge = false
}
