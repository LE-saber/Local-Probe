// Package config validates and stores the local, non-secret access
// configuration. It deliberately contains references to credentials, not
// credential material.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const SchemaVersionV1 = "local-probe.config.v1"

const MaxConfigBytes = 1 << 20

var (
	ErrInvalid          = errors.New("invalid configuration")
	ErrRevisionConflict = errors.New("configuration revision conflict")
)

// ValidationError identifies a configuration field without echoing its value.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Root is an explicitly configured local root. Path interpretation and safe
// opening belong to internal/rootfs; this package only validates its shape.
type Root struct {
	id             string
	path           string
	denyPatterns   []string
	ignorePatterns []string
}

func (r Root) ID() string { return r.id }

func (r Root) Path() string { return r.path }

func (r Root) DenyPatterns() []string { return append([]string(nil), r.denyPatterns...) }

// IgnorePatterns are advisory discovery filters. They never grant access and
// are evaluated only after explicit deny patterns.
func (r Root) IgnorePatterns() []string { return append([]string(nil), r.ignorePatterns...) }

func (r Root) clone() Root {
	r.denyPatterns = append([]string(nil), r.denyPatterns...)
	r.ignorePatterns = append([]string(nil), r.ignorePatterns...)
	return r
}

// Profile is a read-only access profile. A profile can be referenced by more
// than one connection, but its slices are never exposed for mutation.
type Profile struct {
	id             string
	rootIDs        []string
	tools          []string
	denyPatterns   []string
	ignorePatterns []string
	readOnly       bool
}

func (p Profile) ID() string { return p.id }

func (p Profile) RootIDs() []string { return append([]string(nil), p.rootIDs...) }

func (p Profile) Tools() []string { return append([]string(nil), p.tools...) }

func (p Profile) DenyPatterns() []string { return append([]string(nil), p.denyPatterns...) }

// IgnorePatterns are advisory discovery filters. They never grant access and
// are evaluated only after explicit deny patterns.
func (p Profile) IgnorePatterns() []string { return append([]string(nil), p.ignorePatterns...) }

func (p Profile) ReadOnly() bool { return p.readOnly }

func (p Profile) clone() Profile {
	p.rootIDs = append([]string(nil), p.rootIDs...)
	p.tools = append([]string(nil), p.tools...)
	p.denyPatterns = append([]string(nil), p.denyPatterns...)
	p.ignorePatterns = append([]string(nil), p.ignorePatterns...)
	return p
}

// Connection binds one authenticated ingress to one profile and one
// credential reference. The reference is not a secret and is never resolved
// by this package.
type Connection struct {
	id            string
	label         string
	profileID     string
	credentialRef string
	enabled       bool
}

func (c Connection) ID() string { return c.id }

func (c Connection) Label() string { return c.label }

func (c Connection) ProfileID() string { return c.profileID }

func (c Connection) CredentialRef() string { return c.credentialRef }

func (c Connection) Enabled() bool { return c.enabled }

// CredentialRef identifies protected credential material held elsewhere.
type CredentialRef struct {
	id   string
	kind string
}

func (r CredentialRef) ID() string { return r.id }

func (r CredentialRef) Kind() string { return r.kind }

// Config is an immutable validated configuration value. Callers can inspect
// it through the accessors below, but cannot mutate the backing slices.
type Config struct {
	schemaVersion    string
	roots            []Root
	profiles         []Profile
	connections      []Connection
	credentials      []CredentialRef
	environmentTools []EnvironmentTool
}

func NewRoot(id, rootPath string, denyPatterns []string) (Root, error) {
	return NewRootWithIgnore(id, rootPath, denyPatterns, nil)
}

// NewRootWithIgnore creates a root with separate explicit deny and discovery
// ignore patterns. Ignore patterns do not weaken deny enforcement.
func NewRootWithIgnore(id, rootPath string, denyPatterns, ignorePatterns []string) (Root, error) {
	r := Root{id: id, path: rootPath, denyPatterns: append([]string(nil), denyPatterns...), ignorePatterns: append([]string(nil), ignorePatterns...)}
	if err := validateRoot(r, "root"); err != nil {
		return Root{}, err
	}
	return r, nil
}

