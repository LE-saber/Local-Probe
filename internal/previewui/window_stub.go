//go:build !windows

package previewui

// Run keeps the package buildable for tooling and tests on non-Windows
// systems. The Preview executable is intentionally Windows-only.
func Run(options RunOptions) error { return ErrUnsupportedPlatform }
