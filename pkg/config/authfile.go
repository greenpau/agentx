package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/greenpau/agentx/pkg/redact"
)

const (
	authFileSchemaVersion             = 2
	authFileProviderAzureOpenAI       = "azure_openai"
	maxCredentialFileBytes      int64 = 64 << 10
	maxAuthFileProviders              = 32
	// Invalid JSON may contain more api_key members than a valid registry. Keep
	// the diagnostic-protection prepass bounded independently of the schema.
	maxAuthFileCredentialCandidates = 4096
)

type authFileDocument struct {
	Version   int
	Providers []authFileProvider
}

type authFileProvider struct {
	ID           string
	Type         string
	Default      bool
	Capabilities authFileCapabilities
	AzureOpenAI  azureOpenAIAuth
}

type authFileCapabilities struct {
	Reasoning authFileReasoning
}

type authFileReasoning struct {
	Efforts       []string
	DefaultEffort string
}

type azureOpenAIAuth struct {
	Endpoint   string
	Model      string
	Deployment string
	APIKey     string
	APIVersion string
}

type authFileLocation struct {
	root *os.Root
	path string
}

// RequireAuthFile performs the non-reading presence gate used before full
// command-line parsing. Load performs the complete owner, link, identity,
// bounded-read, and schema checks before using any credential.
func RequireAuthFile(path string) error {
	if path == "" {
		path = DefaultAuthFile
	}
	return requireAuthFile(authFileLocation{path: path})
}

// RequireAuthFileAtRoot performs the presence gate through a caller-owned,
// descriptor-pinned application-home root. path is diagnostic-only; the
// actual child is always the literal DefaultAuthFile.
func RequireAuthFileAtRoot(root *os.Root, path string) error {
	if root == nil {
		return errors.New("auth file root is unavailable")
	}
	if path == "" {
		path = DefaultAuthFile
	}
	return requireAuthFile(authFileLocation{root: root, path: path})
}

func requireAuthFile(location authFileLocation) error {
	info, err := location.lstat()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return missingAuthFileError(location.path)
		}
		return authFileSetupError(location.path, "inspect auth file: unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return authFileSetupError(location.path, "auth file must be a direct regular non-symlink file")
	}
	return nil
}

func missingAuthFileError(path string) error {
	return fmt.Errorf("%w: %s", ErrAuthFileMissing, MissingAuthFileDiagnostic(path))
}

func authFileSetupError(path, message string) error {
	return fmt.Errorf("%w: %s\n%s", ErrInvalid, message, MissingAuthFileDiagnostic(path))
}

func loadAuthFile(location authFileLocation) (authFileDocument, error) {
	data, err := readCredentialFile(location)
	if err != nil {
		return authFileDocument{}, err
	}
	defer clear(data)
	return parseAuthFile(data)
}

