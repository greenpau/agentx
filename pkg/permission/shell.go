package permission

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const maxShellSegments = 50

var readOnlyCommands = map[string]struct{}{
	"pwd": {}, "ls": {}, "find": {}, "cat": {}, "head": {}, "tail": {},
	"sort": {}, "uniq": {}, "wc": {}, "cut": {}, "paste": {}, "tr": {},
	"file": {}, "stat": {}, "diff": {}, "grep": {}, "rg": {}, "git": {},
	"jq": {}, "printf": {}, "echo": {}, "which": {}, "type": {},
}

var mutationCommands = map[string]struct{}{
	"rm": {}, "rmdir": {}, "mv": {}, "cp": {}, "mkdir": {}, "touch": {},
	"chmod": {}, "chown": {}, "ln": {}, "sed": {}, "tee": {}, "install": {},
	"dd": {}, "truncate": {}, "patch": {}, "git": {},
}

var pathReadingCommands = map[string]struct{}{
	"ls": {}, "find": {}, "cat": {}, "head": {}, "tail": {}, "sort": {},
	"uniq": {}, "wc": {}, "cut": {}, "paste": {}, "file": {}, "stat": {},
	"diff": {}, "grep": {}, "rg": {}, "jq": {},
}

// ShellAnalysis is a conservative, side-effect-free projection of a command.
// Unsupported grammar requires review and never inherits read-only status.
type ShellAnalysis struct {
	Command         string       `json:"command"`
	Segments        []string     `json:"segments"`
	DenyCandidates  []string     `json:"deny_candidates,omitempty"`
	AllowCandidates []string     `json:"allow_candidates,omitempty"`
	Paths           []PathAccess `json:"paths,omitempty"`
	RemovalTargets  []string     `json:"removal_targets,omitempty"`
	ReadOnly        bool         `json:"read_only"`
	SafeConcurrent  bool         `json:"safe_concurrent"`
	RequiresReview  bool         `json:"requires_review"`
	ReviewReason    string       `json:"review_reason,omitempty"`
	Dangerous       bool         `json:"dangerous"`
	DangerReason    string       `json:"danger_reason,omitempty"`
}

