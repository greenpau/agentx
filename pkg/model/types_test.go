package model

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	valid := Request{
		Input:     []Item{TextMessage(RoleUser, "hello")},
		Tools:     []Tool{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Reasoning: Reasoning{Effort: "xhigh"},
	}
	tests := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{name: "valid"},
		{name: "empty input", mutate: func(request *Request) { request.Input = nil }, wantErr: true},
		{name: "negative output", mutate: func(request *Request) { request.MaxOutputTokens = -1 }, wantErr: true},
		{name: "unknown effort", mutate: func(request *Request) { request.Reasoning.Effort = "ultra" }, wantErr: true},
		{name: "assistant input text", mutate: func(request *Request) {
			request.Input = []Item{{Type: ItemMessage, Role: RoleAssistant, Content: []Content{{Type: ContentInputText, Text: "bad"}}}}
		}, wantErr: true},
		{name: "user output text", mutate: func(request *Request) {
			request.Input = []Item{{Type: ItemMessage, Role: RoleUser, Content: []Content{{Type: ContentOutputText, Text: "bad"}}}}
		}, wantErr: true},
		{name: "missing call id", mutate: func(request *Request) {
			request.Input = []Item{{Type: ItemFunctionCall, Name: "lookup", Arguments: `{}`}}
		}, wantErr: true},
		{name: "missing output call id", mutate: func(request *Request) {
			request.Input = []Item{{Type: ItemFunctionCallOutput, Output: "ok"}}
		}, wantErr: true},
		{name: "empty reasoning", mutate: func(request *Request) {
			request.Input = []Item{{Type: ItemReasoning}}
		}, wantErr: true},
		{name: "array tool schema", mutate: func(request *Request) {
			request.Tools[0].Parameters = json.RawMessage(`[]`)
		}, wantErr: true},
		{name: "malformed tool schema", mutate: func(request *Request) {
			request.Tools[0].Parameters = json.RawMessage(`{"type"`)
		}, wantErr: true},
		{name: "duplicate tool", mutate: func(request *Request) {
			request.Tools = append(request.Tools, request.Tools[0])
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.Input = append([]Item(nil), valid.Input...)
			request.Tools = append([]Tool(nil), valid.Tools...)
			if test.mutate != nil {
				test.mutate(&request)
			}
			err := request.Validate()
			if test.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil && !errors.Is(err, ErrProtocol) {
				t.Fatalf("Validate() error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  Item
		want Item
	}{
		{
			name: "user text",
			got:  TextMessage(RoleUser, "hello"),
			want: Item{Type: ItemMessage, Role: RoleUser, Content: []Content{{Type: ContentInputText, Text: "hello"}}},
		},
		{
			name: "assistant text",
			got:  TextMessage(RoleAssistant, "hello"),
			want: Item{Type: ItemMessage, Role: RoleAssistant, Content: []Content{{Type: ContentOutputText, Text: "hello"}}},
		},
		{
			name: "function call",
			got:  FunctionCall("fc_1", "call_1", "lookup", `{"q":"x"}`),
			want: Item{Type: ItemFunctionCall, ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`},
		},
		{
			name: "function output",
			got:  FunctionCallOutput("call_1", "done"),
			want: Item{Type: ItemFunctionCallOutput, CallID: "call_1", Output: "done"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("constructor = %#v, want %#v", test.got, test.want)
			}
		})
	}
}

type sliceStream struct {
	events []Event
	index  int
	closed bool
	err    error
}

func (s *sliceStream) Next() (Event, error) {
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.err != nil {
		return Event{}, s.err
	}
	return Event{}, io.EOF
}

func (s *sliceStream) Close() error {
	s.closed = true
	return nil
}

func TestDrain(t *testing.T) {
	stream := &sliceStream{events: []Event{{Type: EventTextDelta}, {Type: EventResponseCompleted}}}
	events, err := Drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !stream.closed {
		t.Fatalf("Drain() = %d events, closed=%v", len(events), stream.closed)
	}

	wantErr := errors.New("broken")
	stream = &sliceStream{err: wantErr}
	_, err = Drain(stream)
	if !errors.Is(err, wantErr) || !stream.closed {
		t.Fatalf("Drain() error = %v, closed=%v", err, stream.closed)
	}
}
