// Package childenv owns the environment boundary for non-model child
// processes. Process environments are secret-bearing input: callers select a
// narrow profile rather than inheriting os.Environ implicitly.
package childenv

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maximumEnvironmentBytes = 1 << 20
	maximumEnvironmentItems = 512
	maximumNameBytes        = 256
	maximumValueBytes       = 1 << 20
	maximumSecretBytes      = 2 << 20
	maximumSecretItems      = 256
)

type environmentValue struct {
	name  string
	value string
}

type secretSet []string

var shellNames = map[string]bool{
	"GOEXPERIMENT": true, "GOOS": true, "GOARCH": true, "CGO_ENABLED": true, "GO111MODULE": true,
	"HOME": true, "USERPROFILE": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true, "GOTOOLCHAIN": true,
	"VIRTUAL_ENV": true, "CONDA_PREFIX": true, "CARGO_HOME": true, "RUSTUP_HOME": true,
	"SDKROOT": true, "DEVELOPER_DIR": true, "JAVA_HOME": true, "ANDROID_HOME": true,
	"ANDROID_SDK_ROOT": true, "GRADLE_USER_HOME": true,
	"RUST_BACKTRACE": true, "RUST_LOG": true,
	"NODE_ENV": true, "PYTHONUNBUFFERED": true, "PYTHONDONTWRITEBYTECODE": true,
	"PYTEST_DISABLE_PLUGIN_AUTOLOAD": true, "PYTEST_DEBUG": true,
	"LANG": true, "LANGUAGE": true, "LC_ALL": true, "LC_CTYPE": true, "LC_TIME": true,
	"CHARSET": true, "TERM": true, "COLORTERM": true, "NO_COLOR": true, "FORCE_COLOR": true,
	"TZ": true, "LS_COLORS": true, "LSCOLORS": true, "GREP_COLOR": true, "GREP_COLORS": true,
	"GCC_COLORS": true, "TIME_STYLE": true, "BLOCK_SIZE": true, "BLOCKSIZE": true,
	"SYSTEMROOT": true,
}

var hookBaseNames = map[string]bool{
	"HOME": true, "USERPROFILE": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"LANG": true, "LANGUAGE": true, "LC_ALL": true, "LC_CTYPE": true, "LC_TIME": true,
	"TERM": true, "COLORTERM": true, "NO_COLOR": true, "SYSTEMROOT": true,
}

var processBaseNames = map[string]bool{
	"HOME": true, "USERPROFILE": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"LANG": true, "LANGUAGE": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true,
	"SYSTEMROOT": true,
}

var directoryNames = map[string]bool{
	"HOME": true, "USERPROFILE": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOROOT": true, "VIRTUAL_ENV": true,
	"CONDA_PREFIX": true, "CARGO_HOME": true, "RUSTUP_HOME": true, "SDKROOT": true,
	"DEVELOPER_DIR": true, "JAVA_HOME": true, "ANDROID_HOME": true,
	"ANDROID_SDK_ROOT": true, "GRADLE_USER_HOME": true, "SYSTEMROOT": true,
}

// SensitiveName recognizes ordinary credential-bearing process variables. It
// is deliberately broader than the model-only predicate used for explicitly
// configured MCP credentials.
func SensitiveName(name string) bool {
	if name == "" || len(name) > maximumNameBytes {
		return true
	}
	upper := strings.ToUpper(name)
	for _, fragment := range []string{
		"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "AUTH",
		"COOKIE", "CREDENTIAL", "PRIVATE_KEY", "SUBSCRIPTION_KEY", "CONNECTION_STRING",
		"PROXY",
	} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	if strings.HasSuffix(upper, "_KEY") || strings.HasSuffix(upper, "_URL") ||
		strings.HasSuffix(upper, "_URI") || strings.HasSuffix(upper, "_DSN") {
		return true
	}
	switch upper {
	case "KUBECONFIG", "GOOGLE_APPLICATION_CREDENTIALS", "AWS_SHARED_CREDENTIALS_FILE",
		"AWS_ACCESS_KEY_ID", "AWS_WEB_IDENTITY_TOKEN_FILE", "NETRC", "GIT_ASKPASS",
		"SSH_ASKPASS", "DOCKER_CONFIG", "DOCKER_HOST", "DBUS_SESSION_BUS_ADDRESS",
		"GPG_AGENT_INFO", "XDG_RUNTIME_DIR", "NPM_CONFIG_USERCONFIG",
		"DOTENV_CONFIG_PATH", "CODEX_HOME", "AGENTX_CONFIG_DIR", "GNUPGHOME":
		return true
	default:
		return false
	}
}

