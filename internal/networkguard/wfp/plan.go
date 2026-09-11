// Package wfp contains the platform-neutral planning boundary for a future
// Windows WFP backend.
//
// The plan in this package is deliberately fixed and opaque. It describes the
// deny-only rule families that a future backend may implement, but it does not
// call WFP, Windows Firewall, netsh, a service manager, or any other operating
// system API. The default backend is disabled and cannot produce a
// networkguard capability. A future implementation may live behind explicit
// windows && localprobe_wfp build constraints; this package does not provide a
// silently permissive or pseudo-real driver.
package wfp

import (
	"errors"
)

// ErrPlanOpaque is returned when a Plan is sent through a serialization
// boundary. A plan is local implementation state, not a policy or capability
// that a caller may persist or replay.
var ErrPlanOpaque = errors.New("wfp plan is not serializable")

// RuleFamily is the stable, non-sensitive summary of one fixed ALE layer
// family. It intentionally exposes neither Windows GUIDs nor ABI identifiers.
type RuleFamily string

const (
	RuleFamilyAuthConnectV4        RuleFamily = "auth_connect_v4"
	RuleFamilyAuthConnectV6        RuleFamily = "auth_connect_v6"
	RuleFamilyAuthRecvAcceptV4     RuleFamily = "auth_recv_accept_v4"
	RuleFamilyAuthRecvAcceptV6     RuleFamily = "auth_recv_accept_v6"
	RuleFamilyAuthListenV4         RuleFamily = "auth_listen_v4"
	RuleFamilyAuthListenV6         RuleFamily = "auth_listen_v6"
	RuleFamilyResourceAssignmentV4 RuleFamily = "resource_assignment_v4"
	RuleFamilyResourceAssignmentV6 RuleFamily = "resource_assignment_v6"
)

// Action is the fixed action in every rule in a Plan.
type Action string

const ActionBlock Action = "block"

// SessionMode is the fixed lifetime mode in every rule in a Plan.
type SessionMode string

const SessionDynamicOnly SessionMode = "dynamic_only"

// IdentitySlot identifies one fixed identity condition slot. Values for the
// slots are intentionally not accepted by the plan builder.
type IdentitySlot string

const (
	TargetApp  IdentitySlot = "target_app"
	TargetUser IdentitySlot = "target_user"
)

const (
	fixedRuleCount     = 8
	fixedIdentityCount = 2
)

var fixedFamilies = [...]RuleFamily{
	RuleFamilyAuthConnectV4,
	RuleFamilyAuthConnectV6,
	RuleFamilyAuthRecvAcceptV4,
	RuleFamilyAuthRecvAcceptV6,
	RuleFamilyAuthListenV4,
	RuleFamilyAuthListenV6,
	RuleFamilyResourceAssignmentV4,
	RuleFamilyResourceAssignmentV6,
}

var fixedIdentitySlots = [...]IdentitySlot{TargetApp, TargetUser}

type fixedRule struct {
	family        RuleFamily
	action        Action
	session       SessionMode
	identitySlots [fixedIdentityCount]IdentitySlot
}

var fixedRuleSet = [...]fixedRule{
	{family: RuleFamilyAuthConnectV4, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyAuthConnectV6, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyAuthRecvAcceptV4, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyAuthRecvAcceptV6, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyAuthListenV4, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyAuthListenV6, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyResourceAssignmentV4, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
	{family: RuleFamilyResourceAssignmentV6, action: ActionBlock, session: SessionDynamicOnly, identitySlots: fixedIdentitySlots},
}

// Plan is an opaque, fixed deny plan. It contains no address, port, provider,
// weight, flag, session-flag, or coverage field, and no caller-supplied rule
// configuration can be injected into it.
type Plan struct {
	rules [fixedRuleCount]fixedRule
}

// BuildPlan returns the one supported deny plan. It has no caller-controlled
// inputs by design.
func BuildPlan() Plan {
	return Plan{rules: fixedRuleSet}
}

// Valid reports whether p is exactly the fixed plan emitted by BuildPlan.
func (p Plan) Valid() bool { return p.rules == fixedRuleSet }

// PlanSummary is a diagnostic-only, stable, read-only description of a Plan.
// It is not coverage, attestation, or a capability, and cannot be used with
// Activate.
type PlanSummary struct {
	RuleCount     int
	Families      [fixedRuleCount]RuleFamily
	Action        Action
	Session       SessionMode
	IdentitySlots [fixedIdentityCount]IdentitySlot
}

// Summary returns a diagnostic-only fixed enum summary. It is not coverage,
// attestation, or a capability, and cannot be used with Activate. The zero
// summary represents an invalid or zero Plan.
func (p Plan) Summary() PlanSummary {
	if !p.Valid() {
		return PlanSummary{}
	}
	return PlanSummary{
		RuleCount:     fixedRuleCount,
		Families:      fixedFamilies,
		Action:        ActionBlock,
		Session:       SessionDynamicOnly,
		IdentitySlots: fixedIdentitySlots,
	}
}

// MarshalJSON prevents a local Plan from becoming a replayable wire object.
func (Plan) MarshalJSON() ([]byte, error) { return nil, ErrPlanOpaque }

// UnmarshalJSON prevents a caller from constructing a Plan from wire data.
func (*Plan) UnmarshalJSON([]byte) error { return ErrPlanOpaque }
