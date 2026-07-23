package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxEnvLineBytes          = 1 << 20
	maxEnvFileBytes          = 4 << 20
	maxEnvironmentEntryCount = 4096
)

// ParseEnv reads a dotenv file without performing shell expansion. Treating
// values as data avoids command substitution and accidental secret exposure.
func ParseEnv(r io.Reader) (values map[string]string, err error) {
	defer func() {
		if recover() != nil {
			values = nil
			err = errors.New("read dotenv: input reader failed")
		}
	}()
	if r == nil {
		return nil, errors.New("read dotenv: input reader is unavailable")
	}

	values = make(map[string]string)
	seen := make(map[string]struct{})
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), maxEnvLineBytes)
	totalBytes := 0
	for line := 1; s.Scan(); line++ {
		text := s.Text()
		totalBytes += len(text) + 1
		if totalBytes > maxEnvFileBytes || len(values) >= maxEnvironmentEntryCount {
			return nil, errors.New("dotenv input exceeds its size or entry limit")
		}
		if line == 1 {
			text = strings.TrimPrefix(text, "\uFEFF")
		}
		text = strings.TrimSuffix(text, "\r")
		if !validEnvSourceLine(text) {
			return nil, fmt.Errorf("dotenv line %d: invalid UTF-8 or unsupported control character", line)
		}
		raw := strings.TrimSpace(text)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		if strings.HasPrefix(raw, "export ") {
			raw = strings.TrimSpace(strings.TrimPrefix(raw, "export "))
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("dotenv line %d: expected KEY=VALUE", line)
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fmt.Errorf("dotenv line %d: invalid variable name", line)
		}
		canonical := strings.ToUpper(key)
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("dotenv line %d: duplicate variable name", line)
		}
		value = strings.TrimSpace(value)
		parsed, err := parseEnvValue(value)
		if err != nil {
			return nil, fmt.Errorf("dotenv line %d: %w", line, err)
		}
		seen[canonical] = struct{}{}
		values[key] = parsed
	}
	if err := s.Err(); err != nil {
		return nil, errors.New("read dotenv: input exceeds its limit or could not be read")
	}
	return values, nil
}

func parseEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '\'', '"':
		quote := value[0]
		// Dotenv double quotes support a deliberately small, predictable escape
		// set. Unknown escapes retain their backslash.
		var b strings.Builder
		for i := 1; i < len(value); i++ {
			if value[i] == quote {
				if err := validateQuotedValueSuffix(value[i+1:]); err != nil {
					return "", err
				}
				return b.String(), nil
			}
			if quote == '\'' || value[i] != '\\' || i+1 >= len(value) {
				b.WriteByte(value[i])
				continue
			}
			i++
			switch value[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\\', '"':
				b.WriteByte(value[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(value[i])
			}
		}
		return "", errors.New("unterminated quoted value")
	default:
		for index := 1; index < len(value); index++ {
			if value[index] == '#' && (value[index-1] == ' ' || value[index-1] == '\t') {
				value = strings.TrimSpace(value[:index])
				break
			}
		}
		return value, nil
	}
}

func validateQuotedValueSuffix(suffix string) error {
	if suffix == "" {
		return nil
	}
	if suffix[0] != ' ' && suffix[0] != '\t' {
		return errors.New("unexpected content after quoted value")
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" || strings.HasPrefix(suffix, "#") {
		return nil
	}
	return errors.New("unexpected content after quoted value")
}

func validEnvSourceLine(line string) bool {
	if !utf8.ValidString(line) || strings.ContainsRune(line, '\uFEFF') {
		return false
	}
	return strings.IndexFunc(line, func(character rune) bool {
		return character != '\t' && (unicode.IsControl(character) ||
			unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp))
	}) < 0
}

