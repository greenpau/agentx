package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePluginManifest(t *testing.T, root, name, marketplace string, dependencies []string) PluginRoot {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".agentx-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"name": name, "version": "1.2.3", "description": name + " plugin", "dependencies": dependencies}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agentx-plugin", "plugin.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return PluginRoot{Path: root, Source: SourcePlugin, Marketplace: marketplace, Scope: "user", Enabled: true, Trusted: true, Strict: true}
}

func TestPluginDependencyPostorderAndCycle(t *testing.T) {
	base := t.TempDir()
	b := writePluginManifest(t, filepath.Join(base, "b"), "b", "market", nil)
	c := writePluginManifest(t, filepath.Join(base, "c"), "c", "market", []string{"b"})
	root := writePluginManifest(t, filepath.Join(base, "root"), "root", "market", []string{"c"})
	manager := NewPluginManager()
	snapshot := manager.Reload([]PluginRoot{root, c, b}, PluginPolicy{})
	order, err := snapshot.Resolve("root@market")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{order[0].CanonicalID, order[1].CanonicalID, order[2].CanonicalID}; strings.Join(got, ",") != "b@market,c@market,root@market" {
		t.Fatalf("dependency order = %v", got)
	}

	writePluginManifest(t, filepath.Join(base, "b"), "b", "market", []string{"root"})
	snapshot = manager.Reload([]PluginRoot{root, c, b}, PluginPolicy{})
	if _, err := snapshot.Resolve("root@market"); err == nil || !strings.Contains(err.Error(), "root@market -> c@market -> b@market -> root@market") {
		t.Fatalf("expected attributed cycle, got %v", err)
	}
}

func TestPluginPolicyCollisionAndSnapshotImmutability(t *testing.T) {
	base := t.TempDir()
	installed := writePluginManifest(t, filepath.Join(base, "installed"), "review", "market", nil)
	explicit := writePluginManifest(t, filepath.Join(base, "explicit"), "review", "market", nil)
	explicit.Source = SourceExplicit
	manager := NewPluginManager()
	one := manager.Reload([]PluginRoot{explicit, installed}, PluginPolicy{})
	winner, ok := one.Lookup("review@market")
	if !ok || filepath.Base(winner.Root) != "explicit" || one.Generation != 1 {
		t.Fatalf("unexpected plugin winner: %#v, ok=%v", winner, ok)
	}
	if len(one.Diagnostics) == 0 {
		t.Fatal("expected shadowing diagnostic")
	}
	two := manager.Reload([]PluginRoot{explicit}, PluginPolicy{ManagedReservedNames: map[string]bool{"review": true}})
	if _, ok := two.Lookup("review@market"); ok {
		t.Fatal("managed reservation should make explicit plugin unavailable")
	}
	if !one.Plugins[0].Availability.Usable() || one.Generation != 1 || two.Generation != 2 {
		t.Fatal("published snapshots were mutated")
	}
}

