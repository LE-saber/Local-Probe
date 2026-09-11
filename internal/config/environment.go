package config

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
)

const (
	// MaxEnvironmentTools bounds the number of locally configured logical tools.
	// The limit protects config parsing and keeps discovery requests explicit.
	MaxEnvironmentTools = 128

	// MaxEnvironmentCandidatesPerTool bounds exact files plus non-recursive
	// candidate directories in one logical tool entry. Platform-specific path
	// and reparse/remote checks belong to internal/environment.
	MaxEnvironmentCandidatesPerTool = 256

	maxEnvironmentCandidatePathBytes = 4096
)

// EnvironmentTool is a locally configured allowlist entry for non-executing
// environment discovery. The logical ID is selected by trusted local config;
// candidate paths are never supplied by a model request.
//
// Candidate paths are intentionally only checked for basic string safety here.
// Whether a path is absolute, local, non-reparse and non-remote is a
// platform-specific decision made by internal/environment at discovery time.
type EnvironmentTool struct {
	id             string
	candidateFiles []string
	candidateDirs  []string
}

// NewEnvironmentTool constructs one immutable configuration entry. At least
// one exact file or candidate directory is required.
func NewEnvironmentTool(id string, candidateFiles, candidateDirs []string) (EnvironmentTool, error) {
	tool := EnvironmentTool{
		id:             id,
		candidateFiles: append([]string(nil), candidateFiles...),
		candidateDirs:  append([]string(nil), candidateDirs...),
	}
	if err := validateEnvironmentTool(tool, "environment_tool"); err != nil {
		return EnvironmentTool{}, err
	}
	return tool, nil
}

func (t EnvironmentTool) ID() string { return t.id }

func (t EnvironmentTool) CandidateFiles() []string {
	return append([]string(nil), t.candidateFiles...)
}

func (t EnvironmentTool) CandidateDirs() []string {
	return append([]string(nil), t.candidateDirs...)
}

// NewWithEnvironmentTools preserves New's original signature while allowing
// trusted local callers to opt into configured environment discovery entries.
func NewWithEnvironmentTools(schemaVersion string, roots []Root, profiles []Profile, connections []Connection, credentials []CredentialRef, environmentTools []EnvironmentTool) (Config, error) {
	return NewWithCommandProfiles(schemaVersion, roots, profiles, connections, credentials, environmentTools, commandprofile.DefaultDeveloperMode(), nil)
}

func (c Config) EnvironmentTools() []EnvironmentTool {
	return cloneEnvironmentTools(c.environmentTools)
}

func (c Config) EnvironmentTool(id string) (EnvironmentTool, bool) {
	for _, tool := range c.environmentTools {
		if tool.id == id {
			return tool.clone(), true
		}
	}
	return EnvironmentTool{}, false
}

func (t EnvironmentTool) clone() EnvironmentTool {
	t.candidateFiles = append([]string(nil), t.candidateFiles...)
	t.candidateDirs = append([]string(nil), t.candidateDirs...)
	return t
}

func cloneEnvironmentTools(values []EnvironmentTool) []EnvironmentTool {
	if values == nil {
		return nil
	}
	out := make([]EnvironmentTool, len(values))
	for i, value := range values {
		out[i] = value.clone()
	}
	return out
}

type rawEnvironmentTool struct {
	ID             string   `json:"id"`
	CandidateFiles []string `json:"candidate_files"`
	CandidateDirs  []string `json:"candidate_dirs"`
}

func validateEnvironmentTools(values []EnvironmentTool) error {
	if len(values) > MaxEnvironmentTools {
		return invalid("environment_tools", "too many tools")
	}
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		field := fmt.Sprintf("environment_tools[%d]", i)
		if err := validateEnvironmentTool(value, field); err != nil {
			return err
		}
		if !addUnique(seen, value.id) {
			return invalid(field+".id", "duplicate id")
		}
	}
	return nil
}

func validateEnvironmentTool(tool EnvironmentTool, field string) error {
	if !validIdentifier(tool.id) {
		return invalid(field+".id", "invalid logical id")
	}
	if len(tool.candidateFiles)+len(tool.candidateDirs) > MaxEnvironmentCandidatesPerTool {
		return invalid(field, "too many candidates")
	}
	if len(tool.candidateFiles)+len(tool.candidateDirs) == 0 {
		return invalid(field, "at least one candidate is required")
	}
	if err := validateEnvironmentCandidatePaths(tool.candidateFiles, field+".candidate_files"); err != nil {
		return err
	}
	return validateEnvironmentCandidatePaths(tool.candidateDirs, field+".candidate_dirs")
}

func validateEnvironmentCandidatePaths(values []string, field string) error {
	for i, value := range values {
		if value == "" || len(value) > maxEnvironmentCandidatePathBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "invalid candidate path")
		}
	}
	return nil
}