func validEnvKey(key string) bool {
	if key == "" || !(key[0] == '_' || key[0] >= 'A' && key[0] <= 'Z' || key[0] >= 'a' && key[0] <= 'z') {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// LoadEnvFile returns a merged view in which process variables take
// precedence. It never mutates the process environment.
func LoadEnvFile(path string, environ []string) (map[string]string, error) {
	process, _, err := parseProcessEnvironment(environ)
	if err != nil {
		return nil, err
	}
	return loadEnvFile(path, process)
}

func loadEnvFile(path string, process map[string]string) (map[string]string, error) {
	merged := make(map[string]string)
	if path != "" {
		before, err := os.Lstat(path)
		if err != nil {
			return nil, errors.New("inspect dotenv file: unavailable")
		}
		if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("dotenv file is not a regular non-symlink file")
		}
		if before.Size() > maxEnvFileBytes {
			return nil, fmt.Errorf("dotenv file exceeds %d bytes", maxEnvFileBytes)
		}
		if !envFilePOSIXPermissionsEnforced {
			return nil, errors.New("dotenv file cannot be used because this platform cannot verify owner-only credential-file access")
		}
		if !envFileModePermitsCredentialUse(before.Mode()) || !envFileOwnerPermitsCredentialUse(before) {
			return nil, errors.New("dotenv file access is not owner-only")
		}
		f, err := openEnvFile(path)
		if err != nil {
			return nil, errors.New("open dotenv file: unavailable or unsafe")
		}
		after, err := f.Stat()
		pathAfterOpen, pathErr := os.Lstat(path)
		if err != nil || pathErr != nil || !stableEnvFile(before, after) ||
			!stableEnvFile(after, pathAfterOpen) || pathAfterOpen.Mode()&os.ModeSymlink != 0 {
			_ = f.Close()
			return nil, errors.New("dotenv file changed while opening")
		}
		links, linkErr := openedEnvFileLinkCount(f, after)
		if linkErr != nil || links != 1 {
			_ = f.Close()
			if linkErr != nil {
				return nil, errors.New("inspect dotenv file link count: unavailable")
			}
			return nil, errors.New("dotenv file must have exactly one filesystem link")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
		middle, middleErr := f.Stat()
		if readErr == nil {
			_, readErr = f.Seek(0, io.SeekStart)
		}
		var confirmation []byte
		if readErr == nil {
			confirmation, readErr = io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
		}
		final, finalErr := f.Stat()
		pathFinal, pathFinalErr := os.Lstat(path)
		var finalLinks uint64
		finalLinkErr := finalErr
		if finalErr == nil {
			finalLinks, finalLinkErr = openedEnvFileLinkCount(f, final)
		}
		closeErr := f.Close()
		if readErr != nil {
			return nil, errors.New("read dotenv file: unavailable")
		}
		if len(data) > maxEnvFileBytes || len(confirmation) > maxEnvFileBytes {
			return nil, fmt.Errorf("dotenv file exceeds %d bytes", maxEnvFileBytes)
		}
		if middleErr != nil || finalErr != nil || pathFinalErr != nil || finalLinkErr != nil ||
			!stableEnvFile(before, middle) || !stableEnvFile(middle, final) ||
			!stableEnvFile(final, pathFinal) || pathFinal.Mode()&os.ModeSymlink != 0 ||
			finalLinks != 1 || final.Size() != int64(len(data)) || !bytes.Equal(data, confirmation) {
			return nil, errors.New("dotenv file changed while reading")
		}
		if closeErr != nil {
			return nil, errors.New("close dotenv file: failed")
		}
		parsed, err := ParseEnv(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parse dotenv file: %w", err)
		}
		for key, value := range parsed {
			merged[key] = value
		}
	}
	for key, value := range process {
		merged[key] = value
	}
	return merged, nil
}

func stableEnvFile(before, after os.FileInfo) bool {
	return before != nil && after != nil &&
		before.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(before, after) &&
		before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) &&
		before.Mode() == after.Mode() &&
		envFileModePermitsCredentialUse(after.Mode()) &&
		envFileOwnerPermitsCredentialUse(after)
}

func parseProcessEnvironment(environ []string) (map[string]string, map[string]bool, error) {
	if len(environ) > maxEnvironmentEntryCount {
		return nil, nil, fmt.Errorf("%w: process environment exceeds its entry limit", ErrInvalid)
	}
	values := make(map[string]string)
	present := make(map[string]bool)
	seen := make(map[string]struct{})
	totalBytes := 0
	for _, item := range environ {
		totalBytes += len(item) + 1
		if totalBytes > maxEnvFileBytes {
			return nil, nil, fmt.Errorf("%w: process environment exceeds its byte limit", ErrInvalid)
		}
		key, value, ok := strings.Cut(item, "=")
		if !ok || !validEnvKey(key) {
			continue
		}
		canonical := strings.ToUpper(key)
		if _, duplicate := seen[canonical]; duplicate {
			return nil, nil, fmt.Errorf("%w: process environment contains duplicate variable names", ErrInvalid)
		}
		seen[canonical] = struct{}{}
		if known := canonicalConfigEnvironmentName(canonical); known != "" {
			key = known
		}
		values[key] = value
		present[key] = true
	}
	return values, present, nil
}

func canonicalConfigEnvironmentName(name string) string {
	switch name {
	case "AZURE_OPENAI_ENDPOINT",
		"AZURE_OPENAI_MODEL_NAME",
		"AZURE_OPENAI_DEPLOYMENT",
		"AZURE_OPENAI_SUBSCRIPTION_KEY",
		"AZURE_OPENAI_API_VERSION",
		"AGENTX_REASONING_EFFORT":
		return name
	default:
		return ""
	}
}
