package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// JSONContains reports whether any decoded JSON string, object key, or scalar
// spelling contains a configured literal. Matching the decoded value closes
// wire aliases such as \uXXXX without constructing a redacted document.
func (s *Set) JSONContains(raw []byte) (bool, error) {
	if s.Contains(string(raw)) {
		return true, nil
	}
	value, err := decodeUniqueJSON(raw)
	if err != nil {
		return false, errors.New("decode JSON for inspection")
	}
	matched, err := s.jsonContains(value, 0)
	if err != nil || matched {
		return matched, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return false, errors.New("encode JSON for inspection")
	}
	return s.Contains(string(canonical)), nil
}

func (s *Set) jsonContains(value any, depth int) (bool, error) {
	if depth > maximumJSONSanitizationDepth {
		return false, errors.New("JSON inspection nesting exceeds limit")
	}
	switch typed := value.(type) {
	case string:
		return s.Contains(typed), nil
	case map[string]any:
		for key, child := range typed {
			if s.Contains(key) {
				return true, nil
			}
			matched, err := s.jsonContains(child, depth+1)
			if err != nil || matched {
				return matched, err
			}
		}
		return false, nil
	case []any:
		for _, child := range typed {
			matched, err := s.jsonContains(child, depth+1)
			if err != nil || matched {
				return matched, err
			}
		}
		return false, nil
	case nil:
		return s.Contains("null"), nil
	case bool:
		if typed {
			return s.Contains("true"), nil
		}
		return s.Contains("false"), nil
	case json.Number:
		return s.Contains(typed.String()), nil
	default:
		return false, errors.New("unsupported decoded JSON value")
	}
}

// JSON removes configured literals from decoded JSON string values and keys,
// then emits one canonical JSON value. Decoding before matching closes valid
// wire-spelling aliases such as solidus escapes and arbitrary \uXXXX escapes.
// A malformed document, key collision, or guard-exhaustion suppression fails
// closed without returning any partially sanitized document.
func (s *Set) JSON(raw []byte) ([]byte, error) {
	value, err := decodeUniqueJSON(raw)
	if err != nil {
		return nil, errors.New("decode JSON for sanitization")
	}
	safe, err := s.jsonValue(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return nil, errors.New("encode sanitized JSON")
	}
	if s.Contains(string(encoded)) {
		return nil, errors.New("sanitized JSON reconstructed configured credential material")
	}
	return encoded, nil
}

// JSONBounded has the same semantic contract as JSON and additionally bounds
// the complete canonical output. Its aggregate budget is consumed before
// retaining sanitized strings, so a short credential repeated in a bounded
// input cannot amplify into an unbounded replacement document.
func (s *Set) JSONBounded(raw []byte, limit int) ([]byte, error) {
	if limit < 0 {
		return s.JSON(raw)
	}
	value, err := decodeUniqueJSON(raw)
	if err != nil {
		return nil, errors.New("decode JSON for sanitization")
	}
	budget := jsonOutputBudget{remaining: limit}
	safe, err := s.jsonValueBounded(value, &budget, 0)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return nil, errors.New("encode sanitized JSON")
	}
	if len(encoded) > limit {
		return nil, errors.New("sanitized JSON exceeds output limit")
	}
	if s.Contains(string(encoded)) {
		return nil, errors.New("sanitized JSON reconstructed configured credential material")
	}
	return encoded, nil
}

const maximumJSONSanitizationDepth = 256

func decodeUniqueJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("JSON contains a trailing value")
	}
	return value, nil
}

func decodeUniqueJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maximumJSONSanitizationDepth {
		return nil, errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		switch token.(type) {
		case nil, bool, string, json.Number:
			return token, nil
		default:
			return nil, errors.New("unsupported JSON token")
		}
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("JSON object contains a duplicate key")
			}
			child, err := decodeUniqueJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = child
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("JSON object is not terminated")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			child, err := decodeUniqueJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, child)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("JSON array is not terminated")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

type jsonOutputBudget struct {
	remaining int
}

func (b *jsonOutputBudget) take(size int) error {
	if size < 0 || size > b.remaining {
		return errors.New("sanitized JSON exceeds output limit")
	}
	b.remaining -= size
	return nil
}

