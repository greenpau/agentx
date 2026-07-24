package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/greenpau/agentx/pkg/command"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/features"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/task"
)

const taskCallbackRetryAttempts = 8

// dispatchUserCommand is the one slash-routing boundary shared by interactive,
// aggregate headless, and duplex SDK input. A syntactically invalid slash-like
// string remains ordinary model text. A valid unknown name, or a recognized
// command that is not eligible for this surface, is handled as a local error
// and must never become an API-bound user message.
func (r *runtimeSession) dispatchUserCommand(ctx context.Context, input string, nonInteractive bool) (command.Result, bool, error) {
	invocation, commandLike := command.Parse(input)
	if !commandLike {
		return command.Result{}, false, nil
	}
	registry, err := command.Builtins(r)
	if err != nil {
		return command.Result{}, true, err
	}
	if nonInteractive {
		result, dispatchErr := registry.DispatchNonInteractive(ctx, invocation, false)
		if operationalErrorIs(dispatchErr, command.ErrUnknown) && existingAbsoluteSlashPath(input) {
			return command.Result{}, false, nil
		}
		return r.sanitizeCommandResult(result), true, redactOperationalError(dispatchErr, r.sanitize)
	}
	result, dispatchErr := registry.Dispatch(ctx, invocation, false)
	if operationalErrorIs(dispatchErr, command.ErrUnknown) && existingAbsoluteSlashPath(input) {
		return command.Result{}, false, nil
	}
	return r.sanitizeCommandResult(result), true, redactOperationalError(dispatchErr, r.sanitize)
}

func (r *runtimeSession) sanitizeCommandResult(result command.Result) command.Result {
	result.Output = r.sanitize(result.Output)
	result.Prompt = r.sanitize(result.Prompt)
	return result
}

func existingAbsoluteSlashPath(input string) bool {
	candidate := strings.TrimSpace(input)
	if strings.ContainsAny(candidate, " \t\r\n") || !filepath.IsAbs(candidate) {
		return false
	}
	_, err := os.Stat(candidate)
	return err == nil
}

func (r *runtimeSession) localCommandOutcome(result command.Result, runErr error, started time.Time) engine.Outcome {
	status := r.engine.Status()
	outcome := engine.Outcome{
		SessionID: status.SessionID,
		Status:    protocol.TurnResultSuccess,
		Text:      r.sanitize(result.Output),
		Usage:     status.Usage,
		Duration:  time.Since(started),
	}
	if runErr != nil {
		outcome.Status = protocol.TurnResultError
		outcome.StopReason = "command_error"
	}
	return outcome
}

