package task

import (
	"fmt"
	"io"
)

// Task state remains available through its explicit JSON and accessor
// contracts. Incidental fmt formatting reports only shape so commands, output,
// descriptions, paths, metadata, and errors do not become a parallel egress
// surface.

func (r Record) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.Record{opaque}")
}

func (w WorkItem) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.WorkItem{opaque}")
}

func (t Todo) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.Todo{opaque}")
}

func (p PollResult) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.PollResult{opaque}")
}

func (p WorkPatch) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.WorkPatch{opaque}")
}

func (o Options) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.Options{opaque}")
}

func (s ShellSpec) Format(state fmt.State, _ rune) {
	writeTaskSummary(state, "task.ShellSpec{opaque}")
}

func (m *Manager) Format(state fmt.State, _ rune) {
	if m == nil {
		writeTaskSummary(state, "task.Manager<nil>")
		return
	}
	writeTaskSummary(state, "task.Manager{opaque}")
}

func writeTaskSummary(state fmt.State, value string) {
	_, _ = io.WriteString(state, value)
}
