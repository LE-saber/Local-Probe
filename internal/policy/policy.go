// Package policy binds authenticated connections to immutable, read-only
// configuration snapshots. It does not open files or authenticate transport.
package policy

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

var (
	ErrDenied  = errors.New("policy denied")
	ErrRevoked = errors.New("policy scope revoked")
)

// Manager creates bound scopes from the current configuration snapshot. The
// connection ID passed here must come from a trusted authenticated ingress;
// model-supplied profile or connection arguments are intentionally absent.
type Manager struct {
	store *config.Store
}

func NewManager(store *config.Store) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: configuration store required", ErrDenied)
	}
	return &Manager{store: store}, nil
}

// BoundScope is an immutable authorization decision tied to one configuration
// revision. Call Scope before invoking readcore and handle its validation error.
type BoundScope struct {
	manager      *Manager
	scope        readcore.Scope
	connectionID string
	profileID    string
	revision     string
	tools        map[string]struct{}
	roots        map[string]rootPolicy
}

type rootPolicy struct {
	denyPatterns []string
}

// BindAuthenticated binds one enabled connection to its configured profile.
// There is no parameter for selecting a different profile or root set.
func (m *Manager) BindAuthenticated(connectionID string) (BoundScope, error) {
	if m == nil || m.store == nil {
		return BoundScope{}, fmt.Errorf("%w: manager unavailable", ErrDenied)
	}
	snapshot := m.store.Snapshot()
	cfg := snapshot.Config()
	connection, ok := cfg.Connection(connectionID)
	if !ok || !connection.Enabled() {
		return BoundScope{}, fmt.Errorf("%w: connection unavailable", ErrDenied)
	}
	profile, ok := cfg.Profile(connection.ProfileID())
	if !ok || !profile.ReadOnly() {
		return BoundScope{}, fmt.Errorf("%w: profile unavailable", ErrDenied)
	}
	scope, err := readcore.NewScope(connection.ID(), snapshot.Revision(), profile.RootIDs())
	if err != nil {
		return BoundScope{}, fmt.Errorf("%w: invalid bound scope", ErrDenied)
	}
	tools := make(map[string]struct{}, len(profile.Tools()))
	for _, tool := range profile.Tools() {
		tools[tool] = struct{}{}
	}
	roots := make(map[string]rootPolicy, len(profile.RootIDs()))
	profileDeny := profile.DenyPatterns()
	for _, rootID := range profile.RootIDs() {
		root, ok := cfg.Root(rootID)
		if !ok {
			return BoundScope{}, fmt.Errorf("%w: root unavailable", ErrDenied)
		}
		patterns := append([]string(nil), profileDeny...)
		patterns = append(patterns, root.DenyPatterns()...)
		roots[rootID] = rootPolicy{denyPatterns: patterns}
	}
	return BoundScope{
		manager:      m,
		scope:        scope,
		connectionID: connection.ID(),
		profileID:    profile.ID(),
		revision:     snapshot.Revision(),
		tools:        tools,
		roots:        roots,
	}, nil
}

// Validate re-checks the connection and configuration revision. Any replace,
// disable, or profile reassignment invalidates previously bound scopes.
func (m *Manager) Validate(bound BoundScope) error {
	if m == nil || bound.manager != m || bound.manager == nil || bound.revision == "" {
		return ErrRevoked
	}
	snapshot := m.store.Snapshot()
	if snapshot.Revision() != bound.revision {
		return ErrRevoked
	}
	cfg := snapshot.Config()
	connection, ok := cfg.Connection(bound.connectionID)
	if !ok || !connection.Enabled() || connection.ProfileID() != bound.profileID {
		return ErrRevoked
	}
	if bound.scope.ConnectionID() != bound.connectionID || bound.scope.ProfileRevision() != bound.revision {
		return ErrRevoked
	}
	return nil
}

func (b BoundScope) Validate() error {
	if b.manager == nil {
		return ErrRevoked
	}
	return b.manager.Validate(b)
}

// Scope returns the readcore scope only while the bound authorization remains
// current. The returned value is a transport-free readcore data carrier, not
// a revocable capability; callers must not cache it across operations or
// construct one from model request fields. A future Source/adapter must
// revalidate this BoundScope immediately before every operation.
func (b BoundScope) Scope() (readcore.Scope, error) {
	if err := b.Validate(); err != nil {
		return readcore.Scope{}, err
	}
	return b.scope, nil
}

func (b BoundScope) ConnectionID() string { return b.connectionID }

func (b BoundScope) ProfileID() string { return b.profileID }

func (b BoundScope) Revision() string { return b.revision }

func (b BoundScope) AllowsTool(tool string) bool {
	if b.Validate() != nil {
		return false
	}
	_, ok := b.tools[tool]
	return ok
}

func (b BoundScope) AllowsRoot(rootID string) bool {
	if b.Validate() != nil {
		return false
	}
	_, ok := b.roots[rootID]
	return ok
}

// AllowsPath applies lexical validation and deny patterns after checking the
// profile root allowlist. A matching deny always wins over an allow decision.
// Patterns use slash-separated path.Match globs with conservative
// case-insensitive matching; a pattern without a slash is also checked against
// every path component, making "*.key" deny nested keys. A trailing "/**"
// denies the named subtree recursively.
func (b BoundScope) AllowsPath(rootID, relativePath string) bool {
	if b.Validate() != nil || !readcore.ValidPath(relativePath) {
		return false
	}
	root, ok := b.roots[rootID]
	if !ok || denied(root.denyPatterns, relativePath) {
		return false
	}
	return true
}

func denied(patterns []string, relativePath string) bool {
	for _, pattern := range patterns {
		if matchesSubtree(pattern, relativePath) {
			return true
		}
		if matches(pattern, relativePath) {
			return true
		}
		if strings.Contains(pattern, "/") {
			continue
		}
		for _, component := range strings.Split(relativePath, "/") {
			if matches(pattern, component) {
				return true
			}
		}
	}
	return false
}

func matches(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	if err == nil && matched {
		return true
	}
	matched, err = path.Match(strings.ToLower(pattern), strings.ToLower(value))
	return err == nil && matched
}

// A pattern ending in "/**" denies the named path and every descendant. The
// prefix may itself contain path.Match wildcards; checking each ancestor keeps
// the boundary at a complete path component.
func matchesSubtree(pattern, value string) bool {
	if !strings.HasSuffix(pattern, "/**") {
		return false
	}
	prefix := strings.TrimSuffix(pattern, "/**")
	if prefix == "" {
		return true
	}
	for end := len(value); ; {
		if matches(prefix, value[:end]) {
			return true
		}
		separator := strings.LastIndexByte(value[:end], '/')
		if separator < 0 {
			break
		}
		end = separator
	}
	return false
}