func (r *runtimeSession) RunLocalCommand(ctx context.Context, name string, args []string, raw string) (command.Result, error) {
	switch name {
	case "help":
		registry, err := command.Builtins(r)
		if err != nil {
			return command.Result{}, err
		}
		return command.Result{Kind: command.ResultLocal, Output: registry.HelpText(false)}, nil
	case "exit":
		return command.Result{Kind: command.ResultExit}, nil
	case "clear":
		if err := r.engine.ClearContext(ctx); err != nil {
			return command.Result{}, err
		}
		return command.Result{Kind: command.ResultLocal, Output: "Model context cleared; durable transcript retained."}, nil
	case "model":
		status := r.engine.Status()
		if raw != "" && raw != status.Model {
			return command.Result{}, fmt.Errorf("model is fixed by auth.json to %s", status.Model)
		}
		return command.Result{Kind: command.ResultLocal, Output: status.Model}, nil
	case "effort":
		if raw != "" {
			if err := r.engine.SetReasoningEffort(ctx, strings.TrimSpace(raw)); err != nil {
				return command.Result{}, err
			}
		}
		return command.Result{Kind: command.ResultLocal, Output: r.engine.Status().ReasoningEffort}, nil
	case "skills":
		if strings.TrimSpace(raw) != "" {
			return command.Result{}, errors.New("usage: /skills")
		}
		var lines []string
		for _, skill := range r.skills.Skills {
			if !skill.Availability.Usable() {
				continue
			}
			lines = append(lines, skill.Summary())
		}
		if len(lines) == 0 {
			lines = []string{"No skills discovered for this session."}
		}
		return command.Result{Kind: command.ResultLocal, Output: strings.Join(lines, "\n")}, nil
	case "tasks":
		if strings.TrimSpace(raw) != "" {
			return command.Result{}, errors.New("usage: /tasks")
		}
		records, err := listTasksAfterCallback(ctx, r.tasks)
		if err != nil {
			return command.Result{}, err
		}
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return command.Result{}, errors.New("encode task list")
		}
		return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
	case "status":
		status := r.engine.Status()
		availability := make(map[features.ID]features.Evaluation, len(r.services.features))
		for id, state := range r.services.features {
			availability[id] = features.Evaluate(state)
		}
		data, _ := json.MarshalIndent(map[string]any{"session_id": status.SessionID, "model": status.Model, "reasoning_effort": status.ReasoningEffort, "projected_items": status.ProjectedItems, "workspace": r.workspace, "workspace_trusted": r.services.trusted, "tools": len(r.registry.Descriptors()), "platform": r.services.platform, "features": availability}, "", "  ")
		return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
	case "cost":
		usage := r.engine.Status().Usage
		data, _ := json.MarshalIndent(map[string]any{"usage": usage, "cost_usd": nil, "note": "cost is unknown because no authoritative deployment price was configured"}, "", "  ")
		return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
	case "doctor":
		data, _ := json.MarshalIndent(r.services.doctor.Run(ctx), "", "  ")
		return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
	case "compact":
		if strings.TrimSpace(raw) != "" {
			return command.Result{}, errors.New("manual compaction instructions are not supported by the deterministic local projector")
		}
		if err := r.engine.CompactContext(ctx); err != nil {
			return command.Result{}, err
		}
		return command.Result{Kind: command.ResultLocal, Output: "Model context compacted; durable transcript retained."}, nil
	case "resume", "rewind", "branch":
		return command.Result{}, errors.New("session placement commands must be selected at process startup in this build")
	case "permissions", "plan":
		return command.Result{}, errors.New("live permission-mode replacement is unavailable; restart with --permission-mode")
	case "mcp":
		action, target := localCommandAction(raw)
		switch action {
		case "", "status":
			snapshot := r.services.extensions.mcp.Snapshot()
			snapshot.Diagnostics = append(snapshot.Diagnostics, r.services.extensions.mcpDiagnostics...)
			data, _ := json.MarshalIndent(snapshot, "", "  ")
			return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
		case "reload":
			snapshot := r.services.extensions.mcp.Reconcile(ctx, r.services.extensions.mcpConfigs)
			data, _ := json.MarshalIndent(snapshot, "", "  ")
			return command.Result{Kind: command.ResultLocal, Output: string(data) + "\nTool registry changes take effect in a new session."}, nil
		case "reconnect":
			if target == "" {
				return command.Result{}, errors.New("usage: /mcp [status|reload|reconnect NAME]")
			}
			if err := r.services.extensions.mcp.Reconnect(ctx, target); err != nil {
				return command.Result{}, errors.New("MCP server reconnect failed; inspect /mcp status for a bounded diagnostic")
			}
			return command.Result{Kind: command.ResultLocal, Output: "MCP server reconnected."}, nil
		default:
			return command.Result{}, errors.New("usage: /mcp [status|reload|reconnect NAME]")
		}
	case "plugin":
		action := strings.ToLower(strings.TrimSpace(raw))
		if action != "" && action != "list" {
			return command.Result{}, errors.New("usage: /plugin [list]")
		}
		data, _ := json.MarshalIndent(r.services.extensions.plugins, "", "  ")
		return command.Result{Kind: command.ResultLocal, Output: string(data)}, nil
	case "agents":
		return command.Result{}, errors.New("delegated-agent backend is not configured")
	case "memory":
		if r.services.memory == nil {
			return command.Result{}, errors.New("memory is disabled for non-persistent sessions")
		}
		action, remainder, _ := strings.Cut(strings.TrimSpace(raw), " ")
		switch strings.ToLower(action) {
		case "", "list":
			entries, err := r.services.memory.Recall("", 25_000, time.Now())
			if err != nil {
				return command.Result{}, err
			}
			output, err := memoryEntriesJSON(r.services.memory, entries)
			if err != nil {
				return command.Result{}, err
			}
			return command.Result{Kind: command.ResultLocal, Output: output}, nil
		case "recall":
			entries, err := r.services.memory.Recall(strings.TrimSpace(remainder), 25_000, time.Now())
			if err != nil {
				return command.Result{}, err
			}
			output, err := memoryEntriesJSON(r.services.memory, entries)
			if err != nil {
				return command.Result{}, err
			}
			return command.Result{Kind: command.ResultLocal, Output: output}, nil
		case "remember":
			name, content, ok := strings.Cut(strings.TrimSpace(remainder), " ")
			if !ok || strings.TrimSpace(content) == "" {
				return command.Result{}, errors.New("usage: /memory remember NAME CONTENT")
			}
			if err := r.services.memory.Remember(name, content); err != nil {
				return command.Result{}, err
			}
			return command.Result{Kind: command.ResultLocal, Output: "Memory saved without copying it into the transcript."}, nil
		default:
			return command.Result{}, errors.New("usage: /memory [list|recall QUERY|remember NAME CONTENT]")
		}
	case "output-style":
		if strings.TrimSpace(raw) != "" {
			return command.Result{}, errors.New("output style is frozen for this session; restart with --output-style")
		}
		selection := r.services.extensions.selection
		if selection.Style == nil {
			return command.Result{Kind: command.ResultLocal, Output: "default"}, nil
		}
		return command.Result{Kind: command.ResultLocal, Output: selection.Style.CanonicalName}, nil
	case "login", "logout":
		return command.Result{}, errors.New("Azure credentials are owned by auth.json in the AgentX home; this command will not mutate them")
	default:
		return command.Result{}, fmt.Errorf("unsupported command /%s", name)
	}
}

func listTasksAfterCallback(ctx context.Context, manager *task.Manager) ([]task.Record, error) {
	delay := time.Millisecond
	for attempt := 0; attempt < taskCallbackRetryAttempts; attempt++ {
		records, err := manager.ListContext(ctx)
		if err != task.ErrBusy {
			return records, err
		}
		if attempt == taskCallbackRetryAttempts-1 {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	return nil, task.ErrBusy
}

// localCommandAction parses one action token while preserving the spelling and
// internal spaces of the remaining argument. Command.Parse already trims raw
// arguments, but RunLocalCommand is also a public host boundary and must not
// derive indexes from a differently normalized string.
func localCommandAction(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	action, remainder, _ := strings.Cut(trimmed, " ")
	return strings.ToLower(action), strings.TrimSpace(remainder)
}
