package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/greenpau/agentx/pkg/app"
	"github.com/greenpau/agentx/pkg/cli"
)

func TestRunProcessInformationalAndUsageExitBoundary(t *testing.T) {
	// runProcess owns process-global signal registration, so these cases must
	// remain sequential and must not use t.Parallel.
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version",
			args:       []string{"--version"},
			wantStdout: "agentx " + app.Version + "\n",
		},
		{
			name:       "help",
			args:       []string{"--help"},
			wantStdout: cli.Usage() + "\n",
		},
		{
			name:       "usage error",
			args:       []string{"--print=true"},
			wantCode:   2,
			wantStderr: "--print does not accept a value\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var forceCalls atomic.Int32
			var forcedCode atomic.Int64
			code := runProcess(test.args, strings.NewReader(""), &stdout, &stderr, func(code int) {
				forcedCode.Store(int64(code))
				forceCalls.Add(1)
			})
			if code != test.wantCode {
				t.Errorf("exit code = %d, want %d", code, test.wantCode)
			}
			if stdout.String() != test.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), test.wantStdout)
			}
			if stderr.String() != test.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantStderr)
			}
			if calls := forceCalls.Load(); calls != 0 {
				t.Errorf("forceExit invoked %d times with code %d", calls, forcedCode.Load())
			}
		})
	}
}
