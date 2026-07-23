//go:build !windows

package platform

import "testing"

func TestEnableOwnedProcessTreeIsPortableNoOp(t *testing.T) {
	if err := EnableOwnedProcessTree(); err != nil {
		t.Fatal(err)
	}
	if err := EnableOwnedProcessTree(); err != nil {
		t.Fatalf("repeated enable failed: %v", err)
	}
}
