package cli

import (
	"strings"
	"testing"
)

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

func TestBooleanFlagsRejectInlineValues(t *testing.T) {
	for _, arg := range []string{"--print=true", "--help=false", "--trust-workspace=1", "--continue=yes", "--owned-process-tree=true"} {
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

func TestCredentialFileIsNotCommandLineSelectable(t *testing.T) {
	if _, err := Parse([]string{"--env-file", "credentials.env", "prompt"}); err == nil {
		t.Fatal("removed --env-file option was accepted")
	}
	usage := Usage()
	if strings.Contains(usage, "--env-file") || strings.Contains(usage, ".env.production") {
		t.Fatalf("help still advertises dotenv credential selection: %q", usage)
	}
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
