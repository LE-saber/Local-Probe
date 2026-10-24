package peertransport

import (
	"context"
	"crypto/tls"
	"net/http"
)

type identityKey struct{}

// Identity returns the peer authenticated by inner mTLS, never a JSON/header ID.
func Identity(ctx context.Context) (Pin, bool) {
	pin, ok := ctx.Value(identityKey{}).(Pin)
	return pin, ok
}

// AuthorizeHTTP rechecks revocation for every request, including requests on a
// previously authenticated keep-alive connection. The caller must additionally
// bind this identity to a connection scope before calling any tool handler.
// Passing nil dependencies is denied rather than defaulting to public access.
func AuthorizeHTTP(authorize func(Pin) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorize == nil || next == nil || r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "peer authentication required", http.StatusUnauthorized)
			return
		}
		pin := PublicKeyPin(r.TLS.PeerCertificates[0])
		if !authorize(pin) {
			http.Error(w, "peer authorization revoked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, pin)))
	})
}
