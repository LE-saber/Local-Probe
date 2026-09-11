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

// Executor owns a fixed binding and local confirmation/enforcement handles.
// It is not safe to construct one from model-provided values.
type Executor struct {
	binding     Binding
	mode        commandprofile.DeveloperMode
	confirm     *confirmation.Manager
	enforcement commandprofile.EnforcementCapability
}

func New(binding Binding, mode commandprofile.DeveloperMode, confirmations *confirmation.Manager, enforcement commandprofile.EnforcementCapability) (*Executor, error) {
	if binding.ConnectionID == "" || binding.ProfileRevision == "" || binding.AcquireRevision == nil || confirmations == nil {
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
	}, nil
}

// Execute performs admission, consumes the one-time local confirmation, then
// asks the Windows probe to launch the exact profile executable and variant.
// It never accepts an executable, argv, cwd, environment or timeout from req.
func (e *Executor) Execute(ctx context.Context, req Request) (Result, error) {
	var result Result
	if e == nil || e.confirm == nil || e.binding.AcquireRevision == nil {
		return result, ErrInvalidBinding
	}
	if err := validateRequest(req); err != nil {
		return result, err
	}
	profile := e.binding.Profile
	if req.CommandID != profile.ID() {
		return result, ErrInvalidRequest
	}
	variant, ok := profile.Variant(req.VariantID)
	if !ok {
		return result, ErrInvalidRequest
	}
	release, ok := e.binding.AcquireRevision(e.binding.ProfileRevision)
	if !ok || release == nil {
		return result, ErrProfileChanged
	}
	defer release()
	// The profile/network gate is checked before the confirmation is consumed
	// so a permanently unavailable executor does not burn a user's token.
	if err := profile.Admit(e.mode, e.binding.ConnectionID, e.enforcement); err != nil {
		return result, fmt.Errorf("%w: %v", ErrNotAdmitted, stableAdmissionError(err))
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
		return result, ErrConfirmation
	}

	// This is the only probe descriptor construction in the production bridge.
	// The descriptor is private-state-bearing and cannot be built from a wire
	// request. The profile executable is audited immediately before execution.
	descriptor, err := probe.AuditExecutable(probe.ToolVersionGeneric, profile.Executable())
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrExecutionFailed, err)
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
		return result, err
	}
	return Result{CommandID: req.CommandID, VariantID: req.VariantID, Version: version.Version}, nil
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
