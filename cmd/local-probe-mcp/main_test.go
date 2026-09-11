package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/config"
)

func TestRouteMCPLeavesOAuthMetadataAbsent(t *testing.T) {
	t.Parallel()

	handler := routeMCP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/mcp", want: http.StatusNoContent},
		{path: "/.well-known/oauth-protected-resource/mcp", want: http.StatusNotFound},
		{path: "/.well-known/oauth-authorization-server", want: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != test.want {
			t.Fatalf("GET %s status = %d, want %d", test.path, response.Code, test.want)
		}
	}
}

func TestEnvironmentToolSpecsCopiesConfiguredCandidates(t *testing.T) {
	cfg, err := config.Parse([]byte(`{
  "schema_version": "local-probe.config.v1",
  "roots": [{"id":"root","path":"C:/root"}],
  "profiles": [{"id":"read","roots":["root"],"tools":["get_environment"]}],
  "connections": [{"id":"connection","label":"","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials": [{"id":"credential","kind":"local_token"}],
  "environment_tools": [{"id":"git","candidate_files":["C:/replace/git.exe"],"candidate_dirs":[]}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	specs := environmentToolSpecs(cfg)
	if len(specs) != 1 || specs[0].ID != "git" || len(specs[0].CandidateFiles) != 1 || specs[0].CandidateFiles[0] != "C:/replace/git.exe" {
		t.Fatalf("environmentToolSpecs = %#v", specs)
	}
	specs[0].CandidateFiles[0] = "changed"
	if got := cfg.EnvironmentTools()[0].CandidateFiles()[0]; got != "C:/replace/git.exe" {
		t.Fatalf("CLI conversion exposed mutable config storage: %q", got)
	}
}

func TestOpenAuditSinkFailureIsGenericAndFailClosed(t *testing.T) {
	previous := newAuditSink
	t.Cleanup(func() { newAuditSink = previous })
	newAuditSink = func(string) (*audit.Sink, error) {
		return nil, errors.New("secret-sentinel")
	}
	sink, err := openAuditSink(t.TempDir())
	if sink != nil || err == nil {
		t.Fatalf("openAuditSink() = %v, %v", sink, err)
	}
	if err.Error() != "cannot initialize audit sink" {
		t.Fatalf("openAuditSink error = %v", err)
	}
}
