package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/memory"
)

func TestMemoryPromptGuardsCompleteComposition(t *testing.T) {
	const framedCredential = ")\nordinary content"
	store, err := memory.Open(t.TempDir(), func(value string) bool {
		return strings.Contains(value, framedCredential)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("note", "ordinary content"); err != nil {
		t.Fatal(err)
	}

	prompt, err := memoryPrompt(store)
	if !errors.Is(err, memory.ErrSecret) {
		t.Fatalf("memoryPrompt error = %v, want ErrSecret", err)
	}
	if prompt != "" {
		t.Fatalf("unsafe memory prompt was returned: %q", prompt)
	}
}

func TestMemoryCommandsGuardExactPrettyJSON(t *testing.T) {
	const framedCredential = "\n  {\n    \"name\""
	store, err := memory.Open(t.TempDir(), func(value string) bool {
		return strings.Contains(value, framedCredential)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("note", "ordinary content"); err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeSession{services: runtimeServices{memory: store}}

	for _, raw := range []string{"list", "recall ordinary"} {
		t.Run(raw, func(t *testing.T) {
			result, err := runtime.RunLocalCommand(t.Context(), "memory", nil, raw)
			if !errors.Is(err, memory.ErrSecret) {
				t.Fatalf("RunLocalCommand error = %v, want ErrSecret", err)
			}
			if result.Output != "" {
				t.Fatalf("unsafe memory JSON was returned: %q", result.Output)
			}
		})
	}
}

func TestMemoryCommandsPreserveExplicitEmptyJSON(t *testing.T) {
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeSession{services: runtimeServices{memory: store}}

	for _, raw := range []string{"list", "recall missing"} {
		t.Run(raw, func(t *testing.T) {
			result, err := runtime.RunLocalCommand(t.Context(), "memory", nil, raw)
			if err != nil {
				t.Fatal(err)
			}
			if result.Output != "[]" {
				t.Fatalf("empty memory output = %q, want []", result.Output)
			}
		})
	}
}
