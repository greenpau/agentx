package redact

import "strings"

// SetStream removes every literal in one immutable Set from an ordered byte
// stream. It retains at most the longest literal minus one byte, so matches
// split across arbitrary process-write boundaries are never partially emitted.
type SetStream struct {
	set      *Set
	failures [][]int
	pending  string
	closed   bool
}

// NewSetStream constructs a fresh multi-literal stream filter.
func NewSetStream(set *Set) *SetStream {
	stream := &SetStream{set: set}
	stream.closed = set != nil && !set.Empty() && set.mask == ""
	if set != nil {
		stream.failures = make([][]int, len(set.literals))
		for index, literal := range set.literals {
			stream.failures[index] = literalFailureTable(literal)
		}
	}
	return stream
}

// String exposes neither configured literals nor retained stream prefixes.
func (r SetStream) String() string {
	return ""
}

// GoString applies the same safe projection to diagnostic formatting.
func (r SetStream) GoString() string { return r.String() }

// Write consumes one ordered chunk and returns only bytes that cannot become
// part of a configured literal after a later chunk arrives.
func (r *SetStream) Write(chunk string) string {
	if r == nil {
		return chunk
	}
	if r.closed {
		return ""
	}
	if r.set == nil || r.set.Empty() {
		return chunk
	}
	input := r.pending + chunk
	r.pending = ""
	output, remainder := r.project(input, true)
	r.pending = remainder
	return output
}

// Flush returns the final safe suffix and permanently closes the filter.
func (r *SetStream) Flush() string {
	if r == nil || r.closed {
		return ""
	}
	r.closed = true
	if r.pending == "" {
		return ""
	}
	output, _ := r.project(r.pending, false)
	r.pending = ""
	return output
}

// TruncationMarker returns terminal framing that is compositionally safe for
// the complete configured set.
func (r *SetStream) TruncationMarker() string {
	if r == nil || r.set == nil || len(r.set.mask) < minimumMaskWidth {
		return ""
	}
	return r.set.mask[:minimumMaskWidth]
}

func (r *SetStream) project(input string, retainPrefix bool) (string, string) {
	var output strings.Builder
	for {
		matchAt := -1
		matched := ""
		for _, literal := range r.set.literals {
			index := strings.Index(input, literal)
			if index < 0 {
				continue
			}
			if matchAt < 0 || index < matchAt {
				matchAt = index
				matched = literal
			}
		}
		if matchAt < 0 {
			break
		}
		output.WriteString(input[:matchAt])
		output.WriteString(r.set.mask)
		input = input[matchAt+len(matched):]
	}
	if !retainPrefix {
		output.WriteString(input)
		return output.String(), ""
	}
	hold := 0
	for index, literal := range r.set.literals {
		limit := len(literal) - 1
		if limit <= 0 {
			continue
		}
		tail := input
		if len(tail) > limit {
			tail = tail[len(tail)-limit:]
		}
		if matched := longestLiteralPrefixSuffix(tail, literal, r.failures[index]); matched > hold {
			hold = matched
		}
	}
	output.WriteString(input[:len(input)-hold])
	return output.String(), input[len(input)-hold:]
}

// LiteralStream removes one literal from an ordered sequence of chunks without
// emitting bytes that may still complete that literal in a later chunk. Its
// retained state is bounded by len(literal)-1, independent of stream size.
type LiteralStream struct {
	literal string
	pending string
	mask    string
	failure []int
	closed  bool
}

// NewLiteralStream constructs a fresh exact-literal stream filter.
func NewLiteralStream(literal string) *LiteralStream {
	stream := &LiteralStream{
		literal: literal,
		mask:    Mask(literal),
		failure: literalFailureTable(literal),
	}
	// A stream cannot retract bytes already returned. If a matching value has
	// no compositionally safe marker, suppress the complete stream from
	// construction rather than delete matches and let surrounding context join
	// into the literal.
	stream.closed = literal != "" && stream.mask == ""
	return stream
}

// String exposes no configured literal, including when the literal overlaps
// the diagnostic text or the historical redaction marker.
func (r LiteralStream) String() string {
	return Literal("literal-stream-redactor", r.literal)
}

// GoString applies the same safe projection to diagnostic formatting.
func (r LiteralStream) GoString() string { return r.String() }

// Write consumes one ordered chunk and returns only bytes safe to persist or
// project. A proper literal prefix at the end remains process-local.
func (r *LiteralStream) Write(chunk string) string {
	if r == nil {
		return chunk
	}
	if r.closed {
		return ""
	}
	if r.literal == "" {
		return chunk
	}
	input := r.pending + chunk
	r.pending = ""

	var output strings.Builder
	for {
		match := strings.Index(input, r.literal)
		if match < 0 {
			break
		}
		output.WriteString(input[:match])
		output.WriteString(r.mask)
		input = input[match+len(r.literal):]
	}

	hold := longestLiteralPrefixSuffix(input, r.literal, r.failure)
	output.WriteString(input[:len(input)-hold])
	r.pending = input[len(input)-hold:]
	return output.String()
}

// Flush returns the final safe suffix and clears retained state.
func (r *LiteralStream) Flush() string {
	if r == nil || r.closed {
		return ""
	}
	r.closed = true
	if r.pending == "" {
		return ""
	}
	output := Literal(r.pending, r.literal)
	r.pending = ""
	return output
}

// TruncationMarker returns a terminal marker that can follow any prefix of
// output already returned by Write without reconstructing the literal. The
// task runtime appends this marker only after it stops persisting stream data.
// It intentionally contains no prose: arbitrary fixed text could itself equal
// a short credential.
func (r *LiteralStream) TruncationMarker() string {
	if r == nil || len(r.mask) < minimumMaskWidth {
		return ""
	}
	return r.mask[:minimumMaskWidth]
}

func literalFailureTable(literal string) []int {
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

func longestLiteralPrefixSuffix(value, literal string, failure []int) int {
	matched := 0
	for index := 0; index < len(value); index++ {
		for matched > 0 && value[index] != literal[matched] {
			matched = failure[matched-1]
		}
		if value[index] == literal[matched] {
			matched++
		}
		if matched == len(literal) {
			matched = failure[matched-1]
		}
	}
	return matched
}
