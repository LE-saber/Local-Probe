package deviceidentity

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/LE-saber/Local-Probe/internal/peertransport"
)

// Store is a private directory of write-once protected records. Existing files
// are never replaced: a crash during write fails closed on the next read. It
// must not be restored from an old backup to resurrect revoked credentials.
// CLI mutations are offline; a running service owns its Registry mutations.
type Store struct {
	root *os.Root
	mu   sync.Mutex
}
type secretRecord struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	CA      []byte `json:"ca,omitempty"`
	CAKey   []byte `json:"ca_key,omitempty"`
	Target  []byte `json:"target,omitempty"`
	Key     []byte `json:"key"`
}

// CreateStore requires a NEW explicitly selected directory in an existing
// parent. It never regenerates identity after a failed or partial initialization.
func CreateStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrStorage
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrExists
		}
		return nil, ErrStorage
	}
	if err := protectDirectory(path); err != nil {
		return nil, ErrStorage
	}
	return OpenStore(path)
}

func OpenStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrStorage
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorage
	}
	if err := checkDirectory(info); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrStorage
	}
	return &Store{root: root}, nil
}
func (s *Store) Close() error { return s.root.Close() }

func (s *Store) SaveAuthority(a *Authority) error {
	if a == nil {
		return ErrInvalid
	}
	if _, err := a.ServerTLS(func(peertransport.Pin) bool { return false }); err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(encodePEM("CERTIFICATE", a.ca), encodePEM("PRIVATE KEY", a.caKey)); err != nil {
		return ErrInvalid
	}
	if err := s.write("device.bin", secretRecord{Version, a.id, a.ca, a.caKey, a.target, a.targetKey}); err != nil {
		return err
	}
	if err := s.write("device-public.bin", a.Bundle()); err != nil {
		return err
	}
	// Runtime operations never need to load the CA signing private key.
	return s.write("device-runtime.bin", secretRecord{Version: Version, ID: a.id, Target: a.target, Key: a.targetKey})
}
func (s *Store) LoadAuthority() (*Authority, error) {
	var r secretRecord
	if err := s.read("device.bin", &r); err != nil {
		return nil, err
	}
	if r.Version != Version || !validID(r.ID) {
		return nil, ErrInvalid
	}
	a := &Authority{r.ID, r.CA, r.CAKey, r.Target, r.Key}
	if _, err := tls.X509KeyPair(encodePEM("CERTIFICATE", a.ca), encodePEM("PRIVATE KEY", a.caKey)); err != nil {
		return nil, ErrInvalid
	}
	if _, err := a.ServerTLS(func(peertransport.Pin) bool { return false }); err != nil {
		return nil, err
	}
	return a, nil
}
func (s *Store) SaveAgent(a *Agent) error {
	if a == nil || !validID(a.id) {
		return ErrInvalid
	}
	if _, err := agentKey(a.key); err != nil {
		return err
	}
	return s.write("agent.bin", secretRecord{Version: Version, ID: a.id, Key: a.key})
}
func (s *Store) LoadAgent() (*Agent, error) {
	var r secretRecord
	if err := s.read("agent.bin", &r); err != nil {
		return nil, err
	}
	if r.Version != Version || !validID(r.ID) || len(r.CA) != 0 || len(r.CAKey) != 0 || len(r.Target) != 0 {
		return nil, ErrInvalid
	}
	if _, err := agentKey(r.Key); err != nil {
		return nil, err
	}
	return &Agent{r.ID, r.Key}, nil
}
func agentKey(b []byte) (*ecdsa.PrivateKey, error) {
	v, err := x509.ParsePKCS8PrivateKey(b)
	if err != nil {
		return nil, ErrInvalid
	}
	key, ok := v.(*ecdsa.PrivateKey)
	if !ok {
		return nil, ErrInvalid
	}
	return key, nil
}

// IssueGrant is local owner approval. Expected is the CSR fingerprint the owner
// reviewed, not a value supplied by the remote operation caller.
func (s *Store) IssueGrant(r Request, expected, connection string, lifetime time.Duration) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.LoadAuthority()
	if err != nil {
		return Grant{}, err
	}
	g, err := a.Issue(r, expected, connection, lifetime)
	if err != nil {
		return Grant{}, err
	}
	if s.exists("revoke-" + g.KeyPin + ".bin") {
		return Grant{}, ErrRevoked
	}
	if err := s.write("grant-"+g.KeyPin+".bin", g); err != nil {
		return Grant{}, err
	}
	return g, nil
}

func (s *Store) Resolve(pin peertransport.Pin) (Grant, error) {
	key := hex.EncodeToString(pin[:])
	if s.exists("revoke-" + key + ".bin") {
		return Grant{}, ErrRevoked
	}
	b, err := s.LoadBundle()
	if err != nil {
		return Grant{}, err
	}
	var g Grant
	if err := s.read("grant-"+key+".bin", &g); err != nil {
		return Grant{}, err
	}
	if err := g.validate(b); err != nil {
		return Grant{}, err
	}
	if g.KeyPin != key {
		return Grant{}, ErrTrust
	}
	if s.exists("revoke-" + key + ".bin") {
		return Grant{}, ErrRevoked
	}
	return g, nil
}

// Revoke is durable and terminal for this key. Even a malformed revocation
// record denies access: it is never ignored to make a connection work.
func (s *Store) Revoke(pin peertransport.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hex.EncodeToString(pin[:])
	name := "revoke-" + key + ".bin"
	_, inspectErr := s.root.Lstat(name)
	if inspectErr == nil {
		return nil
	}
	if !errors.Is(inspectErr, fs.ErrNotExist) {
		return ErrStorage
	}
	return s.write(name, struct {
		Version string `json:"version"`
		Key     string `json:"key"`
		Epoch   uint64 `json:"epoch"`
	}{Version, key, 2})
}
func (s *Store) exists(name string) bool {
	_, err := s.root.Lstat(name)
	return !errors.Is(err, fs.ErrNotExist)
}
func (s *Store) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxArtifactBytes {
		return ErrInvalid
	}
	defer clear(data)
	sealed, err := seal(data)
	if err != nil {
		return ErrStorage
	}
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return ErrStorage
	}
	// Keep a failed/partial file as a fail-closed record; never silently retry
	// identity generation or remove a revocation tombstone on error.
	_, writeErr := file.Write(sealed)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrStorage
	}
	return nil
}
func (s *Store) read(name string, out any) error {
	info, err := s.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2*MaxArtifactBytes {
		return ErrStorage
	}
	if err := checkRecord(info); err != nil {
		return err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return ErrStorage
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 2*MaxArtifactBytes+1))
	if err != nil || len(data) > 2*MaxArtifactBytes {
		return ErrStorage
	}
	plain, err := unseal(data)
	if err != nil {
		return ErrStorage
	}
	defer clear(plain)
	return DecodePublic(plain, out)
}
