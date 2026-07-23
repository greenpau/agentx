package permission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellOutputOptionsNeverInheritReadOnlyClassification(t *testing.T) {
	workspace := t.TempDir()
	for _, test := range []struct {
		name    string
		command string
		output  string
	}{
		{name: "sort short split", command: "sort -o sorted.txt input.txt", output: "sorted.txt"},
		{name: "sort short attached", command: "sort -osorted.txt input.txt", output: "sorted.txt"},
		{name: "sort long split", command: "sort --output sorted.txt input.txt", output: "sorted.txt"},
		{name: "sort long attached", command: "sort --output=sorted.txt input.txt", output: "sorted.txt"},
		{name: "diff short split", command: "diff -o patch.txt old.txt new.txt", output: "patch.txt"},
		{name: "diff short attached", command: "diff -opatch.txt old.txt new.txt", output: "patch.txt"},
		{name: "diff long split", command: "diff --output patch.txt old.txt new.txt", output: "patch.txt"},
		{name: "diff long attached", command: "diff --output=patch.txt old.txt new.txt", output: "patch.txt"},
		{name: "compound segment", command: "cat input.txt && sort --output sorted.txt input.txt", output: "sorted.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			analysis, err := AnalyzeShell(test.command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if analysis.ReadOnly || analysis.SafeConcurrent {
				t.Fatalf("output-producing command was classified read-only: %+v", analysis)
			}
			want := filepath.Join(workspace, test.output)
			found := false
			for _, access := range analysis.Paths {
				if access.Operation == PathWrite && samePath(access.Path, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("output path %q was not projected as a write: %+v", want, analysis.Paths)
			}
		})
	}
}

func TestResolverProtectsConfiguredFileThroughHardLinkAlias(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, ".env.production")
	alias := filepath.Join(workspace, "ordinary.txt")
	if err := os.WriteFile(protected, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(protected, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	resolver, err := NewResolver(workspace, nil, protected)
	if err != nil {
		t.Fatal(err)
	}
	decision := resolver.Inspect(alias, PathRead, false)
	if decision.Kind != DecisionAsk || !decision.Protected {
		t.Fatalf("hard-link alias escaped configured protection: %+v", decision)
	}
}

func TestShellOutputOptionWithoutOperandRequiresReview(t *testing.T) {
	for _, command := range []string{"sort --output", "diff -o"} {
		analysis, err := AnalyzeShell(command, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if analysis.ReadOnly || !analysis.RequiresReview || analysis.ReviewReason == "" {
			t.Fatalf("incomplete output option did not require review: %+v", analysis)
		}
	}
}
