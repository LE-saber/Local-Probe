// Package cfaccess verifies Cloudflare Access JWTs at the loopback MCP
// ingress. It deliberately does not implement an OAuth server: Cloudflare
// Access Managed OAuth remains the edge responsibility.
package cfaccess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

const (
	// AccessJWTHeader is the header Cloudflare Access adds after validating a
	// request at the edge. The origin still verifies the signed JWT itself.
	AccessJWTHeader = "Cf-Access-Jwt-Assertion"

	defaultClockSkew = 30 * time.Second
	maxClockSkew     = 5 * time.Minute
	maxConfigBytes   = 256 << 10
	maxTokenBytes    = 64 << 10
)

var (
	ErrInvalidConfig = errors.New("invalid Cloudflare Access configuration")
	ErrInvalidToken  = errors.New("invalid Cloudflare Access token")
)

// Config contains non-secret Cloudflare Access verifier settings. The
// PrincipalToConnection mapping is intentionally explicit: a request cannot
// choose a connection by query, path, or MCP argument.
//
// ClockSkew defaults to 30 seconds and is applied to both exp and nbf. The
// mapping and endpoint values should be loaded from a protected runtime file
// or equivalent external configuration, never from model input.
type Config struct {
	Issuer                string
	JWKSURL               string
	Audience              string
	ClockSkew             time.Duration
	PrincipalToConnection map[string]string
	PublicHosts           []string
}

// FileConfig is the JSON representation accepted by Load and LoadFile.
// Durations are expressed as seconds to keep the file human-editable.
type FileConfig struct {
	Issuer                string            `json:"issuer"`
	JWKSURL               string            `json:"jwks_url"`
	Audience              string            `json:"audience"`
	ClockSkewSeconds      int               `json:"clock_skew_seconds,omitempty"`
	PrincipalToConnection map[string]string `json:"principal_to_connection"`
	PublicHosts           []string          `json:"public_hosts"`
}

// LoadFile loads one strict, bounded JSON configuration. It does not read any
// credential material; Cloudflare Access supplies the signed assertion at
// request time.
func LoadFile(name string) (Config, error) {
	if strings.TrimSpace(name) == "" {
		return Config{}, ErrInvalidConfig
	}
	data, err := os.ReadFile(name)
	if err != nil || len(data) > maxConfigBytes {
		return Config{}, ErrInvalidConfig
	}
	return Parse(data)
}

// Parse validates one complete JSON runtime configuration and rejects unknown
// fields or trailing values.
func Parse(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > maxConfigBytes {
		return Config{}, ErrInvalidConfig
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var raw FileConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, ErrInvalidConfig
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, ErrInvalidConfig
	}
	clockSkew := defaultClockSkew
	if raw.ClockSkewSeconds != 0 {
		if raw.ClockSkewSeconds < 0 {
			return Config{}, ErrInvalidConfig
		}
		clockSkew = time.Duration(raw.ClockSkewSeconds) * time.Second
	}
	cfg := Config{
		Issuer:                raw.Issuer,
		JWKSURL:               raw.JWKSURL,
		Audience:              raw.Audience,
		ClockSkew:             clockSkew,
		PrincipalToConnection: raw.PrincipalToConnection,
		PublicHosts:           append([]string(nil), raw.PublicHosts...),
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// New constructs a verifier backed by coreos/go-oidc's remote JWKS key set.
// The verifier only accepts RS256, checks issuer and audience, and performs
// signature verification through the mature OIDC library. exp and nbf are
// checked below with the configured, symmetric clock skew.
func New(cfg Config) (*Verifier, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	clockSkew := cfg.ClockSkew
	if clockSkew == 0 {
		clockSkew = defaultClockSkew
	}
	mapping := make(map[string]string, len(cfg.PrincipalToConnection))
	for principal, connection := range cfg.PrincipalToConnection {
		mapping[principal] = connection
	}

	keySet := oidc.NewRemoteKeySet(context.Background(), cfg.JWKSURL)
	verifier := oidc.NewVerifier(cfg.Issuer, keySet, &oidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
		SkipExpiryCheck:      true,
	})
	return &Verifier{
		verifier:  verifier,
		mapping:   mapping,
		clockSkew: clockSkew,
		now:       time.Now,
	}, nil
}

func validateConfig(cfg Config) error {
	if err := validateEndpoint(cfg.Issuer, "issuer"); err != nil {
		return err
	}
	if err := validateEndpoint(cfg.JWKSURL, "jwks_url"); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Audience) != cfg.Audience || cfg.Audience == "" || len(cfg.Audience) > 512 || !utf8.ValidString(cfg.Audience) {
		return ErrInvalidConfig
	}
	clockSkew := cfg.ClockSkew
	if clockSkew == 0 {
		clockSkew = defaultClockSkew
	}
	if clockSkew < 0 || clockSkew > maxClockSkew {
		return ErrInvalidConfig
	}
	if len(cfg.PrincipalToConnection) == 0 {
		return ErrInvalidConfig
	}
	for principal, connection := range cfg.PrincipalToConnection {
		if !validPrincipal(principal) || !validConnectionID(connection) {
			return ErrInvalidConfig
		}
	}
	return nil
}

