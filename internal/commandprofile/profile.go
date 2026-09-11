// Package commandprofile defines the small, immutable command profiles that
// may be used by the local developer-mode probe layer.  It intentionally does
// not start processes.  Process admission and execution are separate steps so
// a future launcher cannot accidentally treat a model supplied string as a
// command line.
package commandprofile

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KindVersionProbe Kind     = "version_probe"
	PlatformWindows  Platform = "windows"

	ConfirmationPerCall ConfirmationMode = "per_call"
	NetworkDeny         NetworkMode      = "deny"
	CWDPrivateEmpty     CWDKind          = "private_empty"
	ResultVersion       ResultKind       = "version"

	maxProfiles           = 128
	maxVariantsPerProfile = 16
	maxVariantArguments   = 8
	maxArgumentBytes      = 256
	maxAllowedConnections = 128
	maxFixedEnvironment   = 0
	maxTimeout            = 10 * time.Second
	maxOutputBytes        = 64 << 10
)

var (
	ErrInvalid                    = errors.New("invalid command profile")
	ErrDeveloperModeDisabled      = errors.New("developer mode disabled")
	ErrConnectionNotAllowed       = errors.New("connection is not allowed")
	ErrNetworkEnforcementRequired = errors.New("network enforcement is not verified")
	ErrUnsupportedProfile         = errors.New("unsupported command profile")
)

// Kind identifies the execution semantics of a profile.  Only VersionProbe
// is accepted in this package; more dangerous kinds need a separate review.
type Kind string

type Platform string
type ConfirmationMode string
type NetworkMode string
type CWDKind string
type ResultKind string

// DeveloperMode is local configuration.  The zero value is deliberately
// disabled and denies every connection.
type DeveloperMode struct {
	enabled             bool
	allowedConnections  []string
	defaultConfirmation ConfirmationMode
	networkDefault      NetworkMode
}

func DefaultDeveloperMode() DeveloperMode {
	return DeveloperMode{
		defaultConfirmation: ConfirmationPerCall,
		networkDefault:      NetworkDeny,
	}
}

func NewDeveloperMode(enabled bool, allowedConnections []string, defaultConfirmation ConfirmationMode, networkDefault NetworkMode) (DeveloperMode, error) {
	mode := DeveloperMode{
		enabled:             enabled,
		allowedConnections:  append([]string(nil), allowedConnections...),
		defaultConfirmation: defaultConfirmation,
		networkDefault:      networkDefault,
	}
	if err := mode.validate(); err != nil {
		return DeveloperMode{}, err
	}
	return mode, nil
}

func (d DeveloperMode) Enabled() bool { return d.enabled }

func (d DeveloperMode) AllowedConnections() []string {
	return append([]string(nil), d.allowedConnections...)
}

func (d DeveloperMode) DefaultConfirmation() ConfirmationMode { return d.defaultConfirmation }

func (d DeveloperMode) NetworkDefault() NetworkMode { return d.networkDefault }

func (d DeveloperMode) AllowsConnection(connectionID string) bool {
	if !d.enabled || connectionID == "" {
		return false
	}
	for _, allowed := range d.allowedConnections {
		if allowed == connectionID {
			return true
		}
	}
	return false
}

func (d DeveloperMode) Clone() DeveloperMode {
	d.allowedConnections = append([]string(nil), d.allowedConnections...)
	return d
}

// Validate checks the immutable local developer-mode policy.  Configuration
// parsing calls this before storing a snapshot; runtime admission calls it
// again before allowing a process action.
func (d DeveloperMode) Validate() error { return d.validate() }

func (d DeveloperMode) validate() error {
	if len(d.allowedConnections) > maxAllowedConnections {
		return invalid("developer_mode.allowed_connections", "too many connections")
	}
	seen := make(map[string]struct{}, len(d.allowedConnections))
	for i, connectionID := range d.allowedConnections {
		if !validIdentifier(connectionID) {
			return invalid(fmt.Sprintf("developer_mode.allowed_connections[%d]", i), "invalid connection id")
		}
		if _, ok := seen[connectionID]; ok {
			return invalid(fmt.Sprintf("developer_mode.allowed_connections[%d]", i), "duplicate connection id")
		}
		seen[connectionID] = struct{}{}
	}
	if d.defaultConfirmation != ConfirmationPerCall {
		return invalid("developer_mode.default_confirmation", "only per_call is supported")
	}
	if d.networkDefault != NetworkDeny {
		return invalid("developer_mode.network_default", "only deny is supported")
	}
	return nil
}

