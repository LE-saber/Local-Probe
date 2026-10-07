package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLegacyConnectionTransportDefaultsToLocal(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	connection, ok := c.Connection("account-a")
	if !ok {
		t.Fatal("legacy connection is missing")
	}
	if connection.Transport() != TransportLocal || connection.TunnelID() != "" || connection.TunnelAlias() != "" {
		t.Fatalf("legacy transport = %q, tunnel_id=%q, tunnel_alias=%q", connection.Transport(), connection.TunnelID(), connection.TunnelAlias())
	}

	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"transport":"local"`)) {
		t.Fatalf("legacy marshal did not canonicalize local transport: %s", encoded)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("canonical legacy config did not parse: %v", err)
	}
}

func TestConnectionTransportRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		transport ConnectionTransport
		tunnelID  string
		alias     string
	}{
		{name: "openai runtime", transport: TransportOpenAIRuntime, tunnelID: "runtime-prod-01", alias: "prod-runtime"},
		{name: "cloudflare named", transport: TransportCloudflareNamed, tunnelID: "12345678-1234-1234-1234-123456789012", alias: "cf-prod"},
		{name: "local", transport: TransportLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := connectionTransportJSON(tc.transport, tc.tunnelID, tc.alias)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			before, ok := cfg.Connection("account-a")
			if !ok {
				t.Fatal("connection is missing")
			}
			if before.Transport() != tc.transport || before.TunnelID() != tc.tunnelID || before.TunnelAlias() != tc.alias {
				t.Fatalf("parsed transport = %q, tunnel_id=%q, tunnel_alias=%q", before.Transport(), before.TunnelID(), before.TunnelAlias())
			}

			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			afterConfig, err := Parse(encoded)
			if err != nil {
				t.Fatalf("round-trip parse failed: %v", err)
			}
			after, ok := afterConfig.Connection("account-a")
			if !ok {
				t.Fatal("round-trip connection is missing")
			}
			if after.Transport() != before.Transport() || after.TunnelID() != before.TunnelID() || after.TunnelAlias() != before.TunnelAlias() {
				t.Fatalf("round-trip changed transport metadata: before=%q/%q/%q after=%q/%q/%q", before.Transport(), before.TunnelID(), before.TunnelAlias(), after.Transport(), after.TunnelID(), after.TunnelAlias())
			}
		})
	}
}

func TestNewConnectionWithTransportAndImmutability(t *testing.T) {
	transport := TransportCloudflareNamed
	tunnelID := "cf-prod-01"
	alias := "primary"
	connection, err := NewConnectionWithTransport("remote", "remote ingress", "read-project", "cred-a", true, transport, tunnelID, alias)
	if err != nil {
		t.Fatal(err)
	}
	transport = TransportLocal
	tunnelID = "changed"
	alias = "changed"
	if connection.Transport() != TransportCloudflareNamed || connection.TunnelID() != "cf-prod-01" || connection.TunnelAlias() != "primary" {
		t.Fatalf("constructor did not preserve immutable metadata: %q/%q/%q", connection.Transport(), connection.TunnelID(), connection.TunnelAlias())
	}

	cfg, err := Parse([]byte(connectionTransportJSON(TransportCloudflareNamed, "cf-prod-01", "primary")))
	if err != nil {
		t.Fatal(err)
	}
	connections := cfg.Connections()
	connections[0] = NewConnection("replacement", "replacement", "read-project", "cred-a", true)
	clone := cfg.Clone()
	cloneConnections := clone.Connections()
	cloneConnections[0] = NewConnection("replacement", "replacement", "read-project", "cred-a", true)

	got, ok := cfg.Connection("account-a")
	if !ok || got.Transport() != TransportCloudflareNamed || got.TunnelID() != "cf-prod-01" || got.TunnelAlias() != "primary" {
		t.Fatalf("caller mutation changed original connection: %+v", got)
	}
	cloneGot, ok := clone.Connection("account-a")
	if !ok || cloneGot.Transport() != TransportCloudflareNamed || cloneGot.TunnelID() != "cf-prod-01" || cloneGot.TunnelAlias() != "primary" {
		t.Fatalf("caller mutation changed cloned connection: %+v", cloneGot)
	}
}

func TestConnectionTransportValidation(t *testing.T) {
	cases := []struct {
		name      string
		transport ConnectionTransport
		tunnelID  string
		alias     string
		wantField string
	}{
		{name: "empty enum", transport: ConnectionTransport(""), wantField: "connection.transport"},
		{name: "unsupported enum", transport: ConnectionTransport("ssh"), tunnelID: "prod", wantField: "connection.transport"},
		{name: "local tunnel id", transport: TransportLocal, tunnelID: "prod", wantField: "connection.tunnel_id"},
		{name: "local alias", transport: TransportLocal, alias: "prod", wantField: "connection.tunnel_alias"},
		{name: "remote missing id", transport: TransportOpenAIRuntime, alias: "prod", wantField: "connection.tunnel_id"},
		{name: "url", transport: TransportCloudflareNamed, tunnelID: "https://example.test", wantField: "connection.tunnel_id"},
		{name: "path", transport: TransportCloudflareNamed, tunnelID: `C:\\tunnel`, wantField: "connection.tunnel_id"},
		{name: "command", transport: TransportCloudflareNamed, tunnelID: "cloudflared-tunnel-run", alias: "--prod", wantField: "connection.tunnel_alias"},
		{name: "bearer token", transport: TransportCloudflareNamed, tunnelID: "Bearer-secret", wantField: "connection.tunnel_id"},
		{name: "api key", transport: TransportCloudflareNamed, tunnelID: "sk-proj-secret", wantField: "connection.tunnel_id"},
		{name: "jwt", transport: TransportCloudflareNamed, tunnelID: "eyJheader-payload", wantField: "connection.tunnel_id"},
		{name: "hex key", transport: TransportCloudflareNamed, tunnelID: "0123456789abcdef0123456789abcdef", wantField: "connection.tunnel_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConnectionWithTransport("connection", "label", "profile", "credential", true, tc.transport, tc.tunnelID, tc.alias)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid transport metadata: %v", err)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tc.wantField {
				t.Fatalf("error = %v, want field %q", err, tc.wantField)
			}
			if (tc.tunnelID != "" && strings.Contains(err.Error(), tc.tunnelID)) || (tc.alias != "" && strings.Contains(err.Error(), tc.alias)) {
				t.Fatalf("validation error echoed metadata: %v", err)
			}
		})
	}
}

func TestConnectionTransportJSONValidationAndNoEcho(t *testing.T) {
	secretLike := "sk-proj-very-secret-value"
	data := connectionTransportJSON(TransportCloudflareNamed, secretLike, "prod")
	_, err := Parse([]byte(data))
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted secret-shaped tunnel ID: %v", err)
	}
	if strings.Contains(err.Error(), secretLike) {
		t.Fatalf("parse error echoed secret-shaped metadata: %v", err)
	}
}

func TestExplicitEmptyConnectionTransportIsRejected(t *testing.T) {
	data := connectionTransportJSON(ConnectionTransport(""), "", "")
	_, err := Parse([]byte(data))
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted explicit empty transport: %v", err)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "connections[0].transport" {
		t.Fatalf("error = %v, want connections[0].transport", err)
	}
}

func connectionTransportJSON(transport ConnectionTransport, tunnelID, tunnelAlias string) string {
	old := `{"id": "account-a", "label": "A", "profile_id": "read-project", "credential_ref": "cred-a", "enabled": true}`
	replacement := fmt.Sprintf(`{"id": "account-a", "label": "A", "profile_id": "read-project", "credential_ref": "cred-a", "enabled": true, "transport": %q, "tunnel_id": %q, "tunnel_alias": %q}`, transport, tunnelID, tunnelAlias)
	updated := strings.Replace(validJSON, old, replacement, 1)
	if updated == validJSON {
		panic("connection test fixture did not contain account-a")
	}
	return updated
}
