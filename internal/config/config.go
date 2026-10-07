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

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
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
	workspaceRoots []WorkspaceRootMetadata
	tools          []string
	denyPatterns   []string
	ignorePatterns []string
	readOnly       bool
}

func (p Profile) ID() string { return p.id }

func (p Profile) RootIDs() []string { return append([]string(nil), p.rootIDs...) }

// WorkspaceRoots returns profile-scoped display and enablement metadata. A
// disabled entry is retained here but must not appear in RootIDs, which is
// the effective authorization scope consumed by runtime code.
func (p Profile) WorkspaceRoots() []WorkspaceRootMetadata {
	return cloneWorkspaceRootMetadata(p.workspaceRoots)
}

func (p Profile) Tools() []string { return append([]string(nil), p.tools...) }

func (p Profile) DenyPatterns() []string { return append([]string(nil), p.denyPatterns...) }

// IgnorePatterns are advisory discovery filters. They never grant access and
// are evaluated only after explicit deny patterns.
func (p Profile) IgnorePatterns() []string { return append([]string(nil), p.ignorePatterns...) }

func (p Profile) ReadOnly() bool { return p.readOnly }

func (p Profile) clone() Profile {
	p.rootIDs = append([]string(nil), p.rootIDs...)
	p.workspaceRoots = cloneWorkspaceRootMetadata(p.workspaceRoots)
	p.tools = append([]string(nil), p.tools...)
	p.denyPatterns = append([]string(nil), p.denyPatterns...)
	p.ignorePatterns = append([]string(nil), p.ignorePatterns...)
	return p
}

// WorkspaceRootMetadata is local UI metadata scoped to one access profile.
// It is never a global root enable flag: a shared root may remain enabled for
// another profile while paused in this one.
type WorkspaceRootMetadata struct {
	rootID      string
	displayName string
	enabled     bool
}

func NewWorkspaceRootMetadata(rootID, displayName string, enabled bool) (WorkspaceRootMetadata, error) {
	metadata := WorkspaceRootMetadata{rootID: rootID, displayName: displayName, enabled: enabled}
	if err := validateWorkspaceRootMetadata(metadata, "workspace_roots"); err != nil {
		return WorkspaceRootMetadata{}, err
	}
	return metadata, nil
}

func (m WorkspaceRootMetadata) RootID() string      { return m.rootID }
func (m WorkspaceRootMetadata) DisplayName() string { return m.displayName }
func (m WorkspaceRootMetadata) Enabled() bool       { return m.enabled }

func cloneWorkspaceRootMetadata(values []WorkspaceRootMetadata) []WorkspaceRootMetadata {
	if values == nil {
		return nil
	}
	copyOfValues := make([]WorkspaceRootMetadata, len(values))
	copy(copyOfValues, values)
	return copyOfValues
}

// Connection binds one authenticated ingress to one profile and one
// credential reference. The reference is not a secret and is never resolved
// by this package.
type Connection struct {
	id               string
	label            string
	profileID        string
	credentialRef    string
	enabled          bool
	transport        ConnectionTransport
	tunnelID         string
	tunnelAlias      string
	desktopTransport ConnectionTransport
	desktopPort      int
}

func (c Connection) ID() string { return c.id }

func (c Connection) Label() string { return c.label }

func (c Connection) ProfileID() string { return c.profileID }

func (c Connection) CredentialRef() string { return c.credentialRef }

func (c Connection) Enabled() bool { return c.enabled }

// Transport identifies the configured non-secret ingress metadata. A legacy
// connection created before transport metadata existed reports local.
func (c Connection) Transport() ConnectionTransport { return normalizedTransport(c.transport) }

// TunnelID is an opaque, constrained identifier for a named remote ingress;
// it is never a URL or credential.
func (c Connection) TunnelID() string { return c.tunnelID }

// TunnelAlias is an optional local label for a named remote ingress. It is not
// used for authentication or endpoint resolution.
func (c Connection) TunnelAlias() string { return c.tunnelAlias }

func (c Connection) clone() Connection { return c }

