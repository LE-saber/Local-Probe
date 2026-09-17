//go:build !windows

package previewui

import (
	"errors"
	"testing"
)

func TestLoadPreviewIconStubIsUnsupported(t *testing.T) {
	if icon, err := loadPreviewIcon(32); icon != 0 || !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("stub result = (%d, %v), want (0, ErrUnsupportedPlatform)", icon, err)
	}
	destroyPreviewIcon(0)
}
