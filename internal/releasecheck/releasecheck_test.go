package releasecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func testIdentity(t *testing.T) BuildIdentity {
	t.Helper()
	identity, err := NewBuildIdentity("v9.0.0", testCommit)
	if err != nil {
		t.Fatalf("NewBuildIdentity() error = %v", err)
	}
	return identity
}

func completeStatus() Status {
	return Status{Implemented: true, SelfTested: true, IndependentlyReviewed: true, ReleaseReady: true}
}

func allEvidence(t *testing.T, outcome Outcome, status Status) []Evidence {
	t.Helper()
	items := make([]Evidence, 0, len(requiredCodes))
	for _, code := range RequiredCodes() {
		item, err := NewEvidence(code, outcome, status)
		if err != nil {
			t.Fatalf("NewEvidence(%q) error = %v", code, err)
		}
		items = append(items, item)
	}
	return items
}

func hasCode(values []Code, want Code) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func replaceEvidence(t *testing.T, items []Evidence, code Code, replacement Evidence) []Evidence {
	t.Helper()
	for i, item := range items {
		if item.Code() == code {
			items[i] = replacement
			return items
		}
	}
	t.Fatalf("evidence code %q not found", code)
	return items
}

func TestEvaluateCompleteEvidenceIsDeterministicAndBounded(t *testing.T) {
	report, err := Evaluate(testIdentity(t), allEvidence(t, OutcomePass, completeStatus()))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !report.IsReleaseReady() || report.IsProductionReady() {
		t.Fatalf("unexpected readiness: %#v", report)
	}
	if report.RequiredEvidenceCount() != len(requiredCodes) || report.EvidenceCount() != len(requiredCodes) || report.ImplementedCount() != len(requiredCodes) || report.SelfTestedCount() != len(requiredCodes) || report.IndependentlyReviewedCount() != len(requiredCodes) || report.RecordedReleaseReadyCount() != len(requiredCodes) {
		t.Fatalf("unexpected counters: %#v", report)
	}
	if len(report.HardFailures()) != 0 || len(report.PendingRequired()) != 0 || len(report.Evidence()) != len(requiredCodes) {
		t.Fatalf("unexpected gate lists: %#v", report)
	}
	for i, entry := range report.Evidence() {
		if entry.Code != requiredCodes[i] || entry.Outcome != OutcomePass || entry.Observations != 1 || !entry.Implemented || !entry.SelfTested || !entry.IndependentlyReviewed || !entry.ReleaseReady {
			t.Fatalf("entry %d = %#v", i, entry)
		}
	}
	wire, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	if !json.Valid(wire) || len(wire) > MaxWireBytes || bytes.Contains(wire, []byte("path")) || bytes.Contains(wire, []byte("secret")) {
		t.Fatalf("unsafe report wire = %s", wire)
	}
	second, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("second json.Marshal(report) error = %v", err)
	}
	if !bytes.Equal(wire, second) {
		t.Fatalf("report wire is not deterministic:\n%s\n%s", wire, second)
	}

	// Slices returned to a GUI/CLI are detached copies, so a caller cannot
	// alter the decision or the next wire report through a getter.
	entries := report.Evidence()
	entries[0].Outcome = OutcomeFail
	hard := report.HardFailures()
	hard = append(hard, CommandBypass)
	if !report.IsReleaseReady() || len(report.HardFailures()) != 0 || len(report.PendingRequired()) != 0 {
		t.Fatalf("getter mutation changed report state")
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatalf("getter mutation corrupted report: %v", err)
	}
	blocked, err := Evaluate(testIdentity(t), nil)
	if err != nil {
		t.Fatalf("blocked Evaluate() error = %v", err)
	}
	pending := blocked.PendingRequired()
	pending[0] = CommandBypass
	if blocked.PendingRequired()[0] == CommandBypass {
		t.Fatalf("pending getter did not return a copy")
	}
}

