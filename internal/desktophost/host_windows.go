//go:build windows

package desktophost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/LE-saber/Local-Probe/internal/desktopbridge"
	"github.com/LE-saber/Local-Probe/internal/desktopweb"
	"github.com/LE-saber/Local-Probe/internal/previewui"
	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const (
	wmCreate          = 0x0001
	wmDestroy         = 0x0002
	wmSize            = 0x0005
	wmMove            = 0x0003
	wmDPIChanged      = 0x02E0
	wmClose           = 0x0010
	wmCommand         = 0x0111
	wmAppRPCReady     = 0x8000 + 31
	wmAppNativeDialog = 0x8000 + 32
	wmAppExitDone     = 0x8000 + 33
	wmAppFatal        = 0x8000 + 34
	wmTray            = 0x8000 + 35

	wsOverlappedWindow = 0x00CF0000
	wsExAppWindow      = 0x00040000

	swHide    = 0
	swShow    = 5
	swRestore = 9

	wmUser         = 0x0400
	trayCallback   = wmUser + 91
	trayOpen       = 101
	trayExit       = 102
	menuString     = 0x00000000
	trackRight     = 0x00000002
	trackReturnCmd = 0x00000100

	nimAdd        = 0x00000000
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004
	notifyVersion = 4

	ofNExplorer           = 0x00080000
	ofNPathMustExist      = 0x00000800
	ofNNoChangeDir        = 0x00000008
	ofNHideReadOnly       = 0x00000004
	ofNFileMustExist      = 0x00001000
	ofNNoDereferenceLinks = 0x00100000

	workspaceBIFReturnOnlyFSDirs = 0x00000001
	workspaceBIFNewDialogStyle   = 0x00000040
	workspaceSIGDNFileSysPath    = 0x80058000
	workspaceFOSAllowMultiSelect = 0x00000200
	workspaceFOSForceFileSystem  = 0x00000040
	workspaceFOSPickFolders      = 0x00000020
	workspaceCLSCTXInProcServer  = 0x1
	workspaceHResultCanceled     = 0x800704c7
	workspaceHResultChangedMode  = 0x80010106
)

var (
	user32Host   = windows.NewLazySystemDLL("user32.dll")
	shell32Host  = windows.NewLazySystemDLL("shell32.dll")
	kernel32Host = windows.NewLazySystemDLL("kernel32.dll")
	ole32Host    = windows.NewLazySystemDLL("ole32.dll")
	comdlg32Host = windows.NewLazySystemDLL("comdlg32.dll")

	procRegisterClassExWHost     = user32Host.NewProc("RegisterClassExW")
	procCreateWindowExWHost      = user32Host.NewProc("CreateWindowExW")
	procDefWindowProcWHost       = user32Host.NewProc("DefWindowProcW")
	procDestroyWindowHost        = user32Host.NewProc("DestroyWindow")
	procShowWindowHost           = user32Host.NewProc("ShowWindow")
	procFindWindowExWHost        = user32Host.NewProc("FindWindowExW")
	procGetWindowThreadPIDHost   = user32Host.NewProc("GetWindowThreadProcessId")
	procGetClassNameWHost        = user32Host.NewProc("GetClassNameW")
	procGetClientRectHost        = user32Host.NewProc("GetClientRect")
	procGetMessageWHost          = user32Host.NewProc("GetMessageW")
	procTranslateMessageHost     = user32Host.NewProc("TranslateMessage")
	procDispatchMessageHost      = user32Host.NewProc("DispatchMessageW")
	procPostQuitMessageHost      = user32Host.NewProc("PostQuitMessage")
	procPostMessageWHost         = user32Host.NewProc("PostMessageW")
	procRegisterMessageWHost     = user32Host.NewProc("RegisterWindowMessageW")
	procLoadCursorWHost          = user32Host.NewProc("LoadCursorW")
	procLoadIconWHost            = user32Host.NewProc("LoadIconW")
	procSetForegroundWindowHost  = user32Host.NewProc("SetForegroundWindow")
	procGetCursorPosHost         = user32Host.NewProc("GetCursorPos")
	procCreatePopupMenuHost      = user32Host.NewProc("CreatePopupMenu")
	procAppendMenuWHost          = user32Host.NewProc("AppendMenuW")
	procTrackPopupMenuHost       = user32Host.NewProc("TrackPopupMenu")
	procDestroyMenuHost          = user32Host.NewProc("DestroyMenu")
	procGetModuleHandleWHost     = kernel32Host.NewProc("GetModuleHandleW")
	procShellNotifyIconWHost     = shell32Host.NewProc("Shell_NotifyIconW")
	procGetSaveFileNameWHost     = comdlg32Host.NewProc("GetSaveFileNameW")
	procGetOpenFileNameWHost     = comdlg32Host.NewProc("GetOpenFileNameW")
	procCommDlgExtendedErrorHost = comdlg32Host.NewProc("CommDlgExtendedError")
	procCoInitializeExHost       = ole32Host.NewProc("CoInitializeEx")
	procCoUninitializeHost       = ole32Host.NewProc("CoUninitialize")
	procCoCreateInstanceHost     = ole32Host.NewProc("CoCreateInstance")
	procCoTaskMemFreeHost        = ole32Host.NewProc("CoTaskMemFree")
	procSHBrowseForFolderWHost   = shell32Host.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListEx    = shell32Host.NewProc("SHGetPathFromIDListEx")
	procSHGetPathFromIDListWHost = shell32Host.NewProc("SHGetPathFromIDListW")
	procSHGetPathFromIDListW     = shell32Host.NewProc("SHGetPathFromIDListW")
)

var (
	hostClassOnce sync.Once
	hostClassErr  error
	hostWindows   sync.Map
	hostDiagMu    sync.Mutex
	hostDiagFile  *os.File
	hostDiagSeen  = make(map[string]struct{})
	fileOpenCLSID = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xef}}
	fileOpenIID   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

var (
	ErrAlreadyRunning = errors.New("desktop host already running")
	ErrUnsupported    = errors.New("desktop host requires Windows amd64")
	ErrSecureHost     = errors.New("desktop host security initialization failed")
)

type Options struct {
	ConfigPath string
	AuditDir   string
	RepoRoot   string
	Transport  string
}

