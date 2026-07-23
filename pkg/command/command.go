// Package command defines user-invoked local routing. Commands are distinct
// from model-callable tools and durable asynchronous tasks.
package command

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Source string
type ResultKind string

const (
	SourceBuiltin Source     = "builtin"
	ResultLocal   ResultKind = "local"
	ResultPrompt  ResultKind = "prompt"
	ResultExit    ResultKind = "exit"
)

type Availability struct {
	Available bool
	Reason    string
}

type Descriptor struct {
	Name                   string
	Aliases                []string
	Description            string
	ArgumentHint           string
	Source                 Source
	Hidden                 bool
	Sensitive              bool
	UserInvocable          bool
	ModelInvocable         bool
	SupportsNonInteractive bool
	RemoteSafe             bool
	Availability           func() Availability
	Handler                Handler
}

type Invocation struct {
	Name         string
	Raw          string
	RawArguments string
	Arguments    []string
	HistoryForm  string
	RedactedForm string
}

type Result struct {
	Kind   ResultKind
	Output string
	Prompt string
}

type Handler func(context.Context, Invocation) (Result, error)

const (
	maximumDescriptors       = 4_096
	maximumAliases           = 256
	maximumNameBytes         = 256
	maximumDescriptionBytes  = 16 << 10
	maximumArgumentHintBytes = 4 << 10
	maximumResultBytes       = 16 << 20
)

type Registry struct {
	ordered []Descriptor
	byName  map[string]int
}

func New(descriptors []Descriptor) (*Registry, error) {
	if len(descriptors) > maximumDescriptors {
		return nil, ErrInvalidDescriptor
	}
	r := &Registry{byName: make(map[string]int)}
	for _, descriptor := range descriptors {
		if !validName(descriptor.Name) || descriptor.Handler == nil ||
			len(descriptor.Aliases) > maximumAliases ||
			!validDescriptorText(descriptor.Description, maximumDescriptionBytes) ||
			!validDescriptorText(descriptor.ArgumentHint, maximumArgumentHintBytes) {
			return nil, ErrInvalidDescriptor
		}
		descriptor = cloneDescriptor(descriptor)
		index := len(r.ordered)
		for _, name := range append([]string{descriptor.Name}, descriptor.Aliases...) {
			if !validName(name) {
				return nil, ErrInvalidDescriptor
			}
			if _, exists := r.byName[name]; exists {
				return nil, ErrDuplicate
			}
			r.byName[name] = index
		}
		r.ordered = append(r.ordered, descriptor)
	}
	return r, nil
}

func (r *Registry) Descriptors(includeHidden bool) []Descriptor {
	return r.DescriptorsForSurface(includeHidden, false)
}

