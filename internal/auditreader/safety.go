package auditreader

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func validateDirectory(directory string) (string, error) {
	if directory == "" || len(directory) > maxPathBytes || strings.ContainsRune(directory, 0) || !filepath.IsAbs(directory) {
		return "", ErrInvalidDirectory
	}
	clean := filepath.Clean(directory)
	// UNC/device paths are deliberately outside this local reader's scope.
	if strings.HasPrefix(clean, `\\`) || strings.HasPrefix(clean, `//`) {
		return "", ErrInvalidDirectory
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
		return ErrInvalidDirectory
	}
	for current := filepath.Dir(clean); current != clean; {
		parentInfo, statErr := os.Lstat(current)
		if statErr != nil || parentInfo == nil || parentInfo.Mode()&os.ModeSymlink != 0 || fileInfoReparse(parentInfo) {
			return ErrInvalidDirectory
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func regularNonReparse(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && !fileInfoReparse(info)
}

func openRegularAuditFile(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil || !regularNonReparse(before) {
		return nil, nil, ErrUnsafeFile
	}
	file, err := openAuditFile(path)
	if err != nil {
		return nil, nil, ErrUnsafeFile
	}
	opened, statErr := file.Stat()
	if statErr != nil || !regularNonReparse(opened) || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, ErrUnsafeFile
	}
	after, statErr := os.Lstat(path)
	if statErr != nil || !regularNonReparse(after) || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, ErrUnsafeFile
	}
	return file, before, nil
}

func isEOF(err error) bool { return errors.Is(err, os.ErrClosed) }
