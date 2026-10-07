//go:build windows

package edge

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/jchv/go-webview2/internal/w32"
	"golang.org/x/sys/windows"
)

// SecureHostCallbacks is the narrow event surface used by the Local-Probe
// desktop host. It intentionally does not expose Bind or host objects.
type SecureHostCallbacks struct {
	DocumentURL         string
	MessageReceived     func(message, argsSource, topLevelSource string)
	ResourceRequested   func(uri, method string) (status int, reason, headers string, body []byte)
	NavigationStarting  func(uri string) bool
	NavigationCompleted func(source string, succeeded bool)
	Fatal               func(error)
}

type SecureResourceResponse struct {
	Status  int
	Reason  string
	Headers string
	Body    []byte
}

type secureEventToken struct {
	owner   uintptr
	remove  ComProc
	token   _EventRegistrationToken
	removed bool
}

// Each WebView2 event interface has the same IUnknown + Invoke ABI. The IID is
// still checked per handler so a COM query cannot reinterpret an event object
// as an unrelated interface.
type secureEventHandler struct {
	vtbl   *secureEventHandlerVtbl
	iid    *GUID
	refs   int32
	invoke func(sender, args uintptr) uintptr
}

type secureEventHandlerVtbl struct {
	_IUnknownVtbl
	Invoke ComProc
}

var secureEventHandlerVtblValue = secureEventHandlerVtbl{
	_IUnknownVtbl{
		NewComProc(secureEventQueryInterface),
		NewComProc(secureEventAddRef),
		NewComProc(secureEventRelease),
	},
	NewComProc(secureEventInvoke),
}

var (
	iidIUnknown                    = NewGUID("00000000-0000-0000-C000-000000000046")
	iidWebMessageReceivedHandler   = NewGUID("57213F19-00E6-49FA-8E07-898EA01ECBD2")
	iidNavigationStartingHandler   = NewGUID("9ADBE429-F36D-432B-9DDC-F8881FBD76E3")
	iidNewWindowRequestedHandler   = NewGUID("D4C185FE-C81C-4989-97AF-2D3FA7AB5651")
	iidWebResourceRequestedHandler = NewGUID("AB00B74C-15F1-4646-80E8-E76341D25D71")
	iidPermissionRequestedHandler  = NewGUID("15E1C6A3-C72A-4DF3-91D7-D097FBEC6BFD")
	iidNavigationCompletedHandler  = NewGUID("D33A35BF-1C49-4F98-93AB-006E0533FE1C")
	iidDownloadStartingHandler     = NewGUID("EFEDC989-C396-41CA-83F7-07F845A55724")
	iidCoreWebView2_4              = NewGUID("20D02D59-6DF2-42DC-BD06-F98A694B1302")
)

func newSecureEventHandler(iid *GUID, invoke func(sender, args uintptr) uintptr) *secureEventHandler {
	h := &secureEventHandler{vtbl: &secureEventHandlerVtblValue, iid: iid, invoke: invoke}
	atomic.StoreInt32(&h.refs, 1)
	return h
}

func secureEventQueryInterface(this, refiid, out uintptr) uintptr {
	if out == 0 || refiid == 0 {
		return hresultPointer
	}
	h := (*secureEventHandler)(unsafe.Pointer(this))
	requested := (*GUID)(unsafe.Pointer(refiid))
	*(*uintptr)(unsafe.Pointer(out)) = 0
	if !IsEqualGUID(requested, iidIUnknown) && !IsEqualGUID(requested, h.iid) {
		return hresultNoInterface
	}
	*(*uintptr)(unsafe.Pointer(out)) = this
	atomic.AddInt32(&h.refs, 1)
	return hresultOK
}

func secureEventAddRef(this uintptr) uintptr {
	return uintptr(atomic.AddInt32(&(*secureEventHandler)(unsafe.Pointer(this)).refs, 1))
}

