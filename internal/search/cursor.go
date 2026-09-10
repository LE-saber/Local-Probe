package search

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

const cursorVersion = 1

type Service struct {
	binder Binder
	limits Limits
	key    []byte
	clock  func() time.Time
}

// New creates a search service. The signing key is process-local by default;
// restarting the service therefore invalidates all cursors rather than
// risking reuse against a different root or configuration.
func New(binder Binder, limits Limits, cursorKey []byte) (*Service, error) {
	if binder == nil {
		return nil, fmt.Errorf("%w: source binder is required", ErrInvalidRequest)
	}
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	key := append([]byte(nil), cursorKey...)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("%w: cursor key unavailable", ErrUnavailable)
		}
	}
	if len(key) < 16 || len(key) > 4096 {
		return nil, fmt.Errorf("%w: cursor key length is out of bounds", ErrInvalidRequest)
	}
	return &Service{binder: binder, limits: limits, key: key, clock: time.Now}, nil
}

type cursorFrame struct {
	Path       string `json:"path"`
	Offset     int    `json:"offset"`
	Generation string `json:"generation"`
}

type scanCursorState struct {
	Path          string `json:"path"`
	Version       string `json:"version"`
	Offset        int64  `json:"offset"`
	Line          int    `json:"line"`
	LineStartByte int64  `json:"line_start_byte"`
	LineBytes     int    `json:"line_bytes"`
	MatchLen      int    `json:"match_len"`
	LastMatchByte int64  `json:"last_match_byte"`
	UTF8Carry     []byte `json:"utf8_carry,omitempty"`
}

type cursorPayload struct {
	Version       int              `json:"version"`
	Operation     string           `json:"operation"`
	ConnectionID  string           `json:"connection_id"`
	ProfileID     string           `json:"profile_id"`
	Revision      string           `json:"revision"`
	RootID        string           `json:"root_id"`
	StartPath     string           `json:"start_path"`
	Pattern       string           `json:"pattern,omitempty"`
	Query         string           `json:"query,omitempty"`
	Globs         []string         `json:"globs,omitempty"`
	CaseSensitive bool             `json:"case_sensitive,omitempty"`
	PageSize      int              `json:"page_size"`
	MaxDepth      int              `json:"max_depth,omitempty"`
	MaxEntries    int              `json:"max_entries"`
	MaxReadBytes  int              `json:"max_read_bytes,omitempty"`
	ContextBytes  int              `json:"context_bytes,omitempty"`
	ExpiresAt     int64            `json:"expires_at"`
	Frames        []cursorFrame    `json:"frames"`
	Pending       *scanCursorState `json:"pending,omitempty"`
}

func (s *Service) cursor(payload cursorPayload) (string, error) {
	payload.Version = cursorVersion
	if payload.ExpiresAt == 0 {
		payload.ExpiresAt = s.clock().Add(15 * time.Minute).Unix()
	}
	if len(payload.Frames) == 0 || len(payload.Frames) > s.limits.MaxDepth+1 {
		return "", ErrInvalidCursor
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > s.limits.MaxCursorBytes {
		return "", ErrInvalidCursor
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("v1."))
	_, _ = mac.Write([]byte(payloadPart))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	out := "v1." + payloadPart + "." + signature
	if len(out) > s.limits.MaxCursorBytes {
		return "", ErrInvalidCursor
	}
	return out, nil
}

func (s *Service) decodeCursor(value string) (cursorPayload, error) {
	if value == "" || len(value) > s.limits.MaxCursorBytes {
		return cursorPayload{}, ErrInvalidCursor
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" {
		return cursorPayload{}, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(data) > s.limits.MaxCursorBytes {
		return cursorPayload{}, ErrInvalidCursor
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("v1."))
	_, _ = mac.Write([]byte(parts[1]))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return cursorPayload{}, ErrInvalidCursor
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cursorPayload{}, ErrInvalidCursor
	}
	if payload.Version != cursorVersion || payload.ExpiresAt <= s.clock().Unix() || payload.Operation == "" || !utf8.ValidString(payload.ConnectionID) || !utf8.ValidString(payload.ProfileID) || !utf8.ValidString(payload.Revision) || !utf8.ValidString(payload.RootID) || !utf8.ValidString(payload.StartPath) {
		return cursorPayload{}, ErrInvalidCursor
	}
	if len(payload.Frames) == 0 || len(payload.Frames) > s.limits.MaxDepth+1 {
		return cursorPayload{}, ErrInvalidCursor
	}
	for _, frame := range payload.Frames {
		if frame.Offset < 0 || (frame.Path != "" && !validRelativePath(frame.Path)) || len(frame.Generation) > 512 || !utf8.ValidString(frame.Generation) {
			return cursorPayload{}, ErrInvalidCursor
		}
	}
	if payload.Pending != nil {
		pending := payload.Pending
		if pending.Path == "" || !validRelativePath(pending.Path) || pending.Offset < 0 || pending.Line < 1 || pending.LineStartByte < 0 || pending.LineBytes < 0 || pending.MatchLen < 0 || pending.LastMatchByte < -1 || len(pending.Version) == 0 || len(pending.Version) > 256 || !utf8.ValidString(pending.Version) || len(pending.UTF8Carry) > 4 {
			return cursorPayload{}, ErrInvalidCursor
		}
	}
	return payload, nil
}

func validRelativePath(value string) bool {
	return value == "" || readcore.ValidPath(value)
}

func cursorMatchesScope(payload cursorPayload, connectionID, profileID, revision, rootID, startPath string) bool {
	return payload.ConnectionID == connectionID && payload.ProfileID == profileID && payload.Revision == revision && payload.RootID == rootID && payload.StartPath == startPath
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
