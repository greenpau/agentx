package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/task"
)

const (
	maximumTaskBusyAttempts = 8
	initialTaskBusyDelay    = time.Millisecond
	maximumTaskBusyDelay    = 8 * time.Millisecond
)

// retryTaskBusy bridges the task manager's fail-fast callback guard to the
// tool protocol. ErrBusy proves that the attempted manager operation did not
// enter its state transition, so retrying that exact sentinel is safe. Every
// other error returns untouched without invoking foreign error methods.
func retryTaskBusy[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, errors.New("task tool context is nil")
	}
	delay := initialTaskBusyDelay
	for attempt := 0; attempt < maximumTaskBusyAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := operation()
		if err != task.ErrBusy {
			return value, err
		}
		if attempt == maximumTaskBusyAttempts-1 {
			return zero, task.ErrBusy
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return zero, ctx.Err()
		case <-timer.C:
		}
		if delay < maximumTaskBusyDelay {
			delay *= 2
			if delay > maximumTaskBusyDelay {
				delay = maximumTaskBusyDelay
			}
		}
	}
	return zero, task.ErrBusy
}

func retryTaskBusyError(ctx context.Context, operation func() error) error {
	_, err := retryTaskBusy(ctx, func() (struct{}, error) {
		return struct{}{}, operation()
	})
	return err
}

func taskBusySemanticError(err error) error {
	if err == task.ErrBusy {
		return semanticInvocationError("unavailable", "task runtime remained busy; retry the tool")
	}
	return err
}

func taskInvocationError(operation string, err error) error {
	switch err {
	case task.ErrBusy:
		return invocationError("unavailable", "%s: task runtime remained busy; retry the tool", operation)
	case task.ErrClosed:
		return invocationError("unavailable", "%s: task runtime is closed", operation)
	case task.ErrStopTimeout:
		return invocationError("timeout", "%s: task process did not stop before its deadline", operation)
	case task.ErrNotFound, task.ErrNotRunning, task.ErrInvalidState, task.ErrDependencyCycle:
		return invocationError("semantic_invalid", "%s: %v", operation, err)
	default:
		return invocationError("execution_failed", "%s: %v", operation, err)
	}
}

type taskOutputInput struct {
	TaskID  string `json:"task_id"`
	Offset  int64  `json:"offset,omitempty"`
	Timeout *int   `json:"timeout,omitempty"`
	Block   *bool  `json:"block,omitempty"`
}

func taskOutputDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskOutput", Aliases: []string{"AgentOutputTool", "BashOutputTool"}, Source: SourceBuiltin,
		Description: "Poll one background task for authoritative status and new byte-oriented output.",
		InputSchema: objectSchema(map[string]any{
			"task_id": stringSchema("Stable task ID"), "offset": integerSchema("Previously delivered byte offset", 0, task.MaximumOutputFileBytes),
			"timeout": integerSchema("Polling timeout in milliseconds", 0, 600_000), "block": booleanSchema("Wait for output or completion"),
		}, "task_id"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input taskOutputInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.TaskID == "" || input.Offset < 0 || (input.Timeout != nil && (*input.Timeout < 0 || *input.Timeout > 600_000)) {
				return nil, errors.New("invalid task_id, offset, or timeout")
			}
			return input, nil
		},
		Semantic: func(value any) error {
			_, err := retryTaskBusy(context.Background(), func() (task.Record, error) {
				return manager.Get(task.ID(value.(taskOutputInput).TaskID))
			})
			return taskBusySemanticError(err)
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(taskOutputInput)
			block := true
			if input.Block != nil {
				block = *input.Block
			}
			timeout := 30_000
			if input.Timeout != nil {
				timeout = *input.Timeout
			}
			timeoutDuration := time.Duration(timeout) * time.Millisecond
			pollStarted := time.Now()
			result, err := retryTaskBusy(ctx, func() (task.PollResult, error) {
				remaining := timeoutDuration
				expired := false
				if block && timeoutDuration > 0 {
					remaining = timeoutDuration - time.Since(pollStarted)
					if remaining <= 0 {
						remaining = 0
						expired = true
					}
				}
				pollBlock := block && !expired
				result, pollErr := manager.Poll(ctx, task.ID(input.TaskID), input.Offset, pollBlock, remaining)
				if pollErr == nil && expired && result.Output == "" && !result.Task.Status.Terminal() {
					result.TimedOut = true
				}
				return result, pollErr
			})
			if err != nil {
				return Output{}, taskInvocationError("poll task", err)
			}
			payload, _ := json.Marshal(result)
			return Output{Content: string(payload), Metadata: map[string]any{"task_id": input.TaskID, "status": result.Task.Status, "next_offset": result.NextOffset}}, nil
		},
		MaxResultChars: 100_000,
	}
}