func secureEventRelease(this uintptr) uintptr {
	count := atomic.AddInt32(&(*secureEventHandler)(unsafe.Pointer(this)).refs, -1)
	if count < 0 {
		atomic.StoreInt32(&(*secureEventHandler)(unsafe.Pointer(this)).refs, 0)
		return 0
	}
	return uintptr(count)
}

func secureEventInvoke(this, sender, args uintptr) (result uintptr) {
	h := (*secureEventHandler)(unsafe.Pointer(this))
	defer func() {
		if recover() != nil {
			result = hresultFail
		}
	}()
	if h.invoke == nil {
		return hresultFail
	}
	return h.invoke(sender, args)
}

func (e *Chromium) ConfigureSecureHost(callbacks SecureHostCallbacks) error {
	if e == nil || e.webview != nil || e.hwnd != 0 {
		return errors.New("secure host policy must be set before embedding WebView2")
	}
	// The EventRegistrationToken is an int64 passed by value to the COM
	// Remove* ABI. This narrow bridge currently supports only the Windows
	// amd64 ABI, where that argument occupies one uintptr-sized slot.
	if runtime.GOARCH != "amd64" || unsafe.Sizeof(uintptr(0)) != 8 {
		return errors.New("secure WebView2 host currently supports Windows amd64 only")
	}
	if callbacks.DocumentURL == "" || callbacks.MessageReceived == nil || callbacks.ResourceRequested == nil || callbacks.NavigationStarting == nil || callbacks.NavigationCompleted == nil {
		return errors.New("secure host callbacks are incomplete")
	}
	e.secureHost = &callbacks
	e.SetGlobalPermission(CoreWebView2PermissionStateDeny)
	return nil
}

func (e *Chromium) InitError() error {
	if e == nil {
		return errors.New("nil WebView2 host")
	}
	e.initErrMu.Lock()
	defer e.initErrMu.Unlock()
	return e.initErr
}

func (e *Chromium) recordInitError(err error) {
	if err == nil {
		return
	}
	e.initErrMu.Lock()
	if e.initErr == nil {
		e.initErr = err
	}
	e.initErrMu.Unlock()
}

func (e *Chromium) failSecureHost(err error) {
	if err == nil {
		return
	}
	if e.secureHost != nil && e.secureHost.Fatal != nil {
		e.secureHost.Fatal(err)
	}
}

