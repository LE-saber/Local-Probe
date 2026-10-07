//go:build !windows

package probe

// The fixed process launcher is intentionally Windows-only in this release.
// Keeping this explicit prevents a Unix path launcher from being mistaken for
// the Windows TOCTOU hard gate.
func processExecutionSupported() bool { return false }
