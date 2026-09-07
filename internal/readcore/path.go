package readcore

import (
	"strings"
	"unicode/utf8"
)

func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ValidPath checks a conservative portable lexical subset. It does NOT resolve
// symlinks, junctions, hard links or races; the production Source must do that.
func ValidPath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.HasPrefix(p, "/") {
		return false
	}
	for _, c := range p {
		if c < 32 || c == 127 || strings.ContainsRune("<>:\"\\|?*", c) {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
			return false
		}
		for _, prefix := range []string{"COM", "LPT"} {
			if strings.HasPrefix(base, prefix) {
				suffix := strings.TrimPrefix(base, prefix)
				if len(suffix) == 1 && suffix[0] >= '1' && suffix[0] <= '9' || suffix == "\u00b9" || suffix == "\u00b2" || suffix == "\u00b3" {
					return false
				}
			}
		}
	}
	return true
}

func validateRequest(r Request, scope Scope, maxBytes int) *ItemError {
	if !validID(r.File.RootID) || !ValidPath(r.File.Path) || r.Offset < 0 ||
		r.MaxBytes < 0 || r.MaxBytes > maxBytes || len(r.ExpectedVersion) > 256 || !utf8.ValidString(r.ExpectedVersion) {
		return issue("invalid_request", "use a canonical relative path and a bounded nonnegative byte range")
	}
	if !scope.Allows(r.File.RootID) {
		return issue("denied", "root is not authorized for this connection")
	}
	return nil
}
