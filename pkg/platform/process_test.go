package platform

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("AGENTX_PROCESS_HELPER") != "1" {
		return
	}
	separator := 0
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	mode := os.Args[separator]
	switch mode {
	case "output":
		fmt.Print(strings.Repeat("x", 20))
		fmt.Fprint(os.Stderr, strings.Repeat("y", 12))
	case "exit":
		fmt.Print("evidence")
		os.Exit(3)
	case "environment":
		fmt.Print(os.Getenv("AGENTX_SECRET_FOR_TEST"))
	case "sleep":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func helperSpec(mode string) ProcessSpec {
	return ProcessSpec{
		Program:     os.Args[0],
		Args:        []string{"-test.run=TestProcessHelper", "--", mode},
		Environment: map[string]string{"AGENTX_PROCESS_HELPER": "1"},
	}
}

func TestRunProcessPreservesExitAndBoundsOutput(t *testing.T) {
	spec := helperSpec("output")
	spec.OutputLimit = 8
	result := RunProcess(context.Background(), spec)
	if result.ExitCode != 0 || result.SpawnError != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stdout != "xxxxxxxx" || result.StdoutOmitted != 12 {
		t.Fatalf("stdout evidence = %+v", result)
	}
	// Coverage-instrumented helper binaries may append their own diagnostic to
	// stderr during os.Exit. The process contract must retain the first eight
	// bytes and account for at least the four deliberately truncated bytes;
	// any harness suffix is also correctly counted as omitted output.
	if result.Stderr != "yyyyyyyy" || result.StderrOmitted < 4 {
		t.Fatalf("stderr evidence = %+v", result)
	}

	nonzero := RunProcess(context.Background(), helperSpec("exit"))
	if nonzero.ExitCode != 3 || nonzero.SpawnError != "" || nonzero.Stdout != "evidence" {
		t.Fatalf("nonzero result = %+v", nonzero)
	}
}

func TestRunProcessFiltersEnvironmentAndClassifiesTimeout(t *testing.T) {
	t.Setenv("AGENTX_SECRET_FOR_TEST", "must-not-leak")
	spec := helperSpec("environment")
	spec.InheritSafeEnv = true
	result := RunProcess(context.Background(), spec)
	if result.Stdout != "" {
		t.Fatalf("secret inherited by child: %q", result.Stdout)
	}

	spec = helperSpec("sleep")
	spec.Timeout = 30 * time.Millisecond
	result = RunProcess(context.Background(), spec)
	if !result.TimedOut || result.Cancelled {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestRunProcessRejectsModelCredentialOverlayAliases(t *testing.T) {
	const secret = "synthetic-platform-model-credential"
	t.Setenv("AZURE_OPENAI_SUBSCRIPTION_KEY", secret)
	spec := helperSpec("environment")
	spec.Environment["RENAMED_CREDENTIAL"] = "prefix-" + secret + "-suffix"
	result := RunProcess(t.Context(), spec)
	if result.SpawnError != "invalid child environment" || result.Exited {
		t.Fatalf("credential-bearing process overlay was not rejected before spawn: %+v", result)
	}
	if strings.Contains(result.SpawnError+result.Stderr+result.Stdout, secret) {
		t.Fatalf("credential appeared in process diagnostic: %+v", result)
	}
}
