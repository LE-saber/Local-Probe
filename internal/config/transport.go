package config

import "strings"

// ConnectionTransport identifies the non-secret ingress metadata for a
// connection. It is descriptive configuration only: it does not authenticate
// a caller and it does not contain tunnel credentials or endpoint URLs.
type ConnectionTransport string

const (
	TransportLocal           ConnectionTransport = "local"
	TransportOpenAIRuntime   ConnectionTransport = "openai_runtime"
	TransportCloudflareNamed ConnectionTransport = "cloudflare_named"
)

func (t ConnectionTransport) valid() bool {
	switch t {
	case TransportLocal, TransportOpenAIRuntime, TransportCloudflareNamed:
		return true
	default:
		return false
	}
}

// normalizedTransport makes the zero value equivalent to the legacy local
// connection representation. This preserves configs and in-package callers
// created before transport metadata existed.
func normalizedTransport(t ConnectionTransport) ConnectionTransport {
	if t == "" {
		return TransportLocal
	}
	return t
}

// validateConnectionTransport validates only the shape and relationship of
// non-secret transport metadata. A remote transport must name a constrained
// tunnel identifier; endpoint URLs, shell fragments and credential-shaped
// values are intentionally rejected before they can enter persisted config.
func validateConnectionTransport(transport ConnectionTransport, tunnelID, tunnelAlias, field string) error {
	transport = normalizedTransport(transport)
	if !transport.valid() {
		return invalid(field+".transport", "unsupported transport")
	}

	switch transport {
	case TransportLocal:
		if tunnelID != "" {
			return invalid(field+".tunnel_id", "must be empty for local transport")
		}
		if tunnelAlias != "" {
			return invalid(field+".tunnel_alias", "must be empty for local transport")
		}
	case TransportOpenAIRuntime, TransportCloudflareNamed:
		if !ValidTransportIdentifier(tunnelID) {
			return invalid(field+".tunnel_id", "required constrained identifier")
		}
		if tunnelAlias != "" && !ValidTransportIdentifier(tunnelAlias) {
			return invalid(field+".tunnel_alias", "invalid constrained identifier")
		}
	}
	return nil
}

// validTransportIdentifier intentionally permits only a conservative opaque
// identifier alphabet. UUID-like tunnel IDs and simple deployment aliases are
// supported; URLs, paths, shell syntax, PEM material, bearer/JWT strings and
// common API-key forms are excluded without echoing their values in errors.
// ValidTransportIdentifier reports whether value is safe to use as
// non-secret transport metadata. It is exported so lifecycle components use
// exactly the same validation rule as persisted configuration.
func ValidTransportIdentifier(value string) bool {
	if !validIdentifier(value) || value == "" {
		return false
	}
	if !isASCIIAlphaNumeric(value[0]) || strings.Contains(value, "--") {
		return false
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{
		"bearer-", "token-", "secret-", "password-", "passwd-", "key-", "jwt-",
		"sk-", "pk-", "ghp_", "glpat-", "xox", "ssh-", "eyj",
	} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if lower == "bearer" || lower == "token" || lower == "secret" || lower == "password" || lower == "passwd" || lower == "key" || lower == "jwt" {
		return false
	}
	// Long, separator-free hexadecimal values are much more likely to be a
	// key/hash than a tunnel identifier. UUIDs remain valid because hyphens
	// make their structure explicit.
	if len(value) >= 32 && isHex(value) {
		return false
	}
	if len(value) >= 40 && !strings.ContainsAny(value, "-_") {
		return false
	}
	return true
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func isHex(value string) bool {
	for i := 0; i < len(value); i++ {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f') || (value[i] >= 'A' && value[i] <= 'F')) {
			return false
		}
	}
	return true
}
