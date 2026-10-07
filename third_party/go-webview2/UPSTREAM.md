# Local go-webview2 fork

Source: https://github.com/jchv/go-webview2
Pinned upstream commit: `56598839c808a2340edee99204db479f410e9bf4`
Upstream license: MIT (see `LICENSE`).

This is a minimal source copy of the pinned upstream module used only by the
Windows desktop host. The local fork retains upstream copyright and adds a
narrow secure-host API for exact-source WebMessageReceived handling, explicit
event registration failures, navigation/frame/new-window/download guards, and
WebResourceRequested response control. The desktop host does not use `Bind`,
`SetHtml`, or arbitrary filesystem serving.

The fork also exposes a UI-thread-only preferred color-scheme setter for the
built-in WebView2 menus. Its COM ABI (ICoreWebView2_13 slot 105 and profile slot 9)
was checked against Microsoft's SDK 1.0.1210.39 header; no SDK implementation code
was copied. The default context menus, copy and reload accelerators stay enabled.

The original project also includes Microsoft's WebView2Loader SDK binaries;
their accompanying license is retained under `webviewloader/sdk/LICENSE.txt`.
