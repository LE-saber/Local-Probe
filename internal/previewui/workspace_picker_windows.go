//go:build windows

package previewui

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32Workspace = windows.NewLazySystemDLL("ole32.dll")

	workspaceCoInitializeEx    = ole32Workspace.NewProc("CoInitializeEx")
	workspaceCoUninitialize    = ole32Workspace.NewProc("CoUninitialize")
	workspaceCoCreateInstance  = ole32Workspace.NewProc("CoCreateInstance")
	workspaceCoTaskMemFree     = ole32Workspace.NewProc("CoTaskMemFree")
	workspaceSHBrowseForFolder = shell32.NewProc("SHBrowseForFolderW")
	workspaceSHGetPathFromIDEx = shell32.NewProc("SHGetPathFromIDListEx")
	workspaceSHGetPathFromID   = shell32.NewProc("SHGetPathFromIDListW")
)

const (
	workspaceCLSCTXInProcServer = 0x1
	workspaceCOINITApartment    = 0x2

	workspaceFOSAllowMultiSelect = 0x00000200
	workspaceFOSForceFileSystem  = 0x00000040
	workspaceFOSPickFolders      = 0x00000020

	workspaceSIGDNFileSysPath           = 0x80058000
	workspaceHResultCanceled     uint32 = 0x800704c7
	workspaceHResultChangedMode  uint32 = 0x80010106
	workspaceBIFReturnOnlyFSDirs        = 0x00000001
	workspaceBIFNewDialogStyle          = 0x00000040
)

var (
	workspaceCLSIDFileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xef}}
	workspaceIIDFileOpenDialog   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

var errWorkspacePickerCanceled = errors.New("workspace folder picker canceled")

type workspaceBrowseInfo struct {
	Owner       windows.HWND
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	Param       uintptr
	Image       int32
}

// chooseWorkspaceFolders prefers the Vista+ common file dialog because it can
// select several folders. Some Windows sessions cannot create that COM dialog
// (for example, a broken shell registration or an incompatible apartment), so
// the older shell folder browser is a deliberate single-folder fallback.
func chooseWorkspaceFolders(owner windows.HWND) ([]string, error) {
	paths, err := chooseWorkspaceFoldersCommonDialog(owner)
	if err == nil || errors.Is(err, errWorkspacePickerCanceled) || !errors.Is(err, ErrWorkspacePickerUnavailable) {
		return paths, err
	}
	return chooseWorkspaceFolderFallback(owner)
}

// chooseWorkspaceFoldersCommonDialog uses the Vista+ common file dialog. It
// supports selecting several folders in one modal operation while still
// returning filesystem paths only.
func chooseWorkspaceFoldersCommonDialog(owner windows.HWND) ([]string, error) {
	// COM apartment state is thread-affine. Keep the dialog and its
	// CoUninitialize on one OS thread even though the UI starts it in a worker.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initialized, err := initializeWorkspaceCOM()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}
	if initialized {
		defer workspaceCoUninitialize.Call()
	}

	var dialog unsafe.Pointer
	hr, _, callErr := workspaceCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&workspaceCLSIDFileOpenDialog)),
		0,
		workspaceCLSCTXInProcServer,
		uintptr(unsafe.Pointer(&workspaceIIDFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if failedWorkspaceHRESULT(hr) {
		return nil, fmt.Errorf("%w: create folder picker: %v", ErrWorkspacePickerUnavailable, workspaceHRESULTError(hr, callErr))
	}
	if dialog == nil {
		return nil, fmt.Errorf("%w: create folder picker returned nil dialog", ErrWorkspacePickerUnavailable)
	}
	defer workspaceCOMRelease(dialog)

	options, err := workspaceFileDialogGetOptions(dialog)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}
	options |= workspaceFOSPickFolders | workspaceFOSAllowMultiSelect | workspaceFOSForceFileSystem
	if err := workspaceFileDialogSetOptions(dialog, options); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}

	hr, _, callErr = workspaceCOMCall(dialog, 3, uintptr(owner)) // IFileDialog.Show
	if failedWorkspaceHRESULT(hr) {
		if uint32(hr) == workspaceHResultCanceled {
			return nil, errWorkspacePickerCanceled
		}
		return nil, fmt.Errorf("%w: show folder picker: %v", ErrWorkspacePickerUnavailable, workspaceHRESULTError(hr, callErr))
	}

	var items unsafe.Pointer
	hr, _, callErr = workspaceCOMCall(dialog, 27, uintptr(unsafe.Pointer(&items))) // IFileOpenDialog.GetResults
	if failedWorkspaceHRESULT(hr) {
		return nil, fmt.Errorf("%w: get selected folders: %v", ErrWorkspacePickerUnavailable, workspaceHRESULTError(hr, callErr))
	}
	if items == nil {
		return nil, fmt.Errorf("%w: folder picker returned no selection", ErrWorkspacePickerSelection)
	}
	defer workspaceCOMRelease(items)

	count, err := workspaceShellItemArrayCount(items)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
	}
	if count < 1 || count > MaxWorkspaceFolders {
		return nil, fmt.Errorf("%w: folder picker selection count %d is outside the supported bound", ErrWorkspacePickerSelection, count)
	}
	paths := make([]string, 0, count)
	for index := 0; index < count; index++ {
		item, err := workspaceShellItemArrayItem(items, index)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, err)
		}
		path, pathErr := workspaceShellItemPath(item)
		workspaceCOMRelease(item)
		if pathErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerUnavailable, pathErr)
		}
		if err := ValidateWorkspacePath(path); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerSelection, err)
		}
		paths = append(paths, path)
	}
	return deduplicateWorkspacePaths(paths), nil
}

