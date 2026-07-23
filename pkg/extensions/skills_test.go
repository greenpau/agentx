package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: Display " + name + "\ndescription: " + description + "\nallowed-tools: [Read, Grep]\nuser-invocable: true\n---\nDo $1 and $ARGUMENTS.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadPrecedenceAndGeneration(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeSkill(t, user, "review", "user")
	writeSkill(t, project, "review", "project")
	m := NewManager()
	one := m.Reload([]Root{{Path: project, Source: SourceProject}, {Path: user, Source: SourceUser}})
	skill, ok := one.Lookup("review", false)
	if !ok || skill.Description != "project" || one.Generation != 1 {
		t.Fatalf("unexpected snapshot %#v", one)
	}
	two := m.Reload([]Root{{Path: user, Source: SourceUser}})
	if two.Generation != 2 || one.Skills[0].Description != "project" {
		t.Fatal("snapshot was not immutable or generation failed")
	}
}

func TestExpandIsLiteral(t *testing.T) {
	skill := Skill{Body: "first=$1 all=$ARGUMENTS"}
	if got := Expand(skill, []string{"$(touch nope)", "two"}); got != "first=$(touch nope) all=$(touch nope) two" {
		t.Fatalf("expand = %q", got)
	}
}

func TestExpandPreservesMultiDigitPositions(t *testing.T) {
	arguments := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if got := Expand(Skill{Body: "$10/$1"}, arguments); got != "ten/one" {
		t.Fatalf("expand multi-digit positions = %q", got)
	}
}

func TestUnavailableAndModelDisabled(t *testing.T) {
	snapshot := Snapshot{Skills: []Skill{{CanonicalName: "x", DisableModelInvocation: true, Availability: Available()}}, byName: map[string]int{"x": 0}}
	if _, ok := snapshot.Lookup("x", true); ok {
		t.Fatal("model should not invoke disabled skill")
	}
	if _, ok := snapshot.Lookup("x", false); !ok {
		t.Fatal("user should invoke skill")
	}
}

func TestSkillSnapshotAllowedToolsIsImmutable(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "immutable", "snapshot copy")
	manager := NewManager()
	first := manager.Reload([]Root{{Path: root, Source: SourceProject}})
	if len(first.Skills) != 1 || len(first.Skills[0].AllowedTools) != 2 {
		t.Fatalf("first snapshot=%+v", first.Skills)
	}
	first.Skills[0].AllowedTools[0] = "Mutated"
	second := manager.Snapshot()
	if second.Skills[0].AllowedTools[0] != "Read" {
		t.Fatalf("manager snapshot was mutated through caller copy: %+v", second.Skills[0].AllowedTools)
	}
}
