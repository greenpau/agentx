package cli

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseDebugFlag(t *testing.T) {
	defaults, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Debug {
		t.Fatal("debug logging was enabled by default")
	}

	for _, alias := range []string{"-d", "--debug"} {
		t.Run(alias, func(t *testing.T) {
			opts, err := Parse([]string{alias})
			if err != nil {
				t.Fatal(err)
			}
			if !opts.Debug {
				t.Fatalf("%s did not enable debug logging", alias)
			}
		})
	}
}

func TestDebugFlagRejectsUnsupportedForms(t *testing.T) {
	for _, args := range [][]string{
		{"--debug=provider"},
		{"-d2e"},
		{"--debug-to-stderr"},
		{"--debug-file", "agentx-debug.log"},
		{"--mcp-debug"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) accepted an unsupported debug form", args)
		}
	}
}

func TestHelpDocumentsDebugFlag(t *testing.T) {
	usage := Usage()
	if !strings.Contains(usage, "-d, --debug") || !strings.Contains(usage, "Enable debug diagnostic logging") {
		t.Fatal("help does not document debug diagnostic logging")
	}
}

func TestCompatibilityVersionAliasIsSoleArgumentOnly(t *testing.T) {
	opts, err := Parse([]string{"-V"})
	if err != nil || !opts.Version {
		t.Fatalf("sole -V compatibility path = %#v, %v", opts, err)
	}
	for _, args := range [][]string{{"-V", "prompt"}, {"--print", "-V"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("ordinary grammar accepted compatibility alias in %q", args)
		}
	}
}

func TestParseSDKImplications(t *testing.T) {
	if _, err := Parse([]string{"--sdk-url", "ws://local", "hello", "world"}); err == nil {
		t.Fatal("unsupported remote placement was silently accepted")
	}
}

func TestValidationCombinations(t *testing.T) {
	tests := [][]string{
		{"--input-format", "stream-json", "--output-format", "json"},
		{"--include-partial-messages", "--print"},
		{"--resume", "a", "--continue"},
		{"--fork-session"},
		{"--system-prompt", "x", "--system-prompt-file", "y"},
		{"--json-schema", `{"type":"object"}`},
		{"--max-budget-usd", "1"},
		{"--verbose"},
	}
	for _, args := range tests {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) succeeded", args)
		}
	}
}

func TestParseRejectsNonFiniteMaxBudgetUSD(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			_, err := Parse([]string{"--max-budget-usd", value, "prompt"})
			if err == nil {
				t.Fatalf("Parse accepted non-finite --max-budget-usd %q", value)
			}
			if !IsUsageError(err) {
				t.Fatalf("Parse error for %q = %T %v, want usage error", value, err, err)
			}
			if err.Error() != "--max-budget-usd must be a positive number" {
				t.Fatalf("Parse error for %q = %q, want non-finite validation error", value, err)
			}
		})
	}
}

func TestStandaloneMCPRejectsIgnoredConversationConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--mcp-server", "--session-id", "ses_ignored"},
		{"--mcp-server", "--trust-workspace"},
		{"--mcp-server", "--output-style", "ignored"},
		{"--mcp-server", "--model", "gpt-5.6-sol"},
		{"--mcp-server", "--system-prompt", "ignored"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) silently accepted ignored MCP-host configuration", args)
		}
	}
}