// readCredentialFile retains the file-descriptor and filesystem-identity
// guarantees around the complete read. Errors intentionally omit the selected
// path and all file contents.
func readCredentialFile(location authFileLocation) ([]byte, error) {
	before, err := location.lstat()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingAuthFileError(location.path)
		}
		return nil, errors.New("inspect auth file: unavailable")
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("auth file is not a regular non-symlink file")
	}
	if before.Size() > maxCredentialFileBytes {
		return nil, fmt.Errorf("auth file exceeds %d bytes", maxCredentialFileBytes)
	}
	if !credentialFileAccessControlVerified {
		return nil, errors.New("auth file cannot be used because this platform cannot verify owner-only credential-file access")
	}
	if !credentialFileModePermitsUse(before.Mode()) || !credentialFileOwnerPermitsUse(before) {
		return nil, errors.New("auth file access is not owner-only")
	}

	file, err := openCredentialFile(location.root, DefaultAuthFile, location.path)
	if err != nil {
		return nil, errors.New("open auth file: unavailable or unsafe")
	}
	after, statErr := file.Stat()
	pathAfterOpen, pathErr := location.lstat()
	if statErr != nil || pathErr != nil || !stableCredentialFile(before, after) ||
		!stableCredentialFile(after, pathAfterOpen) || pathAfterOpen.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, errors.New("auth file changed while opening")
	}
	links, linkErr := openedCredentialFileLinkCount(file, after)
	if linkErr != nil || links != 1 {
		_ = file.Close()
		if linkErr != nil {
			return nil, errors.New("inspect auth file link count: unavailable")
		}
		return nil, errors.New("auth file must have exactly one filesystem link")
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	middle, middleErr := file.Stat()
	if readErr == nil {
		_, readErr = file.Seek(0, io.SeekStart)
	}
	var confirmation []byte
	defer func() {
		clear(confirmation)
	}()
	if readErr == nil {
		confirmation, readErr = io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	}
	final, finalErr := file.Stat()
	pathFinal, pathFinalErr := location.lstat()
	var finalLinks uint64
	finalLinkErr := finalErr
	if finalErr == nil {
		finalLinks, finalLinkErr = openedCredentialFileLinkCount(file, final)
	}
	closeErr := file.Close()
	if readErr != nil {
		return nil, errors.New("read auth file: unavailable")
	}
	if int64(len(data)) > maxCredentialFileBytes || int64(len(confirmation)) > maxCredentialFileBytes {
		return nil, fmt.Errorf("auth file exceeds %d bytes", maxCredentialFileBytes)
	}
	if middleErr != nil || finalErr != nil || pathFinalErr != nil || finalLinkErr != nil ||
		!stableCredentialFile(before, middle) || !stableCredentialFile(middle, final) ||
		!stableCredentialFile(final, pathFinal) || pathFinal.Mode()&os.ModeSymlink != 0 ||
		finalLinks != 1 || final.Size() != int64(len(data)) || !bytes.Equal(data, confirmation) {
		return nil, errors.New("auth file changed while reading")
	}
	if closeErr != nil {
		return nil, errors.New("close auth file: failed")
	}
	return data, nil
}

func (location authFileLocation) lstat() (os.FileInfo, error) {
	if location.root != nil {
		return location.root.Lstat(DefaultAuthFile)
	}
	return os.Lstat(location.path)
}

func stableCredentialFile(before, after os.FileInfo) bool {
	return before != nil && after != nil &&
		before.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(before, after) &&
		before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) &&
		before.Mode() == after.Mode() &&
		credentialFileModePermitsUse(after.Mode()) &&
		credentialFileOwnerPermitsUse(after)
}

func parseAuthFile(data []byte) (authFileDocument, error) {
	if len(data) == 0 || int64(len(data)) > maxCredentialFileBytes || !utf8.Valid(data) {
		return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
	}
	candidates, bounded := collectAuthFileCredentialCandidates(data)
	if !bounded {
		// No partial set is safe: an omitted candidate could occur in any later
		// diagnostic framing. Preserve only the error category.
		return authFileDocument{}, &credentialSafeConfigError{}
	}
	document, err := parseAuthFileDocument(data)
	if err != nil {
		return authFileDocument{}, protectConfigurationError(redact.New(candidates...), err)
	}
	return document, nil
}

