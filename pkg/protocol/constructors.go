package protocol

import (
	"fmt"
	"time"

	"github.com/greenpau/agentx/pkg/identity"
)

// NewEventID creates a cryptographically random canonical event identifier.
func NewEventID() (EventID, error) {
	id, err := identity.New("evt")
	if err != nil {
		return "", err
	}
	return EventID(id), nil
}

// NewToolUseID creates a cryptographically random model-tool correlation ID.
func NewToolUseID() (ToolUseID, error) {
	id, err := identity.New("tool")
	if err != nil {
		return "", err
	}
	return ToolUseID(id), nil
}

// NewBaseEvent initializes the common envelope. Callers must attach the payload
// selected by kind before validation or publication.
func NewBaseEvent(sessionID SessionID, turnID TurnID, kind EventKind) (Event, error) {
	if sessionID == "" {
		return Event{}, fmt.Errorf("session id is required")
	}
	id, err := NewEventID()
	if err != nil {
		return Event{}, fmt.Errorf("create event id: %w", err)
	}
	return Event{
		Version:     CurrentVersion,
		ID:          id,
		SessionID:   sessionID,
		TurnID:      turnID,
		Timestamp:   time.Now().UTC(),
		Kind:        kind,
		Visibility:  VisibilityBoth,
		Persistence: PersistenceDurable,
		Origin:      OriginRuntime,
	}, nil
}

// NewMessageEvent constructs and validates a durable semantic message event.
func NewMessageEvent(sessionID SessionID, turnID TurnID, role Role, content ...ContentBlock) (Event, error) {
	event, err := NewBaseEvent(sessionID, turnID, EventKindMessage)
	if err != nil {
		return Event{}, err
	}
	event.Message = &Message{Role: role, Content: append([]ContentBlock(nil), content...)}
	switch role {
	case RoleUser:
		event.Origin = OriginUser
	case RoleAssistant:
		event.Origin = OriginModel
	case RoleSystem:
		event.Origin = OriginRuntime
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// NewToolCallEvent constructs and validates a durable accepted tool call. If
// call.ID is empty a cryptographically random ID is assigned.
func NewToolCallEvent(sessionID SessionID, turnID TurnID, call ToolCall) (Event, error) {
	event, err := NewBaseEvent(sessionID, turnID, EventKindToolCall)
	if err != nil {
		return Event{}, err
	}
	if call.ID == "" {
		call.ID, err = NewToolUseID()
		if err != nil {
			return Event{}, fmt.Errorf("create tool-use id: %w", err)
		}
	}
	if call.Arguments != nil {
		call.Arguments = append([]byte(nil), call.Arguments...)
	}
	if call.RawArguments != nil {
		raw := *call.RawArguments
		call.RawArguments = &raw
	}
	event.Origin = OriginModel
	event.ToolCall = &call
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// NewToolResultEvent constructs and validates a durable terminal tool result.
func NewToolResultEvent(sessionID SessionID, turnID TurnID, result ToolResult) (Event, error) {
	event, err := NewBaseEvent(sessionID, turnID, EventKindToolResult)
	if err != nil {
		return Event{}, err
	}
	event.Origin = OriginCapability
	event.ToolResult = &result
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}
