package environment

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DiscoverTools checks only the exact trusted candidate files and the direct
// entries of trusted candidate directories. It never calls an external
// command, searches PATH, recursively walks a directory, or returns paths
// unless local diagnostics were explicitly requested.
func DiscoverTools(specs []ToolSpec, options DiscoveryOptions) ([]ToolDiscoveryResult, error) {
	if len(specs) > maxToolSpecs {
		return nil, ErrCandidateLimit
	}
	seenIDs := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if !validLogicalID(spec.ID) {
			return nil, ErrInvalidInput
		}
		if _, exists := seenIDs[spec.ID]; exists {
			return nil, ErrDuplicateID
		}
		seenIDs[spec.ID] = struct{}{}
		if len(spec.CandidateFiles)+len(spec.CandidateDirs) > maxCandidateInputs {
			return nil, ErrCandidateLimit
		}
	}

	results := make([]ToolDiscoveryResult, 0, len(specs))
	for _, spec := range specs {
		set := newCandidateSet()
		for _, configuredPath := range spec.CandidateFiles {
			path, err := canonicalCandidatePath(configuredPath)
			if err != nil {
				return nil, err
			}
			kind, err := inspectCandidate(path)
			if err != nil {
				return nil, err
			}
			switch kind {
			case candidateMissing:
				continue
			case candidateRegular:
				set.add(path)
			default:
				return nil, ErrRejectedCandidate
			}
		}
		for _, configuredPath := range spec.CandidateDirs {
			path, err := canonicalCandidatePath(configuredPath)
			if err != nil {
				return nil, err
			}
			kind, err := inspectCandidate(path)
			if err != nil {
				return nil, err
			}
			switch kind {
			case candidateMissing:
				continue
			case candidateDirectory:
				if err := scanCandidateDirectory(path, spec.ID, set); err != nil {
					return nil, err
				}
			default:
				return nil, ErrRejectedCandidate
			}
		}

		result := ToolDiscoveryResult{
			ApprovedLogicalID: spec.ID,
			Exists:            set.len() > 0,
			CandidateCount:    set.len(),
		}
		if options.LocalDiagnostics {
			result.CandidatePaths = set.paths()
			result.DiagnosticsNotice = DiagnosticsNotice
		}
		results = append(results, result)
	}
	return results, nil
}

type candidateSet struct {
	byKey map[string]string
}

func newCandidateSet() *candidateSet {
	return &candidateSet{byKey: make(map[string]string)}
}

func (s *candidateSet) add(path string) {
	key := candidateKey(path)
	if _, exists := s.byKey[key]; !exists {
		s.byKey[key] = path
	}
}

func (s *candidateSet) len() int { return len(s.byKey) }

func (s *candidateSet) paths() []string {
	paths := make([]string, 0, len(s.byKey))
	for _, path := range s.byKey {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		left, right := candidateKey(paths[i]), candidateKey(paths[j])
		if left == right {
			return paths[i] < paths[j]
		}
		return left < right
	})
	return paths
}

func scanCandidateDirectory(path, logicalID string, set *candidateSet) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrUnavailable
	}
	defer directory.Close()
	// Recheck after obtaining the descriptor so a directory replaced between
	// the caller's initial validation and os.Open is not treated as trusted.
	kind, err := inspectCandidate(path)
	if err != nil {
		return err
	}
	if kind != candidateDirectory {
		return ErrRejectedCandidate
	}
	entries, err := directory.ReadDir(maxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrUnavailable
	}
	if len(entries) > maxDirectoryEntries {
		return ErrCandidateLimit
	}
	// File.ReadDir is not required to return sorted entries. Sorting only the
	// small, exact-name candidate set keeps diagnostics deterministic.
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if candidateNameMatches(logicalID, entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		candidatePath := filepath.Join(path, name)
		kind, err := inspectCandidate(candidatePath)
		if err != nil {
			// A directory entry can disappear or become a link while it is
			// being checked. It is safer to omit it than to report it as an
			// approved candidate; the trusted directory itself remains valid.
			if errors.Is(err, ErrRejectedCandidate) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrUnavailable) {
				continue
			}
			return err
		}
		if kind == candidateRegular {
			set.add(candidatePath)
		}
	}
	return nil
}

func validLogicalID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
