package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry is an immutable, deterministic session registry.
type Registry struct {
	byName  map[string]*Descriptor
	aliases map[string]string
	ordered []*Descriptor
	mu      sync.RWMutex
}

// NewRegistry validates descriptors, filters disabled entries, sorts built-ins
// before external entries, and makes built-ins win canonical collisions.
func NewRegistry(descriptors ...Descriptor) (*Registry, error) {
	for i := range descriptors {
		if err := validateDescriptor(descriptors[i]); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(descriptors, func(i, j int) bool {
		leftExternal := descriptors[i].Source != SourceBuiltin
		rightExternal := descriptors[j].Source != SourceBuiltin
		if leftExternal != rightExternal {
			return !leftExternal
		}
		return descriptors[i].Name < descriptors[j].Name
	})
	registry := &Registry{byName: make(map[string]*Descriptor), aliases: make(map[string]string)}
	for i := range descriptors {
		descriptor := descriptors[i]
		if !descriptor.enabled() {
			continue
		}
		if _, exists := registry.byName[descriptor.Name]; exists {
			continue
		}
		copy := cloneDescriptor(descriptor)
		registry.byName[copy.Name] = &copy
		registry.ordered = append(registry.ordered, &copy)
	}
	for _, descriptor := range registry.ordered {
		for _, alias := range descriptor.Aliases {
			if _, canonicalCollision := registry.byName[alias]; canonicalCollision {
				continue
			}
			if prior, exists := registry.aliases[alias]; exists && prior != descriptor.Name {
				return nil, fmt.Errorf("tool alias %q is ambiguous", alias)
			}
			registry.aliases[alias] = descriptor.Name
		}
	}
	return registry, nil
}

func validateDescriptor(descriptor Descriptor) error {
	if strings.TrimSpace(descriptor.Name) == "" {
		return errors.New("tool canonical name is required")
	}
	if descriptor.Validate == nil {
		return fmt.Errorf("tool %s has no input validator", descriptor.Name)
	}
	if descriptor.Call == nil {
		return fmt.Errorf("tool %s has no call implementation", descriptor.Name)
	}
	for _, alias := range descriptor.Aliases {
		if alias == "" || alias == descriptor.Name {
			return fmt.Errorf("tool %s has invalid alias %q", descriptor.Name, alias)
		}
	}
	return nil
}

// Resolve checks canonical names before declared aliases.
func (r *Registry) Resolve(name string) (*Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if descriptor, ok := r.byName[name]; ok {
		copy := cloneDescriptor(*descriptor)
		return &copy, true
	}
	canonical, ok := r.aliases[name]
	if !ok {
		return nil, false
	}
	descriptor, ok := r.byName[canonical]
	if !ok {
		return nil, false
	}
	copy := cloneDescriptor(*descriptor)
	return &copy, true
}

// Descriptors returns immutable descriptor copies in exposure order.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Descriptor, 0, len(r.ordered))
	for _, descriptor := range r.ordered {
		result = append(result, cloneDescriptor(*descriptor))
	}
	return result
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Aliases = append([]string(nil), descriptor.Aliases...)
	descriptor.InputSchema = cloneMap(descriptor.InputSchema)
	return descriptor
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneJSONValue(value)
	}
	return result
}

// cloneJSONValue copies the JSON-shaped mutable containers used by tool
// schemas and result metadata. Function values and scalar descriptor fields
// are immutable values and remain shared deliberately.
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = cloneJSONValue(child)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, child := range typed {
			result[key] = child
		}
		return result
	case json.RawMessage:
		return cloneRaw(typed)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}
