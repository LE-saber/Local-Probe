//go:build windows

package previewui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmAppRefreshDone     = uint32(0x8000 + 20)
	wmAppExportDone      = uint32(0x8000 + 21)
	wmAppTrayExit        = uint32(0x8000 + 22)
	trayCallback         = uint32(0x8000 + 23)
	wmAppConnectDone     = uint32(0x8000 + 24)
	wmAppConnectProgress = uint32(0x8000 + 25)

	wmCreate        = 0x0001
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmPaint         = 0x000f
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmSetFont       = 0x0030
	wmCommand       = 0x0111
	wmNotify        = 0x004e
	wmContextMenu   = 0x007b
	wmLButtonDown   = 0x0201
	wmDPIChanged    = 0x02e0
	wmMouseMove     = 0x0200
	wmMouseWheel    = 0x020a
	wmKeyDown       = 0x0100
	wmRButtonUp     = 0x0205
	wmLButtonDblClk = 0x0203
	wmUser          = 0x0400

	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsTabStop       = 0x00010000
	wsBorder        = 0x00800000
	wsVScroll       = 0x00200000
	wsHScroll       = 0x00100000
	wsOverlapped    = 0x00cf0000
	wsExClientEdge  = 0x00000200
	wsExAppWindow   = 0x00040000
	editMultiline   = 0x00000004
	editAutoVScroll = 0x00000040
	editAutoHScroll = 0x00000080
	editReadOnly    = 0x00000800
	bsPushButton    = 0x00000000
	bsDisabled      = 0x00000800

	showHide    = 0
	showNormal  = 1
	showShow    = 5
	colorWindow = 5

	defaultGUIFont = 17
	idcArrow       = 32512
	idiApplication = 32512

	tabInsertItem = wmUser + 62
	tabGetCurSel  = wmUser + 11
	tabSetCurSel  = wmUser + 12
	tabIfText     = 0x0001
	tcnSelChange  = -551

	menuString       = 0x00000000
	menuChecked      = 0x00000008
	menuPopup        = 0x00000010
	menuRadioCheck   = 0x00000200
	menuSeparator    = 0x00000800
	menuByCommand    = 0x00000000
	trackRightButton = 0x00000002
	trackReturnCmd   = 0x00000100

	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004
	notifyVersion = 4
	ninSelect     = 0x00000400

	ofNExplorer        = 0x00080000
	ofNPathMustExist   = 0x00000800
	ofNNoChangeDir     = 0x00000008
	ofNOverwritePrompt = 0x00000002
	ofNHideReadOnly    = 0x00000004
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procShowWindow             = user32.NewProc("ShowWindow")
	procUpdateWindow           = user32.NewProc("UpdateWindow")
	procMoveWindow             = user32.NewProc("MoveWindow")
	procGetClientRect          = user32.NewProc("GetClientRect")
	procSetWindowTextW         = user32.NewProc("SetWindowTextW")
	procEnableWindow           = user32.NewProc("EnableWindow")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procInvalidateRect         = user32.NewProc("InvalidateRect")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procLoadCursorW            = user32.NewProc("LoadCursorW")
	procLoadIconW              = user32.NewProc("LoadIconW")
	procGetSysColorBrush       = user32.NewProc("GetSysColorBrush")
	procMessageBoxW            = user32.NewProc("MessageBoxW")
	procSetProcessDPIAware     = user32.NewProc("SetProcessDPIAware")
	procSetProcessDPIContext   = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetStockObject         = gdi32.NewProc("GetStockObject")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procCheckMenuRadioItem     = user32.NewProc("CheckMenuRadioItem")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
	procInitCommonControlsEx   = comctl32.NewProc("InitCommonControlsEx")
	procGetSaveFileNameW       = comdlg32.NewProc("GetSaveFileNameW")
	procBeginPaint             = user32.NewProc("BeginPaint")
	procEndPaint               = user32.NewProc("EndPaint")
	procDrawTextW              = user32.NewProc("DrawTextW")
	procFillRect               = user32.NewProc("FillRect")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procRectangle              = gdi32.NewProc("Rectangle")
	procEllipse                = gdi32.NewProc("Ellipse")
	procMoveToEx               = gdi32.NewProc("MoveToEx")
	procLineTo                 = gdi32.NewProc("LineTo")
	procPolygon                = gdi32.NewProc("Polygon")
	procCreateFontW            = gdi32.NewProc("CreateFontW")
)

type winPoint struct {
	X int32
	Y int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type paintStruct struct {
	Hdc         windows.Handle
	Erase       int32
	Paint       winRect
	Restore     int32
	Incremental int32
	Reserved    [32]byte
}

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	WndProc       uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	MenuName      *uint16
	ClassName     *uint16
	HIconSm       windows.Handle
}

type winMessage struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
	Private uint32
}

type notifyHeader struct {
	HwndFrom windows.HWND
	IDFrom   uintptr
	Code     int32
}

type tabItemW struct {
	Mask      uint32
	State     uint32
	StateMask uint32
	Text      *uint16
	TextMax   int32
	Image     int32
	Param     uintptr
}

type initCommonControlsEx struct {
	Size uint32
	ICC  uint32
}

type notifyIconDataW struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	Guid             [16]byte
	BalloonIcon      windows.Handle
}

type openFileNameW struct {
	StructSize      uint32
	Owner           windows.HWND
	Instance        windows.Handle
	Filter          *uint16
	CustomFilter    *uint16
	MaxCustomFilter uint32
	FilterIndex     uint32
	File            *uint16
	MaxFile         uint32
	FileTitle       *uint16
	MaxFileTitle    uint32
	InitialDir      *uint16
	Title           *uint16
	Flags           uint32
	FileOffset      uint16
	FileExtension   uint16
	DefaultExt      *uint16
	CustData        uintptr
	Hook            uintptr
	TemplateName    *uint16
	Reserved        unsafe.Pointer
	Reserved2       uint32
	FlagsEx         uint32
}

type previewWindow struct {
	model     ViewModel
	connector Connector
	title     string

	hwnd      windows.HWND
	tab       windows.HWND
	edit      windows.HWND
	status    windows.HWND
	refresh   windows.HWND
	export    windows.HWND
	start     windows.HWND
	stop      windows.HWND
	reconnect windows.HWND
	gate      windows.HWND
	font      windows.Handle

	section         Section
	current         Snapshot
	connectionState ConnectionControlState
	statusText      string
	hoverNav        int
	scroll          int
	fontScale       FontScale

	mu                     sync.Mutex
	refreshing             bool
	pendingSnapshot        Snapshot
	pendingRefreshErr      bool
	exporting              bool
	pendingExport          []byte
	pendingExportErr       bool
	pendingConnect         ConnectionResult
	pendingConnectProgress ConnectionResult
	exiting                bool

	mutex          windows.Handle
	tray           bool
	icon           windows.Handle
	iconOwned      bool
	taskbarCreated uint32
}

var (
	windowClassOnce sync.Once
	windowClassErr  error
	windowInstances sync.Map // map[windows.HWND]*previewWindow

	// Painting is confined to the Win32 message-loop thread, but keeping the
	// active value atomic also makes the drawText helper safe if that invariant
	// is ever exercised by a diagnostic/test caller.
	activeRenderFontScale uint32 = uint32(DefaultFontScale)
)

const (
	idTab       = 1001
	idEdit      = 1002
	idStatus    = 1003
	idRefresh   = 1004
	idExport    = 1005
	idStart     = 1006
	idStop      = 1007
	idReconnect = 1008
	idGate      = 1009
	idConnect   = 1010
)

const (
	sidebarWidth = 184
	navTop       = 126
	navRowHeight = 52
	contentInset = 32

	vkUp       = 0x26
	vkDown     = 0x28
	vkPageUp   = 0x21
	vkPageDown = 0x22

	dtLeft        = 0x00000000
	dtTop         = 0x00000000
	dtWordBreak   = 0x00000010
	dtSingleLine  = 0x00000020
	dtVCenter     = 0x00000004
	dtEndEllipsis = 0x00008000
	dtNoPrefix    = 0x00000800

	fontNormal = 400
	fontMedium = 550
	fontBold   = 700
)

// Run starts the single-threaded native message loop.  All model work is
// dispatched to a worker goroutine; Win32 controls are touched only from this
// message-loop goroutine.
func Run(options RunOptions) error {
	// A Win32 window and its message queue belong to the OS thread that
	// created them. Without pinning this goroutine, the Go scheduler may move
	// GetMessage to another thread and leave the visible window unresponsive.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if options.Model == nil {
		options.Model = UnconfiguredModel{}
	}
	if options.Title == "" {
		options.Title = "Local-Probe Preview"
	}
	mutex, already, err := acquirePreviewMutex()
	if err != nil {
		return err
	}
	if already {
		return ErrAlreadyRunning
	}
	defer closeWindowsHandle(mutex)

	setDPIAwareness()
	if err := registerPreviewClass(); err != nil {
		return err
	}
	if err := initCommonControls(); err != nil {
		return err
	}
	instance, err := moduleHandle()
	if err != nil {
		return err
	}
	title := wideString(options.Title)
	className := wideString("LocalProbePreviewWindow")
	hwndValue, _, callErr := procCreateWindowExW.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlapped,
		0x80000000, 0x80000000, 1180, 760,
		0, 0, uintptr(instance), 0,
	)
	if hwndValue == 0 {
		if callErr != nil {
			return callErr
		}
		return errors.New("preview window creation failed")
	}
	app := &previewWindow{
		model:      options.Model,
		connector:  options.Connector,
		title:      options.Title,
		hwnd:       windows.HWND(hwndValue),
		section:    SectionOverview,
		current:    sanitizeSnapshot(unconfiguredSnapshot()),
		statusText: "Loading local status...",
		hoverNav:   -1,
		fontScale:  DefaultFontScale,
		mutex:      mutex,
	}
	windowInstances.Store(app.hwnd, app)
	if err := app.initControls(instance); err != nil {
		windowInstances.Delete(app.hwnd)
		_, _, _ = procDestroyWindow.Call(hwndValue)
		return err
	}
	app.taskbarCreated = registerTaskbarMessage()
	app.addTrayIcon()
	app.layout()
	_, _, _ = procShowWindow.Call(hwndValue, showShow)
	_, _, _ = procUpdateWindow.Call(hwndValue)
	app.refreshAsync()

	var msg winMessage
	for {
		result, _, getErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == 0 {
			break
		}
		if int32(result) == -1 {
			if getErr != nil {
				return getErr
			}
			return errors.New("preview message loop failed")
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func registerPreviewClass() error {
	windowClassOnce.Do(func() {
		instance, err := moduleHandle()
		if err != nil {
			windowClassErr = err
			return
		}
		className := wideString("LocalProbePreviewWindow")
		// Class icons live for the lifetime of the registered class and are
		// reclaimed by Windows when the Preview process exits.
		icon, iconErr := loadPreviewIcon(32)
		if iconErr != nil {
			icon, _, _ = procLoadIconW.Call(0, idiApplication)
		}
		smallIcon, smallIconErr := loadPreviewIcon(16)
		if smallIconErr != nil {
			smallIcon = icon
		}
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		brush, _, _ := procGetSysColorBrush.Call(colorWindow)
		class := wndClassExW{CbSize: uint32(unsafe.Sizeof(wndClassExW{})), Style: 0x0003, WndProc: syscall.NewCallback(previewWindowProc), HInstance: instance, HIcon: windows.Handle(icon), HCursor: windows.Handle(cursor), HbrBackground: windows.Handle(brush), ClassName: className, HIconSm: windows.Handle(smallIcon)}
		atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 && callErr != nil && !errors.Is(callErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
			windowClassErr = callErr
		}
	})
	return windowClassErr
}

func initCommonControls() error {
	controls := initCommonControlsEx{Size: uint32(unsafe.Sizeof(initCommonControlsEx{})), ICC: 0x00000008}
	result, _, err := procInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&controls)))
	if result == 0 {
		return err
	}
	return nil
}

