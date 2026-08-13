package command

import (
	"context"
	"fmt"
	"strings"
)

type Host interface {
	RunLocalCommand(context.Context, string, []string, string) (Result, error)
}

// Builtins returns the observable command catalog. A host may report a
// feature-gated operation as unavailable, but descriptors are not silently
// omitted simply because an optional service is absent.
func Builtins(host Host) (*Registry, error) {
	definitions := []struct {
		name, aliases, description, hint      string
		sensitive, nonInteractive, remoteSafe bool
	}{
		{"help", "", "Show commands and usage", "", false, false, true},
		{"clear", "reset", "Clear active model context", "", false, false, true},
		{"compact", "", "Compact projected context", "", false, true, true},
		{"resume", "continue", "Resume a durable session", "[session]", false, false, true},
		{"rewind", "", "Rewind projected conversation state", "[message]", false, false, false},
		{"branch", "fork", "Fork the active session", "[message]", false, false, true},
		{"model", "", "Show or assert the selected logical model", "[name]", false, false, true},
		{"effort", "thinking", "Show or select reasoning effort", "[level]", false, false, true},
		{"permissions", "permission", "Show or select permission mode", "[mode]", false, false, true},
		{"plan", "", "Enter or leave plan mode", "[on|off]", false, false, true},
		{"mcp", "", "Inspect or refresh MCP providers", "[status|reload|reconnect NAME]", false, false, false},
		{"plugin", "plugins", "Inspect plugin activation", "[list]", false, false, false},
		{"skills", "skill", "List available skills", "", false, false, true},
		{"agents", "agent", "Inspect delegated agents", "[list|stop]", false, false, true},
		{"tasks", "task", "List durable background tasks", "", false, false, true},
		{"memory", "", "Inspect or update selected memory", "[list|remember|recall]", false, false, false},
		{"status", "", "Show session status", "", false, false, true},
		{"cost", "usage", "Show token usage and known cost", "", false, true, true},
		{"output-style", "style", "Show active output style", "", false, false, true},
		{"doctor", "diagnostics", "Run read-only diagnostics", "", false, false, true},
		{"login", "", "Configure provider credentials", "", true, false, false},
		{"logout", "", "Remove cached provider credentials", "", true, false, false},
		{"exit", "quit", "End the session", "", false, false, true},
	}
	descriptors := make([]Descriptor, 0, len(definitions))
	for _, definition := range definitions {
		definition := definition
		descriptor := Descriptor{Name: definition.name, Description: definition.description, ArgumentHint: definition.hint, Source: SourceBuiltin, Sensitive: definition.sensitive, UserInvocable: true, ModelInvocable: false, SupportsNonInteractive: definition.nonInteractive, RemoteSafe: definition.remoteSafe}
		if definition.aliases != "" {
			descriptor.Aliases = strings.Split(definition.aliases, ",")
		}
		descriptor.Handler = func(ctx context.Context, invocation Invocation) (Result, error) {
			if host == nil {
				return Result{}, fmt.Errorf("command /%s has no runtime host", invocation.Name)
			}
			return host.RunLocalCommand(ctx, invocation.Name, invocation.Arguments, invocation.RawArguments)
		}
		descriptors = append(descriptors, descriptor)
	}
	return New(descriptors)
}
