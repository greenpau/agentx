package identity

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	a, err := New("ses")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New("ses")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "ses_") || len(a) != len("ses_")+32 {
		t.Fatalf("unexpected identifiers %q %q", a, b)
	}
}