// AnalyzeShell proves only a deliberately small Bash subset. Complex grammar
// remains executable after explicit approval but is never auto-authorized.
func AnalyzeShell(command, workingDirectory string) (ShellAnalysis, error) {
	if len(command) > maximumShellCommandBytes ||
		len(workingDirectory) > maximumPermissionTextBytes ||
		strings.ContainsRune(command, '\x00') {
		return ShellAnalysis{}, errors.New("shell command exceeds its analysis boundary")
	}
	analysis := ShellAnalysis{Command: command}
	if strings.TrimSpace(command) == "" {
		return analysis, errors.New("shell command is empty")
	}
	segments, complex, err := splitShell(command)
	if err != nil {
		return analysis, err
	}
	if len(segments) > maxShellSegments {
		return analysis, fmt.Errorf("shell command exceeds %d analyzable segments", maxShellSegments)
	}
	analysis.Segments = segments
	analysis.ReadOnly = !complex
	if complex {
		analysis.RequiresReview = true
		analysis.ReviewReason = "unsupported shell expansion or compound grammar"
	}
	for _, segment := range segments {
		analysis.AllowCandidates = append(analysis.AllowCandidates, segment)
		words, safe := shellWords(segment)
		if !safe || len(words) == 0 {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "command arguments cannot be proven inert"
			}
			continue
		}
		rawWords := words
		denyWords := stripLeadingAssignments(rawWords, false)
		if normalized, complete := stripSafeWrappers(denyWords); complete {
			denyWords = normalized
		}
		if len(denyWords) > 0 {
			analysis.DenyCandidates = append(analysis.DenyCandidates, strings.Join(denyWords, " "))
		}

		allowWords := stripLeadingAssignments(rawWords, true)
		if normalized, complete := stripSafeWrappers(allowWords); complete {
			allowWords = normalized
		} else {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "shell wrapper grammar requires explicit review"
			}
		}
		if len(allowWords) > 0 {
			analysis.AllowCandidates[len(analysis.AllowCandidates)-1] = strings.Join(allowWords, " ")
		}

		words = stripLeadingAssignments(rawWords, false)
		if hasUnsafeLeadingAssignment(rawWords) {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "execution-affecting environment assignment requires explicit review"
			}
		}
		if normalized, complete := stripSafeWrappers(words); complete {
			words = normalized
		} else {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "shell wrapper grammar requires explicit review"
			}
		}
		if normalized, resolutionChanging := unwrapResolutionChangingCommand(words); resolutionChanging {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "command resolution or evaluation requires explicit review"
			}
			words = normalized
		}
		if len(words) == 0 {
			analysis.ReadOnly = false
			continue
		}
		commandWord := words[0]
		name := commandWord
		// A path-qualified executable is not the reviewed built-in command. For
		// example, ./cat and /tmp/git may be arbitrary programs with arbitrary
		// side effects even though their basenames look familiar.
		pathQualified := !staticAssignment(commandWord) && strings.ContainsAny(commandWord, `/\\`)
		if pathQualified {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "path-qualified executable cannot be proven read-only"
			}
			// Retain conservative mutation and dangerous-removal analysis for
			// familiar basenames, but never use it to grant read-only status.
			name = filepath.Base(commandWord)
		}
		if pathQualified {
			analysis.ReadOnly = false
		} else if _, ok := readOnlyCommands[name]; !ok || !readOnlyInvocation(name, words[1:]) {
			analysis.ReadOnly = false
		}
		if _, mutating := mutationCommands[name]; mutating {
			paths, requiresReview, reason := extractShellPaths(name, words[1:], workingDirectory)
			analysis.Paths = append(analysis.Paths, paths...)
			if requiresReview {
				analysis.ReadOnly = false
				analysis.RequiresReview = true
				if analysis.ReviewReason == "" {
					analysis.ReviewReason = reason
				}
			}
		}
		if _, readsPaths := pathReadingCommands[name]; readsPaths {
			readPaths, ambiguousAttachedOption := extractShellReadPaths(words[1:], workingDirectory)
			analysis.Paths = append(analysis.Paths, readPaths...)
			if ambiguousAttachedOption {
				analysis.ReadOnly = false
				analysis.RequiresReview = true
				if analysis.ReviewReason == "" {
					analysis.ReviewReason = "attached shell option operand cannot be safely separated"
				}
			}
		}
		outputPaths, incompleteOutput := extractExplicitOutputPaths(name, words[1:], workingDirectory)
		if len(outputPaths) > 0 || incompleteOutput {
			analysis.ReadOnly = false
			analysis.Paths = append(analysis.Paths, outputPaths...)
			if incompleteOutput {
				analysis.RequiresReview = true
				if analysis.ReviewReason == "" {
					analysis.ReviewReason = "output option is missing a statically analyzable file operand"
				}
			}
		}
		if name == "rm" || name == "rmdir" {
			for _, word := range words[1:] {
				if strings.HasPrefix(word, "-") {
					continue
				}
				analysis.RemovalTargets = append(analysis.RemovalTargets, word)
				if DangerousRemoval(word, workingDirectory) {
					analysis.Dangerous = true
					analysis.DangerReason = "broad or ambiguous removal target"
				}
			}
		}
		if strings.Count(segment, ">")+strings.Count(segment, "<") > maximumShellRedirections {
			analysis.ReadOnly = false
			analysis.RequiresReview = true
			if analysis.ReviewReason == "" {
				analysis.ReviewReason = "shell redirection count exceeds the analysis boundary"
			}
			continue
		}
		for _, redirect := range redirectionTargets(segment, '>') {
			if redirect == "<dynamic>" {
				analysis.ReadOnly = false
				analysis.RequiresReview = true
				analysis.ReviewReason = "dynamic output redirection target"
			} else if redirect != "/dev/null" {
				analysis.ReadOnly = false
				analysis.Paths = append(analysis.Paths, PathAccess{Path: shellPath(redirect, workingDirectory), Operation: PathWrite})
			}
		}
		for _, redirect := range redirectionTargets(segment, '<') {
			if redirect == "<dynamic>" {
				analysis.ReadOnly = false
				analysis.RequiresReview = true
				analysis.ReviewReason = "dynamic input redirection"
			} else {
				analysis.Paths = append(analysis.Paths, PathAccess{Path: shellPath(redirect, workingDirectory), Operation: PathRead})
			}
		}
	}
	analysis.SafeConcurrent = analysis.ReadOnly && !analysis.RequiresReview
	return analysis, nil
}

