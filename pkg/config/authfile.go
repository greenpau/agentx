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
)

const (
	authFileSchemaVersion             = 1
	authFileProviderAzureOpenAI       = "azure_openai"
	maxCredentialFileBytes      int64 = 64 << 10
)

type authFileDocument struct {
	Version     int
	Provider    string
	AzureOpenAI azureOpenAIAuth
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
	seen := make(map[string]struct{}, 3)
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
		case "provider":
			if !decodeStrictJSONString(raw, &document.Provider) {
				return authFileDocument{}, invalidAuthFile("auth file provider must be a string")
			}
		case "azure_openai":
			document.AzureOpenAI, err = parseAzureOpenAIAuth(raw)
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
	for _, required := range []string{"version", "provider", "azure_openai"} {
		if _, ok := seen[required]; !ok {
			return authFileDocument{}, invalidAuthFile("auth file is missing a required object member")
		}
	}
	if document.Version != authFileSchemaVersion {
		return authFileDocument{}, invalidAuthFile("auth file uses an unsupported schema version")
	}
	if document.Provider != authFileProviderAzureOpenAI {
		return authFileDocument{}, invalidAuthFile("auth file uses an unsupported provider")
	}
	return document, nil
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
