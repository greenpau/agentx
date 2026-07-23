package permission

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var protectedNames = map[string]struct{}{
	".gitconfig": {}, ".gitmodules": {}, ".bashrc": {}, ".bash_profile": {},
	".zshrc": {}, ".zprofile": {}, ".profile": {}, ".ripgreprc": {},
	".mcp.json": {}, ".agentx.json": {}, ".npmrc": {}, ".netrc": {},
	".pypirc": {}, "credentials": {}, "id_rsa": {}, "id_ed25519": {},
}

var protectedDirectories = map[string]struct{}{
	".git": {}, ".vscode": {}, ".idea": {}, ".agentx": {}, ".codex": {},
}

// PathDisposition is the path-specific contribution to permission.
type PathDisposition struct {
	Kind      DecisionKind
	Reason    string
	Lexical   string
	Canonical string
	InScope   bool
	Protected bool
}

// Resolver performs lexical and symlink-aware authorization.
type Resolver struct {
	workspace string
	roots     []string
	home      string
	protected []string
}

// NewResolver canonicalizes all configured roots once per permission context.
func NewResolver(workspace string, additional []string, protectedPaths ...string) (*Resolver, error) {
	if len(additional) > maximumPermissionProjectionItem ||
		len(protectedPaths) > maximumPermissionProjectionItem {
		return nil, errors.New("permission path count exceeds its limit")
	}
	if strings.TrimSpace(workspace) == "" ||
		len(workspace) > maximumPermissionTextBytes ||
		strings.ContainsAny(workspace, "\x00\r\n") {
		return nil, errors.New("workspace is required")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, errors.New("resolve permission workspace")
	}
	root = filepath.Clean(root)
	roots := []string{root}
	if canonical, resolveErr := resolveExistingPrefix(root); resolveErr == nil && !samePath(canonical, root) {
		roots = append(roots, canonical)
	}
	for _, candidate := range additional {
		if strings.TrimSpace(candidate) == "" ||
			len(candidate) > maximumPermissionTextBytes ||
			strings.ContainsAny(candidate, "\x00\r\n") {
			return nil, errors.New("invalid additional permission directory")
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, errors.New("resolve additional permission directory")
		}
		clean := filepath.Clean(abs)
		roots = append(roots, clean)
		if canonical, resolveErr := resolveExistingPrefix(clean); resolveErr == nil && !samePath(canonical, clean) {
			roots = append(roots, canonical)
		}
	}
	home, _ := os.UserHomeDir()
	protected := make([]string, 0, len(protectedPaths)*2)
	for _, candidate := range protectedPaths {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if len(candidate) > maximumPermissionTextBytes ||
			strings.ContainsAny(candidate, "\x00\r\n") {
			return nil, errors.New("invalid protected permission path")
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		clean := filepath.Clean(candidate)
		protected = append(protected, clean)
		if canonical, resolveErr := resolveExistingPrefix(clean); resolveErr == nil && !samePath(canonical, clean) {
			protected = append(protected, canonical)
		}
	}
	return &Resolver{workspace: root, roots: roots, home: filepath.Clean(home), protected: protected}, nil
}

// Inspect resolves a path and applies scope/protected-resource policy.
func (r *Resolver) Inspect(input string, operation PathOperation, acceptEdits bool) PathDisposition {
	if r == nil {
		return PathDisposition{Kind: DecisionDeny, Reason: "path resolver is unavailable"}
	}
	if len(input) > maximumPermissionTextBytes ||
		(operation != PathRead && operation != PathWrite) {
		return PathDisposition{Kind: DecisionDeny, Reason: "invalid path permission request"}
	}
	if suspiciousPath(input, operation) {
		return PathDisposition{Kind: DecisionDeny, Reason: "ambiguous or unsafe path spelling"}
	}
	abs := input
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.workspace, abs)
	}
	abs = filepath.Clean(abs)
	canonical, err := resolveExistingPrefix(abs)
	if err != nil {
		return PathDisposition{Kind: DecisionDeny, Reason: "cannot safely resolve path", Lexical: abs}
	}
	lexicalInScope := anyContains(r.roots, abs)
	canonicalInScope := anyContains(r.roots, canonical)
	protected := isProtected(abs) || isProtected(canonical) || exactProtectedPath(abs, r.protected) || exactProtectedPath(canonical, r.protected)
	disposition := PathDisposition{Lexical: abs, Canonical: canonical, InScope: lexicalInScope && canonicalInScope, Protected: protected}
	if protected {
		disposition.Kind = DecisionAsk
		disposition.Reason = "path targets protected configuration or executable control data"
		return disposition
	}
	if !disposition.InScope {
		disposition.Kind = DecisionAsk
		disposition.Reason = "path is outside approved working directories"
		return disposition
	}
	if operation == PathRead || (operation == PathWrite && acceptEdits) {
		disposition.Kind = DecisionAllow
		disposition.Reason = "path is within an approved working directory"
		return disposition
	}
	disposition.Kind = DecisionAsk
	disposition.Reason = "workspace mutation requires approval"
	return disposition
}

