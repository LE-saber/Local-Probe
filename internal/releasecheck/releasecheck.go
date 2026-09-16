// Package releasecheck evaluates a bounded, local release-evidence set.
//
// The package deliberately consumes only typed Evidence values created by a
// trusted in-process collector. Evidence cannot be marshalled or unmarshalled
// as JSON, so a model, network peer, or wire payload cannot manufacture a
// release decision. Evaluation is read-only and has no filesystem, process,
// network, signing, SBOM, or vulnerability-scanning side effects.
package releasecheck

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

const (
	// ProductionReady is intentionally false. This package is a local gate
	// evaluator and does not itself prove a production release.
	ProductionReady = false

	SchemaVersion = "local-probe.release-check.v1"

	MaxEvidence  = 128
	MaxWireBytes = 32 << 10
	MaxIDBytes   = 64
)

var (
	ErrInvalidIdentity = errors.New("invalid release identity")
	ErrUnknownCode     = errors.New("unknown release evidence code")
	ErrUnknownOutcome  = errors.New("unknown release evidence outcome")
	ErrInvalidEvidence = errors.New("invalid release evidence")
	ErrTooManyEvidence = errors.New("release evidence limit exceeded")
	ErrInvalidReport   = errors.New("invalid release report")
	ErrWireLimit       = errors.New("release report wire budget exceeded")
	ErrLocalOnly       = errors.New("release evidence is local-only")
)

// Code is the fixed vocabulary used by required evidence and hard failures.
// It never carries free-form diagnostics.
type Code string

const (
	AuthorizationBypass Code = "authorization_bypass"
	SilentTruncation    Code = "silent_truncation"
	BudgetUnbounded     Code = "budget_unbounded"
	CommandBypass       Code = "command_bypass"
	CredentialLeak      Code = "credential_leak"
	ScopeReuse          Code = "scope_reuse"
	FalseComplete       Code = "false_complete"
	UnsignedArtifact    Code = "unsigned_artifact"
	SBOMMissing         Code = "sbom_missing"
	LicenseMissing      Code = "license_missing"
	ProvenanceMissing   Code = "provenance_missing"
	// InstallMatrixMissing covers install, update, and uninstall paths in one
	// required matrix; there is no separate update/uninstall code.
	InstallMatrixMissing     Code = "install_matrix_missing"
	SoakMissing              Code = "soak_missing"
	MigrationRollbackMissing Code = "migration_rollback_missing"
	CVEScanMissing           Code = "cve_scan_missing"
	SupportWorkflowMissing   Code = "support_workflow_missing"

	// These codes describe malformed or conflicting local evidence. They are
	// hard failures too, and cannot be removed by setting a boolean field.
	DuplicateEvidence Code = "duplicate_evidence"
	StatusConflict    Code = "status_conflict"
)

// Code aliases with an explicit prefix make call sites easier to read while
// retaining the short fixed enum names above.
const (
	CodeAuthorizationBypass      = AuthorizationBypass
	CodeSilentTruncation         = SilentTruncation
	CodeBudgetUnbounded          = BudgetUnbounded
	CodeCommandBypass            = CommandBypass
	CodeCredentialLeak           = CredentialLeak
	CodeScopeReuse               = ScopeReuse
	CodeFalseComplete            = FalseComplete
	CodeUnsignedArtifact         = UnsignedArtifact
	CodeSBOMMissing              = SBOMMissing
	CodeLicenseMissing           = LicenseMissing
	CodeProvenanceMissing        = ProvenanceMissing
	CodeInstallMatrixMissing     = InstallMatrixMissing
	CodeSoakMissing              = SoakMissing
	CodeMigrationRollbackMissing = MigrationRollbackMissing
	CodeCVEScanMissing           = CVEScanMissing
	CodeSupportWorkflowMissing   = SupportWorkflowMissing
)

// EvidenceCode and FailureCode are descriptive aliases for Code. A failure
// code is still constrained to the same fixed vocabulary.
type EvidenceCode = Code
type FailureCode = Code

