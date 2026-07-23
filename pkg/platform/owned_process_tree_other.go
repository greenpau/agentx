//go:build !windows

package platform

// EnableOwnedProcessTree is a no-op outside Windows. Child capabilities on
// supported Unix hosts establish their own process groups at spawn time, and
// accepting this startup option keeps process launchers portable.
func EnableOwnedProcessTree() error { return nil }