func moduleHandle() (windows.Handle, error) {
	value, _, err := procGetModuleHandleW.Call(0)
	if value == 0 {
		return 0, err
	}
	return windows.Handle(value), nil
}

func acquirePreviewMutex() (windows.Handle, bool, error) {
	name := wideString("Local\\Local-Probe-Preview")
	handle, err := windows.CreateMutex(nil, true, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		closeWindowsHandle(handle)
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	return handle, false, nil
}

func closeWindowsHandle(handle windows.Handle) {
	if handle != 0 {
		_, _, _ = procCloseHandle.Call(uintptr(handle))
	}
}

func setDPIAwareness() {
	// Per-monitor-v2 is best effort.  The fallback is available on older
	// Windows versions and still prevents bitmap scaling of the controls.
	if result, _, _ := procSetProcessDPIContext.Call(uintptr(^uint(3))); result != 0 {
		return
	}
	_, _, _ = procSetProcessDPIAware.Call()
}

func (a *previewWindow) initControls(instance windows.Handle) error {
	if a == nil || a.hwnd == 0 {
		return errors.New("invalid preview window")
	}
	// The Preview is intentionally drawn as one native surface.  This keeps
	// the layout stable across DPI settings and lets the cards/navigation share
	// one paint pass without introducing a webview or a third-party toolkit.
	_ = instance
	return nil
}

func createChild(className, title string, style uint32, x, y, width, height int, parent windows.HWND, id int, instance windows.Handle) windows.HWND {
	class := wideString(className)
	caption := wideString(title)
	value, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(caption)), uintptr(style), uintptr(x), uintptr(y), uintptr(width), uintptr(height), uintptr(parent), uintptr(id), uintptr(instance), 0)
	return windows.HWND(value)
}

func getStockFont() uintptr {
	value, _, _ := procGetStockObject.Call(defaultGUIFont)
	return value
}

func previewWindowProc(hwndValue uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	hwnd := windows.HWND(hwndValue)
	if value, ok := windowInstances.Load(hwnd); ok {
		return value.(*previewWindow).proc(msg, wParam, lParam)
	}
	return defWindowProc(hwnd, msg, wParam, lParam)
}

func (a *previewWindow) proc(msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmSize, wmDPIChanged:
		a.layout()
	case wmPaint:
		a.paint()
		return 0
	case wmEraseBkgnd:
		// The complete client area is repainted by WM_PAINT.  Returning non-zero
		// avoids a white erase flash while the cards are being redrawn.
		return 1
	case wmCommand:
		a.command(uint32(wParam & 0xffff))
	case wmNotify:
		// No child control payload is trusted or dereferenced.  Navigation is
		// handled by the painted client surface below.
	case wmLButtonDown:
		a.mouseDown(pointFromLParam(lParam))
		return 0
	case wmMouseMove:
		a.mouseMove(pointFromLParam(lParam))
		return 0
	case wmMouseWheel:
		a.mouseWheel(int16(uint16((wParam >> 16) & 0xffff)))
		return 0
	case wmKeyDown:
		a.keyDown(uint32(wParam))
		return 0
	case wmClose:
		// Closing the window is deliberately a hide-to-tray operation.
		a.hideWindow()
		return 0
	case trayCallback:
		a.trayMessage(uint32(lParam))
	case wmContextMenu:
		a.showTrayMenu()
	case wmLButtonDblClk:
		a.showWindow()
	case wmAppRefreshDone:
		a.finishRefresh()
	case wmAppExportDone:
		a.finishExport()
	case wmAppConnectProgress:
		a.finishConnectProgress()
	case wmAppConnectDone:
		a.finishConnect()
	case wmAppTrayExit:
		a.exit()
	case wmDestroy:
		a.removeTrayIcon()
		windowInstances.Delete(a.hwnd)
		_, _, _ = procPostQuitMessage.Call(0)
	}
	if a.taskbarCreated != 0 && msg == a.taskbarCreated {
		a.addTrayIcon()
	}
	return defWindowProc(a.hwnd, msg, wParam, lParam)
}

func defWindowProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	value, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return value
}

func (a *previewWindow) command(id uint32) {
	switch id {
	case idRefresh, menuRefresh:
		a.refreshAsync()
	case idExport, menuExport:
		a.exportAsync()
	case idConnect:
		a.connectAsync()
	case menuOpen:
		a.toggleWindow()
	case menuAbout:
		a.showWindow()
		a.section = SectionAbout
		_, _, _ = procSendMessageW.Call(uintptr(a.tab), tabSetCurSel, uintptr(SectionAbout), 0)
		a.render()
	case menuFont100, menuFont125, menuFont150, menuFont175, menuFont200:
		if scale, ok := trayFontScaleForCommand(id); ok {
			a.setFontScale(scale)
		}
	case menuExit:
		a.exit()
	}
}

func (a *previewWindow) layout() {
	if a != nil && a.hwnd != 0 {
		invalidate(a.hwnd)
	}
}

// previewLayout is deliberately small and deterministic.  It is used both by
// the painter and hit testing so a high-DPI resize cannot move a clickable
// control away from the thing the user sees.
type previewLayout struct {
	width       int
	height      int
	mainLeft    int
	mainRight   int
	headerTop   int
	fontRect    winRect
	connectRect winRect
	refreshRect winRect
	exportRect  winRect
	navRects    [5]winRect
}

func makePreviewLayout(width, height int) previewLayout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	mainLeft := sidebarWidth + contentInset
	mainRight := width - contentInset
	if mainRight < mainLeft+120 {
		mainRight = mainLeft + 120
	}
	result := previewLayout{
		width:     width,
		height:    height,
		mainLeft:  mainLeft,
		mainRight: mainRight,
		headerTop: 28,
	}
	for index := range result.navRects {
		y := navTop + index*navRowHeight
		result.navRects[index] = rectInt(12, y, sidebarWidth-12, y+navRowHeight-4)
	}
	const (
		fontButtonWidth    = 152
		connectButtonWidth = 156
		refreshButtonWidth = 104
		exportButtonWidth  = 116
		headerButtonGap    = 12
	)
	exportRight := mainRight
	result.exportRect = rectInt(exportRight-exportButtonWidth, 28, exportRight, 68)
	refreshRight := int(result.exportRect.Left) - headerButtonGap
	result.refreshRect = rectInt(refreshRight-refreshButtonWidth, 28, refreshRight, 68)
	fontRight := int(result.refreshRect.Left) - headerButtonGap
	result.fontRect = rectInt(fontRight-fontButtonWidth, 28, fontRight, 68)
	connectRight := int(result.fontRect.Left) - headerButtonGap
	result.connectRect = rectInt(connectRight-connectButtonWidth, 28, connectRight, 68)
	return result
}

func rectInt(left, top, right, bottom int) winRect {
	return winRect{Left: int32(left), Top: int32(top), Right: int32(right), Bottom: int32(bottom)}
}

func pointFromLParam(value uintptr) winPoint {
	return winPoint{X: int32(int16(uint16(value))), Y: int32(int16(uint16(value >> 16)))}
}

func (a *previewWindow) mouseDown(point winPoint) {
	if a == nil {
		return
	}
	var client winRect
	_, _, _ = procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&client)))
	layout := makePreviewLayout(int(client.Right-client.Left), int(client.Bottom-client.Top))
	x, y := int(point.X), int(point.Y)
	for index, rect := range layout.navRects {
		if rect.Contains(x, y) {
			a.section = Section(index)
			a.scroll = 0
			a.hoverNav = index
			a.render()
			return
		}
	}
	if layout.fontRect.Contains(x, y) {
		a.fontScale = NextFontScale(a.fontScale)
		atomic.StoreUint32(&activeRenderFontScale, uint32(a.fontScale))
		a.render()
		return
	}
	if layout.refreshRect.Contains(x, y) {
		a.command(idRefresh)
		return
	}
	if layout.exportRect.Contains(x, y) {
		a.command(idExport)
		return
	}
	if layout.connectRect.Contains(x, y) {
		a.command(idConnect)
		return
	}
}

