package config

import (
	"fmt"
	"strings"
)

const (
	maxProcessEnvironmentBytes = 4 << 20
	maxEnvironmentEntryCount   = 4096
	reasoningEffortEnvironment = "AGENTX_REASONING_EFFORT"
)

// reasoningEffortFromEnvironment deliberately recognizes no model-provider
// credential variables. auth.json is the only Azure credential source.
func reasoningEffortFromEnvironment(environ []string) (string, bool, error) {
	if len(environ) > maxEnvironmentEntryCount {
		return "", false, fmt.Errorf("%w: process environment exceeds its entry limit", ErrInvalid)
	}
	totalBytes := 0
	var value string
	present := false
	for _, item := range environ {
		totalBytes += len(item) + 1
		if totalBytes > maxProcessEnvironmentBytes {
			return "", false, fmt.Errorf("%w: process environment exceeds its byte limit", ErrInvalid)
		}
		key, candidate, ok := strings.Cut(item, "=")
		if !ok || !validEnvironmentName(key) || !strings.EqualFold(key, reasoningEffortEnvironment) {
			continue
		}
		if present {
			return "", false, fmt.Errorf("%w: process environment contains duplicate reasoning-effort variables", ErrInvalid)
		}
		value = candidate
		present = true
	}
	return value, present, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !(name[0] == '_' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if !(character == '_' ||
			character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}