func splitShell(command string) ([]string, bool, error) {
	var segments []string
	start := 0
	quote := byte(0)
	escaped := false
	complex := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '`' || (ch == '$' && i+1 < len(command) && (command[i+1] == '(' || command[i+1] == '{')) || ch == '(' || ch == ')' || ch == '{' || ch == '}' {
			complex = true
		}
		// Bash's noclobber override is a single output-redirection operator.
		// Do not split its trailing pipe as a pipeline boundary or the protected
		// destination will disappear from the command segment we authorize.
		if ch == '>' && i+1 < len(command) && command[i+1] == '|' {
			i++
			continue
		}
		if ch == ';' || ch == '\n' || ch == '|' || ch == '&' {
			if ch == '&' && (i+1 >= len(command) || command[i+1] != '&') {
				complex = true
			}
			segment := strings.TrimSpace(command[start:i])
			if segment != "" {
				segments = append(segments, segment)
				if len(segments) > maxShellSegments {
					return nil, false, fmt.Errorf("shell command exceeds %d analyzable segments", maxShellSegments)
				}
			}
			if i+1 < len(command) && command[i+1] == ch {
				i++
			}
			start = i + 1
		}
	}
	if quote != 0 || escaped {
		return nil, false, errors.New("unclosed shell quote or escape")
	}
	if tail := strings.TrimSpace(command[start:]); tail != "" {
		segments = append(segments, tail)
		if len(segments) > maxShellSegments {
			return nil, false, fmt.Errorf("shell command exceeds %d analyzable segments", maxShellSegments)
		}
	}
	if len(segments) == 0 {
		return nil, false, errors.New("shell command has no executable segment")
	}
	return segments, complex, nil
}

