package audit

import "regexp"

var (
	bearerPattern     = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	jwtPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	keyPattern        = regexp.MustCompile(`(?i)\b(?:sk-[A-Za-z0-9_-]{8,}|(?:api[-_ ]?key|authorization|cookie|password|private[-_ ]?key|secret)\b)`)
	privateKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)
)

func containsSensitive(value string) bool {
	return bearerPattern.MatchString(value) || jwtPattern.MatchString(value) || keyPattern.MatchString(value) || privateKeyPattern.MatchString(value)
}

func Redact(value string) string {
	if containsSensitive(value) {
		return "[REDACTED]"
	}
	return value
}
