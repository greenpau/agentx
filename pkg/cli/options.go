// Package cli performs mode-defining argument parsing before expensive runtime
// construction, ensuring diagnostics and stdout obey the selected surface.
package cli

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type OutputFormat string
type InputFormat string

const (
	OutputText       OutputFormat = "text"
	OutputJSON       OutputFormat = "json"
	OutputStreamJSON OutputFormat = "stream-json"
	InputText        InputFormat  = "text"
	InputStreamJSON  InputFormat  = "stream-json"
)

type Options struct {
	Print                  bool
	Version                bool
	Help                   bool
	Verbose                bool
	Bare                   bool
	TrustWorkspace         bool
	OutputStyle            string
	MCPConfig              string
	MCPServer              bool
	OutputFormat           OutputFormat
	InputFormat            InputFormat
	IncludePartial         bool
	ReplayUserMessages     bool
	JSONSchema             string
	SDKURL                 string
	EnvFile                string
	CWD                    string
	Model                  string
	Effort                 string
	PermissionMode         string
	DangerouslyBypass      bool
	AllowedTools           []string
	DisallowedTools        []string
	MaxTurns               int
	MaxBudgetUSD           float64
	SessionID              string
	Resume                 string
	Continue               bool
	ForkSession            bool
	NoSessionPersistence   bool
	OwnedProcessTree       bool
	SystemPrompt           string
	SystemPromptFile       string
	AppendSystemPrompt     string
	AppendSystemPromptFile string
	Prompt                 string
}

var errUsage = errors.New("command-line usage error")

type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }
func (e *UsageError) Is(target error) bool {
	return target == errUsage
}

func Parse(args []string) (Options, error) {
	opts := Options{OutputFormat: OutputText, InputFormat: InputText, EnvFile: ".env.production", MaxTurns: 100}
	// Preserve the recovered compatibility fast path without advertising -V
	// as an ordinary root option. It is valid only when it is the entire
	// invocation, before any runtime/configuration construction can occur.
	if len(args) == 1 && args[0] == "-V" {
		opts.Version = true
		return opts, nil
	}
	var positional []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		if hasInline && booleanOption(name) {
			return Options{}, &UsageError{Message: name + " does not accept a value"}
		}
		next := func() (string, error) {
			if hasInline {
				hasInline = false
				return inline, nil
			}
			if index+1 >= len(args) {
				return "", &UsageError{Message: name + " requires a value"}
			}
			index++
			return args[index], nil
		}
		switch name {
		case "-p", "--print":
			opts.Print = true
		case "-h", "--help":
			opts.Help = true
		case "-v", "--version":
			opts.Version = true
		case "--verbose":
			opts.Verbose = true
		case "--bare":
			opts.Bare = true
		case "--trust-workspace":
			opts.TrustWorkspace = true
		case "--mcp-server":
			opts.MCPServer = true
		case "--include-partial-messages":
			opts.IncludePartial = true
		case "--replay-user-messages":
			opts.ReplayUserMessages = true
		case "--continue", "-c":
			opts.Continue = true
		case "--fork-session":
			opts.ForkSession = true
		case "--no-session-persistence":
			opts.NoSessionPersistence = true
		case "--owned-process-tree":
			opts.OwnedProcessTree = true
		case "--dangerously-skip-permissions", "--bypass-permissions":
			opts.DangerouslyBypass = true
		case "--output-format":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.OutputFormat = OutputFormat(value)
		case "--input-format":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.InputFormat = InputFormat(value)
		case "--json-schema":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.JSONSchema = value
		case "--sdk-url":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.SDKURL = value
		case "--env-file":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.EnvFile = value
		case "--output-style":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.OutputStyle = value
		case "--mcp-config":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.MCPConfig = value
		case "--cwd":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.CWD = value
		case "--model":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.Model = value
		case "--effort":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.Effort = value
		case "--permission-mode":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.PermissionMode = value
		case "--allowed-tools":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.AllowedTools = appendList(opts.AllowedTools, value)
		case "--disallowed-tools":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.DisallowedTools = appendList(opts.DisallowedTools, value)
		case "--max-turns":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.MaxTurns, err = strconv.Atoi(value)
			if err != nil || opts.MaxTurns <= 0 {
				return Options{}, &UsageError{Message: "--max-turns must be a positive integer"}
			}
		case "--max-budget-usd":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.MaxBudgetUSD, err = strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(opts.MaxBudgetUSD) || math.IsInf(opts.MaxBudgetUSD, 0) || opts.MaxBudgetUSD <= 0 {
				return Options{}, &UsageError{Message: "--max-budget-usd must be a positive number"}
			}
		case "--session-id":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.SessionID = value
		case "--resume":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.Resume = value
		case "--system-prompt":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.SystemPrompt = value
		case "--system-prompt-file":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.SystemPromptFile = value
		case "--append-system-prompt":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.AppendSystemPrompt = value
		case "--append-system-prompt-file":
			value, err := next()
			if err != nil {
				return Options{}, err
			}
			opts.AppendSystemPromptFile = value
		default:
			return Options{}, &UsageError{Message: "unknown option " + name}
		}
	}
	opts.Prompt = strings.Join(positional, " ")
	// Parsing cannot know whether stdout is a terminal. Defer the two
	// presentation-dependent constraints until the caller applies InferPrint;
	// all grammar and surface-independent conflicts still fail here.
	if err := opts.validate(false); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// InferPrint applies the presentation inference that happens after argument
