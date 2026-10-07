package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/confirmation"
)

var (
	// ErrPreparedInput identifies a value that was not returned by this
	// Executor's local Prepare boundary, or no longer matches its immutable
	// profile binding. It is deliberately separate from ErrInvalidRequest so
	// a local caller can diagnose a stale/foreign prepared value without
	// exposing any argv or path details.
	ErrPreparedInput = errors.New("invalid prepared command input")
	// ErrPrepareNotSupported is returned when a caller asks the local prepare
	// boundary to resolve a profile kind that has no typed-input contract.
	ErrPrepareNotSupported = errors.New("command input preparation not supported")
)

const maxPrepareValues = 32

// PrepareRequest is a local-only typed input request. It contains values for
// declared slots, never a command line or caller-owned argv. Resolver is a
// trusted local callback for root_relative_path slots; it must perform root
// authorization and final OS identity checks before returning one argv token.
// The callback is intentionally not accepted by any MCP or JSON adapter.
type PrepareRequest struct {
	CommandID string
	VariantID string
	Values    map[string]commandprofile.SlotValue
	Resolver  commandprofile.PathResolver
}

// PreparedInput is an immutable, local-only result of typed input
// preparation. Its private owner key makes a zero value, a JSON object, or a
// value prepared by a different Executor invalid. Argv and Digest are
// exposed only as defensive copies/commitments for a trusted local UI; they
// are not audit fields and are never accepted as a wire request.
type PreparedInput struct {
	owner           *preparedKey
	profileID       string
	profileRevision string
	commandID       string
	variantID       string
	resolved        commandprofile.ResolvedInput
}

// preparedKey is unique to one Executor instance. It has no serializable
// state and is never returned to callers.
type preparedKey struct{ marker byte }

// empty reports whether this is the zero value. It is used only to preserve
// the existing version-probe Request shape; fixed-command execution rejects
// both an empty and a malformed prepared input before confirmation.
func (p PreparedInput) empty() bool { return p.owner == nil }

// Valid reports whether the value has the minimum local shape of a prepared
// input. It does not assert ownership by a particular Executor; Execute,
// Confirm and BuildRequest perform that stronger check.
func (p PreparedInput) Valid() bool {
	return p.owner != nil && validPreparedSelector(p.profileID) &&
		validPreparedSelector(p.profileRevision) && validPreparedSelector(p.commandID) &&
		validPreparedSelector(p.variantID) && validDigest(p.resolved.Digest()) &&
		len(p.resolved.Argv()) > 0
}

// CommandID returns the bound command selector, or an empty string for an
// invalid/zero value.
func (p PreparedInput) CommandID() string {
	if !p.Valid() {
		return ""
	}
	return p.commandID
}

// VariantID returns the bound variant selector, or an empty string for an
// invalid/zero value.
func (p PreparedInput) VariantID() string {
	if !p.Valid() {
		return ""
	}
	return p.variantID
}

// ProfileRevision returns the configuration revision captured during
// preparation, or an empty string for an invalid/zero value.
func (p PreparedInput) ProfileRevision() string {
	if !p.Valid() {
		return ""
	}
	return p.profileRevision
}

// Digest returns the canonical resolved-input commitment, or an empty string
// for an invalid/zero value. The digest is suitable for binding to a local
// confirmation capability, not for identifying raw arguments in logs.
func (p PreparedInput) Digest() string {
	if !p.Valid() {
		return ""
	}
	return p.resolved.Digest()
}

// Argv returns a defensive copy of the locally resolved argv. The returned
// slice is for local preview/diagnostics only; the current fixed-command
// executor deliberately does not launch it.
func (p PreparedInput) Argv() []string {
	if !p.Valid() {
		return nil
	}
	return p.resolved.Argv()
}

// MarshalJSON prevents a prepared input from becoming a replayable remote
// authorization object. A future local IPC adapter must define an explicit,
// authenticated representation instead of relying on default JSON.
func (PreparedInput) MarshalJSON() ([]byte, error) {
	return nil, errors.New("prepared command input is local-only")
}