func (e *Chromium) installSecureEvents() error {
	if e.secureHost == nil || e.webview == nil || e.environment == nil {
		return errors.New("WebView2 secure host is not initialized")
	}
	owner := uintptr(unsafe.Pointer(e.webview))
	register := func(add, remove ComProc, iid *GUID, invoke func(sender, args uintptr) uintptr) error {
		handler := newSecureEventHandler(iid, invoke)
		e.secureHandlers = append(e.secureHandlers, handler)
		e.securePins.Pin(unsafe.Pointer(handler))
		var token _EventRegistrationToken
		hr, _, _ := add.Call(owner, uintptr(unsafe.Pointer(handler)), uintptr(unsafe.Pointer(&token)))
		if int32(hr) < 0 {
			return fmt.Errorf("WebView2 event registration failed (HRESULT 0x%08X)", uint32(hr))
		}
		e.secureTokens = append(e.secureTokens, secureEventToken{owner: owner, remove: remove, token: token})
		return nil
	}

	filter, err := windows.UTF16PtrFromString("*")
	if err != nil {
		return err
	}
	hr, _, _ := e.webview.vtbl.AddWebResourceRequestedFilter.Call(owner, uintptr(unsafe.Pointer(filter)), uintptr(COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL))
	if int32(hr) < 0 {
		return hresultError("register all-resource security filter", hr)
	}
	e.secureFilterAdded = true

	checks := []struct {
		name   string
		add    ComProc
		remove ComProc
		iid    *GUID
		invoke func(sender, args uintptr) uintptr
	}{
		{"WebMessageReceived", e.webview.vtbl.AddWebMessageReceived, e.webview.vtbl.RemoveWebMessageReceived, iidWebMessageReceivedHandler, e.secureMessageReceived},
		{"NavigationStarting", e.webview.vtbl.AddNavigationStarting, e.webview.vtbl.RemoveNavigationStarting, iidNavigationStartingHandler, e.secureNavigationStarting},
		{"FrameNavigationStarting", e.webview.vtbl.AddFrameNavigationStarting, e.webview.vtbl.RemoveFrameNavigationStarting, iidNavigationStartingHandler, e.secureFrameNavigationStarting},
		{"NewWindowRequested", e.webview.vtbl.AddNewWindowRequested, e.webview.vtbl.RemoveNewWindowRequested, iidNewWindowRequestedHandler, e.secureNewWindowRequested},
		{"WebResourceRequested", e.webview.vtbl.AddWebResourceRequested, e.webview.vtbl.RemoveWebResourceRequested, iidWebResourceRequestedHandler, e.secureWebResourceRequested},
		{"PermissionRequested", e.webview.vtbl.AddPermissionRequested, e.webview.vtbl.RemovePermissionRequested, iidPermissionRequestedHandler, e.securePermissionRequested},
		{"NavigationCompleted", e.webview.vtbl.AddNavigationCompleted, e.webview.vtbl.RemoveNavigationCompleted, iidNavigationCompletedHandler, e.secureNavigationCompleted},
	}
	for _, check := range checks {
		if err := register(check.add, check.remove, check.iid, check.invoke); err != nil {
			return fmt.Errorf("register %s: %w", check.name, err)
		}
	}

	var webview4 *iCoreWebView2_4
	hr, _, _ = e.webview.vtbl.QueryInterface.Call(owner, uintptr(unsafe.Pointer(iidCoreWebView2_4)), uintptr(unsafe.Pointer(&webview4)))
	if int32(hr) < 0 || webview4 == nil {
		return fmt.Errorf("required ICoreWebView2_4 download gate unavailable (HRESULT 0x%08X)", uint32(hr))
	}
	e.webview4 = webview4
	downloadHandler := newSecureEventHandler(iidDownloadStartingHandler, e.secureDownloadStarting)
	e.secureHandlers = append(e.secureHandlers, downloadHandler)
	e.securePins.Pin(unsafe.Pointer(downloadHandler))
	var downloadToken _EventRegistrationToken
	hr, _, _ = webview4.vtbl.AddDownloadStarting.Call(uintptr(unsafe.Pointer(webview4)), uintptr(unsafe.Pointer(downloadHandler)), uintptr(unsafe.Pointer(&downloadToken)))
	if int32(hr) < 0 {
		return fmt.Errorf("register DownloadStarting: HRESULT 0x%08X", uint32(hr))
	}
	e.secureTokens = append(e.secureTokens, secureEventToken{owner: uintptr(unsafe.Pointer(webview4)), remove: webview4.vtbl.RemoveDownloadStarting, token: downloadToken})
	return nil
}

func (e *Chromium) secureMessageReceived(sender, args uintptr) uintptr {
	if e.secureHost == nil || sender == 0 || args == 0 {
		return hresultFail
	}
	messageArgs := (*secureWebMessageReceivedArgs)(unsafe.Pointer(args))
	source, err := comString(args, messageArgs.vtbl.GetSource)
	if err != nil {
		e.failSecureHost(fmt.Errorf("read WebMessage source: %w", err))
		return hresultFail
	}
	message, err := comString(args, messageArgs.vtbl.TryGetWebMessageAsString)
	if err != nil {
		return hresultOK // Non-string messages are not part of this single-string RPC contract.
	}
	top, err := (*ICoreWebView2)(unsafe.Pointer(sender)).GetSourceChecked()
	if err != nil {
		e.failSecureHost(fmt.Errorf("read top-level WebView source: %w", err))
		return hresultFail
	}
	if source != e.secureHost.DocumentURL || top != e.secureHost.DocumentURL {
		return hresultOK
	}
	e.secureHost.MessageReceived(message, source, top)
	return hresultOK
}