func TestReplayUserMessagesRequiresAndAcceptsDuplexStructuredMode(t *testing.T) {
	if _, err := Parse([]string{"--replay-user-messages"}); err == nil {
		t.Fatal("replay without structured input and output succeeded")
	}
	opts, err := Parse([]string{"--replay-user-messages", "--input-format", "stream-json", "--output-format", "stream-json"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.ReplayUserMessages {
		t.Fatal("replay flag was not retained")
	}
}

func TestListsAndInlineValues(t *testing.T) {
	opts, err := Parse([]string{"-p", "--output-format=stream-json", "--allowed-tools", "Read, Grep", "--allowed-tools=Bash(git status)"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.AllowedTools) != 3 || opts.AllowedTools[2] != "Bash(git status)" {
		t.Fatalf("tools = %#v", opts.AllowedTools)
	}
}

func TestAttachmentPathsAreRepeatableAndPreservedVerbatim(t *testing.T) {
	opts, err := Parse([]string{
		"--attachment", "relative/screen shot.png",
		"--attachment=../literal/../report.pdf",
		"--attachment", " trailing-space.jpg ",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"relative/screen shot.png",
		"../literal/../report.pdf",
		" trailing-space.jpg ",
	}
	if len(opts.Attachments) != len(want) {
		t.Fatalf("attachments = %#v, want %#v", opts.Attachments, want)
	}
	for index := range want {
		if opts.Attachments[index] != want[index] {
			t.Fatalf("attachment %d = %q, want %q", index, opts.Attachments[index], want[index])
		}
	}
	if !strings.Contains(Usage(), "--attachment PATH") || !strings.Contains(Usage(), "repeatable") {
		t.Fatal("help does not document repeatable initial attachments")
	}
}

func TestAttachmentOnlyInputInfersHeadlessAndRejectsUnrelatedSurfaces(t *testing.T) {
	opts, err := Parse([]string{"--attachment", "screen.png"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Prompt != "" || len(opts.Attachments) != 1 {
		t.Fatalf("attachment-only parse = %#v", opts)
	}
	opts = InferPrint(opts, true)
	if !opts.Print {
		t.Fatal("attachment-only initial input did not infer headless mode")
	}
	if !HeadlessRequested([]string{"--attachment", "screen.png"}, true) {
		t.Fatal("attachment-only invocation did not acquire headless signal ownership")
	}

	for _, args := range [][]string{
		{"--mcp-server", "--attachment", "screen.png"},
		{"--list-sessions", "--cwd", "/workspace", "--attachment", "screen.png"},
		{"--delete-session", "ses_123", "--session-revision", "revision", "--cwd", "/workspace", "--attachment", "screen.png"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) accepted an attachment on an unrelated surface", args)
		}
	}
}

func TestAttachmentRequiresOnePathValue(t *testing.T) {
	for _, args := range [][]string{
		{"--attachment"},
		{"--attachment", "--"},
		{"--attachment="},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) accepted a missing attachment path", args)
		}
	}
}

func TestBooleanFlagsRejectInlineValues(t *testing.T) {
	for _, arg := range []string{"--print=true", "--help=false", "--trust-workspace=1", "--continue=yes", "--list-sessions=true", "--owned-process-tree=true"} {
		if _, err := Parse([]string{arg}); err == nil {
			t.Errorf("Parse(%q) accepted a value-bearing boolean flag", arg)
		}
	}
}

func TestOwnedProcessTreeFlagAndHelp(t *testing.T) {
	opts, err := Parse([]string{"--owned-process-tree", "--print", "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.OwnedProcessTree {
		t.Fatal("owned process tree flag was not retained")
	}
	if !strings.Contains(Usage(), "--owned-process-tree") {
		t.Fatal("owned process tree flag is absent from help")
	}
}

func TestHelpIdentifiesAuthJSONCredentialSource(t *testing.T) {
	usage := Usage()
	if !strings.Contains(usage, "auth.json") {
		t.Fatal("help does not identify auth.json as the model configuration source")
	}
}

func TestRawPrintRequestedUsesFullOptionGrammar(t *testing.T) {
	if !RawPrintRequested([]string{"--system-prompt=hello", "--print", "prompt"}) {
		t.Fatal("valid raw print option was missed")
	}
	for _, args := range [][]string{
		{"--", "--print"},
		{"--system-prompt", "--print"},
		{"--print=true"},
		{"--mcp-server", "--print"},
	} {
		if RawPrintRequested(args) {
			t.Fatalf("non-headless command line acquired print signal ownership: %#v", args)
		}
	}
}

func TestHeadlessRequestedUsesFinalSurfaceInference(t *testing.T) {
	for _, test := range []struct {
		name           string
		args           []string
		stdoutTerminal bool
		want           bool
	}{
		{name: "explicit", args: []string{"--print", "prompt"}, stdoutTerminal: true, want: true},
		{name: "structured output", args: []string{"--output-format", "json", "prompt"}, stdoutTerminal: true, want: true},
		{name: "redirected text", args: []string{"prompt"}, stdoutTerminal: false, want: true},
		{name: "interactive", args: []string{"prompt"}, stdoutTerminal: true, want: false},
		{name: "standalone MCP", args: []string{"--mcp-server"}, stdoutTerminal: false, want: false},
		{name: "invalid", args: []string{"--print=true"}, stdoutTerminal: false, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := HeadlessRequested(test.args, test.stdoutTerminal); got != test.want {
				t.Fatalf("HeadlessRequested(%q, %v) = %v, want %v", test.args, test.stdoutTerminal, got, test.want)
			}
		})
	}
}

func TestSurfaceDependentValidationRunsAfterHeadlessInference(t *testing.T) {
	structured, err := Parse([]string{"--output-format", "stream-json", "--include-partial-messages", "prompt"})
	if err != nil {
		t.Fatalf("parse rejected inferably headless structured output: %v", err)
	}
	structured = InferPrint(structured, true)
	if !structured.Print {
		t.Fatal("structured output did not infer print mode")
	}
	if err := structured.Validate(); err != nil {
		t.Fatalf("final structured validation failed: %v", err)
	}

	redirected, err := Parse([]string{"--no-session-persistence", "prompt"})
	if err != nil {
		t.Fatalf("parse rejected option before stdout mode was known: %v", err)
	}
	if err := redirected.Validate(); err == nil {
		t.Fatal("interactive final surface accepted disabled persistence")
	}
	redirected = InferPrint(redirected, false)
	if err := redirected.Validate(); err != nil {
		t.Fatalf("redirected headless final surface rejected disabled persistence: %v", err)
	}
}

func TestParseSessionManagementAllowedForms(t *testing.T) {
	list, err := Parse([]string{"--list-sessions", "--cwd", "/workspace"})
	if err != nil {
		t.Fatalf("minimal list: %v", err)
	}
	if !list.ListSessions || !list.SessionManagementRequested() {
		t.Fatalf("minimal list options = %#v", list)
	}
	if list.OutputFormat != OutputText || list.SessionPageSize != 0 || list.SessionPageToken != "" {
		t.Fatalf("minimal list defaults = %#v", list)
	}

	list, err = Parse([]string{
		"--session-page-token=next-page",
		"--cwd", "/workspace",
		"--list-sessions",
		"--output-format=json",
		"--session-page-size", "500",
	})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if list.CWD != "/workspace" || list.OutputFormat != OutputJSON || list.SessionPageSize != 500 || list.SessionPageToken != "next-page" {
		t.Fatalf("paged list options = %#v", list)
	}

	deletion, err := Parse([]string{
		"--output-format", "json",
		"--delete-session=ses_123",
		"--session-revision", "opaque-revision",
		"--cwd=/workspace",
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deletion.SessionManagementRequested() || deletion.DeleteSession != "ses_123" ||
		deletion.SessionRevision != "opaque-revision" || deletion.CWD != "/workspace" ||
		deletion.OutputFormat != OutputJSON {
		t.Fatalf("delete options = %#v", deletion)
	}

	if _, err := Parse([]string{"--list-sessions", "--cwd", "/workspace", "--"}); err != nil {
		t.Fatalf("trailing option terminator without a prompt was rejected: %v", err)
	}
}

func TestSessionManagementRequiresExplicitNonEmptyCWD(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "list omitted", args: []string{"--list-sessions"}},
		{name: "list empty", args: []string{"--list-sessions", "--cwd", ""}},
		{name: "list inline empty", args: []string{"--list-sessions", "--cwd="}},
		{name: "delete omitted", args: []string{"--delete-session", "ses_123", "--session-revision", "revision"}},
		{name: "delete empty", args: []string{"--delete-session", "ses_123", "--session-revision", "revision", "--cwd", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.args); err == nil {
				t.Fatalf("Parse(%q) accepted management without an explicit non-empty --cwd", test.args)
			} else if !IsUsageError(err) {
				t.Fatalf("Parse(%q) error = %T %v, want usage error", test.args, err, err)
			}
		})
	}
}

func TestSessionManagementSelectorsAndArgumentsAreStrict(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "mutually exclusive", args: []string{"--list-sessions", "--delete-session", "ses_123", "--session-revision", "revision", "--cwd", "/workspace"}},
		{name: "delete missing revision", args: []string{"--delete-session", "ses_123", "--cwd", "/workspace"}},
		{name: "delete empty ID", args: []string{"--delete-session=", "--session-revision", "revision", "--cwd", "/workspace"}},
		{name: "delete empty revision", args: []string{"--delete-session", "ses_123", "--session-revision=", "--cwd", "/workspace"}},
		{name: "list with revision", args: []string{"--list-sessions", "--session-revision", "revision", "--cwd", "/workspace"}},
		{name: "delete with page size", args: []string{"--delete-session", "ses_123", "--session-revision", "revision", "--session-page-size", "1", "--cwd", "/workspace"}},
		{name: "delete with page token", args: []string{"--delete-session", "ses_123", "--session-revision", "revision", "--session-page-token", "next", "--cwd", "/workspace"}},
		{name: "revision without delete", args: []string{"--session-revision", "revision"}},
		{name: "page size without list", args: []string{"--session-page-size", "1"}},
		{name: "page token without list", args: []string{"--session-page-token", "next"}},
		{name: "empty page token", args: []string{"--list-sessions", "--session-page-token=", "--cwd", "/workspace"}},
		{name: "stream output", args: []string{"--list-sessions", "--output-format", "stream-json", "--cwd", "/workspace"}},
		{name: "unsupported output", args: []string{"--list-sessions", "--output-format", "yaml", "--cwd", "/workspace"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.args); err == nil {
				t.Fatalf("Parse(%q) succeeded", test.args)
			} else if !IsUsageError(err) {
				t.Fatalf("Parse(%q) error = %T %v, want usage error", test.args, err, err)
			}
		})
	}
}

