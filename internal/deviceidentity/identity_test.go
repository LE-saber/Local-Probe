package deviceidentity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

type fixture struct {
	target, agent *Store
	authority     *Authority
	request       Request
	pairing       Pairing
	pin           peertransport.Pin
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	target, err := CreateStore(filepath.Join(base, "target"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	a, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = target.SaveAuthority(a); err != nil {
		t.Fatal(err)
	}
	agent, err := CreateStore(filepath.Join(base, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	client, r, err := NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.SaveAgent(client); err != nil {
		t.Fatal(err)
	}
	fp, err := r.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	g, err := target.IssueGrant(r, fp, "connection-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b := a.Bundle()
	bfp, err := b.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	p := Pairing{Version, b, g, bfp}
	if err = agent.InstallPairing(p, bfp); err != nil {
		t.Fatal(err)
	}
	var pin peertransport.Pin
	raw, _ := hex.DecodeString(g.KeyPin)
	copy(pin[:], raw)
	return fixture{target, agent, a, r, p, pin}
}

func TestPersistentPairingAuthenticatesAndRevokesActiveLease(t *testing.T) {
	f := newFixture(t)
	r, err := NewRegistry(f.target)
	if err != nil {
		t.Fatal(err)
	}
	scfg, err := r.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ccfg, err := f.agent.ClientTLS()
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tls.Server(a, scfg).HandshakeContext(ctx) }()
	if err = tls.Client(b, ccfg).HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	lease, release, err := r.Acquire(ctx, f.pin)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	g, ok := GrantFromContext(lease)
	if !ok || g.ConnectionID != "connection-a" || g.Epoch != 1 {
		t.Fatal("grant binding lost")
	}
	if err = r.Revoke(f.pin); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel lease")
	}
	if r.Allows(f.pin) {
		t.Fatal("revoked identity allowed")
	}
	if _, _, err = r.Acquire(ctx, f.pin); !errors.Is(err, ErrRevoked) {
		t.Fatalf("reacquire=%v", err)
	}
	// Reconstructing the registry simulates target restart; no cached grant may
	// resurrect a revoked identity, and issuing the same key again must fail.
	newRegistry, err := NewRegistry(f.target)
	if err != nil {
		t.Fatal(err)
	}
	if newRegistry.Allows(f.pin) {
		t.Fatal("revocation lost on restart")
	}
	fp, _ := f.request.Fingerprint()
	if _, err = f.target.IssueGrant(f.request, fp, "connection-b", time.Hour); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked key reissued: %v", err)
	}
}

func TestRevocationStorageFailureIsReportedAndBlocksRuntime(t *testing.T) {
	f := newFixture(t)
	registry, err := NewRegistry(f.target)
	if err != nil {
		t.Fatal(err)
	}
	path := f.target.root.Name()
	if err = f.target.Close(); err != nil {
		t.Fatal(err)
	}
	if err = registry.Revoke(f.pin); !errors.Is(err, ErrStorage) {
		t.Fatalf("revocation storage failure hidden: %v", err)
	}
	// Recover readable storage containing the old grant. This runtime must keep
	// the failed revocation blocked rather than silently resuming authorization.
	recovered, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	registry.mu.Lock()
	registry.store = recovered
	registry.mu.Unlock()
	if registry.Allows(f.pin) {
		t.Fatal("failed revocation silently reenabled")
	}
}

func TestPairingRejectsReplacedArtifactsAndMetadata(t *testing.T) {
	f := newFixture(t)
	agent, err := f.agent.LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agent.ClientTLS(other.Bundle(), f.pairing.Grant, f.pairing.Fingerprint); err == nil {
		t.Fatal("target replaced")
	}
	changed := f.pairing.Grant
	changed.ConnectionID = "connection-b"
	if _, err = agent.ClientTLS(f.pairing.Bundle, changed, f.pairing.Fingerprint); err == nil {
		t.Fatal("grant scope replaced")
	}
	_, request, err := NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := f.request.Fingerprint()
	if _, err = f.authority.Issue(request, fp, "connection-a", time.Hour); !errors.Is(err, ErrTrust) {
		t.Fatal("CSR replaced")
	}
	if _, err = f.authority.Issue(request, fp, "connection-a", 31*24*time.Hour); err == nil {
		t.Fatal("unbounded certificate lifetime")
	}
}

func TestPrivateStoreIsWriteOnceAndSecretsStayPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAuthority(a); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAuthority(a); !errors.Is(err, ErrExists) {
		t.Fatal("identity overwritten")
	}
	if _, err = CreateStore(path); !errors.Is(err, ErrExists) {
		t.Fatal("existing directory reused")
	}
	public, _ := json.Marshal(a)
	if bytes.Contains(public, a.targetKey) || bytes.Contains(public, a.caKey) {
		t.Fatal("JSON exposed private keys")
	}
	data, err := os.ReadFile(filepath.Join(path, "device.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && bytes.Contains(data, []byte("ca_key")) {
		t.Fatal("Windows identity was not encrypted")
	}
	if err = os.WriteFile(filepath.Join(path, "device.bin"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadAuthority(); err == nil {
		t.Fatal("corrupt identity silently accepted")
	}
}

func TestConcurrentIssueCannotReplaceExistingScope(t *testing.T) {
	f := newFixture(t)
	fp, _ := f.request.Fingerprint()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.target.IssueGrant(f.request, fp, "connection-b", time.Hour)
			if !errors.Is(err, ErrExists) {
				t.Errorf("duplicate issue: %v", err)
			}
		}()
	}
	wg.Wait()
	g, err := f.target.Resolve(f.pin)
	if err != nil || g.ConnectionID != "connection-a" {
		t.Fatal("scope overwritten")
	}
}

func TestPublicDecoderRejectsDuplicateUnknownAndOversizedInput(t *testing.T) {
	for _, data := range [][]byte{[]byte(`{"version":"x","version":"y"}`), []byte(`{"private_key":"x"}`), []byte(`{} {}`), bytes.Repeat([]byte("x"), MaxArtifactBytes+1)} {
		var bundle Bundle
		if DecodePublic(data, &bundle) == nil {
			t.Fatal("invalid public input accepted")
		}
	}
}

func FuzzPublicPairingDecoder(f *testing.F) {
	for _, seed := range []string{`{}`, `{"version":"x","version":"y"}`, `{"ca_pem":"eA=="}`, `{"bundle":[[[[]]]]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var b Bundle
		_ = DecodePublic(data, &b)
		var r Request
		_ = DecodePublic(data, &r)
		var p Pairing
		_ = DecodePublic(data, &p)
	})
}
