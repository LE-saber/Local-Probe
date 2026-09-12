// Package confirmation implements short-lived, one-time local confirmation
// capabilities.  A capability is bound to the exact connection, profile
// revision, command, variant and request nonce that a trusted local caller
// supplied when minting it.  It is not a user-controlled boolean.
package confirmation

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	tokenVersion  = byte(2)
	macBytes      = sha256.Size
	maxFieldBytes = 256
	maxTokenBytes = 4096
	maxTTL        = 5 * time.Minute
)

var (
	ErrInvalid         = errors.New("invalid confirmation capability")
	ErrExpired         = errors.New("confirmation capability expired")
	ErrConsumed        = errors.New("confirmation capability already consumed")
	ErrRequestMismatch = errors.New("confirmation request mismatch")
	ErrWeakSecret      = errors.New("confirmation secret is too short")
	ErrCapacity        = errors.New("confirmation replay capacity exhausted")
)

// Request identifies exactly one local action authorization.  All fields are
// required and are compared during Consume; a model cannot replace one of
// them with a different command, profile, resolved input or request nonce.
type Request struct {
	ConnectionID        string
	ProfileID           string
	ProfileRevision     string
	CommandID           string
	VariantID           string
	RequestNonce        string
	ResolvedInputDigest string
}

// Capability is opaque.  Call String only when a trusted local API must pass
// the capability across a process boundary; it cannot be JSON-marshaled as a
// struct and therefore cannot be silently embedded as a confirmation bool.
type Capability struct{ raw []byte }

func (c Capability) String() string {
	if len(c.raw) == 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(c.raw)
}

func (c Capability) MarshalJSON() ([]byte, error) {
	return nil, errors.New("confirmation capability must be passed as an opaque token")
}

func (*Capability) UnmarshalJSON([]byte) error {
	return errors.New("confirmation capability must be parsed by the local manager")
}

// Manager is the trusted local mint/consume authority.  It owns the HMAC key
// and the one-time replay set; no configuration field can create a Manager.
type Manager struct {
	key      [sha256.Size]byte
	now      func() time.Time
	mu       sync.Mutex
	consumed map[[sha256.Size]byte]int64
}

func New(secret []byte) (*Manager, error) {
	return NewWithClock(secret, time.Now)
}

func NewWithClock(secret []byte, now func() time.Time) (*Manager, error) {
	if len(secret) < sha256.Size {
		return nil, ErrWeakSecret
	}
	if now == nil {
		return nil, fmt.Errorf("%w: missing clock", ErrInvalid)
	}
	manager := &Manager{now: now, consumed: make(map[[sha256.Size]byte]int64)}
	manager.key = sha256.Sum256(secret)
	return manager, nil
}

// Mint is callable only by trusted local UI/CLI code.  ttl is deliberately
// bounded so a confirmation cannot become a durable permission.
func (m *Manager) Mint(request Request, ttl time.Duration) (Capability, error) {
	if m == nil || m.now == nil {
		return Capability{}, fmt.Errorf("%w: uninitialized manager", ErrInvalid)
	}
	if err := validateRequest(request); err != nil {
		return Capability{}, err
	}
	if ttl <= 0 || ttl > maxTTL {
		return Capability{}, fmt.Errorf("%w: invalid lifetime", ErrInvalid)
	}
	expiresAt := m.now().Add(ttl)
	if expiresAt.UnixNano() <= 0 {
		return Capability{}, fmt.Errorf("%w: invalid expiry", ErrInvalid)
	}
	payload, err := encodePayload(request, expiresAt.UnixNano())
	if err != nil {
		return Capability{}, err
	}
	return Capability{raw: appendMAC(payload, m.key[:])}, nil
}

