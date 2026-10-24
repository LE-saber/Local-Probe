package deviceidentity

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"

	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

// Registry is owned by one running target process. Offline CLI writes must not
// race it. Revoke persists a tombstone before cancelling all active leases.
type Registry struct {
	mu      sync.Mutex
	store   *Store
	next    uint64
	active  map[peertransport.Pin]map[uint64]context.CancelFunc
	blocked map[peertransport.Pin]bool
}
type grantKey struct{}

func NewRegistry(store *Store) (*Registry, error) {
	if store == nil {
		return nil, ErrStorage
	}
	if _, err := store.LoadBundle(); err != nil {
		return nil, err
	}
	return &Registry{store: store, active: make(map[peertransport.Pin]map[uint64]context.CancelFunc), blocked: make(map[peertransport.Pin]bool)}, nil
}
func (r *Registry) Allows(pin peertransport.Pin) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked[pin] {
		return false
	}
	_, err := r.store.Resolve(pin)
	return err == nil
}
func (r *Registry) Acquire(ctx context.Context, pin peertransport.Pin) (context.Context, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked[pin] {
		return nil, nil, ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	g, err := r.store.Resolve(pin)
	if err != nil {
		return nil, nil, err
	}
	lease, cancel := context.WithDeadline(context.WithValue(ctx, grantKey{}, g), g.ExpiresAt)
	r.next++
	id := r.next
	if r.active[pin] == nil {
		r.active[pin] = make(map[uint64]context.CancelFunc)
	}
	r.active[pin][id] = cancel
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			cancel()
			delete(r.active[pin], id)
			if len(r.active[pin]) == 0 {
				delete(r.active, pin)
			}
		})
	}
	return lease, release, nil
}
func (r *Registry) Revoke(pin peertransport.Pin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.store.Revoke(pin)
	r.blocked[pin] = true
	// Failed persistence is also fail-closed for current work. Future accesses
	// check the storage state and reject any partial tombstone.
	for _, cancel := range r.active[pin] {
		cancel()
	}
	delete(r.active, pin)
	return err
}
func GrantFromContext(ctx context.Context) (Grant, bool) {
	g, ok := ctx.Value(grantKey{}).(Grant)
	g.Certificate = append([]byte(nil), g.Certificate...)
	return g, ok
}

func (r *Registry) ServerTLS() (*tls.Config, error) {
	return r.store.ServerTLS(r.Allows)
}

// Handler attaches a verified local Grant and a cancellable lease. Tools must
// still resolve ConnectionID through the authoritative policy manager. No caller
// header, argument or CSR can choose a different connection/profile.
func (r *Registry) Handler(next http.Handler) http.Handler {
	return peertransport.AuthorizeHTTP(r.Allows, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if next == nil {
			http.Error(w, "handler unavailable", http.StatusServiceUnavailable)
			return
		}
		pin, ok := peertransport.Identity(req.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, release, err := r.Acquire(req.Context(), pin)
		if err != nil {
			http.Error(w, "revoked", http.StatusForbidden)
			return
		}
		defer release()
		next.ServeHTTP(w, req.WithContext(ctx))
	}))
}