// Outcome records the local collector's result for one required evidence
// category. The category names are failure-oriented (for example,
// authorization_bypass); therefore OutcomePass explicitly means the check
// passed and that failure was not found. OutcomeFail means the named failure
// was found. Pending is not a soft success: a pending required category keeps
// the aggregate release decision false.
type Outcome string

const (
	OutcomePass    Outcome = "pass"
	OutcomeFail    Outcome = "fail"
	OutcomePending Outcome = "pending"
)

// Status records the four deliberately independent evidence states. The
// IndependentlyReviewed bit is a trusted typed collector claim only; it is not
// a cryptographic attestation and this package cannot identify or authenticate
// the reviewing context. The ReleaseReady bit is likewise only a recorded
// collector claim; Evaluate recomputes the aggregate result and never trusts
// either bit as an override.
type Status struct {
	Implemented           bool
	SelfTested            bool
	IndependentlyReviewed bool
	ReleaseReady          bool
}

// BuildIdentity contains only safe release identifiers. It is not a secret,
// path, endpoint, error message, or credential.
type BuildIdentity struct {
	Version string
	Commit  string
}

// NewBuildIdentity validates a release version and commit identifier. A
// commit is normally a 7–64 character hexadecimal object name; "unknown" is
// accepted so an absent provenance proof can be represented by its evidence
// category rather than by leaking an arbitrary diagnostic string.
func NewBuildIdentity(version, commit string) (BuildIdentity, error) {
	if !validVersion(version) || !validCommit(commit) {
		return BuildIdentity{}, ErrInvalidIdentity
	}
	return BuildIdentity{Version: version, Commit: strings.ToLower(commit)}, nil
}

// Evidence is a sealed, local-only observation. Its fields are intentionally
// private: callers must use NewEvidence, and JSON cannot be used to create or
// transport one. The constructor is the trusted collector seam; callers must
// not pass model- or network-derived values to it.
type Evidence struct {
	code    Code
	outcome Outcome
	status  Status
	local   bool
}

// NewEvidence creates one typed local observation. All combinations of the
// fixed enums are accepted so the evaluator can expose contradictory claims as
// a hard status conflict rather than silently normalising them. For a
// failure-oriented code, OutcomePass means its safety check passed; it does
// not mean the failure occurred.
func NewEvidence(code Code, outcome Outcome, status Status) (Evidence, error) {
	if !isRequiredCode(code) {
		return Evidence{}, ErrUnknownCode
	}
	if !isOutcome(outcome) {
		return Evidence{}, ErrUnknownOutcome
	}
	return Evidence{code: code, outcome: outcome, status: status, local: true}, nil
}

// Code returns the fixed evidence category.
func (e Evidence) Code() Code { return e.code }

// Outcome returns the local collector outcome.
func (e Evidence) Outcome() Outcome { return e.outcome }

// Status returns the four recorded states.
func (e Evidence) Status() Status { return e.status }

// MarshalJSON prevents local evidence from crossing a wire boundary. This is
// intentionally an error rather than an empty object, which could otherwise
// be mistaken for valid evidence.
func (Evidence) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

// UnmarshalJSON prevents a wire payload from manufacturing local evidence.
func (*Evidence) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// RequiredCodes returns a fresh, deterministic list of every required
// evidence category.
func RequiredCodes() []Code {
	return append([]Code(nil), requiredCodes...)
}

// EvidenceSummary is the bounded wire-safe projection of one category. It
// contains only fixed enums, booleans, and counts. Observations=0 denotes
// missing evidence.
type EvidenceSummary struct {
	Code                  Code    `json:"code"`
	Outcome               Outcome `json:"outcome"`
	Implemented           bool    `json:"implemented"`
	SelfTested            bool    `json:"self_tested"`
	IndependentlyReviewed bool    `json:"independently_reviewed"`
	ReleaseReady          bool    `json:"release_ready"`
	Observations          int     `json:"observations"`
}

