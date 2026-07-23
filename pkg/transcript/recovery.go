package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/greenpau/agentx/pkg/protocol"
)

const interruptedMessage = "Tool execution was interrupted before a terminal result was recorded. The operation was not rerun during recovery."

// ActiveConversation projects the defensively loaded physical DAG onto the
// most recent resumable main chain. Physical loading deliberately retains
// valid siblings for history/audit consumers; only semantic resume selects a
// leaf. Parallel assistant blocks and terminal results belonging to accepted
// calls on that chain are spliced back in canonical sequence order.
func (s Snapshot) ActiveConversation() Snapshot {
	projected := Snapshot{
		SessionID:          s.SessionID,
		Diagnostics:        append([]Diagnostic(nil), s.Diagnostics...),
		MaxSequence:        s.MaxSequence,
		DroppedDiagnostics: s.DroppedDiagnostics,
	}
	collector := &diagnosticCollector{limit: DefaultMaxDiagnostics}
	if remaining := DefaultMaxDiagnostics - len(projected.Diagnostics); remaining > 0 {
		collector.limit = remaining
	} else {
		collector.limit = 0
	}
	projected.Events, projected.ResumeCursor = activeConversationProjection(s.Events, collector)
	projected.Diagnostics = append(projected.Diagnostics, collector.items...)
	projected.DroppedDiagnostics += collector.dropped
	return projected
}

func activeConversationProjection(events []protocol.Event, collector *diagnosticCollector) ([]protocol.Event, protocol.EventID) {
	if len(events) < 2 {
		result := append([]protocol.Event(nil), events...)
		if len(result) == 1 && result[0].Persistence == protocol.PersistenceDurable && result[0].Kind == protocol.EventKindMessage &&
			result[0].Message != nil && (result[0].Message.Role == protocol.RoleUser || result[0].Message.Role == protocol.RoleAssistant) {
			return result, result[0].ID
		}
		return result, ""
	}
	byID := make(map[protocol.EventID]protocol.Event, len(events))
	for _, event := range events {
		byID[event.ID] = event
	}
	type leafChoice struct {
		leaf   protocol.Event
		anchor protocol.Event
		valid  bool
	}
	choose := func(includeSidechain bool) leafChoice {
		children := make(map[protocol.EventID]int, len(events))
		for _, event := range events {
			if event.Persistence != protocol.PersistenceDurable || event.Sidechain != includeSidechain || event.ParentID == nil {
				continue
			}
			if parent, exists := byID[*event.ParentID]; exists && parent.Sidechain == includeSidechain {
				children[parent.ID]++
			}
		}
		best := leafChoice{}
		for _, leaf := range events {
			if leaf.Persistence != protocol.PersistenceDurable || leaf.Sidechain != includeSidechain || children[leaf.ID] != 0 {
				continue
			}
			cursor := leaf
			var anchor protocol.Event
			found := false
			seen := make(map[protocol.EventID]struct{})
			for {
				if _, duplicate := seen[cursor.ID]; duplicate {
					break
				}
				seen[cursor.ID] = struct{}{}
				if cursor.Kind == protocol.EventKindMessage && cursor.Message != nil &&
					(cursor.Message.Role == protocol.RoleUser || cursor.Message.Role == protocol.RoleAssistant) {
					anchor, found = cursor, true
					break
				}
				if cursor.ParentID == nil {
					break
				}
				parent, exists := byID[*cursor.ParentID]
				if !exists || parent.Sidechain != includeSidechain {
					break
				}
				cursor = parent
			}
			if !found {
				continue
			}
			if !best.valid || laterActiveLeaf(anchor, leaf, best.anchor, best.leaf) {
				best = leafChoice{leaf: leaf, anchor: anchor, valid: true}
			}
		}
		return best
	}
	selected := choose(false)
	if !selected.valid {
		selected = choose(true)
	}
	if !selected.valid {
		// Metadata-only histories have no resumable conversation leaf. Preserve
		// them for compatibility; startup policy decides whether they constitute
		// a user-visible session.
		return append([]protocol.Event(nil), events...), ""
	}

	included := make(map[protocol.EventID]bool, len(events))
	for cursor := selected.leaf; ; {
		if included[cursor.ID] {
			break
		}
		included[cursor.ID] = true
		if cursor.ParentID == nil {
			break
		}
		parent, exists := byID[*cursor.ParentID]
		if !exists {
			break
		}
		cursor = parent
	}

	// Provider streams may persist assistant blocks and tool completions as
	// siblings. Iterate to a fixed point so included assistant groups bring in
	// their accepted calls, chained calls, and exactly-correlated results.
	changed := true
	for changed {
		changed = false
		responseGroups := make(map[string]struct{})
		toolUses := make(map[protocol.ToolUseID]struct{})
		for _, event := range events {
			if !included[event.ID] {
				continue
			}
			if event.Kind == protocol.EventKindMessage && event.Message != nil && event.Message.Role == protocol.RoleAssistant && event.Message.APIResponseID != "" {
				responseGroups[event.Message.APIResponseID] = struct{}{}
			}
			if event.Kind == protocol.EventKindToolCall && event.ToolCall != nil {
				if event.ToolCall.APIResponseID != "" {
					responseGroups[event.ToolCall.APIResponseID] = struct{}{}
				}
				toolUses[event.ToolCall.ID] = struct{}{}
			}
		}
		for _, event := range events {
			if included[event.ID] || event.Sidechain != selected.leaf.Sidechain {
				continue
			}
			include := false
			switch event.Kind {
			case protocol.EventKindMessage:
				if event.Message != nil && event.Message.Role == protocol.RoleAssistant && event.Message.APIResponseID != "" {
					_, include = responseGroups[event.Message.APIResponseID]
				}
			case protocol.EventKindToolCall:
				if event.ToolCall != nil && event.ToolCall.APIResponseID != "" {
					_, include = responseGroups[event.ToolCall.APIResponseID]
				}
			case protocol.EventKindToolResult:
				if event.ToolResult != nil {
					_, include = toolUses[event.ToolResult.ToolUseID]
				}
			}
			if include {
				included[event.ID] = true
				changed = true
			}
		}
	}

	result := make([]protocol.Event, 0, len(included))
	for _, event := range events {
		if included[event.ID] {
			result = append(result, event)
			continue
		}
		if preserveSessionScopedEvent(event) {
			result = append(result, event)
			continue
		}
		collector.add(Diagnostic{Code: "inactive_branch", Message: "event belongs to an inactive conversation branch and was excluded from semantic resume", EventID: event.ID})
	}
	sort.SliceStable(result, func(i, j int) bool {
		// Derived recovery results use sequence zero and arrive after durable
		// history. Keep them after all accepted calls instead of sorting them to
		// the front of the provider projection.
		if result[i].Sequence == 0 || result[j].Sequence == 0 {
			return result[i].Sequence != 0 && result[j].Sequence == 0
		}
		return result[i].Sequence < result[j].Sequence
	})
	return result, selected.leaf.ID
}