func TestEvaluateMissingEvidenceFailsClosed(t *testing.T) {
	report, err := Evaluate(testIdentity(t), nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || len(report.HardFailures()) != 0 || len(report.PendingRequired()) != len(requiredCodes) {
		t.Fatalf("missing evidence did not fail closed: %#v", report)
	}
	for _, entry := range report.Evidence() {
		if entry.Observations != 0 || entry.Outcome != OutcomePending || entry.Implemented || entry.SelfTested || entry.IndependentlyReviewed || entry.ReleaseReady {
			t.Fatalf("missing entry was treated as complete: %#v", entry)
		}
	}
}

func TestEvaluateHardFailuresAndPendingStatuses(t *testing.T) {
	items := allEvidence(t, OutcomePass, completeStatus())
	failed, err := NewEvidence(CommandBypass, OutcomeFail, completeStatus())
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	items = replaceEvidence(t, items, CommandBypass, failed)
	report, err := Evaluate(testIdentity(t), items)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || !hasCode(report.HardFailures(), CommandBypass) || len(report.PendingRequired()) != 0 {
		t.Fatalf("hard failure was not authoritative: %#v", report)
	}

	items = allEvidence(t, OutcomePass, completeStatus())
	incomplete, err := NewEvidence(BudgetUnbounded, OutcomePass, Status{Implemented: true, SelfTested: true, IndependentlyReviewed: true})
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	items = replaceEvidence(t, items, BudgetUnbounded, incomplete)
	report, err = Evaluate(testIdentity(t), items)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || len(report.HardFailures()) != 0 || !hasCode(report.PendingRequired(), BudgetUnbounded) || report.RecordedReleaseReadyCount() != len(requiredCodes)-1 {
		t.Fatalf("incomplete status was not pending: %#v", report)
	}
}

func TestEvaluateDoesNotTrustReleaseReadyBoolean(t *testing.T) {
	items := allEvidence(t, OutcomePass, completeStatus())
	conflicting, err := NewEvidence(AuthorizationBypass, OutcomePass, Status{ReleaseReady: true})
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	items = replaceEvidence(t, items, AuthorizationBypass, conflicting)
	report, err := Evaluate(testIdentity(t), items)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || !hasCode(report.HardFailures(), StatusConflict) || !hasCode(report.HardFailures(), FalseComplete) || !hasCode(report.PendingRequired(), AuthorizationBypass) {
		t.Fatalf("conflicting release_ready claim bypassed gate: %#v", report)
	}

	conflicting, err = NewEvidence(AuthorizationBypass, OutcomePending, Status{Implemented: true, SelfTested: true, IndependentlyReviewed: true, ReleaseReady: true})
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	items = replaceEvidence(t, items, AuthorizationBypass, conflicting)
	report, err = Evaluate(testIdentity(t), items)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || !hasCode(report.HardFailures(), StatusConflict) || !hasCode(report.HardFailures(), FalseComplete) {
		t.Fatalf("pending evidence with release_ready claim bypassed gate: %#v", report)
	}
}

func TestUnknownCommitAlwaysBlocksProvenance(t *testing.T) {
	identity, err := NewBuildIdentity("v9.0.0", "unknown")
	if err != nil {
		t.Fatalf("NewBuildIdentity(unknown) error = %v", err)
	}
	report, err := Evaluate(identity, allEvidence(t, OutcomePass, completeStatus()))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || !hasCode(report.HardFailures(), ProvenanceMissing) || !hasCode(report.PendingRequired(), ProvenanceMissing) {
		t.Fatalf("unknown commit bypassed provenance gate: %#v", report)
	}
	if report.Commit() != "unknown" {
		t.Fatalf("commit = %q, want unknown", report.Commit())
	}
	for _, entry := range report.Evidence() {
		if entry.Code == ProvenanceMissing {
			if entry.Outcome != OutcomeFail || entry.ReleaseReady {
				t.Fatalf("unknown commit left contradictory provenance entry: %#v", entry)
			}
			return
		}
	}
	t.Fatal("unknown commit report omitted provenance entry")
}

func TestEvaluateDuplicateEvidenceIsHardFailure(t *testing.T) {
	items := allEvidence(t, OutcomePass, completeStatus())
	items = append(items, items[0])
	report, err := Evaluate(testIdentity(t), items)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.IsReleaseReady() || !hasCode(report.HardFailures(), DuplicateEvidence) {
		t.Fatalf("duplicate evidence was accepted: %#v", report)
	}
	for _, entry := range report.Evidence() {
		if entry.Code == items[0].Code() && entry.Observations != 2 {
			t.Fatalf("duplicate count was lost: %#v", entry)
		}
	}
}

func TestEvidenceAndReportJSONBoundaries(t *testing.T) {
	item, err := NewEvidence(SoakMissing, OutcomePass, completeStatus())
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	if _, err := json.Marshal(item); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("json.Marshal(Evidence) error = %v, want ErrLocalOnly", err)
	}
	if _, err := json.Marshal([]Evidence{item}); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("json.Marshal([]Evidence) error = %v, want ErrLocalOnly", err)
	}
	var decoded Evidence
	if err := json.Unmarshal([]byte(`{"code":"soak_missing"}`), &decoded); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("json.Unmarshal(Evidence) error = %v, want ErrLocalOnly", err)
	}

	report, err := Evaluate(testIdentity(t), nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if err := json.Unmarshal([]byte(`{"release_ready":true}`), &report); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("json.Unmarshal(Report) error = %v, want ErrLocalOnly", err)
	}
	var zero Report
	if zero.IsReleaseReady() || zero.IsProductionReady() || zero.ReleaseReady() || zero.ProductionReady() {
		t.Fatalf("zero report exposed a ready state")
	}
	if _, err := json.Marshal(zero); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("zero report marshal error = %v, want ErrInvalidReport", err)
	}
	forged := report
	// A caller cannot mutate the private decision fields. This copy only
	// exercises the zero-value/forged-report boundary below.
	forged.data.ReleaseReady = true
	if _, err := json.Marshal(forged); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("forged report marshal error = %v, want ErrInvalidReport", err)
	}
	forged = report
	forged.data.PendingRequired = append(forged.data.PendingRequired, AuthorizationBypass)
	if _, err := json.Marshal(forged); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("mutated report marshal error = %v, want ErrInvalidReport", err)
	}
	invalidSummary := EvidenceSummary{Code: Code("unknown"), Outcome: OutcomePass, Observations: 1}
	if _, err := json.Marshal(invalidSummary); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("unknown summary marshal error = %v, want ErrInvalidReport", err)
	}
}

