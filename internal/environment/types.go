// Package environment provides a small, non-executing environment query and
// trusted-candidate discovery surface.  It deliberately does not inspect the
// caller's environment, search PATH, or start a process.
package environment

import "runtime"

const (
	SchemaVersion = "local-probe.environment.v1"

	// DiagnosticsNotice is returned only when a caller explicitly opts in to
	// local diagnostics. Candidate paths can contain local identity details and
	// must not be forwarded to a remote client by default.
	DiagnosticsNotice = "candidate_paths are local diagnostics only; do not send them to remote clients"

	maxToolSpecs        = 128
	maxCandidateInputs  = 256
	maxDirectoryEntries = 4096
)

// Environment is the coarse environment description safe to expose to a
// remote caller. It contains no user, home, environment-variable, or path
// details.
type Environment struct {
	SchemaVersion string   `json:"schema_version"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Capabilities  []string `json:"capabilities"`
}

// GetEnvironment returns only coarse process-independent environment facts.
// It never reads environment variables or the filesystem and never starts a
// child process.
func GetEnvironment() Environment {
	return Environment{
		SchemaVersion: SchemaVersion,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Capabilities: []string{
			"coarse_environment",
			"tool_discovery",
			"regular_file_candidates",
		},
	}
}

// ToolSpec is a locally supplied allowlist entry. CandidateFiles are exact
// paths. CandidateDirs are scanned non-recursively for a platform-approved
// exact name derived from ID (ID on Unix, and ID or ID.exe on Windows).
// Neither field is interpreted as PATH, and neither is accepted from a model
// request without a trusted local configuration layer.
type ToolSpec struct {
	ID             string   `json:"id"`
	CandidateDirs  []string `json:"candidate_dirs,omitempty"`
	CandidateFiles []string `json:"candidate_files,omitempty"`
}

// DiscoveryOptions controls intentionally sensitive local diagnostics.
// LocalDiagnostics must be explicitly enabled before result paths are
// populated.
type DiscoveryOptions struct {
	LocalDiagnostics bool
}

// Options is retained as a concise alias for callers that prefer the generic
// name while keeping DiscoveryOptions as the self-documenting API.
type Options = DiscoveryOptions

// ToolDiscoveryResult is the path-free default discovery result. Candidate
// paths are included only when DiscoveryOptions.LocalDiagnostics is true.
type ToolDiscoveryResult struct {
	ApprovedLogicalID string   `json:"approved_logical_id"`
	Exists            bool     `json:"exists"`
	CandidateCount    int      `json:"candidate_count"`
	CandidatePaths    []string `json:"candidate_paths,omitempty"`
	DiagnosticsNotice string   `json:"diagnostics_notice,omitempty"`
}
