//go:build windows

package desktophost

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/LE-saber/Local-Probe/internal/desktopweb"
	"golang.org/x/sys/windows"
)

func TestWindowThemeStrictRequest(t *testing.T) {
	for _, theme := range []string{"dark", "light"} {
		got, handled, err := parseWindowTheme(`{"id":"theme-1","method":"window.theme","params":{"theme":"` + theme + `"}}`)
		if err != nil || !handled || got != theme {
			t.Fatalf("theme %s: %s %v %v", theme, got, handled, err)
		}
	}
	for _, raw := range []string{
		`{"id":"1","method":"window.theme","params":{"theme":"auto"}}`,
		`{"id":"1","method":"window.theme","params":{"theme":"DARK"}}`,
		`{"id":"1","method":"window.theme","params":{"theme":null}}`,
		`{"id":2,"method":"window.theme","params":{"theme":"dark"}}`,
		`{"method":"window.theme","params":{"theme":"dark"}}`,
		`{"id":"1","method":"window.theme","params":{"theme":"dark","command":"x"}}`,
		`{"id":"1","method":"window.theme","params":{"theme":"dark"},"path":"x"}`,
	} {
		if _, handled, err := parseWindowTheme(raw); !handled || err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, handled, _ := parseWindowTheme(`{"id":"1","method":"snapshot","params":{}}`); handled {
		t.Fatal("intercepted backend RPC")
	}
	if got := themeColor(0xf2f0ec); got != 0xecf0f2 {
		t.Fatalf("COLORREF: %x", got)
	}
}

func TestWindowThemeRejectsUntrustedSourceBeforeNativeCalls(t *testing.T) {
	h := &hostWindow{accepting: true}
	h.trusted.Store(true)
	// No HWND or WebView: any attempted native theme call would fail/panic.
	for _, source := range []string{"https://evil.invalid/", desktopweb.DocumentURL + "#x"} {
		h.acceptMessage(`{"id":"1","method":"window.theme","params":{"theme":"dark"}}`, source, desktopweb.DocumentURL)
		h.acceptMessage(`{"id":"1","method":"window.theme","params":{"theme":"dark"}}`, desktopweb.DocumentURL, source)
	}
}

func TestWindowThemeNativeFrameRoundTrip(t *testing.T) {
	if windows.RtlGetVersion().BuildNumber < 22000 {
		t.Skip("caption/text COLORREF attributes require Windows 11")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := registerHostWindowClass(); err != nil {
		t.Fatal(err)
	}
	instance, _, _ := procGetModuleHandleWHost.Call(0)
	class, title := wideHost("LocalProbeDesktopWindow"), wideHost("Theme regression (hidden)")
	hwnd, _, _ := procCreateWindowExWHost.Call(wsExAppWindow, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), wsOverlappedWindow, 0, 0, 320, 240, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatal("hidden test window unavailable")
	}
	defer procDestroyWindowHost.Call(hwnd)
	get := windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
	for _, dark := range []bool{true, false, true} {
		if err := setWindowFrameTheme(windows.HWND(hwnd), dark); err != nil {
			t.Fatal(err)
		}
		// The documented caption/border/text attributes are setters only;
		// DwmGetWindowAttribute rejects them. Read back the dark-mode BOOL.
		want := map[uint32]uint32{20: 0}
		if dark {
			want[20] = 1
		}
		for attr, color := range want {
			var actual uint32
			hr, _, _ := get.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&actual)), 4)
			if int32(hr) < 0 || actual != color {
				t.Fatalf("dark=%v attr=%d got=%x want=%x hr=%x", dark, attr, actual, color, hr)
			}
		}
	}
}