func TestSessionManagementRejectsRepeatedOptionOverrides(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "stream output overwritten by JSON",
			args: []string{
				"--list-sessions",
				"--cwd", "/workspace",
				"--output-format", "stream-json",
				"--output-format", "json",
			},
		},
		{
			name: "empty cwd overwritten",
			args: []string{
				"--list-sessions",
				"--cwd=",
				"--cwd", "/workspace",
			},
		},
		{
			name: "empty delete ID overwritten",
			args: []string{
				"--delete-session=",
				"--delete-session", "ses_123",
				"--session-revision", "revision",
				"--cwd", "/workspace",
			},
		},
		{
			name: "empty revision overwritten",
			args: []string{
				"--delete-session", "ses_123",
				"--session-revision=",
				"--session-revision", "revision",
				"--cwd", "/workspace",
			},
		},
		{
			name: "empty page token overwritten",
			args: []string{
				"--list-sessions",
				"--session-page-token=",
				"--session-page-token", "next",
				"--cwd", "/workspace",
			},
		},
		{
			name: "duplicate selector",
			args: []string{
				"--list-sessions",
				"--list-sessions",
				"--cwd", "/workspace",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.args); err == nil {
				t.Fatalf("Parse(%q) accepted a repeated management option", test.args)
			} else if !IsUsageError(err) {
				t.Fatalf("Parse(%q) error = %T %v, want usage error", test.args, err, err)
			}
		})
	}
}