// Verifier validates a Cloudflare Access JWT and returns SDK authentication
// information whose UserID is the mapped Local-Probe connection ID.
type Verifier struct {
	verifier  *oidc.IDTokenVerifier
	mapping   map[string]string
	clockSkew time.Duration
	now       func() time.Time
}

// ClockSkew reports the verifier's expiry tolerance so the SDK middleware can
// apply the same policy after this verifier returns TokenInfo.
func (v *Verifier) ClockSkew() time.Duration {
	if v == nil {
		return 0
	}
	return v.clockSkew
}

// ConnectionIDs returns the explicitly configured target connections. The
// MCP layer uses this during startup to reject a mapping that does not refer
// to an enabled Local-Probe connection; callers cannot mutate verifier state.
func (v *Verifier) ConnectionIDs() []string {
	if v == nil || len(v.mapping) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(v.mapping))
	for _, connectionID := range v.mapping {
		seen[connectionID] = struct{}{}
	}
	connections := make([]string, 0, len(seen))
	for connectionID := range seen {
		connections = append(connections, connectionID)
	}
	sort.Strings(connections)
	return connections
}

// Verify implements auth.TokenVerifier. It intentionally returns the generic
// SDK invalid-token error for all request-time failures, avoiding disclosure
// of issuer, audience, subject, or key-set details to a remote caller.
func (v *Verifier) Verify(ctx context.Context, rawToken string, _ *http.Request) (*auth.TokenInfo, error) {
	if v == nil || v.verifier == nil || len(rawToken) == 0 || len(rawToken) > maxTokenBytes || strings.TrimSpace(rawToken) != rawToken {
		return nil, auth.ErrInvalidToken
	}
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	var claims struct {
		Expiration json.RawMessage `json:"exp"`
		NotBefore  json.RawMessage `json:"nbf"`
	}
	if err := token.Claims(&claims); err != nil {
		return nil, auth.ErrInvalidToken
	}
	expiration, err := parseNumericDate(claims.Expiration)
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	var notBefore time.Time
	if len(claims.NotBefore) != 0 && string(claims.NotBefore) != "null" {
		notBefore, err = parseNumericDate(claims.NotBefore)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
	}
	now := time.Now()
	if v.now != nil {
		now = v.now()
	}
	if expiration.Add(v.clockSkew).Before(now) || (!notBefore.IsZero() && now.Add(v.clockSkew).Before(notBefore)) {
		return nil, auth.ErrInvalidToken
	}
	if !validPrincipal(token.Subject) {
		return nil, auth.ErrInvalidToken
	}
	connectionID, ok := v.mapping[token.Subject]
	if !ok || !validConnectionID(connectionID) {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		UserID:     connectionID,
		Scopes:     []string{"local-probe:read"},
		Expiration: expiration,
	}, nil
}

func validateEndpoint(raw, field string) error {
	if strings.TrimSpace(raw) != raw || raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) {
		return ErrInvalidConfig
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ErrInvalidConfig
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || !isLoopbackHost(u.Hostname()) {
			return ErrInvalidConfig
		}
	}
	_ = field // Keep the public error generic; field names are not request data.
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host).IsLoopback()
}

func validPrincipal(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validConnectionID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func parseNumericDate(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, ErrInvalidToken
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil || number.String() == "" {
		return time.Time{}, ErrInvalidToken
	}
	seconds, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 1e11 {
		return time.Time{}, ErrInvalidToken
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), nil
}
