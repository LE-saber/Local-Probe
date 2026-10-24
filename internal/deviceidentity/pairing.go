package deviceidentity

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
)

type Pairing struct {
	Version     string `json:"version"`
	Bundle      Bundle `json:"bundle"`
	Grant       Grant  `json:"grant"`
	Fingerprint string `json:"fingerprint"`
}

func (a *Agent) Request() (Request, error) {
	key, err := agentKey(a.key)
	if err != nil {
		return Request{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: a.id}}, key)
	if err != nil {
		return Request{}, ErrInvalid
	}
	return Request{Version, a.id, encodePEM("CERTIFICATE REQUEST", csr)}, nil
}

// InstallPairing is explicitly local and write-once. Expected must be the target
// bundle fingerprint the user independently reviewed, not the response's value.
func (s *Store) InstallPairing(p Pairing, expected string) error {
	if p.Version != Version || p.Fingerprint != expected {
		return ErrTrust
	}
	a, err := s.LoadAgent()
	if err != nil {
		return err
	}
	if _, err = a.ClientTLS(p.Bundle, p.Grant, expected); err != nil {
		return err
	}
	return s.write("pairing.bin", p)
}
func (s *Store) LoadPairing() (Pairing, error) {
	var p Pairing
	if err := s.read("pairing.bin", &p); err != nil {
		return Pairing{}, err
	}
	if p.Version != Version {
		return Pairing{}, ErrInvalid
	}
	a, err := s.LoadAgent()
	if err != nil {
		return Pairing{}, err
	}
	if _, err = a.ClientTLS(p.Bundle, p.Grant, p.Fingerprint); err != nil {
		return Pairing{}, err
	}
	return p, nil
}
func (s *Store) ClientTLS() (*tls.Config, error) {
	p, err := s.LoadPairing()
	if err != nil {
		return nil, err
	}
	a, err := s.LoadAgent()
	if err != nil {
		return nil, err
	}
	return a.ClientTLS(p.Bundle, p.Grant, p.Fingerprint)
}
