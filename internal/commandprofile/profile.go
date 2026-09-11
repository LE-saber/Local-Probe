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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	KindVersionProbe Kind     = "version_probe"
	KindFixedCommand Kind     = "fixed_command"
	PlatformWindows  Platform = "windows"

	ConfirmationPerCall ConfirmationMode = "per_call"
	NetworkDeny         NetworkMode      = "deny"
	CWDPrivateEmpty     CWDKind          = "private_empty"
	ResultVersion       ResultKind       = "version"
	ResultExitStatus    ResultKind       = "exit_status"

	maxProfiles           = 128
	maxVariantsPerProfile = 16
	maxVariantArguments   = 8
	maxArgumentBytes      = 256
	maxVariantTotalBytes  = maxVariantArguments * maxArgumentBytes
	maxRelativePathBytes  = 4096
	maxResolvedPathBytes  = 32767
	// Windows command-line size is checked again by the final launcher using
	// UTF-16 encoding and its exact escaping rules. This conservative byte
	// budget prevents the local resolver from producing an oversized argv.
	maxResolvedArgvBytes  = 32767
	maxSlotsPerProfile    = 32
	maxEnumValues         = 64
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
	ErrVariantNotFound            = errors.New("command profile variant not found")
	ErrMissingSlotValue           = errors.New("missing command profile slot value")
	ErrExtraSlotValue             = errors.New("extra command profile slot value")
	ErrPathResolverRequired       = errors.New("trusted path resolver is required")
	ErrPathResolution             = errors.New("trusted path resolution failed")
)

// Kind identifies the execution semantics of a profile. FixedCommand is only
// a configuration/argv-resolution kind in this increment; commandexec keeps
// it disabled until a separately reviewed launcher exists.
type Kind string

type Platform string
type ConfirmationMode string
type NetworkMode string
type CWDKind string
type ResultKind string

// SlotKind identifies the only dynamic value types accepted by a fixed
// command template. Values are typed before they reach ResolveVariant; this
// package never parses a shell command line.
type SlotKind string

const (
	SlotEnum             SlotKind = "enum"
	SlotBoundedInteger   SlotKind = "bounded_integer"
	SlotRootRelativePath SlotKind = "root_relative_path"
)

// SlotSpec declares one typed dynamic value. RootIDs is used only by
// root_relative_path slots and lists the locally configured roots that may be
// selected; the trusted PathResolver still owns authorization.
type SlotSpec struct {
	ID         string
	Kind       SlotKind
	EnumValues []string
	Min        int64
	Max        int64
	RootIDs    []string
}

// TemplateItemSpec is exactly one literal token or one slot reference. The
// two fields are deliberately separate so a template is always assembled as
// an argv slice, never as a shell string.
type TemplateItemSpec struct {
	Literal string
	SlotID  string
}

// SlotValue is the runtime typed value supplied to ResolveVariant. It is
// interpreted according to the declaration's Kind. Text is used for enum,
// Integer for bounded_integer, and RootID + RelativePath for path slots.
type SlotValue struct {
	Text         string
	Integer      int64
	RootID       string
	RelativePath string
}

// PathResolver is a trusted local resolver supplied by the caller. The
// resolver owns root authorization and OS path handling; commandprofile only
// validates the root-relative syntax and treats the returned string as one
// argv token.
type PathResolver func(rootID, relative string) (string, error)

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

// IdentitySpec is the executable identity policy. The booleans and the
// lowercase SHA256 pin are required for a version probe. The digest is a
// configuration-time approval and is checked against the audited Windows
// image handle immediately before launch.
type IdentitySpec struct {
	RequireRegular bool
	RejectReparse  bool
	SHA256         string
}

// VariantSpec is one locally configured argv variant. Exactly one of Exact or
// Template may be supplied. VersionProbe accepts only its historical one-item
// Exact form; FixedCommand may use either bounded form.
type VariantSpec struct {
	ID       string
	Exact    []string
	Template []TemplateItemSpec
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
	Slots        []SlotSpec
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
	slots        []Slot
	variants     []Variant
	cwd          CWDSpec
	environment  EnvironmentSpec
	limits       LimitsSpec
	network      NetworkSpec
	confirmation ConfirmationSpec
	result       ResultSpec
}

type Variant struct {
	id       string
	exact    []string
	template []templateItem
}

type Slot struct {
	id      string
	kind    SlotKind
	enum    []string
	min     int64
	max     int64
	rootIDs []string
}

type templateItem struct {
	literal string
	slot    string
}

