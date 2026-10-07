//go:build windows

package edge

import (
	"testing"
	"unsafe"
)

func TestColorSchemeABIAndUninitialized(t *testing.T) {
	var view colorWebView13Vtbl
	var profile colorProfileVtbl
	stride := unsafe.Sizeof(ComProc(0))
	if unsafe.Offsetof(view.GetProfile) != 105*stride || unsafe.Offsetof(profile.PutPreferredColorScheme) != 9*stride || unsafe.Sizeof(view) != 106*stride {
		t.Fatal("WebView2 profile ABI drift")
	}
	for _, chromium := range []*Chromium{nil, {}} {
		if err := chromium.SetPreferredColorSchemeChecked(true); err == nil {
			t.Fatal("accepted uninitialized WebView")
		}
	}
}
