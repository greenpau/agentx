// Package app assembles process-scoped services and dispatches presentation
// adapters. Root main.go intentionally remains a tiny lifecycle delegator.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/command"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/signals"
	"github.com/greenpau/agentx/pkg/surface"
)

type buildIdentity struct {
	version string
	banner  string
}

var currentBuildIdentity atomic.Pointer[buildIdentity]
var configureBuildIdentityOnce sync.Once

const maxStdinBytes = 8 << 20
const stdinFirstByteTimeout = 3 * time.Second

func init() {
	currentBuildIdentity.Store(&buildIdentity{
		version: "development",
		banner:  "agentx development",
	})
}

// ConfigureBuildIdentity installs process-wide build metadata before runtime
// services start. The root entrypoint is the sole production caller.
func ConfigureBuildIdentity(version, banner string) {
	if version == "" {
		return
	}
	configureBuildIdentityOnce.Do(func() {
		if banner == "" {
			banner = "agentx " + version
		}
		currentBuildIdentity.Store(&buildIdentity{version: version, banner: banner})
	})
}

// ProductVersion returns the immutable process-wide semantic version.
func ProductVersion() string {
	return currentBuildIdentity.Load().version
}

func productVersionBanner() string {
	return currentBuildIdentity.Load().banner
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	classes, usage := inspectOperationalError(err)
	if usage != nil {
		return 2
	}
	if operationalErrorClassified(classes, context.Canceled) {
		return 130
	}
	return 1
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx, home, err := applicationHomeForContext(ctx)
	if err != nil {
		return err
	}
	if err := home.requireAuthFile(); err != nil {
		return err
	}
	opts, err := cli.Parse(args)
	if err != nil {
		return err
	}
	if opts.Help && !opts.SessionManagementRequested() {
		return writeStringExact(stdout, cli.Usage()+"\n")
	}
	if opts.Version && !opts.SessionManagementRequested() {
		return writeStringExact(stdout, productVersionBanner()+"\n")
	}
	opts = cli.InferPrint(opts, writerIsTerminal(stdout))
	if err := opts.Validate(); err != nil {
		return err
	}
	if opts.OwnedProcessTree {
		if err := platform.EnableOwnedProcessTree(); err != nil {
			return fmt.Errorf("establish owned process tree: %w", err)
		}
	}
	if opts.Print && !opts.MCPServer {
		var stop func()
		ctx, stop, err = signals.WithPrintInterrupt(ctx)
		if err != nil {
			return fmt.Errorf("start print interrupt monitor: %w", err)
		}
		defer stop()
	}
	workspace := opts.CWD
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(workspace); statErr != nil || !info.IsDir() {
		return fmt.Errorf("working directory is unavailable: %v", statErr)
	}
	if opts.SessionManagementRequested() {
		return runSessionManagement(ctx, home.sessions, opts, workspace, stdout)
	}

	switch {
	case opts.MCPServer:
		return runMCPServer(ctx, opts, workspace, stdin, stdout, stderr)
	case !opts.Print:
		return runInteractive(ctx, opts, workspace, stdin, stdout, stderr)
	case opts.OutputFormat == cli.OutputStreamJSON && opts.InputFormat == cli.InputStreamJSON:
		return runStructured(ctx, opts, workspace, stdin, stdout, stderr)
	case opts.OutputFormat == cli.OutputStreamJSON:
		return runStructuredOneShot(ctx, opts, workspace, stdin, stdout, stderr)
	default:
		return runHeadless(ctx, opts, workspace, stdin, stdout, stderr)
	}
}

func runHeadless(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	session, err := buildSession(ctx, buildOptions{CLI: opts, Workspace: workspace, Sink: discardSink{}, Stderr: stderr})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = redactOperationalError(errors.Join(returnErr, session.Close()), session.sanitize)
	}()
	promptText, err := headlessPromptContextWithTerminalWarnings(ctx, opts.Prompt, stdin, stderr, session.credentials, stdinFirstByteTimeout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(promptText) == "" {
		return &cli.UsageError{Message: "a prompt is required in headless mode"}
	}
	commandStarted := time.Now()
	commandResult, isCommand, commandErr := session.dispatchUserCommand(ctx, promptText, true)
	if isCommand && (commandErr != nil || commandResult.Kind != command.ResultPrompt) {
		outcome := session.localCommandOutcome(commandResult, commandErr, commandStarted)
		if opts.OutputFormat == cli.OutputJSON {
			if err := writeJSONResult(stdout, outcome, commandErr, session.credentials); err != nil {
				return err
			}
		} else if commandErr == nil && outcome.Text != "" {
			if err := writeTerminalRecord(stdout, session.credentials, outcome.Text+"\n"); err != nil {
				return err
			}
		}
		return commandErr
	}
	if isCommand {
		promptText = commandResult.Prompt
		if strings.TrimSpace(promptText) == "" {
			return errors.New("prompt command produced empty model input")
		}
	}
	outcome, runErr := session.submitPrompt(ctx, promptText, "")
	if opts.OutputFormat == cli.OutputJSON {
		if err := writeJSONResult(stdout, outcome, runErr, session.credentials); err != nil {
			return err
		}
	} else if outcome.Text != "" {
		if err := writeTerminalRecord(stdout, session.credentials, outcome.Text+"\n"); err != nil {
			return err
		}
	}
	return runErr
}