// ModelCredentialName recognizes credentials that may authorize model or
// cloud-provider inference. MCP may deliberately receive a provider-specific
// credential of its own, but it must never receive one of these host-model
// credentials through expansion, an environment key, or a renamed value.
func ModelCredentialName(name string) bool {
	if name == "" || len(name) > maximumNameBytes {
		return true
	}
	upper := strings.ToUpper(name)
	if (strings.HasPrefix(upper, "AZURE_OPENAI_") || strings.HasPrefix(upper, "OPENAI_") ||
		strings.HasPrefix(upper, "AGENTX_")) && SensitiveName(upper) {
		return true
	}
	switch upper {
	case "AZURE_OPENAI_SUBSCRIPTION_KEY",
		"AZURE_OPENAI_API_KEY",
		"OPENAI_API_KEY",
		"AGENTX_API_KEY",
		"AGENTX_AUTH_TOKEN",
		"AGENTX_OAUTH_TOKEN",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SESSION_TOKEN",
		"AWS_SECURITY_TOKEN",
		"AWS_SHARED_CREDENTIALS_FILE",
		"AWS_CONFIG_FILE",
		"AWS_PROFILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
		"AWS_ROLE_ARN",
		"AGENTX_FOUNDRY_API_KEY",
		"AZURE_CLIENT_SECRET",
		"AZURE_USERNAME",
		"AZURE_PASSWORD",
		"AZURE_CLIENT_CERTIFICATE_PATH",
		"AZURE_CLIENT_CERTIFICATE_PASSWORD",
		"AZURE_FEDERATED_TOKEN_FILE",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_GHA_CREDS_PATH",
		"GOOGLE_OAUTH_ACCESS_TOKEN",
		"GOOGLE_CLOUD_ACCESS_TOKEN",
		"GOOGLE_API_KEY",
		"GCP_ACCESS_TOKEN",
		"CLOUDSDK_AUTH_ACCESS_TOKEN",
		"CLOUDSDK_CONFIG":
		return true
	default:
		return false
	}
}

// NonSecretMap returns a frozen environment snapshot with credential-named
// entries removed. It also removes benignly named aliases whose value equals
// or contains a credential value from the same source snapshot.
func NonSecretMap(environment []string) map[string]string {
	if !validEnvironmentInput(environment) {
		return map[string]string{}
	}
	values := parse(environment)
	secrets, ok := secretsFromEnvironment(environment, SensitiveName)
	if !ok {
		return map[string]string{}
	}
	result := make(map[string]string)
	for canonical, item := range values {
		if SensitiveName(canonical) || secrets.contains(item.name) ||
			secrets.contains(item.value) || secrets.contains(item.name+"="+item.value) {
			continue
		}
		result[item.name] = item.value
	}
	return result
}

// Shell constructs the closed environment used by foreground and background
// shell tools. Unknown variables, credential aliases, startup hooks, exported
// functions, proxy/auth sockets, and loader/interpreter injection controls are
// absent. PATH always starts with controlled system directories and admits only
// existing absolute ambient directories afterward.
func Shell(environment []string) []string {
	if !validEnvironmentInput(environment) {
		return []string{}
	}
	values := parse(environment)
	secrets, ok := secretsFromEnvironment(environment, SensitiveName)
	if !ok {
		return []string{}
	}
	selected := make(map[string]string)
	for canonical, item := range values {
		if canonical == "PATH" || !shellNames[canonical] || SensitiveName(canonical) || secrets.contains(item.value) {
			continue
		}
		if normalized, ok := safeProfileValue(canonical, item.value, secrets); ok {
			selected[canonical] = normalized
		}
	}
	ambientPath := ""
	if item, ok := values["PATH"]; ok && !secrets.contains(item.value) {
		ambientPath = item.value
	}
	selected["PATH"] = executablePath(ambientPath, valueOf(values, "SYSTEMROOT"), secrets)
	return profileSerialize(selected, secrets)
}

