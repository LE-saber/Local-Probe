//go:build windows

package previewui

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	workspacePathDialogClass = "LocalProbeWorkspacePathDialog"
	workspacePathDialogTitle = "新增工作空间文件夹"

	idWorkspacePathEdit   = 2201
	idWorkspacePathBrowse = 2202
	idWorkspacePathAdd    = 2203
	idWorkspacePathCancel = 2204

	wsExDlgModalFrame = 0x00000001
	wsExControlParent = 0x00010000
	wsSysMenu         = 0x00080000

	wmNCCreate = 0x0081

	buttonClicked = 0
)

var (
	workspacePathDialogClassOnce sync.Once
	workspacePathDialogClassErr  error
	workspacePathDialogPending   struct {
		sync.Mutex
		value *workspacePathDialog
	}
	workspacePathDialogs sync.Map // map[windows.HWND]*workspacePathDialog

	workspacePathDialogGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	workspacePathDialogGetWindowText       = user32.NewProc("GetWindowTextW")
	workspacePathDialogSetFocus            = user32.NewProc("SetFocus")
	workspacePathDialogIsWindowEnabled     = user32.NewProc("IsWindowEnabled")
)

type workspacePathDialog struct {
	owner    windows.HWND
	hwnd     windows.HWND
	edit     windows.HWND
	browse   windows.HWND
	add      windows.HWND
	cancel   windows.HWND
	instance windows.Handle

	result []string
	err    error
	done   bool
}

