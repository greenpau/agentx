package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenpau/agentx/pkg/extensions"
	"github.com/greenpau/agentx/pkg/redact"
)

func TestUsablePluginsIsolatesDependencyFailure(t *testing.T) {
	root := t.TempDir()
	writeExtensionFixture(t, filepath.Join(root, "broken", ".agentx-plugin", "plugin.json"), `{"name":"broken","dependencies":["missing"]}`)
	writeExtensionFixture(t, filepath.Join(root, "cycle-a", ".agentx-plugin", "plugin.json"), `{"name":"cycle-a","dependencies":["cycle-b"]}`)
	writeExtensionFixture(t, filepath.Join(root, "cycle-b", ".agentx-plugin", "plugin.json"), `{"name":"cycle-b","dependencies":["cycle-a"]}`)
	writeExtensionFixture(t, filepath.Join(root, "healthy", ".agentx-plugin", "plugin.json"), `{"name":"healthy","dependencies":["shared"]}`)
	writeExtensionFixture(t, filepath.Join(root, "shared", ".agentx-plugin", "plugin.json"), `{"name":"shared"}`)

	snapshot := extensions.NewPluginManager().Reload([]extensions.PluginRoot{{
		Path: root, Source: extensions.SourceUser, Marketplace: "market", Scope: "user", Enabled: true, Trusted: true, Strict: true,
	}}, extensions.PluginPolicy{})
	resolved := usablePlugins(&snapshot)
	if len(resolved) != 2 || resolved[0].CanonicalID != "shared@market" || resolved[1].CanonicalID != "healthy@market" {
		t.Fatalf("resolved plugins = %#v", resolved)
	}
	for _, id := range []string{"broken@market", "cycle-a@market", "cycle-b@market"} {
		plugin, usable := snapshot.Lookup(id)
		if usable || plugin.Availability.ProviderAvailable {
			t.Fatalf("plugin %s was not demoted: %#v usable=%v", id, plugin.Availability, usable)
		}
	}
	if !extensionDiagnosticsContain(snapshot.Diagnostics, "broken@market is unavailable") || !extensionDiagnosticsContain(snapshot.Diagnostics, "missing@market not found") {
		t.Fatalf("missing dependency diagnostic = %#v", snapshot.Diagnostics)
	}
	if !extensionDiagnosticsContain(snapshot.Diagnostics, "plugin dependency cycle:") || !extensionDiagnosticsContain(snapshot.Diagnostics, "cycle-a@market") || !extensionDiagnosticsContain(snapshot.Diagnostics, "cycle-b@market") {
		t.Fatalf("dependency cycle diagnostic = %#v", snapshot.Diagnostics)
	}
}

func TestLoadHookComponentsIsolatesMalformedSibling(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "bad-hooks.json")
	good := filepath.Join(root, "good-hooks.json")
	writeExtensionFixture(t, bad, `{"hooks":`)
	writeExtensionFixture(t, good, `{"hooks":[{"id":"healthy","event":"UserPromptSubmit","kind":"command","command":"printf ok","shell":"sh","timeout":1000000000}]}`)

	descriptors, diagnostics := loadHookComponents([]componentFile{
		{path: bad, source: extensions.SourcePlugin, identity: "bad@market", root: root},
		{path: good, source: extensions.SourcePlugin, identity: "good@market", root: root},
	})
	if len(descriptors) != 1 {
		t.Fatalf("healthy hook suppressed by malformed sibling: %#v", descriptors)
	}
	if descriptor := descriptors[0]; descriptor.ID != "healthy" || descriptor.SourceIdentity != "good@market" || descriptor.PluginRoot != root || descriptor.Timeout != time.Second {
		t.Fatalf("healthy hook attribution = %#v", descriptor)
	}
	if len(diagnostics) != 1 || diagnostics[0].Path != bad || !strings.Contains(diagnostics[0].Message, "invalid plugin hook document") {
		t.Fatalf("malformed hook diagnostics = %#v", diagnostics)
	}
}

func TestBuildSessionReportsUnavailableOutputStyleWithBoundedDiagnostic(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "state")
	userRoot := t.TempDir()
	t.Setenv("HOME", userRoot)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(userRoot, "config"))
	t.Setenv("APPDATA", filepath.Join(userRoot, "appdata"))
	opts := buildTestCLIOptions(t, workspace, stateRoot, "ses_missing_output_style")
	opts.Bare = false
	opts.OutputStyle = "test-key-" + strings.Repeat("missing-", 2048) + "\nspoofed-warning"

	var diagnostics bytes.Buffer
	session, err := buildSession(t.Context(), buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: &diagnostics})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	output := diagnostics.String()
	if !strings.HasPrefix(output, "warning: output style: configured output style") || !strings.Contains(output, "is unavailable; using default") {
		t.Fatalf("missing selection diagnostic = %q", output)
	}
	if strings.Contains(output, "\nspoofed-warning\n") {
		t.Fatalf("diagnostic allowed line injection: %q", output)
	}
	if strings.Contains(output, "test-key") || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("diagnostic was not credential-redacted: %q", output)
	}
	if len(output) > maximumExtensionDiagnosticBytes+64 {
		t.Fatalf("diagnostic was not bounded: %d bytes", len(output))
	}
	if session.services.extensions.selection.Style != nil || !session.services.extensions.selection.KeepCodingInstructions {
		t.Fatalf("missing style did not fall back to default: %#v", session.services.extensions.selection)
	}
}

func TestOutputStyleDiagnosticWriteFailureIsReported(t *testing.T) {
	selection := extensions.OutputStyleSelection{Diagnostics: []extensions.Diagnostic{{Message: "missing"}}}
	err := writeOutputStyleSelectionDiagnostics(failingExtensionWriter{}, selection, nil)
	if err == nil || !strings.Contains(err.Error(), "write output-style diagnostic") {
		t.Fatalf("write failure = %v", err)
	}
}

func TestOutputStyleDiagnosticGuardsCompleteWarningFraming(t *testing.T) {
	const secret = "style: diagnostic"
	selection := extensions.OutputStyleSelection{Diagnostics: []extensions.Diagnostic{{Message: "diagnostic"}}}
	var output bytes.Buffer
	if err := writeOutputStyleSelectionDiagnostics(&output, selection, redact.New(secret)); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Contains(got, secret) || !strings.Contains(got, redact.Mask(secret)) {
		t.Fatalf("output-style warning projection = %q", got)
	}
}

type failingExtensionWriter struct{}

func (failingExtensionWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func writeExtensionFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func extensionDiagnosticsContain(diagnostics []extensions.Diagnostic, fragment string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, fragment) {
			return true
		}
	}
	return false
}
