package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/tool"
)

// applicationHome pins the process-wide user data root and the mandatory
// sessions child to direct directory identities. Supported POSIX platforms
// additionally prove effective-user ownership and owner-only mode. Callers
// must verify the identities again before deriving sensitive paths beneath
// them.
type applicationHome struct {
	root     *platform.OwnedDirectory
	sessions *platform.OwnedDirectory
	authPath string

	// toolAuthorizationBlocked latches the first observed loss of either
	// bootstrap directory identity. Restoring an inode to its former pathname
	// cannot make a process that observed the violation trustworthy again.
	toolAuthorizationBlocked atomic.Bool
}

type applicationHomeContextKey struct{}

type applicationHomeAuthorizer struct {
	home *applicationHome
	base tool.Authorizer
}

func (authorizer applicationHomeAuthorizer) Authorize(
	ctx context.Context,
	request permission.Request,
	rebuild permission.Rebuild,
) (permission.Decision, error) {
	if authorizer.home == nil || authorizer.base == nil {
		return permission.Decision{}, errors.New("application-home authorization boundary is unavailable")
	}
	if authorizer.home.toolAuthorizationBlocked.Load() {
		return applicationHomeIdentityDenial(request, nil), nil
	}
	if err := authorizer.home.verify(); err != nil {
		authorizer.home.toolAuthorizationBlocked.Store(true)
		return applicationHomeIdentityDenial(request, nil), nil
	}
	// A concurrent request may have observed an identity violation while this
	// verification was in progress. Honor that process-wide latch before
	// delegating to an evaluator that could otherwise approve the call.
	if authorizer.home.toolAuthorizationBlocked.Load() {
		return applicationHomeIdentityDenial(request, nil), nil
	}
	decision, baseErr := authorizer.base.Authorize(ctx, request, rebuild)
	// Permission evaluation may block for an interactive approval. Reverify
	// after it returns so a concurrent identity violation cannot turn an
	// earlier pending approval into executable authority. Perform this check
	// even when the evaluator failed: identity loss is the stronger terminal
	// outcome and must remain latched if the original inode is later restored.
	if authorizer.home.toolAuthorizationBlocked.Load() {
		return applicationHomeIdentityDenial(request, &decision), nil
	}
	if err := authorizer.home.verify(); err != nil {
		authorizer.home.toolAuthorizationBlocked.Store(true)
		return applicationHomeIdentityDenial(request, &decision), nil
	}
	if authorizer.home.toolAuthorizationBlocked.Load() {
		return applicationHomeIdentityDenial(request, &decision), nil
	}
	if baseErr != nil {
		return permission.Decision{}, baseErr
	}
	return decision, nil
}

func applicationHomeIdentityDenial(
	request permission.Request,
	evaluated *permission.Decision,
) permission.Decision {
	denial := permission.Decision{
		Kind:          permission.DecisionDeny,
		Reason:        "AgentX home identity changed; restart AgentX before using tools",
		Source:        "path",
		Input:         append([]byte(nil), request.Input...),
		OriginalInput: append([]byte(nil), request.Input...),
	}
	if evaluated == nil {
		return denial
	}
	if len(evaluated.OriginalInput) > 0 {
		denial.OriginalInput = append([]byte(nil), evaluated.OriginalInput...)
	}
	if len(evaluated.Input) > 0 {
		denial.Input = append([]byte(nil), evaluated.Input...)
	}
	denial.UserModified = evaluated.UserModified
	denial.EditCycles = evaluated.EditCycles
	return denial
}

// PrepareApplicationHome acquires the process invocation's application home
// before any command-line inspection and freezes that identity in ctx. All
// later startup stages reuse the same root instead of re-reading environment
// selection inputs.
func PrepareApplicationHome(ctx context.Context) (context.Context, error) {
	prepared, _, err := applicationHomeForContext(ctx)
	return prepared, err
}

// RequireApplicationAuth applies the non-reading auth.json presence gate to a
// prepared invocation. The root entrypoint calls it before any full CLI parse;
// Run repeats it so direct package callers retain the same prerequisite.
func RequireApplicationAuth(ctx context.Context) error {
	_, home, err := applicationHomeForContext(ctx)
	if err != nil {
		return err
	}
	return home.requireAuthFile()
}