func (s EvidenceSummary) validate() error {
	if !isRequiredCode(s.Code) || !isOutcome(s.Outcome) || s.Observations < 0 || s.Observations > MaxEvidence {
		return ErrInvalidReport
	}
	if s.Observations == 0 && (s.Outcome != OutcomePending || s.Implemented || s.SelfTested || s.IndependentlyReviewed || s.ReleaseReady) {
		return ErrInvalidReport
	}
	return nil
}

// MarshalJSON emits only the strict, bounded summary shape.
func (s EvidenceSummary) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain EvidenceSummary
	return marshalBounded(plain(s))
}

// Report is the deterministic, machine-readable release-gate result. Its
// decision, status, count, and wire data are private and can only be produced
// by Evaluate. Read-only getters return scalar values or copies. A report
// returned by Evaluate is sealed against accidental post-evaluation mutation
// before it is marshalled. IsReleaseReady is computed from all required
// entries, hard failures, and pending evidence; it is never taken from an
// input boolean. ProductionReady remains false because this evaluator itself
// is not a production attestation, even when IsReleaseReady reports that the
// supplied evidence satisfies every gate.
type Report struct {
	data   reportSnapshot
	sealed reportSnapshot
}

type reportSnapshot struct {
	SchemaVersion              string
	ProductionReady            bool
	Version                    string
	Commit                     string
	ReleaseReady               bool
	RequiredEvidenceCount      int
	EvidenceCount              int
	ImplementedCount           int
	SelfTestedCount            int
	IndependentlyReviewedCount int
	RecordedReleaseReadyCount  int
	Entries                    []EvidenceSummary
	HardFailures               []FailureCode
	PendingRequired            []EvidenceCode
}

func (r Report) validate() error {
	if !reflect.DeepEqual(r.data, r.sealed) {
		return ErrInvalidReport
	}
	d := r.data
	if d.SchemaVersion != SchemaVersion || d.ProductionReady || !validVersion(d.Version) || !validCommit(d.Commit) || d.RequiredEvidenceCount != len(requiredCodes) || d.EvidenceCount < 0 || d.EvidenceCount > MaxEvidence || d.ImplementedCount < 0 || d.ImplementedCount > d.EvidenceCount || d.SelfTestedCount < 0 || d.SelfTestedCount > d.EvidenceCount || d.IndependentlyReviewedCount < 0 || d.IndependentlyReviewedCount > d.EvidenceCount || d.RecordedReleaseReadyCount < 0 || d.RecordedReleaseReadyCount > d.EvidenceCount || len(d.Entries) != len(requiredCodes) || len(d.HardFailures) > MaxEvidence || len(d.PendingRequired) > len(requiredCodes) {
		return ErrInvalidReport
	}
	if !sortedUniqueCodes(d.HardFailures) || !sortedUniqueCodes(d.PendingRequired) {
		return ErrInvalidReport
	}
	for _, code := range d.HardFailures {
		if !isHardCode(code) {
			return ErrInvalidReport
		}
	}
	for _, code := range d.PendingRequired {
		if !isRequiredCode(code) {
			return ErrInvalidReport
		}
	}
	seen := make(map[Code]struct{}, len(d.Entries))
	observations := 0
	for i, entry := range d.Entries {
		if err := entry.validate(); err != nil {
			return err
		}
		if i > 0 && d.Entries[i-1].Code >= entry.Code {
			return ErrInvalidReport
		}
		if _, ok := seen[entry.Code]; ok {
			return ErrInvalidReport
		}
		seen[entry.Code] = struct{}{}
		observations += entry.Observations
	}
	if observations != d.EvidenceCount || len(seen) != len(requiredCodes) {
		return ErrInvalidReport
	}
	wantReady := len(d.HardFailures) == 0 && len(d.PendingRequired) == 0
	if d.ReleaseReady != wantReady {
		return ErrInvalidReport
	}
	if d.ReleaseReady {
		for _, entry := range d.Entries {
			if entry.Observations == 0 || entry.Outcome != OutcomePass || !entry.Implemented || !entry.SelfTested || !entry.IndependentlyReviewed || !entry.ReleaseReady {
				return ErrInvalidReport
			}
		}
	}
	return nil
}

