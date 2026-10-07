//go:build windows

package desktophost

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/LE-saber/Local-Probe/internal/desktopweb"
	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

func TestDPIPixelMatrix(t *testing.T) {
	if _, err := dpiSuggestedRect(1); err == nil {
		t.Fatal("invalid native pointer was accepted")
	}
	for _, dpi := range []uint32{96, 120, 144, 168, 192, 240, 288} {
		if got := dpiPixels(96, dpi); got != int32(dpi) {
			t.Fatalf("dpi %d: %d", dpi, got)
		}
		if got := dpiPixels(800, dpi); got != int32(800*int64(dpi)/96) {
			t.Fatal("rounding drift")
		}
	}
}

func TestDPIHiddenWindowIntegration(t *testing.T) {
	// Awareness is process-wide and one-time. Isolate this from all other
	// tests and user processes; this window is never shown.
	if os.Getenv("LOCAL_PROBE_DPI_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDPIHiddenWindowIntegration$", "-test.v")
		cmd.Env = append(os.Environ(), "LOCAL_PROBE_DPI_TEST_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("DPI child: %v\n%s", err, out)
		}
		t.Logf("isolated DPI verification:\n%s", out)
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := enablePerMonitorV2(); err != nil {
		t.Fatal(err)
	}
	if err := enablePerMonitorV2(); err != nil {
		t.Fatal("already PMv2 must verify, not fail", err)
	}
	if err := registerHostWindowClass(); err != nil {
		t.Fatal(err)
	}
	instance, _, _ := procGetModuleHandleWHost.Call(0)
	class, title := wideHost("LocalProbeDesktopWindow"), wideHost("DPI integration (hidden)")
	hwnd, _, _ := procCreateWindowExWHost.Call(wsExAppWindow, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), wsOverlappedWindow, 0x80000000, 0x80000000, 640, 480, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatal("hidden window unavailable")
	}
	defer procDestroyWindowHost.Call(hwnd)
	h := &hostWindow{hwnd: windows.HWND(hwnd)}
	context, _, _ := user32Host.NewProc("GetWindowDpiAwarenessContext").Call(hwnd)
	if same, _, _ := areDPIContextsEqual.Call(context, dpiPerMonitorV2); same == 0 {
		t.Fatal("HWND did not inherit PMv2")
	}
	if err := sizeInitialDPIWindow(h.hwnd); err != nil {
		t.Fatal(err)
	}
	for _, dpi := range []uint32{96, 120, 144, 168, 192, 240, 288, 96} {
		rect := hostRect{Left: 40, Top: 50, Right: 40 + dpiPixels(600, dpi), Bottom: 50 + dpiPixels(400, dpi)}
		if result := h.handleMessage(wmDPIChanged, uintptr(dpi)|uintptr(dpi)<<16, uintptr(unsafe.Pointer(&rect))); result != 0 {
			t.Fatal("WM_DPICHANGED result")
		}
		var actual hostRect
		if ok, _, _ := getWindowRectDPI.Call(hwnd, uintptr(unsafe.Pointer(&actual))); ok == 0 || actual != rect {
			t.Fatalf("suggested physical rect %d: %+v != %+v", dpi, actual, rect)
		}
	}
	if applyDPIWindowRect(h.hwnd, hostRect{}) == nil {
		t.Fatal("invalid rectangle accepted")
	}
	if os.Getenv("LOCAL_PROBE_DPI_WEBVIEW") == "1" {
		checkDPIWebView(t, h)
	}
}

