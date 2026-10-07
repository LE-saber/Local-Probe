package desktophost

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/LE-saber/Local-Probe/internal/desktopweb"
)

func TestTrustedRPCSourceRequiresExactTopLevelDocument(t *testing.T) {
	if !trustedRPCSource(desktopweb.DocumentURL, desktopweb.DocumentURL) {
		t.Fatal("exact desktop document was rejected")
	}
	for _, source := range []string{
		"https://localassets/index.html?next=https://evil.invalid",
		"https://localassets/index.html#fragment",
		"https://localassets/other.html",
		"https://localassets.evil.invalid/index.html",
		"file:///index.html",
	} {
		if trustedRPCSource(source, desktopweb.DocumentURL) {
			t.Fatalf("accepted untrusted message source %q", source)
		}
	}
	if trustedRPCSource(desktopweb.DocumentURL, "https://localassets/subframe") {
		t.Fatal("accepted a message whose top-level document does not match")
	}
}

func TestUTF16DialogFilterPreservesMultiStringSeparators(t *testing.T) {
	const value = "JSON files (*.json)\x00*.json\x00\x00"
	got := string(utf16.Decode(utf16MultiString(value)))
	if got != value {
		t.Fatalf("dialog filter multi-string changed: %q", got)
	}
}

func TestBackupDialogFilterPreservesUnicodeAndFixedExtension(t *testing.T) {
	const value = "Local-Probe 配置备份 (*.lpbackup)\x00*.lpbackup\x00\x00"
	if got := string(utf16.Decode(utf16MultiString(value))); got != value {
		t.Fatalf("backup filter changed: %q", got)
	}
}

func TestLocalAssetsServeOnlyFixedEmbeddedResources(t *testing.T) {
	for uri, wantType := range map[string]string{
		"https://localassets/":           "text/html; charset=utf-8",
		"https://localassets/index.html": "text/html; charset=utf-8",
		"https://localassets/app.js":     "text/javascript; charset=utf-8",
		"https://localassets/icons.js":   "text/javascript; charset=utf-8",
		"https://localassets/app.css":    "text/css; charset=utf-8",
	} {
		resource := localAsset(uri, "GET")
		if resource.status != 200 || resource.contentType != wantType || len(resource.body) == 0 {
			t.Fatalf("unexpected resource for %s: status=%d type=%q body=%d", uri, resource.status, resource.contentType, len(resource.body))
		}
		if !strings.Contains(resource.headers, "Content-Type: "+wantType) || !strings.Contains(resource.headers, "connect-src 'none'") || strings.Contains(resource.headers, "unsafe-inline") {
			t.Fatalf("resource headers weaken content policy for %s: %q", uri, resource.headers)
		}
	}
}

func TestLocalAssetsRejectNavigationShapesAndUnknownResources(t *testing.T) {
	for _, tc := range []struct{ uri, method string }{
		{"https://localassets/index.html?x=1", "GET"},
		{"https://localassets/index.html#fragment", "GET"},
		{"https://localassets/%2e%2e/app.js", "GET"},
		{"https://localassets/%252e%252e/app.js", "GET"},
		{"https://localassets/private.json", "GET"},
		{"https://localassets/app.js", "POST"},
		{"https://localassets.evil.invalid/app.js", "GET"},
	} {
		resource := localAsset(tc.uri, tc.method)
		if resource.status != 403 || len(resource.body) != 0 {
			t.Fatalf("request was not denied: %s %s -> %d", tc.method, tc.uri, resource.status)
		}
	}
}