// DescriptorsForSurface returns the descriptors that are safe to advertise on
// the selected surface. Headless and SDK clients must not discover commands
// that dispatch will later reject as terminal-UI-only operations.
func (r *Registry) DescriptorsForSurface(includeHidden, nonInteractive bool) []Descriptor {
	if r == nil {
		return nil
	}
	result := make([]Descriptor, 0, len(r.ordered))
	for _, descriptor := range r.ordered {
		if descriptor.Hidden && !includeHidden || nonInteractive && !descriptor.SupportsNonInteractive {
			continue
		}
		result = append(result, cloneDescriptor(descriptor))
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// HelpText renders the current interactive command catalog from the same
// descriptors used for dispatch, so documented names, hints, aliases, and
// descriptions cannot drift into a separate hand-maintained list.
func (r *Registry) HelpText(includeHidden bool) string {
	descriptors := r.Descriptors(includeHidden)
	if len(descriptors) == 0 {
		return "No commands are available for this session."
	}
	invocations := make([]string, len(descriptors))
	width := 0
	for index, descriptor := range descriptors {
		invocation := "/" + descriptor.Name
		if descriptor.ArgumentHint != "" {
			invocation += " " + descriptor.ArgumentHint
		}
		invocations[index] = invocation
		if len(invocation) > width {
			width = len(invocation)
		}
	}
	var output strings.Builder
	output.WriteString("Available commands:\n")
	for index, descriptor := range descriptors {
		description := descriptor.Description
		if len(descriptor.Aliases) > 0 {
			aliases := make([]string, len(descriptor.Aliases))
			for aliasIndex, alias := range descriptor.Aliases {
				aliases[aliasIndex] = "/" + alias
			}
			description += " (aliases: " + strings.Join(aliases, ", ") + ")"
		}
		fmt.Fprintf(&output, "  %-*s  %s", width, invocations[index], description)
		if index+1 < len(descriptors) {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// Parse distinguishes a syntactically valid slash command from ordinary model
// text. Absolute paths and malformed command-like input remain model prompts.
func Parse(input string) (Invocation, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || trimmed[0] != '/' {
		return Invocation{}, false
	}
	if trimmed == "/" {
		return Invocation{Raw: input, HistoryForm: trimmed, RedactedForm: trimmed}, true
	}
	first, rest, _ := strings.Cut(trimmed[1:], " ")
	if strings.ContainsAny(first, `/\\`) || !validName(first) {
		return Invocation{}, false
	}
	return Invocation{Name: first, Raw: input, RawArguments: rest, Arguments: splitArguments(rest), HistoryForm: trimmed, RedactedForm: trimmed}, true
}

var (
	ErrUnknown           = errors.New("Unknown command")
	ErrSyntax            = errors.New("Commands are in the form `/command [args]`")
	ErrNonInteractive    = errors.New("command is unavailable in noninteractive mode")
	ErrUnavailable       = errors.New("command is unavailable")
	ErrExecution         = errors.New("command execution failed")
	ErrSource            = errors.New("command is not invocable from this source")
	ErrInvalidDescriptor = errors.New("invalid command descriptor")
	ErrDuplicate         = errors.New("duplicate command name")
)

func (r *Registry) Dispatch(ctx context.Context, invocation Invocation, fromModel bool) (Result, error) {
	return r.dispatch(ctx, invocation, fromModel, false)
}

// DispatchNonInteractive enforces the descriptor-owned headless eligibility
// decision before loading or invoking the command handler.
func (r *Registry) DispatchNonInteractive(ctx context.Context, invocation Invocation, fromModel bool) (Result, error) {
	return r.dispatch(ctx, invocation, fromModel, true)
}

func (r *Registry) dispatch(ctx context.Context, invocation Invocation, fromModel, nonInteractive bool) (Result, error) {
	if r == nil {
		return Result{}, ErrUnavailable
	}
	if ctx == nil {
		return Result{}, ErrExecution
	}
	if invocation.Name == "" {
		return Result{}, ErrSyntax
	}
	index, ok := r.byName[invocation.Name]
	if !ok {
		return Result{}, ErrUnknown
	}
	descriptor := r.ordered[index]
	if fromModel && !descriptor.ModelInvocable || !fromModel && !descriptor.UserInvocable {
		return Result{}, ErrSource
	}
	if nonInteractive && !descriptor.SupportsNonInteractive {
		return Result{}, ErrNonInteractive
	}
	if descriptor.Availability != nil {
		availability, ok := commandAvailability(descriptor.Availability)
		if !ok {
			return Result{}, ErrUnavailable
		}
		if !availability.Available {
			// Availability callbacks are extension/provider boundaries. Their
			// diagnostics can contain configuration or credential material and
			// are not retained in a public error object or unwrap chain.
			return Result{}, ErrUnavailable
		}
	}
	invocation.Name = descriptor.Name
	invocation.Arguments = append([]string(nil), invocation.Arguments...)
	if descriptor.Sensitive {
		invocation.RedactedForm = "/" + descriptor.Name + " [REDACTED]"
	}
	return runHandler(ctx, descriptor, invocation)
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Aliases = append([]string(nil), descriptor.Aliases...)
	return descriptor
}

func commandAvailability(check func() Availability) (availability Availability, ok bool) {
	defer func() {
		if recover() != nil {
			availability = Availability{}
			ok = false
		}
	}()
	return check(), true
}

func runHandler(ctx context.Context, descriptor Descriptor, invocation Invocation) (result Result, err error) {
	defer func() {
		if recover() != nil {
			result = Result{}
			err = ErrExecution
		}
	}()
	result, err = descriptor.Handler(ctx, invocation)
	if err != nil {
		// A handler is an extension/runtime boundary. Preserve the command
		// identity while discarding the raw cause and its object graph.
		return Result{}, ErrExecution
	}
	if result.Kind != ResultLocal && result.Kind != ResultPrompt && result.Kind != ResultExit {
		return Result{}, ErrExecution
	}
	if len(result.Output) > maximumResultBytes || len(result.Prompt) > maximumResultBytes {
		return Result{}, ErrExecution
	}
	return result, nil
}

func validName(value string) bool {
	if value == "" || len(value) > maximumNameBytes {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == ':') {
			return false
		}
	}
	return true
}

func validDescriptorText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\t' || isBidiFormatting(character) {
			return false
		}
	}
	return true
}

func isBidiFormatting(character rune) bool {
	switch character {
	case '\u061c', '\u200e', '\u200f', '\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

func splitArguments(raw string) []string {
	// Commands preserve RawArguments; this lightweight split exists for hints and
	// built-ins and intentionally performs no shell expansion.
	return strings.Fields(raw)
}