// Opt-in rendering verification against the installed Runtime, with a fresh
// test-only browser profile, no real config/RPC/lifecycle and a hidden HWND.
func checkDPIWebView(t *testing.T, h *hostWindow) {
	t.Helper()
	if hr, _, _ := procCoInitializeExHost.Call(0, 2); int32(hr) < 0 {
		t.Fatal("COM initialization")
	}
	defer procCoUninitializeHost.Call()
	if err := webviewloader.PrepareSecureDllSearch(); err != nil {
		t.Fatal(err)
	}
	h.web = edge.NewChromium()
	h.web.DataPath = t.TempDir()
	h.accepting = true
	var measurement struct {
		Probe         string  `json:"probe"`
		DPR           float64 `json:"dpr"`
		Width, Height float64
	}
	got := false
	gotTheme := false
	err := h.web.ConfigureSecureHost(edge.SecureHostCallbacks{
		DocumentURL: desktopweb.DocumentURL, ResourceRequested: h.resourceRequested,
		NavigationStarting: func(uri string) bool { return uri == desktopweb.DocumentURL },
		NavigationCompleted: func(source string, success bool) {
			if success && source == desktopweb.DocumentURL {
				h.trusted.Store(true)
				h.postTrustedMessage(`{"event":"window.ready"}`)
				h.web.Eval(`window.chrome.webview.postMessage(JSON.stringify({probe:"dpi",dpr:devicePixelRatio,width:innerWidth,height:innerHeight}))`)
			}
		},
		MessageReceived: func(raw, source, top string) {
			if source == desktopweb.DocumentURL && top == desktopweb.DocumentURL {
				if _, handled, err := parseWindowTheme(raw); handled && err == nil {
					h.acceptMessage(raw, source, top)
					gotTheme = true
				}
				var value = measurement
				if json.Unmarshal([]byte(raw), &value) == nil && value.Probe == "dpi" {
					measurement = value
					got = true
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !h.web.Embed(uintptr(h.hwnd)) || h.web.InitError() != nil {
		t.Fatal("WebView2 embed failed", h.web.InitError())
	}
	defer func() {
		if err := h.web.CloseSecureHost(); err != nil {
			t.Error(err)
		}
		// Runtime teardown is asynchronous; browser metrics may still be mapped
		// briefly after COM release. Remove only our fresh test profile, never
		// a user's Desktop/WebView2 directory or any process by name.
		deadline := time.Now().Add(8 * time.Second)
		for {
			err := os.RemoveAll(h.web.DataPath)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Error("test browser profile cleanup", err)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	if err := h.web.ConfigureSecureSettings(); err != nil {
		t.Fatal(err)
	}
	// This opt-in test uses its own hidden HWND/profile, never the user's GUI.
	for _, dark := range []bool{true, false, true} {
		if err := h.web.SetPreferredColorSchemeChecked(dark); err != nil {
			t.Fatal("native menu theme setter", err)
		}
		actual, err := h.web.GetPreferredColorSchemeChecked()
		if err != nil || actual != dark {
			t.Fatalf("native profile theme: want dark=%v, got=%v err=%v", dark, actual, err)
		}
	}
	t.Log("actual WebView2 profile preferred color scheme dark → light → dark verified")
	// Start opposite to the document's default theme: only a real page RPC
	// after window.ready can change it back. There is no backend snapshot reply.
	if err := h.web.SetPreferredColorSchemeChecked(false); err != nil {
		t.Fatal(err)
	}
	h.layoutWebView()
	if err := h.web.NavigateChecked(desktopweb.DocumentURL); err != nil {
		t.Fatal(err)
	}
	peek := user32Host.NewProc("PeekMessageW")
	deadline := time.Now().Add(12 * time.Second)
	for (!got || !gotTheme) && time.Now().Before(deadline) {
		var msg hostMessage
		if ok, _, _ := peek.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1); ok != 0 {
			procTranslateMessageHost.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageHost.Call(uintptr(unsafe.Pointer(&msg)))
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !got {
		t.Fatal("no rendering metrics from WebView2")
	}
	if actual, err := h.web.GetPreferredColorSchemeChecked(); !gotTheme || err != nil || !actual {
		t.Fatalf("real window.ready → frontend theme RPC → native profile failed: received=%v dark=%v err=%v", gotTheme, actual, err)
	}
	t.Log("real page restored dark native theme through window.ready and production RPC without backend snapshot")
	dpi, _, _ := getDPIForWindow.Call(uintptr(h.hwnd))
	var rect hostRect
	procGetClientRectHost.Call(uintptr(h.hwnd), uintptr(unsafe.Pointer(&rect)))
	if math.Abs(measurement.DPR-float64(dpi)/96) > .01 || math.Abs(measurement.Width*measurement.DPR-float64(rect.Right-rect.Left)) > 2 || math.Abs(measurement.Height*measurement.DPR-float64(rect.Bottom-rect.Top)) > 2 {
		t.Fatalf("raster mismatch: dpi=%d metrics=%+v physical=%+v", dpi, measurement, rect)
	}
	t.Logf("actual WebView2 DPI=%d DPR=%g viewport=%gx%g matches physical pixels", dpi, measurement.DPR, measurement.Width, measurement.Height)
}