type hostWindow struct {
	hwnd              windows.HWND
	web               *edge.Chromium
	service           *desktopbridge.Service
	taskbarMsg        uint32
	trayAdded         bool
	icon              windows.Handle
	trusted           atomic.Bool
	fatal             atomic.Bool
	rpcQueue          chan string
	uiResults         chan string
	acceptMu          sync.Mutex
	accepting         bool
	stopWorkers       chan struct{}
	workerDone        chan struct{}
	dispatcherStarted atomic.Bool
	stopOnce          sync.Once
	dialogs           chan *nativeDialogRequest
	exitMu            sync.Mutex
	exitPending       bool
	exitClean         bool
}

type nativeDialogRequest struct {
	ctx    context.Context
	kind   desktopbridge.ExportKind
	data   []byte
	result chan nativeDialogResult
}

type nativeDialogResult struct {
	paths []string
	data  []byte
	err   error
}

type hostWndClassEx struct {
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

type hostPoint struct{ X, Y int32 }
type hostRect struct{ Left, Top, Right, Bottom int32 }

type hostMessage struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      hostPoint
	Private uint32
}

type hostNotifyIconData struct {
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

type hostOpenFileName struct {
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

type hostBrowseInfo struct {
	Owner       windows.HWND
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	Param       uintptr
	Image       int32
}

type hostShellItemArrayVtbl struct {
	IUnknown                   [3]uintptr
	BindToHandler              uintptr
	GetPropertyStore           uintptr
	GetPropertyDescriptionList uintptr
	GetAttributes              uintptr
	GetCount                   uintptr
	GetItemAt                  uintptr
}

type hostShellItemArray struct{ Vtbl *hostShellItemArrayVtbl }

type hostShellItemVtbl struct {
	IUnknown       [3]uintptr
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
}

type hostShellItem struct{ Vtbl *hostShellItemVtbl }

func Run(options Options) (runErr error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" || unsafe.Sizeof(uintptr(0)) != 8 {
		return ErrUnsupported
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Must precede COM, dialogs and every HWND (including WebView2 children).
	// Never fall back to bitmap scaling on unsupported/overridden systems.
	if err := enablePerMonitorV2(); err != nil {
		return err
	}
	if hr, _, _ := procCoInitializeExHost.Call(0, 0x2); int32(hr) < 0 {
		return ErrSecureHost
	} else {
		defer procCoUninitializeHost.Call()
	}
	if err := webviewloader.PrepareSecureDllSearch(); err != nil {
		return ErrSecureHost
	}
	mutexName := wideHost("Local\\Local-Probe-Desktop")
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if mutex != 0 {
			_ = windows.CloseHandle(mutex)
		}
		return ErrAlreadyRunning
	}
	if err != nil {
		return ErrSecureHost
	}
	defer windows.CloseHandle(mutex)
	startHostDiagnostics()
	defer closeHostDiagnostics()
	if err := registerHostWindowClass(); err != nil {
		return ErrSecureHost
	}
	instance, _, _ := procGetModuleHandleWHost.Call(0)
	if instance == 0 {
		return ErrSecureHost
	}
	className := wideHost("LocalProbeDesktopWindow")
	title := wideHost("Local-Probe")
	hwndValue, _, _ := procCreateWindowExWHost.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		0x80000000, 0x80000000, 1240, 800,
		0, 0, instance, 0,
	)
	if hwndValue == 0 {
		return ErrSecureHost
	}
	h := &hostWindow{
		hwnd:        windows.HWND(hwndValue),
		rpcQueue:    make(chan string, 16),
		uiResults:   make(chan string, 17),
		stopWorkers: make(chan struct{}),
		workerDone:  make(chan struct{}),
		dialogs:     make(chan *nativeDialogRequest, 1),
		accepting:   true,
	}
	hostWindows.Store(h.hwnd, h)
	if err := sizeInitialDPIWindow(h.hwnd); err != nil {
		hostWindows.Delete(h.hwnd)
		procDestroyWindowHost.Call(hwndValue)
		return err
	}
	cleanWindow := false
	defer func() {
		if cleanWindow {
			return
		}
		h.stopDispatcher()
		if h.service != nil {
			if closeErr := h.service.Close(); closeErr != nil {
				runErr = errors.Join(runErr, desktopbridge.ErrCloseStopFailed)
			}
		}
		if h.web != nil {
			if closeErr := h.web.CloseSecureHost(); closeErr != nil {
				runErr = errors.Join(runErr, ErrSecureHost)
			}
		}
		h.removeTrayIcon()
		hostWindows.Delete(h.hwnd)
		_, _, _ = procDestroyWindowHost.Call(hwndValue)
	}()

	h.service, err = desktopbridge.New(desktopbridge.Options{
		ConfigPath: options.ConfigPath,
		AuditDir:   options.AuditDir,
		RepoRoot:   options.RepoRoot,
		Transport:  options.Transport,
		PickFolders: func(ctx context.Context) ([]string, error) {
			result := h.requestNativeDialog(ctx, desktopbridge.ExportKind("pick"), nil)
			return result.paths, result.err
		},
		Export: func(ctx context.Context, kind desktopbridge.ExportKind, data []byte) error {
			return h.requestNativeDialog(ctx, kind, data).err
		},
		SaveBackup: func(ctx context.Context, data []byte) error {
			return h.requestNativeDialog(ctx, desktopbridge.ExportKind("backup-save"), data).err
		},
		LoadBackup: func(ctx context.Context) ([]byte, error) {
			result := h.requestNativeDialog(ctx, desktopbridge.ExportKind("backup-open"), nil)
			return result.data, result.err
		},
	})
	if err != nil {
		return ErrSecureHost
	}

	h.web = edge.NewChromium()
	if profile, profileErr := os.UserConfigDir(); profileErr == nil && profile != "" {
		h.web.DataPath = filepath.Join(profile, "Local-Probe", "Desktop", "WebView2")
	}
	if err := h.web.ConfigureSecureHost(edge.SecureHostCallbacks{
		DocumentURL: desktopweb.DocumentURL,
		MessageReceived: func(message, argsSource, topLevelSource string) {
			h.acceptMessage(message, argsSource, topLevelSource)
		},
		ResourceRequested: h.resourceRequested,
		NavigationStarting: func(uri string) bool {
			h.trusted.Store(false)
			allowed := uri == desktopweb.DocumentURL
			if allowed {
				writeHostDiagnostic("navigation_starting_allowed", 0)
			} else {
				writeHostDiagnostic("navigation_blocked", 0)
			}
			return allowed
		},
		NavigationCompleted: func(source string, succeeded bool) {
			trusted := succeeded && source == desktopweb.DocumentURL
			h.trusted.Store(trusted)
			switch {
			case trusted:
				writeHostDiagnostic("navigation_succeeded_trusted", 0)
				// Theme readiness is independent of backend snapshot/config health.
				h.postTrustedMessage(`{"event":"window.ready"}`)
			case !succeeded:
				writeHostDiagnostic("navigation_failed", 0)
			default:
				writeHostDiagnostic("navigation_source_mismatch", 0)
			}
		},
		Fatal: func(err error) {
			h.fatal.Store(true)
			writeHostDiagnostic("webview_fatal", hostHRESULTFromError(err))
			postHostMessage(h.hwnd, wmAppFatal, 0, 0)
		},
	}); err != nil {
		return ErrSecureHost
	}
	if !h.web.Embed(uintptr(h.hwnd)) || h.web.InitError() != nil {
		writeHostDiagnostic("webview_embed_failed", hostHRESULTFromError(h.web.InitError()))
		return ErrSecureHost
	}
	writeHostDiagnostic("webview_controller_ready", 0)
	if err := h.web.ConfigureSecureSettings(); err != nil {
		writeHostDiagnostic("webview_settings_failed", hostHRESULTFromError(err))
		return ErrSecureHost
	}
	// Match the document's initial dark palette before showing the window.
	// Persisted display preferences are synchronized when the trusted page is ready.
	if err := h.applyWindowTheme("dark"); err != nil {
		writeHostDiagnostic("window_theme_failed", hostHRESULTFromError(err))
	}
	if err := h.web.NavigateChecked(desktopweb.DocumentURL); err != nil {
		writeHostDiagnostic("webview_navigate_failed", hostHRESULTFromError(err))
		return ErrSecureHost
	}
	writeHostDiagnostic("navigation_requested", 0)
	h.startDispatcher()
	h.taskbarMsg = registerTaskbarMessageHost()
	h.addTrayIcon()
	_, _, _ = procShowWindowHost.Call(uintptr(h.hwnd), swShow)
	// The first ShowWindow call can defer to a GUI launcher's STARTUPINFO
	// visibility hint. A second explicit restore makes the native UI visible
	// when launched by schedulers and process wrappers.
	_, _, _ = procShowWindowHost.Call(uintptr(h.hwnd), swRestore)
	if !h.trayAdded {
		h.showError("系统托盘不可用。Local-Probe 会保持在任务栏；关闭窗口将退出并尝试停止本次连接。")
	}
	h.layoutWebView()
	if err := h.ensureControllerVisible(); err != nil {
		return ErrSecureHost
	}

	var msg hostMessage
	for {
		result, _, callErr := procGetMessageWHost.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == 0 {
			break
		}
		if int32(result) == -1 {
			if callErr != nil {
				return ErrSecureHost
			}
			return ErrSecureHost
		}
		_, _, _ = procTranslateMessageHost.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageHost.Call(uintptr(unsafe.Pointer(&msg)))
	}
	cleanWindow = h.exitClean
	if !h.exitClean {
		return ErrSecureHost
	}
	if h.fatal.Load() {
		return ErrSecureHost
	}
	return nil
}