// collectAuthFileCredentialCandidates walks JSON tokens without recursion and
// records every string-valued api_key member, including duplicate or misplaced
// members. Strict validation still owns acceptance; this prepass exists only
// so a later parser diagnostic cannot collide with already observable secret
// material. A malformed tail ends collection because no later token is
// structurally observable to the decoder.
func collectAuthFileCredentialCandidates(data []byte) ([]string, bool) {
	type container struct {
		kind         json.Delim
		expectingKey bool
		captureValue bool
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	stack := make([]container, 0, 8)
	candidates := make([]string, 0, maxAuthFileProviders)
	completeParentValue := func() {
		if len(stack) == 0 {
			return
		}
		parent := &stack[len(stack)-1]
		if parent.kind == '{' && !parent.expectingKey {
			parent.expectingKey = true
			parent.captureValue = false
		}
	}

	for {
		token, err := decoder.Token()
		if err != nil {
			return candidates, true
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				stack = append(stack, container{kind: value, expectingKey: true})
			case '[':
				stack = append(stack, container{kind: value})
			case '}', ']':
				if len(stack) == 0 {
					return candidates, true
				}
				stack = stack[:len(stack)-1]
				completeParentValue()
			}
		case string:
			if len(stack) > 0 {
				current := &stack[len(stack)-1]
				if current.kind == '{' && current.expectingKey {
					current.expectingKey = false
					current.captureValue = value == "api_key"
					continue
				}
				if current.kind == '{' && current.captureValue {
					if len(candidates) >= maxAuthFileCredentialCandidates {
						return nil, false
					}
					candidates = append(candidates, value)
				}
			}
			completeParentValue()
		default:
			completeParentValue()
		}
	}
}

func parseAuthFileDocument(data []byte) (authFileDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return authFileDocument{}, invalidAuthFile("auth file must contain one JSON object")
	}

	var document authFileDocument
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
		}
		key, ok := token.(string)
		if !ok {
			return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return authFileDocument{}, invalidAuthFile("auth file contains a duplicate object member")
		}
		seen[key] = struct{}{}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
		}
		switch key {
		case "version":
			if err := json.Unmarshal(raw, &document.Version); err != nil {
				return authFileDocument{}, invalidAuthFile("auth file version must be an integer")
			}
		case "providers":
			document.Providers, err = parseAuthFileProviders(raw)
			if err != nil {
				return authFileDocument{}, err
			}
		default:
			return authFileDocument{}, invalidAuthFile("auth file contains an unsupported object member")
		}
	}
	if token, err = decoder.Token(); err != nil {
		return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != '}' {
		return authFileDocument{}, invalidAuthFile("auth file contains malformed JSON")
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return authFileDocument{}, invalidAuthFile("auth file contains trailing JSON data")
	}
	for _, required := range []string{"version", "providers"} {
		if _, ok := seen[required]; !ok {
			return authFileDocument{}, invalidAuthFile("auth file is missing a required object member")
		}
	}
	if document.Version != authFileSchemaVersion {
		return authFileDocument{}, invalidAuthFile("auth file uses an unsupported schema version")
	}
	if len(document.Providers) == 0 {
		return authFileDocument{}, invalidAuthFile("auth file providers must contain at least one provider")
	}
	return document, nil
}