// showWorkspacePathDialog presents both supported add routes in one small
// native dialog. The dialog itself runs on the Preview message-loop thread;
// only the subsequent configuration mutation is dispatched to a worker.
func showWorkspacePathDialog(owner windows.HWND) ([]string, error) {
	if owner == 0 {
		return nil, fmt.Errorf("%w: missing owner window", ErrWorkspacePickerUnavailable)
	}
	if err := registerWorkspacePathDialogClass(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}
	instance, err := moduleHandle()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}
	dialog := &workspacePathDialog{owner: owner, instance: instance}
	workspacePathDialogPending.Lock()
	workspacePathDialogPending.value = dialog
	workspacePathDialogPending.Unlock()

	class := wideString(workspacePathDialogClass)
	title := wideString(workspacePathDialogTitle)
	hwndValue, _, callErr := procCreateWindowExW.Call(
		wsExDlgModalFrame|wsExControlParent,
		uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(title)),
		wsOverlapped|wsSysMenu,
		0x80000000, 0x80000000, 700, 250,
		uintptr(owner), 0, uintptr(instance), 0,
	)
	workspacePathDialogPending.Lock()
	if workspacePathDialogPending.value == dialog {
		workspacePathDialogPending.value = nil
	}
	workspacePathDialogPending.Unlock()
	if hwndValue == 0 {
		if callErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, callErr)
		}
		return nil, fmt.Errorf("%w: create path input dialog failed", ErrWorkspacePickerUnavailable)
	}
	dialog.hwnd = windows.HWND(hwndValue)
	if _, ok := workspacePathDialogs.Load(dialog.hwnd); !ok {
		workspacePathDialogs.Store(dialog.hwnd, dialog)
	}

	wasEnabled := workspacePathOwnerEnabled(owner)
	_, _, _ = procEnableWindow.Call(uintptr(owner), 0)
	defer func() {
		if wasEnabled {
			_, _, _ = procEnableWindow.Call(uintptr(owner), 1)
		}
		workspacePathDialogs.Delete(dialog.hwnd)
		if owner != 0 {
			_, _, _ = procSetForegroundWindow.Call(uintptr(owner))
		}
	}()

	_, _, _ = procShowWindow.Call(uintptr(dialog.hwnd), showShow)
	_, _, _ = procUpdateWindow.Call(uintptr(dialog.hwnd))
	if dialog.edit != 0 {
		_, _, _ = workspacePathDialogSetFocus.Call(uintptr(dialog.edit))
	}

	var msg winMessage
	for !dialog.done {
		result, _, getErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == 0 {
			dialog.finish(nil, getErr)
			break
		}
		if int32(result) == -1 {
			dialog.finish(nil, getErr)
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	if dialog.err != nil {
		return nil, dialog.err
	}
	return deduplicateWorkspacePaths(dialog.result), nil
}

func registerWorkspacePathDialogClass() error {
	workspacePathDialogClassOnce.Do(func() {
		instance, err := moduleHandle()
		if err != nil {
			workspacePathDialogClassErr = err
			return
		}
		className := wideString(workspacePathDialogClass)
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		brush, _, _ := procGetSysColorBrush.Call(colorWindow)
		class := wndClassExW{
			CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			Style:         0x0003,
			WndProc:       syscall.NewCallback(workspacePathDialogProc),
			HInstance:     instance,
			HCursor:       windows.Handle(cursor),
			HbrBackground: windows.Handle(brush),
			ClassName:     className,
		}
		atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 && callErr != nil && !errors.Is(callErr, windows.ERROR_CLASS_ALREADY_EXISTS) {
			workspacePathDialogClassErr = callErr
		}
	})
	return workspacePathDialogClassErr
}

func workspacePathDialogProc(hwndValue uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	hwnd := windows.HWND(hwndValue)
	if msg == wmNCCreate {
		workspacePathDialogPending.Lock()
		dialog := workspacePathDialogPending.value
		workspacePathDialogPending.Unlock()
		if dialog != nil {
			dialog.hwnd = hwnd
			workspacePathDialogs.Store(hwnd, dialog)
		}
	}
	value, _ := workspacePathDialogs.Load(hwnd)
	dialog, _ := value.(*workspacePathDialog)
	if dialog == nil {
		return defWindowProc(hwnd, msg, wParam, lParam)
	}

	switch msg {
	case wmCreate:
		dialog.initControls()
	case wmCommand:
		id := uint32(wParam & 0xffff)
		notify := uint32((wParam >> 16) & 0xffff)
		if notify != buttonClicked {
			break
		}
		switch id {
		case idWorkspacePathBrowse:
			dialog.browseFolders()
		case idWorkspacePathAdd:
			dialog.addPath()
		case idWorkspacePathCancel:
			dialog.finish(nil, errWorkspacePickerCanceled)
		}
		return 0
	case wmClose:
		dialog.finish(nil, errWorkspacePickerCanceled)
		return 0
	case wmDestroy:
		workspacePathDialogs.Delete(hwnd)
		if !dialog.done {
			// The window is already being destroyed; do not call DestroyWindow
			// again from WM_DESTROY. The outer modal loop will observe done.
			dialog.result = nil
			dialog.err = errWorkspacePickerCanceled
			dialog.done = true
		}
	}
	return defWindowProc(hwnd, msg, wParam, lParam)
}

func (d *workspacePathDialog) initControls() {
	if d == nil || d.hwnd == 0 {
		return
	}
	font := getStockFont()
	d.edit = createChild("EDIT", "", wsChild|wsVisible|wsTabStop|wsBorder|editAutoHScroll, 28, 68, 636, 34, d.hwnd, idWorkspacePathEdit, d.instance)
	d.browse = createChild("BUTTON", "浏览文件夹…", wsChild|wsVisible|wsTabStop|bsPushButton, 28, 122, 160, 38, d.hwnd, idWorkspacePathBrowse, d.instance)
	d.add = createChild("BUTTON", "确认添加", wsChild|wsVisible|wsTabStop|bsPushButton, 420, 122, 116, 38, d.hwnd, idWorkspacePathAdd, d.instance)
	d.cancel = createChild("BUTTON", "取消", wsChild|wsVisible|wsTabStop|bsPushButton, 548, 122, 116, 38, d.hwnd, idWorkspacePathCancel, d.instance)
	label := createChild("STATIC", "粘贴或输入本地文件夹绝对路径，也可以点击“浏览文件夹…”。", wsChild|wsVisible, 28, 26, 636, 28, d.hwnd, 0, d.instance)
	for _, control := range []windows.HWND{label, d.edit, d.browse, d.add, d.cancel} {
		if control != 0 && font != 0 {
			_, _, _ = procSendMessageW.Call(uintptr(control), wmSetFont, font, 1)
		}
	}
}

func (d *workspacePathDialog) browseFolders() {
	if d == nil || d.hwnd == 0 {
		return
	}
	paths, err := chooseWorkspaceFolders(d.hwnd)
	if errors.Is(err, errWorkspacePickerCanceled) {
		return
	}
	if err != nil {
		message, remediation := WorkspaceErrorPresentation(err)
		showMessage(d.hwnd, message+"\n\n"+remediation, workspacePathDialogTitle, 0x00000010)
		return
	}
	if len(paths) == 0 {
		return
	}
	if len(paths) == 1 {
		setControlText(d.edit, paths[0])
		return
	}
	// The modern picker supports multi-select. Keep that capability intact:
	// selecting several folders completes this dialog and submits one batch.
	d.finish(paths, nil)
}

func (d *workspacePathDialog) addPath() {
	if d == nil || d.edit == 0 {
		return
	}
	path, err := readWorkspaceEditText(d.edit)
	if err != nil {
		message, remediation := WorkspaceErrorPresentation(err)
		showMessage(d.hwnd, message+"\n\n"+remediation, workspacePathDialogTitle, 0x00000010)
		return
	}
	d.finish([]string{path}, nil)
}

func (d *workspacePathDialog) finish(paths []string, err error) {
	if d == nil || d.done {
		return
	}
	d.result = deduplicateWorkspacePaths(paths)
	d.err = err
	d.done = true
	if d.hwnd != 0 {
		_, _, _ = procDestroyWindow.Call(uintptr(d.hwnd))
	}
}

func readWorkspaceEditText(edit windows.HWND) (string, error) {
	if edit == 0 {
		return "", ErrWorkspacePathEmpty
	}
	length, _, callErr := workspacePathDialogGetWindowTextLength.Call(uintptr(edit))
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return "", ErrWorkspacePathInvalid
	}
	if length > uintptr(MaxWorkspacePathBytes) {
		return "", ErrWorkspacePathTooLong
	}
	buffer := make([]uint16, int(length)+1)
	if len(buffer) == 0 {
		return "", ErrWorkspacePathEmpty
	}
	read, _, readErr := workspacePathDialogGetWindowText.Call(uintptr(edit), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if readErr != nil && !errors.Is(readErr, syscall.Errno(0)) {
		return "", ErrWorkspacePathInvalid
	}
	if read > uintptr(MaxWorkspacePathBytes) {
		return "", ErrWorkspacePathTooLong
	}
	return NormalizeWorkspaceInputPath(windows.UTF16ToString(buffer[:int(read)]))
}

func workspacePathOwnerEnabled(owner windows.HWND) bool {
	if owner == 0 {
		return false
	}
	value, _, _ := workspacePathDialogIsWindowEnabled.Call(uintptr(owner))
	return value != 0
}