func (a *previewWindow) mouseMove(point winPoint) {
	if a == nil {
		return
	}
	var client winRect
	_, _, _ = procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&client)))
	layout := makePreviewLayout(int(client.Right-client.Left), int(client.Bottom-client.Top))
	hover := -1
	for index, rect := range layout.navRects {
		if rect.Contains(int(point.X), int(point.Y)) {
			hover = index
			break
		}
	}
	if hover != a.hoverNav {
		a.hoverNav = hover
		a.render()
	}
}

func (a *previewWindow) mouseWheel(delta int16) {
	if a == nil || delta == 0 {
		return
	}
	step := 3
	if delta > 0 {
		a.scroll -= step
	} else {
		a.scroll += step
	}
	if a.scroll < 0 {
		a.scroll = 0
	}
	if maximum := a.maxScroll(); a.scroll > maximum {
		a.scroll = maximum
	}
	a.render()
}

func (a *previewWindow) keyDown(key uint32) {
	if a == nil {
		return
	}
	switch key {
	case vkUp, vkPageUp:
		a.scroll -= 3
	case vkDown, vkPageDown:
		a.scroll += 3
	default:
		return
	}
	if a.scroll < 0 {
		a.scroll = 0
	}
	if maximum := a.maxScroll(); a.scroll > maximum {
		a.scroll = maximum
	}
	a.render()
}

func (a *previewWindow) maxScroll() int {
	if a == nil {
		return 0
	}
	count := 0
	switch a.section {
	case SectionConnections:
		count = len(a.current.Connections)
	case SectionDeveloperRules:
		count = len(a.current.DeveloperRules.Rules)
	case SectionLogs:
		count = len(a.current.Logs)
	default:
		return 0
	}
	if count <= 1 {
		return 0
	}
	return count - 1
}

func (r winRect) Contains(x, y int) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func colorRef(red, green, blue byte) uint32 {
	return uint32(red) | uint32(green)<<8 | uint32(blue)<<16
}

func invalidate(hwnd windows.HWND) {
	if hwnd != 0 {
		_, _, _ = procInvalidateRect.Call(uintptr(hwnd), 0, 0)
	}
}

const (
	colorPage       = 0x00fcfaf8 // RGB(248,250,252)
	colorSidebar    = 0x00fbf4ed // RGB(237,244,251)
	colorCard       = 0x00ffffff
	colorCardBorder = 0x00e9e0d7 // RGB(215,224,233)
	colorText       = 0x002b2018 // RGB(24,32,43)
	colorMuted      = 0x0085776b // RGB(107,119,133)
	colorBlue       = 0x00e26f00 // COLORREF for #006fe2
	colorBlueSoft   = 0x00fff7f0 // RGB(240,247,255)
	colorGreen      = 0x006ab841 // RGB(65,184,106)
	colorGreenSoft  = 0x00eef8e8 // RGB(232,248,238)
	colorAmber      = 0x001c91e5
	colorAmberSoft  = 0x00fff7ee // RGB(238,247,255)
	colorRed        = 0x004f5dda
	colorRedSoft    = 0x00fff0ee // RGB(238,240,255)
	colorGraySoft   = 0x00f5f3f1 // RGB(241,243,245)
	colorGray       = 0x00998b80 // RGB(128,139,153)
	colorWhite      = 0x00ffffff
	colorDarkBlue   = 0x003d2816
	colorOrange     = 0x001f8df0
	colorPurple     = 0x00b36cbb
)

// paint renders the complete client area.  The main process owns all of the
// data passed here; the model only exposes sanitizeSnapshot's bounded fields.
func (a *previewWindow) paint() {
	if a == nil || a.hwnd == 0 {
		return
	}
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&ps)))
	var client winRect
	_, _, _ = procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&client)))
	width := int(client.Right - client.Left)
	height := int(client.Bottom - client.Top)
	if width < sidebarWidth+80 || height < 160 {
		fillRectColor(hdc, client, colorPage)
		return
	}
	a.mu.Lock()
	snapshot := sanitizeSnapshot(a.current)
	connectionState := a.connectionState
	connectorAvailable := a.connector != nil
	statusText := a.statusText
	a.mu.Unlock()
	scale := NormalizeFontScale(a.fontScale)
	a.fontScale = scale
	atomic.StoreUint32(&activeRenderFontScale, uint32(scale))
	layout := makePreviewLayout(width, height)
	fillRectColor(hdc, client, colorPage)
	fillRectColor(hdc, rectInt(0, 0, sidebarWidth, height), colorSidebar)
	drawSidebar(hdc, layout, a.section, a.hoverNav)
	drawHeader(hdc, layout, a.section, statusText, scale, connectionState, connectorAvailable)
	drawSection(hdc, layout, snapshot, a.section, a.scroll, connectionState, connectorAvailable)
}

