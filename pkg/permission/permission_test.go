package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParseRuleAndMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		tool      string
		content   string
		wantMatch bool
		wantTool  string
	}{
		{name: "canonical", raw: "Bash(git *)", tool: "Bash", content: "git", wantMatch: true, wantTool: "Bash"},
		{name: "arguments", raw: "Bash(git *)", tool: "Bash", content: "git status", wantMatch: true, wantTool: "Bash"},
		{name: "boundary", raw: "Bash(git:*)", tool: "Bash", content: "github status", wantMatch: false, wantTool: "Bash"},
		{name: "alias", raw: "KillShell", tool: "TaskStop", wantMatch: true, wantTool: "TaskStop"},
		{name: "escaped star", raw: `Bash(echo \*)`, tool: "Bash", content: "echo *", wantMatch: true, wantTool: "Bash"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, err := ParseRule(test.raw, EffectAllow, "test", false)
			if err != nil {
				t.Fatalf("ParseRule: %v", err)
			}
			if rule.Tool != test.wantTool {
				t.Fatalf("tool = %q, want %q", rule.Tool, test.wantTool)
			}
			if got := rule.matches(test.tool, []string{test.content}); got != test.wantMatch {
				t.Fatalf("matches = %v, want %v", got, test.wantMatch)
			}
		})
	}
}

func TestMCPPermissionRulesSupportServerFamiliesAndExactDottedTools(t *testing.T) {
	serverRule := mustRule(t, "mcp__files", EffectDeny)
	if !serverRule.Matches("mcp__files__write") || serverRule.Matches("mcp__files2__write") {
		t.Fatalf("server-wide MCP rule matched the wrong namespace")
	}
	exact := mustRule(t, "mcp__files__resource.read", EffectAllow)
	if !exact.Matches("mcp__files__resource.read") || exact.Matches("mcp__files__resource.write") {
		t.Fatalf("exact dotted MCP rule did not remain least-privilege")
	}
}

func TestGitAndSortMutationFlagsAreNotReadOnly(t *testing.T) {
	commands := []string{
		"git branch new-feature",
		"git branch -D old-feature",
		"git remote set-url origin https://example.test/repo",
		"git remote add origin https://example.test/repo",
		"git diff --output=patch.txt",
		"git grep --open-files-in-pager=vim needle",
		"sort -o output.txt input.txt",
		"find . -fls output.txt",
		"sort --compress-program=./evil input.txt",
		"./cat input.txt",
		"/tmp/git status",
	}
	for _, command := range commands {
		analysis, err := AnalyzeShell(command, "/workspace")
		if err != nil {
			t.Fatalf("AnalyzeShell(%q): %v", command, err)
		}
		if analysis.ReadOnly || analysis.SafeConcurrent {
			t.Errorf("mutation %q classified read-only: %#v", command, analysis)
		}
	}
	reads := []string{"git branch --list", "git remote -v", "git remote get-url origin", "git status", "sort input.txt"}
	for _, command := range reads {
		analysis, err := AnalyzeShell(command, "/workspace")
		if err != nil || !analysis.ReadOnly {
			t.Errorf("read %q classification=%#v err=%v", command, analysis, err)
		}
	}
}

