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

func validateRequest(r Request, scope Scope, limits Limits) *ItemError {
	if !validID(r.File.RootID) || !ValidPath(r.File.Path) || r.Offset < 0 ||
		r.MaxBytes < 0 || r.MaxBytes > limits.MaxItemBytes || len(r.ExpectedVersion) > 256 || !utf8.ValidString(r.ExpectedVersion) {
		return issue("invalid_request", "use a canonical relative path and a bounded read range")
	}
	if !scope.Allows(r.File.RootID) {
		return issue("denied", "root is not authorized for this connection")
	}
	if r.Range.Kind != "" && r.Range.Kind != RangeBytes && r.Range.Kind != RangeLines && r.Range.Kind != RangeTail {
		return issue("invalid_request", "range kind must be bytes, lines, or tail")
	}
	switch r.Range.Kind {
	case RangeLines:
		if r.Offset != 0 || r.Range.StartLine < 1 || r.Range.MaxLines < 1 || r.Range.MaxLines > 1<<20 || r.Range.TailLines != 0 {
			return issue("invalid_request", "lines requires a positive one-based start_line and max_lines")
		}
	case RangeTail:
		if r.Offset != 0 || r.Range.TailLines < 1 || r.Range.TailLines > 1<<20 || r.Range.StartLine != 0 || r.Range.MaxLines != 0 {
			return issue("invalid_request", "tail requires a positive tail_lines count")
		}
	default:
		if r.Range.StartLine != 0 || r.Range.MaxLines != 0 || r.Range.TailLines != 0 || r.Range.MaxScanBytes != 0 {
			return issue("invalid_request", "byte ranges cannot contain line or scan options")
		}
	}
	if r.Range.MaxScanBytes < 0 || r.Range.MaxScanBytes > limits.MaxScanBytes {
		return issue("invalid_request", "max_scan_bytes exceeds the configured scan budget")
	}
	return nil
}