func registerHostWindowClass() error {
	hostClassOnce.Do(func() {
		instance, _, _ := procGetModuleHandleWHost.Call(0)
		if instance == 0 {
			hostClassErr = errors.New("missing module handle")
			return
		}
		className := wideHost("LocalProbeDesktopWindow")
		icon, _, _ := procLoadIconWHost.Call(0, 32512)
		cursor, _, _ := procLoadCursorWHost.Call(0, 32512)
		class := hostWndClassEx{
			CbSize:    uint32(unsafe.Sizeof(hostWndClassEx{})),
			Style:     0x0003,
			WndProc:   syscall.NewCallback(hostWindowProc),
			HInstance: windows.Handle(instance), HIcon: windows.Handle(icon), HIconSm: windows.Handle(icon),
			HCursor: windows.Handle(cursor), ClassName: className,
		}
		atom, _, callErr := procRegisterClassExWHost.Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 && !errors.Is(callErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
			hostClassErr = callErr
		}
	})
	return hostClassErr
}

func hostWindowProc(hwndValue uintptr, message uint32, wParam, lParam uintptr) uintptr {
	hwnd := windows.HWND(hwndValue)
	if value, ok := hostWindows.Load(hwnd); ok {
		return value.(*hostWindow).handleMessage(message, wParam, lParam)
	}
	value, _, _ := procDefWindowProcWHost.Call(hwndValue, uintptr(message), wParam, lParam)
	return value
}

func (h *hostWindow) handleMessage(message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmSize:
		h.layoutWebView()
	case wmMove:
		h.layoutWebView()
	case wmDPIChanged:
		if lParam != 0 {
			rect, err := dpiSuggestedRect(lParam)
			if err == nil {
				err = applyDPIWindowRect(h.hwnd, rect)
			}
			if err != nil {
				writeHostDiagnostic("dpi_resize_failed", 0)
			}
		}
		h.layoutWebView()
		return 0
	case wmClose:
		if h.trayAdded {
			h.hideWindow()
		} else {
			h.beginExit()
		}
		return 0
	case wmCommand:
		switch uint32(wParam & 0xffff) {
		case trayOpen:
			h.showWindow()
		case trayExit:
			h.beginExit()
		}
	case trayCallback:
		switch uint32(lParam & 0xffff) {
		case 0x0203, 0x0400:
			h.showWindow()
		case 0x0205, 0x007b:
			h.showTrayMenu()
		}
	case wmAppRPCReady:
		h.deliverResponses()
	case wmAppNativeDialog:
		h.runNativeDialogs()
	case wmAppExitDone:
		h.finishExit(wParam)
	case wmAppFatal:
		h.beginExit()
	case wmDestroy:
		h.removeTrayIcon()
		hostWindows.Delete(h.hwnd)
		_, _, _ = procPostQuitMessageHost.Call(0)
	}
	if h.taskbarMsg != 0 && message == h.taskbarMsg {
		h.addTrayIcon()
	}
	value, _, _ := procDefWindowProcWHost.Call(uintptr(h.hwnd), uintptr(message), wParam, lParam)
	return value
}

