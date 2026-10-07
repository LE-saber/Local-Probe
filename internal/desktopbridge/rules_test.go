package desktopbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

func ruleParams(s Snapshot, root string) map[string]any {
	return map[string]any{"connection_id": s.Desktop.ActiveID, "expected_revision": s.Desktop.Revision, "update": map[string]any{"root_id": root, "deny_patterns": []string{"secrets/**"}, "ignore_patterns": []string{"build/**"}}}
}

func TestGUIFileRulesPreviewSaveAndReloadRealPolicy(t *testing.T) {
	f := newBridgeFixture(t, true, nil)
	initial := f.service.snapshot()
	read := callRPC(t, f.service, "read", "rules.read", map[string]any{"connection_id": initial.Desktop.ActiveID})
	if !read.OK || read.Data.FileRules == nil || read.Data.FileRules.Revision != initial.Desktop.Revision || len(read.Data.FileRules.Roots) != 1 {
		t.Fatal("rule selection missing")
	}
	for _, root := range []string{"", f.rootID} {
		before, _ := os.ReadFile(f.configPath)
		params := ruleParams(f.service.snapshot(), root)
		params["samples"] = []workspaceadmin.RuleSample{{RootID: f.rootID, Path: "src/main.go"}, {RootID: f.rootID, Path: "secrets/key.txt"}, {RootID: f.rootID, Path: "build/result.txt"}}
		response := callRPC(t, f.service, "preview", "rules.preview", params)
		if !response.OK || response.Data.RulePreview == nil {
			t.Fatalf("preview: %+v", response.Error)
		}
		p := response.Data.RulePreview
		if !p.Changed || len(p.AffectedConnections) != 2 || len(p.Decisions) != 3 || !p.Decisions[0].Allowed || p.Decisions[1].Allowed || !p.Decisions[2].Allowed || !p.Decisions[2].Ignored {
			t.Fatalf("incorrect real policy preview: %+v", p)
		}
		after, _ := os.ReadFile(f.configPath)
		if !bytes.Equal(before, after) {
			t.Fatal("preview wrote config")
		}
		fake := &fakeBridgeController{status: previewconnect.Status{Stage: previewconnect.StageReady, MCPReady: true, TunnelReady: true}}
		f.service.mu.Lock()
		f.service.controller = fake
		f.service.mu.Unlock()
		delete(params, "samples")
		params["acknowledged_connections"] = p.AffectedConnections
		params["offline_confirmed"] = true
		result := desktopWrite(t, f.service, "rules.apply", params)
		if result.Connection.Code != "" || result.Connection.Saved == nil || !*result.Connection.Saved || result.Connection.Applied == nil || *result.Connection.Applied || result.Connection.MCPReady || result.Connection.TunnelReady || result.Desktop.Revision == p.Revision {
			t.Fatalf("save: %+v", result.Connection)
		}
		fake.mu.Lock()
		stops, connects, reconnects := fake.stopCount, fake.connectCount, fake.reconnectCount
		fake.mu.Unlock()
		if stops != 1 || connects != 0 || reconnects != 0 {
			t.Fatal("save must stop without reconnect")
		}
		if result.Desktop.Developer.ExecutionAvailable {
			t.Fatal("rule management enabled execution")
		}
	}
	loaded, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	inMemory, err := config.NewStore(loaded.Config())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := policy.NewManager(inMemory)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{initial.Desktop.ActiveID, "second-connection"} {
		bound, err := manager.BindAuthenticated(id)
		if err != nil {
			t.Fatal(err)
		}
		if bound.AllowsPath(f.rootID, "secrets/key.txt") || !bound.AllowsPath(f.rootID, "src/main.go") || !bound.AllowsPath(f.rootID, "build/result.txt") || !bound.IsIgnoredPath(f.rootID, "build/result.txt") {
			t.Fatal("saved rules not applied by real policy")
		}
	}
	reopened, err := New(f.service.options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	response := callRPC(t, reopened, "read", "rules.read", map[string]any{"connection_id": initial.Desktop.ActiveID})
	if !response.OK || len(response.Data.FileRules.Profile.Deny) != 1 || len(response.Data.FileRules.Roots[0].Deny) != 1 || response.Data.Desktop.Developer.ExecutionAvailable {
		t.Fatal("GUI reopen lost layered rules")
	}
}

func TestGUIRuleValidationAndImpactBeforeStop(t *testing.T) {
	f := newBridgeFixture(t, true, nil)
	initial := f.service.snapshot()
	fake := &fakeBridgeController{}
	f.service.mu.Lock()
	f.service.controller = fake
	f.service.mu.Unlock()
	before, _ := os.ReadFile(f.configPath)
	for _, ack := range [][]string{{initial.Desktop.ActiveID}, {initial.Desktop.ActiveID, initial.Desktop.ActiveID}, {initial.Desktop.ActiveID, "second-connection", "extra"}} {
		p := ruleParams(initial, f.rootID)
		p["offline_confirmed"] = true
		p["acknowledged_connections"] = ack
		r := desktopWrite(t, f.service, "rules.apply", p)
		if r.Connection.Code != "impact_acknowledgement_required" {
			t.Fatal("accepted incomplete impact")
		}
	}
	for _, patch := range []map[string]any{
		{"offline_confirmed": false}, {"connection_id": "second-connection"}, {"executable": "C:\\bad.exe"},
	} {
		p := ruleParams(initial, f.rootID)
		p["offline_confirmed"] = true
		p["acknowledged_connections"] = []string{initial.Desktop.ActiveID, "second-connection"}
		for k, v := range patch {
			p[k] = v
		}
		if callRPC(t, f.service, "invalid", "rules.apply", p).OK {
			t.Fatal("accepted invalid local request")
		}
	}
	for _, update := range []map[string]any{{"root_id": "foreign", "deny_patterns": []string{"**"}}, {"deny_patterns": []string{"../secret"}}, {"deny_patterns": []string{"x", "x"}}, {"deny_patterns": []string{"x"}, "execute": true}} {
		p := ruleParams(initial, "")
		p["update"] = update
		if callRPC(t, f.service, "invalid", "rules.preview", p).OK {
			t.Fatal("invalid preview")
		}
	}
	p := ruleParams(initial, "")
	p["expected_revision"] = "stale"
	p["offline_confirmed"] = true
	p["acknowledged_connections"] = []string{initial.Desktop.ActiveID, "second-connection"}
	if r := desktopWrite(t, f.service, "rules.apply", p); r.Connection.Code != "revision_conflict" {
		t.Fatal("stale config accepted")
	}
	after, _ := os.ReadFile(f.configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid operation changed config")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stopCount != 0 {
		t.Fatal("invalid edit stopped controller")
	}
}

func TestGUIRulesStopFailureAndCASConflictKeepConfiguration(t *testing.T) {
	for _, mode := range []string{"stop_failure", "concurrent_write"} {
		t.Run(mode, func(t *testing.T) {
			f := newBridgeFixture(t, false, nil)
			initial := f.service.snapshot()
			before, _ := os.ReadFile(f.configPath)
			fake := &fakeBridgeController{status: previewconnect.Status{Stage: previewconnect.StageReady, MCPReady: true}}
			fake.stopHook = func() error {
				if mode == "stop_failure" {
					return errors.New("synthetic stop failure")
				}
				patterns := []string{"external/**"}
				_, err := f.service.workspaces.SetRules(context.Background(), initial.Desktop.ActiveID, initial.Desktop.Revision, workspaceadmin.RuleUpdate{Deny: &patterns}, []string{initial.Desktop.ActiveID})
				return err
			}
			f.service.mu.Lock()
			f.service.controller = fake
			f.service.mu.Unlock()
			p := ruleParams(initial, f.rootID)
			p["offline_confirmed"] = true
			p["acknowledged_connections"] = []string{initial.Desktop.ActiveID}
			r := desktopWrite(t, f.service, "rules.apply", p)
			fake.mu.Lock()
			fake.stopHook = nil
			fake.mu.Unlock()
			if r.Connection.Saved == nil || *r.Connection.Saved {
				t.Fatal("failed edit claimed saved")
			}
			if mode == "stop_failure" {
				after, _ := os.ReadFile(f.configPath)
				if r.Connection.Code != "stop_failed" || !bytes.Equal(before, after) {
					t.Fatal("stop failure changed policy")
				}
			} else {
				if r.Connection.Code != "revision_conflict" || r.Connection.MCPReady {
					t.Fatal("CAS conflict not fail closed")
				}
				rules, err := f.service.workspaces.FileRules(context.Background(), initial.Desktop.ActiveID)
				if err != nil || len(rules.Profile.Deny) != 1 || rules.Profile.Deny[0] != "external/**" || len(rules.Roots[0].Deny) != 0 {
					t.Fatal("overwrote concurrent policy")
				}
			}
		})
	}
}

func TestGUIRuleClearAndNoopAndBusy(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	initial := f.service.snapshot()
	p := ruleParams(initial, f.rootID)
	p["offline_confirmed"] = true
	p["acknowledged_connections"] = []string{initial.Desktop.ActiveID}
	first := desktopWrite(t, f.service, "rules.apply", p)
	p["expected_revision"] = first.Desktop.Revision
	p["update"] = map[string]any{"root_id": f.rootID, "deny_patterns": []string{}, "ignore_patterns": []string{}}
	cleared := desktopWrite(t, f.service, "rules.apply", p)
	if cleared.Connection.Code != "" {
		t.Fatal("clear rejected")
	}
	p["expected_revision"] = cleared.Desktop.Revision
	p["acknowledged_connections"] = []string{}
	fake := &fakeBridgeController{}
	f.service.mu.Lock()
	f.service.controller = fake
	f.service.mu.Unlock()
	noop := desktopWrite(t, f.service, "rules.apply", p)
	if noop.Desktop.Revision != cleared.Desktop.Revision || noop.Connection.Saved == nil || *noop.Connection.Saved {
		t.Fatal("noop wrote config")
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 0 {
		t.Fatal("noop stopped connection")
	}
	f.service.mu.Lock()
	f.service.nativeBusy = true
	f.service.mu.Unlock()
	response := callRPC(t, f.service, "busy", "rules.read", map[string]any{"connection_id": initial.Desktop.ActiveID})
	f.service.mu.Lock()
	f.service.nativeBusy = false
	f.service.mu.Unlock()
	if response.OK || response.Error.Code != "busy" {
		t.Fatal("rule read raced native dialog")
	}
}
