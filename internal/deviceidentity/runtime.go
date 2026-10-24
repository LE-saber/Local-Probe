package deviceidentity

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"

	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

func (s *Store) LoadBundle() (Bundle, error) {
	var b Bundle
	if err := s.read("device-public.bin", &b); err != nil {
		return Bundle{}, err
	}
	if _, _, err := b.validate(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// ServerTLS loads the runtime leaf key, never the owner's CA signing key.
func (s *Store) ServerTLS(authorize func(peertransport.Pin) bool) (*tls.Config, error) {
	b, err := s.LoadBundle()
	if err != nil {
		return nil, err
	}
	var r secretRecord
	if err = s.read("device-runtime.bin", &r); err != nil {
		return nil, err
	}
	if r.Version != Version || r.ID != b.DeviceID || len(r.CAKey) != 0 || len(r.CA) != 0 {
		return nil, ErrInvalid
	}
	ca, leaf, err := b.validate()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(leaf.Raw, r.Target) {
		return nil, ErrTrust
	}
	identity, err := tls.X509KeyPair(encodePEM("CERTIFICATE", r.Target), encodePEM("PRIVATE KEY", r.Key))
	if err != nil {
		return nil, ErrTrust
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return peertransport.ServerTLS(identity, roots, authorize)
}
