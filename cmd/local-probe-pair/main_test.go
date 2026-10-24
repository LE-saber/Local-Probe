package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
)

func TestOfflinePairingWorkflowAndMandatoryAcknowledgements(t *testing.T) {
	base := t.TempDir()
	target, agent := filepath.Join(base, "target"), filepath.Join(base, "agent")
	call := func(input []byte, args ...string) ([]byte, error) {
		t.Helper()
		var out, stderr bytes.Buffer
		err := run(args, bytes.NewReader(input), &out, &stderr)
		return out.Bytes(), err
	}
	if _, err := call(nil, "-store", target, "-action", "target-init"); err == nil {
		t.Fatal("initialized without approval")
	}
	bundle, err := call(nil, "-store", target, "-action", "target-init", "-approve", "-offline")
	if err != nil {
		t.Fatal(err)
	}
	request, err := call(nil, "-store", agent, "-action", "agent-init", "-approve", "-offline")
	if err != nil {
		t.Fatal(err)
	}
	bfp, err := call(bundle, "-action", "fingerprint-bundle")
	if err != nil {
		t.Fatal(err)
	}
	rfp, err := call(request, "-action", "fingerprint-request")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := config.NewConnection("remote-a", "remote", "profile-a", "credential-a", true).WithPurpose(config.PurposeAgentRemote)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile-a", nil, []string{"server_info"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.SchemaVersionV2, nil, []config.Profile{profile}, []config.Connection{conn}, []config.CredentialRef{config.NewCredentialRef("credential-a", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "config.json")
	cs, err := config.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cs.Save(cfg); err != nil {
		t.Fatal(err)
	}
	args := []string{"-store", target, "-action", "issue", "-config", path, "-connection-id", "remote-a", "-fingerprint", strings.TrimSpace(string(rfp)), "-approve", "-offline"}
	pairing, err := call(request, args...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = call(pairing, "-store", agent, "-action", "install", "-approve", "-offline", "-fingerprint", strings.TrimSpace(string(bfp))); err != nil {
		t.Fatal(err)
	}
	shown, err := call(nil, "-store", agent, "-action", "show")
	if err != nil {
		t.Fatal(err)
	}
	var p deviceidentity.Pairing
	if err = json.Unmarshal(shown, &p); err != nil {
		t.Fatal(err)
	}
	if p.Grant.ConnectionID != "remote-a" {
		t.Fatal("wrong scope")
	}
	if bytes.Contains(shown, []byte("PRIVATE KEY")) || bytes.Contains(pairing, []byte("ca_key")) {
		t.Fatal("private output")
	}
	if _, err = call(nil, "-store", target, "-action", "revoke", "-key-pin", p.Grant.KeyPin, "-approve", "-offline"); err != nil {
		t.Fatal(err)
	}
}
