package tool

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/permission"
)

const (
	defaultGlobLimit            = 100
	maximumGlobLimit            = 1_000
	defaultGrepLimit            = 250
	maximumGrepLimit            = 10_000
	maximumSearchFile           = 8 << 20
	maximumSearchEntries        = 100_000
	maximumSearchBytes          = 256 << 20
	maximumCollectedSearchBytes = 256 << 10
)

var (
	errSearchBudget    = errors.New("search work budget reached")
	errSearchSatisfied = errors.New("requested search window collected")
)

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type globMatch struct {
	path    string
	modTime int64
}

// globMatchHeap keeps the worst retained match at index zero: the oldest, or
// for equal timestamps the lexicographically greatest path.
type globMatchHeap []globMatch

func (h globMatchHeap) Len() int { return len(h) }
func (h globMatchHeap) Less(i, j int) bool {
	if h[i].modTime == h[j].modTime {
		return h[i].path > h[j].path
	}
	return h[i].modTime < h[j].modTime
}
func (h globMatchHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *globMatchHeap) Push(value any) { *h = append(*h, value.(globMatch)) }
func (h *globMatchHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func betterGlob(candidate, retained globMatch) bool {
	return candidate.modTime > retained.modTime || candidate.modTime == retained.modTime && candidate.path < retained.path
}

func globDescriptor(workspace string, protectedPaths []string) Descriptor {
	return Descriptor{
		Name: "Glob", Source: SourceBuiltin, Description: "Find files matching a bounded glob, newest first.",
		InputSchema: objectSchema(map[string]any{
			"pattern": stringSchema("Glob pattern, including ** for recursive matching"),
			"path":    stringSchema("Absolute search root; defaults to the workspace"),
			"limit":   integerSchema("Maximum returned paths", 1, maximumGlobLimit),
		}, "pattern"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input globInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.Pattern == "" {
				return nil, errors.New("pattern is required")
			}
			if input.Path == "" {
				input.Path = workspace
			}
			if !filepath.IsAbs(input.Path) {
				return nil, errors.New("path must be absolute")
			}
			if _, err := workspaceRelative(workspace, input.Path); err != nil {
				return nil, err
			}
			if input.Limit == 0 {
				input.Limit = defaultGlobLimit
			}
			if input.Limit < 1 || input.Limit > maximumGlobLimit {
				return nil, errors.New("limit outside supported bounds")
			}
			if _, err := compileGlob(input.Pattern); err != nil {
				return nil, err
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			input := value.(globInput)
			return permission.Request{Input: raw, Paths: []permission.PathAccess{{Path: input.Path, Operation: permission.PathRead}}}, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(globInput)
			searchRoot, err := openWorkspaceDirectory(workspace, input.Path)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open glob root: %v", err)
			}
			defer searchRoot.Close()
			matcher, _ := compileGlob(input.Pattern)
			matchesHeap := &globMatchHeap{}
			heap.Init(matchesHeap)
			matched, entries := 0, 0
			scanLimited := false
			err = fs.WalkDir(searchRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					if entry != nil && entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				entries++
				if entries > maximumSearchEntries {
					scanLimited = true
					return errSearchBudget
				}
				if entry.IsDir() {
					candidate := filepath.Join(input.Path, filepath.FromSlash(strings.TrimPrefix(path, "./")))
					if entry.Type()&os.ModeSymlink != 0 || (path != "." && (skipSearchDirectory(entry.Name()) || permission.IsProtectedPath(candidate, protectedPaths...))) {
						return fs.SkipDir
					}
					return nil
				}
				relative := strings.TrimPrefix(path, "./")
				if permission.IsProtectedPath(filepath.Join(input.Path, filepath.FromSlash(relative)), protectedPaths...) {
					return nil
				}
				if !matcher.MatchString(relative) {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return nil
				}
				candidate := globMatch{path: filepath.Join(input.Path, filepath.FromSlash(relative)), modTime: info.ModTime().UnixNano()}
				matched++
				if matchesHeap.Len() < input.Limit {
					heap.Push(matchesHeap, candidate)
				} else if betterGlob(candidate, (*matchesHeap)[0]) {
					(*matchesHeap)[0] = candidate
					heap.Fix(matchesHeap, 0)
				}
				return nil
			})
			if err != nil && !errors.Is(err, errSearchBudget) {
				return Output{}, invocationError("execution_failed", "glob search: %v", err)
			}
			matches := append([]globMatch(nil), (*matchesHeap)...)
			sort.Slice(matches, func(i, j int) bool {
				if matches[i].modTime == matches[j].modTime {
					return matches[i].path < matches[j].path
				}
				return matches[i].modTime > matches[j].modTime
			})
			truncated := scanLimited || matched > input.Limit
			paths := make([]string, len(matches))
			for i := range matches {
				paths[i] = matches[i].path
			}
			content := strings.Join(paths, "\n")
			if truncated {
				content += fmt.Sprintf("\n[more than %d matches; narrow the pattern]", input.Limit)
			}
			return Output{Content: content, Metadata: map[string]any{"count": len(matches), "truncated": truncated, "scanned_entries": entries, "scan_limited": scanLimited}}, nil
		},
	}
}

func skipSearchDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(pattern)
	var expression strings.Builder
	expression.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					expression.WriteString("(?:.*/)?")
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return nil, errors.New("unterminated glob character class")
			}
			end += i + 1
			expression.WriteString(pattern[i : end+1])
			i = end
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteByte('$')
	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return nil, fmt.Errorf("invalid glob: %w", err)
	}
	return compiled, nil
}

type grepInput struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Glob            string `json:"glob,omitempty"`
	OutputMode      string `json:"output_mode,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	LineNumbers     bool   `json:"line_numbers,omitempty"`
	Offset          int    `json:"offset,omitempty"`
	HeadLimit       *int   `json:"head_limit,omitempty"`
}

func grepDescriptor(workspace string, protectedPaths []string) Descriptor {
	return Descriptor{
		Name: "Grep", Source: SourceBuiltin, Description: "Search text files using a Go regular expression with bounded, paginated results.",
		InputSchema: objectSchema(map[string]any{
			"pattern": stringSchema("Regular expression"), "path": stringSchema("Absolute search root or file"),
			"glob":             stringSchema("Optional file glob"),
			"output_mode":      enumSchema("Result shape", "files_with_matches", "content", "count"),
			"case_insensitive": booleanSchema("Case-insensitive expression"),
			"line_numbers":     booleanSchema("Include line numbers in content mode"),
			"offset":           integerSchema("Skip this many mode-specific results", 0, 1_000_000_000),
			"head_limit":       integerSchema("Maximum results; zero means only common output bounds", 0, maximumGrepLimit),
		}, "pattern"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input grepInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if input.Pattern == "" {
				return nil, errors.New("pattern is required")
			}
			if input.Path == "" {
				input.Path = workspace
			}
			if !filepath.IsAbs(input.Path) {
				return nil, errors.New("path must be absolute")
			}
			if _, err := workspaceRelative(workspace, input.Path); err != nil {
				return nil, err
			}
			if input.OutputMode == "" {
				input.OutputMode = "files_with_matches"
			}
			if input.OutputMode != "files_with_matches" && input.OutputMode != "content" && input.OutputMode != "count" {
				return nil, errors.New("invalid output_mode")
			}
			if input.Offset < 0 || (input.HeadLimit != nil && (*input.HeadLimit < 0 || *input.HeadLimit > maximumGrepLimit)) {
				return nil, errors.New("offset or head_limit outside supported bounds")
			}
			pattern := input.Pattern
			if input.CaseInsensitive {
				pattern = "(?i)" + pattern
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf("invalid regular expression: %w", err)
			}
			if input.Glob != "" {
				if _, err := compileGlob(input.Glob); err != nil {
					return nil, err
				}
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			return permission.Request{Input: raw, Paths: []permission.PathAccess{{Path: value.(grepInput).Path, Operation: permission.PathRead}}}, nil
		},
		Call: func(ctx context.Context, call CallContext, value any) (Output, error) {
			return grepCallWorkspace(ctx, call, workspace, value, protectedPaths)
		},
		MaxResultChars: 20_000,
	}
}

func grepCall(ctx context.Context, _ CallContext, value any) (Output, error) {
	input := value.(grepInput)
	workspace := input.Path
	if info, err := os.Lstat(input.Path); err == nil && !info.IsDir() {
		workspace = filepath.Dir(input.Path)
	}
	return grepCallWorkspace(ctx, CallContext{}, workspace, value, nil)
}

func grepCallWorkspace(ctx context.Context, _ CallContext, workspace string, value any, protectedPaths []string) (Output, error) {
	input := value.(grepInput)
	pattern := input.Pattern
	if input.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	expression := regexp.MustCompile(pattern)
	var glob *regexp.Regexp
	if input.Glob != "" {
		glob, _ = compileGlob(input.Glob)
	}
	limit := defaultGrepLimit
	if input.HeadLimit != nil {
		limit = *input.HeadLimit
	}
	if limit == 0 {
		limit = maximumGrepLimit
	}
	capacity := limit
	if capacity > 128 {
		capacity = 128
	}
	results := make([]string, 0, capacity)
	resultIndex, collectedBytes := 0, 0
	resultLimited, scanLimited := false, false
	appendResult := func(result string) bool {
		if resultIndex < input.Offset {
			resultIndex++
			return false
		}
		resultIndex++
		if len(results) >= limit {
			resultLimited = true
			return true
		}
		remaining := maximumCollectedSearchBytes - collectedBytes
		if remaining <= 0 {
			resultLimited = true
			return true
		}
		if len(result) > remaining {
			result = validUTF8Prefix(result, remaining)
			resultLimited = true
		}
		results = append(results, result)
		collectedBytes += len(result)
		return resultLimited
	}
	rooted, err := openWorkspaceParent(workspace, input.Path, false)
	if err != nil {
		return Output{}, invocationError("execution_failed", "open grep path: %v", err)
	}
	defer rooted.Close()
	rootInfo, statErr := rooted.parent.Lstat(rooted.leaf)
	if statErr != nil || rootInfo.Mode()&os.ModeSymlink != 0 {
		if statErr != nil {
			return Output{}, invocationError("execution_failed", "inspect grep root: %v", statErr)
		}
		return Output{}, invocationError("execution_failed", "grep path cannot be a symlink")
	}
	var searchRoot *os.Root
	walkRoot := "."
	requestedFile := ""
	if rootInfo.IsDir() {
		searchRoot, err = openPinnedDirectory(rooted, rootInfo)
		if err != nil {
			return Output{}, invocationError("execution_failed", "open grep root: %v", err)
		}
		defer searchRoot.Close()
	} else {
		if !rootInfo.Mode().IsRegular() {
			return Output{}, invocationError("execution_failed", "grep path is not a regular file or directory")
		}
		searchRoot = rooted.parent
		walkRoot = filepath.ToSlash(rooted.leaf)
		requestedFile = walkRoot
	}
	entries, scannedBytes := 0, int64(0)
	searchDevice, err := rootDevice(searchRoot)
	if err != nil {
		return Output{}, invocationError("execution_failed", "inspect grep filesystem: %v", err)
	}
	err = fs.WalkDir(searchRoot.FS(), walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maximumSearchEntries {
			scanLimited = true
			return errSearchBudget
		}
		if entry.IsDir() {
			candidate := filepath.Join(input.Path, filepath.FromSlash(path))
			if path != walkRoot && (skipSearchDirectory(entry.Name()) || permission.IsProtectedPath(candidate, protectedPaths...)) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if requestedFile != "" && path != requestedFile {
			return nil
		}
		// A recursive permission for a benign directory never grants access
		// to credential/control files found below it. A caller may still
		// target one protected regular file explicitly, in which case the
		// permission resolver asks for that exact path before this call.
		if requestedFile == "" && permission.IsProtectedPath(filepath.Join(input.Path, filepath.FromSlash(path)), protectedPaths...) {
			return nil
		}
		relative, _ := filepath.Rel(filepath.FromSlash(walkRoot), filepath.FromSlash(path))
		relative = filepath.ToSlash(relative)
		if glob != nil && !glob.MatchString(relative) {
			return nil
		}
		file, err := searchRoot.Open(filepath.FromSlash(path))
		if err != nil {
			return nil
		}
		before, beforeErr := entry.Info()
		info, statErr := file.Stat()
		if beforeErr != nil || statErr != nil || !os.SameFile(before, info) || info.Size() > maximumSearchFile || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil
		}
		links, linkErr := openedFileLinkCount(file, info)
		device, deviceErr := openedFileDevice(file, info)
		if linkErr != nil || deviceErr != nil || links != 1 || device != searchDevice {
			_ = file.Close()
			if requestedFile != "" {
				return errors.New("requested grep file has ambiguous filesystem identity")
			}
			return nil
		}
		if scannedBytes+info.Size() > maximumSearchBytes {
			_ = file.Close()
			scanLimited = true
			return errSearchBudget
		}
		scannedBytes += info.Size()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		line, count := 0, 0
		stop := false
		for scanner.Scan() {
			line++
			if !expression.MatchString(scanner.Text()) {
				continue
			}
			count++
			switch input.OutputMode {
			case "files_with_matches":
				stop = appendResult(grepDisplayPath(input.Path, rootInfo.IsDir(), relative))
			case "content":
				prefix := grepDisplayPath(input.Path, rootInfo.IsDir(), relative)
				if input.LineNumbers {
					prefix += fmt.Sprintf(":%d", line)
				}
				stop = appendResult(prefix + ":" + scanner.Text())
			}
			if stop || input.OutputMode == "files_with_matches" {
				break
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return fmt.Errorf("scan %s: %w", grepDisplayPath(input.Path, rootInfo.IsDir(), relative), scanErr)
		}
		if input.OutputMode == "count" && count > 0 {
			stop = appendResult(fmt.Sprintf("%s:%d", grepDisplayPath(input.Path, rootInfo.IsDir(), relative), count))
		}
		if stop {
			return errSearchSatisfied
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSearchSatisfied) && !errors.Is(err, errSearchBudget) {
		return Output{}, invocationError("execution_failed", "grep search: %v", err)
	}
	truncated := resultLimited || scanLimited
	content := strings.Join(results, "\n")
	if truncated {
		content += "\n[additional matches omitted; increase offset or narrow the pattern]"
	}
	return Output{Content: content, Metadata: map[string]any{"count": len(results), "truncated": truncated, "scanned_entries": entries, "scanned_bytes": scannedBytes, "scan_limited": scanLimited}}, nil
}

func rootDevice(root *os.Root) (uint64, error) {
	file, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return 0, err
	}
	device, err := openedFileDevice(file, info)
	closeErr := file.Close()
	return device, errors.Join(err, closeErr)
}

func grepDisplayPath(inputPath string, directory bool, relative string) string {
	if !directory {
		return inputPath
	}
	return filepath.Join(inputPath, filepath.FromSlash(relative))
}

func openWorkspaceDirectory(workspace, target string) (*os.Root, error) {
	rooted, err := openWorkspaceParent(workspace, target, false)
	if err != nil {
		return nil, err
	}
	defer rooted.Close()
	info, err := rooted.parent.Lstat(rooted.leaf)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("search root is not a real directory")
	}
	return openPinnedDirectory(rooted, info)
}

func openPinnedDirectory(rooted *workspaceParent, expected os.FileInfo) (*os.Root, error) {
	directory, err := rooted.parent.OpenRoot(rooted.leaf)
	if err != nil {
		return nil, err
	}
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("search root changed while it was opened")
	}
	if err := rooted.Verify(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	parentFile, err := rooted.parent.Open(".")
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	parentInfo, err := parentFile.Stat()
	if err != nil {
		_ = parentFile.Close()
		_ = directory.Close()
		return nil, err
	}
	parentDevice, err := openedFileDevice(parentFile, parentInfo)
	closeErr := parentFile.Close()
	if err != nil || closeErr != nil {
		_ = directory.Close()
		return nil, errors.Join(err, closeErr)
	}
	directoryFile, err := directory.Open(".")
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	directoryDevice, err := openedFileDevice(directoryFile, opened)
	closeErr = directoryFile.Close()
	if err != nil || closeErr != nil || directoryDevice != parentDevice {
		_ = directory.Close()
		if err == nil && closeErr == nil {
			err = errors.New("search capability refuses filesystem-boundary crossings")
		}
		return nil, errors.Join(err, closeErr)
	}
	return directory, nil
}

func validUTF8Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	prefix := value[:limit]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}
