package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/childenv"
)

const DefaultProcessOutputLimit int64 = 1_000_000

// ProcessSpec intentionally accepts an executable and argument vector rather
// than a shell command. A shell capability must opt into a shell explicitly.
type ProcessSpec struct {
	Program        string
	Args           []string
	Directory      string
	Environment    map[string]string
	InheritSafeEnv bool
	Stdin          []byte
	Timeout        time.Duration
	OutputLimit    int64
}

// ProcessResult preserves independent terminal causes. A nonzero exit is an
// ordinary result, while SpawnError describes failure to start or wait.
type ProcessResult struct {
	Stdout        string        `json:"stdout"`
	Stderr        string        `json:"stderr"`
	StdoutOmitted int64         `json:"stdout_omitted_bytes"`
	StderrOmitted int64         `json:"stderr_omitted_bytes"`
	ExitCode      int           `json:"exit_code"`
	Exited        bool          `json:"exited"`
	Cancelled     bool          `json:"cancelled"`
	TimedOut      bool          `json:"timed_out"`
	StartedAt     time.Time     `json:"started_at"`
	EndedAt       time.Time     `json:"ended_at"`
	Duration      time.Duration `json:"duration"`
	SpawnError    string        `json:"spawn_error,omitempty"`
}

// RunProcess executes a bounded child without a shell. Output beyond the
// configured memory limit is drained and counted instead of blocking the child.
func RunProcess(ctx context.Context, spec ProcessSpec) ProcessResult {
	started := time.Now()
	result := ProcessResult{StartedAt: started, ExitCode: -1}
	finish := func() ProcessResult {
		result.EndedAt = time.Now()
		result.Duration = result.EndedAt.Sub(started)
		return result
	}
	if strings.TrimSpace(spec.Program) == "" {
		result.SpawnError = "program is empty"
		return finish()
	}
	if ctx == nil {
		result.SpawnError = "context is nil"
		return finish()
	}
	if spec.OutputLimit <= 0 {
		spec.OutputLimit = DefaultProcessOutputLimit
	}
	ownedCtx := ctx
	cancel := func() {}
	if spec.Timeout > 0 {
		ownedCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
	}
	defer cancel()

	environment, envErr := childenv.Process(os.Environ(), spec.InheritSafeEnv, spec.Environment)
	if envErr != nil {
		result.SpawnError = "invalid child environment"
		return finish()
	}
	command := exec.CommandContext(ownedCtx, spec.Program, spec.Args...)
	configureOwnedProcess(command)
	command.Dir = spec.Directory
	command.Env = environment
	if spec.Stdin != nil {
		command.Stdin = bytes.NewReader(spec.Stdin)
	}
	stdout := newBoundedBuffer(spec.OutputLimit)
	stderr := newBoundedBuffer(spec.OutputLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result.Stdout, result.StdoutOmitted = stdout.String(), stdout.Omitted()
	result.Stderr, result.StderrOmitted = stderr.String(), stderr.Omitted()
	if command.ProcessState != nil {
		result.Exited = command.ProcessState.Exited()
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if errors.Is(ownedCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	} else if errors.Is(ownedCtx.Err(), context.Canceled) {
		result.Cancelled = true
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			result.SpawnError = fmt.Sprintf("%T: %v", err, err)
		}
	}
	return finish()
}

type boundedBuffer struct {
	mu      sync.Mutex
	data    []byte
	limit   int64
	omitted int64
}

func newBoundedBuffer(limit int64) *boundedBuffer { return &boundedBuffer{limit: limit} }

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	remaining := b.limit - int64(len(b.data))
	if remaining > 0 {
		keep := min(int64(len(value)), remaining)
		b.data = append(b.data, value[:keep]...)
		value = value[keep:]
	}
	b.omitted += int64(len(value))
	return original, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data))
}

func (b *boundedBuffer) Omitted() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.omitted
}
