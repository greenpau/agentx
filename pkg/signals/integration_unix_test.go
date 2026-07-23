//go:build unix

package signals

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	signalHelperEnvironment      = "AGENTX_SIGNALS_INTEGRATION_HELPER"
	printSignalHelperEnvironment = "AGENTX_PRINT_SIGNALS_INTEGRATION_HELPER"
)

// TestProcessMonitorSubprocess exercises the real os/signal registration path
// without delivering a terminating signal to the parent test process.
func TestProcessMonitorSubprocess(t *testing.T) {
	if os.Getenv(signalHelperEnvironment) == "1" {
		runProcessMonitorHelper(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessMonitorSubprocess$", "-test.count=1")
	command.Env = append(os.Environ(), signalHelperEnvironment+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	if line := readHelperLine(t, reader); line != "ready" {
		t.Fatalf("helper readiness = %q; stderr=%q", line, stderr.String())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if line := readHelperLine(t, reader); line != "latched:143:sigterm" {
		t.Fatalf("helper first request = %q; stderr=%q", line, stderr.String())
	}
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
		t.Fatalf("second-signal exit = %v (code %d); stderr=%q", err, processExitCode(exitError), stderr.String())
	}
}

func runProcessMonitorHelper(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Keep the force deadline beyond the parent harness timeout so only the
	// deliberately delivered second signal can satisfy the exit assertion.
	state := NewProcessShutdown(os.Exit, 30*time.Second)
	stop, err := StartProcessMonitor(cancel, state, InterruptOwnedByProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop() }()
	fmt.Fprintln(os.Stdout, "ready")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("helper did not observe the first signal")
	}
	request, ok := state.Snapshot()
	if !ok {
		t.Fatal("helper has no winning signal request")
	}
	fmt.Fprintf(os.Stdout, "latched:%d:%s\n", request.ExitCode, request.Reason)
	select {}
}

// TestPrintOwnershipSubprocess proves that the process monitor acquires SIGINT
// before application startup but routes its semantic cancellation to the print
// context once registered. A different second signal still forces the global
// winning code.
func TestPrintOwnershipSubprocess(t *testing.T) {
	if os.Getenv(printSignalHelperEnvironment) == "1" {
		runPrintOwnershipHelper(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrintOwnershipSubprocess$", "-test.count=1")
	command.Env = append(os.Environ(), printSignalHelperEnvironment+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	if line := readHelperLine(t, reader); line != "ready" {
		t.Fatalf("helper readiness = %q; stderr=%q", line, stderr.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if line := readHelperLine(t, reader); line != "latched:0:sigint:root-live" {
		t.Fatalf("helper print request = %q; stderr=%q", line, stderr.String())
	}
	if err := command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("second-signal zero exit = %v; stderr=%q", err, stderr.String())
	}
}

func runPrintOwnershipHelper(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	// Keep the force deadline beyond the parent harness timeout so only the
	// deliberately delivered second signal can satisfy the exit assertion.
	state := NewProcessShutdown(os.Exit, 30*time.Second)
	stopProcess, err := StartProcessMonitor(cancelRoot, state, InterruptOwnedByPrint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stopProcess() }()
	parent := WithProcessShutdown(rootCtx, state)
	printCtx, stopPrint, err := WithPrintInterrupt(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer stopPrint()
	fmt.Fprintln(os.Stdout, "ready")
	select {
	case <-printCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("helper print context did not observe SIGINT")
	}
	select {
	case <-rootCtx.Done():
		t.Fatal("print-owned SIGINT cancelled the root context")
	default:
	}
	request, ok := state.Snapshot()
	if !ok {
		t.Fatal("helper has no winning print request")
	}
	fmt.Fprintf(os.Stdout, "latched:%d:%s:root-live\n", request.ExitCode, request.Reason)
	select {}
}

func readHelperLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read helper output: %v", err)
	}
	return strings.TrimSpace(line)
}

func processExitCode(exitError *exec.ExitError) int {
	if exitError == nil {
		return 0
	}
	return exitError.ExitCode()
}
