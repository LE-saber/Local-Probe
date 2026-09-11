//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package environment

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func inspectCandidate(path string) (candidateKind, error) {
	if err := validateAbsoluteCandidatePath(path); err != nil {
		return candidateMissing, err
	}
	if err := validateLocalFilesystem(path); err != nil {
		return candidateMissing, err
	}

	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	if len(components) == 1 && components[0] == "" {
		info, err := os.Lstat(current)
		if err != nil {
			return candidateMissing, mapUnixPathError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return candidateMissing, ErrRejectedCandidate
		}
		return candidateKindForInfo(info), nil
	}

	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return candidateMissing, nil
			}
			return candidateMissing, ErrUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return candidateMissing, ErrRejectedCandidate
		}
		if index < len(components)-1 && !info.IsDir() {
			return candidateMissing, ErrRejectedCandidate
		}
		if index == len(components)-1 {
			return candidateKindForInfo(info), nil
		}
	}
	return candidateMissing, ErrInvalidInput
}

func canonicalCandidatePath(path string) (string, error) {
	if err := validateAbsoluteCandidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func validateAbsoluteCandidatePath(path string) error {
	if path == "" || !utf8.ValidString(path) || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidInput
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidInput
		}
	}
	return nil
}

func mapUnixPathError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return ErrUnavailable
}

func candidateNameMatches(logicalID, name string) bool {
	return name == logicalID
}

func candidateKey(path string) string { return path }