// Hook constructs a hook command environment from the frozen session snapshot.
// Baseline locale/path variables are inherited automatically. Other values
// require an explicit hook allow entry. fixed contains runtime-owned variables
// such as the project and plugin roots.
func Hook(candidates map[string]string, allowed map[string]bool, fixed map[string]string) ([]string, error) {
	if !validEnvironmentMap(candidates) || !validEnvironmentMap(fixed) {
		return nil, errors.New("invalid hook environment snapshot")
	}
	values := parseMap(candidates)
	secrets, ok := secretsFromMap(candidates, SensitiveName)
	if !ok {
		return nil, errors.New("hook environment credential scope exceeds its limit")
	}
	selected := make(map[string]string)
	for canonical, item := range values {
		if canonical == "PATH" || SensitiveName(canonical) || secrets.contains(item.value) {
			continue
		}
		if hookBaseNames[canonical] {
			if normalized, ok := safeProfileValue(canonical, item.value, secrets); ok {
				selected[canonical] = normalized
			}
			continue
		}
		if allowed[item.name] || allowed[canonical] {
			normalized := stripLineControls(item.value)
			if normalized != item.value || !safeEnvironmentText(normalized) || secrets.contains(normalized) {
				continue
			}
			selected[item.name] = normalized
		}
	}
	ambientPath := ""
	if item, ok := values["PATH"]; ok && !secrets.contains(item.value) {
		ambientPath = item.value
	}
	selected["PATH"] = executablePath(ambientPath, valueOf(values, "SYSTEMROOT"), secrets)
	for name, value := range fixed {
		if !validName(name) || !safeEnvironmentText(value) || len(value) > maximumValueBytes {
			return nil, errors.New("invalid runtime-owned hook environment")
		}
		if ModelCredentialName(name) || secrets.contains(value) {
			return nil, errors.New("runtime-owned hook environment contains model credential material")
		}
		selected[name] = value
	}
	return serializeWithoutSecrets(selected, secrets)
}

// MCP constructs a stdio server environment. A server receives a minimal
// ambient baseline plus its explicitly configured values. Explicit MCP-owned
// credentials remain supported, while host model credentials and renamed
// aliases of those credentials fail closed.
func MCP(environment []string, explicit map[string]string) ([]string, error) {
	if !validEnvironmentInput(environment) || !validEnvironmentMap(explicit) {
		return nil, errors.New("invalid ambient MCP child environment")
	}
	values := parse(environment)
	modelSecrets, ok := secretsFromEnvironment(environment, ModelCredentialName)
	if !ok {
		return nil, errors.New("ambient MCP credential scope exceeds its limit")
	}
	selected := make(map[string]string)
	for _, name := range []string{"HOME", "USERPROFILE", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT"} {
		if item, ok := values[name]; ok && !modelSecrets.contains(item.value) {
			if normalized, valid := safeProfileValue(name, item.value, modelSecrets); valid {
				selected[name] = normalized
			}
		}
	}
	ambientPath := ""
	if item, ok := values["PATH"]; ok && !modelSecrets.contains(item.value) {
		ambientPath = item.value
	}
	selected["PATH"] = executablePath(ambientPath, valueOf(values, "SYSTEMROOT"), modelSecrets)
	for name, value := range explicit {
		if !validName(name) || !safeEnvironmentText(value) || len(value) > maximumValueBytes {
			return nil, errors.New("invalid explicit MCP child environment")
		}
		if ModelCredentialName(name) || modelSecrets.contains(value) {
			return nil, errors.New("MCP child environment cannot receive host model credentials")
		}
		selected[name] = value
	}
	return serializeWithoutSecrets(selected, modelSecrets)
}