// Desktop launch preferences are non-secret local UI metadata, not an
// authenticated ingress or a grant. Existing transport validation is unchanged.
func (c Connection) DesktopTransport() ConnectionTransport { return c.desktopTransport }
func (c Connection) DesktopPort() int                      { return c.desktopPort }
func (c Connection) WithDesktop(label string, transport ConnectionTransport, port int) Connection {
	c.label, c.desktopTransport, c.desktopPort = label, transport, port
	return c
}

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
	schemaVersion       string
	roots               []Root
	profiles            []Profile
	connections         []Connection
	credentials         []CredentialRef
	environmentTools    []EnvironmentTool
	developerMode       commandprofile.DeveloperMode
	commandProfiles     []commandprofile.Profile
	desktopConnectionID string
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
	return NewProfileWithWorkspaceRoots(id, rootIDs, tools, denyPatterns, ignorePatterns, nil)
}

// NewProfileWithWorkspaceRoots creates a read-only profile with explicit
// profile-scoped workspace display and enablement metadata.
func NewProfileWithWorkspaceRoots(id string, rootIDs, tools, denyPatterns, ignorePatterns []string, workspaceRoots []WorkspaceRootMetadata) (Profile, error) {
	p := Profile{
		id:             id,
		rootIDs:        append([]string(nil), rootIDs...),
		workspaceRoots: cloneWorkspaceRootMetadata(workspaceRoots),
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
	return Connection{id: id, label: label, profileID: profileID, credentialRef: credentialRef, enabled: enabled, transport: TransportLocal}
}

// NewConnectionWithTransport creates a connection with explicit, non-secret
// ingress metadata. It does not resolve credentials, contact a tunnel, or
// enable a remote transport; those responsibilities belong to later runtime
// layers. Local connections must not carry tunnel metadata, while remote
// transports require a constrained tunnel ID.
func NewConnectionWithTransport(id, label, profileID, credentialRef string, enabled bool, transport ConnectionTransport, tunnelID, tunnelAlias string) (Connection, error) {
	if transport == "" {
		return Connection{}, invalid("connection.transport", "transport is required")
	}
	connection := Connection{
		id:            id,
		label:         label,
		profileID:     profileID,
		credentialRef: credentialRef,
		enabled:       enabled,
		transport:     normalizedTransport(transport),
		tunnelID:      tunnelID,
		tunnelAlias:   tunnelAlias,
	}
	if err := validateConnectionTransport(connection.transport, connection.tunnelID, connection.tunnelAlias, "connection"); err != nil {
		return Connection{}, err
	}
	return connection, nil
}

func NewCredentialRef(id, kind string) CredentialRef {
	return CredentialRef{id: id, kind: kind}
}

func New(schemaVersion string, roots []Root, profiles []Profile, connections []Connection, credentials []CredentialRef) (Config, error) {
	return NewWithEnvironmentTools(schemaVersion, roots, profiles, connections, credentials, nil)
}

// NewWithCommandProfiles extends the trusted local configuration with the
// developer-mode command profile layer. Command profiles are immutable and
// are never accepted from MCP request parameters.
func NewWithCommandProfiles(schemaVersion string, roots []Root, profiles []Profile, connections []Connection, credentials []CredentialRef, environmentTools []EnvironmentTool, developerMode commandprofile.DeveloperMode, commandProfiles []commandprofile.Profile) (Config, error) {
	c := Config{
		schemaVersion:    schemaVersion,
		roots:            cloneRoots(roots),
		profiles:         cloneProfiles(profiles),
		connections:      cloneConnections(connections),
		credentials:      append([]CredentialRef(nil), credentials...),
		environmentTools: cloneEnvironmentTools(environmentTools),
		developerMode:    developerMode.Clone(),
		commandProfiles:  cloneCommandProfiles(commandProfiles),
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) SchemaVersion() string { return c.schemaVersion }

func (c Config) DesktopConnectionID() string { return c.desktopConnectionID }
func (c Config) WithDesktopConnectionID(id string) (Config, error) {
	c = c.Clone()
	c.desktopConnectionID = id
	return c, c.validate()
}

func (c Config) Roots() []Root {
	return cloneRoots(c.roots)
}

func (c Config) Profiles() []Profile {
	return cloneProfiles(c.profiles)
}

func (c Config) Connections() []Connection {
	return cloneConnections(c.connections)
}

func (c Config) Credentials() []CredentialRef {
	return append([]CredentialRef(nil), c.credentials...)
}

func (c Config) DeveloperMode() commandprofile.DeveloperMode {
	return c.developerMode.Clone()
}

func (c Config) CommandProfiles() []commandprofile.Profile {
	return cloneCommandProfiles(c.commandProfiles)
}

func (c Config) CommandProfile(id string) (commandprofile.Profile, bool) {
	for _, profile := range c.commandProfiles {
		if profile.ID() == id {
			return profile.Clone(), true
		}
	}
	return commandprofile.Profile{}, false
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
		schemaVersion:       c.schemaVersion,
		roots:               cloneRoots(c.roots),
		profiles:            cloneProfiles(c.profiles),
		connections:         cloneConnections(c.connections),
		credentials:         append([]CredentialRef(nil), c.credentials...),
		environmentTools:    cloneEnvironmentTools(c.environmentTools),
		developerMode:       c.developerMode.Clone(),
		commandProfiles:     cloneCommandProfiles(c.commandProfiles),
		desktopConnectionID: c.desktopConnectionID,
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
	if c.developerMode.Enabled() || len(c.developerMode.AllowedConnections()) > 0 || len(c.commandProfiles) > 0 {
		raw.DeveloperMode = marshalDeveloperMode(c.developerMode)
	}
	if len(c.commandProfiles) > 0 {
		raw.CommandProfiles = make([]rawCommandProfile, len(c.commandProfiles))
		for i, profile := range c.commandProfiles {
			raw.CommandProfiles[i] = marshalCommandProfile(profile)
		}
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
			WorkspaceRoots: marshalWorkspaceRootMetadata(profile.workspaceRoots),
			ReadOnly:       &profileReadOnly,
		}
	}
	for i, connection := range c.connections {
		enabled := connection.enabled
		transport := string(connection.Transport())
		raw.Connections[i] = rawConnection{
			ID:               connection.id,
			Label:            connection.label,
			ProfileID:        connection.profileID,
			CredentialRef:    connection.credentialRef,
			Enabled:          &enabled,
			Transport:        &transport,
			TunnelID:         connection.tunnelID,
			TunnelAlias:      connection.tunnelAlias,
			DesktopTransport: connection.desktopTransport,
			DesktopPort:      connection.desktopPort,
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
	raw.DesktopConnectionID = c.desktopConnectionID
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

// AcquireRevisionLease holds the store's read lock while a trusted local
// operation uses a configuration snapshot. A replacement waits for release,
// so a command executor cannot check a revision and then launch an old profile
// after the configuration has changed. The release function is idempotent.
func (s *Store) AcquireRevisionLease(expected string) (release func(), ok bool) {
	if s == nil || expected == "" {
		return nil, false
	}
	s.mu.RLock()
	if revisionString(s.generation) != expected {
		s.mu.RUnlock()
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(s.mu.RUnlock) }, true
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
	DesktopConnectionID string               `json:"desktop_connection_id,omitempty"`
	SchemaVersion       string               `json:"schema_version"`
	Roots               []rawRoot            `json:"roots"`
	Profiles            []rawProfile         `json:"profiles"`
	Connections         []rawConnection      `json:"connections"`
	Credentials         []rawCredential      `json:"credentials"`
	EnvironmentTools    []rawEnvironmentTool `json:"environment_tools,omitempty"`
	DeveloperMode       *rawDeveloperMode    `json:"developer_mode,omitempty"`
	CommandProfiles     []rawCommandProfile  `json:"command_profiles,omitempty"`
}

type rawRoot struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	DenyPatterns   []string `json:"deny_patterns"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

type rawProfile struct {
	ID             string                     `json:"id"`
	Roots          []string                   `json:"roots"`
	Tools          []string                   `json:"tools"`
	DenyPatterns   []string                   `json:"deny_patterns"`
	IgnorePatterns []string                   `json:"ignore_patterns"`
	ReadOnly       *bool                      `json:"read_only"`
	WorkspaceRoots []rawWorkspaceRootMetadata `json:"workspace_roots,omitempty"`
}

type rawWorkspaceRootMetadata struct {
	RootID      string `json:"root_id"`
	DisplayName string `json:"display_name,omitempty"`
	Enabled     *bool  `json:"enabled"`
}

func marshalWorkspaceRootMetadata(values []WorkspaceRootMetadata) []rawWorkspaceRootMetadata {
	if len(values) == 0 {
		return nil
	}
	raw := make([]rawWorkspaceRootMetadata, len(values))
	for i, value := range values {
		enabled := value.enabled
		raw[i] = rawWorkspaceRootMetadata{RootID: value.rootID, DisplayName: value.displayName, Enabled: &enabled}
	}
	return raw
}

func parseWorkspaceRootMetadata(values []rawWorkspaceRootMetadata) ([]WorkspaceRootMetadata, error) {
	if values == nil {
		return nil, nil
	}
	metadata := make([]WorkspaceRootMetadata, len(values))
	for i, value := range values {
		if value.Enabled == nil {
			return nil, ErrInvalid
		}
		item, err := NewWorkspaceRootMetadata(value.RootID, value.DisplayName, *value.Enabled)
		if err != nil {
			return nil, err
		}
		metadata[i] = item
	}
	return metadata, nil
}

type rawConnection struct {
	DesktopTransport ConnectionTransport `json:"desktop_transport,omitempty"`
	DesktopPort      int                 `json:"desktop_port,omitempty"`
	ID               string              `json:"id"`
	Label            string              `json:"label"`
	ProfileID        string              `json:"profile_id"`
	CredentialRef    string              `json:"credential_ref"`
	Enabled          *bool               `json:"enabled"`
	Transport        *string             `json:"transport,omitempty"`
	TunnelID         string              `json:"tunnel_id,omitempty"`
	TunnelAlias      string              `json:"tunnel_alias,omitempty"`
}

type rawCredential struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func fromRaw(raw rawConfig) (Config, error) {
	c := Config{schemaVersion: raw.SchemaVersion, desktopConnectionID: raw.DesktopConnectionID}
	c.developerMode = commandprofile.DefaultDeveloperMode()
	if raw.DeveloperMode != nil {
		mode, err := parseDeveloperMode(*raw.DeveloperMode)
		if err != nil {
			return Config{}, err
		}
		c.developerMode = mode
	}
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
		workspaceRoots, err := parseWorkspaceRootMetadata(profile.WorkspaceRoots)
		if err != nil {
			return Config{}, fmt.Errorf("%w: profiles[%d].workspace_roots: invalid metadata", ErrInvalid, i)
		}
		c.profiles[i] = Profile{
			id:             profile.ID,
			rootIDs:        append([]string(nil), profile.Roots...),
			workspaceRoots: workspaceRoots,
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
		transport := TransportLocal
		if connection.Transport != nil {
			if *connection.Transport == "" {
				return Config{}, invalid(fmt.Sprintf("connections[%d].transport", i), "must not be empty")
			}
			transport = ConnectionTransport(*connection.Transport)
		}
		c.connections[i] = Connection{
			id:               connection.ID,
			label:            connection.Label,
			profileID:        connection.ProfileID,
			credentialRef:    connection.CredentialRef,
			enabled:          enabled,
			transport:        transport,
			tunnelID:         connection.TunnelID,
			tunnelAlias:      connection.TunnelAlias,
			desktopTransport: connection.DesktopTransport,
			desktopPort:      connection.DesktopPort,
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
	if raw.CommandProfiles != nil {
		c.commandProfiles = make([]commandprofile.Profile, len(raw.CommandProfiles))
		for i, rawProfile := range raw.CommandProfiles {
			profile, err := parseCommandProfile(rawProfile)
			if err != nil {
				return Config{}, fmt.Errorf("%w: command_profiles[%d]: %v", ErrInvalid, i, err)
			}
			c.commandProfiles[i] = profile
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
		for metadataIndex, metadata := range profile.workspaceRoots {
			if _, ok := rootIDs[metadata.rootID]; !ok {
				return invalid(fmt.Sprintf("%s.workspace_roots[%d].root_id", field, metadataIndex), "unknown root reference")
			}
		}
	}
	connectionIDs := make(map[string]struct{}, len(c.connections))
	for i, connection := range c.connections {
		field := fmt.Sprintf("connections[%d]", i)
		if connection.desktopTransport != "" && connection.desktopTransport != TransportOpenAIRuntime && connection.desktopTransport != TransportCloudflareNamed {
			return invalid(field+".desktop_transport", "unsupported desktop transport")
		}
		if connection.desktopPort != 0 && (connection.desktopPort < 1024 || connection.desktopPort > 65535) {
			return invalid(field+".desktop_port", "invalid loopback port")
		}
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
		if err := validateConnectionTransport(connection.Transport(), connection.tunnelID, connection.tunnelAlias, field); err != nil {
			return err
		}
	}
	if err := validateEnvironmentTools(c.environmentTools); err != nil {
		return err
	}
	if err := commandprofile.ValidateProfiles(c.commandProfiles); err != nil {
		return err
	}
	for i, command := range c.commandProfiles {
		for j, slot := range command.Slots() {
			if slot.Kind() != commandprofile.SlotRootRelativePath {
				continue
			}
			for k, rootID := range slot.RootIDs() {
				if _, ok := rootIDs[rootID]; !ok {
					return invalid(fmt.Sprintf("command_profiles[%d].argv.slots[%d].root_ids[%d]", i, j, k), "unknown root reference")
				}
			}
		}
	}
	if c.desktopConnectionID != "" {
		if _, ok := connectionIDs[c.desktopConnectionID]; !ok {
			return invalid("desktop_connection_id", "unknown connection")
		}
	}
	return validateDeveloperModeConnections(c.developerMode, connectionIDs)
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
	if profile.workspaceRoots != nil {
		active := make(map[string]struct{}, len(profile.rootIDs))
		for _, id := range profile.rootIDs {
			active[id] = struct{}{}
		}
		seen := make(map[string]struct{}, len(profile.workspaceRoots))
		for i, metadata := range profile.workspaceRoots {
			metadataField := fmt.Sprintf("%s.workspace_roots[%d]", field, i)
			if err := validateWorkspaceRootMetadata(metadata, metadataField); err != nil {
				return err
			}
			if !addUnique(seen, metadata.rootID) {
				return invalid(metadataField+".root_id", "duplicate id")
			}
			_, isActive := active[metadata.rootID]
			if metadata.enabled != isActive {
				return invalid(metadataField+".enabled", "must match effective profile roots")
			}
		}
		for id := range active {
			if _, ok := seen[id]; !ok {
				return invalid(field+".workspace_roots", "must describe every active root")
			}
		}
	}
	if err := validateIDList(profile.tools, field+".tools"); err != nil {
		return err
	}
	if err := validatePatterns(profile.denyPatterns, field+".deny_patterns"); err != nil {
		return err
	}
	return validatePatterns(profile.ignorePatterns, field+".ignore_patterns")
}

func validateWorkspaceRootMetadata(metadata WorkspaceRootMetadata, field string) error {
	if !validIdentifier(metadata.rootID) {
		return invalid(field+".root_id", "invalid id")
	}
	name := metadata.displayName
	if name == "" || !utf8.ValidString(name) || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsAny(name, `/\\:`) {
		return invalid(field+".display_name", "invalid display name")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return invalid(field+".display_name", "invalid display name")
		}
	}
	return nil
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

func cloneConnections(values []Connection) []Connection {
	if values == nil {
		return nil
	}
	out := make([]Connection, len(values))
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
