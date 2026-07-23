// Package platform owns portable operating-system mechanics and bounded
// resource cleanup. It does not make semantic permission decisions.
package platform

import (
	"runtime"
	"strings"
)

// Profile is the process host classification. Unknown is a supported core
// profile; individual optional capabilities decide whether they can run on it.
type Profile string

const (
	ProfileMacOS   Profile = "macos"
	ProfileWindows Profile = "windows"
	ProfileWSL     Profile = "wsl"
	ProfileLinux   Profile = "linux"
	ProfileUnknown Profile = "unknown"
)

// DetectProfile classifies the current host conservatively. A proc-version
// read failure leaves Linux usable instead of making the whole client fail.
func DetectProfile(readProcVersion func() ([]byte, error)) Profile {
	switch runtime.GOOS {
	case "darwin":
		return ProfileMacOS
	case "windows":
		return ProfileWindows
	case "linux":
		if readProcVersion != nil {
			if contents, err := readProcVersion(); err == nil {
				lower := strings.ToLower(string(contents))
				if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
					return ProfileWSL
				}
			}
		}
		return ProfileLinux
	default:
		return ProfileUnknown
	}
}

// CapabilityState keeps unsupported, unavailable, denied, and failed
// integrations distinct so callers can choose the correct safe degradation.
type CapabilityState string

const (
	CapabilityAvailable              CapabilityState = "available"
	CapabilityUnsupported            CapabilityState = "unsupported"
	CapabilityDependencyAbsent       CapabilityState = "dependency_absent"
	CapabilityPermissionDenied       CapabilityState = "permission_denied"
	CapabilityTemporarilyUnavailable CapabilityState = "temporarily_unavailable"
	CapabilityMalformedConfiguration CapabilityState = "malformed_configuration"
	CapabilityOperationFailed        CapabilityState = "operation_failed"
	CapabilityCancelled              CapabilityState = "cancelled"
)

// Capability is a presentation-safe platform probe result.
type Capability struct {
	State  CapabilityState `json:"state"`
	Reason string          `json:"reason,omitempty"`
}

// Available reports whether the capability may be attempted. It does not
// grant semantic authority to perform the operation.
func (c Capability) Available() bool { return c.State == CapabilityAvailable }