// Schema returns the fixed report schema identifier.
func (r Report) Schema() string { return r.data.SchemaVersion }

// SchemaVersion returns the fixed report schema identifier.
func (r Report) SchemaVersion() string { return r.Schema() }

// Version returns the safe build version identifier.
func (r Report) Version() string { return r.data.Version }

// Commit returns the normalized safe build commit identifier.
func (r Report) Commit() string { return r.data.Commit }

// IsProductionReady is always false for this non-production evaluator.
func (r Report) IsProductionReady() bool { return r.data.ProductionReady }

// ProductionReady is a compatibility alias for IsProductionReady. It is a
// method, not a mutable report field.
func (r Report) ProductionReady() bool { return r.IsProductionReady() }

// IsReleaseReady reports the computed gate result. It is true only when every
// required category has pass outcome and all four status bits, with no hard
// failure or pending required evidence.
func (r Report) IsReleaseReady() bool { return r.data.ReleaseReady }

// ReleaseReady is a compatibility alias for IsReleaseReady. It is a method,
// not a mutable report field.
func (r Report) ReleaseReady() bool { return r.IsReleaseReady() }

// RequiredEvidenceCount returns the number of required categories.
func (r Report) RequiredEvidenceCount() int { return r.data.RequiredEvidenceCount }

// EvidenceCount returns the number of trusted typed observations consumed.
func (r Report) EvidenceCount() int { return r.data.EvidenceCount }

// ImplementedCount returns the number of observations marked implemented.
func (r Report) ImplementedCount() int { return r.data.ImplementedCount }

// SelfTestedCount returns the number of observations marked self-tested.
func (r Report) SelfTestedCount() int { return r.data.SelfTestedCount }

// IndependentlyReviewedCount returns the number of observations carrying the
// trusted typed independent-review claim.
func (r Report) IndependentlyReviewedCount() int { return r.data.IndependentlyReviewedCount }

// RecordedReleaseReadyCount returns the number of observations that claimed
// release readiness. This recorded count is not an override of IsReleaseReady.
func (r Report) RecordedReleaseReadyCount() int { return r.data.RecordedReleaseReadyCount }

// Evidence returns a copy of the deterministic per-category summaries.
func (r Report) Evidence() []EvidenceSummary {
	return append([]EvidenceSummary(nil), r.data.Entries...)
}

// Entries is an alias for Evidence and returns a copy.
func (r Report) Entries() []EvidenceSummary { return r.Evidence() }

// HardFailures returns a copy of the sorted hard-failure codes.
func (r Report) HardFailures() []FailureCode {
	return append([]FailureCode(nil), r.data.HardFailures...)
}

// PendingRequired returns a copy of the sorted required categories that are
// missing, pending, or incomplete.
func (r Report) PendingRequired() []EvidenceCode {
	return append([]EvidenceCode(nil), r.data.PendingRequired...)
}

// MarshalJSON emits a strictly bounded report and refuses forged or mutated
// values. The private snapshot is not part of the wire shape.
func (r Report) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	type wire struct {
		SchemaVersion              string            `json:"schema_version"`
		ProductionReady            bool              `json:"production_ready"`
		Version                    string            `json:"version"`
		Commit                     string            `json:"commit"`
		ReleaseReady               bool              `json:"release_ready"`
		RequiredEvidenceCount      int               `json:"required_evidence_count"`
		EvidenceCount              int               `json:"evidence_count"`
		ImplementedCount           int               `json:"implemented_count"`
		SelfTestedCount            int               `json:"self_tested_count"`
		IndependentlyReviewedCount int               `json:"independently_reviewed_count"`
		RecordedReleaseReadyCount  int               `json:"recorded_release_ready_count"`
		Entries                    []EvidenceSummary `json:"evidence"`
		HardFailures               []FailureCode     `json:"hard_failures"`
		PendingRequired            []EvidenceCode    `json:"pending_required"`
	}
	return marshalBounded(wire{
		SchemaVersion:              r.data.SchemaVersion,
		ProductionReady:            r.data.ProductionReady,
		Version:                    r.data.Version,
		Commit:                     r.data.Commit,
		ReleaseReady:               r.data.ReleaseReady,
		RequiredEvidenceCount:      r.data.RequiredEvidenceCount,
		EvidenceCount:              r.data.EvidenceCount,
		ImplementedCount:           r.data.ImplementedCount,
		SelfTestedCount:            r.data.SelfTestedCount,
		IndependentlyReviewedCount: r.data.IndependentlyReviewedCount,
		RecordedReleaseReadyCount:  r.data.RecordedReleaseReadyCount,
		Entries:                    r.data.Entries,
		HardFailures:               r.data.HardFailures,
		PendingRequired:            r.data.PendingRequired,
	})
}