func (e *Chromium) secureNavigationStarting(sender, args uintptr) uintptr {
	uri, err := navigationStartingURI(args)
	if err != nil {
		e.failSecureHost(fmt.Errorf("read main-frame navigation URI: %w", err))
		if cancelErr := putNavigationCancel(args, true); cancelErr != nil {
			e.failSecureHost(cancelErr)
			return hresultFail
		}
		return hresultOK
	}
	allowed := e.secureHost != nil && e.secureHost.NavigationStarting != nil && e.secureHost.NavigationStarting(uri)
	if !allowed {
		if err := putNavigationCancel(args, true); err != nil {
			e.failSecureHost(err)
			return hresultFail
		}
	}
	return hresultOK
}

func (e *Chromium) secureFrameNavigationStarting(_ uintptr, args uintptr) uintptr {
	if err := putNavigationCancel(args, true); err != nil {
		e.failSecureHost(fmt.Errorf("cancel frame navigation: %w", err))
		return hresultFail
	}
	return hresultOK
}

func (e *Chromium) secureNewWindowRequested(_ uintptr, args uintptr) uintptr {
	if err := putNewWindowHandled(args, true); err != nil {
		e.failSecureHost(fmt.Errorf("suppress new window: %w", err))
		return hresultFail
	}
	return hresultOK
}

func (e *Chromium) securePermissionRequested(_ uintptr, args uintptr) uintptr {
	if args == 0 {
		e.failSecureHost(errors.New("permission request event has no args"))
		return hresultFail
	}
	vtable := (*iCoreWebView2PermissionRequestedEventArgsVtbl)(unsafe.Pointer((*iCoreWebView2PermissionRequestedEventArgs)(unsafe.Pointer(args)).vtbl))
	hr, _, _ := vtable.PutState.Call(args, uintptr(CoreWebView2PermissionStateDeny))
	if int32(hr) < 0 {
		e.failSecureHost(fmt.Errorf("deny WebView2 permission (HRESULT 0x%08X)", uint32(hr)))
		return hr
	}
	return hresultOK
}

func (e *Chromium) secureNavigationCompleted(sender, args uintptr) uintptr {
	if e.secureHost == nil || sender == 0 || args == 0 {
		return hresultFail
	}
	vtable := (*_ICoreWebView2NavigationCompletedEventArgsVtbl)(unsafe.Pointer((*ICoreWebView2NavigationCompletedEventArgs)(unsafe.Pointer(args)).vtbl))
	var success int32
	hr, _, _ := vtable.GetIsSuccess.Call(args, uintptr(unsafe.Pointer(&success)))
	if int32(hr) < 0 {
		e.failSecureHost(fmt.Errorf("read navigation completion (HRESULT 0x%08X)", uint32(hr)))
		return hr
	}
	webview := (*ICoreWebView2)(unsafe.Pointer(sender))
	source, err := webview.GetSourceChecked()
	if err != nil {
		e.failSecureHost(fmt.Errorf("read completed source: %w", err))
		return hresultFail
	}
	e.secureHost.NavigationCompleted(source, success != 0 && source == e.secureHost.DocumentURL)
	return hresultOK
}

func (e *Chromium) secureDownloadStarting(_ uintptr, args uintptr) uintptr {
	if err := putDownloadCancelled(args); err != nil {
		e.failSecureHost(err)
		return hresultFail
	}
	return hresultOK
}