func laterActiveLeaf(anchor, leaf, previousAnchor, previousLeaf protocol.Event) bool {
	if !leaf.Timestamp.Equal(previousLeaf.Timestamp) {
		return leaf.Timestamp.After(previousLeaf.Timestamp)
	}
	if leaf.Sequence != previousLeaf.Sequence {
		return leaf.Sequence > previousLeaf.Sequence
	}
	if !anchor.Timestamp.Equal(previousAnchor.Timestamp) {
		return anchor.Timestamp.After(previousAnchor.Timestamp)
	}
	return anchor.Sequence > previousAnchor.Sequence
}

func preserveSessionScopedEvent(event protocol.Event) bool {
	if event.Persistence != protocol.PersistenceDurable {
		return false
	}
	if event.Kind == protocol.EventKindUsage {
		return true
	}
	if event.Kind != protocol.EventKindSessionMetadata || event.Metadata == nil {
		return false
	}
	// Provider output and context-rewrite records are conversation-scoped and
	// follow the selected branch. Other metadata (for example title, tags,
	// reasoning effort, mode, and worktree attribution) is session-scoped and
	// retains append/last-wins ordering across branch projection.
	switch event.Metadata.Key {
	case "provider_response_output", "context_clear", "context_projection":
		return false
	default:
		return true
	}
}