// UnmarshalJSON rejects reports as an input format. Release reports must be
// freshly computed from local typed evidence.
func (*Report) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// Evaluate computes a release report from a safe build identity and local
// typed evidence. It performs no I/O and does not retain the input slice.
func Evaluate(identity BuildIdentity, evidence []Evidence) (Report, error) {
	if !validVersion(identity.Version) || !validCommit(identity.Commit) {
		return Report{}, ErrInvalidIdentity
	}
	if len(evidence) > MaxEvidence {
		return Report{}, ErrTooManyEvidence
	}
	groups := make(map[Code][]Evidence, len(requiredCodes))
	for _, item := range evidence {
		if !item.local || !isRequiredCode(item.code) || !isOutcome(item.outcome) {
			return Report{}, ErrInvalidEvidence
		}
		groups[item.code] = append(groups[item.code], item)
	}

	hardSet := make(map[Code]struct{})
	pendingSet := make(map[Code]struct{})
	entries := make([]EvidenceSummary, 0, len(requiredCodes))
	implemented, selfTested, independentlyReviewed, recordedReady := 0, 0, 0, 0
	for _, code := range requiredCodes {
		items := groups[code]
		entry := EvidenceSummary{Code: code, Outcome: OutcomePending, Observations: len(items)}
		if len(items) == 0 {
			pendingSet[code] = struct{}{}
		} else {
			entry = summarise(code, items)
			if len(items) > 1 {
				hardSet[DuplicateEvidence] = struct{}{}
			}
			for _, item := range items {
				if item.status.Implemented {
					implemented++
				}
				if item.status.SelfTested {
					selfTested++
				}
				if item.status.IndependentlyReviewed {
					independentlyReviewed++
				}
				if item.status.ReleaseReady {
					recordedReady++
					if item.outcome != OutcomePass || !item.status.Implemented || !item.status.SelfTested || !item.status.IndependentlyReviewed {
						hardSet[StatusConflict] = struct{}{}
						hardSet[FalseComplete] = struct{}{}
					}
				}
				if item.outcome == OutcomeFail {
					hardSet[code] = struct{}{}
				}
			}
			if entry.Outcome == OutcomePending || !entry.Implemented || !entry.SelfTested || !entry.IndependentlyReviewed || !entry.ReleaseReady {
				pendingSet[code] = struct{}{}
			}
		}
		entries = append(entries, entry)
	}

	hard := sortedCodes(hardSet)
	pending := sortedCodes(pendingSet)
	if strings.EqualFold(identity.Commit, "unknown") {
		// An unknown build commit is itself missing provenance. Keep this
		// conservative even if a collector supplied a contradictory pass row,
		// and make the per-category projection agree with the aggregate gate.
		hardSet[ProvenanceMissing] = struct{}{}
		pendingSet[ProvenanceMissing] = struct{}{}
		for i := range entries {
			if entries[i].Code == ProvenanceMissing {
				entries[i].Outcome = OutcomeFail
				entries[i].ReleaseReady = false
				break
			}
		}
		hard = sortedCodes(hardSet)
		pending = sortedCodes(pendingSet)
	}
	ready := len(hard) == 0 && len(pending) == 0
	data := reportSnapshot{
		SchemaVersion:              SchemaVersion,
		ProductionReady:            ProductionReady,
		Version:                    identity.Version,
		Commit:                     strings.ToLower(identity.Commit),
		ReleaseReady:               ready,
		RequiredEvidenceCount:      len(requiredCodes),
		EvidenceCount:              len(evidence),
		ImplementedCount:           implemented,
		SelfTestedCount:            selfTested,
		IndependentlyReviewedCount: independentlyReviewed,
		RecordedReleaseReadyCount:  recordedReady,
		Entries:                    append([]EvidenceSummary(nil), entries...),
		HardFailures:               append([]FailureCode(nil), hard...),
		PendingRequired:            append([]EvidenceCode(nil), pending...),
	}
	return Report{data: cloneSnapshot(data), sealed: cloneSnapshot(data)}, nil
}