func (h *hostWindow) layoutWebView() {
	if h == nil || h.web == nil || h.web.GetController() == nil {
		return
	}
	var rect hostRect
	if result, _, _ := procGetClientRectHost.Call(uintptr(h.hwnd), uintptr(unsafe.Pointer(&rect))); result == 0 {
		writeHostDiagnostic("client_rect_failed", 0)
		return
	}
	if _, _, _, _, err := h.web.GetControllerBoundsChecked(); err != nil {
		writeHostDiagnostic("controller_bounds_get_failed", hostHRESULTFromError(err))
		return
	}
	if err := h.web.SetControllerBoundsChecked(rect.Left, rect.Top, rect.Right, rect.Bottom); err != nil {
		writeHostDiagnostic("controller_bounds_put_failed", hostHRESULTFromError(err))
		return
	}
	if err := h.web.NotifyParentWindowPositionChangedChecked(); err != nil {
		writeHostDiagnostic("controller_position_notify_failed", hostHRESULTFromError(err))
	}
}

func (h *hostWindow) ensureControllerVisible() error {
	if h == nil || h.web == nil || h.web.GetController() == nil {
		return errors.New("WebView2 controller unavailable")
	}
	visible, err := h.web.GetControllerVisibleChecked()
	if err != nil {
		writeHostDiagnostic("controller_visibility_get_failed", hostHRESULTFromError(err))
		return err
	}
	if visible {
		writeHostDiagnostic("controller_visible", 0)
		return nil
	}
	writeHostDiagnostic("controller_was_hidden", 0)
	if err := h.web.SetControllerVisibleChecked(true); err != nil {
		writeHostDiagnostic("controller_visibility_put_failed", hostHRESULTFromError(err))
		return err
	}
	confirmed, err := h.web.GetControllerVisibleChecked()
	if err != nil {
		writeHostDiagnostic("controller_visibility_confirm_failed", hostHRESULTFromError(err))
		return err
	}
	if !confirmed {
		writeHostDiagnostic("controller_visibility_still_hidden", 0)
		return errors.New("WebView2 controller remained hidden")
	}
	writeHostDiagnostic("controller_made_visible", 0)
	return nil
}

func (h *hostWindow) acceptMessage(message, argsSource, topLevelSource string) {
	if !h.trusted.Load() || argsSource != desktopweb.DocumentURL || topLevelSource != desktopweb.DocumentURL {
		return
	}
	if len(message) == 0 || len(message) > 64<<10 {
		h.replyError(requestID(message), "invalid_request")
		return
	}
	h.acceptMu.Lock()
	if !h.accepting {
		h.acceptMu.Unlock()
		h.replyError(requestID(message), "closed")
		return
	}
	if theme, handled, err := parseWindowTheme(message); handled {
		h.acceptMu.Unlock()
		if err != nil {
			h.replyError(requestID(message), "invalid_params")
			return
		}
		if err := h.applyWindowTheme(theme); err != nil {
			writeHostDiagnostic("window_theme_failed", hostHRESULTFromError(err))
			h.replyError(requestID(message), "theme_unavailable")
			return
		}
		writeHostDiagnostic("window_theme_"+theme+"_applied", 0)
		raw, _ := json.Marshal(struct {
			ID   json.RawMessage `json:"id"`
			OK   bool            `json:"ok"`
			Data struct {
				Theme string `json:"theme"`
			} `json:"data"`
		}{ID: requestID(message), OK: true, Data: struct {
			Theme string `json:"theme"`
		}{Theme: theme}})
		h.postTrustedMessage(string(raw))
		return
	}
	defer h.acceptMu.Unlock()
	select {
	case h.rpcQueue <- message:
	default:
		h.replyError(requestID(message), "busy")
	}
}

func (h *hostWindow) startDispatcher() {
	h.dispatcherStarted.Store(true)
	go func() {
		defer close(h.workerDone)
		for {
			select {
			case <-h.stopWorkers:
				return
			default:
			}
			select {
			case <-h.stopWorkers:
				return
			case raw := <-h.rpcQueue:
				response := h.service.Handle(raw)
				select {
				case h.uiResults <- response:
					if !postHostMessage(h.hwnd, wmAppRPCReady, 0, 0) {
						h.fatal.Store(true)
						postHostMessage(h.hwnd, wmAppFatal, 0, 0)
					}
				case <-h.stopWorkers:
					return
				}
			}
		}
	}()
}

func (h *hostWindow) stopDispatcher() {
	if h.workerDone == nil || !h.dispatcherStarted.Load() {
		return
	}
	h.acceptMu.Lock()
	h.accepting = false
	h.stopOnce.Do(func() { close(h.stopWorkers) })
	h.acceptMu.Unlock()
	<-h.workerDone
}

func (h *hostWindow) deliverResponses() {
	for {
		select {
		case raw := <-h.uiResults:
			h.postTrustedMessage(raw)
		default:
			return
		}
	}
}

func (h *hostWindow) postTrustedMessage(raw string) {
	if h == nil || !h.trusted.Load() || h.web == nil {
		writeHostDiagnostic("web_message_reply_skipped_untrusted", 0)
		return
	}
	current, err := h.web.GetSourceChecked()
	if err != nil {
		writeHostDiagnostic("web_message_source_get_failed", hostHRESULTFromError(err))
		return
	}
	if current != desktopweb.DocumentURL {
		h.trusted.Store(false)
		writeHostDiagnostic("web_message_source_mismatch", 0)
		return
	}
	if err := h.web.PostWebMessageAsStringChecked(raw); err != nil {
		writeHostDiagnostic("web_message_post_failed", hostHRESULTFromError(err))
	}
}

