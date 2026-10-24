// Package peertransport implements the encrypted endpoint stream. It does not
// grant filesystem or execution access. Callers must authorize every request.
package peertransport

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
)

var ErrIdentity = errors.New("peer identity rejected")

// Pin is the SHA-256 digest of a paired leaf certificate's public key.
// Pairing must deliver this value over an independently trusted channel.
type Pin [sha256.Size]byte

func PublicKeyPin(cert *x509.Certificate) Pin {
	return sha256.Sum256(cert.RawSubjectPublicKeyInfo)
}

// ServerTLS requires a trusted Agent certificate AND an online authorization
// decision. authorize must be concurrency safe and must not trust client names.
func ServerTLS(identity tls.Certificate, roots *x509.CertPool, authorize func(Pin) bool) (*tls.Config, error) {
	if len(identity.Certificate) == 0 || identity.PrivateKey == nil || roots == nil || authorize == nil {
		return nil, ErrIdentity
	}
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{identity},
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              roots.Clone(),
		SessionTicketsDisabled: true,
		NextProtos:             []string{"http/1.1"},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !authorize(PublicKeyPin(state.PeerCertificates[0])) {
				return ErrIdentity
			}
			return nil
		},
	}, nil
}

// ClientTLS retains normal chain and hostname verification before checking the
// paired target key. A relay-issued certificate alone cannot impersonate it.
func ClientTLS(identity tls.Certificate, roots *x509.CertPool, serverName string, target Pin) (*tls.Config, error) {
	if len(identity.Certificate) == 0 || identity.PrivateKey == nil || roots == nil || serverName == "" || target == (Pin{}) {
		return nil, ErrIdentity
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{identity},
		RootCAs:      roots.Clone(),
		ServerName:   serverName,
		NextProtos:   []string{"http/1.1"},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || PublicKeyPin(state.PeerCertificates[0]) != target {
				return ErrIdentity
			}
			return nil
		},
	}, nil
}
