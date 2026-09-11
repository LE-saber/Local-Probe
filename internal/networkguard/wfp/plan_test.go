package wfp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestBuildPlanHasFixedRules(t *testing.T) {
	plan := BuildPlan()
	if !plan.Valid() {
		t.Fatal("BuildPlan returned an invalid plan")
	}
	summary := plan.Summary()
	wantFamilies := [...]RuleFamily{
		RuleFamilyAuthConnectV4,
		RuleFamilyAuthConnectV6,
		RuleFamilyAuthRecvAcceptV4,
		RuleFamilyAuthRecvAcceptV6,
		RuleFamilyAuthListenV4,
		RuleFamilyAuthListenV6,
		RuleFamilyResourceAssignmentV4,
		RuleFamilyResourceAssignmentV6,
	}
	if summary.RuleCount != len(wantFamilies) {
		t.Fatalf("rule count = %d, want %d", summary.RuleCount, len(wantFamilies))
	}
	if summary.Families != wantFamilies {
		t.Fatalf("families = %#v, want %#v", summary.Families, wantFamilies)
	}
	if summary.Action != ActionBlock {
		t.Fatalf("action = %q, want %q", summary.Action, ActionBlock)
	}
	if summary.Session != SessionDynamicOnly {
		t.Fatalf("session = %q, want %q", summary.Session, SessionDynamicOnly)
	}
	if summary.IdentitySlots != [...]IdentitySlot{TargetApp, TargetUser} {
		t.Fatalf("identity slots = %#v", summary.IdentitySlots)
	}

	seen := make(map[RuleFamily]struct{}, len(summary.Families))
	for _, family := range summary.Families {
		if _, ok := seen[family]; ok {
			t.Fatalf("duplicate family %q", family)
		}
		seen[family] = struct{}{}
	}
	if len(seen) != len(wantFamilies) {
		t.Fatalf("unique family count = %d, want %d", len(seen), len(wantFamilies))
	}
}

func TestPlanIsOpaqueToJSON(t *testing.T) {
	plan := BuildPlan()
	if _, err := json.Marshal(plan); !errors.Is(err, ErrPlanOpaque) {
		t.Fatalf("MarshalJSON error = %v, want %v", err, ErrPlanOpaque)
	}
	var decoded Plan
	if err := json.Unmarshal([]byte(`{}`), &decoded); !errors.Is(err, ErrPlanOpaque) {
		t.Fatalf("UnmarshalJSON error = %v, want %v", err, ErrPlanOpaque)
	}
}
