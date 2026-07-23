// Package sandbox owns optional operating-system process isolation. Semantic
// authorization remains in permission; this package only narrows an already
// approved process and reports explicit availability when the host cannot.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/greenpau/agentx/pkg/childenv"
)

type State string

const (
	StateAvailable   State = "available"
	StateUnsupported State = "unsupported"
	StateUnavailable State = "unavailable"
)

type Status struct {
	State   State  `json:"state"`
	Backend string `json:"backend,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type Runner struct {
	status  Status
	profile string
}

// Detect performs one bounded, side-effect-free backend probe. Merely finding
// an executable is insufficient: managed hosts can install sandbox-exec while
// denying sandbox initialization.
func Detect(parent context.Context, workspace string, environment []string) *Runner {
	runner := &Runner{status: Status{State: StateUnsupported, Reason: "no supported OS sandbox backend"}}
	if runtime.GOOS != "darwin" {
		return runner
	}
	workspace, ok := sandboxRoot(workspace)
	if !ok {
		runner.status = Status{State: StateUnavailable, Backend: "sandbox-exec", Reason: "workspace cannot be sandboxed safely"}
		return runner
	}
	const executable = "/usr/bin/sandbox-exec"
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		runner.status = Status{State: StateUnavailable, Backend: "sandbox-exec", Reason: "sandbox-exec is not installed"}
		return runner
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, executable, "-p", "(version 1)(allow default)", "/usr/bin/true")
	probe.Env = []string{"PATH=/usr/bin:/bin", "HOME=/var/empty"}
	if err := probe.Run(); err != nil {
		runner.status = Status{State: StateUnavailable, Backend: "sandbox-exec", Reason: "host rejected sandbox initialization"}
		return runner
	}
	runner.status = Status{State: StateAvailable, Backend: "sandbox-exec"}
	runner.profile = macOSProfile(workspace, environment)
	return runner
}

func (r *Runner) Status() Status {
	if r == nil {
		return Status{State: StateUnavailable, Reason: "sandbox adapter was not initialized"}
	}
	return r.status
}

func (r *Runner) Available() bool { return r != nil && r.status.State == StateAvailable }

// Command returns an isolated child when available and an ordinary child when
// unavailable. Callers must reflect unavailability in permission/health state;
// this fallback never changes an ask/deny result into allow.
func (r *Runner) Command(ctx context.Context, program string, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	if r != nil && r.Available() {
		wrapped := make([]string, 0, len(args)+3)
		wrapped = append(wrapped, "-p", r.profile, program)
		wrapped = append(wrapped, args...)
		return exec.CommandContext(ctx, "/usr/bin/sandbox-exec", wrapped...)
	}
	return exec.CommandContext(ctx, program, args...)
}

func macOSProfile(workspace string, environment []string) string {
	roots := map[string]bool{"/tmp": true, "/private/tmp": true}
	if root, ok := sandboxRoot(workspace); ok {
		roots[root] = true
	}
	for _, path := range childenv.Directories(environment,
		"TMPDIR", "TEMP", "TMP", "GOCACHE", "GOMODCACHE", "GOPATH",
		"VIRTUAL_ENV", "CARGO_HOME", "RUSTUP_HOME",
	) {
		// An ambient cache/temp variable that resolves to a filesystem root
		// must not turn the profile's narrow write allowlist into `(subpath
		// "/")`. Canonicalize symlinks and apply the same root rejection used
		// for the workspace.
		if root, ok := sandboxRoot(path); ok {
			roots[root] = true
		}
	}
	ordered := make([]string, 0, len(roots))
	for path := range roots {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var profile strings.Builder
	profile.WriteString("(version 1)(allow default)(deny file-write*)")
	profile.WriteString("(allow file-write* (literal \"/dev/null\") (literal \"/dev/tty\")")
	for _, path := range ordered {
		if strings.ContainsAny(path, "\x00\r\n") {
			continue
		}
		profile.WriteString(" (subpath \"")
		profile.WriteString(strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(path))
		profile.WriteString("\")")
	}
	profile.WriteByte(')')
	return profile.String()
}

func sandboxRoot(path string) (string, bool) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", false
		}
		path = absolute
	}
	path = filepath.Clean(path)
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(canonical)
	}
	volume := filepath.VolumeName(path)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	if path == root {
		return "", false
	}
	return path, true
}

// Format prevents the private sandbox profile, including workspace and cache
// paths, from becoming an incidental logging surface.
func (r *Runner) Format(state fmt.State, _ rune) {
	if r == nil {
		_, _ = io.WriteString(state, "sandbox.Runner<nil>")
		return
	}
	_, _ = io.WriteString(state, "sandbox.Runner{opaque}")
}