func (e *Chromium) secureWebResourceRequested(_ uintptr, args uintptr) uintptr {
	response := SecureResourceResponse{Status: 403, Reason: "Forbidden", Headers: "Content-Length: 0\r\nX-Content-Type-Options: nosniff"}
	var request *ICoreWebView2WebResourceRequest
	requestErr := getResourceRequest(args, &request)
	uri, method := "", ""
	if requestErr == nil && request != nil {
		defer releaseUnknown(uintptr(unsafe.Pointer(request)))
		uri, requestErr = resourceRequestString(request, request.vtbl.GetUri)
		if requestErr == nil {
			method, requestErr = resourceRequestString(request, request.vtbl.GetMethod)
		}
	}
	if requestErr == nil && e.secureHost != nil && e.secureHost.ResourceRequested != nil {
		status, reason, headers, body := e.secureHost.ResourceRequested(uri, method)
		response = SecureResourceResponse{Status: status, Reason: reason, Headers: headers, Body: body}
	} else if requestErr != nil {
		// Unknown or unreadable requests remain denied. We do not fall through to
		// WebView2's default network handler.
	}
	if err := e.respondToResourceRequest(args, response); err != nil {
		e.failSecureHost(fmt.Errorf("deny/serve WebResourceRequested: %w", err))
		return hresultFail
	}
	return hresultOK
}

func (e *Chromium) respondToResourceRequest(args uintptr, result SecureResourceResponse) error {
	if args == 0 || e.environment == nil {
		return errors.New("missing WebResourceRequested response target")
	}
	if result.Status < 100 || result.Status > 599 || result.Reason == "" {
		return errors.New("invalid WebResourceRequested response")
	}
	var stream uintptr
	if len(result.Body) != 0 {
		var err error
		stream, err = w32.SHCreateMemStream(result.Body)
		if err != nil {
			return err
		}
		defer releaseUnknown(stream)
	}
	reason, err := windows.UTF16PtrFromString(result.Reason)
	if err != nil {
		return err
	}
	headers, err := windows.UTF16PtrFromString(result.Headers)
	if err != nil {
		return err
	}
	var response *ICoreWebView2WebResourceResponse
	hr, _, _ := e.environment.vtbl.CreateWebResourceResponse.Call(uintptr(unsafe.Pointer(e.environment)), stream, uintptr(result.Status), uintptr(unsafe.Pointer(reason)), uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(&response)))
	if int32(hr) < 0 || response == nil {
		return hresultError("create WebResource response", hr)
	}
	defer releaseUnknown(uintptr(unsafe.Pointer(response)))
	responseArgs := (*ICoreWebView2WebResourceRequestedEventArgs)(unsafe.Pointer(args))
	hr, _, _ = responseArgs.vtbl.PutResponse.Call(args, uintptr(unsafe.Pointer(response)))
	if int32(hr) < 0 {
		return hresultError("set WebResource response", hr)
	}
	return nil
}

func (e *Chromium) CloseSecureHost() error {
	if e == nil {
		return nil
	}
	var first error
	for i := len(e.secureTokens) - 1; i >= 0; i-- {
		token := e.secureTokens[i]
		if token.removed {
			continue
		}
		hr, _, _ := token.remove.Call(token.owner, uintptr(uint64(token.token.Value)))
		if int32(hr) < 0 {
			if first == nil {
				first = hresultError("remove WebView2 event handler", hr)
			}
			continue
		}
		e.secureTokens[i].removed = true
	}
	// Do not close/release anything while an event handler may still be
	// registered. A failed removal keeps the COM objects and Go pins alive so
	// the caller can retry on the owning STA or let process teardown reclaim it.
	if first != nil {
		return first
	}
	if e.secureFilterAdded && e.webview != nil {
		filter, _ := windows.UTF16PtrFromString("*")
		hr, _, _ := e.webview.vtbl.RemoveWebResourceRequestedFilter.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(filter)), uintptr(COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL))
		if int32(hr) < 0 {
			return hresultError("remove WebResourceRequested filter", hr)
		}
		e.secureFilterAdded = false
	}
	if e.controller != nil && !e.controllerClosed {
		hr, _, _ := e.controller.vtbl.Close.Call(uintptr(unsafe.Pointer(e.controller)))
		if int32(hr) < 0 {
			// Keep every interface reference and handler pin intact when Close
			// fails; WebView2 may still call an event handler.
			return hresultError("close WebView2 controller", hr)
		}
		e.controllerClosed = true
	}
	e.secureTokens = nil
	if e.webview4 != nil {
		releaseUnknown(uintptr(unsafe.Pointer(e.webview4)))
		e.webview4 = nil
	}
	if e.webview != nil {
		releaseUnknown(uintptr(unsafe.Pointer(e.webview)))
		e.webview = nil
	}
	if e.controller != nil {
		releaseUnknown(uintptr(unsafe.Pointer(e.controller)))
		e.controller = nil
	}
	if e.environment != nil {
		releaseUnknown(uintptr(unsafe.Pointer(e.environment)))
		e.environment = nil
	}
	for _, handler := range e.secureHandlers {
		secureEventRelease(uintptr(unsafe.Pointer(handler)))
	}
	e.securePins.Unpin()
	e.secureHandlers = nil
	return nil
}

