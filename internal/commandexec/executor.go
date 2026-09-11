// Package commandexec is the local-only bridge from an immutable command
// profile to the Windows fixed-action probe. It deliberately has no MCP
// adapter: a remote caller can never choose an executable, argv, cwd, env,
// timeout or network capability through this package.
package commandexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/confirmation"
	"github.com/LE-saber/Local-Probe/internal/probe"
)

var (
	ErrInvalidRequest  = errors.New("invalid command execution request")
	ErrInvalidBinding  = errors.New("invalid command execution binding")
	ErrProfileChanged  = errors.New("command profile revision changed")
	ErrNotAdmitted     = errors.New("command execution not admitted")
	ErrConfirmation    = errors.New("local confirmation rejected")
	ErrExecutionFailed = errors.New("command execution failed")
	ErrAuditFailed     = errors.New("command audit failed")
)

const maxRequestNonceBytes = 256

// Binding is constructed by trusted local configuration code. Profile and
// revision are captured together; AcquireRevision must hold the current
// configuration's read-side lease, so a replacement cannot race a launch.
type Binding struct {
	ConnectionID    string
	ProfileRevision string
	Profile         commandprofile.Profile
	// AcquireRevision must hold the configuration's read-side revision lease
	// until the returned release function is called. This closes the gap
	// between checking a revision and launching the fixed process.
	AcquireRevision func(expected string) (release func(), ok bool)
}

func NewBinding(connectionID, profileRevision string, profile commandprofile.Profile, acquireRevision func(expected string) (release func(), ok bool)) (Binding, error) {
	if !validIdentifier(connectionID) || profileRevision == "" || len(profileRevision) > 256 ||
		!utf8.ValidString(profileRevision) || strings.ContainsRune(profileRevision, 0) || acquireRevision == nil {
		return Binding{}, ErrInvalidBinding
	}
	return Binding{
		ConnectionID:    connectionID,
		ProfileRevision: profileRevision,
		Profile:         profile.Clone(),
		AcquireRevision: acquireRevision,
	}, nil
}

// Request contains only the model-safe selector fields plus an opaque
// capability minted by the trusted local confirmation manager. The capability
// intentionally cannot be JSON-marshaled, so a remote model cannot mint or
// replace it with a boolean.
type Request struct {
	CommandID    string
	VariantID    string
	RequestNonce string
	Confirmation confirmation.Capability
}

type Result struct {
	CommandID string
	VariantID string
	Version   string
}

// AuditRecorder is the narrow audit dependency used by Executor.  The
// concrete audit.CommandRecorder is the production implementation; keeping
// the interface here lets local tests inject a bounded failure recorder
// without exposing any command-line or token fields.
type AuditRecorder interface {
	RecordAdmission(audit.CommandContext) error
	RecordStart(audit.CommandContext) error
	RecordReject(audit.CommandContext, string) error
}

// Executor owns a fixed binding and local confirmation/enforcement handles.
// It is not safe to construct one from model-provided values.
type Executor struct {
	binding     Binding
	mode        commandprofile.DeveloperMode
	confirm     *confirmation.Manager
	enforcement commandprofile.EnforcementCapability
	audit       AuditRecorder
	admit       func(commandprofile.Profile, commandprofile.DeveloperMode, string, commandprofile.EnforcementCapability) error
	run         func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, error)
}

func New(binding Binding, mode commandprofile.DeveloperMode, confirmations *confirmation.Manager, enforcement commandprofile.EnforcementCapability, recorder AuditRecorder) (*Executor, error) {
	if binding.ConnectionID == "" || binding.ProfileRevision == "" || binding.AcquireRevision == nil || confirmations == nil || recorder == nil {
		return nil, ErrInvalidBinding
	}
	if err := mode.Validate(); err != nil {
		return nil, fmt.Errorf("%w: developer mode", ErrInvalidBinding)
	}
	return &Executor{
		binding:     Binding{ConnectionID: binding.ConnectionID, ProfileRevision: binding.ProfileRevision, Profile: binding.Profile.Clone(), AcquireRevision: binding.AcquireRevision},
		mode:        mode.Clone(),
		confirm:     confirmations,
		enforcement: enforcement,
		audit:       recorder,
		admit:       admitProfile,
		run:         runFixedVersion,
	}, nil
}

