// Package desktopweb serves the small, local-only desktop frontend.
package desktopweb

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var embedded embed.FS

//go:embed assets/index.html
var indexHTML []byte

// DocumentURL is the only navigation target the desktop host should allow.
const DocumentURL = "https://localassets/index.html"

// Document returns a copy of the root document to the desktop host.
func Document() []byte { return append([]byte(nil), indexHTML...) }

// FS returns the embedded web assets with paths relative to the asset root.
// The desktop host should expose these files only at its fixed localassets
// origin; this package does not start a listener or read from disk.
func FS() fs.FS {
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic("desktopweb: embedded assets are missing: " + err.Error())
	}
	return assets
}