func TestReadOnlyShellStillComposesFilesystemPolicy(t *testing.T) {
	workspace := t.TempDir()
	analysis, err := AnalyzeShell("cat .env.private /etc/passwd", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.ReadOnly || len(analysis.Paths) < 2 {
		t.Fatalf("analysis=%#v", analysis)
	}
	evaluator, err := NewEvaluator(Config{Workspace: workspace, Mode: ModeDefault, PromptSuppressed: true})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Bash", ToolUseID: "tool_read", Input: json.RawMessage(`{"command":"cat .env.private /etc/passwd"}`),
		Content: analysis.Command, MatchContents: analysis.Segments, Classification: Classification{ReadOnly: true, ConcurrencySafe: true}, Paths: analysis.Paths, Shell: &analysis,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny {
		t.Fatalf("suppressed protected/out-of-scope read decision=%#v", decision)
	}
}

func TestShellTildeExpansionCannotEscapeAuthorizedPath(t *testing.T) {
	workspace := t.TempDir()
	commands := []string{
		"cat ~/credential.txt",
		"cat ~other/credential.txt",
		"printf value > ~/output.txt",
		"cp source.txt ~/destination.txt",
		"rm -rf ~/target",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			analysis, err := AnalyzeShell(command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, access := range analysis.Paths {
				if strings.HasPrefix(access.Path, "~") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("tilde operand was rewritten as an authorized workspace path: %+v", analysis.Paths)
			}
			evaluator, err := NewEvaluator(Config{
				Workspace: workspace, Mode: ModeBypass, BypassAvailable: true,
				Rules: []Rule{mustRule(t, "Bash", EffectAllow)},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision := evaluator.evaluate(Request{
				Tool: "Bash", Content: analysis.Command,
				Classification: Classification{ReadOnly: analysis.ReadOnly, ConcurrencySafe: analysis.SafeConcurrent},
				Paths:          analysis.Paths, Shell: &analysis,
			})
			if decision.Kind != DecisionDeny || decision.Source != "path" {
				t.Fatalf("tilde expansion escaped path safety: decision=%+v analysis=%+v", decision, analysis)
			}
		})
	}
}

func TestBypassAndAllowRulesResolveOrdinaryPathAsksButNotProtectedPaths(t *testing.T) {
	workspace := t.TempDir()
	ordinary := filepath.Join(workspace, "generated.txt")
	protected := filepath.Join(workspace, ".env.private")
	request := func(path string) Request {
		return Request{
			Tool: "Write", ToolUseID: "tool_write", Input: json.RawMessage(`{"file_path":"` + path + `","content":"ok"}`),
			Classification: Classification{}, Paths: []PathAccess{{Path: path, Operation: PathWrite}},
		}
	}

	t.Run("explicit bypass permits ordinary in-scope mutation", func(t *testing.T) {
		evaluator, err := NewEvaluator(Config{Workspace: workspace, Mode: ModeBypass, BypassAvailable: true, PromptSuppressed: true})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := evaluator.Authorize(t.Context(), request(ordinary), nil)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Kind != DecisionAllow || decision.Source != "mode" {
			t.Fatalf("decision = %#v, want bypass allow", decision)
		}
	})

	t.Run("whole-tool allow resolves ordinary path ask", func(t *testing.T) {
		evaluator, err := NewEvaluator(Config{Workspace: workspace, Rules: []Rule{mustRule(t, "Write", EffectAllow)}, PromptSuppressed: true})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := evaluator.Authorize(t.Context(), request(ordinary), nil)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Kind != DecisionAllow || decision.Source != "test" {
			t.Fatalf("decision = %#v, want rule allow", decision)
		}
	})

	t.Run("protected path remains bypass immune", func(t *testing.T) {
		evaluator, err := NewEvaluator(Config{Workspace: workspace, Mode: ModeBypass, BypassAvailable: true, PromptSuppressed: true})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := evaluator.Authorize(t.Context(), request(protected), nil)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Kind != DecisionDeny || decision.Source != "mode" {
			t.Fatalf("decision = %#v, want prompt-suppressed protected denial", decision)
		}
	})
}

func TestShellWildcardExpansionCannotEscapeAuthorizedPath(t *testing.T) {
	workspace := t.TempDir()
	commands := []string{
		"cat leak?",
		"cat [l]*",
		"rg needle *.txt",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			analysis, err := AnalyzeShell(command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, access := range analysis.Paths {
				if strings.ContainsAny(access.Path, "*?[]{}") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("wildcard operand was not projected into path policy: %+v", analysis.Paths)
			}
			evaluator, err := NewEvaluator(Config{
				Workspace: workspace, Mode: ModeBypass, BypassAvailable: true,
				Rules: []Rule{mustRule(t, "Bash", EffectAllow)},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision := evaluator.evaluate(Request{
				Tool: "Bash", Content: analysis.Command,
				Classification: Classification{ReadOnly: analysis.ReadOnly, ConcurrencySafe: analysis.SafeConcurrent},
				Paths:          analysis.Paths, Shell: &analysis,
			})
			if decision.Kind != DecisionDeny || decision.Source != "path" {
				t.Fatalf("wildcard expansion escaped path safety: decision=%+v analysis=%+v", decision, analysis)
			}
		})
	}
}

func TestShellCapturesEveryRedirectionTarget(t *testing.T) {
	workspace := t.TempDir()
	analysis, err := AnalyzeShell("cat </etc/passwd > ordinary.txt 2> .env.private", workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]PathOperation{
		"/etc/passwd":  PathRead,
		"ordinary.txt": PathWrite,
		".env.private": PathWrite,
	}
	for suffix, operation := range want {
		found := false
		for _, access := range analysis.Paths {
			if strings.HasSuffix(filepath.ToSlash(access.Path), suffix) && access.Operation == operation {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s %s in %#v", operation, suffix, analysis.Paths)
		}
	}
}

func TestShellRedirectionParserDoesNotTreatEscapedQuoteAsSyntax(t *testing.T) {
	workspace := t.TempDir()
	analysis, err := AnalyzeShell(`echo \" > escaped-target.txt`, workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range analysis.Paths {
		if filepath.Base(access.Path) == "escaped-target.txt" && access.Operation == PathWrite {
			return
		}
	}
	t.Fatalf("escaped quote hid output redirection: %#v", analysis.Paths)
}

func TestEvaluatorDenyDominatesAskAndAllow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rules := []Rule{
		mustRule(t, "Bash", EffectAllow),
		mustRule(t, "Bash(git *)", EffectAsk),
		mustRule(t, "Bash(git push *)", EffectDeny),
	}
	evaluator, err := NewEvaluator(Config{Workspace: root, Rules: rules, Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
		t.Fatal("deny must not prompt")
		return ApprovalResponse{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"command":"git push origin main"}`)
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Bash", ToolUseID: "x", Input: raw, Content: "git push origin main", MatchContents: []string{"git push origin main"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.MatchedRule != "Bash(git push *)" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestShellRulesCoverEverySegmentAndNormalizeDenyOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	allowGit := mustRule(t, "Bash(git *)", EffectAllow)
	denyRemove := mustRule(t, "Bash(rm:*)", EffectDeny)
	evaluator, err := NewEvaluator(Config{Workspace: root, Rules: []Rule{allowGit, denyRemove}})
	if err != nil {
		t.Fatal(err)
	}
	compound := "git status; touch marker"
	analysis, err := AnalyzeShell(compound, root)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"command":"git status; touch marker"}`)
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Bash", ToolUseID: "compound", Input: raw, Content: compound,
		MatchContents: analysis.Segments, DenyContents: analysis.DenyCandidates,
		AllowContents: analysis.AllowCandidates, Shell: &analysis,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny { // unresolved ask is fail-closed without an approver
		t.Fatalf("one allowed segment authorized a sibling: %+v", decision)
	}

	command := "PATH=/attacker rm -rf build"
	analysis, err = AnalyzeShell(command, root)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = evaluator.Authorize(context.Background(), Request{
		Tool: "Bash", ToolUseID: "normalized-deny", Input: json.RawMessage(`{}`), Content: command,
		MatchContents: analysis.Segments, DenyContents: analysis.DenyCandidates,
		AllowContents: analysis.AllowCandidates, Shell: &analysis,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.MatchedRule != "Bash(rm:*)" {
		t.Fatalf("dangerous environment prefix hid deny: %+v analysis=%+v", decision, analysis)
	}
}

func TestEvaluatorHardenedEditedInputReauthorization(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	approvals := 0
	evaluator, err := NewEvaluator(Config{
		Workspace: root,
		Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
			approvals++
			return ApprovalResponse{Kind: DecisionAllow, UpdatedInput: json.RawMessage(`{"file_path":"/protected"}`)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := json.RawMessage(`{"file_path":"work.txt"}`)
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Write", ToolUseID: "edit", Input: initial,
		Classification: Classification{}, Paths: []PathAccess{{Path: filepath.Join(root, "work.txt"), Operation: PathWrite}},
	}, func(raw json.RawMessage) (Request, error) {
		return Request{Tool: "Write", ToolUseID: "edit", Input: raw, HardDeny: "edited target is protected"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.Source != "tool_safety" || approvals != 1 || !decision.UserModified {
		t.Fatalf("unexpected hardened decision: %+v, approvals=%d", decision, approvals)
	}
	if string(decision.OriginalInput) != string(initial) {
		t.Fatalf("original input was mutated: %s", decision.OriginalInput)
	}
}

func TestPlanModeDeniesMutationBeforeAnyApproval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	approvals := 0
	evaluator, err := NewEvaluator(Config{
		Workspace: root,
		Mode:      ModePlan,
		Rules:     []Rule{mustRule(t, "Write", EffectAllow), mustRule(t, "Write(*)", EffectAsk)},
		Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
			approvals++
			return ApprovalResponse{Kind: DecisionAllow}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Write", ToolUseID: "plan-write", Input: json.RawMessage(`{"file_path":"ordinary.txt"}`),
		Content: "ordinary.txt", Classification: Classification{ReadOnly: false},
		Paths:        []PathAccess{{Path: filepath.Join(root, "ordinary.txt"), Operation: PathWrite}},
		MandatoryAsk: "write requires approval",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.Source != "mode" || approvals != 0 {
		t.Fatalf("plan mutation decision=%+v approvals=%d", decision, approvals)
	}
}

func TestPlanModeDeniesMutationAfterEditedApproval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	approvals := 0
	updated := json.RawMessage(`{"file_path":"created.txt"}`)
	evaluator, err := NewEvaluator(Config{
		Workspace: root,
		Mode:      ModePlan,
		Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
			approvals++
			return ApprovalResponse{Kind: DecisionAllow, UpdatedInput: updated}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := json.RawMessage(`{"query":"inspect"}`)
	decision, err := evaluator.Authorize(context.Background(), Request{
		Tool: "Search", ToolUseID: "plan-edit", Input: initial,
		Classification: Classification{ReadOnly: true, OpenWorld: true},
	}, func(raw json.RawMessage) (Request, error) {
		return Request{
			Tool: "Write", ToolUseID: "plan-edit", Input: append(json.RawMessage(nil), raw...),
			Classification: Classification{}, Paths: []PathAccess{{Path: filepath.Join(root, "created.txt"), Operation: PathWrite}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.Source != "mode" || approvals != 1 || !decision.UserModified || decision.EditCycles != 1 {
		t.Fatalf("edited plan decision=%+v approvals=%d", decision, approvals)
	}
	if !jsonEqual(decision.Input, updated) || !jsonEqual(decision.OriginalInput, initial) {
		t.Fatalf("edited/original evidence lost: %+v", decision)
	}
}

func TestAcceptEditsAutoAllowsOnlyCanonicalFileEditTools(t *testing.T) {
	root := t.TempDir()
	for _, toolName := range []string{"Write", "Edit"} {
		t.Run(toolName, func(t *testing.T) {
			evaluator, err := NewEvaluator(Config{Workspace: root, Mode: ModeAcceptEdits, PromptSuppressed: true})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(context.Background(), Request{
				Tool: toolName, ToolUseID: "canonical-edit", Input: json.RawMessage(`{}`),
				Classification: Classification{},
				Paths:          []PathAccess{{Path: filepath.Join(root, "ordinary.txt"), Operation: PathWrite}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind != DecisionAllow || decision.Source != "mode" {
				t.Fatalf("canonical edit decision=%+v", decision)
			}
		})
	}

	for _, toolName := range []string{"Bash", "PluginWriter", "mcp__files__write"} {
		t.Run(toolName, func(t *testing.T) {
			approvals := 0
			evaluator, err := NewEvaluator(Config{
				Workspace: root,
				Mode:      ModeAcceptEdits,
				Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
					approvals++
					return ApprovalResponse{Kind: DecisionDeny, Reason: "reviewed"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(context.Background(), Request{
				Tool: toolName, ToolUseID: "general-write", Input: json.RawMessage(`{}`),
				Classification: Classification{},
				Paths:          []PathAccess{{Path: filepath.Join(root, "ordinary.txt"), Operation: PathWrite}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind != DecisionDeny || decision.Source != "user" || approvals != 1 {
				t.Fatalf("general writer was not routed through approval: decision=%+v approvals=%d", decision, approvals)
			}
		})
	}
}

func TestDangerousRemovalProtectsApprovedRootsAndAncestors(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace-parent", "workspace")
	additional := filepath.Join(base, "additional-parent", "additional")
	for _, directory := range []string{workspace, additional, filepath.Join(workspace, "child"), filepath.Join(base, "sibling")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		target    string
		dangerous bool
	}{
		{name: "workspace", target: workspace, dangerous: true},
		{name: "relative workspace", target: ".", dangerous: true},
		{name: "workspace ancestor", target: filepath.Dir(workspace), dangerous: true},
		{name: "additional root", target: additional, dangerous: true},
		{name: "additional ancestor", target: filepath.Dir(additional), dangerous: true},
		{name: "workspace child", target: filepath.Join(workspace, "child"), dangerous: false},
		{name: "prefix sibling", target: workspace + "-sibling", dangerous: false},
		{name: "unrelated sibling", target: filepath.Join(base, "sibling"), dangerous: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DangerousRemoval(test.target, workspace, additional); got != test.dangerous {
				t.Fatalf("DangerousRemoval(%q)=%v, want %v", test.target, got, test.dangerous)
			}
		})
	}

	analysis, err := AnalyzeShell("rm -rf "+additional, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Dangerous {
		t.Fatal("analyzer without the evaluator's additional roots unexpectedly knew the extra boundary")
	}
	evaluator, err := NewEvaluator(Config{
		Workspace:       workspace,
		AdditionalRoots: []string{additional},
		Mode:            ModeAcceptEdits,
		Rules:           []Rule{mustRule(t, "Bash", EffectAllow)},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := evaluator.evaluate(Request{
		Tool: "Bash", Content: analysis.Command, Classification: Classification{},
		Paths: analysis.Paths, Shell: &analysis,
	})
	if decision.Kind != DecisionAsk || decision.Source != "shell_safety" {
		t.Fatalf("additional-root removal bypassed shell safety: %+v", decision)
	}
}

func TestMutationOptionsWithFileOperandsRequireReview(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		command string
		paths   map[string]PathOperation
	}{
		{
			name: "dd operands", command: "dd if=input.img of=output.img bs=1024",
			paths: map[string]PathOperation{"input.img": PathRead, "output.img": PathWrite},
		},
		{
			name: "cp long target directory", command: "cp --target-directory=destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "cp split target directory", command: "cp --target-directory destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "cp short target directory", command: "cp -tdestination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "mv split target directory", command: "mv -t destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "mv long target directory", command: "mv --target-directory=destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "install target directory", command: "install -t destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
		{
			name: "ln target directory", command: "ln -t destination source.txt",
			paths: map[string]PathOperation{"source.txt": PathRead, "destination": PathWrite},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis, err := AnalyzeShell(test.command, root)
			if err != nil {
				t.Fatal(err)
			}
			if !analysis.RequiresReview || analysis.ReviewReason == "" || analysis.SafeConcurrent {
				t.Fatalf("option-based mutation was not review-required: %+v", analysis)
			}
			for base, operation := range test.paths {
				want := filepath.Join(root, base)
				found := false
				for _, access := range analysis.Paths {
					if samePath(access.Path, want) && access.Operation == operation {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing %s path %q in %+v", operation, want, analysis.Paths)
				}
			}

			evaluator, err := NewEvaluator(Config{
				Workspace: root,
				Mode:      ModeAcceptEdits,
				Rules:     []Rule{mustRule(t, "Bash", EffectAllow)},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision := evaluator.evaluate(Request{
				Tool: "Bash", Content: analysis.Command, Classification: Classification{},
				Paths: analysis.Paths, Shell: &analysis,
			})
			if decision.Kind != DecisionAsk {
				t.Fatalf("incomplete shell path grammar was auto-authorized: %+v", decision)
			}
		})
	}
}

func TestPathContainsUsesPlatformComponentAndCaseSemantics(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "tmp", "AgentXWorkspace")
	if !pathContains(root, filepath.Join(root, "nested", "file.txt")) {
		t.Fatal("descendant path was not contained")
	}
	if pathContains(root, root+"Sibling") {
		t.Fatal("textual-prefix sibling was treated as contained")
	}
	caseVariant := filepath.Join(filepath.Dir(root), strings.ToLower(filepath.Base(root)), "file.txt")
	contained := pathContains(root, caseVariant)
	if runtime.GOOS == "windows" {
		if !contained {
			t.Fatal("Windows case-equivalent path was not contained")
		}
	} else if contained {
		t.Fatal("case-variant path was contained on a case-sensitive path adapter")
	}
}

func TestResolverChecksSymlinkTargetAndProtectedPaths(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "value.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolver, err := NewResolver(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	escape := resolver.Inspect(filepath.Join(link, "value.txt"), PathRead, false)
	if escape.Kind != DecisionAsk || escape.InScope {
		t.Fatalf("symlink escape unexpectedly authorized: %+v", escape)
	}
	for _, name := range []string{".env", ".env.private", ".ENV.LOCAL", ".envrc", ".env-production", ".environment", "auth.json", "AUTH.JSON"} {
		protected := resolver.Inspect(filepath.Join(workspace, name), PathWrite, true)
		if protected.Kind != DecisionAsk || !protected.Protected {
			t.Fatalf("protected dotenv path %q unexpectedly authorized for write: %+v", name, protected)
		}
		protectedRead := resolver.Inspect(filepath.Join(workspace, name), PathRead, false)
		if protectedRead.Kind != DecisionAsk || !protectedRead.Protected {
			t.Fatalf("protected dotenv path %q unexpectedly authorized for read: %+v", name, protectedRead)
		}
	}
}

func TestResolverProtectsExactConfiguredCredentialPathAndAgentDirectory(t *testing.T) {
	workspace := t.TempDir()
	credential := filepath.Join(workspace, "config", "prod.settings")
	if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(workspace, nil, credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{credential, filepath.Join(workspace, ".agentx", "mcp.json")} {
		if decision := resolver.Inspect(path, PathRead, false); decision.Kind != DecisionAsk || !decision.Protected {
			t.Fatalf("configured control path was not protected: %s => %#v", path, decision)
		}
	}
}

func TestResolverProtectsConfiguredApplicationHomeDescendants(t *testing.T) {
	workspace := t.TempDir()
	applicationHome := filepath.Join(workspace, "custom-agentx-home")
	sessionFile := filepath.Join(applicationHome, "sessions", "workspace", "session", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(workspace, nil, applicationHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []PathOperation{PathRead, PathWrite} {
		decision := resolver.Inspect(sessionFile, operation, true)
		if decision.Kind != DecisionAsk || !decision.Protected {
			t.Fatalf("application-home descendant %s was not protected: %#v", operation, decision)
		}
	}
	if !IsProtectedPath(sessionFile, applicationHome) {
		t.Fatal("recursive capability predicate exposed an application-home descendant")
	}
}

func TestAnalyzeShellConservativeClassification(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		command   string
		readOnly  bool
		dangerous bool
		review    bool
	}{
		{command: "git status", readOnly: true},
		{command: "find . -exec rm {} ;", readOnly: false, review: true},
		{command: "cat input | sort > output", readOnly: false},
		{command: "rm -rf /", dangerous: true},
		{command: `echo "$(touch bad)"`, review: true},
		{command: "env bash -c whoami", readOnly: false, review: true},
		{command: "NO_COLOR=1 git status", readOnly: true},
		{command: "PATH=/attacker git status", readOnly: false, review: true},
	}
	for _, test := range tests {
		analysis, err := AnalyzeShell(test.command, root)
		if err != nil {
			t.Fatalf("AnalyzeShell(%q): %v", test.command, err)
		}
		if analysis.ReadOnly != test.readOnly || analysis.Dangerous != test.dangerous || analysis.RequiresReview != test.review {
			t.Errorf("AnalyzeShell(%q) = %+v", test.command, analysis)
		}
	}
}

func TestAnalyzeShellProjectsAttachedFileOptionsBeforeBypass(t *testing.T) {
	workspace := t.TempDir()
	for _, command := range []string{
		"file -m.env.private /bin/ls",
		"sort --files0-from=.env.private",
		"jq --from-file=.env.private .",
	} {
		analysis, err := AnalyzeShell(command, workspace)
		if err != nil {
			t.Fatalf("AnalyzeShell(%q): %v", command, err)
		}
		found := false
		for _, access := range analysis.Paths {
			if filepath.Base(access.Path) == ".env.private" && access.Operation == PathRead {
				found = true
			}
		}
		if !found {
			t.Fatalf("attached protected path was not projected for %q: %+v", command, analysis)
		}
	}

	analysis, err := AnalyzeShell("file -m.env.private /bin/ls", workspace)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewEvaluator(Config{Workspace: workspace, Mode: ModeBypass, BypassAvailable: true, PromptSuppressed: true})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluator.Authorize(t.Context(), Request{
		Tool: "Bash", Input: json.RawMessage(`{"command":"file -m.env.private /bin/ls"}`),
		Content: analysis.Command, Paths: analysis.Paths, Shell: &analysis,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != DecisionDeny || decision.Source != "mode" {
		t.Fatalf("bypass authorized attached protected path without review: %#v", decision)
	}
}

func TestProtectedShellMutationSpellingsRemainBypassImmune(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, ".env.private")
	tests := []string{
		"printf x >| .env.private",
		"mv -t .env.private source.txt",
		"mv --target-directory=.env.private source.txt",
		"install -t .env.private source.txt",
		"ln -t .env.private source.txt",
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			analysis, err := AnalyzeShell(command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, access := range analysis.Paths {
				if samePath(access.Path, protected) && access.Operation == PathWrite {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("protected destination was not projected: %+v", analysis)
			}

			evaluator, err := NewEvaluator(Config{
				Workspace:        workspace,
				Mode:             ModeBypass,
				BypassAvailable:  true,
				PromptSuppressed: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(t.Context(), Request{
				Tool: "Bash", ToolUseID: "tool_protected_mutation",
				Input:          json.RawMessage(`{"command":` + strconv.Quote(command) + `}`),
				Content:        analysis.Command,
				Classification: Classification{ReadOnly: analysis.ReadOnly, ConcurrencySafe: analysis.SafeConcurrent},
				Paths:          analysis.Paths,
				Shell:          &analysis,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind == DecisionAllow {
				t.Fatalf("bypass authorized protected mutation: decision=%+v analysis=%+v", decision, analysis)
			}
		})
	}
}

func TestAssignmentsWrappersAndResolutionBuiltinsCannotHideRemovalSafety(t *testing.T) {
	workspace := t.TempDir()
	commands := []string{
		"PATH=/tmp rm -rf /",
		"FOO=x rm -rf /",
		"nice rm -rf /",
		"nice -n 5 rm -rf /",
		"timeout 1 rm -rf /",
		"command rm -rf /",
		"env FOO=x rm -rf /",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			analysis, err := AnalyzeShell(command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if !analysis.Dangerous || len(analysis.RemovalTargets) == 0 {
				t.Fatalf("removal safety was hidden by invocation normalization: %+v", analysis)
			}
			evaluator, err := NewEvaluator(Config{
				Workspace:        workspace,
				Mode:             ModeBypass,
				BypassAvailable:  true,
				PromptSuppressed: true,
				Rules:            []Rule{mustRule(t, "Bash", EffectAllow)},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(t.Context(), shellRequest(command, analysis), nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind == DecisionAllow {
				t.Fatalf("bypass authorized hidden dangerous removal: decision=%+v analysis=%+v", decision, analysis)
			}
		})
	}
}

func TestGitProtectedPathGrammarCannotFallThroughBypassOrToolAllow(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, ".env.private")
	commands := []string{
		"git config --file .env.private user.name attacker",
		"git config --file=.env.private user.name attacker",
		"git --git-dir=.env.private status",
		"git --work-tree .env.private status",
		"git -C .env.private config user.name attacker",
		"git apply .env.private",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			analysis, err := AnalyzeShell(command, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if !analysis.RequiresReview || analysis.ReadOnly {
				t.Fatalf("non-read-only git grammar was not review-required: %+v", analysis)
			}
			found := false
			for _, access := range analysis.Paths {
				if samePath(access.Path, protected) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("protected git operand was not projected: %+v", analysis)
			}

			configs := []Config{
				{
					Workspace:        workspace,
					Mode:             ModeBypass,
					BypassAvailable:  true,
					PromptSuppressed: true,
				},
				{
					Workspace:        workspace,
					PromptSuppressed: true,
					Rules:            []Rule{mustRule(t, "Bash", EffectAllow)},
				},
			}
			for _, config := range configs {
				evaluator, err := NewEvaluator(config)
				if err != nil {
					t.Fatal(err)
				}
				decision, err := evaluator.Authorize(t.Context(), shellRequest(command, analysis), nil)
				if err != nil {
					t.Fatal(err)
				}
				if decision.Kind == DecisionAllow {
					t.Fatalf("git safety fell through evaluator: decision=%+v analysis=%+v", decision, analysis)
				}
			}
		})
	}
}

func shellRequest(command string, analysis ShellAnalysis) Request {
	return Request{
		Tool: "Bash", ToolUseID: "tool_shell_safety",
		Input:          json.RawMessage(`{"command":` + strconv.Quote(command) + `}`),
		Content:        analysis.Command,
		Classification: Classification{ReadOnly: analysis.ReadOnly, ConcurrencySafe: analysis.SafeConcurrent},
		Paths:          analysis.Paths,
		Shell:          &analysis,
	}
}

func mustRule(t *testing.T, raw string, effect Effect) Rule {
	t.Helper()
	rule, err := ParseRule(raw, effect, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}
