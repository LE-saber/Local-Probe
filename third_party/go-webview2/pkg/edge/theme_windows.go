//go:build windows

package edge

import (
	"errors"
	"runtime"
	"unsafe"
)

// ABI checked against Microsoft's WebView2 SDK 1.0.1210.39 WebView2.h.
// ICoreWebView2_13 inherits slots 0..104; get_Profile is slot 105.
type colorWebView13Vtbl struct {
	_IUnknownVtbl
	Inherited  [102]ComProc
	GetProfile ComProc
}
type colorWebView13 struct{ vtbl *colorWebView13Vtbl }
type colorProfileVtbl struct {
	_IUnknownVtbl
	GetProfileName, GetIsInPrivateModeEnabled, GetProfilePath  ComProc
	GetDefaultDownloadFolderPath, PutDefaultDownloadFolderPath ComProc
	GetPreferredColorScheme, PutPreferredColorScheme           ComProc
}
type colorProfile struct{ vtbl *colorProfileVtbl }

var iidColorWebView13 = NewGUID("F75F09A8-667E-4983-88D6-C8773F315E84")

func (e *Chromium) colorProfileChecked() (*colorProfile, error) {
	if e == nil || e.webview == nil {
		return nil, errors.New("WebView2 is not initialized")
	}
	var view *colorWebView13
	hr, _, _ := e.webview.vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(iidColorWebView13)), uintptr(unsafe.Pointer(&view)))
	if int32(hr) < 0 {
		return nil, hresultError("query WebView2 profile interface", hr)
	}
	if view == nil {
		return nil, errors.New("WebView2 returned a nil profile interface")
	}
	defer view.vtbl.Release.Call(uintptr(unsafe.Pointer(view)))
	var profile *colorProfile
	hr, _, _ = view.vtbl.GetProfile.Call(uintptr(unsafe.Pointer(view)), uintptr(unsafe.Pointer(&profile)))
	runtime.KeepAlive(e)
	if int32(hr) < 0 {
		return nil, hresultError("get WebView2 profile", hr)
	}
	if profile == nil {
		return nil, errors.New("WebView2 returned a nil profile")
	}
	return profile, nil
}

// SetPreferredColorSchemeChecked synchronizes built-in UI (including context
// menus) without replacing or disabling it. Call only on the COM UI thread.
func (e *Chromium) SetPreferredColorSchemeChecked(dark bool) error {
	profile, err := e.colorProfileChecked()
	if err != nil {
		return err
	}
	defer profile.vtbl.Release.Call(uintptr(unsafe.Pointer(profile)))
	// COREWEBVIEW2_PREFERRED_COLOR_SCHEME: AUTO=0, LIGHT=1, DARK=2.
	scheme := uintptr(1)
	if dark {
		scheme = 2
	}
	hr, _, _ := profile.vtbl.PutPreferredColorScheme.Call(uintptr(unsafe.Pointer(profile)), scheme)
	if int32(hr) < 0 {
		return hresultError("set WebView2 preferred color scheme", hr)
	}
	return nil
}

// GetPreferredColorSchemeChecked reads the native profile, not page CSS.
// Call on the COM UI thread. Auto/unknown is not reported as explicit light.
func (e *Chromium) GetPreferredColorSchemeChecked() (bool, error) {
	profile, err := e.colorProfileChecked()
	if err != nil {
		return false, err
	}
	defer profile.vtbl.Release.Call(uintptr(unsafe.Pointer(profile)))
	var scheme uint32
	hr, _, _ := profile.vtbl.GetPreferredColorScheme.Call(uintptr(unsafe.Pointer(profile)), uintptr(unsafe.Pointer(&scheme)))
	if int32(hr) < 0 {
		return false, hresultError("get WebView2 preferred color scheme", hr)
	}
	if scheme != 1 && scheme != 2 {
		return false, errors.New("WebView2 color scheme is not explicitly light or dark")
	}
	return scheme == 2, nil
}