func TestSessionManagementPageSizeBounds(t *testing.T) {
	for _, value := range []string{"1", "500"} {
		t.Run("valid "+value, func(t *testing.T) {
			opts, err := Parse([]string{"--list-sessions", "--session-page-size", value, "--cwd", "/workspace"})
			if err != nil {
				t.Fatalf("Parse page size %q: %v", value, err)
			}
			want, _ := strconv.Atoi(value)
			if opts.SessionPageSize != want {
				t.Fatalf("page size = %d, want %d", opts.SessionPageSize, want)
			}
		})
	}
	for _, value := range []string{"", "-1", "0", "501", "not-a-number"} {
		t.Run("invalid "+value, func(t *testing.T) {
			if _, err := Parse([]string{"--list-sessions", "--session-page-size=" + value, "--cwd", "/workspace"}); err == nil {
				t.Fatalf("Parse accepted page size %q", value)
			}
		})
	}
}

func TestSessionManagementRejectsEveryOtherExplicitOption(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "print", args: []string{"--print"}},
		{name: "print alias", args: []string{"-p"}},
		{name: "help", args: []string{"--help"}},
		{name: "help alias", args: []string{"-h"}},
		{name: "version", args: []string{"--version"}},
		{name: "version alias", args: []string{"-v"}},
		{name: "debug", args: []string{"--debug"}},
		{name: "debug alias", args: []string{"-d"}},
		{name: "verbose", args: []string{"--verbose"}},
		{name: "bare", args: []string{"--bare"}},
		{name: "trust workspace", args: []string{"--trust-workspace"}},
		{name: "MCP server", args: []string{"--mcp-server"}},
		{name: "partial messages", args: []string{"--include-partial-messages"}},
		{name: "replay messages", args: []string{"--replay-user-messages"}},
		{name: "continue", args: []string{"--continue"}},
		{name: "continue alias", args: []string{"-c"}},
		{name: "fork", args: []string{"--fork-session"}},
		{name: "no persistence", args: []string{"--no-session-persistence"}},
		{name: "owned process tree", args: []string{"--owned-process-tree"}},
		{name: "bypass permissions", args: []string{"--dangerously-skip-permissions"}},
		{name: "bypass permissions alias", args: []string{"--bypass-permissions"}},
		{name: "input format default", args: []string{"--input-format", "text"}},
		{name: "JSON schema empty", args: []string{"--json-schema", ""}},
		{name: "SDK URL empty", args: []string{"--sdk-url", ""}},
		{name: "output style empty", args: []string{"--output-style", ""}},
		{name: "MCP config empty", args: []string{"--mcp-config", ""}},
		{name: "model empty", args: []string{"--model", ""}},
		{name: "effort empty", args: []string{"--effort", ""}},
		{name: "permission mode default", args: []string{"--permission-mode", "default"}},
		{name: "allowed tools empty", args: []string{"--allowed-tools", ""}},
		{name: "disallowed tools empty", args: []string{"--disallowed-tools", ""}},
		{name: "attachment", args: []string{"--attachment", "screen.png"}},
		{name: "max turns default", args: []string{"--max-turns", "100"}},
		{name: "max budget", args: []string{"--max-budget-usd", "1"}},
		{name: "session ID empty", args: []string{"--session-id", ""}},
		{name: "resume empty", args: []string{"--resume", ""}},
		{name: "system prompt empty", args: []string{"--system-prompt", ""}},
		{name: "system prompt file empty", args: []string{"--system-prompt-file", ""}},
		{name: "append system prompt empty", args: []string{"--append-system-prompt", ""}},
		{name: "append system prompt file empty", args: []string{"--append-system-prompt-file", ""}},
	}
	modes := []struct {
		name string
		args []string
	}{
		{name: "list", args: []string{"--list-sessions", "--cwd", "/workspace"}},
		{name: "delete", args: []string{
			"--delete-session", "ses_123",
			"--session-revision", "revision",
			"--cwd", "/workspace",
		}},
	}
	for _, mode := range modes {
		for _, test := range tests {
			t.Run(mode.name+"/"+test.name, func(t *testing.T) {
				args := append(append([]string{}, mode.args...), test.args...)
				if _, err := Parse(args); err == nil {
					t.Fatalf("Parse(%q) accepted a non-management option", args)
				}
			})
		}
	}
}

