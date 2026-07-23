package mcp

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/greenpau/agentx/pkg/childenv"
)

const maximumExpandedConfigValueBytes = 1 << 20

// ExpandEnvironment expands only $NAME and ${NAME} tokens. It never invokes a
// shell, and a missing value is an explicit configuration error.
func ExpandEnvironment(value string, lookup func(string) (string, bool)) (string, error) {
	return expandEnvironment(value, lookup, nil)
}

func expandEnvironment(value string, lookup func(string) (string, bool), expanded func(string, string)) (string, error) {
	if lookup == nil {
		return "", errors.New("environment lookup is required")
	}
	var result strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '$' {
			result.WriteByte(value[index])
			index++
			continue
		}
		if index+1 < len(value) && value[index+1] == '$' {
			result.WriteByte('$')
			index += 2
			continue
		}
		start := index + 1
		end := start
		braced := start < len(value) && value[start] == '{'
		if braced {
			start++
			end = strings.IndexByte(value[start:], '}')
			if end < 0 {
				return "", errors.New("unterminated MCP environment expansion")
			}
			end += start
		} else {
			for end < len(value) && (value[end] == '_' || unicode.IsLetter(rune(value[end])) || end > start && unicode.IsDigit(rune(value[end]))) {
				end++
			}
		}
		if end == start {
			result.WriteByte('$')
			index++
			continue
		}
		name := value[start:end]
		if !validEnvironmentName(name) {
			return "", errors.New("invalid MCP environment variable name")
		}
		replacement, ok := lookup(name)
		if !ok {
			return "", errors.New("missing MCP environment variable " + name)
		}
		if result.Len()+len(replacement) > maximumExpandedConfigValueBytes {
			return "", errors.New("expanded MCP configuration value exceeds 1 MiB")
		}
		if expanded != nil {
			expanded(name, replacement)
		}
		result.WriteString(replacement)
		if braced {
			index = end + 1
		} else {
			index = end
		}
	}
	if result.Len() > maximumExpandedConfigValueBytes {
		return "", errors.New("expanded MCP configuration value exceeds 1 MiB")
	}
	return result.String(), nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !(name[0] == '_' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {
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

// ExpandConfigEnvironment applies the non-shell expansion grammar to every
// executable stdio field. Map keys are processed in sorted order so a broken
// configuration produces a stable first diagnostic.
func ExpandConfigEnvironment(config Config, lookup func(string) (string, bool)) (Config, error) {
	result := cloneConfig(config)
	recordCredential := func(name, value string) {
		if childenv.SensitiveName(name) && value != "" {
			result.expandedCredentialLiterals = append(result.expandedCredentialLiterals, value)
		}
	}
	var err error
	if result.Command, err = expandEnvironment(result.Command, lookup, recordCredential); err != nil {
		return Config{}, fmt.Errorf("expand command: %w", err)
	}
	for index := range result.Args {
		result.Args[index], err = expandEnvironment(result.Args[index], lookup, recordCredential)
		if err != nil {
			return Config{}, fmt.Errorf("expand argument %d: %w", index, err)
		}
	}
	keys := make([]string, 0, len(result.Env))
	for name := range result.Env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		result.Env[name], err = expandEnvironment(result.Env[name], lookup, recordCredential)
		if err != nil {
			return Config{}, fmt.Errorf("expand environment %s: %w", name, err)
		}
	}
	return result, nil
}
