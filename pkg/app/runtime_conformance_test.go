package app

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/mcp"
)

func TestBareModeExcludesOrdinaryUserFilesystemExtensions(t *testing.T) {
	workspace := t.TempDir()
	userRoot := t.TempDir()
	writeRuntimeFixture(t, filepath.Join(userRoot, "plugins", "demo", ".agentx-plugin", "plugin.json"), `{"name":"demo","version":"1.0.0"}`)
	writeRuntimeFixture(t, filepath.Join(userRoot, "skills", "review", "SKILL.md"), "---\nname: Review\ndescription: Review code\n---\nReview carefully.\n")
	writeRuntimeFixture(t, filepath.Join(workspace, ".codex", "skills", "repo-review", "SKILL.md"), "---\nname: Repo Review\ndescription: Review repository code\n---\nReview carefully.\n")
	writeRuntimeFixture(t, filepath.Join(userRoot, "output-styles", "user.md"), "---\nname: UserStyle\ndescription: user style\n---\nUSER STYLE PROMPT\n")
	writeRuntimeFixture(t, filepath.Join(userRoot, "mcp.json"), `{"mcpServers":{"user-server":{"type":"stdio","command":"unused","disabled":true}}}`)

	ordinary, _, err := discoverExtensionsFromUserRoot(t.Context(), workspace, cli.Options{}, nil, userRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ordinary.mcp.Close() })
	if len(ordinary.plugins.Plugins) == 0 || len(ordinary.skills.Skills) != 0 || !hasOutputStyleSource(ordinary.styles, extensions.SourceUser) || len(ordinary.mcpConfigs) != 1 {
		t.Fatalf("user fixtures did not establish the non-bare control: plugins=%d skills=%d user_style=%v mcp=%d", len(ordinary.plugins.Plugins), len(ordinary.skills.Skills), hasOutputStyleSource(ordinary.styles, extensions.SourceUser), len(ordinary.mcpConfigs))
	}

	trusted, _, err := discoverExtensionsFromUserRoot(t.Context(), workspace, cli.Options{TrustWorkspace: true}, nil, userRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trusted.mcp.Close() })
	if len(trusted.skills.Skills) != 1 || trusted.skills.Skills[0].CanonicalName != "repo-review" || trusted.skills.Skills[0].Source != extensions.SourceProject {
		t.Fatalf("trusted discovery did not use only repository .codex skills: %#v", trusted.skills.Skills)
	}
	// If bare mode accidentally inspects either executable configuration source,
	// these now-malformed documents make discovery fail instead of silently
	// passing because an otherwise valid descriptor happened to be filtered.
	writeRuntimeFixture(t, filepath.Join(userRoot, "plugins", "demo", ".agentx-plugin", "plugin.json"), `{"name":`)
	writeRuntimeFixture(t, filepath.Join(userRoot, "mcp.json"), `{"mcpServers":`)

	bare, descriptors, err := discoverExtensionsFromUserRoot(t.Context(), workspace, cli.Options{Bare: true}, nil, userRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bare.mcp.Close() })
	if len(bare.plugins.Plugins) != 0 || len(bare.skills.Skills) != 0 || hasOutputStyleSource(bare.styles, extensions.SourceUser) {
		t.Fatalf("bare mode observed user extensions: plugins=%d skills=%d styles=%#v", len(bare.plugins.Plugins), len(bare.skills.Skills), bare.styles.Styles)
	}
	if len(bare.mcpConfigs) != 0 || len(bare.mcpState.Servers) != 0 || len(descriptors) != 0 {
		t.Fatalf("bare mode observed user MCP: configs=%d servers=%d tools=%d", len(bare.mcpConfigs), len(bare.mcpState.Servers), len(descriptors))
	}
}

