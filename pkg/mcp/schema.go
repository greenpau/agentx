package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// ValidateToolInput applies the enforceable JSON Schema subset used at the MCP
// trust boundary. Unsupported reference-based schemas fail closed instead of
// forwarding input that was only syntactically decoded.
func ValidateToolInput(schema map[string]any, value any) error {
	if err := ValidateToolSchema(schema); err != nil {
		return err
	}
	return validateSchemaValue(schema, value, "$", 0)
}

// ValidateToolSchema proves at discovery time that the complete advertised
// schema belongs to the subset this runtime can enforce. Invalid or unsupported
// descriptors are omitted before they can enter the model-callable registry.
func ValidateToolSchema(schema map[string]any) error {
	if schema == nil {
		return errors.New("MCP tool schema is empty")
	}
	return validateSchemaDefinition(schema, "$", 0)
}

func validateSchemaDefinition(schema map[string]any, path string, depth int) error {
	if depth > 32 {
		return errors.New("MCP tool schema exceeds validation depth")
	}
	if err := validateSchemaKeywords(schema, path); err != nil {
		return err
	}
	if pattern, ok := schema["pattern"].(string); ok {
		if len(pattern) > 4_096 {
			return fmt.Errorf("%s uses an oversized pattern", path)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s uses an invalid pattern", path)
		}
	}
	if multiple, ok := schemaRational(schema["multipleOf"]); ok && multiple.Sign() <= 0 {
		return fmt.Errorf("%s has invalid multipleOf", path)
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range properties {
			if err := validateSchemaDefinition(raw.(map[string]any), path+"."+name, depth+1); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"items", "not", "additionalProperties"} {
		if child, ok := schema[keyword].(map[string]any); ok {
			if err := validateSchemaDefinition(child, path+"."+keyword, depth+1); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := schema[keyword].([]any); ok {
			for index, branch := range branches {
				if err := validateSchemaDefinition(branch.(map[string]any), fmt.Sprintf("%s.%s[%d]", path, keyword, index), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateSchemaValue(schema map[string]any, value any, path string, depth int) error {
	if depth > 32 {
		return errors.New("MCP tool input exceeds schema validation depth")
	}
	if err := validateSchemaKeywords(schema, path); err != nil {
		return err
	}
	if _, exists := schema["$ref"]; exists {
		return fmt.Errorf("%s uses an unsupported schema reference", path)
	}
	if raw, ok := schema["allOf"]; ok {
		branches, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s has invalid allOf", path)
		}
		for _, branch := range branches {
			object, ok := branch.(map[string]any)
			if !ok {
				return fmt.Errorf("%s has invalid allOf branch", path)
			}
			if err := validateSchemaValue(object, value, path, depth+1); err != nil {
				return err
			}
		}
	}
	for keyword, exact := range map[string]bool{"anyOf": false, "oneOf": true} {
		raw, exists := schema[keyword]
		if !exists {
			continue
		}
		branches, ok := raw.([]any)
		if !ok || len(branches) == 0 {
			return fmt.Errorf("%s has invalid %s", path, keyword)
		}
		matches := 0
		for _, branch := range branches {
			object, ok := branch.(map[string]any)
			if ok && validateSchemaValue(object, value, path, depth+1) == nil {
				matches++
			}
		}
		if matches == 0 || exact && matches != 1 {
			return fmt.Errorf("%s does not satisfy %s", path, keyword)
		}
	}
	if raw, exists := schema["not"]; exists {
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s has invalid not schema", path)
		}
		if validateSchemaValue(object, value, path, depth+1) == nil {
			return fmt.Errorf("%s matches forbidden schema", path)
		}
	}
	if raw, exists := schema["const"]; exists && !jsonValueEqual(raw, value) {
		return fmt.Errorf("%s does not equal the required constant", path)
	}
	if raw, exists := schema["enum"]; exists {
		choices, ok := raw.([]any)
		if !ok || len(choices) == 0 {
			return fmt.Errorf("%s has invalid enum", path)
		}
		matched := false
		for _, choice := range choices {
			matched = matched || jsonValueEqual(choice, value)
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	if rawType, exists := schema["type"]; exists && !matchesSchemaType(rawType, value) {
		return fmt.Errorf("%s has the wrong JSON type", path)
	}

	switch typed := value.(type) {
	case map[string]any:
		if err := validateObjectSchema(schema, typed, path, depth); err != nil {
			return err
		}
	case []any:
		if err := validateArraySchema(schema, typed, path, depth); err != nil {
			return err
		}
	case string:
		if err := validateStringSchema(schema, typed, path); err != nil {
			return err
		}
	case json.Number, float64:
		if err := validateNumberSchema(schema, typed, path); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaKeywords(schema map[string]any, path string) error {
	for keyword, raw := range schema {
		switch keyword {
		case "$comment", "title", "description", "default", "examples", "readOnly", "writeOnly", "deprecated":
			// Annotation-only keywords do not change acceptance.
		case "$ref":
			return fmt.Errorf("%s uses an unsupported schema reference", path)
		case "type":
			switch typed := raw.(type) {
			case string:
				if !knownSchemaType(typed) {
					return fmt.Errorf("%s has invalid type", path)
				}
			case []any:
				if len(typed) == 0 {
					return fmt.Errorf("%s has empty type union", path)
				}
				for _, item := range typed {
					name, ok := item.(string)
					if !ok || !knownSchemaType(name) {
						return fmt.Errorf("%s has invalid type union", path)
					}
				}
			default:
				return fmt.Errorf("%s has invalid type", path)
			}
		case "properties":
			properties, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s has invalid properties", path)
			}
			for name, child := range properties {
				if _, ok := child.(map[string]any); !ok {
					return fmt.Errorf("%s.%s has invalid property schema", path, name)
				}
			}
		case "required":
			items, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s has invalid required list", path)
			}
			for _, item := range items {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("%s has invalid required property", path)
				}
			}
		case "additionalProperties":
			switch raw.(type) {
			case bool, map[string]any:
			default:
				return fmt.Errorf("%s has invalid additionalProperties", path)
			}
		case "items", "not":
			if _, ok := raw.(map[string]any); !ok {
				return fmt.Errorf("%s has invalid %s", path, keyword)
			}
		case "allOf", "anyOf", "oneOf":
			items, ok := raw.([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("%s has invalid %s", path, keyword)
			}
			for _, item := range items {
				if _, ok := item.(map[string]any); !ok {
					return fmt.Errorf("%s has invalid %s branch", path, keyword)
				}
			}
		case "enum":
			if items, ok := raw.([]any); !ok || len(items) == 0 {
				return fmt.Errorf("%s has invalid enum", path)
			}
		case "const":
		case "minProperties", "maxProperties", "minItems", "maxItems", "minLength", "maxLength":
			if _, ok := schemaInteger(raw); !ok {
				return fmt.Errorf("%s has invalid %s", path, keyword)
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
			if _, ok := schemaRational(raw); !ok {
				return fmt.Errorf("%s has invalid %s", path, keyword)
			}
		case "uniqueItems":
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("%s has invalid uniqueItems", path)
			}
		case "pattern":
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("%s has invalid pattern", path)
			}
		default:
			return fmt.Errorf("%s uses unsupported schema keyword %q", path, keyword)
		}
	}
	return nil
}

func knownSchemaType(value string) bool {
	switch value {
	case "null", "object", "array", "string", "boolean", "number", "integer":
		return true
	default:
		return false
	}
}

func validateObjectSchema(schema, value map[string]any, path string, depth int) error {
	if minimum, ok := schemaInteger(schema["minProperties"]); ok && len(value) < minimum {
		return fmt.Errorf("%s has too few properties", path)
	}
	if maximum, ok := schemaInteger(schema["maxProperties"]); ok && len(value) > maximum {
		return fmt.Errorf("%s has too many properties", path)
	}
	if raw, exists := schema["required"]; exists {
		required, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s has invalid required list", path)
		}
		for _, item := range required {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s has invalid required property", path)
			}
			if _, exists := value[name]; !exists {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, child := range value {
		rawChild, declared := properties[name]
		if declared {
			childSchema, ok := rawChild.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s has an invalid schema", path, name)
			}
			if err := validateSchemaValue(childSchema, child, path+"."+name, depth+1); err != nil {
				return err
			}
			continue
		}
		switch additional := schema["additionalProperties"].(type) {
		case bool:
			if !additional {
				return fmt.Errorf("%s.%s is not an allowed property", path, name)
			}
		case map[string]any:
			if err := validateSchemaValue(additional, child, path+"."+name, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateArraySchema(schema map[string]any, value []any, path string, depth int) error {
	if minimum, ok := schemaInteger(schema["minItems"]); ok && len(value) < minimum {
		return fmt.Errorf("%s has too few items", path)
	}
	if maximum, ok := schemaInteger(schema["maxItems"]); ok && len(value) > maximum {
		return fmt.Errorf("%s has too many items", path)
	}
	if unique, _ := schema["uniqueItems"].(bool); unique {
		for i := range value {
			for j := 0; j < i; j++ {
				if jsonValueEqual(value[i], value[j]) {
					return fmt.Errorf("%s contains duplicate items", path)
				}
			}
		}
	}
	itemSchema, _ := schema["items"].(map[string]any)
	if itemSchema != nil {
		for index, item := range value {
			if err := validateSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStringSchema(schema map[string]any, value, path string) error {
	length := utf8.RuneCountInString(value)
	if minimum, ok := schemaInteger(schema["minLength"]); ok && length < minimum {
		return fmt.Errorf("%s is shorter than allowed", path)
	}
	if maximum, ok := schemaInteger(schema["maxLength"]); ok && length > maximum {
		return fmt.Errorf("%s is longer than allowed", path)
	}
	if pattern, ok := schema["pattern"].(string); ok {
		if len(pattern) > 4_096 {
			return fmt.Errorf("%s uses an oversized pattern", path)
		}
		expression, err := regexp.Compile(pattern)
		if err != nil || !expression.MatchString(value) {
			return fmt.Errorf("%s does not match the required pattern", path)
		}
	}
	return nil
}

func validateNumberSchema(schema map[string]any, value any, path string) error {
	number, ok := schemaRational(value)
	if !ok {
		return fmt.Errorf("%s is not a finite number", path)
	}
	for keyword, accept := range map[string]func(int) bool{
		"minimum":          func(comparison int) bool { return comparison >= 0 },
		"maximum":          func(comparison int) bool { return comparison <= 0 },
		"exclusiveMinimum": func(comparison int) bool { return comparison > 0 },
		"exclusiveMaximum": func(comparison int) bool { return comparison < 0 },
	} {
		if bound, exists := schemaRational(schema[keyword]); exists && !accept(number.Cmp(bound)) {
			return fmt.Errorf("%s violates %s", path, keyword)
		}
	}
	if multiple, exists := schemaRational(schema["multipleOf"]); exists {
		quotient := new(big.Rat)
		if multiple.Sign() <= 0 || !quotient.Quo(number, multiple).IsInt() {
			return fmt.Errorf("%s violates multipleOf", path)
		}
	}
	return nil
}

func matchesSchemaType(raw any, value any) bool {
	types := []string{}
	switch typed := raw.(type) {
	case string:
		types = append(types, typed)
	case []any:
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return false
			}
			types = append(types, name)
		}
	default:
		return false
	}
	for _, name := range types {
		switch name {
		case "null":
			if value == nil {
				return true
			}
		case "object":
			_, ok := value.(map[string]any)
			if ok {
				return true
			}
		case "array":
			_, ok := value.([]any)
			if ok {
				return true
			}
		case "string":
			_, ok := value.(string)
			if ok {
				return true
			}
		case "boolean":
			_, ok := value.(bool)
			if ok {
				return true
			}
		case "number":
			_, ok := schemaRational(value)
			if ok {
				return true
			}
		case "integer":
			number, ok := schemaRational(value)
			if ok && number.IsInt() {
				return true
			}
		}
	}
	return false
}

func schemaInteger(value any) (int, bool) {
	number, ok := schemaRational(value)
	if !ok || number.Sign() < 0 || !number.IsInt() || !number.Num().IsInt64() {
		return 0, false
	}
	integer := number.Num().Int64()
	if int64(int(integer)) != integer {
		return 0, false
	}
	return int(integer), true
}

func schemaRational(value any) (*big.Rat, bool) {
	text := ""
	switch typed := value.(type) {
	case json.Number:
		text = string(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, false
		}
		text = strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, false
		}
		text = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	default:
		return nil, false
	}
	number, ok := new(big.Rat).SetString(text)
	return number, ok
}

func jsonValueEqual(left, right any) bool {
	leftNumber, leftOK := schemaRational(left)
	rightNumber, rightOK := schemaRational(right)
	if leftOK || rightOK {
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	}
	return reflect.DeepEqual(left, right)
}