func NewProfile(id string, rootIDs, tools, denyPatterns []string) (Profile, error) {
	return NewProfileWithIgnore(id, rootIDs, tools, denyPatterns, nil)
}

// NewProfileWithIgnore creates a read-only profile with separate explicit
// deny and discovery ignore patterns.
func NewProfileWithIgnore(id string, rootIDs, tools, denyPatterns, ignorePatterns []string) (Profile, error) {
	p := Profile{
		id:             id,
		rootIDs:        append([]string(nil), rootIDs...),
		tools:          append([]string(nil), tools...),
		denyPatterns:   append([]string(nil), denyPatterns...),
		ignorePatterns: append([]string(nil), ignorePatterns...),
		readOnly:       true,
	}
	if err := validateProfile(p, "profile"); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func NewConnection(id, label, profileID, credentialRef string, enabled bool) Connection {
	return Connection{id: id, label: label, profileID: profileID, credentialRef: credentialRef, enabled: enabled}
}

func NewCredentialRef(id, kind string) CredentialRef {
	return CredentialRef{id: id, kind: kind}
}

func New(schemaVersion string, roots []Root, profiles []Profile, connections []Connection, credentials []CredentialRef) (Config, error) {
	return NewWithEnvironmentTools(schemaVersion, roots, profiles, connections, credentials, nil)
}

func (c Config) SchemaVersion() string { return c.schemaVersion }

func (c Config) Roots() []Root {
	return cloneRoots(c.roots)
}

func (c Config) Profiles() []Profile {
	return cloneProfiles(c.profiles)
}

func (c Config) Connections() []Connection {
	return append([]Connection(nil), c.connections...)
}

func (c Config) Credentials() []CredentialRef {
	return append([]CredentialRef(nil), c.credentials...)
}

func (c Config) Root(id string) (Root, bool) {
	for _, r := range c.roots {
		if r.id == id {
			return r.clone(), true
		}
	}
	return Root{}, false
}

func (c Config) Profile(id string) (Profile, bool) {
	for _, p := range c.profiles {
		if p.id == id {
			return p.clone(), true
		}
	}
	return Profile{}, false
}

func (c Config) Connection(id string) (Connection, bool) {
	for _, connection := range c.connections {
		if connection.id == id {
			return connection, true
		}
	}
	return Connection{}, false
}

func (c Config) Credential(id string) (CredentialRef, bool) {
	for _, credential := range c.credentials {
		if credential.id == id {
			return credential, true
		}
	}
	return CredentialRef{}, false
}

func (c Config) Clone() Config {
	return Config{
		schemaVersion:    c.schemaVersion,
		roots:            cloneRoots(c.roots),
		profiles:         cloneProfiles(c.profiles),
		connections:      append([]Connection(nil), c.connections...),
		credentials:      append([]CredentialRef(nil), c.credentials...),
		environmentTools: cloneEnvironmentTools(c.environmentTools),
	}
}

// Parse decodes one complete JSON configuration and rejects unknown fields,
// malformed trailing values, duplicate IDs and dangling references.
func Parse(data []byte) (Config, error) {
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("%w: configuration exceeds size limit", ErrInvalid)
	}
	if !utf8.Valid(data) {
		return Config{}, invalid("configuration", "invalid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Config{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%w: invalid JSON", ErrInvalid)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("%w: multiple JSON values", ErrInvalid)
		}
		return Config{}, fmt.Errorf("%w: invalid trailing data", ErrInvalid)
	}
	return fromRaw(raw)
}

func Load(reader io.Reader) (Config, error) {
	if reader == nil {
		return Config{}, fmt.Errorf("%w: nil reader", ErrInvalid)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("%w: read failed", ErrInvalid)
	}
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("%w: configuration exceeds size limit", ErrInvalid)
	}
	return Parse(data)
}