func cloneSnapshot(value reportSnapshot) reportSnapshot {
	value.Entries = append([]EvidenceSummary(nil), value.Entries...)
	value.HardFailures = append([]FailureCode(nil), value.HardFailures...)
	value.PendingRequired = append([]EvidenceCode(nil), value.PendingRequired...)
	return value
}

func summarise(code Code, items []Evidence) EvidenceSummary {
	summary := EvidenceSummary{Code: code, Outcome: OutcomePass, Observations: len(items), Implemented: true, SelfTested: true, IndependentlyReviewed: true, ReleaseReady: true}
	for _, item := range items {
		if item.outcome == OutcomeFail {
			summary.Outcome = OutcomeFail
		} else if item.outcome == OutcomePending && summary.Outcome == OutcomePass {
			summary.Outcome = OutcomePending
		}
		summary.Implemented = summary.Implemented && item.status.Implemented
		summary.SelfTested = summary.SelfTested && item.status.SelfTested
		summary.IndependentlyReviewed = summary.IndependentlyReviewed && item.status.IndependentlyReviewed
		summary.ReleaseReady = summary.ReleaseReady && item.status.ReleaseReady
	}
	return summary
}

func validVersion(value string) bool {
	if value == "" || len(value) > MaxIDBytes {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r)) {
			return false
		}
	}
	return true
}

func validCommit(value string) bool {
	if value == "unknown" {
		return true
	}
	if len(value) < 7 || len(value) > MaxIDBytes {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isOutcome(value Outcome) bool {
	return value == OutcomePass || value == OutcomeFail || value == OutcomePending
}

func isRequiredCode(value Code) bool {
	switch value {
	case AuthorizationBypass, SilentTruncation, BudgetUnbounded, CommandBypass, CredentialLeak, ScopeReuse, FalseComplete, UnsignedArtifact, SBOMMissing, LicenseMissing, ProvenanceMissing, InstallMatrixMissing, SoakMissing, MigrationRollbackMissing, CVEScanMissing, SupportWorkflowMissing:
		return true
	default:
		return false
	}
}

func isHardCode(value Code) bool {
	return isRequiredCode(value) || value == DuplicateEvidence || value == StatusConflict
}

func sortedCodes(set map[Code]struct{}) []Code {
	values := make([]Code, 0, len(set))
	for code := range set {
		values = append(values, code)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func sortedUniqueCodes(values []Code) bool {
	for i, value := range values {
		if i > 0 && values[i-1] >= value {
			return false
		}
	}
	return true
}

func marshalBounded(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxWireBytes {
		return nil, ErrWireLimit
	}
	return data, nil
}

var requiredCodes = []Code{
	AuthorizationBypass,
	SilentTruncation,
	BudgetUnbounded,
	CommandBypass,
	CredentialLeak,
	ScopeReuse,
	FalseComplete,
	UnsignedArtifact,
	SBOMMissing,
	LicenseMissing,
	ProvenanceMissing,
	InstallMatrixMissing,
	SoakMissing,
	MigrationRollbackMissing,
	CVEScanMissing,
	SupportWorkflowMissing,
}

func init() {
	sort.Slice(requiredCodes, func(i, j int) bool { return requiredCodes[i] < requiredCodes[j] })
}