func (h *hostWindow) replyError(id json.RawMessage, code string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	data, _ := json.Marshal(struct {
		ID    json.RawMessage `json:"id"`
		OK    bool            `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{ID: id, Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: "The local desktop host cannot process this request right now."}})
	h.postTrustedMessage(string(data))
}

func requestID(raw string) json.RawMessage {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil
	}
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope.ID) == 0 || len(envelope.ID) > 128 {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(envelope.ID)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil
	}
	switch value.(type) {
	case nil, string, json.Number:
		return envelope.ID
	default:
		return nil
	}
}

func (h *hostWindow) resourceRequested(rawURI, method string) (int, string, string, []byte) {
	resource := localAsset(rawURI, method)
	assetCode := ""
	switch rawURI {
	case desktopweb.DocumentURL:
		assetCode = "asset_html"
	case "https://localassets/app.js":
		assetCode = "asset_js"
	case "https://localassets/app.css":
		assetCode = "asset_css"
	}
	if assetCode != "" {
		if method == "GET" && resource.status == 200 {
			writeHostDiagnostic(assetCode+"_served", 0)
		} else {
			writeHostDiagnostic(assetCode+"_denied", 0)
		}
	}
	return resource.status, resource.reason, resource.headers, resource.body
}

func (h *hostWindow) requestNativeDialog(ctx context.Context, kind desktopbridge.ExportKind, data []byte) nativeDialogResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	request := &nativeDialogRequest{ctx: ctx, kind: kind, data: append([]byte(nil), data...), result: make(chan nativeDialogResult, 1)}
	select {
	case h.dialogs <- request:
	default:
		return nativeDialogResult{err: errors.New("native dialog busy")}
	}
	if !postHostMessage(h.hwnd, wmAppNativeDialog, 0, 0) {
		return nativeDialogResult{err: errors.New("native dialog unavailable")}
	}
	select {
	case result := <-request.result:
		return result
	case <-ctx.Done():
		// runNativeDialog observes this deadline and dismisses the modal native
		// window. Keep the serialized bridge worker parked until that STA call
		// returns, so another RPC cannot overlap the stale dialog.
		result := <-request.result
		if result.err == nil {
			result.err = ctx.Err()
		}
		return result
	}
}

func (h *hostWindow) runNativeDialogs() {
	for {
		select {
		case request := <-h.dialogs:
			result := h.runNativeDialog(request)
			select {
			case request.result <- result:
			default:
			}
		default:
			return
		}
	}
}

func (h *hostWindow) runNativeDialog(request *nativeDialogRequest) (result nativeDialogResult) {
	defer func() {
		if recover() != nil {
			result = nativeDialogResult{err: errors.New("native dialog failed")}
		}
	}()
	if request == nil || request.ctx.Err() != nil {
		return nativeDialogResult{err: context.Canceled}
	}
	dialogDone := make(chan struct{})
	go cancelHostModalOnDeadline(request.ctx, dialogDone)
	defer close(dialogDone)
	if request.kind == "backup-save" || request.kind == "backup-open" {
		saving := request.kind == "backup-save"
		path, err := chooseHostBackupPath(h.hwnd, saving)
		if err != nil {
			return nativeDialogResult{err: err}
		}
		if request.ctx.Err() != nil {
			return nativeDialogResult{err: request.ctx.Err()}
		}
		if saving {
			return nativeDialogResult{err: desktopbridge.SaveBackupFile(path, request.data)}
		}
		data, err := desktopbridge.ReadBackupFile(path)
		return nativeDialogResult{data: data, err: err}
	}
	if request.kind == desktopbridge.ExportKind("pick") {
		paths, err := showFolderDialog(h.hwnd)
		if err == nil && request.ctx.Err() == nil {
			return nativeDialogResult{paths: paths}
		}
		if request.ctx.Err() != nil {
			return nativeDialogResult{err: request.ctx.Err()}
		}
		return nativeDialogResult{err: err}
	}
	path, ok := chooseHostExportPath(h.hwnd, request.kind)
	if !ok {
		return nativeDialogResult{err: errors.New("export cancelled")}
	}
	if request.ctx.Err() != nil {
		return nativeDialogResult{err: request.ctx.Err()}
	}
	if err := previewui.SaveExport(path, request.data); err != nil {
		return nativeDialogResult{err: errors.New("export failed")}
	}
	return nativeDialogResult{}
}

func cancelHostModalOnDeadline(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-done:
		return
	}
	className := wideHost("#32770")
	for {
		select {
		case <-done:
			return
		default:
		}
		var previous windows.HWND
		for {
			value, _, _ := procFindWindowExWHost.Call(0, uintptr(previous), uintptr(unsafe.Pointer(className)), 0)
			if value == 0 {
				break
			}
			previous = windows.HWND(value)
			var processID uint32
			procGetWindowThreadPIDHost.Call(value, uintptr(unsafe.Pointer(&processID)))
			if int(processID) == os.Getpid() {
				_, _, _ = procPostMessageWHost.Call(value, 0x0111, 2, 0) // WM_COMMAND / IDCANCEL
			}
		}
		select {
		case <-done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func chooseHostWorkspaceFolder(owner windows.HWND) ([]string, error) {
	display := make([]uint16, 32768)
	title := wideHost("选择要授权的本地文件夹")
	info := hostBrowseInfo{Owner: owner, DisplayName: &display[0], Title: title, Flags: workspaceBIFReturnOnlyFSDirs | workspaceBIFNewDialogStyle}
	item, _, _ := procSHBrowseForFolderWHost.Call(uintptr(unsafe.Pointer(&info)))
	if item == 0 {
		return []string{}, nil
	}
	defer procCoTaskMemFreeHost.Call(item)
	pathBuffer := make([]uint16, 32768)
	result, _, callErr := procSHGetPathFromIDListEx.Call(item, uintptr(unsafe.Pointer(&pathBuffer[0])), uintptr(len(pathBuffer)), 0)
	if result == 0 && callErr != nil && errors.Is(callErr, windows.ERROR_PROC_NOT_FOUND) {
		result, _, _ = procSHGetPathFromIDListWHost.Call(item, uintptr(unsafe.Pointer(&pathBuffer[0])))
	}
	if result == 0 {
		return nil, errors.New("folder selection failed")
	}
	path := windows.UTF16ToString(pathBuffer)
	path, err := previewui.NormalizeWorkspaceInputPath(path)
	if err != nil || !filepath.IsAbs(path) {
		return nil, errors.New("folder selection failed")
	}
	return []string{path}, nil
}

func chooseHostExportPath(owner windows.HWND, kind desktopbridge.ExportKind) (string, bool) {
	buffer := make([]uint16, 32768)
	defaultName := "local-probe-" + string(kind) + "-" + time.Now().Format("20060102-150405") + ".json"
	copy(buffer, windows.StringToUTF16(defaultName))
	filter := utf16MultiString("JSON files (*.json)\x00*.json\x00\x00")
	title := wideHost("导出 Local-Probe 数据")
	defaultExt := wideHost("json")
	dialog := hostOpenFileName{
		StructSize: uint32(unsafe.Sizeof(hostOpenFileName{})), Owner: owner, Filter: &filter[0], FilterIndex: 1,
		File: &buffer[0], MaxFile: uint32(len(buffer)), Title: title, DefaultExt: defaultExt,
		Flags: ofNExplorer | ofNPathMustExist | ofNNoChangeDir | ofNHideReadOnly,
	}
	if result, _, _ := procGetSaveFileNameWHost.Call(uintptr(unsafe.Pointer(&dialog))); result == 0 {
		return "", false
	}
	return windows.UTF16ToString(buffer), true
}

func chooseHostBackupPath(owner windows.HWND, saving bool) (string, error) {
	buffer := make([]uint16, 32768)
	filter := utf16MultiString("Local-Probe 配置备份 (*.lpbackup)\x00*.lpbackup\x00\x00")
	title := wideHost("选择 Local-Probe 配置备份")
	flags := uint32(ofNExplorer | ofNPathMustExist | ofNNoChangeDir | ofNHideReadOnly | ofNNoDereferenceLinks)
	if saving {
		// The user edits the base name; we append the full suffix after selection.
		copy(buffer, windows.StringToUTF16("Local-Probe-备份-"+time.Now().Format("20060102-150405")))
		title = wideHost("保存配置备份（文件名无需填写后缀，请使用新名称）")
	} else {
		flags |= ofNFileMustExist
	}
	dialog := hostOpenFileName{StructSize: uint32(unsafe.Sizeof(hostOpenFileName{})), Owner: owner, Filter: &filter[0], FilterIndex: 1, File: &buffer[0], MaxFile: uint32(len(buffer)), Title: title, Flags: flags}
	var result uintptr
	if saving {
		result, _, _ = procGetSaveFileNameWHost.Call(uintptr(unsafe.Pointer(&dialog)))
	} else {
		result, _, _ = procGetOpenFileNameWHost.Call(uintptr(unsafe.Pointer(&dialog)))
	}
	if result == 0 {
		code, _, _ := procCommDlgExtendedErrorHost.Call()
		if code == 0 {
			return "", desktopbridge.ErrBackupCancelled
		}
		return "", errors.New("backup dialog failed")
	}
	path := windows.UTF16ToString(buffer)
	if saving {
		path = desktopbridge.AppendBackupExtension(path)
	}
	return path, nil
}

func (h *hostWindow) addTrayIcon() {
	if h == nil || h.hwnd == 0 {
		return
	}
	if h.icon == 0 {
		icon, _, _ := procLoadIconWHost.Call(0, 32512)
		h.icon = windows.Handle(icon)
	}
	data := hostNotifyIconData{CbSize: uint32(unsafe.Sizeof(hostNotifyIconData{})), HWnd: h.hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: trayCallback, HIcon: h.icon, TimeoutOrVersion: notifyVersion}
	copy(data.Tip[:], windows.StringToUTF16("Local-Probe"))
	if result, _, _ := procShellNotifyIconWHost.Call(nimAdd, uintptr(unsafe.Pointer(&data))); result != 0 {
		h.trayAdded = true
		data.UFlags = 0
		_, _, _ = procShellNotifyIconWHost.Call(nimSetVersion, uintptr(unsafe.Pointer(&data)))
	}
}

func (h *hostWindow) removeTrayIcon() {
	if h == nil || !h.trayAdded {
		return
	}
	data := hostNotifyIconData{CbSize: uint32(unsafe.Sizeof(hostNotifyIconData{})), HWnd: h.hwnd, UID: 1}
	_, _, _ = procShellNotifyIconWHost.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	h.trayAdded = false
}

func (h *hostWindow) showTrayMenu() {
	menu, _, _ := procCreatePopupMenuHost.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenuHost.Call(menu)
	appendHostMenu(menu, trayOpen, "打开 Local-Probe")
	appendHostMenu(menu, trayExit, "退出并停止本次连接")
	var point hostPoint
	_, _, _ = procGetCursorPosHost.Call(uintptr(unsafe.Pointer(&point)))
	_, _, _ = procSetForegroundWindowHost.Call(uintptr(h.hwnd))
	choice, _, _ := procTrackPopupMenuHost.Call(menu, trackRight|trackReturnCmd, uintptr(point.X), uintptr(point.Y), 0, uintptr(h.hwnd), 0)
	if choice != 0 {
		_, _, _ = procPostMessageWHost.Call(uintptr(h.hwnd), wmCommand, choice, 0)
	}
	_, _, _ = procPostMessageWHost.Call(uintptr(h.hwnd), 0, 0, 0)
}

func appendHostMenu(menu uintptr, id uint32, title string) {
	value := wideHost(title)
	_, _, _ = procAppendMenuWHost.Call(menu, menuString, uintptr(id), uintptr(unsafe.Pointer(value)))
}

func (h *hostWindow) showWindow() {
	_, _, _ = procShowWindowHost.Call(uintptr(h.hwnd), swRestore)
	h.layoutWebView()
	if err := h.ensureControllerVisible(); err != nil {
		h.showError("Local-Probe 的本机页面无法显示。请关闭窗口后重试；安全检查未放宽。")
	}
}

func (h *hostWindow) hideWindow() {
	if h.web != nil {
		_ = h.web.Hide()
	}
	_, _, _ = procShowWindowHost.Call(uintptr(h.hwnd), swHide)
}

func (h *hostWindow) beginExit() {
	h.exitMu.Lock()
	if h.exitPending || h.exitClean {
		h.exitMu.Unlock()
		return
	}
	h.exitPending = true
	h.exitMu.Unlock()
	go func() {
		h.stopDispatcher()
		if h.service != nil {
			if err := h.service.Close(); err != nil {
				postHostMessage(h.hwnd, wmAppExitDone, 1, 0)
				return
			}
		}
		postHostMessage(h.hwnd, wmAppExitDone, 0, 0)
	}()
}

func (h *hostWindow) finishExit(wParam uintptr) {
	if wParam != 0 {
		h.showError("Local-Probe 无法确认自有连接已停止。再次选择托盘“退出”可重试；连接清理失败前程序会保持运行。")
		h.exitMu.Lock()
		h.exitPending = false
		h.exitMu.Unlock()
		return
	}
	if h.web != nil {
		if err := h.web.CloseSecureHost(); err != nil {
			h.showError("Local-Probe 仍有本机 WebView2 资源未能安全关闭。再次选择托盘“退出”可重试。")
			h.exitMu.Lock()
			h.exitPending = false
			h.exitMu.Unlock()
			return
		}
	}
	h.exitMu.Lock()
	h.exitClean = true
	h.exitPending = false
	h.exitMu.Unlock()
	_, _, _ = procDestroyWindowHost.Call(uintptr(h.hwnd))
}

func (h *hostWindow) showError(message string) {
	text := wideHost(message)
	title := wideHost("Local-Probe")
	user32Host.NewProc("MessageBoxW").Call(uintptr(h.hwnd), uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x00000010)
}

func ShowStartupError(err error) {
	message := "Local-Probe 桌面无法启动；安全初始化失败，页面与本地调用未开放。"
	switch {
	case errors.Is(err, ErrAlreadyRunning):
		message = "Local-Probe 桌面已在运行。请从任务栏或系统托盘打开现有窗口。"
	case errors.Is(err, desktopbridge.ErrCloseStopFailed):
		message = "Local-Probe 无法确认本次连接已停止。程序会继续运行，请从托盘再次选择退出以重试。"
	case errors.Is(err, ErrUnsupported):
		message = "Local-Probe 桌面当前仅支持 64 位 Windows。"
	case errors.Is(err, ErrDPIUnsupported):
		message = "Local-Probe 无法启用清晰的逐显示器 DPI 模式。请使用 Windows 10 1703 或更新版本，并取消本程序兼容性设置中的高 DPI 缩放替代选项。"
	case err == nil:
		return
	}
	text := wideHost(message)
	title := wideHost("Local-Probe")
	user32Host.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x00000010)
}

func registerTaskbarMessageHost() uint32 {
	name := wideHost("TaskbarCreated")
	value, _, _ := procRegisterMessageWHost.Call(uintptr(unsafe.Pointer(name)))
	return uint32(value)
}

var hostDiagnosticCodes = map[string]struct{}{
	"window_theme_failed":                  {},
	"window_theme_dark_applied":            {},
	"window_theme_light_applied":           {},
	"navigation_requested":                 {},
	"navigation_starting_allowed":          {},
	"navigation_blocked":                   {},
	"navigation_succeeded_trusted":         {},
	"navigation_failed":                    {},
	"navigation_source_mismatch":           {},
	"webview_fatal":                        {},
	"webview_embed_failed":                 {},
	"webview_controller_ready":             {},
	"webview_settings_failed":              {},
	"webview_navigate_failed":              {},
	"asset_html_served":                    {},
	"asset_html_denied":                    {},
	"asset_js_served":                      {},
	"asset_js_denied":                      {},
	"asset_css_served":                     {},
	"asset_css_denied":                     {},
	"client_rect_failed":                   {},
	"controller_bounds_get_failed":         {},
	"controller_bounds_put_failed":         {},
	"controller_position_notify_failed":    {},
	"controller_visible":                   {},
	"controller_was_hidden":                {},
	"controller_made_visible":              {},
	"controller_visibility_get_failed":     {},
	"controller_visibility_put_failed":     {},
	"controller_visibility_confirm_failed": {},
	"controller_visibility_still_hidden":   {},
	"web_message_post_failed":              {},
	"web_message_reply_skipped_untrusted":  {},
	"web_message_source_get_failed":        {},
	"web_message_source_mismatch":          {},
}

func startHostDiagnostics() {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return
	}
	directory := filepath.Join(base, "Local-Probe", "Desktop")
	if os.MkdirAll(directory, 0700) != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(directory, "host-diagnostics.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	hostDiagMu.Lock()
	hostDiagFile = file
	hostDiagSeen = make(map[string]struct{})
	hostDiagMu.Unlock()
}

func closeHostDiagnostics() {
	hostDiagMu.Lock()
	defer hostDiagMu.Unlock()
	if hostDiagFile != nil {
		_ = hostDiagFile.Close()
		hostDiagFile = nil
	}
}

func writeHostDiagnostic(code string, hr uintptr) {
	if _, allowed := hostDiagnosticCodes[code]; !allowed {
		return
	}
	hostDiagMu.Lock()
	defer hostDiagMu.Unlock()
	if hostDiagFile == nil {
		return
	}
	if _, seen := hostDiagSeen[code]; seen {
		return
	}
	hostDiagSeen[code] = struct{}{}
	line := ""
	if hr == 0 {
		line = fmt.Sprintf("%s code=%s\r\n", time.Now().UTC().Format(time.RFC3339), code)
	} else {
		line = fmt.Sprintf("%s code=%s hr=0x%08X\r\n", time.Now().UTC().Format(time.RFC3339), code, uint32(hr))
	}
	if hostDiagFile == nil {
		return
	}
	info, err := hostDiagFile.Stat()
	if err != nil || info.Size()+int64(len(line)) > 64<<10 {
		return
	}
	_, _ = hostDiagFile.WriteString(line)
}

func hostHRESULTFromError(err error) uintptr {
	if err == nil {
		return 0
	}
	const marker = "HRESULT 0x"
	message := err.Error()
	index := strings.Index(message, marker)
	if index < 0 || len(message) < index+len(marker)+8 {
		return 0
	}
	value, parseErr := strconv.ParseUint(message[index+len(marker):index+len(marker)+8], 16, 32)
	if parseErr != nil {
		return 0
	}
	return uintptr(value)
}

func postHostMessage(hwnd windows.HWND, message uint32, wParam, lParam uintptr) bool {
	if hwnd == 0 {
		return false
	}
	result, _, _ := procPostMessageWHost.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return result != 0
}

func wideHost(value string) *uint16 {
	data, err := windows.UTF16FromString(value)
	if err != nil || len(data) == 0 {
		return &[]uint16{0}[0]
	}
	return &data[0]
}

func initializeFileDialogCOM() (bool, error) {
	hr, _, _ := procCoInitializeExHost.Call(0, 0x2)
	if hr == 0 || hr == 1 {
		return true, nil
	}
	if uint32(hr) == workspaceHResultChangedMode {
		return false, nil
	}
	return false, fmt.Errorf("COM init failed (HRESULT 0x%08X)", uint32(hr))
}

func hresultFailed(hr uintptr) bool { return int32(hr) < 0 }

func shellGetFolderArrayPath(items *hostShellItemArray, index uintptr) (string, error) {
	if items == nil || items.Vtbl == nil || items.Vtbl.GetItemAt == 0 {
		return "", errors.New("folder dialog returned an invalid item array")
	}
	var item *hostShellItem
	hr, _, _ := syscall.SyscallN(items.Vtbl.GetItemAt, uintptr(unsafe.Pointer(items)), index, uintptr(unsafe.Pointer(&item)))
	if hresultFailed(hr) || item == nil || item.Vtbl == nil || item.Vtbl.GetDisplayName == 0 {
		return "", errors.New("folder dialog item is unavailable")
	}
	defer syscall.SyscallN(item.Vtbl.IUnknown[2], uintptr(unsafe.Pointer(item)))
	var displayName *uint16
	hr, _, _ = syscall.SyscallN(item.Vtbl.GetDisplayName, uintptr(unsafe.Pointer(item)), workspaceSIGDNFileSysPath, uintptr(unsafe.Pointer(&displayName)))
	if hresultFailed(hr) || displayName == nil {
		return "", errors.New("folder dialog item path is unavailable")
	}
	defer procCoTaskMemFreeHost.Call(uintptr(unsafe.Pointer(displayName)))
	path := windows.UTF16PtrToString(displayName)
	return previewui.NormalizeWorkspaceInputPath(path)
}

func showFolderDialog(owner windows.HWND) ([]string, error) {
	initialized, err := initializeFileDialogCOM()
	if err != nil {
		return nil, err
	}
	if initialized {
		defer procCoUninitializeHost.Call()
	}
	var dialog unsafe.Pointer
	hr, _, _ := procCoCreateInstanceHost.Call(
		uintptr(unsafe.Pointer(&fileOpenCLSID)), 0, workspaceCLSCTXInProcServer,
		uintptr(unsafe.Pointer(&fileOpenIID)), uintptr(unsafe.Pointer(&dialog)),
	)
	if hresultFailed(hr) || dialog == nil {
		return chooseHostWorkspaceFolderFallback(owner)
	}
	defer releaseHostCOM(dialog)
	options, err := hostFileDialogOptions(dialog)
	if err != nil {
		return nil, err
	}
	if err := hostFileDialogSetOptions(dialog, options|workspaceFOSPickFolders|workspaceFOSAllowMultiSelect|workspaceFOSForceFileSystem); err != nil {
		return nil, err
	}
	hr, _, _ = hostCOMCall(dialog, 3, uintptr(owner))
	if hresultFailed(hr) {
		if uint32(hr) == workspaceHResultCanceled {
			return []string{}, nil
		}
		return nil, errors.New("folder dialog failed")
	}
	var items *hostShellItemArray
	hr, _, _ = hostCOMCall(dialog, 27, uintptr(unsafe.Pointer(&items)))
	if hresultFailed(hr) || items == nil || items.Vtbl == nil || items.Vtbl.GetCount == 0 {
		return nil, errors.New("folder dialog returned no selections")
	}
	defer releaseHostCOM(unsafe.Pointer(items))
	var count uint32
	hr, _, _ = syscall.SyscallN(items.Vtbl.GetCount, uintptr(unsafe.Pointer(items)), uintptr(unsafe.Pointer(&count)))
	if hresultFailed(hr) || count == 0 || count > 128 {
		return nil, errors.New("folder selection count is invalid")
	}
	paths := make([]string, 0, count)
	for index := uint32(0); index < count; index++ {
		path, pathErr := shellGetFolderArrayPath(items, uintptr(index))
		if pathErr != nil || !filepath.IsAbs(path) {
			return nil, errors.New("folder selection is invalid")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func chooseHostWorkspaceFolderFallback(owner windows.HWND) ([]string, error) {
	display := make([]uint16, 32768)
	title := wideHost("选择要授权的本地文件夹")
	info := hostBrowseInfo{Owner: owner, DisplayName: &display[0], Title: title, Flags: workspaceBIFReturnOnlyFSDirs | workspaceBIFNewDialogStyle}
	item, _, _ := procSHBrowseForFolderWHost.Call(uintptr(unsafe.Pointer(&info)))
	if item == 0 {
		return []string{}, nil
	}
	defer procCoTaskMemFreeHost.Call(item)
	buffer := make([]uint16, 32768)
	result, _, callErr := procSHGetPathFromIDListEx.Call(item, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if result == 0 && callErr != nil && errors.Is(callErr, windows.ERROR_PROC_NOT_FOUND) {
		result, _, _ = procSHGetPathFromIDListWHost.Call(item, uintptr(unsafe.Pointer(&buffer[0])))
	}
	if result == 0 {
		return nil, errors.New("folder selection failed")
	}
	path, err := previewui.NormalizeWorkspaceInputPath(windows.UTF16ToString(buffer))
	if err != nil || !filepath.IsAbs(path) {
		return nil, errors.New("folder selection failed")
	}
	return []string{path}, nil
}

func hostCOMCall(object unsafe.Pointer, index uintptr, args ...uintptr) (uintptr, uintptr, error) {
	if object == nil {
		return 0, 0, errors.New("nil dialog")
	}
	vtable := *(*unsafe.Pointer)(object)
	method := *(*uintptr)(unsafe.Add(vtable, index*unsafe.Sizeof(uintptr(0))))
	callArgs := append([]uintptr{uintptr(object)}, args...)
	hr, _, callErr := syscall.SyscallN(method, callArgs...)
	return hr, 0, callErr
}

func hostFileDialogOptions(dialog unsafe.Pointer) (uintptr, error) {
	var options uintptr
	hr, _, err := hostCOMCall(dialog, 10, uintptr(unsafe.Pointer(&options)))
	if hresultFailed(hr) {
		return 0, err
	}
	return options, nil
}

func hostFileDialogSetOptions(dialog unsafe.Pointer, options uintptr) error {
	hr, _, err := hostCOMCall(dialog, 11, options)
	if hresultFailed(hr) {
		return err
	}
	return nil
}

func releaseHostCOM(object unsafe.Pointer) {
	if object == nil {
		return
	}
	vtable := *(*unsafe.Pointer)(object)
	method := *(*uintptr)(unsafe.Add(vtable, 2*unsafe.Sizeof(uintptr(0))))
	_, _, _ = syscall.SyscallN(method, uintptr(object))
}