func (c Config) MarshalJSON() ([]byte, error) {
	raw := rawConfig{
		SchemaVersion:    c.schemaVersion,
		Roots:            make([]rawRoot, len(c.roots)),
		Profiles:         make([]rawProfile, len(c.profiles)),
		Connections:      make([]rawConnection, len(c.connections)),
		Credentials:      make([]rawCredential, len(c.credentials)),
		EnvironmentTools: make([]rawEnvironmentTool, len(c.environmentTools)),
	}
	for i, root := range c.roots {
		raw.Roots[i] = rawRoot{ID: root.id, Path: root.path, DenyPatterns: append([]string(nil), root.denyPatterns...), IgnorePatterns: append([]string(nil), root.ignorePatterns...)}
	}
	for i, profile := range c.profiles {
		profileReadOnly := profile.readOnly
		raw.Profiles[i] = rawProfile{
			ID:             profile.id,
			Roots:          append([]string(nil), profile.rootIDs...),
			Tools:          append([]string(nil), profile.tools...),
			DenyPatterns:   append([]string(nil), profile.denyPatterns...),
			IgnorePatterns: append([]string(nil), profile.ignorePatterns...),
			ReadOnly:       &profileReadOnly,
		}
	}
	for i, connection := range c.connections {
		enabled := connection.enabled
		raw.Connections[i] = rawConnection{
			ID:            connection.id,
			Label:         connection.label,
			ProfileID:     connection.profileID,
			CredentialRef: connection.credentialRef,
			Enabled:       &enabled,
		}
	}
	for i, credential := range c.credentials {
		raw.Credentials[i] = rawCredential{ID: credential.id, Kind: credential.kind}
	}
	for i, tool := range c.environmentTools {
		raw.EnvironmentTools[i] = rawEnvironmentTool{
			ID:             tool.id,
			CandidateFiles: append([]string(nil), tool.candidateFiles...),
			CandidateDirs:  append([]string(nil), tool.candidateDirs...),
		}
	}
	return json.Marshal(raw)
}

// Snapshot is a point-in-time configuration and revision pair returned by
// Store. Both values are immutable copies.
type Snapshot struct {
	config   Config
	revision string
}

func (s Snapshot) Config() Config { return s.config.Clone() }

func (s Snapshot) Revision() string { return s.revision }

// Store atomically replaces validated configuration snapshots. A replacement
// always advances the revision, including replacing with equivalent data, so
// previously bound authorization scopes cannot outlive an explicit reload.
type Store struct {
	mu         sync.RWMutex
	config     Config
	generation uint64
}

func NewStore(initial Config) (*Store, error) {
	if err := initial.validate(); err != nil {
		return nil, err
	}
	return &Store{config: initial.Clone(), generation: 1}, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{config: s.config.Clone(), revision: revisionString(s.generation)}
}

func (s *Store) Replace(next Config) (Snapshot, error) {
	return s.replace("", next, false)
}

func (s *Store) ReplaceIfRevision(expected string, next Config) (Snapshot, error) {
	if expected == "" {
		return Snapshot{}, fmt.Errorf("%w: expected revision required", ErrRevisionConflict)
	}
	return s.replace(expected, next, true)
}

func (s *Store) ReplaceJSON(data []byte) (Snapshot, error) {
	next, err := Parse(data)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Replace(next)
}

func (s *Store) replace(expected string, next Config, checkExpected bool) (Snapshot, error) {
	if err := next.validate(); err != nil {
		return Snapshot{}, err
	}
	next = next.Clone()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := revisionString(s.generation)
	if checkExpected && expected != current {
		return Snapshot{}, fmt.Errorf("%w", ErrRevisionConflict)
	}
	s.config = next
	s.generation++
	return Snapshot{config: s.config.Clone(), revision: revisionString(s.generation)}, nil
}

func revisionString(generation uint64) string { return fmt.Sprintf("r%d", generation) }

type rawConfig struct {
	SchemaVersion    string               `json:"schema_version"`
	Roots            []rawRoot            `json:"roots"`
	Profiles         []rawProfile         `json:"profiles"`
	Connections      []rawConnection      `json:"connections"`
	Credentials      []rawCredential      `json:"credentials"`
	EnvironmentTools []rawEnvironmentTool `json:"environment_tools,omitempty"`
}

