package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/greenpau/agentx/pkg/model"
	"github.com/greenpau/agentx/pkg/protocol"
)

type callbackOperationError struct {
	operation  string
	inspection engineErrorInspection
}

func (e *callbackOperationError) Error() string {
	if e == nil || e.operation == "" {
		return "engine callback failed"
	}
	return e.operation + " failed"
}

func cloneModelTools(tools []model.Tool) []model.Tool {
	cloned := make([]model.Tool, len(tools))
	for index, tool := range tools {
		cloned[index] = tool
		cloned[index].Parameters = append(json.RawMessage(nil), tool.Parameters...)
	}
	return cloned
}

func cloneModelRequest(request model.Request) model.Request {
	cloned := request
	cloned.Input = cloneItems(request.Input)
	cloned.Tools = cloneModelTools(request.Tools)
	if request.ParallelToolCalls != nil {
		value := *request.ParallelToolCalls
		cloned.ParallelToolCalls = &value
	}
	if request.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(request.Metadata))
		for key, value := range request.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

func cloneModelEvent(event model.Event) model.Event {
	cloned := event
	if event.Call != nil {
		call := cloneItems([]model.Item{*event.Call})[0]
		cloned.Call = &call
	}
	if event.Usage != nil {
		usage := *event.Usage
		cloned.Usage = &usage
	}
	if event.Response != nil {
		response := *event.Response
		response.Output = cloneItems(event.Response.Output)
		cloned.Response = &response
	}
	if event.Error != nil {
		providerError := *event.Error
		cloned.Error = &providerError
	}
	return cloned
}

func cloneCapabilityCalls(calls []CapabilityCall) []CapabilityCall {
	cloned := make([]CapabilityCall, len(calls))
	for index, call := range calls {
		cloned[index] = call
		cloned[index].Arguments = append(json.RawMessage(nil), call.Arguments...)
	}
	return cloned
}

func cloneCapabilityResults(results []CapabilityResult) []CapabilityResult {
	cloned := make([]CapabilityResult, len(results))
	for index, result := range results {
		cloned[index] = result
		if result.PermissionDenial != nil {
			denial := *result.PermissionDenial
			denial.ToolInput = append(json.RawMessage(nil), result.PermissionDenial.ToolInput...)
			cloned[index].PermissionDenial = &denial
		}
	}
	return cloned
}

func cloneProtocolEvent(event protocol.Event) protocol.Event {
	cloned := event
	if event.ParentID != nil {
		parent := *event.ParentID
		cloned.ParentID = &parent
	}
	if event.LogicalParentID != nil {
		parent := *event.LogicalParentID
		cloned.LogicalParentID = &parent
	}
	if event.Message != nil {
		message := *event.Message
		message.Content = append([]protocol.ContentBlock(nil), event.Message.Content...)
		cloned.Message = &message
	}
	if event.ToolCall != nil {
		call := *event.ToolCall
		call.Arguments = append(json.RawMessage(nil), event.ToolCall.Arguments...)
		if event.ToolCall.RawArguments != nil {
			raw := *event.ToolCall.RawArguments
			call.RawArguments = &raw
		}
		cloned.ToolCall = &call
	}
	if event.ToolResult != nil {
		result := *event.ToolResult
		result.Content = append([]protocol.ContentBlock(nil), event.ToolResult.Content...)
		if event.ToolResult.Error != nil {
			errorInfo := *event.ToolResult.Error
			result.Error = &errorInfo
		}
		cloned.ToolResult = &result
	}
	if event.Metadata != nil {
		metadata := *event.Metadata
		metadata.Value = append(json.RawMessage(nil), event.Metadata.Value...)
		cloned.Metadata = &metadata
	}
	if event.Usage != nil {
		usage := *event.Usage
		if event.Usage.CostUSD != nil {
			cost := *event.Usage.CostUSD
			usage.CostUSD = &cost
		}
		cloned.Usage = &usage
	}
	if event.TurnResult != nil {
		turnResult := *event.TurnResult
		cloned.TurnResult = &turnResult
	}
	if event.Progress != nil {
		progress := *event.Progress
		cloned.Progress = &progress
	}
	if event.Diagnostic != nil {
		diagnostic := *event.Diagnostic
		cloned.Diagnostic = &diagnostic
	}
	if event.Permission != nil {
		permission := *event.Permission
		cloned.Permission = &permission
	}
	if event.Task != nil {
		task := *event.Task
		cloned.Task = &task
	}
	if event.Retry != nil {
		retry := *event.Retry
		cloned.Retry = &retry
	}
	if event.Connection != nil {
		connection := *event.Connection
		cloned.Connection = &connection
	}
	if event.Hook != nil {
		hook := *event.Hook
		if event.Hook.ExitCode != nil {
			exitCode := *event.Hook.ExitCode
			hook.ExitCode = &exitCode
		}
		cloned.Hook = &hook
	}
	if event.Compaction != nil {
		compaction := *event.Compaction
		if event.Compaction.SummaryID != nil {
			value := *event.Compaction.SummaryID
			compaction.SummaryID = &value
		}
		if event.Compaction.PreservedHead != nil {
			value := *event.Compaction.PreservedHead
			compaction.PreservedHead = &value
		}
		if event.Compaction.Anchor != nil {
			value := *event.Compaction.Anchor
			compaction.Anchor = &value
		}
		if event.Compaction.PreservedTail != nil {
			value := *event.Compaction.PreservedTail
			compaction.PreservedTail = &value
		}
		cloned.Compaction = &compaction
	}
	if event.Cancellation != nil {
		cancellation := *event.Cancellation
		cloned.Cancellation = &cancellation
	}
	if event.LocalCommand != nil {
		localCommand := *event.LocalCommand
		cloned.LocalCommand = &localCommand
	}
	return cloned
}