func New(spec Spec) (Profile, error) {
	if err := validateSpec(spec); err != nil {
		return Profile{}, err
	}
	slots := make([]Slot, len(spec.Slots))
	for i, value := range spec.Slots {
		slots[i] = slotFromSpec(value)
	}
	variants := make([]Variant, len(spec.Variants))
	for i, variant := range spec.Variants {
		variants[i] = Variant{
			id:       variant.ID,
			exact:    append([]string(nil), variant.Exact...),
			template: templateFromSpecs(variant.Template),
		}
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
		slots:        slots,
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
	p.slots = append([]Slot(nil), p.slots...)
	for i := range p.slots {
		p.slots[i].enum = append([]string(nil), p.slots[i].enum...)
		p.slots[i].rootIDs = append([]string(nil), p.slots[i].rootIDs...)
	}
	p.variants = append([]Variant(nil), p.variants...)
	for i := range p.variants {
		p.variants[i].exact = append([]string(nil), p.variants[i].exact...)
		p.variants[i].template = append([]templateItem(nil), p.variants[i].template...)
	}
	p.environment.Allow = append([]string(nil), p.environment.Allow...)
	if p.environment.Fixed != nil {
		p.environment.Fixed = map[string]string{}
	}
	return p
}

func (v Variant) ID() string      { return v.id }
func (v Variant) Exact() []string { return append([]string(nil), v.exact...) }
func (v Variant) Template() []TemplateItemSpec {
	out := make([]TemplateItemSpec, len(v.template))
	for i, item := range v.template {
		out[i] = TemplateItemSpec{Literal: item.literal, SlotID: item.slot}
	}
	return out
}
func (v Variant) IsTemplate() bool       { return len(v.template) != 0 }
func (s Slot) ID() string                { return s.id }
func (s Slot) Kind() SlotKind            { return s.kind }
func (s Slot) EnumValues() []string      { return append([]string(nil), s.enum...) }
func (s Slot) Min() int64                { return s.min }
func (s Slot) Max() int64                { return s.max }
func (s Slot) RootIDs() []string         { return append([]string(nil), s.rootIDs...) }
func (p Profile) ID() string             { return p.id }
func (p Profile) Kind() Kind             { return p.kind }
func (p Profile) Platform() []Platform   { return append([]Platform(nil), p.platform...) }
func (p Profile) Executable() string     { return p.executable }
func (p Profile) Identity() IdentitySpec { return p.identity }
func (p Profile) Slots() []Slot {
	out := append([]Slot(nil), p.slots...)
	for i := range out {
		out[i].enum = append([]string(nil), out[i].enum...)
		out[i].rootIDs = append([]string(nil), out[i].rootIDs...)
	}
	return out
}
func (p Profile) CWD() CWDSpec { return p.cwd }
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
		variants[i].template = append([]templateItem(nil), variants[i].template...)
	}
	return variants
}