// ReconcileUnresolved returns an independent model-safe snapshot. A modern
// response group whose calls are all unresolved is omitted; a retained mixed
// group receives synthetic, interrupted, model-visible, ephemeral results for
// only its missing members. Legacy ungrouped calls retain the conservative
// synthetic behavior. No external operation is replayed and no on-disk record
// is changed.
func (s Snapshot) ReconcileUnresolved() Snapshot {
	recovered := Snapshot{
		SessionID:          s.SessionID,
		Events:             append([]protocol.Event(nil), s.Events...),
		Diagnostics:        append([]Diagnostic(nil), s.Diagnostics...),
		MaxSequence:        s.MaxSequence,
		DroppedDiagnostics: s.DroppedDiagnostics,
		ResumeCursor:       s.ResumeCursor,
	}
	resolved := make(map[protocol.ToolUseID]struct{})
	for _, event := range recovered.Events {
		if event.Kind == protocol.EventKindToolResult && event.ToolResult != nil {
			resolved[event.ToolResult.ToolUseID] = struct{}{}
		}
	}

	// Modern provider records carry a response identity on every assistant item.
	// If every accepted tool call in one response is unresolved, omit that whole
	// assistant group from the live projection. Its durable raw records remain
	// untouched as audit evidence, and no side effect is replayed or inferred.
	groupCalls := make(map[string][]protocol.ToolUseID)
	for _, event := range recovered.Events {
		if event.Kind != protocol.EventKindToolCall || event.ToolCall == nil || event.ToolCall.APIResponseID == "" {
			continue
		}
		groupCalls[event.ToolCall.APIResponseID] = append(groupCalls[event.ToolCall.APIResponseID], event.ToolCall.ID)
	}
	fullyUnresolved := make(map[string]struct{})
	for responseID, calls := range groupCalls {
		hasResolved := false
		for _, toolID := range calls {
			if _, ok := resolved[toolID]; ok {
				hasResolved = true
				break
			}
		}
		if !hasResolved {
			fullyUnresolved[responseID] = struct{}{}
		}
	}
	if len(fullyUnresolved) > 0 {
		originalByID := make(map[protocol.EventID]protocol.Event, len(recovered.Events))
		for _, event := range recovered.Events {
			originalByID[event.ID] = event
		}
		filtered := make([]protocol.Event, 0, len(recovered.Events))
		retained := make(map[protocol.EventID]struct{}, len(recovered.Events))
		for _, event := range recovered.Events {
			responseID := eventResponseID(event)
			_, omit := fullyUnresolved[responseID]
			if event.Kind == protocol.EventKindSessionMetadata && event.Metadata != nil && event.Metadata.Key == "provider_response_output" {
				omit = providerOutputEntirelyInGroups(event.Metadata.Value, fullyUnresolved)
			}
			if omit {
				recovered.addDiagnostic(Diagnostic{
					Code: "omitted_fully_unresolved_group", Message: "fully unresolved assistant tool group was omitted from semantic resume", EventID: event.ID,
				})
				continue
			}
			filtered = append(filtered, event)
			retained[event.ID] = struct{}{}
		}
		recovered.Events = filtered
		for recovered.ResumeCursor != "" {
			if _, ok := retained[recovered.ResumeCursor]; ok {
				break
			}
			removed, ok := originalByID[recovered.ResumeCursor]
			if !ok || removed.ParentID == nil {
				recovered.ResumeCursor = ""
				break
			}
			recovered.ResumeCursor = *removed.ParentID
		}
	}

	accepted := make(map[protocol.ToolUseID]protocol.Event)
	order := make([]protocol.ToolUseID, 0)
	for _, event := range recovered.Events {
		if event.Kind == protocol.EventKindToolCall && event.ToolCall != nil {
			if _, exists := accepted[event.ToolCall.ID]; !exists {
				accepted[event.ToolCall.ID] = event
				order = append(order, event.ToolCall.ID)
			}
		}
	}

	for _, toolID := range order {
		if _, exists := resolved[toolID]; exists {
			continue
		}
		callEvent := accepted[toolID]
		parent := callEvent.ID
		result := protocol.Event{
			Version:   protocol.CurrentVersion,
			ID:        recoveryEventID(recovered.SessionID, toolID),
			SessionID: recovered.SessionID,
			TurnID:    callEvent.TurnID,
			ParentID:  &parent,
			// Sequence zero is deliberate: this event has projection order but no
			// fabricated position in authoritative durable history.
			Sequence:    0,
			Timestamp:   callEvent.Timestamp,
			Kind:        protocol.EventKindToolResult,
			Visibility:  protocol.VisibilityBoth,
			Persistence: protocol.PersistenceEphemeral,
			Origin:      protocol.OriginRecovery,
			Session:     callEvent.Session,
			Sidechain:   callEvent.Sidechain,
			AgentName:   callEvent.AgentName,
			AgentID:     callEvent.AgentID,
			ToolResult: &protocol.ToolResult{
				ToolUseID: toolID,
				ToolName:  callEvent.ToolCall.Name,
				Status:    protocol.ToolResultInterrupted,
				Content:   []protocol.ContentBlock{protocol.TextBlock(interruptedMessage)},
				IsError:   true,
				Synthetic: true,
				Error: &protocol.ErrorInfo{
					Code:    "recovered_interruption",
					Message: interruptedMessage,
				},
			},
		}
		recovered.Events = append(recovered.Events, result)
		recovered.addDiagnostic(Diagnostic{
			Code:      "recovered_interrupted_tool",
			Message:   "unresolved tool call received an in-memory interrupted result and was not rerun",
			EventID:   result.ID,
			ToolUseID: toolID,
		})
	}
	return recovered
}