func shellWords(segment string) ([]string, bool) {
	var words []string
	var word strings.Builder
	quote := byte(0)
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(segment); i++ {
		ch := segment[i]
		if escaped {
			word.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '$' || ch == '`') {
				return nil, false
			} else {
				word.WriteByte(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if unicode.IsSpace(rune(ch)) {
			flush()
			if len(words) > maximumShellWords {
				return nil, false
			}
			continue
		}
		if strings.ContainsRune("`$(){}", rune(ch)) {
			return nil, false
		}
		word.WriteByte(ch)
	}
	if quote != 0 || escaped {
		return nil, false
	}
	flush()
	if len(words) > maximumShellWords {
		return nil, false
	}
	return words, true
}

func stripLeadingAssignments(words []string, safeOnly bool) []string {
	for len(words) > 0 && staticAssignment(words[0]) && (!safeOnly || safeEnvironmentAssignment(words[0])) {
		words = words[1:]
	}
	return words
}

func hasUnsafeLeadingAssignment(words []string) bool {
	for len(words) > 0 && staticAssignment(words[0]) {
		if !safeEnvironmentAssignment(words[0]) {
			return true
		}
		words = words[1:]
	}
	return false
}

// stripSafeWrappers recognizes only complete, literal wrapper grammars. It
// returns the original unresolved suffix on failure so callers can force
// review without accidentally treating a guessed child as authorization
// evidence.
func stripSafeWrappers(words []string) ([]string, bool) {
	for len(words) > 0 {
		switch words[0] {
		case "nohup":
			index := 1
			if index < len(words) && words[index] == "--" {
				index++
			} else if index < len(words) && strings.HasPrefix(words[index], "-") {
				return words, false
			}
			if index >= len(words) {
				return words, false
			}
			words = words[index:]
		case "time":
			index := 1
			for index < len(words) && (words[index] == "-p" || words[index] == "--") {
				index++
			}
			if index >= len(words) || strings.HasPrefix(words[index], "-") {
				return words, false
			}
			words = words[index:]
		case "nice":
			index := 1
			for index < len(words) {
				option := words[index]
				switch {
				case option == "--":
					index++
					goto niceCommand
				case option == "-n" || option == "--adjustment":
					index += 2
					if index > len(words) {
						return words, false
					}
				case strings.HasPrefix(option, "--adjustment=") && len(option) > len("--adjustment="):
					index++
				case isNiceAdjustment(option):
					index++
				case strings.HasPrefix(option, "-"):
					return words, false
				default:
					goto niceCommand
				}
			}
		niceCommand:
			if index >= len(words) {
				return words, false
			}
			words = words[index:]
		case "timeout":
			index := 1
			for index < len(words) {
				option := words[index]
				switch {
				case option == "--":
					index++
					goto timeoutDuration
				case option == "--preserve-status" || option == "--foreground" ||
					option == "-v" || option == "--verbose":
					index++
				case option == "-k" || option == "--kill-after" ||
					option == "-s" || option == "--signal":
					index += 2
					if index > len(words) {
						return words, false
					}
				case strings.HasPrefix(option, "--kill-after=") && len(option) > len("--kill-after="):
					index++
				case strings.HasPrefix(option, "--signal=") && len(option) > len("--signal="):
					index++
				case strings.HasPrefix(option, "-"):
					return words, false
				default:
					goto timeoutDuration
				}
			}
		timeoutDuration:
			if index+1 >= len(words) {
				return words, false
			}
			words = words[index+1:]
		default:
			return words, true
		}
	}
	return words, false
}

func isNiceAdjustment(value string) bool {
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for _, character := range value[1:] {
		if !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

var resolutionChangingCommands = map[string]struct{}{
	"eval": {}, "source": {}, ".": {}, "exec": {}, "command": {}, "builtin": {},
	"fc": {}, "coproc": {}, "trap": {}, "enable": {}, "mapfile": {}, "readarray": {},
	"hash": {}, "bind": {}, "complete": {}, "compgen": {}, "alias": {}, "env": {},
	"bash": {}, "sh": {}, "dash": {}, "zsh": {}, "ksh": {}, "sudo": {}, "doas": {},
}

// unwrapResolutionChangingCommand surfaces a simple literal child for safety
// analysis while still telling the caller that the submitted invocation must
// receive mandatory review. Unknown option grammar is never guessed.
func unwrapResolutionChangingCommand(words []string) ([]string, bool) {
	if len(words) == 0 {
		return words, false
	}
	if _, changing := resolutionChangingCommands[words[0]]; !changing {
		return words, false
	}
	switch words[0] {
	case "env":
		index := 1
		if index < len(words) && words[index] == "--" {
			index++
		}
		for index < len(words) && staticAssignment(words[index]) {
			index++
		}
		if index < len(words) && !strings.HasPrefix(words[index], "-") {
			return words[index:], true
		}
	case "command", "exec", "sudo", "doas":
		index := 1
		if index < len(words) && words[index] == "--" {
			index++
		}
		if index < len(words) && !strings.HasPrefix(words[index], "-") {
			return words[index:], true
		}
	}
	return words, true
}

var safeShellEnvironment = map[string]struct{}{
	"GOEXPERIMENT": {}, "GOOS": {}, "GOARCH": {}, "CGO_ENABLED": {}, "GO111MODULE": {},
	"RUST_BACKTRACE": {}, "RUST_LOG": {}, "NODE_ENV": {}, "PYTHONUNBUFFERED": {},
	"PYTHONDONTWRITEBYTECODE": {}, "PYTEST_DISABLE_PLUGIN_AUTOLOAD": {}, "PYTEST_DEBUG": {},
	"LANG": {}, "LANGUAGE": {}, "LC_ALL": {}, "LC_CTYPE": {}, "LC_TIME": {}, "CHARSET": {},
	"TERM": {}, "COLORTERM": {}, "NO_COLOR": {}, "FORCE_COLOR": {}, "TZ": {},
}

func safeEnvironmentAssignment(word string) bool {
	index := strings.IndexByte(word, '=')
	if index <= 0 {
		return false
	}
	_, ok := safeShellEnvironment[word[:index]]
	return ok
}

func staticAssignment(word string) bool {
	index := strings.IndexByte(word, '=')
	if index <= 0 {
		return false
	}
	for i, r := range word[:index] {
		if !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return !strings.ContainsAny(word[index+1:], "$`(){};&|")
}

func readOnlyInvocation(name string, args []string) bool {
	if name == "find" {
		for _, arg := range args {
			switch arg {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
				return false
			}
		}
	}
	if name == "sort" || name == "diff" {
		for _, arg := range args {
			if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "-o") && len(arg) > 2 || strings.HasPrefix(arg, "--output=") {
				return false
			}
			if name == "sort" && (arg == "--compress-program" || strings.HasPrefix(arg, "--compress-program=")) {
				return false
			}
		}
	}
	if name == "rg" {
		for _, arg := range args {
			if arg == "--pre" || strings.HasPrefix(arg, "--pre=") || arg == "--hostname-bin" || strings.HasPrefix(arg, "--hostname-bin=") {
				return false
			}
		}
	}
	if name != "git" {
		for _, arg := range args {
			if strings.ContainsAny(arg, "*$`") {
				return false
			}
		}
		return true
	}
	for _, arg := range args {
		if arg == "--output" || strings.HasPrefix(arg, "--output=") || arg == "--ext-diff" || arg == "--textconv" || arg == "--open-files-in-pager" || strings.HasPrefix(arg, "--open-files-in-pager=") {
			return false
		}
	}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "-C" || strings.HasPrefix(args[0], "-c") || strings.HasPrefix(args[0], "--git-dir") || strings.HasPrefix(args[0], "--work-tree") {
			return false
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "status", "diff", "log", "show", "rev-parse", "ls-files", "grep":
		return true
	case "branch":
		return readOnlyGitBranch(args[1:])
	case "remote":
		return readOnlyGitRemote(args[1:])
	default:
		return false
	}
}

// extractExplicitOutputPaths handles the file-producing options shared by
// sort and diff. They are intentionally parsed separately from generic read
// operands so a split option cannot be mistaken for an observational read.
func extractExplicitOutputPaths(name string, args []string, workingDirectory string) ([]PathAccess, bool) {
	if name != "sort" && name != "diff" {
		return nil, false
	}
	var paths []PathAccess
	incomplete := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		value := ""
		switch {
		case argument == "-o" || argument == "--output":
			index++
			if index >= len(args) {
				incomplete = true
				continue
			}
			value = args[index]
		case strings.HasPrefix(argument, "--output="):
			value = strings.TrimPrefix(argument, "--output=")
		case strings.HasPrefix(argument, "-o") && len(argument) > 2:
			value = strings.TrimPrefix(argument, "-o")
		default:
			continue
		}
		if value == "" || value == "-" {
			incomplete = true
			continue
		}
		paths = append(paths, PathAccess{Path: shellPath(value, workingDirectory), Operation: PathWrite})
	}
	return paths, incomplete
}

func readOnlyGitBranch(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--list" || arg == "-l" || arg == "--show-current" || arg == "-a" || arg == "--all" || arg == "-r" || arg == "--remotes" || arg == "-v" || arg == "-vv" || arg == "--merged" || arg == "--no-merged":
		case strings.HasPrefix(arg, "--sort=") || strings.HasPrefix(arg, "--format=") || strings.HasPrefix(arg, "--contains=") || strings.HasPrefix(arg, "--no-contains=") || strings.HasPrefix(arg, "--points-at=") || strings.HasPrefix(arg, "--column="):
		case arg == "--sort" || arg == "--format" || arg == "--contains" || arg == "--no-contains" || arg == "--points-at" || arg == "--column":
			index++
			if index >= len(args) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func readOnlyGitRemote(args []string) bool {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-v" || args[0] == "--verbose") {
		return true
	}
	if args[0] == "get-url" {
		for _, arg := range args[1:] {
			if arg == "--add" || arg == "--delete" {
				return false
			}
		}
		return len(args) >= 2
	}
	return len(args) >= 3 && args[0] == "show" && (args[1] == "-n" || args[1] == "--no-query")
}

func extractShellPaths(name string, args []string, workingDirectory string) ([]PathAccess, bool, string) {
	if name == "git" {
		if readOnlyInvocation(name, args) {
			return nil, false, ""
		}
		return extractGitMutationPaths(args, workingDirectory), true, "non-read-only git invocation requires explicit review"
	}
	switch name {
	case "dd":
		return extractDDPaths(args, workingDirectory), true, "dd file operands require explicit review"
	case "cp":
		paths, targetDirectorySyntax, complete := extractCopyPaths(args, workingDirectory)
		if targetDirectorySyntax || mutationHasOption(args) || !complete {
			return paths, true, "cp option-based destination requires explicit review"
		}
		return paths, false, ""
	case "mv", "install", "ln":
		targetPaths, targetDirectorySyntax, complete := extractCopyPaths(args, workingDirectory)
		if targetDirectorySyntax {
			return targetPaths, true, name + " option-based destination requires explicit review"
		}
		paths := extractGenericMutationPaths(args, workingDirectory)
		if mutationHasOption(args) || !complete {
			return paths, true, name + " option grammar requires explicit review"
		}
		return paths, false, ""
	}
	return extractGenericMutationPaths(args, workingDirectory), false, ""
}

func extractGitMutationPaths(args []string, workingDirectory string) []PathAccess {
	var paths []PathAccess
	endOptions := false
	subcommandSeen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !endOptions && argument == "--" {
			endOptions = true
			continue
		}
		if !endOptions {
			operation := PathWrite
			value := ""
			switch {
			case argument == "-C":
				operation = PathRead
				index++
				if index < len(args) {
					value = args[index]
				}
			case argument == "--file" || argument == "-f" ||
				argument == "--git-dir" || argument == "--work-tree" ||
				argument == "--directory":
				index++
				if index < len(args) {
					value = args[index]
				}
			case strings.HasPrefix(argument, "--file="):
				value = strings.TrimPrefix(argument, "--file=")
			case strings.HasPrefix(argument, "--git-dir="):
				value = strings.TrimPrefix(argument, "--git-dir=")
			case strings.HasPrefix(argument, "--work-tree="):
				value = strings.TrimPrefix(argument, "--work-tree=")
			case strings.HasPrefix(argument, "--directory="):
				value = strings.TrimPrefix(argument, "--directory=")
			case strings.HasPrefix(argument, "-"):
				continue
			}
			if value != "" && value != "-" {
				paths = append(paths, PathAccess{Path: shellPath(value, workingDirectory), Operation: operation})
				continue
			}
		}
		if !subcommandSeen {
			subcommandSeen = true
			continue
		}
		if endOptions || looksLikeShellPathOperand(argument) {
			paths = append(paths, PathAccess{Path: shellPath(argument, workingDirectory), Operation: PathWrite})
		}
	}
	return paths
}

func looksLikeShellPathOperand(value string) bool {
	return value != "" && value != "-" &&
		(strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") ||
			filepath.IsAbs(value) || strings.ContainsAny(value, `/\`))
}

func extractGenericMutationPaths(args []string, workingDirectory string) []PathAccess {
	var paths []PathAccess
	endOptions := false
	for _, arg := range args {
		if !endOptions && arg == "--" {
			endOptions = true
			continue
		}
		if !endOptions && strings.HasPrefix(arg, "-") {
			continue
		}
		paths = append(paths, PathAccess{Path: shellPath(arg, workingDirectory), Operation: PathWrite})
	}
	return paths
}

func mutationHasOption(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if len(arg) > 1 && strings.HasPrefix(arg, "-") {
			return true
		}
	}
	return false
}

func extractDDPaths(args []string, workingDirectory string) []PathAccess {
	var paths []PathAccess
	for _, arg := range args {
		operation := PathOperation("")
		value := ""
		switch {
		case strings.HasPrefix(arg, "if="):
			operation, value = PathRead, strings.TrimPrefix(arg, "if=")
		case strings.HasPrefix(arg, "of="):
			operation, value = PathWrite, strings.TrimPrefix(arg, "of=")
		}
		if operation == "" || value == "" || value == "-" {
			continue
		}
		paths = append(paths, PathAccess{Path: shellPath(value, workingDirectory), Operation: operation})
	}
	return paths
}

// extractCopyPaths understands the ordinary two-operand form and the GNU
// target-directory forms. Target-directory syntax remains review-required
// even after extracting its value: option parsing differs across cp variants,
// so this analyzer never lets partial emulation become authorization proof.
func extractCopyPaths(args []string, workingDirectory string) ([]PathAccess, bool, bool) {
	positionals := make([]string, 0, len(args))
	targetDirectory := ""
	targetDirectorySyntax := false
	complete := true
	endOptions := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !endOptions && arg == "--" {
			endOptions = true
			continue
		}
		if !endOptions {
			switch {
			case arg == "--target-directory" || arg == "-t":
				targetDirectorySyntax = true
				index++
				if index >= len(args) || args[index] == "" {
					complete = false
					continue
				}
				targetDirectory = args[index]
				continue
			case strings.HasPrefix(arg, "--target-directory="):
				targetDirectorySyntax = true
				targetDirectory = strings.TrimPrefix(arg, "--target-directory=")
				if targetDirectory == "" {
					complete = false
				}
				continue
			case strings.HasPrefix(arg, "-t") && len(arg) > 2:
				targetDirectorySyntax = true
				targetDirectory = strings.TrimPrefix(arg, "-t")
				continue
			case strings.HasPrefix(arg, "-"):
				// Unknown/file-bearing option grammars are intentionally not
				// considered complete authorization evidence.
				if strings.Contains(arg, "=") {
					complete = false
				}
				continue
			}
		}
		positionals = append(positionals, arg)
	}

	var paths []PathAccess
	if targetDirectorySyntax {
		if targetDirectory != "" {
			paths = append(paths, PathAccess{Path: shellPath(targetDirectory, workingDirectory), Operation: PathWrite})
		}
		for _, source := range positionals {
			paths = append(paths, PathAccess{Path: shellPath(source, workingDirectory), Operation: PathRead})
		}
		return paths, true, complete && targetDirectory != "" && len(positionals) > 0
	}
	if len(positionals) < 2 {
		complete = false
	}
	for index, operand := range positionals {
		operation := PathRead
		if index == len(positionals)-1 {
			operation = PathWrite
		}
		paths = append(paths, PathAccess{Path: shellPath(operand, workingDirectory), Operation: operation})
	}
	return paths, false, complete
}

func shellPath(path, workingDirectory string) string {
	// Bash expands an unquoted leading tilde against HOME (or another user's
	// home) after authorization. shellWords intentionally does not retain quote
	// provenance, so treating it as workspace-relative would authorize one path
	// and execute another. Preserve the ambiguous spelling: Resolver rejects a
	// leading tilde before any broad allow or bypass decision can run. This also
	// conservatively rejects a quoted literal tilde rather than risk an escape.
	if strings.HasPrefix(path, "~") {
		return path
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDirectory, path)
	}
	return filepath.Clean(path)
}

func extractShellReadPaths(args []string, workingDirectory string) ([]PathAccess, bool) {
	var paths []PathAccess
	ambiguousAttachedOption := false
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--"):
			// Every attached long-option value is conservatively projected as a
			// possible file operand. This intentionally over-approximates options
			// such as --color=auto so an unlisted --schema=.env.production or
			// --files0-from=.env.production can never bypass protected-path policy.
			_, value, attached := strings.Cut(arg, "=")
			if !attached {
				continue
			}
			arg = value
		case strings.HasPrefix(arg, "-"):
			if len(arg) <= 2 {
				continue
			}
			// Short options may be clustered, so no generic parser can prove
			// where an attached operand begins. Project the most likely suffix
			// and require review even in bypass mode.
			ambiguousAttachedOption = true
			arg = strings.TrimPrefix(arg[2:], "=")
		}
		if arg == "" || arg == "-" || arg == "<" || arg == ">" || arg == ">>" {
			continue
		}
		paths = append(paths, PathAccess{Path: shellPath(arg, workingDirectory), Operation: PathRead})
	}
	return paths, ambiguousAttachedOption
}

func redirectionTargets(segment string, operator byte) []string {
	var targets []string
	quote := byte(0)
	escaped := false
	for i := 0; i < len(segment); i++ {
		ch := segment[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if ch == '\'' || ch == '"' {
			if quote == ch {
				quote = 0
			} else if quote == 0 {
				quote = ch
			}
			continue
		}
		if quote != 0 || ch != operator {
			continue
		}
		repeated := 1
		for i+1 < len(segment) && segment[i+1] == operator {
			i++
			repeated++
		}
		rest := strings.TrimSpace(segment[i+1:])
		if operator == '>' && repeated == 1 && strings.HasPrefix(rest, "|") {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "|"))
		} else if strings.HasPrefix(rest, "|") {
			targets = append(targets, "<dynamic>")
			continue
		}
		if repeated > 1 && operator == '<' || strings.HasPrefix(rest, "&") || strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "<") {
			targets = append(targets, "<dynamic>")
			continue
		}
		words, ok := shellWords(rest)
		if ok && len(words) > 0 && !strings.ContainsAny(words[0], "$`*?") {
			targets = append(targets, words[0])
			continue
		}
		targets = append(targets, "<dynamic>")
	}
	return targets
}
