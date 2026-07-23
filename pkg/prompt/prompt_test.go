package prompt

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiscoverInstructionsOrdersBroadToNarrow(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("broad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "AGENTS.md"), []byte("narrow"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverInstructions(nested)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(got[0], "broad") || !strings.Contains(got[1], "narrow") {
		t.Fatalf("wrong order: %#v", got)
	}
}

func TestBuilderCompleteOverrideSuppressesAppend(t *testing.T) {
	b := NewBuilder()
	b.runGit = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	sections, err := b.Build(context.Background(), Options{
		CWD: "/tmp", Model: "gpt-5.6-sol", Now: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
		Bare: true, Override: "custom", Append: "last", ToolNames: []string{"Write", "Read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sections[0].Text != "custom" {
		t.Fatalf("unexpected sections %#v", sections)
	}
	joined := ""
	for _, section := range sections {
		if section.Name == "append" {
			t.Fatalf("complete override retained append section: %#v", sections)
		}
		joined += section.Text
	}
	if strings.Contains(joined, "project_instructions") || strings.Contains(joined, "last") || !strings.Contains(joined, "Read, Write") {
		t.Fatalf("unexpected prompt %q", joined)
	}
}

func TestPromptGitProbeDoesNotInheritCredentialsOrUserConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture is Unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(bin, "git")
	script := "#!/bin/sh\nprintf 'credential=%s home=%s global=%s nosystem=%s prompt=%s\\n' \"$AZURE_OPENAI_SUBSCRIPTION_KEY\" \"$HOME\" \"$GIT_CONFIG_GLOBAL\" \"$GIT_CONFIG_NOSYSTEM\" \"$GIT_TERMINAL_PROMPT\"\n"
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-git-model-credential"
	t.Setenv("PATH", bin)
	t.Setenv("HOME", root)
	t.Setenv("AZURE_OPENAI_SUBSCRIPTION_KEY", secret)
	sections, err := NewBuilder().Build(t.Context(), Options{
		CWD: root, Model: "gpt-5.6-sol", Bare: true, IncludeGit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, section := range sections {
		joined.WriteString(section.Text)
	}
	text := joined.String()
	if strings.Contains(text, secret) || strings.Contains(text, "home="+root) {
		t.Fatalf("Git context inherited host state: %q", text)
	}
	if !strings.Contains(text, "global="+os.DevNull) || !strings.Contains(text, "nosystem=1") || !strings.Contains(text, "prompt=0") {
		t.Fatalf("Git context did not receive safety controls: %q", text)
	}
}

func TestDiscoverInstructionsDoesNotTruncateLargeOrdinaryFile(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", instructionAdvisoryCharacters+4096)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverInstructions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], content) {
		t.Fatalf("instruction projection was truncated: entries=%d bytes=%d", len(got), len(got[0]))
	}
}

func TestDiscoverInstructionsIsolatesInvalidUTF8Sibling(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("healthy sibling"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverInstructions(nested)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "healthy sibling") {
		t.Fatalf("invalid sibling erased healthy instructions: %#v", got)
	}
}

func TestDiscoverInstructionsRejectsHardCeilingWithoutPartialProjection(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", maximumInstructionFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverInstructions(root)
	if err == nil || got != nil || !strings.Contains(err.Error(), "not truncated") {
		t.Fatalf("hard-ceiling result = entries=%#v err=%v", got, err)
	}
}
