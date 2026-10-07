package desktopbridge

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

type fileRuleRequest struct {
	ConnectionID            string                      `json:"connection_id"`
	ExpectedRevision        string                      `json:"expected_revision"`
	Update                  workspaceadmin.RuleUpdate   `json:"update"`
	Samples                 []workspaceadmin.RuleSample `json:"samples"`
	AcknowledgedConnections []string                    `json:"acknowledged_connections"`
	OfflineConfirmed        bool                        `json:"offline_confirmed"`
}

// Local-only offline rule management. Consent cannot attest external processes:
// the GUI stops only its own controller and never claims live global revocation.
func (s *Service) fileRuleOperation(method string, raw json.RawMessage) (Snapshot, *rpcError) {
	var p fileRuleRequest
	if method == "rules.read" {
		var selection struct {
			ConnectionID string `json:"connection_id"`
		}
		if !decodeParams(raw, &selection) {
			return s.snapshot(), errorFor("invalid_params")
		}
		p.ConnectionID = selection.ConnectionID
	} else if !decodeParams(raw, &p) || p.ExpectedRevision == "" ||
		len(p.Samples) > workspaceadmin.MaxRuleSamples || len(p.AcknowledgedConnections) > 128 ||
		(method == "rules.preview" && (p.OfflineConfirmed || p.AcknowledgedConnections != nil)) ||
		(method == "rules.apply" && (len(p.Samples) != 0 || !p.OfflineConfirmed)) {
		return s.snapshot(), errorFor("invalid_params")
	}
	s.mu.Lock()
	closed, busy, selected := s.closed, s.busy || s.nativeBusy, s.activeID
	s.mu.Unlock()
	if closed {
		return s.snapshot(), errorFor("closed")
	}
	if busy {
		return s.snapshot(), errorFor("busy")
	}
	if p.ConnectionID == "" || p.ConnectionID != selected {
		return s.snapshot(), errorFor("invalid_selection")
	}
	if method != "rules.apply" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if method == "rules.read" {
			rules, err := s.workspaces.FileRules(ctx, p.ConnectionID)
			result := s.snapshot()
			if err != nil {
				return result, errorFor(workspaceErrorCode(err))
			}
			result.FileRules = &rules
			return result, nil
		}
		preview, err := s.workspaces.PreviewRules(ctx, p.ConnectionID, p.ExpectedRevision, p.Update, p.Samples)
		result := s.snapshot()
		if err != nil {
			return result, errorFor(workspaceErrorCode(err))
		}
		result.RulePreview = &preview
		return result, nil
	}
	err := s.startAsync(method, func(ctx context.Context, _ uint64) operationResult {
		fail := func(code string) operationResult {
			return operationResult{code: code, saved: boolPtr(false), applied: boolPtr(false)}
		}
		if p.ConnectionID != s.targetID() {
			return fail("invalid_selection")
		}
		// Validate the revision, proposed policy and complete shared impact BEFORE
		// stopping anything. Revalidate under the manager's mutation lock at CAS.
		preview, err := s.workspaces.PreviewRules(ctx, p.ConnectionID, p.ExpectedRevision, p.Update, nil)
		if err != nil {
			return fail(workspaceErrorCode(err))
		}
		ack := slices.Clone(p.AcknowledgedConnections)
		slices.Sort(ack)
		if !slices.Equal(ack, preview.AffectedConnections) {
			return fail(string(workspaceadmin.CodeImpactRequired))
		}
		if !preview.Changed {
			return operationResult{saved: boolPtr(false), applied: boolPtr(false)}
		}
		s.mu.Lock()
		old := s.controller
		s.mu.Unlock()
		if old != nil {
			if err := old.Stop(); err != nil {
				status := old.Status()
				result := fail("stop_failed")
				result.status = &status
				return result
			}
		}
		s.mu.Lock()
		s.controller = nil
		s.statusKnown = true
		s.status = previewconnect.Status{Stage: previewconnect.StageIdle, Transport: s.transport}
		status := s.status
		s.mu.Unlock()
		saved, err := s.workspaces.SetRules(ctx, p.ConnectionID, p.ExpectedRevision, p.Update, p.AcknowledgedConnections)
		if err != nil {
			result := fail(workspaceErrorCode(err))
			result.status = &status
			return result
		}
		status.Message = "文件规则已保存；本机连接保持停止。"
		status.Remedy = "其它实例须已离线；确认规则后显式连接以加载新配置。"
		rootIDs := []string{}
		if p.Update.RootID != "" {
			rootIDs = append(rootIDs, p.Update.RootID)
		}
		return operationResult{status: &status, rootIDs: rootIDs, saved: boolPtr(saved.Changed), applied: boolPtr(false)}
	})
	if err != nil {
		return s.snapshot(), errorFor(codeFor(err))
	}
	return s.snapshot(), nil
}