// Process constructs the environment for a generic argv-based platform
// helper. Explicit overlays may carry capability-specific values, but model
// credentials and renamed aliases are rejected even when ambient inheritance
// is disabled. The returned slice is always non-nil on success.
func Process(environment []string, inherit bool, overlay map[string]string) ([]string, error) {
	if !validEnvironmentInput(environment) || !validEnvironmentMap(overlay) {
		return nil, errors.New("invalid ambient child process environment")
	}
	values := parse(environment)
	modelSecrets, ok := secretsFromEnvironment(environment, ModelCredentialName)
	if !ok {
		return nil, errors.New("ambient child credential scope exceeds its limit")
	}
	selected := make(map[string]string)
	if inherit {
		for name := range processBaseNames {
			if item, ok := values[name]; ok && !modelSecrets.contains(item.value) {
				if normalized, valid := safeProfileValue(name, item.value, modelSecrets); valid {
					selected[name] = normalized
				}
			}
		}
		ambientPath := ""
		if item, ok := values["PATH"]; ok && !modelSecrets.contains(item.value) {
			ambientPath = item.value
		}
		selected["PATH"] = executablePath(ambientPath, valueOf(values, "SYSTEMROOT"), modelSecrets)
	}
	for name, value := range overlay {
		if !validName(name) || !safeEnvironmentText(value) || len(value) > maximumValueBytes {
			return nil, errors.New("invalid explicit process environment")
		}
		if ModelCredentialName(name) || modelSecrets.contains(value) {
			return nil, errors.New("child process cannot receive host model credentials")
		}
		selected[name] = value
	}
	result, err := serializeWithoutSecrets(selected, modelSecrets)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return []string{}, nil
	}
	return result, nil
}

// MCPExpansionLookup returns a bounded lookup view for non-shell MCP config
// expansion. Host model credential names and renamed aliases are unavailable.
func MCPExpansionLookup(environment []string) func(string) (string, bool) {
	if !validEnvironmentInput(environment) {
		return func(string) (string, bool) { return "", false }
	}
	values := parse(environment)
	modelSecrets, ok := secretsFromEnvironment(environment, ModelCredentialName)
	if !ok {
		return func(string) (string, bool) { return "", false }
	}
	return func(name string) (string, bool) {
		if !validName(name) || ModelCredentialName(name) {
			return "", false
		}
		item, ok := values[strings.ToUpper(name)]
		if !ok || modelSecrets.contains(item.value) {
			return "", false
		}
		return item.value, true
	}
}

// Git returns a hermetic-enough environment for read-only repository context.
// It excludes host credentials and user/system Git configuration, disables
// credential prompts, and avoids repository lock writes.
func Git(environment []string) []string {
	if !validEnvironmentInput(environment) {
		return []string{}
	}
	values := parse(environment)
	secrets, ok := secretsFromEnvironment(environment, SensitiveName)
	if !ok {
		return []string{}
	}
	selected := map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_COUNT":    "2",
		"GIT_CONFIG_KEY_0":    "core.fsmonitor",
		"GIT_CONFIG_VALUE_0":  "false",
		"GIT_CONFIG_KEY_1":    "core.hooksPath",
		"GIT_CONFIG_VALUE_1":  os.DevNull,
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for _, name := range []string{"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT"} {
		if item, ok := values[name]; ok && !secrets.contains(item.value) {
			if normalized, valid := safeProfileValue(name, item.value, secrets); valid {
				selected[name] = normalized
			}
		}
	}
	ambientPath := ""
	if item, ok := values["PATH"]; ok && !secrets.contains(item.value) {
		ambientPath = item.value
	}
	selected["PATH"] = executablePath(ambientPath, valueOf(values, "SYSTEMROOT"), secrets)
	return profileSerialize(selected, secrets)
}

// Directories returns canonical directory targets from selected path-list
// variables without retaining credential aliases. A target may not exist yet
// (cache directories are commonly created by the child), but its nearest
// existing ancestor is resolved and filesystem roots are rejected.
func Directories(environment []string, names ...string) []string {
	if !validEnvironmentInput(environment) {
		return nil
	}
	values := parse(environment)
	secrets, ok := secretsFromEnvironment(environment, SensitiveName)
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, requested := range names {
		item, ok := values[strings.ToUpper(requested)]
		if !ok || secrets.contains(item.value) {
			continue
		}
		for _, candidate := range filepath.SplitList(item.value) {
			if normalized, valid := directoryTarget(candidate); valid &&
				!secrets.contains(normalized) && !seen[normalized] {
				seen[normalized] = true
				result = append(result, normalized)
			}
		}
	}
	sort.Strings(result)
	return result
}