func (p Profile) Variant(id string) (Variant, bool) {
	for _, variant := range p.variants {
		if variant.id == id {
			return Variant{id: variant.id, exact: append([]string(nil), variant.exact...), template: append([]templateItem(nil), variant.template...)}, true
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
	if spec.Kind != KindVersionProbe && spec.Kind != KindFixedCommand {
		return invalid("command_profile.kind", "unsupported command profile kind")
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
	if len(spec.Identity.SHA256) != 64 {
		return invalid("command_profile.identity.sha256", "must be a 64-character lowercase SHA-256 digest")
	}
	if spec.Identity.SHA256 != strings.ToLower(spec.Identity.SHA256) {
		return invalid("command_profile.identity.sha256", "must use lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(spec.Identity.SHA256); err != nil {
		return invalid("command_profile.identity.sha256", "must be hexadecimal")
	}
	if len(spec.Slots) > maxSlotsPerProfile {
		return invalid("command_profile.argv.slots", "too many slots")
	}
	slots := make(map[string]SlotSpec, len(spec.Slots))
	for i, slot := range spec.Slots {
		field := fmt.Sprintf("command_profile.argv.slots[%d]", i)
		if err := validateSlotSpec(slot, field); err != nil {
			return err
		}
		if _, ok := slots[slot.ID]; ok {
			return invalid(field+".slot_id", "duplicate slot id")
		}
		slots[slot.ID] = slot
	}
	if spec.Kind == KindVersionProbe && len(spec.Slots) != 0 {
		return invalid("command_profile.argv.slots", "version_probe does not support slots")
	}
	if len(spec.Variants) == 0 || len(spec.Variants) > maxVariantsPerProfile {
		return invalid("command_profile.argv.variants", "invalid number of variants")
	}
	seen := make(map[string]struct{}, len(spec.Variants))
	usedSlots := make(map[string]struct{}, len(spec.Slots))
	for i, variant := range spec.Variants {
		field := fmt.Sprintf("command_profile.argv.variants[%d]", i)
		if !validIdentifier(variant.ID) {
			return invalid(field+".variant_id", "invalid variant id")
		}
		if _, ok := seen[variant.ID]; ok {
			return invalid(field+".variant_id", "duplicate variant id")
		}
		seen[variant.ID] = struct{}{}
		hasExact := len(variant.Exact) != 0
		hasTemplate := len(variant.Template) != 0
		if hasExact == hasTemplate {
			if spec.Kind == KindVersionProbe {
				return invalid(field, "version_probe requires exact argv")
			}
			return invalid(field, "exact and template are mutually exclusive and one is required")
		}
		if hasExact {
			if len(variant.Exact) > maxVariantArguments {
				return invalid(field+".exact", "too many arguments")
			}
			if spec.Kind == KindVersionProbe && len(variant.Exact) != 1 {
				return invalid(field+".exact", "version probe requires one fixed argument")
			}
			if err := validateArguments(variant.Exact, field+".exact", spec.Kind == KindVersionProbe); err != nil {
				return err
			}
		}
		if hasTemplate {
			if spec.Kind == KindVersionProbe {
				return invalid(field+".template", "version_probe does not support templates")
			}
			if len(variant.Template) > maxVariantArguments {
				return invalid(field+".template", "too many arguments")
			}
			totalTemplateBytes := 0
			for j, item := range variant.Template {
				itemField := fmt.Sprintf("%s.template[%d]", field, j)
				literal, slotID, err := normalizeTemplateItem(item)
				if err != nil {
					return invalid(itemField, err.Error())
				}
				if literal != "" {
					if err := validateArgument(literal, itemField+".literal"); err != nil {
						return err
					}
					totalTemplateBytes += len(literal)
					if totalTemplateBytes > maxVariantTotalBytes {
						return invalid(field+".template", "total argument bytes exceed limit")
					}
					continue
				}
				if _, ok := slots[slotID]; !ok {
					return invalid(itemField+".slot_id", "undeclared slot reference")
				}
				usedSlots[slotID] = struct{}{}
			}
		}
	}
	if spec.Kind == KindFixedCommand {
		for _, slot := range spec.Slots {
			if _, ok := usedSlots[slot.ID]; !ok {
				return invalid("command_profile.argv.slots", "slot is not referenced by a template")
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
	if spec.Kind == KindVersionProbe {
		if spec.Result.Type != ResultVersion || spec.Result.ReturnRawOutput {
			return invalid("command_profile.result", "only structured version output is supported")
		}
	} else if spec.Result.Type != ResultExitStatus || spec.Result.ReturnRawOutput {
		return invalid("command_profile.result", "fixed_command requires structured exit_status output")
	}
	return nil
}

func validateSlotSpec(slot SlotSpec, field string) error {
	if !validIdentifier(slot.ID) {
		return invalid(field+".slot_id", "invalid slot id")
	}
	switch slot.Kind {
	case SlotEnum:
		values := slot.EnumValues
		if len(values) == 0 || len(values) > maxEnumValues {
			return invalid(field+".enum_values", "invalid number of enum values")
		}
		seen := make(map[string]struct{}, len(values))
		totalBytes := 0
		for i, value := range values {
			if err := validateArgument(value, fmt.Sprintf("%s.enum_values[%d]", field, i)); err != nil {
				return err
			}
			if _, ok := seen[value]; ok {
				return invalid(fmt.Sprintf("%s.enum_values[%d]", field, i), "duplicate enum value")
			}
			seen[value] = struct{}{}
			totalBytes += len(value)
			if totalBytes > maxVariantTotalBytes {
				return invalid(field+".enum_values", "total enum value bytes exceed limit")
			}
		}
		if slot.Min != 0 || slot.Max != 0 || len(slot.RootIDs) != 0 {
			return invalid(field, "enum has unexpected bounds or root")
		}
	case SlotBoundedInteger:
		if slot.Min > slot.Max {
			return invalid(field, "minimum exceeds maximum")
		}
		if slot.Min == 0 && slot.Max == 0 {
			// A zero-width [0,0] range is useful and explicit in Go, but the
			// JSON parser requires both fields to be present for this kind.
			// Keep it valid here; runtime values are still bounded.
		}
		if len(slot.RootIDs) != 0 || slot.EnumValues != nil {
			return invalid(field, "bounded_integer has unexpected enum values or root")
		}
	case SlotRootRelativePath:
		if len(slot.RootIDs) == 0 || len(slot.RootIDs) > maxSlotsPerProfile {
			return invalid(field+".root_ids", "at least one root id is required")
		}
		seenRoots := make(map[string]struct{}, len(slot.RootIDs))
		for i, rootID := range slot.RootIDs {
			if !validIdentifier(rootID) {
				return invalid(fmt.Sprintf("%s.root_ids[%d]", field, i), "invalid root id")
			}
			if _, ok := seenRoots[rootID]; ok {
				return invalid(fmt.Sprintf("%s.root_ids[%d]", field, i), "duplicate root id")
			}
			seenRoots[rootID] = struct{}{}
		}
		if slot.Min != 0 || slot.Max != 0 || slot.EnumValues != nil {
			return invalid(field, "root_relative_path has unexpected bounds or enum values")
		}
	default:
		return invalid(field+".kind", "unsupported slot kind")
	}
	return nil
}

func validateArguments(arguments []string, field string, versionOnly bool) error {
	if len(arguments) == 0 || len(arguments) > maxVariantArguments {
		return invalid(field, "invalid number of arguments")
	}
	total := 0
	for i, argument := range arguments {
		if err := validateArgument(argument, fmt.Sprintf("%s[%d]", field, i)); err != nil {
			return err
		}
		total += len(argument)
		if versionOnly && !validVersionArgument(argument) {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "unsupported version argument")
		}
	}
	if total > maxVariantTotalBytes {
		return invalid(field, "total argument bytes exceed limit")
	}
	return nil
}

func validateArgument(argument, field string) error {
	if argument == "" || len(argument) > maxArgumentBytes || !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
		return invalid(field, "invalid argument")
	}
	for _, r := range argument {
		if unicode.IsControl(r) {
			return invalid(field, "control characters are not allowed")
		}
	}
	return nil
}

func normalizeTemplateItem(item TemplateItemSpec) (literal, slotID string, err error) {
	slotID = item.SlotID
	if (item.Literal == "") == (slotID == "") {
		return "", "", errors.New("template item must contain exactly one literal or slot")
	}
	return item.Literal, slotID, nil
}

func slotFromSpec(spec SlotSpec) Slot {
	return Slot{id: spec.ID, kind: spec.Kind, enum: append([]string(nil), spec.EnumValues...), min: spec.Min, max: spec.Max, rootIDs: append([]string(nil), spec.RootIDs...)}
}

func templateFromSpecs(values []TemplateItemSpec) []templateItem {
	if values == nil {
		return nil
	}
	out := make([]templateItem, len(values))
	for i, value := range values {
		literal, slotID, _ := normalizeTemplateItem(value)
		out[i] = templateItem{literal: literal, slot: slotID}
	}
	return out
}

// ResolveVariant validates the selected variant's typed inputs and returns a
// fresh argv slice. It never starts a process or performs OS path lookup.
// Path authorization and conversion to one argv token are delegated to the
// trusted resolver supplied by the local caller.
func (p Profile) ResolveVariant(variantID string, values map[string]SlotValue, resolver PathResolver) ([]string, error) {
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	variant, ok := p.Variant(variantID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrVariantNotFound, variantID)
	}
	if len(values) > maxSlotsPerProfile {
		return nil, fmt.Errorf("%w: too many values", ErrInvalid)
	}
	expected := make(map[string]Slot, len(variant.template))
	for _, item := range variant.template {
		if item.slot == "" {
			continue
		}
		for _, slot := range p.slots {
			if slot.id == item.slot {
				expected[item.slot] = slot
				break
			}
		}
	}
	for slotID := range expected {
		if _, ok := values[slotID]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissingSlotValue, slotID)
		}
	}
	for slotID := range values {
		if _, ok := expected[slotID]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrExtraSlotValue, slotID)
		}
	}

	if len(variant.exact) != 0 {
		return append([]string(nil), variant.exact...), nil
	}
	argv := make([]string, 0, len(variant.template))
	resolvedSlots := make(map[string]string, len(expected))
	totalBytes := 0
	for _, item := range variant.template {
		token := item.literal
		pathToken := false
		if item.slot != "" {
			slot := expected[item.slot]
			if cached, ok := resolvedSlots[item.slot]; ok {
				token = cached
			} else {
				value, err := resolveSlot(slot, values[item.slot], resolver)
				if err != nil {
					return nil, fmt.Errorf("%w: slot %s", err, item.slot)
				}
				resolvedSlots[item.slot] = value
				token = value
			}
			pathToken = slot.kind == SlotRootRelativePath
		}
		if pathToken {
			if err := validateResolvedPathToken(token); err != nil {
				return nil, err
			}
		} else if err := validateArgument(token, "resolved argv"); err != nil {
			return nil, err
		}
		totalBytes += len(token)
		if totalBytes > maxResolvedArgvBytes {
			return nil, fmt.Errorf("%w: resolved argv exceeds byte limit", ErrInvalid)
		}
		argv = append(argv, token)
	}
	return argv, nil
}

