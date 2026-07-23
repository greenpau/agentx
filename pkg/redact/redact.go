// Package redact removes exact sensitive literals without allowing the
// replacement itself, adjacent context, or a later redaction pass to recreate
// a configured literal.
package redact

import (
	"sort"
	"strings"
)

const (
	minimumMaskWidth         = len("[REDACTED]")
	conventionalMask         = "[REDACTED]"
	guardCandidates          = "*#~!@$%^&_-+=:;,.?0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz/|"
	maximumPermutationValues = 5
)

// Set is an immutable collection of exact sensitive literals. Its diagnostic
// formatting never exposes the literals. Build one Set from the complete
// literal union for a sink; chaining independently constructed redactors is
// not compositionally safe because a later replacement can recreate an
// earlier literal.
type Set struct {
	literals []string
	mask     string
}

// New constructs a canonical immutable literal set.
func New(literals ...string) *Set {
	set := newLiteralSet(literals)
	return &set
}

// Union constructs one canonical set from all supplied sets.
func Union(sets ...*Set) *Set {
	var literals []string
	for _, set := range sets {
		if set != nil {
			literals = append(literals, set.literals...)
		}
	}
	return New(literals...)
}

// With returns a new set containing the receiver and the additional literals.
func (s *Set) With(literals ...string) *Set {
	combined := append([]string(nil), literals...)
	if s != nil {
		combined = append(combined, s.literals...)
	}
	return New(combined...)
}

// Empty reports whether the set has no configured nonempty literal.
func (s *Set) Empty() bool { return s == nil || len(s.literals) == 0 }

// Contains reports whether value includes any configured exact literal.
func (s *Set) Contains(value string) bool {
	return s != nil && containsAny(value, s.literals)
}

// ContainsAcrossPermutations reports whether concatenating all values in any
// order can reconstruct a configured literal. Each value is reduced to the
// only prefix/suffix bytes that can participate in a cross-value match. The
// supported field count is explicitly bounded because exhaustive ordering is
// factorial; a larger record conservatively reports unsafe rather than doing
// attacker-controlled combinatorial work.
func (s *Set) ContainsAcrossPermutations(values []string) bool {
	if s == nil || len(s.literals) == 0 || len(values) == 0 {
		return false
	}
	if len(values) > maximumPermutationValues {
		return true
	}
	order := make([]int, 0, len(values))
	used := make([]bool, len(values))
	for _, literal := range s.literals {
		boundary := make([]string, len(values))
		limit := len(literal) - 1
		for index, value := range values {
			if limit <= 0 || len(value) <= limit*2 {
				boundary[index] = value
			} else {
				boundary[index] = value[:limit] + value[len(value)-limit:]
			}
		}
		failure := literalFailureTableForSet(literal)
		var search func() bool
		search = func() bool {
			if len(order) == len(values) {
				matched := 0
				for _, index := range order {
					for offset := 0; offset < len(boundary[index]); offset++ {
						for matched > 0 && boundary[index][offset] != literal[matched] {
							matched = failure[matched-1]
						}
						if boundary[index][offset] == literal[matched] {
							matched++
						}
						if matched == len(literal) {
							return true
						}
					}
				}
				return false
			}
			for index := range values {
				if used[index] {
					continue
				}
				used[index] = true
				order = append(order, index)
				if search() {
					return true
				}
				order = order[:len(order)-1]
				used[index] = false
			}
			return false
		}
		if search() {
			return true
		}
	}
	return false
}

// MaxLiteralBytes returns only the longest configured byte length. It exposes
// no literal content and lets bounded readers retain enough lookahead to avoid
// cutting a match that began before their projection limit.
func (s *Set) MaxLiteralBytes() int {
	maximum := 0
	if s != nil {
		for _, literal := range s.literals {
			if len(literal) > maximum {
				maximum = len(literal)
			}
		}
	}
	return maximum
}

