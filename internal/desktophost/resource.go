package desktophost

import (
	"io/fs"
	"net/url"
	"strings"
	"unicode/utf16"

	"github.com/LE-saber/Local-Probe/internal/desktopweb"
)

const desktopCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; form-action 'none'; frame-src 'none'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; script-src-attr 'none'; style-src-attr 'none'"

type localResource struct {
	status      int
	reason      string
	headers     string
	body        []byte
	contentType string
}

func trustedRPCSource(argsSource, topLevelSource string) bool {
	return argsSource == desktopweb.DocumentURL && topLevelSource == desktopweb.DocumentURL
}

func localAsset(uri, method string) localResource {
	denied := localResource{status: 403, reason: "Forbidden", headers: "Content-Length: 0\r\nX-Content-Type-Options: nosniff\r\nCache-Control: no-store"}
	if method != "GET" || strings.ContainsAny(uri, "?#") {
		return denied
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "https" || u.Host != "localassets" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.EscapedPath() != u.Path || u.RawPath != "" {
		return denied
	}
	var body []byte
	contentType := ""
	switch u.Path {
	case "/", "/index.html":
		body, contentType = desktopweb.Document(), "text/html; charset=utf-8"
	case "/app.js":
		body, err = fs.ReadFile(desktopweb.FS(), "app.js")
		contentType = "text/javascript; charset=utf-8"
	case "/icons.js":
		body, err = fs.ReadFile(desktopweb.FS(), "icons.js")
		contentType = "text/javascript; charset=utf-8"
	case "/app.css":
		body, err = fs.ReadFile(desktopweb.FS(), "app.css")
		contentType = "text/css; charset=utf-8"
	default:
		return denied
	}
	if err != nil || len(body) == 0 {
		return denied
	}
	headers := "Content-Type: " + contentType + "\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n" +
		"X-Content-Type-Options: nosniff\r\n" +
		"Referrer-Policy: no-referrer\r\n" +
		"Cache-Control: no-store\r\n" +
		"X-Frame-Options: DENY\r\n" +
		"Content-Security-Policy: " + desktopCSP
	return localResource{status: 200, reason: "OK", headers: headers, body: body, contentType: contentType}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [24]byte
	i := len(buffer)
	for value > 0 {
		i--
		buffer[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[i:])
}

func utf16MultiString(value string) []uint16 { return utf16.Encode([]rune(value)) }
