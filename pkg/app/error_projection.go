package app

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/surface"
)

const maximumOperationalErrorGraphNodes = 128

var (
	trustedOperationalErrorStringType = reflect.TypeOf(errors.New(""))
	trustedOperationalSingleWrapType  = reflect.TypeOf(fmt.Errorf("wrapped: %w", errors.New("")))
	trustedOperationalMultiWrapType   = reflect.TypeOf(fmt.Errorf("wrapped: %w %w", errors.New(""), errors.New("")))
	trustedOperationalJoinType        = reflect.TypeOf(errors.Join(errors.New(""), errors.New("")))
)

func operationalErrorClassified(classes map[error]struct{}, target error) bool {
	if target == nil {
		return false
	}
	typ := reflect.TypeOf(target)
	if typ == nil || !typ.Comparable() {
		return false
	}
	_, exists := classes[target]
	return exists
}

func operationalErrorIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	classes, _ := inspectOperationalError(err)
	return operationalErrorClassified(classes, target)
}

// inspectOperationalError snapshots exact roots, surface-owned sealed
// classifications, and wrappers whose concrete implementation is owned by the
// Go standard library. It never invokes Error, Is, As, or Unwrap on an
// operational callback's error implementation.
func inspectOperationalError(cause error) (map[error]struct{}, *cli.UsageError) {
	pending := []error{cause}
	seen := make(map[error]struct{})
	nodes := make([]error, 0, maximumOperationalErrorGraphNodes)
	for visited := 0; len(pending) > 0 && visited < maximumOperationalErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		typ := reflect.TypeOf(current)
		if typ != nil && typ.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
		}
		nodes = append(nodes, current)
		children := trustedOperationalErrorChildren(current)
		remaining := maximumOperationalErrorGraphNodes - visited - 1 - len(pending)
		if remaining < 0 {
			remaining = 0
		}
		if len(children) > remaining {
			children = children[:remaining]
		}
		for index := len(children) - 1; index >= 0; index-- {
			pending = append(pending, children[index])
		}
	}

	classes := make(map[error]struct{})
	addClass := func(candidate error) {
		if candidate == nil || len(classes) >= maximumOperationalErrorGraphNodes {
			return
		}
		typ := reflect.TypeOf(candidate)
		if typ != nil && typ.Comparable() {
			classes[candidate] = struct{}{}
		}
	}
	var usage *cli.UsageError
	// Prioritize identities that an earlier public projection deliberately
	// sealed. Otherwise repeated cleanup joins would consume the bound with
	// presentation wrappers and eventually discard the policy sentinel.
	for _, current := range nodes {
		projected, ok := current.(*redactedOperationalError)
		if !ok {
			for _, class := range surface.SealedOutputErrorClassifications(current) {
				addClass(class)
			}
		} else if projected == nil {
			continue
		} else {
			for class := range projected.classes {
				addClass(class)
			}
			if usage == nil && projected.usage != nil {
				usage = projected.usage
			}
		}
	}
	for _, current := range nodes {
		if typed, ok := current.(*cli.UsageError); ok && usage == nil {
			usage = typed
		}
		addClass(current)
	}
	return classes, usage
}

func trustedOperationalErrorChildren(err error) []error {
	switch reflect.TypeOf(err) {
	case trustedOperationalSingleWrapType:
		return []error{err.(interface{ Unwrap() error }).Unwrap()}
	case trustedOperationalMultiWrapType, trustedOperationalJoinType:
		return err.(interface{ Unwrap() []error }).Unwrap()
	}
	return nil
}

func safeOperationalErrorText(err error) (message string) {
	if err == nil {
		return ""
	}
	message = "operation failed"
	defer func() {
		if recover() != nil {
			message = "operation failed"
		}
	}()
	pending := []error{err}
	seen := make(map[error]struct{})
	for visited := 0; len(pending) > 0 && visited < maximumOperationalErrorGraphNodes; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		currentType := reflect.TypeOf(current)
		if currentType != nil && currentType.Comparable() {
			if _, exists := seen[current]; exists {
				continue
			}
			seen[current] = struct{}{}
		}
		switch typed := current.(type) {
		case *redactedOperationalError:
			return typed.Error()
		case *cli.UsageError:
			return typed.Error()
		}
		switch currentType {
		case trustedOperationalErrorStringType, trustedOperationalSingleWrapType, trustedOperationalMultiWrapType:
			return current.Error()
		}
		children := trustedOperationalErrorChildren(current)
		remaining := maximumOperationalErrorGraphNodes - visited - 1 - len(pending)
		if remaining < 0 {
			remaining = 0
		}
		if len(children) > remaining {
			children = children[:remaining]
		}
		for index := len(children) - 1; index >= 0; index-- {
			pending = append(pending, children[index])
		}
	}
	return message
}

func applyOperationalRedactor(redact func(string) string, value string) (result string) {
	if redact == nil {
		return value
	}
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	return redact(value)
}