// UnmarshalJSON prevents callers from forging the private owner key or the
// resolved-input digest through a JSON payload.
func (*PreparedInput) UnmarshalJSON([]byte) error {
	return errors.New("prepared command input must be created by local preparation")
}

// Prepare resolves a fixed_command variant from typed slot values while the
// binding's profile revision lease is held. It never invokes a process,
// network backend, shell, or launcher. A root-relative path is accepted only
// through the trusted Resolver callback; commandprofile still validates the
// returned token, while the callback owns root/reparse/final-identity proof.
func (e *Executor) Prepare(ctx context.Context, req PrepareRequest) (PreparedInput, error) {
	if e == nil || e.confirm == nil || e.binding.AcquireRevision == nil || e.preparedKey == nil {
		return PreparedInput{}, ErrInvalidBinding
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePrepareRequest(req); err != nil {
		return PreparedInput{}, err
	}
	profile := e.binding.Profile
	if req.CommandID != profile.ID() {
		return PreparedInput{}, ErrInvalidRequest
	}
	if profile.Kind() != commandprofile.KindFixedCommand {
		return PreparedInput{}, ErrPrepareNotSupported
	}
	if err := e.localPrepareAdmission(); err != nil {
		return PreparedInput{}, err
	}
	if err := contextFailureForPrepare(ctx); err != nil {
		return PreparedInput{}, err
	}

	// Copy the scalar map before entering the revision-protected resolution
	// window. The prepared result never retains caller-owned maps or slices.
	values := cloneSlotValues(req.Values)
	release, ok := e.binding.AcquireRevision(e.binding.ProfileRevision)
	if !ok || release == nil {
		return PreparedInput{}, ErrProfileChanged
	}
	defer release()
	if err := contextFailureForPrepare(ctx); err != nil {
		return PreparedInput{}, err
	}
	resolved, err := profile.ResolveInput(req.VariantID, values, req.Resolver)
	if err != nil {
		return PreparedInput{}, err
	}
	if err := contextFailureForPrepare(ctx); err != nil {
		return PreparedInput{}, err
	}
	prepared := PreparedInput{
		owner:           e.preparedKey,
		profileID:       profile.ID(),
		profileRevision: e.binding.ProfileRevision,
		commandID:       req.CommandID,
		variantID:       req.VariantID,
		resolved:        resolved,
	}
	if !prepared.Valid() {
		return PreparedInput{}, ErrPreparedInput
	}
	return prepared, nil
}

// Confirm mints a short-lived, one-time capability bound to this prepared
// input's digest and the current profile revision. It reacquires the
// revision lease before minting, so a configuration replacement between
// Prepare and local confirmation cannot authorize an old input. Confirm does
// not consume the capability; Execute consumes it only after its own
// admission checks. The fixed-command execution path remains fail-closed in
// this increment, so a caller can exercise prepare/confirm semantics without
// enabling a launcher.
func (e *Executor) Confirm(prepared PreparedInput, requestNonce string, ttl time.Duration) (confirmation.Capability, error) {
	if e == nil || e.confirm == nil || e.binding.AcquireRevision == nil || e.preparedKey == nil {
		return confirmation.Capability{}, ErrInvalidBinding
	}
	if err := validateRequestNonce(requestNonce); err != nil {
		return confirmation.Capability{}, err
	}
	if err := e.validatePrepared(prepared); err != nil {
		return confirmation.Capability{}, err
	}
	if err := e.localPrepareAdmission(); err != nil {
		return confirmation.Capability{}, err
	}
	release, ok := e.binding.AcquireRevision(e.binding.ProfileRevision)
	if !ok || release == nil {
		return confirmation.Capability{}, ErrProfileChanged
	}
	defer release()
	request := confirmation.Request{
		ConnectionID:        e.binding.ConnectionID,
		ProfileID:           prepared.profileID,
		ProfileRevision:     prepared.profileRevision,
		CommandID:           prepared.commandID,
		VariantID:           prepared.variantID,
		RequestNonce:        requestNonce,
		ResolvedInputDigest: prepared.resolved.Digest(),
	}
	return e.confirm.Mint(request, ttl)
}

// BuildRequest creates the execution request that carries the immutable
// prepared input and its opaque confirmation capability. Callers do not
// need to copy or reconstruct argv. The current fixed executor still rejects
// the request before consuming the token because its final resolver,
// launcher, and OS network enforcement are not yet available.
func (e *Executor) BuildRequest(prepared PreparedInput, requestNonce string, capability confirmation.Capability) (Request, error) {
	if e == nil || e.confirm == nil || e.preparedKey == nil {
		return Request{}, ErrInvalidBinding
	}
	if err := validateRequestNonce(requestNonce); err != nil {
		return Request{}, err
	}
	if err := e.validatePrepared(prepared); err != nil {
		return Request{}, err
	}
	if capability.String() == "" {
		return Request{}, ErrConfirmation
	}
	return Request{
		CommandID:    prepared.commandID,
		VariantID:    prepared.variantID,
		RequestNonce: requestNonce,
		Confirmation: capability,
		Prepared:     prepared,
	}, nil
}

// ConfirmRequest is a convenience for trusted local UI/CLI code. It performs
// Confirm followed by BuildRequest, preserving the same digest/revision
// bindings and returning a request ready for the local Execute boundary.
func (e *Executor) ConfirmRequest(prepared PreparedInput, requestNonce string, ttl time.Duration) (Request, error) {
	capability, err := e.Confirm(prepared, requestNonce, ttl)
	if err != nil {
		return Request{}, err
	}
	return e.BuildRequest(prepared, requestNonce, capability)
}

func (e *Executor) validatePrepared(prepared PreparedInput) error {
	if e == nil || e.preparedKey == nil || !prepared.Valid() || prepared.owner != e.preparedKey {
		return ErrPreparedInput
	}
	profile := e.binding.Profile
	if profile.Kind() != commandprofile.KindFixedCommand ||
		prepared.profileID != profile.ID() || prepared.commandID != profile.ID() ||
		prepared.profileRevision != e.binding.ProfileRevision {
		return ErrPreparedInput
	}
	variant, ok := profile.Variant(prepared.variantID)
	if !ok || !variant.IsTemplate() && len(variant.Exact()) == 0 {
		return ErrPreparedInput
	}
	return nil
}

// localPrepareAdmission applies the non-network local policy before the
// prepare/confirm boundary. Network enforcement is intentionally not checked
// here: a prepared plan is inert, while Execute still applies the complete
// profile admission gate before any version-probe launch. fixed_command is
// rejected even with a test capability until the production boundaries are
// complete.
func (e *Executor) localPrepareAdmission() error {
	if err := e.mode.Validate(); err != nil {
		return fmt.Errorf("%w: profile", ErrInvalidBinding)
	}
	if !e.mode.Enabled() {
		return fmt.Errorf("%w: developer_mode_disabled", ErrNotAdmitted)
	}
	if !e.mode.AllowsConnection(e.binding.ConnectionID) {
		return fmt.Errorf("%w: connection_not_allowed", ErrNotAdmitted)
	}
	return nil
}

func validatePrepareRequest(req PrepareRequest) error {
	if !validIdentifier(req.CommandID) || !validIdentifier(req.VariantID) {
		return ErrInvalidRequest
	}
	if len(req.Values) > maxPrepareValues {
		return fmt.Errorf("%w: too many typed values", ErrInvalidRequest)
	}
	return nil
}

func validateRequestNonce(value string) error {
	if value == "" || len(value) > maxRequestNonceBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return ErrInvalidRequest
	}
	return nil
}

func cloneSlotValues(values map[string]commandprofile.SlotValue) map[string]commandprofile.SlotValue {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]commandprofile.SlotValue, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func contextFailureForPrepare(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	switch ctx.Err() {
	case context.Canceled:
		return context.Canceled
	case context.DeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func validPreparedSelector(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

var _ json.Marshaler = PreparedInput{}
