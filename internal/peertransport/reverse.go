package peertransport

import (
	"context"
	"crypto/x509"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ServeRelay maintains four outbound rendezvous streams for this target. The
// Relay's route/outer credentials are independent of inner peer authorization.
// Reconnecting restores transport only; it never replays a tool call or job.
func (e *Endpoint) ServeRelay(ctx context.Context, endpoint string, headers http.Header, outerRoots *x509.CertPool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "wss" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("explicit WSS Relay target endpoint required")
	}
	headers = headers.Clone()
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			delay := time.Second
			for ctx.Err() == nil {
				raw, err := dialStream(ctx, endpoint, headers, outerRoots, true)
				if err == nil {
					done := make(chan struct{})
					tracked := &trackedConn{Conn: raw, onClose: func() { close(done) }}
					if err = e.AcceptStream(ctx, tracked); err == nil {
						delay = time.Second
						select {
						case <-done:
						case <-ctx.Done():
							_ = tracked.Close()
						}
					}
				}
				if ctx.Err() != nil {
					return
				}
				timer := time.NewTimer(delay + time.Duration(rand.Int64N(int64(delay/2)+1)))
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				delay = min(delay*2, 30*time.Second)
			}
		}()
	}
	workers.Wait()
	return nil
}
