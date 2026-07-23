// Package identity defines identifiers whose distinct types prevent accidental
// correlation across session, turn, message, tool, task, and request domains.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type SessionID string
type TurnID string
type MessageID string
type ToolUseID string
type TaskID string
type RequestID string

// New returns a cryptographically random, locally generated identifier. The
// prefix is human-readable provenance, not part of the uniqueness guarantee.
func New(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate %s identifier: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

func NewSession() (SessionID, error) { v, err := New("ses"); return SessionID(v), err }
func NewTurn() (TurnID, error)       { v, err := New("turn"); return TurnID(v), err }
func NewMessage() (MessageID, error) { v, err := New("msg"); return MessageID(v), err }
func NewTask() (TaskID, error)       { v, err := New("task"); return TaskID(v), err }
func NewRequest() (RequestID, error) { v, err := New("req"); return RequestID(v), err }