// IdentitySpec is the executable identity policy.  The booleans are required
// to be true for a version probe.  SHA256 is optional because the Windows
// enforcer may bind a native file identity instead; when present it must be a
// complete local hexadecimal digest.
type IdentitySpec struct {
	RequireRegular bool
	RejectReparse  bool
	SHA256         string
}

// VariantSpec is an exact, locally configured argv variant.  It has no slots,
// suffix field or free-form argument escape hatch by design.
type VariantSpec struct {
	ID    string
	Exact []string
}

type CWDSpec struct {
	Kind CWDKind
}

// EnvironmentSpec is intentionally empty for the first profile kind.  The
// launcher supplies its package-owned sanitized environment.  User-provided
// inherited or fixed values are rejected during validation.
type EnvironmentSpec struct {
	Inherit bool
	Allow   []string
	Fixed   map[string]string
}

type LimitsSpec struct {
	WallTimeout  time.Duration
	StdoutBytes  int
	StderrBytes  int
	MaxProcesses int
	MaxChildren  int
}

type NetworkSpec struct {
	Mode               NetworkMode
	RequireEnforcement bool
}

type ConfirmationSpec struct {
	Mode      ConfirmationMode
	LocalOnly bool
}

type ResultSpec struct {
	Type            ResultKind
	ReturnRawOutput bool
}

// Spec is the trusted local configuration input for New.  It is not a wire
// request type and is never accepted directly from MCP callers.
type Spec struct {
	ID           string
	Kind         Kind
	Platform     []Platform
	Executable   string
	Identity     IdentitySpec
	Variants     []VariantSpec
	CWD          CWDSpec
	Environment  EnvironmentSpec
	Limits       LimitsSpec
	Network      NetworkSpec
	Confirmation ConfirmationSpec
	Result       ResultSpec
}

// Profile is immutable after construction.
type Profile struct {
	id           string
	kind         Kind
	platform     []Platform
	executable   string
	identity     IdentitySpec
	variants     []Variant
	cwd          CWDSpec
	environment  EnvironmentSpec
	limits       LimitsSpec
	network      NetworkSpec
	confirmation ConfirmationSpec
	result       ResultSpec
}

type Variant struct {
	id    string
	exact []string
}

func New(spec Spec) (Profile, error) {
	if err := validateSpec(spec); err != nil {
		return Profile{}, err
	}
	variants := make([]Variant, len(spec.Variants))
	for i, variant := range spec.Variants {
		variants[i] = Variant{id: variant.ID, exact: append([]string(nil), variant.Exact...)}
	}
	environment := spec.Environment
	environment.Allow = append([]string(nil), environment.Allow...)
	if environment.Fixed != nil {
		environment.Fixed = map[string]string{}
	}
	return Profile{
		id:           spec.ID,
		kind:         spec.Kind,
		platform:     append([]Platform(nil), spec.Platform...),
		executable:   spec.Executable,
		identity:     spec.Identity,
		variants:     variants,
		cwd:          spec.CWD,
		environment:  environment,
		limits:       spec.Limits,
		network:      spec.Network,
		confirmation: spec.Confirmation,
		result:       spec.Result,
	}, nil
}

func (p Profile) Clone() Profile {
	p.platform = append([]Platform(nil), p.platform...)
	p.variants = append([]Variant(nil), p.variants...)
	for i := range p.variants {
		p.variants[i].exact = append([]string(nil), p.variants[i].exact...)
	}
	p.environment.Allow = append([]string(nil), p.environment.Allow...)
	if p.environment.Fixed != nil {
		p.environment.Fixed = map[string]string{}
	}
	return p
}

