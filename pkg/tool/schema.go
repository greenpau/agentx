package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var decimalCompatibility = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

func decodeStrict(raw json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("input must be a JSON object")
	}
	var generic any
	genericDecoder := json.NewDecoder(bytes.NewReader(raw))
	genericDecoder.UseNumber()
	if err := genericDecoder.Decode(&generic); err != nil {
		return err
	}
	if err := genericDecoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("input contains multiple JSON values")
		}
		return err
	}
	typeOf := reflect.TypeOf(destination)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer {
		return errors.New("decode destination must be a pointer")
	}
	target := typeOf.Elem()
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() == reflect.Struct {
		if _, object := generic.(map[string]any); !object {
			return errors.New("input must be a JSON object")
		}
	}
	generic = repairCompatibleScalars(generic, typeOf.Elem())
	repaired, err := json.Marshal(generic)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(repaired))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("input contains multiple JSON values")
		}
		return err
	}
	return nil
}

func repairCompatibleScalars(value any, target reflect.Type) any {
	for target.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if current, exists := object[name]; exists {
				object[name] = repairCompatibleScalars(current, field.Type)
			}
		}
		return object
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return value
		}
		for index := range items {
			items[index] = repairCompatibleScalars(items[index], target.Elem())
		}
		return items
	case reflect.Bool:
		if text, ok := value.(string); ok && (text == "true" || text == "false") {
			parsed, _ := strconv.ParseBool(text)
			return parsed
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if text, ok := value.(string); ok && decimalCompatibility.MatchString(text) && !strings.Contains(text, ".") {
			return json.Number(text)
		}
	case reflect.Float32, reflect.Float64:
		if text, ok := value.(string); ok && decimalCompatibility.MatchString(text) {
			return json.Number(text)
		}
	}
	return value
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerSchema(description string, minimum, maximum int64) map[string]any {
	return map[string]any{"type": "integer", "description": description, "minimum": minimum, "maximum": maximum}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func enumSchema(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func requireNonempty(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}