func TestPluginMalformedSiblingIsolationAndComponentContainment(t *testing.T) {
	base := t.TempDir()
	goodRoot := filepath.Join(base, "good")
	good := writePluginManifest(t, goodRoot, "good", "market", nil)
	if err := os.MkdirAll(filepath.Join(goodRoot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"name":"good","skills":"./skills","hooks":"../escape"}`)
	if err := os.WriteFile(filepath.Join(goodRoot, ".agentx-plugin", "plugin.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	badRoot := filepath.Join(base, "bad")
	if err := os.MkdirAll(filepath.Join(badRoot, ".agentx-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, ".agentx-plugin", "plugin.json"), []byte(`{"name":`), 0o600); err != nil {
		t.Fatal(err)
	}
	container := PluginRoot{Path: base, Source: SourcePlugin, Marketplace: "market", Scope: "user", Enabled: true, Trusted: true, Strict: true}
	snapshot := NewPluginManager().Reload([]PluginRoot{container}, PluginPolicy{})
	plugin, ok := snapshot.Lookup("good@market")
	if !ok || len(plugin.Components["skills"]) != 1 {
		t.Fatalf("valid sibling/component missing: %#v", snapshot)
	}
	if len(snapshot.Diagnostics) < 2 {
		t.Fatalf("expected malformed and traversal diagnostics: %#v", snapshot.Diagnostics)
	}
	_ = good
}

func TestStandardPluginComponentSymlinkCannotEscapeRoot(t *testing.T) {
	base := t.TempDir()
	pluginRoot := filepath.Join(base, "plugin")
	root := writePluginManifest(t, pluginRoot, "contained", "market", nil)
	outside := t.TempDir()
	writeOutputStyle(t, outside, "escaped.md", "EscapeProbe", "must not load", true, false)
	component := filepath.Join(pluginRoot, "output-styles")
	if err := os.Symlink(outside, component); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	snapshot := NewPluginManager().Reload([]PluginRoot{root}, PluginPolicy{})
	plugin, ok := snapshot.Lookup("contained@market")
	if !ok {
		t.Fatalf("plugin should remain usable after isolated component failure: %#v", snapshot)
	}
	if len(plugin.Components["output-styles"]) != 0 {
		t.Fatalf("escaping standard component was attributed to plugin: %#v", plugin.Components)
	}
	if !diagnosticsContain(snapshot.Diagnostics, "standard plugin component output-styles symlink escapes plugin root") {
		t.Fatalf("missing component containment diagnostic: %#v", snapshot.Diagnostics)
	}
}

func writeOutputStyle(t *testing.T, root, file, name, prompt string, keep, force bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: test style\nkeep-coding-instructions: " + boolText(keep) + "\nforce-for-plugin: " + boolText(force) + "\n---\n" + prompt + "\n"
	if err := os.WriteFile(filepath.Join(root, file), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestOutputStylePrecedenceSelectionAndPrompt(t *testing.T) {
	user, project, managed := t.TempDir(), t.TempDir(), t.TempDir()
	writeOutputStyle(t, user, "x.md", "Explanatory", "user prompt", true, false)
	writeOutputStyle(t, project, "x.md", "Explanatory", "project prompt", false, false)
	writeOutputStyle(t, managed, "x.md", "Explanatory", "managed prompt", true, false)
	manager := NewOutputStyleManager()
	snapshot := manager.Reload([]OutputStyleRoot{
		{Path: project, Source: SourceProject},
		{Path: managed, Source: SourceManaged},
		{Path: user, Source: SourceUser},
	})
	style, ok := snapshot.Lookup("Explanatory")
	if !ok || style.Prompt != "managed prompt" {
		t.Fatalf("style precedence failed: %#v", style)
	}
	selection := SelectOutputStyle(snapshot, "Explanatory")
	if selection.Style == nil || selection.PromptSection() != "# Output Style: Explanatory\nmanaged prompt" || !selection.KeepCodingInstructions {
		t.Fatalf("unexpected style selection: %#v", selection)
	}
	if len(snapshot.Diagnostics) < 3 {
		t.Fatalf("expected collision diagnostics: %#v", snapshot.Diagnostics)
	}
}

func TestForcedPluginOutputStyleWinsAndFilesystemCannotForce(t *testing.T) {
	pluginA, pluginB, user := t.TempDir(), t.TempDir(), t.TempDir()
	writeOutputStyle(t, pluginA, "style.md", "Strict", "plugin a", false, true)
	writeOutputStyle(t, pluginB, "style.md", "Strict", "plugin b", true, true)
	writeOutputStyle(t, user, "style.md", "User", "user", true, true)
	snapshot := NewOutputStyleManager().Reload([]OutputStyleRoot{
		{Path: pluginB, Source: SourcePlugin, PluginName: "b"},
		{Path: user, Source: SourceUser},
		{Path: pluginA, Source: SourcePlugin, PluginName: "a"},
	})
	selection := SelectOutputStyle(snapshot, "User")
	if selection.Style == nil || selection.Style.CanonicalName != "a:Strict" || selection.KeepCodingInstructions {
		t.Fatalf("forced style selection = %#v", selection)
	}
	if len(selection.Diagnostics) != 1 || !strings.Contains(selection.Diagnostics[0].Message, "multiple") {
		t.Fatalf("missing force collision diagnostic: %#v", selection.Diagnostics)
	}
	userStyle, ok := snapshot.Lookup("User")
	if !ok || userStyle.ForceForPlugin {
		t.Fatal("filesystem style gained plugin force authority")
	}
}

func TestOutputStyleSymlinkCannotEscapeDiscoveryRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeOutputStyle(t, outside, "escaped.md", "EscapeProbe", "must not load", true, false)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	snapshot := NewOutputStyleManager().Reload([]OutputStyleRoot{{Path: root, Source: SourceProject}})
	if _, ok := snapshot.Lookup("EscapeProbe"); ok {
		t.Fatal("output style escaped its attributed discovery root")
	}
	if !diagnosticsContain(snapshot.Diagnostics, "output-style symlink escapes discovery root") {
		t.Fatalf("missing output-style containment diagnostic: %#v", snapshot.Diagnostics)
	}
}

func TestOutputStyleInternalSymlinkIsCanonicalizedAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	writeOutputStyle(t, root, "style.md", "InternalLink", "load once", true, false)
	link := filepath.Join(root, "alias.md")
	if err := os.Symlink(filepath.Join(root, "style.md"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	snapshot := NewOutputStyleManager().Reload([]OutputStyleRoot{{Path: root, Source: SourceProject}})
	style, ok := snapshot.Lookup("InternalLink")
	if !ok || style.Prompt != "load once" {
		t.Fatalf("eligible internal symlink did not preserve style: %#v", snapshot)
	}
	if len(snapshot.Styles) != len(builtinOutputStyles())+1 {
		t.Fatalf("canonical target was loaded more than once: %#v", snapshot.Styles)
	}
}

func diagnosticsContain(diagnostics []Diagnostic, fragment string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, fragment) {
			return true
		}
	}
	return false
}
