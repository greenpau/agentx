package app

import "testing"

func TestSessionEndReasonKeepsOnlyWireEnum(t *testing.T) {
	for _, reason := range []string{"clear", "resume", "logout", "prompt_input_exit", "other", "bypass_permissions_disabled"} {
		if got := sessionEndReason(reason); got != reason {
			t.Fatalf("sessionEndReason(%q) = %q", reason, got)
		}
	}
	for _, reason := range []string{"sigint", "sigterm", "sighup", "orphan_detected", ""} {
		if got := sessionEndReason(reason); got != "other" {
			t.Fatalf("sessionEndReason(%q) = %q", reason, got)
		}
	}
}
