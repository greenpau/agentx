// Package testing contains capabilities that exist only in the explicit test
// runtime profile. Nothing in this package is registered in production.
package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/greenpau/agentx/pkg/permission"
	"github.com/greenpau/agentx/pkg/tool"
)

const (
	PermissionToolName = "TestingPermission"
	testEnvironmentKey = "NODE_ENV"
)

// EnvironmentEnabled reports whether an immutable environment snapshot
// explicitly selects the test profile. Conflicting duplicate definitions fail
// closed so a production value cannot be hidden by ordering tricks.
func EnvironmentEnabled(environment []string) bool {
	found := false
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(key, testEnvironmentKey) {
			continue
		}
		if value != "test" {
			return false
		}
		found = true
	}
	return found
}

// PermissionDescriptor returns the testing-only capability. Its enablement is
// bound to the supplied environment snapshot, it accepts exactly {}, and it
// always crosses the ordinary approval boundary before returning success.
func PermissionDescriptor(environment []string) tool.Descriptor {
	enabled := EnvironmentEnabled(append([]string(nil), environment...))
	return tool.Descriptor{
		Name:        PermissionToolName,
		Source:      tool.SourceBuiltin,
		Description: "Test tool that always asks for permission before executing. Used for end-to-end testing.",
		Enabled:     func() bool { return enabled },
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		},
		Validate: validateEmptyObject,
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		ProjectPermission: func(_ any, raw json.RawMessage) (permission.Request, error) {
			return permission.Request{Input: append(json.RawMessage(nil), raw...), MandatoryAsk: "Run test?"}, nil
		},
		Call: func(context.Context, tool.CallContext, any) (tool.Output, error) {
			return tool.Output{Content: PermissionToolName + " executed successfully"}, nil
		},
		MaxResultChars: 100_000,
	}
}

func validateEmptyObject(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("input must be an empty JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("input contains multiple JSON values")
		}
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 0 {
		return nil, errors.New("input must be an empty JSON object")
	}
	return struct{}{}, nil
}
