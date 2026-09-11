//go:build windows

package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// windowsExecutionGuard keeps the audited executable and every parent
// directory open with write/delete sharing disabled for the launch window.
// The final handle also supplies the identity used to bind CreateProcess to
// the descriptor; re-opening the pathname is deliberately not the trust
// decision.
type windowsExecutionGuard struct {
	final    windows.Handle
	parents  []windows.Handle
	identity fileIdentity
}

func openWindowsExecutionGuard(path string, expected fileIdentity) (*windowsExecutionGuard, error) {
	if err := validateExecutablePath(path); err != nil {
		return nil, err
	}
	guard := &windowsExecutionGuard{final: windows.InvalidHandle, identity: fileIdentity{}}
	cleanup := func() {
		if guard.final != windows.InvalidHandle {
			_ = windows.CloseHandle(guard.final)
			guard.final = windows.InvalidHandle
		}
		for i := len(guard.parents) - 1; i >= 0; i-- {
			_ = windows.CloseHandle(guard.parents[i])
		}
		guard.parents = nil
	}
	root := path[:3]
	rootHandle, err := openWindowsGuardPath(root, true)
	if err != nil {
		return nil, mapWindowsIdentityError(err)
	}
	guard.parents = append(guard.parents, rootHandle)

	volume := filepath.VolumeName(path)
	current := volume + `\`
	components := strings.Split(path[len(volume)+1:], `\`)
	for index, component := range components {
		current += component
		last := index == len(components)-1
		handle, openErr := openWindowsGuardPath(current, !last)
		if openErr != nil {
			cleanup()
			return nil, mapWindowsIdentityError(openErr)
		}
		var info windows.ByHandleFileInformation
		if infoErr := windows.GetFileInformationByHandle(handle, &info); infoErr != nil {
			_ = windows.CloseHandle(handle)
			cleanup()
			return nil, ErrUnavailable
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			_ = windows.CloseHandle(handle)
			cleanup()
			return nil, ErrRejected
		}
		if !last {
			if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
				_ = windows.CloseHandle(handle)
				cleanup()
				return nil, ErrRejected
			}
			guard.parents = append(guard.parents, handle)
			current += `\`
			continue
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			_ = windows.CloseHandle(handle)
			cleanup()
			return nil, ErrRejected
		}
		fileType, typeErr := windows.GetFileType(handle)
		if typeErr != nil || fileType != windows.FILE_TYPE_DISK || !isPEExecutable(handle) {
			_ = windows.CloseHandle(handle)
			cleanup()
			return nil, ErrRejected
		}
		guard.final = handle
		guard.identity = identityFromWindowsHandle(info)
		if !sameIdentity(expected, guard.identity) {
			cleanup()
			return nil, ErrIdentityChanged
		}
	}
	return guard, nil
}

func (guard *windowsExecutionGuard) close() {
	if guard == nil {
		return
	}
	if guard.final != windows.InvalidHandle {
		_ = windows.CloseHandle(guard.final)
		guard.final = windows.InvalidHandle
	}
	for i := len(guard.parents) - 1; i >= 0; i-- {
		_ = windows.CloseHandle(guard.parents[i])
	}
	guard.parents = nil
}

func openWindowsGuardPath(path string, directory bool) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	desired := uint32(windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if !directory {
		desired |= windows.FILE_READ_DATA
	}
	return windows.CreateFile(
		ptr,
		desired,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

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
	// The fixed launcher accepts only native PE executables. Windows will
	// otherwise route command wrappers through shell associations or cmd.exe.
	// Wrapper extensions are rejected before any filesystem operation.
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return ErrRejected
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
	defer func() {
		if h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}()

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
	// The metadata handle deliberately does not require FILE_READ_DATA so the
	// component walk can inspect directories. Open a second no-reparse handle
	// for the PE header only after the final object is known to be a regular
	// disk file.
	_ = windows.CloseHandle(h)
	h = windows.InvalidHandle
	h, err = openWindowsReadNoReparse(path)
	if err != nil {
		return fileIdentity{}, mapWindowsIdentityError(err)
	}
	if !isPEExecutable(h) {
		return fileIdentity{}, ErrRejected
	}
	key := fmt.Sprintf("%08x:%08x:%08x:%08x:%08x",
		info.VolumeSerialNumber,
		info.FileIndexHigh, info.FileIndexLow,
		info.CreationTime.HighDateTime, info.CreationTime.LowDateTime)
	return fileIdentity{key: key, valid: true}, nil
}

func identityFromWindowsHandle(info windows.ByHandleFileInformation) fileIdentity {
	key := fmt.Sprintf("%08x:%08x:%08x:%08x:%08x",
		info.VolumeSerialNumber,
		info.FileIndexHigh, info.FileIndexLow,
		info.CreationTime.HighDateTime, info.CreationTime.LowDateTime)
	return fileIdentity{key: key, valid: key != ""}
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

func openWindowsReadNoReparse(path string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		ptr,
		windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func isPEExecutable(handle windows.Handle) bool {
	var dos [64]byte
	if !readWindowsAt(handle, 0, dos[:]) || dos[0] != 'M' || dos[1] != 'Z' {
		return false
	}
	peOffset := binary.LittleEndian.Uint32(dos[0x3c:])
	// A valid PE header is near the beginning of the image. Besides avoiding
	// an unbounded read, this rejects tiny/polyglot files before launch.
	if peOffset < 64 || peOffset > 1<<20 {
		return false
	}
	var signature [4]byte
	if !readWindowsAt(handle, int64(peOffset), signature[:]) || string(signature[:]) != "PE\x00\x00" {
		return false
	}
	var optionalMagic [2]byte
	if !readWindowsAt(handle, int64(peOffset)+24, optionalMagic[:]) {
		return false
	}
	magic := binary.LittleEndian.Uint16(optionalMagic[:])
	return magic == 0x10b || magic == 0x20b
}

func readWindowsAt(handle windows.Handle, offset int64, buffer []byte) bool {
	if len(buffer) == 0 {
		return true
	}
	if _, err := windows.Seek(handle, offset, io.SeekStart); err != nil {
		return false
	}
	var read uint32
	if err := windows.ReadFile(handle, buffer, &read, nil); err != nil {
		return false
	}
	return int(read) == len(buffer)
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