const (
	hresultOK          = uintptr(0)
	hresultFail        = uintptr(0x80004005)
	hresultPointer     = uintptr(0x80004003)
	hresultNoInterface = uintptr(0x80004002)
)

func hresultError(operation string, hr uintptr) error {
	return fmt.Errorf("%s failed (HRESULT 0x%08X)", operation, uint32(hr))
}

func comString(owner uintptr, proc ComProc) (string, error) {
	if owner == 0 {
		return "", errors.New("nil COM object")
	}
	var value *uint16
	hr, _, _ := proc.Call(owner, uintptr(unsafe.Pointer(&value)))
	if int32(hr) < 0 {
		if value != nil {
			windows.CoTaskMemFree(unsafe.Pointer(value))
		}
		return "", hresultError("read COM string", hr)
	}
	if value == nil {
		return "", errors.New("COM returned a nil string")
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(value))
	return windows.UTF16PtrToString(value), nil
}

func (i *ICoreWebView2) GetSourceChecked() (string, error) {
	if i == nil || i.vtbl == nil {
		return "", errors.New("nil WebView2 source")
	}
	return comString(uintptr(unsafe.Pointer(i)), i.vtbl.GetSource)
}

func (e *Chromium) GetSourceChecked() (string, error) {
	if e == nil || e.webview == nil {
		return "", errors.New("WebView2 is not initialized")
	}
	return e.webview.GetSourceChecked()
}

func (e *Chromium) PostWebMessageAsStringChecked(message string) error {
	if e == nil || e.webview == nil {
		return errors.New("WebView2 is not initialized")
	}
	value, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return err
	}
	hr, _, _ := e.webview.vtbl.PostWebMessageAsString.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(value)))
	if int32(hr) < 0 {
		return hresultError("post WebMessage response", hr)
	}
	return nil
}

// GetControllerBoundsChecked returns the current WebView controller bounds and
// preserves the COM HRESULT instead of relying on the thread's last-error slot.
func (e *Chromium) GetControllerBoundsChecked() (left, top, right, bottom int32, err error) {
	if e == nil || e.controller == nil {
		return 0, 0, 0, 0, errors.New("WebView2 controller is not initialized")
	}
	var bounds w32.Rect
	hr, _, _ := e.controller.vtbl.GetBounds.Call(uintptr(unsafe.Pointer(e.controller)), uintptr(unsafe.Pointer(&bounds)))
	if int32(hr) < 0 {
		return 0, 0, 0, 0, hresultError("get WebView2 controller bounds", hr)
	}
	return bounds.Left, bounds.Top, bounds.Right, bounds.Bottom, nil
}

// SetControllerBoundsChecked sets controller bounds using the native WebView2
// ABI and reports its HRESULT.
func (e *Chromium) SetControllerBoundsChecked(left, top, right, bottom int32) error {
	if e == nil || e.controller == nil {
		return errors.New("WebView2 controller is not initialized")
	}
	bounds := w32.Rect{Left: left, Top: top, Right: right, Bottom: bottom}
	hr, _, _ := e.controller.vtbl.PutBounds.Call(uintptr(unsafe.Pointer(e.controller)), uintptr(unsafe.Pointer(&bounds)))
	if int32(hr) < 0 {
		return hresultError("set WebView2 controller bounds", hr)
	}
	return nil
}

