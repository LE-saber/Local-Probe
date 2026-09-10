package search

import (
	"path"
	"strings"
	"unicode/utf8"
)

func validateRelativePath(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return validRelativePath(value)
}

func validateGlob(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "" {
			return false
		}
		if _, err := path.Match(part, ""); err != nil {
			return false
		}
	}
	return true
}

// matchGlob uses slash-separated path.Match components. A double-star
// component matches zero or more complete path components; it never crosses a
// slash through a single-component pattern.
func matchGlob(pattern, value string, caseSensitive bool) bool {
	patternParts := strings.Split(pattern, "/")
	valueParts := strings.Split(value, "/")
	if !caseSensitive {
		for i := range patternParts {
			patternParts[i] = strings.ToLower(patternParts[i])
		}
		for i := range valueParts {
			valueParts[i] = strings.ToLower(valueParts[i])
		}
	}
	var match func(int, int) bool
	match = func(pi, vi int) bool {
		for pi < len(patternParts) {
			if patternParts[pi] == "**" {
				if pi+1 == len(patternParts) {
					return true
				}
				for n := vi; n <= len(valueParts); n++ {
					if match(pi+1, n) {
						return true
					}
				}
				return false
			}
			if vi >= len(valueParts) {
				return false
			}
			matched, err := path.Match(patternParts[pi], valueParts[vi])
			if err != nil || !matched {
				return false
			}
			pi++
			vi++
		}
		return vi == len(valueParts)
	}
	return match(0, 0)
}