func directoryTarget(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	root := string(os.PathSeparator)
	if volume != "" {
		root = volume + string(os.PathSeparator)
	}
	if samePath(clean, root) {
		return "", false
	}
	existing := clean
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", false
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(existing, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false
	}
	target := filepath.Clean(filepath.Join(resolved, relative))
	if samePath(target, root) {
		return "", false
	}
	return target, true
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func parse(environment []string) map[string]environmentValue {
	values := make(map[string]environmentValue)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validName(name) || strings.ContainsRune(value, 0) ||
			!utf8.ValidString(value) || len(value) > maximumValueBytes {
			continue
		}
		values[strings.ToUpper(name)] = environmentValue{name: name, value: value}
	}
	return values
}

func parseMap(environment map[string]string) map[string]environmentValue {
	keys := make([]string, 0, len(environment))
	for name := range environment {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	values := make(map[string]environmentValue)
	for _, name := range keys {
		value := environment[name]
		if !validName(name) || strings.ContainsRune(value, 0) ||
			!utf8.ValidString(value) || len(value) > maximumValueBytes {
			continue
		}
		values[strings.ToUpper(name)] = environmentValue{name: name, value: value}
	}
	return values
}

func secretsFromEnvironment(environment []string, predicate func(string) bool) (secretSet, bool) {
	seen := make(map[string]bool)
	var result secretSet
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validName(name) || strings.ContainsRune(value, 0) ||
			!utf8.ValidString(value) || len(value) > maximumValueBytes || !predicate(name) || value == "" {
			continue
		}
		if !appendSecret(&result, seen, value) {
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result, true
}

func secretsFromMap(environment map[string]string, predicate func(string) bool) (secretSet, bool) {
	seen := make(map[string]bool)
	var result secretSet
	for name, value := range environment {
		if !validName(name) || strings.ContainsRune(value, 0) ||
			!utf8.ValidString(value) || len(value) > maximumValueBytes || !predicate(name) || value == "" {
			continue
		}
		if !appendSecret(&result, seen, value) {
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i]) > len(result[j]) })
	return result, true
}

func appendSecret(result *secretSet, seen map[string]bool, value string) bool {
	candidates := []string{value, strings.TrimSpace(value)}
	for _, candidate := range append([]string(nil), candidates...) {
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		clean := filepath.Clean(candidate)
		candidates = append(candidates, clean)
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			candidates = append(candidates, resolved)
		}
	}
	total := 0
	for _, current := range *result {
		total += len(current)
	}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		if len(*result)+1 > maximumSecretItems || total+len(candidate) > maximumSecretBytes {
			return false
		}
		seen[candidate] = true
		*result = append(*result, candidate)
		total += len(candidate)
	}
	return true
}