func TestSessionManagementSelectorCannotBeConsumedAsOptionValue(t *testing.T) {
	scalarOptions := []string{
		"--output-format",
		"--input-format",
		"--json-schema",
		"--sdk-url",
		"--output-style",
		"--mcp-config",
		"--cwd",
		"--model",
		"--effort",
		"--permission-mode",
		"--allowed-tools",
		"--disallowed-tools",
		"--attachment",
		"--max-turns",
		"--max-budget-usd",
		"--session-id",
		"--resume",
		"--delete-session",
		"--session-revision",
		"--session-page-size",
		"--session-page-token",
		"--system-prompt",
		"--system-prompt-file",
		"--append-system-prompt",
		"--append-system-prompt-file",
	}
	selectors := []struct {
		name string
		args []string
	}{
		{
			name: "list",
			args: []string{"--list-sessions", "--cwd", "/workspace"},
		},
		{
			name: "delete inline",
			args: []string{
				"--delete-session=ses_123",
				"--session-revision", "revision",
				"--cwd", "/workspace",
			},
		},
	}
	for _, option := range scalarOptions {
		for _, selector := range selectors {
			t.Run(option+"/"+selector.name, func(t *testing.T) {
				args := append([]string{option}, selector.args...)
				if _, err := Parse(args); err == nil {
					t.Fatalf("Parse(%q) consumed a management selector as %s's value", args, option)
				} else if !IsUsageError(err) {
					t.Fatalf("Parse(%q) error = %T %v, want usage error", args, err, err)
				}
				for _, terminal := range []bool{false, true} {
					if HeadlessRequested(args, terminal) {
						t.Fatalf("HeadlessRequested(%q, %v) routed swallowed management", args, terminal)
					}
				}
			})
		}
	}

	if _, err := Parse([]string{"--model", "--", "--list-sessions"}); err == nil {
		t.Fatal("scalar option consumed the option terminator as its value")
	}
	opts, err := Parse([]string{"--", "--list-sessions"})
	if err != nil {
		t.Fatalf("literal selector after option terminator: %v", err)
	}
	if opts.SessionManagementRequested() || opts.Prompt != "--list-sessions" {
		t.Fatalf("literal selector after option terminator = %#v", opts)
	}
}