func TestBareModeDoesNotMaterializePersistentMemory(t *testing.T) {
	projectRoot := t.TempDir()
	layout := sessionLayout{sessionDir: filepath.Join(projectRoot, "sessions", "ses_test")}
	memoryRoot := filepath.Join(projectRoot, "memory")

	store, err := openRuntimeMemory(layout, true)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatal("bare mode opened persistent memory")
	}
	if _, err := os.Stat(memoryRoot); !os.IsNotExist(err) {
		t.Fatalf("bare mode materialized memory root: %v", err)
	}

	store, err = openRuntimeMemory(layout, false)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("non-bare memory control was not opened")
	}
	if info, err := os.Stat(memoryRoot); err != nil || !info.IsDir() {
		t.Fatalf("non-bare memory root unavailable: info=%v err=%v", info, err)
	}
}

func TestCompleteSystemOverrideSuppressesAppendStyleAndMemory(t *testing.T) {
	style := extensions.OutputStyle{CanonicalName: "UserStyle", Prompt: "STYLE SENTINEL"}
	selection := extensions.OutputStyleSelection{Style: &style}
	if got := composeAppendPrompt("COMPLETE OVERRIDE", "APPEND SENTINEL", selection.PromptSection(), "MEMORY SENTINEL"); got != "" {
		t.Fatalf("complete override retained lower-precedence prompt material: %q", got)
	}

	missing := filepath.Join(t.TempDir(), "unused-append.md")
	override, appendPrompt, err := loadPromptFlags(cli.Options{SystemPrompt: "COMPLETE OVERRIDE", AppendSystemPromptFile: missing})
	if err != nil {
		t.Fatalf("complete override read suppressed append file: %v", err)
	}
	if override != "COMPLETE OVERRIDE" || appendPrompt != "" {
		t.Fatalf("prompt precedence = override %q append %q", override, appendPrompt)
	}
	overrideFile := filepath.Join(t.TempDir(), "system.md")
	writeRuntimeFixture(t, overrideFile, "FILE OVERRIDE\n")
	override, appendPrompt, err = loadPromptFlags(cli.Options{SystemPromptFile: overrideFile, AppendSystemPromptFile: missing})
	if err != nil || strings.TrimSpace(override) != "FILE OVERRIDE" || appendPrompt != "" {
		t.Fatalf("file prompt precedence = override %q append %q err=%v", override, appendPrompt, err)
	}

	want := "APPEND SENTINEL\n\nSTYLE SENTINEL\n\nMEMORY SENTINEL"
	if got := composeAppendPrompt("", "APPEND SENTINEL", "STYLE SENTINEL", "MEMORY SENTINEL"); got != want {
		t.Fatalf("ordinary append composition = %q, want %q", got, want)
	}
}

func TestBuildFailureRemovesOwnedTemporarySession(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "state")
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	opts := buildTestCLIOptions(t, workspace, stateRoot, "ses_temporary_build_failure")
	opts.NoSessionPersistence = true
	opts.SystemPromptFile = filepath.Join(workspace, "missing-system-prompt.md")

	session, err := buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: io.Discard})
	if session != nil {
		_ = session.Close()
	}
	if err == nil {
		t.Fatal("startup unexpectedly succeeded with a missing system prompt file")
	}
	entries, readErr := os.ReadDir(temporaryRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "agentx-session-") {
			t.Fatalf("startup failure leaked temporary session %s", filepath.Join(temporaryRoot, entry.Name()))
		}
	}
}

func TestStateAndSessionDirectoriesRejectDirectSymlinks(t *testing.T) {
	t.Run("configured state root", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "state")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		before, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTX_STATE_DIR", link)
		if _, err := stateRoot(t.TempDir()); err == nil {
			t.Fatal("direct state-root symlink was accepted")
		}
		after, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("state-root symlink target mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
	})

	t.Run("project component", func(t *testing.T) {
		state := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(state, "projects")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Setenv("AGENTX_STATE_DIR", state)
		if _, _, err := resolveSessionLayout(t.Context(), t.TempDir(), cli.Options{SessionID: "ses_project_link", NoSessionPersistence: true}); err == nil {
			t.Fatal("symlinked projects component was accepted")
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
			t.Fatalf("project symlink target was populated: entries=%v err=%v", entries, err)
		}
	})

	t.Run("resumed session", func(t *testing.T) {
		workspace := t.TempDir()
		state := filepath.Join(t.TempDir(), "state")
		sessionID := "ses_resume_link"
		sessions := filepath.Dir(testSessionDir(state, workspace, sessionID))
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(sessions, sessionID)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Setenv("AGENTX_STATE_DIR", state)
		if _, _, err := resolveSessionLayout(t.Context(), workspace, cli.Options{Resume: sessionID, NoSessionPersistence: true}); err == nil {
			t.Fatal("direct resumed-session symlink was accepted")
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
			t.Fatalf("resumed-session symlink target was populated: entries=%v err=%v", entries, err)
		}
	})
}

