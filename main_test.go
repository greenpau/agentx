package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	agentapp "github.com/greenpau/agentx/pkg/app"
	"github.com/greenpau/agentx/pkg/cli"
)

func TestBuildIdentityDefaults(t *testing.T) {
	wantVersion := appVersion
	if wantVersion == "" {
		data, err := os.ReadFile("VERSION")
		if err != nil {
			t.Fatal(err)
		}
		wantVersion = strings.TrimSpace(string(data))
	}
	if app.Version != wantVersion {
		t.Fatalf("package version = %q, want %q", app.Version, wantVersion)
	}
	if agentapp.ProductVersion() != app.Version {
		t.Fatalf("application version = %q, package version = %q", agentapp.ProductVersion(), app.Version)
	}
	if !strings.HasPrefix(app.Banner(), "agentx "+wantVersion) {
		t.Fatalf("banner = %q, want version prefix %q", app.Banner(), wantVersion)
	}
}

func TestLinkerStampedBuildIdentity(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agentx")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(
		"go", "build", "-o", binary,
		"-ldflags=-X main.appVersion=9.8.7 -X main.gitBranch=release-test -X main.gitCommit=deadbeef -X main.buildUser=builder -X main.buildDate=2026-07-23",
		".",
	)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stamped binary: %v\n%s", err, output)
	}
	output, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run stamped binary: %v\n%s", err, output)
	}
	got := strings.TrimSpace(string(output))
	wantPrefix := "agentx 9.8.7, branch: release-test, commit: deadbeef, build on 2026-07-23 by builder ("
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("stamped banner = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("stamped banner = %q, want runtime %s/%s", got, runtime.GOOS, runtime.GOARCH)
	}
}

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
			wantStdout: app.Banner() + "\n",
		},
		{
			name:       "short version",
			args:       []string{"-v"},
			wantStdout: app.Banner() + "\n",
		},
		{
			name:       "compatibility version",
			args:       []string{"-V"},
			wantStdout: app.Banner() + "\n",
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
