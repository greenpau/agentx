package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/childenv"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/task"
)

const (
	defaultBashTimeout  = 120_000
	maximumBashTimeout  = 600_000
	maximumShellCapture = 1 << 20
)

type bashInput struct {
	Command         string `json:"command"`
	Description     string `json:"description,omitempty"`
	Timeout         int    `json:"timeout,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

type bashInvocation struct {
	Input    bashInput
	Analysis permission.ShellAnalysis
}

type shellCommandRunner interface {
	Command(context.Context, string, ...string) *exec.Cmd
}

func bashDescriptor(workspace, shell string, manager *task.Manager, environment []string, sandboxers ...shellCommandRunner) Descriptor {
	var sandboxer shellCommandRunner
	if len(sandboxers) > 0 {
		sandboxer = sandboxers[0]
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	return Descriptor{
		Name: "Bash", Source: SourceBuiltin,
		Description: "Execute an authorized Bash command synchronously or as a retrievable background task.",
		InputSchema: objectSchema(map[string]any{
			"command":           stringSchema("Complete Bash command"),
			"description":       stringSchema("Short user-facing description"),
			"timeout":           integerSchema("Timeout in milliseconds", 1, maximumBashTimeout),
			"run_in_background": booleanSchema("Launch as a durable task"),
		}, "command"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input bashInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if strings.TrimSpace(input.Command) == "" {
				return nil, errors.New("command is required")
			}
			if input.Timeout == 0 {
				input.Timeout = defaultBashTimeout
			}
			if input.Timeout < 1 || input.Timeout > maximumBashTimeout {
				return nil, fmt.Errorf("timeout must be between 1 and %d milliseconds", maximumBashTimeout)
			}
			analysis, err := permission.AnalyzeShell(input.Command, workspace)
			if err != nil {
				return nil, err
			}
			return bashInvocation{Input: input, Analysis: analysis}, nil
		},
		Semantic: func(value any) error {
			invocation := value.(bashInvocation)
			if invocation.Input.RunInBackground && manager == nil {
				return errors.New("background task runtime is unavailable")
			}
			if !filepath.IsAbs(shell) {
				return errors.New("configured shell path is not absolute")
			}
			if _, err := os.Stat(shell); err != nil {
				return fmt.Errorf("configured shell is unavailable: %w", err)
			}
			if _, err := task.ShellArguments(shell, invocation.Input.Command); err != nil {
				return err
			}
			return nil
		},
		Classify: func(value any) permission.Classification {
			invocation := value.(bashInvocation)
			// Shell commands are never auto-authorized from a generic read-only
			// label. Repository configuration and command-specific file options
			// can turn apparently observational programs into code execution or
			// protected reads. The analyzer still projects paths, deny candidates,
			// and danger signals for composed policy and explicit allow rules.
			return permission.Classification{
				Destructive: invocation.Analysis.Dangerous,
			}
		},
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			invocation := value.(bashInvocation)
			return permission.Request{
				Input: raw, Content: invocation.Input.Command, MatchContents: invocation.Analysis.Segments,
				DenyContents: invocation.Analysis.DenyCandidates, AllowContents: invocation.Analysis.AllowCandidates,
				Paths: invocation.Analysis.Paths, Shell: &invocation.Analysis,
			}, nil
		},
		Call: func(ctx context.Context, call CallContext, value any) (Output, error) {
			invocation := value.(bashInvocation)
			input := invocation.Input
			env := environment
			if env == nil {
				env = os.Environ()
			}
			env = sanitizedEnvironment(env)
			if input.RunInBackground {
				var commandFactory func(context.Context, string, ...string) *exec.Cmd
				if sandboxer != nil {
					commandFactory = sandboxer.Command
				}
				record, err := retryTaskBusy(ctx, func() (task.Record, error) {
					return manager.LaunchShell(ctx, task.ShellSpec{
						Command: input.Command, Description: input.Description, ToolUseID: call.ToolUseID,
						Dir: workspace, Env: env, Shell: shell, Timeout: time.Duration(input.Timeout) * time.Millisecond,
						CommandFactory: commandFactory,
					})
				})
				if err != nil {
					return Output{}, taskInvocationError("launch background shell", err)
				}
				payload, _ := json.Marshal(record)
				return Output{Content: string(payload), Metadata: map[string]any{"task_id": record.ID, "status": record.Status}}, nil
			}
			timeoutContext, cancel := context.WithTimeout(ctx, time.Duration(input.Timeout)*time.Millisecond)
			defer cancel()
			shellArgs, err := task.ShellArguments(shell, input.Command)
			if err != nil {
				return Output{}, invocationError("unavailable", "%v", err)
			}
			cmd := exec.CommandContext(timeoutContext, shell, shellArgs...)
			if sandboxer != nil {
				cmd = sandboxer.Command(timeoutContext, shell, shellArgs...)
			}
			cmd.Dir = workspace
			cmd.Env = env
			// The foreground invocation owns a fresh process group. Replacing
			// CommandContext's default single-process kill prevents children that
			// inherited the shell's group from surviving cancellation or timeout;
			// This call path still performs the sole Wait and reaps the group leader.
			task.PrepareOwnedProcess(cmd)
			cmd.Cancel = func() error { return task.StopOwnedProcess(cmd) }
			captureLimit := maximumShellCapture
			if call.CredentialLookahead > 0 && captureLimit <= int(^uint(0)>>1)-call.CredentialLookahead {
				captureLimit += call.CredentialLookahead
			}
			capture := &boundedBuffer{limit: captureLimit}
			cmd.Stdout = capture
			cmd.Stderr = capture
			err = cmd.Start()
			if err == nil {
				if verifyErr := task.VerifyOwnedProcess(cmd); verifyErr != nil {
					stopErr := task.StopOwnedProcess(cmd)
					waitErr := cmd.Wait()
					err = errors.Join(fmt.Errorf("verify process containment: %w", verifyErr), stopErr, waitErr)
				} else {
					err = cmd.Wait()
				}
			}
			output, captureTruncated, outputSuppressed := capture.Project(call.ProjectOutput, maximumShellCapture)
			if timeoutContext.Err() != nil {
				if errors.Is(timeoutContext.Err(), context.DeadlineExceeded) {
					return Output{}, invocationError("timeout", "command timed out after %d ms\n%s", input.Timeout, output)
				}
				return Output{}, timeoutContext.Err()
			}
			if err != nil {
				var exitError *exec.ExitError
				if errors.As(err, &exitError) {
					return Output{}, invocationError("execution_failed", "command exited with status %d\n%s", exitError.ExitCode(), output)
				}
				return Output{}, invocationError("execution_failed", "start command: %v", err)
			}
			return Output{Content: output, ContentSuppressed: outputSuppressed, Metadata: map[string]any{
				"exit_code": 0, "truncated": captureTruncated,
			}}, nil
		},
		MaxResultChars: 30_000,
	}
}

func sanitizedEnvironment(environment []string) []string {
	return childenv.Shell(environment)
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		chunk := p
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = b.buffer.Write(chunk)
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.buffer.String()
	if b.truncated {
		result += "\n[process output capture truncated]"
	}
	return result
}

func (b *boundedBuffer) Project(project func(string, bool, int) (string, bool, bool), limit int) (string, bool, bool) {
	b.mu.Lock()
	value := b.buffer.String()
	rawTruncated := b.truncated
	b.mu.Unlock()
	if project == nil {
		if len(value) > limit {
			value = value[:limit]
			rawTruncated = true
		}
		if rawTruncated {
			value += "\n[process output capture truncated]"
		}
		return value, rawTruncated, false
	}
	return project(value, rawTruncated, limit)
}
