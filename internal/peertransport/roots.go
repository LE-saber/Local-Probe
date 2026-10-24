package peertransport

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// LoadPublicRoots loads an explicitly trusted private edge/Relay CA. Empty path
// uses OS trust. Only public CA certificate PEM is allowed, never private keys.
func LoadPublicRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute public CA path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("public CA unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("invalid public CA")
	}
	roots := x509.NewCertPool()
	count := 0
	for len(bytes.TrimSpace(data)) > 0 {
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("only public CA certificate PEM allowed")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return nil, errors.New("invalid public CA")
		}
		roots.AddCert(cert)
		count++
		data = rest
	}
	if count == 0 {
		return nil, errors.New("public CA required")
	}
	return roots, nil
}