type taskStopInput struct {
	TaskID  string `json:"task_id,omitempty"`
	ShellID string `json:"shell_id,omitempty"`
}

func taskStopDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskStop", Aliases: []string{"KillShell"}, Source: SourceBuiltin, Description: "Stop one running task by stable identity.",
		InputSchema: objectSchema(map[string]any{"task_id": stringSchema("Stable task ID"), "shell_id": stringSchema("Legacy shell task ID")}),
		Validate: func(raw json.RawMessage) (any, error) {
			var input taskStopInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.TaskID == "" && input.ShellID == "" {
				return nil, errors.New("provide exactly one of task_id or shell_id")
			}
			if input.TaskID != "" && input.ShellID != "" && input.TaskID != input.ShellID {
				return nil, errors.New("task_id and shell_id identify different tasks")
			}
			if input.TaskID == "" {
				input.TaskID = input.ShellID
			}
			// Strict function-call producers can materialize every optional schema
			// member. Treat equal canonical and legacy aliases as one identity;
			// conflicting values remain structurally invalid.
			input.ShellID = ""
			return input, nil
		},
		Semantic: func(value any) error {
			record, err := retryTaskBusy(context.Background(), func() (task.Record, error) {
				return manager.Get(task.ID(value.(taskStopInput).TaskID))
			})
			if err != nil {
				return taskBusySemanticError(err)
			}
			if record.Status != task.StatusRunning {
				return task.ErrNotRunning
			}
			return nil
		},
		Classify: func(any) permission.Classification { return permission.Classification{} },
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			id := task.ID(value.(taskStopInput).TaskID)
			if err := retryTaskBusyError(ctx, func() error { return manager.Stop(id) }); err != nil {
				return Output{}, taskInvocationError("stop task", err)
			}
			record, err := retryTaskBusy(ctx, func() (task.Record, error) { return manager.Get(id) })
			if err != nil {
				return Output{}, taskInvocationError("read stopped task", err)
			}
			payload, _ := json.Marshal(record)
			return Output{Content: string(payload), Metadata: map[string]any{"task_id": id, "status": record.Status}}, nil
		},
	}
}