func resolveSlot(slot Slot, value SlotValue, resolver PathResolver) (string, error) {
	switch slot.kind {
	case SlotEnum:
		if value.Integer != 0 || value.RootID != "" || value.RelativePath != "" {
			return "", fmt.Errorf("%w: enum value contains fields for another slot kind", ErrInvalid)
		}
		if err := validateArgument(value.Text, "enum value"); err != nil {
			return "", err
		}
		for _, allowed := range slot.enum {
			if value.Text == allowed {
				return value.Text, nil
			}
		}
		return "", fmt.Errorf("%w: enum value is not allowed", ErrInvalid)
	case SlotBoundedInteger:
		if value.Text != "" || value.RootID != "" || value.RelativePath != "" {
			return "", fmt.Errorf("%w: integer value contains fields for another slot kind", ErrInvalid)
		}
		if value.Integer < slot.min || value.Integer > slot.max {
			return "", fmt.Errorf("%w: integer is outside range", ErrInvalid)
		}
		return strconv.FormatInt(value.Integer, 10), nil
	case SlotRootRelativePath:
		if value.Text != "" || value.Integer != 0 {
			return "", fmt.Errorf("%w: path value contains fields for another slot kind", ErrInvalid)
		}
		if !validRootRelativePath(value.RootID, value.RelativePath) {
			return "", fmt.Errorf("%w: invalid root-relative path", ErrInvalid)
		}
		allowed := false
		for _, rootID := range slot.rootIDs {
			if rootID == value.RootID {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("%w: root is not declared for slot", ErrInvalid)
		}
		if resolver == nil {
			return "", ErrPathResolverRequired
		}
		resolved, err := resolver(value.RootID, value.RelativePath)
		if err != nil {
			return "", ErrPathResolution
		}
		if err := validateResolvedPathToken(resolved); err != nil {
			return "", err
		}
		return resolved, nil
	default:
		return "", fmt.Errorf("%w: unsupported slot kind", ErrInvalid)
	}
}

func validRootRelativePath(rootID, relative string) bool {
	if !validIdentifier(rootID) || relative == "" || len(relative) > maxRelativePathBytes || !utf8.ValidString(relative) || strings.HasPrefix(relative, "/") || strings.ContainsAny(relative, `\:`) || strings.Contains(relative, "//") {
		return false
	}
	for _, r := range relative {
		if r == 0 || unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(relative, "/") {
		if !validWindowsRelativePart(part) {
			return false
		}
	}
	return true
}

func validateResolvedPathToken(value string) error {
	if value == "" || len(value) > maxResolvedPathBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return invalid("resolved path", "invalid path token")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return invalid("resolved path", "control characters are not allowed")
		}
	}
	return nil
}

