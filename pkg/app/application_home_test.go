package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/platform"
)

type blockingApplicationHomeAuthorizer struct {
	entered  chan struct{}
	release  chan struct{}
	decision permission.Decision
	err      error
}

func (authorizer *blockingApplicationHomeAuthorizer) Authorize(
	ctx context.Context,
	request permission.Request,
	_ permission.Rebuild,
) (permission.Decision, error) {
	close(authorizer.entered)
	select {
	case <-authorizer.release:
		if authorizer.err != nil {
			return permission.Decision{}, authorizer.err
		}
		if authorizer.decision.Kind != "" {
			return authorizer.decision, nil
		}
		return permission.Decision{
			Kind:          permission.DecisionAllow,
			Input:         request.Input,
			OriginalInput: request.Input,
		}, nil
	case <-ctx.Done():
		return permission.Decision{}, ctx.Err()
	}
}

func setApplicationHomeTestUserHome(t *testing.T, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		return
	}
	t.Setenv("HOME", home)
}

func TestPrepareApplicationHomeDefaultsToUserDotAgentX(t *testing.T) {
	userHome := t.TempDir()
	setApplicationHomeTestUserHome(t, userHome)
	t.Setenv("AGENTX_HOME", "test-reset-before-unset")
	if err := os.Unsetenv("AGENTX_HOME"); err != nil {
		t.Fatal(err)
	}

	home, err := prepareApplicationHome()
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := physicalTestPath(t, filepath.Join(userHome, ".agentx"))
	if home.root.Path() != wantRoot {
		t.Fatalf("application home = %q, want %q", home.root.Path(), wantRoot)
	}
	if home.sessions.Path() != filepath.Join(wantRoot, "sessions") {
		t.Fatalf("sessions directory = %q", home.sessions.Path())
	}
	for _, path := range []string{home.root.Path(), home.sessions.Path()} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s is not a direct directory: %s", path, info.Mode())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", path, info.Mode().Perm())
		}
	}
}

func TestDefaultApplicationHomeRejectsRelativeUserHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows user-home selection does not use HOME")
	}
	t.Setenv("HOME", "relative-home")
	t.Setenv("AGENTX_HOME", "")

	if _, err := applicationHomePath(); err == nil ||
		!strings.Contains(err.Error(), "user home directory must be an absolute path") {
		t.Fatalf("relative user-home error = %v", err)
	}
}

func TestAGENTXHomeOverridesDefaultApplicationHome(t *testing.T) {
	userHome := t.TempDir()
	preferred := filepath.Join(t.TempDir(), "preferred")
	setApplicationHomeTestUserHome(t, userHome)
	t.Setenv("AGENTX_HOME", preferred)

	home, err := prepareApplicationHome()
	if err != nil {
		t.Fatal(err)
	}
	wantPreferred := physicalTestPath(t, preferred)
	if home.root.Path() != wantPreferred {
		t.Fatalf("application home = %q, want AGENTX_HOME %q", home.root.Path(), wantPreferred)
	}
	if _, err := os.Lstat(filepath.Join(userHome, ".agentx")); !os.IsNotExist(err) {
		t.Fatalf("default application home was materialized: %v", err)
	}
}

func TestPreparedApplicationHomeIsFrozenAcrossInvocation(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	t.Setenv("AGENTX_HOME", first)

	ctx, err := PrepareApplicationHome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "auth.json"), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTX_HOME", second)

	var stdout bytes.Buffer
	if err := Run(ctx, []string{"--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("prepared invocation reselected its application home: %v", err)
	}
	if stdout.String() != cli.Usage()+"\n" {
		t.Fatalf("help output = %q", stdout.String())
	}
	if _, err := os.Lstat(second); !os.IsNotExist(err) {
		t.Fatalf("later environment value was materialized: %v", err)
	}
}