func (v Variant) ID() string             { return v.id }
func (v Variant) Exact() []string        { return append([]string(nil), v.exact...) }
func (p Profile) ID() string             { return p.id }
func (p Profile) Kind() Kind             { return p.kind }
func (p Profile) Platform() []Platform   { return append([]Platform(nil), p.platform...) }
func (p Profile) Executable() string     { return p.executable }
func (p Profile) Identity() IdentitySpec { return p.identity }
func (p Profile) CWD() CWDSpec           { return p.cwd }
func (p Profile) Environment() EnvironmentSpec {
	env := p.environment
	env.Allow = append([]string(nil), env.Allow...)
	if env.Fixed != nil {
		env.Fixed = map[string]string{}
	}
	return env
}
func (p Profile) Limits() LimitsSpec             { return p.limits }
func (p Profile) Network() NetworkSpec           { return p.network }
func (p Profile) Confirmation() ConfirmationSpec { return p.confirmation }
func (p Profile) Result() ResultSpec             { return p.result }

func (p Profile) Variants() []Variant {
	variants := append([]Variant(nil), p.variants...)
	for i := range variants {
		variants[i].exact = append([]string(nil), variants[i].exact...)
	}
	return variants
}

func (p Profile) Variant(id string) (Variant, bool) {
	for _, variant := range p.variants {
		if variant.id == id {
			return Variant{id: variant.id, exact: append([]string(nil), variant.exact...)}, true
		}
	}
	return Variant{}, false
}

// EnforcementCapability is an opaque local capability.  No JSON field can
// set it; a future Windows enforcer must inject the value through the local
// API.  The zero value is intentionally unverified.
type EnforcementCapability struct{ marker *struct{} }

func (c EnforcementCapability) Verified() bool { return c.marker != nil }

func (c EnforcementCapability) MarshalJSON() ([]byte, error) {
	return nil, errors.New("enforcement capability is not serializable")
}

func (*EnforcementCapability) UnmarshalJSON([]byte) error {
	return errors.New("enforcement capability is not deserializable")
}

// Admit checks the local developer-mode and network hard gates.  Confirmation
// tokens are checked separately by internal/confirmation and must be bound to
// the same connection/profile revision/variant/request nonce.
func (p Profile) Admit(mode DeveloperMode, connectionID string, enforcement EnforcementCapability) error {
	if err := validateProfile(p); err != nil {
		return err
	}
	if err := mode.validate(); err != nil {
		return err
	}
	if !mode.Enabled() {
		return ErrDeveloperModeDisabled
	}
	if !mode.AllowsConnection(connectionID) {
		return ErrConnectionNotAllowed
	}
	if p.network.RequireEnforcement && !enforcement.Verified() {
		return ErrNetworkEnforcementRequired
	}
	return nil
}

func ValidateProfiles(profiles []Profile) error {
	if len(profiles) > maxProfiles {
		return invalid("command_profiles", "too many profiles")
	}
	seen := make(map[string]struct{}, len(profiles))
	for i, profile := range profiles {
		if _, ok := seen[profile.id]; ok {
			return invalid(fmt.Sprintf("command_profiles[%d].id", i), "duplicate id")
		}
		seen[profile.id] = struct{}{}
		if err := validateProfile(profile); err != nil {
			return fmt.Errorf("%w: command_profiles[%d]: %v", ErrInvalid, i, err)
		}
	}
	return nil
}

