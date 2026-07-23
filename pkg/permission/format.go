package permission

import (
	"fmt"
	"io"
)

// Formatting methods below intentionally report shape rather than values.
// Permission inputs, paths, rule text, approval reasons, and selected JSON are
// deliberate protocol data but are unsafe as an incidental logging surface.

func (c Config) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.Config{opaque}")
}

func (e *Evaluator) Format(state fmt.State, _ rune) {
	if e == nil {
		writePermissionSummary(state, "permission.Evaluator<nil>")
		return
	}
	writePermissionSummary(state, "permission.Evaluator{opaque}")
}

func (p PathAccess) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.PathAccess{opaque}")
}

func (p PathDisposition) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.PathDisposition{opaque}")
}

func (r Request) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.Request{opaque}")
}

func (d Decision) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.Decision{opaque}")
}

func (r ApprovalRequest) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.ApprovalRequest{opaque}")
}

func (r ApprovalResponse) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.ApprovalResponse{opaque}")
}

func (r Rule) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.Rule{opaque}")
}

func (s ShellAnalysis) Format(state fmt.State, _ rune) {
	writePermissionSummary(state, "permission.ShellAnalysis{opaque}")
}

func writePermissionSummary(state fmt.State, value string) {
	_, _ = io.WriteString(state, value)
}