func TestTemporarySessionCleanupRejectsPostConstructionReplacement(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	t.Setenv("AGENTX_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	layout, _, err := resolveSessionLayout(t.Context(), t.TempDir(), cli.Options{SessionID: "ses_temp_identity", NoSessionPersistence: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.lock.Close(); err != nil {
		t.Fatal(err)
	}
	layout.lock = nil
	original := layout.sessionDir + "-original"
	if err := os.Rename(layout.sessionDir, original); err != nil {
		t.Skipf("directory replacement unavailable: %v", err)
	}
	if err := os.Mkdir(layout.sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(layout.sessionDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := layout.verify(); err == nil {
		t.Fatal("replaced temporary session passed identity verification")
	}
	if err := layout.removeTemporary(); err == nil {
		t.Fatal("cleanup accepted a replaced temporary session")
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("replacement temporary session was modified: content=%q err=%v", content, err)
	}
}

func TestProductionMCPLoaderExpandsEnvironmentAndIsolatesMissingValues(t *testing.T) {
	workspace := t.TempDir()
	userRoot := t.TempDir()
	writeRuntimeFixture(t, filepath.Join(userRoot, "mcp.json"), `{
  "mcpServers": {
    "expanded": {"type":"stdio","command":"$MCP_BIN","args":["--root=${MCP_ROOT}","$$literal"],"env":{"TOKEN":"$MCP_TOKEN"},"disabled":true},
    "broken": {"type":"stdio","command":"$MISSING_MCP_BIN","disabled":true}
  }
}`)
	runtime, _, err := discoverExtensionsFromUserRoot(t.Context(), workspace, cli.Options{}, []string{
		"MCP_BIN=/opt/mcp/server", "MCP_ROOT=/workspace", "MCP_TOKEN=synthetic-token",
	}, userRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.mcp.Close() })
	if len(runtime.mcpConfigs) != 2 {
		t.Fatalf("MCP config count = %d", len(runtime.mcpConfigs))
	}
	byName := make(map[string]mcp.Config)
	for _, config := range runtime.mcpConfigs {
		byName[config.Name] = config
	}
	expanded := byName["expanded"]
	if expanded.Command != "/opt/mcp/server" || len(expanded.Args) != 2 || expanded.Args[0] != "--root=/workspace" || expanded.Args[1] != "$literal" || expanded.Env["TOKEN"] != "synthetic-token" || expanded.ConfigurationError != "" {
		t.Fatalf("expanded production config = %#v", expanded)
	}
	if !strings.Contains(byName["broken"].ConfigurationError, "MISSING_MCP_BIN") {
		t.Fatalf("missing expansion was not retained as a diagnostic: %#v", byName["broken"])
	}
	found := false
	for _, diagnostic := range runtime.mcpState.Diagnostics {
		if strings.Contains(diagnostic.Server+diagnostic.Source+diagnostic.Message, "MISSING_MCP_BIN") {
			t.Fatalf("public expansion diagnostic exposed missing-variable identity: %#v", diagnostic)
		}
		found = found || diagnostic.Message == "MCP configuration expansion failed"
	}
	if !found {
		t.Fatalf("generic expansion diagnostic = %#v", runtime.mcpState.Diagnostics)
	}
}

func writeRuntimeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasOutputStyleSource(snapshot extensions.OutputStyleSnapshot, source extensions.Source) bool {
	for _, style := range snapshot.Styles {
		if style.Source == source {
			return true
		}
	}
	return false
}