func validateSpec(spec Spec) error {
	if !validIdentifier(spec.ID) {
		return invalid("command_profile.id", "invalid id")
	}
	if spec.Kind != KindVersionProbe {
		return invalid("command_profile.kind", "only version_probe is supported")
	}
	if len(spec.Platform) != 1 || spec.Platform[0] != PlatformWindows {
		return invalid("command_profile.platform", "only windows is supported")
	}
	if !validWindowsExecutable(spec.Executable) {
		return invalid("command_profile.executable", "must be an absolute local .exe path")
	}
	if !spec.Identity.RequireRegular || !spec.Identity.RejectReparse {
		return invalid("command_profile.identity", "regular and non-reparse checks are required")
	}
	if spec.Identity.SHA256 != "" {
		if len(spec.Identity.SHA256) != 64 {
			return invalid("command_profile.identity.sha256", "must be a SHA-256 digest")
		}
		if _, err := hex.DecodeString(spec.Identity.SHA256); err != nil {
			return invalid("command_profile.identity.sha256", "must be hexadecimal")
		}
	}
	if len(spec.Variants) == 0 || len(spec.Variants) > maxVariantsPerProfile {
		return invalid("command_profile.argv.variants", "invalid number of variants")
	}
	seen := make(map[string]struct{}, len(spec.Variants))
	for i, variant := range spec.Variants {
		field := fmt.Sprintf("command_profile.argv.variants[%d]", i)
		if !validIdentifier(variant.ID) {
			return invalid(field+".variant_id", "invalid variant id")
		}
		if _, ok := seen[variant.ID]; ok {
			return invalid(field+".variant_id", "duplicate variant id")
		}
		seen[variant.ID] = struct{}{}
		if len(variant.Exact) == 0 || len(variant.Exact) > maxVariantArguments {
			return invalid(field+".exact", "invalid exact argument count")
		}
		for j, argument := range variant.Exact {
			if argument == "" || len(argument) > maxArgumentBytes || !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
				return invalid(fmt.Sprintf("%s.exact[%d]", field, j), "invalid exact argument")
			}
		}
	}
	if spec.CWD.Kind != CWDPrivateEmpty {
		return invalid("command_profile.cwd.kind", "only private_empty is supported")
	}
	if spec.Environment.Inherit || len(spec.Environment.Allow) != 0 || len(spec.Environment.Fixed) != maxFixedEnvironment {
		return invalid("command_profile.env", "custom or inherited environment is not supported")
	}
	if spec.Limits.WallTimeout <= 0 || spec.Limits.WallTimeout > maxTimeout {
		return invalid("command_profile.limits.wall_timeout_ms", "timeout is outside the hard bound")
	}
	if spec.Limits.StdoutBytes <= 0 || spec.Limits.StdoutBytes > maxOutputBytes || spec.Limits.StderrBytes <= 0 || spec.Limits.StderrBytes > maxOutputBytes {
		return invalid("command_profile.limits", "output limit is outside the hard bound")
	}
	if spec.Limits.MaxProcesses != 1 || spec.Limits.MaxChildren != 0 {
		return invalid("command_profile.limits", "only one process and no children are supported")
	}
	if spec.Network.Mode != NetworkDeny || !spec.Network.RequireEnforcement {
		return invalid("command_profile.network", "network deny and enforcement are required")
	}
	if spec.Confirmation.Mode != ConfirmationPerCall || !spec.Confirmation.LocalOnly {
		return invalid("command_profile.confirmation", "only local per_call confirmation is supported")
	}
	if spec.Result.Type != ResultVersion || spec.Result.ReturnRawOutput {
		return invalid("command_profile.result", "only structured version output is supported")
	}
	return nil
}

func validateProfile(profile Profile) error {
	variants := profile.Variants()
	variantSpecs := make([]VariantSpec, len(variants))
	for i, variant := range variants {
		variantSpecs[i] = VariantSpec{ID: variant.id, Exact: variant.exact}
	}
	return validateSpec(Spec{
		ID: profile.id, Kind: profile.kind, Platform: profile.platform, Executable: profile.executable,
		Identity: profile.identity, Variants: variantSpecs, CWD: profile.cwd,
		Environment: profile.environment, Limits: profile.limits, Network: profile.network,
		Confirmation: profile.confirmation, Result: profile.result,
	})
}

func validWindowsExecutable(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	if len(value) < 4 || !isASCIIAlpha(value[0]) || value[1] != ':' || (value[2] != '\\' && value[2] != '/') {
		return false
	}
	if strings.HasPrefix(value, `\\`) || strings.ContainsAny(value, `*?"%`) || !strings.EqualFold(value[len(value)-4:], ".exe") {
		return false
	}
	for i := 2; i < len(value); i++ {
		if value[i] == ':' {
			return false
		}
	}
	for _, part := range strings.FieldsFunc(value[3:], func(r rune) bool { return r == '\\' || r == '/' }) {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
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

func invalid(field, reason string) error { return fmt.Errorf("%w: %s: %s", ErrInvalid, field, reason) }

// Keep encoding/json imported in this package's API surface intentionally. It
// is used by EnforcementCapability's methods and prevents accidental default
// serialization of the opaque capability in future refactors.
var _ json.Marshaler = EnforcementCapability{}