func (secrets secretSet) contains(value string) bool {
	for _, secret := range secrets {
		if strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func validName(name string) bool {
	if name == "" || len(name) > maximumNameBytes ||
		!(name[0] == '_' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if !(value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9') {
			return false
		}
	}
	return true
}

func validEnvironmentInput(environment []string) bool {
	if len(environment) > maximumEnvironmentItems {
		return false
	}
	seen := make(map[string]string, len(environment))
	total := 0
	for _, entry := range environment {
		total += len(entry) + 1
		if total > maximumEnvironmentBytes {
			return false
		}
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if !validName(name) {
			if name == "" && strings.HasPrefix(entry, "=") {
				// Windows environment blocks may carry drive-current-directory
				// pseudo entries such as =C:=...; they are never inherited.
				continue
			}
			if SensitiveName(name) || ModelCredentialName(name) {
				return false
			}
			continue
		}
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) || len(value) > maximumValueBytes {
			return false
		}
		canonical := strings.ToUpper(name)
		if previous, duplicate := seen[canonical]; duplicate && previous != name {
			return false
		}
		seen[canonical] = name
	}
	return true
}

func validEnvironmentMap(environment map[string]string) bool {
	if len(environment) > maximumEnvironmentItems {
		return false
	}
	seen := make(map[string]struct{}, len(environment))
	total := 0
	for name, value := range environment {
		total += len(name) + len(value) + 2
		if total > maximumEnvironmentBytes || !validName(name) ||
			strings.ContainsRune(value, 0) || !utf8.ValidString(value) || len(value) > maximumValueBytes {
			return false
		}
		canonical := strings.ToUpper(name)
		if _, duplicate := seen[canonical]; duplicate {
			return false
		}
		seen[canonical] = struct{}{}
	}
	return true
}

func profileValue(name, value string) (string, bool) {
	if value == "" {
		return "", name == "LC_ALL" || name == "LANGUAGE" || name == "NO_COLOR"
	}
	if directoryNames[name] {
		return existingDirectory(value)
	}
	if name == "GOPATH" {
		var paths []string
		for _, candidate := range filepath.SplitList(value) {
			normalized, ok := existingDirectory(candidate)
			if !ok {
				return "", false
			}
			paths = append(paths, normalized)
		}
		return strings.Join(paths, string(os.PathListSeparator)), len(paths) > 0
	}
	if name == "GOTOOLCHAIN" {
		return value, value == "auto" || value == "local" || strings.HasPrefix(value, "go1.")
	}
	return value, safeEnvironmentText(value) && len(value) <= maximumValueBytes
}

func safeProfileValue(name, value string, secrets secretSet) (string, bool) {
	normalized, ok := profileValue(name, value)
	return normalized, ok && !secrets.contains(normalized)
}

func existingDirectory(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	return clean, err == nil && info.IsDir()
}

func executablePath(ambient, systemRoot string, secrets secretSet) string {
	controlled := []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/local/go/bin"}
	if runtime.GOOS == "windows" {
		controlled = nil
		if systemRoot != "" {
			controlled = append(controlled, filepath.Join(systemRoot, "System32"), systemRoot)
		}
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(controlled)+8)
	for _, candidate := range append(controlled, filepath.SplitList(ambient)...) {
		if normalized, ok := existingDirectory(candidate); ok &&
			!secrets.contains(normalized) && !seen[normalized] {
			seen[normalized] = true
			result = append(result, normalized)
		}
	}
	return strings.Join(result, string(os.PathListSeparator))
}

func valueOf(values map[string]environmentValue, name string) string {
	if item, ok := values[name]; ok {
		return item.value
	}
	return ""
}

func profileSerialize(values map[string]string, secrets secretSet) []string {
	result, err := serializeWithoutSecrets(values, secrets)
	if err == nil && result != nil {
		return result
	}
	// A nil exec.Cmd.Env means ambient inheritance. Keep the fallback explicitly
	// non-nil even if the host has no controlled executable directory.
	fallback, fallbackErr := serializeWithoutSecrets(
		map[string]string{"PATH": executablePath("", "", secrets)},
		secrets,
	)
	if fallbackErr != nil || fallback == nil {
		return []string{}
	}
	return fallback
}

func stripLineControls(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

func safeEnvironmentText(value string) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character) ||
			unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
	}) < 0
}

func serialize(values map[string]string) ([]string, error) {
	if len(values) > maximumEnvironmentItems {
		return nil, errors.New("child environment contains too many entries")
	}
	keys := make([]string, 0, len(values))
	canonical := make(map[string]struct{}, len(values))
	for key, value := range values {
		if !validName(key) || !safeEnvironmentText(value) || len(value) > maximumValueBytes {
			return nil, errors.New("child environment contains an invalid entry")
		}
		folded := strings.ToUpper(key)
		if _, duplicate := canonical[folded]; duplicate {
			return nil, errors.New("child environment contains duplicate variable names")
		}
		canonical[folded] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	total := 0
	for _, key := range keys {
		entry := key + "=" + values[key]
		total += len(entry) + 1
		if total > maximumEnvironmentBytes {
			return nil, errors.New("child environment exceeds byte limit")
		}
		result = append(result, entry)
	}
	return result, nil
}

func serializeWithoutSecrets(values map[string]string, secrets secretSet) ([]string, error) {
	result, err := serialize(values)
	if err != nil {
		return nil, err
	}
	for _, entry := range result {
		if secrets.contains(entry) {
			return nil, errors.New("child environment projection contains credential material")
		}
	}
	if secrets.contains(strings.Join(result, "\x00")) {
		return nil, errors.New("child environment projection contains credential material")
	}
	return result, nil
}
