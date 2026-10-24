// Package deviceidentity owns local pairing keys and public pairing artifacts.
// Its signing methods belong to the owner's local management boundary only.
package deviceidentity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"time"

	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

const Version = "local-probe.pairing.v1"
const MaxArtifactBytes = 64 << 10

var (
	ErrInvalid = errors.New("invalid pairing artifact")
	ErrTrust   = errors.New("pairing fingerprint or identity mismatch")
	ErrStorage = errors.New("identity storage unavailable")
	ErrExists  = errors.New("identity record already exists")
	ErrRevoked = errors.New("paired identity revoked")
)

// Bundle is public. Fingerprint must be compared via an independently trusted
// path; an attacker-provided bundle and its matching hash do not establish trust.
type Bundle struct {
	Version    string `json:"version"`
	DeviceID   string `json:"device_id"`
	ServerName string `json:"server_name"`
	CA         []byte `json:"ca_pem"`
	Target     []byte `json:"target_pem"`
}

type Request struct {
	Version string `json:"version"`
	AgentID string `json:"agent_id"`
	CSR     []byte `json:"csr_pem"`
}

// Grant contains no private key. ConnectionID is chosen locally by the owner,
// not copied from a caller's CSR. Epoch 1 is terminally revoked by a tombstone.
type Grant struct {
	Version      string    `json:"version"`
	DeviceID     string    `json:"device_id"`
	AgentID      string    `json:"agent_id"`
	ConnectionID string    `json:"connection_id"`
	KeyPin       string    `json:"key_pin"`
	Epoch        uint64    `json:"epoch"`
	Certificate  []byte    `json:"certificate_pem"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Private state is deliberately unexported: default JSON marshaling cannot leak
// keys into a diagnostic or public pairing response.
type Authority struct {
	id                           string
	ca, caKey, target, targetKey []byte
}
type Agent struct {
	id  string
	key []byte
}

func NewAuthority() (*Authority, error) {
	id, err := randomID("device-")
	if err != nil {
		return nil, err
	}
	caKey, err := newKey()
	if err != nil {
		return nil, err
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ca := template(now, 5*365*24*time.Hour)
	ca.Subject = pkix.Name{CommonName: id}
	ca.IsCA = true
	ca.BasicConstraintsValid = true
	ca.MaxPathLenZero = true
	ca.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	target := template(now, 365*24*time.Hour)
	target.Subject = pkix.Name{CommonName: id}
	target.DNSNames = []string{id + ".local-probe.invalid"}
	target.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	leaf, err := x509.CreateCertificate(rand.Reader, target, parsed, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caPrivate, _ := x509.MarshalPKCS8PrivateKey(caKey)
	private, _ := x509.MarshalPKCS8PrivateKey(key)
	return &Authority{id, der, caPrivate, leaf, private}, nil
}

func NewAgent() (*Agent, Request, error) {
	id, err := randomID("agent-")
	if err != nil {
		return nil, Request{}, err
	}
	key, err := newKey()
	if err != nil {
		return nil, Request{}, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: id}}, key)
	if err != nil {
		return nil, Request{}, err
	}
	private, _ := x509.MarshalPKCS8PrivateKey(key)
	return &Agent{id, private}, Request{Version, id, encodePEM("CERTIFICATE REQUEST", der)}, nil
}

func (a *Authority) Bundle() Bundle {
	return Bundle{Version, a.id, a.id + ".local-probe.invalid", encodePEM("CERTIFICATE", a.ca), encodePEM("CERTIFICATE", a.target)}
}

func (b Bundle) Fingerprint() (string, error) {
	if _, _, err := b.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (b Bundle) Verify(expected string) error {
	actual, err := b.Fingerprint()
	if err != nil {
		return err
	}
	if !validHash(expected) || actual != expected {
		return ErrTrust
	}
	return nil
}

func (r Request) Fingerprint() (string, error) {
	csr, err := r.validate()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append([]byte(Version+"\x00"+r.AgentID+"\x00"), csr.Raw...))
	return hex.EncodeToString(hash[:]), nil
}

// Issue requires the exact CSR fingerprint reviewed by the owner. Extensions in
// the CSR are ignored: clients cannot request CA status, names or server usage.
func (a *Authority) Issue(r Request, expected, connection string, lifetime time.Duration) (Grant, error) {
	actual, err := r.Fingerprint()
	if err != nil {
		return Grant{}, err
	}
	if actual != expected || !validHash(expected) {
		return Grant{}, ErrTrust
	}
	if !validID(connection) || lifetime <= 0 || lifetime > 30*24*time.Hour {
		return Grant{}, ErrInvalid
	}
	csr, _ := r.validate()
	ca, err := x509.ParseCertificate(a.ca)
	if err != nil {
		return Grant{}, ErrInvalid
	}
	caKey, err := x509.ParsePKCS8PrivateKey(a.caKey)
	if err != nil {
		return Grant{}, ErrInvalid
	}
	cert := template(time.Now().UTC(), lifetime)
	cert.Subject = pkix.Name{CommonName: r.AgentID, Organization: []string{connection}, OrganizationalUnit: []string{a.id}}
	cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if cert.NotAfter.After(ca.NotAfter) {
		return Grant{}, ErrInvalid
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
	if err != nil {
		return Grant{}, ErrInvalid
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Grant{}, ErrInvalid
	}
	pin := peertransport.PublicKeyPin(leaf)
	return Grant{Version, a.id, r.AgentID, connection, hex.EncodeToString(pin[:]), 1, encodePEM("CERTIFICATE", der), leaf.NotAfter}, nil
}

func (a *Authority) ServerTLS(authorize func(peertransport.Pin) bool) (*tls.Config, error) {
	identity, err := tls.X509KeyPair(encodePEM("CERTIFICATE", a.target), encodePEM("PRIVATE KEY", a.targetKey))
	if err != nil {
		return nil, ErrInvalid
	}
	ca, _, err := a.Bundle().validate()
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return peertransport.ServerTLS(identity, roots, authorize)
}

// ClientTLS validates the trusted public bundle, grant, own key and target pin.
// It never accepts a private key in a grant or pairing bundle.
func (a *Agent) ClientTLS(b Bundle, g Grant, expectedBundle string) (*tls.Config, error) {
	if err := b.Verify(expectedBundle); err != nil {
		return nil, err
	}
	if err := g.validate(b); err != nil {
		return nil, err
	}
	if g.AgentID != a.id {
		return nil, ErrTrust
	}
	identity, err := tls.X509KeyPair(g.Certificate, encodePEM("PRIVATE KEY", a.key))
	if err != nil {
		return nil, ErrTrust
	}
	ca, target, err := b.validate()
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return peertransport.ClientTLS(identity, roots, b.ServerName, peertransport.PublicKeyPin(target))
}

func (b Bundle) validate() (*x509.Certificate, *x509.Certificate, error) {
	if b.Version != Version || !validID(b.DeviceID) || b.ServerName != b.DeviceID+".local-probe.invalid" {
		return nil, nil, ErrInvalid
	}
	ca, err := parseCert(b.CA)
	if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || ca.Subject.CommonName != b.DeviceID {
		return nil, nil, ErrInvalid
	}
	leaf, err := parseCert(b.Target)
	if err != nil || leaf.IsCA || leaf.Subject.CommonName != b.DeviceID {
		return nil, nil, ErrInvalid
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: b.ServerName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return nil, nil, ErrInvalid
	}
	return ca, leaf, nil
}

func (g Grant) validate(b Bundle) error {
	ca, _, err := b.validate()
	if err != nil {
		return err
	}
	if g.Version != Version || g.DeviceID != b.DeviceID || !validID(g.AgentID) || !validID(g.ConnectionID) || g.Epoch != 1 || !validHash(g.KeyPin) {
		return ErrInvalid
	}
	cert, err := parseCert(g.Certificate)
	if err != nil || cert.IsCA || cert.Subject.CommonName != g.AgentID || !cert.NotAfter.Equal(g.ExpiresAt) {
		return ErrInvalid
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != g.ConnectionID || len(cert.Subject.OrganizationalUnit) != 1 || cert.Subject.OrganizationalUnit[0] != g.DeviceID {
		return ErrTrust
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return ErrInvalid
	}
	pin := peertransport.PublicKeyPin(cert)
	if hex.EncodeToString(pin[:]) != g.KeyPin {
		return ErrTrust
	}
	return nil
}

func (r Request) validate() (*x509.CertificateRequest, error) {
	if r.Version != Version || !validID(r.AgentID) {
		return nil, ErrInvalid
	}
	der, err := decodePEM(r.CSR, "CERTIFICATE REQUEST")
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrInvalid
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrInvalid
	}
	return csr, nil
}

func template(now time.Time, lifetime time.Duration) *x509.Certificate {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 159))
	if err != nil {
		return &x509.Certificate{}
	}
	return &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime).Truncate(time.Second), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
}
func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func encodePEM(kind string, b []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b})
}
func decodePEM(data []byte, kind string) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxArtifactBytes {
		return nil, ErrInvalid
	}
	b, rest := pem.Decode(data)
	if b == nil || b.Type != kind || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalid
	}
	return b.Bytes, nil
}
func parseCert(b []byte) (*x509.Certificate, error) {
	der, err := decodePEM(b, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrInvalid
	}
	return cert, nil
}
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
func validHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

// DecodePublic parses a bounded public artifact; duplicate keys are rejected.
func DecodePublic(data []byte, out any) error {
	if len(data) == 0 || len(data) > MaxArtifactBytes {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := rejectDuplicates(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return err
	}
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
func rejectDuplicates(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return ErrInvalid
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if err := rejectDuplicates(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := rejectDuplicates(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token()
	if err != nil {
		return ErrInvalid
	}
	return nil
}
