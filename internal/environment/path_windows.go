//go:build windows

package environment

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

func inspectCandidate(path string) (candidateKind, error) {
	if err := validateAbsoluteCandidatePath(path); err != nil {
		return candidateMissing, err
	}
	rootPtr, err := windows.UTF16PtrFromString(path[:3])
	if err != nil {
		return candidateMissing, ErrInvalidInput
	}
	switch driveType := windows.GetDriveType(rootPtr); driveType {
	case windows.DRIVE_REMOTE:
		return candidateMissing, ErrRejectedCandidate
	case windows.DRIVE_UNKNOWN, windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_CDROM:
		return candidateMissing, ErrUnavailable
	}
	missing, err := checkWindowsPathComponents(path)
	if err != nil {
		return candidateMissing, err
	}
	if missing {
		return candidateMissing, nil
	}

	h, err := openWindowsNoReparse(path)
	if err != nil {
		if isWindowsMissing(err) {
			return candidateMissing, nil
		}
		return candidateMissing, mapWindowsPathError(err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return candidateMissing, ErrUnavailable
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return candidateMissing, ErrRejectedCandidate
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return candidateDirectory, nil
	}
	fileType, err := windows.GetFileType(h)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return candidateMissing, ErrRejectedCandidate
	}
	return candidateRegular, nil
}

// canonicalCandidatePath accepts the JSON-friendly forward-slash spelling of
// a Windows absolute path, then applies the same strict validation to the
// native backslash form used by Win32. UNC, device, ADS, traversal, and
// non-canonical separator forms remain rejected by validation below.
func canonicalCandidatePath(path string) (string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if err := validateAbsoluteCandidatePath(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func validateAbsoluteCandidatePath(path string) error {
	if path == "" || !utf8.ValidString(path) || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidInput
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) ||
		strings.HasPrefix(strings.ToLower(path), `\\?\`) ||
		strings.HasPrefix(strings.ToLower(path), `\\.\`) ||
		strings.HasPrefix(strings.ToLower(path), `\??\`) {
		return ErrInvalidInput
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || len(path) < 3 || path[2] != '\\' {
		return ErrInvalidInput
	}
	if strings.Contains(path[2:], ":") {
		return ErrInvalidInput
	}
	for _, component := range strings.Split(path[3:], `\`) {
		if component == "" || component == "." || component == ".." || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || windowsReservedName(component) {
			return ErrInvalidInput
		}
	}
	return nil
}

func checkWindowsPathComponents(path string) (bool, error) {
	volume := filepath.VolumeName(path)
	current := volume + `\`
	components := strings.Split(path[len(volume)+1:], `\`)
	for index, component := range components {
		current += component
		if index < len(components)-1 {
			current += `\`
		}
		h, err := openWindowsNoReparse(current)
		if err != nil {
			if isWindowsMissing(err) {
				return true, nil
			}
			return false, mapWindowsPathError(err)
		}
		var info windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(h, &info)
		_ = windows.CloseHandle(h)
		if infoErr != nil {
			return false, ErrUnavailable
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return false, ErrRejectedCandidate
		}
		if index < len(components)-1 && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return false, ErrRejectedCandidate
		}
	}
	return false, nil
}

func openWindowsNoReparse(path string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		ptr,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func isWindowsMissing(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windows.ERROR_FILE_NOT_FOUND || errno == windows.ERROR_PATH_NOT_FOUND || errno == windows.ERROR_INVALID_NAME
}

func mapWindowsPathError(err error) error {
	if isWindowsMissing(err) {
		return nil
	}
	return ErrUnavailable
}

func candidateNameMatches(logicalID, name string) bool {
	return strings.EqualFold(name, logicalID) || strings.EqualFold(name, logicalID+".exe")
}

func candidateKey(path string) string { return strings.ToLower(path) }

func windowsReservedName(component string) bool {
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " .")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return true
	}
	upper := strings.ToUpper(base)
	for _, prefix := range []string{"COM", "LPT"} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(upper, prefix)
		if len(suffix) == 1 && suffix[0] >= '1' && suffix[0] <= '9' {
			return true
		}
		return suffix == "¹" || suffix == "²" || suffix == "³"
	}
	return false
}