func eventResponseID(event protocol.Event) string {
	switch event.Kind {
	case protocol.EventKindMessage:
		if event.Message != nil && event.Message.Role == protocol.RoleAssistant {
			return event.Message.APIResponseID
		}
	case protocol.EventKindToolCall:
		if event.ToolCall != nil {
			return event.ToolCall.APIResponseID
		}
	}
	return ""
}

func providerOutputEntirelyInGroups(raw json.RawMessage, groups map[string]struct{}) bool {
	var items []struct {
		APIResponseID string `json:"api_response_id"`
	}
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return false
	}
	seen := false
	for _, item := range items {
		if item.APIResponseID == "" {
			return false
		}
		if _, ok := groups[item.APIResponseID]; !ok {
			return false
		}
		seen = true
	}
	return seen
}

func (s *Snapshot) addDiagnostic(diagnostic Diagnostic) {
	if len(s.Diagnostics) < DefaultMaxDiagnostics {
		s.Diagnostics = append(s.Diagnostics, diagnostic)
		return
	}
	s.DroppedDiagnostics++
}

func recoveryEventID(sessionID protocol.SessionID, toolID protocol.ToolUseID) protocol.EventID {
	digest := sha256.Sum256([]byte(fmt.Sprintf("agentx-recovery-v1\x00%s\x00%s", sessionID, toolID)))
	return protocol.EventID("evt_recovery_" + hex.EncodeToString(digest[:16]))
}

// ModelEvents returns an independent, ordered slice of model-visible semantic
// events. Derived interrupted results are included after reconciliation.
func (s Snapshot) ModelEvents() []protocol.Event {
	result := make([]protocol.Event, 0, len(s.Events))
	for _, event := range s.Events {
		if !event.Visibility.ModelVisible() {
			continue
		}
		switch event.Kind {
		case protocol.EventKindMessage, protocol.EventKindToolCall, protocol.EventKindToolResult:
			result = append(result, event)
		}
	}
	return result
}

// ToolPairs returns retained accepted calls in acceptance order and requires
// exactly one terminal result for each. Call ReconcileUnresolved first for
// crash recovery and fully-unresolved-group projection.
func (s Snapshot) ToolPairs() ([]ToolPair, error) {
	accepted := make(map[protocol.ToolUseID]protocol.Event)
	order := make([]protocol.ToolUseID, 0)
	resolved := make(map[protocol.ToolUseID]protocol.Event)
	for _, event := range s.Events {
		switch event.Kind {
		case protocol.EventKindToolCall:
			if _, exists := accepted[event.ToolCall.ID]; exists {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateToolUse, event.ToolCall.ID)
			}
			accepted[event.ToolCall.ID] = event
			order = append(order, event.ToolCall.ID)
		case protocol.EventKindToolResult:
			toolID := event.ToolResult.ToolUseID
			call, exists := accepted[toolID]
			if !exists {
				return nil, fmt.Errorf("%w: %s", ErrUnknownToolUse, toolID)
			}
			if event.ToolResult.ToolName != call.ToolCall.Name {
				return nil, fmt.Errorf("%w: %s", ErrToolNameMismatch, toolID)
			}
			if _, exists := resolved[toolID]; exists {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateToolResult, toolID)
			}
			resolved[toolID] = event
		}
	}
	pairs := make([]ToolPair, 0, len(order))
	for _, toolID := range order {
		result, exists := resolved[toolID]
		if !exists {
			return nil, fmt.Errorf("tool use %s has no terminal result", toolID)
		}
		pairs = append(pairs, ToolPair{Call: accepted[toolID], Result: result})
	}
	return pairs, nil
}