func TestSessionManagementRejectsPositionalTokens(t *testing.T) {
	for _, args := range [][]string{
		{"--list-sessions", "--cwd", "/workspace", "prompt"},
		{"prompt", "--list-sessions", "--cwd", "/workspace"},
		{"--list-sessions", "--cwd", "/workspace", "--", "prompt"},
		{"--list-sessions", "--cwd", "/workspace", ""},
		{"--delete-session", "ses_123", "--session-revision", "revision", "--cwd", "/workspace", "prompt"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) accepted a positional token", args)
		}
	}

	opts, err := Parse([]string{"--", "--list-sessions"})
	if err != nil {
		t.Fatalf("option-like prompt after --: %v", err)
	}
	if opts.SessionManagementRequested() || opts.Prompt != "--list-sessions" {
		t.Fatalf("option-like prompt was treated as management: %#v", opts)
	}
}

func TestSessionManagementNeverInfersPrintOrHeadless(t *testing.T) {
	for _, args := range [][]string{
		{"--list-sessions", "--cwd", "/workspace"},
		{"--list-sessions", "--cwd", "/workspace", "--output-format", "json"},
		{"--delete-session", "ses_123", "--session-revision", "revision", "--cwd", "/workspace"},
		{"--delete-session", "ses_123", "--session-revision", "revision", "--cwd", "/workspace", "--output-format", "json"},
	} {
		opts, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%q): %v", args, err)
		}
		if opts.Print {
			t.Fatalf("Parse(%q) enabled print", args)
		}
		for _, stdoutTerminal := range []bool{false, true} {
			if inferred := InferPrint(opts, stdoutTerminal); inferred.Print {
				t.Fatalf("InferPrint(%q, %v) enabled print", args, stdoutTerminal)
			}
			if HeadlessRequested(args, stdoutTerminal) {
				t.Fatalf("HeadlessRequested(%q, %v) = true", args, stdoutTerminal)
			}
		}
	}
}

func TestHelpDocumentsNativeSessionManagement(t *testing.T) {
	usage := Usage()
	for _, option := range []string{
		"--list-sessions",
		"--delete-session ID",
		"--session-revision TOKEN",
		"--session-page-size N",
		"--session-page-token TOKEN",
	} {
		if !strings.Contains(usage, option) {
			t.Errorf("help does not document %s", option)
		}
	}
}
