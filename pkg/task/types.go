// Package task owns identity-bearing work that may outlive the tool call that
// launched it. Tool invocations and tasks deliberately have separate IDs and
// lifecycle records.
package task

import (
	"errors"
	"time"
)

// ID is a stable task identity.
type ID string

// Kind identifies the runtime implementation that owns a task.
type Kind string

const (
	KindShell Kind = "local_shell"
	KindWork  Kind = "work_item"
)

// Status is the lifecycle state of asynchronous work.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusKilled    Status = "killed"
)

// Terminal reports whether the state can no longer transition.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusKilled
}

// Record is the durable, presentation-independent task state. Process handles
// and cancellation functions remain process-local and are never serialized.
type Record struct {
	Version     int    `json:"version"`
	ID          ID     `json:"id"`
	Kind        Kind   `json:"kind"`
	Status      Status `json:"status"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	ToolUseID   string `json:"tool_use_id,omitempty"`
	Owner       string `json:"owner,omitempty"`
	OutputPath  string `json:"output_path,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
	// OutputIncomplete records an inspectability failure independently from
	// the process result. It deliberately carries no external error text.
	OutputIncomplete bool `json:"output_incomplete,omitempty"`
	// OutputWarning is a bounded, credential-sanitized presentation message.
	OutputWarning string     `json:"output_warning,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Notified      bool       `json:"notified"`
}

// WorkStatus is the task-v2 planning lifecycle. It is deliberately distinct
// from asynchronous runtime Status because its wire value is in_progress.
type WorkStatus string

const (
	WorkPending    WorkStatus = "pending"
	WorkInProgress WorkStatus = "in_progress"
	WorkCompleted  WorkStatus = "completed"
)

// WorkItem is an atomic identity-bearing planning task.
type WorkItem struct {
	Version     int               `json:"version"`
	ID          ID                `json:"id"`
	Subject     string            `json:"subject"`
	Description string            `json:"description"`
	ActiveForm  string            `json:"active_form"`
	Status      WorkStatus        `json:"status"`
	Owner       string            `json:"owner,omitempty"`
	Blockers    []ID              `json:"blockers,omitempty"`
	Dependents  []ID              `json:"dependents,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Todo is the mutually exclusive legacy planning surface.
type Todo struct {
	Content    string     `json:"content"`
	Status     WorkStatus `json:"status"`
	ActiveForm string     `json:"active_form"`
}

var (
	ErrNotFound        = errors.New("task not found")
	ErrNotRunning      = errors.New("task is not running")
	ErrInvalidState    = errors.New("invalid task state transition")
	ErrDependencyCycle = errors.New("task dependency cycle")
	ErrClosed          = errors.New("task manager is closed")
	ErrStopTimeout     = errors.New("timed out waiting for task process to stop")
	ErrBusy            = errors.New("task manager host callback is active")
)

// PollResult contains a byte-oriented output delta and authoritative state.
type PollResult struct {
	Task       Record `json:"task"`
	Output     string `json:"output"`
	NextOffset int64  `json:"next_offset"`
	TimedOut   bool   `json:"timed_out"`
}

// WorkPatch atomically changes a work item. Nil scalar fields mean unchanged;
// a nil metadata value removes that key.
type WorkPatch struct {
	Subject     *string
	Description *string
	ActiveForm  *string
	Status      *WorkStatus
	Owner       *string
	Blockers    *[]ID
	Metadata    map[string]*string
	Delete      bool
}