// Parse validates only the token's bounded wire shape.  HMAC and request
// binding are checked by Consume, which is the only operation that authorizes
// an action.
func (m *Manager) Parse(encoded string) (Capability, error) {
	if m == nil || m.now == nil || encoded == "" || len(encoded) > maxTokenBytes {
		return Capability{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 1+8+macBytes || len(raw) > maxTokenBytes {
		return Capability{}, ErrInvalid
	}
	return Capability{raw: append([]byte(nil), raw...)}, nil
}

// Consume verifies the HMAC, expiry and exact request binding, then marks the
// token used.  The mark occurs while holding the manager lock, making a pair
// of concurrent consumes deterministic: exactly one succeeds.
func (m *Manager) Consume(capability Capability, request Request) error {
	if m == nil || m.now == nil {
		return ErrInvalid
	}
	if err := validateRequest(request); err != nil {
		return err
	}
	payload, tokenRequest, expiresAt, mac, err := decode(capability.raw)
	if err != nil {
		return err
	}
	expected := hmac.New(sha256.New, m.key[:])
	_, _ = expected.Write(payload)
	if subtle.ConstantTimeCompare(mac, expected.Sum(nil)) != 1 {
		return ErrInvalid
	}
	now := m.now()
	if expiresAt <= now.UnixNano() {
		return ErrExpired
	}
	if tokenRequest.ConnectionID != request.ConnectionID ||
		tokenRequest.ProfileID != request.ProfileID ||
		tokenRequest.ProfileRevision != request.ProfileRevision ||
		tokenRequest.CommandID != request.CommandID ||
		tokenRequest.VariantID != request.VariantID ||
		tokenRequest.RequestNonce != request.RequestNonce ||
		subtle.ConstantTimeCompare([]byte(tokenRequest.ResolvedInputDigest), []byte(request.ResolvedInputDigest)) != 1 {
		return ErrRequestMismatch
	}
	digest := sha256.Sum256(capability.raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.consumed[digest]; exists {
		return ErrConsumed
	}
	// Expiry is stored alongside every consumed digest. Only expired records
	// may be removed; evicting a live record would make that capability
	// replayable. If live capacity is exhausted, fail closed.
	nowUnixNano := now.UnixNano()
	for old, oldExpiry := range m.consumed {
		if oldExpiry <= nowUnixNano {
			delete(m.consumed, old)
		}
	}
	if len(m.consumed) >= 4096 {
		return ErrCapacity
	}
	m.consumed[digest] = expiresAt
	return nil
}

func validateRequest(request Request) error {
	values := []struct {
		field string
		value string
	}{
		{"connection_id", request.ConnectionID},
		{"profile_id", request.ProfileID},
		{"profile_revision", request.ProfileRevision},
		{"command_id", request.CommandID},
		{"variant_id", request.VariantID},
		{"request_nonce", request.RequestNonce},
		{"resolved_input_digest", request.ResolvedInputDigest},
	}
	for _, value := range values {
		if value.value == "" || len(value.value) > maxFieldBytes || !utf8.ValidString(value.value) || strings.ContainsRune(value.value, 0) {
			return fmt.Errorf("%w: invalid %s", ErrInvalid, value.field)
		}
	}
	if len(request.ResolvedInputDigest) != sha256.Size*2 || request.ResolvedInputDigest != strings.ToLower(request.ResolvedInputDigest) {
		return fmt.Errorf("%w: invalid resolved_input_digest", ErrInvalid)
	}
	if _, err := hex.DecodeString(request.ResolvedInputDigest); err != nil {
		return fmt.Errorf("%w: invalid resolved_input_digest", ErrInvalid)
	}
	return nil
}

func encodePayload(request Request, expiresAt int64) ([]byte, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	payload := make([]byte, 0, 1+8+7*(2+maxFieldBytes))
	payload = append(payload, tokenVersion)
	var expiry [8]byte
	binary.BigEndian.PutUint64(expiry[:], uint64(expiresAt))
	payload = append(payload, expiry[:]...)
	for _, field := range []string{request.ConnectionID, request.ProfileID, request.ProfileRevision, request.CommandID, request.VariantID, request.RequestNonce, request.ResolvedInputDigest} {
		if len(field) > 0xffff {
			return nil, ErrInvalid
		}
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(field)))
		payload = append(payload, length[:]...)
		payload = append(payload, field...)
	}
	if len(payload)+macBytes > maxTokenBytes {
		return nil, ErrInvalid
	}
	return payload, nil
}

func appendMAC(payload, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return append(append([]byte(nil), payload...), mac.Sum(nil)...)
}

func decode(raw []byte) (payload []byte, request Request, expiresAt int64, mac []byte, err error) {
	if len(raw) < 1+8+macBytes || len(raw) > maxTokenBytes {
		return nil, Request{}, 0, nil, ErrInvalid
	}
	payload = raw[:len(raw)-macBytes]
	mac = raw[len(raw)-macBytes:]
	if payload[0] != tokenVersion {
		return nil, Request{}, 0, nil, ErrInvalid
	}
	expiresAt = int64(binary.BigEndian.Uint64(payload[1:9]))
	position := 9
	fields := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		if position+2 > len(payload) {
			return nil, Request{}, 0, nil, ErrInvalid
		}
		length := int(binary.BigEndian.Uint16(payload[position : position+2]))
		position += 2
		if length == 0 || length > maxFieldBytes || position+length > len(payload) {
			return nil, Request{}, 0, nil, ErrInvalid
		}
		value := payload[position : position+length]
		if !utf8.Valid(value) || strings.ContainsRune(string(value), 0) {
			return nil, Request{}, 0, nil, ErrInvalid
		}
		fields = append(fields, string(value))
		position += length
	}
	if position != len(payload) {
		return nil, Request{}, 0, nil, ErrInvalid
	}
	request = Request{
		ConnectionID: fields[0], ProfileID: fields[1], ProfileRevision: fields[2],
		CommandID: fields[3], VariantID: fields[4], RequestNonce: fields[5],
		ResolvedInputDigest: fields[6],
	}
	return payload, request, expiresAt, append([]byte(nil), mac...), nil
}

// Ensure the opaque token cannot accidentally gain a default JSON encoding if
// its representation changes later.
var _ json.Marshaler = Capability{}