// GetControllerVisibleChecked reads the controller's visibility state.
func (e *Chromium) GetControllerVisibleChecked() (bool, error) {
	if e == nil || e.controller == nil {
		return false, errors.New("WebView2 controller is not initialized")
	}
	var visible int32
	hr, _, _ := e.controller.vtbl.GetIsVisible.Call(uintptr(unsafe.Pointer(e.controller)), uintptr(unsafe.Pointer(&visible)))
	if int32(hr) < 0 {
		return false, hresultError("get WebView2 controller visibility", hr)
	}
	return visible != 0, nil
}

// SetControllerVisibleChecked changes the controller visibility state.
func (e *Chromium) SetControllerVisibleChecked(visible bool) error {
	if e == nil || e.controller == nil {
		return errors.New("WebView2 controller is not initialized")
	}
	hr, _, _ := e.controller.vtbl.PutIsVisible.Call(uintptr(unsafe.Pointer(e.controller)), uintptr(boolToInt(visible)))
	if int32(hr) < 0 {
		return hresultError("set WebView2 controller visibility", hr)
	}
	return nil
}

// NotifyParentWindowPositionChangedChecked refreshes WebView2's parent-window
// geometry after the native bounds have changed.
func (e *Chromium) NotifyParentWindowPositionChangedChecked() error {
	if e == nil || e.controller == nil {
		return errors.New("WebView2 controller is not initialized")
	}
	hr, _, _ := e.controller.vtbl.NotifyParentWindowPositionChanged.Call(uintptr(unsafe.Pointer(e.controller)))
	if int32(hr) < 0 {
		return hresultError("notify WebView2 parent position", hr)
	}
	return nil
}

func (e *Chromium) NavigateChecked(url string) error {
	if e == nil || e.webview == nil {
		return errors.New("WebView2 is not initialized")
	}
	value, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	hr, _, _ := e.webview.vtbl.Navigate.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(value)))
	if int32(hr) < 0 {
		return hresultError("navigate WebView2", hr)
	}
	return nil
}

func (e *Chromium) ConfigureSecureSettings() error {
	if e == nil || e.webview == nil {
		return errors.New("WebView2 is not initialized")
	}
	settings, err := e.webview.GetSettings()
	if err != nil {
		return err
	}
	defer releaseUnknown(uintptr(unsafe.Pointer(settings)))
	for _, setting := range []struct {
		name  string
		proc  ComProc
		value uintptr
	}{
		{"web messages", settings.vtbl.PutIsWebMessageEnabled, 1},
		{"script dialogs", settings.vtbl.PutAreDefaultScriptDialogsEnabled, 0},
		{"context menus", settings.vtbl.PutAreDefaultContextMenusEnabled, 1},
		{"developer tools", settings.vtbl.PutAreDevToolsEnabled, 0},
		{"host objects", settings.vtbl.PutAreHostObjectsAllowed, 0},
		{"status bar", settings.vtbl.PutIsStatusBarEnabled, 0},
	} {
		hr, _, _ := setting.proc.Call(uintptr(unsafe.Pointer(settings)), setting.value)
		if int32(hr) < 0 {
			return hresultError("set secure WebView2 "+setting.name, hr)
		}
	}
	return nil
}

func releaseUnknown(value uintptr) {
	if value == 0 {
		return
	}
	unknown := (*_IUnknownVtbl)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(value))))
	_, _, _ = unknown.Release.Call(value)
}

func getResourceRequest(args uintptr, out **ICoreWebView2WebResourceRequest) error {
	if args == 0 || out == nil {
		return errors.New("nil WebResourceRequested args")
	}
	view := (*ICoreWebView2WebResourceRequestedEventArgs)(unsafe.Pointer(args))
	hr, _, _ := view.vtbl.GetRequest.Call(args, uintptr(unsafe.Pointer(out)))
	if int32(hr) < 0 {
		return hresultError("get WebResource request", hr)
	}
	return nil
}