func openModelStream(ctx context.Context, provider model.Provider, request model.Request) (stream model.Stream, err error) {
	callbackRequest := cloneModelRequest(request)
	defer func() {
		if recover() != nil {
			stream = nil
			err = fmt.Errorf("%w: model provider stream callback panicked", model.ErrProtocol)
		}
	}()
	stream, err = provider.Stream(ctx, callbackRequest)
	if err != nil {
		closeModelStream(stream)
		return nil, err
	}
	if stream == nil {
		return nil, fmt.Errorf("%w: model provider returned a nil stream", model.ErrProtocol)
	}
	return stream, nil
}

func nextModelStream(stream model.Stream) (event model.Event, err error) {
	defer func() {
		if recover() != nil {
			event = model.Event{}
			err = fmt.Errorf("%w: model stream callback panicked", model.ErrProtocol)
		}
	}()
	event, err = stream.Next()
	if err != nil {
		return model.Event{}, err
	}
	return cloneModelEvent(event), err
}

func closeModelStream(stream model.Stream) {
	if stream == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	_ = stream.Close()
}

func (e *Engine) capabilitySchemas() (schemas []model.Tool, err error) {
	defer func() {
		if recover() != nil {
			schemas = nil
			err = fmt.Errorf("%w: capability schema callback panicked", model.ErrProtocol)
		}
	}()
	return cloneModelTools(e.config.Capabilities.Schemas()), nil
}

func (e *Engine) executeCapabilities(ctx context.Context, calls []CapabilityCall) (results []CapabilityResult) {
	callbackCalls := cloneCapabilityCalls(calls)
	defer func() {
		if recover() != nil {
			results = nil
		}
	}()
	return cloneCapabilityResults(e.config.Capabilities.Execute(ctx, callbackCalls))
}

func appendTranscriptEvent(ctx context.Context, store Store, event protocol.Event) (normalized protocol.Event, written bool, err error) {
	stable := cloneProtocolEvent(event)
	callbackEvent := cloneProtocolEvent(stable)
	normalized = stable
	defer func() {
		if recover() != nil {
			// The callback may have completed its append before panicking. Treat
			// the stable event identity as ambiguously committed so an accepted
			// capability is settled but never executed or replayed automatically.
			normalized = stable
			written = true
			err = errors.New("transcript append callback panicked")
		}
	}()
	normalized, written, err = store.AppendEvent(ctx, callbackEvent)
	if err != nil {
		err = &callbackOperationError{
			operation:  "transcript append",
			inspection: inspectEngineErrorWithContext(err, ctx.Err()),
		}
	}
	return cloneProtocolEvent(normalized), written, err
}

func flushTranscriptStore(ctx context.Context, store Store) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("transcript flush callback panicked")
		}
	}()
	err = store.Flush(ctx)
	if err != nil {
		return &callbackOperationError{
			operation:  "transcript flush",
			inspection: inspectEngineErrorWithContext(err, ctx.Err()),
		}
	}
	return nil
}

func publishSinkEvent(ctx context.Context, sink EventSink, event protocol.Event) (err error) {
	callbackEvent := cloneProtocolEvent(event)
	defer func() {
		if recover() != nil {
			err = errors.New("event sink callback panicked")
		}
	}()
	return sink.Publish(ctx, callbackEvent)
}
