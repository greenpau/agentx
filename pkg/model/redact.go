package model

import "github.com/greenpau/agentx/pkg/redact"

// StreamRedactor removes sensitive material from ordered text chunks. Write
// may retain a bounded suffix that could complete a match in a later chunk;
// Flush returns the final safe suffix when the stream ends.
type StreamRedactor interface {
	Write(string) string
	Flush() string
}

// NewLiteralStreamRedactor constructs a redactor for one exact sensitive
// literal. It delegates to the shared primitive used by non-model egress paths.
func NewLiteralStreamRedactor(literal string) StreamRedactor {
	return redact.NewLiteralStream(literal)
}

func newSetStreamRedactor(set *redact.Set) StreamRedactor {
	return redact.NewSetStream(set)
}

// literalFailureTable supports provider metadata reflection detection. Stream
// redaction itself lives in pkg/redact; this small model-local table avoids
// exporting matching internals as public credential-handling API.
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
