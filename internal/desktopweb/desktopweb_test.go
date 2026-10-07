package desktopweb

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedDocumentAndLocalOnlyPolicy(t *testing.T) {
	for _, name := range []string{"index.html", "app.css", "app.js"} {
		if _, err := fs.Stat(FS(), name); err != nil {
			t.Fatalf("embedded asset %q missing: %v", name, err)
		}
	}
	document, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	if string(Document()) != text {
		t.Fatal("Document should match the embedded root document")
	}
	if DocumentURL != "https://localassets/index.html" {
		t.Fatalf("unexpected desktop document URL: %q", DocumentURL)
	}
	for _, required := range []string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'",
		"connect-src 'none'", "form-action 'none'", "frame-src 'none'", "base-uri 'none'",
		"./app.css", "./app.js",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("document missing local-only policy token %q", required)
		}
	}
	for _, forbidden := range []string{"https://cdn.", "http://", "<iframe", "<form"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("document contains forbidden external surface %q", forbidden)
		}
	}
}

func TestFrontendHasNoMockReadyOrSeedFolders(t *testing.T) {
	script, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(script))
	for _, forbidden := range []string{"seedfolders", "simulate-error", "mock-ready", "fake success"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("frontend contains mock-only operation %q", forbidden)
		}
	}
}