func suspiciousPath(path string, operation PathOperation) bool {
	if path == "" || strings.ContainsRune(path, '\x00') || strings.HasPrefix(path, "~") || strings.Contains(path, "${") || strings.Contains(path, "$(") || strings.Contains(path, "%") {
		return true
	}
	// Shells expand wildcard operands after authorization. Treat them as
	// ambiguous for reads as well as writes so a broad allow/bypass rule cannot
	// authorize a lexical workspace pattern that later resolves through an
	// in-workspace symlink to an out-of-scope object.
	if strings.ContainsAny(path, "*?[]{}") {
		return true
	}
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(path)
		if strings.HasPrefix(lower, `\\?\`) || strings.HasPrefix(lower, `\\.\`) || strings.Contains(lower, "~1") {
			return true
		}
	}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		if len(part) > 2 && strings.Trim(part, ".") == "" {
			return true
		}
	}
	return false
}

func resolveExistingPrefix(path string) (string, error) {
	current := path
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func anyContains(roots []string, path string) bool {
	for _, root := range roots {
		if pathContains(root, path) {
			return true
		}
	}
	return false
}

func pathContains(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func isProtected(path string) bool {
	slash := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(slash, "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		if _, ok := protectedNames[lower]; ok {
			return true
		}
		if _, ok := protectedDirectories[lower]; ok {
			return true
		}
		// Dotenv tooling uses more than the conventional `.env.<suffix>`
		// spelling: `.envrc`, `.env-production`, and tool-specific variants can
		// all contain live credentials. Protect the complete `.env*` namespace
		// so a naming variation cannot inherit workspace auto-authorization.
		if strings.HasPrefix(lower, ".env") || strings.Contains(lower, "credentials") || strings.HasSuffix(lower, "_rsa") {
			return true
		}
	}
	return false
}

// IsProtectedPath reports whether a path names credential material,
// executable configuration, or another protected control resource. Capability
// implementations that expand one authorized directory into many files (for
// example recursive search) must apply this predicate to every discovered
// descendant; authorizing the directory does not implicitly authorize hidden
// protected children.
func IsProtectedPath(path string, configured ...string) bool {
	if len(path) > maximumPermissionTextBytes ||
		len(configured) > maximumPermissionProjectionItem {
		return true
	}
	for _, candidate := range configured {
		if len(candidate) > maximumPermissionTextBytes {
			return true
		}
	}
	if isProtected(path) {
		return true
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	if exactProtectedPath(abs, configured) {
		return true
	}
	canonical, err := resolveExistingPrefix(abs)
	return err != nil || exactProtectedPath(canonical, configured)
}

func exactProtectedPath(path string, configured []string) bool {
	pathInfo, pathInfoErr := os.Stat(path)
	for _, candidate := range configured {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			candidate = absolute
		}
		candidate = filepath.Clean(candidate)
		if samePath(path, candidate) {
			return true
		}
		// Canonical spelling detects symlinks, while SameFile also detects a
		// regular-file hard-link alias whose pathname has no protected marker.
		if candidateInfo, err := os.Stat(candidate); pathInfoErr == nil && err == nil && os.SameFile(pathInfo, candidateInfo) {
			return true
		}
		if canonical, err := resolveExistingPrefix(candidate); err == nil && samePath(path, canonical) {
			return true
		}
	}
	return false
}

// DangerousRemoval reports broad recursive targets that must never be
// silently authorized. In addition to system boundaries, removing an
// approved root or any of its ancestors is dangerous: deleting an ancestor
// implicitly deletes the approved root even when the literal target is not
// itself inside that root.
//
// approvedRoots is variadic to preserve the original two-argument call while
// allowing an evaluator with additional working directories to apply the same
// invariant to every approved boundary.
func DangerousRemoval(path, workspace string, approvedRoots ...string) bool {
	if path == "" || len(path) > maximumPermissionTextBytes ||
		len(workspace) > maximumPermissionTextBytes ||
		len(approvedRoots) > maximumPermissionProjectionItem ||
		strings.ContainsAny(path, "*?") {
		return true
	}
	for _, root := range approvedRoots {
		if len(root) > maximumPermissionTextBytes {
			return true
		}
	}
	workspace = absoluteClean(workspace, "")
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspace, abs)
	}
	abs = filepath.Clean(abs)
	boundaries := make([]string, 0, len(approvedRoots)+1)
	boundaries = append(boundaries, workspace)
	boundaries = append(boundaries, approvedRoots...)
	for _, boundary := range boundaries {
		boundary = absoluteClean(boundary, workspace)
		if boundary != "" && pathContains(abs, boundary) {
			return true
		}
	}
	volume := filepath.VolumeName(abs)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	home, _ := os.UserHomeDir()
	if samePath(abs, root) {
		return true
	}
	if home != "" && samePath(abs, home) {
		return true
	}
	parent := filepath.Dir(abs)
	return samePath(parent, root) || (home != "" && samePath(parent, home))
}

func absoluteClean(path, relativeTo string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if !filepath.IsAbs(path) && relativeTo != "" {
		path = filepath.Join(relativeTo, path)
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}
