package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectReturnsExplicitBackendState(t *testing.T) {
	runner := Detect(context.Background(), t.TempDir(), nil)
	status := runner.Status()
	if status.State != StateAvailable && status.State != StateUnsupported && status.State != StateUnavailable {
		t.Fatalf("invalid sandbox state: %#v", status)
	}
	if runtime.GOOS != "darwin" && status.State != StateUnsupported {
		t.Fatalf("non-Darwin status = %#v", status)
	}
}

func TestMacOSProfileEscapesWorkspaceAndBoundsWrites(t *testing.T) {
	profile := macOSProfile(`/tmp/work\"quoted`, []string{"GOCACHE=/tmp/go-cache", "AZURE_OPENAI_SUBSCRIPTION_KEY=secret"})
	for _, required := range []string{"(deny file-write*)", `/tmp/go-cache`, `work\\\"quoted`} {
		if !strings.Contains(profile, required) {
			t.Fatalf("profile missing %q: %s", required, profile)
		}
	}
	if strings.Contains(profile, "secret") {
		t.Fatal("sandbox profile included an unrelated environment secret")
	}
}

func TestPublicRunnerContainsNilContextsAndOpaqueProfileFormatting(t *testing.T) {
	const secret = "sandbox-profile-secret-must-not-escape"
	runner := &Runner{
		status:  Status{State: StateAvailable, Backend: "sandbox-exec"},
		profile: `(version 1)(allow file-write* (subpath "/` + secret + `"))`,
	}
	cmd := runner.Command(nil, "/usr/bin/true")
	if cmd == nil || cmd.Path == "" {
		t.Fatalf("nil-context command was not constructed: %#v", cmd)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if rendered := fmt.Sprintf(format, runner); strings.Contains(rendered, secret) {
			t.Fatalf("runner %s exposed private profile in %q", format, rendered)
		}
	}
	// Detect must also remain panic-free when a host gives no parent context.
	detected := Detect(nil, t.TempDir(), nil)
	if detected == nil {
		t.Fatal("nil-context detection returned nil runner")
	}
}

func TestMacOSProfileRejectsBroadOrInvalidWorkspaceRoots(t *testing.T) {
	for _, workspace := range []string{"", string(filepathSeparator()), "/\nunsafe"} {
		profile := macOSProfile(workspace, nil)
		if strings.Contains(profile, `(subpath ".")`) || strings.Contains(profile, `(subpath "/")`) || strings.Contains(profile, "unsafe") {
			t.Fatalf("unsafe workspace %q widened profile: %s", workspace, profile)
		}
	}
}

func TestMacOSProfileRejectsFilesystemRootFromAmbientWriteDirectories(t *testing.T) {
	profile := macOSProfile(t.TempDir(), []string{
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + string(filepathSeparator()),
		"GOCACHE=" + string(filepathSeparator()),
	})
	if strings.Contains(profile, `(subpath "/")`) || strings.Contains(profile, `(subpath "\\")`) {
		t.Fatalf("ambient root widened sandbox profile: %s", profile)
	}
}

func TestSandboxRootCanonicalizesSymlinkBeforeBroadRootCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Unix-style filesystem roots")
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(string(filepathSeparator()), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if root, ok := sandboxRoot(link); ok {
		t.Fatalf("symlink to filesystem root accepted as sandbox root: %q", root)
	}
}

func filepathSeparator() byte {
	if runtime.GOOS == "windows" {
		return '\\'
	}
	return '/'
}
