//go:build !windows

package previewui

import "errors"

var (
	ErrInvalidPreviewIcon = errors.New("invalid preview icon resource")
	ErrPreviewIconSize    = errors.New("invalid preview icon size")
	ErrPreviewIconCreate  = errors.New("preview icon creation failed")
)

// loadPreviewIcon keeps callers buildable on non-Windows platforms. The
// Preview executable is Windows-only, so there is deliberately no fallback
// image decoder or filesystem lookup here.
func loadPreviewIcon(size int) (uintptr, error) { return 0, ErrUnsupportedPlatform }

// destroyPreviewIcon is a safe no-op for the non-Windows build.
func destroyPreviewIcon(icon uintptr) {}
