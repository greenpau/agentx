package model

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type chunkReader struct {
	data  []byte
	sizes []int
	read  int
}

func (r *chunkReader) Read(destination []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	size := 1
	if len(r.sizes) > 0 {
		size = r.sizes[r.read%len(r.sizes)]
	}
	r.read++
	if size > len(destination) {
		size = len(destination)
	}
	if size > len(r.data) {
		size = len(r.data)
	}
	copy(destination, r.data[:size])
	r.data = r.data[size:]
	return size, nil
}

func TestSSEDecoderArbitraryChunksAndMultilineData(t *testing.T) {
	input := ": heartbeat\r\n" +
		"event: response.output_text.delta\r\n" +
		"id: evt-1\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"héllo\"}\r\n\r\n" +
		"data:[DONE]\n\n"
	decoder := newSSEDecoder(&chunkReader{data: []byte(input), sizes: []int{1, 2, 5, 3}}, 1024)

	heartbeat, err := decoder.Next()
	if err != nil || !heartbeat.activity || len(heartbeat.data) != 0 {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}
	record, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if record.event != "response.output_text.delta" || record.id != "evt-1" {
		t.Fatalf("record metadata = %#v", record)
	}
	wantData := "{\"type\":\"response.output_text.delta\",\n\"delta\":\"héllo\"}"
	if string(record.data) != wantData {
		t.Fatalf("data = %q, want %q", record.data, wantData)
	}
	done, err := decoder.Next()
	if err != nil || !done.done {
		t.Fatalf("done = %#v, %v", done, err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestSSEDecoderCommentPreservesPartialEvent(t *testing.T) {
	decoder := newSSEDecoder(strings.NewReader("event: x\ndata: one\n: ping\ndata: two\n\n"), 100)
	activity, err := decoder.Next()
	if err != nil || !activity.activity || len(activity.data) != 0 {
		t.Fatalf("activity = %#v, %v", activity, err)
	}
	record, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if record.event != "x" || string(record.data) != "one\ntwo" {
		t.Fatalf("record = %#v", record)
	}
}

func TestSSEDecoderDispatchesFinalUnterminatedEvent(t *testing.T) {
	decoder := newSSEDecoder(strings.NewReader("data: final"), 100)
	record, err := decoder.Next()
	if err != nil || string(record.data) != "final" {
		t.Fatalf("record = %#v, %v", record, err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestSSEDecoderBoundsLinesAndEvents(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "line", input: "data: " + strings.Repeat("x", 20) + "\n\n"},
		{name: "multiline event", input: "data: 12345\ndata: 67890\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := newSSEDecoder(strings.NewReader(test.input), 10)
			_, err := decoder.Next()
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("Next() error = %v, want ErrProtocol", err)
			}
		})
	}
}