type rawRoot struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	DenyPatterns   []string `json:"deny_patterns"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

type rawProfile struct {
	ID             string   `json:"id"`
	Roots          []string `json:"roots"`
	Tools          []string `json:"tools"`
	DenyPatterns   []string `json:"deny_patterns"`
	IgnorePatterns []string `json:"ignore_patterns"`
	ReadOnly       *bool    `json:"read_only"`
}

type rawConnection struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	ProfileID     string `json:"profile_id"`
	CredentialRef string `json:"credential_ref"`
	Enabled       *bool  `json:"enabled"`
}

type rawCredential struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func fromRaw(raw rawConfig) (Config, error) {
	c := Config{schemaVersion: raw.SchemaVersion}
	c.roots = make([]Root, len(raw.Roots))
	for i, root := range raw.Roots {
		c.roots[i] = Root{id: root.ID, path: root.Path, denyPatterns: append([]string(nil), root.DenyPatterns...), ignorePatterns: append([]string(nil), root.IgnorePatterns...)}
	}
	c.profiles = make([]Profile, len(raw.Profiles))
	for i, profile := range raw.Profiles {
		readOnly := true
		if profile.ReadOnly != nil {
			readOnly = *profile.ReadOnly
		}
		c.profiles[i] = Profile{
			id:             profile.ID,
			rootIDs:        append([]string(nil), profile.Roots...),
			tools:          append([]string(nil), profile.Tools...),
			denyPatterns:   append([]string(nil), profile.DenyPatterns...),
			ignorePatterns: append([]string(nil), profile.IgnorePatterns...),
			readOnly:       readOnly,
		}
	}
	c.connections = make([]Connection, len(raw.Connections))
	for i, connection := range raw.Connections {
		if connection.Enabled == nil {
			return Config{}, invalid(fmt.Sprintf("connections[%d].enabled", i), "field is required")
		}
		enabled := *connection.Enabled
		c.connections[i] = Connection{
			id:            connection.ID,
			label:         connection.Label,
			profileID:     connection.ProfileID,
			credentialRef: connection.CredentialRef,
			enabled:       enabled,
		}
	}
	c.credentials = make([]CredentialRef, len(raw.Credentials))
	for i, credential := range raw.Credentials {
		c.credentials[i] = CredentialRef{id: credential.ID, kind: credential.Kind}
	}
	if raw.EnvironmentTools != nil {
		c.environmentTools = make([]EnvironmentTool, len(raw.EnvironmentTools))
		for i, tool := range raw.EnvironmentTools {
			c.environmentTools[i] = EnvironmentTool{
				id:             tool.ID,
				candidateFiles: append([]string(nil), tool.CandidateFiles...),
				candidateDirs:  append([]string(nil), tool.CandidateDirs...),
			}
		}
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.schemaVersion != SchemaVersionV1 {
		return invalid("schema_version", "unsupported schema version")
	}
	rootIDs := make(map[string]struct{}, len(c.roots))
	for i, root := range c.roots {
		field := fmt.Sprintf("roots[%d]", i)
		if err := validateRoot(root, field); err != nil {
			return err
		}
		if !addUnique(rootIDs, root.id) {
			return invalid(field+".id", "duplicate id")
		}
	}
	credentialIDs := make(map[string]struct{}, len(c.credentials))
	for i, credential := range c.credentials {
		field := fmt.Sprintf("credentials[%d]", i)
		if !validIdentifier(credential.id) || !validIdentifier(credential.kind) {
			return invalid(field, "invalid id or kind")
		}
		if !addUnique(credentialIDs, credential.id) {
			return invalid(field+".id", "duplicate id")
		}
	}
	profileIDs := make(map[string]struct{}, len(c.profiles))
	for i, profile := range c.profiles {
		field := fmt.Sprintf("profiles[%d]", i)
		if err := validateProfile(profile, field); err != nil {
			return err
		}
		if !addUnique(profileIDs, profile.id) {
			return invalid(field+".id", "duplicate id")
		}
		for _, rootID := range profile.rootIDs {
			if _, ok := rootIDs[rootID]; !ok {
				return invalid(field+".roots", "unknown root reference")
			}
		}
	}
	connectionIDs := make(map[string]struct{}, len(c.connections))
	for i, connection := range c.connections {
		field := fmt.Sprintf("connections[%d]", i)
		if !validIdentifier(connection.id) || connection.profileID == "" || connection.credentialRef == "" {
			return invalid(field, "invalid id or required reference")
		}
		if connection.label != "" && (!utf8.ValidString(connection.label) || len(connection.label) > 256) {
			return invalid(field+".label", "invalid label")
		}
		if !addUnique(connectionIDs, connection.id) {
			return invalid(field+".id", "duplicate id")
		}
		if _, ok := profileIDs[connection.profileID]; !ok {
			return invalid(field+".profile_id", "unknown profile reference")
		}
		if _, ok := credentialIDs[connection.credentialRef]; !ok {
			return invalid(field+".credential_ref", "unknown credential reference")
		}
	}
	return validateEnvironmentTools(c.environmentTools)
}

func validateRoot(root Root, field string) error {
	if !validIdentifier(root.id) {
		return invalid(field+".id", "invalid id")
	}
	if root.path == "" || !utf8.ValidString(root.path) || strings.ContainsRune(root.path, 0) {
		return invalid(field+".path", "invalid root path")
	}
	if err := validatePatterns(root.denyPatterns, field+".deny_patterns"); err != nil {
		return err
	}
	return validatePatterns(root.ignorePatterns, field+".ignore_patterns")
}

func validateProfile(profile Profile, field string) error {
	if !validIdentifier(profile.id) {
		return invalid(field+".id", "invalid id")
	}
	if !profile.readOnly {
		return invalid(field+".read_only", "write profiles are not enabled")
	}
	if err := validateIDList(profile.rootIDs, field+".roots"); err != nil {
		return err
	}
	if err := validateIDList(profile.tools, field+".tools"); err != nil {
		return err
	}
	if err := validatePatterns(profile.denyPatterns, field+".deny_patterns"); err != nil {
		return err
	}
	return validatePatterns(profile.ignorePatterns, field+".ignore_patterns")
}

func validateIDList(values []string, field string) error {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if !validIdentifier(value) {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "invalid id")
		}
		if !addUnique(seen, value) {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "duplicate id")
		}
	}
	return nil
}

func validatePatterns(patterns []string, field string) error {
	for i, pattern := range patterns {
		if pattern == "" || len(pattern) > 4096 || !utf8.ValidString(pattern) || strings.ContainsRune(pattern, 0) || strings.Contains(pattern, "\\") || strings.HasPrefix(pattern, "/") {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "invalid relative pattern")
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return invalid(fmt.Sprintf("%s[%d]", field, i), "parent traversal is not allowed")
			}
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "invalid glob pattern")
		}
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func addUnique(seen map[string]struct{}, value string) bool {
	if _, exists := seen[value]; exists {
		return false
	}
	seen[value] = struct{}{}
	return true
}

func invalid(field, reason string) error {
	return &ValidationError{Field: field, Reason: reason}
}

func cloneRoots(values []Root) []Root {
	if values == nil {
		return nil
	}
	out := make([]Root, len(values))
	for i, value := range values {
		out[i] = value.clone()
	}
	return out
}

func cloneProfiles(values []Profile) []Profile {
	if values == nil {
		return nil
	}
	out := make([]Profile, len(values))
	for i, value := range values {
		out[i] = value.clone()
	}
	return out
}

// rejectDuplicateJSONKeys walks every object before schema decoding. The
// canonical key uses the smallest rune in its Unicode SimpleFold class so a
// case collision is rejected even when encoding/json would compare it through
// a case-insensitive field match. This is deliberately stricter than merely
// rejecting duplicate exact spellings.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return fmt.Errorf("%w: invalid JSON object keys", ErrInvalid)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: multiple JSON values", ErrInvalid)
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, ok := mustJSONString(decoder)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			folded := foldJSONKey(key)
			if _, exists := seen[folded]; exists {
				return fmt.Errorf("duplicate object key")
			}
			seen[folded] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated array")
		}
	}
	return nil
}

func mustJSONString(decoder *json.Decoder) (string, bool) {
	token, err := decoder.Token()
	if err != nil {
		return "", false
	}
	key, ok := token.(string)
	return key, ok
}

func foldJSONKey(key string) string {
	var folded strings.Builder
	for _, r := range key {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
