package previewui

import "strconv"

// FontScale is the display scale used by the native Preview painter.  It is
// deliberately kept as a small, non-persistent UI preference: changing it
// cannot affect the MCP process, tunnel, configuration, or audit data.
type FontScale uint16

const (
	FontScale100 FontScale = 100
	FontScale125 FontScale = 125
	FontScale150 FontScale = 150
	FontScale175 FontScale = 175
	FontScale200 FontScale = 200

	// Start large enough to improve readability immediately while keeping the
	// existing compact layout usable.  The button can cycle through every
	// supported size without persistence.
	DefaultFontScale = FontScale150
)

var fontScaleSteps = [...]FontScale{
	FontScale100,
	FontScale125,
	FontScale150,
	FontScale175,
	FontScale200,
}

// NormalizeFontScale maps an invalid value to the safe default.  Keeping the
// fallback deterministic also makes any future persisted/UI-provided value
// fail closed without introducing a configuration surface today.
func NormalizeFontScale(value FontScale) FontScale {
	for _, step := range fontScaleSteps {
		if value == step {
			return value
		}
	}
	return DefaultFontScale
}

// NextFontScale advances through the supported display sizes and wraps back
// to the smallest size.  Invalid values start at the default size.
func NextFontScale(value FontScale) FontScale {
	value = NormalizeFontScale(value)
	for index, step := range fontScaleSteps {
		if value == step {
			return fontScaleSteps[(index+1)%len(fontScaleSteps)]
		}
	}
	return DefaultFontScale
}

// ScaleFontSize applies a percentage to a base pixel size, rounding to the
// nearest integer and keeping positive text visible at every supported step.
func ScaleFontSize(base int, scale FontScale) int {
	if base <= 0 {
		return base
	}
	scale = NormalizeFontScale(scale)
	value := (base*int(scale) + 50) / 100
	if value < 1 {
		return 1
	}
	return value
}

func FontScaleLabel(value FontScale) string {
	return "字号 " + strconv.Itoa(int(NormalizeFontScale(value))) + "%"
}