// chooseWorkspaceFolderFallback uses the inbox shell folder browser. It is
// intentionally single-select, but does not require IFileOpenDialog/COM and
// therefore keeps the manual browse route usable when the modern picker is
// unavailable.
func chooseWorkspaceFolderFallback(owner windows.HWND) ([]string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initialized, err := initializeWorkspaceCOM()
	if err != nil {
		return nil, fmt.Errorf("%w: initialize fallback folder browser: %v", ErrWorkspacePickerUnavailable, err)
	}
	if initialized {
		defer workspaceCoUninitialize.Call()
	}

	display := make([]uint16, 260)
	prompt := wideString("选择要授权的本地文件夹")
	flags := uint32(workspaceBIFReturnOnlyFSDirs)
	if initialized {
		flags |= workspaceBIFNewDialogStyle
	}
	info := workspaceBrowseInfo{
		Owner:       owner,
		DisplayName: &display[0],
		Title:       prompt,
		Flags:       flags,
	}
	item, _, callErr := workspaceSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&info)))
	if item == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return nil, fmt.Errorf("%w: fallback folder browser unavailable: %v", ErrWorkspacePickerUnavailable, callErr)
		}
		return nil, errWorkspacePickerCanceled
	}
	defer workspaceCoTaskMemFree.Call(item)

	pathBuffer := make([]uint16, 32768)
	result, _, pathErr := workspaceSHGetPathFromIDEx.Call(item, uintptr(unsafe.Pointer(&pathBuffer[0])), uintptr(len(pathBuffer)), 0)
	if result == 0 && pathErr != nil && errors.Is(pathErr, windows.ERROR_PROC_NOT_FOUND) {
		// Vista+ exposes SHGetPathFromIDListEx; retain the older API as a
		// compatibility fallback for a shell32 implementation that lacks it.
		result, _, pathErr = workspaceSHGetPathFromID.Call(item, uintptr(unsafe.Pointer(&pathBuffer[0])))
	}
	if result == 0 {
		if pathErr != nil && !errors.Is(pathErr, syscall.Errno(0)) {
			return nil, fmt.Errorf("%w: fallback folder browser returned no filesystem path: %v", ErrWorkspacePickerSelection, pathErr)
		}
		return nil, fmt.Errorf("%w: fallback folder browser returned no filesystem path", ErrWorkspacePickerSelection)
	}
	path := windows.UTF16ToString(pathBuffer)
	if path, err := NormalizeWorkspaceInputPath(path); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspacePickerSelection, err)
	} else {
		return []string{path}, nil
	}
}

