package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type hostFunc func(context.Context, string, []string, string) (Result, error)

func (f hostFunc) RunLocalCommand(ctx context.Context, name string, args []string, raw string) (Result, error) {
	return f(ctx, name, args, raw)
}

func TestParseSeparatesPathsAndCommands(t *testing.T) {
	if invocation, ok := Parse(" /model gpt-5.6-sol "); !ok || invocation.Name != "model" || invocation.RawArguments != "gpt-5.6-sol" {
		t.Fatalf("invocation=%#v ok=%v", invocation, ok)
	}
	if invocation, ok := Parse("/model  gpt-5.6-sol"); !ok || invocation.RawArguments != " gpt-5.6-sol" {
		t.Fatalf("raw argument spacing was not preserved: invocation=%#v ok=%v", invocation, ok)
	}
	if invocation, ok := Parse("/"); !ok || invocation.Name != "" {
		t.Fatalf("bare slash was not classified for local syntax handling: invocation=%#v ok=%v", invocation, ok)
	}
	for _, input := range []string{"/tmp/file", "//server/share", "/bad.name", "/mødel"} {
		if _, ok := Parse(input); ok {
			t.Errorf("treated %q as command", input)
		}
	}
}

func TestDispatchAliasAndUnknown(t *testing.T) {
	registry, err := Builtins(hostFunc(func(_ context.Context, name string, _ []string, _ string) (Result, error) {
		return Result{Kind: ResultLocal, Output: name}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	invocation, _ := Parse("/usage")
	result, err := registry.Dispatch(context.Background(), invocation, false)
	if err != nil || result.Output != "cost" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	_, err = registry.Dispatch(context.Background(), Invocation{Name: "missing"}, false)
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("err=%v", err)
	}
	if err.Error() != "Unknown command" {
		t.Fatalf("unknown command message = %q", err)
	}
	if _, err := registry.Dispatch(context.Background(), Invocation{}, false); !errors.Is(err, ErrSyntax) {
		t.Fatalf("bare-slash syntax error = %v", err)
	}
	if _, err := registry.Dispatch(context.Background(), Invocation{Name: "COST"}, false); !errors.Is(err, ErrUnknown) {
		t.Fatalf("case-insensitive command lookup unexpectedly succeeded: %v", err)
	}
}

func TestHelpTextUsesDispatchCatalog(t *testing.T) {
	registry, err := Builtins(hostFunc(func(_ context.Context, name string, _ []string, _ string) (Result, error) {
		return Result{Kind: ResultLocal, Output: name}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	help := registry.HelpText(false)
	for _, expected := range []string{
		"Available commands:",
		"/help",
		"/memory [list|remember|recall]",
		"Show token usage and known cost (aliases: /usage)",
	} {
		if !strings.Contains(help, expected) {
			t.Errorf("help omitted %q:\n%s", expected, help)
		}
	}
	if strings.Contains(help, "--print") {
		t.Fatalf("slash-command help unexpectedly rendered process flags:\n%s", help)
	}
	if strings.Index(help, "/clear") > strings.Index(help, "/help") {
		t.Fatalf("help catalog is not sorted by canonical command name:\n%s", help)
	}
}

func TestNonInteractiveDispatchUsesDescriptorOptIn(t *testing.T) {
	called := make(map[string]int)
	registry, err := Builtins(hostFunc(func(_ context.Context, name string, _ []string, _ string) (Result, error) {
		called[name]++
		return Result{Kind: ResultLocal, Output: name}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"compact", "cost", "usage"} {
		invocation, ok := Parse("/" + name)
		if !ok {
			t.Fatalf("could not parse /%s", name)
		}
		if _, err := registry.DispatchNonInteractive(context.Background(), invocation, false); err != nil {
			t.Fatalf("supported noninteractive command /%s failed: %v", name, err)
		}
	}
	for _, name := range []string{"mcp", "help", "model", "skills", "tasks", "doctor", "exit"} {
		invocation, ok := Parse("/" + name)
		if !ok {
			t.Fatalf("could not parse /%s", name)
		}
		if _, err := registry.DispatchNonInteractive(context.Background(), invocation, false); !errors.Is(err, ErrNonInteractive) {
			t.Fatalf("unsupported noninteractive command /%s = %v", name, err)
		}
		if called[name] != 0 {
			t.Fatalf("unsupported noninteractive command /%s invoked its handler", name)
		}
	}
	want := []string{"compact", "cost"}
	got := registry.DescriptorsForSurface(false, true)
	if len(got) != len(want) {
		t.Fatalf("noninteractive descriptors = %#v", got)
	}
	for index := range want {
		if got[index].Name != want[index] {
			t.Fatalf("noninteractive descriptor %d = %q, want %q", index, got[index].Name, want[index])
		}
	}
}

func TestBuiltinArgumentHintsMatchSupportedHostSurface(t *testing.T) {
	registry, err := Builtins(hostFunc(func(_ context.Context, name string, _ []string, _ string) (Result, error) {
		return Result{Kind: ResultLocal, Output: name}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := make(map[string]Descriptor)
	for _, descriptor := range registry.Descriptors(true) {
		descriptors[descriptor.Name] = descriptor
	}
	for _, test := range []struct {
		name        string
		hint        string
		description string
	}{
		{name: "compact", hint: "", description: "Compact projected context"},
		{name: "mcp", hint: "[status|reload|reconnect NAME]", description: "Inspect or refresh MCP providers"},
		{name: "plugin", hint: "[list]", description: "Inspect plugin activation"},
		{name: "skills", hint: "", description: "List available skills"},
		{name: "tasks", hint: "", description: "List durable background tasks"},
		{name: "output-style", hint: "", description: "Show active output style"},
	} {
		descriptor, ok := descriptors[test.name]
		if !ok {
			t.Fatalf("missing builtin descriptor %q", test.name)
		}
		if descriptor.ArgumentHint != test.hint || descriptor.Description != test.description {
			t.Errorf("/%s contract = hint %q description %q, want %q and %q", test.name, descriptor.ArgumentHint, descriptor.Description, test.hint, test.description)
		}
	}
}

func TestDispatchContainsCallbackFailuresAndDropsRawCauses(t *testing.T) {
	const secret = "command-callback-secret-must-not-escape"
	tests := []struct {
		name       string
		descriptor Descriptor
		want       error
	}{
		{
			name: "availability panic",
			descriptor: Descriptor{
				Name: "test", UserInvocable: true, Handler: func(context.Context, Invocation) (Result, error) {
					return Result{Kind: ResultLocal, Output: "unexpected"}, nil
				},
				Availability: func() Availability { panic(secret) },
			},
			want: ErrUnavailable,
		},
		{
			name: "availability reason",
			descriptor: Descriptor{
				Name: "test", UserInvocable: true, Handler: func(context.Context, Invocation) (Result, error) {
					return Result{Kind: ResultLocal, Output: "unexpected"}, nil
				},
				Availability: func() Availability { return Availability{Reason: secret} },
			},
			want: ErrUnavailable,
		},
		{
			name: "handler panic",
			descriptor: Descriptor{
				Name: "test", UserInvocable: true,
				Handler: func(context.Context, Invocation) (Result, error) { panic(secret) },
			},
			want: ErrExecution,
		},
		{
			name: "handler error",
			descriptor: Descriptor{
				Name: "test", UserInvocable: true,
				Handler: func(context.Context, Invocation) (Result, error) {
					return Result{}, errors.New(secret)
				},
			},
			want: ErrExecution,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := New([]Descriptor{test.descriptor})
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Dispatch(context.Background(), Invocation{Name: "test"}, false)
			if !errors.Is(err, test.want) {
				t.Fatalf("dispatch error = %v, want %v", err, test.want)
			}
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("callback payload escaped in %q", rendered)
				}
			}
		})
	}
}

func TestRegistrySnapshotsCallerOwnedSlices(t *testing.T) {
	aliases := []string{"old"}
	var observed []string
	registry, err := New([]Descriptor{{
		Name: "test", Aliases: aliases, UserInvocable: true,
		Handler: func(_ context.Context, invocation Invocation) (Result, error) {
			observed = append([]string(nil), invocation.Arguments...)
			invocation.Arguments[0] = "handler-mutated"
			return Result{Kind: ResultLocal}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	aliases[0] = "mutated"
	descriptors := registry.Descriptors(true)
	descriptors[0].Aliases[0] = "published-mutated"
	if _, err := registry.Dispatch(context.Background(), Invocation{Name: "old", Arguments: []string{"safe"}}, false); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0] != "safe" {
		t.Fatalf("handler observed aliased arguments: %#v", observed)
	}
	if current := registry.Descriptors(true); len(current) != 1 || len(current[0].Aliases) != 1 || current[0].Aliases[0] != "old" {
		t.Fatalf("registry descriptors were caller-mutable: %#v", current)
	}
}

func TestSecretBearingCommandValuesHaveOpaqueDebugFormatting(t *testing.T) {
	const secret = "format-secret-must-not-escape"
	values := []any{
		Availability{Reason: secret},
		Descriptor{Name: secret, Aliases: []string{secret}, Description: secret, ArgumentHint: secret, Source: Source(secret)},
		Invocation{Name: secret, Raw: secret, RawArguments: secret, Arguments: []string{secret}, HistoryForm: secret, RedactedForm: secret},
		Result{Kind: ResultKind(secret), Output: secret, Prompt: secret},
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if rendered := fmt.Sprintf(format, value); strings.Contains(rendered, secret) {
				t.Fatalf("%T %s exposed secret in %q", value, format, rendered)
			}
		}
	}
}

func TestRegistryRejectsUnsafeDescriptorAndHandlerResultShapes(t *testing.T) {
	handler := func(context.Context, Invocation) (Result, error) {
		return Result{Kind: ResultLocal}, nil
	}
	for _, descriptor := range []Descriptor{
		{Name: "bad", Description: "line one\nline two", Handler: handler},
		{Name: "bad", ArgumentHint: "unsafe\rhint", Handler: handler},
		{Name: "bad", Description: "spoof\u202etext", Handler: handler},
		{Name: "bad", Description: string([]byte{0xff}), Handler: handler},
		{Name: strings.Repeat("x", maximumNameBytes+1), Handler: handler},
		{Name: "bad", Aliases: make([]string, maximumAliases+1), Handler: handler},
	} {
		if _, err := New([]Descriptor{descriptor}); !errors.Is(err, ErrInvalidDescriptor) {
			t.Fatalf("unsafe descriptor error = %v", err)
		}
	}

	registry, err := New([]Descriptor{{
		Name: "test", UserInvocable: true,
		Handler: func(context.Context, Invocation) (Result, error) {
			return Result{Kind: ResultKind("provider-controlled-kind"), Output: "unsafe"}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := registry.Dispatch(context.Background(), Invocation{Name: "test"}, false); !errors.Is(err, ErrExecution) || result != (Result{}) {
		t.Fatalf("invalid handler result = %#v, %v", result, err)
	}

	oversized, err := New([]Descriptor{{
		Name: "test", UserInvocable: true,
		Handler: func(context.Context, Invocation) (Result, error) {
			return Result{Kind: ResultLocal, Output: strings.Repeat("x", maximumResultBytes+1)}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := oversized.Dispatch(context.Background(), Invocation{Name: "test"}, false); !errors.Is(err, ErrExecution) || result != (Result{}) {
		t.Fatalf("oversized handler result = %#v, %v", result, err)
	}
}