// LiteralCount reports only the number of configured literals. It exposes no
// credential content and lets sink owners enforce aggregate workload bounds.
func (s *Set) LiteralCount() int {
	if s == nil {
		return 0
	}
	return len(s.literals)
}

// TotalLiteralBytes reports only the aggregate configured byte count. It
// exposes no credential content and lets sink owners reject oversized unions.
func (s *Set) TotalLiteralBytes() int {
	total := 0
	if s != nil {
		for _, literal := range s.literals {
			total += len(literal)
		}
	}
	return total
}

// Covers reports whether every literal in other is already present in s.
// Neither set's literal content is exposed. This is useful when a sink freezes
// its complete scope before construction and later providers rederive a
// candidate scope that must not widen it.
func (s *Set) Covers(other *Set) bool {
	if other == nil || len(other.literals) == 0 {
		return true
	}
	if s == nil || len(s.literals) < len(other.literals) {
		return false
	}
	present := make(map[string]struct{}, len(s.literals))
	for _, literal := range s.literals {
		present[literal] = struct{}{}
	}
	for _, literal := range other.literals {
		if _, ok := present[literal]; !ok {
			return false
		}
	}
	return true
}

// String deliberately exposes no configured literal, including when a
// configured literal occurs in this diagnostic label itself.
func (s Set) String() string {
	result, suppressed := s.Redact("redact.Set")
	if suppressed {
		return ""
	}
	return result
}

// GoString applies the same safe diagnostic projection.
func (s Set) GoString() string { return s.String() }

// Mask returns the deterministic replacement used for the supplied literals.
// It cannot contain or join with adjacent text to reconstruct any member of
// the set. An empty mask means the only safe complete-value result for a match
// is suppression.
func Mask(literals ...string) string {
	return New(literals...).mask
}

// Literal removes every non-overlapping occurrence of one exact literal.
func Literal(value, literal string) string {
	return Literals(value, literal)
}

// Redact removes every configured literal from one complete logical value.
// suppressed is true when no replacement marker can preserve the value
// safely; callers must drop the enclosing content instead of adding framing
// around the empty result.
func (s *Set) Redact(value string) (result string, suppressed bool) {
	if s == nil || len(s.literals) == 0 {
		return value, false
	}
	if s.mask == "" {
		if containsAny(value, s.literals) {
			return "", true
		}
		return value, false
	}
	result = value
	for _, literal := range s.literals {
		result = strings.ReplaceAll(result, literal, s.mask)
	}
	// Keep the externally visible invariant defensive even if marker selection
	// or replacement ordering is changed later.
	if containsAny(result, s.literals) {
		return "", true
	}
	return result, false
}

// Apply removes literals from one complete logical value. When preservation
// is impossible it returns an empty string; security-sensitive callers that
// add later framing should use Redact and honor its suppression flag.
func (s *Set) Apply(value string) string {
	result, _ := s.Redact(value)
	return result
}

// TerminalMarker returns framing that can safely terminate any prefix of an
// Apply result. Nothing may be appended after it. Empty means no set-safe
// terminal framing is available.
func (s *Set) TerminalMarker() string {
	if s == nil {
		return ""
	}
	return s.mask
}

