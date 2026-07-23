package mcp

import (
	"encoding/json"
	"testing"
)

func TestValidateToolInputEnforcesAdvertisedObjectSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"name", "count"},
		"properties": map[string]any{
			"name":  map[string]any{"type": "string", "minLength": json.Number("2")},
			"count": map[string]any{"type": "integer", "minimum": json.Number("1")},
		},
	}
	if err := ValidateToolInput(schema, map[string]any{"name": "ok", "count": json.Number("2")}); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	invalid := []map[string]any{
		{"name": "ok"},
		{"name": "x", "count": json.Number("2")},
		{"name": "ok", "count": json.Number("0")},
		{"name": "ok", "count": json.Number("2"), "extra": true},
	}
	for _, value := range invalid {
		if err := ValidateToolInput(schema, value); err == nil {
			t.Fatalf("invalid input passed schema: %#v", value)
		}
	}
}

func TestValidateToolInputFailsClosedForReferences(t *testing.T) {
	if err := ValidateToolInput(map[string]any{"type": "object", "$ref": "https://example.test/schema"}, map[string]any{}); err == nil {
		t.Fatal("unresolvable reference was silently ignored")
	}
}

func TestValidateToolInputFailsClosedForUnsupportedAssertions(t *testing.T) {
	if err := ValidateToolInput(map[string]any{"type": "object", "unevaluatedProperties": false}, map[string]any{}); err == nil {
		t.Fatal("unsupported schema assertion was silently ignored")
	}
}

func TestValidateToolDescriptorOmitsUnsupportedSchemaAtDiscovery(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","unevaluatedProperties":false}`)
	if err := validateToolDescriptor(ToolDescriptor{Name: "unsupported", InputSchema: raw}); err == nil {
		t.Fatal("descriptor with unenforceable schema survived discovery validation")
	}
}

func TestValidateToolInputPreservesExactLargeJSONNumbers(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "integer", "const": json.Number("9007199254740992")},
		},
		"required": []any{"value"},
	}
	if err := ValidateToolInput(schema, map[string]any{"value": json.Number("9007199254740993")}); err == nil {
		t.Fatal("distinct integers above 2^53 collapsed during schema validation")
	}
	if err := ValidateToolInput(schema, map[string]any{"value": json.Number("9007199254740992")}); err != nil {
		t.Fatalf("exact large integer was rejected: %v", err)
	}
}