func TestPreparedApplicationHomeRejectsPathReplacement(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "agentx-home")
	t.Setenv("AGENTX_HOME", selected)

	ctx, err := PrepareApplicationHome(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, config.DefaultAuthFile), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}

	displaced := filepath.Join(parent, "displaced-home")
	if err := os.Rename(selected, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(selected, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, config.DefaultAuthFile), []byte(`{replacement}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err = Run(ctx, []string{"--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if !errors.Is(err, platform.ErrDirectoryIdentityChanged) {
		t.Fatalf("path replacement error = %v, want ErrDirectoryIdentityChanged", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("replacement home reached the requested surface: %q", stdout.String())
	}
}

func TestApplicationHomeIdentityDenialInvalidatesPendingAndFutureAuthorization(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "agentx-home")
	t.Setenv("AGENTX_HOME", selected)

	home, err := prepareApplicationHome()
	if err != nil {
		t.Fatal(err)
	}
	base := &blockingApplicationHomeAuthorizer{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		decision: permission.Decision{
			Kind:          permission.DecisionAllow,
			Reason:        "synthetic prior approval",
			Source:        "user",
			MatchedRule:   "Read(ordinary)",
			OriginalInput: []byte(`{"file_path":"ordinary"}`),
			Input:         []byte(`{"file_path":"edited"}`),
			UserModified:  true,
			EditCycles:    1,
		},
	}
	authorizer := applicationHomeAuthorizer{home: home, base: base}
	firstResult := make(chan permission.Decision, 1)
	firstError := make(chan error, 1)
	go func() {
		decision, authorizeErr := authorizer.Authorize(
			t.Context(),
			permission.Request{Tool: "Read", Input: []byte(`{"file_path":"ordinary"}`)},
			nil,
		)
		firstResult <- decision
		firstError <- authorizeErr
	}()
	<-base.entered

	displaced := filepath.Join(parent, "displaced-home")
	if err := os.Rename(selected, displaced); err != nil {
		t.Fatal(err)
	}
	second, err := authorizer.Authorize(
		t.Context(),
		permission.Request{Tool: "Read", Input: []byte(`{"file_path":"displaced"}`)},
		nil,
	)
	if err != nil || second.Kind != permission.DecisionDeny {
		t.Fatalf("concurrent identity-loss decision = %#v, error = %v", second, err)
	}
	if err := os.Rename(displaced, selected); err != nil {
		t.Fatal(err)
	}
	close(base.release)

	if err := <-firstError; err != nil {
		t.Fatal(err)
	}
	first := <-firstResult
	if first.Kind != permission.DecisionDeny ||
		first.Reason != "AgentX home identity changed; restart AgentX before using tools" ||
		first.Source != "path" || first.MatchedRule != "" ||
		!bytes.Equal(first.OriginalInput, base.decision.OriginalInput) ||
		!bytes.Equal(first.Input, base.decision.Input) ||
		!first.UserModified || first.EditCycles != 1 {
		t.Fatalf("pending edited approval lost denial evidence: %#v", first)
	}
	third, err := authorizer.Authorize(
		t.Context(),
		permission.Request{Tool: "Read", Input: []byte(`{"file_path":"restored"}`)},
		nil,
	)
	if err != nil || third.Kind != permission.DecisionDeny {
		t.Fatalf("restored-home decision = %#v, error = %v", third, err)
	}
}

func TestApplicationHomeIdentityDenialOverridesBaseErrorAndLatches(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "agentx-home")
	t.Setenv("AGENTX_HOME", selected)

	home, err := prepareApplicationHome()
	if err != nil {
		t.Fatal(err)
	}
	base := &blockingApplicationHomeAuthorizer{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("synthetic approval transport failure"),
	}
	authorizer := applicationHomeAuthorizer{home: home, base: base}
	result := make(chan permission.Decision, 1)
	resultErr := make(chan error, 1)
	go func() {
		decision, authorizeErr := authorizer.Authorize(
			t.Context(),
			permission.Request{Tool: "Read", Input: []byte(`{"file_path":"ordinary"}`)},
			nil,
		)
		result <- decision
		resultErr <- authorizeErr
	}()
	<-base.entered

	displaced := filepath.Join(parent, "displaced-home")
	if err := os.Rename(selected, displaced); err != nil {
		t.Fatal(err)
	}
	close(base.release)
	if err := <-resultErr; err != nil {
		t.Fatalf("identity loss did not override base error: %v", err)
	}
	if decision := <-result; decision.Kind != permission.DecisionDeny {
		t.Fatalf("identity loss after base error = %#v, want deny", decision)
	}
	if err := os.Rename(displaced, selected); err != nil {
		t.Fatal(err)
	}
	future, err := authorizer.Authorize(
		t.Context(),
		permission.Request{Tool: "Read", Input: []byte(`{"file_path":"restored"}`)},
		nil,
	)
	if err != nil || future.Kind != permission.DecisionDeny {
		t.Fatalf("restored-home decision after base error = %#v, error = %v", future, err)
	}
}

func TestInvalidAGENTXHomeDoesNotFallThroughToDefault(t *testing.T) {
	userHome := t.TempDir()
	setApplicationHomeTestUserHome(t, userHome)
	t.Setenv("AGENTX_HOME", "relative-agentx-home")

	if _, err := prepareApplicationHome(); err == nil || !strings.Contains(err.Error(), "AGENTX_HOME must be an absolute path") {
		t.Fatalf("relative AGENTX_HOME error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(userHome, ".agentx")); !os.IsNotExist(err) {
		t.Fatalf("invalid override fell through to the default home: %v", err)
	}
}

func TestBlankAGENTXHomeUsesDefaultApplicationHome(t *testing.T) {
	userHome := t.TempDir()
	setApplicationHomeTestUserHome(t, userHome)
	t.Setenv("AGENTX_HOME", " \t ")

	home, err := prepareApplicationHome()
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := physicalTestPath(t, filepath.Join(userHome, ".agentx"))
	if home.root.Path() != wantDefault {
		t.Fatalf("blank AGENTX_HOME selected %q, want %q", home.root.Path(), wantDefault)
	}
}

func TestApplicationHomeOverridePreservesNonNFCSpelling(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "cafe\u0301")
	got, err := validateApplicationHomeOverride("AGENTX_HOME", configured)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean(configured); got != want {
		t.Fatalf("application-home spelling = %q, want lexical clean %q", got, want)
	}
	if !strings.Contains(got, "e\u0301") {
		t.Fatalf("application-home spelling was unexpectedly Unicode-normalized: %q", got)
	}
}

func TestPrepareApplicationHomeRejectsSymlinkedSessionsDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentx-home")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "sessions")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("AGENTX_HOME", root)

	if _, err := prepareApplicationHome(); err == nil {
		t.Fatal("symlinked sessions directory was accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was populated: %#v", entries)
	}
}

func TestMissingAuthStopsInformationSurfaceWithSetupGuidance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentx-home")
	t.Setenv("AGENTX_HOME", root)
	var stdout bytes.Buffer

	err := Run(t.Context(), []string{"--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if !errors.Is(err, config.ErrAuthFileMissing) {
		t.Fatalf("help without auth.json = %v, want ErrAuthFileMissing", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("help wrote stdout before the auth gate: %q", stdout.String())
	}
	diagnostic := err.Error()
	for _, required := range []string{
		config.UserGuideURL,
		`"version": 1`,
		`"provider": "azure_openai"`,
		`"azure_openai"`,
		`"endpoint"`,
		`"model"`,
		`"deployment"`,
		`"api_key"`,
		`"api_version"`,
	} {
		if !strings.Contains(diagnostic, required) {
			t.Errorf("missing-auth diagnostic lacks %q: %s", required, diagnostic)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "sessions")); statErr != nil {
		t.Fatalf("missing-auth startup did not bootstrap sessions: %v", statErr)
	}
}

func TestInformationSurfaceChecksAuthPresenceWithoutParsingSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentx-home")
	t.Setenv("AGENTX_HOME", root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Information-only startup checks for a direct credential file but does
	// not parse or construct a provider client.
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(t.Context(), []string{"--help"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("help parsed unused credentials: %v", err)
	}
	if stdout.String() != cli.Usage()+"\n" {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestUsageFailureStillBootstrapsApplicationHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentx-home")
	t.Setenv("AGENTX_HOME", root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.DefaultAuthFile), []byte(`{presence-only}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Run(t.Context(), []string{"--print=true"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !cli.IsUsageError(err) {
		t.Fatalf("invalid invocation error = %v, want usage error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "sessions")); statErr != nil {
		t.Fatalf("usage failure did not bootstrap sessions: %v", statErr)
	}
}

func TestMissingAuthPrecedesUsageFailureWithSetupGuidance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentx-home")
	t.Setenv("AGENTX_HOME", root)

	err := Run(t.Context(), []string{"--print=true"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, config.ErrAuthFileMissing) {
		t.Fatalf("invalid invocation without auth = %v, want ErrAuthFileMissing", err)
	}
	if cli.IsUsageError(err) || !strings.Contains(err.Error(), config.UserGuideURL) ||
		!strings.Contains(err.Error(), config.AuthFilePlaceholder) {
		t.Fatalf("missing-auth invocation did not return setup guidance: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "sessions")); statErr != nil {
		t.Fatalf("missing-auth invocation did not bootstrap sessions: %v", statErr)
	}
}

func TestMissingAuthStopsEverySurfaceBeforeDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "malformed", args: []string{"--print=true"}},
		{name: "interactive"},
		{name: "headless", args: []string{"--print", "hello"}},
		{name: "help", args: []string{"--help"}},
		{name: "version", args: []string{"--version"}},
		{name: "standalone MCP", args: []string{"--mcp-server"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "agentx-home")
			t.Setenv("AGENTX_HOME", root)
			var stdout, stderr bytes.Buffer

			err := Run(t.Context(), test.args, strings.NewReader(""), &stdout, &stderr)
			if !errors.Is(err, config.ErrAuthFileMissing) {
				t.Fatalf("Run(%v) = %v, want ErrAuthFileMissing", test.args, err)
			}
			if cli.IsUsageError(err) {
				t.Fatalf("Run(%v) reached CLI usage before the auth gate: %v", test.args, err)
			}
			for _, required := range []string{
				filepath.Join(root, config.DefaultAuthFile),
				config.UserGuideURL,
				config.AuthFilePlaceholder,
			} {
				if !strings.Contains(err.Error(), required) {
					t.Fatalf("Run(%v) diagnostic lacks %q: %v", test.args, required, err)
				}
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("Run(%v) dispatched output before auth: stdout=%q stderr=%q", test.args, stdout.String(), stderr.String())
			}
			entries, readErr := os.ReadDir(filepath.Join(root, "sessions"))
			if readErr != nil {
				t.Fatalf("Run(%v) did not bootstrap sessions: %v", test.args, readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("Run(%v) created persistent session state before auth: %v", test.args, entries)
			}
		})
	}
}

func physicalTestPath(t *testing.T, path string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, filepath.Base(path))
}