func parseAuthFileProviders(data []byte) ([]authFileProvider, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, invalidAuthFile("auth file providers contain malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return nil, invalidAuthFile("auth file providers must be an array")
	}
	providers := make([]authFileProvider, 0)
	for decoder.More() {
		if len(providers) >= maxAuthFileProviders {
			return nil, invalidAuthFile(fmt.Sprintf("auth file providers exceed the limit of %d", maxAuthFileProviders))
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, invalidAuthFile("auth file providers contain malformed JSON")
		}
		provider, err := parseAuthFileProvider(raw)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	if token, err = decoder.Token(); err != nil {
		return nil, invalidAuthFile("auth file providers contain malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != ']' {
		return nil, invalidAuthFile("auth file providers contain malformed JSON")
	}
	if len(providers) == 0 {
		return nil, invalidAuthFile("auth file providers must contain at least one provider")
	}
	return providers, nil
}

func parseAuthFileProvider(data []byte) (authFileProvider, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return authFileProvider{}, invalidAuthFile("auth file provider entries must be objects")
	}

	var provider authFileProvider
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
		}
		key, ok := token.(string)
		if !ok {
			return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return authFileProvider{}, invalidAuthFile("auth file contains a duplicate object member")
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
		}
		switch key {
		case "id":
			if !decodeStrictJSONString(raw, &provider.ID) {
				return authFileProvider{}, invalidAuthFile("auth file provider id must be a string")
			}
		case "type":
			if !decodeStrictJSONString(raw, &provider.Type) {
				return authFileProvider{}, invalidAuthFile("auth file provider type must be a string")
			}
		case "default":
			trimmed := bytes.TrimSpace(raw)
			if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
				return authFileProvider{}, invalidAuthFile("auth file provider default must be a boolean")
			}
			provider.Default = bytes.Equal(trimmed, []byte("true"))
		case "capabilities":
			provider.Capabilities, err = parseAuthFileCapabilities(raw)
			if err != nil {
				return authFileProvider{}, err
			}
		case "azure_openai":
			provider.AzureOpenAI, err = parseAzureOpenAIAuth(raw)
			if err != nil {
				return authFileProvider{}, err
			}
		default:
			return authFileProvider{}, invalidAuthFile("auth file contains an unsupported object member")
		}
	}
	if token, err = decoder.Token(); err != nil {
		return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != '}' {
		return authFileProvider{}, invalidAuthFile("auth file provider entry contains malformed JSON")
	}
	for _, required := range []string{"id", "type", "capabilities", "azure_openai"} {
		if _, ok := seen[required]; !ok {
			return authFileProvider{}, invalidAuthFile("auth file provider entry is missing a required object member")
		}
	}
	if provider.Type != authFileProviderAzureOpenAI {
		return authFileProvider{}, invalidAuthFile("auth file uses an unsupported provider type")
	}
	return provider, nil
}

func parseAuthFileCapabilities(data []byte) (authFileCapabilities, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return authFileCapabilities{}, invalidAuthFile("auth file capabilities must be an object")
	}
	var capabilities authFileCapabilities
	seen := make(map[string]struct{}, 1)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
		}
		key, ok := token.(string)
		if !ok {
			return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return authFileCapabilities{}, invalidAuthFile("auth file contains a duplicate object member")
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
		}
		if key != "reasoning" {
			return authFileCapabilities{}, invalidAuthFile("auth file contains an unsupported object member")
		}
		capabilities.Reasoning, err = parseAuthFileReasoning(raw)
		if err != nil {
			return authFileCapabilities{}, err
		}
	}
	if token, err = decoder.Token(); err != nil {
		return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != '}' {
		return authFileCapabilities{}, invalidAuthFile("auth file capabilities contain malformed JSON")
	}
	if _, ok := seen["reasoning"]; !ok {
		return authFileCapabilities{}, invalidAuthFile("auth file capabilities are missing required reasoning capabilities")
	}
	return capabilities, nil
}

func parseAuthFileReasoning(data []byte) (authFileReasoning, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities must be an object")
	}
	var reasoning authFileReasoning
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
		}
		key, ok := token.(string)
		if !ok {
			return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return authFileReasoning{}, invalidAuthFile("auth file contains a duplicate object member")
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
		}
		switch key {
		case "efforts":
			reasoning.Efforts, err = parseAuthFileStringArray(raw, "reasoning efforts")
			if err != nil {
				return authFileReasoning{}, err
			}
		case "default_effort":
			if !decodeStrictJSONString(raw, &reasoning.DefaultEffort) {
				return authFileReasoning{}, invalidAuthFile("auth file reasoning default_effort must be a string")
			}
		default:
			return authFileReasoning{}, invalidAuthFile("auth file contains an unsupported object member")
		}
	}
	if token, err = decoder.Token(); err != nil {
		return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != '}' {
		return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities contain malformed JSON")
	}
	for _, required := range []string{"efforts", "default_effort"} {
		if _, ok := seen[required]; !ok {
			return authFileReasoning{}, invalidAuthFile("auth file reasoning capabilities are missing a required object member")
		}
	}
	if len(reasoning.Efforts) == 0 {
		return authFileReasoning{}, invalidAuthFile("auth file reasoning efforts must contain at least one effort")
	}
	return reasoning, nil
}

