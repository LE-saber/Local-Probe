package supportbundle

import (
	"os"
	"path/filepath"
	"strings"
)

func validateDirectory(directory string) (string, error) {
	if directory == "" || len(directory) > maxPathBytes || strings.ContainsRune(directory, 0) || !filepath.IsAbs(directory) {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean(directory)
	if strings.HasPrefix(clean, `\\`) || strings.HasPrefix(clean, `//`) {
		return "", ErrInvalidPath
	}
	if err := validateDirectoryStillSafe(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func validateDirectoryStillSafe(directory string) error {
	clean := filepath.Clean(directory)
	info, err := os.Lstat(clean)
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || fileInfoReparse(info) {
		return ErrInvalidPath
	}
	for current := filepath.Dir(clean); current != clean; {
		parentInfo, statErr := os.Lstat(current)
		if statErr != nil || parentInfo == nil || parentInfo.Mode()&os.ModeSymlink != 0 || fileInfoReparse(parentInfo) {
			return ErrInvalidPath
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}