// Execute performs admission, records the admission, consumes the one-time
// local confirmation, records the start, then asks the Windows probe to launch
// the exact profile executable and variant. It never accepts an executable,
// argv, cwd, environment or timeout from req.
func (e *Executor) Execute(ctx context.Context, req Request) (Result, error) {
	var result Result
	if e == nil || e.confirm == nil || e.binding.AcquireRevision == nil || e.audit == nil || e.admit == nil || e.run == nil {
		return result, ErrInvalidBinding
	}
	initialAudit := e.auditContext(req, e.binding.Profile, e.networkState())
	if err := validateRequest(req); err != nil {
		return e.reject(initialAudit, ErrInvalidRequest, "invalid_request")
	}
	profile := e.binding.Profile
	if req.CommandID != profile.ID() {
		return e.reject(initialAudit, ErrInvalidRequest, "invalid_request")
	}
	// fixed_command is configuration/resolve-only in this increment. Typed
	// slot values are not represented in Request or bound into confirmation;
	// a future executor must bind a resolved-input digest and profile revision
	// before this gate can be relaxed. Reject before consuming confirmation or
	// invoking the runner so a fixed profile cannot execute accidentally.
	if profile.Kind() != commandprofile.KindVersionProbe {
		return e.reject(initialAudit, fmt.Errorf("%w: unsupported_profile", ErrNotAdmitted), "unsupported_profile")
	}
	variant, ok := profile.Variant(req.VariantID)
	if !ok {
		return e.reject(initialAudit, ErrInvalidRequest, "invalid_request")
	}
	release, ok := e.binding.AcquireRevision(e.binding.ProfileRevision)
	if !ok || release == nil {
		return e.reject(e.auditContext(req, profile, e.networkState()), ErrProfileChanged, "stale_version")
	}
	defer release()

	// The profile/network gate is checked before the confirmation is consumed
	// so a permanently unavailable executor does not burn a user's token.
	if err := e.admit(profile, e.mode, e.binding.ConnectionID, e.enforcement); err != nil {
		admissionErr := fmt.Errorf("%w: %v", ErrNotAdmitted, stableAdmissionError(err))
		return e.reject(e.auditContext(req, profile, e.networkState()), admissionErr, "denied")
	}
	auditContext := e.auditContext(req, profile, audit.NetworkEnforcementVerified)
	if err := e.audit.RecordAdmission(auditContext); err != nil {
		return e.auditFailure(auditContext, err)
	}

	confirmationRequest := confirmation.Request{
		ConnectionID:    e.binding.ConnectionID,
		ProfileID:       profile.ID(),
		ProfileRevision: e.binding.ProfileRevision,
		CommandID:       req.CommandID,
		VariantID:       req.VariantID,
		RequestNonce:    req.RequestNonce,
	}
	if err := e.confirm.Consume(req.Confirmation, confirmationRequest); err != nil {
		return e.reject(auditContext, ErrConfirmation, "denied")
	}
	if err := e.audit.RecordStart(auditContext); err != nil {
		return e.auditFailure(auditContext, err)
	}

	return e.run(ctx, profile, variant)
}

func admitProfile(profile commandprofile.Profile, mode commandprofile.DeveloperMode, connectionID string, enforcement commandprofile.EnforcementCapability) error {
	return profile.Admit(mode, connectionID, enforcement)
}