func runInteractive(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	interactions := newTerminalInteractions(stdin, stdout)
	defer interactions.Close()
	sink := newInteractiveSink(stdout)
	session, err := buildSession(ctx, buildOptions{CLI: opts, Workspace: workspace, Sink: sink, Approver: interactions.Approve, Ask: interactions.Ask, Stderr: stderr})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = redactOperationalError(errors.Join(returnErr, session.Close()), session.sanitize)
	}()
	interactions.SetCredentialSanitizer(session.credentials)
	sink.SetCredentialSanitizer(session.credentials)
	if err := writeTerminalRecord(stdout, session.credentials, fmt.Sprintf("AgentX %s — %s via Azure OpenAI — session %s\n", ProductVersion(), session.config.Azure.ModelName, session.engine.SessionID())); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := interactions.ReadLine(ctx, "> ")
		if operationalErrorIs(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if result, isCommand, dispatchErr := session.dispatchUserCommand(ctx, line, false); isCommand {
			if dispatchErr != nil {
				if err := writeTerminalRecord(stderr, session.credentials, safeOperationalErrorText(dispatchErr)+"\n"); err != nil {
					return err
				}
				continue
			}
			if result.Kind == command.ResultExit {
				return nil
			}
			if result.Output != "" {
				if err := writeTerminalRecord(stdout, session.credentials, result.Output+"\n"); err != nil {
					return err
				}
			}
			if result.Kind != command.ResultPrompt {
				continue
			}
			line = result.Prompt
		}
		outcome, runErr := session.submitPrompt(ctx, line, "")
		if err := sink.finish(outcome); err != nil {
			return err
		}
		if runErr != nil {
			if err := writeTerminalRecord(stderr, session.credentials, safeOperationalErrorText(runErr)+"\n"); err != nil {
				return err
			}
			if operationalErrorIs(runErr, context.Canceled) {
				return runErr
			}
		}
	}
}

func headlessPrompt(positional string, reader io.Reader) (string, error) {
	return headlessPromptContext(context.Background(), positional, reader)
}

func headlessPromptContext(ctx context.Context, positional string, reader io.Reader) (string, error) {
	return headlessPromptContextWithWarnings(ctx, positional, reader, io.Discard, stdinFirstByteTimeout)
}

func headlessPromptContextWithWarnings(ctx context.Context, positional string, reader io.Reader, warnings io.Writer, firstByteTimeout time.Duration) (string, error) {
	if file, ok := reader.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return positional, nil
		}
	}
	data, timedOut, err := readAllContextFirstByte(ctx, reader, maxStdinBytes, firstByteTimeout)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if timedOut && warnings != nil {
		record := fmt.Sprintf("warning: no stdin data received within %s; continuing without piped input\n", firstByteTimeout)
		if err := writeStringExact(warnings, record); err != nil {
			return "", fmt.Errorf("write stdin timeout warning: %w", err)
		}
	}
	if len(data) > maxStdinBytes {
		return "", errors.New("stdin exceeds 8 MiB")
	}
	if !utf8.Valid(data) {
		return "", errors.New("stdin is not valid UTF-8")
	}
	// Text-mode stdin is prompt content, not terminal input. Normalize line
	// endings while preserving indentation and trailing whitespace within the
	// prompt; trimming the payload would silently rewrite pasted source code.
	stdinText := strings.ReplaceAll(string(data), "\r\n", "\n")
	stdinText = strings.ReplaceAll(stdinText, "\r", "\n")
	switch {
	case strings.TrimSpace(positional) == "":
		return stdinText, nil
	case strings.TrimSpace(stdinText) == "":
		return positional, nil
	default:
		return strings.TrimRight(positional, "\r\n") + "\n" + strings.TrimLeft(stdinText, "\n"), nil
	}
}

func headlessPromptContextWithTerminalWarnings(ctx context.Context, positional string, reader io.Reader, warnings io.Writer, credentials *redact.Set, firstByteTimeout time.Duration) (string, error) {
	projector := newTerminalLineWriter(warnings, credentials)
	prompt, err := headlessPromptContextWithWarnings(ctx, positional, reader, projector, firstByteTimeout)
	return prompt, errors.Join(err, projector.Flush())
}

func writerIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Keep protocol imported here so compile-time surface construction cannot
// accidentally drift to a provider-specific event type.
var _ engine.EventSink = discardSink{}
var _ = protocol.CurrentVersion
var _ = surface.MaxNDJSONRecordBytes