func (s *Set) boundedJSONString(value string, budget *jsonOutputBudget) (string, error) {
	safe, truncated, suppressed := s.RedactBounded(value, budget.remaining)
	if truncated || suppressed {
		return "", errors.New("JSON string could not be safely sanitized within output limit")
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return "", errors.New("encode sanitized JSON string")
	}
	if err := budget.take(len(encoded)); err != nil {
		return "", err
	}
	return safe, nil
}

func (s *Set) jsonValueBounded(value any, budget *jsonOutputBudget, depth int) (any, error) {
	if depth > maximumJSONSanitizationDepth {
		return nil, errors.New("JSON sanitization nesting exceeds limit")
	}
	switch typed := value.(type) {
	case string:
		return s.boundedJSONString(typed, budget)
	case map[string]any:
		if err := budget.take(2); err != nil {
			return nil, err
		}
		if len(typed) > 0 {
			if err := budget.take(len(typed) - 1 + len(typed)); err != nil {
				return nil, err
			}
		}
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			safeKey, err := s.boundedJSONString(key, budget)
			if err != nil {
				return nil, errors.New("JSON object key could not be safely sanitized within output limit")
			}
			if _, exists := result[safeKey]; exists {
				return nil, errors.New("JSON object keys collided after sanitization")
			}
			safeChild, err := s.jsonValueBounded(child, budget, depth+1)
			if err != nil {
				return nil, err
			}
			result[safeKey] = safeChild
		}
		return result, nil
	case []any:
		if err := budget.take(2); err != nil {
			return nil, err
		}
		if len(typed) > 1 {
			if err := budget.take(len(typed) - 1); err != nil {
				return nil, err
			}
		}
		result := make([]any, len(typed))
		for index, child := range typed {
			safe, err := s.jsonValueBounded(child, budget, depth+1)
			if err != nil {
				return nil, fmt.Errorf("sanitize JSON array item: %w", err)
			}
			result[index] = safe
		}
		return result, nil
	case nil:
		if s.Contains("null") {
			return nil, errors.New("JSON null could not be safely sanitized")
		}
		if err := budget.take(len("null")); err != nil {
			return nil, err
		}
		return nil, nil
	case bool:
		spelling := "false"
		if typed {
			spelling = "true"
		}
		if s.Contains(spelling) {
			return nil, errors.New("JSON boolean could not be safely sanitized")
		}
		if err := budget.take(len(spelling)); err != nil {
			return nil, err
		}
		return typed, nil
	case json.Number:
		spelling := typed.String()
		if s.Contains(spelling) {
			return nil, errors.New("JSON number could not be safely sanitized")
		}
		if err := budget.take(len(spelling)); err != nil {
			return nil, err
		}
		return typed, nil
	default:
		return nil, errors.New("unsupported decoded JSON value")
	}
}

func (s *Set) jsonValue(value any) (any, error) {
	switch typed := value.(type) {
	case string:
		safe, suppressed := s.Redact(typed)
		if suppressed {
			return nil, errors.New("JSON string could not be safely sanitized")
		}
		return safe, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			safeKey, suppressed := s.Redact(key)
			if suppressed {
				return nil, errors.New("JSON object key could not be safely sanitized")
			}
			if _, exists := result[safeKey]; exists {
				return nil, errors.New("JSON object keys collided after sanitization")
			}
			safeChild, err := s.jsonValue(child)
			if err != nil {
				return nil, err
			}
			result[safeKey] = safeChild
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			safe, err := s.jsonValue(child)
			if err != nil {
				return nil, fmt.Errorf("sanitize JSON array item: %w", err)
			}
			result[index] = safe
		}
		return result, nil
	case nil:
		if s.Contains("null") {
			return nil, errors.New("JSON null could not be safely sanitized")
		}
		return nil, nil
	case bool:
		spelling := "false"
		if typed {
			spelling = "true"
		}
		if s.Contains(spelling) {
			return nil, errors.New("JSON boolean could not be safely sanitized")
		}
		return typed, nil
	case json.Number:
		if s.Contains(typed.String()) {
			return nil, errors.New("JSON number could not be safely sanitized")
		}
		return typed, nil
	default:
		return nil, errors.New("unsupported decoded JSON value")
	}
}