func runFixedVersion(ctx context.Context, profile commandprofile.Profile, variant commandprofile.Variant) (Result, error) {
	// This is the only probe descriptor construction in the production bridge.
	// The descriptor is private-state-bearing and cannot be built from a wire
	// request. The profile executable is audited immediately before execution.
	descriptor, err := probe.AuditExecutable(probe.ToolVersionGeneric, profile.Executable())
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrExecutionFailed, err)
	}
	limits := profile.Limits()
	policy := probe.VersionExecutionPolicy{
		WallTimeout: limits.WallTimeout,
		StdoutBytes: limits.StdoutBytes,
		StderrBytes: limits.StderrBytes,
		SHA256:      profile.Identity().SHA256,
	}
	version, err := probe.ToolVersionWithPolicy(ctx, descriptor, variant.Exact(), policy)
	if err != nil {
		return Result{}, err
	}
	return Result{CommandID: profile.ID(), VariantID: variant.ID(), Version: version.Version}, nil
}

func (e *Executor) auditContext(req Request, profile commandprofile.Profile, network audit.NetworkEnforcement) audit.CommandContext {
	identity := profile.Identity().SHA256
	return audit.CommandContext{
		ConnectionID:    e.binding.ConnectionID,
		ProfileID:       profile.ID(),
		ProfileRevision: e.binding.ProfileRevision,
		CommandID:       req.CommandID,
		VariantID:       req.VariantID,
		IdentityDigest:  identity,
		Network:         network,
	}
}

func (e *Executor) networkState() audit.NetworkEnforcement {
	if e != nil && e.enforcement.Verified() {
		return audit.NetworkEnforcementVerified
	}
	return audit.NetworkEnforcementUnavailable
}

func (e *Executor) reject(context audit.CommandContext, executionErr error, reason string) (Result, error) {
	if e != nil && e.audit != nil {
		if err := e.audit.RecordReject(context, reason); err != nil {
			return Result{}, fmt.Errorf("%w: %s", ErrAuditFailed, stableAuditError(err))
		}
	}
	return Result{}, executionErr
}

// auditFailure emits the synchronous security event best-effort and returns a
// path-free failure. A failed admission/start event is never followed by a
// process launch, even if rejection delivery also fails.
func (e *Executor) auditFailure(context audit.CommandContext, cause error) (Result, error) {
	if e != nil && e.audit != nil {
		_ = e.audit.RecordReject(context, "unavailable")
	}
	return Result{}, fmt.Errorf("%w: %s", ErrAuditFailed, stableAuditError(cause))
}

func stableAuditError(err error) string {
	switch {
	case errors.Is(err, audit.ErrQueueFull):
		return "queue_full"
	case errors.Is(err, audit.ErrClosed), errors.Is(err, audit.ErrStorage), errors.Is(err, audit.ErrRotation), errors.Is(err, audit.ErrRetention):
		return "unavailable"
	default:
		return "unavailable"
	}
}

func validateRequest(req Request) error {
	if !validIdentifier(req.CommandID) || !validIdentifier(req.VariantID) || req.RequestNonce == "" ||
		len(req.RequestNonce) > maxRequestNonceBytes || !utf8.ValidString(req.RequestNonce) || strings.ContainsRune(req.RequestNonce, 0) {
		return ErrInvalidRequest
	}
	if req.Confirmation.String() == "" {
		return ErrInvalidRequest
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

// Keep the admission error category path-free and stable. Detailed local
// diagnostics belong to a future audit adapter, not this execution result.
func stableAdmissionError(err error) string {
	switch {
	case errors.Is(err, commandprofile.ErrDeveloperModeDisabled):
		return "developer_mode_disabled"
	case errors.Is(err, commandprofile.ErrConnectionNotAllowed):
		return "connection_not_allowed"
	case errors.Is(err, commandprofile.ErrNetworkEnforcementRequired):
		return "network_enforcement_required"
	case errors.Is(err, commandprofile.ErrUnsupportedProfile):
		return "unsupported_profile"
	default:
		return "profile_rejected"
	}
}
