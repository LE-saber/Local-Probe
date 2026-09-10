//go:build windows

package probe

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func validateExecutablePath(path string) error {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return ErrInvalidInput
	}
	// Win32 device, extended-length, and UNC spellings are intentionally not
	// accepted.  The descriptor is for a local drive path only.
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) ||
		strings.HasPrefix(strings.ToLower(path), `\\?\`) ||
		strings.HasPrefix(strings.ToLower(path), `\\.\`) ||
		strings.HasPrefix(strings.ToLower(path), `\??\`) {
		return ErrInvalidInput
	}
	if !filepath.IsAbs(path) {
		return ErrInvalidInput
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || len(path) < 3 || path[2] != '\\' {
		return ErrInvalidInput
	}
	// A colon is legal only as the drive separator.  This excludes alternate
	// data streams and other drive-relative aliases.
	if strings.Contains(path[2:], ":") || filepath.Clean(path) != path {
		return ErrInvalidInput
	}
	components := strings.Split(path[3:], `\`)
	for _, component := range components {
		if component == "" || component == "." || component == ".." || windowsReservedName(component) {
			return ErrInvalidInput
		}
	}
	return nil
}

func captureIdentity(path string) (fileIdentity, error) {
	if err := validateExecutablePath(path); err != nil {
		return fileIdentity{}, err
	}
	root := path[:3]
	rootPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return fileIdentity{}, ErrInvalidInput
	}
	driveType := windows.GetDriveType(rootPtr)
	switch driveType {
	case windows.DRIVE_REMOTE:
		return fileIdentity{}, ErrRejected
	case windows.DRIVE_UNKNOWN, windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_CDROM:
		return fileIdentity{}, ErrUnavailable
	}
	if err := checkWindowsPathComponents(path); err != nil {
		return fileIdentity{}, err
	}

	h, err := openWindowsNoReparse(path)
	if err != nil {
		return fileIdentity{}, mapWindowsIdentityError(err)
	}
	defer windows.CloseHandle(h)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fileIdentity{}, ErrUnavailable
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fileIdentity{}, ErrRejected
	}
	fileType, err := windows.GetFileType(h)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return fileIdentity{}, ErrRejected
	}
	key := fmt.Sprintf("%08x:%08x:%08x:%08x:%08x",
		info.VolumeSerialNumber,
		info.FileIndexHigh, info.FileIndexLow,
		info.CreationTime.HighDateTime, info.CreationTime.LowDateTime)
	return fileIdentity{key: key, valid: true}, nil
}

func checkWindowsPathComponents(path string) error {
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
			return mapWindowsIdentityError(err)
		}
		var info windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(h, &info)
		_ = windows.CloseHandle(h)
		if infoErr != nil {
			return ErrUnavailable
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrRejected
		}
		if index < len(components)-1 && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return ErrRejected
		}
	}
	return nil
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

func mapWindowsIdentityError(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	var errno windows.Errno
	if errors.As(err, &errno) {
		switch errno {
		case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND, windows.ERROR_INVALID_NAME:
			return ErrNotFound
		case windows.ERROR_CANT_ACCESS_FILE, windows.ERROR_ACCESS_DENIED:
			return ErrUnavailable
		}
	}
	return ErrUnavailable
}

func windowsReservedName(component string) bool {
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " .")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(strings.ToUpper(base), "COM") || strings.HasPrefix(strings.ToUpper(base), "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}
