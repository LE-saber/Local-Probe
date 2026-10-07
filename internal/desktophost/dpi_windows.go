//go:build windows

package desktophost

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ErrDPIUnsupported = errors.New("per-monitor-v2 DPI is required; remove compatibility DPI overrides and use Windows 10 1703 or newer")

var (
	setProcessDPIContext = user32Host.NewProc("SetProcessDpiAwarenessContext")
	getThreadDPIContext  = user32Host.NewProc("GetThreadDpiAwarenessContext")
	areDPIContextsEqual  = user32Host.NewProc("AreDpiAwarenessContextsEqual")
	getDPIForWindow      = user32Host.NewProc("GetDpiForWindow")
	getWindowRectDPI     = user32Host.NewProc("GetWindowRect")
	setWindowPosDPI      = user32Host.NewProc("SetWindowPos")
	monitorFromWindowDPI = user32Host.NewProc("MonitorFromWindow")
	getMonitorInfoDPI    = user32Host.NewProc("GetMonitorInfoW")
)

const dpiPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)

// lParam is native callback memory, not a Go pointer. Copy its bounded RECT
// through the OS instead of reinterpreting uintptr as a GC-managed pointer.
// Failed native reads are rejected rather than dereferenced.
func dpiSuggestedRect(address uintptr) (hostRect, error) {
	var rect hostRect
	var copied uintptr
	size := unsafe.Sizeof(rect)
	if address == 0 {
		return rect, ErrDPIUnsupported
	}
	err := windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&rect)), size, &copied)
	if err != nil || copied != size {
		return hostRect{}, ErrDPIUnsupported
	}
	return rect, nil
}

func enablePerMonitorV2() error {
	for _, proc := range []*windows.LazyProc{setProcessDPIContext, getThreadDPIContext, areDPIContextsEqual, getDPIForWindow} {
		if proc.Find() != nil {
			return ErrDPIUnsupported
		}
	}
	// A manifest may already have set PMv2. Verify the effective context,
	// rather than treating ERROR_ACCESS_DENIED as successful initialization.
	setProcessDPIContext.Call(dpiPerMonitorV2)
	current, _, _ := getThreadDPIContext.Call()
	equal, _, _ := areDPIContextsEqual.Call(current, dpiPerMonitorV2)
	if equal == 0 {
		return ErrDPIUnsupported
	}
	return nil
}

func dpiPixels(logical int32, dpi uint32) int32 {
	return int32((int64(logical)*int64(dpi) + 48) / 96)
}

func sizeInitialDPIWindow(hwnd windows.HWND) error {
	dpi, _, _ := getDPIForWindow.Call(uintptr(hwnd))
	if dpi == 0 {
		return ErrDPIUnsupported
	}
	var rect hostRect
	if ok, _, _ := getWindowRectDPI.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return ErrDPIUnsupported
	}
	rect.Right = rect.Left + dpiPixels(1240, uint32(dpi))
	rect.Bottom = rect.Top + dpiPixels(800, uint32(dpi))
	// Fit small laptop work areas by changing the viewport, never by shrinking
	// a bitmap or applying a CSS transform.
	var info struct {
		Size          uint32
		Monitor, Work hostRect
		Flags         uint32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	monitor, _, _ := monitorFromWindowDPI.Call(uintptr(hwnd), 2)
	if ok, _, _ := getMonitorInfoDPI.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return ErrDPIUnsupported
	}
	work := info.Work
	width, height := rect.Right-rect.Left, rect.Bottom-rect.Top
	if width > work.Right-work.Left {
		width = work.Right - work.Left
	}
	if height > work.Bottom-work.Top {
		height = work.Bottom - work.Top
	}
	if rect.Left < work.Left {
		rect.Left = work.Left
	}
	if rect.Top < work.Top {
		rect.Top = work.Top
	}
	if rect.Left+width > work.Right {
		rect.Left = work.Right - width
	}
	if rect.Top+height > work.Bottom {
		rect.Top = work.Bottom - height
	}
	rect.Right = rect.Left + width
	rect.Bottom = rect.Top + height
	return applyDPIWindowRect(hwnd, rect)
}

func applyDPIWindowRect(hwnd windows.HWND, rect hostRect) error {
	width, height := int64(rect.Right)-int64(rect.Left), int64(rect.Bottom)-int64(rect.Top)
	if width <= 0 || height <= 0 || width > 1<<31-1 || height > 1<<31-1 {
		return ErrDPIUnsupported
	}
	// Coordinates may be negative for monitors left/above the primary display.
	ok, _, _ := setWindowPosDPI.Call(uintptr(hwnd), 0, uintptr(rect.Left), uintptr(rect.Top), uintptr(width), uintptr(height), 0x0004|0x0010)
	if ok == 0 {
		return ErrDPIUnsupported
	}
	return nil
}