func applicationHomeForContext(ctx context.Context) (context.Context, *applicationHome, error) {
	if ctx == nil {
		return nil, nil, errors.New("application context is unavailable")
	}
	if home, ok := ctx.Value(applicationHomeContextKey{}).(*applicationHome); ok && home != nil {
		if err := home.verify(); err != nil {
			return ctx, nil, err
		}
		return ctx, home, nil
	}
	home, err := prepareApplicationHome()
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, applicationHomeContextKey{}, home), home, nil
}

// applicationHomePath returns the effective per-user application root.
// AGENTX_HOME is the only supported override and relocates the complete
// application home rather than only sessions.
func applicationHomePath() (string, error) {
	if configured := os.Getenv("AGENTX_HOME"); strings.TrimSpace(configured) != "" {
		return validateApplicationHomeOverride("AGENTX_HOME", configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine user home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("user home directory is unavailable")
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("user home directory must be an absolute path")
	}
	return filepath.Clean(filepath.Join(home, ".agentx")), nil
}

func validateApplicationHomeOverride(name, configured string) (string, error) {
	if !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	cleaned := filepath.Clean(configured)
	if filepath.Dir(cleaned) == cleaned {
		return "", fmt.Errorf("%s cannot name a filesystem root", name)
	}
	return cleaned, nil
}

// prepareApplicationHome is intentionally idempotent. It is invoked before
// command-line parsing so every process invocation establishes the required
// application home and sessions directory, including invocations that later
// fail usage validation.
func prepareApplicationHome() (*applicationHome, error) {
	rootPath, err := applicationHomePath()
	if err != nil {
		return nil, err
	}
	root, err := platform.AcquirePrivateDirectory(rootPath)
	if err != nil {
		return nil, fmt.Errorf("prepare AgentX home: %w", err)
	}
	sessions, err := root.EnsurePrivateChild("sessions")
	if err != nil {
		return nil, fmt.Errorf("prepare AgentX sessions directory: %w", err)
	}
	result := &applicationHome{
		root:     root,
		sessions: sessions,
		authPath: filepath.Join(root.Path(), config.DefaultAuthFile),
	}
	if err := result.verify(); err != nil {
		return nil, err
	}
	return result, nil
}

func (home *applicationHome) verify() error {
	if home == nil || home.root == nil || home.sessions == nil {
		return errors.New("AgentX home directory identity is unavailable")
	}
	if err := home.root.Verify(); err != nil {
		return fmt.Errorf("verify AgentX home directory: %w", err)
	}
	if err := home.sessions.Verify(); err != nil {
		return fmt.Errorf("verify AgentX sessions directory: %w", err)
	}
	if filepath.Clean(home.sessions.Path()) != filepath.Join(home.root.Path(), "sessions") {
		return errors.New("AgentX sessions path does not match its owned directory")
	}
	if filepath.Clean(home.authPath) != filepath.Join(home.root.Path(), config.DefaultAuthFile) {
		return errors.New("AgentX auth path does not match its owned directory")
	}
	return nil
}

func (home *applicationHome) requireAuthFile() error {
	if err := home.verify(); err != nil {
		return err
	}
	root, err := home.root.OpenRoot()
	if err != nil {
		return fmt.Errorf("open AgentX home for authentication: %w", err)
	}
	gateErr := config.RequireAuthFileAtRoot(root, home.authPath)
	closeErr := root.Close()
	verifyErr := home.verify()
	return errors.Join(gateErr, closeErr, verifyErr)
}

func (home *applicationHome) loadRuntimeConfig(environ []string, overrides config.Overrides) (config.Runtime, error) {
	if err := home.verify(); err != nil {
		return config.Runtime{}, err
	}
	root, err := home.root.OpenRoot()
	if err != nil {
		return config.Runtime{}, fmt.Errorf("open AgentX home for authentication: %w", err)
	}
	runtimeConfig, loadErr := config.LoadAtRoot(root, home.authPath, environ, overrides)
	closeErr := root.Close()
	verifyErr := home.verify()
	return runtimeConfig, errors.Join(loadErr, closeErr, verifyErr)
}

func (home *applicationHome) protectedPaths(additional []string) []string {
	paths := make([]string, 0, len(additional)+2)
	if home != nil && home.root != nil {
		// Protect the entire dynamically named home even when an override puts
		// it inside the workspace. Keep authPath separately so file-identity
		// matching can also catch a hard-link alias of the credential.
		paths = append(paths, home.root.Path())
	}
	if home != nil && home.authPath != "" {
		paths = append(paths, home.authPath)
	}
	return append(paths, additional...)
}
