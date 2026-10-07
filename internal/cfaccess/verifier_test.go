package cfaccess

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

func TestVerifierAcceptsMappedSubjectWithConfiguredClockSkew(t *testing.T) {
	fixture := newOIDCFixture(t)
	base := time.Unix(1_800_000_000, 0).UTC()
	verifier := fixture.verifier(t, Config{
		ClockSkew:             30 * time.Second,
		PrincipalToConnection: map[string]string{"subject-a": "connection-a"},
	})
	verifier.now = func() time.Time { return base }

	token := fixture.token(t, fixture.privateKey, base, "subject-a", base.Add(-10*time.Second), base.Add(10*time.Minute))
	info, err := verifier.Verify(context.Background(), token, nil)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if info.UserID != "connection-a" || len(info.Scopes) != 1 || info.Scopes[0] != "local-probe:read" {
		t.Fatalf("TokenInfo = %#v", info)
	}
	if !info.Expiration.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("Expiration = %v", info.Expiration)
	}

	// A token just beyond its expiry is accepted inside the configured skew.
	withinSkew := fixture.token(t, fixture.privateKey, base, "subject-a", base.Add(-10*time.Second), base.Add(-10*time.Second))
	if _, err := verifier.Verify(context.Background(), withinSkew, nil); err != nil {
		t.Fatalf("Verify() within clock skew error = %v", err)
	}
}

func TestVerifierRejectsSignatureIssuerAudienceAndTimeFailures(t *testing.T) {
	fixture := newOIDCFixture(t)
	base := time.Unix(1_800_000_000, 0).UTC()
	verifier := fixture.verifier(t, Config{
		ClockSkew:             time.Second,
		PrincipalToConnection: map[string]string{"subject-a": "connection-a"},
	})
	verifier.now = func() time.Time { return base }

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		token string
	}{
		{
			name:  "bad signature",
			token: fixture.token(t, otherKey, base, "subject-a", base, base.Add(time.Hour)),
		},
		{
			name:  "wrong issuer",
			token: fixture.tokenWithIssuer(t, fixture.privateKey, fixture.server.URL+"/wrong", "audience-a", "subject-a", base, base.Add(time.Hour)),
		},
		{
			name:  "wrong audience",
			token: fixture.tokenWithIssuer(t, fixture.privateKey, fixture.server.URL, "audience-b", "subject-a", base, base.Add(time.Hour)),
		},
		{
			name:  "expired",
			token: fixture.token(t, fixture.privateKey, base, "subject-a", base, base.Add(-2*time.Second)),
		},
		{
			name:  "not before",
			token: fixture.tokenWithNBF(t, fixture.privateKey, base, "subject-a", base.Add(2*time.Second), base.Add(time.Hour)),
		},
		{
			name:  "unknown subject",
			token: fixture.token(t, fixture.privateKey, base, "not-mapped", base, base.Add(time.Hour)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), test.token, nil); err == nil || err != auth.ErrInvalidToken {
				t.Fatalf("Verify() error = %v, want auth.ErrInvalidToken", err)
			}
		})
	}

	missingExpiry := fixture.tokenWithoutExpiry(t, fixture.privateKey, base, "subject-a")
	if _, err := verifier.Verify(context.Background(), missingExpiry, nil); err != auth.ErrInvalidToken {
		t.Fatalf("missing exp error = %v, want auth.ErrInvalidToken", err)
	}

	// nbf within the configured skew is accepted.
	withinSkew := fixture.tokenWithNBF(t, fixture.privateKey, base, "subject-a", base.Add(500*time.Millisecond), base.Add(time.Hour))
	if _, err := verifier.Verify(context.Background(), withinSkew, nil); err != nil {
		t.Fatalf("nbf within clock skew error = %v", err)
	}
}

func TestParseStrictExternalConfiguration(t *testing.T) {
	cfg, err := Parse([]byte(`{
        "issuer":"http://127.0.0.1:12345",
        "jwks_url":"http://127.0.0.1:12345/keys",
        "audience":"audience-a",
        "clock_skew_seconds":15,
        "principal_to_connection":{"subject-a":"connection-a"},
        "public_hosts":["mcp.example.test"]
    }`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.ClockSkew != 15*time.Second || cfg.PublicHosts[0] != "mcp.example.test" {
		t.Fatalf("Config = %#v", cfg)
	}

	for _, data := range []string{
		`{"issuer":"http://127.0.0.1:1","jwks_url":"http://127.0.0.1:1/keys","audience":"a","principal_to_connection":{"s":"c"},"unexpected":true}`,
		`{"issuer":"http://127.0.0.1:1","jwks_url":"http://127.0.0.1:1/keys","audience":"a","principal_to_connection":{"s":"c"}} {"extra":true}`,
		`{"issuer":"http://example.test","jwks_url":"https://example.test/keys","audience":"a","principal_to_connection":{"s":"c"}}`,
	} {
		if _, err := Parse([]byte(data)); err != ErrInvalidConfig {
			t.Fatalf("Parse(%s) error = %v, want ErrInvalidConfig", data, err)
		}
	}
}

type oidcFixture struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &oidctest.Server{PublicKeys: []oidctest.PublicKey{{
		PublicKey: privateKey.Public(),
		KeyID:     "test-key",
		Algorithm: oidc.RS256,
	}}}
	server := httptest.NewServer(provider)
	provider.SetIssuer(server.URL)
	t.Cleanup(server.Close)
	return &oidcFixture{server: server, privateKey: privateKey}
}

func (f *oidcFixture) verifier(t *testing.T, overrides Config) *Verifier {
	t.Helper()
	cfg := Config{
		Issuer:                f.server.URL,
		JWKSURL:               f.server.URL + "/keys",
		Audience:              "audience-a",
		ClockSkew:             overrides.ClockSkew,
		PrincipalToConnection: overrides.PrincipalToConnection,
	}
	verifier, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func (f *oidcFixture) token(t *testing.T, key *rsa.PrivateKey, now time.Time, subject string, iat, exp time.Time) string {
	return f.tokenWithIssuer(t, key, f.server.URL, "audience-a", subject, iat, exp)
}

func (f *oidcFixture) tokenWithIssuer(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject string, iat, exp time.Time) string {
	return f.tokenJSON(t, key, map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": subject,
		"iat": iat.Unix(),
		"exp": exp.Unix(),
	})
}

func (f *oidcFixture) tokenWithNBF(t *testing.T, key *rsa.PrivateKey, now time.Time, subject string, nbf, exp time.Time) string {
	return f.tokenJSON(t, key, map[string]any{
		"iss": f.server.URL,
		"aud": "audience-a",
		"sub": subject,
		"iat": now.Unix(),
		"nbf": nbf.Unix(),
		"exp": exp.Unix(),
	})
}

func (f *oidcFixture) tokenWithoutExpiry(t *testing.T, key *rsa.PrivateKey, now time.Time, subject string) string {
	return f.tokenJSON(t, key, map[string]any{
		"iss": f.server.URL,
		"aud": "audience-a",
		"sub": subject,
		"iat": now.Unix(),
	})
}

func (f *oidcFixture) tokenJSON(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	data, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return oidctest.SignIDToken(key, "test-key", oidc.RS256, strings.TrimSpace(string(data)))
}
