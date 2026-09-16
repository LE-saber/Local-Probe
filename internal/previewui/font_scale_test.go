package previewui

import "testing"

func TestNormalizeFontScale(t *testing.T) {
	for _, value := range []FontScale{FontScale100, FontScale125, FontScale150, FontScale175, FontScale200} {
		if got := NormalizeFontScale(value); got != value {
			t.Fatalf("NormalizeFontScale(%d) = %d, want %d", value, got, value)
		}
	}
	if got := NormalizeFontScale(101); got != DefaultFontScale {
		t.Fatalf("invalid scale normalized to %d, want %d", got, DefaultFontScale)
	}
}

func TestNextFontScaleCyclesSupportedSteps(t *testing.T) {
	want := []FontScale{FontScale175, FontScale200, FontScale100, FontScale125, FontScale150, FontScale175}
	value := DefaultFontScale
	for index, expected := range want {
		value = NextFontScale(value)
		if value != expected {
			t.Fatalf("step %d = %d, want %d", index, value, expected)
		}
	}
	if got := NextFontScale(101); got != FontScale175 {
		t.Fatalf("invalid scale next = %d, want %d", got, FontScale175)
	}
}

func TestScaleFontSizeRoundsAndKeepsNonPositiveValues(t *testing.T) {
	tests := []struct {
		base  int
		scale FontScale
		want  int
	}{
		{10, FontScale100, 10},
		{10, FontScale125, 13},
		{10, FontScale150, 15},
		{10, FontScale175, 18},
		{10, FontScale200, 20},
		{1, FontScale125, 1},
		{0, FontScale200, 0},
		{-4, FontScale200, -4},
	}
	for _, test := range tests {
		if got := ScaleFontSize(test.base, test.scale); got != test.want {
			t.Fatalf("ScaleFontSize(%d, %d) = %d, want %d", test.base, test.scale, got, test.want)
		}
	}
}

func TestFontScaleLabelNormalizesValue(t *testing.T) {
	if got := FontScaleLabel(FontScale150); got != "字号 150%" {
		t.Fatalf("label = %q", got)
	}
	if got := FontScaleLabel(101); got != "字号 150%" {
		t.Fatalf("invalid label = %q", got)
	}
}