type taskCreateInput struct {
	Subject     string            `json:"subject"`
	Description string            `json:"description"`
	ActiveForm  string            `json:"active_form"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func taskCreateDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskCreate", Source: SourceBuiltin, Description: "Create a durable pending planning task.",
		InputSchema: objectSchema(map[string]any{
			"subject": stringSchema("Short task subject"), "description": stringSchema("Detailed task objective"),
			"active_form": stringSchema("Present-progress wording"), "metadata": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}, "subject", "description", "active_form"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input taskCreateInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.Subject == "" || input.Description == "" || input.ActiveForm == "" {
				return nil, errors.New("subject, description, and active_form are required")
			}
			return input, nil
		},
		Classify: func(any) permission.Classification { return permission.Classification{} },
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(taskCreateInput)
			item, err := retryTaskBusy(ctx, func() (task.WorkItem, error) {
				return manager.CreateWork(input.Subject, input.Description, input.ActiveForm, input.Metadata)
			})
			if err != nil {
				return Output{}, taskInvocationError("create task", err)
			}
			payload, _ := json.Marshal(item)
			return Output{Content: string(payload), Metadata: map[string]any{"task_id": item.ID}}, nil
		},
	}
}

type taskIDInput struct {
	TaskID string `json:"task_id"`
}

func taskGetDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskGet", Source: SourceBuiltin, Description: "Get one authoritative planning task.",
		InputSchema: objectSchema(map[string]any{"task_id": stringSchema("Stable task ID")}, "task_id"),
		Validate:    taskIDValidator,
		Semantic: func(value any) error {
			_, err := retryTaskBusy(context.Background(), func() (task.WorkItem, error) {
				return manager.GetWork(task.ID(value.(taskIDInput).TaskID))
			})
			return taskBusySemanticError(err)
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			item, err := retryTaskBusy(ctx, func() (task.WorkItem, error) {
				return manager.GetWork(task.ID(value.(taskIDInput).TaskID))
			})
			if err != nil {
				return Output{}, taskInvocationError("get task", err)
			}
			payload, _ := json.Marshal(item)
			return Output{Content: string(payload)}, nil
		},
	}
}

func taskIDValidator(raw json.RawMessage) (any, error) {
	var input taskIDInput
	if err := decodeStrict(raw, &input); err != nil {
		return nil, err
	}
	if input.TaskID == "" {
		return nil, errors.New("task_id is required")
	}
	return input, nil
}

func taskListDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskList", Source: SourceBuiltin, Description: "List durable planning tasks in deterministic order.",
		InputSchema: objectSchema(map[string]any{}),
		Validate: func(raw json.RawMessage) (any, error) {
			var input struct{}
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		Call: func(ctx context.Context, _ CallContext, _ any) (Output, error) {
			items, err := retryTaskBusy(ctx, func() ([]task.WorkItem, error) {
				return manager.ListWorkContext(ctx)
			})
			if err != nil {
				return Output{}, taskInvocationError("list tasks", err)
			}
			payload, _ := json.Marshal(items)
			return Output{Content: string(payload)}, nil
		},
	}
}

type taskUpdateInput struct {
	TaskID      string             `json:"task_id"`
	Subject     *string            `json:"subject,omitempty"`
	Description *string            `json:"description,omitempty"`
	ActiveForm  *string            `json:"active_form,omitempty"`
	Status      *task.WorkStatus   `json:"status,omitempty"`
	Owner       *string            `json:"owner,omitempty"`
	Blockers    *[]task.ID         `json:"blockers,omitempty"`
	Metadata    map[string]*string `json:"metadata,omitempty"`
	Delete      bool               `json:"delete,omitempty"`
}

func taskUpdateDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TaskUpdate", Source: SourceBuiltin, Description: "Atomically update or delete one durable planning task.",
		InputSchema: objectSchema(map[string]any{
			"task_id": stringSchema("Stable task ID"), "subject": stringSchema("Replacement subject"),
			"description": stringSchema("Replacement description"), "active_form": stringSchema("Replacement active form"),
			"status": enumSchema("New state", "pending", "in_progress", "completed"), "owner": stringSchema("Owner identity"),
			"blockers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": []string{"string", "null"}}},
			"delete":   booleanSchema("Delete this task"),
		}, "task_id"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input taskUpdateInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.TaskID == "" {
				return nil, errors.New("task_id is required")
			}
			return input, nil
		},
		Semantic: func(value any) error {
			_, err := retryTaskBusy(context.Background(), func() (task.WorkItem, error) {
				return manager.GetWork(task.ID(value.(taskUpdateInput).TaskID))
			})
			return taskBusySemanticError(err)
		},
		Classify: func(value any) permission.Classification {
			return permission.Classification{Destructive: value.(taskUpdateInput).Delete}
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(taskUpdateInput)
			item, err := retryTaskBusy(ctx, func() (task.WorkItem, error) {
				return manager.UpdateWork(task.ID(input.TaskID), task.WorkPatch{
					Subject: input.Subject, Description: input.Description, ActiveForm: input.ActiveForm,
					Status: input.Status, Owner: input.Owner, Blockers: input.Blockers, Metadata: input.Metadata, Delete: input.Delete,
				})
			})
			if err != nil {
				return Output{}, taskInvocationError("update task", err)
			}
			if input.Delete {
				return Output{Content: fmt.Sprintf("deleted task %s", input.TaskID)}, nil
			}
			payload, _ := json.Marshal(item)
			return Output{Content: string(payload)}, nil
		},
	}
}

type todoWriteInput struct {
	Todos []task.Todo `json:"todos"`
}

func todoWriteDescriptor(manager *task.Manager) Descriptor {
	return Descriptor{
		Name: "TodoWrite", Source: SourceBuiltin, Description: "Replace the complete legacy todo list.",
		InputSchema: objectSchema(map[string]any{"todos": map[string]any{"type": "array", "items": objectSchema(map[string]any{
			"content": stringSchema("Todo text"), "status": enumSchema("Todo state", "pending", "in_progress", "completed"), "active_form": stringSchema("Present-progress wording"),
		}, "content", "status", "active_form")}}, "todos"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input todoWriteInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			return input, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(todoWriteInput)
			if err := retryTaskBusyError(ctx, func() error { return manager.ReplaceTodos(input.Todos) }); err != nil {
				return Output{}, taskInvocationError("replace todos", err)
			}
			stored := append([]task.Todo(nil), input.Todos...)
			allComplete := len(stored) > 0
			for _, todo := range stored {
				if todo.Status != task.WorkCompleted {
					allComplete = false
					break
				}
			}
			if allComplete {
				stored = nil
			}
			payload, _ := json.Marshal(stored)
			return Output{Content: string(payload)}, nil
		},
	}
}
