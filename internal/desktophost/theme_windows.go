//go:build windows

package desktophost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/sys/windows"
)

var dwmSetWindowAttributeTheme = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// A cosmetic UI-thread request, not a backend permission or lifecycle operation.
func parseWindowTheme(raw string) (string, bool, error) {
	var route struct {
		Method string `json:"method"`
	}
	if json.Unmarshal([]byte(raw), &route) != nil || route.Method != "window.theme" {
		return "", false, nil
	}
	var request struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			Theme string `json:"theme"`
		} `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 64<<10 || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.ID == "" || len(request.ID) > 128 || (request.Params.Theme != "dark" && request.Params.Theme != "light") {
		return "", true, errors.New("invalid window theme request")
	}
	return request.Params.Theme, true, nil
}

// COLORREF is 0x00BBGGRR, not the CSS RGB integer.
func themeColor(rgb uint32) uint32 { return (rgb&0xff)<<16 | rgb&0xff00 | rgb>>16&0xff }

func setWindowFrameTheme(hwnd windows.HWND, dark bool) error {
	if err := dwmSetWindowAttributeTheme.Find(); err != nil {
		return err
	}
	values := []struct{ attribute, value uint32 }{{20, 0}, {34, themeColor(0xe2e3da)}, {35, themeColor(0xffffff)}, {36, themeColor(0x23261f)}}
	if dark {
		values = []struct{ attribute, value uint32 }{{20, 1}, {34, themeColor(0x3c3c3c)}, {35, themeColor(0x222222)}, {36, themeColor(0xf2f0ec)}}
	}
	var failures []error
	for _, setting := range values {
		hr, _, _ := dwmSetWindowAttributeTheme.Call(uintptr(hwnd), uintptr(setting.attribute), uintptr(unsafe.Pointer(&setting.value)), 4)
		if int32(hr) < 0 {
			failures = append(failures, fmt.Errorf("window theme attribute %d (HRESULT 0x%08X)", setting.attribute, uint32(hr)))
		}
	}
	return errors.Join(failures...)
}

func (h *hostWindow) applyWindowTheme(theme string) error {
	// Both are invoked from trusted WebMessageReceived on the native STA thread.
	frameErr := setWindowFrameTheme(h.hwnd, theme == "dark")
	menuErr := h.web.SetPreferredColorSchemeChecked(theme == "dark")
	if menuErr == nil {
		actual, err := h.web.GetPreferredColorSchemeChecked()
		if err != nil {
			menuErr = err
		} else if actual != (theme == "dark") {
			menuErr = errors.New("WebView2 did not apply the requested menu theme")
		}
	}
	return errors.Join(frameErr, menuErr)
}