// parsing. A structured format is always headless, and redirected text output
// uses the one-shot adapter unless the standalone MCP host was selected.
func InferPrint(options Options, stdoutTerminal bool) Options {
	if !options.MCPServer && (options.OutputFormat != OutputText || options.InputFormat == InputStreamJSON || !stdoutTerminal) {
		options.Print = true
	}
	return options
}

// HeadlessRequested performs the same option/value/-- parsing and presentation
// inference as the application before root assigns SIGINT ownership. Invalid,
// informational, and standalone-MCP invocations deliberately return false
// because no headless model turn can start from them.
func HeadlessRequested(args []string, stdoutTerminal bool) bool {
	options, err := Parse(args)
	if err != nil || options.Help || options.Version || options.MCPServer {
		return false
	}
	return InferPrint(options, stdoutTerminal).Print
}

// RawPrintRequested is retained for callers that specifically need to know
// whether -p/--print was present, rather than whether the eventual surface is
// headless.
func RawPrintRequested(args []string) bool {
	options, err := Parse(args)
	return err == nil && options.Print
}

func (o Options) Validate() error {
	return o.validate(true)
}

func (o Options) validate(finalSurface bool) error {
	if o.Verbose {
		return &UsageError{Message: "--verbose is unavailable because no verbose diagnostic projection is installed"}
	}
	if o.SDKURL != "" {
		return &UsageError{Message: "--sdk-url is unavailable in this local execution profile"}
	}
	if o.MCPServer {
		if o.Print || o.OutputFormat != OutputText || o.InputFormat != InputText || strings.TrimSpace(o.Prompt) != "" ||
			o.Resume != "" || o.Continue || o.ForkSession || o.SessionID != "" || o.Bare || o.TrustWorkspace ||
			o.OutputStyle != "" || o.MCPConfig != "" || o.Model != "" || o.Effort != "" || o.SystemPrompt != "" ||
			o.SystemPromptFile != "" || o.AppendSystemPrompt != "" || o.AppendSystemPromptFile != "" {
			return &UsageError{Message: "--mcp-server is a standalone stdio surface and cannot be combined with conversation options"}
		}
	}
	if o.JSONSchema != "" {
		return &UsageError{Message: "--json-schema is unavailable; no output-validation contract is installed"}
	}
	if o.MaxBudgetUSD > 0 {
		return &UsageError{Message: "--max-budget-usd is unavailable because this provider profile does not expose authoritative pricing"}
	}
	switch o.OutputFormat {
	case OutputText, OutputJSON, OutputStreamJSON:
	default:
		return &UsageError{Message: fmt.Sprintf("unsupported output format %q", o.OutputFormat)}
	}
	switch o.InputFormat {
	case InputText, InputStreamJSON:
	default:
		return &UsageError{Message: fmt.Sprintf("unsupported input format %q", o.InputFormat)}
	}
	if o.InputFormat == InputStreamJSON && o.OutputFormat != OutputStreamJSON {
		return &UsageError{Message: "--input-format stream-json requires --output-format stream-json"}
	}
	if o.IncludePartial && (o.OutputFormat != OutputStreamJSON || finalSurface && !o.Print) {
		return &UsageError{Message: "--include-partial-messages requires --print and stream-json output"}
	}
	if o.ReplayUserMessages && (o.InputFormat != InputStreamJSON || o.OutputFormat != OutputStreamJSON) {
		return &UsageError{Message: "--replay-user-messages requires stream-json input and output"}
	}
	if o.NoSessionPersistence && finalSurface && !o.Print {
		return &UsageError{Message: "--no-session-persistence is headless-only"}
	}
	if o.Bare && (o.OutputStyle != "" || o.MCPConfig != "") {
		return &UsageError{Message: "--bare cannot load an output style or MCP configuration"}
	}
	if o.MCPConfig != "" && !o.TrustWorkspace {
		return &UsageError{Message: "--mcp-config requires --trust-workspace"}
	}
	if o.NoSessionPersistence && (o.Resume != "" || o.Continue || o.ForkSession) {
		return &UsageError{Message: "--no-session-persistence cannot resume, continue, or fork a session"}
	}
	if o.SystemPrompt != "" && o.SystemPromptFile != "" {
		return &UsageError{Message: "--system-prompt and --system-prompt-file are mutually exclusive"}
	}
	if o.AppendSystemPrompt != "" && o.AppendSystemPromptFile != "" {
		return &UsageError{Message: "--append-system-prompt and --append-system-prompt-file are mutually exclusive"}
	}
	continuity := 0
	for _, set := range []bool{o.Continue, o.Resume != "", o.SessionID != ""} {
		if set {
			continuity++
		}
	}
	if continuity > 1 {
		return &UsageError{Message: "--continue, --resume, and --session-id are mutually exclusive"}
	}
	if o.ForkSession && o.Resume == "" {
		return &UsageError{Message: "--fork-session requires --resume"}
	}
	if o.DangerouslyBypass && o.PermissionMode != "" && o.PermissionMode != "bypassPermissions" {
		return &UsageError{Message: "bypass flag conflicts with --permission-mode"}
	}
	if o.PermissionMode != "" {
		switch o.PermissionMode {
		case "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions":
		default:
			return &UsageError{Message: fmt.Sprintf("unsupported permission mode %q", o.PermissionMode)}
		}
	}
	return nil
}