func TestConstructorsRejectUnknownValuesAndUnsafeIdentity(t *testing.T) {
	if _, err := NewEvidence(Code("unknown"), OutcomePass, completeStatus()); !errors.Is(err, ErrUnknownCode) {
		t.Fatalf("unknown code error = %v, want ErrUnknownCode", err)
	}
	if _, err := NewEvidence(SoakMissing, Outcome("done"), completeStatus()); !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("unknown outcome error = %v, want ErrUnknownOutcome", err)
	}
	for _, identity := range []BuildIdentity{
		{Version: "", Commit: testCommit},
		{Version: "v1/secret", Commit: testCommit},
		{Version: "v1", Commit: "short"},
		{Version: "v1", Commit: `C:\secret`},
	} {
		if _, err := Evaluate(identity, nil); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("Evaluate(%#v) error = %v, want ErrInvalidIdentity", identity, err)
		}
	}
	if _, err := NewBuildIdentity("v1", "ABCDEF0123456"); err != nil {
		t.Fatalf("uppercase commit should be safe: %v", err)
	}
}

func TestEvaluateRejectsUnsealedAndOversizedInput(t *testing.T) {
	if _, err := Evaluate(testIdentity(t), []Evidence{{}}); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("zero Evidence error = %v, want ErrInvalidEvidence", err)
	}
	items := make([]Evidence, 0, MaxEvidence+1)
	for i := 0; i < MaxEvidence+1; i++ {
		item, err := NewEvidence(AuthorizationBypass, OutcomePending, Status{})
		if err != nil {
			t.Fatalf("NewEvidence() error = %v", err)
		}
		items = append(items, item)
	}
	if _, err := Evaluate(testIdentity(t), items); !errors.Is(err, ErrTooManyEvidence) {
		t.Fatalf("oversized input error = %v, want ErrTooManyEvidence", err)
	}
}

func TestEvaluateIsSafeForConcurrentCallers(t *testing.T) {
	items := allEvidence(t, OutcomePass, completeStatus())
	identity := testIdentity(t)
	want, err := Evaluate(identity, items)
	if err != nil {
		t.Fatalf("baseline Evaluate() error = %v", err)
	}
	wantWire, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("baseline json.Marshal() error = %v", err)
	}
	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				got, err := Evaluate(identity, items)
				if err != nil {
					t.Errorf("concurrent Evaluate() error = %v", err)
					return
				}
				wire, err := json.Marshal(got)
				if err != nil {
					t.Errorf("concurrent json.Marshal() error = %v", err)
					return
				}
				if !bytes.Equal(wantWire, wire) || !reflect.DeepEqual(want, got) {
					t.Errorf("concurrent result changed")
					return
				}
			}
		}()
	}
	wg.Wait()
}