func parseAuthFileStringArray(data []byte, field string) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, invalidAuthFile("auth file " + field + " contain malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return nil, invalidAuthFile("auth file " + field + " must be an array")
	}
	values := make([]string, 0, 6)
	for decoder.More() {
		if len(values) >= 6 {
			return nil, invalidAuthFile("auth file " + field + " exceed their limit")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, invalidAuthFile("auth file " + field + " contain malformed JSON")
		}
		var value string
		if !decodeStrictJSONString(raw, &value) {
			return nil, invalidAuthFile("auth file " + field + " must contain only strings")
		}
		values = append(values, value)
	}
	if token, err = decoder.Token(); err != nil {
		return nil, invalidAuthFile("auth file " + field + " contain malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != ']' {
		return nil, invalidAuthFile("auth file " + field + " contain malformed JSON")
	}
	return values, nil
}

func parseAzureOpenAIAuth(data []byte) (azureOpenAIAuth, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings must be an object")
	}

	var auth azureOpenAIAuth
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
		}
		key, ok := token.(string)
		if !ok {
			return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return azureOpenAIAuth{}, invalidAuthFile("auth file contains a duplicate object member")
		}
		seen[key] = struct{}{}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
		}
		var target *string
		switch key {
		case "endpoint":
			target = &auth.Endpoint
		case "model":
			target = &auth.Model
		case "deployment":
			target = &auth.Deployment
		case "api_key":
			target = &auth.APIKey
		case "api_version":
			target = &auth.APIVersion
		default:
			return azureOpenAIAuth{}, invalidAuthFile("auth file contains an unsupported object member")
		}
		if !decodeStrictJSONString(raw, target) {
			return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI fields must be strings")
		}
	}
	if token, err = decoder.Token(); err != nil {
		return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
	} else if delimiter, ok = token.(json.Delim); !ok || delimiter != '}' {
		return azureOpenAIAuth{}, invalidAuthFile("auth file Azure OpenAI settings contain malformed JSON")
	}
	for _, required := range []string{"endpoint", "model", "deployment", "api_key", "api_version"} {
		if _, ok := seen[required]; !ok {
			return azureOpenAIAuth{}, invalidAuthFile("auth file is missing a required Azure OpenAI field")
		}
	}
	if strings.TrimSpace(auth.Endpoint) == "" ||
		strings.TrimSpace(auth.Model) == "" ||
		strings.TrimSpace(auth.Deployment) == "" ||
		auth.APIKey == "" {
		return azureOpenAIAuth{}, invalidAuthFile("auth file contains an empty required Azure OpenAI field")
	}
	return auth, nil
}

func decodeStrictJSONString(data []byte, target *string) bool {
	data = bytes.TrimSpace(data)
	if target == nil || len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' ||
		!utf8.Valid(data) || !validJSONSurrogateEscapes(data) {
		return false
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return false
	}
	*target = value
	return true
}

func validJSONSurrogateEscapes(data []byte) bool {
	const (
		highSurrogateStart = 0xd800
		highSurrogateEnd   = 0xdbff
		lowSurrogateStart  = 0xdc00
		lowSurrogateEnd    = 0xdfff
	)
	for index := 0; index < len(data); index++ {
		if data[index] != '\\' {
			continue
		}
		index++
		if index >= len(data) || data[index] != 'u' {
			continue
		}
		value, ok := decodeFourHex(data, index+1)
		if !ok {
			return false
		}
		index += 4
		switch {
		case value >= lowSurrogateStart && value <= lowSurrogateEnd:
			return false
		case value < highSurrogateStart || value > highSurrogateEnd:
			continue
		}
		if index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
			return false
		}
		low, ok := decodeFourHex(data, index+3)
		if !ok || low < lowSurrogateStart || low > lowSurrogateEnd {
			return false
		}
		index += 6
	}
	return true
}

func decodeFourHex(data []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, character := range data[start : start+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func invalidAuthFile(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, message)
}