func booleanOption(name string) bool {
	switch name {
	case "-p", "--print", "-h", "--help", "-v", "--version", "--verbose", "--bare", "--trust-workspace", "--mcp-server", "--include-partial-messages", "--replay-user-messages", "--continue", "-c", "--fork-session", "--no-session-persistence", "--owned-process-tree", "--dangerously-skip-permissions", "--bypass-permissions":
		return true
	default:
		return false
	}
}

func appendList(target []string, value string) []string {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			target = append(target, item)
		}
	}
	return target
}

func IsUsageError(err error) bool {
	if errors.Is(err, errUsage) {
		return true
	}
	var target *UsageError
	return errors.As(err, &target)
}

func Usage() string {
	return `Usage: agentx [options] [prompt]

Core options:
  -p, --print                    Run non-interactively
  --output-format FORMAT         text, json, or stream-json
  --input-format FORMAT          text or stream-json
  --include-partial-messages     Emit normalized model deltas in stream-json
  --replay-user-messages         Echo schema-valid user replay acknowledgements
  --model NAME                   Logical model name (default from .env.production)
  --effort LEVEL                 none, low, medium, high, xhigh, or max
  --permission-mode MODE         default, acceptEdits, plan, dontAsk, bypassPermissions
  --allowed-tools LIST           Comma-separated allow rules
  --disallowed-tools LIST        Comma-separated deny rules
  --dangerously-skip-permissions Explicitly request bypassPermissions mode
  --max-turns N                  Bound recursive model turns

Continuity and context:
  --resume SESSION               Resume a durable session
  --continue                     Resume the latest session
  --fork-session                 Fork the session selected by --resume
  --session-id ID                Use an explicit new session identifier
  --no-session-persistence       Disable transcript writes (headless only)
  --system-prompt TEXT           Replace the default system prompt
  --system-prompt-file PATH      Replace it from a bounded file
  --append-system-prompt TEXT    Append dynamic system instructions
  --append-system-prompt-file PATH Append them from a bounded file

Runtime and extensions:
  --env-file PATH                Credential environment file
  --cwd PATH                     Select the working directory
  --bare                         Suppress implicit filesystem instructions, extensions, MCP, and memory
  --trust-workspace              Allow project instructions and executable extensions
  --output-style NAME            Select a discovered response style
  --mcp-config PATH              Load trusted MCP server definitions
  --mcp-server                   Host core capabilities over MCP stdio
  --owned-process-tree           Kill Windows descendants when AgentX exits

Information:
  -h, --help                     Show help
  -v, --version                  Show version`
}