func validWindowsRelativePart(part string) bool {
	if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, `<>"|*?[]`) {
		return false
	}
	base := part
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return false
	}
	upper := strings.ToUpper(base)
	if isNumberedDOSDevice(upper, "COM") || isNumberedDOSDevice(upper, "LPT") {
		return false
	}
	return true
}

func isNumberedDOSDevice(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	switch strings.TrimPrefix(value, prefix) {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	default:
		return false
	}
}

func validVersionArgument(argument string) bool {
	switch argument {
	case "-v", "--version", "version":
		return true
	default:
		return false
	}
}

func validateProfile(profile Profile) error {
	variants := profile.Variants()
	variantSpecs := make([]VariantSpec, len(variants))
	for i, variant := range variants {
		items := variant.Template()
		template := make([]TemplateItemSpec, len(items))
		for j, item := range items {
			template[j] = TemplateItemSpec{Literal: item.Literal, SlotID: item.SlotID}
		}
		variantSpecs[i] = VariantSpec{ID: variant.id, Exact: variant.exact, Template: template}
	}
	slotSpecs := make([]SlotSpec, len(profile.slots))
	for i, slot := range profile.slots {
		slotSpecs[i] = SlotSpec{ID: slot.id, Kind: slot.kind, EnumValues: append([]string(nil), slot.enum...), Min: slot.min, Max: slot.max, RootIDs: append([]string(nil), slot.rootIDs...)}
	}
	return validateSpec(Spec{
		ID: profile.id, Kind: profile.kind, Platform: profile.platform, Executable: profile.executable,
		Identity: profile.identity, Slots: slotSpecs, Variants: variantSpecs, CWD: profile.cwd,
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