// RedactBounded applies the set while bounding the preserved content bytes.
// A set-safe terminal marker may extend the result beyond limit. truncated is
// true only when input projection was cut; suppressed has the same meaning as
// Redact. The algorithm never constructs the unbounded expanded result.
func (s *Set) RedactBounded(value string, limit int) (result string, truncated, suppressed bool) {
	if limit < 0 {
		result, suppressed = s.Redact(value)
		return result, false, suppressed
	}
	if s == nil || len(s.literals) == 0 {
		if len(value) <= limit {
			return value, false, false
		}
		return value[:limit], true, false
	}
	if s.mask == "" {
		if containsAny(value, s.literals) {
			return "", false, true
		}
		if len(value) <= limit {
			return value, false, false
		}
		return "", true, true
	}

	var output strings.Builder
	output.Grow(minimum(limit+len(s.mask), len(value)+len(s.mask)))
	appendPart := func(part string, more bool) bool {
		remaining := limit - output.Len()
		if remaining < len(part) {
			if remaining > 0 {
				output.WriteString(part[:remaining])
			}
			output.WriteString(s.mask)
			return false
		}
		output.WriteString(part)
		if more && output.Len() == limit {
			output.WriteString(s.mask)
			return false
		}
		return true
	}

	remaining := value
	for len(remaining) > 0 {
		matchAt := -1
		matched := ""
		for _, literal := range s.literals {
			index := strings.Index(remaining, literal)
			if index >= 0 && (matchAt < 0 || index < matchAt) {
				matchAt = index
				matched = literal
			}
		}
		if matchAt < 0 {
			if !appendPart(remaining, false) {
				result = output.String()
				if containsAny(result, s.literals) {
					return "", true, true
				}
				return result, true, false
			}
			break
		}
		if !appendPart(remaining[:matchAt], true) {
			result = output.String()
			if containsAny(result, s.literals) {
				return "", true, true
			}
			return result, true, false
		}
		remaining = remaining[matchAt+len(matched):]
		if !appendPart(s.mask, len(remaining) > 0) {
			result = output.String()
			if containsAny(result, s.literals) {
				return "", true, true
			}
			return result, true, false
		}
	}
	result = output.String()
	if containsAny(result, s.literals) {
		return "", false, true
	}
	return result, false, false
}

// Literals removes every configured nonempty literal. The replacement uses one
// common marker that cannot contain a literal or join with either adjacent
// context to reconstruct one. If no printable marker can satisfy the complete
// set, a matching complete value is suppressed rather than risk reconstruction.
func Literals(value string, literals ...string) string {
	return New(literals...).Apply(value)
}

func newLiteralSet(input []string) Set {
	seen := make(map[string]struct{}, len(input))
	literals := make([]string, 0, len(input))
	for _, literal := range input {
		if literal == "" {
			continue
		}
		if _, exists := seen[literal]; exists {
			continue
		}
		seen[literal] = struct{}{}
		literals = append(literals, literal)
	}
	sort.Slice(literals, func(i, j int) bool {
		if len(literals[i]) != len(literals[j]) {
			return len(literals[i]) > len(literals[j])
		}
		return literals[i] < literals[j]
	})
	if len(literals) == 0 {
		return Set{}
	}

	if markerSafe(conventionalMask, literals) {
		return Set{literals: literals, mask: conventionalMask}
	}
	for index := 0; index < len(guardCandidates); index++ {
		guard := guardCandidates[index]
		mask := strings.Repeat(string([]byte{guard}), minimumMaskWidth)
		if markerSafe(mask, literals) {
			return Set{literals: literals, mask: mask}
		}
	}
	return Set{literals: literals}
}

func markerSafe(marker string, literals []string) bool {
	for _, literal := range literals {
		// A match wholly inside the marker requires marker to contain literal.
		// A match spanning both sides requires literal to contain the complete
		// marker. The overlap loop rejects matches crossing only one side.
		if strings.Contains(marker, literal) || strings.Contains(literal, marker) {
			return false
		}
		limit := len(literal) - 1
		if len(marker) < limit {
			limit = len(marker)
		}
		for size := 1; size <= limit; size++ {
			if marker[:size] == literal[len(literal)-size:] ||
				marker[len(marker)-size:] == literal[:size] {
				return false
			}
		}
	}
	return true
}

func containsAny(value string, literals []string) bool {
	for _, literal := range literals {
		if strings.Contains(value, literal) {
			return true
		}
	}
	return false
}

func literalFailureTableForSet(literal string) []int {
	failure := make([]int, len(literal))
	for index, matched := 1, 0; index < len(literal); index++ {
		for matched > 0 && literal[index] != literal[matched] {
			matched = failure[matched-1]
		}
		if literal[index] == literal[matched] {
			matched++
		}
		failure[index] = matched
	}
	return failure
}

func minimum(left, right int) int {
	if left < right {
		return left
	}
	return right
}
