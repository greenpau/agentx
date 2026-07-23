package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/redact"
)

func TestLiteralStreamRedactorEverySplitBoundary(t *testing.T) {
	const (
		secret = "test-api-key-DO-NOT-LEAK"
		input  = "before test-api-key-DO-NOT-LEAK after"
	)
	want := "before " + redact.Mask(secret) + " after"
	for split := 0; split <= len(input); split++ {
		t.Run(fmt.Sprintf("split_%d", split), func(t *testing.T) {
			stream := NewLiteralStreamRedactor(secret)
			got := stream.Write(input[:split]) + stream.Write(input[split:]) + stream.Flush()
			if got != want {
				t.Fatalf("split %d redaction = %q, want %q", split, got, want)
			}
			if strings.Contains(got, secret) {
				t.Fatalf("split %d leaked literal", split)
			}
		})
	}
}

func TestLiteralStreamRedactorFlushIsTerminal(t *testing.T) {
	stream := NewLiteralStreamRedactor("secret")
	if got := stream.Write("noise sec"); got != "noise " {
		t.Fatalf("prefix output = %q", got)
	}
	if got := stream.Write("tional"); got != "sectional" {
		t.Fatalf("mismatched prefix output = %q", got)
	}
	if got := stream.Write("secre"); got != "" {
		t.Fatalf("held prefix output = %q", got)
	}
	if got := stream.Flush(); got != "secre" {
		t.Fatalf("flush = %q", got)
	}
	if got := stream.Write("t"); got != "" {
		t.Fatalf("post-flush write = %q", got)
	}
}