func drawSidebar(hdc uintptr, layout previewLayout, active Section, hover int) {
	// A small two-colour mark keeps the product recognisable without shipping
	// an external image or an embedded binary asset.
	drawBrandMark(hdc, 42, 24)
	drawText(hdc, "LOCAL-PROBE", rectInt(74, 26, sidebarWidth-12, 48), colorDarkBlue, 12, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "Preview", rectInt(74, 46, sidebarWidth-12, 65), colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	labels := []string{"总览", "连接", "开发者规则", "日志与诊断", "关于 / 设置"}
	for index, rect := range layout.navRects {
		if index == int(active) {
			drawRoundRect(hdc, rect, 9, colorWhite, colorWhite)
			fillRectColor(hdc, winRect{Left: rect.Left, Top: rect.Top + 10, Right: rect.Left + 4, Bottom: rect.Bottom - 10}, colorBlue)
		} else if index == hover {
			drawRoundRect(hdc, rect, 9, colorSidebar, colorSidebar)
		}
		drawNavIcon(hdc, int(rect.Left)+25, int(rect.Top)+int((rect.Bottom-rect.Top)/2), index, index == int(active))
		textColor := colorText
		if index != int(active) {
			textColor = colorMuted
		}
		drawText(hdc, labels[index], winRect{Left: rect.Left + 48, Top: rect.Top, Right: rect.Right - 7, Bottom: rect.Bottom}, uint32(textColor), 13, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
	}
	drawText(hdc, "本地只读预览", rectInt(24, layout.height-58, sidebarWidth-16, layout.height-38), colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, Version, rectInt(24, layout.height-38, sidebarWidth-16, layout.height-18), colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
}

func drawHeader(hdc uintptr, layout previewLayout, section Section, statusText string, scale FontScale, connectionState ConnectionControlState, connectorAvailable bool) {
	title, subtitle := sectionTitle(section)
	drawText(hdc, title, rectInt(layout.mainLeft, layout.headerTop, int(layout.connectRect.Left)-18, layout.headerTop+36), colorText, 25, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, subtitle, rectInt(layout.mainLeft, layout.headerTop+38, int(layout.connectRect.Left)-18, layout.headerTop+64), colorMuted, 11, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawConnectButton(hdc, layout.connectRect, ConnectionButtonLabel(connectionState, connectorAvailable), connectorAvailable && !connectionState.Busy)
	drawActionButton(hdc, layout.fontRect, FontScaleLabel(scale), false)
	drawActionButton(hdc, layout.refreshRect, "↻  刷新", false)
	drawActionButton(hdc, layout.exportRect, "导出诊断", false)
	if statusText == "" {
		statusText = "Ready"
	}
	drawText(hdc, statusText, rectInt(layout.mainRight-230, layout.headerTop+72, layout.mainRight, layout.headerTop+94), colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
}

func sectionTitle(section Section) (string, string) {
	switch section {
	case SectionConnections:
		return "连接", "查看本地 MCP 与 Tunnel 配置的只读投影"
	case SectionDeveloperRules:
		return "开发者规则", "查看已登记的命令规则；Preview 不会修改或启用规则"
	case SectionLogs:
		return "日志与诊断", "有界、脱敏的操作审计和故障摘要"
	case SectionAbout:
		return "关于 / 设置", "Local-Probe Preview 的版本和运行边界"
	default:
		return "实时状态", "本地服务、连接和审计状态概览"
	}
}

func drawSection(hdc uintptr, layout previewLayout, snapshot Snapshot, section Section, scroll int, connectionState ConnectionControlState, connectorAvailable bool) {
	switch section {
	case SectionConnections:
		drawConnectionsPage(hdc, layout, snapshot, scroll, connectionState, connectorAvailable)
	case SectionDeveloperRules:
		drawRulesPage(hdc, layout, snapshot, scroll)
	case SectionLogs:
		drawLogsPage(hdc, layout, snapshot, scroll)
	case SectionAbout:
		drawAboutPage(hdc, layout, snapshot)
	default:
		drawOverviewPage(hdc, layout, snapshot, connectionState, connectorAvailable)
	}
}

func drawOverviewPage(hdc uintptr, layout previewLayout, s Snapshot, connectionState ConnectionControlState, connectorAvailable bool) {
	mainLeft, mainRight := layout.mainLeft, layout.mainRight
	cardWidth := mainRight - mainLeft
	statusRect := rectInt(mainLeft, 128, mainRight, 244)
	drawCard(hdc, statusRect)
	drawStatusDot(hdc, mainLeft+28, 169, s.Status)
	drawText(hdc, "服务状态", rectInt(mainLeft+54, 142, mainLeft+250, 169), colorMuted, 11, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, localizedStatus(s.Status, s.Unconfigured), rectInt(mainLeft+54, 169, mainLeft+360, 203), colorText, 21, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	statusDescription := "配置已加载，连接状态按本地快照展示"
	if s.Unconfigured {
		statusDescription = "尚未发现可读配置；Preview 仍保持只读"
	} else if !s.Overview.StatusKnown {
		statusDescription = "运行时状态尚未接入，当前仅显示配置投影"
	}
	drawText(hdc, statusDescription, rectInt(mainLeft+54, 204, mainLeft+440, 228), colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "配置修订", rectInt(mainRight-260, 146, mainRight-176, 168), colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, displayAtom(s.Overview.ConfigRevision, "不可用"), rectInt(mainRight-260, 169, mainRight-24, 193), colorText, 12, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawPill(hdc, rectInt(mainRight-260, 202, mainRight-24, 226), auditLabel(s.Audit), auditColor(s.Audit))

	metricTop, metricBottom := 264, 352
	metrics := []struct {
		label string
		value int
		color uint32
	}{
		{"根目录", s.Overview.RootCount, colorBlue},
		{"配置档案", s.Overview.ProfileCount, colorPurple},
		{"连接", s.Overview.ConnectionCount, colorGreen},
		{"已启用", s.Overview.EnabledConnectionCount, colorOrange},
	}
	gap := 14
	metricWidth := (cardWidth - gap*3) / 4
	for index, metric := range metrics {
		x := mainLeft + index*(metricWidth+gap)
		rect := rectInt(x, metricTop, x+metricWidth, metricBottom)
		drawCard(hdc, rect)
		drawMetricIcon(hdc, x+24, metricTop+30, metric.color, index)
		drawText(hdc, metric.label, rectInt(x+50, metricTop+16, int(rect.Right)-14, metricTop+38), colorMuted, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
		drawText(hdc, fmt.Sprintf("%d", metric.value), rectInt(x+22, metricTop+42, int(rect.Right)-14, metricBottom-12), colorText, 23, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	}

	bottomTop := 374
	leftWidth := (cardWidth - 14) / 2
	drawConnectionsCard(hdc, rectInt(mainLeft, bottomTop, mainLeft+leftWidth, 570), s)
	drawAuditCard(hdc, rectInt(mainLeft+leftWidth+14, bottomTop, mainRight, 570), s)
	drawGateCard(hdc, rectInt(mainLeft, 588, mainRight, minInt(layout.height-22, 742)), s, connectionState, connectorAvailable)
}

func drawConnectionsCard(hdc uintptr, rect winRect, s Snapshot) {
	drawCard(hdc, rect)
	drawText(hdc, "连接概览", winRect{Left: rect.Left + 20, Top: rect.Top + 16, Right: rect.Right - 18, Bottom: rect.Top + 44}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	if len(s.Connections) == 0 {
		drawText(hdc, "暂无已配置连接", winRect{Left: rect.Left + 20, Top: rect.Top + 68, Right: rect.Right - 20, Bottom: rect.Top + 96}, colorMuted, 11, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
		return
	}
	rowTop := int(rect.Top) + 55
	for index, c := range s.Connections {
		if index >= 3 {
			break
		}
		drawConnectionRow(hdc, winRect{Left: rect.Left + 16, Top: int32(rowTop), Right: rect.Right - 16, Bottom: int32(rowTop + 42)}, c)
		rowTop += 48
	}
	if len(s.Connections) > 3 {
		drawText(hdc, fmt.Sprintf("还有 %d 个连接，请打开“连接”查看", len(s.Connections)-3), winRect{Left: rect.Left + 20, Top: rect.Bottom - 26, Right: rect.Right - 18, Bottom: rect.Bottom - 9}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	}
}

func drawAuditCard(hdc uintptr, rect winRect, s Snapshot) {
	drawCard(hdc, rect)
	drawText(hdc, "日志与诊断", winRect{Left: rect.Left + 20, Top: rect.Top + 16, Right: rect.Right - 18, Bottom: rect.Top + 44}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	valueX := rect.Left + 20
	drawText(hdc, "审计状态", winRect{Left: valueX, Top: rect.Top + 66, Right: rect.Right - 20, Bottom: rect.Top + 88}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawPill(hdc, winRect{Left: valueX, Top: rect.Top + 92, Right: valueX + 112, Bottom: rect.Top + 121}, auditLabel(s.Audit), auditColor(s.Audit))
	drawText(hdc, fmt.Sprintf("记录 %d", s.Audit.Records), winRect{Left: valueX + 132, Top: rect.Top + 94, Right: rect.Right - 20, Bottom: rect.Top + 120}, colorText, 12, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, fmt.Sprintf("丢弃 %d · 损坏 %t", s.Audit.Dropped, s.Audit.Corrupt), winRect{Left: valueX, Top: rect.Top + 139, Right: rect.Right - 20, Bottom: rect.Top + 162}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "内容、路径、命令和凭据不会显示在 Preview", winRect{Left: valueX, Top: rect.Bottom - 30, Right: rect.Right - 18, Bottom: rect.Bottom - 12}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
}

func drawGateCard(hdc uintptr, rect winRect, s Snapshot, connectionState ConnectionControlState, connectorAvailable bool) {
	if rect.Bottom <= rect.Top {
		return
	}
	display := connectionState.Display()
	background := uint32(colorBlueSoft)
	statusColorValue := uint32(colorBlue)
	if display.Healthy {
		background, statusColorValue = colorGreenSoft, colorGreen
	} else if display.Title == "连接失败" || strings.Contains(display.StatusLabel, "失败") {
		background, statusColorValue = colorRedSoft, colorRed
	} else if display.Busy {
		background, statusColorValue = colorAmberSoft, colorAmber
	}
	drawRoundRect(hdc, rect, 10, background, background)
	drawStatusDotColor(hdc, int(rect.Left)+28, int(rect.Top)+31, statusColorValue)
	drawText(hdc, "Tunnel 连接", winRect{Left: rect.Left + 52, Top: rect.Top + 11, Right: rect.Right - 260, Bottom: rect.Top + 36}, colorText, 13, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, display.Title, winRect{Left: rect.Left + 52, Top: rect.Top + 38, Right: rect.Right - 260, Bottom: rect.Top + 63}, statusColorValue, 11, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, display.PhaseLabel+" · "+display.StatusLabel, winRect{Left: rect.Left + 52, Top: rect.Top + 66, Right: rect.Right - 260, Bottom: rect.Top + 88}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, display.Detail, winRect{Left: rect.Left + 52, Top: rect.Top + 91, Right: rect.Right - 260, Bottom: rect.Top + 113}, colorText, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, display.Remediation, winRect{Left: rect.Left + 52, Top: rect.Top + 115, Right: rect.Right - 260, Bottom: rect.Bottom - 12}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawPill(hdc, winRect{Left: rect.Right - 242, Top: rect.Top + 18, Right: rect.Right - 20, Bottom: rect.Top + 47}, "公开主机 "+displayAtom(display.PublicHost, "未配置"), struct{ fg, bg uint32 }{colorBlue, colorWhite})
	drawPill(hdc, winRect{Left: rect.Right - 242, Top: rect.Top + 57, Right: rect.Right - 20, Bottom: rect.Top + 86}, "Token "+display.TokenStatus, struct{ fg, bg uint32 }{colorMuted, colorWhite})
	drawText(hdc, ConnectionButtonLabel(connectionState, connectorAvailable), winRect{Left: rect.Right - 242, Top: rect.Top + 96, Right: rect.Right - 20, Bottom: rect.Top + 122}, colorText, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	_ = s
}

func drawConnectionsPage(hdc uintptr, layout previewLayout, s Snapshot, scroll int, connectionState ConnectionControlState, connectorAvailable bool) {
	card := rectInt(layout.mainLeft, 128, layout.mainRight, layout.height-24)
	drawCard(hdc, card)
	drawText(hdc, fmt.Sprintf("%d 个连接", len(s.Connections)), winRect{Left: card.Left + 22, Top: card.Top + 16, Right: card.Right - 20, Bottom: card.Top + 44}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "只读投影 · 公开主机和 token 状态可见，私有路径与凭据不会显示", winRect{Left: card.Left + 22, Top: card.Top + 45, Right: card.Right - 20, Bottom: card.Top + 68}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	control := rectInt(int(card.Left)+18, int(card.Top)+76, int(card.Right)-18, int(card.Top)+194)
	drawConnectionControlCard(hdc, control, connectionState, connectorAvailable)
	rowTop := int(card.Top) + 210
	visible := 0
	visibleLimit := maxVisibleRows(int(card.Bottom)-rowTop, 58, 8)
	for index := scroll; index < len(s.Connections) && visible < visibleLimit; index++ {
		c := s.Connections[index]
		drawConnectionRow(hdc, winRect{Left: card.Left + 18, Top: int32(rowTop), Right: card.Right - 18, Bottom: int32(rowTop + 58)}, c)
		rowTop += 66
		visible++
	}
	if len(s.Connections) == 0 {
		drawEmptyState(hdc, card, "暂无已配置连接", "连接配置将由本地管理入口提供")
	} else if scroll > 0 || scroll+visible < len(s.Connections) {
		drawText(hdc, fmt.Sprintf("显示 %d-%d / %d · 使用鼠标滚轮或方向键浏览", scroll+1, minInt(scroll+visible, len(s.Connections)), len(s.Connections)), winRect{Left: card.Left + 22, Top: card.Bottom - 31, Right: card.Right - 20, Bottom: card.Bottom - 12}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	}
}

func drawConnectionControlCard(hdc uintptr, rect winRect, state ConnectionControlState, connectorAvailable bool) {
	display := state.Display()
	background := uint32(colorGraySoft)
	statusColorValue := uint32(colorGray)
	if display.Healthy {
		background, statusColorValue = colorGreenSoft, colorGreen
	} else if display.Busy {
		background, statusColorValue = colorAmberSoft, colorAmber
	} else if display.Title == "连接失败" || strings.Contains(display.StatusLabel, "失败") {
		background, statusColorValue = colorRedSoft, colorRed
	}
	drawRoundRect(hdc, rect, 8, background, background)
	drawStatusDotColor(hdc, int(rect.Left)+22, int(rect.Top)+28, statusColorValue)
	drawText(hdc, display.Title, winRect{Left: rect.Left + 42, Top: rect.Top + 10, Right: rect.Right - 360, Bottom: rect.Top + 36}, colorText, 12, fontBold, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, display.PhaseLabel+" · "+display.StatusLabel, winRect{Left: rect.Left + 42, Top: rect.Top + 39, Right: rect.Right - 360, Bottom: rect.Top + 62}, statusColorValue, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, display.Detail, winRect{Left: rect.Left + 42, Top: rect.Top + 66, Right: rect.Right - 360, Bottom: rect.Top + 90}, colorText, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, display.Remediation, winRect{Left: rect.Left + 42, Top: rect.Top + 91, Right: rect.Right - 360, Bottom: rect.Bottom - 10}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawPill(hdc, winRect{Left: rect.Right - 332, Top: rect.Top + 16, Right: rect.Right - 18, Bottom: rect.Top + 44}, "公开主机 "+displayAtom(display.PublicHost, "未配置"), struct{ fg, bg uint32 }{colorBlue, colorWhite})
	drawPill(hdc, winRect{Left: rect.Right - 332, Top: rect.Top + 52, Right: rect.Right - 18, Bottom: rect.Top + 80}, "Token "+display.TokenStatus, struct{ fg, bg uint32 }{colorMuted, colorWhite})
	drawText(hdc, ConnectionButtonLabel(state, connectorAvailable), winRect{Left: rect.Right - 332, Top: rect.Top + 88, Right: rect.Right - 18, Bottom: rect.Top + 112}, colorText, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
}

func drawRulesPage(hdc uintptr, layout previewLayout, s Snapshot, scroll int) {
	intro := rectInt(layout.mainLeft, 128, layout.mainRight, 218)
	drawCard(hdc, intro)
	drawText(hdc, "开发者模式", winRect{Left: intro.Left + 22, Top: intro.Top + 16, Right: intro.Right - 180, Bottom: intro.Top + 42}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	modeText := "未开启"
	modeColor := uint32(colorGray)
	if s.DeveloperRules.Enabled {
		modeText = "已开启"
		modeColor = colorGreen
	}
	drawPill(hdc, winRect{Left: intro.Right - 140, Top: intro.Top + 20, Right: intro.Right - 22, Bottom: intro.Top + 50}, modeText, struct{ fg, bg uint32 }{modeColor, colorWhite})
	drawText(hdc, fmt.Sprintf("允许的连接：%d · 规则：%d", len(s.DeveloperRules.AllowedConnectionIDs), len(s.DeveloperRules.Rules)), winRect{Left: intro.Left + 22, Top: intro.Top + 50, Right: intro.Right - 20, Bottom: intro.Top + 74}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	rulesCard := rectInt(layout.mainLeft, 232, layout.mainRight, layout.height-24)
	drawCard(hdc, rulesCard)
	drawText(hdc, "规则列表", winRect{Left: rulesCard.Left + 22, Top: rulesCard.Top + 16, Right: rulesCard.Right - 20, Bottom: rulesCard.Top + 42}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	if !s.DeveloperRules.Available {
		drawEmptyState(hdc, rulesCard, "规则投影不可用", "当前配置未提供可展示的开发者规则")
		return
	}
	rowTop := int(rulesCard.Top) + 58
	visible := 0
	visibleLimit := maxVisibleRows(int(rulesCard.Bottom)-rowTop, 58, 7)
	for index := scroll; index < len(s.DeveloperRules.Rules) && visible < visibleLimit; index++ {
		rule := s.DeveloperRules.Rules[index]
		drawRuleRow(hdc, winRect{Left: rulesCard.Left + 18, Top: int32(rowTop), Right: rulesCard.Right - 18, Bottom: int32(rowTop + 58)}, rule)
		rowTop += 65
		visible++
	}
	if len(s.DeveloperRules.Rules) == 0 {
		drawEmptyState(hdc, rulesCard, "暂无规则", "规则配置仅能通过受信的本地管理入口变更")
	} else if scroll > 0 || scroll+visible < len(s.DeveloperRules.Rules) {
		drawText(hdc, fmt.Sprintf("显示 %d-%d / %d · 使用鼠标滚轮或方向键浏览", scroll+1, minInt(scroll+visible, len(s.DeveloperRules.Rules)), len(s.DeveloperRules.Rules)), winRect{Left: rulesCard.Left + 22, Top: rulesCard.Bottom - 31, Right: rulesCard.Right - 20, Bottom: rulesCard.Bottom - 12}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	}
}

func drawLogsPage(hdc uintptr, layout previewLayout, s Snapshot, scroll int) {
	card := rectInt(layout.mainLeft, 128, layout.mainRight, layout.height-24)
	drawCard(hdc, card)
	drawText(hdc, "审计事件", winRect{Left: card.Left + 22, Top: card.Top + 16, Right: card.Right - 200, Bottom: card.Top + 42}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawPill(hdc, winRect{Left: card.Right - 166, Top: card.Top + 18, Right: card.Right - 22, Bottom: card.Top + 48}, auditLabel(s.Audit), auditColor(s.Audit))
	drawText(hdc, "仅显示结构化、脱敏且有界的记录；原始消息和正文不会进入此界面", winRect{Left: card.Left + 22, Top: card.Top + 48, Right: card.Right - 20, Bottom: card.Top + 70}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	rowTop := int(card.Top) + 86
	visible := 0
	visibleLimit := maxVisibleRows(int(card.Bottom)-rowTop, 56, 8)
	for index := scroll; index < len(s.Logs) && visible < visibleLimit; index++ {
		drawLogRow(hdc, winRect{Left: card.Left + 18, Top: int32(rowTop), Right: card.Right - 18, Bottom: int32(rowTop + 56)}, s.Logs[index])
		rowTop += 64
		visible++
	}
	if len(s.Logs) == 0 {
		drawEmptyState(hdc, card, "暂无可显示日志", "审计记录会在本地操作发生后以脱敏形式出现")
	} else if scroll > 0 || scroll+visible < len(s.Logs) {
		drawText(hdc, fmt.Sprintf("显示 %d-%d / %d · 使用鼠标滚轮或方向键浏览", scroll+1, minInt(scroll+visible, len(s.Logs)), len(s.Logs)), winRect{Left: card.Left + 22, Top: card.Bottom - 31, Right: card.Right - 20, Bottom: card.Bottom - 12}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	}
}

func drawAboutPage(hdc uintptr, layout previewLayout, s Snapshot) {
	leftWidth := (layout.mainRight - layout.mainLeft - 14) / 2
	left := rectInt(layout.mainLeft, 128, layout.mainLeft+leftWidth, layout.height-24)
	right := rectInt(layout.mainLeft+leftWidth+14, 128, layout.mainRight, layout.height-24)
	drawCard(hdc, left)
	drawText(hdc, "Local-Probe", winRect{Left: left.Left + 22, Top: left.Top + 22, Right: left.Right - 20, Bottom: left.Top + 54}, colorText, 20, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "Preview 版本", winRect{Left: left.Left + 22, Top: left.Top + 60, Right: left.Right - 20, Bottom: left.Top + 86}, colorMuted, 11, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawPill(hdc, winRect{Left: left.Left + 22, Top: left.Top + 104, Right: left.Left + 146, Bottom: left.Top + 134}, Version, struct{ fg, bg uint32 }{colorBlue, colorBlueSoft})
	drawText(hdc, "原生 Win32 · 无 HTTP listener · 无 Electron", winRect{Left: left.Left + 22, Top: left.Top + 154, Right: left.Right - 20, Bottom: left.Top + 178}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, "关闭窗口会隐藏到托盘；Exit 只退出 Preview，不停止 MCP/Tunnel。", winRect{Left: left.Left + 22, Top: left.Top + 204, Right: left.Right - 20, Bottom: left.Top + 256}, colorText, 11, fontNormal, dtWordBreak|dtNoPrefix)
	drawCard(hdc, right)
	drawText(hdc, "运行边界", winRect{Left: right.Left + 22, Top: right.Top + 22, Right: right.Right - 20, Bottom: right.Top + 50}, colorText, 14, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawBoundaryRow(hdc, right.Left+22, right.Top+70, "生命周期控制", "production_gate", false)
	drawBoundaryRow(hdc, right.Left+22, right.Top+118, "审计与诊断", auditLabel(s.Audit), s.Audit.Available)
	drawBoundaryRow(hdc, right.Left+22, right.Top+166, "配置状态", localizedStatus(s.Status, s.Unconfigured), !s.Unconfigured)
	drawText(hdc, "Preview 只读取经过适配器脱敏后的状态。它不会展示文件路径、连接端点、命令行、环境变量、凭据或原始错误消息。", winRect{Left: right.Left + 22, Top: right.Top + 230, Right: right.Right - 22, Bottom: right.Top + 294}, colorMuted, 10, fontNormal, dtWordBreak|dtNoPrefix)
}

func drawCard(hdc uintptr, rect winRect) {
	drawRoundRect(hdc, rect, 10, colorCard, colorCardBorder)
}

func drawRoundRect(hdc uintptr, rect winRect, radius int, fill, border uint32) {
	if rect.Right <= rect.Left || rect.Bottom <= rect.Top {
		return
	}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(fill))
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(border))
	if brush == 0 || pen == 0 {
		if brush != 0 {
			_, _, _ = procDeleteObject.Call(brush)
		}
		if pen != 0 {
			_, _, _ = procDeleteObject.Call(pen)
		}
		return
	}
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	_, _, _ = procRoundRect.Call(hdc, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right), uintptr(rect.Bottom), uintptr(radius), uintptr(radius))
	_, _, _ = procSelectObject.Call(hdc, oldBrush)
	_, _, _ = procSelectObject.Call(hdc, oldPen)
	_, _, _ = procDeleteObject.Call(brush)
	_, _, _ = procDeleteObject.Call(pen)
}

func fillRectColor(hdc uintptr, rect winRect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	if brush == 0 {
		return
	}
	_, _, _ = procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rect)), brush)
	_, _, _ = procDeleteObject.Call(brush)
}

func drawText(hdc uintptr, value string, rect winRect, color uint32, size, weight int, flags uint32) {
	if value == "" || rect.Right <= rect.Left || rect.Bottom <= rect.Top {
		return
	}
	if flags&dtSingleLine != 0 {
		// All single-line labels are bounded by their local rectangle.  This is
		// especially important after the user selects 175% or 200%.
		flags |= dtEndEllipsis
	}
	scale := FontScale(atomic.LoadUint32(&activeRenderFontScale))
	scaledSize := ScaleFontSize(size, scale)
	face := wideString("Microsoft YaHei UI")
	font, _, _ := procCreateFontW.Call(
		uintptr(^uint32(uint32(scaledSize-1))), // negative height is character height
		0, 0, 0, uintptr(weight), 0, 0, 0,
		0x86, 0, 0, 0, 0, uintptr(unsafe.Pointer(face)),
	)
	if font == 0 {
		font = getStockFont()
	}
	oldFont, _, _ := procSelectObject.Call(hdc, font)
	_, _, _ = procSetTextColor.Call(hdc, uintptr(color))
	_, _, _ = procSetBkMode.Call(hdc, 1) // TRANSPARENT
	text := wideString(value)
	copyRect := rect
	_, _, _ = procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(text)), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&copyRect)), uintptr(flags))
	_, _, _ = procSelectObject.Call(hdc, oldFont)
	if font != getStockFont() {
		_, _, _ = procDeleteObject.Call(font)
	}
}

func drawActionButton(hdc uintptr, rect winRect, label string, disabled bool) {
	fill, border, text := uint32(colorWhite), uint32(colorCardBorder), uint32(colorText)
	if disabled {
		fill, border, text = colorGraySoft, colorCardBorder, colorGray
	}
	drawRoundRect(hdc, rect, 8, fill, border)
	drawText(hdc, label, winRect{Left: rect.Left + 8, Top: rect.Top, Right: rect.Right - 8, Bottom: rect.Bottom}, text, 11, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix)
}

func drawConnectButton(hdc uintptr, rect winRect, label string, active bool) {
	if !active {
		drawActionButton(hdc, rect, label, true)
		return
	}
	drawRoundRect(hdc, rect, 8, colorBlue, colorBlue)
	drawText(hdc, label, winRect{Left: rect.Left + 8, Top: rect.Top, Right: rect.Right - 8, Bottom: rect.Bottom}, colorWhite, 11, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
}

func drawPill(hdc uintptr, rect winRect, label string, style struct{ fg, bg uint32 }) {
	drawRoundRect(hdc, rect, int((rect.Bottom-rect.Top)/2), style.bg, style.bg)
	drawText(hdc, label, winRect{Left: rect.Left + 10, Top: rect.Top, Right: rect.Right - 10, Bottom: rect.Bottom}, style.fg, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
}

func drawStatusDot(hdc uintptr, x, y int, status string) {
	drawStatusDotColor(hdc, x, y, statusColor(status, false))
}

func drawStatusDotColor(hdc uintptr, x, y int, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(color))
	if brush == 0 || pen == 0 {
		if brush != 0 {
			_, _, _ = procDeleteObject.Call(brush)
		}
		if pen != 0 {
			_, _, _ = procDeleteObject.Call(pen)
		}
		return
	}
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	_, _, _ = procEllipse.Call(hdc, uintptr(x-7), uintptr(y-7), uintptr(x+7), uintptr(y+7))
	_, _, _ = procSelectObject.Call(hdc, oldBrush)
	_, _, _ = procSelectObject.Call(hdc, oldPen)
	_, _, _ = procDeleteObject.Call(brush)
	_, _, _ = procDeleteObject.Call(pen)
}

func drawConnectionRow(hdc uintptr, rect winRect, c Connection) {
	if rect.Bottom <= rect.Top {
		return
	}
	drawStatusDot(hdc, int(rect.Left)+10, int(rect.Top)+21, c.State)
	label := displayLabel(c.Label, c.LabelOmitted, c.ConnectionID)
	drawText(hdc, label, winRect{Left: rect.Left + 28, Top: rect.Top + 5, Right: rect.Right - 160, Bottom: rect.Top + 29}, colorText, 11, fontBold, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	transport := localizedTransport(c.Transport)
	if c.StatusKnown {
		transport += " · " + localizedStatus(c.State, false)
	}
	drawText(hdc, transport, winRect{Left: rect.Left + 28, Top: rect.Top + 29, Right: rect.Right - 160, Bottom: rect.Bottom - 3}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawPill(hdc, winRect{Left: rect.Right - 126, Top: rect.Top + 10, Right: rect.Right - 4, Bottom: rect.Top + 38}, connectionLabel(c), struct{ fg, bg uint32 }{statusColor(c.State, c.StatusKnown), statusBackground(c.State, c.StatusKnown)})
}

func drawRuleRow(hdc uintptr, rect winRect, rule DeveloperRule) {
	drawStatusDotColor(hdc, int(rect.Left)+10, int(rect.Top)+20, colorBlue)
	drawText(hdc, displayAtom(rule.ID, "未命名规则"), winRect{Left: rect.Left + 28, Top: rect.Top + 5, Right: rect.Right - 260, Bottom: rect.Top + 30}, colorText, 11, fontBold, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, displayAtom(rule.Kind, "unknown"), winRect{Left: rect.Right - 230, Top: rect.Top + 5, Right: rect.Right - 10, Bottom: rect.Top + 30}, colorBlue, 10, fontMedium, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, fmt.Sprintf("变体 %d · 参数槽位 %d", len(rule.VariantIDs), len(rule.SlotKinds)), winRect{Left: rect.Left + 28, Top: rect.Top + 31, Right: rect.Right - 12, Bottom: rect.Bottom - 4}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
}

func drawLogRow(hdc uintptr, rect winRect, entry LogEntry) {
	drawStatusDotColor(hdc, int(rect.Left)+10, int(rect.Top)+20, severityColor(entry.Severity))
	timestamp := entry.Timestamp.Format("15:04:05")
	drawText(hdc, timestamp, winRect{Left: rect.Left + 28, Top: rect.Top + 4, Right: rect.Left + 98, Bottom: rect.Top + 29}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, displayAtom(entry.Component, "unknown"), winRect{Left: rect.Left + 112, Top: rect.Top + 4, Right: rect.Left + 240, Bottom: rect.Top + 29}, colorText, 11, fontBold, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawText(hdc, displayAtom(entry.EventType, "event"), winRect{Left: rect.Left + 252, Top: rect.Top + 4, Right: rect.Right - 190, Bottom: rect.Top + 29}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
	drawPill(hdc, winRect{Left: rect.Right - 172, Top: rect.Top + 6, Right: rect.Right - 78, Bottom: rect.Top + 32}, localizedOutcome(entry.Outcome), struct{ fg, bg uint32 }{severityColor(entry.Severity), statusBackground(entry.Severity, true)})
	drawText(hdc, displayAtom(entry.ErrorCode, ""), winRect{Left: rect.Right - 68, Top: rect.Top + 4, Right: rect.Right - 4, Bottom: rect.Top + 29}, colorMuted, 9, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix|dtEndEllipsis)
}

func drawBoundaryRow(hdc uintptr, x, y int32, label, value string, healthy bool) {
	drawText(hdc, label, winRect{Left: x, Top: y, Right: x + 152, Bottom: y + 28}, colorMuted, 10, fontNormal, dtSingleLine|dtVCenter|dtNoPrefix)
	fg, bg := uint32(colorGray), uint32(colorGraySoft)
	if healthy {
		fg, bg = colorGreen, colorGreenSoft
	}
	drawPill(hdc, winRect{Left: x + 152, Top: y, Right: x + 300, Bottom: y + 28}, value, struct{ fg, bg uint32 }{fg, bg})
}

func drawEmptyState(hdc uintptr, rect winRect, title, subtitle string) {
	y := int(rect.Top) + 120
	drawStatusDotColor(hdc, int(rect.Left)+36, y+2, colorGray)
	drawText(hdc, title, winRect{Left: rect.Left + 58, Top: int32(y - 15), Right: rect.Right - 22, Bottom: int32(y + 12)}, colorText, 13, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	drawText(hdc, subtitle, winRect{Left: rect.Left + 58, Top: int32(y + 16), Right: rect.Right - 22, Bottom: int32(y + 44)}, colorMuted, 10, fontNormal, dtWordBreak|dtNoPrefix)
}

func drawBrandMark(hdc uintptr, x, y int) {
	points := []winPoint{{X: int32(x), Y: int32(y - 17)}, {X: int32(x + 17), Y: int32(y)}, {X: int32(x), Y: int32(y + 17)}, {X: int32(x - 17), Y: int32(y)}}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorOrange))
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(colorOrange))
	if brush != 0 && pen != 0 {
		oldBrush, _, _ := procSelectObject.Call(hdc, brush)
		oldPen, _, _ := procSelectObject.Call(hdc, pen)
		_, _, _ = procPolygon.Call(hdc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
		_, _, _ = procSelectObject.Call(hdc, oldBrush)
		_, _, _ = procSelectObject.Call(hdc, oldPen)
	}
	if brush != 0 {
		_, _, _ = procDeleteObject.Call(brush)
	}
	if pen != 0 {
		_, _, _ = procDeleteObject.Call(pen)
	}
	inner := []winPoint{{X: int32(x), Y: int32(y - 8)}, {X: int32(x + 8), Y: int32(y)}, {X: int32(x), Y: int32(y + 8)}, {X: int32(x - 8), Y: int32(y)}}
	brush, _, _ = procCreateSolidBrush.Call(uintptr(colorBlue))
	pen, _, _ = procCreatePen.Call(0, 1, uintptr(colorBlue))
	if brush != 0 && pen != 0 {
		oldBrush, _, _ := procSelectObject.Call(hdc, brush)
		oldPen, _, _ := procSelectObject.Call(hdc, pen)
		_, _, _ = procPolygon.Call(hdc, uintptr(unsafe.Pointer(&inner[0])), uintptr(len(inner)))
		_, _, _ = procSelectObject.Call(hdc, oldBrush)
		_, _, _ = procSelectObject.Call(hdc, oldPen)
	}
	if brush != 0 {
		_, _, _ = procDeleteObject.Call(brush)
	}
	if pen != 0 {
		_, _, _ = procDeleteObject.Call(pen)
	}
}

func drawNavIcon(hdc uintptr, x, y, section int, active bool) {
	color := uint32(colorMuted)
	if active {
		color = colorBlue
	}
	pen, _, _ := procCreatePen.Call(0, 2, uintptr(color))
	if pen == 0 {
		return
	}
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	switch section {
	case 0: // home
		points := []winPoint{{X: int32(x - 9), Y: int32(y)}, {X: int32(x), Y: int32(y - 8)}, {X: int32(x + 9), Y: int32(y)}, {X: int32(x + 9), Y: int32(y + 9)}, {X: int32(x - 9), Y: int32(y + 9)}}
		_, _, _ = procPolygon.Call(hdc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	case 1: // connections
		_, _, _ = procEllipse.Call(hdc, uintptr(x-10), uintptr(y-5), uintptr(x), uintptr(y+5))
		_, _, _ = procEllipse.Call(hdc, uintptr(x), uintptr(y-5), uintptr(x+10), uintptr(y+5))
		_, _, _ = procMoveToEx.Call(hdc, uintptr(x-5), uintptr(y), 0, 0)
		_, _, _ = procLineTo.Call(hdc, uintptr(x+5), uintptr(y))
	case 2: // developer rules
		_, _, _ = procEllipse.Call(hdc, uintptr(x-8), uintptr(y-8), uintptr(x+8), uintptr(y+8))
		_, _, _ = procMoveToEx.Call(hdc, uintptr(x), uintptr(y-12), 0, 0)
		_, _, _ = procLineTo.Call(hdc, uintptr(x), uintptr(y-8))
		_, _, _ = procMoveToEx.Call(hdc, uintptr(x), uintptr(y+8), 0, 0)
		_, _, _ = procLineTo.Call(hdc, uintptr(x), uintptr(y+12))
	case 3: // logs
		for offset := -6; offset <= 6; offset += 6 {
			_, _, _ = procMoveToEx.Call(hdc, uintptr(x-9), uintptr(y+offset), 0, 0)
			_, _, _ = procLineTo.Call(hdc, uintptr(x+9), uintptr(y+offset))
		}
	case 4: // about/settings
		_, _, _ = procEllipse.Call(hdc, uintptr(x-9), uintptr(y-9), uintptr(x+9), uintptr(y+9))
		drawText(hdc, "i", winRect{Left: int32(x - 3), Top: int32(y - 10), Right: int32(x + 4), Bottom: int32(y + 10)}, color, 12, fontBold, dtSingleLine|dtVCenter|dtNoPrefix)
	}
	_, _, _ = procSelectObject.Call(hdc, oldPen)
	_, _, _ = procDeleteObject.Call(pen)
}

func drawMetricIcon(hdc uintptr, x, y int, color uint32, kind int) {
	pen, _, _ := procCreatePen.Call(0, 2, uintptr(color))
	if pen == 0 {
		return
	}
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	switch kind {
	case 0:
		_, _, _ = procRectangle.Call(hdc, uintptr(x-8), uintptr(y-7), uintptr(x+8), uintptr(y+7))
	case 1:
		_, _, _ = procEllipse.Call(hdc, uintptr(x-8), uintptr(y-8), uintptr(x+8), uintptr(y+8))
	case 2:
		_, _, _ = procEllipse.Call(hdc, uintptr(x-8), uintptr(y-8), uintptr(x+8), uintptr(y+8))
		_, _, _ = procMoveToEx.Call(hdc, uintptr(x-12), uintptr(y), 0, 0)
		_, _, _ = procLineTo.Call(hdc, uintptr(x+12), uintptr(y))
	default:
		for offset := -6; offset <= 6; offset += 6 {
			_, _, _ = procMoveToEx.Call(hdc, uintptr(x-9), uintptr(y+offset), 0, 0)
			_, _, _ = procLineTo.Call(hdc, uintptr(x+9), uintptr(y+offset))
		}
	}
	_, _, _ = procSelectObject.Call(hdc, oldPen)
	_, _, _ = procDeleteObject.Call(pen)
}

func statusColor(status string, known bool) uint32 {
	if !known || status == "" || status == "unknown" || status == "unavailable" {
		return colorGray
	}
	switch status {
	case "ready", "local_mcp_ready", "enabled", "success", "ok":
		return colorGreen
	case "auth_failed", "error", "failed", "degraded":
		return colorRed
	case "starting", "polling", "backoff", "resuming":
		return colorAmber
	default:
		return colorBlue
	}
}

func statusBackground(status string, known bool) uint32 {
	switch statusColor(status, known) {
	case colorGreen:
		return colorGreenSoft
	case colorRed:
		return colorRedSoft
	case colorAmber:
		return colorAmberSoft
	default:
		return colorGraySoft
	}
}

func severityColor(severity string) uint32 {
	switch severity {
	case "error", "fatal":
		return colorRed
	case "warn", "warning":
		return colorAmber
	case "info":
		return colorBlue
	default:
		return colorGray
	}
}

func localizedStatus(status string, unconfigured bool) string {
	if unconfigured || status == "unconfigured" {
		return "未配置"
	}
	switch status {
	case "ready":
		return "就绪"
	case "local_mcp_ready":
		return "本地 MCP 就绪"
	case "polling", "starting", "resuming":
		return "连接中"
	case "degraded":
		return "降级"
	case "backoff":
		return "退避中"
	case "auth_failed":
		return "认证失败"
	case "stopped":
		return "已停止"
	default:
		return "未知"
	}
}

func localizedTransport(value string) string {
	switch value {
	case "local":
		return "本地"
	case "cloudflare_named":
		return "Cloudflare Tunnel"
	case "openai_runtime":
		return "OpenAI Runtime"
	default:
		return "未知传输"
	}
}

func localizedOutcome(value string) string {
	switch value {
	case "success", "ok", "allowed":
		return "成功"
	case "rejected", "denied":
		return "拒绝"
	case "error", "failed":
		return "失败"
	default:
		return "记录"
	}
}

func connectionLabel(c Connection) string {
	if !c.Enabled {
		return "已停用"
	}
	if !c.StatusKnown {
		return "未知"
	}
	return localizedStatus(c.State, false)
}

func auditLabel(a AuditStatus) string {
	if !a.Available {
		return "不可用"
	}
	if a.Corrupt {
		return "部分损坏"
	}
	if a.Truncated {
		return "有界视图"
	}
	return "正常"
}

func auditColor(a AuditStatus) struct{ fg, bg uint32 } {
	if !a.Available || a.Corrupt {
		return struct{ fg, bg uint32 }{colorRed, colorRedSoft}
	}
	if a.Truncated {
		return struct{ fg, bg uint32 }{colorAmber, colorAmberSoft}
	}
	return struct{ fg, bg uint32 }{colorGreen, colorGreenSoft}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxVisibleRows(available, rowHeight, gap int) int {
	if available < rowHeight {
		return 0
	}
	return (available + gap) / (rowHeight + gap)
}

func moveControl(hwnd windows.HWND, x, y, width, height int) {
	if hwnd != 0 {
		_, _, _ = procMoveWindow.Call(uintptr(hwnd), uintptr(x), uintptr(y), uintptr(width), uintptr(height), 1)
	}
}

func (a *previewWindow) connectAsync() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.connectionState.Busy || a.exiting {
		a.mu.Unlock()
		return
	}
	connector := a.connector
	previous := NormalizeConnectionResult(a.connectionState.Result)
	a.connectionState = ConnectionControlState{Busy: true, Result: ConnectionResult{
		Phase:           ConnectionPreflight,
		PublicHost:      previous.PublicHost,
		TokenConfigured: previous.TokenConfigured,
	}}
	a.mu.Unlock()
	a.setStatus("正在连接…")
	a.render()
	if connector == nil {
		a.mu.Lock()
		a.pendingConnect = ConnectionResult{Phase: ConnectionFailed, Code: ConnectionConnectorMissing, PublicHost: previous.PublicHost, TokenConfigured: previous.TokenConfigured}
		a.mu.Unlock()
		postWindowMessage(a.hwnd, wmAppConnectDone, 0, 0)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var result ConnectionResult
		if previous.Phase == ConnectionReady {
			if progressReconnector, ok := connector.(ProgressReconnector); ok {
				result = progressReconnector.ReconnectWithProgress(ctx, func(progress ConnectionProgress) {
					a.queueConnectProgress(progress)
				})
			} else if reconnector, ok := connector.(Reconnector); ok {
				result = reconnector.Reconnect(ctx)
			} else {
				result = connector.Connect(ctx)
			}
		} else if progressConnector, ok := connector.(ProgressConnector); ok {
			result = progressConnector.ConnectWithProgress(ctx, func(progress ConnectionProgress) {
				a.queueConnectProgress(progress)
			})
		} else {
			result = connector.Connect(ctx)
		}
		result = normalizeFinishedConnectionResult(result)
		a.mu.Lock()
		a.pendingConnect = result
		exiting := a.exiting
		a.mu.Unlock()
		if !exiting {
			postWindowMessage(a.hwnd, wmAppConnectDone, 0, 0)
		}
	}()
}

func (a *previewWindow) queueConnectProgress(progress ConnectionProgress) {
	a.mu.Lock()
	a.pendingConnectProgress = NormalizeConnectionResult(ConnectionResult{
		Phase: progress.Phase, Code: progress.Code, PublicHost: progress.PublicHost, TokenConfigured: progress.TokenConfigured,
	})
	exiting := a.exiting
	a.mu.Unlock()
	if !exiting {
		postWindowMessage(a.hwnd, wmAppConnectProgress, 0, 0)
	}
}

func (a *previewWindow) finishConnectProgress() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.connectionState.Busy {
		a.mu.Unlock()
		return
	}
	progress := a.pendingConnectProgress
	a.connectionState.Result = progress
	a.mu.Unlock()
	if progress.Phase != ConnectionIdle {
		a.setStatus(connectionPhaseLabel(progress.Phase) + "…")
	}
	a.render()
}

func (a *previewWindow) finishConnect() {
	if a == nil {
		return
	}
	a.mu.Lock()
	result := normalizeFinishedConnectionResult(a.pendingConnect)
	a.connectionState = ConnectionControlState{Result: result}
	a.mu.Unlock()
	display := a.connectionState.Display()
	if display.Healthy {
		a.setStatus("Tunnel 已连接")
	} else {
		a.setStatus(display.Title)
	}
	a.render()
}

func normalizeFinishedConnectionResult(result ConnectionResult) ConnectionResult {
	result = NormalizeConnectionResult(result)
	if result.Phase == ConnectionReady {
		result.Code = ConnectionOK
		return result
	}
	if result.Phase != ConnectionFailed {
		if result.Code == "" {
			result.Code = ConnectionUnknown
		}
		result.Phase = ConnectionFailed
	}
	if result.Code == "" || result.Code == ConnectionOK {
		result.Code = ConnectionUnknown
	}
	return result
}

func (a *previewWindow) refreshAsync() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.refreshing || a.exiting {
		a.mu.Unlock()
		return
	}
	a.refreshing = true
	a.mu.Unlock()
	a.setStatus("Refreshing...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		snapshot, err := a.model.Refresh(ctx)
		a.mu.Lock()
		a.pendingSnapshot = snapshot
		a.pendingRefreshErr = err != nil
		exiting := a.exiting
		a.mu.Unlock()
		if !exiting {
			postWindowMessage(a.hwnd, wmAppRefreshDone, 0, 0)
		}
	}()
}

func (a *previewWindow) finishRefresh() {
	a.mu.Lock()
	snapshot := a.pendingSnapshot
	failed := a.pendingRefreshErr
	a.refreshing = false
	a.mu.Unlock()
	if failed {
		a.setStatus("Refresh unavailable")
		return
	}
	a.current = sanitizeSnapshot(snapshot)
	a.setStatus("Ready")
	a.render()
}

func (a *previewWindow) exportAsync() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.exporting || a.exiting {
		a.mu.Unlock()
		return
	}
	a.exporting = true
	a.mu.Unlock()
	a.setStatus("Preparing diagnostics...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := a.model.ExportDiagnostics(ctx)
		if err == nil {
			data, err = ValidateAndScrubExport(data)
		}
		a.mu.Lock()
		a.pendingExport = append([]byte(nil), data...)
		a.pendingExportErr = err != nil
		exiting := a.exiting
		a.mu.Unlock()
		if !exiting {
			postWindowMessage(a.hwnd, wmAppExportDone, 0, 0)
		}
	}()
}

func (a *previewWindow) finishExport() {
	a.mu.Lock()
	data := append([]byte(nil), a.pendingExport...)
	failed := a.pendingExportErr
	a.exporting = false
	a.mu.Unlock()
	if failed || len(data) == 0 {
		a.setStatus("Diagnostics unavailable")
		showMessage(a.hwnd, "Diagnostics are unavailable or failed the safety check.", "Local-Probe Preview", 0x00000010)
		return
	}
	path, ok := chooseExportPath(a.hwnd)
	if !ok {
		a.setStatus("Ready")
		return
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	if err := WriteSupportBundle(path, data); err != nil {
		a.setStatus("Export failed")
		showMessage(a.hwnd, "The diagnostic file was not written.", "Local-Probe Preview", 0x00000010)
		return
	}
	a.setStatus("Diagnostics exported")
	showMessage(a.hwnd, "Diagnostics exported.", "Local-Probe Preview", 0x00000040)
}

func (a *previewWindow) render() {
	if a == nil || a.hwnd == 0 {
		return
	}
	invalidate(a.hwnd)
}

func (a *previewWindow) setStatus(value string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.statusText = value
	a.mu.Unlock()
	invalidate(a.hwnd)
}

func (a *previewWindow) windowVisible() bool {
	if a == nil || a.hwnd == 0 {
		return false
	}
	result, _, _ := procIsWindowVisible.Call(uintptr(a.hwnd))
	return result != 0
}

func (a *previewWindow) hideWindow() {
	if a == nil || a.hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(uintptr(a.hwnd), showHide)
}

func (a *previewWindow) toggleWindow() {
	if a.windowVisible() {
		a.hideWindow()
		return
	}
	a.showWindow()
}

func (a *previewWindow) setFontScale(scale FontScale) {
	if a == nil {
		return
	}
	a.fontScale = NormalizeFontScale(scale)
	atomic.StoreUint32(&activeRenderFontScale, uint32(a.fontScale))
	a.render()
}

func (a *previewWindow) showWindow() {
	if a == nil {
		return
	}
	_, _, _ = procShowWindow.Call(uintptr(a.hwnd), showNormal)
	_, _, _ = procSetForegroundWindow.Call(uintptr(a.hwnd))
	if a.current.SchemaVersion == "" {
		a.refreshAsync()
	}
	a.render()
}

func (a *previewWindow) exit() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.exiting {
		a.mu.Unlock()
		return
	}
	a.exiting = true
	a.mu.Unlock()
	_, _, _ = procDestroyWindow.Call(uintptr(a.hwnd))
}

func registerTaskbarMessage() uint32 {
	name := wideString("TaskbarCreated")
	value, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	return uint32(value)
}

func (a *previewWindow) addTrayIcon() {
	if a == nil || a.hwnd == 0 {
		return
	}
	if a.icon == 0 {
		icon, err := loadPreviewIcon(32)
		if err == nil {
			a.icon = windows.Handle(icon)
			a.iconOwned = true
		} else {
			icon, _, _ = procLoadIconW.Call(0, idiApplication)
			a.icon = windows.Handle(icon)
			a.iconOwned = false
		}
	}
	nid := notifyIconDataW{CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})), HWnd: a.hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: trayCallback, HIcon: a.icon, TimeoutOrVersion: notifyVersion}
	copy(nid.Tip[:], utf16.Encode([]rune("Local-Probe Preview")))
	if result, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); result != 0 {
		a.tray = true
		// The version call is best effort. Shell_NotifyIcon accepts the same
		// structure with NIM_SETVERSION and the version in the union field.
		nid.UFlags = 0
		_, _, _ = procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
	}
}

func (a *previewWindow) removeTrayIcon() {
	if a == nil {
		return
	}
	if a.tray {
		nid := notifyIconDataW{CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})), HWnd: a.hwnd, UID: 1}
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	}
	a.tray = false
	if a.iconOwned {
		destroyPreviewIcon(uintptr(a.icon))
	}
	a.icon = 0
	a.iconOwned = false
}

func (a *previewWindow) trayMessage(message uint32) {
	switch trayEventCode(message) {
	case wmLButtonDblClk, ninSelect:
		a.showWindow()
	case wmRButtonUp, wmContextMenu:
		a.showTrayMenu()
	}
}

func (a *previewWindow) showTrayMenu() {
	if a == nil {
		return
	}
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	model := buildTrayMenuModel(a.windowVisible(), a.fontScale)
	var fontMenu uintptr
	for _, item := range model.Items {
		switch item.Kind {
		case trayMenuSeparator:
			appendMenu(menu, menuSeparator, 0, "")
		case trayMenuSubmenu:
			fontMenu, _, _ = procCreatePopupMenu.Call()
			if fontMenu == 0 {
				continue
			}
			var firstFont, lastFont, checkedFont uintptr
			for _, fontItem := range model.FontItems {
				flags := uint32(menuString | menuRadioCheck)
				if fontItem.Checked {
					flags |= menuChecked
				}
				appendMenu(fontMenu, flags, uintptr(fontItem.Command), fontItem.Label)
				if firstFont == 0 {
					firstFont = uintptr(fontItem.Command)
				}
				lastFont = uintptr(fontItem.Command)
				if fontItem.Checked {
					checkedFont = uintptr(fontItem.Command)
				}
			}
			if firstFont != 0 && checkedFont != 0 {
				_, _, _ = procCheckMenuRadioItem.Call(fontMenu, firstFont, lastFont, checkedFont, menuByCommand)
			}
			appendMenu(menu, menuPopup|menuString, fontMenu, item.Label)
		default:
			appendMenu(menu, menuString, uintptr(item.Command), item.Label)
		}
	}
	var point winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	_, _, _ = procSetForegroundWindow.Call(uintptr(a.hwnd))
	choice, _, _ := procTrackPopupMenu.Call(menu, trackRightButton|trackReturnCmd, uintptr(point.X), uintptr(point.Y), 0, uintptr(a.hwnd), 0)
	if choice != 0 {
		a.command(uint32(choice))
	}
	// A WM_NULL lets the shell dismiss the menu cleanly when it loses focus.
	postWindowMessage(a.hwnd, 0, 0, 0)
}

func appendMenu(menu uintptr, flags uint32, id uintptr, title string) {
	text := wideString(title)
	_, _, _ = procAppendMenuW.Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(text)))
}

func postWindowMessage(hwnd windows.HWND, message uint32, wParam, lParam uintptr) {
	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	}
}

func setControlText(hwnd windows.HWND, value string) {
	if hwnd == 0 {
		return
	}
	text := wideString(value)
	_, _, _ = procSetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(text)))
}

func showMessage(hwnd windows.HWND, text, caption string, flags uint32) {
	message := wideString(text)
	title := wideString(caption)
	_, _, _ = procMessageBoxW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), uintptr(flags))
}

func chooseExportPath(hwnd windows.HWND) (string, bool) {
	buffer := make([]uint16, 32768)
	copy(buffer, utf16.Encode([]rune("local-probe-diagnostics.json")))
	filter := wideBuffer("JSON files (*.json)\x00*.json\x00All files (*.*)\x00*.*\x00\x00")
	defaultExt := wideString("json")
	title := wideString("Export diagnostics")
	dialog := openFileNameW{StructSize: uint32(unsafe.Sizeof(openFileNameW{})), Owner: hwnd, Filter: &filter[0], FilterIndex: 1, File: &buffer[0], MaxFile: uint32(len(buffer)), Title: title, DefaultExt: defaultExt, Flags: ofNExplorer | ofNPathMustExist | ofNNoChangeDir | ofNOverwritePrompt | ofNHideReadOnly}
	if result, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&dialog))); result == 0 {
		return "", false
	}
	return windows.UTF16ToString(buffer), true
}

func wideString(value string) *uint16 {
	data, err := windows.UTF16FromString(value)
	if err != nil || len(data) == 0 {
		return &[]uint16{0}[0]
	}
	return &data[0]
}

func wideBuffer(value string) []uint16 {
	data := utf16.Encode([]rune(value))
	if len(data) == 0 || data[len(data)-1] != 0 {
		data = append(data, 0)
	}
	return data
}