func initializeWorkspaceCOM() (bool, error) {
	hr, _, _ := workspaceCoInitializeEx.Call(0, workspaceCOINITApartment)
	if hr == 0 || hr == 1 { // S_OK or S_FALSE
		return true, nil
	}
	// RPC_E_CHANGED_MODE means this thread already has a different apartment;
	// the caller may still use COM, but must not uninitialize it here.
	if uint32(hr) == workspaceHResultChangedMode {
		return false, nil
	}
	return false, workspaceHRESULTError(hr, nil)
}

func workspaceFileDialogGetOptions(dialog unsafe.Pointer) (uintptr, error) {
	var options uintptr
	hr, _, callErr := workspaceCOMCall(dialog, 10, uintptr(unsafe.Pointer(&options))) // GetOptions
	if failedWorkspaceHRESULT(hr) {
		return 0, fmt.Errorf("read folder picker options: %w", workspaceHRESULTError(hr, callErr))
	}
	return options, nil
}

func workspaceFileDialogSetOptions(dialog unsafe.Pointer, options uintptr) error {
	hr, _, callErr := workspaceCOMCall(dialog, 9, options) // SetOptions
	if failedWorkspaceHRESULT(hr) {
		return fmt.Errorf("set folder picker options: %w", workspaceHRESULTError(hr, callErr))
	}
	return nil
}

func workspaceShellItemArrayCount(items unsafe.Pointer) (int, error) {
	var count uint32
	hr, _, callErr := workspaceCOMCall(items, 7, uintptr(unsafe.Pointer(&count)))
	if failedWorkspaceHRESULT(hr) {
		return 0, fmt.Errorf("read selected folder count: %w", workspaceHRESULTError(hr, callErr))
	}
	return int(count), nil
}

func workspaceShellItemArrayItem(items unsafe.Pointer, index int) (unsafe.Pointer, error) {
	var item unsafe.Pointer
	hr, _, callErr := workspaceCOMCall(items, 8, uintptr(index), uintptr(unsafe.Pointer(&item)))
	if failedWorkspaceHRESULT(hr) {
		return nil, fmt.Errorf("read selected folder %d: %w", index, workspaceHRESULTError(hr, callErr))
	}
	if item == nil {
		return nil, errors.New("selected folder item is nil")
	}
	return item, nil
}

func workspaceShellItemPath(item unsafe.Pointer) (string, error) {
	var value unsafe.Pointer
	hr, _, callErr := workspaceCOMCall(item, 5, workspaceSIGDNFileSysPath, uintptr(unsafe.Pointer(&value)))
	if failedWorkspaceHRESULT(hr) {
		return "", fmt.Errorf("read selected folder path: %w", workspaceHRESULTError(hr, callErr))
	}
	if value == nil {
		return "", errors.New("selected folder path is nil")
	}
	defer workspaceCoTaskMemFree.Call(uintptr(value))
	return windows.UTF16PtrToString((*uint16)(value)), nil
}

func deduplicateWorkspacePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		key := workspacePathKey(path)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	return out
}

func workspaceCOMRelease(object unsafe.Pointer) {
	if object != nil {
		_, _, _ = workspaceCOMCall(object, 2) // IUnknown.Release
	}
}

func workspaceCOMCall(object unsafe.Pointer, method int, args ...uintptr) (uintptr, uintptr, error) {
	if object == nil || method < 0 {
		return 0, 0, errors.New("invalid COM object or method")
	}
	vtablePointer := *(*unsafe.Pointer)(object)
	if vtablePointer == nil {
		return 0, 0, errors.New("COM method is unavailable")
	}
	// COM objects start with a pointer to a contiguous vtable. A Go slice
	// header cannot be cast over the object itself; build a bounded view over
	// the vtable pointer before selecting the requested method.
	vtable := unsafe.Slice((*uintptr)(vtablePointer), method+1)
	if vtable[method] == 0 {
		return 0, 0, errors.New("COM method is unavailable")
	}
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, uintptr(object))
	callArgs = append(callArgs, args...)
	return syscall.SyscallN(vtable[method], callArgs...)
}

func failedWorkspaceHRESULT(hr uintptr) bool {
	return int32(hr) < 0
}

func workspaceHRESULTError(hr uintptr, callErr error) error {
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return callErr
	}
	return syscall.Errno(uint32(hr))
}