func resourceRequestString(request *ICoreWebView2WebResourceRequest, proc ComProc) (string, error) {
	return comString(uintptr(unsafe.Pointer(request)), proc)
}

func navigationStartingURI(args uintptr) (string, error) {
	if args == 0 {
		return "", errors.New("nil navigation args")
	}
	view := (*navigationStartingArgs)(unsafe.Pointer(args))
	return comString(args, view.vtbl.GetURI)
}

func putNavigationCancel(args uintptr, cancel bool) error {
	if args == 0 {
		return errors.New("nil navigation args")
	}
	view := (*navigationStartingArgs)(unsafe.Pointer(args))
	hr, _, _ := view.vtbl.PutCancel.Call(args, uintptr(boolToInt(cancel)))
	if int32(hr) < 0 {
		return hresultError("set navigation cancel", hr)
	}
	return nil
}

func putNewWindowHandled(args uintptr, handled bool) error {
	if args == 0 {
		return errors.New("nil new-window args")
	}
	view := (*newWindowRequestedArgs)(unsafe.Pointer(args))
	hr, _, _ := view.vtbl.PutHandled.Call(args, uintptr(boolToInt(handled)))
	if int32(hr) < 0 {
		return hresultError("set new-window handled", hr)
	}
	return nil
}

func putDownloadCancelled(args uintptr) error {
	if args == 0 {
		return errors.New("nil download args")
	}
	view := (*downloadStartingArgs)(unsafe.Pointer(args))
	hr, _, _ := view.vtbl.PutCancel.Call(args, 1)
	if int32(hr) < 0 {
		return hresultError("cancel download", hr)
	}
	hr, _, _ = view.vtbl.PutHandled.Call(args, 1)
	if int32(hr) < 0 {
		return hresultError("handle cancelled download", hr)
	}
	return nil
}

// The following layouts mirror only the WebView2 COM slots used by the secure
// host. The ICoreWebView2_4 layout is deliberately sourced from the pinned
// upstream ICoreWebView2_3 prefix, not a handwritten full wrapper.
type iCoreWebView2_4Vtbl struct {
	iCoreWebView2_3Vtbl
	AddFrameCreated        ComProc
	RemoveFrameCreated     ComProc
	AddDownloadStarting    ComProc
	RemoveDownloadStarting ComProc
}

type iCoreWebView2_4 struct {
	vtbl *iCoreWebView2_4Vtbl
}

type navigationStartingArgsVtbl struct {
	_IUnknownVtbl
	GetURI             ComProc
	GetIsUserInitiated ComProc
	GetIsRedirected    ComProc
	GetRequestHeaders  ComProc
	GetCancel          ComProc
	PutCancel          ComProc
	GetNavigationId    ComProc
}

type navigationStartingArgs struct{ vtbl *navigationStartingArgsVtbl }

type newWindowRequestedArgsVtbl struct {
	_IUnknownVtbl
	GetURI             ComProc
	PutNewWindow       ComProc
	GetNewWindow       ComProc
	PutHandled         ComProc
	GetHandled         ComProc
	GetIsUserInitiated ComProc
	GetDeferral        ComProc
	GetWindowFeatures  ComProc
}

type newWindowRequestedArgs struct{ vtbl *newWindowRequestedArgsVtbl }

type downloadStartingArgsVtbl struct {
	_IUnknownVtbl
	GetDownloadOperation ComProc
	GetCancel            ComProc
	PutCancel            ComProc
	GetResultFilePath    ComProc
	PutResultFilePath    ComProc
	GetHandled           ComProc
	PutHandled           ComProc
	GetDeferral          ComProc
}

type downloadStartingArgs struct{ vtbl *downloadStartingArgsVtbl }

type secureWebMessageReceivedArgsVtbl struct {
	_IUnknownVtbl
	GetSource                ComProc
	GetWebMessageAsJSON      ComProc
	TryGetWebMessageAsString ComProc
}

type secureWebMessageReceivedArgs struct {
	vtbl *secureWebMessageReceivedArgsVtbl
}
