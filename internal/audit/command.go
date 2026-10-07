package audit

// CommandContext is the complete safe identity carried by a command audit
// event. It contains opaque rule identifiers, not an executable path or an
// argv template. IdentityDigest is a lower-case SHA-256-sized digest supplied
// by the trusted local launcher; it is never derived from model input here.
type CommandContext struct {
	ConnectionID    string
	ProfileID       string
	ProfileRevision string
	CommandID       string
	VariantID       string
	IdentityDigest  string
	Network         NetworkEnforcement
}

// CommandResult contains bounded process outcome metadata. The process
// output is intentionally represented only by byte counts.
type CommandResult struct {
	ExitCode    *int64
	TimedOut    bool
	Cancelled   bool
	DurationMS  int64
	StdoutBytes int64
	StderrBytes int64
	ErrorCode   string
}

// CommandRecorder exposes the four fixed command event producers. It does
// not accept an action, command line, environment, path, or response body.
// The wrapped Recorder must remain safe for concurrent use, as Sink is.
type CommandRecorder struct {
	sink Recorder
}

func NewCommandRecorder(sink Recorder) *CommandRecorder {
	return &CommandRecorder{sink: sink}
}

// RecordAdmission records a successful command admission. Admission is only
// auditable when network enforcement has been verified.
func (r *CommandRecorder) RecordAdmission(ctx CommandContext) error {
	if err := validateCommandContext(ctx, true, true); err != nil {
		return err
	}
	return r.emit(Event{
		Component:          ComponentCommand,
		EventType:          EventCommandAdmission,
		Action:             CommandActionAdmission,
		Severity:           SeverityInfo,
		Outcome:            OutcomeSucceeded,
		ConnectionID:       ctx.ConnectionID,
		ProfileID:          ctx.ProfileID,
		ProfileRevision:    ctx.ProfileRevision,
		CommandID:          ctx.CommandID,
		VariantID:          ctx.VariantID,
		IdentityDigest:     ctx.IdentityDigest,
		NetworkEnforcement: ctx.Network,
	}, false)
}

// RecordStart records dispatch intent for an already admitted command,
// immediately before the launcher is invoked. It is separate from admission
// but is not proof that the operating system created or resumed a process;
// command.result records the observable terminal outcome.
func (r *CommandRecorder) RecordStart(ctx CommandContext) error {
	if err := validateCommandContext(ctx, true, true); err != nil {
		return err
	}
	return r.emit(Event{
		Component:          ComponentCommand,
		EventType:          EventCommandStart,
		Action:             CommandActionStart,
		Severity:           SeverityInfo,
		Outcome:            OutcomeStarted,
		ConnectionID:       ctx.ConnectionID,
		ProfileID:          ctx.ProfileID,
		ProfileRevision:    ctx.ProfileRevision,
		CommandID:          ctx.CommandID,
		VariantID:          ctx.VariantID,
		IdentityDigest:     ctx.IdentityDigest,
		NetworkEnforcement: ctx.Network,
	}, false)
}

// RecordResult records a bounded process result. An unknown or malformed
// error label becomes the stable "unavailable" code and is never copied to
// disk. A successful exit with lost network enforcement is downgraded so an
// audit record cannot claim a verified command result.
func (r *CommandRecorder) RecordResult(ctx CommandContext, result CommandResult) error {
	if err := validateCommandContext(ctx, true, false); err != nil {
		return err
	}
	if err := validateCommandResult(result); err != nil {
		return err
	}

	outcome := OutcomeSucceeded
	errorCode := normalizeCommandError(result.ErrorCode)
	if err := validateExplicitResultCode(result, errorCode, ctx.Network); err != nil {
		return err
	}
	switch {
	case result.TimedOut:
		outcome = OutcomeFailed
		errorCode = "deadline_exceeded"
	case result.Cancelled:
		outcome = OutcomeFailed
		errorCode = "cancelled"
	case result.ExitCode == nil:
		if result.ErrorCode == "" || !validNoExitResultCode(errorCode) {
			return ErrInvalidEvent
		}
		outcome = OutcomeFailed
	case *result.ExitCode != 0:
		outcome = OutcomeFailed
		// A natural non-zero exit is represented by the exit code itself.
		// Ignore any caller text or contradictory category rather than writing
		// it to the audit record.
		errorCode = "child_exit"
	case result.ErrorCode == "invalid_output":
		outcome = OutcomeFailed
		errorCode = "invalid_output"
	case ctx.Network != NetworkEnforcementVerified:
		outcome = OutcomeDegraded
		errorCode = "unavailable"
	default:
		errorCode = ""
	}

	event := Event{
		Component:          ComponentCommand,
		EventType:          EventCommandResult,
		Action:             CommandActionResult,
		Severity:           SeverityInfo,
		Outcome:            outcome,
		ErrorCode:          errorCode,
		DurationMS:         result.DurationMS,
		ConnectionID:       ctx.ConnectionID,
		ProfileID:          ctx.ProfileID,
		ProfileRevision:    ctx.ProfileRevision,
		CommandID:          ctx.CommandID,
		VariantID:          ctx.VariantID,
		IdentityDigest:     ctx.IdentityDigest,
		ExitCode:           cloneInt64Pointer(result.ExitCode),
		TimedOut:           result.TimedOut,
		Cancelled:          result.Cancelled,
		CommandBytes:       CommandBytes{Stdout: result.StdoutBytes, Stderr: result.StderrBytes},
		NetworkEnforcement: ctx.Network,
	}
	return r.emit(event, false)
}

