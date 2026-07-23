package command

import (
	"fmt"
	"io"
)

// Format keeps accidental logging and debug formatting from becoming a second
// command-output surface. Structured consumers should select deliberate fields.
func (a Availability) Format(state fmt.State, _ rune) {
	writeSummary(state, "command.Availability{opaque}")
}

// Format omits descriptor text and callback identities, either of which may
// originate in an extension or contain sensitive configuration.
func (d Descriptor) Format(state fmt.State, _ rune) {
	writeSummary(state, "command.Descriptor{opaque}")
}

// Format never emits raw command input. RedactedForm remains available as an
// explicit field for presentation code that has selected that contract.
func (i Invocation) Format(state fmt.State, _ rune) {
	writeSummary(state, "command.Invocation{opaque}")
}

// Format reports shape only; Output and Prompt are deliberate result channels
// and must not also escape through incidental debug formatting.
func (r Result) Format(state fmt.State, _ rune) {
	writeSummary(state, "command.Result{opaque}")
}

// Format omits the registry's descriptor callbacks and text.
func (r *Registry) Format(state fmt.State, _ rune) {
	if r == nil {
		writeSummary(state, "command.Registry<nil>")
		return
	}
	writeSummary(state, "command.Registry{opaque}")
}

func writeSummary(state fmt.State, value string) {
	_, _ = io.WriteString(state, value)
}