func validateExplicitResultCode(result CommandResult, normalized string, network NetworkEnforcement) error {
	if result.TimedOut {
		if result.ErrorCode != "" && (!validErrorCode(result.ErrorCode) || normalized != "deadline_exceeded") {
			return ErrInvalidEvent
		}
		return nil
	}
	if result.Cancelled {
		if result.ErrorCode != "" && (!validErrorCode(result.ErrorCode) || normalized != "cancelled") {
			return ErrInvalidEvent
		}
		return nil
	}
	if result.ExitCode == nil {
		if normalized == "deadline_exceeded" || normalized == "cancelled" {
			return ErrInvalidEvent
		}
		if result.ErrorCode != "" && validErrorCode(result.ErrorCode) && !validNoExitResultCode(normalized) {
			return ErrInvalidEvent
		}
		return nil
	}
	if *result.ExitCode == 0 {
		if result.ErrorCode == "invalid_output" {
			return nil
		}
		if validErrorCode(result.ErrorCode) && (result.ErrorCode != "unavailable" || network == NetworkEnforcementVerified) {
			return ErrInvalidEvent
		}
		return nil
	}
	if validErrorCode(result.ErrorCode) && result.ErrorCode != "child_exit" {
		return ErrInvalidEvent
	}
	return nil
}

// RecordReject records a command admission or policy rejection as a security
// event. EmitSecurity is deliberately used so a full asynchronous queue cannot
// turn a safety decision into an unlogged best-effort event.
func (r *CommandRecorder) RecordReject(ctx CommandContext, reasonCode string) error {
	ctx, invalidContext := sanitizeRejectContext(ctx)
	if invalidContext {
		// The rejection itself is the security evidence. Invalid selectors must
		// never prevent that evidence from reaching the synchronous sink.
		reasonCode = "invalid_request"
	}
	return r.emit(Event{
		Class:              ClassSecurity,
		Component:          ComponentCommand,
		EventType:          EventCommandReject,
		Action:             CommandActionReject,
		Severity:           SeverityWarn,
		Outcome:            OutcomeRejected,
		ErrorCode:          normalizeCommandError(reasonCode),
		ConnectionID:       ctx.ConnectionID,
		ProfileID:          ctx.ProfileID,
		ProfileRevision:    ctx.ProfileRevision,
		CommandID:          ctx.CommandID,
		VariantID:          ctx.VariantID,
		IdentityDigest:     ctx.IdentityDigest,
		NetworkEnforcement: ctx.Network,
	}, true)
}

// sanitizeRejectContext keeps the security event useful without copying an
// invalid selector, path-like value, digest, or network state. Reject events
// intentionally allow omitted selectors; the fixed event type and
// invalid_request code carry the safe outcome.
func sanitizeRejectContext(value CommandContext) (CommandContext, bool) {
	invalid := false
	for _, field := range []*string{
		&value.ConnectionID, &value.ProfileID, &value.ProfileRevision, &value.CommandID, &value.VariantID,
	} {
		if err := validateRuleID(*field, false); err != nil {
			*field = ""
			invalid = true
		}
	}
	if value.IdentityDigest != "" {
		if err := validateDigest(value.IdentityDigest); err != nil {
			value.IdentityDigest = ""
			invalid = true
		}
	}
	if err := validateNetworkEnforcement(value.Network); err != nil {
		value.Network = NetworkEnforcementNotChecked
		invalid = true
	}
	return value, invalid
}

func (r *CommandRecorder) emit(event Event, security bool) error {
	if r == nil || r.sink == nil {
		return ErrClosed
	}
	if security {
		return r.sink.EmitSecurity(event)
	}
	return r.sink.Emit(event)
}

func validateCommandContext(value CommandContext, requireIdentity, requireVerifiedNetwork bool) error {
	for _, field := range []string{value.ConnectionID, value.ProfileID, value.ProfileRevision, value.CommandID, value.VariantID} {
		if err := validateRuleID(field, false); err != nil {
			return err
		}
	}
	if value.IdentityDigest == "" {
		if requireIdentity {
			return ErrInvalidEvent
		}
	} else if err := validateDigest(value.IdentityDigest); err != nil {
		return err
	}
	if err := validateNetworkEnforcement(value.Network); err != nil {
		return err
	}
	if requireVerifiedNetwork && value.Network != NetworkEnforcementVerified {
		return ErrInvalidEvent
	}
	return nil
}

func validateCommandResult(value CommandResult) error {
	if value.DurationMS < 0 || value.DurationMS > maxDuration {
		return ErrInvalidEvent
	}
	if value.StdoutBytes < 0 || value.StdoutBytes > maxCounter || value.StderrBytes < 0 || value.StderrBytes > maxCounter {
		return ErrInvalidEvent
	}
	if value.StdoutBytes > maxCounter-value.StderrBytes {
		return ErrInvalidEvent
	}
	if value.TimedOut && value.Cancelled {
		return ErrInvalidEvent
	}
	if (value.TimedOut || value.Cancelled) && value.ExitCode != nil {
		return ErrInvalidEvent
	}
	if value.ExitCode != nil && (*value.ExitCode < 0 || uint64(*value.ExitCode) > uint64(^uint32(0))) {
		return ErrInvalidEvent
	}
	return nil
}

func normalizeCommandError(value string) string {
	if validErrorCode(value) {
		return value
	}
	return "unavailable"
}

func validNoExitResultCode(value string) bool {
	return oneOf(value,
		"invalid_input", "not_found", "rejected_executable", "identity_changed",
		"output_limit", "unavailable", "invalid_output", "hash_mismatch", "hash_limit",
		"unsupported_platform")
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
